//go:build windows

package laodi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const windowsNotificationPreferenceName = "windows-notifications.json"

func validateWindowsNotificationUpgrade(dir string, manifest WindowsManifest) error {
	registered, pending, err := windowsNotificationRegistrationPresent(dir)
	if err != nil {
		return err
	}
	if (registered || pending) && !hashName.MatchString(manifest.Files["LaodiNotify.exe"]) {
		return errors.New("owned notification registration requires a helper in the selected version; remove the registration before installing a payload without it")
	}
	if hashName.MatchString(manifest.Files["LaodiNotify.exe"]) {
		// Registration is deferred until health succeeds, but an incompatible
		// stable asset must be rejected before the version pointer changes. A
		// prior receipt binds this hash and cleanup still needs the old asset.
		logo, err := readWindowsPrivatePayloadFile(dir, "laodi-logo.png")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && serviceHash(logo) != manifest.Files["laodi-logo.png"] {
			return errors.New("stable notification artwork changed; explicit migration required")
		}
	}
	return nil
}

func windowsNotificationRegistrationPresent(dir string) (registered, pending bool, err error) {
	for _, name := range []string{"notification-install.json", "notification-install.pending.json"} {
		_, readErr := readServiceFile(filepath.Join(dir, name))
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return false, false, readErr
		}
		if name == "notification-install.json" {
			registered = true
		} else {
			pending = true
		}
	}
	return registered, pending, nil
}

type windowsNotificationPreference struct {
	Schema  int  `json:"schema"`
	Enabled bool `json:"enabled"`
}

// The activation target and artwork remain stable while the implementation is
// selected from the verified immutable version. No running helper is replaced.
func ensureWindowsNotificationLogo(dir string, manifest WindowsManifest) error {
	data, err := readWindowsPrivatePayloadFile(filepath.Join(dir, "versions", manifest.Version), "laodi-logo.png")
	if err != nil || serviceHash(data) != manifest.Files["laodi-logo.png"] {
		return errors.New("notification artwork is not the verified payload asset")
	}
	existing, err := readWindowsPrivatePayloadFile(dir, "laodi-logo.png")
	if errors.Is(err, os.ErrNotExist) {
		return writeWindowsBytes(dir, "laodi-logo.png", data, false)
	}
	if err != nil {
		return err
	}
	if serviceHash(existing) != serviceHash(data) {
		return errors.New("stable notification artwork changed; explicit migration required")
	}
	return nil
}

func windowsNotificationsEnabled(dir string) (bool, error) {
	data, err := readServiceFile(filepath.Join(dir, windowsNotificationPreferenceName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var p windowsNotificationPreference
	if err := decodeWindowsRecord(data, &p); err != nil || p.Schema != 1 {
		return false, errors.New("invalid notification preference; retained")
	}
	return p.Enabled, nil
}

// Only installed Windows monitors use this provider. Development Watch callers
// and the macOS backend keep their explicit notifier behavior.
func ConfiguredWindowsNotifier(dir string) string {
	enabled, err := windowsNotificationsEnabled(dir)
	if err != nil || !enabled {
		return ""
	}
	if _, err := readServiceFile(filepath.Join(dir, "notification-install.json")); err != nil {
		return ""
	}
	return filepath.Join(dir, "laodi-host.exe")
}

func WindowsNotificationTarget(dir string) (string, error) {
	c, err := readWindowsCurrent(dir)
	if err != nil {
		return "", err
	}
	if !hashName.MatchString(c.Manifest.Files["LaodiNotify.exe"]) {
		return "", errors.New("installed version has no Windows notification helper")
	}
	logo, err := readWindowsPrivatePayloadFile(dir, "laodi-logo.png")
	if err != nil || serviceHash(logo) != c.Manifest.Files["laodi-logo.png"] {
		return "", errors.New("stable notification artwork is missing or changed")
	}
	return filepath.Join(dir, "versions", c.Manifest.Version, "LaodiNotify.exe"), nil
}

func WindowsInstalledRoot(executable string) string {
	dir := filepath.Dir(executable)
	if filepath.Base(filepath.Dir(dir)) != "versions" {
		return ""
	}
	root := filepath.Dir(filepath.Dir(dir))
	c, err := readWindowsCurrent(root)
	if err != nil || c.Manifest.Version != filepath.Base(dir) {
		return ""
	}
	return root
}

func prepareWindowsNotifications(p DistributionPlan, manifest WindowsManifest, result *DistributionResult) error {
	unlock, err := AcquireLock(filepath.Join(p.StateDir, "notification-management"))
	if err != nil {
		return err
	}
	defer unlock()
	enabled, err := windowsNotificationsEnabled(p.StateDir)
	if err != nil {
		return err
	}
	// The stable asset is part of the installation, even when registration is
	// deferred. An explicit later registration can then use the verified route
	// without enabling automatic event delivery as a side effect.
	if hashName.MatchString(manifest.Files["LaodiNotify.exe"]) {
		if err := ensureWindowsNotificationLogo(p.StateDir, manifest); err != nil {
			return err
		}
	}
	if !p.RequestNotifications {
		result.NotificationStatus = "not_requested"
		if enabled {
			result.NotificationStatus = "unchanged"
		}
		return nil
	}
	if !hashName.MatchString(manifest.Files["LaodiNotify.exe"]) {
		result.NotificationStatus = "not_available"
		result.Warnings = append(result.Warnings, "windows_notification_helper_missing")
		return nil
	}
	helper := filepath.Join(p.StateDir, "versions", manifest.Version, "LaodiNotify.exe")
	call := func(action string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		return p.notifierRunner(ctx, helper, action)
	}
	data, err := call("--register")
	var registration struct {
		OK bool `json:"ok"`
	}
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &registration) != nil || !registration.OK {
		result.NotificationStatus = "registration_failed"
		result.Warnings = append(result.Warnings, "windows_notification_registration_failed_local_events_retained")
		return nil
	}
	if err := writeWindowsRecord(p.StateDir, windowsNotificationPreferenceName, windowsNotificationPreference{Schema: 1, Enabled: true}); err != nil {
		return err
	}
	data, err = call("--status")
	var status struct {
		OK            bool   `json:"ok"`
		Authorization string `json:"authorization"`
	}
	result.NotificationStatus = "registered_status_unknown"
	if err == nil && len(data) <= 16384 && json.Unmarshal(data, &status) == nil && status.OK {
		switch status.Authorization {
		case "enabled", "disabled_for_application", "disabled_for_user", "disabled_by_policy", "disabled_by_manifest":
			result.NotificationStatus = status.Authorization
		}
	}
	result.Warnings = append(result.Warnings, "notification_registered_visibility_unconfirmed")
	return nil
}

func removeWindowsNotifications(p DistributionPlan) error {
	unlock, err := AcquireLock(filepath.Join(p.StateDir, "notification-management"))
	if err != nil {
		return err
	}
	defer unlock()
	registered, pending, err := windowsNotificationRegistrationPresent(p.StateDir)
	if err != nil {
		return err
	}
	if !registered && !pending {
		return nil
	}
	helper, err := WindowsNotificationTarget(p.StateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	data, err := p.notifierRunner(ctx, helper, "--unregister")
	var response struct {
		OK bool `json:"ok"`
	}
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &response) != nil || !response.OK {
		return errors.New("notification registration could not be removed with ownership checks; retained")
	}
	return writeWindowsRecord(p.StateDir, windowsNotificationPreferenceName, windowsNotificationPreference{Schema: 1, Enabled: false})
}

// ManageWindowsNotifications is an explicit user command. Status and template
// validation never register or send; a synthetic send requires action=test.
func ManageWindowsNotifications(ctx context.Context, dir, action string) ([]byte, error) {
	registering := action == "enable" || action == "register"
	mutating := registering || action == "disable" || action == "unregister"
	if registering {
		if err := verifyWindowsInstallationStatePath(dir); err != nil {
			return nil, err
		}
		if err := verifyWindowsInstallationFile(filepath.Join(dir, "laodi-host.exe")); err != nil {
			return nil, err
		}
	}
	if action == "status" {
		registered, pending, err := windowsNotificationRegistrationPresent(dir)
		if err != nil {
			return nil, err
		}
		if pending {
			return []byte(`{"schema_version":1,"action":"status","ok":false,"delivery":"not_requested","authorization":"recovery_pending","visible_to_user":"unconfirmed"}`), nil
		}
		if !registered {
			return []byte(`{"schema_version":1,"action":"status","ok":true,"delivery":"not_requested","authorization":"not_registered","visible_to_user":"unconfirmed"}`), nil
		}
	}
	if mutating {
		// Serialize explicit registration with the current-version transaction.
		// Taking these locks in the installer's order prevents a concurrent
		// registration from attaching itself to a version being replaced by a
		// payload that has no notification helper.
		releaseDistribution, err := AcquireLock(filepath.Join(dir, "distribution-management"))
		if err != nil {
			return nil, err
		}
		defer releaseDistribution()
		unlock, err := AcquireLock(filepath.Join(dir, "notification-management"))
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	if registering {
		if _, err := os.Lstat(filepath.Join(dir, windowsJournalName)); err == nil {
			return nil, errors.New("Windows upgrade recovery is pending; complete installation recovery before registering notifications")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	c, err := readWindowsCurrent(dir)
	if err != nil {
		return nil, err
	}
	if registering {
		if err := ensureWindowsNotificationLogo(dir, c.Manifest); err != nil {
			return nil, err
		}
	}
	helper, err := WindowsNotificationTarget(dir)
	if err != nil {
		return nil, err
	}
	args := []string{"--" + action}
	switch action {
	case "enable", "register":
		args = []string{"--register"}
	case "disable", "unregister":
		args = []string{"--unregister"}
	case "test":
		args = []string{"--send", "--id", "synthetic-visible-notice", "--kind", "test"}
	case "status", "test-template", "test-activation":
	default:
		return nil, fmt.Errorf("unknown notification action %q", action)
	}
	data, err := RunWindowsNotificationHelper(ctx, helper, dir, args...)
	if err == nil && (action == "enable" || action == "disable" || action == "unregister") {
		err = writeWindowsRecord(dir, windowsNotificationPreferenceName, windowsNotificationPreference{Schema: 1, Enabled: action == "enable"})
	}
	return data, err
}
