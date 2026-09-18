package laodi

func SummarizeReport(r Report) AgentSummary {
	s := AgentSummary{SchemaVersion: SchemaVersion, Coverage: r.Coverage, Parser: r.Parser, Counts: map[string]int{}, Diagnostics: r.Diagnostics, Unknowns: []string{"no_network_capture", "no_blocking", "record_time_not_upload_time", "no_remote_retention_verification", "unknown_clients_and_in_memory_uploads_not_covered"}}
	hasHooks := r.Parser == HookParserID
	for _, f := range r.Findings {
		hasHooks = hasHooks || isToolHookKind(f.Kind)
		s.Counts[f.Kind]++
		addSummarySource(&s, f.Source)
	}
	if hasHooks {
		s.Unknowns = append(s.Unknowns, "tool_hooks_require_client_configuration", "tool_output_detection_does_not_prove_model_request_or_upload", "unobserved_tools_and_host_background_uploads_not_covered")
	} else {
		s.Unknowns = append(s.Unknowns, "no_source_content_inspection")
	}
	if r.Parser != HookParserID {
		s.Unknowns = append(s.Unknowns, "snapshot_parser_reads_metadata_only", "snapshot_authorization_not_inferred", "attempt_count_is_not_network_request_count", "acceptance_is_client_http_success_record_only")
	}
	return s
}

func SummarizeState(st State) AgentSummary {
	parser := st.Parser
	if parser == "" {
		parser = "unknown"
	}
	s := AgentSummary{SchemaVersion: SchemaVersion, Coverage: st.Coverage, Parser: parser, Counts: map[string]int{}, Events: []SummaryEvent{}, Diagnostics: st.Diagnostics, Unknowns: []string{"no_network_capture", "no_blocking", "notification_delivery_not_guaranteed", "event_time_is_observation_time"}}
	s.Unknowns = append(s.Unknowns, "tool_hooks_require_client_configuration", "tool_output_detection_does_not_prove_model_request_or_upload", "unobserved_tools_and_host_background_uploads_not_covered", "hook_replay_dedup_window_limited")
	for _, e := range st.Events {
		s.Counts[e.Kind]++
		addSummarySource(&s, e.Source)
		s.Events = append(s.Events, SummaryEvent{Source: summarySource(e.Source), ID: e.ID, Kind: e.Kind, Counts: e.Counts, BaselineExisting: e.BaselineExisting, Notification: e.Notification, ObservedAt: e.ObservedAt, Unknowns: e.Unknowns})
	}
	return s
}

func summarySource(source string) string {
	if source == "zcode" || source == "claude-code" {
		return source
	}
	return ""
}

func addSummarySource(summary *AgentSummary, source string) {
	if source = summarySource(source); source != "" {
		if summary.SourceCounts == nil {
			summary.SourceCounts = map[string]int{}
		}
		summary.SourceCounts[source]++
	}
}
