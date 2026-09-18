package laodi

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func privateStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func storeReport(findings ...Finding) Report {
	return Report{SchemaVersion: SchemaVersion, Parser: ParserID, Coverage: "observing", Findings: findings}
}

func storeFinding(key, hash string) Finding {
	return Finding{Key: key, Kind: "snapshot", Evidence: "manifest", EvidenceHash: hash, Counts: map[string]int{"files": 1}, Unknowns: []string{"upload_success"}}
}

func TestStoreBaselineChangesAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("read-only load created missing state directory")
	}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	finding := storeFinding("archive-one", "manifest-v1")
	baseline := storeReport(finding)
	events, err := ApplyReport(&state, baseline, "root-one", now)
	if err != nil || len(events) != 0 {
		t.Fatalf("baseline notified: events=%v err=%v", events, err)
	}
	if len(state.Events) != 1 || !state.Events[0].BaselineExisting || state.Events[0].Notification != "suppressed_baseline" {
		t.Fatalf("missing quiet baseline: %+v", state.Events)
	}
	// State owns its evidence summary rather than aliasing mutable scanner maps.
	finding.Counts["files"] = 99
	finding.Unknowns[0] = "changed"
	if state.Events[0].Counts["files"] != 1 || state.Events[0].Unknowns[0] != "upload_success" {
		t.Fatal("event aliases scanner data")
	}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, stateFileName): 0600} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("wrong private permissions on %s: %v, %v", name, info, err)
		}
	}
	state, err = LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	events, err = ApplyReport(&state, baseline, "root-one", now.Add(time.Second))
	if err != nil || len(events) != 0 || len(state.Events) != 1 {
		t.Fatalf("restart duplicated unchanged evidence: events=%v err=%v", events, err)
	}
	changed := storeFinding("archive-one", "accepted-v2")
	changed.Kind = "accepted"
	events, err = ApplyReport(&state, storeReport(changed, storeFinding("archive-two", "manifest-v1")), "root-one", now.Add(2*time.Second))
	if err != nil || len(events) != 2 {
		t.Fatalf("changed/new evidence not returned: events=%v err=%v", events, err)
	}
	if events[0].Kind != "accepted" || events[0].BaselineExisting || events[0].Notification != "pending" {
		t.Fatalf("wrong update event: %+v", events[0])
	}
	if events[0].ID == state.Events[0].ID || events[0].ID == events[1].ID {
		t.Fatal("event IDs collided")
	}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil || !reflect.DeepEqual(state, loaded) {
		t.Fatalf("state did not round-trip: err=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != stateFileName {
		t.Fatalf("temporary files retained: %v %v", entries, err)
	}
}

func TestStoreRetentionPreservesSeenAndCapacityDegradesWithoutEviction(t *testing.T) {
	state := emptyState()
	now := time.Now()
	findings := make([]Finding, maxSeen)
	for i := range findings {
		findings[i] = storeFinding(fmt.Sprintf("archive-%d", i), "hash")
	}
	if _, err := ApplyReport(&state, storeReport(findings...), "root", now); err != nil {
		t.Fatal(err)
	}
	if len(state.Seen) != maxSeen || len(state.Events) != maxEvents || state.DroppedEvents != maxSeen-maxEvents {
		t.Fatalf("incorrect retention: seen=%d events=%d dropped=%d", len(state.Seen), len(state.Events), state.DroppedEvents)
	}
	dir := privateStateDir(t)
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if events, err := ApplyReport(&state, storeReport(findings[0]), "root", now.Add(time.Second)); err != nil || len(events) != 0 {
		t.Fatalf("evicted event lost dedup cursor: %v %v", events, err)
	}
	events, err := ApplyReport(&state, storeReport(storeFinding("archive-0", "changed"), storeFinding("overflow", "hash")), "root", now.Add(2*time.Second))
	if err != nil || len(events) != 2 || events[0].Kind != "monitor_capacity_degraded" || state.Coverage != "degraded" || !hasStoreDiagnostic(state.Diagnostics, snapshotCapacityDiagnostic) {
		t.Fatalf("capacity gap was not exposed without stopping updates: %v %v", events, err)
	}
	if len(state.Seen) != maxSeen || state.Seen["archive-0"] != "changed" || state.Seen["overflow"] != "" {
		t.Fatal("full snapshot store evicted old history or claimed to track overflow")
	}
	if events, err := ApplyReport(&state, storeReport(storeFinding("archive-0", "changed-again")), "root", now.Add(3*time.Second)); err != nil || len(events) != 1 || state.Coverage != "degraded" {
		t.Fatalf("existing key cannot advance at capacity: %v %v", events, err)
	}
}

func TestStoreRejectsInvalidInputWithoutMutation(t *testing.T) {
	state := emptyState()
	if _, err := ApplyReport(&state, storeReport(storeFinding("a", "v1")), "root", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		report Report
		root   string
	}{
		{"root changed", storeReport(storeFinding("a", "v2")), "different-root"},
		{"root empty", storeReport(), ""},
		{"report schema", Report{SchemaVersion: SchemaVersion + 1}, "root"},
		{"empty key", storeReport(storeFinding("", "v2")), "root"},
		{"empty hash", storeReport(storeFinding("a", "")), "root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := state
			if _, err := ApplyReport(&state, tc.report, tc.root, time.Now()); err == nil {
				t.Fatal("accepted invalid input")
			}
			if !reflect.DeepEqual(before, state) {
				t.Fatal("error changed state")
			}
		})
	}
}

func TestStoreRejectsMalformedOrOversizedFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"broken", "{"},
		{"null", "null"},
		{"unknown schema", `{"schema_version":2}`},
		{"missing schema", `{}`},
		{"unknown field", `{"schema_version":1,"surprise":true}`},
		{"trailing JSON", `{"schema_version":1} {}`},
		{"missing root", `{"schema_version":1,"initialized":true}`},
		{"history without initialization", `{"schema_version":1,"seen":{"a":"b"}}`},
		{"oversize", strings.Repeat(" ", maxStateBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := privateStateDir(t)
			path := filepath.Join(dir, stateFileName)
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadState(dir); err == nil {
				t.Fatal("accepted malformed state")
			}
			contents, err := os.ReadFile(path)
			if err != nil || string(contents) != tc.data {
				t.Fatal("load modified broken state")
			}
		})
	}
	state := emptyState()
	state.Coverage = strings.Repeat("x", maxStateBytes)
	dir := privateStateDir(t)
	if err := SaveState(dir, state); err == nil {
		t.Fatal("accepted oversized encoded state")
	}
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); !os.IsNotExist(err) {
		t.Fatal("oversized state was persisted")
	}
}

func TestStoreRejectsSymlinksAndUnsafePermissions(t *testing.T) {
	target := privateStateDir(t)
	if err := SaveState(target, emptyState()); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "linked-state-dir")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(alias); err == nil {
		t.Fatal("loaded through state directory symlink")
	}
	if err := SaveState(alias, emptyState()); err == nil {
		t.Fatal("saved through state directory symlink")
	}
	dir := privateStateDir(t)
	path := filepath.Join(dir, stateFileName)
	if err := os.Symlink(filepath.Join(target, stateFileName), path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir); err == nil {
		t.Fatal("loaded through state file symlink")
	}
	if err := SaveState(dir, emptyState()); err == nil {
		t.Fatal("replaced state file symlink")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was changed")
	}
	if err := os.Chmod(target, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(target); err == nil {
		t.Fatal("accepted broadly readable state directory")
	}
	if err := os.Chmod(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(target, stateFileName), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(target); err == nil {
		t.Fatal("accepted broadly readable state file")
	}
}

func TestStateLockAcrossProcessesAndExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background backend intentionally unsupported")
	}
	dir := privateStateDir(t)
	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if duplicate, err := AcquireLock(dir); err == nil {
		duplicate()
		t.Fatal("admitted concurrent writer in same process")
	}
	runLockHelper(t, dir, "expect-locked")
	release()
	release() // release must be safe for both explicit and deferred cleanup.
	runLockHelper(t, dir, "exit-with-lock")
	releaseAgain, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("process exit left stale lock: %v", err)
	}
	releaseAgain()
	if info, err := os.Stat(filepath.Join(dir, ".lock")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("lock file not retained privately")
	}
	if err := os.Remove(filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireLock(dir); err == nil {
		release()
		t.Fatal("followed symbolic lock file")
	}
}

func runLockHelper(t *testing.T, dir, mode string) {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(program, "-test.run=^TestStateLockHelper$")
	cmd.Env = append(os.Environ(), "LAODI_TEST_LOCK_DIR="+dir, "LAODI_TEST_LOCK_MODE="+mode)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock subprocess %s failed: %v\n%s", mode, err, output)
	}
}

func TestStateLockHelper(t *testing.T) {
	mode := os.Getenv("LAODI_TEST_LOCK_MODE")
	if mode == "" {
		return
	}
	release, err := AcquireLock(os.Getenv("LAODI_TEST_LOCK_DIR"))
	if mode == "expect-locked" {
		if err == nil {
			release()
			t.Fatal("another process acquired held lock")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass release and test defers; the kernel must release it.
	os.Exit(0)
}

func workspaceStoreFinding(key string) Finding {
	finding := storeFinding(key, key+"-hash")
	finding.Kind = "workspace_snapshot_upload_acceptance_recorded"
	return finding
}

func hasStoreUnknown(event Event, unknown string) bool {
	for _, value := range event.Unknowns {
		if value == unknown {
			return true
		}
	}
	return false
}

func TestStoreWorkspaceUpgradeBaselinesOnlyNewFamilyAndSurvivesRestart(t *testing.T) {
	for _, oldParser := range []string{"", "zcode-3.12.3.7463-v2"} {
		t.Run("parser="+oldParser, func(t *testing.T) {
			state := emptyState()
			state.Initialized, state.RootID, state.Parser = true, "root", oldParser
			state.Seen["old-git"] = "stable-git"
			state.Seen["old-extra"] = "stable-extra"
			oldGit := storeFinding("old-git", "stable-git")
			oldGit.Kind = "sensitive_manifest_match"
			oldExtra := storeFinding("old-extra", "stable-extra")
			oldExtra.Kind = "global_config_manifest_match"
			newGit := storeFinding("new-git", "new-git-hash")
			newGit.Kind = "upload_acceptance_recorded"
			ordinary := workspaceStoreFinding("old-ordinary-accepted")
			now := time.Now().UTC()
			events, err := ApplyReport(&state, storeReport(oldGit, oldExtra, ordinary, newGit), "root", now)
			if err != nil || len(events) != 1 || events[0].Key != "new-git" {
				t.Fatalf("upgrade changed old-family notification semantics: %v %v", events, err)
			}
			if state.Parser != ParserID || state.WorkspaceSnapshotGeneration != workspaceSnapshotGeneration || state.WorkspaceBaselineIncomplete {
				t.Fatalf("classification generation did not advance: %+v", state)
			}
			if len(state.Events) != 2 || !state.Events[0].BaselineExisting || state.Events[0].Notification != "suppressed_upgrade_baseline" {
				t.Fatalf("old ordinary evidence was not quietly baselined: %+v", state.Events)
			}
			if !hasStoreUnknown(state.Events[0], "first_observed_after_parser_upgrade") || !hasStoreUnknown(state.Events[0], "existence_before_initial_install_unknown") {
				t.Fatal("upgrade baseline falsely implies an original-install time boundary")
			}
			dir := privateStateDir(t)
			if err := SaveState(dir, state); err != nil {
				t.Fatal(err)
			}
			state, err = LoadState(dir)
			if err != nil {
				t.Fatal(err)
			}
			events, err = ApplyReport(&state, storeReport(ordinary, workspaceStoreFinding("new-ordinary-accepted")), "root", now.Add(time.Second))
			if err != nil || len(events) != 1 || events[0].Key != "new-ordinary-accepted" || events[0].BaselineExisting || events[0].Notification != "pending" {
				t.Fatalf("restart repeated migration or hid a new ordinary snapshot: %v %v", events, err)
			}
			if state.Seen["old-git"] != "stable-git" || state.Seen["old-extra"] != "stable-extra" {
				t.Fatal("migration discarded existing family cursors")
			}
		})
	}
}

func TestStoreWorkspaceUpgradeWaitsForValidScan(t *testing.T) {
	state := emptyState()
	state.Initialized, state.RootID, state.Parser = true, "root", "zcode-3.12.3.7463-v2"
	now := time.Now().UTC()
	for _, coverage := range []string{"unsupported_build", "degraded"} {
		sensor := storeFinding("sensor:coverage", coverage)
		sensor.Kind = "coverage_degraded"
		report := storeReport(sensor)
		report.Coverage = coverage
		if _, err := ApplyReport(&state, report, "root", now); err != nil {
			t.Fatal(err)
		}
		if state.WorkspaceSnapshotGeneration != 0 || state.Parser != "zcode-3.12.3.7463-v2" {
			t.Fatal("sensor-only failure consumed parser migration")
		}
	}
	dir := privateStateDir(t)
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	events, err := ApplyReport(&state, storeReport(workspaceStoreFinding("existing-ordinary")), "root", now.Add(time.Second))
	if err != nil || len(events) != 0 || !state.Events[len(state.Events)-1].BaselineExisting {
		t.Fatalf("first usable scan after failure alerted on existing evidence: %v %v", events, err)
	}
}

func TestStorePartialWorkspaceUpgradeNeverSilencesLaterFindings(t *testing.T) {
	state := emptyState()
	state.Initialized, state.RootID = true, "root"
	now := time.Now().UTC()
	first := storeReport(workspaceStoreFinding("first-readable"))
	first.Coverage = "degraded"
	first.Diagnostics = []Diagnostic{{Code: "artifact_budget_exceeded"}}
	events, err := ApplyReport(&state, first, "root", now)
	if err != nil || len(events) != 0 || !state.WorkspaceBaselineIncomplete || !hasStoreUnknown(state.Events[0], "parser_upgrade_scan_incomplete") {
		t.Fatalf("partial initial upgrade baseline failed: %v %v", events, err)
	}
	dir := privateStateDir(t)
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	state, err = LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		report := storeReport(workspaceStoreFinding(fmt.Sprintf("later-%d", round)))
		if round < 3 {
			report.Coverage = "degraded"
		}
		events, err := ApplyReport(&state, report, "root", now.Add(time.Duration(round)*time.Second))
		if err != nil || len(events) != 1 || events[0].BaselineExisting || !hasStoreUnknown(events[0], "first_observed_after_incomplete_parser_upgrade") || !hasStoreUnknown(events[0], "existence_at_parser_upgrade_unknown") {
			t.Fatalf("partial-upgrade discovery was hidden or given a false time boundary: %v %v", events, err)
		}
	}
	if state.WorkspaceBaselineIncomplete {
		t.Fatal("complete scan did not close the partial-baseline interval")
	}
	events, err = ApplyReport(&state, storeReport(workspaceStoreFinding("after-complete")), "root", now.Add(4*time.Second))
	if err != nil || len(events) != 1 || hasStoreUnknown(events[0], "first_observed_after_incomplete_parser_upgrade") {
		t.Fatalf("completed baseline uncertainty never converged: %v %v", events, err)
	}
}

func toolStoreFinding(index int) Finding {
	kinds := []string{"sensitive_tool_access_requested", "sensitive_tool_output_detected", "hook_coverage_degraded"}
	return Finding{
		Key: fmt.Sprintf("hook:test:%06d", index), Kind: kinds[index%len(kinds)],
		Evidence: "synthetic_hook", EvidenceHash: fmt.Sprintf("hash-%d", index),
		Unknowns: []string{"upload_not_observed"},
	}
}

func toolStoreReport(findings ...Finding) Report {
	return Report{SchemaVersion: SchemaVersion, Parser: HookParserID, Coverage: "hook_inbox_ready", Findings: findings}
}

func TestStoreHookWindowDoesNotExhaustSnapshotBudget(t *testing.T) {
	state := emptyState()
	state.Initialized, state.RootID = true, "root"
	state.Seen["snapshot-key"] = "snapshot-hash"
	findings := make([]Finding, maxSeen+100)
	for i := range findings {
		findings[i] = toolStoreFinding(i)
	}
	now := time.Now().UTC()
	events, err := ApplyReport(&state, toolStoreReport(findings...), "root", now)
	if err != nil || len(events) != len(findings) {
		t.Fatalf("high-volume hook stream stopped monitoring: events=%d err=%v", len(events), err)
	}
	if len(state.Seen) != 1 || state.Seen["snapshot-key"] != "snapshot-hash" {
		t.Fatal("hook stream consumed or changed snapshot cursors")
	}
	if len(state.HookSeen) != maxHookSeen || len(state.HookSeenOrder) != maxHookSeen || state.HookSeenOrder[0] != findings[len(findings)-maxHookSeen].Key {
		t.Fatal("hook cursor window is not bounded to recent keys")
	}
	if len(state.Events) != maxEvents || state.DroppedEvents != len(findings)-maxEvents {
		t.Fatal("hook event retention became unbounded")
	}
	dir := privateStateDir(t)
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil || !reflect.DeepEqual(loaded.HookSeen, state.HookSeen) || !reflect.DeepEqual(loaded.HookSeenOrder, state.HookSeenOrder) {
		t.Fatalf("restart lost hook deduplication window: %v", err)
	}
	events, err = ApplyReport(&loaded, toolStoreReport(findings[len(findings)-3:]...), "root", now.Add(time.Second))
	if err != nil || len(events) != 0 {
		t.Fatalf("within-window crash replay duplicated events: %v %v", events, err)
	}
	events, err = ApplyReport(&loaded, toolStoreReport(findings[0]), "root", now.Add(2*time.Second))
	if err != nil || len(events) != 1 || events[0].Key != findings[0].Key {
		t.Fatalf("outside-window replay should be observable again: %v %v", events, err)
	}
	if len(loaded.HookSeen) != maxHookSeen || loaded.HookSeenOrder[len(loaded.HookSeenOrder)-1] != findings[0].Key || len(loaded.Seen) != 1 {
		t.Fatal("old replay broke window or snapshot isolation")
	}
}

func TestStoreHookUpdatesAreAtomicWhenInputFailsAfterSnapshotOverflow(t *testing.T) {
	state := emptyState()
	state.Initialized, state.RootID = true, "root"
	for i := 0; i < maxSeen; i++ {
		state.Seen[fmt.Sprintf("snapshot-%d", i)] = "hash"
	}
	if events, err := ApplyReport(&state, toolStoreReport(toolStoreFinding(0), toolStoreFinding(1)), "root", time.Now()); err != nil || len(events) != 2 {
		t.Fatalf("full snapshot budget blocked independent hooks: %v %v", events, err)
	}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	changed := toolStoreFinding(0)
	changed.EvidenceHash = "changed"
	_, err = ApplyReport(&state, toolStoreReport(changed, toolStoreFinding(2), storeFinding("new-snapshot", "new-hash"), storeFinding("invalid-hash", "")), "root", time.Now())
	if err == nil {
		t.Fatal("invalid evidence was accepted after snapshot overflow")
	}
	after, err := json.Marshal(state)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed report aliased or partially changed hook map/order")
	}
	events, err := ApplyReport(&state, toolStoreReport(changed), "root", time.Now())
	if err != nil || len(events) != 1 || state.HookSeen[changed.Key] != "changed" || state.HookSeenOrder[len(state.HookSeenOrder)-1] != changed.Key {
		t.Fatalf("changed hook did not advance recent-key order: %v %v", events, err)
	}
}

func TestStoreSnapshotCapacityGapSurvivesEventEvictionAndRestart(t *testing.T) {
	state := emptyState()
	findings := make([]Finding, maxSeen+20)
	for i := range findings {
		findings[i] = storeFinding(fmt.Sprintf("snapshot-%d", i), "hash")
	}
	findings = append(findings, toolStoreFinding(0))
	now := time.Now().UTC()
	events, err := ApplyReport(&state, storeReport(findings...), "root", now)
	if err != nil || len(events) != 2 || events[0].Kind != "monitor_capacity_degraded" || events[0].BaselineExisting || !isToolHookKind(events[1].Kind) {
		t.Fatalf("initial overflow silenced health or hook evidence: %v %v", events, err)
	}
	if len(state.Seen) != maxSeen || len(state.HookSeen) != 1 || len(state.Events) != maxEvents || state.Coverage != "degraded" {
		t.Fatal("overflow did not retain bounded independent stores")
	}
	for _, event := range state.Events {
		if event.Key == fmt.Sprintf("snapshot-%d", maxSeen) {
			t.Fatal("untracked overflow was recorded as tracked evidence")
		}
	}
	// High-volume hooks can evict the displayed capacity event, but never the
	// persistent gap latch. The next scan must not announce the same gap again.
	hooks := make([]Finding, maxEvents+10)
	for i := range hooks {
		hooks[i] = toolStoreFinding(i + 1)
	}
	if _, err := ApplyReport(&state, toolStoreReport(hooks...), "root", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, event := range state.Events {
		if event.Kind == "monitor_capacity_degraded" {
			t.Fatal("test did not roll the displayed capacity event out")
		}
	}
	dir := privateStateDir(t)
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	state, err = LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	events, err = ApplyReport(&state, storeReport(findings[maxSeen]), "root", now.Add(2*time.Second))
	if err != nil || len(events) != 0 || state.Coverage != "degraded" || !hasStoreDiagnostic(state.Diagnostics, snapshotCapacityDiagnostic) {
		t.Fatalf("restart duplicated or forgot the stable capacity gap: %v %v", events, err)
	}
	if state.Seen[findings[maxSeen].Key] != "" || len(state.Seen) != maxSeen {
		t.Fatal("restart silently admitted overflow")
	}
	// There is deliberately no automatic snapshot eviction. Simulate an explicit
	// future remediation making space and verify a clean scan can close the gap.
	delete(state.Seen, "snapshot-1")
	if _, err := ApplyReport(&state, storeReport(), "root", now.Add(3*time.Second)); err != nil || state.Coverage != "observing" || hasStoreDiagnostic(state.Diagnostics, snapshotCapacityDiagnostic) {
		t.Fatalf("capacity gap could not recover after actual room became available: %v", err)
	}
}

func TestStoreHookWindowValidationAndLegacyEmptyFields(t *testing.T) {
	for _, test := range []struct {
		name  string
		seen  map[string]string
		order []string
	}{
		{"missing-order", map[string]string{"one": "hash"}, nil},
		{"extra-order", nil, []string{"one"}},
		{"duplicate-order", map[string]string{"one": "hash", "two": "hash"}, []string{"one", "one"}},
		{"unknown-key", map[string]string{"one": "hash"}, []string{"two"}},
		{"empty-key", map[string]string{"": "hash"}, []string{""}},
		{"empty-hash", map[string]string{"one": ""}, []string{"one"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := emptyState()
			state.Initialized, state.RootID = true, "root"
			state.HookSeen, state.HookSeenOrder = test.seen, test.order
			dir := privateStateDir(t)
			if err := SaveState(dir, state); err == nil {
				t.Fatal("persisted inconsistent hook cursor window")
			}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, stateFileName), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadState(dir); err == nil {
				t.Fatal("loaded inconsistent hook cursor window")
			}
		})
	}
	state := emptyState()
	state.Initialized, state.RootID = true, "root"
	for i := 0; i <= maxHookSeen; i++ {
		key := fmt.Sprintf("hook-%d", i)
		state.HookSeen[key] = "hash"
		state.HookSeenOrder = append(state.HookSeenOrder, key)
	}
	if err := SaveState(privateStateDir(t), state); err == nil {
		t.Fatal("accepted an oversized hook window")
	}
	dir := privateStateDir(t)
	legacy := `{"schema_version":1,"initialized":true,"root_id":"root","seen":{"snapshot":"hash"},"events":[],"coverage":"observing","last_checked_at":"2026-09-18T00:00:00Z","dropped_events":0}`
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil || len(loaded.HookSeen) != 0 || len(loaded.HookSeenOrder) != 0 {
		t.Fatalf("legacy empty hook fields were not accepted: %v", err)
	}
	if events, err := ApplyReport(&loaded, toolStoreReport(toolStoreFinding(0)), "root", time.Now()); err != nil || len(events) != 1 || len(loaded.Seen) != 1 {
		t.Fatalf("legacy state cannot accept independent hook events: %v %v", events, err)
	}
}

func TestStoreMigratesDevelopmentHookCursorsWithoutLosingSnapshots(t *testing.T) {
	state := emptyState()
	state.Initialized, state.RootID = true, "root"
	state.Seen["snapshot"] = "snapshot-hash"
	for i := 0; i < maxHookSeen+20; i++ {
		finding := toolStoreFinding(i)
		state.Seen[finding.Key] = finding.EvidenceHash
	}
	// Cursor 0 sorts oldest, but its retained event proves it was observed most
	// recently. Prefer that evidence when migrating the unordered legacy map.
	recent := toolStoreFinding(0)
	state.Events = []Event{{Key: recent.Key, Kind: recent.Kind, EvidenceHash: recent.EvidenceHash}}
	events, err := ApplyReport(&state, toolStoreReport(recent), "root", time.Now())
	if err != nil || len(events) != 0 || len(state.Seen) != 1 || state.Seen["snapshot"] != "snapshot-hash" {
		t.Fatalf("development migration lost deduplication or snapshot state: %v %v", events, err)
	}
	if len(state.HookSeen) != maxHookSeen || state.HookSeenOrder[len(state.HookSeenOrder)-1] != recent.Key {
		t.Fatal("migration did not preserve bounded recent legacy evidence")
	}
}
