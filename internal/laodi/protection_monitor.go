package laodi

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	protectionHealthKey     = "sensor:archive-protection"
	protectionHealthKind    = "protection_coverage_degraded"
	protectionCheckInterval = time.Minute
)

type protectionHealthChecker func(home, stateDir, app string) ProtectionSummary

// This is deliberately opt-in: synthetic scans and hook-only installations
// without a client path inspect only an explicitly enabled protection receipt.
type protectionMonitor struct {
	home, stateDir, app string
	check               protectionHealthChecker
	nextCheck           time.Time
	status              string
}

func newProtectionMonitor(home, stateDir, app string, check protectionHealthChecker) *protectionMonitor {
	// A Windows hook-only watcher discovers only its own opt-in receipt. Keep
	// checking even when protection is enabled after this watcher has started.
	discover := runtime.GOOS == "windows" && filepath.IsAbs(home) && filepath.IsAbs(stateDir) && app == "" && check == nil
	if home == "" || app == "" && !discover {
		return nil
	}
	if check == nil {
		verifier := protectionBundleVerifier{inspect: func(app string) bool {
			_, _, err := inspectProtectionBundle(app)
			return err == nil
		}}
		check = func(home, stateDir, app string) ProtectionSummary {
			if discover {
				app = protectionRecordedApp(home, stateDir)
			}
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
	extra          os.FileInfo
}

func (stamp protectionBundleStamp) matches(other protectionBundleStamp) bool {
	return sameProtectionFileStamp(stamp.plist, other.plist) && sameProtectionFileStamp(stamp.archive, other.archive) && (stamp.extra == nil && other.extra == nil || sameProtectionFileStamp(stamp.extra, other.extra))
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
	if runtime.GOOS == "windows" {
		paths = []string{app, filepath.Join(filepath.Dir(app), "resources", "app.asar"), filepath.Join(filepath.Dir(app), "resources", "glm", "zcode.cjs")}
	}
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
	stamp := protectionBundleStamp{plist: files[0], archive: files[1]}
	if len(files) > 2 {
		stamp.extra = files[2]
	}
	return stamp, true
}
