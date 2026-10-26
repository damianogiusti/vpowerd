package monitor

import (
	"fmt"
	"time"

	"github.com/damianogiusti/vpowerd/internal/ups"
)

// EventKind classifies an event in the log.
type EventKind string

const (
	EventConnected      EventKind = "connected"
	EventDisconnected   EventKind = "disconnected"
	EventOnBattery      EventKind = "on_battery"
	EventPowerRestored  EventKind = "power_restored"
	EventBatteryLow     EventKind = "battery_low"
	EventFault          EventKind = "fault"
	EventFaultCleared   EventKind = "fault_cleared"
	EventSelfTestStart  EventKind = "self_test_started"
	EventSelfTestEnd    EventKind = "self_test_ended"
	EventShutdownActive EventKind = "shutdown_active"
	EventCommand        EventKind = "command"
)

// Event is one entry of the event log.
type Event struct {
	Time    time.Time `json:"time"`
	Kind    EventKind `json:"kind"`
	Message string    `json:"message,omitempty"`
}

// diffEvents compares two consecutive samples. prev is nil for the first
// sample after a connection, which only reports conditions already active.
func diffEvents(prev *ups.Status, cur ups.Status) []Event {
	var events []Event
	add := func(kind EventKind, msg string) {
		events = append(events, Event{Time: cur.Time, Kind: kind, Message: msg})
	}
	var was ups.StatusFlags
	if prev != nil {
		was = prev.Flags
	}
	now := cur.Flags

	switch {
	case now.UtilityFail && !was.UtilityFail:
		add(EventOnBattery, fmt.Sprintf("input %.1f V", cur.InputVoltage))
	case !now.UtilityFail && was.UtilityFail:
		add(EventPowerRestored, fmt.Sprintf("input %.1f V", cur.InputVoltage))
	}
	if now.BatteryLow && !was.BatteryLow {
		add(EventBatteryLow, fmt.Sprintf("battery %.2f V", cur.BatteryVoltage))
	}
	switch {
	case now.UPSFailed && !was.UPSFailed:
		add(EventFault, "")
	case !now.UPSFailed && was.UPSFailed:
		add(EventFaultCleared, "")
	}
	switch {
	case now.SelfTest && !was.SelfTest:
		add(EventSelfTestStart, "")
	case !now.SelfTest && was.SelfTest:
		add(EventSelfTestEnd, fmt.Sprintf("battery %.2f V", cur.BatteryVoltage))
	}
	if now.ShutdownActive && !was.ShutdownActive {
		add(EventShutdownActive, "")
	}
	return events
}
