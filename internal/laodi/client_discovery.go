package laodi

// ClientCandidate reports only public program identity. Discovering a program
// is not validation of its hooks, configuration location or snapshot protocol.
type ClientCandidate struct {
	Adapter          string `json:"adapter"`
	Executable       string `json:"executable"`
	FileVersion      string `json:"file_version,omitempty"`
	SHA256           string `json:"sha256"`
	SnapshotCoverage string `json:"snapshot_coverage"`
	HookCoverage     string `json:"hook_coverage"`
}

type ClientDiscovery struct {
	Candidates []ClientCandidate `json:"candidates"`
	Scope      string            `json:"scope"`
	Unknowns   []string          `json:"unknowns"`
}
