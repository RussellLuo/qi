package fstool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/tools"
)

func executionArgs(args string) tools.Execution {
	return tools.Execution{Arguments: json.RawMessage(args)}
}

func TestOptionsValidation(t *testing.T) {
	dir := t.TempDir()
	defaults := DefaultOptions(dir)
	if defaults.ReadLimit != 2000 || defaults.ReadMaxLineLength != 2000 || defaults.ReadMaxBytes != 51200 || defaults.DiffBasisMaxBytes != 10*1024*1024 {
		t.Fatalf("defaults: %+v", defaults)
	}
	for _, invalid := range []int{0, -1} {
		for _, field := range []string{"read_limit", "read_max_line_length", "read_max_bytes", "diff_basis_max_bytes"} {
			o := defaults
			switch field {
			case "read_limit":
				o.ReadLimit = invalid
			case "read_max_line_length":
				o.ReadMaxLineLength = invalid
			case "read_max_bytes":
				o.ReadMaxBytes = invalid
			case "diff_basis_max_bytes":
				o.DiffBasisMaxBytes = invalid
			}
			if f, err := New(o); err == nil {
				f.Close()
				t.Fatalf("%s=%d accepted", field, invalid)
			}
		}
	}
	put(t, filepath.Join(dir, "file"), "x")
	for _, cwd := range []string{"   ", filepath.Join(dir, "missing"), filepath.Join(dir, "file")} {
		if f, err := New(DefaultOptions(cwd)); err == nil {
			f.Close()
			t.Fatalf("cwd %q accepted", cwd)
		}
	}
	f, err := New(DefaultOptions(""))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cwd, err := os.Getwd()
	if err != nil || f.options.CWD != cwd {
		t.Fatalf("default cwd = %q / %v", f.options.CWD, err)
	}
}

func TestPathRules(t *testing.T) {
	f, dir := newFS(t, nil)
	outside := t.TempDir()
	put(t, filepath.Join(outside, "outside.txt"), "outside")
	put(t, filepath.Join(dir, "a.txt"), "inside")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "  ", ".", "sub", "a\x00.txt", "missing-parent/file.txt", "a.txt/child", "missing/../a.txt", "sub/new.txt/"} {
		if _, _, err := f.Read(context.Background(), path, 1, 1); err == nil {
			t.Fatalf("read accepted %q", path)
		}
		if _, err := f.Write(context.Background(), path, "x"); err == nil {
			t.Fatalf("write accepted %q", path)
		}
	}
	for _, path := range []string{"./a.txt", filepath.Join(dir, "a.txt")} {
		r, _, err := f.Read(context.Background(), path, 1, 1)
		if err != nil || r.Path != filepath.Join(dir, "a.txt") || r.Lines[0].Text != "inside" {
			t.Fatalf("absolute display: %+v / %v", r, err)
		}
	}
	rel, err := filepath.Rel(dir, filepath.Join(outside, "outside.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{rel, filepath.Join(outside, "outside.txt")} {
		r, _, err := f.Read(context.Background(), path, 1, 1)
		if err != nil || r.Lines[0].Text != "outside" {
			t.Fatalf("host access: %+v / %v", r, err)
		}
		if _, err := f.Write(context.Background(), path, "outside"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.Write(context.Background(), "sub/new.txt", "new")
	if err != nil || result.Path != filepath.Join(dir, "sub", "new.txt") || result.Operation != "create" {
		t.Fatalf("create: %+v / %v", result, err)
	}
	assertNoTemps(t, dir)
	assertNoTemps(t, outside)
}

func TestSymlinksAndPhysicalParentTraversal(t *testing.T) {
	f, dir := newFS(t, nil)
	outside := t.TempDir()
	child := filepath.Join(outside, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "a.txt"), "inside")
	put(t, filepath.Join(outside, "a.txt"), "outside")
	for link, target := range map[string]string{
		"in": "a.txt", "absolute-in": filepath.Join(dir, "a.txt"), "chain": "in",
		"out": filepath.Join(outside, "a.txt"), "out-dir": outside,
		"broken": "missing.txt", "loop": "loop", "jump": child, "file-slash": "a.txt/",
	} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct{ path, target, want string }{
		{"in", filepath.Join(dir, "a.txt"), "inside"},
		{"absolute-in", filepath.Join(dir, "a.txt"), "inside"},
		{"chain", filepath.Join(dir, "a.txt"), "inside"},
		{"out", filepath.Join(outside, "a.txt"), "outside"},
		{"out-dir/a.txt", filepath.Join(outside, "a.txt"), "outside"},
	} {
		put(t, tt.target, tt.want)
		r, _, err := f.Read(context.Background(), tt.path, 1, 1)
		if err != nil || r.Path != filepath.Join(dir, tt.path) || r.Lines[0].Text != tt.want {
			t.Fatalf("link read: %+v / %v", r, err)
		}
		if _, err := f.Write(context.Background(), tt.path, "changed"); err != nil {
			t.Fatal(err)
		}
		if get(t, tt.target) != "changed" {
			t.Fatal("write did not follow symlink target")
		}
		first := strings.Split(tt.path, "/")[0]
		if info, err := os.Lstat(filepath.Join(dir, first)); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("write replaced symlink", err)
		}
	}
	put(t, filepath.Join(outside, "physical.txt"), "physical")
	put(t, filepath.Join(dir, "physical.txt"), "lexical")
	raw := dir + string(filepath.Separator) + "jump/../physical.txt"
	r, _, err := f.Read(context.Background(), "jump/../physical.txt", 1, 1)
	if err != nil || r.Path != raw || r.Lines[0].Text != "physical" {
		t.Fatalf("symlink/.. traversal: %+v / %v", r, err)
	}
	if _, err := f.Write(context.Background(), "jump/../new.txt", "new"); err != nil {
		t.Fatal(err)
	}
	if get(t, filepath.Join(outside, "new.txt")) != "new" {
		t.Fatal("missing target used lexical parent")
	}
	for _, path := range []string{"loop", "out-dir", "file-slash"} {
		if _, _, err := f.Read(context.Background(), path, 1, 1); err == nil {
			t.Fatalf("read accepted %s", path)
		}
		if _, err := f.Write(context.Background(), path, "x"); err == nil {
			t.Fatalf("write accepted %s", path)
		}
	}
	if _, err := f.Write(context.Background(), "broken", "created"); err != nil {
		t.Fatal(err)
	}
	if get(t, filepath.Join(dir, "missing.txt")) != "created" {
		t.Fatal("dangling link target not created")
	}
	assertNoTemps(t, dir)
	assertNoTemps(t, outside)
}

func TestFallbackCWDFixedAtCreation(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(first, "a.txt"), "first")
	put(t, filepath.Join(second, "a.txt"), "second")
	t.Chdir(dir)
	f, err := New(DefaultOptions("first"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t.Chdir(second)
	result, _, err := f.Read(context.Background(), "a.txt", 1, 1)
	if err != nil || result.Lines[0].Text != "first" {
		t.Fatalf("fallback followed process cwd: %+v %v", result, err)
	}
}

func TestCloseAndCancellation(t *testing.T) {
	f, dir := newFS(t, nil)
	put(t, filepath.Join(dir, "a.txt"), "original")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := f.Read(ctx, "a.txt", 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := f.Write(ctx, "a.txt", "changed"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if get(t, filepath.Join(dir, "a.txt")) != "original" {
		t.Fatal("canceled write changed original")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Read(context.Background(), "a.txt", 1, 1); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := f.Write(context.Background(), "a.txt", "changed"); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}

func TestCloseWaitsForOperation(t *testing.T) {
	f, _ := newFS(t, nil)
	done, err := f.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- f.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("closed with active operation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	done()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish")
	}
}

func TestCanceledWriteLockWait(t *testing.T) {
	f, _ := newFS(t, nil)
	unlock, err := f.lockWrite(context.Background(), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		release, err := f.lockWrite(ctx, "a.txt")
		if release != nil {
			release()
		}
		waiting <- err
	}()
	cancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled wait hung")
	}
	unlock()
	if len(f.writes) != 0 {
		t.Fatal("canceled wait retained lock")
	}
}
