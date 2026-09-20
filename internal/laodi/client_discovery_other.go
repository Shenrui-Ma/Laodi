//go:build !windows

package laodi

func DiscoverClients() ClientDiscovery {
	return ClientDiscovery{Candidates: []ClientCandidate{}, Scope: "windows_standard_locations_and_path",
		Unknowns: []string{"discovery_command_windows_only"}}
}
