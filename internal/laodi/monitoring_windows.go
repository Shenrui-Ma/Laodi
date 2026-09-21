//go:build windows

package laodi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"time"
)

// AddMonitoringSummary is read-only. It does not discover arbitrary user files,
// install hooks, request permission, or send notifications. Contract checks use
// only the paths in the owned integration receipt and fail closed on upgrades.
func AddMonitoringSummary(summary *AgentSummary, stateDir string) {
	now := time.Now().UTC()
	m := &MonitoringSummary{CheckedAt: now, Background: "identity_or_heartbeat_unverified"}
	if windowsDistributionHealthy(stateDir, now.Add(-75*time.Second)) == nil {
		m.Background = "verified_running"
	}
	integration, err := readWindowsIntegration(stateDir)
	for _, adapter := range []string{"zcode", "claude-code"} {
		client := ClientMonitoringSummary{Adapter: adapter, Contract: "not_configured", Hooks: "not_configured", Callback: getHookObservationSummary(stateDir, adapter)}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			client.Contract, client.Hooks = "receipt_unavailable", "unverified"
		} else if err == nil {
			for _, installed := range integration.Clients {
				if installed.Adapter != adapter {
					continue
				}
				client.Contract = "changed_or_unverified"
				if current, e := DetectWindowsHookContract(adapter, installed.Executable); e == nil && reflect.DeepEqual(current, installed) {
					client.Contract = "verified"
				}
				client.Hooks = "missing_disabled_or_edited"
				plan := DistributionPlan{StateDir: stateDir, WindowsClients: []WindowsHookContract{installed}, options: DistributionOptions{Home: integration.Home}}
				if preflightWindowsDistributionHooks(plan) == nil {
					client.Hooks = "configured"
				}
			}
		}
		m.Clients = append(m.Clients, client)
	}
	m.Notifications = NotificationMonitoringSummary{Preference: "unavailable", Authorization: "unknown", Visibility: "unconfirmed"}
	if enabled, e := windowsNotificationsEnabled(stateDir); e == nil {
		m.Notifications.Preference = "disabled"
		if enabled {
			m.Notifications.Preference = "enabled"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if data, e := ManageWindowsNotifications(ctx, stateDir, "status"); e == nil && len(data) <= 16384 {
		var response struct {
			Authorization string `json:"authorization"`
		}
		if json.Unmarshal(data, &response) == nil {
			switch response.Authorization {
			case "enabled", "disabled_for_application", "disabled_for_user", "disabled_by_group_policy", "disabled_by_manifest", "not_registered", "recovery_pending":
				m.Notifications.Authorization = response.Authorization
			}
		}
	}
	summary.Monitoring = m
	if m.Notifications.Authorization == "enabled" || m.Notifications.Authorization == "disabled_for_application" || m.Notifications.Authorization == "disabled_for_user" || m.Notifications.Authorization == "disabled_by_group_policy" || m.Notifications.Authorization == "disabled_by_manifest" {
		unknowns := summary.Unknowns[:0]
		for _, value := range summary.Unknowns {
			if value != "system_notification_permission_not_checked" {
				unknowns = append(unknowns, value)
			}
		}
		summary.Unknowns = unknowns
	}
	summary.Unknowns = appendUnknown(summary.Unknowns, "callback_samples_are_best_effort_not_current_session_delivery", "callback_caller_not_authenticated", "notification_visibility_not_confirmed")
}
