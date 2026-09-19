//go:build windows

package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func windowsDistributionHealthy(dir string, after time.Time) error {
	state, err := LoadState(dir)
	if err != nil {
		return err
	}
	if !state.Running || !state.LastCheckedAt.After(after) {
		return errors.New("monitor heartbeat is not fresh")
	}
	current, err := readWindowsCurrent(dir)
	if err != nil {
		return err
	}
	data, err := readServiceFile(filepath.Join(dir, monitorProcessReceipt))
	if err != nil {
		return err
	}
	var record monitorProcess
	if err := decodeWindowsRecord(data, &record); err != nil {
		return err
	}
	if record.Schema != SchemaVersion || record.PID == 0 || record.Created == 0 {
		return errors.New("monitor identity receipt is invalid")
	}
	process, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, record.PID)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(process)
	if result, err := syscall.WaitForSingleObject(process, 0); err != nil || result != syscall.WAIT_TIMEOUT {
		return errors.New("selected monitor process is not running")
	}
	executable, created, err := processIdentity(process)
	if err != nil {
		return err
	}
	createdFiletime := syscall.Filetime{LowDateTime: uint32(created), HighDateTime: uint32(created >> 32)}
	if !state.LastCheckedAt.After(time.Unix(0, createdFiletime.Nanoseconds())) {
		return errors.New("heartbeat predates the current monitor process")
	}
	actual, err := os.Stat(executable)
	if err != nil {
		return err
	}
	expected, err := os.Stat(filepath.Join(dir, "versions", current.Manifest.Version, "laodi-host.exe"))
	if err != nil {
		return err
	}
	recorded, err := os.Stat(record.Executable)
	if err != nil {
		return err
	}
	if created != record.Created || !os.SameFile(actual, expected) || !os.SameFile(actual, recorded) {
		return errors.New("fresh heartbeat did not come from the selected version's monitor identity")
	}
	return nil
}

func waitWindowsDistributionHealth(dir string, after time.Time) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := windowsDistributionHealthy(dir, after)
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("selected Windows monitor did not publish a fresh verified heartbeat")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
