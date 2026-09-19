//go:build windows

package laodi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const windowsManifestName = "windows-manifest.json"
const windowsCurrentName = "current-version.json"
const windowsJournalName = "windows-upgrade.json"

var errWindowsInstallationIncomplete = errors.New("Windows installation record references missing or invalid files")

var windowsVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func validWindowsReleaseVersion(tag string) bool {
	if len(tag) > 128 || !windowsVersion.MatchString(tag) {
		return false
	}
	if _, suffix, ok := strings.Cut(tag, "-"); ok {
		for _, part := range strings.Split(suffix, ".") {
			if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
				return false
			}
		}
	}
	return true
}

// Protocol 1 deliberately keeps the two stable entry points immutable. Updating
// these launchers requires a future, separately verified protocol migration.
type WindowsManifest struct {
	Schema   int               `json:"schema"`
	Version  string            `json:"version"`
	Protocol int               `json:"protocol"`
	Files    map[string]string `json:"files"`
}
type windowsCurrent struct {
	Manifest  WindowsManifest   `json:"manifest"`
	Launchers map[string]string `json:"launchers"`
}
type windowsJournal struct {
	Schema int            `json:"schema"`
	Old    windowsCurrent `json:"old"`
	New    windowsCurrent `json:"new"`
}

func decodeWindowsRecord(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing record data")
	}
	return nil
}
func readWindowsSource(root *os.Root, name string) ([]byte, error) {
	full, err := windowsPrivatePath(filepath.Join(root.Name(), name))
	if err != nil {
		return nil, err
	}
	if err = windowsNoReparseAncestors(full); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("payload must be a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = windowsCheckFileIdentity(syscall.Handle(f.Fd()), false); err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("payload identity changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxDistributionFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDistributionFile {
		return nil, errors.New("oversized payload")
	}
	return b, nil
}
func validWindowsManifest(m WindowsManifest) bool {
	if m.Schema != 1 || m.Protocol != 1 || !validWindowsReleaseVersion(m.Version) || len(m.Files) < 4 || len(m.Files) > 32 {
		return false
	}
	for _, name := range []string{"laodi.exe", "laodi-host.exe", "laodi-logo.png", "SKILL.md"} {
		if !hashName.MatchString(m.Files[name]) {
			return false
		}
	}
	for name, hash := range m.Files {
		if name != "laodi.exe" && name != "laodi-host.exe" && name != "laodi-logo.png" && name != "SKILL.md" && name != "usage.md" && name != "LaodiNotify.exe" {
			return false
		}
		if !hashName.MatchString(hash) {
			return false
		}
	}
	return true
}
func readWindowsPayload(dir string) (WindowsManifest, error) {
	var m WindowsManifest
	root, err := os.OpenRoot(dir)
	if err != nil {
		return m, err
	}
	defer root.Close()
	data, err := distributionRead(root, windowsManifestName)
	if err != nil {
		return m, err
	}
	if len(data) > maxServiceFileBytes {
		return m, errors.New("oversized Windows manifest")
	}
	if err = decodeWindowsRecord(data, &m); err != nil || !validWindowsManifest(m) {
		return m, errors.New("invalid Windows payload manifest or launcher protocol")
	}
	entries, err := readDir(root, ".", 40)
	if err != nil {
		return m, err
	}
	if len(entries) != len(m.Files)+1 {
		return m, errors.New("unexpected Windows payload entry")
	}
	var size int
	for name, want := range m.Files {
		data, err = distributionRead(root, name)
		if err != nil {
			return m, err
		}
		size += len(data)
		if size > maxDistributionTotal || serviceHash(data) != want {
			return m, errors.New("Windows payload checksum mismatch")
		}
	}
	return m, nil
}
func readWindowsCurrent(dir string) (windowsCurrent, error) {
	var c windowsCurrent
	data, err := readServiceFile(filepath.Join(dir, windowsCurrentName))
	if err != nil {
		return c, err
	}
	if err = decodeWindowsRecord(data, &c); err != nil {
		return c, err
	}
	if err := verifyWindowsCurrentRecord(dir, c); err != nil {
		return c, err
	}
	return c, nil
}

func verifyWindowsCurrentRecord(dir string, c windowsCurrent) error {
	if !validWindowsManifest(c.Manifest) || len(c.Launchers) != 2 {
		return errors.New("invalid current version record")
	}
	for _, name := range []string{"laodi.exe", "laodi-host.exe"} {
		data, err := readWindowsPrivatePayloadFile(dir, name)
		if err != nil {
			// Only an absent current-version record means a fresh installation.
			// Do not expose a dependency's ENOENT through errors.Is here.
			return fmt.Errorf("%w: stable launcher %s: %v", errWindowsInstallationIncomplete, name, err)
		}
		if !hashName.MatchString(c.Launchers[name]) || serviceHash(data) != c.Launchers[name] {
			return errors.New("stable launcher ownership changed")
		}
	}
	if err := verifyWindowsVersion(dir, c); err != nil {
		return fmt.Errorf("%w: selected version: %v", errWindowsInstallationIncomplete, err)
	}
	return nil
}
func readWindowsPrivatePayloadFile(dir, name string) ([]byte, error) {
	root, err := openStateRoot(dir, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openStateFile(root, name, os.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxDistributionFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDistributionFile {
		return nil, errors.New("oversized payload")
	}
	return b, nil
}
func verifyWindowsVersion(dir string, c windowsCurrent) error {
	if !validWindowsManifest(c.Manifest) {
		return errors.New("invalid version receipt")
	}
	vdir := filepath.Join(dir, "versions", c.Manifest.Version)
	root, err := openStateRoot(vdir, false)
	if err != nil {
		return err
	}
	root.Close()
	actual, err := readWindowsPayload(vdir)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, c.Manifest) {
		return errors.New("version receipt changed")
	}
	for name := range actual.Files {
		if _, err = readWindowsPrivatePayloadFile(vdir, name); err != nil {
			return err
		}
	}
	return nil
}
func writeWindowsRecord(dir, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeWindowsBytes(dir, name, append(data, '\n'), true)
}
func writeWindowsBytes(dir, name string, data []byte, replace bool) error {
	root, err := openStateRoot(dir, true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = checkRegularFile(root, name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !replace {
		if _, err = root.Lstat(name); err == nil {
			return os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	var suffix [16]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return err
	}
	temp := ".install-" + hex.EncodeToString(suffix[:]) + ".tmp"
	f, err := createPrivateFile(root, temp, os.O_WRONLY)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if !replace {
		return publishPrivateFile(root, temp, name, false)
	}
	return replaceStateFile(root, temp, name)
}
func planWindowsDistribution(options DistributionOptions) (DistributionPlan, error) {
	var err error
	if options.Home == "" {
		options.Home, err = os.UserHomeDir()
		if err != nil {
			return DistributionPlan{}, err
		}
	}
	if options.StateDir == "" {
		options.StateDir, err = DefaultStateDir(options.Home)
		if err != nil {
			return DistributionPlan{}, err
		}
	}
	if options.SourceDir == "" {
		exe, e := os.Executable()
		if e != nil {
			return DistributionPlan{}, e
		}
		options.SourceDir = filepath.Dir(exe)
	}
	for _, path := range []string{options.StateDir, options.SourceDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return DistributionPlan{}, errors.New("install paths must be clean absolute paths")
		}
	}
	p := DistributionPlan{SourceDir: options.SourceDir, StateDir: options.StateDir, RuntimeDir: filepath.Join(options.StateDir, "versions"), Executable: filepath.Join(options.StateDir, "laodi.exe"), HooksOnly: true, Adapters: []string{}, RequestNotifications: options.RequestNotifications, options: options, healthCheck: waitWindowsDistributionHealth, windowsService: windowsDistributionService, stopMonitor: StopMonitor}
	old, oldErr := readWindowsCurrent(options.StateDir)
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return p, oldErr
	}
	if options.Remove {
		if oldErr != nil {
			return p, oldErr
		}
		p.files = old.Manifest.Files
		p.FileCount = len(p.files)
		return p, nil
	}
	m, err := readWindowsPayload(options.SourceDir)
	if err != nil {
		return p, err
	}
	p.files = m.Files
	p.FileCount = len(m.Files) + 1
	p.Upgrade = oldErr == nil && !reflect.DeepEqual(old.Manifest, m)
	if oldErr == nil && old.Manifest.Version == m.Version && p.Upgrade {
		return p, errors.New("version directories are immutable; use a new version")
	}
	if _, err = os.Lstat(filepath.Join(options.StateDir, windowsJournalName)); err == nil {
		p.PendingRecovery = true
		if oldErr != nil {
			return p, errors.New("Windows recovery journal exists without a valid current record; retained")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return p, err
	}
	if _, err = LoadState(options.StateDir); err != nil {
		return p, err
	}
	return p, nil
}
func windowsDistributionService(p DistributionPlan) (ServicePlan, error) {
	c, err := readWindowsCurrent(p.StateDir)
	if err != nil {
		return ServicePlan{}, err
	}
	return PlanService(ServiceOptions{Executable: filepath.Join(p.StateDir, "laodi-host.exe"), MonitorExecutable: filepath.Join(p.StateDir, "versions", c.Manifest.Version, "laodi-host.exe"), StateDir: p.StateDir, Home: p.options.Home, HooksOnly: true})
}
func stageWindowsVersion(p DistributionPlan, m WindowsManifest) error {
	vdir := filepath.Join(p.StateDir, "versions", m.Version)
	if _, err := os.Lstat(vdir); err == nil {
		return verifyWindowsVersion(p.StateDir, windowsCurrent{Manifest: m})
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Stage under a random private sibling; failed writes never reserve the
	// immutable version name and a clean retry remains possible.
	versions, err := openStateRoot(filepath.Join(p.StateDir, "versions"), true)
	if err != nil {
		return err
	}
	defer versions.Close()
	unpin, err := windowsPinRoot(versions)
	if err != nil {
		return err
	}
	defer unpin()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	stageName := ".stage-" + hex.EncodeToString(nonce[:])
	stageDir := filepath.Join(versions.Name(), stageName)
	stage, err := openStateRoot(stageDir, true)
	if err != nil {
		return err
	}
	stage.Close()
	defer versions.RemoveAll(stageName)
	source, err := os.OpenRoot(p.SourceDir)
	if err != nil {
		return err
	}
	defer source.Close()
	for name := range m.Files {
		data, e := distributionRead(source, name)
		if e != nil {
			return e
		}
		if serviceHash(data) != m.Files[name] {
			return errors.New("source changed during staging")
		}
		if e = writeWindowsBytes(stageDir, name, data, false); e != nil {
			return e
		}
	}
	if err = writeWindowsRecord(stageDir, windowsManifestName, m); err != nil {
		return err
	}
	actual, err := readWindowsPayload(stageDir)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, m) {
		return errors.New("staged Windows payload changed")
	}
	for name := range m.Files {
		if _, err := readWindowsPrivatePayloadFile(stageDir, name); err != nil {
			return err
		}
	}
	if err := publishPrivateDirectory(versions, stageName, m.Version); err != nil {
		return err
	}
	return verifyWindowsVersion(p.StateDir, windowsCurrent{Manifest: m})
}
func recoverWindowsUpgrade(p DistributionPlan, service ServicePlan) error {
	data, err := readServiceFile(filepath.Join(p.StateDir, windowsJournalName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j windowsJournal
	if err = decodeWindowsRecord(data, &j); err != nil || j.Schema != 1 {
		return errors.New("invalid Windows recovery journal; retained")
	}
	if err = verifyWindowsCurrentRecord(p.StateDir, j.Old); err != nil {
		return err
	}
	if err = verifyWindowsCurrentRecord(p.StateDir, j.New); err != nil {
		return err
	}
	current, err := readWindowsCurrent(p.StateDir)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, j.Old) && !reflect.DeepEqual(current, j.New) {
		return errors.New("current version differs from recovery journal")
	}
	owned, err := inspectServiceOwnership(service)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("upgrade recovery requires the owned background task; recovery material retained")
	}
	if err = stopWindowsVersion(p, current); err != nil {
		return err
	}
	if err = writeWindowsRecord(p.StateDir, windowsCurrentName, j.Old); err != nil {
		return err
	}
	started := time.Now()
	if err = startWindowsService(service); err != nil {
		return err
	}
	if err = p.healthCheck(p.StateDir, started); err != nil {
		return err
	}
	return removeWindowsJournal(p.StateDir)
}
func stopWindowsVersion(p DistributionPlan, c windowsCurrent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := p.stopMonitor(ctx, p.StateDir, filepath.Join(p.StateDir, "versions", c.Manifest.Version, "laodi-host.exe"))
	if err != nil {
		// A crash or a completed graceful stop may leave no live process receipt.
		// Prove the writer lock is free before treating the monitor as stopped.
		unlock, lockErr := AcquireLock(p.StateDir)
		if lockErr != nil {
			return err
		}
		unlock()
	}
	service, planErr := p.windowsService(p)
	if planErr != nil {
		return planErr
	}
	return waitWindowsTaskStopped(service)
}
func removeWindowsJournal(dir string) error {
	root, err := openStateRoot(dir, false)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = checkRegularFile(root, windowsJournalName); err != nil {
		return err
	}
	return root.Remove(windowsJournalName)
}
func installWindowsDistribution(p DistributionPlan) (result DistributionResult, err error) {
	result.NotificationStatus = "not_configured"
	result.Warnings = []string{"windows_client_protocol_unverified_hooks_not_installed", "windows_notification_integration_not_verified"}
	root, err := openStateRoot(p.StateDir, true)
	if err != nil {
		return result, err
	}
	root.Close()
	unlock, err := AcquireLock(filepath.Join(p.StateDir, "distribution-management"))
	if err != nil {
		return result, err
	}
	defer unlock()
	m, err := readWindowsPayload(p.SourceDir)
	if err != nil {
		return result, err
	}
	old, oldErr := readWindowsCurrent(p.StateDir)
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return result, oldErr
	}
	// A plan can outlive another process. Re-read recovery presence under the
	// distribution lock before publishing any current-version record.
	if _, journalErr := os.Lstat(filepath.Join(p.StateDir, windowsJournalName)); journalErr == nil {
		p.PendingRecovery = true
		if oldErr != nil {
			return result, errors.New("Windows recovery journal exists without a valid current record; retained")
		}
	} else if !errors.Is(journalErr, os.ErrNotExist) {
		return result, journalErr
	} else {
		p.PendingRecovery = false
	}
	if err = stageWindowsVersion(p, m); err != nil {
		return result, err
	}
	next := windowsCurrent{Manifest: m, Launchers: old.Launchers}
	if oldErr != nil {
		next.Launchers = map[string]string{}
		for _, name := range []string{"laodi.exe", "laodi-host.exe"} {
			data, e := readWindowsPrivatePayloadFile(filepath.Join(p.StateDir, "versions", m.Version), name)
			if e != nil {
				return result, e
			}
			existing, readErr := readWindowsPrivatePayloadFile(p.StateDir, name)
			if readErr == nil {
				if serviceHash(existing) != m.Files[name] {
					return result, errors.New("partial installation launcher differs from requested payload; retained")
				}
			} else if errors.Is(readErr, os.ErrNotExist) {
				if e = writeWindowsBytes(p.StateDir, name, data, false); e != nil {
					return result, e
				}
			} else {
				return result, readErr
			}
			next.Launchers[name] = m.Files[name]
		}
		if err = writeWindowsRecord(p.StateDir, windowsCurrentName, next); err != nil {
			return result, err
		}
	}
	service, err := p.windowsService(p)
	if err != nil {
		return result, err
	}
	if p.PendingRecovery {
		// Serialize recovery with direct service/hook management just as for a
		// fresh upgrade. Release before InstallService, which takes this lock.
		management, lockErr := lockDistributionUpgrade(p)
		if lockErr != nil {
			return result, lockErr
		}
		err = recoverWindowsUpgrade(p, service)
		management()
		if err != nil {
			return result, err
		}
		old, err = readWindowsCurrent(p.StateDir)
		if err != nil {
			return result, err
		}
		next.Launchers = old.Launchers
	}
	if oldErr != nil || reflect.DeepEqual(old.Manifest, m) {
		result.RuntimeInstalled = true
		started := time.Now()
		if err = InstallService(service); err != nil {
			return result, fmt.Errorf("runtime staged; background NOT connected: %w", err)
		}
		healthAfter := started
		if oldErr == nil {
			// Same-version installs must not restart a healthy monitor. Its quiet
			// heartbeat persists every 30 seconds; asking for a newer heartbeat
			// inside the 15-second startup deadline would reject healthy installs.
			healthAfter = started.Add(-75 * time.Second)
		}
		if err = p.healthCheck(p.StateDir, healthAfter); err != nil {
			return result, fmt.Errorf("runtime staged; background heartbeat NOT verified: %w", err)
		}
		result.ServiceInstalled = true
		return result, nil
	}
	// Removal intentionally retains versions/history. Reinstalling a newer
	// package may therefore find a valid runtime with no task or task receipt.
	// Restore the owned old monitor before entering the usual rollback contract.
	owned, err := inspectServiceOwnership(service)
	if err != nil {
		return result, err
	}
	if !owned {
		started := time.Now()
		if err = InstallService(service); err != nil {
			return result, err
		}
		if err = p.healthCheck(p.StateDir, started); err != nil {
			return result, err
		}
	}
	management, err := lockDistributionUpgrade(p)
	if err != nil {
		return result, err
	}
	defer management()
	owned, err = inspectServiceOwnership(service)
	if err != nil {
		return result, err
	}
	if !owned {
		return result, errors.New("upgrade requires an owned background task")
	}
	journal := windowsJournal{Schema: 1, Old: old, New: next}
	if err = writeWindowsRecord(p.StateDir, windowsJournalName, journal); err != nil {
		return result, err
	}
	if err = stopWindowsVersion(p, old); err != nil {
		return result, err
	}
	if err = writeWindowsRecord(p.StateDir, windowsCurrentName, next); err == nil {
		started := time.Now()
		err = startWindowsService(service)
		if err == nil {
			err = p.healthCheck(p.StateDir, started)
		}
	}
	if err != nil {
		failure := err
		if recovery := recoverWindowsUpgrade(p, service); recovery != nil {
			return result, fmt.Errorf("upgrade failed: %v; recovery pending: %w", failure, recovery)
		}
		return result, fmt.Errorf("upgrade failed; old version restored: %w", failure)
	}
	if err = removeWindowsJournal(p.StateDir); err != nil {
		return result, err
	}
	result.RuntimeInstalled = true
	result.ServiceInstalled = true
	result.Updated = true
	return result, nil
}
func uninstallWindowsDistribution(p DistributionPlan) (DistributionResult, error) {
	r := DistributionResult{NotificationStatus: "not_configured"}
	unlock, err := AcquireLock(filepath.Join(p.StateDir, "distribution-management"))
	if err != nil {
		return r, err
	}
	defer unlock()
	if _, err = os.Lstat(filepath.Join(p.StateDir, windowsJournalName)); err == nil {
		return r, errors.New("upgrade recovery is pending; removal refused")
	} else if !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	c, err := readWindowsCurrent(p.StateDir)
	if err != nil {
		return r, err
	}
	service, err := p.windowsService(p)
	if err != nil {
		return r, err
	}
	if err = stopWindowsVersion(p, c); err != nil {
		return r, err
	}
	if err = UninstallService(service); err != nil {
		return r, err
	}
	r.Warnings = []string{"history_and_immutable_versions_retained", "no_verified_windows_hooks_or_notification_registration_installed"}
	return r, nil
}

// WindowsRoute verifies the protected selection and executable hashes before
// returning a version executable. Only stable entry points invoke this route.
func WindowsRoute(executable string) (string, error) {
	dir := filepath.Dir(executable)
	if _, err := os.Lstat(filepath.Join(dir, windowsCurrentName)); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	c, err := readWindowsCurrent(dir)
	if err != nil {
		return "", err
	}
	base := filepath.Base(executable)
	if base != "laodi.exe" && base != "laodi-host.exe" {
		return "", errors.New("unknown stable launcher")
	}
	return filepath.Join(dir, "versions", c.Manifest.Version, base), nil
}
