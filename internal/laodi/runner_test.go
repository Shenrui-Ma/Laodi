package laodi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchKeepsBaselineQuietAndPersistsChanges(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "state")
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture(".git/objects/old"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, &Scanner{Root: root, Build: KnownBuild}, WatchOptions{StateDir: data, Interval: time.Second})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("watcher did not stop")
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, e := LoadState(data)
		if e == nil && st.Initialized {
			if len(st.Events) != 1 || !st.Events[0].BaselineExisting {
				t.Fatal(st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("baseline timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeFixture(t, root, testWorkspace+"/manifests/"+hashB+".json", manifestFixture(".git/objects/new"))
	deadline = time.Now().Add(3 * time.Second)
	for {
		st, e := LoadState(data)
		if e != nil {
			t.Fatal(e)
		}
		if len(st.Events) == 2 {
			if st.Events[1].BaselineExisting || st.Events[1].Notification != "not_configured" {
				t.Fatal(st.Events[1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new event timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSendNoticeUsesFixedArgumentsAndUnderstandsDelivery(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "argv.json")
	helper := syntheticNotifier(t, out, "args")
	status := sendNotice(context.Background(), helper, Event{ID: "abc123", Kind: "sensitive_manifest_match"})
	if status != "accepted_by_os" {
		t.Fatal(status)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "--send\n--id\nabc123\n--kind\nsnapshot-history\n" {
		t.Fatal(string(b))
	}
	if !json.Valid([]byte(`{"delivery":"accepted_by_os"}`)) {
		t.Fatal("invalid fixture")
	}
}

func TestExtraConfigurationNoticeUsesOnlyFixedMetadata(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	helper := syntheticNotifier(t, out, "args")
	for kind, want := range map[string]string{
		"global_config_manifest_match": "snapshot-config", "global_config_upload_attempt_recorded": "config-upload-attempt", "global_config_upload_acceptance_recorded": "config-upload-accepted",
		"workspace_snapshot_manifest": "snapshot-workspace", "workspace_snapshot_upload_attempt_recorded": "workspace-upload-attempt", "workspace_snapshot_upload_acceptance_recorded": "workspace-upload-accepted",
	} {
		t.Run(kind, func(t *testing.T) {
			if got := sendNotice(context.Background(), helper, Event{ID: "opaque123", Kind: kind, Evidence: "PRIVATE/config/path"}); got != "accepted_by_os" {
				t.Fatal(got)
			}
			b, e := os.ReadFile(out)
			if e != nil || string(b) != "--send\n--id\nopaque123\n--kind\n"+want+"\n" {
				t.Fatalf("unexpected helper arguments %q %v", b, e)
			}
		})
	}
}

func TestWatchIncompleteBaselineDoesNotHideNewValidEvidence(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "state")
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture("src/a.go"))
	if e := os.WriteFile(filepath.Join(root, testWorkspace, "manifests", hashA+".json"), []byte(`{"schema":`), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, &Scanner{Root: root, Build: KnownBuild}, WatchOptions{StateDir: data, Interval: time.Second})
	}()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	until := time.Now().Add(3 * time.Second)
	for {
		st, _ := LoadState(data)
		if st.Initialized {
			break
		}
		if time.Now().After(until) {
			t.Fatal("partial baseline did not initialize")
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeFixture(t, root, testWorkspace+"/manifests/"+hashB+".json", manifestFixture(".git/objects/new"))
	until = time.Now().Add(3 * time.Second)
	for {
		st, _ := LoadState(data)
		for _, e := range st.Events {
			if e.Kind == "sensitive_manifest_match" && !e.BaselineExisting {
				return
			}
		}
		if time.Now().After(until) {
			t.Fatal("valid new evidence hidden by corrupt old evidence")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestQueuedNotificationIsRecoveredOnce(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "state")
	calls := filepath.Join(t.TempDir(), "calls")
	helper := syntheticNotifier(t, calls, "calls")
	st := emptyState()
	_, e := ApplyReport(&st, Report{SchemaVersion: 1, Coverage: "observing"}, RootID(root), time.Now())
	if e != nil {
		t.Fatal(e)
	}
	_, e = ApplyReport(&st, Report{SchemaVersion: 1, Coverage: "observing", Findings: []Finding{{Key: "test:snapshot:history", Kind: "sensitive_manifest_match", EvidenceHash: "test"}}}, RootID(root), time.Now())
	if e != nil {
		t.Fatal(e)
	}
	st.Events[0].Notification = "queued"
	if e = SaveState(data, st); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = Watch(context.Background(), &Scanner{Root: root, Build: KnownBuild}, WatchOptions{StateDir: data, Interval: time.Second, Duration: 1100 * time.Millisecond, Notifier: helper}); e != nil {
			t.Fatal(e)
		}
	}
	b, e := os.ReadFile(calls)
	if e != nil || string(b) != "called\n" {
		t.Fatalf("notification recovery not exactly once: %q %v", b, e)
	}
	st, e = LoadState(data)
	if e != nil || st.Events[0].Notification != "accepted_by_os" {
		t.Fatalf("delivery status missing: %+v %v", st, e)
	}
}

func TestPersistentCoverageFailureCreatesLimitedEvent(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "state")
	err := Watch(context.Background(), &Scanner{Root: root, Build: "unknown"}, WatchOptions{StateDir: data, Interval: time.Second, Duration: 2200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Events) != 1 || st.Events[0].Kind != "coverage_degraded" || len(st.Diagnostics) == 0 {
		t.Fatalf("failure not persisted: %+v", st)
	}
}

func TestCoverageRecoveryAllowsAnotherFailureEpisode(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "state")
	rel := testWorkspace + "/manifests/" + hashA + ".json"
	writeFixture(t, root, rel, manifestFixture("src/a.go"))
	p := filepath.Join(root, rel)
	broken := func() {
		t.Helper()
		if e := os.WriteFile(p, []byte(`{"schema":`), 0600); e != nil {
			t.Fatal(e)
		}
	}
	broken()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, &Scanner{Root: root, Build: KnownBuild}, WatchOptions{StateDir: data, Interval: time.Second})
	}()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	wait := func(pred func(State) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			st, e := LoadState(data)
			if e == nil && pred(st) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("coverage transition timeout: %+v %v", st, e)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	failures := func(st State) int {
		n := 0
		for _, e := range st.Events {
			if e.Kind == "coverage_degraded" {
				n++
			}
		}
		return n
	}
	wait(func(st State) bool { return failures(st) == 1 })
	writeFixture(t, root, rel, manifestFixture("src/a.go"))
	wait(func(st State) bool { return st.Coverage == "observing" })
	broken()
	wait(func(st State) bool { return failures(st) == 2 })
}

func TestWatchSnapshotCapacityKeepsHooksAndKnownSnapshotsRunning(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(t.TempDir(), "state")
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture(".git/objects/known"))
	writeFixture(t, root, testWorkspace+"/manifests/"+hashB+".json", manifestFixture(".git/objects/new"))
	knownKey := testWorkspace + ":" + hashA + ":history"
	newKey := testWorkspace + ":" + hashB + ":history"
	state := emptyState()
	state.Initialized, state.RootID, state.Parser = true, RootID(root), ParserID
	state.WorkspaceSnapshotGeneration = workspaceSnapshotGeneration
	state.Seen[knownKey] = "earlier-classification"
	for i := 1; i < maxSeen; i++ {
		state.Seen[fmt.Sprintf("retained-snapshot-%d", i)] = "hash"
	}
	if err := SaveState(data, state); err != nil {
		t.Fatal(err)
	}
	if err := SubmitHookInspection(data, runtimeHook(t, "claude-code", "PostToolUse", "synthetic-capacity-hook")); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(t.TempDir(), "calls")
	helper := syntheticNotifier(t, calls, "append-args")
	for _, duration := range []time.Duration{1100 * time.Millisecond, 100 * time.Millisecond} {
		if err := Watch(context.Background(), &Scanner{Root: root, Build: KnownBuild}, WatchOptions{StateDir: data, Interval: time.Second, Duration: duration, Notifier: helper}); err != nil {
			t.Fatalf("full snapshot store stopped the monitor: %v", err)
		}
	}
	state, err := LoadState(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Seen) != maxSeen || state.Seen[knownKey] != digest(hashA+":history") || state.Seen[newKey] != "" {
		t.Fatal("known cursor did not advance or overflow silently changed snapshot history")
	}
	if state.Coverage != "degraded" || !hasStoreDiagnostic(state.Diagnostics, snapshotCapacityDiagnostic) || len(state.HookSeen) != 1 {
		t.Fatalf("capacity gap or independent hook cursor missing: coverage=%s diagnostics=%v hooks=%d", state.Coverage, state.Diagnostics, len(state.HookSeen))
	}
	capacityEvents, hookEvents := 0, 0
	for _, event := range state.Events {
		switch event.Kind {
		case "monitor_capacity_degraded":
			capacityEvents++
			if event.Notification != "accepted_by_os" || event.BaselineExisting {
				t.Fatalf("capacity alert was not eligible for notification: %+v", event)
			}
		case "sensitive_tool_output_detected":
			hookEvents++
		}
	}
	if capacityEvents != 1 || hookEvents != 1 {
		t.Fatalf("capacity alert repeated or hook was lost: capacity=%d hooks=%d", capacityEvents, hookEvents)
	}
	status, err := GetHookInboxStatus(data)
	if err != nil || status.Pending != 0 {
		t.Fatalf("persisted hook was not acknowledged after capacity exhaustion: %+v %v", status, err)
	}
	output, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(output), "coverage-degraded") != 1 || strings.Count(string(output), "tool-output-sensitive") != 1 {
		t.Fatalf("capacity notice or hook notice missing/repeated: %q %v", output, err)
	}
}
