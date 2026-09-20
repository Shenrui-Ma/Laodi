//go:build windows

package laodi

// Opt-in acceptance against an exact public client executable. The model API is
// a deterministic loopback server; no credentials or private profile are used.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type claudeLabProbe struct {
	Event       string         `json:"event"`
	Tool        string         `json:"tool"`
	ToolUseID   string         `json:"tool_use_id"`
	UTF8        bool           `json:"utf8"`
	UnicodePath bool           `json:"unicode_path"`
	Bytes       int            `json:"bytes"`
	Started     time.Time      `json:"started"`
	Completed   time.Time      `json:"completed"`
	Inspection  HookInspection `json:"inspection"`
}

func init() {
	if len(os.Args) != 3 || os.Args[1] != "--claude-hook-probe" || os.Getenv("LAODI_CLAUDE_LAB_PROBE") != "1" {
		return
	}
	p := claudeLabProbe{Started: time.Now().UTC()}
	data, _ := io.ReadAll(io.LimitReader(os.Stdin, MaxHookInputBytes+1))
	p.Bytes = len(data)
	p.UTF8 = utf8.Valid(data)
	p.Inspection = InspectHook("claude-code", bytes.NewReader(data))
	var body struct {
		Event     string         `json:"hook_event_name"`
		Tool      string         `json:"tool_name"`
		ToolUseID string         `json:"tool_use_id"`
		Input     map[string]any `json:"tool_input"`
	}
	_ = json.Unmarshal(data, &body)
	p.Event = body.Event
	p.Tool = body.Tool
	p.ToolUseID = body.ToolUseID
	for _, value := range body.Input {
		if s, ok := value.(string); ok && strings.Contains(s, "中文") {
			p.UnicodePath = true
		}
	}
	time.Sleep(900 * time.Millisecond)
	p.Completed = time.Now().UTC()
	b, _ := json.Marshal(p)
	_ = os.WriteFile(filepath.Join(os.Args[2], fmt.Sprintf("probe-%d-%d.json", os.Getpid(), p.Started.UnixNano())), b, 0600)
	// Deliberately fail after a delay: an async observer must not block the
	// tool result, inject output, or turn this into a model/session failure.
	os.Exit(7)
}

func claudeLabEnv(root, api, proxy string) []string {
	windows := os.Getenv("SystemRoot")
	values := map[string]string{
		"SystemRoot": windows, "WINDIR": windows, "ComSpec": filepath.Join(windows, "System32", "cmd.exe"),
		"PATH": `C:\Program Files\Git\cmd;` + filepath.Join(windows, "System32") + ";" + filepath.Join(windows, "System32", "WindowsPowerShell", "v1.0"), "PATHEXT": ".COM;.EXE;.BAT;.CMD",
		"HOME": root, "USERPROFILE": root, "APPDATA": filepath.Join(root, "roaming"), "LOCALAPPDATA": filepath.Join(root, "local"), "TEMP": filepath.Join(root, "temp"), "TMP": filepath.Join(root, "temp"), "CLAUDE_CONFIG_DIR": filepath.Join(root, ".claude"),
		"ANTHROPIC_BASE_URL": api, "ANTHROPIC_API_KEY": "invalid-synthetic-laodi-loopback-key", "ANTHROPIC_MODEL": "claude-sonnet-4-6", "ANTHROPIC_SMALL_FAST_MODEL": "claude-sonnet-4-6",
		"HTTP_PROXY": proxy, "HTTPS_PROXY": proxy, "ALL_PROXY": proxy, "NO_PROXY": "127.0.0.1,localhost,::1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL": "1", "DISABLE_UPDATES": "1", "DISABLE_AUTOUPDATER": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1", "DO_NOT_TRACK": "1",
		"CLAUDE_CODE_SKIP_PROMPT_HISTORY": "1", "CLAUDE_CODE_DISABLE_TERMINAL_TITLE": "1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1", "CLAUDE_CODE_DISABLE_FILE_CHECKPOINTING": "1", "CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS": "1", "CLAUDE_CODE_DISABLE_POLICY_SKILLS": "1", "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "1",
		"CLAUDE_CODE_DISABLE_WINDOWS_SHELL_LAUNCHER": "1", "CLAUDE_CODE_USE_POWERSHELL_TOOL": "1", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "0", "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION": "false", "API_TIMEOUT_MS": "15000", "MAX_THINKING_TOKENS": "0",
		"GIT_CONFIG_GLOBAL": filepath.Join(root, "empty-gitconfig"), "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0", "LAODI_CLAUDE_LAB_PROBE": "1", "GORACE": "atexit_sleep_ms=0",
	}
	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	return env
}

func claudeLabRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(privateStateDir(t), "中文 path's & % ! $()")
	for _, name := range []string{"temp", "local", "roaming", ".claude", "project", "probes"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func claudeLabCommand(ctx context.Context, client, root, api, proxy string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, client, args...)
	cmd.Env = claudeLabEnv(root, api, proxy)
	cmd.Dir = filepath.Join(root, "project")
	return cmd
}

func TestClaudeWindowsPublicVersion(t *testing.T) {
	client := os.Getenv("LAODI_TEST_CLAUDE_EXE")
	if client == "" {
		t.Skip("explicit public Claude executable required")
	}
	if _, err := DetectWindowsHookContract("claude-code", client); err != nil {
		t.Fatal(err)
	}
	root := claudeLabRoot(t)
	for _, arg := range []string{"--version", "--help"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := claudeLabCommand(ctx, client, root, "http://127.0.0.1:1", "http://127.0.0.1:1", arg)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("public %s failed: %v (%d output bytes)", arg, err, len(out))
		}
		if arg == "--version" && !bytes.Contains(out, []byte("2.1.278")) {
			t.Fatal("public client version mismatch")
		}
		if arg == "--help" {
			for _, flag := range []string{"--restricted", "--settings", "--tools", "--no-session-persistence", "--permission-mode"} {
				if !bytes.Contains(out, []byte(flag)) {
					t.Fatalf("required public flag absent: %s", flag)
				}
			}
		}
	}
	t.Log("exact public client version and isolation flags verified; no model request started")
}

type claudeLabAPI struct {
	mu         sync.Mutex
	Project    string
	Requests   int
	Tools      map[string]bool
	Results    map[string]time.Time
	Denied     int
	Errors     []string
	ToolErrors map[string]string
}

func (lab *claudeLabAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lab.mu.Lock()
	defer lab.mu.Unlock()
	if r.Method != "POST" || r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "invalid-synthetic-laodi-loopback-key" {
		lab.Denied++
		http.Error(w, "loopback harness rejects unsupported request", 403)
		return
	}
	lab.Requests++
	if lab.Requests > 12 {
		lab.Errors = append(lab.Errors, "request_limit")
		http.Error(w, "bounded harness request limit", 400)
		return
	}
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
		lab.Errors = append(lab.Errors, "request_invalid")
		http.Error(w, "invalid", 400)
		return
	}
	for _, tool := range body.Tools {
		lab.Tools[tool.Name] = true
	}
	for _, message := range body.Messages {
		var blocks []struct {
			Type    string          `json:"type"`
			ID      string          `json:"tool_use_id"`
			Error   bool            `json:"is_error"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(message.Content, &blocks) == nil {
			for _, block := range blocks {
				if block.Type == "tool_result" {
					if block.Error && block.ID == "lab_powershell" {
						text := strings.ReplaceAll(string(block.Content), filepath.Dir(lab.Project), "[LAB]")
						if len(text) > 600 {
							text = text[:600]
						}
						lab.ToolErrors[block.ID] = text
					}
					if _, ok := lab.Results[block.ID]; !ok {
						lab.Results[block.ID] = time.Now().UTC()
					}
				}
			}
		}
	}
	tool, id := "", ""
	var input any
	if len(body.Tools) > 0 {
		switch {
		case lab.Results["lab_read"].IsZero():
			tool = "Read"
			id = "lab_read"
			input = map[string]any{"file_path": filepath.Join(lab.Project, "中文.env")}
		case lab.Results["lab_powershell"].IsZero():
			tool = "PowerShell"
			id = "lab_powershell"
			input = map[string]any{"command": "Get-Content -LiteralPath '中文.env'"}
		case lab.Results["lab_missing"].IsZero():
			tool = "Read"
			id = "lab_missing"
			input = map[string]any{"file_path": filepath.Join(lab.Project, "missing.env")}
		case lab.Results["lab_normal"].IsZero():
			tool = "Read"
			id = "lab_normal"
			input = map[string]any{"file_path": filepath.Join(lab.Project, "README.txt")}
		}
	}
	if tool != "" && !lab.Tools[tool] {
		lab.Errors = append(lab.Errors, "required_tool_not_available:"+tool)
		http.Error(w, "tool unavailable", 400)
		return
	}
	var block map[string]any
	reason := "end_turn"
	if tool != "" {
		block = map[string]any{"type": "tool_use", "id": id, "name": tool, "input": input}
		reason = "tool_use"
	} else {
		time.Sleep(2 * time.Second)
		block = map[string]any{"type": "text", "text": "LOCAL_SYNTHETIC_HARNESS_COMPLETE"}
	}
	message := map[string]any{"id": fmt.Sprintf("msg_lab_%d", lab.Requests), "type": "message", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{block}, "stop_reason": reason, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20}}
	if !body.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	send := func(event string, value any) {
		data, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	message["content"] = []any{}
	message["stop_reason"] = nil
	send("message_start", map[string]any{"type": "message_start", "message": message})
	if tool != "" {
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": tool, "input": map[string]any{}}})
		raw, _ := json.Marshal(input)
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(raw)}})
	} else {
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "LOCAL_SYNTHETIC_HARNESS_COMPLETE"}})
	}
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 20}})
	send("message_stop", map[string]any{"type": "message_stop"})
}

func TestClaudeWindowsLoopbackCallbacks(t *testing.T) {
	client, hook := os.Getenv("LAODI_TEST_CLAUDE_EXE"), os.Getenv("LAODI_TEST_HOOK_EXE")
	if client == "" || hook == "" || os.Getenv("LAODI_TEST_CLAUDE_MOCK") != "1" {
		t.Skip("requires explicit public client/hook executables and loopback opt-in")
	}
	if _, err := DetectWindowsHookContract("claude-code", client); err != nil {
		t.Fatal(err)
	}
	root := claudeLabRoot(t)
	project := filepath.Join(root, "project")
	state := filepath.Join(root, "state")
	for name, data := range map[string]string{"中文.env": "# 无效的本地测试内容\nOPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\n", "README.txt": "Ordinary synthetic text with no credential. 中文。\n"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Copy both native targets to the same special-character directory. This
	// proves the actual host's argv form rather than testing a shell quote.
	hookBytes, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	hookCopy := filepath.Join(root, "laodi observer.exe")
	if err := os.WriteFile(hookCopy, hookBytes, 0600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probeBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(root, "async failing probe.exe")
	if err := os.WriteFile(probe, probeBytes, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanHookConfig(HookConfigOptions{Adapter: "claude-code", Executable: hookCopy, ClientExecutable: client, Home: root, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallHookConfig(plan); err != nil {
		t.Fatal(err)
	}
	settings, _, err := readHookConfigFile(plan.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := decodeHookConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	hooks := config["hooks"].(map[string]any)
	for _, event := range plan.Events {
		hooks[event] = append(hooks[event].([]any), map[string]any{"matcher": "Read|PowerShell", "hooks": []any{map[string]any{"type": "command", "command": probe, "args": []string{"--claude-hook-probe", filepath.Join(root, "probes")}, "async": true}}})
	}
	settings, err = marshalHookConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.ConfigPath, settings, 0600); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(root, "empty-mcp.json")
	if err := os.WriteFile(mcp, []byte(`{"mcpServers":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	lab := &claudeLabAPI{Project: project, Tools: map[string]bool{}, Results: map[string]time.Time{}, ToolErrors: map[string]string{}}
	server := httptest.NewServer(lab)
	defer server.Close()
	var proxyMu sync.Mutex
	proxyRequests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyMu.Lock()
		proxyRequests++
		proxyMu.Unlock()
		http.Error(w, "external destinations are not available in the loopback harness", 403)
	}))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	cmd := claudeLabCommand(ctx, client, root, server.URL, proxy.URL, "--restricted", "--settings", plan.ConfigPath, "--setting-sources", "", "--strict-mcp-config", "--mcp-config", mcp, "--tools", "Read,PowerShell", "--allowedTools", "Read,PowerShell(Get-Content -LiteralPath *)", "--permission-mode", "dontAsk", "--permission-prompts", "none", "--disable-slash-commands", "--no-session-persistence", "--output-format", "json", "--model", "claude-sonnet-4-6", "--max-turns", "8", "--system-prompt", "Local deterministic synthetic tool harness. Follow the requested read-only tools.", "-p", "Run the fixed local synthetic read-only acceptance sequence.")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	err = cmd.Run()
	if err != nil {
		diagnostic := strings.ReplaceAll(stderr.String(), root, "[LAB]")
		if len(diagnostic) > 1200 {
			diagnostic = diagnostic[:1200]
		}
		t.Fatalf("isolated client failed: %v, stdout_bytes=%d, stderr=%s", err, stdout.Len(), diagnostic)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("LOCAL_SYNTHETIC_HARNESS_COMPLETE")) {
		t.Fatalf("client did not complete mock sequence; stdout_bytes=%d", stdout.Len())
	}
	lab.mu.Lock()
	requests, denied, labErrors := lab.Requests, lab.Denied, append([]string(nil), lab.Errors...)
	results := lab.Results
	lab.mu.Unlock()
	if len(labErrors) > 0 || denied != 0 || len(results) != 4 {
		t.Fatalf("loopback sequence incomplete: requests=%d results=%d denied=%d errors=%v", requests, len(results), denied, labErrors)
	}
	probeEntries, err := os.ReadDir(filepath.Join(root, "probes"))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	probeSignals := map[string]int{}
	probeUnknowns := map[string]int{}
	asyncBeforeCompletion := false
	unicodeVerified := false
	for _, entry := range probeEntries {
		data, err := os.ReadFile(filepath.Join(root, "probes", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var p claudeLabProbe
		if json.Unmarshal(data, &p) != nil || !p.UTF8 {
			t.Fatal("actual callback was not valid raw UTF-8 JSON")
		}
		counts[p.Event+":"+p.Tool]++
		for _, signal := range p.Inspection.Signals {
			probeSignals[signal.Kind]++
			for _, unknown := range signal.Unknowns {
				probeUnknowns[unknown]++
			}
		}
		for _, unknown := range p.Inspection.Unknowns {
			probeUnknowns[unknown]++
		}
		unicodeVerified = unicodeVerified || p.UnicodePath
		if at := results[p.ToolUseID]; at.After(started) && at.Before(p.Completed) {
			asyncBeforeCompletion = true
		}
	}
	if !unicodeVerified || !asyncBeforeCompletion || counts["PostToolUse:Read"] != 2 || counts["PostToolUse:PowerShell"] != 1 || counts["PostToolUseFailure:Read"] != 1 || counts["PreToolUse:Read"] != 3 || counts["PreToolUse:PowerShell"] != 1 || len(probeEntries) != 8 {
		t.Fatalf("host callback contract incomplete: callbacks=%v unicode=%v async=%v tool_errors=%v", counts, unicodeVerified, asyncBeforeCompletion, lab.ToolErrors)
	}
	batch, err := ReadHookInbox(state, 64)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, finding := range batch.Findings {
		kinds[finding.Kind]++
	}
	if len(batch.Findings) != 2 || kinds["sensitive_tool_output_detected"] != 2 {
		status, _ := GetHookInboxStatus(state)
		t.Fatalf("real native observer findings differ from two successful credential reads; kinds=%v probes=%v unknowns=%v status=%+v diagnostics=%+v", kinds, probeSignals, probeUnknowns, status, batch.Diagnostics)
	}
	proxyMu.Lock()
	proxyCount := proxyRequests
	proxyMu.Unlock()
	summary := map[string]any{"client": "claude-code", "version": "2.1.278", "api": "loopback_mock_only", "requests": requests, "tool_results": len(results), "callback_counts": counts, "finding_counts": kinds, "raw_utf8": true, "special_path_argv": true, "async_continued_before_probe_exit": asyncBeforeCompletion, "failing_async_hook_exit": 7, "client_exit": 0, "external_proxy_requests_rejected": proxyCount, "elapsed_ms": time.Since(started).Milliseconds(), "no_real_credentials_or_model_service": true}
	encoded, _ := json.Marshal(summary)
	t.Log(string(encoded))
	if output := os.Getenv("LAODI_TEST_CLAUDE_SUMMARY"); output != "" {
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
