//go:build !darwin

package laodi

import (
	"errors"
	"os"
)

func swapRuntimeDirectories(_ *os.File, _, _ string) error {
	return errors.New("atomic runtime updates require macOS")
}
