package laodi

// A PE display/file version is evidence of executable identity, not evidence
// that a Windows snapshot protocol matches a verified macOS build. A separate
// Windows contract must be reviewed before changing this fail-closed result.
func DetectBuild(app string) string { return "unknown" }
