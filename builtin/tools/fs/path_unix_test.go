//go:build unix

package fstool

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSpecialFilesRejected(t *testing.T) {
	f, dir := newFS(t, nil)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Read(context.Background(), "pipe", 1, 1); err == nil {
		t.Fatal("read accepted FIFO")
	}
	if _, err := f.Write(context.Background(), "pipe", "x"); err == nil {
		t.Fatal("write accepted FIFO")
	}
	info, err := os.Lstat(filepath.Join(dir, "pipe"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("FIFO replaced")
	}
	assertNoTemps(t, dir)
}
