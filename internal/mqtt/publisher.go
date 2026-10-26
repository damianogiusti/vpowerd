// Package mqtt publishes UPS state to an MQTT broker with Home Assistant
// discovery, and optionally accepts commands from Home Assistant buttons.
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/damianogiusti/vpowerd/internal/monitor"
	"github.com/damianogiusti/vpowerd/internal/settings"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

// Source is the part of the monitor the publisher needs.
type Source interface {
	Snapshot() monitor.Snapshot
	Subscribe() (<-chan monitor.Snapshot, func())
	Execute(ctx context.Context, cmd ups.Command) error
}

const (
	publishTimeout = 5 * time.Second
	commandTimeout = 20 * time.Second
	connectTimeout = 10 * time.Second
	maxRetryDelay  = time.Minute
	payloadOnline  = "online"
	payloadOffline = "offline"
)

// Publisher bridges the monitor to MQTT.
type Publisher struct {
	cfg          settings.MQTT
	deviceID     string
	allowControl bool
	src          Source
	log          *slog.Logger
	client       paho.Client

	mu sync.Mutex
	// published remembers the retained discovery topics we own, so entities
	// that disappear (e.g. after a firmware change) can be removed.
	published map[string]bool
	// publishedDevice is the device block of the last discovery round.
	publishedDevice string
	// availability is the last retained availability payload, so it is not
	// re-sent on every poll.
	availability string

	statusMu sync.Mutex
	status   Status
}

// NewPublisher prepares a publisher. Call Run to connect.
func NewPublisher(cfg settings.MQTT, deviceID string, allowControl bool, src Source, log *slog.Logger) *Publisher {
	return &Publisher{
		cfg: cfg, deviceID: deviceID, allowControl: allowControl, src: src, log: log,
		published: map[string]bool{},
		status:    Status{State: StateConnecting, Broker: cfg.URL},
	}
}

func (p *Publisher) base() string          { return p.cfg.TopicPrefix + "/" + p.deviceID }
func (p *Publisher) stateTopic() string    { return p.base() + "/state" }
func (p *Publisher) availTopic() string    { return p.base() + "/availability" }
func (p *Publisher) commandTopic() string  { return p.base() + "/command" }
func (p *Publisher) haStatusTopic() string { return p.cfg.DiscoveryPrefix + "/status" }

// clientOptions builds the Paho options shared by Run and TestConnection.
func clientOptions(cfg settings.MQTT) *paho.ClientOptions {
	return paho.NewClientOptions().
		AddBroker(cfg.URL).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetConnectTimeout(connectTimeout).
		SetOrderMatters(false)
}

// Run connects and publishes until ctx is cancelled.
//
// The first connection is retried here instead of by Paho, so the error
// (wrong host, bad credentials) is visible in Status and in the web UI.
// After the first success Paho reconnects by itself.
func (p *Publisher) Run(ctx context.Context) {
	opts := clientOptions(p.cfg).
		SetWill(p.availTopic(), payloadOffline, 1, true).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(maxRetryDelay).
		SetOnConnectHandler(p.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			p.log.Warn("MQTT connection lost", "error", err)
			p.setStatus(StateConnecting, err)
		})
	p.client = paho.NewClient(opts)
	p.setStatus(StateConnecting, nil)
	if !p.connectWithRetry(ctx) {
		return
	}
	defer p.client.Disconnect(500)

	updates, unsubscribe := p.src.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			p.publish(p.availTopic(), true, []byte(payloadOffline))
			return
		case snap := <-updates:
			p.publishSnapshot(snap, false)
		}
	}
}

// connectWithRetry returns false when ctx is cancelled before a connection.
func (p *Publisher) connectWithRetry(ctx context.Context) bool {
	delay := 2 * time.Second
	for {
		tok := p.client.Connect()
		err := waitToken(ctx, tok, connectTimeout+time.Second)
		if err == nil {
			return true
		}
		p.client.Disconnect(0)
		if ctx.Err() != nil {
			return false
		}
		p.log.Warn("MQTT connect failed", "broker", p.cfg.URL, "error", err, "retry_in", delay)
		p.setStatus(StateConnecting, err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// waitToken waits for a Paho token, honouring ctx and a timeout.
func waitToken(ctx context.Context, tok paho.Token, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-tok.Done():
		return tok.Error()
	case <-timer.C:
		return fmt.Errorf("no answer from broker within %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status reports the connection state.
func (p *Publisher) Status() Status {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	return p.status
}

func (p *Publisher) setStatus(state State, err error) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.status = Status{State: state, Broker: p.cfg.URL}
	if err != nil {
		p.status.LastError = err.Error()
	}
}

func (p *Publisher) onConnect(c paho.Client) {
	p.log.Info("MQTT connected", "broker", p.cfg.URL)
	p.setStatus(StateConnected, nil)
	// Home Assistant announces "online" after a restart: send discovery again.
	c.Subscribe(p.haStatusTopic(), 1, func(_ paho.Client, m paho.Message) {
		if string(m.Payload()) == payloadOnline {
			p.publishSnapshot(p.src.Snapshot(), true)
		}
	})
	if p.allowControl {
		c.Subscribe(p.commandTopic(), 1, p.onCommand)
	}
	p.publishSnapshot(p.src.Snapshot(), true)
}

func (p *Publisher) onCommand(_ paho.Client, m paho.Message) {
	kind, ok := ups.ParseCommandKind(string(m.Payload()))
	// Shutdown needs explicit delays, which only the REST API accepts.
	if !ok || kind == ups.CmdShutdown {
		p.log.Warn("ignoring MQTT command", "payload", string(m.Payload()))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		if err := p.src.Execute(ctx, ups.Command{Kind: kind}); err != nil {
			p.log.Warn("MQTT command failed", "kind", kind, "error", err)
		}
	}()
}

// publishSnapshot sends state and availability. Discovery is sent only when
// forced (new connection, Home Assistant restart) or when the entity set or
// the device block changed, not on every poll.
func (p *Publisher) publishSnapshot(snap monitor.Snapshot, forceDiscovery bool) {
	if p.client == nil || !p.client.IsConnectionOpen() {
		return
	}
	if !snap.Connected || snap.Status == nil {
		p.setAvailability(payloadOffline, forceDiscovery)
		return
	}
	p.publishDiscovery(snap, forceDiscovery)
	if payload, err := json.Marshal(newStateMessage(*snap.Status)); err == nil {
		p.publish(p.stateTopic(), true, payload)
	}
	p.setAvailability(payloadOnline, forceDiscovery)
}

func (p *Publisher) setAvailability(payload string, force bool) {
	p.mu.Lock()
	changed := p.availability != payload
	p.availability = payload
	p.mu.Unlock()
	if changed || force {
		p.publish(p.availTopic(), true, []byte(payload))
	}
}

func (p *Publisher) publishDiscovery(snap monitor.Snapshot, force bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entities := sensorEntities()
	if p.allowControl {
		entities = append(entities, buttonEntities()...)
	}
	device := p.device(snap)
	deviceKey := fmt.Sprintf("%+v", device)
	wanted := map[string]entity{}
	for _, e := range entities {
		if e.include(snap) {
			wanted[fmt.Sprintf("%s/%s/%s/%s/config", p.cfg.DiscoveryPrefix, e.component, p.deviceID, e.key)] = e
		}
	}
	if !force && deviceKey == p.publishedDevice && sameKeys(wanted, p.published) {
		return
	}
	for topic, e := range wanted {
		payload, err := json.Marshal(p.discoveryFor(e, device))
		if err != nil {
			p.log.Error("discovery payload", "entity", e.key, "error", err)
			continue
		}
		p.publish(topic, true, payload)
	}
	// An empty retained payload removes an entity from Home Assistant.
	for topic := range p.published {
		if _, ok := wanted[topic]; !ok {
			p.publish(topic, true, nil)
		}
	}
	p.published = make(map[string]bool, len(wanted))
	for topic := range wanted {
		p.published[topic] = true
	}
	p.publishedDevice = deviceKey
}

func sameKeys(a map[string]entity, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func (p *Publisher) discoveryFor(e entity, device discoveryDevice) discoveryPayload {
	d := discoveryPayload{
		Name:              e.name,
		UniqueID:          fmt.Sprintf("vpowerd_%s_%s", p.deviceID, e.key),
		ObjectID:          fmt.Sprintf("%s_%s", p.deviceID, e.key),
		Unit:              e.unit,
		DeviceClass:       e.deviceClass,
		StateClass:        e.stateClass,
		Icon:              e.icon,
		EntityCategory:    e.entityCategory,
		Options:           e.options,
		AvailabilityTopic: p.availTopic(),
		Device:            device,
	}
	switch e.component {
	case componentButton:
		d.CommandTopic = p.commandTopic()
		d.PayloadPress = e.key
	default:
		d.StateTopic = p.stateTopic()
		d.ValueTemplate = e.valueTemplate
		if d.ValueTemplate == "" {
			d.ValueTemplate = fmt.Sprintf("{{ value_json.%s }}", e.key)
		}
		d.PayloadOn = e.payloadOn
		d.PayloadOff = e.payloadOff
	}
	return d
}

func (p *Publisher) device(snap monitor.Snapshot) discoveryDevice {
	d := discoveryDevice{Identifiers: []string{"vpowerd_" + p.deviceID}, Name: "UPS " + p.deviceID}
	if id := snap.Identity; id != nil {
		d.Model = id.Model
		if d.Model == "" {
			d.Model = string(id.Protocol)
		}
		d.SWVersion = id.Firmware
		d.SerialNum = id.Serial
	}
	return d
}

func (p *Publisher) publish(topic string, retained bool, payload []byte) {
	tok := p.client.Publish(topic, 1, retained, payload)
	if !tok.WaitTimeout(publishTimeout) {
		p.log.Warn("MQTT publish timed out", "topic", topic)
		return
	}
	if err := tok.Error(); err != nil {
		p.log.Warn("MQTT publish failed", "topic", topic, "error", err)
	}
}
