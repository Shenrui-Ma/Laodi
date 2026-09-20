package laodi

// WindowsHookContract records only public binary/protocol identity. It is not a
// claim that this user's session delivered a hook or that snapshots are parsed.
type WindowsHookContract struct {
	Adapter       string `json:"adapter"`
	Executable    string `json:"executable"`
	Version       string `json:"version"`
	SHA256        string `json:"sha256"`
	Executor      string `json:"executor"`
	Source        string `json:"source"`
	RuntimeSHA256 string `json:"runtime_sha256,omitempty"`
}
