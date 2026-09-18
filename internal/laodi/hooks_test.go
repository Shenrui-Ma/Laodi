package laodi

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// These are deterministic synthetic fixtures, never live credentials.
const hookFakeToken = "ghp_Q7m2Lp9Rx4Vt8Na3Ks6Yw1Bd5Jc0HfUzEeAi"
const hookFakeValue = "L4p9Vz2Rk7Wq8Mn3Bc6Tf1Yu0Hs5XeAa"
const hookFakePEM = "-----BEGIN PRIVATE KEY-----\nTGFvZGktc3ludGhldGljLWZpeHR1cmUtbm90LWEtcmVhbC1wcml2YXRlLWtleQ==\n-----END PRIVATE KEY-----"

func inspectHookFixture(t *testing.T, adapter, event, tool string, args, response any) HookInspection {
	t.Helper()
	value := map[string]any{
		"session_id": "synthetic-session", "tool_use_id": "synthetic-tool",
		"hook_event_name": event, "tool_name": tool, "tool_input": args,
	}
	if response != nil {
		value["tool_response"] = response
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return InspectHook(adapter, strings.NewReader(string(data)))
}

func hookSignalCounts(t *testing.T, r HookInspection, kind string) map[string]int {
	t.Helper()
	for _, signal := range r.Signals {
		if signal.Kind == kind {
			return signal.Counts
		}
	}
	t.Fatalf("missing %s in %#v", kind, r)
	return nil
}

func TestHookDirectSensitiveAccess(t *testing.T) {
	for _, adapter := range []string{"zcode", "claude-code"} {
		for _, tc := range []struct{ tool, file, command, category string }{
			{"Read", "/workspace/.env", "", "env_file"},
			{"Read", ".env.production", "", "env_file"},
			{"Read", `C:\Users\person\.aws\credentials`, "", "credential_file"},
			{"Read", ".ssh/id_ed25519", "", "private_key_file"},
			{"Bash", "", "cat -- '/some project/.env.local'", "env_file"},
			{"Bash", "", "/bin/cat -n ~/.ssh/id_rsa", "private_key_file"},
			{"Bash", "", "head -n 20 ~/.aws/credentials", "credential_file"},
			{"Bash", "", "tail -c 40 .env", "env_file"},
			{"Bash", "", "sed -n '1,20p' .env", "env_file"},
			{"Bash", "", "sed -n -e p ~/.npmrc", "credential_file"},
		} {
			t.Run(adapter+"/"+tc.tool+"/"+tc.file+tc.command, func(t *testing.T) {
				args := map[string]any{"file_path": tc.file, "command": tc.command}
				r := inspectHookFixture(t, adapter, "PreToolUse", tc.tool, args, nil)
				counts := hookSignalCounts(t, r, "sensitive_tool_access_requested")
				if counts[tc.category] != 1 || len(counts) != 1 {
					t.Fatalf("unexpected counts: %v", counts)
				}
				if !reflect.DeepEqual(r.Signals[0].Unknowns, []string{"read_not_confirmed", "upload_not_observed"}) {
					t.Fatal("access request must not imply read or upload")
				}
			})
		}
	}
}

func TestHookAccessDoesNotTreatListingOrComplexShellAsReading(t *testing.T) {
	for _, command := range []string{
		"ls ~/.ssh", "ls -la .env", "git log --oneline -10", "echo cat .env",
		"python -c 'print(open(\".env\").read())'", "cat $(echo .env)",
		"cat .env | head -n 4", "cat .env; echo done", "cat $SECRET_PATH",
		"cat .env > /tmp/local-copy", "cat *.env", "sed -i 's/a/b/' .env",
	} {
		r := inspectHookFixture(t, "zcode", "PreToolUse", "Bash", map[string]any{"command": command}, nil)
		if len(r.Signals) != 0 {
			t.Fatalf("unparsed command must not generate fabricated access: %q: %#v", command, r)
		}
		if !reflect.DeepEqual(r.Unknowns, []string{"shell_command_not_parsed"}) {
			t.Fatalf("must expose parsing boundary for %q: %#v", command, r)
		}
	}
	for _, file := range []string{"README.md", ".env.example", ".env.sample", ".env.template", "~/.ssh/id_ed25519.pub", "~/.ssh/known_hosts"} {
		r := inspectHookFixture(t, "claude-code", "PreToolUse", "Read", map[string]any{"file_path": file}, nil)
		if len(r.Signals) != 0 {
			t.Fatalf("ordinary file generated incident: %q", file)
		}
	}
}

func TestHookOutputAdaptersAndBodyShapes(t *testing.T) {
	for _, tc := range []struct {
		name, adapter, tool, category string
		response                      any
	}{
		{"zcode-output", "zcode", "Bash", "provider_token", map[string]any{"output": hookFakeToken, "metadata": map[string]any{"ignored": "value"}}},
		{"claude-stdout", "claude-code", "Bash", "provider_token", map[string]any{"stdout": hookFakeToken, "stderr": "", "interrupted": false}},
		{"claude-stderr", "claude-code", "Bash", "credential_assignment", map[string]any{"stdout": "", "stderr": "OPENAI_API_KEY=" + hookFakeValue}},
		{"claude-read", "claude-code", "Read", "private_key_block", map[string]any{"type": "text", "file": map[string]any{"filePath": "/never/open/me", "content": hookFakePEM}}},
		{"zcode-string", "zcode", "Read", "credential_assignment", "export SERVICE_AUTH_TOKEN=" + hookFakeValue},
		{"zcode-content", "zcode", "Read", "provider_token", map[string]any{"content": []any{map[string]any{"type": "text", "text": hookFakeToken}}}},
		{"content-array", "claude-code", "Read", "provider_token", []any{map[string]any{"type": "text", "text": hookFakeToken}}},
		{"numbered-read", "claude-code", "Read", "credential_assignment", "   1→API_KEY=" + hookFakeValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := inspectHookFixture(t, tc.adapter, "PostToolUse", tc.tool, map[string]any{}, tc.response)
			counts := hookSignalCounts(t, r, "sensitive_tool_output_detected")
			if counts[tc.category] != 1 {
				t.Fatalf("wrong detection: %#v", r)
			}
		})
	}
}

func TestHookOutputFalsePositiveControls(t *testing.T) {
	for _, value := range []string{
		"ce60323d style: lint", strings.Repeat("abcdef0123456789", 4),
		"a0387755-645e-4616-9751-e6d583c3d183", "SGVsbG8gd29ybGQgdGhpcyBpcyBub3QgYSBzZWNyZXQ=",
		"API_KEY=your_api_key_here", "API_KEY=<redacted>", "API_KEY=sk-REDACTED_REDACTED_REDACTED", "TOKEN=********************",
		"PASSWORD=change_me_before_use", "TOKEN=example_token_do_not_use", "TOKEN=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"TOKEN=a0387755-645e-4616-9751-e6d583c3d183", "HASH=" + strings.Repeat("abcdef0123456789", 4),
		"github_pat_" + strings.Repeat("a", 80), "sk-proj-" + strings.Repeat("x", 80),
		"commit = " + hookFakeValue, "-----BEGIN PRIVATE KEY-----", "-----BEGIN PUBLIC KEY-----\nTGFvZGktc3ludGhldGljLWZpeHR1cmUtbm90LWEtcmVhbC1wcml2YXRlLWtleQ==\n-----END PUBLIC KEY-----",
	} {
		r := inspectHookFixture(t, "claude-code", "PostToolUse", "Bash", map[string]any{}, map[string]any{"stdout": value})
		if len(r.Signals) != 0 {
			t.Fatalf("ordinary/redacted output generated incident: %q: %#v", value, r)
		}
	}
}

func TestHookDoesNotScanInputsOrMetadata(t *testing.T) {
	for _, tool := range []string{"Bash", "Read", "Write", "Edit", "mcp__server__Read"} {
		value := map[string]any{
			"hook_event_name": "PostToolUse", "tool_name": tool,
			"tool_input":    map[string]any{"content": hookFakePEM, "command": "echo " + hookFakeToken},
			"tool_response": map[string]any{"stdout": "ordinary output", "filePath": hookFakeToken, "metadata": map[string]any{"content": hookFakePEM}},
			"cwd":           hookFakeToken, "transcript_path": hookFakePEM, "session_id": hookFakeToken, "tool_use_id": hookFakeValue,
		}
		data, _ := json.Marshal(value)
		r := InspectHook("zcode", strings.NewReader(string(data)))
		if len(r.Signals) != 0 {
			t.Fatalf("scanned inputs/metadata for %s: %#v", tool, r)
		}
		serialized, _ := json.Marshal(r)
		for _, forbidden := range []string{hookFakeToken, hookFakePEM, hookFakeValue, "filePath", "transcript_path", "session_id", "tool_use_id"} {
			if strings.Contains(string(serialized), forbidden) {
				t.Fatalf("raw metadata escaped serialization: %q", forbidden)
			}
		}
	}
}

func TestHookSignalsNeverSerializeMatchedText(t *testing.T) {
	secret := hookFakePEM + "\nAPI_KEY=" + hookFakeValue + "\n" + hookFakeToken
	r := inspectHookFixture(t, "zcode", "PostToolUse", "Read", map[string]any{"file_path": "/private/unique-user/.env"}, secret)
	counts := hookSignalCounts(t, r, "sensitive_tool_output_detected")
	if len(counts) != 3 {
		t.Fatalf("expected three categories: %v", counts)
	}
	serialized, _ := json.Marshal(r)
	for _, forbidden := range []string{hookFakeToken, hookFakeValue, "TGFvZGkt", "unique-user", "synthetic-session", "synthetic-tool", "PRIVATE KEY", "API_KEY"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("content leaked into result: %q", forbidden)
		}
	}
}

func TestHookFailuresRemainFailures(t *testing.T) {
	for _, adapter := range []string{"zcode", "claude-code"} {
		value := map[string]any{"hook_event_name": "PostToolUseFailure", "tool_name": "Bash", "session_id": "s", "tool_use_id": "t", "error": "command failed: API_KEY=" + hookFakeValue}
		// An assignment must be on a supported boundary; error may include a
		// credential in a separate output line.
		value["error"] = "command failed\nAPI_KEY=" + hookFakeValue
		data, _ := json.Marshal(value)
		r := InspectHook(adapter, strings.NewReader(string(data)))
		hookSignalCounts(t, r, "sensitive_tool_output_detected")
		if r.EventName != "PostToolUseFailure" || r.Signals[0].Unknowns[len(r.Signals[0].Unknowns)-1] != "tool_failed" {
			t.Fatalf("failed tool was misrepresented: %#v", r)
		}
		delete(value, "error")
		data, _ = json.Marshal(value)
		r = InspectHook(adapter, strings.NewReader(string(data)))
		if len(r.Signals) != 0 || !reflect.DeepEqual(r.Unknowns, []string{"failed_tool_no_response"}) {
			t.Fatalf("missing failure output claimed success: %#v", r)
		}
	}
}

func TestHookCamelAliasesOnlyForZCode(t *testing.T) {
	data := `{"hookEventName":"PostToolUse","toolName":"Bash","toolResponse":{"output":"` + hookFakeToken + `"},"sessionId":"s","toolUseId":"t"}`
	r := InspectHook("zcode", strings.NewReader(data))
	hookSignalCounts(t, r, "sensitive_tool_output_detected")
	if r.SessionID != "s" || r.ToolUseID != "t" {
		t.Fatal("ZCode IDs not normalized")
	}
	r = InspectHook("claude-code", strings.NewReader(data))
	if hookSignalCounts(t, r, "hook_coverage_degraded")["schema_unsupported"] != 1 {
		t.Fatal("Claude must not guess undocumented aliases")
	}
	data = `{"hook_event_name":"PreToolUse","hookEventName":"PostToolUse","tool_name":"Bash"}`
	r = InspectHook("zcode", strings.NewReader(data))
	if hookSignalCounts(t, r, "hook_coverage_degraded")["schema_unsupported"] != 1 {
		t.Fatal("conflicting aliases were accepted")
	}
}

func TestHookMalformedAndOversizeInputs(t *testing.T) {
	for _, tc := range []struct{ name, input, reason string }{
		{"empty", "", "input_invalid"},
		{"invalid-json", `{"hook_event_name":`, "input_invalid"},
		{"trailing-json", `{} {}`, "input_invalid"},
		{"duplicate", `{"hook_event_name":"PreToolUse","hook_event_name":"PostToolUse"}`, "input_invalid"},
		{"array", `[]`, "schema_unsupported"},
		{"missing-event", `{}`, "schema_unsupported"},
		{"unknown-event", `{"hook_event_name":"PostSecretTransmit"}`, "schema_unsupported"},
		{"wrong-tool", `{"hook_event_name":"PreToolUse","tool_name":42}`, "schema_unsupported"},
		{"missing-output", `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`, "output_missing"},
		{"metadata-only", `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_response":{"outputFile":"/never/read","metadata":{"content":"` + hookFakeToken + `"}}}`, "output_shape_unsupported"},
		{"size-limit", strings.Repeat(" ", MaxHookInputBytes+1), "input_too_large"},
		{"nesting-limit", strings.Repeat("[", maxHookDepth+2) + "0" + strings.Repeat("]", maxHookDepth+2), "input_depth_exceeded"},
		{"node-limit", "[" + strings.Repeat("0,", maxHookNodes) + "0]", "input_nodes_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := InspectHook("zcode", strings.NewReader(tc.input))
			counts := hookSignalCounts(t, r, "hook_coverage_degraded")
			if counts[tc.reason] != 1 || len(counts) != 1 {
				t.Fatalf("unexpected reason: %v", counts)
			}
		})
	}
}

type hookFailReader struct{}

func (hookFailReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic raw secret must not escape")
}

func TestHookReaderFailureAndUnsupportedAdapter(t *testing.T) {
	for _, tc := range []struct {
		adapter, reason string
		reader          io.Reader
	}{
		{"zcode", "input_read_failed", hookFailReader{}},
		{"claude-code", "input_invalid", nil},
		{"unknown-product-containing-a-secret", "adapter_unsupported", hookFailReader{}},
	} {
		r := InspectHook(tc.adapter, tc.reader)
		if hookSignalCounts(t, r, "hook_coverage_degraded")[tc.reason] != 1 {
			t.Fatalf("incorrect failure classification: %#v", r)
		}
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "secret") {
			t.Fatal("unknown adapter or error text escaped")
		}
	}
}

func TestHookBenignAndUnsupportedScopesStayQuiet(t *testing.T) {
	for _, response := range []any{"", map[string]any{"stdout": "", "stderr": ""}, []any{}} {
		r := inspectHookFixture(t, "claude-code", "PostToolUse", "Bash", map[string]any{}, response)
		if len(r.Signals) != 0 {
			t.Fatalf("empty supported body is not a gap: %#v", r)
		}
	}
	r := inspectHookFixture(t, "zcode", "PostToolUse", "Write", map[string]any{"content": hookFakeToken}, nil)
	if len(r.Signals) != 0 || !reflect.DeepEqual(r.Unknowns, []string{"tool_not_observed"}) {
		t.Fatalf("unsupported tool caused incident storm: %#v", r)
	}
	r = InspectHook("zcode", strings.NewReader(`{"hook_event_name":"SessionStart","prompt":"`+hookFakeToken+`"}`))
	if len(r.Signals) != 0 || !reflect.DeepEqual(r.Unknowns, []string{"event_not_observed"}) {
		t.Fatalf("non-tool event inspected: %#v", r)
	}
}

func TestHookRulesAndCountsAreBounded(t *testing.T) {
	r := inspectHookFixture(t, "zcode", "PostToolUse", "Bash", map[string]any{}, strings.Repeat(hookFakeToken+"\n", 1000))
	counts := hookSignalCounts(t, r, "sensitive_tool_output_detected")
	if counts["provider_token"] != maxHookRuleCount {
		t.Fatalf("counts unbounded or incorrect: %v", counts)
	}
}

func TestHookReadFileCamelPathAndMixedContent(t *testing.T) {
	r := inspectHookFixture(t, "zcode", "PreToolUse", "read_file", map[string]any{"filePath": "/home/person/.env"}, nil)
	hookSignalCounts(t, r, "sensitive_tool_access_requested")
	r = inspectHookFixture(t, "zcode", "PostToolUse", "Read", map[string]any{}, map[string]any{"content": []any{
		map[string]any{"type": "image", "data": hookFakeToken},
		map[string]any{"type": "text", "text": "ordinary text"},
	}})
	if len(r.Signals) != 1 || hookSignalCounts(t, r, "hook_coverage_degraded")["output_partial"] != 1 || !reflect.DeepEqual(r.Unknowns, []string{"response_fields_not_observed"}) {
		t.Fatalf("image payload was scanned or partial coverage hidden: %#v", r)
	}
}

func TestHookTruncatedAndMixedOutputsRetainCoverageGap(t *testing.T) {
	for _, flag := range []string{"truncated", "isTruncated"} {
		for _, text := range []string{"ordinary output", hookFakeToken} {
			r := inspectHookFixture(t, "zcode", "PostToolUse", "Bash", map[string]any{}, map[string]any{"stdout": text, flag: true})
			if hookSignalCounts(t, r, "hook_coverage_degraded")["output_truncated"] != 1 {
				t.Fatalf("missing truncation evidence: %#v", r)
			}
			wantSignals := 1
			if text == hookFakeToken {
				wantSignals = 2
				hookSignalCounts(t, r, "sensitive_tool_output_detected")
			}
			if len(r.Signals) != wantSignals {
				t.Fatalf("detection or coverage gap lost: %#v", r)
			}
		}
	}
	r := inspectHookFixture(t, "claude-code", "PostToolUse", "Read", map[string]any{}, []any{
		map[string]any{"type": "text", "text": hookFakeToken},
		map[string]any{"type": "image", "source": map[string]any{"data": hookFakePEM}},
	})
	if len(r.Signals) != 2 || hookSignalCounts(t, r, "hook_coverage_degraded")["output_partial"] != 1 {
		t.Fatalf("mixed body missing persisted gap: %#v", r)
	}
	counts := hookSignalCounts(t, r, "sensitive_tool_output_detected")
	if counts["provider_token"] != 1 || counts["private_key_block"] != 0 {
		t.Fatalf("scanned image metadata: %v", counts)
	}
}

func TestHookHighEntropyHexCredentialAssignment(t *testing.T) {
	value := "a4b9d67e2f1c0853e8d0a26f4951bc7364a7ec1d9f502b683ac04d5e798162fb"
	for _, key := range []string{"API_KEY", "SERVICE_SECRET_KEY", "TOKEN"} {
		r := inspectHookFixture(t, "zcode", "PostToolUse", "Bash", map[string]any{}, key+"="+value)
		if hookSignalCounts(t, r, "sensitive_tool_output_detected")["credential_assignment"] != 1 {
			t.Fatalf("hex credential assignment was silently excluded: %#v", r)
		}
	}
	for _, text := range []string{value, "SHA256=" + value, "commit " + value} {
		r := inspectHookFixture(t, "claude-code", "PostToolUse", "Bash", map[string]any{}, text)
		if len(r.Signals) != 0 {
			t.Fatalf("ordinary hash triggered detection: %#v", r)
		}
	}
}

func TestHookPartialAndTruncatedReasonsMergeWithoutDroppingDetection(t *testing.T) {
	r := inspectHookFixture(t, "zcode", "PostToolUse", "Read", map[string]any{}, map[string]any{
		"content":   []any{map[string]any{"type": "text", "text": hookFakeToken}, map[string]any{"type": "image", "data": "not-scanned"}},
		"truncated": true,
	})
	if len(r.Signals) != 2 {
		t.Fatalf("one signal per kind required by queue schema: %#v", r)
	}
	gaps := hookSignalCounts(t, r, "hook_coverage_degraded")
	if len(gaps) != 2 || gaps["output_partial"] != 1 || gaps["output_truncated"] != 1 {
		t.Fatalf("coverage reasons not merged: %v", gaps)
	}
	hookSignalCounts(t, r, "sensitive_tool_output_detected")
	for _, signal := range r.Signals {
		if signal.Kind == "sensitive_tool_output_detected" {
			found := false
			for _, unknown := range signal.Unknowns {
				found = found || unknown == "inspection_incomplete"
			}
			if !found {
				t.Fatal("partial detection presented without completeness limitation")
			}
		}
	}
}

func TestHookBenignAssignmentCandidatesCannotHideLaterCredential(t *testing.T) {
	text := strings.Repeat("ORDINARY_CONFIG="+hookFakeValue+"\n", maxHookRuleCount+8) + "API_KEY=" + hookFakeValue
	r := inspectHookFixture(t, "zcode", "PostToolUse", "Bash", map[string]any{}, text)
	counts := hookSignalCounts(t, r, "sensitive_tool_output_detected")
	if counts["credential_assignment"] != 1 || len(counts) != 1 {
		t.Fatalf("benign assignment prefix hid the credential: %v", counts)
	}
}

func TestHookPlaceholderCandidatesCannotHideLaterProviderToken(t *testing.T) {
	placeholder := "ghp_" + strings.Repeat("a", 36)
	text := strings.Repeat(placeholder+"\n", maxHookRuleCount+8) + hookFakeToken
	r := inspectHookFixture(t, "claude-code", "PostToolUse", "Bash", map[string]any{}, text)
	counts := hookSignalCounts(t, r, "sensitive_tool_output_detected")
	if counts["provider_token"] != 1 || len(counts) != 1 {
		t.Fatalf("placeholder prefix hid the provider token: %v", counts)
	}
}

func TestHookNearLimitCandidateScanReachesFinalCredential(t *testing.T) {
	line := "ORDINARY_CONFIG=" + hookFakeValue + "\n"
	// Newlines expand by one byte in JSON; leave room for the envelope and
	// final synthetic credential. This exercises >20,000 benign matches.
	text := strings.Repeat(line, (MaxHookInputBytes-1024)/(len(line)+1)) + "API_KEY=" + hookFakeValue
	data, err := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash",
		"session_id": "synthetic-session", "tool_use_id": "synthetic-tool",
		"tool_response": map[string]any{"stdout": text},
	})
	if err != nil || len(data) > MaxHookInputBytes || len(data) < MaxHookInputBytes-2048 {
		t.Fatalf("invalid near-limit fixture: length=%d error=%v", len(data), err)
	}
	r := InspectHook("zcode", strings.NewReader(string(data)))
	counts := hookSignalCounts(t, r, "sensitive_tool_output_detected")
	if counts["credential_assignment"] != 1 || len(r.Signals) != 1 {
		t.Fatalf("near-limit scan lost tail evidence or exceeded input budget: %#v", r)
	}
}
