package laodi

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	stateFileName               = "state.json"
	maxStateBytes               = 4 << 20
	maxSeen                     = 4096
	maxHookSeen                 = 2048
	maxEvents                   = 256
	workspaceSnapshotGeneration = 1
	snapshotCapacityDiagnostic  = "snapshot_dedup_capacity_exceeded"
)

func emptyState() State {
	return State{SchemaVersion: SchemaVersion, Seen: make(map[string]string), HookSeen: make(map[string]string), Events: []Event{}}
}

// LoadState never creates a directory or repairs malformed state. In particular,
// a failed load must not reset deduplication and make old evidence appear new.
func LoadState(dir string) (State, error) {
	root, err := openStateRoot(dir, false)
	if errors.Is(err, os.ErrNotExist) {
		return emptyState(), nil
	}
	if err != nil {
		return State{}, err
	}
	defer root.Close()
	f, err := openStateFile(root, stateFileName, os.O_RDONLY, false)
	if errors.Is(err, os.ErrNotExist) {
		return emptyState(), nil
	}
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	if len(data) > maxStateBytes {
		return State{}, errors.New("state exceeds 4 MiB limit")
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("invalid state JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return State{}, errors.New("invalid state JSON: trailing data")
	}
	if err := validateState(state); err != nil {
		return State{}, err
	}
	if state.Seen == nil {
		state.Seen = make(map[string]string)
	}
	if state.HookSeen == nil {
		state.HookSeen = make(map[string]string)
	}
	return state, nil
}

// SaveState replaces the state only after the complete bounded JSON is flushed.
// Callers that perform read-modify-write must hold AcquireLock across all steps.
func SaveState(dir string, state State) error {
	if err := validateState(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxStateBytes {
		return errors.New("state exceeds 4 MiB limit")
	}
	root, err := openStateRoot(dir, true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkRegularFile(root, stateFileName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("name state temporary file: %w", err)
	}
	name := ".state-" + hex.EncodeToString(suffix[:]) + ".tmp"
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create state temporary file: %w", err)
	}
	defer root.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write state temporary file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync state temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close state temporary file: %w", err)
	}
	// Check again after writing. Rename replaces a symlink instead of following
	// it, but a detected symlink is still a configuration error, not a repair.
	if err := checkRegularFile(root, stateFileName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Rename(name, stateFileName); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	dirFile, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("state saved, open directory for sync: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("state saved, sync directory: %w", err)
	}
	return nil
}

// ApplyReport retains the first observation as a quiet baseline. Subsequent
// changes to an evidence hash create events; repeated unchanged evidence does
// not. Snapshot Seen records outlive the bounded event list and survive restarts.
// Hook cursors use an independent recent window: a replay outside that window
// can create another event, but high-volume hooks never exhaust snapshot Seen.
func ApplyReport(state *State, report Report, rootID string, now time.Time) ([]Event, error) {
	if state == nil {
		return nil, errors.New("state is nil")
	}
	if rootID == "" {
		return nil, errors.New("root identity is empty")
	}
	if report.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported report schema %d", report.SchemaVersion)
	}
	if state.SchemaVersion == 0 && !state.Initialized && state.RootID == "" && len(state.Seen) == 0 && len(state.HookSeen) == 0 && len(state.HookSeenOrder) == 0 && len(state.Events) == 0 {
		// A zero State is a convenient in-memory starting point, never an accepted
		// on-disk schema.
		stateCopy := emptyState()
		return applyReport(state, stateCopy, report, rootID, now)
	}
	if err := validateState(*state); err != nil {
		return nil, err
	}
	return applyReport(state, *state, report, rootID, now)
}

func applyReport(destination *State, current State, report Report, rootID string, now time.Time) ([]Event, error) {
	if current.RootID != "" && current.RootID != rootID {
		return nil, errors.New("state belongs to a different data root; use a separate state directory")
	}
	// Work on a copy so invalid input cannot partially advance a cursor.
	next := current
	next.Seen = make(map[string]string, len(current.Seen))
	for key, hash := range current.Seen {
		next.Seen[key] = hash
	}
	next.HookSeen = make(map[string]string, len(current.HookSeen))
	for key, hash := range current.HookSeen {
		next.HookSeen[key] = hash
	}
	next.HookSeenOrder = append([]string(nil), current.HookSeenOrder...)
	next.Events = append([]Event(nil), current.Events...)
	migrateLegacyHookSeen(&next, current)
	capacityWasReported := hasStoreDiagnostic(current.Diagnostics, snapshotCapacityDiagnostic)
	// The gap remains until there is actual room again, not merely until a
	// particular scanner page happens to contain only previously tracked keys.
	capacityExceeded := capacityWasReported && len(next.Seen) >= maxSeen
	newEvents := []Event{}
	baseline := !current.Initialized
	validWorkspaceScan := canBaselineWorkspaceSnapshots(report)
	workspaceBaseline := current.WorkspaceSnapshotGeneration < workspaceSnapshotGeneration && validWorkspaceScan
	for _, finding := range report.Findings {
		if finding.Key == "" || finding.EvidenceHash == "" || finding.Kind == "" {
			return nil, errors.New("finding requires key, kind, and evidence hash")
		}
		hookFinding := isToolHookKind(finding.Kind)
		protectionFinding := isProtectionHealthFinding(finding)
		seen := next.Seen
		if hookFinding || protectionFinding {
			seen = next.HookSeen
		}
		if seen[finding.Key] == finding.EvidenceHash {
			continue
		}
		_, exists := seen[finding.Key]
		if !hookFinding && !protectionFinding && !exists && len(next.Seen) >= maxSeen {
			capacityExceeded = true
			// Do not claim to track an unseen snapshot without a durable cursor.
			// Existing snapshots and independent tool hooks continue normally.
			continue
		}
		workspaceFinding := isWorkspaceSnapshotKind(finding.Kind)
		findingBaseline := baseline && !hookFinding && !protectionFinding
		upgradeBaseline := current.Initialized && workspaceBaseline && workspaceFinding && !exists
		if hookFinding || protectionFinding {
			rememberHook(&next, finding.Key, finding.EvidenceHash)
		} else {
			next.Seen[finding.Key] = finding.EvidenceHash
		}
		id := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", rootID, finding.Key, finding.EvidenceHash, now.UTC().Format(time.RFC3339Nano), len(next.Events)+next.DroppedEvents)))
		event := Event{
			Source:           finding.Source,
			ID:               hex.EncodeToString(id[:16]),
			Key:              finding.Key,
			Kind:             finding.Kind,
			Counts:           copyCounts(finding.Counts),
			Evidence:         finding.Evidence,
			EvidenceHash:     finding.EvidenceHash,
			Unknowns:         append([]string(nil), finding.Unknowns...),
			ObservedAt:       now.UTC(),
			BaselineExisting: findingBaseline || upgradeBaseline,
			Notification:     "pending",
		}
		if upgradeBaseline {
			// This is a baseline at parser upgrade, not proof that an artifact
			// existed before Laodi was originally installed.
			event.Notification = "suppressed_upgrade_baseline"
			event.Unknowns = appendUnknown(event.Unknowns, "first_observed_after_parser_upgrade", "existence_before_initial_install_unknown")
			if report.Coverage == "degraded" {
				event.Unknowns = appendUnknown(event.Unknowns, "parser_upgrade_scan_incomplete")
			}
		} else if findingBaseline {
			event.Notification = "suppressed_baseline"
		} else {
			if workspaceFinding && !exists && current.WorkspaceBaselineIncomplete {
				// Do not keep suppressing newly discovered objects while a large
				// or damaged directory remains partially scanned. Alert with an
				// explicit unknown time boundary instead.
				event.Unknowns = appendUnknown(event.Unknowns, "first_observed_after_incomplete_parser_upgrade", "existence_at_parser_upgrade_unknown")
			}
			newEvents = append(newEvents, event)
		}
		next.Events = append(next.Events, event)
		if len(next.Events) > maxEvents {
			next.Events = next.Events[1:]
			next.DroppedEvents++
		}
	}
	if capacityExceeded && !capacityWasReported {
		// Diagnostics are the durable latch for this single capacity episode.
		// Neither a full snapshot map nor high-volume hooks can evict it, and
		// loss of the retained event itself does not cause another notification.
		id := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", rootID, snapshotCapacityDiagnostic, now.UTC().Format(time.RFC3339Nano), len(next.Events)+next.DroppedEvents)))
		event := Event{
			Source: "laodi", ID: hex.EncodeToString(id[:16]),
			Key: "monitor:snapshot-dedup-capacity", Kind: "monitor_capacity_degraded",
			Counts:   map[string]int{"snapshot_keys": len(next.Seen)},
			Evidence: "local_snapshot_dedup_store", EvidenceHash: snapshotCapacityDiagnostic,
			Unknowns:   []string{"new_snapshot_evidence_not_tracked", "existing_snapshots_and_hooks_continue", "task_continues"},
			ObservedAt: now.UTC(), Notification: "pending",
		}
		next.Events = append(next.Events, event)
		if len(next.Events) > maxEvents {
			next.Events = next.Events[1:]
			next.DroppedEvents++
		}
		// Give the coverage alert first access to the bounded notification queue.
		newEvents = append([]Event{event}, newEvents...)
	}
	if validWorkspaceScan {
		next.Parser = report.Parser
		next.WorkspaceSnapshotGeneration = workspaceSnapshotGeneration
		if workspaceBaseline {
			next.WorkspaceBaselineIncomplete = current.Initialized && (report.Coverage == "degraded" || capacityExceeded)
		} else if !capacityExceeded && (report.Coverage == "observing" || report.Coverage == "no_evidence_directory") {
			// Findings in this completing scan still carry the earlier uncertainty;
			// only subsequent scans have a complete classification baseline.
			next.WorkspaceBaselineIncomplete = false
		}
	}
	if report.Parser == HookParserID {
		next.Parser = HookParserID
	}
	next.SchemaVersion = SchemaVersion
	next.RootID = rootID
	next.Initialized = true
	next.Coverage = report.Coverage
	next.Diagnostics = append([]Diagnostic(nil), report.Diagnostics...)
	if capacityExceeded {
		next.Coverage = "degraded"
		if !hasStoreDiagnostic(next.Diagnostics, snapshotCapacityDiagnostic) {
			next.Diagnostics = append(next.Diagnostics, Diagnostic{Code: snapshotCapacityDiagnostic})
		}
	}
	next.LastCheckedAt = now.UTC()
	*destination = next
	return newEvents, nil
}

func hasStoreDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

// Order is oldest to newest distinct inserted/changed key. Unchanged duplicate
// replays do not refresh it. One fixed protection-health key may share this
// existing format; pinning it prevents busy hooks from repeating a guard alarm.
// The schema stays readable by older releases during an upgrade rollback.
func rememberHook(state *State, key, hash string) {
	if _, exists := state.HookSeen[key]; exists {
		for i, old := range state.HookSeenOrder {
			if old == key {
				state.HookSeenOrder = append(state.HookSeenOrder[:i], state.HookSeenOrder[i+1:]...)
				break
			}
		}
	} else if len(state.HookSeenOrder) >= maxHookSeen {
		oldest := 0
		if state.HookSeenOrder[oldest] == protectionHealthKey {
			oldest++
		}
		delete(state.HookSeen, state.HookSeenOrder[oldest])
		state.HookSeenOrder = append(state.HookSeenOrder[:oldest], state.HookSeenOrder[oldest+1:]...)
	}
	state.HookSeen[key] = hash
	state.HookSeenOrder = append(state.HookSeenOrder, key)
}

// Unreleased development states stored hooks in Seen. Only their reserved
// "hook:" namespace is migrated; snapshot keys and their capacity are untouched.
// Legacy cursors lack full ordering, so retain recent surviving events first,
// then prefer any already-established new-window order over legacy history.
func migrateLegacyHookSeen(next *State, current State) {
	var keys []string
	for key := range current.Seen {
		if strings.HasPrefix(key, "hook:") {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	for _, key := range keys {
		delete(next.Seen, key)
		rememberHook(next, key, current.Seen[key])
	}
	for _, event := range current.Events {
		if isToolHookKind(event.Kind) && strings.HasPrefix(event.Key, "hook:") && current.Seen[event.Key] == event.EvidenceHash {
			rememberHook(next, event.Key, event.EvidenceHash)
		}
	}
	for _, key := range current.HookSeenOrder {
		rememberHook(next, key, current.HookSeen[key])
	}
}

func isWorkspaceSnapshotKind(kind string) bool {
	switch kind {
	case "workspace_snapshot_manifest", "workspace_snapshot_upload_attempt_recorded", "workspace_snapshot_upload_acceptance_recorded":
		return true
	}
	return false
}

// A sensor failure does not demonstrate that the new classification family was
// inspected. Partial scans count only when they actually contain parsed evidence.
func canBaselineWorkspaceSnapshots(report Report) bool {
	if report.Parser != ParserID {
		return false
	}
	switch report.Coverage {
	case "observing", "no_evidence_directory":
		return true
	case "degraded":
		for _, finding := range report.Findings {
			if isWorkspaceSnapshotKind(finding.Kind) {
				return true
			}
			switch finding.Kind {
			case "sensitive_manifest_match", "upload_attempt_recorded", "upload_acceptance_recorded",
				"global_config_manifest_match", "global_config_upload_attempt_recorded", "global_config_upload_acceptance_recorded":
				return true
			}
		}
	}
	return false
}

func appendUnknown(existing []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, old := range existing {
			if old == value {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, value)
		}
	}
	return existing
}

func copyCounts(source map[string]int) map[string]int {
	if source == nil {
		return nil
	}
	result := make(map[string]int, len(source))
	for key, count := range source {
		result[key] = count
	}
	return result
}

func validateState(state State) error {
	if state.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported state schema %d", state.SchemaVersion)
	}
	if state.Initialized && state.RootID == "" {
		return errors.New("initialized state has no root identity")
	}
	if !state.Initialized && (len(state.Seen) != 0 || len(state.HookSeen) != 0 || len(state.HookSeenOrder) != 0 || len(state.Events) != 0 || state.DroppedEvents != 0 || state.WorkspaceSnapshotGeneration != 0 || state.WorkspaceBaselineIncomplete) {
		return errors.New("uninitialized state contains observation history")
	}
	if state.WorkspaceSnapshotGeneration < 0 || state.WorkspaceSnapshotGeneration > workspaceSnapshotGeneration || (state.WorkspaceBaselineIncomplete && state.WorkspaceSnapshotGeneration == 0) {
		return errors.New("unsupported workspace snapshot classification generation")
	}
	if len(state.Seen) > maxSeen {
		return errors.New("state exceeds 4096 evidence keys")
	}
	if len(state.HookSeen) > maxHookSeen || len(state.HookSeenOrder) != len(state.HookSeen) {
		return errors.New("invalid hook cursor window size")
	}
	hookKeys := make(map[string]bool, len(state.HookSeenOrder))
	for _, key := range state.HookSeenOrder {
		if key == "" || state.HookSeen[key] == "" || hookKeys[key] {
			return errors.New("invalid hook cursor window order")
		}
		hookKeys[key] = true
	}
	if len(state.Events) > maxEvents || state.DroppedEvents < 0 {
		return errors.New("invalid state event retention counters")
	}
	for key, hash := range state.Seen {
		if key == "" || hash == "" {
			return errors.New("invalid state evidence cursor")
		}
	}
	return nil
}

// Parent paths can include system aliases such as macOS /var -> /private/var.
// The state directory itself and files within it must not be symbolic links.
func openStateRoot(dir string, create bool) (*os.Root, error) {
	if dir == "" {
		return nil, errors.New("state directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			return nil, fmt.Errorf("create state parent: %w", err)
		}
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("resolve state parent: %w", err)
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	if create {
		if err := os.Mkdir(abs, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspect state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("state directory must be a directory, not a symbolic link")
	}
	if info.Mode().Perm() != 0700 {
		return nil, errors.New("state directory permissions must be 0700")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open state directory: %w", err)
	}
	opened, err := root.Stat(".")
	after, afterErr := os.Lstat(abs)
	if err != nil || afterErr != nil || !after.IsDir() || !os.SameFile(info, opened) || !os.SameFile(after, opened) {
		root.Close()
		return nil, errors.New("state directory changed while opening")
	}
	return root, nil
}

func checkRegularFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a symbolic link or special file", name)
	}
	if info.Mode().Perm() != 0600 {
		return fmt.Errorf("%s permissions must be 0600", name)
	}
	return nil
}

func openStateFile(root *os.Root, name string, flags int, create bool) (*os.File, error) {
	if create {
		f, err := root.OpenFile(name, flags|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create %s: %w", name, err)
		}
	}
	if err := checkRegularFile(root, name); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	f, err := openExistingStateFile(root, name, flags)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	after, afterErr := root.Lstat(name)
	if err != nil || afterErr != nil || !after.Mode().IsRegular() || opened.Mode().Perm() != 0600 || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		f.Close()
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	return f, nil
}
