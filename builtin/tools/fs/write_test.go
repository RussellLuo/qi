package fstool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func get(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Create/update values and confirmation text follow DSH tool-fs/src/write.ts;
// before/after follow fs-local/src/{index,fsio}.ts, inspected 2026-10-14.
func TestWriteCreateUpdateAndLF(t *testing.T) {
	f, dir := newFS(t, nil)
	definition := NewWriteDefinition(f)
	created := execute(t, definition, `{"file_path":"./a.txt","content":"hello\r\nworld\r"}`)
	var result WriteResult
	if err := json.Unmarshal(created.Value, &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, WriteResult{filepath.Join(dir, "a.txt"), "create", nil, "hello\nworld\r"}) {
		t.Fatalf("%+v", result)
	}
	if got := textOf(t, created); got != "<path>"+filepath.Join(dir, "a.txt")+"</path>\n<type>file</type>\n<content>\nCreated file\n</content>" {
		t.Fatal(got)
	}
	if get(t, filepath.Join(dir, "a.txt")) != "hello\r\nworld\r" {
		t.Fatal("on-disk newlines changed")
	}
	updated := execute(t, definition, `{"file_path":"a.txt","content":"new\r\ntext"}`)
	if err := json.Unmarshal(updated.Value, &result); err != nil {
		t.Fatal(err)
	}
	before := "hello\nworld\r"
	if !reflect.DeepEqual(result, WriteResult{filepath.Join(dir, "a.txt"), "update", &before, "new\ntext"}) {
		t.Fatalf("%+v", result)
	}
	if got := textOf(t, updated); got != "<path>"+filepath.Join(dir, "a.txt")+"</path>\n<type>file</type>\n<content>\nUpdated file\n</content>" {
		t.Fatal(got)
	}
	if get(t, filepath.Join(dir, "a.txt")) != "new\r\ntext" {
		t.Fatal("on-disk newlines changed")
	}
	assertNoTemps(t, dir)
}

func TestWriteDiffBasisBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, old, new string
		wantBefore     *string
	}{
		{"below", "old", "new", pointer("old")},
		{"old at cap", "1234", "new", nil},
		{"old above cap", "12345", "new", nil},
		{"new at cap", "old", "1234", nil},
		{"new above cap", "old", "12345", nil},
		{"UTF8 byte cap", "你好", "a", nil},
		{"invalid old UTF8", string([]byte{0xff}), "new", nil},
		{"empty old", "", "new", pointer("")},
		{"empty new", "old", "", pointer("old")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, dir := newFS(t, func(o *Options) { o.DiffBasisMaxBytes = 4 })
			put(t, filepath.Join(dir, "a.txt"), tt.old)
			result, err := f.Write(context.Background(), "a.txt", tt.new)
			if err != nil {
				t.Fatal(err)
			}
			if result.Operation != "update" || !reflect.DeepEqual(result.Before, tt.wantBefore) || result.After != tt.new {
				t.Fatalf("%+v", result)
			}
			if get(t, filepath.Join(dir, "a.txt")) != tt.new {
				t.Fatal("wrong file content")
			}
			assertNoTemps(t, dir)
		})
	}
	f, dir := newFS(t, nil)
	result := execute(t, NewWriteDefinition(f), `{"file_path":"empty.txt","content":""}`)
	var empty WriteResult
	if err := json.Unmarshal(result.Value, &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Path != filepath.Join(dir, "empty.txt") || empty.Operation != "create" || empty.Before != nil || empty.After != "" || get(t, filepath.Join(dir, "empty.txt")) != "" {
		t.Fatal(string(result.Value))
	}
}

func pointer(s string) *string { return &s }

func TestWriteValidation(t *testing.T) {
	f, dir := newFS(t, nil)
	put(t, filepath.Join(dir, "a.txt"), "old")
	for _, args := range []string{
		`{}`, `null`, `[]`, `{"file_path":"a.txt"}`, `{"file_path":"a.txt","content":null}`,
		`{"file_path":"a.txt","content":2}`, `{"file_path":" ","content":"x"}`,
		`{"file_path":"a.txt","content":"x","justification":"why"}`,
	} {
		if _, err := NewWriteDefinition(f).Execute(context.Background(), executionArgs(args)); err == nil {
			t.Fatalf("accepted %s", args)
		}
		if get(t, filepath.Join(dir, "a.txt")) != "old" {
			t.Fatal("validation failure changed file")
		}
	}
	if _, err := f.Write(context.Background(), "a.txt", string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := f.Write(context.Background(), "a.txt", "你好"); err != nil {
		t.Fatal(err)
	}
	if get(t, filepath.Join(dir, "a.txt")) != "你好" {
		t.Fatal("incorrect UTF-8 write")
	}
	assertNoTemps(t, dir)
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".qi-fs-") {
			t.Fatalf("temporary file leaked: %s", e.Name())
		}
	}
}

func TestAtomicWriteFailurePreservesFileAndCleansTemp(t *testing.T) {
	for _, stage := range []string{"partial write", "cancel after write", "target check", "rename"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			put(t, filepath.Join(dir, "a.txt"), "original")
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			boom := errors.New("injected write failure")
			err = atomicWrite(ctx, root, "a.txt", 0600, func(file *os.File) error {
				if _, err := file.WriteString("partial new content"); err != nil {
					return err
				}
				switch stage {
				case "partial write":
					return boom
				case "cancel after write":
					cancel()
				case "target check":
					// Keep the original elsewhere, then make the target a directory.
					if err := root.Rename("a.txt", "saved.txt"); err != nil {
						return err
					}
					return root.Mkdir("a.txt", 0700)
				case "rename":
					// Remove the owned temporary name to force Rename to fail.
					return root.Remove(filepath.Base(file.Name()))
				}
				return nil
			})
			if err == nil {
				t.Fatal("expected failure")
			}
			if stage == "partial write" && !errors.Is(err, boom) {
				t.Fatal(err)
			}
			if stage == "cancel after write" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			original := "a.txt"
			if stage == "target check" {
				original = "saved.txt"
			}
			if get(t, filepath.Join(dir, original)) != "original" {
				t.Fatal("original truncated")
			}
			assertNoTemps(t, dir)
		})
	}
}

func TestConcurrentWritesHaveCoherentBefore(t *testing.T) {
	f, dir := newFS(t, nil)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	const count = 24
	results := make(chan WriteResult, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := "sub/a.txt"
			if i%2 == 0 {
				path = "./alias/a.txt"
			}
			result, err := f.Write(context.Background(), path, fmt.Sprintf("value-%d", i))
			if err != nil {
				errs <- err
			} else {
				results <- result
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// Each successful write's before must be the preceding successful after.
	// Starting from the unique create yields one chain ending at the file.
	var first *WriteResult
	next := map[string]WriteResult{}
	for r := range results {
		if r.Operation == "create" {
			if first != nil {
				t.Fatal("multiple creates")
			}
			copy := r
			first = &copy
		} else {
			if r.Before == nil {
				t.Fatal("update without basis")
			}
			if _, exists := next[*r.Before]; exists {
				t.Fatal("branching before chain")
			}
			next[*r.Before] = r
		}
	}
	if first == nil {
		t.Fatal("no create")
	}
	last := first.After
	for i := 1; i < count; i++ {
		r, ok := next[last]
		if !ok {
			t.Fatalf("broken before chain at %s", last)
		}
		last = r.After
	}
	if get(t, filepath.Join(dir, "sub", "a.txt")) != last {
		t.Fatal("final write differs from final chain value")
	}
	if len(f.writes) != 0 {
		t.Fatal("path locks retained")
	}
	assertNoTemps(t, dir)
	assertNoTemps(t, filepath.Join(dir, "sub"))
}

// DSH tool-fs has no write-size knob; diff capture is independently bounded.
func TestWriteAboveFormerLimit(t *testing.T) {
	f, dir := newFS(t, nil)
	put(t, filepath.Join(dir, "a.txt"), "old")
	content := strings.Repeat("x", 1024*1024+1)
	result, err := f.Write(context.Background(), "a.txt", content)
	if err != nil || result.Before == nil || *result.Before != "old" || result.After != content {
		t.Fatalf("large write: %+v / %v", result.Before, err)
	}
	if get(t, filepath.Join(dir, "a.txt")) != content {
		t.Fatal("large write incomplete")
	}
	assertNoTemps(t, dir)
}
