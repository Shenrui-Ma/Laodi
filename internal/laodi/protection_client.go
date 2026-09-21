package laodi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ZCodeProtectionASARSHA256 = "6d99a52d5c25bcdc9651d0a4aad57d215580cb11fe06c8a9ce3553387013678e"
const maxProtectionASARBytes = 512 << 20

type ZCodeProtectionPlan struct {
	App          string `json:"app"`
	Home         string `json:"home"`
	Workspace    string `json:"workspace,omitempty"`
	Executable   string `json:"executable"`
	Build        string `json:"build"`
	ASARSHA256   string `json:"asar_sha256"`
	RunningCount int    `json:"running_count"`
	RunningPIDs  []int  `json:"running_pids"`
}

// PlanZCodeProtection validates the exact client build before any restriction.
// Only identity and process metadata are inspected; no account settings are read.
func planZCodeProtectionDarwin(app, home, workspace string) (ZCodeProtectionPlan, error) {
	if runtime.GOOS != "darwin" {
		return ZCodeProtectionPlan{}, errors.New("client protection currently requires macOS")
	}
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		return ZCodeProtectionPlan{}, errors.New("run protection as your normal user, without sudo")
	}
	paths := []string{app, home}
	if workspace != "" {
		paths = append(paths, workspace)
	}
	for _, p := range paths {
		if err := checkProtectionPath(p, true, false); err != nil {
			return ZCodeProtectionPlan{}, err
		}
	}
	if filepath.Ext(app) != ".app" {
		return ZCodeProtectionPlan{}, errors.New("protection requires an application bundle")
	}
	executable := filepath.Join(app, "Contents/MacOS/ZCode")
	if err := checkProtectionPath(executable, false, false); err != nil {
		return ZCodeProtectionPlan{}, err
	}
	info, err := os.Lstat(executable)
	if err != nil || info.Mode().Perm()&0111 == 0 {
		return ZCodeProtectionPlan{}, errors.New("client executable is unavailable")
	}
	build, digest, err := inspectProtectionBundle(app)
	if err != nil {
		return ZCodeProtectionPlan{}, err
	}
	pids, err := RunningZCodeProcesses(app)
	if err != nil {
		return ZCodeProtectionPlan{}, err
	}
	return ZCodeProtectionPlan{App: app, Home: home, Workspace: workspace, Executable: executable, Build: build, ASARSHA256: digest, RunningCount: len(pids), RunningPIDs: pids}, nil
}

func inspectProtectionBundleDarwin(app string) (string, string, error) {
	plistPath := filepath.Join(app, "Contents", "Info.plist")
	data, err := readProtectionFile(plistPath, 1<<20)
	if err != nil {
		return "", "", errors.New("cannot safely read application identity")
	}
	build, err := protectionBundleVersion(data)
	if err != nil || build != KnownBuild {
		return "", "", errors.New("application build is not supported by this protection profile")
	}
	file, err := openProtectionFile(filepath.Join(app, "Contents", "Resources", "app.asar"), maxProtectionASARBytes)
	if err != nil {
		return "", "", errors.New("cannot safely read the bounded application archive")
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maxProtectionASARBytes+1))
	if err != nil || n > maxProtectionASARBytes {
		return "", "", errors.New("application archive is unreadable or too large")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != ZCodeProtectionASARSHA256 {
		return "", "", errors.New("application archive differs from the verified protection profile")
	}
	return build, digest, nil
}

func protectionBundleVersion(data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, rootCount, dictionaryCount := 0, 0, 0
	version, pending, seenVersion := "", false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", errors.New("invalid application property list")
		}
		switch item := token.(type) {
		case xml.StartElement:
			if item.Name.Space != "" {
				return "", errors.New("namespaced application property list is unsupported")
			}
			depth++
			if depth == 1 {
				rootCount++
				if item.Name.Local != "plist" {
					return "", errors.New("invalid application property list root")
				}
			}
			if depth == 2 {
				dictionaryCount++
				if item.Name.Local != "dict" {
					return "", errors.New("invalid application property list dictionary")
				}
			}
			if depth != 3 {
				continue
			}
			if pending {
				if item.Name.Local != "string" {
					return "", errors.New("application version must be a string")
				}
				var err error
				version, err = protectionPlainXML(decoder, item)
				if err != nil {
					return "", errors.New("invalid application version")
				}
				depth--
				pending = false
			} else if item.Name.Local == "key" {
				key, err := protectionPlainXML(decoder, item)
				if err != nil {
					return "", errors.New("invalid application property key")
				}
				depth--
				if key == "CFBundleVersion" {
					if seenVersion {
						return "", errors.New("duplicate application version")
					}
					seenVersion = true
					pending = true
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth <= 2 && strings.TrimSpace(string(item)) != "" {
				return "", errors.New("unexpected application property list text")
			}
		}
	}
	if rootCount != 1 || dictionaryCount != 1 || pending || version == "" || depth != 0 {
		return "", errors.New("application version is missing or ambiguous")
	}
	return version, nil
}

func protectionPlainXML(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var value strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}
		switch item := token.(type) {
		case xml.CharData:
			value.Write(item)
		case xml.EndElement:
			if item.Name != start.Name {
				return "", errors.New("mismatched property element")
			}
			return value.String(), nil
		default:
			return "", errors.New("property value must contain plain text")
		}
	}
}

func checkProtectionPathDarwin(path string, directory, allowMissing bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return errors.New("protection paths must be clean absolute paths")
	}
	if path == string(filepath.Separator) && !directory {
		return errors.New("protection file cannot be a filesystem root")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current := string(filepath.Separator)
	for i, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("protection path does not exist or is unreadable")
		}
		wantDir := i < len(parts)-1 || directory
		if info.Mode()&os.ModeSymlink != 0 || (wantDir && !info.IsDir()) || (!wantDir && !info.Mode().IsRegular()) {
			return errors.New("protection paths must not contain symbolic links or special files")
		}
	}
	return nil
}

func openProtectionFile(path string, limit int64) (*os.File, error) {
	if err := checkProtectionPath(path, false, false); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	before, err := root.Lstat(filepath.Base(path))
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return nil, errors.New("invalid bounded protection file")
	}
	file, err := openExistingStateFile(root, filepath.Base(path), os.O_RDONLY)
	if err != nil {
		return nil, errors.New("cannot safely open protection file")
	}
	opened, err := file.Stat()
	after, afterErr := root.Lstat(filepath.Base(path))
	if err != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(after, opened) || !opened.Mode().IsRegular() || opened.Size() > limit {
		file.Close()
		return nil, errors.New("protection file changed while opening")
	}
	return file, nil
}

func readProtectionFile(path string, limit int64) ([]byte, error) {
	file, err := openProtectionFile(path, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("protection file exceeds limit")
	}
	return data, nil
}

// RunningZCodeProcesses reads process IDs and executable names only. Arguments
// and environments may contain credentials and are deliberately not queried.
func runningZCodeProcessesDarwin(app string) ([]int, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("application process inspection requires macOS")
	}
	// Removal must remain possible after the application has been uninstalled.
	if !filepath.IsAbs(app) || filepath.Clean(app) != app || strings.ContainsAny(app, "\x00\r\n") || filepath.Ext(app) != ".app" {
		return nil, errors.New("process inspection requires a clean application path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/ps", "-ww", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=", "-o", "comm=")
	var output protectionProcessOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, errors.New("cannot inspect running application processes")
	}
	return parseProtectionProcesses(output.Bytes(), app), nil
}

type protectionProcessOutput struct{ buffer bytes.Buffer }

func (output *protectionProcessOutput) Bytes() []byte { return output.buffer.Bytes() }

func (output *protectionProcessOutput) Write(data []byte) (int, error) {
	if output.buffer.Len()+len(data) > 1<<20 {
		return 0, errors.New("process listing exceeds limit")
	}
	return output.buffer.Write(data)
}

func parseProtectionProcesses(data []byte, app string) []int {
	seen := make(map[int]bool)
	pids := []int{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		separator := strings.IndexAny(line, " \t")
		if separator < 1 {
			continue
		}
		pid, err := strconv.Atoi(line[:separator])
		comm := strings.TrimSpace(line[separator:])
		// All copies share the user cache. A renamed bundle must also prevent
		// permission changes, even when --app selects another installed copy.
		name := filepath.Base(comm)
		knownName := name == "ZCode" || name == "ZCode Helper" || strings.HasPrefix(name, "ZCode Helper (") && strings.HasSuffix(name, ")")
		otherBundle := filepath.IsAbs(comm) && strings.HasSuffix(filepath.Dir(comm), ".app/Contents/MacOS") && knownName
		bareName := comm == name && knownName
		if err == nil && pid > 0 && !seen[pid] && (bareName || otherBundle || strings.HasPrefix(comm, app+"/Contents/")) {
			pids = append(pids, pid)
			seen[pid] = true
		}
	}
	sort.Ints(pids)
	return pids
}
