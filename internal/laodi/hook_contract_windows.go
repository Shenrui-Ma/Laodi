//go:build windows

package laodi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

const claudeWindowsHookSHA256 = "006ea5c8638f67f10a5ae66bb232fd267c9f6af294e3f03f4cfcf1fd3f2cced8"
const zcodeWindowsHookSHA256 = "564db1fd7b7c9cb71deca8178f7ffd865ace870fb0240e02c93a3300499118be"
const zcodeWindowsRuntimeSHA256 = "8f5cfccf2a899b92e57bc2a5760b949c1a928f739652fffc9e6d07c24f11ba05"

// Exact allowlist: never infer Windows support from macOS build numbers or a
// version resource alone. Public release bytes and documented argv semantics
// are pinned together; a changed/unknown binary needs a new contract review.
func DetectWindowsHookContract(adapter, executable string) (WindowsHookContract, error) {
	identity, ok := inspectClientExecutable(adapter, executable)
	if !ok {
		return WindowsHookContract{}, ErrWindowsHookContractUnverified
	}
	if adapter == "zcode" && identity.SHA256 == zcodeWindowsHookSHA256 && identity.FileVersion == "3.14.0.7681" {
		runtimePath := filepath.Join(filepath.Dir(executable), "resources", "glm", "zcode.cjs")
		hash, err := hashWindowsHookRuntime(runtimePath)
		if err != nil || hash != zcodeWindowsRuntimeSHA256 {
			return WindowsHookContract{}, ErrWindowsHookContractUnverified
		}
		return WindowsHookContract{Adapter: adapter, Executable: executable, Version: "3.14.0.7681", SHA256: identity.SHA256, RuntimeSHA256: hash, Executor: "command-native-shell-async", Source: "https://zcode.z.ai/en/docs/hooks"}, nil
	}
	if adapter != "claude-code" || identity.SHA256 != claudeWindowsHookSHA256 || identity.FileVersion != "2.1.278.0" {
		return WindowsHookContract{}, ErrWindowsHookContractUnverified
	}
	return WindowsHookContract{Adapter: adapter, Executable: executable, Version: "2.1.278", SHA256: identity.SHA256, Executor: "command-argv-async", Source: "https://downloads.claude.ai/claude-code-releases/2.1.278/manifest.json"}, nil
}

func hashWindowsHookRuntime(path string) (string, error) {
	if _, err := windowsPrivatePath(path); err != nil {
		return "", err
	}
	if err := windowsNoReparseAncestors(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := windowsCheckFileIdentity(syscall.Handle(f.Fd()), false); err != nil {
		return "", err
	}
	before, err := f.Stat()
	if err != nil || before.Size() > 32<<20 {
		return "", errors.New("unsupported runtime size")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(f, 32<<20+1))
	if err != nil || size != before.Size() {
		return "", errors.New("cannot hash client runtime")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", errors.New("client runtime changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func planWindowsHookConfig(options HookConfigOptions) (HookConfigPlan, error) {
	if options.Adapter != "claude-code" && options.Adapter != "zcode" {
		return HookConfigPlan{}, errors.New("hook adapter must be zcode or claude-code")
	}
	var err error
	if options.Home == "" {
		options.Home, err = os.UserHomeDir()
		if err != nil {
			return HookConfigPlan{}, err
		}
	}
	if options.StateDir == "" {
		options.StateDir, err = DefaultStateDir(options.Home)
		if err != nil {
			return HookConfigPlan{}, err
		}
	}
	for _, path := range []string{options.Home, options.StateDir} {
		if err := validWindowsHookLiteralPath(path); err != nil {
			return HookConfigPlan{}, err
		}
		if err := windowsNoReparseAncestors(path); err != nil {
			return HookConfigPlan{}, err
		}
	}
	if !options.Remove {
		if err := verifyWindowsInstallationStatePath(options.StateDir); err != nil {
			return HookConfigPlan{}, err
		}
	}
	configPath := filepath.Join(options.Home, ".claude", "settings.json")
	if options.Adapter == "zcode" {
		configPath = filepath.Join(options.Home, ".zcode", "cli", "config.json")
	}
	if !options.Remove {
		if err := verifyWindowsInstallationPath(filepath.Dir(configPath)); err != nil {
			return HookConfigPlan{}, err
		}
	}
	if err := checkHookPath(options.Home, configPath); err != nil {
		return HookConfigPlan{}, err
	}
	receiptPath := filepath.Join(options.StateDir, "hook-install-"+options.Adapter+".json")
	if options.Remove {
		return windowsHookRemovalPlan(options, configPath, receiptPath)
	}
	if options.Executable == "" {
		options.Executable, err = os.Executable()
		if err != nil {
			return HookConfigPlan{}, err
		}
	}
	if _, currentErr := readWindowsCurrent(options.StateDir); currentErr == nil {
		options.Executable = filepath.Join(options.StateDir, "laodi-host.exe")
	} else if !errors.Is(currentErr, os.ErrNotExist) {
		return HookConfigPlan{}, currentErr
	}
	if err := validWindowsHookLiteralPath(options.Executable); err != nil {
		return HookConfigPlan{}, err
	}
	if err := verifyWindowsInstallationFile(options.Executable); err != nil {
		return HookConfigPlan{}, err
	}
	if _, ok := inspectClientExecutable("laodi", options.Executable); !ok {
		return HookConfigPlan{}, errors.New("hook executable must be a regular native PE .exe without reparse components")
	}
	if options.ClientExecutable == "" {
		for _, candidate := range DiscoverClients().Candidates {
			if candidate.Adapter != options.Adapter {
				continue
			}
			if _, err := DetectWindowsHookContract(options.Adapter, candidate.Executable); err != nil {
				continue
			}
			if options.ClientExecutable != "" {
				return HookConfigPlan{}, errors.New("multiple verified clients found; select one with --client-exe")
			}
			options.ClientExecutable = candidate.Executable
		}
	}
	contract, err := DetectWindowsHookContract(options.Adapter, options.ClientExecutable)
	if err != nil {
		return HookConfigPlan{}, err
	}
	plan := HookConfigPlan{Adapter: options.Adapter, ConfigPath: configPath, ReceiptPath: receiptPath, Command: options.Executable,
		Arguments: []string{"hook", "--adapter", options.Adapter, "--state-dir", options.StateDir}, ClientContract: &contract,
		Events: []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}, Tools: []string{"Bash", "PowerShell", "Read"}, options: options}
	if options.Adapter == "zcode" {
		plan.Command, plan.Shell, err = windowsZCodeHookCommand(options.Executable, plan.Arguments)
		if err != nil {
			return HookConfigPlan{}, err
		}
		plan.Arguments = nil
		plan.Tools = []string{"Bash", "Read"}
	}
	return finishWindowsHookPlan(plan)
}

// Called after the independent hook lock has created its state parent, before
// reading or changing client settings. A missing path can become virtualized
// only upon creation, after the earlier read-only planning check succeeded.
func verifyHookInstallState(plan HookConfigPlan) error {
	return verifyWindowsInstallationStatePath(plan.options.StateDir)
}

func validWindowsHookLiteralPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") || strings.Contains(path, "${") {
		return errors.New("hook paths must be clean absolute literals without control characters or client variable placeholders")
	}
	_, err := windowsPrivatePath(path)
	return err
}

func finishWindowsHookPlan(plan HookConfigPlan) (HookConfigPlan, error) {
	events := make(map[string]any)
	for _, event := range plan.Events {
		events[event] = []any{hookConfigEntry(plan)}
	}
	var err error
	var hooks any = events
	if plan.Adapter == "zcode" {
		hooks = map[string]any{"enabled": true, "events": events}
	}
	plan.Configuration, err = json.Marshal(map[string]any{"hooks": hooks})
	return plan, err
}

// Uninstall relies on its protected receipt, even if the client or Laodi binary
// was removed. No executable is launched or rediscovered for cleanup.
func windowsHookRemovalPlan(options HookConfigOptions, configPath, receiptPath string) (HookConfigPlan, error) {
	data, err := readServiceFile(receiptPath)
	if err != nil {
		return HookConfigPlan{}, errors.New("no readable owned Windows hook installation exists")
	}
	if _, err := decodeHookConfig(data); err != nil {
		return HookConfigPlan{}, errors.New("invalid hook ownership receipt")
	}
	var receipt hookConfigReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || receipt.Version != 2 || receipt.ClientContract == nil || receipt.Adapter != options.Adapter || receipt.ConfigPath != configPath {
		return HookConfigPlan{}, errors.New("invalid Windows hook ownership receipt")
	}
	contract := receipt.ClientContract
	knownClaude := contract.Adapter == "claude-code" && contract.Version == "2.1.278" && contract.SHA256 == claudeWindowsHookSHA256 && contract.Executor == "command-argv-async"
	knownZCode := contract.Adapter == "zcode" && contract.Version == "3.14.0.7681" && contract.SHA256 == zcodeWindowsHookSHA256 && contract.RuntimeSHA256 == zcodeWindowsRuntimeSHA256 && contract.Executor == "command-native-shell-async"
	if contract.Adapter != options.Adapter || (!knownClaude && !knownZCode) {
		return HookConfigPlan{}, errors.New("unknown owned Windows hook contract")
	}
	var entry struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Async   bool     `json:"async"`
			Shell   string   `json:"shell,omitempty"`
		} `json:"hooks"`
	}
	decoder = json.NewDecoder(bytes.NewReader(receipt.Entry))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entry); err != nil || len(entry.Hooks) != 1 {
		return HookConfigPlan{}, errors.New("invalid owned Windows hook entry")
	}
	hook := entry.Hooks[0]
	if knownClaude {
		if err := validWindowsHookLiteralPath(hook.Command); err != nil {
			return HookConfigPlan{}, err
		}
	}
	wantArgs := []string{"hook", "--adapter", options.Adapter, "--state-dir", options.StateDir}
	if hook.Type != "command" || !hook.Async || (knownClaude && (entry.Matcher != "Bash|PowerShell|Read" || !reflect.DeepEqual(hook.Args, wantArgs) || hook.Shell != "")) || (knownZCode && (entry.Matcher != "Bash|Read" || hook.Args != nil || hook.Command == "" || hook.Shell == "")) {
		return HookConfigPlan{}, errors.New("owned Windows hook entry changed")
	}
	plan := HookConfigPlan{Adapter: options.Adapter, ConfigPath: configPath, ReceiptPath: receiptPath, Command: hook.Command, Arguments: wantArgs, ClientContract: contract,
		Events: []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}, Tools: []string{"Bash", "PowerShell", "Read"}, options: options}
	if knownZCode {
		if err := validWindowsHookLiteralPath(hook.Shell); err != nil {
			return HookConfigPlan{}, err
		}
		wantCommand, _, err := windowsZCodeHookCommand(hook.Shell, wantArgs)
		if err != nil || hook.Command != wantCommand {
			return HookConfigPlan{}, errors.New("owned native hook bridge data changed")
		}
		plan.Arguments = nil
		plan.Shell = hook.Shell
		plan.Tools = []string{"Bash", "Read"}
	}
	if _, err := decodeHookReceipt(data, plan); err != nil {
		return HookConfigPlan{}, err
	}
	return finishWindowsHookPlan(plan)
}

// ZCode's reviewed command executor delegates to Node spawn with an explicit
// shell executable. Node passes ['-c', command] to non-cmd shells on Windows.
// Laodi implements only this versioned data protocol, never arbitrary commands.
func windowsZCodeHookCommand(executable string, args []string) (string, string, error) {
	if len(args) != 5 || args[0] != "hook" || args[1] != "--adapter" || args[2] != "zcode" || args[3] != "--state-dir" {
		return "", "", errors.New("invalid native hook bridge arguments")
	}
	payload, err := json.Marshal(map[string]string{"adapter": "zcode", "state_dir": args[4]})
	if err != nil {
		return "", "", err
	}
	return "laodi-hook-v1:" + base64.RawURLEncoding.EncodeToString(payload), executable, nil
}
