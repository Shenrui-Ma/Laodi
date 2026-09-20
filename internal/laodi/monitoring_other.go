//go:build !windows

package laodi

func recordHookObservation(string, HookInspection) {}
func AddMonitoringSummary(*AgentSummary, string)   {}
