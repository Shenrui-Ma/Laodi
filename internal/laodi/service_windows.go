//go:build windows

package laodi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const serviceLabel = "dev.laodi.guardian"
const serviceReceiptName = "service-install.json"
const maxServiceFileBytes = 64 << 10

type ServiceOptions struct {
	Executable, Root, StateDir, App, Build, Notifier, Home string
	MonitorExecutable                                      string
	HooksOnly                                              bool
}

// A task is deliberately not represented as a launchd plist.
type ServicePlan struct {
	Label            string   `json:"label"`
	Domain           string   `json:"domain"`
	PlistPath        string   `json:"plist_path,omitempty"`
	ReceiptPath      string   `json:"receipt_path"`
	Arguments        []string `json:"arguments"`
	Plist            string   `json:"plist,omitempty"`
	PlistHash        string   `json:"plist_hash,omitempty"`
	UID              int      `json:"uid,omitempty"`
	TaskName         string   `json:"task_name"`
	TaskXML          string   `json:"task_xml"`
	TaskHash         string   `json:"task_hash"`
	UserSID          string   `json:"user_sid"`
	options          ServiceOptions
	runner           func(context.Context, ...string) error // Legacy launchd test injection only.
	taskRunner       func(context.Context, string, ServicePlan) (windowsTask, error)
	startupRunner    func(context.Context, string, ServicePlan, string) (startupLink, error)
	startupDirectory func() (string, error)
}

type windowsTask struct {
	Exists bool   `json:"exists"`
	XML    string `json:"xml"`
	State  int    `json:"state"`
}

type serviceReceipt struct {
	SchemaVersion  int       `json:"schema_version"`
	Label          string    `json:"label"`
	TaskName       string    `json:"task_name"`
	TaskHash       string    `json:"task_hash"`
	RegisteredHash string    `json:"registered_hash"`
	UserSID        string    `json:"user_sid"`
	InstalledAt    time.Time `json:"installed_at"`
	Backend        string    `json:"backend,omitempty"`
	StartupPath    string    `json:"startup_path,omitempty"`
	LinkTarget     string    `json:"link_target,omitempty"`
	LinkArguments  string    `json:"link_arguments,omitempty"`
	LinkHash       string    `json:"link_hash,omitempty"`
}

func PlanService(options ServiceOptions) (ServicePlan, error) {
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return ServicePlan{}, err
	}
	if options.Home == "" {
		options.Home, err = os.UserHomeDir()
		if err != nil {
			return ServicePlan{}, err
		}
	}
	if options.Executable == "" {
		options.Executable, err = os.Executable()
		if err != nil {
			return ServicePlan{}, err
		}
	}
	if options.StateDir == "" {
		options.StateDir, err = DefaultStateDir(options.Home)
		if err != nil {
			return ServicePlan{}, err
		}
	}
	if options.Root == "" {
		options.Root = DefaultEvidenceRoot(options.Home)
	}
	for _, p := range []string{options.Home, options.Executable, options.MonitorExecutable, options.StateDir, options.Root, options.App, options.Notifier} {
		if p != "" && (!filepath.IsAbs(p) || strings.ContainsAny(p, "\x00\r\n") || strings.HasPrefix(p, `\\`) || strings.Contains(strings.TrimPrefix(filepath.Clean(p), filepath.VolumeName(p)), ":")) {
			return ServicePlan{}, errors.New("service paths must be absolute local paths without alternate streams or control characters")
		}
	}
	if strings.ContainsAny(options.Build, "\x00\r\n") {
		return ServicePlan{}, errors.New("invalid service build")
	}
	for _, p := range []string{options.Executable, options.Notifier} {
		if p == "" {
			continue
		}
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(p), ".exe") {
			return ServicePlan{}, errors.New("service executables must be existing regular .exe files")
		}
	}
	args := []string{options.Executable, "watch", "--root", options.Root, "--state-dir", options.StateDir}
	if options.App != "" {
		args = append(args, "--app", options.App)
	}
	if options.HooksOnly {
		args = append(args, "--hooks-only")
	}
	if options.Build != "" {
		args = append(args, "--build", options.Build)
	}
	if options.Notifier != "" {
		args = append(args, "--notifier", options.Notifier)
	}
	// Separate installations and users cannot accidentally address each other's tasks.
	name := serviceLabel + "." + serviceHash([]byte(sid + "\x00" + strings.ToLower(filepath.Clean(options.StateDir))))[:24]
	quoted := make([]string, 0, len(args)-1)
	for _, arg := range args[1:] {
		quoted = append(quoted, syscall.EscapeArg(arg))
	}
	escape := func(s string) string { var b strings.Builder; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	taskXML := `<?xml version="1.0"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
<RegistrationInfo><Description>Laodi user background monitoring</Description><URI>\` + name + `</URI></RegistrationInfo>
<Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + escape(sid) + `</UserId></LogonTrigger></Triggers>
<Principals><Principal id="LaodiUser"><UserId>` + escape(sid) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
<Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><AllowHardTerminate>false</AllowHardTerminate><StartWhenAvailable>true</StartWhenAvailable><RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable><IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings><AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><Hidden>true</Hidden><RunOnlyIfIdle>false</RunOnlyIfIdle><WakeToRun>false</WakeToRun><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Priority>7</Priority><RestartOnFailure><Interval>PT1M</Interval><Count>3</Count></RestartOnFailure></Settings>
<Actions Context="LaodiUser"><Exec><Command>` + escape(args[0]) + `</Command><Arguments>` + escape(strings.Join(quoted, " ")) + `</Arguments><WorkingDirectory>` + escape(filepath.Dir(args[0])) + `</WorkingDirectory></Exec></Actions></Task>`
	if len(taskXML) > maxServiceFileBytes {
		return ServicePlan{}, errors.New("service configuration exceeds size limit")
	}
	return ServicePlan{Label: serviceLabel, Domain: "interactive-user", ReceiptPath: filepath.Join(options.StateDir, serviceReceiptName), Arguments: args, TaskName: name, TaskXML: taskXML, TaskHash: serviceHash([]byte(taskXML)), UserSID: sid, options: options, taskRunner: runWindowsTask, runner: runLaunchctl, startupRunner: runWindowsStartup, startupDirectory: windowsStartupDirectory}, nil
}

func validateServicePlan(plan ServicePlan) error {
	expected, err := PlanService(plan.options)
	if err != nil {
		return err
	}
	if plan.Label != expected.Label || plan.Domain != expected.Domain || plan.ReceiptPath != expected.ReceiptPath || plan.TaskName != expected.TaskName || plan.TaskXML != expected.TaskXML || plan.TaskHash != expected.TaskHash || plan.UserSID != expected.UserSID || !reflect.DeepEqual(plan.Arguments, expected.Arguments) || plan.Plist != "" || plan.PlistPath != "" || plan.PlistHash != "" || plan.taskRunner == nil {
		return errors.New("service plan changed; generate a fresh plan")
	}
	return nil
}

func lockServiceManagement(plan ServicePlan) (func(), error) {
	root, err := openStateRoot(plan.options.StateDir, true)
	if err != nil {
		return nil, err
	}
	root.Close()
	return AcquireLock(filepath.Join(plan.options.StateDir, "service-management"))
}

func callWindowsTask(plan ServicePlan, operation string) (windowsTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := plan.taskRunner(ctx, operation, plan)
	if err != nil {
		return result, fmt.Errorf("Windows background task %s failed (background monitoring is not connected): %w", operation, err)
	}
	return result, nil
}

func inspectServiceOwnership(plan ServicePlan) (bool, error) {
	_, receipt, owned, err := inspectWindowsTask(plan)
	if err == nil && owned && receipt.TaskHash != plan.TaskHash {
		return false, errors.New("owned Windows service settings differ from the requested plan")
	}
	return owned, err
}

func inspectWindowsTask(plan ServicePlan) (windowsTask, serviceReceipt, bool, error) {
	if data, err := readServiceFile(plan.ReceiptPath); err == nil {
		var receipt serviceReceipt
		if decodeWindowsRecord(data, &receipt) != nil {
			return windowsTask{}, receipt, false, errors.New("invalid Windows service receipt")
		}
		if receipt.Backend == "user_startup" {
			return inspectWindowsStartup(plan, receipt)
		}
		if receipt.Backend != "" && receipt.Backend != "task_scheduler" {
			return windowsTask{}, receipt, false, errors.New("unknown Windows service backend")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return windowsTask{}, serviceReceipt{}, false, errors.New("Windows service receipt unreadable; retained")
	}
	task, err := callWindowsTask(plan, "query")
	if err != nil {
		return task, serviceReceipt{}, false, err
	}
	data, readErr := readServiceFile(plan.ReceiptPath)
	if !task.Exists && errors.Is(readErr, os.ErrNotExist) {
		return task, serviceReceipt{}, false, nil
	}
	if readErr != nil {
		return task, serviceReceipt{}, false, errors.New("task receipt missing or unreadable; refusing to change an unowned task")
	}
	var receipt serviceReceipt
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&receipt) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) || receipt.SchemaVersion != SchemaVersion || receipt.Label != plan.Label || receipt.TaskName != plan.TaskName || receipt.UserSID != plan.UserSID || len(receipt.RegisteredHash) != 64 || len(receipt.TaskHash) != 64 {
		return task, receipt, false, errors.New("invalid Windows service receipt")
	}
	if !task.Exists || serviceHash([]byte(task.XML)) != receipt.RegisteredHash {
		return task, receipt, false, errors.New("Windows task configuration changed or is missing; refusing to change it")
	}
	return task, receipt, true, nil
}

func InstallService(plan ServicePlan) error {
	if err := validateServicePlan(plan); err != nil {
		return err
	}
	release, err := lockServiceManagement(plan)
	if err != nil {
		return err
	}
	defer release()
	// A packaged parent can redirect AppData writes even when this process has
	// no package identity. The scheduler must see the same executable and state.
	// Keep this out of removal so an old redirected installation stays removable.
	if err := verifyWindowsInstallationStatePath(plan.options.StateDir); err != nil {
		return err
	}
	if err := verifyWindowsInstallationFile(plan.options.Executable); err != nil {
		return err
	}
	task, receipt, owned, err := inspectWindowsTask(plan)
	if err != nil {
		return err
	}
	if owned {
		if receipt.TaskHash != plan.TaskHash {
			return errors.New("Laodi is installed with different settings; explicitly uninstall before changing the service")
		}
		if receipt.Backend == "user_startup" {
			return startWindowsStartup(plan, receipt)
		}
		if task.State == 4 {
			return nil
		}
		plan.TaskXML = task.XML
		_, err := callWindowsTask(plan, "run")
		return err
	}
	registered, err := callWindowsTask(plan, "create")
	if err != nil {
		var failure *windowsServiceError
		if errors.As(err, &failure) && failure.Class == "access_denied" && failure.Stage == "register" && failure.HResult == -2147024891 {
			// A failed registration must not have left a task or receipt behind.
			current, _, nowOwned, checkErr := inspectWindowsTask(plan)
			if checkErr != nil {
				return errors.Join(err, checkErr)
			}
			if current.Exists || nowOwned {
				return errors.New("task creation left ownership ambiguous; refusing fallback")
			}
			return installWindowsStartup(plan)
		}
		return err
	}
	if !registered.Exists || registered.XML == "" {
		return errors.New("task registration returned no verifiable configuration; task retained for manual review")
	}
	receipt = serviceReceipt{Backend: "task_scheduler", SchemaVersion: SchemaVersion, Label: plan.Label, TaskName: plan.TaskName, TaskHash: plan.TaskHash, RegisteredHash: serviceHash([]byte(registered.XML)), UserSID: plan.UserSID, InstalledAt: time.Now().UTC()}
	data, _ := json.Marshal(receipt)
	if err := writeNewServiceFile(plan.ReceiptPath, append(data, '\n')); err != nil {
		// Registration succeeded, but ownership publication failed. The remove
		// operation compares the exact XML observed immediately after creation.
		cleanup := plan
		cleanup.TaskXML = registered.XML
		_, rollbackErr := callWindowsTask(cleanup, "delete-exact")
		return errors.Join(fmt.Errorf("publish task ownership receipt: %w", err), rollbackErr)
	}
	plan.TaskXML = registered.XML
	_, err = callWindowsTask(plan, "run")
	// Preserve receipt on a start failure: retry/removal must remain reviewable.
	return err
}

func startWindowsService(plan ServicePlan) error {
	task, receipt, owned, err := inspectWindowsTask(plan)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("no owned Windows service")
	}
	if receipt.TaskHash != plan.TaskHash {
		return errors.New("owned Windows service settings differ from the requested plan")
	}
	if receipt.Backend == "user_startup" {
		return startWindowsStartup(plan, receipt)
	}
	if task.State == 4 {
		return nil
	}
	plan.TaskXML = task.XML
	_, err = callWindowsTask(plan, "run")
	return err
}

func stopWindowsService(plan ServicePlan) error {
	task, receipt, owned, err := inspectWindowsTask(plan)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("no owned Windows service")
	}
	if receipt.Backend == "user_startup" {
		return stopWindowsStartup(plan)
	}
	if task.State != 4 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	executable := plan.options.MonitorExecutable
	if executable == "" {
		executable = plan.options.Executable
	}
	if err := StopMonitor(ctx, plan.options.StateDir, executable); err != nil {
		return err
	}
	return waitWindowsTaskStopped(plan)
}

func waitWindowsTaskStopped(plan ServicePlan) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A stable launcher can outlive its versioned child briefly. Wait for the
	// exact task to settle before removal; never force-end the launcher.
	for {
		current, receipt, _, err := inspectWindowsTask(plan)
		if err != nil {
			return err
		}
		if receipt.Backend == "user_startup" {
			return waitWindowsStartupStopped(ctx, plan)
		}
		if current.State != 4 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("monitor exited but task has not finished; ownership retained")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func UninstallService(plan ServicePlan) error {
	if err := validateServicePlan(plan); err != nil {
		return err
	}
	release, err := lockServiceManagement(plan)
	if err != nil {
		return err
	}
	defer release()
	task, receipt, owned, err := inspectWindowsTask(plan)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("no owned Laodi service is installed")
	}
	if err := stopWindowsService(plan); err != nil {
		return err
	}
	data, err := readServiceFile(plan.ReceiptPath)
	if err != nil {
		return err
	}
	if receipt.Backend == "user_startup" {
		if _, err := callWindowsStartup(plan, "delete", receipt.LinkHash); err != nil {
			return err
		}
	} else {
		cleanup := plan
		cleanup.TaskXML = task.XML
		if _, err := callWindowsTask(cleanup, "delete-exact"); err != nil {
			return err
		}
	}
	return removeServiceFile(plan.ReceiptPath, serviceHash(data))
}

// Retain link-time compatibility for macOS-only distribution code. A Windows
// caller must not accidentally translate launchctl verbs into task operations.
func callService(plan ServicePlan, args ...string) error {
	return errors.New("launchd operation is not supported on Windows")
}
func runLaunchctl(context.Context, ...string) error {
	return errors.New("launchctl is not supported on Windows")
}

func runWindowsTask(ctx context.Context, operation string, plan ServicePlan) (windowsTask, error) {
	if strings.ContainsAny(plan.TaskName, `\/'"`+"\r\n") {
		return windowsTask{}, errors.New("invalid exact task name")
	}
	// Only management operations start built-in Windows PowerShell. Hook events
	// and the resident monitor do not incur a PowerShell process per event.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := `$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; [Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $stage='connect'; $svc=New-Object -ComObject 'Schedule.Service'; $svc.Connect(); $folder=$svc.GetFolder('\'); $name=` + quote(plan.TaskName) + `; $task=$null; try { $task=$folder.GetTask($name) } catch { if ($_.Exception.HResult -ne -2147024894 -and $_.Exception.InnerException.HResult -ne -2147024894) { throw } }; `
	switch operation {
	case "query":
	case "create":
		script += `if ($null -ne $task) { throw 'Task already exists; refusing overwrite' }; $xml=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(` + quote(base64.StdEncoding.EncodeToString([]byte(plan.TaskXML))) + `)); $stage='register'; $task=$folder.RegisterTask($name,$xml,2,` + quote(plan.UserSID) + `,$null,3,$null); `
	case "run":
		script += `if ($null -eq $task) { throw 'Task missing' }; $expected=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(` + quote(base64.StdEncoding.EncodeToString([]byte(plan.TaskXML))) + `)); if ($task.Xml -cne $expected) { throw 'Task configuration changed; refusing run' }; $null=$task.Run($null); `
	case "delete-exact":
		script += `if ($null -eq $task) { throw 'Task missing' }; $expected=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(` + quote(base64.StdEncoding.EncodeToString([]byte(plan.TaskXML))) + `)); if ($task.Xml -cne $expected) { throw 'Task configuration changed; refusing removal' }; if ([int]$task.State -eq 4) { throw 'Task is still running; refusing removal' }; $folder.DeleteTask($name,0); $task=$null; `
	default:
		return windowsTask{}, errors.New("unsupported Windows task operation")
	}
	script += `if ($null -eq $task) { @{exists=$false;xml='';state=0}|ConvertTo-Json -Compress } else { @{exists=$true;xml=[string]$task.Xml;state=[int]$task.State}|ConvertTo-Json -Compress }`
	var result windowsTask
	if err := runServicePowerShell(ctx, script, &result); err != nil {
		return result, err
	}
	if len(result.XML) > maxServiceFileBytes {
		return result, errors.New("task XML exceeds size limit")
	}
	return result, nil
}

type serviceBoundedBuffer struct{ buffer bytes.Buffer }

func (b *serviceBoundedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *serviceBoundedBuffer) String() string { return b.buffer.String() }

func (b *serviceBoundedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 4*maxServiceFileBytes {
		return 0, errors.New("task output exceeds limit")
	}
	return b.buffer.Write(p)
}

func serviceHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func readServiceFile(path string) ([]byte, error) {
	root, err := openStateRoot(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openStateFile(root, filepath.Base(path), os.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxServiceFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxServiceFileBytes {
		return nil, errors.New("service file exceeds size limit")
	}
	return data, nil
}

func serviceParent(path string) (*os.Root, error) { return openStateRoot(filepath.Dir(path), true) }

func writeNewServiceFile(path string, data []byte) error {
	root, err := serviceParent(path)
	if err != nil {
		return err
	}
	defer root.Close()
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	name := ".laodi-" + hex.EncodeToString(suffix[:]) + ".tmp"
	f, err := createPrivateFile(root, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer root.Remove(name)
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
	// Go's Root.Rename does not offer no-clobber on Windows. The native move
	// omits REPLACE_EXISTING so a raced foreign receipt cannot be overwritten.
	oldp, err := syscall.UTF16PtrFromString(filepath.Join(filepath.Dir(path), name))
	if err != nil {
		return err
	}
	newp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	r, _, moveErr := svcMoveFileEx.Call(uintptr(unsafe.Pointer(oldp)), uintptr(unsafe.Pointer(newp)), 8)
	if r == 0 {
		return moveErr
	}
	return nil
}

func removeServiceFile(path, expectedHash string) error {
	data, err := readServiceFile(path)
	if err != nil {
		return err
	}
	if serviceHash(data) != expectedHash {
		return errors.New("service file was edited; refusing to remove it")
	}
	root, err := openStateRoot(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(filepath.Base(path))
}
