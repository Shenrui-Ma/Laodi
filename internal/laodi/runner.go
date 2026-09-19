package laodi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

func DetectBuild(app string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", "CFBundleVersion", "raw", app+"/Contents/Info.plist").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

type WatchOptions struct {
	StateDir           string
	Interval, Duration time.Duration
	Notifier           string
	Output             io.Writer
	HooksOnly          bool
	ProtectionHome     string
	protectionCheck    protectionHealthChecker
}
type noticeResult struct {
	ID, Status string
	Ready      chan bool
}

func Watch(ctx context.Context, scanner *Scanner, opts WatchOptions) error {
	if opts.Interval < time.Second {
		return fmt.Errorf("interval must be at least one second")
	}
	release, err := AcquireLock(opts.StateDir)
	if err != nil {
		return err
	}
	defer release()
	state, err := LoadState(opts.StateDir)
	if err != nil {
		return err
	}
	rootID := RootID(scanner.Root)
	if opts.HooksOnly {
		rootID = RootID("laodi:tool-hooks-only")
	}
	if state.RootID != "" && state.RootID != rootID {
		return fmt.Errorf("state belongs to another evidence root")
	}
	state.Running = true
	defer func() { state.Running = false; _ = SaveState(opts.StateDir, state) }()
	if opts.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Duration)
		defer cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	jobs := make(chan Event, 16)
	messages := make(chan noticeResult, 32)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-jobs:
				ready := make(chan bool, 1)
				select {
				case messages <- noticeResult{ev.ID, "dispatching", ready}:
				case <-ctx.Done():
					return
				}
				select {
				case allowed := <-ready:
					if !allowed {
						continue
					}
				case <-ctx.Done():
					return
				}
				status := sendNotice(ctx, opts.Notifier, ev)
				select {
				case messages <- noticeResult{ev.ID, status, nil}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); wg.Wait() }()
	applyNotice := func(n noticeResult) error {
		found := false
		for i := range state.Events {
			if state.Events[i].ID == n.ID {
				state.Events[i].Notification = n.Status
				found = true
				break
			}
		}
		var e error
		if found {
			e = SaveState(opts.StateDir, state)
		}
		if n.Ready != nil {
			n.Ready <- found && e == nil
		}
		return e
	}
	recovery := []Event{}
	lastNotice := map[string]time.Time{}
	for i := range state.Events {
		e := &state.Events[i]
		switch e.Notification {
		case "queued", "pending", "queue_full":
			recovery = append(recovery, *e)
		case "dispatching":
			e.Notification = "unknown_after_restart"
		}
		if e.Notification == "accepted_by_os" || e.Notification == "unknown_after_restart" || e.Notification == "unknown_timeout" {
			if e.ObservedAt.After(lastNotice[e.Kind]) {
				lastNotice[e.Kind] = e.ObservedAt
			}
		}
	}
	brokenRounds := 0
	lastPersist := time.Time{}
	protection := newProtectionMonitor(opts.ProtectionHome, opts.StateDir, scanner.App, opts.protectionCheck)
	cycle := func() error {
		var report Report
		if opts.HooksOnly {
			report = Report{SchemaVersion: SchemaVersion, Parser: HookParserID, Coverage: "hook_inbox_ready", CheckedAt: time.Now().UTC()}
		} else {
			report = scanner.Scan()
		}
		previousCoverage := state.Coverage
		wasInitialized := state.Initialized
		previousDiagnostics, _ := json.Marshal(state.Diagnostics)
		broken := !opts.HooksOnly && report.Coverage != "observing" && report.Coverage != "no_evidence_directory"
		if report.Coverage == "no_evidence_directory" && hasSnapshotHistory(state.Seen) {
			broken = true
			report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "evidence_directory_disappeared"})
			report.Coverage = "degraded"
		}
		if broken {
			brokenRounds++
		} else {
			brokenRounds = 0
			// Recovery advances the health cursor without a noisy success banner.
			// The same failure in a later episode must be observable again.
			if _, ok := state.Seen["sensor:coverage"]; ok {
				state.Seen["sensor:coverage"] = digest("coverage_recovered")
			}
		}
		if brokenRounds >= 3 {
			codes := []string{}
			for _, d := range report.Diagnostics {
				codes = append(codes, d.Code)
			}
			sort.Strings(codes)
			report.Findings = append(report.Findings, Finding{Key: "sensor:coverage", Kind: "coverage_degraded", Evidence: "local_sensor", EvidenceHash: digest(report.Coverage + strings.Join(codes, ",")), Unknowns: []string{"coverage_incomplete", "task_continues"}})
		}
		// Even incomplete scans establish a baseline for valid evidence. Otherwise one
		// corrupt old manifest could prevent every later valid event from being seen.
		if state.Initialized && previousCoverage != "observing" && previousCoverage != "no_evidence_directory" && previousCoverage != "hook_inbox_ready" {
			for i := range report.Findings {
				report.Findings[i].Unknowns = append(report.Findings[i].Unknowns, "first_observed_after_incomplete_scan")
			}
		}
		batch, inboxErr := ReadHookInbox(opts.StateDir, 32)
		if inboxErr != nil {
			report.Findings = append(report.Findings, Finding{Key: "hook:inbox:unreadable", Kind: "hook_coverage_degraded", Evidence: "local_hook_inbox", EvidenceHash: digest("hook_inbox_unreadable"), Unknowns: []string{"tool_events_may_be_missing", "task_continues"}})
			report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "hook_inbox_unreadable"})
		} else {
			report.Findings = append(report.Findings, batch.Findings...)
			// A past queue gap remains an audit fact, not a claim that snapshot
			// parsing or all future tool events are currently broken.
			report.Diagnostics = append(report.Diagnostics, batch.Diagnostics...)
			for _, diagnostic := range batch.Diagnostics {
				if diagnostic.Code == "hook_inbox_record_invalid" || diagnostic.Code == "hook_inbox_scan_limit" {
					report.Findings = append(report.Findings, Finding{Key: "hook:inbox:" + diagnostic.Code, Kind: "hook_coverage_degraded", Evidence: "local_hook_inbox", EvidenceHash: digest(diagnostic.Code), Unknowns: []string{"tool_events_may_be_missing", "task_continues"}})
				}
			}
		}
		protection.observe(time.Now(), &report, &state)
		events, e := ApplyReport(&state, report, rootID, time.Now().UTC())
		if e != nil {
			return e
		}
		events = append(recovery, events...)
		recovery = nil
		// Keep all evidence, but show only the strongest new phase per snapshot.
		strongest := map[string]Event{}
		rank := map[string]int{
			"sensitive_manifest_match": 1, "upload_attempt_recorded": 2, "upload_acceptance_recorded": 3,
			"workspace_snapshot_manifest": 1, "workspace_snapshot_upload_attempt_recorded": 2, "workspace_snapshot_upload_acceptance_recorded": 3,
			"global_config_manifest_match": 1, "global_config_upload_attempt_recorded": 2, "global_config_upload_acceptance_recorded": 3,
			"coverage_degraded": 4, "monitor_capacity_degraded": 4,
			"sensitive_tool_access_requested": 1, "sensitive_tool_output_detected": 4, "hook_coverage_degraded": 3,
			protectionHealthKind: 4,
		}
		for _, ev := range events {
			group := ev.Key
			if n := strings.LastIndex(group, ":"); n >= 0 && ev.Kind != protectionHealthKind {
				group = group[:n]
			}
			if old, ok := strongest[group]; !ok || rank[ev.Kind] > rank[old.Kind] {
				strongest[group] = ev
			}
		}
		selected := map[string]bool{}
		for _, ev := range strongest {
			selected[ev.ID] = true
		}
		dispatch := []Event{}
		for _, ev := range events {
			for i := range state.Events {
				saved := &state.Events[i]
				if saved.ID != ev.ID {
					continue
				}
				switch {
				case ev.Kind == "sensitive_tool_access_requested":
					saved.Notification = "recorded_only"
				case opts.Notifier == "":
					saved.Notification = "not_configured"
				case !selected[ev.ID]:
					saved.Notification = "superseded_by_stronger_evidence"
				case !lastNotice[ev.Kind].IsZero() && time.Since(lastNotice[ev.Kind]) < 10*time.Minute:
					saved.Notification = "aggregated"
				default:
					saved.Notification = "queued"
					lastNotice[ev.Kind] = time.Now()
					dispatch = append(dispatch, *saved)
				}
			}
		}
		currentDiagnostics, _ := json.Marshal(state.Diagnostics)
		if !wasInitialized || len(events) > 0 || len(batch.Receipts) > 0 || previousCoverage != state.Coverage || string(previousDiagnostics) != string(currentDiagnostics) || time.Since(lastPersist) >= 30*time.Second {
			if e = SaveState(opts.StateDir, state); e != nil {
				return e
			}
			lastPersist = time.Now()
		}
		if inboxErr == nil && len(batch.Receipts) > 0 {
			// Remove only after the dedup cursor and events are durable. A failed
			// acknowledgement leaves replayable records, not lost observations.
			if e = AckHookInbox(opts.StateDir, batch.Receipts); e != nil {
				state.Diagnostics = append(state.Diagnostics, Diagnostic{Code: "hook_inbox_ack_failed"})
				if e = SaveState(opts.StateDir, state); e != nil {
					return e
				}
			}
		}
		for _, ev := range dispatch {
			select {
			case jobs <- ev:
			default:
				for i := range state.Events {
					if state.Events[i].ID == ev.ID {
						state.Events[i].Notification = "queue_full"
					}
				}
			}
		}
		if len(events) > 0 && opts.Output != nil {
			_ = json.NewEncoder(opts.Output).Encode(SummarizeState(state))
		}
		return nil
	}
	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	if err = cycle(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return SaveState(opts.StateDir, state)
		case message := <-messages:
			if err = applyNotice(message); err != nil {
				return err
			}
		case <-ticker.C:
			if err = cycle(); err != nil {
				return err
			}
		}
	}
}

func sendNotice(ctx context.Context, helper string, event Event) string {
	kinds := map[string]string{
		"sensitive_manifest_match": "snapshot-history", "upload_attempt_recorded": "upload-attempt", "upload_acceptance_recorded": "upload-accepted",
		"workspace_snapshot_manifest": "snapshot-workspace", "workspace_snapshot_upload_attempt_recorded": "workspace-upload-attempt", "workspace_snapshot_upload_acceptance_recorded": "workspace-upload-accepted",
		"global_config_manifest_match": "snapshot-config", "global_config_upload_attempt_recorded": "config-upload-attempt", "global_config_upload_acceptance_recorded": "config-upload-accepted",
		"coverage_degraded": "coverage-degraded", "monitor_capacity_degraded": "coverage-degraded",
		"sensitive_tool_output_detected": "tool-output-sensitive", "hook_coverage_degraded": "hook-coverage-degraded",
		protectionHealthKind: "protection-coverage-degraded",
	}
	kind, ok := kinds[event.Kind]
	if !ok {
		return "unsupported_notice_kind"
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, helper, "--send", "--id", event.ID, "--kind", kind).Output()
	if ctx.Err() != nil {
		return "unknown_timeout"
	}
	var response struct {
		Delivery  string `json:"delivery"`
		ErrorCode string `json:"error_code"`
	}
	if json.Unmarshal(out, &response) != nil {
		if err != nil {
			return "helper_failed"
		}
		return "invalid_helper_response"
	}
	switch response.Delivery {
	case "accepted_by_os":
		return "accepted_by_os"
	case "unknown":
		return "unknown_timeout"
	case "not_available":
		if response.ErrorCode == "notification_not_authorized" {
			return "not_authorized"
		}
		return "not_available"
	default:
		return "not_accepted"
	}
}
