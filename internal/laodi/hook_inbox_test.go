package laodi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func inboxInspection(id string) HookInspection {
	return HookInspection{
		Adapter: "zcode", EventName: "PostToolUse", SessionID: "session-sensitive-" + id, ToolUseID: "tool-sensitive-" + id,
		Signals: []HookSignal{{Kind: "sensitive_tool_output_detected", Counts: map[string]int{"provider_token": 1}, Unknowns: []string{"credential_validity_unknown", "upload_not_observed"}}},
	}
}

func TestHookInboxReplayAckAndDedupWithoutRawIdentifiers(t *testing.T) {
	dir := privateStateDir(t)
	in := inboxInspection("CANARY_SECRET_VALUE")
	if err := SubmitHookInspection(dir, in); err != nil {
		t.Fatal(err)
	}
	if err := SubmitHookInspection(dir, in); err != nil {
		t.Fatal(err)
	}
	batch, err := ReadHookInbox(dir, 64)
	if err != nil || batch.Pending != 1 || len(batch.Findings) != 1 || len(batch.Receipts) != 1 {
		t.Fatalf("unexpected batch: %+v %v", batch, err)
	}
	replay, err := ReadHookInbox(dir, 64)
	if err != nil || !reflect.DeepEqual(batch, replay) {
		t.Fatalf("unacked crash replay changed: %+v %v", replay, err)
	}
	state := emptyState()
	if _, err := ApplyReport(&state, storeReport(), "root", time.Now()); err != nil {
		t.Fatal(err)
	}
	if events, err := ApplyReport(&state, storeReport(batch.Findings...), "root", time.Now()); err != nil || len(events) != 1 {
		t.Fatalf("first application: %v %v", events, err)
	}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if events, err := ApplyReport(&loaded, storeReport(replay.Findings...), "root", time.Now()); err != nil || len(events) != 0 {
		t.Fatalf("crash replay duplicated durable event: %v %v", events, err)
	}
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0700 {
				t.Errorf("directory is not private")
			}
			return nil
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("file is not private")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{"CANARY_SECRET_VALUE", in.SessionID, in.ToolUseID} {
			if strings.Contains(string(data), secret) || strings.Contains(path, secret) {
				t.Error("raw identifier reached disk")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := AckHookInbox(dir, batch.Receipts); err != nil {
			t.Fatal(err)
		}
	}
	after, err := ReadHookInbox(dir, 64)
	if err != nil || after.Pending != 0 || len(after.Findings) != 0 {
		t.Fatalf("ack did not remove exactly processed records: %+v %v", after, err)
	}
	if err := SubmitHookInspection(dir, in); err != nil {
		t.Fatal(err)
	}
	repeated, err := ReadHookInbox(dir, 64)
	if err != nil || !reflect.DeepEqual(batch.Findings, repeated.Findings) {
		t.Fatalf("identity changed after ack/re-submit: %+v %v", repeated, err)
	}
}

func TestHookInboxUnknownTextRejectedWithoutPersistence(t *testing.T) {
	cases := map[string]func(*HookInspection){
		"adapter":              func(in *HookInspection) { in.Adapter = "CANARY_SECRET" },
		"event":                func(in *HookInspection) { in.EventName = "CANARY_SECRET" },
		"kind":                 func(in *HookInspection) { in.Signals[0].Kind = "CANARY_SECRET" },
		"count":                func(in *HookInspection) { in.Signals[0].Counts["CANARY_SECRET"] = 1 },
		"unknown":              func(in *HookInspection) { in.Signals[0].Unknowns = []string{"CANARY_SECRET"} },
		"negative":             func(in *HookInspection) { in.Signals[0].Counts["provider_token"] = -1 },
		"huge count":           func(in *HookInspection) { in.Signals[0].Counts["provider_token"] = MaxHookInputBytes + 1 },
		"wrong classification": func(in *HookInspection) { in.Signals[0].Counts["env_file"] = 1 },
		"large id":             func(in *HookInspection) { in.SessionID = strings.Repeat("CANARY_SECRET", 500) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			dir := privateStateDir(t)
			in := inboxInspection("test")
			change(&in)
			if err := SubmitHookInspection(dir, in); !errors.Is(err, errHookInboxInvalid) || strings.Contains(err.Error(), "CANARY_SECRET") {
				t.Fatalf("unsafe error: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, hookInboxName))
			if err != nil || len(entries) != 1 || entries[0].Name() != hookInboxGapName {
				t.Fatalf("rejected inspection persisted more than fixed gap: %v %v", entries, err)
			}
			data, _ := os.ReadFile(filepath.Join(dir, hookInboxName, hookInboxGapName))
			if string(data) != "observation_gap\n" {
				t.Fatalf("gap is not fixed metadata: %q", data)
			}
			batch, err := ReadHookInbox(dir, 1)
			if err != nil || len(batch.Findings) != 1 || batch.Findings[0].Kind != "hook_coverage_degraded" || batch.Diagnostics[0].Code != "hook_inbox_gap_recorded" {
				t.Fatalf("missing explicit coverage gap: %+v %v", batch, err)
			}
		})
	}
}

func TestHookInboxConcurrentProducersCannotSilentlyLoseSuccessfulWrites(t *testing.T) {
	dir := privateStateDir(t)
	const count = 24
	results := make(chan error, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			results <- SubmitHookInspection(dir, inboxInspection(fmt.Sprint(i)))
		}(i)
	}
	workers.Wait()
	close(results)
	succeeded, failed := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, errHookInboxUnavailable) {
			failed++
		} else {
			t.Fatalf("unexpected submit error: %v", err)
		}
	}
	batch, err := ReadHookInbox(dir, count)
	if err != nil || succeeded == 0 || batch.Pending != succeeded || len(batch.Receipts) != succeeded {
		t.Fatalf("successful writes lost: success=%d failure=%d batch=%+v err=%v", succeeded, failed, batch, err)
	}
	status, err := GetHookInboxStatus(dir)
	if err != nil || (failed > 0 && !status.HasGap) {
		t.Fatalf("bounded lock failures were silent: %+v %v", status, err)
	}
}

func TestHookInboxCapacityAndGapRemainAfterAck(t *testing.T) {
	dir := privateStateDir(t)
	for i := range maxHookInboxRecords {
		if err := SubmitHookInspection(dir, inboxInspection(fmt.Sprint(i))); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if err := SubmitHookInspection(dir, inboxInspection("overflow")); !errors.Is(err, errHookInboxFull) {
		t.Fatalf("overflow was not explicit: %v", err)
	}
	if err := SubmitHookInspection(dir, inboxInspection("0")); err != nil {
		t.Fatalf("exact duplicate should succeed at capacity: %v", err)
	}
	batch, err := ReadHookInbox(dir, maxHookInboxRecords+1)
	if err != nil || batch.Pending != maxHookInboxRecords || len(batch.Receipts) != maxHookInboxRecords || len(batch.Findings) != maxHookInboxRecords+1 {
		t.Fatalf("queue is not bounded or gap missing: pending=%d receipts=%d findings=%d err=%v", batch.Pending, len(batch.Receipts), len(batch.Findings), err)
	}
	if err := AckHookInbox(dir, batch.Receipts); err != nil {
		t.Fatal(err)
	}
	batch, err = ReadHookInbox(dir, 1)
	if err != nil || batch.Pending != 0 || len(batch.Findings) != 1 || len(batch.Receipts) != 0 {
		t.Fatalf("ack erased historical loss marker: %+v %v", batch, err)
	}
}

func TestHookInboxLimitsReadAndMarksMissingCorrelation(t *testing.T) {
	dir := privateStateDir(t)
	in := inboxInspection("uncorrelated")
	in.SessionID, in.ToolUseID = "", ""
	for range 3 {
		if err := SubmitHookInspection(dir, in); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := ReadHookInbox(dir, 2)
	if err != nil || batch.Pending != 3 || len(batch.Receipts) != 2 || len(batch.Findings) != 2 || batch.Findings[0].Key == batch.Findings[1].Key {
		t.Fatalf("missing IDs caused unsafe dedup or read exceeded limit: %+v %v", batch, err)
	}
	for _, finding := range batch.Findings {
		if !strings.Contains(strings.Join(finding.Unknowns, ","), "correlation_unavailable") {
			t.Fatal("missing ID uncertainty was lost")
		}
	}
}

func TestHookInboxHMACUsesPrivatePerInstallationKey(t *testing.T) {
	var keys []string
	for range 2 {
		dir := privateStateDir(t)
		if err := SubmitHookInspection(dir, inboxInspection("same")); err != nil {
			t.Fatal(err)
		}
		batch, err := ReadHookInbox(dir, 1)
		if err != nil || len(batch.Findings) != 1 {
			t.Fatal(err)
		}
		keys = append(keys, batch.Findings[0].Key)
	}
	if keys[0] == keys[1] {
		t.Fatal("session correlation uses a globally guessable unsalted identity")
	}
}

func TestHookInboxRejectsSymlinksAndUnsafeReceipts(t *testing.T) {
	for _, location := range []string{"state", "inbox", "key", "lock", "record", "gap"} {
		t.Run(location, func(t *testing.T) {
			base := privateStateDir(t)
			dir := filepath.Join(base, "state")
			target := filepath.Join(base, "outside")
			if err := os.WriteFile(target, []byte("CANARY_UNCHANGED"), 0600); err != nil {
				t.Fatal(err)
			}
			if location == "state" || location == "inbox" {
				targetDir := filepath.Join(base, "external-dir")
				os.Mkdir(targetDir, 0700)
				link := dir
				if location == "inbox" {
					os.Mkdir(dir, 0700)
					link = filepath.Join(dir, hookInboxName)
				}
				if err := os.Symlink(targetDir, link); err != nil {
					t.Fatal(err)
				}
				if err := SubmitHookInspection(dir, inboxInspection("a")); err == nil {
					t.Fatal("accepted directory symlink")
				}
				entries, _ := os.ReadDir(targetDir)
				if len(entries) != 0 {
					t.Fatal("wrote through directory symlink")
				}
				return
			}
			if err := SubmitHookInspection(dir, inboxInspection("a")); err != nil {
				t.Fatal(err)
			}
			batch, _ := ReadHookInbox(dir, 1)
			name := map[string]string{"key": hookInboxKeyName, "lock": ".lock", "record": batch.Receipts[0], "gap": hookInboxGapName}[location]
			path := filepath.Join(dir, hookInboxName, name)
			os.Remove(path)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if location == "gap" {
				if _, err := ReadHookInbox(dir, 1); err == nil {
					t.Fatal("accepted gap symlink")
				}
			} else if err := SubmitHookInspection(dir, inboxInspection("a")); err == nil {
				t.Fatal("accepted file symlink")
			}
			if location == "record" {
				if err := AckHookInbox(dir, batch.Receipts); err == nil {
					t.Fatal("ack accepted record symlink")
				}
			}
			data, _ := os.ReadFile(target)
			if string(data) != "CANARY_UNCHANGED" {
				t.Fatal("symlink target modified")
			}
		})
	}
	dir := privateStateDir(t)
	if err := SubmitHookInspection(dir, inboxInspection("a")); err != nil {
		t.Fatal(err)
	}
	for _, receipt := range []string{"../state.json", hookInboxKeyName, strings.Repeat("f", 64) + ".json/../.key"} {
		if err := AckHookInbox(dir, []string{receipt}); !errors.Is(err, errHookInboxInvalid) {
			t.Fatalf("unsafe receipt accepted: %v", err)
		}
	}
}

func TestHookInboxDamagedRecordsAreBoundedAndDoNotHideValidOnes(t *testing.T) {
	dir := privateStateDir(t)
	if err := SubmitHookInspection(dir, inboxInspection("valid")); err != nil {
		t.Fatal(err)
	}
	root, err := openHookInbox(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	badName := strings.Repeat("0", 64) + ".json"
	if err := os.WriteFile(filepath.Join(root.Name(), badName), []byte(strings.Repeat("x", maxHookRecordBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := ReadHookInbox(dir, 1)
	if err != nil || batch.Pending != 2 || len(batch.Receipts) != 1 || len(batch.Findings) != 1 || len(batch.Diagnostics) != 1 {
		t.Fatalf("oversized first record hid valid later evidence: %+v %v", batch, err)
	}
	if err := AckHookInbox(dir, batch.Receipts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root.Name(), badName)); err != nil {
		t.Fatal("damaged evidence silently deleted")
	}
	if err := AckHookInbox(dir, []string{badName}); err == nil {
		t.Fatal("unverified record acknowledged")
	}
}

func TestHookInboxClosedSchemaAndFingerprintRejectTampering(t *testing.T) {
	dir := privateStateDir(t)
	if err := SubmitHookInspection(dir, inboxInspection("valid")); err != nil {
		t.Fatal(err)
	}
	batch, _ := ReadHookInbox(dir, 1)
	path := filepath.Join(dir, hookInboxName, batch.Receipts[0])
	data, _ := os.ReadFile(path)
	var record map[string]any
	json.Unmarshal(data, &record)
	record["raw_output"] = "SHOULD_NOT_BE_ACCEPTED"
	data, _ = json.Marshal(record)
	os.WriteFile(path, data, 0600)
	batch, err := ReadHookInbox(dir, 64)
	if err != nil || len(batch.Findings) != 0 || len(batch.Receipts) != 0 || len(batch.Diagnostics) != 1 {
		t.Fatalf("tampered record became an event: %+v %v", batch, err)
	}
	if _, err := decodeHookRecord(data); err == nil {
		t.Fatal("persistence schema accepted arbitrary data")
	}
}

func TestHookInboxLockTimeoutAndStatusAreExplicit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if err := SubmitHookInspection(dir, HookInspection{}); err != nil {
		t.Fatal(err)
	}
	status, err := GetHookInboxStatus(dir)
	if err != nil || status.Exists {
		t.Fatalf("missing status incorrect: %+v %v", status, err)
	}
	if _, err := ReadHookInbox(dir, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("clean hook/status/read created state")
	}
	if err := SubmitHookInspection(dir, inboxInspection("first")); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireLock(filepath.Join(dir, hookInboxName))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = SubmitHookInspection(dir, inboxInspection("blocked"))
	release()
	if !errors.Is(err, errHookInboxUnavailable) || time.Since(start) > time.Second {
		t.Fatalf("hook lock did not fail within bounded time: %v %s", err, time.Since(start))
	}
	status, err = GetHookInboxStatus(dir)
	if err != nil || !status.Exists || !status.HasGap || status.Pending != 1 {
		t.Fatalf("lock timeout went unrecorded: %+v %v", status, err)
	}
}
