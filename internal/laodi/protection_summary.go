package laodi

import (
	"errors"
	"os"
	"path/filepath"
)

// ProtectionSummary contains fixed labels and counts only, never cache paths,
// account names, receipt data or filesystem errors.
type ProtectionSummary struct {
	Status              string `json:"status"`
	Scope               string `json:"scope"`
	ProtectedWorkspaces int    `json:"protected_workspaces"`
	ProtectedArtifacts  int    `json:"protected_artifacts"`
}

func SummarizeArchiveGuard(s ArchiveGuardStatus) ProtectionSummary {
	p := ProtectionSummary{Status: "disabled", Scope: "verified_client_archive_paths_only", ProtectedWorkspaces: s.ProtectedWorkspaces, ProtectedArtifacts: s.ProtectedArtifacts}
	switch {
	case s.RecoveryNeeded:
		p.Status = "recovery_needed"
	case s.Enabled && s.Healthy:
		p.Status = "enabled"
	case s.Enabled || len(s.Problems) > 0:
		p.Status = "degraded"
	}
	return p
}

func ReadArchiveProtectionSummary(home, stateDir, app string) ProtectionSummary {
	return readArchiveProtectionSummary(home, stateDir, app, func(app string) bool {
		_, _, err := inspectProtectionBundle(app)
		return err == nil
	})
}

func readArchiveProtectionSummary(home, stateDir, app string, verify func(string) bool) ProtectionSummary {
	p := ProtectionSummary{Status: "disabled", Scope: "verified_client_archive_paths_only"}
	if _, err := os.Lstat(filepath.Join(stateDir, "archive-guard", "receipt.json")); errors.Is(err, os.ErrNotExist) {
		return p
	}
	s, err := InspectArchiveGuard(home, stateDir)
	if err != nil {
		p.Status = "degraded"
		if s.RecoveryNeeded {
			p.Status = "recovery_needed"
		}
		return p
	}
	p = SummarizeArchiveGuard(s)
	if p.Status == "enabled" {
		if !verify(app) {
			p.Status = "unsupported_client"
		}
	}
	return p
}

func AddArchiveProtectionSummary(s *AgentSummary, home, stateDir, app string) {
	p := ReadArchiveProtectionSummary(home, stateDir, app)
	if p.Status == "disabled" {
		return
	}
	s.Protection = &p
	unknowns := []string{}
	for _, u := range s.Unknowns {
		if u != "no_blocking" {
			unknowns = append(unknowns, u)
		}
	}
	s.Unknowns = append(unknowns, "blocking_limited_to_verified_archive_paths", "other_upload_routes_not_blocked", "blocked_attempt_count_not_observed")
}

func lockArchiveGuardForRemoval(stateDir string) (func(), error) {
	guardDir := filepath.Join(stateDir, "archive-guard")
	release, err := AcquireLock(guardDir)
	if err != nil {
		return nil, errors.New("归档限制正在变更；稍后再移除老底")
	}
	if _, err := os.Lstat(filepath.Join(guardDir, "receipt.json")); !errors.Is(err, os.ErrNotExist) {
		release()
		return nil, errors.New("归档限制尚未撤销。请先正常退出客户端并执行 protect disable，再移除老底")
	}
	return release, nil
}
