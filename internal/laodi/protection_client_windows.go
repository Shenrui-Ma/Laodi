package laodi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

const windowsArchiveVersion = "3.11.2.6792"
const windowsArchiveEXE = "5e928a515f78220553e94356f3c2226d142c6fe9e5ec8595c77e5f27a5e36bbc"
const windowsArchiveASAR = "14aa5db53b67a9f1b3cf9ecbd7fb314588ff928e5ed753b1f5249f4826fbc731"
const windowsArchiveCLI = "e9f1868c0fdb863537ed910ee3828b9be96b8c2fd805473f63b439e1113266b8"

var windowsArchiveBuild = windowsSnapshotBuildID(windowsArchiveVersion, windowsArchiveEXE, windowsArchiveASAR, windowsArchiveCLI)

func inspectProtectionBundle(app string) (string, string, error) {
	if DetectBuild(app) != windowsArchiveBuild {
		return "", "", errors.New("client does not match the verified Windows archive protection profile")
	}
	return windowsArchiveVersion, windowsArchiveASAR, nil
}

func PlanZCodeProtection(app, home, workspace string) (ZCodeProtectionPlan, error) {
	if err := checkArchiveProtectionScope(home); err != nil {
		return ZCodeProtectionPlan{}, err
	}
	for _, p := range []string{home, workspace} {
		if p != "" {
			if err := checkProtectionPath(p, true, false); err != nil {
				return ZCodeProtectionPlan{}, err
			}
		}
	}
	build, hash, err := inspectProtectionBundle(app)
	if err != nil {
		return ZCodeProtectionPlan{}, err
	}
	pids, err := RunningZCodeProcesses(app)
	if err != nil {
		return ZCodeProtectionPlan{}, err
	}
	return ZCodeProtectionPlan{App: app, Home: home, Workspace: workspace, Executable: app, Build: build, ASARSHA256: hash, RunningCount: len(pids), RunningPIDs: pids}, nil
}

func checkArchiveProtectionScope(home string) error {
	return checkWindowsArchiveEnvironment(home, os.Getenv)
}

// The pinned producer appends .zcode/v2 to its data base, choosing the first
// nonblank DATA_BASE_DIR or HOME before os.homedir (USERPROFILE on Windows).
// This checks our inherited environment, not a running client's environment.
func checkWindowsArchiveEnvironment(home string, getenv func(string) string) error {
	root := home
	for _, key := range []string{"ZCODE_DATA_BASE_DIR", "HOME", "USERPROFILE"} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			root = value
			break
		}
	}
	equalHome := func(value string) bool {
		value = filepath.Clean(value)
		return localAbsoluteWindowsPath(value) && strings.EqualFold(value, filepath.Clean(home))
	}
	if !equalHome(root) {
		return errors.New("alternate client cache roots require a separate protection profile")
	}
	if value := strings.TrimSpace(getenv("ZCODE_DESKTOP_HOME_DIR")); value != "" && !equalHome(value) {
		return errors.New("alternate client cache roots require a separate protection profile")
	}
	return nil
}

// All ordinary copies share this account's cache. Conservatively require all
// matching client processes to exit; never inspect arguments or stop a client.
func RunningZCodeProcesses(app string) ([]int, error) {
	if app != "" && !localAbsoluteWindowsPath(app) {
		return nil, errors.New("invalid client executable path")
	}
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snapshot)
	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	pids := []int{}
	err = syscall.Process32First(snapshot, &entry)
	for count := 0; err == nil; count++ {
		if count >= 16384 {
			return nil, errors.New("process inventory exceeds bounds")
		}
		if strings.EqualFold(syscall.UTF16ToString(entry.ExeFile[:]), "ZCode.exe") {
			pids = append(pids, int(entry.ProcessID))
		}
		err = syscall.Process32Next(snapshot, &entry)
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	sort.Ints(pids)
	return pids, nil
}

func TestArchiveProtection(home, state, app string) (ArchiveProtectionTestResult, error) {
	if err := checkArchiveProtectionScope(home); err != nil {
		return ArchiveProtectionTestResult{Status: "unsupported_client"}, err
	}
	if app == "" {
		app = protectionRecordedApp(home, state)
	}
	return testWindowsArchiveProtection(home, state, func() error { _, _, err := inspectProtectionBundle(app); return err })
}

func testWindowsArchiveProtection(home, state string, verify func() error) (result ArchiveProtectionTestResult, err error) {
	result.Status = "failed"
	defer func() {
		if err != nil {
			err = errors.New("archive protection self-test did not pass; inspect protection status")
		}
	}()
	dir, err := windowsArchiveState(home, state, false)
	if err != nil {
		return result, err
	}
	r, err := windowsArchiveLoad(dir, home)
	if err != nil {
		return result, err
	}
	unlock, err := AcquireLock(dir)
	if err != nil {
		return result, err
	}
	defer unlock()
	if s, e := windowsArchiveInspect(r); e != nil || !s.Healthy {
		return result, errors.New("guard is not healthy")
	}
	if verify == nil || verify() != nil {
		result.Status = "unsupported_client"
		return result, errors.New("client is not verified")
	}
	control, err := os.OpenRoot(dir)
	if err != nil {
		return result, err
	}
	defer control.Close()
	cache, err := os.OpenRoot(windowsArchiveRoot(home))
	if err != nil {
		return result, err
	}
	defer cache.Close()
	var random [6]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, err
	}
	name := hex.EncodeToString(random[:])
	a, cleanA, err := archiveProbe(control, "selftest-"+name, false)
	result.UnprotectedCreate = a.created
	if err != nil {
		return result, err
	}
	b, cleanB, err := archiveProbe(cache, name, true)
	result.ArchiveCreateDenied, result.MetadataReadWrite, result.CleanupComplete = b.denied, b.metadata, cleanA && cleanB
	if b.denied {
		result.DeniedOperations = 1
	}
	if err != nil {
		return result, err
	}
	s, err := windowsArchiveInspect(r)
	result.GuardHealthy = err == nil && s.Healthy
	result.ClientVerified = verify() == nil
	if !result.GuardHealthy || !result.ClientVerified || !result.CleanupComplete || !result.UnprotectedCreate || !b.denied || !b.metadata {
		return result, errors.New("incomplete self-test evidence")
	}
	result.Status = "passed"
	return result, nil
}

func protectionRecordedApp(home, state string) string {
	dir, err := windowsArchiveState(home, state, false)
	if err != nil {
		return ""
	}
	r, err := windowsArchiveLoad(dir, home)
	if err != nil {
		return ""
	}
	return r.App
}
