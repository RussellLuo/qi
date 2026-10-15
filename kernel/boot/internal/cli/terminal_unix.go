//go:build darwin || linux

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// IsTerminal distinguishes a terminal from other character devices (e.g. null).
func IsTerminal(file *os.File) bool {
	var size [4]uint16
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	return errno == 0
}
