//go:build windows

package laodi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// Service failures carry a fixed classification, never PowerShell's CLIXML,
// localized exception message, script text, account name, or account SID.
type windowsServiceError struct {
	Class   string `json:"class"`
	Stage   string `json:"stage"`
	HResult int64  `json:"hresult"`
}

func (e *windowsServiceError) Error() string {
	return fmt.Sprintf("Windows background management failed (%s, stage=%s, HRESULT=0x%08X)", e.Class, e.Stage, uint32(e.HResult))
}
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func runServicePowerShell(ctx context.Context, body string, out any) error {
	script := `$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; [Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $stage='operation'; try { ` + body + ` } catch { $h=[long]$_.Exception.HResult; $e=$_.Exception; while($null -ne $e.InnerException) { $e=$e.InnerException; $h=[long]$e.HResult }; $class='operation_failed'; if($h -eq -2147024891){$class='access_denied'}; @{service_error=@{class=$class;stage=$stage;hresult=$h}} | ConvertTo-Json -Compress; exit 1 }`
	encoded := utf16.Encode([]rune(script))
	raw := make([]byte, len(encoded)*2)
	for i, v := range encoded {
		binary.LittleEndian.PutUint16(raw[i*2:], v)
	}
	windowsDir := os.Getenv("SystemRoot")
	if !filepath.IsAbs(windowsDir) {
		return errors.New("SystemRoot is not an absolute path")
	}
	cmd := exec.CommandContext(ctx, filepath.Join(windowsDir, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var output, diagnostics serviceBoundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &diagnostics
	runErr := cmd.Run()
	data := bytes.TrimPrefix(output.Bytes(), []byte{0xef, 0xbb, 0xbf})
	var failure struct {
		Error *windowsServiceError `json:"service_error"`
	}
	if json.Unmarshal(data, &failure) == nil && failure.Error != nil {
		e := failure.Error
		if e.Class != "access_denied" {
			e.Class = "operation_failed"
		}
		switch e.Stage {
		case "connect", "register", "operation", "shortcut_shell", "shortcut_create", "shortcut_configure", "shortcut_save", "shortcut_verify":
		default:
			e.Stage = "operation"
		}
		return e
	}
	if runErr != nil {
		return errors.New("Windows background management returned no valid structured result")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("Windows background management returned an invalid bounded response")
	}
	return nil
}

type startupLink struct {
	Path, Hash, Target, Arguments string
	Exists                        bool
}

const startupLauncherReceipt = "startup-launcher.json"

var errStartupLauncherIdentityUnavailable = errors.New("Startup launcher identity unavailable")

func windowsStartupDirectory() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var dir string
	if err := runServicePowerShell(ctx, `[Environment]::GetFolderPath([Environment+SpecialFolder]::Startup) | ConvertTo-Json -Compress`, &dir); err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) || strings.HasPrefix(dir, `\\`) {
		return "", errors.New("current-user Startup folder is not an absolute local path")
	}
	return dir, nil
}
func startupPath(plan ServicePlan) (string, error) {
	if plan.startupDirectory == nil {
		return "", errors.New("missing Startup folder resolver")
	}
	dir, err := plan.startupDirectory()
	if err != nil {
		return "", err
	}
	if err := verifyWindowsInstallationPath(dir); err != nil {
		return "", err
	}
	root, closeRoot, err := openWindowsStartupRoot(dir)
	if err != nil {
		return "", err
	}
	_ = root
	closeRoot()
	return filepath.Join(dir, plan.TaskName+".lnk"), nil
}

// Existing user shell folders inherit trusted administrative ACEs. Do not
// impose private-state ACL rules on them, and never rewrite their permissions.
func openWindowsStartupRoot(dir string) (*os.Root, func(), error) {
	if _, err := windowsPrivatePath(dir); err != nil {
		return nil, nil, err
	}
	if err := windowsLocalVolume(dir); err != nil {
		return nil, nil, err
	}
	if err := windowsNoReparseAncestors(dir); err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	unpin, err := windowsPinRoot(root)
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	closeRoot := func() { unpin(); root.Close() }
	f, err := root.Open(".")
	if err != nil {
		closeRoot()
		return nil, nil, err
	}
	err = windowsCheckClientHandle(syscall.Handle(f.Fd()), true)
	if err == nil {
		err = verifyWindowsInstallationDirectoryHandle(dir, f)
	}
	f.Close()
	if err != nil {
		closeRoot()
		return nil, nil, err
	}
	return root, closeRoot, nil
}
func readWindowsStartupFile(path string) ([]byte, error) {
	_, closeRoot, err := openWindowsStartupRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer closeRoot()
	data, _, err := readHookConfigFile(path)
	if len(data) > maxServiceFileBytes {
		return nil, errors.New("Startup shortcut exceeds size limit")
	}
	return data, err
}
func removeWindowsStartupFile(path, hash string) error {
	root, closeRoot, err := openWindowsStartupRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer closeRoot()
	data, err := readWindowsStartupFile(path)
	if err != nil {
		return err
	}
	if serviceHash(data) != hash {
		return errors.New("Startup entry changed; refusing removal")
	}
	return root.Remove(filepath.Base(path))
}

func startupArguments(plan ServicePlan) string {
	args := make([]string, 0, len(plan.Arguments)-1)
	for _, a := range plan.Arguments[1:] {
		args = append(args, syscall.EscapeArg(a))
	}
	return strings.Join(args, " ")
}
func callWindowsStartup(plan ServicePlan, op, hash string) (startupLink, error) {
	if plan.startupRunner == nil {
		return startupLink{}, errors.New("missing Startup backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return plan.startupRunner(ctx, op, plan, hash)
}
func runWindowsStartup(ctx context.Context, op string, plan ServicePlan, hash string) (startupLink, error) {
	path, err := startupPath(plan)
	if err != nil {
		return startupLink{}, err
	}
	_, closeRoot, err := openWindowsStartupRoot(filepath.Dir(path))
	if err != nil {
		return startupLink{}, err
	}
	defer closeRoot()
	link := startupLink{Path: path, Target: plan.Arguments[0], Arguments: startupArguments(plan)}
	data, err := readWindowsStartupFile(path)
	if err == nil {
		if e := verifyWindowsInstallationFile(path); e != nil {
			return link, e
		}
		link.Exists = true
		link.Hash = serviceHash(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return link, errors.New("Startup entry is unreadable or unsafe; retained")
	}
	switch op {
	case "query":
		return link, nil
	case "create":
		if link.Exists {
			return link, errors.New("Startup entry already exists; refusing overwrite")
		}
		// Generate the shortcut inside the private state directory. Publish its exact
		// bytes with a no-clobber native move; COM never writes the real Startup entry.
		root, err := openStateRoot(plan.options.StateDir, false)
		if err != nil {
			return link, err
		}
		defer root.Close()
		unpin, err := windowsPinRoot(root)
		if err != nil {
			return link, err
		}
		defer unpin()
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return link, err
		}
		name := ".startup-" + hex.EncodeToString(nonce[:]) + ".lnk"
		f, err := createPrivateFile(root, name, os.O_WRONLY)
		if err != nil {
			return link, err
		}
		temp := filepath.Join(root.Name(), name)
		defer root.Remove(name)
		if err := f.Close(); err != nil {
			return link, err
		}
		// Save into the exclusively created private file, retaining its explicit
		// owner instead of inheriting the process token's administrative default.
		if err := writeNativeWindowsShortcut(temp, link.Target, link.Arguments); err != nil {
			return link, err
		}
		data, err = readServiceFile(temp)
		if err != nil {
			return link, err
		}
		if err = replaceHookConfigFile(path, data, nil, 0600); err != nil {
			return link, err
		}
		if err := verifyWindowsInstallationFile(path); err != nil {
			return link, err
		}
		link.Exists = true
		link.Hash = serviceHash(data)
		return link, nil
	case "delete":
		if !link.Exists || hash == "" || hash != link.Hash {
			return link, errors.New("Startup entry changed or missing; refusing removal")
		}
		return link, removeWindowsStartupFile(path, hash)
	case "run":
		if !link.Exists || hash == "" || hash != link.Hash {
			return link, errors.New("Startup entry changed or missing; refusing launch")
		}
		return link, launchWindowsStartupMonitor(ctx, plan)
	default:
		return link, errors.New("unsupported Startup operation")
	}
}
func inspectWindowsStartup(plan ServicePlan, receipt serviceReceipt) (windowsTask, serviceReceipt, bool, error) {
	fail := func() (windowsTask, serviceReceipt, bool, error) {
		return windowsTask{}, receipt, false, errors.New("Startup ownership or configuration changed; retained")
	}
	path, err := startupPath(plan)
	if err != nil {
		return windowsTask{}, receipt, false, err
	}
	if receipt.SchemaVersion != SchemaVersion || receipt.Label != plan.Label || receipt.TaskName != plan.TaskName || receipt.UserSID != plan.UserSID || receipt.TaskHash != plan.TaskHash || receipt.StartupPath != path || receipt.LinkTarget != plan.Arguments[0] || receipt.LinkArguments != startupArguments(plan) || len(receipt.LinkHash) != 64 || receipt.RegisteredHash != "" {
		return fail()
	}
	link, err := callWindowsStartup(plan, "query", "")
	if err != nil {
		return windowsTask{}, receipt, false, err
	}
	if !link.Exists || link.Path != path || link.Hash != receipt.LinkHash || link.Target != receipt.LinkTarget || link.Arguments != receipt.LinkArguments {
		return fail()
	}
	return windowsTask{Exists: true, State: 3}, receipt, true, nil
}
func installWindowsStartup(plan ServicePlan) error {
	link, err := callWindowsStartup(plan, "create", "")
	if err != nil {
		return err
	}
	if !link.Exists || len(link.Hash) != 64 {
		return errors.New("Startup registration returned no verifiable shortcut; retained")
	}
	receipt := serviceReceipt{SchemaVersion: SchemaVersion, Label: plan.Label, TaskName: plan.TaskName, TaskHash: plan.TaskHash, UserSID: plan.UserSID, InstalledAt: time.Now().UTC(), Backend: "user_startup", StartupPath: link.Path, LinkTarget: link.Target, LinkArguments: link.Arguments, LinkHash: link.Hash}
	data, _ := json.Marshal(receipt)
	if err := writeNewServiceFile(plan.ReceiptPath, append(data, '\n')); err != nil {
		_, rollbackErr := callWindowsStartup(plan, "delete", link.Hash)
		return errors.Join(err, rollbackErr)
	}
	return startWindowsStartup(plan, receipt)
}
func startWindowsStartup(plan ServicePlan, receipt serviceReceipt) error {
	_, _, owned, err := inspectWindowsStartup(plan, receipt)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("Startup entry is not owned")
	}
	_, err = callWindowsStartup(plan, "run", receipt.LinkHash)
	return err
}

// WindowsServiceBackend reports verified ownership only; the distribution's
// separate heartbeat test remains the authority for background health.
func WindowsServiceBackend(plan ServicePlan) (string, string, error) {
	_, r, owned, err := inspectWindowsTask(plan)
	if err != nil {
		return "", "", err
	}
	if !owned {
		return "", "", nil
	}
	if r.Backend == "user_startup" {
		return "user_startup", "task_access_denied", nil
	}
	return "task_scheduler", "", nil
}

type startupLauncher struct {
	Schema     int    `json:"schema"`
	PID        uint32 `json:"pid"`
	Created    uint64 `json:"created"`
	Executable string `json:"executable"`
}

func startupLauncherRunning(plan ServicePlan) (bool, error) {
	data, err := readServiceFile(filepath.Join(plan.options.StateDir, startupLauncherReceipt))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r startupLauncher
	if decodeWindowsRecord(data, &r) != nil || r.Schema != 1 || r.PID == 0 || r.Created == 0 || r.Executable != plan.Arguments[0] {
		return false, errors.New("invalid Startup launcher receipt; retained")
	}
	h, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, r.PID)
	if err == syscall.Errno(87) {
		return false, nil
	}
	if err != nil {
		return false, errStartupLauncherIdentityUnavailable
	}
	defer syscall.CloseHandle(h)
	wait, err := syscall.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	if wait == 0 {
		return false, nil
	}
	image, created, err := processIdentity(h)
	if err != nil {
		// The launcher can exit between the zero-time wait and the identity
		// query. Windows may then deny QueryFullProcessImageName even though
		// this exact process handle is now signaled. Do not misclassify a
		// completed graceful stop or crash as an ownership failure.
		if wait, waitErr := syscall.WaitForSingleObject(h, 0); waitErr == nil && wait == 0 {
			return false, nil
		}
		return false, errStartupLauncherIdentityUnavailable
	}
	if created != r.Created {
		return false, nil
	} // PID reused; never signal that process.
	actual, err := os.Stat(image)
	if err != nil {
		return false, err
	}
	expected, err := os.Stat(r.Executable)
	if err != nil {
		return false, err
	}
	if !os.SameFile(actual, expected) {
		return false, errors.New("Startup launcher executable identity changed")
	}
	return true, nil
}
func launchWindowsStartupMonitor(ctx context.Context, plan ServicePlan) error {
	if running, err := startupLauncherRunning(plan); err != nil {
		return err
	} else if running {
		return nil
	}
	// A logon-launched instance has no installer launcher receipt. The existing
	// kernel writer lock prevents a second monitor. Never kill it on reinstall.
	unlock, err := AcquireLock(plan.options.StateDir)
	if err != nil {
		// Only a live, exact monitor identity may turn lock contention into success.
		return verifyWindowsStartupMonitor(plan)
	}
	unlock()
	cmd := exec.Command(plan.Arguments[0], plan.Arguments[1:]...)
	cmd.Dir = filepath.Dir(plan.Arguments[0])
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return errors.New("Startup monitor launch failed")
	}
	defer cmd.Process.Release()
	h, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return errors.New("Startup launcher started but identity could not be recorded")
	}
	defer syscall.CloseHandle(h)
	image, created, err := processIdentity(h)
	if err != nil {
		return err
	}
	a, err := os.Stat(image)
	if err != nil {
		return err
	}
	b, err := os.Stat(plan.Arguments[0])
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) {
		return errors.New("Startup launcher identity differs from plan")
	}
	return writeWindowsRecord(plan.options.StateDir, startupLauncherReceipt, startupLauncher{Schema: 1, PID: uint32(cmd.Process.Pid), Created: created, Executable: plan.Arguments[0]})
}
func verifyWindowsStartupMonitor(plan ServicePlan) error {
	data, err := readServiceFile(filepath.Join(plan.options.StateDir, monitorProcessReceipt))
	if err != nil {
		return errors.New("monitor lock busy without a verifiable monitor receipt")
	}
	var r monitorProcess
	if decodeWindowsRecord(data, &r) != nil || r.Schema != SchemaVersion || r.PID == 0 || r.Created == 0 {
		return errors.New("invalid running monitor receipt")
	}
	h, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, r.PID)
	if err != nil {
		return errors.New("running monitor identity unavailable")
	}
	defer syscall.CloseHandle(h)
	image, created, err := processIdentity(h)
	if err != nil {
		return err
	}
	expected := plan.options.MonitorExecutable
	if expected == "" {
		expected = plan.options.Executable
	}
	a, err := os.Stat(image)
	if err != nil {
		return err
	}
	b, err := os.Stat(expected)
	if err != nil {
		return err
	}
	c, err := os.Stat(r.Executable)
	if err != nil {
		return err
	}
	if created != r.Created || !os.SameFile(a, b) || !os.SameFile(a, c) {
		return errors.New("running monitor identity differs from plan")
	}
	if wait, e := syscall.WaitForSingleObject(h, 0); e != nil || wait == 0 {
		return errors.New("monitor process has exited")
	}
	return nil
}
func stopWindowsStartup(plan ServicePlan) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	expected := plan.options.MonitorExecutable
	if expected == "" {
		expected = plan.options.Executable
	}
	if err := StopMonitor(ctx, plan.options.StateDir, expected); err != nil {
		if lockErr := windowsStartupLocksIdle(plan.options.StateDir); lockErr != nil {
			return err
		}
	}
	return waitWindowsStartupStopped(ctx, plan)
}

// A logon launch has no installer launcher receipt, and its supervisor keeps
// running between worker retries. Hold both locks together to prove that neither
// process can still own this installation, even when its receipt is unavailable.
func windowsStartupLocksIdle(stateDir string) error {
	unlockSupervisor, err := acquireWindowsNamedLock(stateDir, ".startup-supervisor.lock")
	if err != nil {
		return err
	}
	defer unlockSupervisor()
	unlockWorker, err := AcquireLock(stateDir)
	if err != nil {
		return err
	}
	defer unlockWorker()
	return nil
}

func waitWindowsStartupStopped(ctx context.Context, plan ServicePlan) error {
	for {
		running, err := startupLauncherRunning(plan)
		if err != nil && !errors.Is(err, errStartupLauncherIdentityUnavailable) {
			return err
		}
		// An exiting process may stop answering identity queries before its
		// handle signals. Retry only this unavailable-identity case within the
		// existing deadline; never treat unavailable ownership as stopped.
		if err == nil && !running {
			if err := windowsStartupLocksIdle(plan.options.StateDir); err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("Startup monitor or launcher has not stopped; ownership retained")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
