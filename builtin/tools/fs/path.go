package fstool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

type localTarget struct {
	display string
	key     string
}

// resolve separates DSH fs-local's absolute display spelling from physical
// identity. CWD resolves relative paths; it does not confine file access.
func (f *FS) resolve(ctx context.Context, cwd, path string) (localTarget, error) {
	if isBlank(path) || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		return localTarget{}, errors.New("file_path must be a non-empty UTF-8 path")
	}
	if cwd == "" {
		cwd = f.options.CWD
	}
	if !filepath.IsAbs(cwd) {
		return localTarget{}, errors.New("tool execution cwd must be absolute")
	}
	raw := path
	if !filepath.IsAbs(raw) {
		raw = cwd + string(filepath.Separator) + raw
	}
	display := filepath.Clean(raw)
	// POSIX resolves symlink/.. physically before lexical normalization.
	if runtime.GOOS != "windows" {
		for _, part := range strings.Split(raw, string(filepath.Separator)) {
			if part == ".." {
				display = raw
				break
			}
		}
	} else {
		raw = display
	}
	key, err := physicalPath(ctx, raw)
	return localTarget{display: display, key: key}, err
}

// Walk components before cleaning .., following links even for a missing leaf.
// Reads and atomic writes both require existing parents in this backend.
func physicalPath(ctx context.Context, path string) (string, error) {
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, volume), string(filepath.Separator))
	if strings.HasSuffix(path, string(filepath.Separator)) {
		parts[len(parts)-1] = "."
	}
	links := 0
	for len(parts) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		part := parts[0]
		parts = parts[1:]
		if part == "" {
			continue
		}
		info, err := os.Stat(current)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("parent path %q is not a directory", current)
		}
		if part == "." {
			continue
		}
		if part == ".." {
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, part)
		info, err = os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) && len(parts) == 0 {
			return next, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		links++
		if links > 40 {
			return "", errors.New("too many symbolic links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			volume = filepath.VolumeName(target)
			current = volume + string(filepath.Separator)
			target = strings.TrimPrefix(target, volume)
		}
		targetParts := strings.Split(target, string(filepath.Separator))
		if strings.HasSuffix(target, string(filepath.Separator)) {
			targetParts[len(targetParts)-1] = "."
		}
		parts = append(targetParts, parts...)
	}
	return current, nil
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func regular(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", info.Name())
	}
	return nil
}

func openRegular(root *os.Root, path string) (*os.File, error) {
	info, err := root.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := regular(info); err != nil {
		return nil, err
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err == nil {
		err = regular(info)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
