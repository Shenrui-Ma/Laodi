//go:build windows

package laodi

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"
)

type zcodeShellProbe struct {
	ImplicitC bool   `json:"implicit_c"`
	RawSHA256 string `json:"raw_sha256"`
	RawUTF8   bool   `json:"raw_utf8"`
}

func init() {
	output := os.Getenv("LAODI_ZCODE_SHELL_LAB_PROBE")
	if output == "" || len(os.Args) != 3 || os.Args[1] != "-c" || os.Args[2] != "laodi-native-probe-v1" {
		return
	}
	data, _ := io.ReadAll(io.LimitReader(os.Stdin, MaxHookInputBytes+1))
	result, _ := json.Marshal(zcodeShellProbe{ImplicitC: true, RawSHA256: serviceHash(data), RawUTF8: utf8.Valid(data)})
	// An actual native child exits nonzero after the caller has continued.
	time.Sleep(700 * time.Millisecond)
	_ = os.WriteFile(output, result, 0600)
	os.Exit(7)
}

// Opt-in reproduction of the reviewed ZCode executionPort's Node spawn shape.
// This is not an authenticated ZCode session or an invocation of its model.
func TestWindowsZCodeNodeShellStableGUIBridge(t *testing.T) {
	node, hook := os.Getenv("LAODI_TEST_NODE_EXE"), os.Getenv("LAODI_TEST_HOOK_EXE")
	if node == "" || hook == "" {
		t.Skip("requires explicit Node and a native GUI-subsystem Laodi executable")
	}
	parsed, err := pe.Open(hook)
	if err != nil {
		t.Fatal(err)
	}
	optional, ok := parsed.OptionalHeader.(*pe.OptionalHeader64)
	gui := ok && optional.Subsystem == 2
	_ = parsed.Close()
	if !gui {
		t.Fatal("bridge acceptance requires a real x64 GUI-subsystem PE")
	}
	root := filepath.Join(privateStateDir(t), "中文 path's & % ! $()")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	source, manifest := windowsPayloadFixture(t, "v0.4.0-node-lab")
	data, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"laodi.exe", "laodi-host.exe"} {
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Files[name] = serviceHash(data)
	}
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(source, windowsManifestName), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, source, state)); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probeData, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(root, "delayed failure probe.exe")
	if err := os.WriteFile(probe, probeData, 0600); err != nil {
		t.Fatal(err)
	}
	probeOutput := filepath.Join(root, "probe-result.json")
	input := append([]byte(`{"hook_event_name":"PreToolUse","session_id":"local-native-node","tool_use_id":"local-read","tool_name":"Read","tool_input":{"file_path":"C:\\中文 合成\\.env"}}`), '\r', '\n')
	command, shell, err := windowsZCodeHookCommand(filepath.Join(state, "laodi-host.exe"), []string{"hook", "--adapter", "zcode", "--state-dir", state})
	if err != nil {
		t.Fatal(err)
	}
	// The Node library itself supplies -c. Nothing here quotes shell source or
	// adds -c manually; this is the reviewed executionPort's exact spawn API.
	script := `const {spawn}=require('node:child_process');
const raw=Buffer.from(process.env.LAB_STDIN_BASE64,'base64');
let continued=false;
function run(command,shell){return new Promise((resolve,reject)=>{
 const child=spawn(command,[],{cwd:process.cwd(),env:process.env,shell,stdio:['pipe','pipe','pipe'],windowsHide:true,detached:false});
 let stdout=0,stderr=0; child.stdout.on('data',b=>stdout+=b.length);child.stderr.on('data',b=>stderr+=b.length);
 child.on('error',reject);child.on('close',(code,signal)=>resolve({code,signal,stdout,stderr,continued}));
 child.stdin.on('error',()=>{});child.stdin.end(raw);
});}
const callbacks=[run(process.env.LAB_COMMAND,process.env.LAB_SHELL),run('laodi-native-probe-v1',process.env.LAB_PROBE)];
continued=true;
Promise.all(callbacks).then(children=>process.stdout.write(JSON.stringify({node:process.version,platform:process.platform,children}))).catch(()=>process.exitCode=1);`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script)
	cmd.Dir = root
	cmd.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WINDIR=" + os.Getenv("SystemRoot"), "TEMP=" + root, "TMP=" + root,
		"LAB_STDIN_BASE64=" + base64.StdEncoding.EncodeToString(input), "LAB_COMMAND=" + command, "LAB_SHELL=" + shell, "LAB_PROBE=" + probe,
		"LAODI_ZCODE_SHELL_LAB_PROBE=" + probeOutput, "GORACE=atexit_sleep_ms=0"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("isolated Node spawn failed: %v, stderr_bytes=%d", err, stderr.Len())
	}
	var result struct {
		Node     string `json:"node"`
		Platform string `json:"platform"`
		Children []struct {
			Code      int    `json:"code"`
			Signal    string `json:"signal"`
			Stdout    int    `json:"stdout"`
			Stderr    int    `json:"stderr"`
			Continued bool   `json:"continued"`
		} `json:"children"`
	}
	if json.Unmarshal(output, &result) != nil || result.Platform != "win32" || len(result.Children) != 2 {
		t.Fatal("Node child process result invalid")
	}
	observer, failure := result.Children[0], result.Children[1]
	if observer.Code != 0 || observer.Stdout != 0 || observer.Stderr != 0 || observer.Signal != "" || failure.Code != 7 || !observer.Continued || !failure.Continued {
		t.Fatalf("native callback contract failed: %+v", result)
	}
	probeResult, err := os.ReadFile(probeOutput)
	if err != nil {
		t.Fatal(err)
	}
	var p zcodeShellProbe
	if json.Unmarshal(probeResult, &p) != nil || !p.ImplicitC || !p.RawUTF8 || p.RawSHA256 != serviceHash(input) {
		t.Fatal("Node did not preserve -c or raw stdin bytes")
	}
	batch, err := ReadHookInbox(state, 32)
	if err != nil || len(batch.Findings) != 1 || batch.Findings[0].Kind != "sensitive_tool_access_requested" {
		t.Fatalf("GUI stable launcher did not route the raw callback: %v %+v", err, batch)
	}
	summary := map[string]any{"scope": "reviewed_zcode_node_spawn_shape", "node": result.Node, "gui_subsystem": true, "stable_version_route": true, "implicit_c": true, "raw_utf8_bytes_equal": true, "special_path": true, "observer_exit": observer.Code, "observer_stdout_bytes": observer.Stdout, "observer_stderr_bytes": observer.Stderr, "delayed_probe_exit": failure.Code, "caller_continued": true, "findings": 1, "real_zcode_session": false}
	encoded, _ = json.Marshal(summary)
	t.Log(string(encoded))
	if output := os.Getenv("LAODI_TEST_ZCODE_SHELL_SUMMARY"); output != "" {
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
