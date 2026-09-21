//go:build !windows

package laodi

func PlanArchiveGuard(home string) (ArchiveGuardPlan, error) { return planArchiveGuardDarwin(home) }
func EnableArchiveGuard(plan ArchiveGuardPlan, state string) (ArchiveGuardResult, error) {
	return enableArchiveGuardDarwin(plan, state)
}
func InspectArchiveGuard(home, state string) (ArchiveGuardStatus, error) {
	return inspectArchiveGuardDarwin(home, state)
}
func DisableArchiveGuard(home, state string) (ArchiveGuardResult, error) {
	return disableArchiveGuardDarwin(home, state)
}
func PlanZCodeProtection(app, home, workspace string) (ZCodeProtectionPlan, error) {
	return planZCodeProtectionDarwin(app, home, workspace)
}
func RunningZCodeProcesses(app string) ([]int, error) { return runningZCodeProcessesDarwin(app) }
func inspectProtectionBundle(app string) (string, string, error) {
	return inspectProtectionBundleDarwin(app)
}
func checkProtectionPath(path string, directory, missing bool) error {
	return checkProtectionPathDarwin(path, directory, missing)
}
func TestArchiveProtection(home, state, app string) (ArchiveProtectionTestResult, error) {
	return testArchiveProtectionDarwin(home, state, app)
}

func protectionRecordedApp(home, state string) string { return "" }

func checkArchiveProtectionScope(home string) error { return nil }
