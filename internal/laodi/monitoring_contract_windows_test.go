//go:build windows

package laodi

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsMonitoringVerifiedContractAndCurrentHookState(t *testing.T) {
	publicExecutable := os.Getenv("LAODI_TEST_ZCODE_EXE")
	if publicExecutable == "" {
		t.Skip("explicit public client executable required for exact contract verification")
	}
	// Copy only the public executable and pinned hook runtime. All edits and
	// deletion below affect this synthetic installation, never the real client.
	clientRoot := privateStateDir(t)
	client := filepath.Join(clientRoot, "ZCode.exe")
	copyPublicFile := func(source, destination string) {
		t.Helper()
		in, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("copy public client fixture: %v %v", copyErr, closeErr)
		}
	}
	copyPublicFile(publicExecutable, client)
	copyPublicFile(filepath.Join(filepath.Dir(publicExecutable), "resources", "glm", "zcode.cjs"), filepath.Join(clientRoot, "resources", "glm", "zcode.cjs"))
	if _, err := DetectWindowsHookContract("zcode", client); err != nil {
		t.Fatalf("public fixture does not match the reviewed contract: %v", err)
	}
	home := privateStateDir(t)
	state := filepath.Join(home, "state")
	h := &windowsDistributionHarness{}
	plan := h.plan(t, windowsNativePayloadFixture(t, "v0.4.1-monitoring-contract"), state)
	plan.options.ClientExecutable = client
	if err := planWindowsDistributionHooks(&plan); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".zcode", "cli", "config.json")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := readWindowsIntegration(state)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(t *testing.T) ClientMonitoringSummary {
		t.Helper()
		s := AgentSummary{}
		AddMonitoringSummary(&s, state)
		if s.Monitoring == nil || s.Monitoring.Background == "verified_running" {
			t.Fatal("fake service health must not claim a real running monitor")
		}
		for _, c := range s.Monitoring.Clients {
			if c.Adapter == "zcode" {
				return c
			}
		}
		t.Fatal("verified adapter omitted from monitoring summary")
		return ClientMonitoringSummary{}
	}
	current := snapshot(t)
	if current.Contract != "verified" || current.Hooks != "configured" || current.Callback.Status != "not_recorded" {
		t.Fatalf("verified installation misreported: %+v", current)
	}
	// This feeds the real inspector/inbox callback path with a normal synthetic
	// envelope; it does not claim a live client session or authenticated caller.
	in := InspectHook("zcode", strings.NewReader(`{"hook_event_name":"PreToolUse","session_id":"monitoring-contract-fixture","tool_use_id":"normal-fixture","tool_name":"Bash","tool_input":{"command":"git status"}}`))
	if len(in.Signals) != 0 {
		t.Fatal("normal fixture generated a risk signal")
	}
	if err := SubmitHookInspection(state, in); err != nil {
		t.Fatal(err)
	}
	current = snapshot(t)
	if current.Contract != "verified" || current.Hooks != "configured" || current.Callback.Status != "observed_caller_not_authenticated" || current.Callback.LastSample == nil {
		t.Fatalf("normal callback or verified contract lost: %+v", current)
	}
	queue, err := GetHookInboxStatus(state)
	if err != nil || queue.Pending != 0 {
		t.Fatalf("normal callback generated incident queue: %+v %v", queue, err)
	}
	t.Run("disabled_hooks", func(t *testing.T) {
		var disabled map[string]any
		if err := json.Unmarshal(config, &disabled); err != nil {
			t.Fatal(err)
		}
		disabled["hooks"].(map[string]any)["enabled"] = false
		data, err := json.Marshal(disabled)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		c := snapshot(t)
		if c.Contract != "verified" || c.Hooks == "configured" || c.Callback.LastSample == nil {
			t.Fatalf("historical callback hid disabled hooks: %+v", c)
		}
	})
	t.Run("missing_hook_config", func(t *testing.T) {
		if err := os.Remove(configPath); err != nil {
			t.Fatal(err)
		}
		c := snapshot(t)
		if c.Contract != "verified" || c.Hooks == "configured" {
			t.Fatalf("missing hooks remained configured: %+v", c)
		}
	})
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	t.Run("changed_receipt_client_path", func(t *testing.T) {
		changed := integration
		changed.Clients = append([]WindowsHookContract(nil), integration.Clients...)
		changed.Clients[0].Executable = filepath.Join(clientRoot, "missing-client.exe")
		if err := writeWindowsRecord(state, windowsIntegrationName, changed); err != nil {
			t.Fatal(err)
		}
		c := snapshot(t)
		if c.Contract == "verified" || c.Hooks == "configured" || c.Callback.LastSample == nil {
			t.Fatalf("receipt edit inherited verification: %+v", c)
		}
	})
	if err := writeWindowsRecord(state, windowsIntegrationName, integration); err != nil {
		t.Fatal(err)
	}
	t.Run("missing_client_executable", func(t *testing.T) {
		if err := os.Remove(client); err != nil {
			t.Fatal(err)
		}
		c := snapshot(t)
		if c.Contract == "verified" || c.Callback.LastSample == nil {
			t.Fatalf("removed public fixture still verified: %+v", c)
		}
	})
	t.Run("missing_integration_receipt", func(t *testing.T) {
		if err := os.Remove(filepath.Join(state, windowsIntegrationName)); err != nil {
			t.Fatal(err)
		}
		c := snapshot(t)
		if c.Contract == "verified" || c.Hooks == "configured" || c.Callback.LastSample == nil {
			t.Fatalf("callback sample recreated missing ownership: %+v", c)
		}
	})
}
