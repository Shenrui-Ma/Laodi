//go:build windows

package laodi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var replaceClientSettings = fileKernel.NewProc("ReplaceFileW")

func checkHookPath(home, path string) error {
	for _, value := range []string{home, path} {
		if _, err := windowsPrivatePath(value); err != nil {
			return err
		}
		if err := windowsNoReparseAncestors(value); err != nil {
			return err
		}
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("hook settings path must remain inside the selected home")
	}
	if err := windowsLocalVolume(home); err != nil {
		return err
	}
	current := home
	parts := strings.Split(rel, string(filepath.Separator))
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			current = filepath.Join(current, parts[i])
		}
		f, err := os.Open(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("cannot inspect hook settings path")
		}
		err = windowsCheckClientHandle(syscall.Handle(f.Fd()), i < len(parts)-1)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Client-owned files may inherit administrative access. Preserve that existing
// ACL; reject write access by other users rather than weakening Laodi's private
// state ACL policy. Read-only ACEs do not authorize replacing hook commands.
func windowsCheckClientHandle(handle syscall.Handle, directory bool) error {
	if err := windowsCheckFileIdentity(handle, directory); err != nil {
		return err
	}
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return err
	}
	var owner *syscall.SID
	var acl, descriptor *byte
	result, _, _ := getFileSecurityInfo.Call(uintptr(handle), 1, 1|4, uintptr(unsafe.Pointer(&owner)), 0, uintptr(unsafe.Pointer(&acl)), 0, uintptr(unsafe.Pointer(&descriptor)))
	if result != 0 {
		return syscall.Errno(result)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(descriptor))))
	ownerSID, err := owner.String()
	if err != nil || (ownerSID != sid && !(directory && (ownerSID == "S-1-5-18" || ownerSID == "S-1-5-32-544"))) {
		return errors.New("hook settings must be owned by the current user")
	}
	if acl == nil {
		return errors.New("hook settings require a non-null DACL")
	}
	header := (*struct {
		Revision, Reserved     byte
		Size, Count, Reserved2 uint16
	})(unsafe.Pointer(acl))
	for i := uint16(0); i < header.Count; i++ {
		var ace *byte
		ok, _, callErr := getSecurityACE.Call(uintptr(unsafe.Pointer(acl)), uintptr(i), uintptr(unsafe.Pointer(&ace)))
		if ok == 0 {
			return callErr
		}
		h := (*struct {
			Type, Flags byte
			Size        uint16
			Mask        uint32
		})(unsafe.Pointer(ace))
		if h.Type != 0 || h.Size < 12 {
			return errors.New("hook settings contain an unsupported DACL entry")
		}
		aceSID, err := (*syscall.SID)(unsafe.Add(unsafe.Pointer(ace), 8)).String()
		if err != nil {
			return errors.New("hook settings contain an invalid DACL entry")
		}
		if directory && aceSID == "S-1-3-0" && h.Flags&8 != 0 {
			continue
		}
		// FILE_WRITE_DATA/APPEND/EA/ATTRIBUTES, DELETE_CHILD, DELETE,
		// WRITE_DAC/OWNER, GENERIC_WRITE/ALL can alter this hook surface.
		const writeMask = 0x00000156 | 0x00010000 | 0x00040000 | 0x00080000 | 0x40000000 | 0x10000000
		if aceSID != sid && aceSID != "S-1-5-18" && aceSID != "S-1-5-32-544" && h.Mask&writeMask != 0 {
			return errors.New("hook settings grant write access to another user")
		}
	}
	return nil
}

func ensureHookConfigParent(plan HookConfigPlan) error {
	if err := checkHookPath(plan.options.Home, plan.ConfigPath); err != nil {
		return err
	}
	if err := windowsCreateDirectory(filepath.Dir(plan.ConfigPath)); err != nil {
		return errors.New("cannot create hook settings directory")
	}
	if err := verifyWindowsInstallationPath(filepath.Dir(plan.ConfigPath)); err != nil {
		return err
	}
	return checkHookPath(plan.options.Home, plan.ConfigPath)
}

func readHookConfigFile(path string) ([]byte, os.FileMode, error) {
	if _, err := windowsPrivatePath(path); err != nil {
		return nil, 0, err
	}
	if err := windowsNoReparseAncestors(path); err != nil {
		return nil, 0, err
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, 0600, err
	}
	defer parent.Close()
	unpin, err := windowsPinRoot(parent)
	if err != nil {
		return nil, 0, err
	}
	defer unpin()
	name := filepath.Base(path)
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, 0600, err
	}
	f, err := parent.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, 0, errors.New("cannot open hook settings")
	}
	defer f.Close()
	if err := windowsCheckClientHandle(syscall.Handle(f.Fd()), false); err != nil {
		return nil, 0, err
	}
	opened, err := f.Stat()
	after, afterErr := parent.Lstat(name)
	if err != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		return nil, 0, errors.New("hook settings changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHookConfigBytes+1))
	if err != nil || len(data) > maxHookConfigBytes {
		return nil, 0, errors.New("hook settings are unreadable or exceed 2 MiB")
	}
	return data, before.Mode().Perm(), nil
}

func replaceHookConfigFile(path string, data, expected []byte, mode os.FileMode) error {
	if len(data) > maxHookConfigBytes {
		return errors.New("hook settings exceed 2 MiB limit")
	}
	if _, err := windowsPrivatePath(path); err != nil {
		return err
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errors.New("cannot open hook settings parent")
	}
	defer parent.Close()
	unpin, err := windowsPinRoot(parent)
	if err != nil {
		return err
	}
	defer unpin()
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	err = windowsCheckClientHandle(syscall.Handle(dir.Fd()), true)
	dir.Close()
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("cannot name hook settings temporary file")
	}
	name := ".laodi-hook-" + hex.EncodeToString(random[:]) + ".tmp"
	stagePath := filepath.Join(parent.Name(), name)
	f, err := windowsOpen(stagePath, os.O_WRONLY, true, false, syscall.FILE_SHARE_READ)
	if err != nil {
		return errors.New("cannot create private hook settings temporary file")
	}
	defer parent.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return errors.New("cannot write hook settings")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errors.New("cannot sync hook settings")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close hook settings")
	}
	current, currentMode, err := readHookConfigFile(path)
	oldPtr, _ := syscall.UTF16PtrFromString(stagePath)
	newPtr, _ := syscall.UTF16PtrFromString(path)
	if expected == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("hook settings appeared during installation; refusing to replace them")
		}
		return windowsMovePrivatePath(oldPtr, newPtr, 8)
	}
	if err != nil || !bytes.Equal(current, expected) || currentMode != mode {
		return errors.New("hook settings changed during installation; retry after the other editor finishes")
	}
	// ReplaceFileW retains the replaced file's DACL. No IGNORE_ACL_ERRORS or
	// IGNORE_MERGE_ERRORS flags: a permission merge failure must remain visible.
	for attempt := 0; ; attempt++ {
		ok, _, callErr := replaceClientSettings.Call(uintptr(unsafe.Pointer(newPtr)), uintptr(unsafe.Pointer(oldPtr)), 0, 0, 0, 0)
		if ok != 0 {
			return nil
		}
		if attempt >= 4 || (!errors.Is(callErr, syscall.ERROR_ACCESS_DENIED) && !errors.Is(callErr, syscall.Errno(32)) && !errors.Is(callErr, syscall.Errno(33))) {
			return errors.New("cannot atomically replace hook settings while preserving permissions")
		}
		time.Sleep(time.Duration(20<<attempt) * time.Millisecond)
	}
}
