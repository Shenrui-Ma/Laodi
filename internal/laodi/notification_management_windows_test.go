//go:build windows

package laodi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The subprocess supplies only the helper's JSON protocol. It never registers
// a Windows identity, invokes a notification API, or submits a toast.
func TestWindowsNotificationManagementProcess(t *testing.T) {
	action := os.Getenv("LAODI_NOTIFICATION_MANAGEMENT_TEST")
	if action == "" {
		return
	}
	if action != "register" && action != "unregister" {
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema_version": 1, "action": action,
		"ok":         os.Getenv("LAODI_NOTIFICATION_MANAGEMENT_FAIL") != "1",
		"error_code": "synthetic_failure",
	})
	os.Exit(0)
}

func TestWindowsNotificationMutationsRespectDistributionAndNotificationLocks(t *testing.T) {
	source, _ := windowsNotificationPayload(t, "v0.4.0-notification-locks")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, state)); err != nil {
		t.Fatal(err)
	}
	oldCommand := windowsNotificationCommand
	t.Cleanup(func() { windowsNotificationCommand = oldCommand })
	windowsNotificationCommand = func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("blocked management action launched a notification helper")
		return nil
	}
	for _, lock := range []string{"distribution-management", "notification-management"} {
		for _, action := range []string{"enable", "disable", "register", "unregister"} {
			t.Run(lock+"/"+action, func(t *testing.T) {
				unlock, err := AcquireLock(filepath.Join(state, lock))
				if err != nil {
					t.Fatal(err)
				}
				_, err = ManageWindowsNotifications(context.Background(), state, action)
				unlock()
				if !errors.Is(err, syscall.Errno(33)) {
					t.Fatalf("mutation was not excluded by management lock: %v", err)
				}
				// A failed second lock must not retain the first lock.
				for _, released := range []string{"distribution-management", "notification-management"} {
					u, err := AcquireLock(filepath.Join(state, released))
					if err != nil {
						t.Fatal("failed action leaked a management lock", err)
					}
					u()
				}
			})
		}
	}
	if _, err := readServiceFile(filepath.Join(state, windowsNotificationPreferenceName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("blocked mutation wrote notification preference", err)
	}
}

func TestWindowsNotificationManagementAliasesPreservePreferenceContract(t *testing.T) {
	source, manifest := windowsNotificationPayload(t, "v0.4.0-notification-aliases")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, state)); err != nil {
		t.Fatal(err)
	}
	type scenario struct {
		action       string
		before, want bool
		fail         bool
	}
	for _, test := range []scenario{
		{"register", false, false, false}, {"register", true, true, false},
		{"enable", false, true, false}, {"disable", true, false, false}, {"unregister", true, false, false},
		{"register", false, false, true}, {"enable", false, false, true},
		{"disable", true, true, true}, {"unregister", true, true, true},
	} {
		t.Run(test.action, func(t *testing.T) {
			if err := writeWindowsRecord(state, windowsNotificationPreferenceName, windowsNotificationPreference{Schema: 1, Enabled: test.before}); err != nil {
				t.Fatal(err)
			}
			oldCommand := windowsNotificationCommand
			t.Cleanup(func() { windowsNotificationCommand = oldCommand })
			calls := 0
			windowsNotificationCommand = func(ctx context.Context, helper string, args ...string) *exec.Cmd {
				calls++
				wantAction := "--register"
				if test.action == "disable" || test.action == "unregister" {
					wantAction = "--unregister"
				}
				if helper != filepath.Join(state, "versions", manifest.Version, "LaodiNotify.exe") || len(args) != 3 || args[0] != "--state-dir" || args[1] != state || args[2] != wantAction {
					t.Fatalf("unexpected helper route/argv: %s %q", helper, args)
				}
				for _, lock := range []string{"distribution-management", "notification-management"} {
					u, err := AcquireLock(filepath.Join(state, lock))
					if err == nil {
						u()
						t.Fatal("helper ran outside management lock", lock)
					}
					if !errors.Is(err, syscall.Errno(33)) {
						t.Fatal("unexpected management lock failure", err)
					}
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWindowsNotificationManagementProcess$")
				cmd.Env = append(os.Environ(), "LAODI_NOTIFICATION_MANAGEMENT_TEST="+strings.TrimPrefix(wantAction, "--"))
				if test.fail {
					cmd.Env = append(cmd.Env, "LAODI_NOTIFICATION_MANAGEMENT_FAIL=1")
				}
				return cmd
			}
			_, err := ManageWindowsNotifications(context.Background(), state, test.action)
			if (err != nil) != test.fail || calls != 1 {
				t.Fatalf("wrong management result: calls=%d err=%v", calls, err)
			}
			got, err := windowsNotificationsEnabled(state)
			if err != nil || got != test.want {
				t.Fatalf("preference changed contrary to action result: got %v want %v: %v", got, test.want, err)
			}
		})
	}
}

func TestWindowsPendingUpgradeRefusesRegistrationButAllowsOwnedCleanup(t *testing.T) {
	source, _ := windowsNotificationPayload(t, "v0.4.0-notification-pending-upgrade")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, state)); err != nil {
		t.Fatal(err)
	}
	if err := writeWindowsRecord(state, windowsJournalName, map[string]int{"synthetic_pending_upgrade": 1}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"enable", "register", "disable", "unregister"} {
		t.Run(action, func(t *testing.T) {
			if err := writeWindowsRecord(state, windowsNotificationPreferenceName, windowsNotificationPreference{Schema: 1, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			oldCommand := windowsNotificationCommand
			t.Cleanup(func() { windowsNotificationCommand = oldCommand })
			calls := 0
			windowsNotificationCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
				calls++
				if len(args) != 3 || args[2] != "--unregister" {
					t.Fatalf("pending upgrade attempted new registration: %q", args)
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWindowsNotificationManagementProcess$")
				cmd.Env = append(os.Environ(), "LAODI_NOTIFICATION_MANAGEMENT_TEST=unregister")
				return cmd
			}
			_, err := ManageWindowsNotifications(context.Background(), state, action)
			registering := action == "enable" || action == "register"
			if registering {
				if err == nil || !strings.Contains(err.Error(), "recovery is pending") || calls != 0 {
					t.Fatalf("pending upgrade admitted registration: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("pending upgrade blocked ownership-checked cleanup: calls=%d err=%v", calls, err)
			}
			enabled, err := windowsNotificationsEnabled(state)
			if err != nil || enabled != registering {
				t.Fatalf("wrong preference after pending action: %v %v", enabled, err)
			}
			if _, err := readServiceFile(filepath.Join(state, windowsJournalName)); err != nil {
				t.Fatal("notification management changed recovery journal", err)
			}
		})
	}
}
