//go:build windows

package laodi

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"
	"unsafe"
)

// Directory junctions and file hard links can be created by an ordinary user.
// They exercise native Windows attacks without requiring symlink privileges or
// skipping the generic store/inbox tests on default Windows installations.
func createUnsafeTestLink(target, path string) error {
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		if err := os.WriteFile(target, []byte("synthetic link target"), 0600); err != nil {
			return err
		}
		return os.Link(target, path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Link(target, path)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	sub := utf16.Encode([]rune(`\??\` + target))
	print := utf16.Encode([]rune(target))
	buf := make([]byte, 16+(len(sub)+1+len(print)+1)*2)
	binary.LittleEndian.PutUint32(buf, 0xa0000003)
	binary.LittleEndian.PutUint16(buf[4:], uint16(len(buf)-8))
	binary.LittleEndian.PutUint16(buf[10:], uint16(len(sub)*2))
	binary.LittleEndian.PutUint16(buf[12:], uint16((len(sub)+1)*2))
	binary.LittleEndian.PutUint16(buf[14:], uint16(len(print)*2))
	for i, v := range sub {
		binary.LittleEndian.PutUint16(buf[16+i*2:], v)
	}
	for i, v := range print {
		binary.LittleEndian.PutUint16(buf[16+(len(sub)+1+i)*2:], v)
	}
	var returned uint32
	return syscall.DeviceIoControl(h, 0x000900a4, &buf[0], uint32(len(buf)), nil, 0, &returned, nil)
}

func assertUnsafeTestLink(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("unsafe link was removed: %v", err)
	}
	name, _ := syscall.UTF16PtrFromString(path)
	h, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		t.Fatal(err)
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0 && info.NumberOfLinks <= 1 {
		t.Fatal("unsafe link was replaced")
	}
}

func assertPrivateTestPath(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	f, err := windowsOpen(path, os.O_RDONLY, false, mode == 0700, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE)
	if err != nil {
		t.Fatalf("path does not have private Windows protection: %v", err)
	}
	f.Close()
}

func setPrivateTestPermissions(path string, mode os.FileMode) error {
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return err
	}
	sddl := "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)"
	if mode&0077 != 0 {
		sddl += "(A;OICI;GR;;;WD)"
	}
	text, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return err
	}
	var sd uintptr
	ok, _, callErr := convertSD.Call(uintptr(unsafe.Pointer(text)), 1, uintptr(unsafe.Pointer(&sd)), 0)
	if ok == 0 {
		return callErr
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	ok, _, callErr = fileAdvapi.NewProc("SetFileSecurityW").Call(uintptr(unsafe.Pointer(name)), 4|0x80000000, sd)
	if ok == 0 {
		return callErr
	}
	return nil
}

func TestWindowsPrivatePathKeepsShortAliasesRejected(t *testing.T) {
	for _, path := range []string{`C:\Users\RUNNER~1\AppData\Local\Temp\state`, `C:\PROGRA~1\Laodi\state`} {
		if _, err := windowsPrivatePath(path); err == nil {
			t.Errorf("accepted short-path alias %q", path)
		}
	}
	for _, path := range []string{`C:\Users\runneradmin\AppData\Local\Temp\state`, `C:\Users\测试用户\AppData\Local\Temp\state`} {
		if _, err := windowsPrivatePath(path); err != nil {
			t.Errorf("rejected unambiguous local path %q: %v", path, err)
		}
	}
}

func TestWindowsPrivateStateRejectsADSAndPathAliases(t *testing.T) {
	dir := privateStateDir(t)
	for _, path := range []string{dir + ":stream", dir + ".", dir + " ", `\\localhost\c$\private`, `\\?\` + dir, `C:relative`, filepath.Join(dir, "NUL"), filepath.Join(dir, "COM1.txt")} {
		if _, err := openStateRoot(path, true); err == nil {
			t.Errorf("accepted ambiguous path %q", path)
		}
	}
	root, err := openStateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"state.json:secret", "NUL", "state.json.", "../escape"} {
		if f, err := createPrivateFile(root, name, os.O_WRONLY); err == nil {
			f.Close()
			t.Errorf("accepted ambiguous name %q", name)
		}
	}
	if err := SaveState(dir, emptyState()); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(strings.ToUpper(dir)); err != nil {
		t.Fatalf("case alias lost protected identity: %v", err)
	}
}

func TestWindowsStateRejectsHardLinkedFiles(t *testing.T) {
	for _, name := range []string{stateFileName, ".lock"} {
		t.Run(name, func(t *testing.T) {
			dir := privateStateDir(t)
			if err := SaveState(dir, emptyState()); err != nil {
				t.Fatal(err)
			}
			if name == ".lock" {
				release, err := AcquireLock(dir)
				if err != nil {
					t.Fatal(err)
				}
				release()
			}
			if err := os.Link(filepath.Join(dir, name), filepath.Join(dir, "alias")); err != nil {
				t.Fatal(err)
			}
			if name == stateFileName {
				if _, err := LoadState(dir); err == nil {
					t.Fatal("read hard-linked state")
				}
				if err := SaveState(dir, emptyState()); err == nil {
					t.Fatal("replaced hard-linked state")
				}
			} else if release, err := AcquireLock(dir); err == nil {
				release()
				t.Fatal("accepted hard-linked lock")
			}
		})
	}
}

func TestWindowsLockCannotBeReplacedWhileHeld(t *testing.T) {
	dir := privateStateDir(t)
	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.Remove(filepath.Join(dir, ".lock")); err == nil {
		t.Fatal("deleted active lock identity")
	}
	if err := os.Rename(filepath.Join(dir, ".lock"), filepath.Join(dir, "old-lock")); err == nil {
		t.Fatal("renamed active lock identity")
	}
	if duplicate, err := AcquireLock(strings.ToUpper(dir)); err == nil {
		duplicate()
		t.Fatal("case alias admitted second writer")
	}
	if err := os.Rename(dir, dir+"-replaced"); err == nil {
		t.Fatal("renamed state directory while writer holds its lock")
	}
	parent := filepath.Dir(dir)
	if err := os.Rename(parent, parent+"-replaced"); err == nil {
		t.Fatal("renamed state ancestor while writer holds its lock")
	}
}

func TestWindowsPublicationDoesNotReplaceExistingWithoutPermission(t *testing.T) {
	dir := privateStateDir(t)
	root, err := openStateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for name, value := range map[string]string{"old": "old content", "new": "user content"} {
		f, err := createPrivateFile(root, name, os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.WriteString(value); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := publishPrivateFile(root, "old", "new", false); err == nil {
		t.Fatal("no-clobber publication overwrote existing file")
	}
	data, err := os.ReadFile(filepath.Join(dir, "new"))
	if err != nil || string(data) != "user content" {
		t.Fatal("existing content changed")
	}
}

func TestWindowsScannerRejectsHardLinksAddedAfterCaching(t *testing.T) {
	dir := t.TempDir()
	rel := testWorkspace + "/manifests/" + hashA + ".json"
	writeFixture(t, dir, rel, manifestFixture(".git/objects/synthetic"))
	scanner := Scanner{Root: dir, Build: KnownBuild}
	if r := scanner.Scan(); count(r, "sensitive_manifest_match") != 1 {
		t.Fatalf("fixture not scanned: %+v", r)
	}
	if err := os.Link(filepath.Join(dir, rel), filepath.Join(t.TempDir(), "alias.json")); err != nil {
		t.Fatal(err)
	}
	if r := scanner.Scan(); r.Coverage != "degraded" || len(r.Findings) != 0 {
		t.Fatalf("cached artifact bypassed native identity validation: %+v", r)
	}
}

func TestWindowsRejectsBroadDirectoryAndFileDACL(t *testing.T) {
	dir := privateStateDir(t)
	if err := SaveState(dir, emptyState()); err != nil {
		t.Fatal(err)
	}
	if err := setPrivateTestPermissions(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir); err == nil {
		t.Fatal("accepted broad directory DACL")
	}
	if err := setPrivateTestPermissions(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := setPrivateTestPermissions(filepath.Join(dir, stateFileName), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir); err == nil {
		t.Fatal("accepted broad file DACL")
	}
}

func TestWindowsStateChineseAndSpacedPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "老底 测试 ' & % ! $ (状态)")
	if err := SaveState(dir, emptyState()); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir); err != nil {
		t.Fatal(err)
	}
	if err := SubmitHookInspection(dir, inboxInspection("synthetic")); err != nil {
		t.Fatal(err)
	}
	batch, err := ReadHookInbox(dir, 1)
	if err != nil || len(batch.Receipts) != 1 {
		t.Fatalf("special-character inbox failed: %v %+v", err, batch)
	}
	if err := AckHookInbox(dir, batch.Receipts); err != nil {
		t.Fatal(err)
	}
}
