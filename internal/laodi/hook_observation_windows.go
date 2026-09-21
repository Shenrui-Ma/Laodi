//go:build windows

package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const hookObservationDirectory = "hook-observations"
const hookObservationInterval = time.Minute

type hookObservationRecord struct {
	Schema  int                  `json:"schema"`
	Samples map[string]time.Time `json:"samples"`
}

func validHookObservationCategory(category string) bool {
	switch category {
	case "callback_received", "shell_command_not_parsed", "tool_not_observed", "event_not_observed", "failed_tool_no_response", "response_fields_not_observed", "correlation_unavailable", "inspection_incomplete":
		return true
	}
	return false
}

func hookObservationCategories(in HookInspection) []string {
	categories := []string{"callback_received"}
	for _, value := range in.Unknowns {
		if validHookObservationCategory(value) {
			categories = appendUnknown(categories, value)
		}
	}
	for _, signal := range in.Signals {
		if signal.Kind == "hook_coverage_degraded" {
			categories = appendUnknown(categories, "inspection_incomplete")
		}
	}
	return categories
}

func readHookObservations(root *os.Root, adapter string) (hookObservationRecord, error) {
	record := hookObservationRecord{Schema: 1, Samples: map[string]time.Time{}}
	data, err := readHookRecordBytes(root, adapter+".json")
	if errors.Is(err, os.ErrNotExist) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	if decodeWindowsRecord(data, &record) != nil || record.Schema != 1 || len(record.Samples) > 8 || len(record.Samples) == 0 {
		return record, errHookInboxInvalid
	}
	for category, at := range record.Samples {
		if !validHookObservationCategory(category) || at.IsZero() || at.After(time.Now().Add(time.Minute)) {
			return record, errHookInboxInvalid
		}
	}
	return record, nil
}

// This is best-effort timestamp sampling, not an exact invocation count or
// authentication of the caller. Normal complex commands never become incidents.
// At most one write per category per minute; no raw IDs, commands or outputs.
// A separate directory/lock preserves version-1 inbox compatibility and gives
// risk records priority. Missing/unreadable telemetry never proves no callback.
func recordHookObservation(stateDir string, in HookInspection) {
	if in.Adapter != "zcode" && in.Adapter != "claude-code" {
		return
	}
	parent, err := openStateRoot(stateDir, true)
	if err != nil {
		return
	}
	defer parent.Close()
	if privateDirectory(parent, hookObservationDirectory) != nil {
		return
	}
	root, err := openStateRoot(filepath.Join(parent.Name(), hookObservationDirectory), false)
	if err != nil {
		return
	}
	defer root.Close()
	categories := hookObservationCategories(in)
	now := time.Now().UTC()
	needsWrite := func(record hookObservationRecord) bool {
		for _, category := range categories {
			if now.Sub(record.Samples[category]) >= hookObservationInterval {
				return true
			}
		}
		return false
	}
	record, err := readHookObservations(root, in.Adapter)
	if err != nil || !needsWrite(record) {
		return
	}
	release, err := lockHookInbox(root)
	if err != nil {
		return
	}
	defer release()
	record, err = readHookObservations(root, in.Adapter)
	if err != nil || !needsWrite(record) {
		return
	}
	for _, category := range categories {
		if now.Sub(record.Samples[category]) >= hookObservationInterval {
			record.Samples[category] = now
		}
	}
	_ = writeWindowsRecord(root.Name(), in.Adapter+".json", record)
}

func getHookObservationSummary(stateDir, adapter string) HookObservationSummary {
	summary := HookObservationSummary{Status: "not_recorded"}
	root, err := openStateRoot(filepath.Join(stateDir, hookObservationDirectory), false)
	if errors.Is(err, os.ErrNotExist) {
		return summary
	}
	if err != nil {
		summary.Status = "unavailable"
		return summary
	}
	defer root.Close()
	record, err := readHookObservations(root, adapter)
	if err != nil {
		summary.Status = "unavailable"
		return summary
	}
	for category, at := range record.Samples {
		summary.Samples = append(summary.Samples, HookObservationSample{Category: category, At: at})
		if summary.LastSample == nil || at.After(*summary.LastSample) {
			timestamp := at
			summary.LastSample = &timestamp
		}
	}
	if summary.LastSample != nil {
		summary.Status = "observed_caller_not_authenticated"
	}
	sort.Slice(summary.Samples, func(i, j int) bool { return summary.Samples[i].Category < summary.Samples[j].Category })
	return summary
}
