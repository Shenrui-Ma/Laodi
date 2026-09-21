package laodi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveProtectionSelfTestNativeDenialAndCleanup(t *testing.T) {
	f := newArchiveFixture(t, true)
	f.enable(t)
	beforeRoot, beforeWorkspace := archiveTestACL(t, f.root), archiveTestACL(t, f.workspace)
	checks := 0
	for range 2 {
		result, err := testArchiveProtection(f.home, f.state, "synthetic-client", func() error { checks++; return nil })
		if err != nil || result.Status != "passed" || !result.ClientVerified || !result.GuardHealthy || !result.UnprotectedCreate || !result.ArchiveCreateDenied || !result.MetadataReadWrite || !result.CleanupComplete || result.DeniedOperations != 1 {
			t.Fatalf("native probe failed: %+v, %v", result, err)
		}
		data, err := json.Marshal(result)
		if err != nil || strings.Contains(string(data), f.home) || strings.Contains(string(data), "synthetic-client") {
			t.Fatal("result contains raw paths or cannot serialize")
		}
	}
	if checks != 4 {
		t.Fatalf("expected before/after client verification per probe, got %d", checks)
	}
	entries, err := os.ReadDir(f.root)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(f.workspace) {
		t.Fatalf("synthetic workspace survived cleanup: %v, %v", entries, err)
	}
	entries, err = os.ReadDir(filepath.Join(f.state, "archive-guard"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "selftest-") {
			t.Fatal("control probe survived cleanup")
		}
	}
	if strings.Join(beforeRoot, "\n") != strings.Join(archiveTestACL(t, f.root), "\n") || strings.Join(beforeWorkspace, "\n") != strings.Join(archiveTestACL(t, f.workspace), "\n") {
		t.Fatal("self-test changed existing ACLs")
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveProtectionSelfTestDisabledCannotPass(t *testing.T) {
	f := newArchiveFixture(t, false)
	called := false
	result, err := testArchiveProtection(f.home, f.state, "synthetic-client", func() error { called = true; return nil })
	if err == nil || result.Status != "disabled" || called || result.ArchiveCreateDenied {
		t.Fatalf("disabled probe misreported: %+v %v", result, err)
	}
}

func TestArchiveProtectionSelfTestUnsupportedClientCannotPass(t *testing.T) {
	f := newArchiveFixture(t, false)
	f.enable(t)
	defer DisableArchiveGuard(f.home, f.state)
	result, err := TestArchiveProtection(f.home, f.state, filepath.Join(f.home, "missing.app"))
	if err == nil || result.Status != "unsupported_client" || result.ArchiveCreateDenied || strings.Contains(err.Error(), f.home) {
		t.Fatalf("unsupported client probe misreported: %+v %v", result, err)
	}
}

func TestArchiveProtectionSelfTestClientChangesAfterProbe(t *testing.T) {
	f := newArchiveFixture(t, false)
	f.enable(t)
	defer DisableArchiveGuard(f.home, f.state)
	checks := 0
	result, err := testArchiveProtection(f.home, f.state, "synthetic-client", func() error {
		checks++
		if checks == 2 {
			return errors.New("client changed")
		}
		return nil
	})
	if err == nil || result.Status == "passed" || result.ClientVerified || !result.CleanupComplete || !result.ArchiveCreateDenied {
		t.Fatalf("changed client probe misreported: %+v %v", result, err)
	}
}

func TestArchiveProtectionSelfTestRejectsLinkedHome(t *testing.T) {
	f := newArchiveFixture(t, false)
	f.enable(t)
	defer DisableArchiveGuard(f.home, f.state)
	link := filepath.Join(f.home, "linked-home")
	if err := os.Symlink(f.home, link); err != nil {
		t.Fatal(err)
	}
	result, err := testArchiveProtection(link, filepath.Join(link, "laodi-state"), "synthetic-client", func() error { return nil })
	if err == nil || result.Status == "passed" || result.ArchiveCreateDenied {
		t.Fatalf("linked home probe misreported: %+v %v", result, err)
	}
}

func TestArchiveProbeUnexpectedSuccessNeverReportsDenied(t *testing.T) {
	f := newArchiveFixture(t, false)
	root, err := os.OpenRoot(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	result, clean, err := archiveProbe(root, "abcdefabcdef", true)
	if err == nil || result.denied || !result.created || !clean {
		t.Fatalf("unprotected probe was treated as blocked: %+v clean=%v err=%v", result, clean, err)
	}
}
