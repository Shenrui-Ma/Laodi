//go:build windows

package laodi

// Windows persistence intentionally accepts only local fixed NTFS/ReFS volumes.
// Mode bits are not ACLs on Windows. Every open checks the handle's owner, DACL,
// reparse attribute and link count before any content is read or written.
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	fileKernel           = syscall.NewLazyDLL("kernel32.dll")
	fileAdvapi           = syscall.NewLazyDLL("advapi32.dll")
	convertSD            = fileAdvapi.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	getFileSecurityInfo  = fileAdvapi.NewProc("GetSecurityInfo")
	getSecurityControl   = fileAdvapi.NewProc("GetSecurityDescriptorControl")
	getSecurityACE       = fileAdvapi.NewProc("GetAce")
	getVolumePathName    = fileKernel.NewProc("GetVolumePathNameW")
	getVolumeInformation = fileKernel.NewProc("GetVolumeInformationW")
	getDriveType         = fileKernel.NewProc("GetDriveTypeW")
	movePrivateFile      = fileKernel.NewProc("MoveFileExW")
)

const fileFullControl = 0x001f01ff

var currentSID = sync.OnceValues(func() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String()
})

func windowsCurrentUserSID() (string, error) { return currentSID() }

func windowsPrivateSecurityAttributes() (*syscall.SecurityAttributes, func(), error) {
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return nil, nil, err
	}
	// Protected DACL; child files and directories inherit only these two ACEs.
	sddl, err := syscall.UTF16PtrFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return nil, nil, err
	}
	var descriptor uintptr
	ok, _, callErr := convertSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if ok == 0 {
		return nil, nil, callErr
	}
	attributes := &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: descriptor}
	return attributes, func() { syscall.LocalFree(syscall.Handle(descriptor)) }, nil
}

// Reject Win32 aliases before filepath.Clean can erase their distinguishing
// syntax. ADS, device/UNC paths, short aliases, trailing dots and spaces never
// name a private store. Case aliases are allowed and identify the same handle.
func windowsPrivatePath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("private path is empty or invalid")
	}
	path = strings.ReplaceAll(path, "/", `\`)
	if strings.HasPrefix(path, `\\`) {
		return "", errors.New("private storage requires a local drive path")
	}
	if v := filepath.VolumeName(path); v != "" && (len(v) != 2 || v[1] != ':' || !filepath.IsAbs(path)) {
		return "", errors.New("private storage rejects device and drive-relative paths")
	}
	parts := strings.Split(strings.TrimPrefix(path, filepath.VolumeName(path)), `\`)
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.ContainsAny(part, `:~`) || strings.TrimRight(part, " .") != part {
			return "", errors.New("private storage rejects stream and ambiguous path names")
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return "", errors.New("private storage rejects reserved device names")
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func windowsLocalVolume(path string) error {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var volume [syscall.MAX_PATH + 1]uint16
	ok, _, callErr := getVolumePathName.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&volume[0])), uintptr(len(volume)))
	if ok == 0 {
		return callErr
	}
	kind, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(&volume[0])))
	if kind != 3 {
		return errors.New("private storage requires a local fixed drive")
	}
	var fs [64]uint16
	ok, _, callErr = getVolumeInformation.Call(uintptr(unsafe.Pointer(&volume[0])), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&fs[0])), uintptr(len(fs)))
	if ok == 0 {
		return callErr
	}
	nameFS := syscall.UTF16ToString(fs[:])
	if nameFS != "NTFS" && nameFS != "ReFS" {
		return errors.New("private storage requires NTFS or ReFS ACLs")
	}
	return nil
}

func windowsNoReparseAncestors(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		name, err := syscall.UTF16PtrFromString(p)
		if err != nil {
			return err
		}
		attrs, err := syscall.GetFileAttributes(name)
		if err != nil {
			if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) && !errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
				return err
			}
		} else if attrs&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("private storage rejects reparse points and junctions")
		}
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	return nil
}

func windowsCreateDirectory(path string) error {
	if err := windowsNoReparseAncestors(path); err != nil {
		return err
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa, free, err := windowsPrivateSecurityAttributes()
	if err != nil {
		return err
	}
	defer free()
	err = syscall.CreateDirectory(name, sa)
	if errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
		return nil
	}
	if errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		if err := windowsCreateDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		err = syscall.CreateDirectory(name, sa)
		if errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
			return nil
		}
	}
	return err
}

func windowsOpen(path string, flags int, create, directory bool, share uint32) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(syscall.GENERIC_READ | 0x00020000) // READ_CONTROL
	if flags&(os.O_WRONLY|os.O_RDWR) != 0 {
		access |= syscall.GENERIC_WRITE
	}
	disposition := uint32(syscall.OPEN_EXISTING)
	var attributes *syscall.SecurityAttributes
	if create {
		disposition = syscall.CREATE_NEW
		var free func()
		attributes, free, err = windowsPrivateSecurityAttributes()
		if err != nil {
			return nil, err
		}
		defer free()
	}
	options := uint32(syscall.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		options |= syscall.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := syscall.CreateFile(name, access, share, attributes, disposition, options, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private file", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	if err := windowsCheckHandle(handle, directory); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func windowsCheckHandle(handle syscall.Handle, directory bool) error {
	if err := windowsCheckFileIdentity(handle, directory); err != nil {
		return err
	}
	return windowsCheckPrivateDACL(handle)
}

func windowsCheckFileIdentity(handle syscall.Handle, directory bool) error {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("private storage rejects reparse points")
	}
	if (info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errors.New("private storage has incorrect file type")
	}
	if !directory && info.NumberOfLinks != 1 {
		return errors.New("private storage rejects hard links")
	}
	return nil
}

func windowsCheckPrivateDACL(handle syscall.Handle) error {
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
	if err != nil || ownerSID != sid {
		return errors.New("private storage must be owned by the current user")
	}
	var control uint16
	var revision uint32
	ok, _, callErr := getSecurityControl.Call(uintptr(unsafe.Pointer(descriptor)), uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision)))
	if ok == 0 {
		return callErr
	}
	if control&4 == 0 || acl == nil {
		return errors.New("private storage requires a non-null DACL")
	}
	header := (*struct {
		Revision, Reserved     byte
		Size, Count, Reserved2 uint16
	})(unsafe.Pointer(acl))
	userFull := false
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
			return errors.New("private storage contains an unsupported DACL entry")
		}
		aceSID, err := (*syscall.SID)(unsafe.Add(unsafe.Pointer(ace), 8)).String()
		if err != nil || (aceSID != sid && aceSID != "S-1-5-18") {
			return errors.New("private storage grants access outside the current user and SYSTEM")
		}
		if aceSID == sid && h.Flags&8 == 0 && h.Mask&fileFullControl == fileFullControl {
			userFull = true
		}
	}
	if !userFull {
		return errors.New("private storage lacks current-user full control")
	}
	return nil
}

func openStateRoot(dir string, create bool) (*os.Root, error) {
	abs, err := windowsPrivatePath(dir)
	if err != nil {
		return nil, err
	}
	if err := windowsLocalVolume(abs); err != nil {
		return nil, err
	}
	if err := windowsNoReparseAncestors(abs); err != nil {
		return nil, err
	}
	if create {
		if err := windowsCreateDirectory(abs); err != nil {
			return nil, err
		}
	}
	// Deny deletion while binding os.Root to the same directory identity.
	checked, err := windowsOpen(abs, os.O_RDONLY, false, true, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	if err != nil {
		return nil, err
	}
	defer checked.Close()
	before, err := checked.Stat()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		return nil, errors.New("state directory changed while opening")
	}
	return root, nil
}

func privateDirectory(root *os.Root, name string) error {
	if _, err := windowsPrivateName(root, name); err != nil {
		return err
	}
	dir, err := openStateRoot(filepath.Join(root.Name(), name), true)
	if err != nil {
		return err
	}
	return dir.Close()
}

func windowsPrivateName(root *os.Root, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", errors.New("private file name must be a single component")
	}
	return windowsPrivatePath(filepath.Join(root.Name(), name))
}

func createPrivateFile(root *os.Root, name string, flags int) (*os.File, error) {
	_, err := windowsPrivateName(root, name)
	if err != nil {
		return nil, err
	}
	// Root.OpenFile binds creation to the opened directory, even if an ancestor
	// was renamed. The protected parent DACL is inherited at creation; verify
	// the resulting handle before exposing it to callers that write content.
	f, err := root.OpenFile(name, flags|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := windowsCheckHandle(syscall.Handle(f.Fd()), false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func openExistingStateFile(root *os.Root, name string, flags int) (*os.File, error) {
	_, err := windowsPrivateName(root, name)
	if err != nil {
		return nil, err
	}
	if flags&(os.O_TRUNC|os.O_CREATE) != 0 {
		return nil, errors.New("private existing files must not be truncated before validation")
	}
	f, err := root.OpenFile(name, flags, 0)
	if err != nil {
		return nil, err
	}
	if err := windowsCheckHandle(syscall.Handle(f.Fd()), false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func checkRegularFile(root *os.Root, name string) error {
	f, err := openStateFile(root, name, os.O_RDONLY, false)
	if err != nil {
		return err
	}
	return f.Close()
}

func openStateFile(root *os.Root, name string, flags int, create bool) (*os.File, error) {
	if create {
		f, err := createPrivateFile(root, name, flags)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	f, err := openExistingStateFile(root, name, flags)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	after, afterErr := root.Lstat(name)
	if err != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		f.Close()
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	return f, nil
}

func replaceStateFile(root *os.Root, oldName, newName string) error {
	return publishPrivateFile(root, oldName, newName, true)
}

// Pin every named ancestor for path-based Win32 calls. The directory handles
// deny FILE_SHARE_DELETE; after binding them, compare against os.Root so a
// previously renamed ancestor cannot redirect a publication to another tree.
func windowsPinRoot(root *os.Root) (func(), error) {
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	if err := windowsNoReparseAncestors(root.Name()); err != nil {
		return nil, err
	}
	for path := root.Name(); ; path = filepath.Dir(path) {
		f, err := os.Open(path)
		if err != nil {
			closeAll()
			return nil, err
		}
		files = append(files, f)
		if err := windowsCheckFileIdentity(syscall.Handle(f.Fd()), true); err != nil {
			closeAll()
			return nil, err
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	bound, err := root.Stat(".")
	named, namedErr := os.Stat(root.Name())
	if err != nil || namedErr != nil || !os.SameFile(bound, named) {
		closeAll()
		return nil, errors.New("private directory name no longer identifies the opened root")
	}
	if err := windowsNoReparseAncestors(root.Name()); err != nil {
		closeAll()
		return nil, err
	}
	return closeAll, nil
}

func publishPrivateFile(root *os.Root, oldName, newName string, replace bool) error {
	unpin, err := windowsPinRoot(root)
	if err != nil {
		return err
	}
	defer unpin()
	oldPath, err := windowsPrivateName(root, oldName)
	if err != nil {
		return err
	}
	newPath, err := windowsPrivateName(root, newName)
	if err != nil {
		return err
	}
	if err := checkRegularFile(root, oldName); err != nil {
		return err
	}
	if err := checkRegularFile(root, newName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	oldPtr, _ := syscall.UTF16PtrFromString(oldPath)
	newPtr, _ := syscall.UTF16PtrFromString(newPath)
	flags := uintptr(8) // MOVEFILE_WRITE_THROUGH
	if replace {
		flags |= 1
	}
	return windowsMovePrivatePath(oldPtr, newPtr, flags)
}

// Publish a fully verified, closed staging directory without overwriting an
// immutable version. Its parent is pinned throughout the same-volume move.
func publishPrivateDirectory(root *os.Root, oldName, newName string) error {
	unpin, err := windowsPinRoot(root)
	if err != nil {
		return err
	}
	defer unpin()
	oldPath, err := windowsPrivateName(root, oldName)
	if err != nil {
		return err
	}
	newPath, err := windowsPrivateName(root, newName)
	if err != nil {
		return err
	}
	if _, err := root.Lstat(newName); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := openStateRoot(oldPath, false)
	if err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	oldPtr, _ := syscall.UTF16PtrFromString(oldPath)
	newPtr, _ := syscall.UTF16PtrFromString(newPath)
	return windowsMovePrivatePath(oldPtr, newPtr, 8)
}

func windowsMovePrivatePath(oldName, newName *uint16, flags uintptr) error {
	for attempt := 0; ; attempt++ {
		ok, _, err := movePrivateFile.Call(uintptr(unsafe.Pointer(oldName)), uintptr(unsafe.Pointer(newName)), flags)
		if ok != 0 {
			return nil
		}
		// MoveFileExW can report ACCESS_DENIED while a reader retains the
		// destination, even when that handle allows FILE_SHARE_DELETE. Keep
		// the same bounded retry budget as sharing/lock violations; a genuine
		// permission denial still fails without changing ownership or the DACL.
		if attempt >= 4 || (!errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, syscall.Errno(32)) && !errors.Is(err, syscall.Errno(33))) {
			return err
		}
		time.Sleep(time.Duration(20<<attempt) * time.Millisecond)
	}
}

// Windows does not support fsync on the read-only directory handle used by
// os.Root. File data is flushed before MoveFileExW(MOVEFILE_WRITE_THROUGH).
func syncStateDirectory(root *os.Root) error { return nil }
