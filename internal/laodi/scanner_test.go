package laodi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testWorkspace = "0123456789ab"

var hashA = strings.Repeat("a", 64)
var hashB = strings.Repeat("b", 64)

func writeFixture(t *testing.T, root, rel string, v any) {
	t.Helper()
	p := filepath.Join(root, rel)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func manifestFixture(paths ...string) any {
	files := []map[string]any{}
	for _, p := range paths {
		files = append(files, map[string]any{"path": p, "sizeBytes": 123})
	}
	return map[string]any{"schema": "repo_snapshot_manifest/v2", "workspaceKey": "/PRIVATE/customer-project", "createdAt": 123456, "files": files, "stats": map[string]any{"includedFileCount": len(files)}}
}
func count(r Report, kind string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

func TestManifestAndStateSemantics(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture(".git/objects/x", ".git/logs/HEAD", "src/a.go"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project", "failureCount": 600, "pendingUpload": map[string]any{"nextManifestHash": hashA, "attemptCount": 0}})
	s := Scanner{Root: root, Build: KnownBuild}
	r := s.Scan()
	if r.Coverage != "observing" || count(r, "sensitive_manifest_match") != 1 || count(r, "upload_attempt_recorded") != 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project", "activeUpload": map[string]any{"nextManifestHash": hashA, "attemptCount": 2}, "pendingUpload": map[string]any{"nextManifestHash": hashA, "attemptCount": 2}, "lastAcceptedManifestHash": hashA})
	r = s.Scan()
	if count(r, "upload_attempt_recorded") != 1 || count(r, "upload_acceptance_recorded") != 1 {
		t.Fatalf("alias/accepted mismatch: %+v", r)
	}
	out, _ := json.Marshal(SummarizeReport(r))
	for _, private := range []string{"PRIVATE", "customer-project", hashA, ".git/objects", "workspacePath"} {
		if strings.Contains(string(out), private) {
			t.Fatalf("agent summary exposed %q", private)
		}
	}
}

func TestAcceptedDoesNotAttachToDifferentPendingManifest(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture("src/normal.go"))
	writeFixture(t, root, testWorkspace+"/manifests/"+hashB+".json", manifestFixture(".git/objects/history"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project", "lastAcceptedManifestHash": hashA, "activeUpload": map[string]any{"nextManifestHash": hashB, "attemptCount": 1}})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if count(r, "upload_acceptance_recorded") != 0 || count(r, "upload_attempt_recorded") != 1 {
		t.Fatalf("pending falsely accepted: %+v", r)
	}
}

func TestUnsupportedBuildDoesNotInterpretEvidence(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture(".git/HEAD"))
	r := (&Scanner{Root: root, Build: "3.99.0"}).Scan()
	if r.Coverage != "unsupported_build" || len(r.Findings) != 0 || r.Artifacts != 0 {
		t.Fatal(r)
	}
}

func TestSymlinksAndStatePathsAreNotFollowed(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writeFixture(t, other, "outside.json", manifestFixture(".git/objects/sensitive"))
	if e := os.MkdirAll(filepath.Join(root, testWorkspace, "manifests"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Join(other, "outside.json"), filepath.Join(root, testWorkspace, "manifests", hashA+".json")); e != nil {
		t.Fatal(e)
	}
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project", "lastAcceptedManifestHash": hashA, "lastAcceptedManifestPath": filepath.Join(other, "outside.json")})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if len(r.Findings) != 0 || r.Coverage != "degraded" {
		t.Fatalf("followed outside path: %+v", r)
	}
}

func TestPartialWriteRecoversAndOrdinarySnapshotIsNotGitHistory(t *testing.T) {
	root := t.TempDir()
	rel := testWorkspace + "/manifests/" + hashA + ".json"
	writeFixture(t, root, rel, manifestFixture("src/a.go"))
	s := Scanner{Root: root, Build: KnownBuild}
	if r := s.Scan(); count(r, "sensitive_manifest_match") != 0 || count(r, "workspace_snapshot_manifest") != 1 {
		t.Fatal(r)
	}
	p := filepath.Join(root, rel)
	if e := os.WriteFile(p, []byte(`{"schema":`), 0600); e != nil {
		t.Fatal(e)
	}
	if r := s.Scan(); r.Coverage != "degraded" || len(r.Findings) != 0 {
		t.Fatal(r)
	}
	writeFixture(t, root, rel, manifestFixture(".git/objects/new"))
	if r := s.Scan(); r.Coverage != "observing" || count(r, "sensitive_manifest_match") != 1 {
		t.Fatal(r)
	}
}

func TestRawBytesAreNotUsedAsZCodeManifestIdentifier(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture(".git/objects/x"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project", "lastAcceptedManifestHash": hashA})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if count(r, "upload_acceptance_recorded") != 1 {
		t.Fatal("must not compare file bytes hash with ZCode path/size identifier")
	}
}

func TestHistoricalManifestsCannotStarveAnotherWorkspaceCurrentEvidence(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxArtifacts; i++ {
		writeFixture(t, root, testWorkspace+"/manifests/"+fmt.Sprintf("%064x", i)+".json", manifestFixture("src/ordinary.go"))
	}
	second := "fedcba987654"
	writeFixture(t, root, second+"/manifests/"+hashA+".json", manifestFixture(".git/objects/pack/history.pack"))
	writeFixture(t, root, second+"/state.json", map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
		"lastAcceptedManifestHash": hashA,
		"activeUpload":             map[string]any{"nextManifestHash": hashA, "attemptCount": 1},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if count(r, "upload_acceptance_recorded") != 1 || count(r, "upload_attempt_recorded") != 1 {
		t.Fatalf("old manifests starved current evidence: %+v", r)
	}
	if r.Artifacts > MaxArtifacts || r.Coverage != "degraded" {
		t.Fatalf("partial bounded scan must report degraded coverage: %+v", r)
	}
}

func TestGitMetadataDoesNotClaimHistoricalObjects(t *testing.T) {
	for _, p := range []string{".git", "repo/.git", ".git/HEAD", ".git/config", ".git/logs/HEAD", ".git/objects", ".git/lfs/objects", ".git/objects/../config", "repo/.gitignore"} {
		t.Run(p, func(t *testing.T) {
			data, _ := json.Marshal(manifestFixture(p))
			m, err := parseManifest(json.NewDecoder(strings.NewReader(string(data))))
			if err != nil || m.HistoryFiles != 0 {
				t.Fatalf("metadata %q classified as historical object: %+v, %v", p, m, err)
			}
		})
	}
	data, _ := json.Marshal(manifestFixture(".git/objects/aa/object", "repo/.git/objects/pack/p.pack", `.git\lfs\objects\aa\large`))
	m, err := parseManifest(json.NewDecoder(strings.NewReader(string(data))))
	if err != nil || m.HistoryFiles != 3 {
		t.Fatalf("Git and LFS object paths must still be counted: %+v, %v", m, err)
	}

	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture(".git", ".git/logs/HEAD"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
		"lastAcceptedManifestHash": hashA, "activeUpload": map[string]any{"nextManifestHash": hashA, "attemptCount": 3},
	})
	if r := (&Scanner{Root: root, Build: KnownBuild}).Scan(); count(r, "sensitive_manifest_match") != 0 || count(r, "upload_attempt_recorded") != 0 || count(r, "upload_acceptance_recorded") != 0 || count(r, "workspace_snapshot_upload_acceptance_recorded") != 1 {
		t.Fatalf("metadata snapshot must stay visible without claiming historical objects: %+v", r)
	}
}

func TestCurrentReferenceBudgetRotatesAcrossScans(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxWorkspaces; i++ {
		ws := fmt.Sprintf("%012x", i)
		writeFixture(t, root, ws+"/manifests/"+hashA+".json", manifestFixture("src/ordinary.go"))
		writeFixture(t, root, ws+"/manifests/"+hashB+".json", manifestFixture(".git/objects/history"))
		writeFixture(t, root, ws+"/state.json", map[string]any{
			"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
			"lastAcceptedManifestHash": hashA, "activeUpload": map[string]any{"nextManifestHash": hashB, "attemptCount": 1},
		})
	}
	s := &Scanner{Root: root, Build: KnownBuild}
	first, second := s.Scan(), s.Scan()
	if first.Coverage != "degraded" || second.Coverage != "degraded" {
		t.Fatal("overflow of current references must be visible in coverage")
	}
	if first.Artifacts > MaxArtifacts || second.Artifacts > MaxArtifacts {
		t.Fatal("reference rotation exceeded the per-scan artifact budget")
	}
	if count(first, "upload_attempt_recorded")+count(second, "upload_attempt_recorded") != MaxWorkspaces {
		t.Fatalf("pending evidence starved behind accepted references: first=%d second=%d", count(first, "upload_attempt_recorded"), count(second, "upload_attempt_recorded"))
	}
	if len(s.cache) > MaxArtifacts {
		t.Fatalf("metadata cache exceeded budget: %d", len(s.cache))
	}
}

func TestHistoricalDiscoverySharesBudgetAndRotatesWithinPage(t *testing.T) {
	root := t.TempDir()
	second := "fedcba987654"
	for _, ws := range []string{testWorkspace, second} {
		for i := 0; i < MaxArtifacts; i++ {
			writeFixture(t, root, ws+"/manifests/"+fmt.Sprintf("%064x", i)+".json", manifestFixture(".git/objects/history"))
		}
	}
	s := &Scanner{Root: root, Build: KnownBuild}
	seen := map[string]bool{}
	for scan := 0; scan < 2; scan++ {
		r := s.Scan()
		byWorkspace := map[string]int{}
		for _, finding := range r.Findings {
			seen[finding.Key] = true
			byWorkspace[strings.SplitN(finding.Key, ":", 2)[0]]++
		}
		if r.Coverage != "degraded" || r.Artifacts > MaxArtifacts || byWorkspace[testWorkspace] != MaxArtifacts/2 || byWorkspace[second] != MaxArtifacts/2 {
			t.Fatalf("historical budget was not shared evenly: %+v", r)
		}
	}
	if len(seen) != MaxArtifacts*2 {
		t.Fatalf("history within the bounded page did not rotate: %d/%d", len(seen), MaxArtifacts*2)
	}
}

func extraManifestFixture(group string, paths ...string) map[string]any {
	files := []map[string]any{}
	for _, p := range paths {
		files = append(files, map[string]any{"path": p, "sizeBytes": 123, "contentHash": strings.Repeat("c", 64), "source": "app-memory:PRIVATE-settings"})
	}
	return map[string]any{"schema": "repo_snapshot_extra_manifest/v1", "groups": []map[string]any{{"groupId": group, "files": files}}}
}

func TestExtraManifestWithoutWorkspaceManifest(t *testing.T) {
	root := t.TempDir()
	// The paths are metadata only; neither target exists or is opened.
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashA+".json", extraManifestFixture("global-configs", "settings.behavior.json", "/PRIVATE/nonexistent/mcp.json"))
	s := &Scanner{Root: root, Build: KnownBuild}
	r := s.Scan()
	if r.Coverage != "observing" || len(r.Findings) != 1 || count(r, "global_config_manifest_match") != 1 || r.Findings[0].Counts["global_config_paths"] != 2 {
		t.Fatalf("independent extra manifest missed: %+v", r)
	}
	if count(r, "sensitive_manifest_match") != 0 || count(r, "global_config_upload_acceptance_recorded") != 0 {
		t.Fatalf("extra manifest fabricated history or acceptance: %+v", r)
	}
	if cached := s.Scan(); cached.BytesRead != 0 || count(cached, "global_config_manifest_match") != 1 {
		t.Fatalf("extra metadata cache did not apply: %+v", cached)
	}
}

func TestEmptyWorkspaceStillReportsExtraUploadEvidence(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture())
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashA+".json", extraManifestFixture("global-configs", "mcp.json"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
		"lastAcceptedManifestHash": hashA, "lastAcceptedExtraManifestHash": hashA,
		"activeUpload": map[string]any{"nextManifestHash": hashA, "nextExtraManifestHash": hashA, "attemptCount": 1},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if r.Coverage != "observing" || len(r.Findings) != 3 || count(r, "global_config_manifest_match") != 1 || count(r, "global_config_upload_attempt_recorded") != 1 || count(r, "global_config_upload_acceptance_recorded") != 1 {
		t.Fatalf("empty ordinary manifest suppressed extra evidence: %+v", r)
	}
	for _, f := range r.Findings {
		if len(f.Counts) != 1 || f.Counts["global_config_paths"] != 1 {
			t.Fatalf("extra evidence leaked unsupported counts: %+v", f)
		}
	}
}

func TestExtraAcceptedAndPendingHashesAreIndependent(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashA+".json", extraManifestFixture("global-configs", "settings.behavior.json"))
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashB+".json", extraManifestFixture("global-configs", "mcp.json", "skills.json"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
		"lastAcceptedExtraManifestHash": hashA,
		"activeUpload":                  map[string]any{"nextExtraManifestHash": hashB, "attemptCount": 2},
		"pendingUpload":                 map[string]any{"nextExtraManifestHash": hashB, "attemptCount": 2},
		"latestPendingUpload":           map[string]any{"nextExtraManifestHash": hashB, "attemptCount": 2},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if count(r, "global_config_upload_acceptance_recorded") != 1 || count(r, "global_config_upload_attempt_recorded") != 1 {
		t.Fatalf("extra slots not deduplicated: %+v", r)
	}
	for _, f := range r.Findings {
		switch f.Kind {
		case "global_config_upload_acceptance_recorded":
			if f.Key != testWorkspace+":extra:"+hashA+":accepted" || f.Counts["global_config_paths"] != 1 {
				t.Fatalf("pending extra data attached to acceptance: %+v", f)
			}
		case "global_config_upload_attempt_recorded":
			if f.Key != testWorkspace+":extra:"+hashB+":attempt" || f.Counts["global_config_paths"] != 2 {
				t.Fatalf("attempt attached to wrong extra data: %+v", f)
			}
		}
	}
}

func TestExtraPendingAliasAndLatestSlotSemantics(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashA+".json", extraManifestFixture("global-configs", "mcp.json"))
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashB+".json", extraManifestFixture("global-configs", "skills.json"))
	state := map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
		"pendingUpload":       map[string]any{"nextExtraManifestHash": hashA, "attemptCount": 1},
		"latestPendingUpload": map[string]any{"nextExtraManifestHash": hashB, "attemptCount": 0},
	}
	writeFixture(t, root, testWorkspace+"/state.json", state)
	s := &Scanner{Root: root, Build: KnownBuild}
	if r := s.Scan(); count(r, "global_config_upload_attempt_recorded") != 1 || count(r, "global_config_upload_acceptance_recorded") != 0 {
		t.Fatalf("queued extra group incorrectly interpreted: %+v", r)
	}
	state["activeUpload"] = map[string]any{"nextExtraManifestHash": hashA, "attemptCount": 0}
	state["latestPendingUpload"] = map[string]any{"nextExtraManifestHash": hashB, "attemptCount": 1}
	writeFixture(t, root, testWorkspace+"/state.json", state)
	r := s.Scan()
	if count(r, "global_config_upload_attempt_recorded") != 1 {
		t.Fatalf("active slot must take precedence over pending alias: %+v", r)
	}
	for _, f := range r.Findings {
		if f.Kind == "global_config_upload_attempt_recorded" && !strings.Contains(f.Key, hashB) {
			t.Fatalf("latest pending group not independently associated: %+v", f)
		}
	}
}

func TestExtraReferencesGroupIsNotGlobalConfiguration(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashA+".json", extraManifestFixture("references", "mcp.json"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
		"lastAcceptedExtraManifestHash": hashA,
		"activeUpload":                  map[string]any{"nextExtraManifestHash": hashA, "attemptCount": 1},
	})
	if r := (&Scanner{Root: root, Build: KnownBuild}).Scan(); r.Coverage != "observing" || len(r.Findings) != 0 {
		t.Fatalf("references group reported as global configuration: %+v", r)
	}
}

func TestExtraSummaryContainsCountsNotConfigurationMetadata(t *testing.T) {
	root := t.TempDir()
	fixture := extraManifestFixture("global-configs", "/PRIVATE/customer-project/mcp.json")
	fixture["unrecognized_contents"] = map[string]any{"token": "SYNTHETIC-SECRET-DO-NOT-SHARE"}
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashA+".json", fixture)
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if len(r.Findings) != 1 {
		t.Fatal(r)
	}
	out, err := json.Marshal(SummarizeReport(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE", "customer-project", "mcp.json", "app-memory", "contentHash", strings.Repeat("c", 64), hashA, "SYNTHETIC-SECRET", `"source":`, "workspacePath"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("extra summary exposed %q: %s", secret, out)
		}
	}
}

func TestExtraAndOrdinarySameHashHaveDistinctFindingKeys(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, testWorkspace+"/manifests/"+hashA+".json", manifestFixture(".git/objects/history"))
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashA+".json", extraManifestFixture("global-configs", "mcp.json"))
	writeFixture(t, root, testWorkspace+"/state.json", map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
		"lastAcceptedManifestHash": hashA, "lastAcceptedExtraManifestHash": hashA,
		"activeUpload": map[string]any{"nextManifestHash": hashA, "nextExtraManifestHash": hashA, "attemptCount": 1},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	keys, fingerprints := map[string]bool{}, map[string]bool{}
	for _, f := range r.Findings {
		keys[f.Key], fingerprints[f.EvidenceHash] = true, true
	}
	if r.Coverage != "observing" || len(r.Findings) != 6 || len(keys) != 6 || len(fingerprints) != 6 {
		t.Fatalf("manifest types collided: %+v", r)
	}
}

func TestExtraInvalidMetadataAndSymlinksDegrade(t *testing.T) {
	for _, mode := range []string{"unknown-schema", "invalid-entry", "missing-groups", "partial-json", "oversized", "symlink-file", "symlink-directory"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			rel := testWorkspace + "/extra-manifests/" + hashA + ".json"
			fixture := extraManifestFixture("global-configs", "mcp.json")
			switch mode {
			case "unknown-schema":
				fixture["schema"] = "repo_snapshot_extra_manifest/v99"
			case "invalid-entry":
				fixture["groups"] = []any{map[string]any{"groupId": "global-configs", "files": []any{map[string]any{"path": "mcp.json", "sizeBytes": 3, "contentHash": "not-a-hash"}}}}
			case "missing-groups":
				delete(fixture, "groups")
			}
			writeFixture(t, root, rel, fixture)
			p := filepath.Join(root, rel)
			switch mode {
			case "partial-json":
				if err := os.WriteFile(p, []byte(`{"schema":`), 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.Truncate(p, MaxArtifactBytes+1); err != nil {
					t.Fatal(err)
				}
			case "symlink-file", "symlink-directory":
				outside := t.TempDir()
				writeFixture(t, outside, hashA+".json", fixture)
				target := filepath.Join(outside, hashA+".json")
				if mode == "symlink-directory" {
					p, target = filepath.Dir(p), outside
				}
				if err := os.RemoveAll(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
			}
			writeFixture(t, root, testWorkspace+"/state.json", map[string]any{
				"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project", "lastAcceptedExtraManifestHash": hashA,
			})
			r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
			if r.Coverage != "degraded" || len(r.Findings) != 0 || len(r.Diagnostics) == 0 {
				t.Fatalf("invalid extra artifact was trusted: %+v", r)
			}
		})
	}
}

func TestExtraCurrentReferencesPrecedeOrdinaryHistoryAcrossWorkspaces(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxArtifacts; i++ {
		writeFixture(t, root, testWorkspace+"/manifests/"+fmt.Sprintf("%064x", i)+".json", manifestFixture("src/ordinary.go"))
	}
	second := "fedcba987654"
	writeFixture(t, root, second+"/extra-manifests/"+hashA+".json", extraManifestFixture("global-configs", "mcp.json"))
	writeFixture(t, root, second+"/state.json", map[string]any{
		"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project", "lastAcceptedExtraManifestHash": hashA,
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	if count(r, "global_config_upload_acceptance_recorded") != 1 || r.Coverage != "degraded" || r.Artifacts > MaxArtifacts {
		t.Fatalf("ordinary history starved current extra evidence: %+v", r)
	}
}

func TestExtraHistoricalDiscoverySharesBudgetWithOrdinaryHistory(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxArtifacts; i++ {
		id := fmt.Sprintf("%064x", i)
		writeFixture(t, root, testWorkspace+"/manifests/"+id+".json", manifestFixture(".git/objects/history"))
		writeFixture(t, root, testWorkspace+"/extra-manifests/"+id+".json", extraManifestFixture("global-configs", "mcp.json"))
	}
	s := &Scanner{Root: root, Build: KnownBuild}
	keys := map[string]bool{}
	for i := 0; i < 2; i++ {
		r := s.Scan()
		if r.Coverage != "degraded" || r.Artifacts > MaxArtifacts || count(r, "sensitive_manifest_match") != MaxArtifacts/2 || count(r, "global_config_manifest_match") != MaxArtifacts/2 {
			t.Fatalf("history type did not share budget: %+v", r)
		}
		for _, f := range r.Findings {
			keys[f.Key] = true
		}
	}
	if len(keys) != MaxArtifacts*2 || len(s.cache) > MaxArtifacts {
		t.Fatalf("extra history failed bounded rotation: %d keys, %d cached", len(keys), len(s.cache))
	}
}

func TestExtraCurrentReferenceBudgetRotatesWithOrdinaryReferences(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxWorkspaces; i++ {
		ws := fmt.Sprintf("%012x", i)
		for _, id := range []string{hashA, hashB} {
			writeFixture(t, root, ws+"/manifests/"+id+".json", manifestFixture(".git/objects/history"))
			writeFixture(t, root, ws+"/extra-manifests/"+id+".json", extraManifestFixture("global-configs", "mcp.json"))
		}
		writeFixture(t, root, ws+"/state.json", map[string]any{
			"workspaceKey": "/PRIVATE/customer-project", "workspacePath": "/PRIVATE/customer-project",
			"lastAcceptedManifestHash": hashA, "lastAcceptedExtraManifestHash": hashA,
			"activeUpload": map[string]any{"nextManifestHash": hashB, "nextExtraManifestHash": hashB, "attemptCount": 1},
		})
	}
	s := &Scanner{Root: root, Build: KnownBuild}
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		r := s.Scan()
		if r.Coverage != "degraded" || r.Artifacts > MaxArtifacts || len(s.cache) > MaxArtifacts {
			t.Fatalf("combined reference rotation exceeded budget: %+v", r)
		}
		for _, f := range r.Findings {
			seen[f.Kind]++
		}
	}
	for _, kind := range []string{"upload_acceptance_recorded", "global_config_upload_acceptance_recorded", "upload_attempt_recorded", "global_config_upload_attempt_recorded"} {
		if seen[kind] != MaxWorkspaces {
			t.Fatalf("current reference kind %q starved: %d/%d", kind, seen[kind], MaxWorkspaces)
		}
	}
}
