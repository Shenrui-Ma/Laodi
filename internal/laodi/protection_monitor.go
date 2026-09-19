package laodi

import (
	"os"
	"path/filepath"
	"time"
)

const (
	protectionHealthKey     = "sensor:archive-protection"
	protectionHealthKind    = "protection_coverage_degraded"
	protectionCheckInterval = time.Minute
)

type protectionHealthChecker func(home, stateDir, app string) ProtectionSummary

// This is deliberately opt-in: synthetic scans and hook-only installations
// without a client path must never inspect the current user's real home.
type protectionMonitor struct {
	home, stateDir, app string
	check               protectionHealthChecker
	nextCheck           time.Time
	status              string
}

func newProtectionMonitor(home, stateDir, app string, check protectionHealthChecker) *protectionMonitor {
	if home == "" || app == "" {
		return nil
	}
	if check == nil {
		verifier := protectionBundleVerifier{inspect: func(app string) bool {
			_, _, err := inspectProtectionBundle(app)
			return err == nil
		}}
		check = func(home, stateDir, app string) ProtectionSummary {
			return readArchiveProtectionSummary(home, stateDir, app, verifier.verify)
		}
	}
	return &protectionMonitor{home: home, stateDir: stateDir, app: app, check: check}
}

func (monitor *protectionMonitor) observe(now time.Time, report *Report, state *State) {
	if monitor == nil {
		return
	}
	if monitor.nextCheck.IsZero() || !now.Before(monitor.nextCheck) {
		monitor.status = monitor.check(monitor.home, monitor.stateDir, monitor.app).Status
		monitor.nextCheck = now.Add(protectionCheckInterval)
	}
	switch monitor.status {
	case "enabled", "disabled":
		// Recovery is silent, but must let a later failure episode be recorded.
		// The existing bounded auxiliary cursor format remains downgrade-readable.
		if _, exists := state.HookSeen[protectionHealthKey]; exists {
			state.HookSeen[protectionHealthKey] = digest("archive_protection_recovered")
		}
		return
	case "unknown", "degraded", "recovery_needed", "unsupported_client":
	default:
		// Never persist unexpected checker output, which could contain an error
		// or path. A future unrecognized status is a visible uncertainty.
		monitor.status = "unknown"
	}
	report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "archive_protection_" + monitor.status})
	report.Findings = append(report.Findings, Finding{
		Source: "laodi", Key: protectionHealthKey, Kind: protectionHealthKind,
		Evidence: "local_archive_guard_health", EvidenceHash: digest("archive_protection_" + monitor.status),
		Unknowns: []string{"archive_restriction_not_confirmed", "other_upload_routes_not_blocked", "blocked_attempt_count_not_observed", "task_continues"},
	})
}

func isProtectionHealthFinding(finding Finding) bool {
	return finding.Kind == protectionHealthKind && finding.Key == protectionHealthKey
}

type protectionBundleStamp struct {
	plist, archive os.FileInfo
}

func (stamp protectionBundleStamp) matches(other protectionBundleStamp) bool {
	return sameProtectionFileStamp(stamp.plist, other.plist) && sameProtectionFileStamp(stamp.archive, other.archive)
}

func sameProtectionFileStamp(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) &&
		before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && before.Mode() == after.Mode()
}

// Each watch process performs a complete initial verification. Subsequent
// health checks only rehash when the bundle's file metadata changes. Failures
// are never cached. This detects ordinary client updates, not a same-user
// attacker deliberately preserving all metadata while editing file contents.
type protectionBundleVerifier struct {
	inspect  func(string) bool
	app      string
	stamp    protectionBundleStamp
	verified bool
}

func (verifier *protectionBundleVerifier) verify(app string) bool {
	before, ok := readProtectionBundleStamp(app)
	if !ok {
		verifier.verified = false
		return false
	}
	if verifier.verified && verifier.app == app && verifier.stamp.matches(before) {
		return true
	}
	verifier.verified = false
	if !verifier.inspect(app) {
		return false
	}
	after, ok := readProtectionBundleStamp(app)
	if !ok || !before.matches(after) {
		return false
	}
	verifier.app, verifier.stamp, verifier.verified = app, after, true
	return true
}

func readProtectionBundleStamp(app string) (protectionBundleStamp, bool) {
	paths := []string{filepath.Join(app, "Contents", "Info.plist"), filepath.Join(app, "Contents", "Resources", "app.asar")}
	files := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		if err := checkProtectionPath(path, false, false); err != nil {
			return protectionBundleStamp{}, false
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return protectionBundleStamp{}, false
		}
		files[i] = info
	}
	return protectionBundleStamp{plist: files[0], archive: files[1]}, true
}
