package fstool

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/RussellLuo/qi/kernel/core/tools"
)

type WriteResult struct {
	Path      string  `json:"path"`
	Operation string  `json:"operation"`
	Before    *string `json:"before"`
	After     string  `json:"after"`
}

// Write atomically creates or replaces a regular file. Content is stored
// verbatim; Before and After normalize CRLF pairs to LF for diff consumers.
// Before is optional presentation data, not proof of the file's existence.
func (f *FS) Write(ctx context.Context, path, content string) (WriteResult, error) {
	return f.write(ctx, "", path, content)
}

func (f *FS) write(ctx context.Context, cwd, path, content string) (WriteResult, error) {
	if !utf8.ValidString(content) {
		return WriteResult{}, errors.New("content must be valid UTF-8")
	}
	done, err := f.begin(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	defer done()
	target, err := f.resolve(ctx, cwd, path)
	if err != nil {
		return WriteResult{}, err
	}
	unlock, err := f.lockWrite(ctx, target.key)
	if err != nil {
		return WriteResult{}, err
	}
	defer unlock()
	// Pin the parent directory so creation, rename, and cleanup all use the
	// same directory even if an external process retargets a parent symlink.
	parent, err := os.OpenRoot(filepath.Dir(target.key))
	if err != nil {
		return WriteResult{}, err
	}
	defer parent.Close()
	base := filepath.Base(target.key)
	info, err := writeTarget(parent, base)
	if err != nil {
		return WriteResult{}, err
	}
	result := WriteResult{Path: target.display, Operation: "create", After: normalizeLF(content)}
	mode := os.FileMode(0600)
	if info != nil {
		result.Operation = "update"
		mode = info.Mode().Perm()
		if len(content) < f.options.DiffBasisMaxBytes {
			result.Before, err = readBasis(ctx, parent, base, f.options.DiffBasisMaxBytes)
			if err != nil {
				return WriteResult{}, err
			}
		}
	}
	err = atomicWrite(ctx, parent, base, mode, func(file *os.File) error {
		_, err := io.Copy(file, contextReader{ctx, strings.NewReader(content)})
		return err
	})
	if err != nil {
		return WriteResult{}, err
	}
	return result, nil
}

func writeTarget(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The physical target was resolved before locking. Reject a link introduced
	// there by an external writer rather than following it during atomic commit.
	if err := regular(info); err != nil {
		return nil, err
	}
	return info, nil
}

func normalizeLF(text string) string { return strings.ReplaceAll(text, "\r\n", "\n") }

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func readBasis(ctx context.Context, root *os.Root, name string, maxBytes int) (*string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := openRegular(root, name)
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() >= int64(maxBytes) {
		return nil, nil
	}
	// Read at most the threshold even if the file grows after Stat.
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, int64(maxBytes)))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(data) >= maxBytes || int64(len(data)) != info.Size() || !utf8.Valid(data) {
		return nil, nil
	}
	text := normalizeLF(string(data))
	return &text, nil
}

// populate is isolated to test real partial-write and pre-commit failures.
// All fallible work happens before Rename; the original file is never opened
// for truncation. The exclusive random temporary file is owned by this call.
func atomicWrite(ctx context.Context, parent *os.Root, name string, mode os.FileMode, populate func(*os.File) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	var file *os.File
	var temp string
	for attempts := 0; attempts < 10; attempts++ {
		temp = fmt.Sprintf(".qi-fs-%s.tmp", rand.Text())
		file, err = parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer func() {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		if temp != "" {
			err = errors.Join(err, parent.Remove(temp))
		}
	}()
	if err := populate(file); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	err = file.Close()
	file = nil
	if err != nil {
		return err
	}
	if _, err := writeTarget(parent, name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := parent.Rename(temp, name); err != nil {
		return err
	}
	temp = ""
	return nil
}

func NewWriteDefinition(f *FS) tools.Definition {
	return tools.Definition{
		Name: "write_file", Description: "Create or fully replace a UTF-8 text file. Relative paths use the session working directory. Provide file_path before content.",
		Parameters:  tools.JSONSchema(`{"type":"object","additionalProperties":false,"required":["file_path","content"],"properties":{"file_path":{"type":"string"},"content":{"type":"string"}}}`),
		Output:      tools.JSONSchema(`{"type":"object","additionalProperties":false,"required":["path","operation","before","after"],"properties":{"path":{"type":"string"},"operation":{"type":"string","enum":["create","update"]},"before":{"oneOf":[{"type":"string"},{"type":"null"}]},"after":{"type":"string"}}}`),
		FailureMode: tools.ToolFailureResult,
		Execute: func(ctx context.Context, execution tools.Execution) (tools.ExecutionResult, error) {
			args, err := parseObject(execution.Arguments, "file_path", "content")
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			path, err := stringArg(args, "file_path")
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			content, err := stringArg(args, "content")
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			result, err := f.write(ctx, execution.CWD, path, content)
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			return executionResult(result, renderWrite(result))
		},
	}
}
