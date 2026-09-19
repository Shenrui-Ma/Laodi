//go:build windows

package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWindowsStagingFailureLeavesVersionAvailableForRetry(t *testing.T) {
	source, manifest := windowsPayloadFixture(t, "v0.4.0-stage-test")
	state := filepath.Join(t.TempDir(), "state")
	plan := DistributionPlan{SourceDir: source, StateDir: state}
	file := filepath.Join(source, "laodi.exe")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("corrupted synthetic download"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stageWindowsVersion(plan, manifest); err == nil {
		t.Fatal("staged corrupt source")
	}
	version := filepath.Join(state, "versions", manifest.Version)
	if _, err := os.Stat(version); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed stage reserved immutable version name")
	}
	entries, err := os.ReadDir(filepath.Join(state, "versions"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed stage retained files: %v %v", entries, err)
	}
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := stageWindowsVersion(plan, manifest); err != nil {
		t.Fatalf("clean retry failed: %v", err)
	}
	if err := verifyWindowsVersion(state, windowsCurrent{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsMissingInstalledPayloadIsNotAFreshInstallation(t *testing.T) {
	for _, missing := range []string{"laodi.exe", "versions/v0.4.0-missing/laodi-host.exe", "versions/v0.4.0-missing/windows-manifest.json"} {
		t.Run(missing, func(t *testing.T) {
			source, _ := windowsPayloadFixture(t, "v0.4.0-missing")
			state := filepath.Join(t.TempDir(), "state")
			h := &windowsDistributionHarness{}
			if _, err := InstallDistribution(h.plan(t, source, state)); err != nil {
				t.Fatal(err)
			}
			plan := h.plan(t, source, state)
			before, err := readServiceFile(filepath.Join(state, windowsCurrentName))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(state, missing)); err != nil {
				t.Fatal(err)
			}
			if _, err := readWindowsCurrent(state); !errors.Is(err, errWindowsInstallationIncomplete) || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing installed payload misclassified: %v", err)
			}
			if _, err := InstallDistribution(plan); err == nil {
				t.Fatal("silently adopted broken installation")
			}
			after, err := readServiceFile(filepath.Join(state, windowsCurrentName))
			if err != nil || string(before) != string(after) {
				t.Fatal("failed retry replaced current version record")
			}
		})
	}
}

func TestWindowsFirstInstallResumesOnlyMatchingPartialLaunchers(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "edited"}[edited], func(t *testing.T) {
			source, manifest := windowsPayloadFixture(t, "v0.4.0-partial")
			state := filepath.Join(t.TempDir(), "state")
			if err := stageWindowsVersion(DistributionPlan{SourceDir: source, StateDir: state}, manifest); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(source, "laodi.exe"))
			if err != nil {
				t.Fatal(err)
			}
			if edited {
				data = []byte("user-owned different executable")
			}
			if err := writeWindowsBytes(state, "laodi.exe", data, false); err != nil {
				t.Fatal(err)
			}
			h := &windowsDistributionHarness{}
			_, err = InstallDistribution(h.plan(t, source, state))
			if edited {
				if err == nil {
					t.Fatal("adopted edited partial launcher")
				}
				if _, err := os.Stat(filepath.Join(state, windowsCurrentName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("published pointer after edited launcher")
				}
			} else if err != nil {
				t.Fatalf("matching partial installation could not resume: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(state, "laodi.exe"))
			if err != nil || string(got) != string(data) {
				t.Fatal("existing launcher was modified")
			}
		})
	}
}

func TestWindowsFreshHeartbeatMustIdentifySelectedVersion(t *testing.T) {
	source, _ := windowsPayloadFixture(t, "v0.4.0-identity")
	stateDir := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, stateDir)); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(-time.Second)
	state := emptyState()
	state.Running = true
	state.LastCheckedAt = time.Now()
	if err := SaveState(stateDir, state); err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	executable, created, err := processIdentity(handle)
	if err != nil {
		t.Fatal(err)
	}
	record := monitorProcess{Schema: SchemaVersion, PID: uint32(os.Getpid()), Created: created, Executable: executable}
	if err := writeWindowsRecord(stateDir, monitorProcessReceipt, record); err != nil {
		t.Fatal(err)
	}
	if err := windowsDistributionHealthy(stateDir, after); err == nil {
		t.Fatal("fresh heartbeat with wrong executable committed the selected version")
	}
}

func TestWindowsFirstInstallRequiresHealthyMonitorAndCanRetry(t *testing.T) {
	source, _ := windowsPayloadFixture(t, "v0.4.0-first-health")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{failHealth: true}
	result, err := InstallDistribution(h.plan(t, source, state))
	if err == nil || result.ServiceInstalled {
		t.Fatal("reported background connected without a verified heartbeat")
	}
	if _, err := readWindowsCurrent(state); err != nil {
		t.Fatal("startup failure lost retry material")
	}
	if result, err = InstallDistribution(h.plan(t, source, state)); err != nil || !result.ServiceInstalled {
		t.Fatalf("retry did not finish first installation: %+v %v", result, err)
	}
}

func TestWindowsInstallRechecksRecoveryJournalAfterPlanning(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.0-before")
	b, bm := windowsPayloadFixture(t, "v0.4.0-after")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	plan := h.plan(t, b, state)
	if plan.PendingRecovery {
		t.Fatal("unexpected recovery before test interruption")
	}
	old, err := readWindowsCurrent(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := stageWindowsVersion(plan, bm); err != nil {
		t.Fatal(err)
	}
	next := windowsCurrent{Manifest: bm, Launchers: old.Launchers}
	if err := writeWindowsRecord(state, windowsJournalName, windowsJournal{Schema: 1, Old: old, New: next}); err != nil {
		t.Fatal(err)
	}
	if err := writeWindowsRecord(state, windowsCurrentName, next); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, windowsJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale plan bypassed recovery and retained journal")
	}
	if h.stops != 2 {
		t.Fatalf("expected recovery then upgrade, observed %d stops", h.stops)
	}
}

func TestWindowsRecoveryRejectsEditedBackupLauncherHashesBeforeStopping(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.0-old")
	b, bm := windowsPayloadFixture(t, "v0.4.0-new")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	plan := h.plan(t, b, state)
	old, err := readWindowsCurrent(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := stageWindowsVersion(plan, bm); err != nil {
		t.Fatal(err)
	}
	next := windowsCurrent{Manifest: bm, Launchers: map[string]string{}}
	for name, hash := range old.Launchers {
		next.Launchers[name] = hash
	}
	if err := writeWindowsRecord(state, windowsCurrentName, next); err != nil {
		t.Fatal(err)
	}
	before, err := readServiceFile(filepath.Join(state, windowsCurrentName))
	if err != nil {
		t.Fatal(err)
	}
	old.Launchers["laodi.exe"] = serviceHash([]byte("changed backup ownership"))
	if err := writeWindowsRecord(state, windowsJournalName, windowsJournal{Schema: 1, Old: old, New: next}); err != nil {
		t.Fatal(err)
	}
	service, err := plan.windowsService(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverWindowsUpgrade(plan, service); err == nil {
		t.Fatal("recovery accepted changed launcher ownership in backup")
	}
	after, err := readServiceFile(filepath.Join(state, windowsCurrentName))
	if err != nil || string(after) != string(before) {
		t.Fatal("failed recovery overwrote current pointer")
	}
	if h.stops != 0 {
		t.Fatal("failed backup verification stopped a live service")
	}
}

func TestWindowsRecoveryRespectsServiceManagementAndOwnership(t *testing.T) {
	for _, mode := range []string{"locked", "edited-task"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := windowsPayloadFixture(t, "v0.4.0-recovery-old")
			b, bm := windowsPayloadFixture(t, "v0.4.0-recovery-new")
			state := filepath.Join(t.TempDir(), "state")
			h := &windowsDistributionHarness{}
			if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
				t.Fatal(err)
			}
			plan := h.plan(t, b, state)
			old, err := readWindowsCurrent(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := stageWindowsVersion(plan, bm); err != nil {
				t.Fatal(err)
			}
			next := windowsCurrent{Manifest: bm, Launchers: old.Launchers}
			if err := writeWindowsRecord(state, windowsJournalName, windowsJournal{Schema: 1, Old: old, New: next}); err != nil {
				t.Fatal(err)
			}
			if err := writeWindowsRecord(state, windowsCurrentName, next); err != nil {
				t.Fatal(err)
			}
			before, err := readServiceFile(filepath.Join(state, windowsCurrentName))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "locked" {
				release, err := AcquireLock(filepath.Join(state, "service-management"))
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			} else {
				h.task.XML += "<!-- user change -->"
			}
			if _, err := InstallDistribution(plan); err == nil {
				t.Fatal("recovery ignored service management boundary")
			}
			if h.stops != 0 {
				t.Fatal("recovery stopped monitor before validating ownership and acquiring management lock")
			}
			after, err := readServiceFile(filepath.Join(state, windowsCurrentName))
			if err != nil || string(before) != string(after) {
				t.Fatal("refused recovery changed selected version")
			}
			if _, err := readServiceFile(filepath.Join(state, windowsJournalName)); err != nil {
				t.Fatal("refused recovery discarded journal")
			}
		})
	}
}

func TestWindowsDirectoryPublicationCannotReplaceImmutableVersion(t *testing.T) {
	dir := privateStateDir(t)
	root, err := openStateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{".stage-test", "v0.4.0"} {
		if err := privateDirectory(root, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := publishPrivateDirectory(root, ".stage-test", "v0.4.0"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing version not preserved: %v", err)
	}
	for _, name := range []string{".stage-test", "v0.4.0"} {
		if _, err := root.Stat(name); err != nil {
			t.Fatal(err)
		}
	}
}
