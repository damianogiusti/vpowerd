package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.Device != DeviceAuto || c.PollInterval != 10*time.Second || c.AllowControl || c.MQTT.Enabled {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.MQTT.ClientID != "vpowerd-ups" {
		t.Errorf("client id %q", c.MQTT.ClientID)
	}
}

func TestLoadEnvAndFlags(t *testing.T) {
	env := map[string]string{
		"VPOWERD_POLL_INTERVAL": "5s",
		"VPOWERD_ALLOW_CONTROL": "true",
		"VPOWERD_MQTT_URL":      "tcp://broker:1883",
	}
	c, err := Load([]string{"-poll", "3s"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.PollInterval != 3*time.Second {
		t.Errorf("flag should win over env, got %s", c.PollInterval)
	}
	if !c.AllowControl || !c.MQTT.Enabled {
		t.Errorf("env not applied: %+v", c)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"bad duration": {"VPOWERD_POLL_INTERVAL": "soon"},
		"too fast":     {"VPOWERD_POLL_INTERVAL": "500ms"},
		"bad bool":     {"VPOWERD_ALLOW_CONTROL": "maybe"},
		"bad mock":     {"VPOWERD_MOCK": "X"},
		"bad id":       {"VPOWERD_DEVICE_ID": "My UPS"},
	} {
		if _, err := Load(nil, func(k string) string { return env[k] }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
