package laodi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const KnownBuild = "3.12.3.7463"
const MaxArtifactBytes int64 = 8 << 20
const MaxArtifacts = 256
const MaxWorkspaces = 128
const MaxScanBytes int64 = 32 << 20

var hashName = regexp.MustCompile(`^[a-f0-9]{64}$`)
var workspaceName = regexp.MustCompile(`^[a-f0-9]{12}$`)

type manifest struct {
	Schema            string
	WorkspaceKey      string
	Files             int
	HistoryFiles      int
	GlobalConfigFiles int
	Hash              string // Raw evidence bytes, NOT ZCode's path/size-only manifest identifier.
}

type manifestRef struct {
	ID    string
	Extra bool
}

func (ref manifestRef) directory() string {
	if ref.Extra {
		return "extra-manifests"
	}
	return "manifests"
}

type upload struct {
	NextManifestHash      string `json:"nextManifestHash"`
	NextExtraManifestHash string `json:"nextExtraManifestHash"`
	AttemptCount          int    `json:"attemptCount"`
}

type clientState struct {
	WorkspaceKey                  string  `json:"workspaceKey"`
	WorkspacePath                 string  `json:"workspacePath"`
	ActiveUpload                  *upload `json:"activeUpload"`
	PendingUpload                 *upload `json:"pendingUpload"`
	LatestPendingUpload           *upload `json:"latestPendingUpload"`
	LastAcceptedManifestHash      string  `json:"lastAcceptedManifestHash"`
	LastAcceptedExtraManifestHash string  `json:"lastAcceptedExtraManifestHash"`
}

type cachedArtifact struct {
	Size     int64
	Modified time.Time
	Identity os.FileInfo
	Manifest *manifest
	State    *clientState
	Hash     string
	Code     string
}

// Scanner retains at most MaxArtifacts parsed metadata, not source contents.
// A bounded metadata sweep avoids one descriptor per historic manifest.
type Scanner struct {
	Root            string
	Build           string
	App             string
	appStamp        time.Time
	appFiles        []os.FileInfo
	cache           map[string]cachedArtifact
	referenceCursor int
	workspaceCursor int
	historyOffsets  map[string]int
}

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func RootID(s string) string { return digest(s) }

func (s *Scanner) Scan() Report {
	refreshClientBuild(s)
	r := Report{SchemaVersion: SchemaVersion, Parser: ParserID, Coverage: "observing", Findings: []Finding{}, Diagnostics: []Diagnostic{}, CheckedAt: time.Now().UTC()}
	addDiag := func(code string) {
		for _, d := range r.Diagnostics {
			if d.Code == code {
				return
			}
		}
		r.Diagnostics = append(r.Diagnostics, Diagnostic{Code: code})
		r.Coverage = "degraded"
	}
	if !supportedSnapshotBuild(s.Build, s.App) {
		r.Coverage = "unsupported_build"
		r.Diagnostics = append(r.Diagnostics, Diagnostic{Code: unsupportedSnapshotBuildDiagnostic(s.Build)})
		return r
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.Coverage = "no_evidence_directory"
			return r
		}
		addDiag("evidence_directory_unreadable")
		return r
	}
	defer root.Close()
	if s.cache == nil {
		s.cache = make(map[string]cachedArtifact)
	}
	seen := map[string]bool{}
	defer func() {
		for k := range s.cache {
			if !seen[k] {
				delete(s.cache, k)
			}
		}
	}()
	entries, err := readDir(root, ".", MaxWorkspaces+1)
	if err != nil {
		addDiag("evidence_directory_unreadable")
		return r
	}
	if len(entries) > MaxWorkspaces {
		addDiag("workspace_budget_exceeded")
		entries = entries[:MaxWorkspaces]
	}
	type workspaceScan struct {
		name       string
		state      *clientState
		references []manifestRef
		history    []manifestRef
		historyPos int
		remaining  int
		seen       map[manifestRef]bool
		byID       map[manifestRef]*manifest
	}
	workspaces := []*workspaceScan{}
	// State in every admitted workspace is read before any manifest. Otherwise
	// one large historic directory can consume the budget before a later state.
	for _, entry := range entries {
		ws := entry.Name()
		if !entry.IsDir() || !workspaceName.MatchString(ws) {
			continue
		}
		w := &workspaceScan{name: ws, seen: map[manifestRef]bool{}, byID: map[manifestRef]*manifest{}}
		a := s.read(root, path.Join(ws, "state.json"), "state", seen, &r)
		if a.Code != "" {
			if a.Code != "missing" {
				addDiag(a.Code)
			}
		} else {
			w.state = a.State
		}
		addID := func(id string, extra bool) {
			ref := manifestRef{ID: id, Extra: extra}
			if hashName.MatchString(id) && !w.seen[ref] {
				w.seen[ref] = true
				w.references = append(w.references, ref)
			}
		}
		if state := w.state; state != nil {
			addID(state.LastAcceptedManifestHash, false)
			addID(state.LastAcceptedExtraManifestHash, true)
			active := state.ActiveUpload
			if active == nil {
				active = state.PendingUpload
			}
			if active != nil {
				addID(active.NextManifestHash, false)
				addID(active.NextExtraManifestHash, true)
			}
			if state.LatestPendingUpload != nil {
				addID(state.LatestPendingUpload.NextManifestHash, false)
				addID(state.LatestPendingUpload.NextExtraManifestHash, true)
			}
		}
		workspaces = append(workspaces, w)
	}
	process := func(w *workspaceScan, ref manifestRef) {
		rel := path.Join(w.name, ref.directory(), ref.ID+".json")
		a := s.read(root, rel, ref.directory(), seen, &r)
		if a.Code != "" {
			prefix := "manifest_"
			if ref.Extra {
				prefix = "extra_manifest_"
			}
			addDiag(prefix + a.Code)
			return
		}
		m := a.Manifest
		if !ref.Extra && w.state != nil && m.WorkspaceKey != w.state.WorkspaceKey {
			addDiag("workspace_identity_mismatch")
			return
		}
		w.byID[ref] = m
		if ref.Extra && m.GlobalConfigFiles > 0 {
			r.Findings = append(r.Findings, Finding{Key: w.name + ":extra:" + ref.ID + ":manifest", Kind: "global_config_manifest_match", Counts: map[string]int{"global_config_paths": m.GlobalConfigFiles}, Evidence: rel, EvidenceHash: digest("extra:" + ref.ID + ":manifest"), Unknowns: []string{"configuration_contents", "upload_completion", "remote_retention", "network_payload"}})
		}
		if m.HistoryFiles > 0 {
			r.Findings = append(r.Findings, Finding{Key: w.name + ":" + ref.ID + ":history", Kind: "sensitive_manifest_match", Counts: map[string]int{"git_history_paths": m.HistoryFiles}, Evidence: rel, EvidenceHash: digest(ref.ID + ":history"), Unknowns: []string{"upload_completion", "remote_retention", "network_payload"}})
		} else if !ref.Extra && m.Files > 0 {
			r.Findings = append(r.Findings, Finding{Key: w.name + ":" + ref.ID + ":manifest", Kind: "workspace_snapshot_manifest", Counts: map[string]int{"workspace_files": m.Files}, Evidence: rel, EvidenceHash: digest(ref.ID + ":manifest"), Unknowns: []string{"workspace_contents", "user_authorization", "upload_completion", "remote_retention", "network_payload"}})
		}
	}
	// Current references take precedence across all workspaces. Rotate the
	// starting reference if even the current queue cannot fit in this scan.
	type reference struct {
		workspace *workspaceScan
		ref       manifestRef
	}
	references := []reference{}
	for position := 0; position < 6; position++ {
		for _, w := range workspaces {
			if position < len(w.references) {
				references = append(references, reference{w, w.references[position]})
			}
		}
	}
	if len(references) > 0 {
		start := s.referenceCursor % len(references)
		processed := 0
		for processed < len(references) && r.Artifacts < MaxArtifacts {
			ref := references[(start+processed)%len(references)]
			process(ref.workspace, ref.ref)
			processed++
		}
		s.referenceCursor = (start + processed) % len(references)
		if processed < len(references) {
			addDiag("artifact_budget_exceeded")
		}
	}
	// Historical discovery is deliberately bounded. Directory entries beyond
	// the first page are not guaranteed coverage, and explicitly degrade it.
	// Within the admitted page both workspaces and manifests rotate, with no
	// retained directory descriptors or unbounded traversal to seek an offset.
	if s.historyOffsets == nil {
		s.historyOffsets = map[string]int{}
	}
	liveWorkspaces := map[string]bool{}
	for _, w := range workspaces {
		liveWorkspaces[w.name] = true
		// Interleave both bounded directory pages so a large ordinary history
		// cannot starve extra manifests within the same workspace.
		pages := [2][]manifestRef{}
		for i, extra := range []bool{false, true} {
			ref := manifestRef{Extra: extra}
			prefix := "manifest_"
			if extra {
				prefix = "extra_manifest_"
			}
			files, e := readDir(root, path.Join(w.name, ref.directory()), MaxArtifacts+1)
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				addDiag(prefix + "directory_unreadable")
			}
			if len(files) > MaxArtifacts {
				addDiag(prefix + "budget_exceeded")
				files = files[:MaxArtifacts]
			}
			for _, f := range files {
				ref := manifestRef{ID: strings.TrimSuffix(f.Name(), ".json"), Extra: extra}
				if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") && hashName.MatchString(ref.ID) && !w.seen[ref] {
					pages[i] = append(pages[i], ref)
				}
			}
		}
		for position := 0; position < len(pages[0]) || position < len(pages[1]); position++ {
			for _, page := range pages {
				if position < len(page) {
					w.history = append(w.history, page[position])
				}
			}
		}
		w.remaining = len(w.history)
		if w.remaining > 0 {
			w.historyPos = s.historyOffsets[w.name] % w.remaining
		}
	}
	for ws := range s.historyOffsets {
		if !liveWorkspaces[ws] {
			delete(s.historyOffsets, ws)
		}
	}
	if len(workspaces) > 0 {
		start := s.workspaceCursor % len(workspaces)
		s.workspaceCursor = (start + 1) % len(workspaces)
		for {
			remaining := false
			for i := range workspaces {
				w := workspaces[(start+i)%len(workspaces)]
				if w.remaining == 0 {
					continue
				}
				remaining = true
				if r.Artifacts >= MaxArtifacts {
					addDiag("artifact_budget_exceeded")
					break
				}
				process(w, w.history[w.historyPos])
				w.historyPos = (w.historyPos + 1) % len(w.history)
				s.historyOffsets[w.name] = w.historyPos
				w.remaining--
			}
			if !remaining || r.Artifacts >= MaxArtifacts {
				break
			}
		}
		for _, w := range workspaces {
			if w.remaining > 0 {
				addDiag("artifact_budget_exceeded")
				break
			}
		}
	}
	for _, w := range workspaces {
		state, byID, ws := w.state, w.byID, w.name
		stateRel := path.Join(ws, "state.json")
		if state == nil {
			continue
		}
		if m := byID[manifestRef{ID: state.LastAcceptedManifestHash}]; m != nil && m.Files > 0 {
			id := state.LastAcceptedManifestHash
			if m.HistoryFiles > 0 {
				r.Findings = append(r.Findings, Finding{Key: ws + ":" + id + ":accepted", Kind: "upload_acceptance_recorded", Counts: map[string]int{"git_history_paths": m.HistoryFiles}, Evidence: stateRel, EvidenceHash: digest(id + ":accepted"), Unknowns: []string{"remote_retention", "remote_use", "server_body_not_validated", "manifest_hash_is_not_content_hash"}})
			} else {
				r.Findings = append(r.Findings, Finding{Key: ws + ":" + id + ":accepted", Kind: "workspace_snapshot_upload_acceptance_recorded", Counts: map[string]int{"workspace_files": m.Files}, Evidence: stateRel, EvidenceHash: digest(id + ":accepted"), Unknowns: []string{"workspace_contents", "user_authorization", "network_payload", "remote_retention", "remote_use", "server_body_not_validated", "manifest_hash_is_not_content_hash"}})
			}
		}
		if m := byID[manifestRef{ID: state.LastAcceptedExtraManifestHash, Extra: true}]; m != nil && m.GlobalConfigFiles > 0 {
			id := state.LastAcceptedExtraManifestHash
			r.Findings = append(r.Findings, Finding{Key: ws + ":extra:" + id + ":accepted", Kind: "global_config_upload_acceptance_recorded", Counts: map[string]int{"global_config_paths": m.GlobalConfigFiles}, Evidence: stateRel, EvidenceHash: digest("extra:" + id + ":accepted"), Unknowns: []string{"configuration_contents", "remote_retention", "remote_use", "server_body_not_validated"}})
		}
		active := state.ActiveUpload
		if active == nil {
			active = state.PendingUpload
		}
		attemptedExtra := map[string]bool{}
		for _, u := range []*upload{active, state.LatestPendingUpload} {
			if u == nil || u.AttemptCount <= 0 {
				continue
			}
			id := u.NextExtraManifestHash
			if m := byID[manifestRef{ID: id, Extra: true}]; m != nil && m.GlobalConfigFiles > 0 && !attemptedExtra[id] {
				attemptedExtra[id] = true
				r.Findings = append(r.Findings, Finding{Key: ws + ":extra:" + id + ":attempt", Kind: "global_config_upload_attempt_recorded", Counts: map[string]int{"global_config_paths": m.GlobalConfigFiles}, Evidence: stateRel, EvidenceHash: digest("extra:" + id + ":attempt"), Unknowns: []string{"configuration_contents", "credential_obtained", "network_upload_started", "upload_completion", "remote_retention"}})
			}
			m := byID[manifestRef{ID: u.NextManifestHash}]
			if m == nil || m.Files == 0 {
				continue
			}
			id = u.NextManifestHash
			key := ws + ":" + id + ":attempt"
			duplicate := false
			for _, f := range r.Findings {
				if f.Key == key {
					duplicate = true
				}
			}
			if duplicate {
				continue
			}
			if m.HistoryFiles > 0 {
				r.Findings = append(r.Findings, Finding{Key: key, Kind: "upload_attempt_recorded", Counts: map[string]int{"attempts_recorded": u.AttemptCount, "git_history_paths": m.HistoryFiles}, Evidence: stateRel, EvidenceHash: digest(id + ":attempt"), Unknowns: []string{"credential_obtained", "network_upload_started", "upload_completion", "remote_retention"}})
			} else {
				r.Findings = append(r.Findings, Finding{Key: key, Kind: "workspace_snapshot_upload_attempt_recorded", Counts: map[string]int{"attempts_recorded": u.AttemptCount, "workspace_files": m.Files}, Evidence: stateRel, EvidenceHash: digest(id + ":attempt"), Unknowns: []string{"workspace_contents", "user_authorization", "network_payload", "credential_obtained", "network_upload_started", "upload_completion", "remote_retention"}})
			}
		}
	}
	sort.Slice(r.Findings, func(i, j int) bool { return r.Findings[i].Key < r.Findings[j].Key })
	return r
}

func readDir(root *os.Root, rel string, limit int) ([]os.DirEntry, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("not directory")
	}
	if err := checkScannerPath(root, rel, info); err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := checkScannerHandle(f, true); err != nil {
		return nil, err
	}
	items, err := f.ReadDir(limit)
	if err == io.EOF {
		err = nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	return items, err
}

func (s *Scanner) read(root *os.Root, rel string, artifactType string, seen map[string]bool, r *Report) cachedArtifact {
	if r.Artifacts >= MaxArtifacts {
		return cachedArtifact{Code: "budget_exceeded"}
	}
	parent, err := root.Lstat(path.Dir(rel))
	if errors.Is(err, os.ErrNotExist) {
		return cachedArtifact{Code: "missing"}
	}
	if err != nil || !parent.IsDir() {
		return cachedArtifact{Code: "unsupported_directory"}
	}
	info, err := root.Lstat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return cachedArtifact{Code: "missing"}
	}
	if err != nil {
		return cachedArtifact{Code: "unreadable"}
	}
	if !info.Mode().IsRegular() {
		return cachedArtifact{Code: "unsupported_file_type"}
	}
	if err := checkScannerPath(root, rel, info); err != nil {
		return cachedArtifact{Code: "unsupported_file_type"}
	}
	if info.Size() > MaxArtifactBytes {
		return cachedArtifact{Code: "size_limit"}
	}
	seen[rel] = true
	r.Artifacts++
	if c, ok := s.cache[rel]; ok && os.SameFile(c.Identity, info) && c.Size == info.Size() && c.Modified.Equal(info.ModTime()) {
		return c
	}
	if r.BytesRead+info.Size() > MaxScanBytes {
		return cachedArtifact{Code: "scan_byte_budget_exceeded"}
	}
	r.BytesRead += info.Size()
	c := cachedArtifact{Size: info.Size(), Modified: info.ModTime(), Identity: info}
	f, err := root.Open(rel)
	if err != nil {
		return cachedArtifact{Code: "unreadable"}
	}
	defer f.Close()
	if err := checkScannerHandle(f, false); err != nil {
		return cachedArtifact{Code: "unsupported_file_type"}
	}
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return cachedArtifact{Code: "changed_during_read"}
	}
	h := sha256.New()
	limited := io.LimitReader(f, MaxArtifactBytes+1)
	dec := json.NewDecoder(io.TeeReader(limited, h))
	switch artifactType {
	case "manifests":
		c.Manifest, err = parseManifest(dec)
	case "extra-manifests":
		c.Manifest, err = parseExtraManifest(dec)
	default:
		var st clientState
		err = dec.Decode(&st)
		if err == nil && (st.WorkspaceKey == "" || st.WorkspacePath == "") {
			err = errors.New("unrecognized state")
		}
		c.State = &st
	}
	if err == nil {
		var extra any
		if e := dec.Decode(&extra); e != io.EOF {
			err = errors.New("trailing or incomplete data")
		}
	}
	final, e := f.Stat()
	if e != nil || final.Size() != info.Size() || !final.ModTime().Equal(info.ModTime()) {
		return cachedArtifact{Code: "changed_during_read"}
	}
	if err != nil {
		c.Code = "invalid_or_incomplete"
	} else {
		c.Hash = hex.EncodeToString(h.Sum(nil))
		if c.Manifest != nil {
			c.Manifest.Hash = c.Hash
		}
	}
	s.cache[rel] = c
	return c
}

func parseManifest(dec *json.Decoder) (*manifest, error) {
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("manifest object expected")
	}
	m := &manifest{}
	haveFiles := false
	keys := map[string]bool{}
	for dec.More() {
		t, e := dec.Token()
		if e != nil {
			return nil, e
		}
		key, ok := t.(string)
		if !ok || keys[key] {
			return nil, errors.New("invalid or duplicate field")
		}
		keys[key] = true
		switch key {
		case "schema":
			err = dec.Decode(&m.Schema)
		case "workspaceKey":
			err = dec.Decode(&m.WorkspaceKey)
		case "files":
			haveFiles = true
			t, err = dec.Token()
			if err != nil || t != json.Delim('[') {
				return nil, errors.New("files array expected")
			}
			for dec.More() {
				var f struct {
					Path      string `json:"path"`
					SizeBytes int64  `json:"sizeBytes"`
				}
				if e = dec.Decode(&f); e != nil {
					return nil, e
				}
				m.Files++
				if m.Files > 100000 || len(f.Path) > 8192 || f.Path == "" || f.SizeBytes < 0 {
					return nil, errors.New("manifest entry limit")
				}
				if isHistoryObjectPath(f.Path) {
					m.HistoryFiles++
				}
			}
			_, err = dec.Token()
		default:
			err = skipJSON(dec, 0)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	if m.Schema != "repo_snapshot_manifest/v2" || m.WorkspaceKey == "" || !haveFiles {
		return nil, errors.New("unsupported manifest schema")
	}
	return m, nil
}

// Extra manifests classify configuration metadata independently of workspace
// files. Paths, sources and content hashes are never opened or retained.
func parseExtraManifest(dec *json.Decoder) (*manifest, error) {
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("extra manifest object expected")
	}
	m := &manifest{}
	haveGroups := false
	keys := map[string]bool{}
	for dec.More() {
		t, e := dec.Token()
		if e != nil {
			return nil, e
		}
		key, ok := t.(string)
		if !ok || keys[key] {
			return nil, errors.New("invalid or duplicate field")
		}
		keys[key] = true
		switch key {
		case "schema":
			err = dec.Decode(&m.Schema)
		case "groups":
			haveGroups = true
			t, err = dec.Token()
			if err != nil || t != json.Delim('[') {
				return nil, errors.New("groups array expected")
			}
			groups := 0
			for dec.More() {
				groups++
				if groups > 256 {
					return nil, errors.New("extra group limit")
				}
				group, files, e := parseExtraGroup(dec)
				if e != nil {
					return nil, e
				}
				m.Files += files
				if m.Files > 100000 {
					return nil, errors.New("extra entry limit")
				}
				if group == "global-configs" {
					m.GlobalConfigFiles += files
				}
			}
			_, err = dec.Token()
		default:
			err = skipJSON(dec, 0)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	if m.Schema != "repo_snapshot_extra_manifest/v1" || !haveGroups {
		return nil, errors.New("unsupported extra manifest schema")
	}
	return m, nil
}

func parseExtraGroup(dec *json.Decoder) (string, int, error) {
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return "", 0, errors.New("extra group object expected")
	}
	group, files, haveFiles := "", 0, false
	keys := map[string]bool{}
	for dec.More() {
		t, e := dec.Token()
		if e != nil {
			return "", 0, e
		}
		key, ok := t.(string)
		if !ok || keys[key] {
			return "", 0, errors.New("invalid or duplicate group field")
		}
		keys[key] = true
		switch key {
		case "groupId":
			err = dec.Decode(&group)
		case "files":
			haveFiles = true
			t, err = dec.Token()
			if err != nil || t != json.Delim('[') {
				return "", 0, errors.New("extra files array expected")
			}
			for dec.More() {
				var f struct {
					Path        string `json:"path"`
					SizeBytes   *int64 `json:"sizeBytes"`
					ContentHash string `json:"contentHash"`
				}
				if e := dec.Decode(&f); e != nil {
					return "", 0, e
				}
				files++
				if files > 100000 || f.Path == "" || len(f.Path) > 8192 || f.SizeBytes == nil || *f.SizeBytes < 0 || !hashName.MatchString(f.ContentHash) {
					return "", 0, errors.New("invalid or excessive extra entry")
				}
			}
			_, err = dec.Token()
		default:
			err = skipJSON(dec, 0)
		}
		if err != nil {
			return "", 0, err
		}
	}
	if _, err = dec.Token(); err != nil {
		return "", 0, err
	}
	if group == "" || len(group) > 256 || !haveFiles {
		return "", 0, errors.New("invalid extra group")
	}
	return group, files, nil
}

// Paths are classification input only; they are never opened. A gitfile,
// HEAD, config or reflog is not evidence that historical objects were packed.
func isHistoryObjectPath(name string) bool {
	segments := strings.Split(path.Clean(strings.ReplaceAll(name, "\\", "/")), "/")
	for i, segment := range segments {
		if segment != ".git" {
			continue
		}
		if i+2 < len(segments) && segments[i+1] == "objects" {
			return true
		}
		if i+3 < len(segments) && segments[i+1] == "lfs" && segments[i+2] == "objects" {
			return true
		}
	}
	return false
}

func skipJSON(dec *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("nesting limit")
	}
	t, e := dec.Token()
	if e != nil {
		return e
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if d == '{' {
		for dec.More() {
			if _, e = dec.Token(); e != nil {
				return e
			}
			if e = skipJSON(dec, depth+1); e != nil {
				return e
			}
		}
	} else if d == '[' {
		for dec.More() {
			if e = skipJSON(dec, depth+1); e != nil {
				return e
			}
		}
	} else {
		return errors.New("unexpected delimiter")
	}
	_, e = dec.Token()
	return e
}
