//go:build windows

package laodi

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var lockFileEx = fileKernel.NewProc("LockFileEx")

// A real kernel byte-range lock is released on CloseHandle or process exit.
// Denying FILE_SHARE_DELETE for the lock's lifetime prevents path replacement
// from creating a second lock identity while the first writer is active.
func AcquireLock(dir string) (func(), error) {
	root, err := openStateRoot(dir, true)
	if err != nil {
		return nil, err
	}
	unpin, err := windowsPinRoot(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	cleanup := func() { unpin(); root.Close() }
	created, err := openStateFile(root, ".lock", os.O_RDWR, true)
	if err != nil {
		cleanup()
		return nil, err
	}
	created.Close()
	path, err := windowsPrivateName(root, ".lock")
	if err != nil {
		cleanup()
		return nil, err
	}
	f, err := windowsOpen(path, os.O_RDWR, false, false, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	if err != nil {
		cleanup()
		return nil, err
	}
	opened, err := f.Stat()
	named, nameErr := root.Lstat(".lock")
	if err != nil || nameErr != nil || !os.SameFile(opened, named) {
		f.Close()
		cleanup()
		return nil, fmt.Errorf("lock file changed while opening")
	}
	var overlapped syscall.Overlapped
	ok, _, callErr := lockFileEx.Call(f.Fd(), 1|2, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		f.Close()
		cleanup()
		return nil, fmt.Errorf("state directory is locked by another Laodi process: %w", callErr)
	}
	var once sync.Once
	return func() { once.Do(func() { f.Close(); cleanup() }) }, nil
}
