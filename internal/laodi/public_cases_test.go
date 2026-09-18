package laodi

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These fixtures reproduce documented artifact shapes, not real client uploads.
// Large sizes are JSON numbers only; no repository, archive, or secret exists.
// File lists are reduced examples; aggregate size metadata retains case scale.
const publicCaseWorkspaceKey = "/synthetic/public-case-project"

func publicCaseFile(path string, size int64) map[string]any {
	return map[string]any{"path": path, "sizeBytes": size}
}

func publicCaseManifest(t *testing.T, root, id string, files ...map[string]any) {
	t.Helper()
	writeFixture(t, root, testWorkspace+"/manifests/"+id+".json", map[string]any{
		"schema": "repo_snapshot_manifest/v2", "workspaceKey": publicCaseWorkspaceKey, "files": files,
	})
}

func publicCaseState(t *testing.T, root string, fields map[string]any) {
	t.Helper()
	state := map[string]any{"workspaceKey": publicCaseWorkspaceKey, "workspacePath": publicCaseWorkspaceKey}
	for key, value := range fields {
		state[key] = value
	}
	writeFixture(t, root, testWorkspace+"/state.json", state)
}

func publicCaseKinds(t *testing.T, r Report, expected map[string]int) {
	t.Helper()
	if r.Coverage != "observing" {
		t.Fatalf("synthetic artifacts were not fully interpreted: %+v", r)
	}
	wantTotal := 0
	for kind, want := range expected {
		wantTotal += want
		if got := count(r, kind); got != want {
			t.Fatalf("%s: got %d, want %d; report=%+v", kind, got, want, r)
		}
	}
	if len(r.Findings) != wantTotal {
		t.Fatalf("unexpected additional evidence classification: %+v", r)
	}
}

func TestPublicCaseFerstarGitAndLFSPendingIsNotSending(t *testing.T) {
	root := t.TempDir()
	publicCaseManifest(t, root, hashA,
		publicCaseFile(".git/objects/pack/synthetic.pack", 102_200_000),
		publicCaseFile(".git/lfs/objects/aa/bb/synthetic", 196_100_000),
		publicCaseFile(".git/logs/HEAD", 600_000),
		publicCaseFile("src/main.ts", 100),
	)
	publicCaseState(t, root, map[string]any{
		"failureCount":       564,
		"lastCompressedSize": map[string]any{"manifestHash": hashA, "encryptedSizeBytes": 313_000_000},
		"pendingUpload": map[string]any{
			"nextManifestHash": hashA, "attemptCount": 0,
			"encryptedArtifactPath": "pending/synthetic.enc",
		},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	publicCaseKinds(t, r, map[string]int{"sensitive_manifest_match": 1})
	if r.Findings[0].Counts["git_history_paths"] != 2 {
		t.Fatalf("Git objects and LFS should count, but reflog metadata should not: %+v", r)
	}
	if r.BytesRead >= 16<<10 {
		t.Fatalf("scanner read more than the small metadata fixture: %d", r.BytesRead)
	}
}

func TestPublicCaseLargeManifestWithinBudget(t *testing.T) {
	root := t.TempDir()
	const fileCount = 42411
	files := make([]map[string]any, 0, fileCount)
	files = append(files, publicCaseFile(".git/objects/pack/synthetic.pack", 100), publicCaseFile(".git/lfs/objects/aa/bb/synthetic", 100))
	for i := len(files); i < fileCount; i++ {
		files = append(files, publicCaseFile("src/file"+strconv.Itoa(i)+".txt", 100))
	}
	publicCaseManifest(t, root, hashA, files...)
	publicCaseState(t, root, map[string]any{
		"failureCount":  564,
		"pendingUpload": map[string]any{"nextManifestHash": hashA, "attemptCount": 0, "encryptedArtifactPath": "pending/nonexistent.enc"},
	})
	s := &Scanner{Root: root, Build: KnownBuild}
	first := s.Scan()
	publicCaseKinds(t, first, map[string]int{"sensitive_manifest_match": 1})
	if first.Findings[0].Counts["git_history_paths"] != 2 || first.BytesRead <= 0 || first.BytesRead >= MaxArtifactBytes || first.BytesRead > MaxScanBytes || first.Artifacts != 2 {
		t.Fatalf("large metadata fixture exceeded coverage or budgets: %+v", first)
	}
	m := s.cache[testWorkspace+"/manifests/"+hashA+".json"].Manifest
	if m == nil || m.Files != fileCount {
		t.Fatalf("large manifest entries were not completely counted: %+v", m)
	}
	second := s.Scan()
	publicCaseKinds(t, second, map[string]int{"sensitive_manifest_match": 1})
	if second.BytesRead != 0 || second.Artifacts != 2 {
		t.Fatalf("unchanged large manifest was reread: %+v", second)
	}
}

func TestPublicCaseWindowsLargePackAndExtraConfigurations(t *testing.T) {
	root := t.TempDir()
	publicCaseManifest(t, root, hashA,
		publicCaseFile(".git/objects/pack/synthetic.pack", 257_535_221),
		publicCaseFile("README.md", 100),
	)
	writeFixture(t, root, testWorkspace+"/extra-manifests/"+hashB+".json",
		extraManifestFixture("global-configs", "settings.behavior.json", "skills.json"))
	s := &Scanner{Root: root, Build: KnownBuild}
	expected := map[string]int{"sensitive_manifest_match": 1, "global_config_manifest_match": 1}
	for phase := 0; phase < 3; phase++ {
		attempts := 0
		if phase > 0 {
			attempts = 1
		}
		fields := map[string]any{
			"failureCount":  1,
			"pendingUpload": map[string]any{"nextManifestHash": hashA, "nextExtraManifestHash": hashB, "attemptCount": attempts},
		}
		if phase > 0 {
			expected["upload_attempt_recorded"] = 1
			expected["global_config_upload_attempt_recorded"] = 1
		}
		if phase == 2 {
			fields["lastAcceptedManifestHash"] = hashA
			fields["lastAcceptedExtraManifestHash"] = hashB
			expected["upload_acceptance_recorded"] = 1
			expected["global_config_upload_acceptance_recorded"] = 1
		}
		publicCaseState(t, root, fields)
		publicCaseKinds(t, s.Scan(), expected)
	}
}

func TestPublicCaseVonngOrdinaryWorkspaceAccepted(t *testing.T) {
	root := t.TempDir()
	publicCaseManifest(t, root, hashA, publicCaseFile("docs/reference.html", 1024), publicCaseFile("README.md", 100))
	publicCaseState(t, root, map[string]any{
		"activeUpload":             map[string]any{"nextManifestHash": hashA, "attemptCount": 1},
		"lastAcceptedManifestHash": hashA,
		"lastCompressedSize":       map[string]any{"manifestHash": hashA, "workspaceSizeBytes": 405_000_000, "encryptedSizeBytes": 7_400_000},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	publicCaseKinds(t, r, map[string]int{
		"workspace_snapshot_manifest": 1, "workspace_snapshot_upload_attempt_recorded": 1,
		"workspace_snapshot_upload_acceptance_recorded": 1,
	})
	for _, f := range r.Findings {
		if f.Counts["workspace_files"] != 2 || f.Counts["git_history_paths"] != 0 {
			t.Fatalf("ordinary workspace classified as Git history: %+v", f)
		}
		for _, unknown := range []string{"workspace_contents", "user_authorization", "network_payload"} {
			found := false
			for _, actual := range f.Unknowns {
				found = found || actual == unknown
			}
			if !found {
				t.Fatalf("ordinary snapshot omits %s limitation: %+v", unknown, f)
			}
		}
	}
}

func TestPublicCaseOversizedPayloadFailuresDoNotProveUploads(t *testing.T) {
	root := t.TempDir()
	publicCaseManifest(t, root, hashA, publicCaseFile("docs/page.html", 1024), publicCaseFile("docs/index.html", 100))
	publicCaseState(t, root, map[string]any{
		"failureCount":       102,
		"lastCompressedSize": map[string]any{"manifestHash": hashA, "workspaceSizeBytes": 1_530_000_000, "encryptedSizeBytes": (1 << 30) + 32784},
		"pendingUpload": map[string]any{
			"nextManifestHash": hashA, "attemptCount": 0,
		},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	publicCaseKinds(t, r, map[string]int{"workspace_snapshot_manifest": 1})
	if r.BytesRead >= 16<<10 {
		t.Fatalf("metadata sizes triggered content reads: %d", r.BytesRead)
	}
}

func TestPublicCaseAcceptedOrdinarySnapshotDoesNotAcceptNewGitPending(t *testing.T) {
	root := t.TempDir()
	publicCaseManifest(t, root, hashA, publicCaseFile("src/main.go", 100))
	publicCaseManifest(t, root, hashB, publicCaseFile(".git/objects/pack/synthetic.pack", 1000))
	publicCaseState(t, root, map[string]any{
		"lastAcceptedManifestHash": hashA,
		"activeUpload":             map[string]any{"nextManifestHash": hashB, "attemptCount": 1},
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	publicCaseKinds(t, r, map[string]int{
		"workspace_snapshot_manifest": 1, "workspace_snapshot_upload_acceptance_recorded": 1,
		"sensitive_manifest_match": 1, "upload_attempt_recorded": 1,
	})
	for _, f := range r.Findings {
		if f.Kind == "workspace_snapshot_upload_acceptance_recorded" && f.Key != testWorkspace+":"+hashA+":accepted" {
			t.Fatalf("accepted state matched a different manifest: %+v", f)
		}
	}
}

func TestPublicCaseDisabledIndexingDoesNotHideSnapshotEvidence(t *testing.T) {
	data := t.TempDir()
	root := filepath.Join(data, "checkpoints")
	// Settings are a separate artifact, not an authoritative upload receipt.
	// The scanner must inspect evidence regardless of this switch.
	writeFixture(t, data, "setting.json", map[string]any{"repoSnapshotIndexingEnabled": false})
	publicCaseManifest(t, root, hashA, publicCaseFile(".git/objects/aa/synthetic", 100))
	publicCaseState(t, root, map[string]any{
		"lastAcceptedManifestHash": hashA,
	})
	r := (&Scanner{Root: root, Build: KnownBuild}).Scan()
	publicCaseKinds(t, r, map[string]int{"sensitive_manifest_match": 1, "upload_acceptance_recorded": 1})
}

func TestPublicCaseAcceptedEvidenceSurvivesPendingCleanup(t *testing.T) {
	for _, history := range []bool{false, true} {
		name, file, manifestKind, attemptKind, acceptedKind := "ordinary", "src/main.go", "workspace_snapshot_manifest", "workspace_snapshot_upload_attempt_recorded", "workspace_snapshot_upload_acceptance_recorded"
		if history {
			name, file, manifestKind, attemptKind, acceptedKind = "git", ".git/objects/aa/synthetic", "sensitive_manifest_match", "upload_attempt_recorded", "upload_acceptance_recorded"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			publicCaseManifest(t, root, hashA, publicCaseFile(file, 100))
			publicCaseState(t, root, map[string]any{
				"pendingUpload":            map[string]any{"nextManifestHash": hashA, "attemptCount": 1},
				"lastAcceptedManifestHash": hashA,
			})
			s := &Scanner{Root: root, Build: KnownBuild}
			publicCaseKinds(t, s.Scan(), map[string]int{manifestKind: 1, attemptKind: 1, acceptedKind: 1})
			publicCaseState(t, root, map[string]any{"lastAcceptedManifestHash": hashA})
			r := s.Scan()
			publicCaseKinds(t, r, map[string]int{manifestKind: 1, acceptedKind: 1})
			for _, f := range r.Findings {
				if f.Kind == acceptedKind && filepath.Base(f.Evidence) != "state.json" {
					t.Fatalf("acceptance should be attributed to state: %+v", f)
				}
			}
		})
	}
}

func TestPublicCaseOrdinaryUploadAliasesAndLatestPending(t *testing.T) {
	root := t.TempDir()
	publicCaseManifest(t, root, hashA, publicCaseFile("src/a.go", 100))
	publicCaseManifest(t, root, hashB, publicCaseFile("src/b.go", 100))
	s := &Scanner{Root: root, Build: KnownBuild}
	slot := func(id string, attempts int) map[string]any {
		return map[string]any{"nextManifestHash": id, "attemptCount": attempts}
	}
	for _, tc := range []struct {
		name   string
		fields map[string]any
		ids    []string
	}{
		{"active_alias_wins", map[string]any{"activeUpload": slot(hashA, 1), "pendingUpload": slot(hashB, 1), "latestPendingUpload": slot(hashA, 1)}, []string{hashA}},
		{"latest_independent", map[string]any{"activeUpload": slot(hashA, 1), "pendingUpload": slot(hashA, 1), "latestPendingUpload": slot(hashB, 2)}, []string{hashA, hashB}},
		{"pending_fallback", map[string]any{"pendingUpload": slot(hashB, 1)}, []string{hashB}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publicCaseState(t, root, tc.fields)
			r := s.Scan()
			publicCaseKinds(t, r, map[string]int{"workspace_snapshot_manifest": 2, "workspace_snapshot_upload_attempt_recorded": len(tc.ids)})
			for _, id := range tc.ids {
				found := false
				for _, f := range r.Findings {
					found = found || f.Kind == "workspace_snapshot_upload_attempt_recorded" && strings.Contains(f.Key, ":"+id+":")
				}
				if !found {
					t.Fatalf("independent current upload reference was missed: %+v", r)
				}
			}
		})
	}
}
