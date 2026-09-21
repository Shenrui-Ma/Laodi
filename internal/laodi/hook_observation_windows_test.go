//go:build windows

package laodi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWindowsHookObservationsNormalCallbackIsPrivateAndQuiet(t *testing.T) {
	dir := privateStateDir(t)
	payload := `{"hook_event_name":"PreToolUse","session_id":"PRIVATE_SESSION_CANARY","tool_use_id":"PRIVATE_TOOL_CANARY","tool_name":"Bash","tool_input":{"command":"npm run build && git status"}}`
	in := InspectHook("claude-code", strings.NewReader(payload))
	if len(in.Signals) != 0 {
		t.Fatal("normal command produced a signal")
	}
	if err := SubmitHookInspection(dir, in); err != nil {
		t.Fatal(err)
	}
	queue, err := GetHookInboxStatus(dir)
	if err != nil || queue.Exists || queue.Pending != 0 {
		t.Fatalf("normal callback created risk queue: %+v %v", queue, err)
	}
	s := getHookObservationSummary(dir, "claude-code")
	if s.LastSample == nil || s.Status != "observed_caller_not_authenticated" {
		t.Fatalf("normal callback lost: %+v", s)
	}
	seen := map[string]bool{}
	for _, sample := range s.Samples {
		seen[sample.Category] = true
	}
	if !seen["shell_command_not_parsed"] || seen["inspection_incomplete"] {
		t.Fatalf("complex command confused with observer failure: %+v", s)
	}
	path := filepath.Join(dir, hookObservationDirectory, "claude-code.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE_SESSION_CANARY", "PRIVATE_TOOL_CANARY", "npm", "git status", dir} {
		if bytes.Contains(before, []byte(private)) {
			t.Fatal("private input persisted")
		}
	}
	for i := 0; i < 10; i++ {
		_ = SubmitHookInspection(dir, in)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("repeated callback bypassed rate limit")
	}
	failed := InspectHook("claude-code", strings.NewReader(`{"hook_event_name":12}`))
	if err := SubmitHookInspection(dir, failed); err != nil {
		t.Fatal(err)
	}
	s = getHookObservationSummary(dir, "claude-code")
	seen = map[string]bool{}
	for _, sample := range s.Samples {
		seen[sample.Category] = true
	}
	if !seen["inspection_incomplete"] {
		t.Fatal("observer failure not distinguished")
	}
}

func TestWindowsHookObservationsFailureDoesNotLoseRisk(t *testing.T) {
	dir := privateStateDir(t)
	parent, err := openStateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = privateDirectory(parent, hookObservationDirectory); err != nil {
		t.Fatal(err)
	}
	parent.Close()
	path := filepath.Join(dir, hookObservationDirectory)
	if err := writeWindowsBytes(path, "claude-code.json", []byte(`{"schema":1,"samples":{"PRIVATE_HOSTILE_FIELD":"2026-01-01T00:00:00Z"}}`), true); err != nil {
		t.Fatal(err)
	}
	in := InspectHook("claude-code", strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":".env"}}`))
	if err := SubmitHookInspection(dir, in); err != nil {
		t.Fatal(err)
	}
	batch, err := ReadHookInbox(dir, 8)
	if err != nil || len(batch.Findings) != 1 {
		t.Fatalf("telemetry failure damaged risk delivery: %v %+v", err, batch)
	}
	s := getHookObservationSummary(dir, "claude-code")
	data, _ := json.Marshal(s)
	if s.Status != "unavailable" || bytes.Contains(data, []byte("PRIVATE_HOSTILE_FIELD")) {
		t.Fatal("corrupt metadata was trusted or disclosed")
	}
}

func TestWindowsHookObservationsConcurrentAndBounded(t *testing.T) {
	dir := privateStateDir(t)
	in := HookInspection{Adapter: "claude-code", Unknowns: []string{"tool_not_observed", "arbitrary-private-value"}}
	recordHookObservation(dir, in)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); recordHookObservation(dir, in) }()
	}
	wg.Wait()
	s := getHookObservationSummary(dir, "claude-code")
	if len(s.Samples) != 2 || s.LastSample == nil {
		t.Fatalf("unbounded or lost categories: %+v", s)
	}
	entries, err := os.ReadDir(filepath.Join(dir, hookObservationDirectory))
	if err != nil || len(entries) != 2 {
		t.Fatalf("unbounded metadata files: %d %v", len(entries), err)
	}
	for _, sample := range s.Samples {
		if sample.At.After(time.Now()) {
			t.Fatal("future timestamp")
		}
	}
}

func TestWindowsMonitoringMissingStateIsReadOnlyAndNotProtected(t *testing.T) {
	dir := filepath.Join(privateStateDir(t), "not-created")
	s := AgentSummary{}
	AddMonitoringSummary(&s, dir)
	if s.Monitoring == nil || s.Monitoring.Background == "verified_running" {
		t.Fatal("missing state shown as running")
	}
	for _, client := range s.Monitoring.Clients {
		if client.Contract != "not_configured" || client.Hooks != "not_configured" || client.Callback.Status != "not_recorded" {
			t.Fatalf("missing installation treated as protection: %+v", client)
		}
	}
	if s.Monitoring.Notifications.Authorization != "not_registered" || s.Monitoring.Notifications.Visibility != "unconfirmed" {
		t.Fatal("notification coverage overclaimed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("status created state")
	}
}

func TestWindowsMonitoringChangedClientDoesNotInheritContract(t *testing.T) {
	dir := privateStateDir(t)
	client := WindowsHookContract{Adapter: "claude-code", Executable: filepath.Join(dir, "missing-client.exe"), Version: "2.1.278", SHA256: claudeWindowsHookSHA256}
	if err := writeWindowsRecord(dir, windowsIntegrationName, windowsIntegration{Schema: 1, Home: dir, Clients: []WindowsHookContract{client}}); err != nil {
		t.Fatal(err)
	}
	recordHookObservation(dir, HookInspection{Adapter: "claude-code"})
	s := AgentSummary{}
	AddMonitoringSummary(&s, dir)
	current := s.Monitoring.Clients[1]
	if current.Contract != "changed_or_unverified" || current.Hooks == "configured" || current.Callback.LastSample == nil {
		t.Fatalf("historical callback masked current mismatch: %+v", current)
	}
}
