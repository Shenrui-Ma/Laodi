package laodi

import "time"

const SchemaVersion = 1
const ParserID = "zcode-3.12.3.7463-v3"

// Findings deliberately contain no source file contents or credential values.
type Finding struct {
	Source       string         `json:"source,omitempty"`
	Key          string         `json:"key"`
	Kind         string         `json:"kind"`
	Counts       map[string]int `json:"counts,omitempty"`
	Evidence     string         `json:"evidence"`
	EvidenceHash string         `json:"evidence_hash"` // Classification fingerprint for deduplication, not vendor content authentication.
	Unknowns     []string       `json:"unknowns"`
}

type Diagnostic struct {
	Code string `json:"code"`
}

type Report struct {
	SchemaVersion int          `json:"schema_version"`
	Parser        string       `json:"parser"`
	Coverage      string       `json:"coverage"`
	Artifacts     int          `json:"artifacts_checked"`
	BytesRead     int64        `json:"evidence_bytes_read"`
	Findings      []Finding    `json:"findings"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
	CheckedAt     time.Time    `json:"checked_at"`
}

type Event struct {
	Source           string         `json:"source,omitempty"`
	ID               string         `json:"id"`
	Key              string         `json:"key"`
	Kind             string         `json:"kind"`
	Counts           map[string]int `json:"counts,omitempty"`
	Evidence         string         `json:"evidence"`
	EvidenceHash     string         `json:"evidence_hash"`
	Unknowns         []string       `json:"unknowns"`
	ObservedAt       time.Time      `json:"observed_at"`
	BaselineExisting bool           `json:"baseline_existing"`
	Notification     string         `json:"notification"`
}

type State struct {
	SchemaVersion               int               `json:"schema_version"`
	Parser                      string            `json:"parser,omitempty"`
	WorkspaceSnapshotGeneration int               `json:"workspace_snapshot_generation,omitempty"`
	WorkspaceBaselineIncomplete bool              `json:"workspace_baseline_incomplete,omitempty"`
	RootID                      string            `json:"root_id"`
	Initialized                 bool              `json:"initialized"`
	Running                     bool              `json:"running,omitempty"`
	Seen                        map[string]string `json:"seen"`
	HookSeen                    map[string]string `json:"hook_seen,omitempty"`
	HookSeenOrder               []string          `json:"hook_seen_order,omitempty"`
	Events                      []Event           `json:"events"`
	Coverage                    string            `json:"coverage"`
	Diagnostics                 []Diagnostic      `json:"diagnostics,omitempty"`
	LastCheckedAt               time.Time         `json:"last_checked_at"`
	DroppedEvents               int               `json:"dropped_events"`
}

// AgentSummary is a separate whitelist type, never a redaction of raw Report.
type AgentSummary struct {
	Protection    *ProtectionSummary `json:"protection,omitempty"`
	SourceCounts  map[string]int     `json:"source_counts,omitempty"`
	SchemaVersion int                `json:"schema_version"`
	Coverage      string             `json:"coverage"`
	Parser        string             `json:"parser"`
	Counts        map[string]int     `json:"counts"`
	Events        []SummaryEvent     `json:"events,omitempty"`
	Diagnostics   []Diagnostic       `json:"diagnostics,omitempty"`
	Unknowns      []string           `json:"unknowns"`
}

type SummaryEvent struct {
	Source           string         `json:"source,omitempty"`
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	Counts           map[string]int `json:"counts,omitempty"`
	BaselineExisting bool           `json:"baseline_existing"`
	Notification     string         `json:"notification"`
	ObservedAt       time.Time      `json:"observed_at"`
	Unknowns         []string       `json:"unknowns,omitempty"`
}
