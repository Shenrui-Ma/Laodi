package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

func TestWindowsNativeShellBridgePreservesRawChineseInput(t *testing.T) {
	state := filepath.Join(t.TempDir(), "中文 空格 '&%!$()")
	payload, _ := json.Marshal(map[string]string{"adapter": "zcode", "state_dir": state})
	command := "laodi-hook-v1:" + base64.RawURLEncoding.EncodeToString(payload)
	input := `{"hook_event_name":"PreToolUse","session_id":"synthetic-session","tool_use_id":"synthetic-read","tool_name":"Read","tool_input":{"file_path":"C:\\独立测试\\.env"}}`
	if !runPlatformHookShell([]string{"-c", command}, strings.NewReader(input)) {
		t.Fatal("bridge not handled")
	}
	batch, err := laodi.ReadHookInbox(state, 32)
	if err != nil || len(batch.Findings) != 1 {
		t.Fatalf("raw UTF-8 hook not recorded: %v %+v", err, batch)
	}
}

func TestWindowsNativeShellBridgeRejectsCommandsAndInvalidData(t *testing.T) {
	for _, args := range [][]string{
		{"-c"}, {"-c", "echo should-never-execute"}, {"-c", "laodi-hook-v1:!"},
		{"-c", "laodi-hook-v1:" + base64.RawURLEncoding.EncodeToString([]byte(`{"adapter":"claude-code","state_dir":"C:\\test"}`))},
		{"-c", "laodi-hook-v1:" + base64.RawURLEncoding.EncodeToString([]byte(`{"adapter":"zcode","state_dir":"relative"}`))},
		{"-c", "laodi-hook-v1:" + base64.RawURLEncoding.EncodeToString([]byte(`{"adapter":"zcode","state_dir":"C:\\test","command":"bad"}`))},
		{"-c", strings.Repeat("x", 16385)},
	} {
		if !runPlatformHookShell(args, strings.NewReader("")) {
			t.Fatal("invalid native bridge call escaped fail-open boundary")
		}
	}
	if runPlatformHookShell([]string{"version"}, nil) {
		t.Fatal("unrelated command handled")
	}
}
