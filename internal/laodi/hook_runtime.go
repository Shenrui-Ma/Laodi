package laodi

// Hook events are already observations submitted by an explicitly configured
// adapter, not historical snapshot files discovered during the first sweep.
func isToolHookKind(kind string) bool {
	switch kind {
	case "sensitive_tool_access_requested", "sensitive_tool_output_detected", "hook_coverage_degraded":
		return true
	}
	return false
}

func hasSnapshotHistory(seen map[string]string) bool {
	for key := range seen {
		// Snapshot keys begin with the verified 12-character workspace id.
		if len(key) > 12 && key[12] == ':' && workspaceName.MatchString(key[:12]) {
			return true
		}
	}
	return false
}
