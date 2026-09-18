package laodi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

const maxHookConfigBytes = 2 << 20

type HookConfigOptions struct {
	Adapter, Executable, StateDir, Home string
}

// HookConfigPlan contains only Laodi's proposed configuration, never existing
// settings. Creating a plan neither reads settings contents nor writes files.
type HookConfigPlan struct {
	Adapter       string          `json:"adapter"`
	ConfigPath    string          `json:"config_path"`
	ReceiptPath   string          `json:"receipt_path"`
	Command       string          `json:"command"`
	Configuration json.RawMessage `json:"configuration"`
	Events        []string        `json:"events"`
	Tools         []string        `json:"tools"`
	options       HookConfigOptions
}

type hookConfigReceipt struct {
	Version         int             `json:"version"`
	Adapter         string          `json:"adapter"`
	ConfigPath      string          `json:"config_path"`
	Entry           json.RawMessage `json:"entry"`
	Events          []string        `json:"events"`
	PreviousEnabled string          `json:"previous_enabled,omitempty"`
	CreatedHooks    bool            `json:"created_hooks"`
	CreatedEvents   bool            `json:"created_events"`
	CreatedNames    []string        `json:"created_event_names"`
}

func PlanHookConfig(options HookConfigOptions) (HookConfigPlan, error) {
	if runtime.GOOS == "windows" {
		return HookConfigPlan{}, errors.New("hook configuration currently requires a POSIX shell")
	}
	if options.Adapter != "zcode" && options.Adapter != "claude-code" {
		return HookConfigPlan{}, errors.New("hook adapter must be zcode or claude-code")
	}
	var err error
	if options.Home == "" {
		options.Home, err = os.UserHomeDir()
		if err != nil {
			return HookConfigPlan{}, errors.New("cannot resolve hook home")
		}
	}
	if options.Executable == "" {
		options.Executable, err = os.Executable()
		if err != nil {
			return HookConfigPlan{}, errors.New("cannot resolve hook executable")
		}
	}
	if options.StateDir == "" {
		options.StateDir = filepath.Join(options.Home, "Library", "Application Support", "Laodi-skills")
	}
	for _, path := range []string{options.Home, options.Executable, options.StateDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return HookConfigPlan{}, errors.New("hook paths must be clean absolute paths without newlines or NUL")
		}
	}
	info, err := os.Lstat(options.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return HookConfigPlan{}, errors.New("hook executable must be an executable regular file, not a symbolic link")
	}
	info, err = os.Lstat(options.Home)
	if err != nil || !info.IsDir() {
		return HookConfigPlan{}, errors.New("hook home must be an existing directory, not a symbolic link")
	}
	configPath := filepath.Join(options.Home, ".claude", "settings.json")
	if options.Adapter == "zcode" {
		configPath = filepath.Join(options.Home, ".zcode", "cli", "config.json")
	}
	if err := checkHookPath(options.Home, configPath); err != nil {
		return HookConfigPlan{}, err
	}
	if info, err := os.Lstat(options.StateDir); err == nil && !info.IsDir() {
		return HookConfigPlan{}, errors.New("hook state directory must not be a symbolic link or special file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return HookConfigPlan{}, errors.New("cannot inspect hook state directory")
	}
	command := hookShellQuote(options.Executable) + " hook --adapter " + options.Adapter + " --state-dir " + hookShellQuote(options.StateDir) + " >/dev/null 2>/dev/null || true"
	plan := HookConfigPlan{
		Adapter: options.Adapter, ConfigPath: configPath,
		ReceiptPath: filepath.Join(options.StateDir, "hook-install-"+options.Adapter+".json"),
		Command:     command, Events: []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}, Tools: []string{"Bash", "Read"}, options: options,
	}
	events := make(map[string]any)
	for _, event := range plan.Events {
		events[event] = []any{hookConfigEntry(plan)}
	}
	hooks := events
	if plan.Adapter == "zcode" {
		hooks = map[string]any{"enabled": true, "events": events}
	}
	plan.Configuration, err = json.Marshal(map[string]any{"hooks": hooks})
	return plan, err
}

// Adjacent quoted fragments also break vendor ${VARIABLE} interpolation before
// the shell sees the command. Neither shell substitutions nor backticks execute.
func hookShellQuote(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, "'", "'\"'\"'"), "$", "'\"$\"'") + "'"
}

func hookConfigEntry(plan HookConfigPlan) map[string]any {
	return map[string]any{"matcher": "Bash|Read", "hooks": []any{map[string]any{"type": "command", "command": plan.Command, "async": true}}}
}

func validateHookConfigPlan(plan HookConfigPlan) error {
	expected, err := PlanHookConfig(plan.options)
	if err != nil || !reflect.DeepEqual(expected, plan) {
		return errors.New("hook plan changed or its paths are unsafe; generate a fresh plan")
	}
	return nil
}

func lockHookConfig(plan HookConfigPlan) (func(), error) {
	root, err := openStateRoot(plan.options.StateDir, true)
	if err != nil {
		return nil, errors.New("cannot open private hook state directory")
	}
	root.Close()
	return AcquireLock(filepath.Join(plan.options.StateDir, "hook-management"))
}

func InstallHookConfig(plan HookConfigPlan) error {
	if err := validateHookConfigPlan(plan); err != nil {
		return err
	}
	release, err := lockHookConfig(plan)
	if err != nil {
		return err
	}
	defer release()
	data, mode, err := readHookConfigFile(plan.ConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	config, err := decodeHookConfig(data)
	if err != nil {
		return err
	}
	receiptData, receiptErr := readServiceFile(plan.ReceiptPath)
	if receiptErr == nil {
		receipt, err := decodeHookReceipt(receiptData, plan)
		if err != nil {
			return err
		}
		_, events, err := hookConfigMaps(config, plan.Adapter, false)
		if err != nil || !ownedHookEntries(events, receipt) {
			return errors.New("owned hook entries are missing or edited; refusing to replace them")
		}
		entry, _ := json.Marshal(hookConfigEntry(plan))
		if !jsonEqual(entry, receipt.Entry) {
			return errors.New("hooks use different settings; uninstall the owned hooks before changing settings")
		}
		return checkHookEnabled(config, plan.Adapter)
	}
	if !errors.Is(receiptErr, os.ErrNotExist) {
		return errors.New("cannot read hook ownership receipt")
	}
	_, hadHooks := config["hooks"]
	hooks, events, err := hookConfigMaps(config, plan.Adapter, true)
	if err != nil {
		return err
	}
	receipt := hookConfigReceipt{Version: 1, Adapter: plan.Adapter, ConfigPath: plan.ConfigPath, Events: plan.Events, CreatedHooks: !hadHooks}
	if plan.Adapter == "claude-code" {
		if err := checkHookEnabled(config, plan.Adapter); err != nil {
			return err
		}
	} else {
		// Never activate another person's disabled hooks as an installation side effect.
		enabled, present := hooks["enabled"]
		receipt.PreviousEnabled = "absent"
		if present {
			flag, ok := enabled.(bool)
			if !ok {
				return errors.New("ZCode hooks.enabled must be a boolean")
			}
			if flag {
				receipt.PreviousEnabled = "true"
			} else {
				receipt.PreviousEnabled = "false"
			}
		}
		if receipt.PreviousEnabled != "true" && hasHookEntries(events) {
			return errors.New("ZCode has disabled or implicitly disabled existing hooks; enable them explicitly before installing Laodi")
		}
		hooks["enabled"] = true
		// An absent events map was created by hookConfigMaps; distinguish it from
		// an existing empty map using the unmodified settings bytes.
		original, _ := decodeHookConfig(data)
		if oldHooks, ok := original["hooks"].(map[string]any); ok {
			_, existed := oldHooks["events"]
			receipt.CreatedEvents = !existed
		} else {
			receipt.CreatedEvents = true
		}
	}
	entry := hookConfigEntry(plan)
	receipt.Entry, _ = json.Marshal(entry)
	for _, event := range plan.Events {
		if _, exists := events[event]; !exists {
			receipt.CreatedNames = append(receipt.CreatedNames, event)
		}
		entries, _ := events[event].([]any)
		for _, existing := range entries {
			if reflect.DeepEqual(existing, entry) {
				return errors.New("matching hooks already exist without a Laodi ownership receipt; refusing to adopt them")
			}
		}
		events[event] = append(entries, entry)
	}
	next, err := marshalHookConfig(config)
	if err != nil {
		return err
	}
	receiptData, _ = json.Marshal(receipt)
	// Publish ownership first. Interrupted installs remain explicit incomplete
	// states; they never silently adopt or overwrite an unowned hook.
	if err := replaceHookConfigFile(plan.ReceiptPath, receiptData, nil, 0600); err != nil {
		return err
	}
	if err := ensureHookConfigParent(plan); err != nil {
		return errors.Join(err, removeServiceFile(plan.ReceiptPath, serviceHash(receiptData)))
	}
	if err := replaceHookConfigFile(plan.ConfigPath, next, data, mode); err != nil {
		return errors.Join(err, removeServiceFile(plan.ReceiptPath, serviceHash(receiptData)))
	}
	return nil
}

func UninstallHookConfig(plan HookConfigPlan) error {
	if err := validateHookConfigPlan(plan); err != nil {
		return err
	}
	release, err := lockHookConfig(plan)
	if err != nil {
		return err
	}
	defer release()
	receiptData, err := readServiceFile(plan.ReceiptPath)
	if err != nil {
		return errors.New("no readable owned hook installation exists")
	}
	receipt, err := decodeHookReceipt(receiptData, plan)
	if err != nil {
		return err
	}
	data, mode, err := readHookConfigFile(plan.ConfigPath)
	if err != nil {
		return err
	}
	config, err := decodeHookConfig(data)
	if err != nil {
		return err
	}
	hooks, events, err := hookConfigMaps(config, plan.Adapter, false)
	if err != nil || !ownedHookEntries(events, receipt) {
		return errors.New("owned hook entries are missing, duplicated, or edited; refusing to guess which entries to remove")
	}
	for _, event := range receipt.Events {
		entries := events[event].([]any)
		remaining := make([]any, 0, len(entries)-1)
		for _, entry := range entries {
			encoded, _ := json.Marshal(entry)
			if !jsonEqual(encoded, receipt.Entry) {
				remaining = append(remaining, entry)
			}
		}
		if len(remaining) == 0 {
			created := false
			for _, name := range receipt.CreatedNames {
				created = created || name == event
			}
			if created {
				delete(events, event)
			} else {
				events[event] = remaining
			}
		} else {
			events[event] = remaining
		}
	}
	if plan.Adapter == "zcode" {
		// Do not disable foreign hooks added after installation.
		if !hasHookEntries(events) && hooks["enabled"] == true {
			switch receipt.PreviousEnabled {
			case "absent":
				delete(hooks, "enabled")
			case "false":
				hooks["enabled"] = false
			}
		}
		if receipt.CreatedEvents && len(events) == 0 {
			delete(hooks, "events")
		}
	}
	if receipt.CreatedHooks && len(hooks) == 0 {
		delete(config, "hooks")
	}
	next, err := marshalHookConfig(config)
	if err != nil {
		return err
	}
	if err := replaceHookConfigFile(plan.ConfigPath, next, data, mode); err != nil {
		return err
	}
	return removeServiceFile(plan.ReceiptPath, serviceHash(receiptData))
}

func checkHookEnabled(config map[string]any, adapter string) error {
	if adapter == "claude-code" {
		if value, ok := config["disableAllHooks"]; ok && value != false {
			return errors.New("Claude Code hooks are disabled or disableAllHooks has an unsupported value")
		}
		return nil
	}
	hooks, _, err := hookConfigMaps(config, adapter, false)
	if err != nil || hooks["enabled"] != true {
		return errors.New("ZCode hooks are disabled; enable them explicitly")
	}
	return nil
}

func hookConfigMaps(config map[string]any, adapter string, create bool) (map[string]any, map[string]any, error) {
	hooks, ok := config["hooks"].(map[string]any)
	if !ok {
		if _, exists := config["hooks"]; exists || !create {
			return nil, nil, errors.New("hooks must be a JSON object")
		}
		hooks = make(map[string]any)
		config["hooks"] = hooks
	}
	events := hooks
	if adapter == "zcode" {
		events, ok = hooks["events"].(map[string]any)
		if !ok {
			if _, exists := hooks["events"]; exists || !create {
				return nil, nil, errors.New("ZCode hook events must be a JSON object")
			}
			events = make(map[string]any)
			hooks["events"] = events
		}
	}
	for _, value := range events {
		entries, ok := value.([]any)
		if !ok {
			return nil, nil, errors.New("hook event values must be arrays")
		}
		for _, entry := range entries {
			if _, ok := entry.(map[string]any); !ok {
				return nil, nil, errors.New("hook event entries must be objects")
			}
		}
	}
	return hooks, events, nil
}

func hasHookEntries(events map[string]any) bool {
	for _, value := range events {
		if entries, ok := value.([]any); ok && len(entries) != 0 {
			return true
		}
	}
	return false
}

func ownedHookEntries(events map[string]any, receipt hookConfigReceipt) bool {
	for _, event := range receipt.Events {
		entries, ok := events[event].([]any)
		if !ok {
			return false
		}
		count := 0
		for _, entry := range entries {
			data, _ := json.Marshal(entry)
			if jsonEqual(data, receipt.Entry) {
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}

func decodeHookReceipt(data []byte, plan HookConfigPlan) (hookConfigReceipt, error) {
	if _, err := decodeHookConfig(data); err != nil {
		return hookConfigReceipt{}, errors.New("invalid hook ownership receipt")
	}
	var receipt hookConfigReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || receipt.Version != 1 || receipt.Adapter != plan.Adapter || receipt.ConfigPath != plan.ConfigPath || !reflect.DeepEqual(receipt.Events, plan.Events) {
		return hookConfigReceipt{}, errors.New("invalid hook ownership receipt")
	}
	if receipt.PreviousEnabled != "" && receipt.PreviousEnabled != "absent" && receipt.PreviousEnabled != "true" && receipt.PreviousEnabled != "false" {
		return hookConfigReceipt{}, errors.New("invalid hook ownership receipt")
	}
	seen := make(map[string]bool)
	for _, name := range receipt.CreatedNames {
		if (name != "PreToolUse" && name != "PostToolUse" && name != "PostToolUseFailure") || seen[name] {
			return hookConfigReceipt{}, errors.New("invalid hook ownership receipt")
		}
		seen[name] = true
	}
	if _, err := decodeHookConfig(receipt.Entry); err != nil {
		return hookConfigReceipt{}, errors.New("invalid owned hook entry")
	}
	return receipt, nil
}

func jsonEqual(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

// Decode token-by-token to reject duplicate keys anywhere, retaining unknown
// fields and exact JSON numbers rather than silently applying last-key wins.
func decodeHookConfig(data []byte) (map[string]any, error) {
	if data == nil {
		return make(map[string]any), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeHookValue(decoder, 0)
	if err != nil {
		return nil, errors.New("hook settings must be strict JSON without duplicate keys or excessive nesting")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("hook settings contain trailing data")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("hook settings must be a JSON object")
	}
	return object, nil
}

func decodeHookValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("excessive depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if delimiter == '{' {
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return nil, errors.New("invalid object key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate key")
			}
			object[name], err = decodeHookValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
		}
		_, err = decoder.Token()
		return object, err
	}
	if delimiter == '[' {
		array := []any{}
		for decoder.More() {
			value, err := decodeHookValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err = decoder.Token()
		return array, err
	}
	return nil, errors.New("invalid delimiter")
}

func marshalHookConfig(config map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil || len(data)+1 > maxHookConfigBytes {
		return nil, errors.New("hook settings exceed 2 MiB limit")
	}
	return append(data, '\n'), nil
}

func checkHookPath(home, path string) error {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("hook settings path must remain inside the selected home")
	}
	current := home
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return errors.New("hook settings paths must not contain symbolic links or special files")
		}
	}
	return nil
}

func ensureHookConfigParent(plan HookConfigPlan) error {
	if err := checkHookPath(plan.options.Home, plan.ConfigPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plan.ConfigPath), 0700); err != nil {
		return errors.New("cannot create hook settings directory")
	}
	return checkHookPath(plan.options.Home, plan.ConfigPath)
}

func readHookConfigFile(path string) ([]byte, os.FileMode, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, 0600, err
	}
	defer parent.Close()
	name := filepath.Base(path)
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, 0600, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 {
		return nil, 0, errors.New("hook settings must be a regular file without group or world write permission")
	}
	f, err := openExistingStateFile(parent, name, os.O_RDONLY)
	if err != nil {
		return nil, 0, errors.New("cannot safely open hook settings")
	}
	defer f.Close()
	opened, err := f.Stat()
	after, afterErr := parent.Lstat(name)
	if err != nil || afterErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		return nil, 0, errors.New("hook settings changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHookConfigBytes+1))
	if err != nil || len(data) > maxHookConfigBytes {
		return nil, 0, errors.New("hook settings are unreadable or exceed 2 MiB")
	}
	return data, before.Mode().Perm(), nil
}

func replaceHookConfigFile(path string, data, expected []byte, mode os.FileMode) error {
	if len(data) > maxHookConfigBytes {
		return errors.New("hook settings exceed 2 MiB limit")
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errors.New("cannot open hook settings parent")
	}
	defer parent.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("cannot name hook settings temporary file")
	}
	name := ".laodi-hook-" + hex.EncodeToString(random[:]) + ".tmp"
	f, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return errors.New("cannot create hook settings temporary file")
	}
	defer parent.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return errors.New("cannot write hook settings")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errors.New("cannot sync hook settings")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close hook settings")
	}
	current, currentMode, err := readHookConfigFile(path)
	if expected == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("hook settings appeared during installation; refusing to replace them")
		}
		// Publishing a previously absent file must be atomic and no-clobber.
		if err := parent.Link(name, filepath.Base(path)); err != nil {
			return errors.New("cannot publish new hook settings")
		}
	} else {
		if err != nil || !bytes.Equal(expected, current) || currentMode != mode {
			return errors.New("hook settings changed during installation; retry after the other editor finishes")
		}
		if err := parent.Rename(name, filepath.Base(path)); err != nil {
			return errors.New("cannot atomically replace hook settings")
		}
	}
	dir, err := parent.Open(".")
	if err != nil {
		return errors.New("hook settings saved but directory cannot be opened for sync")
	}
	defer dir.Close()
	return dir.Sync()
}
