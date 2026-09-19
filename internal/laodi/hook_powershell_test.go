package laodi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPowerShellReadGrammar(t *testing.T) {
	for _, tc := range []struct{ command, path string }{
		{`Get-Content .env`, `.env`},
		{`get-content -LiteralPath 'C:\中文 项目\a''b &$%!()\.env' -Raw`, `C:\中文 项目\a'b &$%!()\.env`},
		{`Get-Content -Path "C:\project\.aws\credentials" -Encoding utf8 -TotalCount 4`, `C:\project\.aws\credentials`},
		{`Microsoft.PowerShell.Management\Get-Content -Tail 2 -LiteralPath .env.production`, `.env.production`},
		{`Get-Content -LiteralPath 'C:\[literal]\a` + "`" + `b\.env'`, "C:\\[literal]\\a`b\\.env"},
	} {
		paths, parsed := hookPowerShellReadCommandPaths(tc.command)
		if !parsed || !reflect.DeepEqual(paths, []string{tc.path}) {
			t.Errorf("%q: %q %v", tc.command, paths, parsed)
		}
		r := inspectHookFixture(t, "claude-code", "PreToolUse", "PowerShell", map[string]any{"command": tc.command}, nil)
		if counts := hookSignalCounts(t, r, "sensitive_tool_access_requested"); len(counts) != 1 {
			t.Fatalf("missing sensitive access: %#v", r)
		}
		serialized, _ := json.Marshal(r)
		if strings.Contains(string(serialized), tc.path) {
			t.Fatal("raw path escaped lexical inspection")
		}
	}
}

func TestPowerShellComplexCommandsStayUnknown(t *testing.T) {
	for _, command := range []string{
		`Get-Content $env:SECRET`, `Get-Content "$HOME\.env"`, "Get-Content `.env", `Get-Content .env; Write-Output done`,
		`Get-Content .env | Out-String`, `& Get-Content .env`, `'Get-Content' .env`, `Get-Content '.env' '-Raw'`,
		`Get-Content -Path '*.env'`, `Get-Content -LiteralPath .env -Wait`, `Get-Content -Raw -Tail 3 .env`,
		`Get-Content -Tail 2 -TotalCount 3 .env`, `Get-Content -Raw -Raw .env`, `Get-Content -LiteralPath '.env' '-Path' '.env.local'`,
		`Get-Content env:API_KEY`, `Get-Content 'C:\.env:stream'`, `Get-Content @('.env')`, `Get-Content '.env', '.env.local'`,
		`Get-Content -LiteralPath '.env'".local"`, `cat .env`, `type .env`, `cmd.exe /c type .env`, `wsl cat .env`,
		"Get-Content .env\n", "Get-Content .env\x00", `Get-Content -LiteralPath "unterminated`,
	} {
		if paths, parsed := hookPowerShellReadCommandPaths(command); parsed || len(paths) != 0 {
			t.Errorf("complex syntax classified: %q", command)
		}
		r := inspectHookFixture(t, "claude-code", "PreToolUse", "PowerShell", map[string]any{"command": command}, nil)
		if len(r.Signals) != 0 || !containsHookUnknown(r.Unknowns, "shell_command_not_parsed") {
			t.Errorf("complex command claimed observed: %#v", r)
		}
	}
}

func containsHookUnknown(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestPowerShellOutputAndFailureAreInspectedWithoutClaimingDelivery(t *testing.T) {
	post := inspectHookFixture(t, "claude-code", "PostToolUse", "PowerShell", nil, map[string]any{"stdout": "API_KEY=" + hookFakeValue})
	if hookSignalCounts(t, post, "sensitive_tool_output_detected")["credential_assignment"] != 1 {
		t.Fatal(post)
	}
	failure, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUseFailure", "tool_name": "PowerShell", "error": "API_KEY=" + hookFakeValue})
	r := InspectHook("claude-code", strings.NewReader(string(failure)))
	if hookSignalCounts(t, r, "sensitive_tool_output_detected")["credential_assignment"] != 1 {
		t.Fatal(r)
	}
	if !containsHookUnknown(r.Signals[0].Unknowns, "tool_failed") {
		t.Fatal("failure treated as success")
	}
	zcode := inspectHookFixture(t, "zcode", "PreToolUse", "PowerShell", map[string]any{"command": "Get-Content .env"}, nil)
	if len(zcode.Signals) != 0 || !containsHookUnknown(zcode.Unknowns, "tool_not_observed") {
		t.Fatal("undocumented adapter alias guessed")
	}
}

func TestHookRejectsInvalidEncoding(t *testing.T) {
	for _, raw := range []string{"{\"tool_name\":\"\xff\"}", "\xff\xfe{\x00}\x00"} {
		r := InspectHook("claude-code", strings.NewReader(raw))
		if hookSignalCounts(t, r, "hook_coverage_degraded")["input_encoding_unsupported"] != 1 {
			t.Fatal(r)
		}
	}
}
