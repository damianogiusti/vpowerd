// Package ups holds the protocol-independent domain model of a UPS.
package ups

import "time"

// Physical units. Distinct types make it a compile error to put a frequency
// where a voltage is expected; JSON encoding stays a plain number.
type (
	Volts   float64
	Amperes float64
	Hertz   float64
	Percent float64
	Celsius float64
	// VoltAmperes is the apparent power rating, e.g. 800 for an 800VA unit.
	VoltAmperes int
	// Seconds is used instead of time.Duration because time.Duration encodes
	// to JSON as nanoseconds, which is hostile to Home Assistant templates.
	Seconds int
)

// Protocol identifies the wire dialect detected on the device.
// Values match the protocol IDs used by ViewPower so logs can be compared.
type Protocol string

const (
	ProtocolUnknown Protocol = ""
	// ProtocolPMV is the Voltronic/Megatec dialect answering "V" to "M".
	ProtocolPMV Protocol = "PMV"
	// ProtocolPMVH, PMVI and PMVJ answer "H", "I" and "J" to "M" and report
	// battery capacity and runtime through "QI".
	ProtocolPMVH Protocol = "PMV_H"
	ProtocolPMVI Protocol = "PMV_I"
	ProtocolPMVJ Protocol = "PMV_J"
	// ProtocolMegatec is not a ViewPower protocol: it is the fallback used when
	// the device does not answer "M" but answers the classic "Q1" query.
	ProtocolMegatec Protocol = "MEGATEC"
)

// ReportsCapacity tells whether the dialect reports battery capacity and
// runtime itself ("QI"), instead of us estimating it from battery voltage.
func (p Protocol) ReportsCapacity() bool {
	switch p {
	case ProtocolPMVH, ProtocolPMVI, ProtocolPMVJ:
		return true
	default:
		return false
	}
}

// WorkMode is the operating mode derived from the status bits.
type WorkMode string

const (
	ModeUnknown     WorkMode = "unknown"
	ModeStandby     WorkMode = "standby"
	ModeLine        WorkMode = "line"
	ModeBattery     WorkMode = "battery"
	ModeBatteryTest WorkMode = "battery_test"
	ModeFault       WorkMode = "fault"
)

// OnBattery is true when the load is fed by the battery.
func (m WorkMode) OnBattery() bool {
	return m == ModeBattery || m == ModeBatteryTest
}

// ChargeSource tells where the battery charge value comes from.
type ChargeSource string

const (
	ChargeReported  ChargeSource = "reported"
	ChargeEstimated ChargeSource = "estimated"
)

// StatusFlags are the 8 status bits of the QS/Q1 reply, b7 first.
type StatusFlags struct {
	UtilityFail     bool `json:"utility_fail"`
	BatteryLow      bool `json:"battery_low"`
	BypassBoost     bool `json:"bypass_boost_active"`
	UPSFailed       bool `json:"ups_failed"`
	LineInteractive bool `json:"line_interactive"`
	SelfTest        bool `json:"self_test_in_progress"`
	ShutdownActive  bool `json:"shutdown_active"`
	BeeperOn        bool `json:"beeper_on"`
}

// Status is one polled measurement.
type Status struct {
	Time              time.Time    `json:"time"`
	Mode              WorkMode     `json:"mode"`
	InputVoltage      Volts        `json:"input_voltage"`
	InputFaultVoltage Volts        `json:"input_fault_voltage"`
	OutputVoltage     Volts        `json:"output_voltage"`
	OutputFrequency   Hertz        `json:"output_frequency"`
	LoadPercent       Percent      `json:"load_percent"`
	BatteryVoltage    Volts        `json:"battery_voltage"`
	BatteryCharge     Percent      `json:"battery_charge"`
	ChargeSource      ChargeSource `json:"battery_charge_source"`
	// RuntimeRemaining is nil when the dialect does not report it.
	RuntimeRemaining *Seconds `json:"runtime_remaining,omitempty"`
	// Temperature is nil when the reply has no parsable value.
	Temperature *Celsius    `json:"temperature,omitempty"`
	Flags       StatusFlags `json:"flags"`
}

// Rating is the nominal data from the "F" query.
type Rating struct {
	Voltage        Volts   `json:"voltage"`
	Current        Amperes `json:"current"`
	BatteryVoltage Volts   `json:"battery_voltage"`
	Frequency      Hertz   `json:"frequency"`
}

// Identity describes the device. Empty strings mean "not reported".
type Identity struct {
	Protocol Protocol `json:"protocol"`
	Model    string   `json:"model,omitempty"`
	Firmware string   `json:"firmware,omitempty"`
	Serial   string   `json:"serial,omitempty"`
	// RatedPower is nil when the device does not report it.
	RatedPower *VoltAmperes `json:"rated_power,omitempty"`
}

// CommandKind lists the controls the daemon can send.
type CommandKind string

const (
	CmdBeeperToggle   CommandKind = "beeper_toggle"
	CmdTest10s        CommandKind = "test_10s"
	CmdTestCancel     CommandKind = "test_cancel"
	CmdShutdown       CommandKind = "shutdown"
	CmdShutdownCancel CommandKind = "shutdown_cancel"
)

// Command is a control request. Delays are only used by CmdShutdown.
type Command struct {
	Kind CommandKind `json:"kind"`
	// ShutdownDelay is how long the UPS waits before cutting the output.
	ShutdownDelay time.Duration `json:"-"`
	// RestoreAfter is how long the UPS waits before restoring the output.
	// Zero means "do not restore automatically".
	RestoreAfter time.Duration `json:"-"`
}

// ParseCommandKind validates an external string, e.g. from HTTP or MQTT.
func ParseCommandKind(s string) (CommandKind, bool) {
	switch k := CommandKind(s); k {
	case CmdBeeperToggle, CmdTest10s, CmdTestCancel, CmdShutdown, CmdShutdownCancel:
		return k, true
	default:
		return "", false
	}
}
