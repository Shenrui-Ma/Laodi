//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package laodi

import (
	"errors"
	"os"
)

func openExistingStateFile(root *os.Root, name string, flags int) (*os.File, error) {
	return root.OpenFile(name, flags, 0)
}

func AcquireLock(dir string) (func(), error) {
	return nil, errors.New("background monitoring is not supported on this platform: no verified process lock backend")
}
