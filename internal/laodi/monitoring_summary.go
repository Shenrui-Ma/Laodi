package laodi

import "time"

// These closed metadata fields describe independent checks. They deliberately
// do not combine into a claim that a tool or a current session is protected.
type MonitoringSummary struct {
	CheckedAt     time.Time                     `json:"checked_at"`
	Background    string                        `json:"background"`
	Clients       []ClientMonitoringSummary     `json:"clients"`
	Notifications NotificationMonitoringSummary `json:"notifications"`
}

type ClientMonitoringSummary struct {
	Adapter  string                 `json:"adapter"`
	Contract string                 `json:"contract"`
	Hooks    string                 `json:"hooks"`
	Callback HookObservationSummary `json:"callback"`
}

type NotificationMonitoringSummary struct {
	Preference    string `json:"preference"`
	Authorization string `json:"authorization"`
	Visibility    string `json:"visibility"`
}

type HookObservationSample struct {
	Category string    `json:"category"`
	At       time.Time `json:"at"`
}

type HookObservationSummary struct {
	Status     string                  `json:"status"`
	LastSample *time.Time              `json:"last_sample,omitempty"`
	Samples    []HookObservationSample `json:"samples,omitempty"`
}
