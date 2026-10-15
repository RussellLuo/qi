package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
)

// The kernel releases this lifetime writer lock even after a process crash.
// It covers recovery, truncation, creation/deletion and every ordinary append.
func acquireStoreLock(dir string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(dir, ".writer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockStoreFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock JSONL store %q: %w", dir, err)
	}
	return file, nil
}
