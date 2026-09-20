//go:build !windows

package laodi

import "path/filepath"

func DefaultStateDir(home string) (string, error) {
	return filepath.Join(home, "Library", "Application Support", "Laodi-skills"), nil
}

func DefaultClientApp(home string) string { return "/Applications/ZCode.app" }

func DefaultEvidenceRoot(home string) string {
	return filepath.Join(home, ".zcode", "v2", "checkpoints")
}

func clientMetadataPath(app string) string { return filepath.Join(app, "Contents", "Info.plist") }
