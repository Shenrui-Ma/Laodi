//go:build windows

package laodi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func startupFixture(t *testing.T) (ServicePlan, *startupLink, *int) {
	t.Helper()
	p := windowsServiceFixture(t)
	dir := t.TempDir()
	p.startupDirectory = func() (string, error) { return dir, nil }
	link := &startupLink{Path: filepath.Join(dir, p.TaskName+".lnk"), Target: p.Arguments[0], Arguments: startupArguments(p)}
	launches := new(int)
	running := false
	p.startupRunner = func(ctx context.Context, op string, p ServicePlan, hash string) (startupLink, error) {
		switch op {
		case "query":
		case "create":
			if link.Exists {
				return *link, errors.New("foreign link")
			}
			link.Exists = true
			link.Hash = serviceHash([]byte("synthetic shortcut bytes"))
		case "run":
			if !link.Exists || hash != link.Hash {
				return *link, errors.New("edited link")
			}
			if !running {
				*launches++
				running = true
			}
		case "delete":
			if hash != link.Hash {
				return *link, errors.New("edited link")
			}
			link.Exists = false
		default:
			t.Fatalf("unexpected startup operation %s", op)
		}
		return *link, nil
	}
	return p, link, launches
}
func accessDeniedTask(t *testing.T, p *ServicePlan) {
	t.Helper()
	fakeWindowsTask(t, p)
	original := p.taskRunner
	p.taskRunner = func(ctx context.Context, op string, p ServicePlan) (windowsTask, error) {
		if op == "create" {
			return windowsTask{}, &windowsServiceError{Class: "access_denied", Stage: "register", HResult: -2147024891}
		}
		return original(ctx, op, p)
	}
}
func TestWindowsStartupFallbackOwnershipAndRepeat(t *testing.T) {
	p, link, launches := startupFixture(t)
	accessDeniedTask(t, &p)
	if err := InstallService(p); err != nil {
		t.Fatal(err)
	}
	if !link.Exists || *launches != 1 {
		t.Fatal("fallback not started")
	}
	mode, reason, err := WindowsServiceBackend(p)
	if err != nil || mode != "user_startup" || reason != "task_access_denied" {
		t.Fatalf("mode: %q %q %v", mode, reason, err)
	}
	// No Task Scheduler operation should be needed after fallback ownership exists.
	p.taskRunner = func(context.Context, string, ServicePlan) (windowsTask, error) {
		t.Fatal("startup backend queried Task Scheduler")
		return windowsTask{}, nil
	}
	if err := InstallService(p); err != nil {
		t.Fatal(err)
	}
	if *launches != 1 {
		t.Fatal("repeat installation restarted monitor")
	}
	if err := startWindowsService(p); err != nil {
		t.Fatal(err)
	}
	if err := waitWindowsTaskStopped(p); err != nil {
		t.Fatal(err)
	} // fake runner has no native monitor.
	if err := UninstallService(p); err != nil {
		t.Fatal(err)
	}
	if link.Exists {
		t.Fatal("shortcut retained")
	}
	if _, err := os.Stat(p.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt retained")
	}
}
func TestWindowsStartupFallbackRequiresExactDenial(t *testing.T) {
	cases := []struct {
		name       string
		failure    error
		queryFails bool
	}{
		{"generic", errors.New("Access is denied E_ACCESSDENIED 0x80070005"), false},
		{"connect", &windowsServiceError{Class: "access_denied", Stage: "connect", HResult: -2147024891}, false},
		{"other_hresult", &windowsServiceError{Class: "access_denied", Stage: "register", HResult: -1}, false},
		{"query_failure", &windowsServiceError{Class: "access_denied", Stage: "register", HResult: -2147024891}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, link, _ := startupFixture(t)
			calls := 0
			p.taskRunner = func(_ context.Context, op string, _ ServicePlan) (windowsTask, error) {
				if op == "query" {
					calls++
					if tc.queryFails && calls > 1 {
						return windowsTask{}, errors.New("ambiguous query")
					}
					return windowsTask{}, nil
				}
				return windowsTask{}, tc.failure
			}
			if err := InstallService(p); err == nil {
				t.Fatal("unconfirmed failure accepted")
			}
			if link.Exists {
				t.Fatal("unexpected fallback")
			}
		})
	}
}
func TestWindowsStartupForeignAndEditedEntries(t *testing.T) {
	for _, kind := range []string{"foreign_link", "edited_link", "edited_receipt", "orphan_receipt"} {
		t.Run(kind, func(t *testing.T) {
			p, link, _ := startupFixture(t)
			accessDeniedTask(t, &p)
			if kind == "foreign_link" {
				link.Exists = true
				link.Hash = serviceHash([]byte("foreign"))
			} else {
				if err := InstallService(p); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "edited_link":
					link.Hash = serviceHash([]byte("edited"))
				case "edited_receipt":
					data, _ := readServiceFile(p.ReceiptPath)
					data = []byte(strings.Replace(string(data), `"user_startup"`, `"unknown"`, 1))
					if err := os.WriteFile(p.ReceiptPath, data, 0600); err != nil {
						t.Fatal(err)
					}
				case "orphan_receipt":
					link.Exists = false
				}
			}
			before := *link
			if err := InstallService(p); err == nil {
				t.Fatal("foreign or edited ownership accepted")
			}
			if err := UninstallService(p); err == nil {
				t.Fatal("foreign or edited entry removed")
			}
			if *link != before {
				t.Fatal("foreign material changed")
			}
		})
	}
}
func TestWindowsStartupLegacyTaskReceiptPreserved(t *testing.T) {
	p, link, _ := startupFixture(t)
	task, _ := fakeWindowsTask(t, &p)
	if err := InstallService(p); err != nil {
		t.Fatal(err)
	}
	data, err := readServiceFile(p.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `,"backend":"task_scheduler"`, "", 1))
	if err := os.WriteFile(p.ReceiptPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallService(p); err != nil {
		t.Fatal(err)
	}
	mode, _, err := WindowsServiceBackend(p)
	if err != nil || mode != "task_scheduler" || link.Exists {
		t.Fatal("legacy task migrated unexpectedly")
	}
	task.State = 3
	if err := UninstallService(p); err != nil {
		t.Fatal(err)
	}
}
func TestWindowsServicePowerShellErrorsAreStructured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result any
	err := runServicePowerShell(ctx, `$stage='register'; throw [System.UnauthorizedAccessException]::new('PRIVATE_USERNAME PRIVATE_SCRIPT')`, &result)
	var failure *windowsServiceError
	if !errors.As(err, &failure) || failure.Class != "access_denied" || failure.Stage != "register" || failure.HResult != -2147024891 {
		t.Fatalf("unexpected classification %v", err)
	}
	if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "CLIXML") {
		t.Fatal("raw exception leaked")
	}
}
func TestWindowsStartupNativeShortcutRoundTrip(t *testing.T) {
	p := windowsServiceFixture(t)
	dir := t.TempDir()
	p.startupDirectory = func() (string, error) { return dir, nil }
	root, err := openStateRoot(p.options.StateDir, true)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	link, err := callWindowsStartup(p, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := callWindowsStartup(p, "delete", link.Hash); err != nil {
			t.Error(err)
		}
	}()
	queried, err := callWindowsStartup(p, "query", "")
	if err != nil || queried != link {
		t.Fatalf("native shortcut mismatch: %v", err)
	}
	if _, err := callWindowsStartup(p, "create", ""); err == nil {
		t.Fatal("overwrote shortcut")
	}
	if _, err := callWindowsStartup(p, "delete", serviceHash([]byte("wrong"))); err == nil {
		t.Fatal("removed edited link")
	}
	t.Log("native WScript shortcut generated and exactly verified only inside an isolated temporary folder")
}
func TestWindowsStartupNativeMonitorLifecycle(t *testing.T) {
	exe := os.Getenv("LAODI_NATIVE_STARTUP_MONITOR_EXE")
	if exe == "" {
		t.Skip("explicit native monitor fixture binary required")
	}
	original := windowsServiceFixture(t)
	o := original.options
	o.Executable = exe
	p, err := PlanService(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p.startupDirectory = func() (string, error) { return dir, nil }
	accessDeniedTask(t, &p)
	if err := InstallService(p); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if owned, _ := inspectServiceOwnership(p); owned {
			if err := UninstallService(p); err != nil {
				t.Error(err)
			}
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		state, err := LoadState(p.options.StateDir)
		if err == nil && state.Running && state.Initialized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("monitor did not initialize")
		}
		time.Sleep(50 * time.Millisecond)
	}
	before, err := readServiceFile(filepath.Join(p.options.StateDir, monitorProcessReceipt))
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallService(p); err != nil {
		t.Fatal(err)
	}
	after, err := readServiceFile(filepath.Join(p.options.StateDir, monitorProcessReceipt))
	if err != nil || string(before) != string(after) {
		t.Fatal("reinstall changed monitor process")
	}
	if err := UninstallService(p); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(p.options.StateDir)
	if err != nil || state.Running {
		t.Fatal("monitor did not stop gracefully")
	}
	// Exercises the same start/stop primitives used by update rollback.
	if err := InstallService(p); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		if err := verifyWindowsStartupMonitor(p); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restart failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := stopWindowsService(p); err != nil {
		t.Fatal(err)
	}
	if err := startWindowsService(p); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		if err := verifyWindowsStartupMonitor(p); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rollback-style restart failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWindowsStartupPartialInstallUpgradeAndRollback(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.1-windows.beta.1")
	b, _ := windowsPayloadFixture(t, "v0.4.1-windows.beta.2")
	c, _ := windowsPayloadFixture(t, "v0.4.1-windows.beta.3")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	partial := h.plan(t, a, state)
	original := partial.windowsService
	partial.windowsService = func(p DistributionPlan) (ServicePlan, error) {
		s, err := original(p)
		if err != nil {
			return s, err
		}
		run := s.taskRunner
		s.taskRunner = func(ctx context.Context, op string, p ServicePlan) (windowsTask, error) {
			if op == "create" {
				return windowsTask{}, errors.New("old installer could not create task")
			}
			return run(ctx, op, p)
		}
		return s, nil
	}
	if _, err := InstallDistribution(partial); err == nil {
		t.Fatal("old installer should leave staged runtime")
	}
	old, err := readWindowsCurrent(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, serviceReceiptName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial install unexpectedly has service receipt")
	}
	dir := t.TempDir()
	link := startupLink{}
	running := false
	starts := 0
	plan := func(source string) DistributionPlan {
		p := h.plan(t, source, state)
		original := p.windowsService
		p.windowsService = func(d DistributionPlan) (ServicePlan, error) {
			s, err := original(d)
			if err != nil {
				return s, err
			}
			accessDeniedTask(t, &s)
			s.startupDirectory = func() (string, error) { return dir, nil }
			s.startupRunner = func(_ context.Context, op string, s ServicePlan, hash string) (startupLink, error) {
				switch op {
				case "query":
				case "create":
					if link.Exists {
						return link, errors.New("exists")
					}
					link = startupLink{Exists: true, Path: filepath.Join(dir, s.TaskName+".lnk"), Target: s.Arguments[0], Arguments: startupArguments(s), Hash: serviceHash([]byte("synthetic shortcut"))}
				case "run":
					if hash != link.Hash {
						return link, errors.New("hash changed")
					}
					if !running {
						starts++
						running = true
					}
				case "delete":
					if hash != link.Hash {
						return link, errors.New("hash changed")
					}
					link.Exists = false
				}
				return link, nil
			}
			return s, nil
		}
		p.stopMonitor = func(context.Context, string, string) error { h.stops++; running = false; return nil }
		return p
	}
	result, err := InstallDistribution(plan(b))
	if err != nil {
		t.Fatal(err)
	}
	if !result.ServiceInstalled || result.BackgroundMode != "user_startup" || result.BackgroundReason != "task_access_denied" || starts != 2 {
		t.Fatalf("partial install upgrade failed: %#v; starts=%d", result, starts)
	}
	current, err := readWindowsCurrent(state)
	if err != nil || current.Manifest.Version != "v0.4.1-windows.beta.2" {
		t.Fatal("upgrade version not current")
	}
	for name, hash := range old.Launchers {
		if current.Launchers[name] != hash {
			t.Fatal("stable launcher was replaced")
		}
	}
	h.failHealth = true
	if _, err := InstallDistribution(plan(c)); err == nil || !strings.Contains(err.Error(), "old version restored") {
		t.Fatalf("expected verified rollback: %v", err)
	}
	restored, err := readWindowsCurrent(state)
	if err != nil || restored.Manifest.Version != current.Manifest.Version || !running {
		t.Fatal("startup backend did not restore previous running version")
	}
	if _, err := os.Stat(filepath.Join(state, windowsJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback left journal")
	}
}

func TestWindowsStartupNativeInheritedAdminDirectory(t *testing.T) {
	p := windowsServiceFixture(t)
	dir := t.TempDir()
	p.startupDirectory = func() (string, error) { return dir, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var before string
	// Set only DACL information, matching setPrivateTestPermissions. Get-Acl /
	// Set-Acl can also request owner/SACL privileges on otherwise ordinary users.
	setStartupTestDACL(t, dir, "D:P(A;OICI;FA;;;"+p.UserSID+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err := runServicePowerShell(ctx, `(Get-Acl -LiteralPath `+psQuote(dir)+`).Sddl|ConvertTo-Json -Compress`, &before); err != nil {
		t.Fatal(err)
	}
	root, err := openStateRoot(p.options.StateDir, true)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	// The private-state checker intentionally rejects this normal shell folder.
	if root, err := openStateRoot(dir, false); err == nil {
		root.Close()
		t.Fatal("fixture lacks inherited administrative access")
	}
	link, err := callWindowsStartup(p, "create", "")
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateTestPath(t, link.Path, 0600)
	if _, err := callWindowsStartup(p, "delete", link.Hash); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := runServicePowerShell(ctx, `(Get-Acl -LiteralPath `+psQuote(dir)+`).Sddl|ConvertTo-Json -Compress`, &after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("Startup directory permissions changed")
	}
}
func TestWindowsStartupNativeRejectsHardLinkedEntry(t *testing.T) {
	p := windowsServiceFixture(t)
	dir := t.TempDir()
	p.startupDirectory = func() (string, error) { return dir, nil }
	source := filepath.Join(dir, "foreign-file")
	if err := os.WriteFile(source, []byte("foreign shortcut"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, p.TaskName+".lnk")
	if err := os.Link(source, path); err != nil {
		t.Fatal(err)
	}
	if _, err := callWindowsStartup(p, "query", ""); err == nil {
		t.Fatal("accepted hard-linked shortcut")
	}
	if _, err := callWindowsStartup(p, "create", ""); err == nil {
		t.Fatal("overwrote hard-linked shortcut")
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "foreign shortcut" {
		t.Fatal("foreign source changed")
	}
}

func setStartupTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	text, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	var sd uintptr
	ok, _, callErr := convertSD.Call(uintptr(unsafe.Pointer(text)), 1, uintptr(unsafe.Pointer(&sd)), 0)
	if ok == 0 {
		t.Fatal(callErr)
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	ok, _, callErr = fileAdvapi.NewProc("SetFileSecurityW").Call(uintptr(unsafe.Pointer(name)), 4|0x80000000, sd)
	if ok == 0 {
		t.Fatal(callErr)
	}
}
