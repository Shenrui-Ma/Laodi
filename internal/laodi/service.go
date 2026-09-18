package laodi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

const serviceLabel = "dev.laodi.guardian"
const serviceReceiptName = "service-install.json"
const maxServiceFileBytes = 64 << 10

type ServiceOptions struct {
	Executable string
	Root       string
	StateDir   string
	App        string
	Build      string
	Notifier   string
	Home       string
	HooksOnly  bool
}

// ServicePlan is a read-only preview. Only InstallService/UninstallService run
// launchctl, and the CLI must require explicit application before calling them.
type ServicePlan struct {
	Label       string   `json:"label"`
	Domain      string   `json:"domain"`
	PlistPath   string   `json:"plist_path"`
	ReceiptPath string   `json:"receipt_path"`
	Arguments   []string `json:"arguments"`
	Plist       string   `json:"plist"`
	PlistHash   string   `json:"plist_hash"`
	UID         int      `json:"uid"`
	options     ServiceOptions
	runner      func(context.Context, ...string) error
}

type serviceReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	Label         string    `json:"label"`
	PlistPath     string    `json:"plist_path"`
	PlistHash     string    `json:"plist_hash"`
	UID           int       `json:"uid"`
	InstalledAt   time.Time `json:"installed_at"`
}

func PlanService(options ServiceOptions) (ServicePlan, error) {
	if runtime.GOOS != "darwin" {
		return ServicePlan{}, errors.New("user background installation currently supports macOS only")
	}
	if os.Geteuid() == 0 || os.Getuid() == 0 {
		return ServicePlan{}, errors.New("run Laodi setup as your normal user, without sudo or root")
	}
	var err error
	if options.Home == "" {
		options.Home, err = os.UserHomeDir()
		if err != nil {
			return ServicePlan{}, err
		}
	}
	if !filepath.IsAbs(options.Home) {
		return ServicePlan{}, errors.New("service home must be an absolute path")
	}
	if options.Executable == "" {
		options.Executable, err = os.Executable()
		if err != nil {
			return ServicePlan{}, err
		}
	}
	if options.Root == "" {
		options.Root = filepath.Join(options.Home, ".zcode", "v2", "checkpoints")
	}
	if options.StateDir == "" {
		options.StateDir = filepath.Join(options.Home, "Library", "Application Support", "Laodi-skills")
	}
	if options.App == "" {
		options.App = "/Applications/ZCode.app"
	}
	for _, item := range []struct{ label, value string }{{"executable", options.Executable}, {"root", options.Root}, {"state directory", options.StateDir}, {"app", options.App}, {"notifier", options.Notifier}} {
		if item.value != "" && (!filepath.IsAbs(item.value) || strings.ContainsAny(item.value, "\x00\r\n")) {
			return ServicePlan{}, fmt.Errorf("service %s must be an absolute path without control characters", item.label)
		}
	}
	info, err := os.Stat(options.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return ServicePlan{}, errors.New("service executable must be an existing executable regular file")
	}
	if options.Notifier != "" {
		info, err = os.Stat(options.Notifier)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return ServicePlan{}, errors.New("notifier must be an existing executable regular file")
		}
	}
	args := []string{options.Executable, "watch", "--root", options.Root, "--state-dir", options.StateDir, "--app", options.App}
	if options.HooksOnly {
		args = append(args, "--hooks-only")
	}
	if options.Build != "" {
		args = append(args, "--build", options.Build)
	}
	if options.Notifier != "" {
		args = append(args, "--notifier", options.Notifier)
	}
	var plist strings.Builder
	plist.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>" + serviceLabel + "</string>\n<key>ProgramArguments</key><array>\n")
	for _, arg := range args {
		plist.WriteString("<string>")
		if err := xml.EscapeText(&plist, []byte(arg)); err != nil {
			return ServicePlan{}, err
		}
		plist.WriteString("</string>\n")
	}
	plist.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>ThrottleInterval</key><integer>30</integer>\n<key>StandardOutPath</key><string>/dev/null</string>\n<key>StandardErrorPath</key><string>/dev/null</string>\n<key>ProcessType</key><string>Background</string>\n</dict></plist>\n")
	content := plist.String()
	if len(content) > maxServiceFileBytes {
		return ServicePlan{}, errors.New("service configuration exceeds size limit")
	}
	return ServicePlan{
		Label: serviceLabel, Domain: fmt.Sprintf("gui/%d", os.Getuid()),
		PlistPath:   filepath.Join(options.Home, "Library", "LaunchAgents", serviceLabel+".plist"),
		ReceiptPath: filepath.Join(options.StateDir, serviceReceiptName),
		Arguments:   args, Plist: content, PlistHash: serviceHash([]byte(content)), UID: os.Getuid(),
		options: options, runner: runLaunchctl,
	}, nil
}

// InstallService never overwrites a foreign or edited plist. Repeated setup of
// the identical owned configuration is safe. Configuration changes require an
// explicit uninstall so an existing monitor is not silently interrupted.
func InstallService(plan ServicePlan) error {
	if err := validateServicePlan(plan); err != nil {
		return err
	}
	release, err := lockServiceManagement(plan)
	if err != nil {
		return err
	}
	defer release()
	owned, err := inspectServiceOwnership(plan)
	if err != nil {
		return err
	}
	if owned {
		data, err := readServiceFile(plan.PlistPath)
		if err != nil {
			return err
		}
		if serviceHash(data) != plan.PlistHash {
			return errors.New("Laodi is installed with different settings; explicitly uninstall it before changing the service")
		}
		if err := callService(plan, "print", plan.Domain+"/"+plan.Label); err == nil {
			return nil
		}
		return callService(plan, "bootstrap", plan.Domain, plan.PlistPath)
	}
	if err := writeNewServiceFile(plan.PlistPath, []byte(plan.Plist)); err != nil {
		return err
	}
	receipt := serviceReceipt{SchemaVersion: SchemaVersion, Label: plan.Label, PlistPath: plan.PlistPath, PlistHash: plan.PlistHash, UID: plan.UID, InstalledAt: time.Now().UTC()}
	data, err := json.Marshal(receipt)
	if err == nil {
		err = writeNewServiceFile(plan.ReceiptPath, append(data, '\n'))
	}
	if err != nil {
		rollbackErr := removeServiceFile(plan.PlistPath, plan.PlistHash)
		return errors.Join(fmt.Errorf("save service ownership receipt: %w", err), rollbackErr)
	}
	if err := callService(plan, "bootstrap", plan.Domain, plan.PlistPath); err != nil {
		// The new service was not accepted. Remove only the exact files this
		// invocation wrote; a raced edit remains intact and is reported.
		plistErr := removeServiceFile(plan.PlistPath, plan.PlistHash)
		var receiptErr error
		if plistErr == nil {
			receiptErr = removeServiceFile(plan.ReceiptPath, serviceHash(append(data, '\n')))
		}
		return errors.Join(err, plistErr, receiptErr)
	}
	return nil
}

func UninstallService(plan ServicePlan) error {
	if err := validateServicePlan(plan); err != nil {
		return err
	}
	release, err := lockServiceManagement(plan)
	if err != nil {
		return err
	}
	defer release()
	owned, err := inspectServiceOwnership(plan)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("no owned Laodi service is installed")
	}
	receiptData, err := readServiceFile(plan.ReceiptPath)
	if err != nil {
		return err
	}
	var receipt serviceReceipt
	if err := json.Unmarshal(receiptData, &receipt); err != nil {
		return err
	}
	// A failed stop never turns into deletion of a potentially running service.
	if err := callService(plan, "bootout", plan.Domain+"/"+plan.Label); err != nil {
		return err
	}
	if err := removeServiceFile(plan.PlistPath, receipt.PlistHash); err != nil {
		return err
	}
	return removeServiceFile(plan.ReceiptPath, serviceHash(receiptData))
}

func validateServicePlan(plan ServicePlan) error {
	expected, err := PlanService(plan.options)
	if err != nil {
		return err
	}
	if plan.UID != expected.UID || plan.Label != expected.Label || plan.Domain != expected.Domain || plan.PlistPath != expected.PlistPath || plan.ReceiptPath != expected.ReceiptPath || plan.PlistHash != expected.PlistHash || plan.Plist != expected.Plist || !reflect.DeepEqual(plan.Arguments, expected.Arguments) || plan.runner == nil {
		return errors.New("service plan changed; generate a fresh plan")
	}
	return nil
}

func lockServiceManagement(plan ServicePlan) (func(), error) {
	root, err := openStateRoot(plan.options.StateDir, true)
	if err != nil {
		return nil, err
	}
	root.Close()
	// Independent of the monitor's lock, so a running monitor can be uninstalled.
	return AcquireLock(filepath.Join(plan.options.StateDir, "service-management"))
}

func inspectServiceOwnership(plan ServicePlan) (bool, error) {
	plist, plistErr := readServiceFile(plan.PlistPath)
	receiptData, receiptErr := readServiceFile(plan.ReceiptPath)
	if errors.Is(plistErr, os.ErrNotExist) && errors.Is(receiptErr, os.ErrNotExist) {
		return false, nil
	}
	if plistErr != nil || receiptErr != nil {
		return false, errors.New("service files are foreign, incomplete, or unreadable; refusing to overwrite them")
	}
	var receipt serviceReceipt
	decoder := json.NewDecoder(bytes.NewReader(receiptData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return false, errors.New("invalid service ownership receipt")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return false, errors.New("invalid service ownership receipt trailing data")
	}
	if receipt.SchemaVersion != SchemaVersion || receipt.Label != plan.Label || receipt.PlistPath != plan.PlistPath || receipt.UID != plan.UID || receipt.PlistHash != serviceHash(plist) {
		return false, errors.New("service ownership or plist hash does not match; refusing to change it")
	}
	return true, nil
}

func callService(plan ServicePlan, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := plan.runner(ctx, args...); err != nil {
		return fmt.Errorf("launchctl %s failed: %w", args[0], err)
	}
	return nil
}

func runLaunchctl(ctx context.Context, args ...string) error {
	// No shell, sudo, environment interpolation, or unbounded captured output.
	return exec.CommandContext(ctx, "/bin/launchctl", args...).Run()
}

func serviceHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func readServiceFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openStateFile(root, filepath.Base(path), os.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxServiceFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxServiceFileBytes {
		return nil, errors.New("service file exceeds size limit")
	}
	return data, nil
}

func serviceParent(path string) (*os.Root, error) {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() {
		return nil, errors.New("service parent must be a directory, not a symbolic link")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("service parent changed while opening")
	}
	return root, nil
}

func writeNewServiceFile(path string, data []byte) error {
	root, err := serviceParent(path)
	if err != nil {
		return err
	}
	defer root.Close()
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	name := ".laodi-" + hex.EncodeToString(suffix[:]) + ".tmp"
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
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
	// Link is an atomic, no-clobber publish: a newly appeared foreign plist can
	// never be replaced between the ownership check and this operation.
	if err := root.Link(name, filepath.Base(path)); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func removeServiceFile(path, expectedHash string) error {
	data, err := readServiceFile(path)
	if err != nil {
		return err
	}
	if serviceHash(data) != expectedHash {
		return errors.New("service file was edited; refusing to remove it")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(filepath.Base(path))
}
