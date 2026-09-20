//go:build windows

package laodi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsNotificationFakeProcess(t *testing.T) {
	mode := os.Getenv("LAODI_FAKE_NOTIFICATION_PROCESS")
	if mode == "" {
		return
	}
	switch mode {
	case "success":
		fmt.Print(`{"schema_version":1,"action":"status","ok":true,"delivery":"not_requested","authorization":"unknown","authorization_error_code":"0x80070490"}`)
	case "wrong-action":
		fmt.Print(`{"schema_version":1,"action":"register","ok":true}`)
	case "malformed":
		fmt.Print("not JSON")
	case "failure":
		fmt.Print(`{"schema_version":1,"action":"status","ok":false,"error_code":"synthetic_api_failure"}`)
	case "overflow":
		fmt.Print(strings.Repeat("x", 32<<10))
	case "timeout":
		time.Sleep(5 * time.Second)
	}
	os.Exit(0)
}

func TestWindowsNotificationProtocol(t *testing.T) {
	for _, mode := range []string{"success", "wrong-action", "malformed", "failure", "overflow", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			old := windowsNotificationCommand
			t.Cleanup(func() { windowsNotificationCommand = old })
			state := filepath.Join(t.TempDir(), "state 中文 & % !")
			helper := filepath.Join(t.TempDir(), "LaodiNotify.exe")
			windowsNotificationCommand = func(ctx context.Context, executable string, args ...string) *exec.Cmd {
				if executable != helper || len(args) != 3 || args[0] != "--state-dir" || args[1] != state || args[2] != "--status" {
					t.Fatalf("unexpected helper argv: %q %q", executable, args)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("helper without deadline")
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWindowsNotificationFakeProcess$")
				cmd.Env = append(os.Environ(), "LAODI_FAKE_NOTIFICATION_PROCESS="+mode)
				return cmd
			}
			ctx := context.Background()
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			data, err := RunWindowsNotificationHelper(ctx, helper, state, "--status")
			if (mode == "success") != (err == nil) {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if len(data) > 16<<10 {
				t.Fatal("unbounded notification response")
			}
			if mode == "success" && !strings.Contains(string(data), "0x80070490") {
				t.Fatal("unknown OS status was lost")
			}
		})
	}
}

func TestWindowsNotificationRejectsUntrustedArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--activate", "untrusted"}, {"--status", "extra"}, {"--send", "--id", "SYNTHETIC secret", "--kind", "test"}, {"--send", "--id", "id", "--kind", "arbitrary title"}, {"--state-dir", "foreign", "--status"}} {
		if validWindowsNotificationArguments(args) {
			t.Fatalf("untrusted args accepted: %q", args)
		}
	}
	if !validWindowsNotificationArguments([]string{"--send", "--id", "valid-synthetic-id", "--kind", "test"}) {
		t.Fatal("fixed send protocol rejected")
	}
	if !validWindowsNotificationArguments([]string{"--test-activation"}) || validWindowsNotificationArguments([]string{"--test-activation", "foreign-clsid"}) {
		t.Fatal("activation diagnostic must have no caller-defined COM target")
	}
}
