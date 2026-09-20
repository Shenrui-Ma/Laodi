package laodi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProtectionMonitorRequiresExplicitHomeAndClient(t *testing.T) {
	for _, item := range []struct{ home, app string }{{"", ""}, {"/synthetic/home", ""}, {"", "/synthetic/Client.app"}} {
		monitor := newProtectionMonitor(item.home, "synthetic-state", item.app, func(string, string, string) ProtectionSummary {
			t.Fatal("unconfigured monitor invoked the protection reader")
			return ProtectionSummary{}
		})
		if monitor != nil {
			t.Fatal("partially configured monitor was enabled")
		}
		monitor.observe(time.Now(), nil, nil)
	}
}

func TestProtectionMonitorChecksAtMostOncePerMinute(t *testing.T) {
	calls := 0
	monitor := newProtectionMonitor("synthetic-home", "synthetic-state", "synthetic-app", func(home, stateDir, app string) ProtectionSummary {
		if home != "synthetic-home" || stateDir != "synthetic-state" || app != "synthetic-app" {
			t.Fatal("checker received different paths")
		}
		calls++
		if calls == 1 {
			return ProtectionSummary{Status: "unknown"}
		}
		return ProtectionSummary{Status: "enabled"}
	})
	state := emptyState()
	now := time.Unix(1000, 0)
	for _, delta := range []time.Duration{0, time.Second, 59*time.Second + 999*time.Millisecond} {
		report := storeReport()
		monitor.observe(now.Add(delta), &report, &state)
		if calls != 1 || len(report.Findings) != 1 || len(report.Diagnostics) != 1 {
			t.Fatalf("expensive check repeated or cached failure disappeared: calls=%d report=%+v", calls, report)
		}
	}
	report := storeReport()
	monitor.observe(now.Add(time.Minute), &report, &state)
	if calls != 2 || len(report.Findings) != 0 || len(report.Diagnostics) != 0 {
		t.Fatalf("minute boundary did not refresh silently: calls=%d report=%+v", calls, report)
	}
}

func TestProtectionMonitorUsesOnlyFixedFailureMetadata(t *testing.T) {
	for _, status := range []string{"enabled", "disabled", "unknown", "degraded", "recovery_needed", "unsupported_client", "", "/private/unexpected-checker-error"} {
		t.Run(status, func(t *testing.T) {
			monitor := newProtectionMonitor("/private/synthetic-home", "/private/synthetic-state", "/private/synthetic-app", func(string, string, string) ProtectionSummary {
				return ProtectionSummary{Status: status, Scope: "PRIVATE_SCOPE", ProtectedArtifacts: 123, ProtectedWorkspaces: 456}
			})
			state := emptyState()
			report := storeReport()
			monitor.observe(time.Unix(1000, 0), &report, &state)
			if status == "enabled" || status == "disabled" {
				if len(report.Findings) != 0 || len(report.Diagnostics) != 0 || len(state.HookSeen) != 0 {
					t.Fatal("healthy or disabled guard created an observation")
				}
				return
			}
			if len(report.Findings) != 1 || len(report.Diagnostics) != 1 {
				t.Fatalf("missing health failure: %+v", report)
			}
			finding := report.Findings[0]
			if !isProtectionHealthFinding(finding) || finding.Source != "laodi" || len(finding.Counts) != 0 || finding.Evidence != "local_archive_guard_health" {
				t.Fatalf("health metadata mixed with upload evidence: %+v", finding)
			}
			data, err := json.Marshal(report)
			if err != nil || strings.Contains(string(data), "PRIVATE_SCOPE") || strings.Contains(string(data), "/private/") {
				t.Fatalf("report leaked checker metadata: %s err=%v", data, err)
			}
			if status == "" || strings.HasPrefix(status, "/") {
				if report.Diagnostics[0].Code != "archive_protection_unknown" {
					t.Fatal("unrecognized status was not normalized")
				}
			}
		})
	}
}

func TestProtectionMonitorRecoveryAllowsLaterFailureWithoutSuccessEvent(t *testing.T) {
	for _, recovery := range []string{"enabled", "disabled"} {
		t.Run(recovery, func(t *testing.T) {
			status := "degraded"
			monitor := newProtectionMonitor("synthetic-home", "synthetic-state", "synthetic-app", func(string, string, string) ProtectionSummary {
				return ProtectionSummary{Status: status}
			})
			state := emptyState()
			now := time.Unix(1000, 0)
			for index, item := range []struct {
				status string
				events int
			}{{"degraded", 1}, {"degraded", 0}, {recovery, 0}, {"degraded", 1}} {
				status = item.status
				report := storeReport()
				at := now.Add(time.Duration(index) * time.Minute)
				monitor.observe(at, &report, &state)
				events, err := ApplyReport(&state, report, "synthetic-root", at)
				if err != nil || len(events) != item.events {
					t.Fatalf("stage %d events=%v err=%v", index, events, err)
				}
				for _, event := range events {
					if event.BaselineExisting || event.Kind != protectionHealthKind {
						t.Fatalf("failure hidden by baseline or mislabeled: %+v", event)
					}
				}
				if index == 2 && len(state.Diagnostics) != 0 {
					t.Fatal("recovered diagnostic remained active")
				}
			}
			if len(state.Events) != 2 {
				t.Fatal("recovery emitted an event")
			}
		})
	}
}

func protectionMonitorTestBundle(t *testing.T) (string, string, string) {
	t.Helper()
	dir := protectionClientTestDir(t)
	app := filepath.Join(dir, "Synthetic.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Resources"), 0700); err != nil {
		t.Fatal(err)
	}
	plist, archive := filepath.Join(app, "Contents", "Info.plist"), filepath.Join(app, "Contents", "Resources", "app.asar")
	for _, file := range []string{plist, archive} {
		if err := os.WriteFile(file, []byte("synthetic-fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return app, plist, archive
}

func TestProtectionVerifierCachesOnlySuccessfulUnchangedIdentity(t *testing.T) {
	app, _, _ := protectionMonitorTestBundle(t)
	calls := 0
	verifier := protectionBundleVerifier{inspect: func(got string) bool {
		if got != app {
			t.Fatal("wrong bundle passed to full verification")
		}
		calls++
		return true
	}}
	if !verifier.verify(app) || !verifier.verify(app) || calls != 1 {
		t.Fatalf("unchanged verified bundle was not cached: calls=%d", calls)
	}
	// A second watcher must not inherit a previous process's trusted stamp.
	second := protectionBundleVerifier{inspect: verifier.inspect}
	if !second.verify(app) || calls != 2 {
		t.Fatal("verification cache escaped its monitor instance")
	}
}

func TestProtectionVerifierRechecksMetadataChanges(t *testing.T) {
	for _, target := range []string{"plist", "archive"} {
		for _, change := range []string{"size", "mtime", "mode", "inode"} {
			t.Run(target+"/"+change, func(t *testing.T) {
				app, plist, archive := protectionMonitorTestBundle(t)
				file := archive
				if target == "plist" {
					file = plist
				}
				calls := 0
				verifier := protectionBundleVerifier{inspect: func(string) bool { calls++; return true }}
				if !verifier.verify(app) {
					t.Fatal("initial verification failed")
				}
				before, err := os.Lstat(file)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "size":
					err = os.WriteFile(file, []byte("a longer synthetic fixture"), before.Mode().Perm())
				case "mtime":
					err = os.Chtimes(file, before.ModTime(), before.ModTime().Add(2*time.Second))
				case "mode":
					err = os.Chmod(file, 0640)
				case "inode":
					replacement := file + ".replacement"
					if err = os.WriteFile(replacement, []byte("synthetic-fixture"), before.Mode().Perm()); err == nil {
						err = os.Chtimes(replacement, before.ModTime(), before.ModTime())
					}
					if err == nil {
						err = os.Rename(replacement, file)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if !verifier.verify(app) || calls != 2 || !verifier.verify(app) || calls != 2 {
					t.Fatalf("changed metadata did not invalidate cached verification: calls=%d", calls)
				}
			})
		}
	}
}

func TestProtectionVerifierRetriesFailuresAndRejectsVerificationRace(t *testing.T) {
	app, _, archive := protectionMonitorTestBundle(t)
	calls, allow, mutate := 0, false, false
	verifier := protectionBundleVerifier{inspect: func(string) bool {
		calls++
		if mutate {
			if err := os.WriteFile(archive, []byte("changed while inspecting"), 0600); err != nil {
				t.Fatal(err)
			}
			mutate = false
		}
		return allow
	}}
	if verifier.verify(app) || verifier.verify(app) || calls != 2 {
		t.Fatal("failed identity verification was cached")
	}
	allow, mutate = true, true
	if verifier.verify(app) || calls != 3 {
		t.Fatal("metadata changed during verification but was trusted")
	}
	if !verifier.verify(app) || calls != 4 || !verifier.verify(app) || calls != 4 {
		t.Fatal("failure did not recover and cache a stable successful check")
	}
}

func TestProtectionVerifierRejectsSymlinkDespiteMatchingTargetMetadata(t *testing.T) {
	app, _, archive := protectionMonitorTestBundle(t)
	calls := 0
	verifier := protectionBundleVerifier{inspect: func(string) bool { calls++; return true }}
	if !verifier.verify(app) {
		t.Fatal("initial verification failed")
	}
	moved := archive + ".moved"
	if err := os.Rename(archive, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, archive); err != nil {
		t.Fatal(err)
	}
	if verifier.verify(app) || calls != 1 {
		t.Fatal("symlink replacement was accepted or unsafe file was inspected")
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, archive); err != nil {
		t.Fatal(err)
	}
	if !verifier.verify(app) || calls != 2 {
		t.Fatal("unsafe path failure failed to invalidate the previous trusted stamp")
	}
}
