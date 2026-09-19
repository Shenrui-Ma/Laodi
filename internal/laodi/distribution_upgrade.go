package laodi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

const upgradeJournalName = "runtime-upgrade.json"

type upgradeJournal struct {
	Version      int               `json:"version"`
	Stage        string            `json:"stage"`
	Old          map[string]string `json:"old"`
	New          map[string]string `json:"new"`
	ServiceOwned bool              `json:"service_owned"`
}

func ownedDistributionFiles(dir string) (map[string]string, error) {
	if _, err := os.Lstat(dir); err != nil {
		return nil, err
	}
	data, err := readServiceFile(filepath.Join(dir, distributionReceiptName))
	if err != nil {
		return nil, errors.New("runtime has no valid ownership receipt; refusing to overwrite it")
	}
	var receipt distributionReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&receipt); err != nil || receipt.Version != 1 {
		return nil, errors.New("invalid runtime ownership receipt")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid runtime receipt trailing data")
	}
	files, err := distributionFiles(dir, true)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(files, receipt.Files) {
		return nil, errors.New("installed runtime was edited; refusing to change it")
	}
	return files, nil
}

func inspectUpgradeJournal(plan DistributionPlan) (upgradeJournal, error) {
	var j upgradeJournal
	data, err := readServiceFile(filepath.Join(plan.StateDir, upgradeJournalName))
	if err != nil {
		return j, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return j, errors.New("invalid upgrade recovery journal")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return j, errors.New("invalid upgrade journal trailing data")
	}
	if j.Version != 1 || !strings.HasPrefix(j.Stage, ".runtime-upgrade-") || filepath.Base(j.Stage) != j.Stage || strings.ContainsAny(j.Stage, "\\\x00\r\n") || len(j.Stage) > 80 {
		return j, errors.New("invalid upgrade recovery directory")
	}
	installed, err := ownedDistributionFiles(plan.RuntimeDir)
	if err != nil {
		return j, err
	}
	staged, err := ownedDistributionFiles(filepath.Join(plan.StateDir, j.Stage))
	if err != nil {
		return j, fmt.Errorf("upgrade backup cannot be verified: %w", err)
	}
	if !((reflect.DeepEqual(installed, j.Old) && reflect.DeepEqual(staged, j.New)) || (reflect.DeepEqual(installed, j.New) && reflect.DeepEqual(staged, j.Old))) {
		return j, errors.New("upgrade files differ from the recovery journal; files retained")
	}
	return j, nil
}

// Lock order: distribution -> service -> hooks. The monitor and inbox locks
// remain independent, so Agent callbacks can enqueue throughout an update.
func lockDistributionUpgrade(plan DistributionPlan) (func(), error) {
	service, err := AcquireLock(filepath.Join(plan.StateDir, "service-management"))
	if err != nil {
		return nil, err
	}
	hooks, err := AcquireLock(filepath.Join(plan.StateDir, "hook-management"))
	if err != nil {
		service()
		return nil, err
	}
	return func() { hooks(); service() }, nil
}

func inspectUpgradeHooks(plan DistributionPlan) ([]string, error) {
	adapters := []string{}
	for _, adapter := range []string{"zcode", "claude-code"} {
		hook, err := PlanHookConfig(HookConfigOptions{Adapter: adapter, Executable: plan.Executable, StateDir: plan.StateDir, Home: plan.options.Home})
		if err != nil {
			return nil, err
		}
		data, err := readServiceFile(hook.ReceiptPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		receipt, err := decodeHookReceipt(data, hook)
		if err != nil {
			return nil, err
		}
		configData, _, err := readHookConfigFile(hook.ConfigPath)
		if err != nil {
			return nil, err
		}
		config, err := decodeHookConfig(configData)
		if err != nil {
			return nil, err
		}
		_, events, err := hookConfigMaps(config, adapter, false)
		if err != nil || !ownedHookEntries(events, receipt) {
			return nil, errors.New("installed hooks were edited; update left configuration unchanged")
		}
		if err := checkHookEnabled(config, adapter); err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	return adapters, nil
}

func waitDistributionHealth(dir string, after time.Time) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, err := LoadState(dir)
		if err == nil && state.Running && state.LastCheckedAt.After(after) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("updated monitor did not publish a fresh heartbeat")
}

func stopUpgradeService(service ServicePlan) error {
	if err := callService(service, "print", service.Domain+"/"+service.Label); err == nil {
		if err := callService(service, "bootout", service.Domain+"/"+service.Label); err != nil {
			return err
		}
	}
	// bootout can return while SIGTERM cleanup is finishing. Wait for the
	// monitor's lock without copying or rolling back its live state database.
	deadline := time.Now().Add(12 * time.Second)
	for {
		release, err := AcquireLock(service.options.StateDir)
		if err == nil {
			release()
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return fmt.Errorf("monitor has not stopped: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func startUpgradeService(plan DistributionPlan, service ServicePlan) error {
	started := time.Now().UTC()
	if err := callService(service, "bootstrap", service.Domain, service.PlistPath); err != nil {
		return err
	}
	if err := plan.healthCheck(plan.StateDir, started); err != nil {
		return err
	}
	return callService(service, "print", service.Domain+"/"+service.Label)
}

func exchangeUpgrade(plan DistributionPlan, stage string) error {
	root, err := os.OpenRoot(plan.StateDir)
	if err != nil {
		return err
	}
	defer root.Close()
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := swapRuntimeDirectories(parent, "runtime", stage); err != nil {
		return fmt.Errorf("atomic runtime exchange failed: %w", err)
	}
	return parent.Sync()
}

func clearUpgradeJournal(plan DistributionPlan, j upgradeJournal) error {
	// A changed backup must not be deleted, even after a successful startup.
	if _, err := inspectUpgradeJournal(plan); err != nil {
		return err
	}
	root, err := os.OpenRoot(plan.StateDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(upgradeJournalName); err != nil {
		return err
	}
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return err
	}
	// If the process stops here, an orphaned private backup is harmless. The
	// journal must be durably gone before removing recovery material.
	return root.RemoveAll(j.Stage)
}

func rollbackDistributionUpgrade(plan DistributionPlan) error {
	j, err := inspectUpgradeJournal(plan)
	if err != nil {
		return err
	}
	service, err := distributionService(plan)
	if err != nil {
		return err
	}
	owned, err := inspectServiceOwnership(service)
	if err != nil {
		return err
	}
	if owned != j.ServiceOwned {
		return errors.New("service ownership changed during update; recovery files retained")
	}
	if owned {
		data, err := readServiceFile(service.PlistPath)
		if err != nil || serviceHash(data) != service.PlistHash {
			return errors.New("service configuration changed during update; recovery files retained")
		}
		if err := stopUpgradeService(service); err != nil {
			return err
		}
	}
	current, err := ownedDistributionFiles(plan.RuntimeDir)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(current, j.New) {
		if err := exchangeUpgrade(plan, j.Stage); err != nil {
			return err
		}
	}
	if owned {
		if err := startUpgradeService(plan, service); err != nil {
			return fmt.Errorf("old runtime restored but monitor restart failed: %w", err)
		}
	}
	return clearUpgradeJournal(plan, j)
}

func recoverDistributionUpgrade(plan DistributionPlan) error {
	unlock, err := lockDistributionUpgrade(plan)
	if err != nil {
		return err
	}
	defer unlock()
	if plan.healthCheck == nil {
		return errors.New("upgrade health check is missing")
	}
	return rollbackDistributionUpgrade(plan)
}

func upgradeDistribution(plan DistributionPlan) (result DistributionResult, err error) {
	result = DistributionResult{RuntimeInstalled: true, NotificationStatus: "not_requested", HookAdapters: []string{}}
	unlock, err := lockDistributionUpgrade(plan)
	if err != nil {
		return result, err
	}
	defer unlock()
	if plan.healthCheck == nil {
		return result, errors.New("upgrade health check is missing")
	}
	if err := preflightDistribution(plan); err != nil {
		return result, err
	}
	adapters, err := inspectUpgradeHooks(plan)
	if err != nil {
		return result, err
	}
	result.HookAdapters = adapters
	old, err := ownedDistributionFiles(plan.RuntimeDir)
	if err != nil {
		return result, err
	}
	service, err := distributionService(plan)
	if err != nil {
		return result, err
	}
	owned, err := inspectServiceOwnership(service)
	if err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(plan.StateDir, ".runtime-upgrade-")
	if err != nil {
		return result, err
	}
	// installDistributionPayload creates its destination; reserve the unique
	// name first, then create a complete, independently verified runtime there.
	if err := os.Remove(stage); err != nil {
		return result, err
	}
	staging := plan
	staging.RuntimeDir = stage
	if err := installDistributionPayload(staging); err != nil {
		os.RemoveAll(stage)
		return result, err
	}
	staged, err := ownedDistributionFiles(stage)
	if err != nil || !reflect.DeepEqual(staged, plan.files) {
		os.RemoveAll(stage)
		return result, errors.New("staged runtime verification failed")
	}
	j := upgradeJournal{Version: 1, Stage: filepath.Base(stage), Old: old, New: plan.files, ServiceOwned: owned}
	data, _ := json.Marshal(j)
	if len(data) > maxServiceFileBytes {
		os.RemoveAll(stage)
		return result, errors.New("upgrade journal exceeds size limit")
	}
	if err := publishUpgradeJournal(plan, j, data, writeNewServiceFile); err != nil {
		return result, err
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			if recovery := rollbackDistributionUpgrade(plan); recovery != nil {
				err = errors.Join(err, fmt.Errorf("automatic recovery failed; retained upgrade journal: %w", recovery))
			} else {
				result.Updated = false
				result.ServiceInstalled = owned
				err = fmt.Errorf("update failed; previous runtime restored: %w", err)
			}
		}
	}()
	if owned {
		if err = stopUpgradeService(service); err != nil {
			return result, err
		}
	}
	if _, err = inspectUpgradeJournal(plan); err != nil {
		return result, err
	}
	if err = exchangeUpgrade(plan, j.Stage); err != nil {
		return result, err
	}
	if owned {
		if err = startUpgradeService(plan, service); err != nil {
			return result, err
		}
	}
	result.Updated = true
	result.ServiceInstalled = owned
	if err = clearUpgradeJournal(plan, j); err != nil {
		// If the journal was already removed, only backup cleanup failed. The
		// healthy runtime remains current and must not be rolled back blindly.
		if _, statErr := os.Lstat(filepath.Join(plan.StateDir, upgradeJournalName)); errors.Is(statErr, os.ErrNotExist) {
			committed = true
			result.Warnings = append(result.Warnings, "更新已完成；旧版本临时备份未能清理。")
			err = nil
		}
		return result, err
	}
	committed = true
	if owned {
		result.Warnings = append(result.Warnings, "已保留现有配置和事件记录；仅重启老底监测进程，Agent 会话未重启。")
	} else {
		result.Warnings = append(result.Warnings, "程序已更新；后台服务尚未注册。需要接入时再次运行安装命令。")
	}
	if plan.RequestNotifications {
		query := plan
		query.RequestNotifications = false
		result.NotificationStatus, err = distributionNotifications(query)
		if err != nil {
			result.Warnings = append(result.Warnings, "更新已完成，通知状态暂未确认。")
			err = nil
		}
		if result.NotificationStatus == "denied" || result.NotificationStatus == "not_determined" {
			result.Warnings = append(result.Warnings, "通知未开启；需要提醒时，在系统设置 → 通知 → 老底中开启。")
		}
	}
	return result, nil
}

func publishUpgradeJournal(plan DistributionPlan, j upgradeJournal, data []byte, write func(string, []byte) error) error {
	path := filepath.Join(plan.StateDir, upgradeJournalName)
	if err := write(path, data); err != nil {
		// Publication may have succeeded before the directory fsync failed.
		// Keep its recovery material unless the journal is definitely absent.
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			os.RemoveAll(filepath.Join(plan.StateDir, j.Stage))
		}
		return err
	}
	return nil
}
