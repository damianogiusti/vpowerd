// Package config reads settings from flags, with environment variables as
// defaults so the same binary fits Docker, LXC and systemd.
package config

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/damianogiusti/vpowerd/internal/settings"
	"github.com/damianogiusti/vpowerd/internal/transport"
)

// DeviceAuto means "scan /sys for a known UPS USB ID".
const DeviceAuto = "auto"

// Config is the validated daemon configuration.
type Config struct {
	// Device is DeviceAuto, a /dev/hidrawN path or a serial port path.
	Device string
	// Transport forces hidraw or serial; auto decides from the device.
	Transport transport.Kind
	// MockModel, when set, replaces the device with a simulator answering
	// this letter to "M" (V, H, I or J).
	MockModel     string
	Listen        string
	PollInterval  time.Duration
	HistoryWindow time.Duration
	// DeviceID is used in MQTT topics and Home Assistant unique IDs.
	DeviceID string
	// AllowControl enables commands that change the UPS state.
	AllowControl bool
	// APIToken, when set, is required as a Bearer token for commands and raw
	// queries.
	APIToken string
	LogLevel slog.Level
	// StateDir holds settings saved from the web UI.
	StateDir string
	// MQTT is the initial broker configuration, from flags or environment.
	// Settings saved from the web UI take precedence over it.
	MQTT settings.MQTT
}

// Load parses args (without the program name). getenv provides defaults.
func Load(args []string, getenv func(string) string) (Config, error) {
	env := func(key, fallback string) string {
		if v := getenv("VPOWERD_" + key); v != "" {
			return v
		}
		return fallback
	}
	var errs []error
	envDuration := func(key string, fallback time.Duration) time.Duration {
		raw := env(key, "")
		if raw == "" {
			return fallback
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("VPOWERD_%s: %w", key, err))
			return fallback
		}
		return d
	}
	envBool := func(key string, fallback bool) bool {
		raw := env(key, "")
		if raw == "" {
			return fallback
		}
		b, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("VPOWERD_%s: %w", key, err))
			return fallback
		}
		return b
	}

	var c Config
	var logLevel, transportKind string
	fs := flag.NewFlagSet("vpowerd", flag.ContinueOnError)
	fs.StringVar(&c.Device, "device", env("DEVICE", DeviceAuto), "auto, /dev/hidrawN or serial port (env VPOWERD_DEVICE)")
	fs.StringVar(&transportKind, "transport", env("TRANSPORT", string(transport.KindAuto)), "auto, hidraw or serial (env VPOWERD_TRANSPORT)")
	fs.StringVar(&c.MockModel, "mock", env("MOCK", ""), "simulate a UPS answering V, H, I or J to M (env VPOWERD_MOCK)")
	fs.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "HTTP listen address (env VPOWERD_LISTEN)")
	fs.DurationVar(&c.PollInterval, "poll", envDuration("POLL_INTERVAL", 10*time.Second), "poll interval (env VPOWERD_POLL_INTERVAL)")
	fs.DurationVar(&c.HistoryWindow, "history", envDuration("HISTORY_WINDOW", 24*time.Hour), "in-memory history window (env VPOWERD_HISTORY_WINDOW)")
	fs.StringVar(&c.DeviceID, "id", env("DEVICE_ID", "ups"), "device id for MQTT and HA (env VPOWERD_DEVICE_ID)")
	fs.BoolVar(&c.AllowControl, "allow-control", envBool("ALLOW_CONTROL", false), "enable UPS control commands (env VPOWERD_ALLOW_CONTROL)")
	fs.StringVar(&c.APIToken, "api-token", env("API_TOKEN", ""), "Bearer token for commands and raw queries (env VPOWERD_API_TOKEN)")
	fs.StringVar(&logLevel, "log-level", env("LOG_LEVEL", "info"), "debug, info, warn, error (env VPOWERD_LOG_LEVEL)")
	fs.StringVar(&c.StateDir, "state-dir", env("STATE_DIR", "/var/lib/vpowerd"), "directory for settings saved from the UI (env VPOWERD_STATE_DIR)")
	fs.StringVar(&c.MQTT.URL, "mqtt-url", env("MQTT_URL", ""), "e.g. tcp://broker:1883; empty disables MQTT (env VPOWERD_MQTT_URL)")
	fs.StringVar(&c.MQTT.Username, "mqtt-user", env("MQTT_USER", ""), "MQTT username (env VPOWERD_MQTT_USER)")
	fs.StringVar(&c.MQTT.Password, "mqtt-password", env("MQTT_PASSWORD", ""), "MQTT password (env VPOWERD_MQTT_PASSWORD)")
	fs.StringVar(&c.MQTT.ClientID, "mqtt-client-id", env("MQTT_CLIENT_ID", ""), "MQTT client id, default vpowerd-<id> (env VPOWERD_MQTT_CLIENT_ID)")
	fs.StringVar(&c.MQTT.TopicPrefix, "mqtt-prefix", env("MQTT_PREFIX", settings.DefaultTopicPrefix), "MQTT topic prefix (env VPOWERD_MQTT_PREFIX)")
	fs.StringVar(&c.MQTT.DiscoveryPrefix, "mqtt-discovery-prefix", env("MQTT_DISCOVERY_PREFIX", settings.DefaultDiscoveryPrefix), "HA discovery prefix (env VPOWERD_MQTT_DISCOVERY_PREFIX)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if k, err := transport.ParseKind(transportKind); err != nil {
		errs = append(errs, err)
	} else {
		c.Transport = k
	}
	if err := c.LogLevel.UnmarshalText([]byte(logLevel)); err != nil {
		errs = append(errs, fmt.Errorf("log level %q: %w", logLevel, err))
	}
	if c.PollInterval < 2*time.Second {
		errs = append(errs, fmt.Errorf("poll interval %s: minimum is 2s", c.PollInterval))
	}
	if c.HistoryWindow < c.PollInterval {
		errs = append(errs, fmt.Errorf("history window %s: shorter than poll interval", c.HistoryWindow))
	}
	switch c.MockModel {
	case "", "V", "H", "I", "J":
	default:
		errs = append(errs, fmt.Errorf("mock model %q: want V, H, I or J", c.MockModel))
	}
	if !validID(c.DeviceID) {
		errs = append(errs, fmt.Errorf("device id %q: use only a-z, 0-9, _ and -", c.DeviceID))
	}
	c.MQTT.Enabled = c.MQTT.URL != ""
	c.MQTT = c.MQTT.Normalized(c.DeviceID)
	if err := c.MQTT.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("MQTT: %w", err))
	}
	if c.StateDir == "" {
		errs = append(errs, errors.New("state dir must not be empty"))
	}
	return c, errors.Join(errs...)
}

func validID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
