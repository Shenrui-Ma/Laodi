//go:build windows

package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

const windowsIntegrationName = "windows-integration.json"

type windowsIntegration struct {
	Schema  int                   `json:"schema"`
	Home    string                `json:"home"`
	Clients []WindowsHookContract `json:"clients"`
}

func readWindowsIntegration(state string) (windowsIntegration, error) {
	var integration windowsIntegration
	data, err := readServiceFile(filepath.Join(state, windowsIntegrationName))
	if err != nil {
		return integration, err
	}
	if err := decodeWindowsRecord(data, &integration); err != nil || integration.Schema != 1 || len(integration.Clients) > 2 || len(integration.Clients) == 0 {
		return integration, errors.New("invalid Windows integration receipt")
	}
	if err := validWindowsHookLiteralPath(integration.Home); err != nil {
		return integration, err
	}
	seen := map[string]bool{}
	for _, client := range integration.Clients {
		if (client.Adapter != "zcode" && client.Adapter != "claude-code") || seen[client.Adapter] {
			return integration, errors.New("invalid Windows adapter receipt")
		}
		seen[client.Adapter] = true
	}
	return integration, nil
}

func planWindowsDistributionHooks(p *DistributionPlan) error {
	existing, err := readWindowsIntegration(p.StateDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if p.options.Home != existing.Home {
			return errors.New("installed hook home differs; retain the original home for upgrade/removal")
		}
		p.WindowsClients = existing.Clients
		for _, client := range existing.Clients {
			p.Adapters = append(p.Adapters, client.Adapter)
		}
		if err := preflightWindowsDistributionHooks(*p); err != nil {
			return err
		}
	}
	if p.options.Remove {
		return nil
	}
	var candidates []ClientCandidate
	if p.options.ClientExecutable != "" {
		for _, adapter := range []string{"zcode", "claude-code"} {
			if contract, e := DetectWindowsHookContract(adapter, p.options.ClientExecutable); e == nil {
				candidates = append(candidates, ClientCandidate{Adapter: adapter, Executable: contract.Executable})
			}
		}
		if len(candidates) != 1 {
			return ErrWindowsHookContractUnverified
		}
	} else if p.options.AppCandidates != nil {
		for _, path := range p.options.AppCandidates {
			for _, adapter := range []string{"zcode", "claude-code"} {
				if contract, e := DetectWindowsHookContract(adapter, path); e == nil {
					candidates = append(candidates, ClientCandidate{Adapter: adapter, Executable: contract.Executable})
				}
			}
		}
	} else {
		candidates = DiscoverClients().Candidates
	}
	selected := map[string]WindowsHookContract{}
	for _, client := range p.WindowsClients {
		selected[client.Adapter] = client
	}
	for _, candidate := range candidates {
		contract, e := DetectWindowsHookContract(candidate.Adapter, candidate.Executable)
		if e != nil {
			continue
		}
		if old, present := selected[contract.Adapter]; present && !reflect.DeepEqual(old, contract) {
			return errors.New("multiple clients or changed owned client identity; select the existing installation or remove its hooks first")
		}
		selected[contract.Adapter] = contract
	}
	p.Adapters = nil
	p.WindowsClients = nil
	for _, adapter := range []string{"zcode", "claude-code"} {
		if contract, ok := selected[adapter]; ok {
			// Verify the current executable again even for an existing receipt. An
			// upgraded/unknown client never silently inherits the old protocol.
			current, e := DetectWindowsHookContract(adapter, contract.Executable)
			if e != nil || !reflect.DeepEqual(current, contract) {
				return ErrWindowsHookContractUnverified
			}
			p.Adapters = append(p.Adapters, adapter)
			p.WindowsClients = append(p.WindowsClients, contract)
		}
	}
	return nil
}

// Ownership is checked before staging, service changes, and version selection.
// A receipt with missing/edited entries requires explicit repair; never adopt a
// matching foreign command or overwrite someone else's edit on upgrade.
func preflightWindowsDistributionHooks(p DistributionPlan) error {
	for _, client := range p.WindowsClients {
		plan, err := PlanHookConfig(HookConfigOptions{Adapter: client.Adapter, Home: p.options.Home, StateDir: p.StateDir, Remove: true})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(*plan.ClientContract, client) || plan.options.Home != p.options.Home {
			return errors.New("Windows client receipt changed")
		}
		stable := filepath.Join(p.StateDir, "laodi-host.exe")
		if (client.Adapter == "zcode" && plan.Shell != stable) || (client.Adapter == "claude-code" && plan.Command != stable) {
			return errors.New("installed hooks do not use the owned stable launcher")
		}
		data, _, err := readHookConfigFile(plan.ConfigPath)
		if err != nil {
			return err
		}
		config, err := decodeHookConfig(data)
		if err != nil {
			return err
		}
		receiptData, err := readServiceFile(plan.ReceiptPath)
		if err != nil {
			return err
		}
		receipt, err := decodeHookReceipt(receiptData, plan)
		if err != nil {
			return err
		}
		_, events, err := hookConfigMaps(config, plan.Adapter, false)
		if err != nil || !ownedHookEntries(events, receipt) {
			return errors.New("owned Windows hooks are missing or edited; refusing upgrade/removal")
		}
		if !p.options.Remove {
			if err := checkHookEnabled(config, plan.Adapter); err != nil {
				return err
			}
		}
	}
	return nil
}

func preflightInstalledWindowsDistributionHooks(p DistributionPlan) error {
	integration, err := readWindowsIntegration(p.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	p.WindowsClients = integration.Clients
	return preflightWindowsDistributionHooks(p)
}

func applyWindowsDistributionHooks(p DistributionPlan, result *DistributionResult) error {
	if len(p.WindowsClients) == 0 {
		result.Warnings = append(result.Warnings, "no_verified_windows_client_hooks_not_installed")
		return nil
	}
	integration, err := readWindowsIntegration(p.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		integration = windowsIntegration{Schema: 1, Home: p.options.Home}
	} else if err != nil {
		return err
	}
	for _, client := range p.WindowsClients {
		plan, err := PlanHookConfig(HookConfigOptions{Adapter: client.Adapter, Executable: filepath.Join(p.StateDir, "laodi-host.exe"), ClientExecutable: client.Executable, Home: p.options.Home, StateDir: p.StateDir})
		if err != nil {
			return err
		}
		if err := InstallHookConfig(plan); err != nil {
			return err
		}
		found := false
		for _, old := range integration.Clients {
			if old.Adapter == client.Adapter {
				if !reflect.DeepEqual(old, client) {
					return errors.New("Windows integration identity changed")
				}
				found = true
			}
		}
		if !found {
			integration.Clients = append(integration.Clients, client)
			sort.Slice(integration.Clients, func(i, j int) bool { return integration.Clients[i].Adapter < integration.Clients[j].Adapter })
		}
		if err := writeWindowsRecord(p.StateDir, windowsIntegrationName, integration); err != nil {
			return err
		}
		result.HookAdapters = append(result.HookAdapters, client.Adapter)
	}
	result.Warnings = append(result.Warnings, "new_client_session_required_live_hook_delivery_not_verified")
	return nil
}

func removeWindowsDistributionHooks(p DistributionPlan, result *DistributionResult) error {
	for _, client := range p.WindowsClients {
		plan, err := PlanHookConfig(HookConfigOptions{Adapter: client.Adapter, Home: p.options.Home, StateDir: p.StateDir, Remove: true})
		if err != nil {
			return err
		}
		if err := UninstallHookConfig(plan); err != nil {
			return err
		}
		result.HookAdapters = append(result.HookAdapters, client.Adapter)
		// Update the receipt after each removal so a partial failure can resume
		// without interpreting an already removed adapter as user tampering.
		integration, err := readWindowsIntegration(p.StateDir)
		if err != nil {
			return err
		}
		remaining := integration.Clients[:0]
		for _, old := range integration.Clients {
			if old.Adapter != client.Adapter {
				remaining = append(remaining, old)
			}
		}
		integration.Clients = remaining
		if len(remaining) == 0 {
			data, err := readServiceFile(filepath.Join(p.StateDir, windowsIntegrationName))
			if err != nil {
				return err
			}
			if err := removeServiceFile(filepath.Join(p.StateDir, windowsIntegrationName), serviceHash(data)); err != nil {
				return err
			}
		} else if err := writeWindowsRecord(p.StateDir, windowsIntegrationName, integration); err != nil {
			return err
		}
	}
	return nil
}
