package laodi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type upgradeFixture struct {
	options          DistributionOptions
	registered       bool
	calls            []string
	failNewBootstrap bool
	failNewHealth    bool
}

func newUpgradeFixture(t *testing.T) *upgradeFixture {
	t.Helper()
	f := &upgradeFixture{options: distributionFixture(t)}
	f.options.RequestNotifications = false
	fixtureTool(t, &f.options, "app")
	plan := f.plan(t)
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	return f
}

func (f *upgradeFixture) plan(t *testing.T) DistributionPlan {
	t.Helper()
	p, err := PlanDistribution(f.options)
	if err != nil {
		t.Fatal(err)
	}
	p.serviceRunner = func(_ context.Context, args ...string) error {
		f.calls = append(f.calls, args[0])
		switch args[0] {
		case "print":
			if !f.registered {
				return errors.New("not loaded")
			}
		case "bootout":
			f.registered = false
		case "bootstrap":
			b, _ := os.ReadFile(p.Executable)
			if f.failNewBootstrap && string(b) == "new payload" {
				return errors.New("synthetic new service failure")
			}
			f.registered = true
		default:
			t.Fatalf("unexpected service action %v", args)
		}
		return nil
	}
	p.healthCheck = func(_ string, _ time.Time) error {
		b, _ := os.ReadFile(p.Executable)
		if f.failNewHealth && string(b) == "new payload" {
			return errors.New("synthetic health failure")
		}
		return nil
	}
	p.notifierRunner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"--status"}) {
			t.Fatal("upgrade requested permission again")
		}
		return []byte(`{"ok":true,"authorization":"authorized"}`), nil
	}
	return p
}

func (f *upgradeFixture) newSource(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.options.SourceDir, "laodi"), []byte("new payload"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.options.SourceDir, "LaodiNotify.app", "Contents", "Resources", "Laodi.icns")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic icon"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeDirectoryExchangeIsAtomicAndReversible(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS syscall")
	}
	dir := t.TempDir()
	for _, name := range []string{"runtime", "staged"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "value"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	for i := 0; i < 2; i++ {
		if err := swapRuntimeDirectories(parent, "runtime", "staged"); err != nil {
			t.Fatal(err)
		}
		value, err := os.ReadFile(filepath.Join(dir, "runtime", "value"))
		if err != nil {
			t.Fatal(err)
		}
		want := "staged"
		if i == 1 {
			want = "runtime"
		}
		if string(value) != want {
			t.Fatalf("unexpected exchanged content %q", value)
		}
	}
}

func TestDistributionUpgradePreservesConfigurationHistoryAndQueue(t *testing.T) {
	f := newUpgradeFixture(t)
	oldPlan := f.plan(t)
	paths := []string{filepath.Join(f.options.Home, ".zcode", "cli", "config.json"), filepath.Join(oldPlan.StateDir, serviceReceiptName), filepath.Join(f.options.Home, "Library", "LaunchAgents", serviceLabel+".plist"), filepath.Join(oldPlan.StateDir, "hook-install-zcode.json")}
	for _, name := range []string{"history-canary", "queue-canary", "hmac-key-canary"} {
		p := filepath.Join(oldPlan.StateDir, name)
		if err := os.WriteFile(p, []byte("preserve "+name), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	before := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = string(b)
	}
	f.newSource(t)
	f.options.RequestNotifications = true
	plan := f.plan(t)
	if !plan.Upgrade || plan.PendingRecovery {
		t.Fatalf("wrong update preview: %+v", plan)
	}
	b, _ := os.ReadFile(plan.Executable)
	if string(b) == "new payload" {
		t.Fatal("preview modified runtime")
	}
	result, err := InstallDistribution(plan)
	if err != nil || !result.Updated || !result.ServiceInstalled || result.NotificationStatus != "authorized" {
		t.Fatalf("update failed %+v %v", result, err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != before[p] {
			t.Fatalf("changed retained file %s", p)
		}
	}
	if _, err := ownedDistributionFiles(plan.RuntimeDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plan.RuntimeDir, "LaodiNotify.app/Contents/Resources/Laodi.icns")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"print", "bootout", "bootstrap", "print"}) {
		t.Fatalf("unexpected update service actions %v", f.calls)
	}
	f.calls = nil
	result, err = InstallDistribution(f.plan(t))
	if err != nil || result.Updated || !reflect.DeepEqual(f.calls, []string{"print"}) {
		t.Fatalf("identical install restarted monitor: %+v %v %v", result, err, f.calls)
	}
}

func TestDistributionUpgradeRollsBackFailedStart(t *testing.T) {
	for _, failure := range []string{"bootstrap", "health"} {
		t.Run(failure, func(t *testing.T) {
			f := newUpgradeFixture(t)
			old, err := ownedDistributionFiles(f.plan(t).RuntimeDir)
			if err != nil {
				t.Fatal(err)
			}
			f.newSource(t)
			f.failNewBootstrap = failure == "bootstrap"
			f.failNewHealth = failure == "health"
			plan := f.plan(t)
			result, err := InstallDistribution(plan)
			if err == nil || !strings.Contains(err.Error(), "previous runtime restored") || result.Updated || !f.registered {
				t.Fatalf("bad recovery: %+v %v registered=%v", result, err, f.registered)
			}
			actual, err := ownedDistributionFiles(plan.RuntimeDir)
			if err != nil || !reflect.DeepEqual(actual, old) {
				t.Fatalf("old runtime not restored: %v", err)
			}
			if _, err := os.Stat(filepath.Join(plan.StateDir, upgradeJournalName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed recovery retained journal")
			}
		})
	}
}

func prepareInterruptedUpgrade(t *testing.T, f *upgradeFixture, exchange bool) (DistributionPlan, upgradeJournal) {
	t.Helper()
	f.newSource(t)
	p := f.plan(t)
	old, err := ownedDistributionFiles(p.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(p.StateDir, ".runtime-upgrade-interrupted")
	staged := p
	staged.RuntimeDir = stage
	if err := installDistributionPayload(staged); err != nil {
		t.Fatal(err)
	}
	j := upgradeJournal{Version: 1, Stage: filepath.Base(stage), Old: old, New: p.files, ServiceOwned: true}
	b, _ := json.Marshal(j)
	if err := writeNewServiceFile(filepath.Join(p.StateDir, upgradeJournalName), b); err != nil {
		t.Fatal(err)
	}
	f.registered = false // simulate crash after bootout
	if exchange {
		if err := exchangeUpgrade(p, j.Stage); err != nil {
			t.Fatal(err)
		}
	}
	return p, j
}

func TestDistributionRecoversInterruptedUpgradeBeforeRetry(t *testing.T) {
	for _, exchanged := range []bool{false, true} {
		t.Run(fmt.Sprint(exchanged), func(t *testing.T) {
			f := newUpgradeFixture(t)
			_, j := prepareInterruptedUpgrade(t, f, exchanged)
			p := f.plan(t)
			if !p.PendingRecovery {
				t.Fatal("interrupted transaction missing from preview")
			}
			result, err := InstallDistribution(p)
			if err != nil || !result.Updated || !f.registered {
				t.Fatalf("retry failed %+v %v", result, err)
			}
			actual, err := ownedDistributionFiles(p.RuntimeDir)
			if err != nil || !reflect.DeepEqual(actual, j.New) {
				t.Fatal("retry did not install new runtime", err)
			}
			if _, err := os.Stat(filepath.Join(p.StateDir, upgradeJournalName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("journal survived successful retry")
			}
		})
	}
}

func TestUpgradeJournalPublicationFailureRetainsRecoveryMaterial(t *testing.T) {
	f := newUpgradeFixture(t)
	p, j := prepareInterruptedUpgrade(t, f, false)
	path := filepath.Join(p.StateDir, upgradeJournalName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(j)
	err := publishUpgradeJournal(p, j, b, func(path string, data []byte) error {
		if err := writeNewServiceFile(path, data); err != nil {
			t.Fatal(err)
		}
		return errors.New("synthetic directory fsync failure after publication")
	})
	if err == nil {
		t.Fatal("expected publication error")
	}
	if _, err := inspectUpgradeJournal(p); err != nil {
		t.Fatal("published journal lost recovery material", err)
	}
	if _, err := InstallDistribution(f.plan(t)); err != nil {
		t.Fatal(err)
	}
}

func TestDistributionRemoveCannotInvalidatePendingRecovery(t *testing.T) {
	f := newUpgradeFixture(t)
	_, _ = prepareInterruptedUpgrade(t, f, true)
	p := f.plan(t)
	if _, err := UninstallDistribution(p); err == nil || !strings.Contains(err.Error(), "needs recovery") {
		t.Fatalf("removal must refuse pending recovery: %v", err)
	}
	if _, err := inspectUpgradeJournal(p); err != nil {
		t.Fatal("removal damaged recovery files", err)
	}
	if _, err := InstallDistribution(f.plan(t)); err != nil {
		t.Fatal("recovery failed after refused removal", err)
	}
}

func TestDistributionUpgradeRejectsChangedBackupAndHooks(t *testing.T) {
	for _, kind := range []string{"backup", "hooks"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpgradeFixture(t)
			var p DistributionPlan
			if kind == "backup" {
				var j upgradeJournal
				p, j = prepareInterruptedUpgrade(t, f, true)
				if err := os.WriteFile(filepath.Join(p.StateDir, j.Stage, "laodi"), []byte("foreign edit"), 0700); err != nil {
					t.Fatal(err)
				}
				if _, err := PlanDistribution(f.options); err == nil {
					t.Fatal("tampered backup accepted")
				}
			} else {
				f.newSource(t)
				p = f.plan(t)
				path := filepath.Join(f.options.Home, ".zcode/cli/config.json")
				b, _ := os.ReadFile(path)
				var c map[string]any
				json.Unmarshal(b, &c)
				c["hooks"].(map[string]any)["enabled"] = false
				b, _ = json.Marshal(c)
				os.WriteFile(path, b, 0600)
				if _, err := InstallDistribution(p); err == nil {
					t.Fatal("disabled hooks silently enabled")
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(after, b) {
					t.Fatal("user hook configuration rewritten")
				}
			}
		})
	}
}
