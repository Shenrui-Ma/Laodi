//go:build windows

package laodi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Exercise the real protocol-1 stable launcher and its versioned child. Every
// executable, ownership record and shortcut is confined to synthetic folders.
func nativeStartupVersionedFixture(t *testing.T) (ServicePlan, func(string) ServicePlan) {
	t.Helper()
	exe := os.Getenv("LAODI_NATIVE_STARTUP_MONITOR_EXE")
	if exe == "" {
		t.Skip("explicit native monitor fixture binary required")
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	state := filepath.Join(base, "state")
	shortcuts := t.TempDir()
	selectVersion := func(tag string) ServicePlan {
		m := WindowsManifest{Schema: 1, Protocol: 1, Version: tag, Files: map[string]string{}}
		for _, name := range []string{"laodi.exe", "laodi-host.exe", "laodi-logo.png", "SKILL.md"} {
			data := []byte("synthetic fixture " + tag)
			if filepath.Ext(name) == ".exe" {
				data = binary
				if _, err := os.Stat(filepath.Join(state, name)); errors.Is(err, os.ErrNotExist) {
					if err := writeWindowsBytes(state, name, data, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			m.Files[name] = serviceHash(data)
			if err := writeWindowsBytes(filepath.Join(state, "versions", tag), name, data, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeWindowsRecord(filepath.Join(state, "versions", tag), windowsManifestName, m); err != nil {
			t.Fatal(err)
		}
		c := windowsCurrent{Manifest: m, Launchers: map[string]string{"laodi.exe": serviceHash(binary), "laodi-host.exe": serviceHash(binary)}}
		if err := writeWindowsRecord(state, windowsCurrentName, c); err != nil {
			t.Fatal(err)
		}
		p, err := PlanService(ServiceOptions{Executable: filepath.Join(state, "laodi-host.exe"), MonitorExecutable: filepath.Join(state, "versions", tag, "laodi-host.exe"), Home: base, StateDir: state, HooksOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		p.startupDirectory = func() (string, error) { return shortcuts, nil }
		accessDeniedTask(t, &p)
		return p
	}
	return selectVersion("v0.4.1-crash.1"), selectVersion
}

func awaitNativeStartupMonitor(t *testing.T, p ServicePlan) monitorProcess {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if verifyWindowsStartupMonitor(p) == nil {
			data, err := readServiceFile(filepath.Join(p.options.StateDir, monitorProcessReceipt))
			var r monitorProcess
			if err == nil && decodeWindowsRecord(data, &r) == nil {
				state, err := LoadState(p.options.StateDir)
				if err == nil && state.Initialized && state.Running {
					return r
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("versioned monitor did not become healthy")
	return monitorProcess{}
}

func crashNativeStartupMonitor(t *testing.T, p ServicePlan, r monitorProcess) {
	t.Helper()
	// Open and validate the same native handle that is terminated; never signal
	// a process merely because it reused the recorded PID or executable name.
	h, err := syscall.OpenProcess(1|0x1000|syscall.SYNCHRONIZE, false, r.PID)
	if err != nil {
		t.Fatalf("open exact synthetic child for crash injection: %v", err)
	}
	defer syscall.CloseHandle(h)
	image, created, err := processIdentity(h)
	if err != nil || created != r.Created || image != p.options.MonitorExecutable || r.Executable != image {
		t.Fatal("synthetic child identity changed; refusing crash injection")
	}
	if err := syscall.TerminateProcess(h, 73); err != nil {
		t.Fatalf("terminate exact synthetic child: %v", err)
	}
	if result, err := syscall.WaitForSingleObject(h, 10000); err != nil || result != 0 {
		t.Fatal("synthetic child did not exit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := waitWindowsStartupStopped(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsStartupMonitor(p); err == nil {
		t.Fatal("crashed monitor was accepted as healthy")
	}
}

func TestWindowsStartupNativeVersionedCrashLifecycle(t *testing.T) {
	for _, action := range []string{"reinstall_update", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			p, selectVersion := nativeStartupVersionedFixture(t)
			if err := InstallService(p); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if owned, _ := inspectServiceOwnership(p); owned {
					if err := UninstallService(p); err != nil {
						t.Error(err)
					}
				}
			})
			before := awaitNativeStartupMonitor(t, p)
			if err := InstallService(p); err != nil {
				t.Fatal(err)
			}
			if after := awaitNativeStartupMonitor(t, p); after != before {
				t.Fatal("repeat install replaced the healthy monitor")
			}
			crashNativeStartupMonitor(t, p, before)
			t.Log("real versioned child exited with code 73; protocol-1 launcher exited and native monitor lock released")
			if action == "reinstall_update" {
				if err := InstallService(p); err != nil {
					t.Fatal(err)
				}
				if after := awaitNativeStartupMonitor(t, p); after.Created == before.Created && after.PID == before.PID {
					t.Fatal("explicit reinstall did not start a new monitor")
				}
				if err := stopWindowsService(p); err != nil {
					t.Fatal(err)
				}
				state, err := LoadState(p.options.StateDir)
				if err != nil || state.Running {
					t.Fatal("normal stop did not persist stopped state")
				}
				// Use the same stop/select/start boundary as the updater, with
				// unchanged stable executables and service receipt/shortcut.
				p = selectVersion("v0.4.1-crash.2")
				if err := startWindowsService(p); err != nil {
					t.Fatal(err)
				}
				awaitNativeStartupMonitor(t, p)
			}
			if err := UninstallService(p); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(p.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("service ownership remained after uninstall")
			}
			link, err := callWindowsStartup(p, "query", "")
			if err != nil || link.Exists {
				t.Fatal("owned synthetic shortcut remained after uninstall")
			}
			if err := startWindowsService(p); err == nil {
				t.Fatal("uninstalled service restarted")
			}
			if running, err := startupLauncherRunning(p); err != nil || running {
				t.Fatal("launcher remained after uninstall")
			}
		})
	}
}

func TestWindowsStartupStopDeadlineRetainsLiveLauncher(t *testing.T) {
	p := windowsServiceFixture(t)
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	image, created, err := processIdentity(h)
	if err != nil {
		t.Fatal(err)
	}
	r := startupLauncher{Schema: 1, PID: uint32(os.Getpid()), Created: created, Executable: image}
	if err := writeWindowsRecord(p.options.StateDir, startupLauncherReceipt, r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := waitWindowsStartupStopped(ctx, p); err == nil {
		t.Fatal("live launcher was treated as stopped")
	}
	if running, err := startupLauncherRunning(p); err != nil || !running {
		t.Fatal("deadline changed live launcher ownership")
	}
	// A recycled PID must not address the unrelated process now using it.
	r.Created++
	if err := writeWindowsRecord(p.options.StateDir, startupLauncherReceipt, r); err != nil {
		t.Fatal(err)
	}
	if running, err := startupLauncherRunning(p); err != nil || running {
		t.Fatal("recycled PID was accepted as the installed launcher")
	}
}
