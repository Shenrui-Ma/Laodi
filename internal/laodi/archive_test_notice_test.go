package laodi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func passedArchiveTest() ArchiveProtectionTestResult {
	return ArchiveProtectionTestResult{Status: "passed", ClientVerified: true, GuardHealthy: true,
		UnprotectedCreate: true, ArchiveCreateDenied: true, MetadataReadWrite: true,
		CleanupComplete: true, DeniedOperations: 1}
}

func TestArchiveTestNoticeRequiresAllEvidence(t *testing.T) {
	for _, invalidate := range []func(*ArchiveProtectionTestResult){
		func(r *ArchiveProtectionTestResult) { r.Status = "failed" },
		func(r *ArchiveProtectionTestResult) { r.ClientVerified = false },
		func(r *ArchiveProtectionTestResult) { r.GuardHealthy = false },
		func(r *ArchiveProtectionTestResult) { r.UnprotectedCreate = false },
		func(r *ArchiveProtectionTestResult) { r.ArchiveCreateDenied = false },
		func(r *ArchiveProtectionTestResult) { r.MetadataReadWrite = false },
		func(r *ArchiveProtectionTestResult) { r.CleanupComplete = false },
		func(r *ArchiveProtectionTestResult) { r.DeniedOperations = 0 },
	} {
		result := passedArchiveTest()
		invalidate(&result)
		if got := NotifyArchiveProtectionTest(context.Background(), "/nonexistent/helper", result); got != "test_not_passed" {
			t.Fatalf("incomplete probe reached helper: %s", got)
		}
	}
}

func TestArchiveTestNoticeUsesSyntheticKindWithoutIncidentWrite(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "args")
	helper := syntheticNotifier(t, calls, "args")
	if got := NotifyArchiveProtectionTest(context.Background(), helper, passedArchiveTest()); got != "accepted_by_os" {
		t.Fatal(got)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(args) != 5 || args[0] != "--send" || args[1] != "--id" ||
		!strings.HasPrefix(args[2], "archive_test_") || args[3] != "--kind" || args[4] != "archive-blocked-test" {
		t.Fatalf("unexpected notification protocol: %q", data)
	}
	if got := NotifyArchiveProtectionTest(context.Background(), "", passedArchiveTest()); got != "not_configured" {
		t.Fatal(got)
	}
}
