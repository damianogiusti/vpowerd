package mqtt

import (
	"github.com/damianogiusti/vpowerd/internal/monitor"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

// component is a Home Assistant MQTT platform.
type component string

const (
	componentSensor       component = "sensor"
	componentBinarySensor component = "binary_sensor"
	componentButton       component = "button"
)

// entity describes one Home Assistant entity.
type entity struct {
	component      component
	key            string
	name           string
	unit           string
	deviceClass    string
	stateClass     string
	icon           string
	entityCategory string
	valueTemplate  string
	options        []string
	// payloadOn is used by binary sensors; buttons send their key.
	payloadOn  string
	payloadOff string
	// include decides, from the first connected snapshot, whether the
	// device can provide the entity at all.
	include func(monitor.Snapshot) bool
}

func always(monitor.Snapshot) bool { return true }

func hasRuntime(s monitor.Snapshot) bool {
	return s.Identity != nil && s.Identity.Protocol.ReportsCapacity()
}

func hasTemperature(s monitor.Snapshot) bool {
	return s.Status != nil && s.Status.Temperature != nil
}

func modeOptions() []string {
	modes := []ups.WorkMode{ups.ModeUnknown, ups.ModeStandby, ups.ModeLine, ups.ModeBattery, ups.ModeBatteryTest, ups.ModeFault}
	out := make([]string, len(modes))
	for i, m := range modes {
		out[i] = string(m)
	}
	return out
}

func sensorEntities() []entity {
	return []entity{
		{component: componentSensor, key: "mode", name: "Mode", deviceClass: "enum", options: modeOptions(), icon: "mdi:power-plug-battery", include: always},
		{component: componentSensor, key: "input_voltage", name: "Input voltage", unit: "V", deviceClass: "voltage", stateClass: "measurement", include: always},
		{component: componentSensor, key: "input_fault_voltage", name: "Input fault voltage", unit: "V", deviceClass: "voltage", stateClass: "measurement", entityCategory: "diagnostic", include: always},
		{component: componentSensor, key: "output_voltage", name: "Output voltage", unit: "V", deviceClass: "voltage", stateClass: "measurement", include: always},
		{component: componentSensor, key: "output_frequency", name: "Output frequency", unit: "Hz", deviceClass: "frequency", stateClass: "measurement", include: always},
		{component: componentSensor, key: "load_percent", name: "Load", unit: "%", stateClass: "measurement", icon: "mdi:gauge", include: always},
		{component: componentSensor, key: "battery_voltage", name: "Battery voltage", unit: "V", deviceClass: "voltage", stateClass: "measurement", include: always},
		{component: componentSensor, key: "battery_charge", name: "Battery charge", unit: "%", deviceClass: "battery", stateClass: "measurement", include: always},
		{component: componentSensor, key: "runtime_remaining", name: "Runtime remaining", unit: "s", deviceClass: "duration", stateClass: "measurement", include: hasRuntime},
		{component: componentSensor, key: "temperature", name: "Temperature", unit: "°C", deviceClass: "temperature", stateClass: "measurement", include: hasTemperature},
		{component: componentBinarySensor, key: "utility_power", name: "Utility power", deviceClass: "power", payloadOn: "true", payloadOff: "false",
			valueTemplate: "{{ 'false' if value_json.on_battery else 'true' }}", include: always},
		{component: componentBinarySensor, key: "battery_low", name: "Battery low", deviceClass: "battery", payloadOn: "true", payloadOff: "false",
			valueTemplate: "{{ value_json.battery_low | lower }}", include: always},
		{component: componentBinarySensor, key: "fault", name: "Fault", deviceClass: "problem", payloadOn: "true", payloadOff: "false",
			valueTemplate: "{{ value_json.fault | lower }}", include: always},
		{component: componentBinarySensor, key: "self_test", name: "Self test", deviceClass: "running", payloadOn: "true", payloadOff: "false",
			valueTemplate: "{{ value_json.self_test | lower }}", entityCategory: "diagnostic", include: always},
		{component: componentBinarySensor, key: "beeper", name: "Beeper", deviceClass: "sound", payloadOn: "true", payloadOff: "false",
			valueTemplate: "{{ value_json.beeper_on | lower }}", entityCategory: "diagnostic", include: always},
	}
}

// buttonEntities are published only when control is enabled. Shutdown is
// deliberately not a button: one accidental tap would cut the load.
func buttonEntities() []entity {
	return []entity{
		{component: componentButton, key: string(ups.CmdTest10s), name: "Battery test 10 s", icon: "mdi:battery-sync", entityCategory: "config", include: always},
		{component: componentButton, key: string(ups.CmdTestCancel), name: "Cancel battery test", icon: "mdi:battery-remove", entityCategory: "config", include: always},
		{component: componentButton, key: string(ups.CmdBeeperToggle), name: "Toggle beeper", icon: "mdi:volume-off", entityCategory: "config", include: always},
		{component: componentButton, key: string(ups.CmdShutdownCancel), name: "Cancel shutdown", icon: "mdi:power-cycle", entityCategory: "config", include: always},
	}
}

// discoveryDevice is the "device" block of a discovery payload.
type discoveryDevice struct {
	Identifiers []string `json:"identifiers"`
	Name        string   `json:"name"`
	Model       string   `json:"model,omitempty"`
	SWVersion   string   `json:"sw_version,omitempty"`
	SerialNum   string   `json:"serial_number,omitempty"`
}

// discoveryPayload covers the keys used by sensor, binary_sensor and button.
type discoveryPayload struct {
	Name              string          `json:"name"`
	UniqueID          string          `json:"unique_id"`
	ObjectID          string          `json:"object_id"`
	StateTopic        string          `json:"state_topic,omitempty"`
	CommandTopic      string          `json:"command_topic,omitempty"`
	PayloadPress      string          `json:"payload_press,omitempty"`
	ValueTemplate     string          `json:"value_template,omitempty"`
	PayloadOn         string          `json:"payload_on,omitempty"`
	PayloadOff        string          `json:"payload_off,omitempty"`
	Unit              string          `json:"unit_of_measurement,omitempty"`
	DeviceClass       string          `json:"device_class,omitempty"`
	StateClass        string          `json:"state_class,omitempty"`
	Icon              string          `json:"icon,omitempty"`
	EntityCategory    string          `json:"entity_category,omitempty"`
	Options           []string        `json:"options,omitempty"`
	AvailabilityTopic string          `json:"availability_topic"`
	Device            discoveryDevice `json:"device"`
}

// stateMessage is the flat JSON published on the state topic, so Home
// Assistant templates stay simple.
type stateMessage struct {
	Mode              ups.WorkMode     `json:"mode"`
	OnBattery         bool             `json:"on_battery"`
	InputVoltage      ups.Volts        `json:"input_voltage"`
	InputFaultVoltage ups.Volts        `json:"input_fault_voltage"`
	OutputVoltage     ups.Volts        `json:"output_voltage"`
	OutputFrequency   ups.Hertz        `json:"output_frequency"`
	LoadPercent       ups.Percent      `json:"load_percent"`
	BatteryVoltage    ups.Volts        `json:"battery_voltage"`
	BatteryCharge     ups.Percent      `json:"battery_charge"`
	ChargeSource      ups.ChargeSource `json:"battery_charge_source"`
	RuntimeRemaining  *ups.Seconds     `json:"runtime_remaining"`
	Temperature       *ups.Celsius     `json:"temperature"`
	BatteryLow        bool             `json:"battery_low"`
	Fault             bool             `json:"fault"`
	SelfTest          bool             `json:"self_test"`
	BeeperOn          bool             `json:"beeper_on"`
	BypassBoost       bool             `json:"bypass_boost_active"`
}

func newStateMessage(s ups.Status) stateMessage {
	return stateMessage{
		Mode:              s.Mode,
		OnBattery:         s.Mode.OnBattery(),
		InputVoltage:      s.InputVoltage,
		InputFaultVoltage: s.InputFaultVoltage,
		OutputVoltage:     s.OutputVoltage,
		OutputFrequency:   s.OutputFrequency,
		LoadPercent:       s.LoadPercent,
		BatteryVoltage:    s.BatteryVoltage,
		BatteryCharge:     s.BatteryCharge,
		ChargeSource:      s.ChargeSource,
		RuntimeRemaining:  s.RuntimeRemaining,
		Temperature:       s.Temperature,
		BatteryLow:        s.Flags.BatteryLow,
		Fault:             s.Flags.UPSFailed,
		SelfTest:          s.Flags.SelfTest,
		BeeperOn:          s.Flags.BeeperOn,
		BypassBoost:       s.Flags.BypassBoost,
	}
}
