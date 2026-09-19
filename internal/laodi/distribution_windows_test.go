//go:build windows

package laodi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func windowsPayloadFixture(t *testing.T, tag string) (string, WindowsManifest) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "发布 ' & % ! $ (包)")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	m := WindowsManifest{Schema: 1, Version: tag, Protocol: 1, Files: map[string]string{}}
	for _, name := range []string{"laodi.exe", "laodi-host.exe", "laodi-logo.png", "SKILL.md"} {
		data := []byte("synthetic never executed " + tag + " " + name)
		m.Files[name] = serviceHash(data)
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, windowsManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	return source, m
}

type windowsDistributionHarness struct {
	task          windowsTask
	starts, stops int
	failHealth    bool
}

func TestWindowsIdenticalInstallAcceptsRecentHeartbeatWithoutRestart(t *testing.T) {
	source, _ := windowsPayloadFixture(t, "v0.4.0-heartbeat")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, state)); err != nil {
		t.Fatal(err)
	}
	starts := h.starts
	p := h.plan(t, source, state)
	p.healthCheck = func(_ string, after time.Time) error {
		if time.Since(after) < 70*time.Second {
			return errors.New("quiet running monitor cannot promise a new disk heartbeat within 15 seconds")
		}
		return nil
	}
	if _, err := InstallDistribution(p); err != nil {
		t.Fatal(err)
	}
	if h.starts != starts {
		t.Fatal("identical install restarted the monitor")
	}
}

func (h *windowsDistributionHarness) plan(t *testing.T, source, state string) DistributionPlan {
	t.Helper()
	p, err := PlanDistribution(DistributionOptions{SourceDir: source, StateDir: state, Home: filepath.Dir(state)})
	if err != nil {
		t.Fatal(err)
	}
	p.stopMonitor = func(context.Context, string, string) error { h.stops++; h.task.State = 3; return nil }
	p.healthCheck = func(string, time.Time) error {
		if h.failHealth {
			h.failHealth = false
			return errors.New("synthetic startup failure")
		}
		return nil
	}
	p.windowsService = func(p DistributionPlan) (ServicePlan, error) {
		s, err := windowsDistributionService(p)
		if err != nil {
			return s, err
		}
		s.taskRunner = func(_ context.Context, op string, s ServicePlan) (windowsTask, error) {
			switch op {
			case "query":
				return h.task, nil
			case "create":
				if h.task.Exists {
					return h.task, errors.New("exists")
				}
				h.task = windowsTask{Exists: true, XML: s.TaskXML, State: 3}
			case "run":
				if h.task.XML != s.TaskXML {
					return h.task, errors.New("changed task")
				}
				h.starts++
				h.task.State = 4
			case "delete-exact":
				if h.task.XML != s.TaskXML {
					return h.task, errors.New("changed task")
				}
				h.task = windowsTask{}
			default:
				return h.task, errors.New("unexpected task operation")
			}
			return h.task, nil
		}
		return s, nil
	}
	return p
}
func TestWindowsDistributionPreservesHistoryQueueAndLaunchers(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.0-test.1")
	b, _ := windowsPayloadFixture(t, "v0.4.0-test.2")
	state := filepath.Join(t.TempDir(), "状态 with spaces")
	h := &windowsDistributionHarness{}
	p := h.plan(t, a, state)
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planning wrote state")
	}
	if _, err := InstallDistribution(p); err != nil {
		t.Fatal(err)
	}
	old, err := readWindowsCurrent(state)
	if err != nil {
		t.Fatal(err)
	}
	st := emptyState()
	if err = SaveState(state, st); err != nil {
		t.Fatal(err)
	}
	if err = SubmitHookInspection(state, runtimeHook(t, "claude-code", "PostToolUse", "synthetic-upgrade")); err != nil {
		t.Fatal(err)
	}
	before, err := ReadHookInbox(state, 32)
	if err != nil {
		t.Fatal(err)
	}
	starts := h.starts
	if _, err = InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	if h.starts != starts {
		t.Fatal("same version restarted task")
	}
	result, err := InstallDistribution(h.plan(t, b, state))
	if err != nil || !result.Updated {
		t.Fatalf("upgrade: %+v %v", result, err)
	}
	next, err := readWindowsCurrent(state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old.Launchers, next.Launchers) {
		t.Fatal("stable launchers changed")
	}
	after, err := ReadHookInbox(state, 32)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("queue changed: %v", err)
	}
	if got, err := LoadState(state); err != nil || !reflect.DeepEqual(got, st) {
		t.Fatalf("state changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "versions", old.Manifest.Version, "laodi.exe")); err != nil {
		t.Fatal("old version was deleted")
	}
}
func TestWindowsDistributionRollsBackFailedStart(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.0-a")
	b, _ := windowsPayloadFixture(t, "v0.4.0-b")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	h.failHealth = true
	if _, err := InstallDistribution(h.plan(t, b, state)); err == nil || !strings.Contains(err.Error(), "old version restored") {
		t.Fatalf("rollback: %v", err)
	}
	c, err := readWindowsCurrent(state)
	if err != nil || c.Manifest.Version != "v0.4.0-a" {
		t.Fatalf("wrong rollback: %+v %v", c, err)
	}
	if _, err = os.Stat(filepath.Join(state, windowsJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovered journal retained")
	}
}

func TestWindowsNewerReinstallAfterRemovalRetainsHistory(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.0-a")
	b, _ := windowsPayloadFixture(t, "v0.4.0-b")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(state, emptyState()); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	if h.task.Exists {
		t.Fatal("task not removed")
	}
	if _, err := InstallDistribution(h.plan(t, b, state)); err != nil {
		t.Fatal(err)
	}
	if !h.task.Exists {
		t.Fatal("task not restored")
	}
	c, err := readWindowsCurrent(state)
	if err != nil || c.Manifest.Version != "v0.4.0-b" {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(state, stateFileName)); err != nil {
		t.Fatal("history removed")
	}
}
func TestWindowsDistributionRecoveryAndOwnership(t *testing.T) {
	for _, phase := range []string{"journal-published", "pointer-switched", "new-started"} {
		t.Run(phase, func(t *testing.T) {
			a, _ := windowsPayloadFixture(t, "v0.4.0-a")
			b, bm := windowsPayloadFixture(t, "v0.4.0-b")
			state := filepath.Join(t.TempDir(), "state")
			h := &windowsDistributionHarness{}
			if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
				t.Fatal(err)
			}
			old, err := readWindowsCurrent(state)
			if err != nil {
				t.Fatal(err)
			}
			p := h.plan(t, b, state)
			if err = stageWindowsVersion(p, bm); err != nil {
				t.Fatal(err)
			}
			next := windowsCurrent{Manifest: bm, Launchers: old.Launchers}
			if err = writeWindowsRecord(state, windowsJournalName, windowsJournal{Schema: 1, Old: old, New: next}); err != nil {
				t.Fatal(err)
			}
			if phase != "journal-published" {
				if err = writeWindowsRecord(state, windowsCurrentName, next); err != nil {
					t.Fatal(err)
				}
			}
			p = h.plan(t, b, state)
			if _, err = UninstallDistribution(p); err == nil {
				t.Fatal("removal invalidated recovery")
			}
			if _, err = InstallDistribution(p); err != nil {
				t.Fatal(err)
			}
			c, err := readWindowsCurrent(state)
			if err != nil || c.Manifest.Version != bm.Version {
				t.Fatal(err)
			}
		})
	}
}
func TestWindowsDistributionRejectsEditedPayloadTaskAndVersionReuse(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.0-a")
	b, _ := windowsPayloadFixture(t, "v0.4.0-b")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	h.task.XML += "<!-- user edit -->"
	if _, err := InstallDistribution(h.plan(t, b, state)); err == nil {
		t.Fatal("edited task accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "versions", "v0.4.0-a", "laodi.exe"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanDistribution(DistributionOptions{SourceDir: b, StateDir: state}); err == nil {
		t.Fatal("edited executable accepted")
	}
}
func TestWindowsRouteRejectsChangedLauncher(t *testing.T) {
	source, _ := windowsPayloadFixture(t, "v0.4.0-test")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, state)); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(state, "laodi.exe")
	target, err := WindowsRoute(exe)
	if err != nil || !strings.Contains(target, "versions") {
		t.Fatal(target, err)
	}
	if err = os.WriteFile(exe, []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = WindowsRoute(exe); err == nil {
		t.Fatal("edited launcher accepted")
	}
}
func zipFixture(t *testing.T, source string, extra *zip.FileHeader) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		f, e := w.Create(entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		data, e := os.ReadFile(filepath.Join(source, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		f.Write(data)
	}
	if extra != nil {
		f, e := w.CreateHeader(extra)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte("synthetic"))
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestWindowsArchiveRejectsAliasesAndEscapesBeforeWriting(t *testing.T) {
	source, _ := windowsPayloadFixture(t, "v0.4.0-test")
	for _, name := range []string{"../outside", "C:/outside", "//server/share", "\\\\?\\C:\\x", "laodi.exe:stream", "NUL", "laodi.exe.", "laodi.exe ", "LAODI.EXE", "laodi.exe"} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "extract")
			if err := ExtractWindowsRelease(zipFixture(t, source, &zip.FileHeader{Name: name}), dest); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("extracted before validation")
			}
		})
	}
	for _, mode := range []os.FileMode{os.ModeSymlink | 0777, os.ModeDir | 0700} {
		hdr := &zip.FileHeader{Name: "LaodiNotify.exe"}
		hdr.SetMode(mode)
		if err := ExtractWindowsRelease(zipFixture(t, source, hdr), filepath.Join(t.TempDir(), "extract")); err == nil {
			t.Fatal("nonregular entry accepted")
		}
	}
	dest := filepath.Join(t.TempDir(), "extract")
	if err := ExtractWindowsRelease(zipFixture(t, source, nil), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := readWindowsPayload(dest); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsReleaseVersionAndCanceledDownload(t *testing.T) {
	for _, tag := range []string{"v1.2.3", "v1.2.3-preview.2"} {
		if !validWindowsReleaseVersion(tag) {
			t.Fatal(tag)
		}
	}
	for _, tag := range []string{"v01.2.3", "v1.2.3-01", "v1.2.3-preview.01", "v1.2.3/../../x", "v1.2.3+local"} {
		if validWindowsReleaseVersion(tag) {
			t.Fatal(tag)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := officialWindowsDownload(ctx, "https://github.com/Shenrui-Ma/Laodi-skills/releases/download/v0.0.0/SHA256SUMS-windows", 65536); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transfer: %v", err)
	}
	if _, err := officialWindowsDownload(context.Background(), "http://github.com/", 65536); err == nil {
		t.Fatal("insecure origin accepted")
	}
	if _, err := officialWindowsDownload(context.Background(), "https://example.com/", 65536); err == nil {
		t.Fatal("foreign origin accepted")
	}
}
