package laodi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveProtectionSummaryUsesOnlyFixedLabels(t *testing.T) {
	for _, item := range []struct {
		value ArchiveGuardStatus
		want  string
	}{
		{ArchiveGuardStatus{}, "disabled"},
		{ArchiveGuardStatus{Enabled: true, Healthy: true}, "enabled"},
		{ArchiveGuardStatus{Enabled: true}, "degraded"},
		{ArchiveGuardStatus{RecoveryNeeded: true}, "recovery_needed"},
		{ArchiveGuardStatus{Problems: []string{"/PRIVATE/secret"}}, "degraded"},
	} {
		s := SummarizeArchiveGuard(item.value)
		if s.Status != item.want {
			t.Fatalf("status=%s, want=%s", s.Status, item.want)
		}
		b, err := json.Marshal(s)
		if err != nil || strings.Contains(string(b), "PRIVATE") {
			t.Fatal("summary leaked raw error")
		}
	}
}

func TestArchiveProtectionQueriesAreReadOnlyWithoutReceipt(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, "not-created")
	s := ReadArchiveProtectionSummary(home, state, "/not-installed.app")
	if s.Status != "disabled" {
		t.Fatal(s)
	}
	report := AgentSummary{Unknowns: []string{"no_blocking"}}
	AddArchiveProtectionSummary(&report, home, state, "/not-installed.app")
	if report.Protection != nil || report.Unknowns[0] != "no_blocking" {
		t.Fatal(report)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("query created state")
	}
}

func TestArchiveProtectionRemovalPreservesRecoveryReceipt(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(dir, "archive-guard")
	if err := os.Mkdir(guard, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(guard, "receipt.json")
	if err := os.WriteFile(path, []byte("PRIVATE malformed receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if release, err := lockArchiveGuardForRemoval(dir); err == nil {
		release()
		t.Fatal("removal accepted a recovery receipt")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "PRIVATE malformed receipt" {
		t.Fatal("removal changed recovery data")
	}
}
