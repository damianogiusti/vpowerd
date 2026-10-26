package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/damianogiusti/vpowerd/internal/settings"
)

// State is the MQTT connection state shown in the web UI.
type State string

const (
	StateDisabled   State = "disabled"
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
)

// Status is the MQTT connection state with the last error, if any.
type Status struct {
	State     State  `json:"state"`
	Broker    string `json:"broker,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// Manager owns the running publisher and replaces it when the settings
// change, so the broker can be edited from the web UI without a restart.
type Manager struct {
	store        *settings.Store
	src          Source
	deviceID     string
	allowControl bool
	log          *slog.Logger

	// mu serialises Start, Update and Stop: only one publisher runs at a time,
	// so two clients never fight over the same client ID.
	mu      sync.Mutex
	parent  context.Context
	running *runningPublisher
}

type runningPublisher struct {
	pub    *Publisher
	cancel context.CancelFunc
	done   chan struct{}
}

// NewManager builds a manager. Call Start before Update.
func NewManager(store *settings.Store, src Source, deviceID string, allowControl bool, log *slog.Logger) *Manager {
	return &Manager{store: store, src: src, deviceID: deviceID, allowControl: allowControl, log: log}
}

// Start runs the publisher for the stored settings. Publishers stop when
// parent is cancelled; call Stop to wait for them.
func (m *Manager) Start(parent context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parent = parent
	cfg, _ := m.store.MQTT()
	m.startLocked(cfg)
}

// Stop stops the publisher and waits for it to disconnect.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// Settings returns the stored configuration and where it comes from.
func (m *Manager) Settings() (settings.MQTT, settings.Source) {
	return m.store.MQTT()
}

// Status returns the state of the running publisher.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running == nil {
		return Status{State: StateDisabled}
	}
	return m.running.pub.Status()
}

// Update validates, saves and applies a new configuration.
func (m *Manager) Update(next settings.MQTT) error {
	next = next.Normalized(m.deviceID)
	if err := next.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parent == nil {
		return fmt.Errorf("mqtt manager not started")
	}
	if err := m.store.SaveMQTT(next); err != nil {
		return err
	}
	m.stopLocked()
	m.startLocked(next)
	m.log.Info("MQTT settings updated", "enabled", next.Enabled, "broker", next.URL)
	return nil
}

func (m *Manager) startLocked(cfg settings.MQTT) {
	if !cfg.Enabled || m.parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(m.parent)
	r := &runningPublisher{
		pub:    NewPublisher(cfg, m.deviceID, m.allowControl, m.src, m.log),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() {
		defer close(r.done)
		r.pub.Run(ctx)
	}()
	m.running = r
}

func (m *Manager) stopLocked() {
	if m.running == nil {
		return
	}
	m.running.cancel()
	<-m.running.done
	m.running = nil
}

// TestConnection tries to connect with cfg and disconnects right away.
// It uses a separate client ID so it does not kick the running session.
func (m *Manager) TestConnection(ctx context.Context, cfg settings.MQTT) error {
	cfg = cfg.Normalized(m.deviceID)
	cfg.Enabled = true
	if err := cfg.Validate(); err != nil {
		return err
	}
	client := paho.NewClient(clientOptions(cfg).SetClientID(cfg.ClientID + "-test"))
	if err := waitToken(ctx, client.Connect(), connectTimeout+connectTimeout/2); err != nil {
		client.Disconnect(0)
		return err
	}
	client.Disconnect(250)
	return nil
}
