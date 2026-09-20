package laodi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
)

const distributionReceiptName = ".distribution.json"
const maxDistributionFile = 64 << 20
const maxDistributionTotal = 128 << 20

// DistributionOptions controls a local, user-owned release installation. No
// network download, privileged operation, or client restart is performed.
type DistributionOptions struct {
	SourceDir            string
	Home                 string
	StateDir             string
	RequestNotifications bool
	Remove               bool // plan explicit removal without requiring installed tools to remain present
	LookPath             func(string) (string, error)
	AppCandidates        []string // nil selects standard application locations
	ClientExecutable     string   // Windows: explicit reviewed client executable
}

type DistributionPlan struct {
	SourceDir            string                `json:"source_dir"`
	StateDir             string                `json:"state_dir"`
	RuntimeDir           string                `json:"runtime_dir"`
	Executable           string                `json:"executable"`
	Notifier             string                `json:"notifier"`
	Adapters             []string              `json:"adapters"`
	App                  string                `json:"app,omitempty"`
	HooksOnly            bool                  `json:"hooks_only"`
	FileCount            int                   `json:"file_count"`
	RequestNotifications bool                  `json:"request_notifications"`
	Upgrade              bool                  `json:"upgrade"`
	PendingRecovery      bool                  `json:"pending_recovery"`
	WindowsClients       []WindowsHookContract `json:"windows_clients,omitempty"`
	options              DistributionOptions
	files                map[string]string
	serviceRunner        func(context.Context, ...string) error
	notifierRunner       func(context.Context, string, ...string) ([]byte, error)
	healthCheck          func(string, time.Time) error
	windowsService       func(DistributionPlan) (ServicePlan, error)
	stopMonitor          func(context.Context, string, string) error
}

type DistributionResult struct {
	RuntimeInstalled   bool     `json:"runtime_installed"`
	HookAdapters       []string `json:"hook_adapters"`
	ServiceInstalled   bool     `json:"service_installed"`
	NotificationStatus string   `json:"notification_status"`
	Updated            bool     `json:"updated"`
	Warnings           []string `json:"warnings,omitempty"`
}

type distributionReceipt struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

// PlanDistribution validates only local paths, file hashes, and installation
// ownership. It never reads credentials or invokes launchctl/notification APIs.
func PlanDistribution(options DistributionOptions) (DistributionPlan, error) {
	if runtime.GOOS == "windows" {
		return planWindowsDistribution(options)
	}
	if runtime.GOOS != "darwin" {
		return DistributionPlan{}, errors.New("release installation currently supports macOS only")
	}
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		return DistributionPlan{}, errors.New("run Laodi install as your normal user, without sudo")
	}
	var err error
	if options.Home == "" {
		options.Home, err = os.UserHomeDir()
		if err != nil {
			return DistributionPlan{}, err
		}
	}
	if options.SourceDir == "" {
		executable, err := os.Executable()
		if err != nil {
			return DistributionPlan{}, err
		}
		options.SourceDir = filepath.Dir(executable)
	}
	if options.StateDir == "" {
		options.StateDir = filepath.Join(options.Home, "Library", "Application Support", "Laodi-skills")
	}
	for _, path := range []string{options.SourceDir, options.Home, options.StateDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return DistributionPlan{}, errors.New("installation paths must be clean absolute paths")
		}
	}
	if err := distributionDirectory(options.Home); err != nil {
		return DistributionPlan{}, err
	}
	if err := distributionDirectory(options.SourceDir); err != nil {
		return DistributionPlan{}, err
	}
	if err := checkHookPath(options.Home, filepath.Join(options.StateDir, "runtime", distributionReceiptName)); err != nil {
		return DistributionPlan{}, err
	}
	files, err := distributionFiles(options.SourceDir, false)
	if err != nil {
		return DistributionPlan{}, err
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	candidates := options.AppCandidates
	if candidates == nil {
		candidates = []string{"/Applications/ZCode.app", filepath.Join(options.Home, "Applications", "ZCode.app")}
	}
	app := ""
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate || strings.ContainsAny(candidate, "\x00\r\n") {
			return DistributionPlan{}, errors.New("application candidates must be absolute")
		}
		if distributionAppExists(candidate) {
			app = candidate
			break
		}
	}
	adapters := []string{}
	if app != "" || distributionOnPath(options.LookPath, "zcode") {
		adapters = append(adapters, "zcode")
	}
	if distributionOnPath(options.LookPath, "claude") {
		adapters = append(adapters, "claude-code")
	}
	runtimeDir := filepath.Join(options.StateDir, "runtime")
	plan := DistributionPlan{SourceDir: options.SourceDir, StateDir: options.StateDir, RuntimeDir: runtimeDir,
		Executable: filepath.Join(runtimeDir, "laodi"), Notifier: filepath.Join(runtimeDir, "LaodiNotify.app", "Contents", "MacOS", "LaodiNotify"),
		Adapters: adapters, App: app, HooksOnly: app == "", FileCount: len(files), RequestNotifications: options.RequestNotifications,
		options: options, files: files, serviceRunner: runLaunchctl, notifierRunner: runDistributionNotifier, healthCheck: waitDistributionHealth}
	if installed, err := ownedDistributionFiles(runtimeDir); err == nil {
		plan.Upgrade = !reflect.DeepEqual(installed, files)
	} else if !errors.Is(err, os.ErrNotExist) {
		return DistributionPlan{}, err
	}
	if _, err := os.Lstat(filepath.Join(options.StateDir, upgradeJournalName)); err == nil {
		plan.PendingRecovery = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return DistributionPlan{}, err
	}
	if err := preflightDistribution(plan); err != nil {
		return DistributionPlan{}, err
	}
	return plan, nil
}

func distributionDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return errors.New("installation directory is missing, symbolic, or not a directory")
	}
	return nil
}
func distributionAppExists(path string) bool {
	if err := distributionDirectory(path); err != nil {
		return false
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Lstat("Contents/MacOS/ZCode")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return false
	}
	info, err = root.Lstat("Contents/Info.plist")
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := root.Open("Contents/Info.plist")
	if err != nil {
		return false
	}
	return file.Close() == nil
}
func distributionOnPath(lookup func(string) (string, error), name string) bool {
	path, err := lookup(name)
	if err != nil || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func distributionFiles(dir string, installed bool) (map[string]string, error) {
	if installed {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || !distributionPrivateMode(info, 0700) {
			return nil, errors.New("installed runtime directory permissions must remain 0700 without special bits")
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := make(map[string]string)
	var total int64
	visited := 0
	var visit func(string) error
	visit = func(name string) error {
		visited++
		if visited > 256 || strings.Count(name, string(filepath.Separator)) > 16 {
			return errors.New("release payload exceeds directory limit")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if installed && !distributionPrivateMode(info, 0700) {
				return errors.New("installed runtime directory permissions must remain 0700 without special bits")
			}
			f, err := root.Open(name)
			if err != nil {
				return err
			}
			entries, err := f.ReadDir(129)
			f.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if len(entries) > 128 {
				return errors.New("release payload exceeds file limit")
			}
			for _, entry := range entries {
				if err := visit(filepath.Join(name, entry.Name())); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("release payload must contain only regular files and directories")
		}
		if installed {
			mode := os.FileMode(0600)
			if name == "laodi" || name == filepath.FromSlash("LaodiNotify.app/Contents/MacOS/LaodiNotify") {
				mode = 0700
			}
			if !distributionPrivateMode(info, mode) {
				return errors.New("installed runtime file permissions changed or contain special bits")
			}
		}
		if len(files) >= 128 || info.Size() > maxDistributionFile || total+info.Size() > maxDistributionTotal {
			return errors.New("release payload exceeds size limit")
		}
		data, err := distributionRead(root, name)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxDistributionTotal {
			return errors.New("release payload exceeds size limit")
		}
		files[name] = serviceHash(data)
		return nil
	}
	if installed {
		info, err := root.Lstat(distributionReceiptName)
		if err != nil || !info.Mode().IsRegular() || !distributionPrivateMode(info, 0600) {
			return nil, errors.New("installed runtime receipt permissions must remain 0600 without special bits")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() == distributionReceiptName {
				continue
			}
			if entry.Name() != "laodi" && entry.Name() != "LaodiNotify.app" && entry.Name() != "skills" {
				return nil, errors.New("installed runtime contains unknown files")
			}
		}
	}
	for _, name := range []string{"laodi", "LaodiNotify.app", "skills"} {
		if err := visit(name); err != nil {
			return nil, fmt.Errorf("invalid release payload: %w", err)
		}
	}
	for _, name := range []string{"laodi", "LaodiNotify.app/Contents/Info.plist", "LaodiNotify.app/Contents/MacOS/LaodiNotify", "skills/laodi/SKILL.md"} {
		if _, ok := files[filepath.FromSlash(name)]; !ok {
			return nil, errors.New("release payload is missing a required file")
		}
	}
	for _, name := range []string{"laodi", "LaodiNotify.app/Contents/MacOS/LaodiNotify"} {
		info, err := root.Lstat(name)
		if err != nil || info.Mode().Perm()&0111 == 0 {
			return nil, errors.New("release executable is not executable")
		}
	}
	return files, nil
}
func distributionPrivateMode(info os.FileInfo, permissions os.FileMode) bool {
	return info.Mode().Perm() == permissions && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func distributionRead(root *os.Root, name string) ([]byte, error) {
	if runtime.GOOS == "windows" {
		return readWindowsSource(root, name)
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("payload file must be regular")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("payload changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxDistributionFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDistributionFile {
		return nil, errors.New("payload file exceeds limit")
	}
	return data, nil
}

func preflightDistribution(plan DistributionPlan) error {
	// Watch refuses to reuse an observation history for a different evidence
	// root. Check the same identity before copying files or changing hooks, so
	// bootstrap cannot succeed into an immediate KeepAlive failure loop.
	// Explicit removal does not need a compatible observation history.
	if !plan.options.Remove {
		state, err := LoadState(plan.StateDir)
		if err != nil {
			return fmt.Errorf("inspect existing monitor state: %w", err)
		}
		root, err := filepath.Abs(filepath.Join(plan.options.Home, ".zcode", "v2", "checkpoints"))
		if err != nil {
			return err
		}
		rootID := RootID(root)
		if plan.HooksOnly {
			rootID = RootID("laodi:tool-hooks-only")
		}
		if state.RootID != "" && state.RootID != rootID {
			return errors.New("existing monitor history belongs to a different evidence root or monitoring mode; installation was not changed and history was retained")
		}
	}
	_, err := os.Lstat(plan.RuntimeDir)
	if errors.Is(err, os.ErrNotExist) {
		for _, path := range []string{filepath.Join(plan.options.Home, "Library", "LaunchAgents", serviceLabel+".plist"), filepath.Join(plan.StateDir, serviceReceiptName), filepath.Join(plan.StateDir, "hook-install-zcode.json"), filepath.Join(plan.StateDir, "hook-install-claude-code.json")} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return errors.New("an existing or incomplete Laodi installation uses another location; remove it explicitly before release installation")
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := distributionDirectory(plan.RuntimeDir); err != nil {
		return err
	}
	data, err := readServiceFile(filepath.Join(plan.RuntimeDir, distributionReceiptName))
	if err != nil {
		return errors.New("runtime has no valid ownership receipt; refusing to overwrite it")
	}
	var receipt distributionReceipt
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.Version != 1 {
		return errors.New("invalid runtime ownership receipt")
	}
	installed, err := distributionFiles(plan.RuntimeDir, true)
	if err != nil || !reflect.DeepEqual(installed, receipt.Files) {
		return errors.New("installed runtime was edited; refusing to change it")
	}
	if plan.PendingRecovery {
		if _, err := inspectUpgradeJournal(plan); err != nil {
			return err
		}
	}
	service, err := distributionService(plan)
	if err != nil {
		return err
	}
	owned, err := inspectServiceOwnership(service)
	if err != nil {
		return err
	}
	if owned {
		data, err := readServiceFile(service.PlistPath)
		if err != nil {
			return err
		}
		if !plan.options.Remove && serviceHash(data) != service.PlistHash {
			return errors.New("existing service uses different settings; remove it explicitly before installing")
		}
	}
	// Check both receipts, even if a tool has since been removed from this machine.
	for _, adapter := range []string{"zcode", "claude-code"} {
		data, err := readServiceFile(filepath.Join(plan.StateDir, "hook-install-"+adapter+".json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		hook, err := PlanHookConfig(HookConfigOptions{Adapter: adapter, Executable: plan.Executable, StateDir: plan.StateDir, Home: plan.options.Home})
		if err != nil {
			return err
		}
		receipt, err := decodeHookReceipt(data, hook)
		if err != nil {
			return err
		}
		entry, _ := json.Marshal(hookConfigEntry(hook))
		if !jsonEqual(entry, receipt.Entry) {
			return errors.New("existing hooks use another executable; remove them explicitly before installing")
		}
	}
	return nil
}

func distributionService(plan DistributionPlan) (ServicePlan, error) {
	service, err := PlanService(ServiceOptions{Executable: plan.Executable, Home: plan.options.Home, StateDir: plan.StateDir, App: plan.App, Notifier: plan.Notifier, HooksOnly: plan.HooksOnly})
	if err == nil {
		service.runner = plan.serviceRunner
	}
	return service, err
}

// InstallDistribution is intentionally staged, not a cross-file transaction.
// Results remain useful on error; completed stages and existing data are kept.
func InstallDistribution(plan DistributionPlan) (DistributionResult, error) {
	if runtime.GOOS == "windows" {
		return installWindowsDistribution(plan)
	}
	result := DistributionResult{NotificationStatus: "not_requested", HookAdapters: []string{}}
	if plan.options.Remove {
		return result, errors.New("removal plan cannot be used for installation")
	}
	current, err := PlanDistribution(plan.options)
	if err != nil {
		return result, err
	}
	expected, _ := json.Marshal(current)
	provided, _ := json.Marshal(plan)
	if string(expected) != string(provided) || !reflect.DeepEqual(current.files, plan.files) || plan.serviceRunner == nil || plan.notifierRunner == nil {
		return result, errors.New("installation plan changed; generate a fresh plan")
	}
	root, err := openStateRoot(plan.StateDir, true)
	if err != nil {
		return result, err
	}
	root.Close()
	release, err := AcquireLock(filepath.Join(plan.StateDir, "distribution-management"))
	if err != nil {
		return result, err
	}
	defer release()
	// Another installer may have finished while this invocation was acquiring
	// the management lock. Recompute recovery/update flags under that lock.
	lockedPlan, err := PlanDistribution(plan.options)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(lockedPlan.files, plan.files) {
		return result, errors.New("release source changed while acquiring the installation lock")
	}
	lockedPlan.serviceRunner, lockedPlan.notifierRunner, lockedPlan.healthCheck = plan.serviceRunner, plan.notifierRunner, plan.healthCheck
	plan = lockedPlan
	if plan.PendingRecovery {
		if err := recoverDistributionUpgrade(plan); err != nil {
			return result, err
		}
		fresh, err := PlanDistribution(plan.options)
		if err != nil {
			return result, err
		}
		fresh.serviceRunner, fresh.notifierRunner, fresh.healthCheck = plan.serviceRunner, plan.notifierRunner, plan.healthCheck
		plan = fresh
	}
	if err := preflightDistribution(plan); err != nil {
		return result, err
	}
	if plan.Upgrade {
		return upgradeDistribution(plan)
	}
	if err := installDistributionPayload(plan); err != nil {
		return result, err
	}
	result.RuntimeInstalled = true
	if len(plan.Adapters) == 0 {
		result.Warnings = append(result.Warnings, "No supported tool was found. Runtime files are installed, but monitoring is not connected; run install again after installing a supported tool.")
		return result, nil
	}
	for _, adapter := range plan.Adapters {
		hook, err := PlanHookConfig(HookConfigOptions{Adapter: adapter, Executable: plan.Executable, StateDir: plan.StateDir, Home: plan.options.Home})
		if err != nil {
			return result, err
		}
		if err := InstallHookConfig(hook); err != nil {
			return result, fmt.Errorf("connect %s hooks: %w", adapter, err)
		}
		result.HookAdapters = append(result.HookAdapters, adapter)
	}
	service, err := distributionService(plan)
	if err != nil {
		return result, err
	}
	if err := InstallService(service); err != nil {
		return result, err
	}
	result.ServiceInstalled = true
	result.Warnings = append(result.Warnings, "Existing agent processes were not restarted. Start a new agent session if its hook settings are loaded only at session creation.")
	if plan.RequestNotifications {
		result.NotificationStatus, err = distributionNotifications(plan)
		if err != nil {
			result.Warnings = append(result.Warnings, "监测已安装，通知状态暂未确认。可在系统设置 → 通知 → 老底中检查；本地事件仍会记录。")
			return result, nil
		}
		switch result.NotificationStatus {
		case "denied":
			result.Warnings = append(result.Warnings, "通知已关闭，监测和本地记录不受影响。需要提醒时，在系统设置 → 通知 → 老底中开启。")
		case "not_determined":
			result.Warnings = append(result.Warnings, "通知授权尚未完成，监测和本地记录已就绪。可稍后重新运行安装命令，或在系统设置 → 通知 → 老底中检查。")
		}
	}
	return result, nil
}

func installDistributionPayload(plan DistributionPlan) error {
	if _, err := os.Lstat(plan.RuntimeDir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Mkdir is a no-clobber ownership boundary. A partial copy is deliberately
	// retained without a receipt, so a later invocation cannot silently adopt it.
	if err := os.Mkdir(plan.RuntimeDir, 0700); err != nil {
		return err
	}
	source, err := os.OpenRoot(plan.SourceDir)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := os.OpenRoot(plan.RuntimeDir)
	if err != nil {
		return err
	}
	defer dest.Close()
	names := make([]string, 0, len(plan.files))
	for name := range plan.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := distributionRead(source, name)
		if err != nil {
			return err
		}
		if serviceHash(data) != plan.files[name] {
			return errors.New("release payload changed after planning")
		}
		if err := dest.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if name == "laodi" || name == filepath.FromSlash("LaodiNotify.app/Contents/MacOS/LaodiNotify") {
			mode = 0700
		}
		f, err := dest.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
	}
	receipt, _ := json.Marshal(distributionReceipt{Version: 1, Files: plan.files})
	return writeNewServiceFile(filepath.Join(plan.RuntimeDir, distributionReceiptName), append(receipt, '\n'))
}

func distributionNotifications(plan DistributionPlan) (string, error) {
	call := func(action string) (string, error) {
		deadline := 12 * time.Second
		if action == "--request-permission" {
			// The helper waits up to 60 seconds for the user's response. Leave
			// enough time for its timeout result and process cleanup to return.
			deadline = 70 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()
		data, err := plan.notifierRunner(ctx, plan.Notifier, action)
		if err != nil {
			return "unknown", errors.New("notification helper failed; inspect notification status separately")
		}
		var status struct {
			OK            bool   `json:"ok"`
			Authorization string `json:"authorization"`
		}
		if len(data) > 8192 || json.Unmarshal(data, &status) != nil || !status.OK {
			return "unknown", errors.New("notification helper returned an invalid status")
		}
		switch status.Authorization {
		case "not_determined", "authorized", "provisional", "denied":
			return status.Authorization, nil
		default:
			return "unknown", errors.New("notification authorization is unknown")
		}
	}
	status, err := call("--status")
	if err != nil || status != "not_determined" || !plan.RequestNotifications {
		return status, err
	}
	status, err = call("--request-permission")
	if err == nil {
		return status, nil
	}
	// A lost response does not establish denial: the user may have already
	// made a choice. Query once without opening another permission request.
	if latest, checkErr := call("--status"); checkErr == nil {
		return latest, nil
	}
	return status, err
}
func runDistributionNotifier(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(output, 8193))
	if len(data) > 8192 {
		command.Process.Kill()
	}
	waitErr := command.Wait()
	return data, errors.Join(readErr, waitErr)
}

// UninstallDistribution removes only owned service/hook registrations. Runtime
// payload and incident records stay in place for inspection or reinstallation.
func UninstallDistribution(plan DistributionPlan) (DistributionResult, error) {
	if runtime.GOOS == "windows" {
		return uninstallWindowsDistribution(plan)
	}
	result := DistributionResult{HookAdapters: []string{}, NotificationStatus: "unchanged"}
	current, err := PlanDistribution(plan.options)
	if err != nil {
		return result, err
	}
	expected, _ := json.Marshal(current)
	provided, _ := json.Marshal(plan)
	if string(expected) != string(provided) || !reflect.DeepEqual(current.files, plan.files) || plan.serviceRunner == nil {
		return result, errors.New("removal plan changed; generate a fresh plan")
	}
	if _, err := os.Lstat(plan.RuntimeDir); err != nil {
		return result, errors.New("no owned release runtime is installed")
	}
	result.RuntimeInstalled = true
	if err := preflightDistribution(plan); err != nil {
		return result, err
	}
	guardRelease, err := lockArchiveGuardForRemoval(plan.StateDir)
	if err != nil {
		return result, err
	}
	defer guardRelease()
	release, err := AcquireLock(filepath.Join(plan.StateDir, "distribution-management"))
	if err != nil {
		return result, err
	}
	defer release()
	lockedPlan, err := PlanDistribution(plan.options)
	if err != nil {
		return result, err
	}
	if lockedPlan.PendingRecovery {
		return result, errors.New("an interrupted update needs recovery; rerun install or update before removing Laodi")
	}
	if !reflect.DeepEqual(lockedPlan.files, plan.files) {
		return result, errors.New("runtime changed while acquiring the removal lock; retry removal")
	}
	service, err := distributionService(plan)
	if err != nil {
		return result, err
	}
	owned, err := inspectServiceOwnership(service)
	if err != nil {
		return result, err
	}
	if owned {
		if err := UninstallService(service); err != nil {
			return result, err
		}
	}
	for _, adapter := range []string{"zcode", "claude-code"} {
		if _, err := os.Lstat(filepath.Join(plan.StateDir, "hook-install-"+adapter+".json")); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return result, err
		}
		hook, err := PlanHookConfig(HookConfigOptions{Adapter: adapter, Executable: plan.Executable, StateDir: plan.StateDir, Home: plan.options.Home})
		if err != nil {
			return result, err
		}
		if err := UninstallHookConfig(hook); err != nil {
			return result, err
		}
	}
	result.Warnings = append(result.Warnings, "Owned background service and hooks were removed. Runtime files and incident history were retained.")
	return result, nil
}
