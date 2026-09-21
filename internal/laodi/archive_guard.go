package laodi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const archiveGuardLimit = 1024
const archiveGuardReceiptLimit = 1 << 20

var archiveHash = regexp.MustCompile(`^[0-9a-f]{12}$`)
var archiveAccount = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var archiveACLLine = regexp.MustCompile(`^\s*([0-9]+): (.+)$`)

// ArchiveGuardPlan describes only the fixed, user-owned client cache. It does
// not authorize a client version: callers must verify that separately.
type ArchiveGuardPlan struct {
	Home               string `json:"home"`
	ClientApp          string `json:"-"`
	Root               string `json:"root"`
	ExistingWorkspaces int    `json:"existing_workspaces"`
	ExistingArtifacts  int    `json:"existing_artifacts"`
	CreatesRoot        bool   `json:"creates_root"`
	runner             archiveGuardRunner
}

type ArchiveGuardStatus struct {
	Enabled             bool     `json:"enabled"`
	Healthy             bool     `json:"healthy"`
	Changed             bool     `json:"changed"`
	RecoveryNeeded      bool     `json:"recovery_needed"`
	ProtectedWorkspaces int      `json:"protected_workspaces"`
	ProtectedArtifacts  int      `json:"protected_artifacts"`
	Problems            []string `json:"problems,omitempty"`
	Limitations         []string `json:"limitations"`
}

type ArchiveGuardResult = ArchiveGuardStatus
type archiveGuardRunner func(context.Context, string, ...string) ([]byte, error)

type archiveIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint64 `json:"uid"`
	Mode   uint32 `json:"mode"`
}

type archiveNode struct {
	Path     string          `json:"path"`
	Kind     string          `json:"kind"`
	Identity archiveIdentity `json:"identity"`
}

type archiveChange struct {
	archiveNode
	Rule   string   `json:"rule"`
	Before []string `json:"before"`
}

type archiveReceipt struct {
	Version   int             `json:"version"`
	Home      string          `json:"home"`
	Principal string          `json:"principal"`
	Phase     string          `json:"phase"`
	Changes   []archiveChange `json:"changes"`
}

func archiveStatus() ArchiveGuardStatus {
	return ArchiveGuardStatus{Limitations: []string{
		"Fixed-build cache compatibility control; not a whole-process security boundary.",
		"The owner can remove ACLs; deliberately imported directories, symlinks, alternate cache roots and already-open file descriptors are not prevented.",
		"Rules affect this cache for all processes using the same user account; repository Git data is not modified.",
	}}
}

func archivePrincipal() (string, error) {
	if runtime.GOOS != "darwin" || os.Getuid() <= 0 || os.Geteuid() != os.Getuid() {
		return "", errors.New("archive guard requires macOS and a normal, unprivileged user")
	}
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil || !archiveAccount.MatchString(account.Username) {
		return "", errors.New("cannot resolve a safe current-user ACL principal")
	}
	return "user:" + account.Username, nil
}

// archivePath rejects links in the home and every cache ancestor. macOS /var
// itself is a platform symlink, so callers should use the canonical home path.
func archivePath(path string, allowMissing bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n\t") {
		return errors.New("archive guard paths must be clean absolute paths")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive guard path contains a link or a non-directory")
		}
	}
	return nil
}

func archiveNumber(info os.FileInfo, field string) uint64 {
	stat := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !stat.IsValid() || stat.Kind() != reflect.Struct {
		return 0
	}
	v := stat.FieldByName(field)
	if !v.IsValid() {
		return 0
	}
	if v.CanUint() {
		return v.Uint()
	}
	if v.CanInt() {
		return uint64(v.Int())
	}
	return 0
}

func archiveMetadata(path, relative, kind string) (archiveNode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return archiveNode{}, err
	}
	if (!info.IsDir() && !info.Mode().IsRegular()) || info.Mode()&os.ModeSymlink != 0 ||
		(info.Mode().IsRegular() && archiveNumber(info, "Nlink") != 1) || archiveNumber(info, "Uid") != uint64(os.Getuid()) {
		return archiveNode{}, errors.New("archive cache contains a link, special file, or foreign-owned entry")
	}
	return archiveNode{Path: relative, Kind: kind, Identity: archiveIdentity{
		Device: archiveNumber(info, "Dev"), Inode: archiveNumber(info, "Ino"),
		UID: archiveNumber(info, "Uid"), Mode: uint32(info.Mode()),
	}}, nil
}

func archiveDirectory(node archiveNode) bool { return os.FileMode(node.Identity.Mode).IsDir() }

func archiveSingleName(name string) bool {
	return name != "." && name != ".." && filepath.IsLocal(name) && filepath.Clean(name) == name &&
		filepath.Base(name) == name && !strings.ContainsAny(name, "\x00\r\n\t")
}

// archiveLayout reads directory entries and metadata only, never file content.
func archiveLayout(home string) ([]archiveNode, bool, error) {
	if err := archivePath(home, false); err != nil {
		return nil, false, err
	}
	parent := filepath.Join(home, ".zcode", "v2")
	if err := archivePath(parent, false); err != nil {
		return nil, false, err
	}
	for _, path := range []string{home, filepath.Join(home, ".zcode"), parent} {
		node, err := archiveMetadata(path, ".", "root")
		if err != nil || !archiveDirectory(node) || os.FileMode(node.Identity.Mode).Perm()&0022 != 0 {
			return nil, false, errors.New("home and cache ancestors must be real user-owned directories without group or other write access")
		}
	}
	if _, err := os.Lstat(filepath.Join(parent, "repo-snapshots")); !errors.Is(err, os.ErrNotExist) {
		return nil, false, errors.New("legacy repo-snapshots storage requires separate migration; refusing archive guard")
	}
	root := filepath.Join(parent, "checkpoints")
	first, err := archiveMetadata(root, ".", "root")
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil || !archiveDirectory(first) {
		return nil, false, errors.New("checkpoints must be a real user-owned directory")
	}
	nodes := []archiveNode{first}
	count := 1
	var visit func(string, int) error
	visit = func(relative string, depth int) error {
		if depth > 8 {
			return errors.New("archive cache exceeds metadata depth limit")
		}
		dir, err := os.Open(filepath.Join(root, relative))
		if err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(archiveGuardLimit + 1)
		dir.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			count++
			if count > archiveGuardLimit || strings.ContainsAny(entry.Name(), "\x00\r\n\t") {
				return errors.New("archive cache exceeds metadata bounds or contains an unsupported name")
			}
			rel := filepath.Join(relative, entry.Name())
			parts := strings.Split(rel, string(filepath.Separator))
			kind := "metadata"
			if !archiveHash.MatchString(parts[0]) {
				return errors.New("unexpected entry in checkpoints root")
			}
			if len(parts) == 1 {
				kind = "workspace"
			} else if parts[1] == "tmp" || parts[1] == "pending" {
				kind = "artifact-file"
				if parts[1] == "pending" {
					kind = "pending-file"
				}
			}
			node, err := archiveMetadata(filepath.Join(root, rel), rel, kind)
			if err != nil {
				return err
			}
			isDir := archiveDirectory(node)
			if len(parts) == 1 && !isDir {
				return errors.New("workspace cache must be a directory")
			}
			if len(parts) == 2 {
				knownDir := parts[1] == "tmp" || parts[1] == "pending" || parts[1] == "manifests" || parts[1] == "extra-manifests"
				if (knownDir && !isDir) || (isDir && !knownDir) {
					return errors.New("unsupported workspace cache layout")
				}
			}
			if isDir && (kind == "artifact-file" || kind == "pending-file") {
				node.Kind = "artifact-dir"
			}
			if node.Kind != "metadata" {
				nodes = append(nodes, node)
			}
			if isDir {
				if err := visit(rel, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(".", 0); err != nil {
		return nil, false, err
	}
	return nodes, false, nil
}

func planArchiveGuardDarwin(home string) (ArchiveGuardPlan, error) {
	if _, err := archivePrincipal(); err != nil {
		return ArchiveGuardPlan{}, err
	}
	nodes, missing, err := archiveLayout(home)
	if err != nil {
		return ArchiveGuardPlan{}, err
	}
	plan := ArchiveGuardPlan{Home: home, Root: filepath.Join(home, ".zcode", "v2", "checkpoints"), CreatesRoot: missing}
	for _, node := range nodes {
		if node.Kind == "workspace" {
			plan.ExistingWorkspaces++
		} else if strings.Contains(node.Kind, "file") || node.Kind == "artifact-dir" {
			plan.ExistingArtifacts++
		}
	}
	return plan, nil
}

func archiveRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil {
		// Do not copy client paths or command output to a public diagnostic.
		return nil, fmt.Errorf("archive ACL command failed: %w", err)
	}
	if output.Len() > 32<<10 {
		return nil, errors.New("archive ACL output exceeds metadata limit")
	}
	return output.Bytes(), nil
}

func archiveCommand(run archiveGuardRunner, name string, args ...string) ([]byte, error) {
	if run == nil {
		run = archiveRun
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return run(ctx, name, args...)
}

func archiveNormalizeACL(line string) (string, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 || len(fields) > 4 || strings.Join(fields, " ") != line {
		return "", errors.New("unsupported ACL syntax")
	}
	action := len(fields) - 2
	if fields[action] != "allow" && fields[action] != "deny" {
		return "", errors.New("unsupported ACL action")
	}
	if action == 2 && fields[1] != "inherited" {
		return "", errors.New("unsupported ACL inheritance syntax")
	}
	rights := strings.Split(fields[len(fields)-1], ",")
	for _, right := range rights {
		if !archiveAccount.MatchString(right) {
			return "", errors.New("unsupported ACL permission")
		}
	}
	sort.Strings(rights)
	fields[len(fields)-1] = strings.Join(rights, ",")
	return strings.Join(fields, " "), nil
}

func archiveReadACL(path string, run archiveGuardRunner) ([]string, error) {
	output, err := archiveCommand(run, "/bin/ls", "-lde", path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) < 1 || len(lines) > 33 {
		return nil, errors.New("archive ACL entry limit exceeded")
	}
	acl := []string{}
	for _, line := range lines[1:] {
		match := archiveACLLine.FindStringSubmatch(line)
		if match == nil || match[1] != strconv.Itoa(len(acl)) {
			return nil, errors.New("unexpected ACL metadata output")
		}
		normalized, err := archiveNormalizeACL(match[2])
		if err != nil {
			return nil, err
		}
		acl = append(acl, normalized)
	}
	return acl, nil
}

func archiveRule(principal, kind string) string {
	rights := "add_subdirectory"
	prefix := principal + " deny "
	switch kind {
	case "root":
		rights = "add_subdirectory,directory_inherit,limit_inherit,only_inherit"
	case "inherited-workspace":
		prefix = principal + " inherited deny "
	case "artifact-dir":
		rights = "add_file,add_subdirectory"
	case "artifact-file":
		rights = "append,write"
	case "pending-file":
		rights = "append,read,write"
	}
	return prefix + rights
}

func archiveStatePath(home, stateDir string, create bool) (string, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return "", errors.New("archive guard requires a clean absolute state directory")
	}
	rel, err := filepath.Rel(home, stateDir)
	if err != nil || !filepath.IsLocal(rel) || rel == "." || strings.HasPrefix(rel, ".zcode"+string(filepath.Separator)) || rel == ".zcode" {
		return "", errors.New("archive guard state must be beneath home and outside the client cache")
	}
	dir := filepath.Join(stateDir, "archive-guard")
	if err := archivePath(dir, true); err != nil {
		return "", err
	}
	if create {
		root, err := openStateRoot(dir, true)
		if err != nil {
			return "", err
		}
		root.Close()
	}
	if info, err := os.Lstat(dir); err == nil {
		if info.Mode().Perm() != 0700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || archiveNumber(info, "Uid") != uint64(os.Getuid()) {
			return "", errors.New("archive guard receipt directory must remain private and user-owned")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return dir, nil
}

func archiveLoad(dir, home, principal string) (*archiveReceipt, error) {
	path := filepath.Join(dir, "receipt.json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || archiveNumber(info, "Nlink") != 1 || archiveNumber(info, "Uid") != uint64(os.Getuid()) || info.Size() > archiveGuardReceiptLimit {
		return nil, errors.New("invalid archive guard receipt metadata")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openStateFile(root, "receipt.json", os.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, archiveGuardReceiptLimit+1))
	if err != nil || len(data) > archiveGuardReceiptLimit {
		return nil, errors.New("cannot read bounded archive guard receipt")
	}
	var receipt archiveReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(new(any)) != io.EOF || receipt.Version != 1 || receipt.Home != home || receipt.Principal != principal {
		return nil, errors.New("invalid or foreign archive guard receipt")
	}
	if receipt.Phase != "preparing" && receipt.Phase != "enabling" && receipt.Phase != "enabled" && receipt.Phase != "disabling" {
		return nil, errors.New("invalid archive guard receipt phase")
	}
	if len(receipt.Changes) > archiveGuardLimit || (len(receipt.Changes) == 0 && receipt.Phase != "preparing") || (len(receipt.Changes) != 0 && receipt.Phase == "preparing") {
		return nil, errors.New("invalid archive guard receipt change count")
	}
	if len(receipt.Changes) > 0 && (receipt.Changes[0].Path != "." || receipt.Changes[0].Kind != "root") {
		return nil, errors.New("archive guard receipt must start with the fixed root change")
	}
	seen := map[string]bool{}
	for index, change := range receipt.Changes {
		if seen[change.Path] || change.Identity.UID != uint64(os.Getuid()) || change.Identity.Inode == 0 || change.Identity.Device == 0 || len(change.Before) > 31 {
			return nil, errors.New("invalid archive guard change identity")
		}
		seen[change.Path] = true
		parts := strings.Split(change.Path, string(filepath.Separator))
		valid := change.Path == "." && index == 0 && change.Kind == "root" && archiveDirectory(change.archiveNode)
		// Root inheritance applies to every new direct directory, even names
		// unsupported by the client. Rollback may own only its exact inherited
		// entry there, never arbitrary descendants of such an unexpected name.
		if change.Kind == "inherited-workspace" && archiveSingleName(change.Path) && archiveDirectory(change.archiveNode) {
			valid = true
		}
		if filepath.IsLocal(change.Path) && filepath.Clean(change.Path) == change.Path && !strings.ContainsAny(change.Path, "\x00\r\n\t") && archiveHash.MatchString(parts[0]) {
			if len(parts) == 1 {
				valid = (change.Kind == "workspace" || change.Kind == "inherited-workspace") && archiveDirectory(change.archiveNode)
			} else if parts[1] == "tmp" || parts[1] == "pending" {
				valid = change.Kind == "artifact-dir" && archiveDirectory(change.archiveNode) ||
					len(parts) > 2 && os.FileMode(change.Identity.Mode).IsRegular() &&
						(change.Kind == "artifact-file" && parts[1] == "tmp" || change.Kind == "pending-file" && parts[1] == "pending")
			}
		}
		if !valid || change.Rule != archiveRule(principal, change.Kind) {
			return nil, errors.New("archive guard receipt contains an out-of-scope change")
		}
		for _, acl := range change.Before {
			normalized, err := archiveNormalizeACL(acl)
			if err != nil || normalized != acl || acl == change.Rule || change.Kind == "root" && (strings.Contains(acl, "directory_inherit") || strings.Contains(acl, "file_inherit")) {
				return nil, errors.New("archive guard receipt contains ambiguous prior ACLs")
			}
		}
		if change.Kind == "inherited-workspace" && len(change.Before) != 0 {
			return nil, errors.New("ambiguous inherited archive guard entry")
		}
	}
	return &receipt, nil
}

func archiveSave(dir string, receipt *archiveReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil || len(data) > archiveGuardReceiptLimit {
		return errors.New("archive guard receipt exceeds size limit")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := ".receipt-" + rand.Text() + ".tmp"
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot stage archive guard receipt: %w", err)
	}
	defer root.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(name, "receipt.json"); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func archiveExpected(change archiveChange) []string {
	return append([]string{change.Rule}, change.Before...)
}

func archiveChangeState(root string, change archiveChange, run archiveGuardRunner) (bool, bool, error) {
	path := filepath.Join(root, change.Path)
	if err := archivePath(filepath.Dir(path), false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, true, nil
		}
		return false, false, err
	}
	node, err := archiveMetadata(path, change.Path, change.Kind)
	if errors.Is(err, os.ErrNotExist) {
		return false, true, nil
	}
	if err != nil || node.Identity != change.Identity {
		return false, false, errors.New("archive cache identity or permissions changed; refusing ACL mutation")
	}
	acl, err := archiveReadACL(path, run)
	if err != nil {
		return false, false, err
	}
	if reflect.DeepEqual(acl, archiveExpected(change)) {
		return true, false, nil
	}
	if reflect.DeepEqual(acl, change.Before) || len(acl) == 0 && len(change.Before) == 0 {
		return false, false, nil
	}
	return false, false, errors.New("archive ACL was edited or reordered; refusing to remove unknown rules")
}

func archiveApply(root string, change archiveChange, enable bool, run archiveGuardRunner) (bool, error) {
	installed, missing, err := archiveChangeState(root, change, run)
	if err != nil || missing || installed == enable {
		return false, err
	}
	args := []string{"-a#", "0", filepath.Join(root, change.Path)}
	if enable {
		args = []string{"+a#", "0", change.Rule, filepath.Join(root, change.Path)}
	}
	if _, err := archiveCommand(run, "/bin/chmod", args...); err != nil {
		return false, err
	}
	installed, missing, err = archiveChangeState(root, change, run)
	if err != nil || missing || installed != enable {
		return true, errors.New("archive ACL verification failed; recovery receipt retained")
	}
	return true, nil
}

// EnableArchiveGuard installs exact, separately indexed ACL entries. Every ACL
// change is durably described before mutation. Failures retain a recovery
// receipt; DisableArchiveGuard safely unwinds only still-identifiable entries.
func enableArchiveGuardDarwin(plan ArchiveGuardPlan, stateDir string) (ArchiveGuardResult, error) {
	result := archiveStatus()
	principal, err := archivePrincipal()
	if err != nil {
		return result, err
	}
	fresh, err := PlanArchiveGuard(plan.Home)
	if err != nil || plan.Root != fresh.Root {
		return result, errors.New("invalid or changed archive guard plan")
	}
	dir, err := archiveStatePath(plan.Home, stateDir, true)
	if err != nil {
		return result, err
	}
	release, err := AcquireLock(dir)
	if err != nil {
		return result, err
	}
	defer release()
	receipt, err := archiveLoad(dir, plan.Home, principal)
	if err == nil {
		return archiveInspect(plan.Home, receipt, plan.runner)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	receipt = &archiveReceipt{Version: 1, Home: plan.Home, Principal: principal, Phase: "preparing", Changes: []archiveChange{}}
	if err := archiveSave(dir, receipt); err != nil {
		return result, err
	}
	result.RecoveryNeeded = true
	if fresh.CreatesRoot {
		if err := os.Mkdir(fresh.Root, 0700); err != nil {
			return result, err
		}
	}
	nodes, _, err := archiveLayout(plan.Home)
	if err != nil {
		return result, err
	}
	for _, node := range nodes {
		before, err := archiveReadACL(filepath.Join(fresh.Root, node.Path), plan.runner)
		if err != nil {
			return result, err
		}
		rule := archiveRule(principal, node.Kind)
		for _, acl := range before {
			if acl == rule || acl == archiveRule(principal, "inherited-workspace") ||
				node.Kind == "root" && (strings.Contains(acl, "directory_inherit") || strings.Contains(acl, "file_inherit")) {
				return result, errors.New("pre-existing archive ACL has ambiguous ownership or inheritance")
			}
		}
		if len(before) >= 32 {
			return result, errors.New("archive ACL entry limit exceeded")
		}
		receipt.Changes = append(receipt.Changes, archiveChange{archiveNode: node, Rule: rule, Before: before})
	}
	receipt.Phase = "enabling"
	if err := archiveSave(dir, receipt); err != nil {
		return result, err
	}
	for _, change := range receipt.Changes {
		changed, err := archiveApply(fresh.Root, change, true, plan.runner)
		result.Changed = result.Changed || changed
		if err != nil {
			return result, err
		}
	}
	receipt.Phase = "enabled"
	if err := archiveSave(dir, receipt); err != nil {
		return result, err
	}
	status, err := archiveInspect(plan.Home, receipt, plan.runner)
	status.Changed = result.Changed
	return status, err
}

func archiveInspect(home string, receipt *archiveReceipt, run archiveGuardRunner) (ArchiveGuardStatus, error) {
	status := archiveStatus()
	if receipt == nil {
		return status, nil
	}
	status.RecoveryNeeded = true
	if receipt.Phase != "enabled" {
		return status, errors.New("interrupted archive guard change; run disable to recover before enabling again")
	}
	nodes, missing, err := archiveLayout(home)
	if err != nil || missing {
		status.RecoveryNeeded = true
		return status, errors.New("archive cache layout changed or contains an unsupported entry")
	}
	root := filepath.Join(home, ".zcode", "v2", "checkpoints")
	known := map[string]archiveChange{}
	for _, change := range receipt.Changes {
		known[change.Path] = change
	}
	for _, node := range nodes {
		change, exists := known[node.Path]
		if !exists {
			if node.Kind != "workspace" {
				return status, errors.New("new unguarded archive artifact found")
			}
			change = archiveChange{archiveNode: node, Rule: archiveRule(receipt.Principal, "inherited-workspace"), Before: []string{}}
		}
		installed, missing, err := archiveChangeState(root, change, run)
		if err != nil || missing || !installed {
			return status, errors.New("archive guard is incomplete, changed, or bypassed by an imported directory")
		}
		if node.Kind == "workspace" {
			status.ProtectedWorkspaces++
		} else if node.Kind != "root" {
			status.ProtectedArtifacts++
		}
	}
	status.Enabled, status.Healthy, status.RecoveryNeeded = true, true, false
	return status, nil
}

func inspectArchiveGuardDarwin(home, stateDir string) (ArchiveGuardStatus, error) {
	status := archiveStatus()
	principal, err := archivePrincipal()
	if err != nil {
		return status, err
	}
	if err := archivePath(home, false); err != nil {
		return status, err
	}
	dir, err := archiveStatePath(home, stateDir, false)
	if err != nil {
		return status, err
	}
	receipt, err := archiveLoad(dir, home, principal)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	status, err = archiveInspect(home, receipt, nil)
	if err != nil {
		status.Problems = append(status.Problems, err.Error())
	}
	return status, err
}

func disableArchiveGuardDarwin(home, stateDir string) (ArchiveGuardResult, error) {
	return disableArchiveGuard(home, stateDir, nil)
}

// Rollback inspects only direct workspace directories. Unsupported descendants
// may be why the guard is being disabled; they must not prevent removal of
// separately verified, owned rules. Links and foreign entries remain untouched.
func archiveRollbackWorkspaces(root string) ([]archiveNode, error) {
	if err := archivePath(root, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	dir, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(archiveGuardLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > archiveGuardLimit {
		return nil, errors.New("workspace rollback exceeds metadata limit; root inheritance has been stopped")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	nodes := []archiveNode{}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, errors.New("cannot identify a direct cache entry; recovery receipt retained")
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || archiveNumber(info, "Uid") != uint64(os.Getuid()) {
			continue
		}
		if !archiveSingleName(entry.Name()) {
			return nil, errors.New("direct cache directory has an unsupported name; recovery receipt retained")
		}
		node, err := archiveMetadata(path, entry.Name(), "workspace")
		if err != nil || !archiveDirectory(node) {
			return nil, errors.New("direct cache directory changed during rollback inspection")
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func archivePruneAbsent(root string, changes []archiveChange) []archiveChange {
	kept := []archiveChange{changes[0]}
	for _, change := range changes[1:] {
		path := filepath.Join(root, change.Path)
		err := archivePath(filepath.Dir(path), false)
		if err == nil {
			_, err = os.Lstat(path)
		}
		// Only a confirmed absent path is retired. Links, inaccessible parents,
		// replacement inodes and edited ACLs remain recorded for later refusal.
		if !errors.Is(err, os.ErrNotExist) {
			kept = append(kept, change)
		}
	}
	return kept
}

func disableArchiveGuard(home, stateDir string, run archiveGuardRunner) (ArchiveGuardResult, error) {
	result := archiveStatus()
	principal, err := archivePrincipal()
	if err != nil {
		return result, err
	}
	if err := archivePath(home, false); err != nil {
		return result, err
	}
	dir, err := archiveStatePath(home, stateDir, false)
	if err != nil {
		return result, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	release, err := AcquireLock(dir)
	if err != nil {
		return result, err
	}
	defer release()
	receipt, err := archiveLoad(dir, home, principal)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.RecoveryNeeded = true
	root := filepath.Join(home, ".zcode", "v2", "checkpoints")
	if receipt.Phase != "preparing" {
		// Remove verified root inheritance before enumerating children. Unsupported
		// new descendants are not followed, and never edited merely to roll back.
		receipt.Phase = "disabling"
		if err := archiveSave(dir, receipt); err != nil {
			return result, err
		}
		changed, err := archiveApply(root, receipt.Changes[0], false, run)
		result.Changed = changed
		if err != nil {
			return result, err
		}
		receipt.Changes = archivePruneAbsent(root, receipt.Changes)
		nodes, err := archiveRollbackWorkspaces(root)
		if err != nil {
			return result, err
		}
		known := map[string]bool{}
		for _, change := range receipt.Changes {
			known[change.Path] = true
		}
		for _, node := range nodes {
			if known[node.Path] || node.Kind != "workspace" {
				continue
			}
			acl, err := archiveReadACL(filepath.Join(root, node.Path), run)
			if err != nil {
				return result, err
			}
			rule := archiveRule(principal, "inherited-workspace")
			matches := 0
			for _, line := range acl {
				if line == rule {
					matches++
				}
			}
			if matches == 0 {
				continue // An imported unguarded directory is not ours to edit.
			}
			if matches != 1 || len(acl) != 1 {
				return result, errors.New("new workspace has ambiguous inherited ACL ownership")
			}
			if len(receipt.Changes) >= archiveGuardLimit {
				return result, errors.New("inherited workspace rollback exceeds receipt limit")
			}
			node.Kind = "inherited-workspace"
			receipt.Changes = append(receipt.Changes, archiveChange{archiveNode: node, Rule: rule, Before: []string{}})
		}
		if err := archiveSave(dir, receipt); err != nil {
			return result, err
		}
		for _, change := range receipt.Changes[1:] {
			changed, err := archiveApply(root, change, false, run)
			result.Changed = result.Changed || changed
			if err != nil {
				return result, err
			}
		}
	}
	if err := os.Remove(filepath.Join(dir, "receipt.json")); err != nil {
		return result, err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return result, err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return result, err
	}
	result.RecoveryNeeded, result.Healthy = false, true
	return result, nil
}
