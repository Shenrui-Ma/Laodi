//go:build windows

package laodi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Real updater processes and native files/locks are used. Only Task Scheduler
// and heartbeat responses are deterministic test adapters; this is a process
// termination test, not a claim to simulate power loss or a real task restart.
func windowsCrashPlan(t *testing.T, source, state, control, phase string) DistributionPlan {
	t.Helper()
	p, err := PlanDistribution(DistributionOptions{SourceDir: source, StateDir: state, Home: filepath.Dir(state), AppCandidates: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	loadTask := func() (windowsTask, error) {
		var task windowsTask
		data, err := readServiceFile(filepath.Join(control, "task.json"))
		if err != nil {
			return task, err
		}
		err = json.Unmarshal(data, &task)
		return task, err
	}
	saveTask := func(task windowsTask) error { return writeWindowsRecord(control, "task.json", task) }
	stopAt := func(boundary string) {
		if phase != boundary {
			return
		}
		fmt.Fprintln(os.Stdout, "fault-ready:"+boundary)
		var ignored [1]byte
		_, _ = io.ReadFull(os.Stdin, ignored[:])
		t.Fatal("fault controller failed to terminate its updater child")
	}
	p.stopMonitor = func(context.Context, string, string) error {
		stopAt("journal-published")
		task, err := loadTask()
		if err != nil {
			return err
		}
		task.State = 3
		if err := saveTask(task); err != nil {
			return err
		}
		stopAt("old-monitor-stopped")
		return nil
	}
	p.healthCheck = func(string, time.Time) error {
		current, err := readWindowsCurrent(state)
		if err != nil {
			return err
		}
		if current.Manifest.Version == "v0.4.0-crash-new" {
			stopAt("new-health-check")
			if strings.HasPrefix(phase, "recovery-") {
				return errors.New("synthetic failed new-monitor health")
			}
		} else {
			stopAt("recovery-health-check")
		}
		return nil
	}
	p.windowsService = func(p DistributionPlan) (ServicePlan, error) {
		service, err := windowsDistributionService(p)
		if err != nil {
			return service, err
		}
		service.taskRunner = func(_ context.Context, operation string, plan ServicePlan) (windowsTask, error) {
			task, err := loadTask()
			if err != nil {
				return task, err
			}
			switch operation {
			case "query":
				return task, nil
			case "create":
				if task.Exists {
					return task, errors.New("test task already exists")
				}
				task = windowsTask{Exists: true, XML: plan.TaskXML, State: 3}
			case "run":
				if !task.Exists || task.XML != plan.TaskXML {
					return task, errors.New("test task ownership changed")
				}
				current, err := readWindowsCurrent(state)
				if err != nil {
					return task, err
				}
				isNew := current.Manifest.Version == "v0.4.0-crash-new"
				if isNew {
					stopAt("pointer-switched")
				} else {
					stopAt("recovery-pointer-restored")
				}
				task.State = 4
				if err := saveTask(task); err != nil {
					return task, err
				}
				if isNew {
					stopAt("new-start-requested")
				} else {
					stopAt("recovery-start-requested")
				}
				return task, nil
			case "delete-exact":
				if task.XML != plan.TaskXML {
					return task, errors.New("test task ownership changed")
				}
				task = windowsTask{}
			default:
				return task, errors.New("unexpected task operation")
			}
			return task, saveTask(task)
		}
		return service, nil
	}
	return p
}

func TestWindowsCrashUpdaterHelper(t *testing.T) {
	phase := os.Getenv("LAODI_CRASH_PHASE")
	if phase == "" {
		return
	}
	plan := windowsCrashPlan(t, os.Getenv("LAODI_CRASH_SOURCE"), os.Getenv("LAODI_CRASH_STATE"), os.Getenv("LAODI_CRASH_CONTROL"), phase)
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	t.Fatal("requested crash boundary was not reached")
}

func startWindowsCrashUpdater(t *testing.T, source, state, control, phase string) func() {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestWindowsCrashUpdaterHelper$")
	cmd.Env = append(os.Environ(), "LAODI_CRASH_PHASE="+phase, "LAODI_CRASH_SOURCE="+source, "LAODI_CRASH_STATE="+state, "LAODI_CRASH_CONTROL="+control)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	kill := func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			input.Close()
			select {
			case err := <-done:
				if err == nil {
					t.Error("updater exited normally instead of being terminated")
				}
			case <-time.After(10 * time.Second):
				t.Error("terminated updater did not exit")
			}
		})
	}
	t.Cleanup(kill)
	line := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(output).ReadString('\n'); line <- strings.TrimSpace(text) }()
	select {
	case actual := <-line:
		if actual != "fault-ready:"+phase {
			kill()
			t.Fatalf("updater did not reach requested boundary: %q %s", actual, diagnostics.String())
		}
	case <-time.After(20 * time.Second):
		kill()
		t.Fatalf("updater did not reach %s: %s", phase, diagnostics.String())
	}
	return kill
}

func assertWindowsDurableSnapshot(t *testing.T, state string, before map[string]string) {
	t.Helper()
	after := windowsInstallationSnapshot(t, state)
	for name, hash := range before {
		if name == windowsCurrentName {
			continue
		}
		if after[name] != hash {
			t.Fatalf("updater changed retained identity/history file %q", name)
		}
	}
}

func TestWindowsKilledUpdaterRecoversEveryDurableBoundary(t *testing.T) {
	phases := []string{"journal-published", "old-monitor-stopped", "pointer-switched", "new-start-requested", "new-health-check", "recovery-pointer-restored", "recovery-start-requested", "recovery-health-check"}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			a, am := windowsPayloadFixture(t, "v0.4.0-crash-old")
			b, bm := windowsPayloadFixture(t, "v0.4.0-crash-new")
			state := filepath.Join(t.TempDir(), "state")
			control := filepath.Join(t.TempDir(), "task-adapter")
			if err := writeWindowsRecord(control, "task.json", windowsTask{}); err != nil {
				t.Fatal(err)
			}
			if _, err := InstallDistribution(windowsCrashPlan(t, a, state, control, "")); err != nil {
				t.Fatal(err)
			}
			windowsSeedDurableHistoryAndQueue(t, state)
			before := windowsInstallationSnapshot(t, state)
			kill := startWindowsCrashUpdater(t, b, state, control, phase)
			current, err := readWindowsCurrent(state)
			if err != nil {
				t.Fatal(err)
			}
			wantVersion := am.Version
			if phase == "pointer-switched" || phase == "new-start-requested" || phase == "new-health-check" {
				wantVersion = bm.Version
			}
			if current.Manifest.Version != wantVersion {
				t.Fatalf("wrong durable version at %s: %s", phase, current.Manifest.Version)
			}
			if _, err := readServiceFile(filepath.Join(state, windowsJournalName)); err != nil {
				t.Fatal("crash boundary has no recovery journal")
			}
			assertWindowsDurableSnapshot(t, state, before)
			// The updater is alive and holding management locks at this point.
			if _, err := InstallDistribution(windowsCrashPlan(t, b, state, control, "")); err == nil || !strings.Contains(err.Error(), "locked") {
				t.Fatalf("second updater was not excluded: %v", err)
			}
			if err := SubmitHookInspection(state, inboxInspection("during-upgrade-"+phase)); err != nil {
				t.Fatalf("independent queue blocked by update locks: %v", err)
			}
			queued, err := ReadHookInbox(state, 32)
			if err != nil || len(queued.Receipts) != 2 {
				t.Fatalf("queue unavailable at update boundary: %+v %v", queued, err)
			}
			beforeKill := windowsInstallationSnapshot(t, state)
			kill()
			for _, name := range []string{"distribution-management", "service-management", "hook-management"} {
				release, err := AcquireLock(filepath.Join(state, name))
				if err != nil {
					t.Fatalf("process death left %s locked: %v", name, err)
				}
				release()
			}
			if _, err := InstallDistribution(windowsCrashPlan(t, a, state, control, "")); err != nil {
				t.Fatalf("restart recovery failed: %v", err)
			}
			current, err = readWindowsCurrent(state)
			if err != nil || current.Manifest.Version != am.Version {
				t.Fatalf("old version not recovered: %v", err)
			}
			if _, err := os.Stat(filepath.Join(state, windowsJournalName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("successful recovery retained pending journal")
			}
			delete(beforeKill, windowsJournalName)
			assertWindowsDurableSnapshot(t, state, beforeKill)
			if result, err := InstallDistribution(windowsCrashPlan(t, b, state, control, "")); err != nil || !result.Updated {
				t.Fatalf("upgrade retry failed: %+v %v", result, err)
			}
			assertWindowsDurableSnapshot(t, state, beforeKill)
			if replay, err := ReadHookInbox(state, 32); err != nil || fmt.Sprint(replay.Receipts) != fmt.Sprint(queued.Receipts) {
				t.Fatalf("queue identities changed after recovery/retry: %v", err)
			}
		})
	}
}

func TestWindowsPointerSharingConflictRollsBackAndPreservesQueue(t *testing.T) {
	a, am := windowsPayloadFixture(t, "v0.4.0-sharing-old")
	b, _ := windowsPayloadFixture(t, "v0.4.0-sharing-new")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	windowsSeedDurableHistoryAndQueue(t, state)
	before := windowsInstallationSnapshot(t, state)
	// A native reader denying delete sharing blocks the actual pointer rename.
	// Release it only after the installer enters rollback, without filling disk
	// or injecting an artificial write error into production code.
	held, err := windowsOpen(filepath.Join(state, windowsCurrentName), os.O_RDONLY, false, false, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	p := h.plan(t, b, state)
	stop := p.stopMonitor
	calls := 0
	p.stopMonitor = func(ctx context.Context, dir, executable string) error {
		calls++
		if calls == 2 {
			if err := held.Close(); err != nil {
				return err
			}
		}
		return stop(ctx, dir, executable)
	}
	if _, err := InstallDistribution(p); err == nil || !strings.Contains(err.Error(), "old version restored") {
		t.Fatalf("native sharing failure did not roll back: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected upgrade stop and recovery stop, got %d", calls)
	}
	current, err := readWindowsCurrent(state)
	if err != nil || current.Manifest.Version != am.Version {
		t.Fatalf("sharing failure changed selected version: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, windowsJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed rollback retained recovery journal")
	}
	assertWindowsDurableSnapshot(t, state, before)
	if result, err := InstallDistribution(h.plan(t, b, state)); err != nil || !result.Updated {
		t.Fatalf("retry after sharing conflict failed: %+v %v", result, err)
	}
	assertWindowsDurableSnapshot(t, state, before)
}
