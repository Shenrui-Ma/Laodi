package laodi

import (
	"slices"
	"testing"
)

func TestReportSummaryDistinguishesSnapshotMetadataAndToolOutput(t *testing.T) {
	snapshot := SummarizeReport(Report{Parser: ParserID})
	if !slices.Contains(snapshot.Unknowns, "no_source_content_inspection") || !slices.Contains(snapshot.Unknowns, "snapshot_parser_reads_metadata_only") {
		t.Fatal(snapshot)
	}
	for _, report := range []Report{
		{Parser: HookParserID},
		{Parser: ParserID, Findings: []Finding{{Kind: "sensitive_tool_output_detected", Source: "zcode"}}},
	} {
		summary := SummarizeReport(report)
		if slices.Contains(summary.Unknowns, "no_source_content_inspection") || !slices.Contains(summary.Unknowns, "tool_output_detection_does_not_prove_model_request_or_upload") {
			t.Fatal(summary)
		}
	}
}
