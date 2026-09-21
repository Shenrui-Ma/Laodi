//go:build windows

package laodi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// Run in a separate child because the token's default owner is process-wide.
// No privilege is enabled, no real user directory or system ACL is changed.
func TestWindowsPrivateCreationWithAdministrativeDefaultOwner(t *testing.T) {
	if os.Getenv("LAODI_ADMIN_OWNER_HELPER") == "1" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestWindowsAdministrativeOwnerHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "LAODI_ADMIN_OWNER_HELPER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("administrative default-owner regression: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "ADMIN_OWNER_UNAVAILABLE") {
		if os.Getenv("LAODI_REQUIRE_ADMIN_OWNER") == "1" {
			t.Fatalf("required administrative default-owner regression did not execute: %s", output)
		}
		t.Skip("administrative default-owner token unavailable; ordinary-user suite still required")
	}
	t.Log(string(output))
}

func TestWindowsAdministrativeOwnerHelper(t *testing.T) {
	if os.Getenv("LAODI_ADMIN_OWNER_HELPER") != "1" {
		return
	}
	process, _ := syscall.GetCurrentProcess()
	var token syscall.Token
	if err := syscall.OpenProcessToken(process, 0x0008|0x0080, &token); err != nil {
		t.Skip("ADMIN_OWNER_UNAVAILABLE")
	}
	defer token.Close()
	var size uint32
	_ = syscall.GetTokenInformation(token, 4, nil, 0, &size)
	if size == 0 {
		t.Fatal("missing original token owner")
	}
	original := make([]byte, size)
	if err := syscall.GetTokenInformation(token, 4, &original[0], size, &size); err != nil {
		t.Fatal(err)
	}
	admin, err := syscall.StringToSid("S-1-5-32-544")
	if err != nil {
		t.Fatal(err)
	}
	setOwner := fileAdvapi.NewProc("SetTokenInformation")
	ok, _, ownerError := setOwner.Call(uintptr(token), 4, uintptr(unsafe.Pointer(&admin)), unsafe.Sizeof(admin))
	if ok == 0 {
		t.Logf("default-owner test setup result: %v", ownerError)
		t.Skip("ADMIN_OWNER_UNAVAILABLE")
	}
	defer func() {
		ok, _, err := setOwner.Call(uintptr(token), 4, uintptr(unsafe.Pointer(&original[0])), unsafe.Sizeof(admin))
		if ok == 0 {
			t.Errorf("restore child token owner: %v", err)
		}
	}()
	dir := privateStateDir(t)
	// Prove the unfavorable default actually applies to an implicit creation.
	probe := filepath.Join(dir, "default-owner-probe")
	if err := os.WriteFile(probe, nil, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(probe)
	if err != nil {
		t.Fatal(err)
	}
	ownerErr := windowsCheckPrivateDACL(syscall.Handle(f.Fd()))
	f.Close()
	if ownerErr == nil || !strings.Contains(ownerErr.Error(), "owned by the current user") {
		t.Fatal("fixture did not reproduce non-user default ownership")
	}
	t.Run("receipt", func(t *testing.T) {
		path := filepath.Join(dir, "synthetic-receipt.json")
		if err := writeNewServiceFile(path, []byte(`{"synthetic":true}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := readServiceFile(path); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("state_and_hooks", func(t *testing.T) {
		if err := SaveState(dir, emptyState()); err != nil {
			t.Fatal(err)
		}
		if err := SubmitHookInspection(dir, inboxInspection("synthetic-admin-default")); err != nil {
			t.Fatal(err)
		}
		batch, err := ReadHookInbox(dir, 4)
		if err != nil || len(batch.Findings) != 1 {
			t.Fatalf("hook delivery: %v", err)
		}
	})
	t.Run("shortcut", func(t *testing.T) {
		p := windowsServiceFixture(t)
		stateRoot, err := openStateRoot(p.options.StateDir, true)
		if err != nil {
			t.Fatal(err)
		}
		stateRoot.Close()
		shortcuts := privateStateDir(t)
		p.startupDirectory = func() (string, error) { return shortcuts, nil }
		link, err := runWindowsStartup(context.Background(), "create", p, "")
		if err != nil {
			t.Fatal(err)
		}
		assertPrivateTestPath(t, link.Path, 0600)
		if _, err := runWindowsStartup(context.Background(), "delete", p, link.Hash); err != nil {
			t.Fatal(err)
		}
	})
}
