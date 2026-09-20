//go:build windows

package laodi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Backend tests supply reviewed protocol metadata directly, without adding a
// production option that could enable arbitrary unverified client binaries.
func windowsBackendPlan(t *testing.T, adapter string) HookConfigPlan {
	t.Helper()
	home := privateStateDir(t)
	options := HookConfigOptions{Adapter: adapter, Home: home, StateDir: filepath.Join(home, "state"), Executable: filepath.Join(home, "中文 path's & % ! $()", "laodi.exe")}
	contract := &WindowsHookContract{Adapter: adapter, Executable: filepath.Join(home, "missing-client.exe"), Version: "2.1.278", SHA256: claudeWindowsHookSHA256, Executor: "command-argv-async", Source: "https://downloads.claude.ai/claude-code-releases/2.1.278/manifest.json"}
	plan := HookConfigPlan{Adapter: adapter, ConfigPath: filepath.Join(home, ".claude", "settings.json"), ReceiptPath: filepath.Join(options.StateDir, "hook-install-"+adapter+".json"), Command: options.Executable, Arguments: []string{"hook", "--adapter", adapter, "--state-dir", options.StateDir}, ClientContract: contract, Events: []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}, Tools: []string{"Bash", "PowerShell", "Read"}, options: options}
	if adapter == "zcode" {
		plan.ConfigPath = filepath.Join(home, ".zcode", "cli", "config.json")
		contract.Version = "3.14.0.7681"
		contract.SHA256 = zcodeWindowsHookSHA256
		contract.RuntimeSHA256 = zcodeWindowsRuntimeSHA256
		contract.Executor = "command-native-shell-async"
		contract.Source = "https://zcode.z.ai/en/docs/hooks"
		var err error
		plan.Command, plan.Shell, err = windowsZCodeHookCommand(options.Executable, plan.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		plan.Arguments = nil
		plan.Tools = []string{"Bash", "Read"}
	}
	plan, err := finishWindowsHookPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestWindowsOwnedHooksMergeRemoveAfterExecutablesGone(t *testing.T) {
	for _, adapter := range []string{"zcode", "claude-code"} {
		t.Run(adapter, func(t *testing.T) {
			plan := windowsBackendPlan(t, adapter)
			original := `{"private":"SYNTHETIC_ONLY","future":9007199254740993,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"foreign"}]}]}}`
			if adapter == "zcode" {
				original = `{"private":"SYNTHETIC_ONLY","future":9007199254740993,"hooks":{"enabled":true,"events":{"Stop":[{"hooks":[{"type":"command","command":"foreign"}]}]}}}`
			}
			writeHookConfigFixture(t, plan.ConfigPath, original)
			if err := setPrivateTestPermissions(plan.ConfigPath, 0644); err != nil {
				t.Fatal(err)
			}
			if err := InstallHookConfig(plan); err == nil {
				t.Fatal("manufactured plan bypassed contract validation")
			}
			if err := installHookConfig(plan); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(plan.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(first, []byte("9007199254740993")) || !bytes.Contains(first, []byte("SYNTHETIC_ONLY")) {
				t.Fatal("foreign fields changed")
			}
			if err := installHookConfig(plan); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(plan.ConfigPath)
			if !bytes.Equal(first, second) {
				t.Fatal("reinstall duplicated entries")
			}
			// The existing Everyone read ACE survives native replacement. It must
			// remain unacceptable to the separate strict private-state reader.
			root, err := os.OpenRoot(filepath.Dir(plan.ConfigPath))
			if err != nil {
				t.Fatal(err)
			}
			f, privateErr := openExistingStateFile(root, filepath.Base(plan.ConfigPath), os.O_RDONLY)
			if f != nil {
				f.Close()
			}
			root.Close()
			if privateErr == nil {
				t.Fatal("client ACL was silently replaced with private state ACL")
			}
			removeOptions := plan.options
			removeOptions.Remove = true
			removeOptions.Executable = ""
			removeOptions.ClientExecutable = ""
			removal, err := PlanHookConfig(removeOptions)
			if err != nil {
				t.Fatal(err)
			}
			preview, _ := json.Marshal(removal)
			if bytes.Contains(preview, []byte("SYNTHETIC_ONLY")) {
				t.Fatal("removal preview leaked unrelated settings")
			}
			if err := UninstallHookConfig(removal); err != nil {
				t.Fatal(err)
			}
			final, _ := os.ReadFile(plan.ConfigPath)
			if !jsonEqual(final, []byte(original)) {
				t.Fatal("uninstall changed foreign configuration")
			}
			if _, err := os.Stat(plan.ReceiptPath); !os.IsNotExist(err) {
				t.Fatal("owned receipt retained")
			}
		})
	}
}

func TestWindowsHooksRefuseEditedOwnedEntry(t *testing.T) {
	plan := windowsBackendPlan(t, "zcode")
	if err := installHookConfig(plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(plan.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(plan.Command), []byte("edited-foreign-command"), 1)
	if err := os.WriteFile(plan.ConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	options := plan.options
	options.Remove = true
	removal, err := PlanHookConfig(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := UninstallHookConfig(removal); err == nil {
		t.Fatal("edited entry removed")
	}
	after, _ := os.ReadFile(plan.ConfigPath)
	if !bytes.Equal(after, data) {
		t.Fatal("failed removal changed config")
	}
}

func TestWindowsHookConfigRejectsLinksConcurrentEditsAndInterpolation(t *testing.T) {
	plan := windowsBackendPlan(t, "claude-code")
	writeHookConfigFixture(t, plan.ConfigPath, `{"before":true}`)
	if err := replaceHookConfigFile(plan.ConfigPath, []byte(`{}`), []byte(`{"stale":true}`), 0666); err == nil {
		t.Fatal("concurrent edit overwritten")
	}
	target := filepath.Join(plan.options.Home, "other.json")
	if err := os.Link(plan.ConfigPath, target); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readHookConfigFile(plan.ConfigPath); err == nil {
		t.Fatal("hardlink accepted")
	}
	if err := validWindowsHookLiteralPath(filepath.Join(plan.options.Home, "${CLAUDE_PROJECT_DIR}")); err == nil {
		t.Fatal("client interpolation accepted")
	}
	command, shell, err := windowsZCodeHookCommand(plan.options.Executable, []string{"hook", "--adapter", "zcode", "--state-dir", plan.options.StateDir})
	if err != nil || shell != plan.options.Executable || !strings.HasPrefix(command, "laodi-hook-v1:") || strings.ContainsAny(command, "% !$&'\";") {
		t.Fatalf("bridge data is not shell independent: %v", err)
	}
}

// Explicit opt-in fixture is a public installed/downloaded program, never a
// private config or session. Normal CI still exercises the native merge backend.
func TestWindowsVerifiedClientPlan(t *testing.T) {
	client := os.Getenv("LAODI_TEST_ZCODE_EXE")
	if client == "" {
		t.Skip("set explicit public ZCode executable to verify exact reviewed bytes")
	}
	self, _ := os.Executable()
	home := privateStateDir(t)
	plan, err := PlanHookConfig(HookConfigOptions{Adapter: "zcode", Executable: self, ClientExecutable: client, Home: home, StateDir: filepath.Join(home, "state")})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ClientContract.Executor != "command-native-shell-async" || plan.Shell != self {
		t.Fatal("wrong native protocol")
	}
	if err := InstallHookConfig(plan); err != nil {
		t.Fatal(err)
	}
	options := plan.options
	options.Remove = true
	options.ClientExecutable = filepath.Join(home, "gone.exe")
	removal, err := PlanHookConfig(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := UninstallHookConfig(removal); err != nil {
		t.Fatal(err)
	}
}
