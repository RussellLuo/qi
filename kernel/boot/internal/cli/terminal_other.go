//go:build !darwin && !linux && !windows

package cli

import "os"

func IsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
