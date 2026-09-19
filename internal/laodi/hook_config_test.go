package laodi

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func testHookConfigPlan(t *testing.T, adapter string) HookConfigPlan {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows client hook install contract is not verified; POSIX configuration tests do not apply")
	}
	home := t.TempDir()
	executable := filepath.Join(home, "laodi")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanHookConfig(HookConfigOptions{Adapter: adapter, Executable: executable, StateDir: filepath.Join(home, "private-state"), Home: home})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func writeHookConfigFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func readHookConfigFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := decodeHookConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestHookPlanIsReadOnlyAndContainsOnlyOurConfiguration(t *testing.T) {
	for _, adapter := range []string{"zcode", "claude-code"} {
		t.Run(adapter, func(t *testing.T) {
			plan := testHookConfigPlan(t, adapter)
			writeHookConfigFixture(t, plan.ConfigPath, `{"secret-setting":"SYNTHETIC_PRIVATE_VALUE"}`)
			next, err := PlanHookConfig(plan.options)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := json.Marshal(next)
			if err != nil || bytes.Contains(preview, []byte("SYNTHETIC_PRIVATE_VALUE")) {
				t.Fatalf("preview must never include existing settings: %v", err)
			}
			if _, err := os.Stat(plan.options.StateDir); !os.IsNotExist(err) {
				t.Fatal("planning must not create state")
			}
			if !strings.Contains(plan.Command, ">/dev/null 2>/dev/null || true") {
				t.Fatal("hook must suppress protocol output and all nonzero exits")
			}
		})
	}
}

func TestHookInstallPreservesForeignSettingsAndUninstallsOnlyOwnedEntries(t *testing.T) {
	for _, adapter := range []string{"zcode", "claude-code"} {
		t.Run(adapter, func(t *testing.T) {
			plan := testHookConfigPlan(t, adapter)
			original := `{"secret":"SYNTHETIC_PRIVATE_VALUE","future":{"number":9007199254740993},"hooks":{"PreToolUse":[],"Stop":[{"hooks":[{"type":"command","command":"foreign-command"}]}]}}`
			if adapter == "zcode" {
				original = `{"secret":"SYNTHETIC_PRIVATE_VALUE","future":{"number":9007199254740993},"hooks":{"enabled":true,"unknown":42,"events":{"PreToolUse":[],"Stop":[{"hooks":[{"type":"command","command":"foreign-command"}]}]}}}`
			}
			writeHookConfigFixture(t, plan.ConfigPath, original)
			if err := os.Chmod(plan.ConfigPath, 0644); err != nil {
				t.Fatal(err)
			}
			if err := InstallHookConfig(plan); err != nil {
				t.Fatal(err)
			}
			first, _ := os.ReadFile(plan.ConfigPath)
			if err := InstallHookConfig(plan); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(plan.ConfigPath)
			if !bytes.Equal(first, second) {
				t.Fatal("idempotent install rewrote settings")
			}
			receipt, _ := os.ReadFile(plan.ReceiptPath)
			if bytes.Contains(receipt, []byte("SYNTHETIC_PRIVATE_VALUE")) || bytes.Contains(receipt, []byte("foreign-command")) {
				t.Fatal("receipt copied foreign configuration")
			}
			if err := UninstallHookConfig(plan); err != nil {
				t.Fatal(err)
			}
			restored := readHookConfigFixture(t, plan.ConfigPath)
			expected, _ := decodeHookConfig([]byte(original))
			if !reflect.DeepEqual(restored, expected) {
				t.Fatalf("foreign configuration changed: %v", restored)
			}
			info, _ := os.Stat(plan.ConfigPath)
			if info.Mode().Perm() != 0644 {
				t.Fatal("existing configuration permissions changed")
			}
			if _, err := os.Stat(plan.ReceiptPath); !os.IsNotExist(err) {
				t.Fatal("owned receipt remains after uninstall")
			}
		})
	}
}

func TestHookNewFilesArePrivateAndInstallHasNoRuntimeEffects(t *testing.T) {
	plan := testHookConfigPlan(t, "zcode")
	if err := InstallHookConfig(plan); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{plan.ConfigPath, plan.ReceiptPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private file expected: %s %v", path, err)
		}
	}
	for _, path := range []string{filepath.Dir(plan.ConfigPath), plan.options.StateDir} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("private directory expected: %s %v", path, err)
		}
	}
	if err := UninstallHookConfig(plan); err != nil {
		t.Fatal(err)
	}
	if restored := readHookConfigFixture(t, plan.ConfigPath); len(restored) != 0 {
		t.Fatalf("expected empty configuration, got %v", restored)
	}
}

func TestHookRejectsDisabledAndAmbiguousConfigurations(t *testing.T) {
	cases := []struct{ name, adapter, config string }{
		{"disabled-existing", "zcode", `{"hooks":{"enabled":false,"events":{"Stop":[{"hooks":[]}]}}}`},
		{"implicit-disabled-existing", "zcode", `{"hooks":{"events":{"Stop":[{"hooks":[]}]}}}`},
		{"claude-disabled", "claude-code", `{"disableAllHooks":true}`},
		{"duplicate-root", "claude-code", `{"hooks":{},"hooks":{}}`},
		{"duplicate-nested", "claude-code", `{"foreign":{"token":"one","token":"two"}}`},
		{"unknown-hooks-shape", "claude-code", `{"hooks":{"FutureEvent":{}}}`},
		{"null-hooks", "zcode", `{"hooks":null}`},
		{"string-enabled", "zcode", `{"hooks":{"enabled":"true","events":{}}}`},
		{"jsonc", "claude-code", "{/* keep me */}"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan := testHookConfigPlan(t, test.adapter)
			writeHookConfigFixture(t, plan.ConfigPath, test.config)
			if err := InstallHookConfig(plan); err == nil {
				t.Fatal("ambiguous settings accepted")
			}
			data, _ := os.ReadFile(plan.ConfigPath)
			if string(data) != test.config {
				t.Fatal("rejected configuration was changed")
			}
			if _, err := os.Stat(plan.ReceiptPath); !os.IsNotExist(err) {
				t.Fatal("rejected install created a receipt")
			}
		})
	}
}

func TestHookUninstallRefusesEditedOwnedEntry(t *testing.T) {
	plan := testHookConfigPlan(t, "claude-code")
	if err := InstallHookConfig(plan); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(plan.ConfigPath)
	changed := bytes.Replace(data, []byte("Bash|Read"), []byte("Bash"), 1)
	if err := os.WriteFile(plan.ConfigPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallHookConfig(plan); err == nil {
		t.Fatal("edited hook uninstalled")
	}
	if err := InstallHookConfig(plan); err == nil {
		t.Fatal("edited hook overwritten")
	}
	after, _ := os.ReadFile(plan.ConfigPath)
	if !bytes.Equal(changed, after) {
		t.Fatal("edited entry changed")
	}
}

func TestHookShellQuotePreventsExecutionAndVendorInterpolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting; Windows hook executor contract remains unverified")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-exist")
	value := "a'b `touch " + marker + "` $(touch " + marker + ") ${CLAUDE_PROJECT_DIR} ${ZCODE_PLUGIN_ROOT}"
	quoted := hookShellQuote(value)
	if strings.Contains(quoted, "${") {
		t.Fatal("vendor interpolation survived quoting")
	}
	output, err := exec.Command("/bin/sh", "-c", "printf '%s' "+quoted).Output()
	if err != nil || string(output) != value {
		t.Fatalf("quote changed literal value: %q %v", output, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("shell substitution executed")
	}
}

func TestHookPlansRejectUnsafePaths(t *testing.T) {
	plan := testHookConfigPlan(t, "claude-code")
	for _, stateDir := range []string{"relative", plan.options.Home + "/../escape", plan.options.Home + "/newline\npath", plan.options.Home + "/nul\x00path"} {
		options := plan.options
		options.StateDir = stateDir
		if _, err := PlanHookConfig(options); err == nil {
			t.Fatalf("accepted unsafe state path %q", stateDir)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Dir(plan.ConfigPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanHookConfig(plan.options); err == nil {
		t.Fatal("symlink settings directory accepted")
	}
	if err := InstallHookConfig(plan); err == nil {
		t.Fatal("stale plan accepted a new symlink")
	}
}

func TestHookInstallRejectsOversizedSettingsAndForeignIdenticalEntry(t *testing.T) {
	plan := testHookConfigPlan(t, "claude-code")
	writeHookConfigFixture(t, plan.ConfigPath, strings.Repeat(" ", maxHookConfigBytes+1))
	if err := InstallHookConfig(plan); err == nil {
		t.Fatal("oversized configuration accepted")
	}
	writeHookConfigFixture(t, plan.ConfigPath, string(plan.Configuration))
	if err := InstallHookConfig(plan); err == nil {
		t.Fatal("unowned matching hooks adopted")
	}
}

func TestHookRejectsEditedPlanAndConfigFileSymlink(t *testing.T) {
	plan := testHookConfigPlan(t, "claude-code")
	changed := plan
	changed.Command = "touch /tmp/not-authorized"
	if err := InstallHookConfig(changed); err == nil {
		t.Fatal("modified preview accepted")
	}
	if err := os.MkdirAll(filepath.Dir(plan.ConfigPath), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(plan.options.Home, "foreign.json")
	writeHookConfigFixture(t, target, `{"mustRemain":true}`)
	if err := os.Symlink(target, plan.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if err := InstallHookConfig(plan); err == nil {
		t.Fatal("symbolic-link settings accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != `{"mustRemain":true}` {
		t.Fatal("symlink target changed")
	}
}

func TestHookConfigurationReplacementRejectsConcurrentChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeHookConfigFixture(t, path, `{"foreign":"newer"}`)
	if err := replaceHookConfigFile(path, []byte(`{"hooks":{}}`), []byte(`{"foreign":"older"}`), 0600); err == nil {
		t.Fatal("concurrently edited settings overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{"foreign":"newer"}` {
		t.Fatal("foreign changes were lost")
	}
}

func TestHookZCodeRestoresDisabledEmptySettingsWithoutDisablingNewForeignHooks(t *testing.T) {
	for _, addForeign := range []bool{false, true} {
		plan := testHookConfigPlan(t, "zcode")
		writeHookConfigFixture(t, plan.ConfigPath, `{"hooks":{"enabled":false,"events":{}}}`)
		if err := InstallHookConfig(plan); err != nil {
			t.Fatal(err)
		}
		if addForeign {
			config := readHookConfigFixture(t, plan.ConfigPath)
			_, events, _ := hookConfigMaps(config, "zcode", false)
			events["Stop"] = []any{map[string]any{"hooks": []any{}}}
			data, _ := marshalHookConfig(config)
			if err := os.WriteFile(plan.ConfigPath, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := UninstallHookConfig(plan); err != nil {
			t.Fatal(err)
		}
		hooks := readHookConfigFixture(t, plan.ConfigPath)["hooks"].(map[string]any)
		if hooks["enabled"] != addForeign {
			t.Fatal("uninstall changed unrelated hooks activation")
		}
	}
}
