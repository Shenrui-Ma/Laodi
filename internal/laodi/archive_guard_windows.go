package laodi

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// The Windows receipt is deliberately distinct from the POSIX inode/ACL format.
// Only a fixed cache subtree is eligible; no repository or system ACL is edited.
type windowsArchiveIdentity struct {
	Volume    uint32 `json:"volume"`
	Index     uint64 `json:"index"`
	Directory bool   `json:"directory"`
}
type windowsArchiveNode struct {
	Path      string                 `json:"path"`
	Kind      string                 `json:"kind"`
	Identity  windowsArchiveIdentity `json:"identity"`
	Before    []byte                 `json:"before"`
	Protected bool                   `json:"protected"`
	Control   uint16                 `json:"control"`
}
type windowsArchiveReceipt struct {
	Version   int                  `json:"version"`
	App       string               `json:"app,omitempty"`
	Home      string               `json:"home"`
	Principal string               `json:"principal"`
	Phase     string               `json:"phase"`
	Changes   []windowsArchiveNode `json:"changes"`
}

func checkProtectionPath(path string, directory, missing bool) error {
	if !localAbsoluteWindowsPath(path) {
		return errors.New("protection requires a clean local absolute path")
	}
	if _, err := windowsPrivatePath(path); err != nil {
		return err
	}
	if err := windowsNoReparseAncestors(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if missing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() != directory || !directory && !info.Mode().IsRegular() {
		return errors.New("unexpected protection path type")
	}
	return nil
}

// Read metadata through a handle without requesting file content access. This
// remains available for pending packages after FILE_READ_DATA has been denied.
func windowsArchiveOpen(path string, mutate bool) (syscall.Handle, error) {
	if !localAbsoluteWindowsPath(path) {
		return 0, errors.New("invalid archive path")
	}
	if err := windowsNoReparseAncestors(path); err != nil {
		return 0, err
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	access, share := uint32(0x20000|0x80), uint32(syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	if mutate {
		// Exclusive target handle prevents rename/delete during the pinned
		// path-based ACL mutation. Metadata remains accessible after denial.
		access, share = 0x02000000, 0
	}
	h, err := syscall.CreateFile(name, access, share, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, &os.PathError{Op: "archive metadata", Path: path, Err: err}
	}
	return h, nil
}

func windowsArchiveRead(h syscall.Handle) (windowsArchiveNode, error) {
	var n windowsArchiveNode
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return n, err
	}
	directory := info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0
	if err := windowsCheckFileIdentity(h, directory); err != nil {
		return n, err
	}
	if err := windowsCheckClientHandle(h, directory); err != nil {
		return n, err
	}
	var owner *syscall.SID
	var acl *byte
	data := make([]byte, 65536)
	var needed uint32
	ok, _, callErr := fileAdvapi.NewProc("GetKernelObjectSecurity").Call(uintptr(h), 1|4, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return n, callErr
	}
	sd := &data[0]
	var defaulted, present uint32
	ok, _, callErr = fileAdvapi.NewProc("GetSecurityDescriptorOwner").Call(uintptr(unsafe.Pointer(sd)), uintptr(unsafe.Pointer(&owner)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 {
		return n, callErr
	}
	ok, _, callErr = fileAdvapi.NewProc("GetSecurityDescriptorDacl").Call(uintptr(unsafe.Pointer(sd)), uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&acl)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 || present == 0 {
		return n, errors.New("archive DACL unavailable")
	}
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return n, err
	}
	if owner == nil || acl == nil {
		return n, errors.New("archive owner or DACL is missing")
	}
	ownerSID, err := owner.String()
	if err != nil || ownerSID != sid {
		return n, errors.New("archive cache must be owned by the current user")
	}
	var control uint16
	var revision uint32
	ok, _, err = getSecurityControl.Call(uintptr(unsafe.Pointer(sd)), uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision)))
	if ok == 0 {
		return n, err
	}
	size := *(*uint16)(unsafe.Add(unsafe.Pointer(acl), 2))
	if size < 8 || size > 32768 {
		return n, errors.New("archive ACL exceeds bounds")
	}
	n.Before = append([]byte(nil), unsafe.Slice(acl, int(size))...)
	if _, err := windowsArchiveACEs(n.Before); err != nil {
		return n, err
	}
	n.Protected = control&0x1000 != 0
	if control&0x100 != 0 {
		return n, errors.New("archive inheritance update is pending")
	}
	n.Control = control & 0x1400
	n.Identity = windowsArchiveIdentity{info.VolumeSerialNumber, uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), directory}
	return n, nil
}

func windowsArchiveMetadata(path, rel, kind string) (windowsArchiveNode, error) {
	h, err := windowsArchiveOpen(path, false)
	if err != nil {
		return windowsArchiveNode{}, err
	}
	defer syscall.CloseHandle(h)
	n, err := windowsArchiveRead(h)
	n.Path, n.Kind = rel, kind
	return n, err
}

func windowsArchiveACEs(acl []byte) ([][]byte, error) {
	if len(acl) < 8 || len(acl) > 32768 || int(binary.LittleEndian.Uint16(acl[2:])) != len(acl) || (acl[0] != 2 && acl[0] != 4) {
		return nil, errors.New("invalid archive ACL")
	}
	count := int(binary.LittleEndian.Uint16(acl[4:]))
	if count > 128 {
		return nil, errors.New("too many archive ACEs")
	}
	result := [][]byte{}
	offset := 8
	for i := 0; i < count; i++ {
		if offset+8 > len(acl) {
			return nil, errors.New("truncated archive ACE")
		}
		size := int(binary.LittleEndian.Uint16(acl[offset+2:]))
		if size < 16 || size%4 != 0 || offset+size > len(acl) {
			return nil, errors.New("invalid archive ACE size")
		}
		ace := acl[offset : offset+size]
		if ace[0] > 1 || ace[1]&^byte(31) != 0 || ace[8] != 1 || int(ace[9])*4+16 != size {
			return nil, errors.New("unsupported archive ACE")
		}
		result = append(result, ace)
		offset += size
	}
	return result, nil
}

func windowsArchiveACL(aces [][]byte) []byte {
	size := 8
	for _, a := range aces {
		size += len(a)
	}
	acl := make([]byte, 8, size)
	acl[0] = 2
	binary.LittleEndian.PutUint16(acl[2:], uint16(size))
	binary.LittleEndian.PutUint16(acl[4:], uint16(len(aces)))
	for _, a := range aces {
		acl = append(acl, a...)
	}
	return acl
}

func windowsArchiveRule(principal, kind string) ([]byte, error) {
	sid, err := syscall.StringToSid(principal)
	if err != nil {
		return nil, err
	}
	length, _, _ := fileAdvapi.NewProc("GetLengthSid").Call(uintptr(unsafe.Pointer(sid)))
	if length < 8 || length > 68 {
		return nil, errors.New("invalid archive principal")
	}
	mask, flags := uint32(4), byte(0)
	switch kind {
	case "root":
		flags = 2 | 4 | 8 // directory inherit, one generation, inherit only
	case "workspace":
	case "inherited-workspace":
		flags = 16
	case "artifact-dir":
		mask = 2 | 4
	case "artifact-file":
		mask = 2 | 4
	case "pending-file":
		mask = 1 | 2 | 4
	default:
		return nil, errors.New("invalid archive rule kind")
	}
	a := make([]byte, 8+int(length))
	a[0], a[1] = 1, flags
	binary.LittleEndian.PutUint16(a[2:], uint16(len(a)))
	binary.LittleEndian.PutUint32(a[4:], mask)
	copy(a[8:], unsafe.Slice((*byte)(unsafe.Pointer(sid)), int(length)))
	return a, nil
}

func windowsArchiveExpected(n windowsArchiveNode, principal string) ([]byte, error) {
	aces, err := windowsArchiveACEs(n.Before)
	if err != nil {
		return nil, err
	}
	rule, err := windowsArchiveRule(principal, n.Kind)
	if err != nil {
		return nil, err
	}
	if len(aces) >= 128 || len(n.Before)+len(rule) > 32768 {
		return nil, errors.New("archive ACL has no space for a recoverable guard entry")
	}
	for _, a := range aces {
		if bytes.Equal(a, rule) {
			return nil, errors.New("ambiguous pre-existing archive rule")
		}
	}
	if n.Kind == "inherited-workspace" {
		// Windows places inherited denies after explicit entries, before other
		// inherited entries. Explicit denies on existing workspaces go first.
		at := 0
		for at < len(aces) && aces[at][1]&16 == 0 {
			at++
		}
		result := append([][]byte{}, aces[:at]...)
		result = append(result, rule)
		result = append(result, aces[at:]...)
		return windowsArchiveACL(result), nil
	}
	return windowsArchiveACL(append([][]byte{rule}, aces...)), nil
}

func windowsArchiveSameACL(a, b []byte) bool {
	aa, ea := windowsArchiveACEs(a)
	bb, eb := windowsArchiveACEs(b)
	if ea != nil || eb != nil || len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if !bytes.Equal(aa[i], bb[i]) {
			return false
		}
	}
	return true
}

func windowsArchiveRoot(home string) string {
	return filepath.Join(home, ".zcode", "v2", "checkpoints")
}

func windowsArchiveLayout(home string) ([]windowsArchiveNode, bool, error) {
	if err := checkProtectionPath(home, true, false); err != nil {
		return nil, false, err
	}
	if err := windowsLocalVolume(home); err != nil {
		return nil, false, err
	}
	for _, p := range []string{home, filepath.Join(home, ".zcode"), filepath.Join(home, ".zcode", "v2")} {
		if _, err := windowsArchiveMetadata(p, ".", "root"); err != nil {
			return nil, false, err
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".zcode", "v2", "repo-snapshots")); !errors.Is(err, os.ErrNotExist) {
		return nil, false, errors.New("legacy snapshot cache requires migration")
	}
	root := windowsArchiveRoot(home)
	first, err := windowsArchiveMetadata(root, ".", "root")
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !first.Identity.Directory {
		return nil, false, errors.New("archive root must be a directory")
	}
	nodes := []windowsArchiveNode{first}
	count := 1
	var visit func(string, int) error
	visit = func(rel string, depth int) error {
		if depth > 8 {
			return errors.New("archive depth exceeds limit")
		}
		f, err := os.Open(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		entries, err := f.ReadDir(archiveGuardLimit + 1)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			count++
			if count > archiveGuardLimit || !archiveSingleName(entry.Name()) {
				return errors.New("archive layout exceeds bounds")
			}
			next := filepath.Join(rel, entry.Name())
			parts := strings.Split(next, string(filepath.Separator))
			kind := "metadata"
			if !archiveHash.MatchString(parts[0]) {
				return errors.New("unexpected workspace cache name")
			}
			if len(parts) == 1 {
				kind = "workspace"
			} else if parts[1] == "tmp" {
				kind = "artifact-file"
			} else if parts[1] == "pending" {
				kind = "pending-file"
			}
			n, err := windowsArchiveMetadata(filepath.Join(root, next), next, kind)
			if err != nil {
				return err
			}
			if len(parts) == 1 && !n.Identity.Directory {
				return errors.New("workspace is not a directory")
			}
			if len(parts) == 2 {
				known := parts[1] == "tmp" || parts[1] == "pending" || parts[1] == "manifests" || parts[1] == "extra-manifests"
				if known != n.Identity.Directory {
					return errors.New("unsupported archive layout")
				}
			}
			if n.Identity.Directory && (kind == "artifact-file" || kind == "pending-file") {
				n.Kind = "artifact-dir"
			}
			if kind != "metadata" {
				nodes = append(nodes, n)
			}
			if n.Identity.Directory {
				if err := visit(next, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err = visit(".", 0)
	return nodes, false, err
}

func PlanArchiveGuard(home string) (ArchiveGuardPlan, error) {
	nodes, missing, err := windowsArchiveLayout(home)
	if err != nil {
		return ArchiveGuardPlan{}, err
	}
	p := ArchiveGuardPlan{Home: home, Root: windowsArchiveRoot(home), CreatesRoot: missing}
	for _, n := range nodes {
		if n.Kind == "workspace" {
			p.ExistingWorkspaces++
		} else if n.Kind != "root" {
			p.ExistingArtifacts++
		}
	}
	return p, nil
}

func windowsArchiveState(home, state string, create bool) (string, error) {
	if !localAbsoluteWindowsPath(home) || !localAbsoluteWindowsPath(state) {
		return "", errors.New("archive state requires absolute local paths")
	}
	rel, err := filepath.Rel(home, state)
	if err != nil || !filepath.IsLocal(rel) || rel == "." || strings.EqualFold(strings.Split(rel, string(filepath.Separator))[0], ".zcode") {
		return "", errors.New("archive state must be beneath home and outside client cache")
	}
	dir := filepath.Join(state, "archive-guard")
	if err := windowsNoReparseAncestors(dir); err != nil {
		return "", err
	}
	if !create {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			return dir, nil
		}
	}
	root, err := openStateRoot(dir, create)
	if err != nil {
		return "", err
	}
	root.Close()
	return dir, nil
}

func windowsArchiveLoad(dir, home string) (*windowsArchiveReceipt, error) {
	root, err := openStateRoot(dir, false)
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
		return nil, errors.New("archive receipt exceeds bounds")
	}
	var r windowsArchiveReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return nil, err
	}
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Version != 2 || r.Home != home || r.Principal != sid {
		return nil, errors.New("invalid Windows archive receipt")
	}
	if r.App != "" && !localAbsoluteWindowsPath(r.App) {
		return nil, errors.New("invalid receipt client path")
	}
	if r.Phase != "preparing" && r.Phase != "enabling" && r.Phase != "enabled" && r.Phase != "disabling" {
		return nil, errors.New("invalid archive phase")
	}
	if len(r.Changes) > archiveGuardLimit || len(r.Changes) == 0 && r.Phase != "preparing" || len(r.Changes) != 0 && r.Phase == "preparing" {
		return nil, errors.New("invalid archive change count")
	}
	seen := map[string]bool{}
	for i, n := range r.Changes {
		parts := strings.Split(n.Path, string(filepath.Separator))
		valid := i == 0 && n.Path == "." && n.Kind == "root" && n.Identity.Directory
		if i > 0 && filepath.IsLocal(n.Path) && filepath.Clean(n.Path) == n.Path && archiveHash.MatchString(parts[0]) {
			if len(parts) == 1 {
				valid = (n.Kind == "workspace" || n.Kind == "inherited-workspace") && n.Identity.Directory
			} else if parts[1] == "tmp" || parts[1] == "pending" {
				valid = n.Kind == "artifact-dir" && n.Identity.Directory || len(parts) > 2 && !n.Identity.Directory && (parts[1] == "tmp" && n.Kind == "artifact-file" || parts[1] == "pending" && n.Kind == "pending-file")
			}
		}
		// A direct unexpected directory can still inherit our root rule. Its
		// descendants are never traversed or edited during rollback.
		if i > 0 && n.Kind == "inherited-workspace" && archiveSingleName(n.Path) && n.Identity.Directory {
			valid = true
		}
		if _, err := windowsPrivatePath(filepath.Join(windowsArchiveRoot(home), n.Path)); err != nil {
			valid = false
		}
		if !valid || seen[strings.ToLower(n.Path)] || n.Identity.Index == 0 || n.Identity.Volume == 0 || n.Control & ^uint16(0x1400) != 0 || n.Protected != (n.Control&0x1000 != 0) {
			return nil, errors.New("out-of-scope archive receipt change")
		}
		if _, err := windowsArchiveExpected(n, sid); err != nil {
			return nil, err
		}
		seen[strings.ToLower(n.Path)] = true
	}
	return &r, nil
}

func windowsArchiveSave(dir string, r *windowsArchiveReceipt) error {
	b, err := json.Marshal(r)
	if err != nil || len(b) > archiveGuardReceiptLimit {
		return errors.New("archive receipt exceeds bounds")
	}
	return writeWindowsBytes(dir, "receipt.json", b, true)
}

func windowsArchiveApply(root string, n windowsArchiveNode, sid string, enable bool) (bool, error) {
	path := filepath.Join(root, n.Path)
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer parent.Close()
	unpin, err := windowsPinRoot(parent)
	if err != nil {
		return false, err
	}
	defer unpin()
	h, err := windowsArchiveOpen(path, true)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(h)
	current, err := windowsArchiveRead(h)
	if err != nil {
		return false, err
	}
	if current.Identity != n.Identity {
		return false, errors.New("archive identity or inheritance changed; recovery retained")
	}
	expected, err := windowsArchiveExpected(n, sid)
	if err != nil {
		return false, err
	}
	source, target := n.Before, expected
	sourceControl, targetControl := n.Control, n.Control
	if n.Kind == "root" {
		targetControl |= 0x400
	}
	if !enable {
		source, target = expected, n.Before
		sourceControl, targetControl = targetControl, sourceControl
	}
	if windowsArchiveSameACL(current.Before, target) && current.Control == targetControl {
		return false, nil
	}
	if !windowsArchiveSameACL(current.Before, source) || current.Control != sourceControl {
		return false, errors.New("archive ACL was edited; refusing to remove unknown rules")
	}
	if err := windowsArchiveSetDACL(h, target, targetControl); err != nil {
		return false, err
	}
	after, err := windowsArchiveRead(h)
	if err != nil || after.Identity != n.Identity || after.Control != targetControl || !windowsArchiveSameACL(after.Before, target) {
		return true, errors.New("archive ACL verification failed; recovery retained")
	}
	return true, nil
}

func EnableArchiveGuard(plan ArchiveGuardPlan, state string) (ArchiveGuardResult, error) {
	result := archiveStatus()
	fresh, err := PlanArchiveGuard(plan.Home)
	if err != nil || fresh.Root != plan.Root {
		return result, errors.New("archive layout changed")
	}
	dir, err := windowsArchiveState(plan.Home, state, true)
	if err != nil {
		return result, err
	}
	unlock, err := AcquireLock(dir)
	if err != nil {
		return result, err
	}
	defer unlock()
	if r, err := windowsArchiveLoad(dir, plan.Home); err == nil {
		return windowsArchiveInspect(r)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	sid, err := windowsCurrentUserSID()
	if err != nil {
		return result, err
	}
	r := &windowsArchiveReceipt{Version: 2, Home: plan.Home, App: plan.ClientApp, Principal: sid, Phase: "preparing"}
	if err := windowsArchiveSave(dir, r); err != nil {
		return result, err
	}
	result.RecoveryNeeded = true
	if fresh.CreatesRoot {
		if err := windowsCreateDirectory(fresh.Root); err != nil {
			return result, err
		}
	}
	nodes, _, err := windowsArchiveLayout(plan.Home)
	if err != nil {
		return result, err
	}
	for _, n := range nodes {
		if _, err := windowsArchiveExpected(n, sid); err != nil {
			return result, err
		}
	}
	r.Changes, r.Phase = nodes, "enabling"
	if err := windowsArchiveSave(dir, r); err != nil {
		return result, err
	}
	for _, n := range nodes {
		changed, err := windowsArchiveApply(fresh.Root, n, sid, true)
		result.Changed = result.Changed || changed
		if err != nil {
			return result, err
		}
	}
	r.Phase = "enabled"
	if err := windowsArchiveSave(dir, r); err != nil {
		return result, err
	}
	status, err := windowsArchiveInspect(r)
	status.Changed = result.Changed
	return status, err
}

func windowsArchiveInherited(n windowsArchiveNode, sid string) (windowsArchiveNode, bool, error) {
	rule, err := windowsArchiveRule(sid, "inherited-workspace")
	if err != nil {
		return n, false, err
	}
	aces, err := windowsArchiveACEs(n.Before)
	if err != nil {
		return n, false, err
	}
	kept := [][]byte{}
	count := 0
	for _, a := range aces {
		if bytes.Equal(a, rule) {
			count++
		} else {
			kept = append(kept, a)
		}
	}
	if count == 0 {
		return n, false, nil
	}
	if count != 1 {
		return n, false, errors.New("ambiguous inherited archive rule")
	}
	original := n.Before
	n.Before = windowsArchiveACL(kept)
	n.Kind = "inherited-workspace"
	expected, err := windowsArchiveExpected(n, sid)
	if err != nil || !windowsArchiveSameACL(original, expected) {
		return n, false, errors.New("inherited archive rule order changed")
	}
	return n, true, nil
}

func windowsArchiveInspect(r *windowsArchiveReceipt) (ArchiveGuardStatus, error) {
	result := archiveStatus()
	result.RecoveryNeeded = true
	if r.Phase != "enabled" {
		return result, errors.New("interrupted archive guard change; disable to recover")
	}
	nodes, missing, err := windowsArchiveLayout(r.Home)
	if err != nil || missing {
		return result, errors.New("archive layout unavailable or changed")
	}
	known := map[string]windowsArchiveNode{}
	for _, n := range r.Changes {
		known[n.Path] = n
	}
	for _, current := range nodes {
		n, ok := known[current.Path]
		if !ok {
			if current.Kind != "workspace" {
				return result, errors.New("new unguarded archive artifact")
			}
			var inherited bool
			n, inherited, err = windowsArchiveInherited(current, r.Principal)
			if err != nil || !inherited {
				return result, errors.New("workspace did not inherit protection")
			}
		}
		expected, err := windowsArchiveExpected(n, r.Principal)
		if err != nil || n.Identity != current.Identity || current.Control != windowsArchiveEnabledControl(n) || !windowsArchiveSameACL(expected, current.Before) {
			return result, errors.New("archive guard changed or incomplete")
		}
		if current.Kind == "workspace" {
			result.ProtectedWorkspaces++
		} else if current.Kind != "root" {
			result.ProtectedArtifacts++
		}
	}
	result.Enabled, result.Healthy, result.RecoveryNeeded = true, true, false
	return result, nil
}

func InspectArchiveGuard(home, state string) (ArchiveGuardStatus, error) {
	result := archiveStatus()
	dir, err := windowsArchiveState(home, state, false)
	if err != nil {
		return result, err
	}
	r, err := windowsArchiveLoad(dir, home)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		result.RecoveryNeeded = true
		return result, err
	}
	result, err = windowsArchiveInspect(r)
	if err != nil {
		result.Problems = []string{"archive_restrictions_not_confirmed"}
	}
	return result, err
}

func DisableArchiveGuard(home, state string) (ArchiveGuardResult, error) {
	result := archiveStatus()
	dir, err := windowsArchiveState(home, state, false)
	if err != nil {
		return result, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	unlock, err := AcquireLock(dir)
	if err != nil {
		return result, err
	}
	defer unlock()
	r, err := windowsArchiveLoad(dir, home)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		result.RecoveryNeeded = true
		return result, err
	}
	result.RecoveryNeeded = true
	root := windowsArchiveRoot(home)
	if r.Phase != "preparing" {
		r.Phase = "disabling"
		if err := windowsArchiveSave(dir, r); err != nil {
			return result, err
		}
		changed, err := windowsArchiveApply(root, r.Changes[0], r.Principal, false)
		result.Changed = changed
		if err != nil {
			return result, err
		}
		// Stop inheritance first, then journal new direct children before changing
		// them. Existing edited objects are never overwritten during recovery.
		f, err := os.Open(root)
		if err == nil {
			entries, e := f.ReadDir(archiveGuardLimit + 1)
			f.Close()
			if e != nil && !errors.Is(e, io.EOF) {
				return result, e
			}
			if len(entries) > archiveGuardLimit {
				return result, errors.New("archive rollback exceeds bounds")
			}
			known := map[string]bool{}
			for _, n := range r.Changes {
				known[strings.ToLower(n.Path)] = true
			}
			for _, entry := range entries {
				if known[strings.ToLower(entry.Name())] || !entry.IsDir() {
					continue
				}
				n, e := windowsArchiveMetadata(filepath.Join(root, entry.Name()), entry.Name(), "workspace")
				if e != nil {
					return result, e
				}
				n, owned, e := windowsArchiveInherited(n, r.Principal)
				if e != nil {
					return result, e
				}
				if owned {
					r.Changes = append(r.Changes, n)
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		if len(r.Changes) > archiveGuardLimit {
			return result, errors.New("archive rollback receipt exceeds bounds")
		}
		if err := windowsArchiveSave(dir, r); err != nil {
			return result, err
		}
		for _, n := range r.Changes[1:] {
			changed, err := windowsArchiveApply(root, n, r.Principal, false)
			result.Changed = result.Changed || changed
			if err != nil {
				return result, err
			}
		}
	}
	receiptRoot, err := openStateRoot(dir, false)
	if err != nil {
		return result, err
	}
	defer receiptRoot.Close()
	if err := receiptRoot.Remove("receipt.json"); err != nil {
		return result, err
	}
	result.RecoveryNeeded, result.Healthy = false, true
	return result, nil
}

// Use the low-level handle-based security setter deliberately: the higher-level
// SetSecurityInfo synthesizes/propagates inheritance and can duplicate or rewrite
// existing ACEs. The guard must mutate exactly one journalled object at a time.
// AUTO_INHERIT_REQ preserves the AUTO_INHERITED control bit without propagating
// into existing children; each child is independently verified below.
func windowsArchiveSetDACL(handle syscall.Handle, acl []byte, control uint16) error {
	type descriptor struct {
		Revision                 byte
		Reserved                 byte
		Control                  uint16
		Owner, Group, Sacl, Dacl uintptr
	}
	var sd descriptor
	ok, _, err := fileAdvapi.NewProc("InitializeSecurityDescriptor").Call(uintptr(unsafe.Pointer(&sd)), 1)
	if ok == 0 {
		return err
	}
	ok, _, err = fileAdvapi.NewProc("SetSecurityDescriptorDacl").Call(uintptr(unsafe.Pointer(&sd)), 1, uintptr(unsafe.Pointer(&acl[0])), 0)
	if ok == 0 {
		return err
	}
	flags := uintptr(control)
	if control&0x400 != 0 {
		flags |= 0x100
	}
	ok, _, err = fileAdvapi.NewProc("SetSecurityDescriptorControl").Call(uintptr(unsafe.Pointer(&sd)), 0x1500, flags)
	if ok == 0 {
		return err
	}
	ok, _, err = fileAdvapi.NewProc("SetKernelObjectSecurity").Call(uintptr(handle), 4, uintptr(unsafe.Pointer(&sd)))
	if ok == 0 {
		return err
	}
	return nil
}

func windowsArchiveEnabledControl(n windowsArchiveNode) uint16 {
	if n.Kind == "root" {
		return n.Control | 0x400
	}
	return n.Control
}
