package laodi

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These are real Windows PowerShell child processes against synthetic files.
// They verify the accepted lexical grammar, not a client's hook delivery.
func TestPowerShellGrammarAgainstNativeProcess(t *testing.T) {
	program := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(program); err != nil {
		t.Fatal("Windows PowerShell is required for this native grammar test")
	}
	dir := filepath.Join(t.TempDir(), "中文 空格 a'b & % ! $ (round) [literal]")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, ".env")
	payload := []byte("API_KEY=" + hookFakeValue + "\r\n")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	command := "Get-Content -LiteralPath '" + strings.ReplaceAll(file, "'", "''") + "' -Raw -Encoding UTF8"
	paths, ok := hookPowerShellReadCommandPaths(command)
	if !ok || len(paths) != 1 || paths[0] != file {
		t.Fatalf("native literal was not classified: %q", paths)
	}
	cmd := exec.Command(program, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	output, err := cmd.Output()
	if err != nil || !bytes.Contains(output, payload) {
		t.Fatalf("native Get-Content failed: %v", err)
	}
	r := inspectHookFixture(t, "claude-code", "PostToolUse", "PowerShell", nil, map[string]any{"stdout": string(output)})
	if hookSignalCounts(t, r, "sensitive_tool_output_detected")["credential_assignment"] != 1 {
		t.Fatal("synthetic native output was not detected")
	}
}
