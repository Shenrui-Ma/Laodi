//go:build windows

package laodi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func windowsNativePayloadFixture(t *testing.T, tag string) string {
	t.Helper()
	source, manifest := windowsPayloadFixture(t, tag)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"laodi.exe", "laodi-host.exe"} {
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Files[name] = serviceHash(data)
	}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, windowsManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestWindowsDistributionVerifiedHooksUpgradeAndRemove(t *testing.T) {
	client := os.Getenv("LAODI_TEST_ZCODE_EXE")
	if client == "" {
		t.Skip("explicit public ZCode executable required for exact identity integration")
	}
	home := privateStateDir(t)
	state := filepath.Join(home, "state")
	a := windowsNativePayloadFixture(t, "v0.4.0-hooks.1")
	b := windowsNativePayloadFixture(t, "v0.4.0-hooks.2")
	h := &windowsDistributionHarness{}
	plan := h.plan(t, a, state)
	plan.options.ClientExecutable = client
	if err := planWindowsDistributionHooks(&plan); err != nil {
		t.Fatal(err)
	}
	result, err := InstallDistribution(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.HookAdapters, []string{"zcode"}) {
		t.Fatal("hook adapter not reported")
	}
	configPath := filepath.Join(home, ".zcode", "cli", "config.json")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// A direct invocation from a version directory chooses the stable host.
	direct, err := PlanHookConfig(HookConfigOptions{Adapter: "zcode", Home: home, StateDir: state, Executable: filepath.Join(state, "versions", "v0.4.0-hooks.1", "laodi.exe"), ClientExecutable: client})
	if err != nil {
		t.Fatal(err)
	}
	if direct.Shell != filepath.Join(state, "laodi-host.exe") {
		t.Fatal("direct installation pinned an immutable version")
	}
	starts := h.starts
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	if starts != h.starts {
		t.Fatal("same version restarted monitor")
	}
	upgrade := h.plan(t, b, state)
	edited := bytes.Replace(original, []byte("laodi-hook-v1:"), []byte("foreign-edited:"), 1)
	if err := os.WriteFile(configPath, edited, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := readServiceFile(filepath.Join(state, windowsCurrentName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallDistribution(upgrade); err == nil {
		t.Fatal("upgrade ignored edited hooks")
	}
	after, _ := readServiceFile(filepath.Join(state, windowsCurrentName))
	if !bytes.Equal(before, after) {
		t.Fatal("failed preflight changed version selection")
	}
	if err := os.WriteFile(configPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = InstallDistribution(h.plan(t, b, state))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || len(result.HookAdapters) != 1 {
		t.Fatal("upgrade lost hook integration")
	}
	currentConfig, _ := os.ReadFile(configPath)
	if !bytes.Equal(original, currentConfig) {
		t.Fatal("upgrade changed stable hook command")
	}
	if _, err := UninstallDistribution(h.plan(t, b, state)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(configPath)
	if !jsonEqual(data, []byte(`{}`)) {
		t.Fatal("removal retained owned hooks")
	}
	if _, err := os.Stat(filepath.Join(state, windowsIntegrationName)); !os.IsNotExist(err) {
		t.Fatal("integration receipt remained")
	}
	// Reinstall after removal explicitly chooses the same client; old runtime
	// versions and historical state remain owned and available.
	reinstall := h.plan(t, b, state)
	reinstall.options.ClientExecutable = client
	if err := planWindowsDistributionHooks(&reinstall); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallDistribution(reinstall); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallDistribution(h.plan(t, b, state)); err != nil {
		t.Fatal(err)
	}
}
