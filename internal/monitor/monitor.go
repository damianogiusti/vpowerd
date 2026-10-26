// Package monitor owns the UPS connection: it detects the device, polls it,
// keeps a bounded history and reconnects after failures.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/damianogiusti/vpowerd/internal/megatec"
	"github.com/damianogiusti/vpowerd/internal/transport"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

// ErrNotConnected is returned by Execute and Raw while no UPS is connected.
var ErrNotConnected = errors.New("monitor: UPS not connected")

// Opener opens a fresh transport for every connection attempt, so a device
// that re-enumerates (e.g. hidraw0 -> hidraw1) is found again.
type Opener func() (transport.Transport, error)

// Snapshot is the current public state.
type Snapshot struct {
	Connected bool          `json:"connected"`
	Device    string        `json:"device,omitempty"`
	Identity  *ups.Identity `json:"identity,omitempty"`
	Rating    *ups.Rating   `json:"rating,omitempty"`
	Status    *ups.Status   `json:"status,omitempty"`
	LastError string        `json:"last_error,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Sample is one compact history point.
type Sample struct {
	Time           time.Time    `json:"t"`
	InputVoltage   ups.Volts    `json:"in_v"`
	OutputVoltage  ups.Volts    `json:"out_v"`
	LoadPercent    ups.Percent  `json:"load"`
	BatteryVoltage ups.Volts    `json:"bat_v"`
	BatteryCharge  ups.Percent  `json:"charge"`
	Mode           ups.WorkMode `json:"mode"`
}

// Options configures a Monitor.
type Options struct {
	PollInterval  time.Duration
	HistoryWindow time.Duration
	// MaxFailures is the number of consecutive failed polls before the
	// transport is closed and the device detected again.
	MaxFailures int
	Logger      *slog.Logger
}

const (
	maxHistorySamples = 20_000
	maxEvents         = 500
	pollTimeout       = 15 * time.Second
	minBackoff        = 2 * time.Second
	maxBackoff        = time.Minute
)

// Monitor is safe for concurrent use.
type Monitor struct {
	open Opener
	opts Options
	log  *slog.Logger

	requests chan request

	mu      sync.RWMutex
	snap    Snapshot
	history *ring[Sample]
	events  *ring[Event]
	subs    map[chan Snapshot]struct{}
}

// New builds a Monitor. Call Run to start it.
func New(open Opener, opts Options) *Monitor {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 10 * time.Second
	}
	if opts.HistoryWindow <= 0 {
		opts.HistoryWindow = 24 * time.Hour
	}
	if opts.MaxFailures <= 0 {
		opts.MaxFailures = 3
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	samples := min(int(opts.HistoryWindow/opts.PollInterval)+1, maxHistorySamples)
	return &Monitor{
		open:     open,
		opts:     opts,
		log:      opts.Logger,
		requests: make(chan request),
		history:  newRing[Sample](samples),
		events:   newRing[Event](maxEvents),
		subs:     make(map[chan Snapshot]struct{}),
	}
}

// Snapshot returns the current state.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snap
}

// History returns the samples in the retention window, oldest first.
func (m *Monitor) History() []Sample {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.history.snapshot()
}

// Events returns the recent events, oldest first.
func (m *Monitor) Events() []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.events.snapshot()
}

// Subscribe delivers every new snapshot. Slow subscribers only get the
// latest one: the channel has a buffer of 1 and older values are dropped.
func (m *Monitor) Subscribe() (<-chan Snapshot, func()) {
	ch := make(chan Snapshot, 1)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

// Execute runs a control command on the poll goroutine.
func (m *Monitor) Execute(ctx context.Context, cmd ups.Command) error {
	_, err := m.do(ctx, request{command: &cmd})
	return err
}

// Raw sends a diagnostic query and returns the raw reply.
func (m *Monitor) Raw(ctx context.Context, query string) (string, error) {
	return m.do(ctx, request{raw: query})
}

type request struct {
	command *ups.Command
	raw     string
	reply   chan result
}

type result struct {
	value string
	err   error
}

func (m *Monitor) do(ctx context.Context, req request) (string, error) {
	req.reply = make(chan result, 1)
	select {
	case m.requests <- req:
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(pollTimeout):
		// Nobody is serving requests: the session is reconnecting.
		return "", ErrNotConnected
	}
	select {
	case r := <-req.reply:
		return r.value, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Run blocks until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	backoff := minBackoff
	for {
		started := time.Now()
		err := m.session(ctx)
		if ctx.Err() != nil {
			m.setDisconnected(nil)
			return
		}
		m.setDisconnected(err)
		if time.Since(started) > maxBackoff {
			backoff = minBackoff
		}
		m.log.Warn("UPS session ended, retrying", "error", err, "retry_in", backoff)
		if !m.idle(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// idle waits for d while refusing requests, so callers fail fast.
func (m *Monitor) idle(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case req := <-m.requests:
			req.reply <- result{err: ErrNotConnected}
		}
	}
}

func (m *Monitor) session(ctx context.Context) error {
	t, err := m.open()
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer t.Close()

	detectCtx, cancel := context.WithTimeout(ctx, time.Minute)
	drv, err := megatec.Detect(detectCtx, t)
	cancel()
	if err != nil {
		return fmt.Errorf("detect on %s: %w", t.Name(), err)
	}
	identifyCtx, cancel := context.WithTimeout(ctx, time.Minute)
	identity, rating, err := drv.Identify(identifyCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("identify: %w", err)
	}
	m.log.Info("UPS connected", "device", t.Name(), "protocol", identity.Protocol, "model", identity.Model)
	m.setConnected(t.Name(), identity, rating)

	ticker := time.NewTicker(m.opts.PollInterval)
	defer ticker.Stop()
	failures := 0
	poll := func() error {
		pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
		defer cancel()
		status, err := drv.Poll(pollCtx)
		if err == nil {
			failures = 0
			m.update(status)
			return nil
		}
		failures++
		m.log.Warn("poll failed", "error", err, "consecutive", failures)
		if errors.Is(err, transport.ErrDeviceGone) || failures >= m.opts.MaxFailures {
			return fmt.Errorf("poll: %w", err)
		}
		return nil
	}
	if err := poll(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := poll(); err != nil {
				return err
			}
		case req := <-m.requests:
			req.reply <- m.serve(ctx, drv, req)
		}
	}
}

func (m *Monitor) serve(ctx context.Context, drv *megatec.Driver, req request) result {
	reqCtx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	if req.command != nil {
		m.log.Info("executing command", "kind", req.command.Kind)
		err := drv.Execute(reqCtx, *req.command)
		if err == nil {
			m.addEvent(Event{Time: time.Now(), Kind: EventCommand, Message: string(req.command.Kind)})
		}
		return result{err: err}
	}
	value, err := drv.Raw(reqCtx, req.raw)
	return result{value: value, err: err}
}

func (m *Monitor) setConnected(device string, id ups.Identity, rating *ups.Rating) {
	m.mu.Lock()
	m.snap = Snapshot{Connected: true, Device: device, Identity: &id, Rating: rating, UpdatedAt: time.Now()}
	m.events.push(Event{Time: time.Now(), Kind: EventConnected, Message: fmt.Sprintf("%s on %s", id.Protocol, device)})
	snap := m.snap
	m.mu.Unlock()
	m.publish(snap)
}

func (m *Monitor) setDisconnected(err error) {
	m.mu.Lock()
	wasConnected := m.snap.Connected
	m.snap.Connected = false
	m.snap.Status = nil
	m.snap.LastError = ""
	if err != nil {
		m.snap.LastError = err.Error()
	}
	m.snap.UpdatedAt = time.Now()
	if wasConnected {
		m.events.push(Event{Time: time.Now(), Kind: EventDisconnected, Message: m.snap.LastError})
	}
	snap := m.snap
	m.mu.Unlock()
	m.publish(snap)
}

func (m *Monitor) update(s ups.Status) {
	m.mu.Lock()
	var prev *ups.Status
	if m.snap.Status != nil {
		p := *m.snap.Status
		prev = &p
	}
	for _, e := range diffEvents(prev, s) {
		m.events.push(e)
		m.log.Info("event", "kind", e.Kind, "message", e.Message)
	}
	m.snap.Status = &s
	m.snap.LastError = ""
	m.snap.UpdatedAt = s.Time
	m.history.push(Sample{
		Time:           s.Time,
		InputVoltage:   s.InputVoltage,
		OutputVoltage:  s.OutputVoltage,
		LoadPercent:    s.LoadPercent,
		BatteryVoltage: s.BatteryVoltage,
		BatteryCharge:  s.BatteryCharge,
		Mode:           s.Mode,
	})
	snap := m.snap
	m.mu.Unlock()
	m.publish(snap)
}

func (m *Monitor) addEvent(e Event) {
	m.mu.Lock()
	m.events.push(e)
	m.mu.Unlock()
}

func (m *Monitor) publish(snap Snapshot) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for ch := range m.subs {
		select {
		case <-ch: // drop the stale value
		default:
		}
		select {
		case ch <- snap:
		default:
		}
	}
}
