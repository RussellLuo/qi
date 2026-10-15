package fstool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/tools"
)

// Per-call cwd is a file-definition contract; a Profile and Agent are unnecessary.
func TestDefinitionExecutionCWDsAreIndependent(t *testing.T) {
	files, fallback := newFS(t, nil)
	read, write := NewReadDefinition(files), NewWriteDefinition(files)
	type outcome struct {
		cwd, name   string
		write, read tools.ExecutionResult
		err         error
	}
	done := make(chan outcome, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, name := range []string{"left", "right"} {
		cwd := t.TempDir()
		go func() {
			out := outcome{cwd: cwd, name: name}
			args, _ := json.Marshal(map[string]string{"file_path": "same.txt", "content": name})
			out.write, out.err = write.Execute(ctx, tools.Execution{CWD: cwd, Arguments: args})
			if out.err == nil {
				out.read, out.err = read.Execute(ctx, tools.Execution{CWD: cwd, Arguments: json.RawMessage(`{"file_path":"same.txt"}`)})
			}
			done <- out
		}()
	}
	for range 2 {
		select {
		case out := <-done:
			if out.err != nil || out.write.IsError || out.read.IsError {
				t.Fatalf("file operation: %+v", out)
			}
			validateSchema(t, write.Output, out.write.Value)
			validateSchema(t, read.Output, out.read.Value)
			var result ReadResult
			if err := json.Unmarshal(out.read.Value, &result); err != nil {
				t.Fatal(err)
			}
			if result.Path != filepath.Join(out.cwd, "same.txt") || len(result.Lines) != 1 || result.Lines[0].Text != out.name {
				t.Fatalf("cwd crossed calls: %+v", result)
			}
			if data, err := os.ReadFile(filepath.Join(out.cwd, "same.txt")); err != nil || string(data) != out.name {
				t.Fatalf("file: %q / %v", data, err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if _, err := os.Stat(filepath.Join(fallback, "same.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("call used fallback cwd", err)
	}
}

func TestDefinitionCancellationDoesNotTouchFiles(t *testing.T) {
	files, cwd := newFS(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, definition := range []tools.Definition{NewReadDefinition(files), NewWriteDefinition(files)} {
		args := json.RawMessage(`{"file_path":"cancelled.txt"}`)
		if definition.Name == "write_file" {
			args = json.RawMessage(`{"file_path":"cancelled.txt","content":"changed"}`)
		}
		_, err := definition.Execute(ctx, tools.Execution{Arguments: args})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation = %v", definition.Name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cwd, "cancelled.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled definition touched disk", err)
	}
}
