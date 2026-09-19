//go:build darwin

package laodi

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// macOS SDK sys/syscall.h and sys/stdio.h define renameatx_np=488 and
// RENAME_SWAP=2. Use an open parent descriptor and relative directory names.
// There is deliberately no non-atomic fallback on unsupported filesystems.
func swapRuntimeDirectories(parent *os.File, first, second string) error {
	a, err := syscall.BytePtrFromString(first)
	if err != nil {
		return err
	}
	b, err := syscall.BytePtrFromString(second)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(488, parent.Fd(), uintptr(unsafe.Pointer(a)), parent.Fd(), uintptr(unsafe.Pointer(b)), 2, 0)
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	runtime.KeepAlive(parent)
	if errno != 0 {
		return errno
	}
	return nil
}
