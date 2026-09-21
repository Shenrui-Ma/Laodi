//go:build windows

package laodi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const startupSupervisorReceipt = "startup-supervisor.json"

// Keep the protocol-1 stable launcher and shortcut unchanged. Only the selected
// versioned host supervises its own worker, and only for the exact owned Startup
// invocation. Task Scheduler, foreground and finite watches retain their policy.
func RunWindowsStartupSupervisor(parent context.Context, stateDir string, args []string) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return true, err
	}
	// Direct setup may use an unversioned development binary. Such watches do
	// not own a current-version pointer and must keep the ordinary watch path.
	if filepath.Base(exe) != "laodi-host.exe" || !sameWindowsInstallationPath(filepath.Dir(filepath.Dir(exe)), filepath.Join(stateDir, "versions")) {
		return false, nil
	}
	data, err := readServiceFile(filepath.Join(stateDir, serviceReceiptName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var receipt serviceReceipt
	if err := decodeWindowsRecord(data, &receipt); err != nil {
		return true, err
	}
	if receipt.Backend != "user_startup" || receipt.LinkArguments != startupArguments(ServicePlan{Arguments: append([]string{""}, args...)}) {
		return false, nil
	}
	current, err := readWindowsCurrent(stateDir)
	if err != nil {
		return true, err
	}
	expected := filepath.Join(stateDir, "versions", current.Manifest.Version, "laodi-host.exe")
	if !sameWindowsInstallationPath(exe, expected) {
		return false, nil
	}
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return true, err
	}
	if receipt.SchemaVersion != SchemaVersion || receipt.Label != serviceLabel || receipt.UserSID != sid || receipt.LinkTarget != filepath.Join(stateDir, "laodi-host.exe") || len(receipt.LinkHash) != 64 {
		return true, errors.New("invalid Startup supervisor ownership")
	}
	unlock, err := acquireWindowsNamedLock(stateDir, ".startup-supervisor.lock")
	if err != nil {
		return true, err
	}
	defer unlock()
	ctx, cleanup, err := windowsProcessContext(parent, stateDir, startupSupervisorReceipt)
	if err != nil {
		return true, err
	}
	defer cleanup()
	approval, err := startupApprovalSnapshot(filepath.Base(receipt.StartupPath))
	if err != nil {
		return true, err
	}
	check := func() (bool, error) {
		latest, err := readServiceFile(filepath.Join(stateDir, serviceReceiptName))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if serviceHash(latest) != serviceHash(data) {
			return false, nil
		}
		link, err := readWindowsStartupFile(receipt.StartupPath)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if serviceHash(link) != receipt.LinkHash {
			return false, nil
		}
		selected, err := readWindowsCurrent(stateDir)
		if err != nil {
			return false, err
		}
		if selected.Manifest.Version != current.Manifest.Version {
			return false, nil
		}
		now, err := startupApprovalSnapshot(filepath.Base(receipt.StartupPath))
		return now == approval, err
	}
	run := func() error {
		cmd := exec.Command(exe, append(append([]string{}, args...), "--startup-worker")...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// A stop may arrive before the child publishes its native event.
			for {
				select {
				case <-done:
					return nil
				default:
				}
				if err := stopWindowsProcessReceipt(deadline, stateDir, exe, monitorProcessReceipt); err == nil {
					<-done
					return nil
				}
				select {
				case <-done:
					return nil
				case <-deadline.Done():
					return errors.New("Startup worker has not stopped; ownership retained")
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
	}
	return true, runStartupRecovery(ctx, check, run, time.Minute, 3)
}

func runStartupRecovery(ctx context.Context, allowed func() (bool, error), run func() error, delay time.Duration, retries int) error {
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return nil
		}
		ok, err := allowed()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		err = run()
		if err == nil || ctx.Err() != nil {
			return err
		}
		if attempt >= retries {
			return fmt.Errorf("Startup recovery exhausted after %d retries: %w", retries, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// StartupApproved is not a public schema. Treat its values as opaque: any
// change (including disable), unknown read failure or size change prevents
// recovery. Never infer "enabled" from undocumented bytes or write this key.
// Initial launch remains the shell's login decision or an explicit installation.
func startupApprovalSnapshot(name string) (string, error) {
	var values string
	for _, hive := range []syscall.Handle{syscall.HKEY_CURRENT_USER, syscall.HKEY_LOCAL_MACHINE} {
		keyName, _ := syscall.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\StartupFolder`)
		var key syscall.Handle
		err := syscall.RegOpenKeyEx(hive, keyName, 0, syscall.KEY_QUERY_VALUE, &key)
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			values += "missing-key;"
			continue
		}
		if err != nil {
			return "", err
		}
		valueName, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			syscall.RegCloseKey(key)
			return "", err
		}
		var kind uint32
		size := uint32(256)
		value := make([]byte, size)
		err = syscall.RegQueryValueEx(key, valueName, nil, &kind, &value[0], &size)
		syscall.RegCloseKey(key)
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			values += "missing-value;"
			continue
		}
		if err != nil {
			return "", err
		}
		if size > uint32(len(value)) {
			return "", errors.New("Startup preference exceeds bounded size")
		}
		values += fmt.Sprintf("%d:%x;", kind, value[:size])
	}
	return serviceHash([]byte(values)), nil
}
