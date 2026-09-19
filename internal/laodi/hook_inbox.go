package laodi

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	hookInboxName       = "hook-inbox"
	maxHookInboxRecords = 256
	maxHookRecordBytes  = 16 << 10
	hookInboxKeyName    = ".key"
	hookInboxGapName    = ".gap"
)

var (
	errHookInboxUnavailable = errors.New("hook inbox unavailable; observation coverage may be incomplete")
	errHookInboxInvalid     = errors.New("hook inspection rejected; observation coverage may be incomplete")
	errHookInboxFull        = errors.New("hook inbox full; observation coverage may be incomplete")
)

// HookInboxStatus reports queue metadata without implying hook delivery works.
type HookInboxStatus struct {
	Exists  bool `json:"exists"`
	Pending int  `json:"pending"`
	HasGap  bool `json:"has_gap"`
}

// GetHookInboxStatus is a bounded metadata-only snapshot. It creates nothing;
// a concurrent producer can change Pending immediately after this call.
func GetHookInboxStatus(stateDir string) (HookInboxStatus, error) {
	var status HookInboxStatus
	root, err := openHookInbox(stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, errHookInboxUnavailable
	}
	defer root.Close()
	status.Exists = true
	entries, err := hookInboxEntries(root)
	if err != nil {
		return status, errHookInboxUnavailable
	}
	for _, entry := range entries {
		if entry.Name() == hookInboxGapName {
			if err := checkRegularFile(root, hookInboxGapName); err != nil {
				return status, errHookInboxUnavailable
			}
			status.HasGap = true
		}
		if !hookInboxControl(entry.Name()) {
			status.Pending++
		}
	}
	return status, nil
}

// HookInboxBatch receipts are opaque record names, not paths. Pending is the
// total number of queued records at the start of this bounded read. Findings
// must be durably saved by the state writer before its receipts are acknowledged.
type HookInboxBatch struct {
	Findings    []Finding
	Receipts    []string
	Pending     int
	Diagnostics []Diagnostic
}

// This is intentionally a separate, closed persistence schema. Neither raw
// input, tool output, session IDs, paths nor arbitrary errors reach the queue.
type hookInboxRecord struct {
	Version     int               `json:"version"`
	Adapter     string            `json:"adapter"`
	EventName   string            `json:"event"`
	Correlation string            `json:"correlation"`
	Signals     []hookInboxSignal `json:"signals"`
}

type hookInboxSignal struct {
	Kind     string         `json:"kind"`
	Counts   map[string]int `json:"counts,omitempty"`
	Unknowns []string       `json:"unknowns,omitempty"`
}

// SubmitHookInspection is for short-lived asynchronous hook processes. It has
// an independent lock, bounded retry and bounded queue; it never writes State.
func SubmitHookInspection(stateDir string, in HookInspection) error {
	if len(in.Signals) == 0 {
		return nil
	}
	root, err := openHookInbox(stateDir, true)
	if err != nil {
		return errHookInboxUnavailable
	}
	defer root.Close()
	fail := func(err error) error {
		markHookInboxGap(root)
		return err
	}
	record, err := sanitizeHookInspection(in)
	if err != nil {
		return fail(errHookInboxInvalid)
	}
	release, err := lockHookInbox(root)
	if err != nil {
		return fail(errHookInboxUnavailable)
	}
	defer release()
	key, err := hookInboxKey(root, true)
	if err != nil {
		return fail(errHookInboxUnavailable)
	}
	if in.SessionID == "" || in.ToolUseID == "" {
		// No body-derived fallback: missing correlation yields independent
		// observations with an explicit limit, rather than hashing secrets.
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return fail(errHookInboxUnavailable)
		}
		record.Correlation = hex.EncodeToString(nonce[:])
		for i := range record.Signals {
			record.Signals[i].Unknowns = appendUnknown(record.Signals[i].Unknowns, "correlation_unavailable")
		}
	} else {
		identity, _ := json.Marshal([]string{in.Adapter, in.EventName, in.SessionID, in.ToolUseID})
		record.Correlation = hookMAC(key, identity)
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxHookRecordBytes {
		return fail(errHookInboxInvalid)
	}
	name := hookMAC(key, data) + ".json"
	if existing, err := readHookRecordBytes(root, name); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fail(errHookInboxInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(errHookInboxUnavailable)
	}
	entries, err := hookInboxEntries(root)
	if err != nil {
		return fail(errHookInboxUnavailable)
	}
	count := 0
	for _, entry := range entries {
		if !hookInboxControl(entry.Name()) {
			count++
		}
	}
	if count >= maxHookInboxRecords {
		return fail(errHookInboxFull)
	}
	if err := writeHookRecord(root, name, data); err != nil {
		return fail(errHookInboxUnavailable)
	}
	return nil
}

// ReadHookInbox does not create a missing inbox and never consumes records. A
// crash before Ack therefore replays the same Finding keys and fingerprints.
func ReadHookInbox(stateDir string, limit int) (HookInboxBatch, error) {
	var batch HookInboxBatch
	root, err := openHookInbox(stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return batch, nil
	}
	if err != nil {
		return batch, errHookInboxUnavailable
	}
	defer root.Close()
	release, err := lockHookInbox(root)
	if err != nil {
		return batch, errHookInboxUnavailable
	}
	defer release()
	if limit <= 0 {
		limit = 64
	}
	if limit > maxHookInboxRecords {
		limit = maxHookInboxRecords
	}
	if _, err := root.Lstat(hookInboxGapName); err == nil {
		if err := checkRegularFile(root, hookInboxGapName); err != nil {
			return batch, errHookInboxUnavailable
		}
		batch.Diagnostics = append(batch.Diagnostics, Diagnostic{Code: "hook_inbox_gap_recorded"})
		batch.Findings = append(batch.Findings, hookInboxGapFinding())
	} else if !errors.Is(err, os.ErrNotExist) {
		return batch, errHookInboxUnavailable
	}
	entries, err := hookInboxEntries(root)
	if err != nil {
		batch.Diagnostics = append(batch.Diagnostics, Diagnostic{Code: "hook_inbox_scan_limit"})
		return batch, errHookInboxUnavailable
	}
	key, keyErr := hookInboxKey(root, false)
	processed := 0
	for _, entry := range entries {
		name := entry.Name()
		if hookInboxControl(name) {
			continue
		}
		batch.Pending++
		if processed >= limit {
			continue
		}
		if !validHookReceipt(name) || keyErr != nil {
			batch.Diagnostics = addHookDiagnostic(batch.Diagnostics, "hook_inbox_record_invalid")
			continue
		}
		data, err := readHookRecordBytes(root, name)
		if err != nil || hookMAC(key, data)+".json" != name {
			batch.Diagnostics = addHookDiagnostic(batch.Diagnostics, "hook_inbox_record_invalid")
			continue
		}
		record, err := decodeHookRecord(data)
		if err != nil {
			batch.Diagnostics = addHookDiagnostic(batch.Diagnostics, "hook_inbox_record_invalid")
			continue
		}
		processed++
		for _, signal := range record.Signals {
			signalBytes, _ := json.Marshal(signal)
			batch.Findings = append(batch.Findings, Finding{
				Key:  "hook:" + record.Adapter + ":" + record.Correlation + ":" + signal.Kind,
				Kind: signal.Kind, Counts: copyCounts(signal.Counts), Source: record.Adapter,
				Evidence:     "tool_hook:" + record.Adapter + ":" + record.EventName,
				EvidenceHash: hookMAC(key, signalBytes), Unknowns: append([]string(nil), signal.Unknowns...),
			})
		}
		batch.Receipts = append(batch.Receipts, name)
	}
	return batch, nil
}

// AckHookInbox is idempotent and removes only verified records previously read
// by the caller. A sticky gap is deliberately not acked: a concurrent producer
// unable to acquire the lock must never have its loss marker silently erased.
func AckHookInbox(stateDir string, receipts []string) error {
	if len(receipts) == 0 {
		return nil
	}
	if len(receipts) > maxHookInboxRecords {
		return errHookInboxInvalid
	}
	for _, name := range receipts {
		if !validHookReceipt(name) {
			return errHookInboxInvalid
		}
	}
	root, err := openHookInbox(stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errHookInboxUnavailable
	}
	defer root.Close()
	release, err := lockHookInbox(root)
	if err != nil {
		return errHookInboxUnavailable
	}
	defer release()
	key, err := hookInboxKey(root, false)
	if err != nil {
		return errHookInboxUnavailable
	}
	for _, name := range receipts {
		data, err := readHookRecordBytes(root, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || hookMAC(key, data)+".json" != name {
			return errHookInboxInvalid
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errHookInboxUnavailable
		}
	}
	if err := syncHookInbox(root); err != nil {
		return errHookInboxUnavailable
	}
	return nil
}

func sanitizeHookInspection(in HookInspection) (hookInboxRecord, error) {
	record := hookInboxRecord{Version: 1, Adapter: in.Adapter, EventName: in.EventName}
	if in.Adapter != "zcode" && in.Adapter != "claude-code" && in.Adapter != "unknown" {
		return record, errHookInboxInvalid
	}
	if in.EventName != "PreToolUse" && in.EventName != "PostToolUse" && in.EventName != "PostToolUseFailure" && in.EventName != "" {
		return record, errHookInboxInvalid
	}
	if len(in.SessionID) > 4096 || len(in.ToolUseID) > 4096 || len(in.Signals) == 0 || len(in.Signals) > 3 {
		return record, errHookInboxInvalid
	}
	seen := map[string]bool{}
	for _, input := range in.Signals {
		if seen[input.Kind] || !validHookSignalKind(input.Kind) || len(input.Counts) > 16 || len(input.Unknowns) > 8 {
			return record, errHookInboxInvalid
		}
		if (in.Adapter == "unknown" || in.EventName == "") && input.Kind != "hook_coverage_degraded" {
			return record, errHookInboxInvalid
		}
		seen[input.Kind] = true
		signal := hookInboxSignal{Kind: input.Kind, Counts: make(map[string]int)}
		for name, value := range input.Counts {
			if !validHookCount(input.Kind, name) || value < 1 || value > MaxHookInputBytes {
				return record, errHookInboxInvalid
			}
			signal.Counts[name] = value
		}
		for _, unknown := range input.Unknowns {
			if !validHookUnknown(unknown) {
				return record, errHookInboxInvalid
			}
			signal.Unknowns = appendUnknown(signal.Unknowns, unknown)
		}
		sort.Strings(signal.Unknowns)
		record.Signals = append(record.Signals, signal)
	}
	sort.Slice(record.Signals, func(i, j int) bool { return record.Signals[i].Kind < record.Signals[j].Kind })
	return record, nil
}

func validHookSignalKind(kind string) bool {
	return kind == "sensitive_tool_access_requested" || kind == "sensitive_tool_output_detected" || kind == "hook_coverage_degraded"
}

func validHookCount(kind, name string) bool {
	switch kind {
	case "sensitive_tool_access_requested":
		return name == "env_file" || name == "credential_file" || name == "private_key_file"
	case "sensitive_tool_output_detected":
		return name == "private_key_block" || name == "provider_token" || name == "credential_assignment"
	case "hook_coverage_degraded":
		switch name {
		case "input_invalid", "input_too_large", "input_depth_exceeded", "input_nodes_exceeded", "input_read_failed", "input_encoding_unsupported", "input_timeout", "adapter_unsupported", "schema_unsupported", "output_missing", "output_shape_unsupported", "output_partial", "output_truncated":
			return true
		}
	}
	return false
}

func validHookUnknown(value string) bool {
	switch value {
	case "read_not_confirmed", "upload_not_observed", "credential_validity_unknown", "model_delivery_not_observed", "inspection_incomplete", "tool_failed", "correlation_unavailable":
		return true
	}
	return false
}

func openHookInbox(stateDir string, create bool) (*os.Root, error) {
	parent, err := openStateRoot(stateDir, create)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if create {
		if err := privateDirectory(parent, hookInboxName); err != nil {
			return nil, err
		}
	}
	before, err := parent.Lstat(hookInboxName)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, errHookInboxUnavailable
	}
	// Go 1.25 gives a nested Root only its relative child name. Our Unix
	// no-follow file opener and lock use Root.Name(), so bind an absolute
	// name explicitly and keep verifying it against the already-open parent.
	root, err := openStateRoot(filepath.Join(parent.Name(), hookInboxName), false)
	if err != nil {
		return nil, err
	}
	opened, openedErr := root.Stat(".")
	after, afterErr := parent.Lstat(hookInboxName)
	if openedErr != nil || afterErr != nil || !after.IsDir() || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		root.Close()
		return nil, errHookInboxUnavailable
	}
	return root, nil
}

func lockHookInbox(root *os.Root) (func(), error) {
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		release, err := AcquireLock(root.Name())
		if err == nil {
			return release, nil
		}
		if !time.Now().Before(deadline) {
			return nil, errHookInboxUnavailable
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func hookInboxKey(root *os.Root, create bool) ([]byte, error) {
	if create {
		if _, err := root.Lstat(hookInboxKeyName); errors.Is(err, os.ErrNotExist) {
			var key [32]byte
			if _, err := rand.Read(key[:]); err != nil {
				return nil, err
			}
			if err := writeHookRecord(root, hookInboxKeyName, key[:]); err != nil {
				return nil, err
			}
		}
	}
	f, err := openStateFile(root, hookInboxKeyName, os.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(data) != 32 {
		return nil, errHookInboxInvalid
	}
	return data, nil
}

func hookMAC(key, data []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

func validHookReceipt(name string) bool {
	if len(name) != 69 || !strings.HasSuffix(name, ".json") || filepath.Base(name) != name {
		return false
	}
	decoded, err := hex.DecodeString(name[:64])
	return err == nil && len(decoded) == 32 && strings.ToLower(name) == name
}

func hookInboxControl(name string) bool {
	return name == ".lock" || name == hookInboxKeyName || name == hookInboxGapName
}

func hookInboxEntries(root *os.Root) ([]os.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxHookInboxRecords + 4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxHookInboxRecords+3 {
		return nil, errHookInboxFull
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func readHookRecordBytes(root *os.Root, name string) ([]byte, error) {
	f, err := openStateFile(root, name, os.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxHookRecordBytes+1))
	if err != nil || len(data) > maxHookRecordBytes {
		return nil, errHookInboxInvalid
	}
	return data, nil
}

func decodeHookRecord(data []byte) (hookInboxRecord, error) {
	var record hookInboxRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, errHookInboxInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || record.Version != 1 {
		return record, errHookInboxInvalid
	}
	if correlation, err := hex.DecodeString(record.Correlation); err != nil || len(correlation) != 32 {
		return record, errHookInboxInvalid
	}
	in := HookInspection{Adapter: record.Adapter, EventName: record.EventName}
	for _, signal := range record.Signals {
		in.Signals = append(in.Signals, HookSignal{Kind: signal.Kind, Counts: signal.Counts, Unknowns: signal.Unknowns})
	}
	if _, err := sanitizeHookInspection(in); err != nil {
		return record, err
	}
	return record, nil
}

func writeHookRecord(root *os.Root, name string, data []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".write-" + hex.EncodeToString(nonce[:])
	f, err := createPrivateFile(root, tmp, os.O_WRONLY)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := root.Lstat(name); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errHookInboxInvalid
	}
	if err := replaceStateFile(root, tmp, name); err != nil {
		return err
	}
	return syncHookInbox(root)
}

func syncHookInbox(root *os.Root) error {
	return syncStateDirectory(root)
}

func markHookInboxGap(root *os.Root) {
	// O_EXCL neither follows a planted symlink nor truncates another writer.
	f, err := createPrivateFile(root, hookInboxGapName, os.O_WRONLY)
	if err != nil {
		return
	}
	f.Write([]byte("observation_gap\n"))
	f.Sync()
	f.Close()
	syncHookInbox(root)
}

func hookInboxGapFinding() Finding {
	return Finding{Key: "hook:inbox:gap", Kind: "hook_coverage_degraded", Evidence: "hook_inbox_gap_marker", EvidenceHash: "hook-inbox-gap-v1", Unknowns: []string{"inspection_incomplete", "upload_not_observed"}}
}

func addHookDiagnostic(diagnostics []Diagnostic, code string) []Diagnostic {
	for _, existing := range diagnostics {
		if existing.Code == code {
			return diagnostics
		}
	}
	return append(diagnostics, Diagnostic{Code: code})
}
