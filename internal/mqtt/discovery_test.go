package mqtt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damianogiusti/vpowerd/internal/monitor"
	"github.com/damianogiusti/vpowerd/internal/settings"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

func TestStateMessageJSON(t *testing.T) {
	s := ups.Status{Mode: ups.ModeBattery, InputVoltage: 0, BatteryCharge: 80, Flags: ups.StatusFlags{UtilityFail: true}}
	raw, err := json.Marshal(newStateMessage(s))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{`"mode":"battery"`, `"on_battery":true`, `"utility_power":false`, `"battery_charge":80`, `"runtime_remaining":null`} {
		if !strings.Contains(got, want) {
			t.Errorf("state %s lacks %s", got, want)
		}
	}
}

// The UPS tests itself a few times a day; mains stay present during the test.
func TestSelfTestKeepsUtilityPower(t *testing.T) {
	s := ups.Status{Mode: ups.ModeBatteryTest, Flags: ups.StatusFlags{SelfTest: true}}
	raw, err := json.Marshal(newStateMessage(s))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, `"utility_power":true`) {
		t.Errorf("state %s reports a power loss during a self test", got)
	}
}

func TestDiscoveryPayloads(t *testing.T) {
	p := NewPublisher(settings.MQTT{TopicPrefix: "vpowerd", DiscoveryPrefix: "homeassistant"}, "ups", true, nil, nil)
	device := p.device(monitor.Snapshot{Identity: &ups.Identity{Protocol: ups.ProtocolPMV}})
	if device.Model != "PMV" {
		t.Errorf("model fallback %q", device.Model)
	}

	var voltage, button entity
	for _, e := range append(sensorEntities(), buttonEntities()...) {
		switch e.key {
		case "input_voltage":
			voltage = e
		case string(ups.CmdTest10s):
			button = e
		}
	}
	v := p.discoveryFor(voltage, device)
	if v.StateTopic != "vpowerd/ups/state" || v.ValueTemplate != "{{ value_json.input_voltage }}" || v.CommandTopic != "" {
		t.Errorf("sensor payload %+v", v)
	}
	b := p.discoveryFor(button, device)
	if b.CommandTopic != "vpowerd/ups/command" || b.PayloadPress != "test_10s" || b.StateTopic != "" {
		t.Errorf("button payload %+v", b)
	}
}

func TestRuntimeEntityOnlyWhenReported(t *testing.T) {
	var runtime entity
	for _, e := range sensorEntities() {
		if e.key == "runtime_remaining" {
			runtime = e
		}
	}
	if runtime.include(monitor.Snapshot{Identity: &ups.Identity{Protocol: ups.ProtocolPMV}}) {
		t.Error("PMV does not report runtime")
	}
	if !runtime.include(monitor.Snapshot{Identity: &ups.Identity{Protocol: ups.ProtocolPMVJ}}) {
		t.Error("PMV_J reports runtime")
	}
}
