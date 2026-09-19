//go:build windows

package laodi

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Retry the kernel lock on one verified handle. Reopening the directory tree,
// checking every ACL and pinning all ancestors on every 5 ms retry caused the
// contending producers themselves to multiply filesystem work. Keep those same
// checks and pins alive across retries; no file security decision is cached
// beyond this call and no lock pathname can be swapped while a waiter exists.
func lockHookInbox(root *os.Root) (func(), error) {
	deadline := time.Now().Add(100 * time.Millisecond)
	if _, err := windowsPrivatePath(root.Name()); err != nil {
		return nil, errHookInboxUnavailable
	}
	if err := windowsLocalVolume(root.Name()); err != nil {
		return nil, errHookInboxUnavailable
	}
	unpin, err := windowsPinRoot(root)
	if err != nil {
		return nil, errHookInboxUnavailable
	}
	directory, err := windowsOpen(root.Name(), os.O_RDONLY, false, true, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	if err != nil {
		unpin()
		return nil, errHookInboxUnavailable
	}
	cleanup := func() { directory.Close(); unpin() }
	created, err := openStateFile(root, ".lock", os.O_RDWR, true)
	if err != nil {
		cleanup()
		return nil, errHookInboxUnavailable
	}
	created.Close()
	path, err := windowsPrivateName(root, ".lock")
	if err != nil {
		cleanup()
		return nil, errHookInboxUnavailable
	}
	f, err := windowsOpen(path, os.O_RDWR, false, false, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	if err != nil {
		cleanup()
		return nil, errHookInboxUnavailable
	}
	opened, err := f.Stat()
	named, nameErr := root.Lstat(".lock")
	if err != nil || nameErr != nil || !os.SameFile(opened, named) {
		f.Close()
		cleanup()
		return nil, errHookInboxUnavailable
	}
	for {
		if !time.Now().Before(deadline) {
			f.Close()
			cleanup()
			return nil, errHookInboxUnavailable
		}
		var overlapped syscall.Overlapped
		ok, _, callErr := lockFileEx.Call(f.Fd(), 1|2, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		if ok != 0 {
			var once sync.Once
			return func() { once.Do(func() { f.Close(); cleanup() }) }, nil
		}
		remaining := time.Until(deadline)
		if !errors.Is(callErr, syscall.Errno(33)) || remaining <= 0 {
			f.Close()
			cleanup()
			return nil, errHookInboxUnavailable
		}
		if remaining > 5*time.Millisecond {
			remaining = 5 * time.Millisecond
		}
		time.Sleep(remaining)
	}
}
