//go:build windows

package laodi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsFailedUpgradeDoesNotRegisterNewNotificationHelper(t *testing.T) {
	a, am := windowsPayloadFixture(t, "v0.4.0-notification-old")
	b, _ := windowsNotificationPayload(t, "v0.4.0-notification-new")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	p := h.plan(t, b, state)
	p.RequestNotifications = true
	calls := 0
	p.notifierRunner = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte(`{"ok":true,"authorization":"enabled"}`), nil
	}
	h.failHealth = true
	result, err := InstallDistribution(p)
	if err == nil || !strings.Contains(err.Error(), "old version restored") {
		t.Fatalf("upgrade did not restore old version: %+v %v", result, err)
	}
	if calls != 0 {
		t.Fatalf("unhealthy upgrade invoked notification registration %d times", calls)
	}
	current, err := readWindowsCurrent(state)
	if err != nil || current.Manifest.Version != am.Version {
		t.Fatal("old version was not restored", err)
	}
	for _, name := range []string{"notification-install.json", "notification-install.pending.json", windowsNotificationPreferenceName} {
		if _, err := readServiceFile(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed upgrade changed notification ownership or preference: %s: %v", name, err)
		}
	}
}

func TestWindowsNotificationReceiptPreventsDroppingCleanupHelper(t *testing.T) {
	for _, receipt := range []string{"notification-install.json", "notification-install.pending.json"} {
		t.Run(receipt, func(t *testing.T) {
			a, _ := windowsNotificationPayload(t, "v0.4.0-notification-existing")
			b, _ := windowsPayloadFixture(t, "v0.4.0-notification-absent")
			state := filepath.Join(t.TempDir(), "state")
			h := &windowsDistributionHarness{}
			if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
				t.Fatal(err)
			}
			// Generate the plan before an explicit concurrent registration. The
			// installer must repeat the guard under its distribution lock.
			stale := h.plan(t, b, state)
			if err := writeWindowsRecord(state, receipt, map[string]int{"synthetic_receipt": 1}); err != nil {
				t.Fatal(err)
			}
			before, err := readServiceFile(filepath.Join(state, windowsCurrentName))
			if err != nil {
				t.Fatal(err)
			}
			starts, stops := h.starts, h.stops
			_, err = PlanDistribution(DistributionOptions{SourceDir: b, StateDir: state, Home: filepath.Dir(state), AppCandidates: []string{}})
			if err == nil {
				t.Fatal("planner accepted losing the registered notification cleanup helper")
			}
			if _, err := InstallDistribution(stale); err == nil {
				t.Fatal("stale plan bypassed notification cleanup helper guard")
			}
			after, err := readServiceFile(filepath.Join(state, windowsCurrentName))
			if err != nil || !bytes.Equal(before, after) || h.starts != starts || h.stops != stops {
				t.Fatal("refused upgrade changed current version or background task", err)
			}
			if _, err := readServiceFile(filepath.Join(state, receipt)); err != nil {
				t.Fatal("refused upgrade changed registration receipt", err)
			}
		})
	}
}

func TestWindowsNotificationArtworkChangeIsRejectedBeforeVersionSwitch(t *testing.T) {
	a, _ := windowsNotificationPayload(t, "v0.4.0-notification-artwork-old")
	b, bm := windowsNotificationPayload(t, "v0.4.0-notification-artwork-new")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	// The plan predates the older installation publishing its stable artwork.
	stale := h.plan(t, b, state)
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	if err := writeWindowsRecord(state, "notification-install.json", map[string]int{"synthetic_receipt": 1}); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{windowsCurrentName, "laodi-logo.png", "notification-install.json"} {
		data, err := readServiceFile(filepath.Join(state, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	starts, stops := h.starts, h.stops
	_, err := PlanDistribution(DistributionOptions{SourceDir: b, StateDir: state, Home: filepath.Dir(state), AppCandidates: []string{}})
	if err == nil || !strings.Contains(err.Error(), "artwork changed") {
		t.Fatalf("planner accepted incompatible stable artwork: %v", err)
	}
	if _, err := InstallDistribution(stale); err == nil || !strings.Contains(err.Error(), "artwork changed") {
		t.Fatalf("stale plan bypassed stable artwork preflight: %v", err)
	}
	for name, want := range before {
		got, err := readServiceFile(filepath.Join(state, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("refused artwork upgrade changed %s: %v", name, err)
		}
	}
	if h.starts != starts || h.stops != stops {
		t.Fatal("refused artwork upgrade changed background task")
	}
	if _, err := os.Stat(filepath.Join(state, "versions", bm.Version)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("artwork failure was detected only after staging", err)
	}
}
