//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package laodi

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// O_NOFOLLOW rejects a last-moment symlink swap before a descriptor is opened;
// O_NONBLOCK prevents a replaced FIFO from blocking the monitor. store.go then
// verifies descriptor identity against the bound directory before reading.
func openExistingStateFile(root *os.Root, name string, flags int) (*os.File, error) {
	path := filepath.Join(root.Name(), name)
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// AcquireLock holds a kernel advisory lock until release or process exit. The
// lock file intentionally remains in place: unlinking it could create two lock
// inodes and admit concurrent writers. Its contents are not a PID lease.
func AcquireLock(dir string) (func(), error) {
	root, err := openStateRoot(dir, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openStateFile(root, ".lock", os.O_RDWR, true)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state directory is locked by another Laodi process: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		})
	}, nil
}
