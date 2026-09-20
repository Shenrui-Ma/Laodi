package laodi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func windowsNotificationPayload(t *testing.T, version string) (string, WindowsManifest) {
	t.Helper()
	source, m := windowsPayloadFixture(t, version)
	data := []byte("synthetic notification helper " + version)
	if err := os.WriteFile(filepath.Join(source, "LaodiNotify.exe"), data, 0600); err != nil {
		t.Fatal(err)
	}
	m.Files["LaodiNotify.exe"] = serviceHash(data)
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(source, windowsManifestName), b, 0600); err != nil {
		t.Fatal(err)
	}
	return source, m
}

func TestWindowsDistributionRegistersNotifierWithoutSendingAndRemovesOnlyOwned(t *testing.T) {
	source, m := windowsNotificationPayload(t, "v0.4.0-notification")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	p := h.plan(t, source, state)
	p.RequestNotifications = true
	var calls []string
	p.notifierRunner = func(_ context.Context, helper string, args ...string) ([]byte, error) {
		if helper != filepath.Join(state, "versions", m.Version, "LaodiNotify.exe") {
			t.Fatal("helper did not select immutable verified version")
		}
		calls = append(calls, args[0])
		switch args[0] {
		case "--register":
			if err := writeWindowsRecord(state, "notification-install.json", map[string]int{"synthetic_receipt": 1}); err != nil {
				t.Fatal(err)
			}
		case "--unregister":
			root, err := openStateRoot(state, false)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.Remove("notification-install.json"); err != nil {
				t.Fatal(err)
			}
		case "--status":
		default:
			t.Fatalf("installer attempted notification send or unknown action: %v", args)
		}
		return []byte(`{"schema_version":1,"ok":true,"authorization":"enabled"}`), nil
	}
	r, err := InstallDistribution(p)
	if err != nil || !r.ServiceInstalled || r.NotificationStatus != "enabled" {
		t.Fatalf("notification integration failed: %+v %v", r, err)
	}
	if !reflect.DeepEqual(calls, []string{"--register", "--status"}) {
		t.Fatal(calls)
	}
	if ConfiguredWindowsNotifier(state) != filepath.Join(state, "laodi-host.exe") {
		t.Fatal("monitor has no stable notification route")
	}
	selected, err := WindowsNotificationTarget(state)
	if err != nil || selected != filepath.Join(state, "versions", m.Version, "LaodiNotify.exe") {
		t.Fatal("helper target is not verified", err)
	}
	if _, err := UninstallDistribution(p); err != nil {
		t.Fatal(err)
	}
	if ConfiguredWindowsNotifier(state) != "" {
		t.Fatal("removed notifier still configured")
	}
	if calls[len(calls)-1] != "--unregister" {
		t.Fatal("owned notification identity not removed")
	}
}

func TestWindowsNotificationAPIUnknownDoesNotClaimVisibleOrDisableEvents(t *testing.T) {
	source, _ := windowsNotificationPayload(t, "v0.4.0-notification-unknown")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	p := h.plan(t, source, state)
	p.RequestNotifications = true
	p.notifierRunner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "--register" {
			return []byte(`{"ok":true}`), nil
		}
		return []byte(`{"ok":true,"authorization":"unknown","authorization_error_code":"0x80070490"}`), nil
	}
	r, err := InstallDistribution(p)
	if err != nil || !r.ServiceInstalled || r.NotificationStatus != "registered_status_unknown" {
		t.Fatalf("unknown API state confused with successful delivery: %+v %v", r, err)
	}
	if enabled, err := windowsNotificationsEnabled(state); err != nil || !enabled {
		t.Fatal("registration preference lost", err)
	}
	if strings.Contains(r.NotificationStatus, "accepted") || strings.Contains(r.NotificationStatus, "visible") {
		t.Fatal("visibility was invented")
	}
}

func TestWindowsNotificationRegistrationFailureDoesNotClaimConnection(t *testing.T) {
	source, _ := windowsNotificationPayload(t, "v0.4.0-notification-failed")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	p := h.plan(t, source, state)
	p.RequestNotifications = true
	p.notifierRunner = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("foreign registration")
	}
	r, err := InstallDistribution(p)
	if err != nil || !r.ServiceInstalled || r.NotificationStatus != "registration_failed" {
		t.Fatalf("registration failure hid background status: %+v %v", r, err)
	}
	if enabled, err := windowsNotificationsEnabled(state); err != nil || enabled {
		t.Fatal("failed registration enabled delivery", err)
	}
}

func TestWindowsNotificationQueriesDoNotCreateStateOrRegistration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	b, err := ManageWindowsNotifications(context.Background(), dir, "status")
	if err != nil || !strings.Contains(string(b), "not_registered") {
		t.Fatal("fresh status not explicit", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status created state or registration")
	}
}

func TestWindowsNotificationPendingReceiptIsNotReportedRemoved(t *testing.T) {
	source, _ := windowsNotificationPayload(t, "v0.4.0-notification-pending")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	p := h.plan(t, source, state)
	if _, err := InstallDistribution(p); err != nil {
		t.Fatal(err)
	}
	c, err := readWindowsCurrent(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWindowsNotificationLogo(state, c.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeWindowsRecord(state, "notification-install.pending.json", map[string]int{"synthetic_pending": 1}); err != nil {
		t.Fatal(err)
	}
	b, err := ManageWindowsNotifications(context.Background(), state, "status")
	if err != nil || !strings.Contains(string(b), "recovery_pending") {
		t.Fatalf("pending registration hidden: %s %v", b, err)
	}
	called := false
	p.notifierRunner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) != 1 || args[0] != "--unregister" {
			t.Fatalf("unexpected pending cleanup: %v", args)
		}
		called = true
		return []byte(`{"ok":true}`), nil
	}
	if err := removeWindowsNotifications(p); err != nil || !called {
		t.Fatalf("pending cleanup skipped: %v", err)
	}
}
