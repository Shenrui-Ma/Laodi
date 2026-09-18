package laodi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimeHook(t *testing.T, adapter, phase, id string) HookInspection {
	t.Helper()
	fixture := map[string]any{
		"hook_event_name": phase, "tool_name": "Bash", "session_id": "PRIVATE-session-name",
		"tool_use_id": id, "cwd": "/PRIVATE/customer", "transcript_path": "/PRIVATE/transcript",
		"tool_input":    map[string]any{"command": "cat /PRIVATE/customer/.env"},
		"tool_response": map[string]any{"stdout": "ghp_Q7m2Lp9Rx4Vt8Na3Ks6Yw1Bd5Jc0HfUzEeAi", "stderr": ""},
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return InspectHook(adapter, strings.NewReader(string(b)))
}

func TestWatchHookEventsAreNotSuppressedAsSnapshotBaseline(t *testing.T) {
	data := filepath.Join(t.TempDir(), "state")
	for _, adapter := range []string{"zcode", "claude-code"} {
		for _, phase := range []string{"PreToolUse", "PostToolUse"} {
			if err := SubmitHookInspection(data, runtimeHook(t, adapter, phase, "PRIVATE-tool")); err != nil {
				t.Fatal(err)
			}
		}
	}
	helper := filepath.Join(t.TempDir(), "notice.sh")
	calls := filepath.Join(t.TempDir(), "calls")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> '"+calls+"'\nprintf '{\"delivery\":\"accepted_by_os\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// No ZCode checkpoint directory exists. Hook events must not make its
	// continued absence look like a disappeared snapshot directory.
	scanner := &Scanner{Root: filepath.Join(t.TempDir(), "absent"), Build: KnownBuild}
	opts := WatchOptions{StateDir: data, Interval: time.Second, Duration: 1200 * time.Millisecond, Notifier: helper}
	for run := 0; run < 2; run++ {
		if err := Watch(context.Background(), scanner, opts); err != nil {
			t.Fatal(err)
		}
		st, err := LoadState(data)
		if err != nil || len(st.Events) != 4 || st.Coverage != "no_evidence_directory" {
			t.Fatalf("wrong combined monitor state: %+v %v", st, err)
		}
		for _, event := range st.Events {
			if event.BaselineExisting || event.Source == "" {
				t.Fatalf("active hook event hidden or source lost: %+v", event)
			}
			if event.Kind == "sensitive_tool_access_requested" && event.Notification != "recorded_only" {
				t.Fatalf("request-only evidence must not toast: %+v", event)
			}
		}
		status, err := GetHookInboxStatus(data)
		if err != nil || status.Pending != 0 {
			t.Fatalf("durable events were not acknowledged: %+v %v", status, err)
		}
		b, _ := json.Marshal(SummarizeState(st))
		for _, private := range []string{"PRIVATE", "ghp_", "customer", "transcript"} {
			if strings.Contains(string(b), private) {
				t.Fatalf("summary exposed private input: %s", private)
			}
		}
		if run == 0 {
			// Replay after acknowledgement must still deduplicate against State.
			if err := SubmitHookInspection(data, runtimeHook(t, "zcode", "PostToolUse", "PRIVATE-tool")); err != nil {
				t.Fatal(err)
			}
		}
	}
	b, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(b), "tool-output-sensitive") != 1 || strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("notices not fixed/redacted/rate-limited: %q %v", b, err)
	}
}

func TestHooksOnlyWorksWithoutAnySupportedSnapshotClient(t *testing.T) {
	data := filepath.Join(t.TempDir(), "state")
	if err := SubmitHookInspection(data, runtimeHook(t, "claude-code", "PostToolUse", "tool")); err != nil {
		t.Fatal(err)
	}
	err := Watch(context.Background(), &Scanner{Root: "/unused", Build: "not-a-zcode-build"}, WatchOptions{
		StateDir: data, Interval: time.Second, Duration: 50 * time.Millisecond, HooksOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(data)
	if err != nil || st.Parser != HookParserID || st.Coverage != "hook_inbox_ready" || len(st.Events) != 1 {
		t.Fatalf("standalone tool adapter depends on ZCode: %+v %v", st, err)
	}
	if st.Events[0].Kind != "sensitive_tool_output_detected" || st.Events[0].Notification != "not_configured" {
		t.Fatal(st.Events[0])
	}
}

func TestHookIncompleteInspectionBecomesVisibleEvent(t *testing.T) {
	data := filepath.Join(t.TempDir(), "state")
	if err := SubmitHookInspection(data, InspectHook("zcode", strings.NewReader(`{"PRIVATE-secret":`))); err != nil {
		t.Fatal(err)
	}
	if err := Watch(context.Background(), &Scanner{}, WatchOptions{StateDir: data, Interval: time.Second, Duration: 50 * time.Millisecond, HooksOnly: true}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(data)
	if err != nil || len(st.Events) != 1 || st.Events[0].Kind != "hook_coverage_degraded" || st.Events[0].BaselineExisting {
		t.Fatalf("failed inspection was hidden: %+v %v", st, err)
	}
}

func TestPartialTruncatedHookKeepsCredentialFindingAndPrioritizesNotice(t *testing.T) {
	data := filepath.Join(t.TempDir(), "state")
	payload := `{"hook_event_name":"PostToolUse","tool_name":"Read","session_id":"private-session","tool_use_id":"private-tool","tool_response":{"truncated":true,"content":[{"type":"text","text":"ghp_Q7m2Lp9Rx4Vt8Na3Ks6Yw1Bd5Jc0HfUzEeAi"},{"type":"image","data":"uninspected"}]}}`
	in := InspectHook("zcode", strings.NewReader(payload))
	if err := SubmitHookInspection(data, in); err != nil {
		t.Fatal(err)
	}
	batch, err := ReadHookInbox(data, 32)
	if err != nil || len(batch.Findings) != 2 || len(batch.Diagnostics) != 0 {
		t.Fatalf("incomplete output discarded valid credential evidence: %+v %v", batch, err)
	}
	helper := filepath.Join(t.TempDir(), "notice.sh")
	calls := filepath.Join(t.TempDir(), "calls")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> '"+calls+"'\nprintf '{\"delivery\":\"accepted_by_os\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, &Scanner{}, WatchOptions{StateDir: data, Interval: time.Second, HooksOnly: true, Notifier: helper})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := LoadState(data)
		accepted := false
		for _, event := range st.Events {
			accepted = accepted || event.Kind == "sensitive_tool_output_detected" && event.Notification == "accepted_by_os"
		}
		if accepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake notifier acknowledgement not persisted: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, err := LoadState(data)
	if err != nil || len(st.Events) != 2 {
		t.Fatalf("missing persisted events: %+v %v", st, err)
	}
	for _, e := range st.Events {
		if e.Kind == "hook_coverage_degraded" && (e.Counts["output_partial"] != 1 || e.Counts["output_truncated"] != 1 || e.Notification != "superseded_by_stronger_evidence") {
			t.Fatalf("partial reasons or notice priority incorrect: %+v", e)
		}
	}
	b, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(b), "tool-output-sensitive") != 1 || strings.Contains(string(b), "hook-coverage-degraded") {
		t.Fatalf("coverage notice hid credential notice: %q %v", b, err)
	}
}
