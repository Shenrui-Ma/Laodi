//go:build windows

package laodi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var windowsNotificationCommand = exec.CommandContext

// RunWindowsNotificationHelper invokes one verified version payload. Selection
// and payload hashes are verified by the caller; registration identity remains
// bound to the stable launcher in stateDir across version switches.
func RunWindowsNotificationHelper(ctx context.Context, helper, stateDir string, args ...string) ([]byte, error) {
	if !filepath.IsAbs(helper) || !filepath.IsAbs(stateDir) || strings.ContainsAny(helper+stateDir, "\x00\r\n") {
		return nil, errors.New("notification helper and state directory must be absolute paths")
	}
	if !validWindowsNotificationArguments(args) {
		return nil, errors.New("invalid notification helper action")
	}
	limit := 12 * time.Second
	if args[0] == "--com-server" {
		limit = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	argv := append([]string{"--state-dir", stateDir}, args...)
	cmd := windowsNotificationCommand(ctx, helper, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var stdout, stderr notificationBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	data := bytes.TrimSpace(stdout.Bytes())
	if len(data) == 0 && args[0] == "--com-server" && err == nil {
		return nil, nil
	}
	if ctx.Err() != nil {
		return data, fmt.Errorf("notification helper timed out: %w", ctx.Err())
	}
	var response struct {
		Schema    int    `json:"schema_version"`
		Action    string `json:"action"`
		OK        bool   `json:"ok"`
		Delivery  string `json:"delivery"`
		ErrorCode string `json:"error_code"`
	}
	if json.Unmarshal(data, &response) != nil || response.Schema != 1 || response.Action != strings.TrimPrefix(args[0], "--") {
		return data, errors.New("notification helper returned invalid or mismatched JSON")
	}
	if err != nil {
		return data, fmt.Errorf("notification helper action failed: %w", err)
	}
	if !response.OK {
		return data, fmt.Errorf("notification helper reported %s", response.ErrorCode)
	}
	return data, nil
}

func validWindowsNotificationArguments(args []string) bool {
	if len(args) == 1 {
		switch args[0] {
		case "--register", "--unregister", "--status", "--test-template", "--test-activation", "--com-server":
			return true
		}
	}
	if len(args) == 2 && args[0] == "--activate" && args[1] == "status" {
		return true
	}
	if len(args) != 5 || args[0] != "--send" || args[1] != "--id" || args[3] != "--kind" || len(args[2]) < 1 || len(args[2]) > 128 {
		return false
	}
	for _, c := range args[2] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	switch args[4] {
	case "test", "archive-blocked-test", "snapshot-history", "upload-attempt", "upload-accepted", "snapshot-workspace", "workspace-upload-attempt", "workspace-upload-accepted", "snapshot-config", "config-upload-attempt", "config-upload-accepted", "tool-output-sensitive", "hook-coverage-degraded", "coverage-degraded":
		return true
	}
	return false
}

// Do not embed bytes.Buffer: its promoted ReadFrom lets io.Copy bypass Write
// and would silently remove this limit when exec drains a child pipe.
type notificationBuffer struct{ buffer bytes.Buffer }

func (b *notificationBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *notificationBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 16<<10 {
		return 0, errors.New("notification helper output exceeds 16 KiB")
	}
	return b.buffer.Write(p)
}
