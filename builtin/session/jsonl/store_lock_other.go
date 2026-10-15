//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package jsonl

import (
	"errors"
	"os"
)

func lockStoreFile(*os.File) error {
	return errors.New("exclusive JSONL writer locking is unsupported on this platform")
}
