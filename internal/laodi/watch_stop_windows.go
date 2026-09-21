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
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var svcKernel32 = syscall.NewLazyDLL("kernel32.dll")
var svcCreateEvent = svcKernel32.NewProc("CreateEventW")
var svcOpenEvent = svcKernel32.NewProc("OpenEventW")
var svcSetEvent = svcKernel32.NewProc("SetEvent")
var svcQueryImage = svcKernel32.NewProc("QueryFullProcessImageNameW")
var svcMoveFileEx = svcKernel32.NewProc("MoveFileExW")

const monitorProcessReceipt = "monitor-process.json"

type monitorProcess struct {
	Schema     int    `json:"schema_version"`
	PID        uint32 `json:"pid"`
	Created    uint64 `json:"created"`
	Executable string `json:"executable"`
	Event      string `json:"event"`
}

func processIdentity(handle syscall.Handle) (string, uint64, error) {
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", 0, err
	}
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	r, _, err := svcQueryImage.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return "", 0, err
	}
	return syscall.UTF16ToString(buf[:n]), uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

// WatchContext is installed only after the exclusive monitor lock is held.
// Its event is private to this user and this process incarnation. Cancellation
// takes the normal Watch exit path, which flushes state and marks it stopped.
func WatchContext(parent context.Context, stateDir string) (context.Context, context.CancelFunc, error) {
	return windowsProcessContext(parent, stateDir, monitorProcessReceipt)
}

func windowsProcessContext(parent context.Context, stateDir, receiptName string) (context.Context, context.CancelFunc, error) {
	root, err := openStateRoot(stateDir, true)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return nil, nil, err
	}
	executable, created, err := processIdentity(handle)
	if err != nil {
		return nil, nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, err
	}
	eventName := `Local\Laodi.Stop.` + hex.EncodeToString(nonce[:])
	p, err := syscall.UTF16PtrFromString(eventName)
	if err != nil {
		return nil, nil, err
	}
	attrs, free, err := windowsPrivateSecurityAttributes()
	if err != nil {
		return nil, nil, err
	}
	defer free()
	event, _, createErr := svcCreateEvent.Call(uintptr(unsafe.Pointer(attrs)), 1, 0, uintptr(unsafe.Pointer(p)))
	if event == 0 {
		return nil, nil, createErr
	}
	if createErr == syscall.ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(event))
		return nil, nil, errors.New("monitor event already exists")
	}
	record := monitorProcess{Schema: SchemaVersion, PID: uint32(os.Getpid()), Created: created, Executable: executable, Event: eventName}
	data, _ := json.Marshal(record)
	data = append(data, '\n')
	path := filepath.Join(stateDir, receiptName)
	// A crash leaves a receipt. The exclusive monitor lock proves no monitor
	// owns it now; validate the file before replacing it through the backend.
	if err := checkRegularFile(root, receiptName); err != nil && !errors.Is(err, os.ErrNotExist) {
		syscall.CloseHandle(syscall.Handle(event))
		return nil, nil, err
	}
	temp := ".monitor-" + hex.EncodeToString(nonce[:]) + ".tmp"
	f, err := createPrivateFile(root, temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err == nil {
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = replaceStateFile(root, temp, receiptName)
	}
	_ = root.Remove(temp)
	if err != nil {
		syscall.CloseHandle(syscall.Handle(event))
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			result, waitErr := syscall.WaitForSingleObject(syscall.Handle(event), 100)
			if waitErr != nil || result == 0 {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cancel()
			<-done
			_ = syscall.CloseHandle(syscall.Handle(event))
			_ = removeServiceFile(path, serviceHash(data))
		})
	}
	return ctx, cleanup, nil
}

// StopMonitor never terminates a process. Both creation time and executable
// file identity must match before signalling its per-process native event.
func StopMonitor(ctx context.Context, stateDir, expectedExecutable string) error {
	if data, err := readServiceFile(filepath.Join(stateDir, startupSupervisorReceipt)); err == nil {
		// The supervisor also owns the backoff interval, when no child is alive.
		// Its cancellation stops only its exact child and prevents a later retry.
		gone, err := startupSupervisorExited(data)
		if err != nil {
			return err
		}
		if !gone {
			return stopWindowsProcessReceipt(ctx, stateDir, expectedExecutable, startupSupervisorReceipt)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return stopWindowsProcessReceipt(ctx, stateDir, expectedExecutable, monitorProcessReceipt)
}

// A crashed supervisor can leave its independently running worker behind.
// Only a signalled handle, a vanished PID or a different creation time permits
// falling through to the worker receipt; permission failures do not.
func startupSupervisorExited(data []byte) (bool, error) {
	var r monitorProcess
	if decodeWindowsRecord(data, &r) != nil || r.Schema != SchemaVersion || r.PID == 0 || r.Created == 0 {
		return false, errors.New("invalid Startup supervisor receipt")
	}
	h, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, r.PID)
	if errors.Is(err, syscall.Errno(87)) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(h)
	status, err := syscall.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	if status == 0 {
		return true, nil
	}
	_, created, err := processIdentity(h)
	if err != nil {
		return false, err
	}
	return created != r.Created, nil
}

func stopWindowsProcessReceipt(ctx context.Context, stateDir, expectedExecutable, receiptName string) error {
	data, err := readServiceFile(filepath.Join(stateDir, receiptName))
	if err != nil {
		return fmt.Errorf("monitor stop identity unavailable: %w", err)
	}
	var record monitorProcess
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&record) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) || record.Schema != SchemaVersion || record.PID == 0 || record.Created == 0 || !strings.HasPrefix(record.Event, `Local\Laodi.Stop.`) || len(strings.TrimPrefix(record.Event, `Local\Laodi.Stop.`)) != 32 {
		return errors.New("invalid monitor process receipt")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(record.Event, `Local\Laodi.Stop.`)); err != nil {
		return errors.New("invalid monitor event identity")
	}
	process, err := syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, record.PID)
	if err != nil {
		return fmt.Errorf("open exact monitor process: %w", err)
	}
	defer syscall.CloseHandle(process)
	executable, created, err := processIdentity(process)
	if err != nil {
		return err
	}
	actualInfo, err := os.Stat(executable)
	if err != nil {
		return err
	}
	expectedInfo, err := os.Stat(expectedExecutable)
	if err != nil {
		return err
	}
	recordInfo, err := os.Stat(record.Executable)
	if err != nil {
		return err
	}
	if created != record.Created || !os.SameFile(actualInfo, expectedInfo) || !os.SameFile(actualInfo, recordInfo) {
		return errors.New("monitor PID, creation time or executable identity changed; refusing stop")
	}
	p, err := syscall.UTF16PtrFromString(record.Event)
	if err != nil {
		return err
	}
	event, _, eventErr := svcOpenEvent.Call(2, 0, uintptr(unsafe.Pointer(p)))
	if event == 0 {
		return fmt.Errorf("open exact monitor stop event: %w", eventErr)
	}
	defer syscall.CloseHandle(syscall.Handle(event))
	if r, _, err := svcSetEvent.Call(event); r == 0 {
		return err
	}
	for {
		result, err := syscall.WaitForSingleObject(process, 100)
		if err != nil {
			return err
		}
		if result == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("monitor did not stop gracefully: %w", ctx.Err())
		default:
		}
	}
}

// Isolated tests may wait for receipt cleanup rather than process exit when
// the monitor itself runs in their test process.
func waitMonitorReceiptRemoved(ctx context.Context, stateDir string) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := readServiceFile(filepath.Join(stateDir, monitorProcessReceipt)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
