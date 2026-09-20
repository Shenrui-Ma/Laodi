//go:build windows

package laodi

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// Use the Unicode Shell interfaces directly, avoiding WScript's automation
// persistence wrapper. Both interfaces are provided by Windows itself:
// https://learn.microsoft.com/windows/win32/shell/links
// https://learn.microsoft.com/windows/win32/api/shobjidl_core/nn-shobjidl_core-ishelllinkw
var shortcutOle32 = syscall.NewLazyDLL("ole32.dll")
var shortcutCoInitialize = shortcutOle32.NewProc("CoInitializeEx")
var shortcutCoUninitialize = shortcutOle32.NewProc("CoUninitialize")
var shortcutCoCreate = shortcutOle32.NewProc("CoCreateInstance")

type shortcutGUID struct {
	D1     uint32
	D2, D3 uint16
	D4     [8]byte
}

var shortcutCLSID = shortcutGUID{D1: 0x00021401, D4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
var shortcutIID = shortcutGUID{D1: 0x000214f9, D4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
var shortcutPersistIID = shortcutGUID{D1: 0x0000010b, D4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

type shortcutCOM struct{ vtable *[21]uintptr }

func shortcutHRESULT(stage string, hr uintptr) error {
	if int32(hr) >= 0 {
		return nil
	}
	return &windowsServiceError{Class: "operation_failed", Stage: stage, HResult: int64(int32(hr))}
}

//go:uintptrescapes
func (p *shortcutCOM) call(method int, args ...uintptr) uintptr {
	values := append([]uintptr{uintptr(unsafe.Pointer(p))}, args...)
	hr, _, _ := syscall.SyscallN(p.vtable[method], values...)
	runtime.KeepAlive(p)
	return hr
}
func (p *shortcutCOM) release() { p.call(2) }
func newShortcutCOM() (*shortcutCOM, error) {
	var p *shortcutCOM
	hr, _, _ := shortcutCoCreate.Call(uintptr(unsafe.Pointer(&shortcutCLSID)), 0, 1, uintptr(unsafe.Pointer(&shortcutIID)), uintptr(unsafe.Pointer(&p)))
	if err := shortcutHRESULT("shortcut_create", hr); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("Windows Shell returned no shortcut interface")
	}
	return p, nil
}
func (p *shortcutCOM) persist() (*shortcutCOM, error) {
	var out *shortcutCOM
	hr := p.call(0, uintptr(unsafe.Pointer(&shortcutPersistIID)), uintptr(unsafe.Pointer(&out)))
	if err := shortcutHRESULT("shortcut_persist", hr); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("Windows Shell returned no persistence interface")
	}
	return out, nil
}
func (p *shortcutCOM) setText(method int, value string) error {
	text, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	hr := p.call(method, uintptr(unsafe.Pointer(text)))
	runtime.KeepAlive(text)
	return shortcutHRESULT("shortcut_configure", hr)
}
func writeNativeWindowsShortcut(path, target, args string) error {
	for _, value := range []string{path, target, args} {
		text, err := syscall.UTF16FromString(value)
		if err != nil || len(text) > 32768 {
			return errors.New("shortcut value exceeds the Windows UTF-16 limit or contains NUL")
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := shortcutCoInitialize.Call(0, 2) // COINIT_APARTMENTTHREADED
	if err := shortcutHRESULT("shortcut_initialize", hr); err != nil {
		return err
	}
	defer shortcutCoUninitialize.Call()
	link, err := newShortcutCOM()
	if err != nil {
		return err
	}
	defer link.release()
	for _, item := range []struct {
		method int
		value  string
	}{{20, target}, {11, args}, {9, filepath.Dir(target)}, {7, "Laodi user background monitoring"}} {
		if err := link.setText(item.method, item.value); err != nil {
			return err
		}
	}
	if err := shortcutHRESULT("shortcut_configure", link.call(15, 7)); err != nil {
		return err
	} // SW_SHOWMINNOACTIVE
	persisted, err := link.persist()
	if err != nil {
		return err
	}
	defer persisted.release()
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	hr = persisted.call(6, uintptr(unsafe.Pointer(name)), 1) // IPersistFile::Save
	runtime.KeepAlive(name)
	if err := shortcutHRESULT("shortcut_save", hr); err != nil {
		return err
	}
	// Reopen on a separate COM object: validate serialized values, not the
	// original object's cached properties. Never resolve or execute the link.
	reopened, err := newShortcutCOM()
	if err != nil {
		return err
	}
	defer reopened.release()
	reader, err := reopened.persist()
	if err != nil {
		return err
	}
	defer reader.release()
	hr = reader.call(5, uintptr(unsafe.Pointer(name)), 0) // IPersistFile::Load, STGM_READ
	runtime.KeepAlive(name)
	if err := shortcutHRESULT("shortcut_load", hr); err != nil {
		return err
	}
	targetBuf := make([]uint16, 32768)
	argsBuf := make([]uint16, 32768)
	hr = reopened.call(3, uintptr(unsafe.Pointer(&targetBuf[0])), uintptr(len(targetBuf)), 0, 4) // SLGP_RAWPATH
	if err := shortcutHRESULT("shortcut_verify", hr); err != nil {
		return err
	}
	hr = reopened.call(10, uintptr(unsafe.Pointer(&argsBuf[0])), uintptr(len(argsBuf)))
	if err := shortcutHRESULT("shortcut_verify", hr); err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(syscall.UTF16ToString(targetBuf)), filepath.Clean(target)) || syscall.UTF16ToString(argsBuf) != args {
		return errors.New("Windows shortcut serialized values differ from plan")
	}
	return nil
}
