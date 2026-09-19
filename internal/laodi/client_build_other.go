//go:build !windows

package laodi

import (
	"context"
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
