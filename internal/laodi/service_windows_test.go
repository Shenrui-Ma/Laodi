//go:build windows

package laodi

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func windowsServiceFixture(t *testing.T) ServicePlan {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	plan, err := PlanService(ServiceOptions{Executable: exe, Home: base, StateDir: filepath.Join(base, "状态 ' & % ! $ (local)"), Root: filepath.Join(base, "合成 root"), HooksOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestWindowsServiceTaskPlan(t *testing.T) {
	plan := windowsServiceFixture(t)
	if plan.Plist != "" || plan.PlistPath != "" || plan.TaskHash == "" {
		t.Fatal("Windows task misrepresented as plist")
	}
	if _, err := os.Stat(plan.options.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planning wrote state")
	}
	var task struct {
		Principal struct {
			User  string `xml:"UserId"`
			Logon string `xml:"LogonType"`
			Level string `xml:"RunLevel"`
		} `xml:"Principals>Principal"`
		Settings struct {
			Multiple      string `xml:"MultipleInstancesPolicy"`
			Limit         string `xml:"ExecutionTimeLimit"`
			Battery       bool   `xml:"DisallowStartIfOnBatteries"`
			StopBattery   bool   `xml:"StopIfGoingOnBatteries"`
			HardTerminate bool   `xml:"AllowHardTerminate"`
			Restart       string `xml:"RestartOnFailure>Interval"`
		} `xml:"Settings"`
		Action struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Actions>Exec"`
	}
	if err := xml.Unmarshal([]byte(plan.TaskXML), &task); err != nil {
		t.Fatal(err)
	}
	if task.Principal.User != plan.UserSID || task.Principal.Logon != "InteractiveToken" || task.Principal.Level != "LeastPrivilege" {
		t.Fatalf("unsafe principal: %+v", task.Principal)
	}
	if task.Settings.Multiple != "IgnoreNew" || task.Settings.Limit != "PT0S" || task.Settings.Battery || task.Settings.StopBattery || task.Settings.HardTerminate || task.Settings.Restart != "PT1M" {
		t.Fatalf("unsafe lifecycle: %+v", task.Settings)
	}
	if task.Action.Command != plan.Arguments[0] {
		t.Fatal("executable changed")
	}
	ptr, err := syscall.UTF16PtrFromString(syscall.EscapeArg(task.Action.Command) + " " + task.Action.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	// EscapeArg round-trip uses Windows' native parser, including Unicode and
	// characters that would otherwise be interpreted by a shell.
	var argc int32
	argv, err := syscall.CommandLineToArgv(ptr, &argc)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(argv))))
	actual := make([]string, int(argc))
	for i := range actual {
		actual[i] = syscall.UTF16ToString((*argv)[i][:])
	}
	if !reflect.DeepEqual(actual, plan.Arguments) {
		t.Fatalf("argv mismatch: %#v", actual)
	}
}

func fakeWindowsTask(t *testing.T, plan *ServicePlan) (*windowsTask, *[]string) {
	t.Helper()
	task := &windowsTask{}
	calls := &[]string{}
	plan.taskRunner = func(ctx context.Context, op string, p ServicePlan) (windowsTask, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded task call")
		}
		*calls = append(*calls, op)
		switch op {
		case "query":
			return *task, nil
		case "create":
			if task.Exists {
				return *task, errors.New("already exists")
			}
			task.Exists = true
			task.XML = p.TaskXML + "\n"
			task.State = 3
		case "run":
			if !task.Exists || task.XML != p.TaskXML {
				return *task, errors.New("run must match exact task")
			}
			task.State = 4
		case "delete-exact":
			if !task.Exists || task.XML != p.TaskXML || task.State == 4 {
				return *task, errors.New("unsafe deletion")
			}
			*task = windowsTask{}
		default:
			t.Fatalf("unexpected task operation %s", op)
		}
		return *task, nil
	}
	return task, calls
}

func TestWindowsServiceInstallIdempotenceAndOwnership(t *testing.T) {
	plan := windowsServiceFixture(t)
	task, calls := fakeWindowsTask(t, &plan)
	if err := InstallService(plan); err != nil {
		t.Fatal(err)
	}
	if task.State != 4 {
		t.Fatal("not started immediately")
	}
	n := len(*calls)
	if err := InstallService(plan); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != n+1 || (*calls)[n] != "query" {
		t.Fatal("repeat installation restarted monitor")
	}
	task.XML += "edited"
	if err := InstallService(plan); err == nil {
		t.Fatal("overwrote edited task")
	}
	if err := UninstallService(plan); err == nil {
		t.Fatal("removed edited task")
	}
	task.XML = strings.TrimSuffix(task.XML, "edited")
	task.State = 3
	if err := UninstallService(plan); err != nil {
		t.Fatal(err)
	}
	if task.Exists {
		t.Fatal("task retained")
	}
	if _, err := os.Stat(plan.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt retained")
	}
}

func TestWindowsServicePolicyAndForeignTaskFailures(t *testing.T) {
	for _, kind := range []string{"policy", "foreign", "start"} {
		t.Run(kind, func(t *testing.T) {
			plan := windowsServiceFixture(t)
			task, _ := fakeWindowsTask(t, &plan)
			runner := plan.taskRunner
			if kind == "foreign" {
				task.Exists = true
				task.XML = "foreign"
				task.State = 3
			}
			plan.taskRunner = func(ctx context.Context, op string, p ServicePlan) (windowsTask, error) {
				if (kind == "policy" && op == "create") || (kind == "start" && op == "run") {
					return windowsTask{}, errors.New("synthetic policy denial")
				}
				return runner(ctx, op, p)
			}
			if err := InstallService(plan); err == nil {
				t.Fatal("reported background success despite failure")
			}
			_, receiptErr := os.Stat(plan.ReceiptPath)
			if kind == "start" && receiptErr != nil {
				t.Fatal("start failure lost recovery receipt")
			}
			if kind != "start" && !errors.Is(receiptErr, os.ErrNotExist) {
				t.Fatal("published unowned receipt")
			}
			if kind == "foreign" && task.XML != "foreign" {
				t.Fatal("foreign task changed")
			}
		})
	}
}

func TestWindowsTaskNativeRoundTrip(t *testing.T) {
	if os.Getenv("LAODI_NATIVE_TASK_TEST") != "1" {
		t.Skip("explicit native Task Scheduler test opt-in")
	}
	plan := windowsServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := runWindowsTask(ctx, "query", plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Exists {
		t.Fatal("synthetic task unexpectedly exists")
	}
	created, err := runWindowsTask(ctx, "create", plan)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		p := plan
		p.TaskXML = created.XML
		if _, err := runWindowsTask(context.Background(), "delete-exact", p); err != nil {
			t.Errorf("synthetic task cleanup: %v", err)
		}
	}()
	queried, err := runWindowsTask(ctx, "query", plan)
	if err != nil {
		t.Fatal(err)
	}
	if !queried.Exists || queried.XML != created.XML {
		t.Fatal("registered XML does not round-trip")
	}
	t.Log("non-elevated interactive-user task registration/query/exact cleanup passed; task action was not run")
}

func TestWindowsMonitorStopHelper(t *testing.T) {
	if os.Getenv("LAODI_STOP_HELPER") != "1" {
		return
	}
	err := Watch(context.Background(), &Scanner{Root: os.Getenv("LAODI_STOP_ROOT")}, WatchOptions{StateDir: os.Getenv("LAODI_STOP_STATE"), Interval: time.Second, HooksOnly: true, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowsTaskNativeMonitorLifecycle(t *testing.T) {
	exe := os.Getenv("LAODI_NATIVE_MONITOR_EXE")
	if exe == "" {
		t.Skip("explicit native monitor binary test opt-in")
	}
	plan := windowsServiceFixture(t)
	options := plan.options
	options.Executable = exe
	var err error
	plan, err = PlanService(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallService(plan); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if owned, _ := inspectServiceOwnership(plan); owned {
			if err := UninstallService(plan); err != nil {
				t.Errorf("synthetic monitor cleanup: %v", err)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		state, err := LoadState(plan.options.StateDir)
		if err == nil && state.Running && state.Initialized {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("scheduled monitor did not initialize")
		case <-time.After(50 * time.Millisecond):
		}
	}
	before, err := readServiceFile(filepath.Join(plan.options.StateDir, monitorProcessReceipt))
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallService(plan); err != nil {
		t.Fatal(err)
	}
	after, err := readServiceFile(filepath.Join(plan.options.StateDir, monitorProcessReceipt))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("idempotent setup restarted live monitor")
	}
	if err := UninstallService(plan); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(plan.options.StateDir)
	if err != nil || state.Running || !state.Initialized {
		t.Fatalf("graceful task shutdown did not retain stopped state: %v", err)
	}
	task, err := callWindowsTask(plan, "query")
	if err != nil || task.Exists {
		t.Fatalf("synthetic task not removed: %v", err)
	}
	t.Log("native ordinary-user install, immediate hidden monitor start, repeat install, graceful stop, exact task removal and retained state passed")
}

func TestWindowsMonitorGracefulStopAndIdentity(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "状态 safe")
	cmd := exec.Command(exe, "-test.run=^TestWindowsMonitorStopHelper$")
	cmd.Env = append(os.Environ(), "LAODI_STOP_HELPER=1", "LAODI_STOP_STATE="+stateDir, "LAODI_STOP_ROOT="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	for {
		state, e := LoadState(stateDir)
		if e == nil && state.Running && state.Initialized {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("monitor exited before ready: %v", err)
		case <-ctx.Done():
			t.Fatal("monitor not ready")
		case <-time.After(25 * time.Millisecond):
		}
	}
	path := filepath.Join(stateDir, monitorProcessReceipt)
	data, err := readServiceFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record monitorProcess
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	old := record.Created
	record.Created++
	tampered, _ := json.Marshal(record)
	if err := os.WriteFile(path, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopMonitor(ctx, stateDir, exe); err == nil {
		t.Fatal("accepted recycled process identity")
	}
	record.Created = old
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(filepath.Dir(exe), "not-the-monitor.exe")
	if err := StopMonitor(ctx, stateDir, wrong); err == nil {
		t.Fatal("accepted different executable")
	}
	if err := StopMonitor(ctx, stateDir, exe); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(stateDir)
	if err != nil || state.Running || !state.Initialized {
		t.Fatalf("state not saved on graceful stop: %+v %v", state, err)
	}
	if err := waitMonitorReceiptRemoved(ctx, stateDir); err != nil {
		t.Fatal(err)
	}
}
