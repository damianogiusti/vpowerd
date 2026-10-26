package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenWithoutFileUsesDefaults(t *testing.T) {
	defaults := MQTT{Enabled: true, URL: "tcp://env:1883"}
	s, err := Open(filepath.Join(t.TempDir(), "settings.json"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	got, src := s.MQTT()
	if got != defaults || src != SourceEnvironment {
		t.Errorf("got %+v from %s", got, src)
	}
}

func TestSaveAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "settings.json")
	s, err := Open(path, MQTT{})
	if err != nil {
		t.Fatal(err)
	}
	want := MQTT{Enabled: true, URL: "tcp://ha:1883", Username: "u", Password: "p", ClientID: "c", TopicPrefix: "vpowerd", DiscoveryPrefix: "homeassistant"}
	if err := s.SaveMQTT(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}

	reopened, err := Open(path, MQTT{URL: "tcp://ignored:1883"})
	if err != nil {
		t.Fatal(err)
	}
	got, src := reopened.MQTT()
	if got != want || src != SourceFile {
		t.Errorf("got %+v from %s", got, src)
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, MQTT{}); err == nil {
		t.Error("accepted a corrupt file")
	}
}

func TestNormalizeAndValidate(t *testing.T) {
	m := MQTT{Enabled: true, URL: " tcp://ha:1883 ", TopicPrefix: "/vp/"}.Normalized("ups")
	if m.URL != "tcp://ha:1883" || m.ClientID != "vpowerd-ups" || m.TopicPrefix != "vp" || m.DiscoveryPrefix != DefaultDiscoveryPrefix {
		t.Errorf("normalized %+v", m)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	for name, bad := range map[string]MQTT{
		"no url":     {Enabled: true},
		"bad scheme": {Enabled: true, URL: "http://ha:1883"},
		"no host":    {Enabled: true, URL: "tcp://:1883"},
		"wildcard":   {Enabled: true, URL: "tcp://ha:1883", TopicPrefix: "ups/#"},
	} {
		if err := bad.Normalized("ups").Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (MQTT{Enabled: false}).Normalized("ups").Validate(); err != nil {
		t.Errorf("disabled config without URL rejected: %v", err)
	}
}
