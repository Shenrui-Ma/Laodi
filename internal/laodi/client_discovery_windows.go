package laodi

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

const maxClientExecutableBytes = 512 << 20

// DiscoverClients does not execute clients or read any settings/session data.
// A bounded list of public executable candidates avoids whole-disk searches.
func DiscoverClients() ClientDiscovery {
	result := ClientDiscovery{Candidates: []ClientCandidate{}, Scope: "windows_standard_locations_and_path",
		Unknowns: []string{"windows_client_contract_unverified", "nonstandard_install_locations_not_searched", "live_hook_delivery_not_verified"}}
	paths := make(map[string]string)
	if state, err := DefaultStateDir(""); err == nil {
		local := filepath.Dir(state)
		for adapter, names := range map[string][]string{
			"zcode":       {filepath.Join("Programs", "ZCode", "ZCode.exe"), filepath.Join("ZCode", "ZCode.exe")},
			"claude-code": {filepath.Join("Programs", "Claude", "Claude.exe"), filepath.Join("AnthropicClaude", "claude.exe")},
		} {
			for _, name := range names {
				paths[filepath.Join(local, name)] = adapter
			}
		}
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if !localAbsoluteWindowsPath(root) {
			continue
		}
		paths[filepath.Join(root, "ZCode", "ZCode.exe")] = "zcode"
		paths[filepath.Join(root, "Claude", "Claude.exe")] = "claude-code"
	}
	for name, adapter := range map[string]string{"zcode.exe": "zcode", "claude.exe": "claude-code"} {
		if candidate, err := exec.LookPath(name); err == nil && localAbsoluteWindowsPath(candidate) {
			paths[candidate] = adapter
		}
	}
	return inspectClientCandidates(result, paths)
}

func inspectClientCandidates(result ClientDiscovery, paths map[string]string) ClientDiscovery {
	seen := make(map[string]bool)
	for name, adapter := range paths {
		alias := strings.ToLower(name)
		if seen[alias] {
			continue
		}
		seen[alias] = true
		identity, ok := inspectClientExecutable(adapter, name)
		if ok {
			result.Candidates = append(result.Candidates, identity)
		}
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		return strings.ToLower(result.Candidates[i].Executable) < strings.ToLower(result.Candidates[j].Executable)
	})
	return result
}

func inspectClientExecutable(adapter, name string) (ClientCandidate, bool) {
	if !localAbsoluteWindowsPath(name) || !strings.EqualFold(filepath.Ext(name), ".exe") {
		return ClientCandidate{}, false
	}
	// Refuse any reparse component before opening a candidate. The opened file
	// is rechecked against the pathname below before its hash is reported.
	for current := name; ; current = filepath.Dir(current) {
		wide, err := syscall.UTF16PtrFromString(current)
		if err != nil {
			return ClientCandidate{}, false
		}
		attrs, err := syscall.GetFileAttributes(wide)
		if err != nil || attrs&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return ClientCandidate{}, false
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	before, err := os.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 2 || before.Size() > maxClientExecutableBytes {
		return ClientCandidate{}, false
	}
	f, err := os.Open(name)
	if err != nil {
		return ClientCandidate{}, false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return ClientCandidate{}, false
	}
	var header [64]byte
	if _, err := io.ReadFull(f, header[:]); err != nil || header[0] != 'M' || header[1] != 'Z' {
		return ClientCandidate{}, false
	}
	offset := int64(binary.LittleEndian.Uint32(header[60:]))
	if offset < 64 || offset > 1<<20 || offset+26 > opened.Size() {
		return ClientCandidate{}, false
	}
	var peHeader [26]byte
	if _, err := f.ReadAt(peHeader[:], offset); err != nil || string(peHeader[:4]) != "PE\x00\x00" {
		return ClientCandidate{}, false
	}
	magic := binary.LittleEndian.Uint16(peHeader[24:26])
	if magic != 0x10b && magic != 0x20b {
		return ClientCandidate{}, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ClientCandidate{}, false
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(f, maxClientExecutableBytes+1))
	if err != nil || count != opened.Size() {
		return ClientCandidate{}, false
	}
	version := clientFileVersion(name)
	after, err := os.Lstat(name)
	if err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return ClientCandidate{}, false
	}
	return ClientCandidate{Adapter: adapter, Executable: name, FileVersion: version,
		SHA256: hex.EncodeToString(hash.Sum(nil)), SnapshotCoverage: "unsupported_build", HookCoverage: "contract_unverified"}, true
}

func clientFileVersion(name string) string {
	wide, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}
	api := syscall.NewLazyDLL("api-ms-win-core-version-l1-1-0.dll")
	size, _, _ := api.NewProc("GetFileVersionInfoSizeW").Call(uintptr(unsafe.Pointer(wide)), 0)
	if size == 0 || size > 1<<20 {
		return ""
	}
	data := make([]byte, int(size))
	ok, _, _ := api.NewProc("GetFileVersionInfoW").Call(uintptr(unsafe.Pointer(wide)), 0, size, uintptr(unsafe.Pointer(&data[0])))
	if ok == 0 {
		return ""
	}
	subblock, _ := syscall.UTF16PtrFromString(`\`)
	var value unsafe.Pointer
	var length uint32
	ok, _, _ = api.NewProc("VerQueryValueW").Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(subblock)), uintptr(unsafe.Pointer(&value)), uintptr(unsafe.Pointer(&length)))
	if ok == 0 || length < 52 || value == nil {
		return ""
	}
	start, found := uintptr(unsafe.Pointer(&data[0])), uintptr(value)
	if found < start || found > start+uintptr(len(data))-52 {
		return ""
	}
	// VS_FIXEDFILEINFO begins with signature, structure version and file version.
	info := unsafe.Slice((*uint32)(value), 13)
	if info[0] != 0xfeef04bd {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", info[2]>>16, info[2]&0xffff, info[3]>>16, info[3]&0xffff)
}
