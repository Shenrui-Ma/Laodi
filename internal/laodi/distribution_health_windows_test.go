//go:build windows

package laodi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWindowsHealthReceiptHelper(t *testing.T) {
	if os.Getenv("LAODI_HEALTH_HANDSHAKE") != "1" {
		return
	}
	dir := os.Getenv("LAODI_HEALTH_STATE")
	release, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, cleanup, err := WatchContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fmt.Fprintln(os.Stdout, "receipt")
	var signal [1]byte
	if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	state.Running = true
	state.LastCheckedAt = time.Now()
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "heartbeat")
	_, _ = io.ReadFull(os.Stdin, signal[:])
}

// A restarted copy of the same version publishes its process receipt before
// its first heartbeat. The predecessor's fresh state must not make it healthy.
func TestWindowsHealthRejectsPredecessorHeartbeatAfterSameVersionRestart(t *testing.T) {
	source, manifest := windowsPayloadFixture(t, "v0.4.0-health-restart")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Files["laodi-host.exe"] = serviceHash(binary)
	if err := os.WriteFile(filepath.Join(source, "laodi-host.exe"), binary, 0600); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(source, windowsManifestName), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, dir)); err != nil {
		t.Fatal(err)
	}
	predecessor := emptyState()
	predecessor.Running = true
	predecessor.LastCheckedAt = time.Now().Add(-time.Second)
	if err := SaveState(dir, predecessor); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(dir, "versions", manifest.Version, "laodi-host.exe"), "-test.run=^TestWindowsHealthReceiptHelper$")
	cmd.Env = append(os.Environ(), "LAODI_HEALTH_HANDSHAKE=1", "LAODI_HEALTH_STATE="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		input.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("health helper: %v %s", err, diagnostics.String())
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("health helper did not exit")
		}
	}()
	lines := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	await := func(want string) {
		t.Helper()
		select {
		case line := <-lines:
			if line != want {
				t.Fatalf("health helper stage %q, want %q", line, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("health helper never reached %s", want)
		}
	}
	await("receipt")
	after := predecessor.LastCheckedAt.Add(-time.Second)
	if err := windowsDistributionHealthy(dir, after); err == nil || !strings.Contains(err.Error(), "heartbeat predates") {
		t.Fatalf("accepted predecessor heartbeat for new process: %v", err)
	}
	if _, err := input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	await("heartbeat")
	if err := windowsDistributionHealthy(dir, after); err != nil {
		t.Fatalf("restarted process's own heartbeat rejected: %v", err)
	}
}
