//go:build windows

package laodi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var replaceClientSettings = fileKernel.NewProc("ReplaceFileW")

// This diagnostic contains only closed role, permission and check-stage
// categories. Never include a SID, account name, path or descriptor text.
type windowsHookACLError struct {
	Stage, Reason, Role, Permissions, Scope, Entry, Inheritance string
}

func (e *windowsHookACLError) Error() string {
	return fmt.Sprintf("hook settings ACL rejected (stage=%s, reason=%s, role=%s, permissions=%s, scope=%s, entry=%s, inheritance=%s)", e.Stage, e.Reason, e.Role, e.Permissions, e.Scope, e.Entry, e.Inheritance)
}

func windowsHookACLRole(sid, current string) string {
	if sid == current {
		return "current_user"
	}
	switch sid {
	case "S-1-5-18":
		return "system"
	case "S-1-5-32-544":
		return "administrators"
	case "S-1-3-0":
		return "creator_owner"
	case "S-1-1-0":
		return "everyone"
	case "S-1-5-11":
		return "authenticated_users"
	case "S-1-5-32-545":
		return "builtin_users"
	default:
		return "other_principal"
	}
}

func windowsHookWritePermissions(mask uint32) string {
	var permissions []string
	for _, p := range []struct {
		mask uint32
		name string
	}{{0x6, "data_write"}, {0x110, "metadata_write"}, {0x10040, "delete"}, {0xc0000, "security_write"}, {0x40000000, "generic_write"}, {0x10000000, "generic_all"}} {
		if mask&p.mask != 0 {
			permissions = append(permissions, p.name)
		}
	}
	return strings.Join(permissions, "+")
}

func windowsHookACLInheritance(flags byte) string {
	var inheritance []string
	for _, f := range []struct {
		flag byte
		name string
	}{{1, "files"}, {2, "directories"}, {4, "no_propagate"}, {8, "inherit_only"}, {16, "inherited"}} {
		if flags&f.flag != 0 {
			inheritance = append(inheritance, f.name)
		}
	}
	if len(inheritance) == 0 {
		return "none"
	}
	return strings.Join(inheritance, "+")
}

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
		stage := "ancestor_directory"
		if i == -1 {
			stage = "home_directory"
		} else if i == len(parts)-1 {
			stage = "settings_file"
		}
		err = windowsCheckClientHandleAt(syscall.Handle(f.Fd()), i < len(parts)-1, stage)
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
	stage := "settings_file"
	if directory {
		stage = "client_directory"
	}
	return windowsCheckClientHandleAt(handle, directory, stage)
}

func windowsCheckClientHandleAt(handle syscall.Handle, directory bool, stage string) error {
	reject := func(reason, role, permissions, scope, entry string) *windowsHookACLError {
		return &windowsHookACLError{Stage: stage, Reason: reason, Role: role, Permissions: permissions, Scope: scope, Entry: entry, Inheritance: "not_applicable"}
	}
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
		return reject("descriptor_unavailable", "unknown", "unknown", "object", "none")
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(descriptor))))
	if owner == nil {
		return reject("owner_unavailable", "unknown", "unknown", "object", "none")
	}
	ownerSID, err := owner.String()
	if err != nil {
		return reject("owner_unavailable", "unknown", "unknown", "object", "none")
	}
	if ownerSID != sid && !(directory && (ownerSID == "S-1-5-18" || ownerSID == "S-1-5-32-544")) {
		return reject("owner_not_current_user", windowsHookACLRole(ownerSID, sid), "ownership", "object", "none")
	}
	if acl == nil {
		return reject("null_dacl", "everyone", "full_access", "object", "none")
	}
	header := (*struct {
		Revision, Reserved     byte
		Size, Count, Reserved2 uint16
	})(unsafe.Pointer(acl))
	for i := uint16(0); i < header.Count; i++ {
		var ace *byte
		ok, _, _ := getSecurityACE.Call(uintptr(unsafe.Pointer(acl)), uintptr(i), uintptr(unsafe.Pointer(&ace)))
		if ok == 0 {
			return reject("entry_unavailable", "unknown", "unknown", "unknown", "unknown")
		}
		h := (*struct {
			Type, Flags byte
			Size        uint16
			Mask        uint32
		})(unsafe.Pointer(ace))
		if (h.Type != 0 && h.Type != 1) || h.Size < 16 || h.Flags&^byte(0x1f) != 0 {
			return reject("unsupported_entry", "unknown", "unknown", "unknown", "unsupported")
		}
		aceSID, err := (*syscall.SID)(unsafe.Add(unsafe.Pointer(ace), 8)).String()
		if err != nil {
			return reject("invalid_entry", "unknown", "unknown", "unknown", "unknown")
		}
		// A conventional deny ACE cannot grant foreign access. Do not attempt
		// to subtract it from later allows: ordering/group membership matters,
		// and every untrusted write allow must still be rejected below.
		if h.Type == 1 {
			continue
		}
		// Inherit-only ACEs do not authorize access to this object. On a
		// directory, keep rejecting foreign writes that could propagate to
		// newly created hook files or directories before their next check.
		if h.Flags&8 != 0 && (!directory || h.Flags&3 == 0) {
			continue
		}
		if directory && aceSID == "S-1-3-0" && h.Flags&8 != 0 {
			continue
		}
		// FILE_WRITE_DATA/APPEND/EA/ATTRIBUTES, DELETE_CHILD, DELETE,
		// WRITE_DAC/OWNER, GENERIC_WRITE/ALL can alter this hook surface.
		permissions := windowsHookWritePermissions(h.Mask)
		if aceSID != sid && aceSID != "S-1-5-18" && aceSID != "S-1-5-32-544" && permissions != "" {
			scope := "object"
			if h.Flags&8 != 0 {
				scope = "descendants"
			}
			diagnostic := reject("foreign_write_allow", windowsHookACLRole(aceSID, sid), permissions, scope, "allow")
			diagnostic.Inheritance = windowsHookACLInheritance(h.Flags)
			return diagnostic
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
	err = windowsCheckClientHandleAt(syscall.Handle(dir.Fd()), true, "settings_parent")
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
