package mqtt

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/damianogiusti/vpowerd/internal/monitor"
	"github.com/damianogiusti/vpowerd/internal/settings"
	"github.com/damianogiusti/vpowerd/internal/transport"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"), settings.MQTT{})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mon := monitor.New(func() (transport.Transport, error) { return transport.NewMock("V"), nil }, monitor.Options{Logger: log})
	return NewManager(store, mon, "ups", false, log)
}

func TestManagerHotSwap(t *testing.T) {
	m := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("initial state %s", got)
	}

	// Port 1 on loopback refuses connections: the error must surface.
	if err := m.Update(settings.MQTT{Enabled: true, URL: "tcp://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.Status().LastError == "" {
		if time.Now().After(deadline) {
			t.Fatalf("no connection error reported, status %+v", m.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s := m.Status(); s.State != StateConnecting || s.Broker != "tcp://127.0.0.1:1" {
		t.Errorf("status %+v", s)
	}

	saved, source := m.Settings()
	if source != settings.SourceFile || saved.ClientID != "vpowerd-ups" {
		t.Errorf("saved %+v from %s", saved, source)
	}

	// Disabling must stop the retry loop promptly.
	start := time.Now()
	if err := m.Update(settings.MQTT{Enabled: false, URL: "tcp://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("stopping took %s", elapsed)
	}
	if got := m.Status().State; got != StateDisabled {
		t.Errorf("state after disable %s", got)
	}
	m.Stop()
}

func TestManagerRejectsInvalid(t *testing.T) {
	m := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	if err := m.Update(settings.MQTT{Enabled: true, URL: "http://ha"}); err == nil {
		t.Error("accepted http:// broker")
	}
	if _, source := m.Settings(); source != settings.SourceEnvironment {
		t.Error("invalid settings must not be saved")
	}
}
