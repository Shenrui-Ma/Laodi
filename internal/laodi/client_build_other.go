//go:build !windows

package laodi

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

func DetectBuild(app string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", "CFBundleVersion", "raw", clientMetadataPath(app)).Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

func refreshClientBuild(s *Scanner) {
	if s.App != "" {
		info, e := os.Stat(clientMetadataPath(s.App))
		if e != nil {
			s.Build = "unknown"
		} else if !info.ModTime().Equal(s.appStamp) {
			s.Build = DetectBuild(s.App)
			s.appStamp = info.ModTime()
		}
	}
}

func supportedSnapshotBuild(build, app string) bool          { return build == KnownBuild }
func unsupportedSnapshotBuildDiagnostic(build string) string { return "zcode_build_not_verified" }
