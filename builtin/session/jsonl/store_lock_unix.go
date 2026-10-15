//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package jsonl

import (
	"errors"
	"os"
	"syscall"
)

func lockStoreFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrStoreLocked
	}
	return err
}
