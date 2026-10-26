// Package settings persists the options that can be changed from the web UI.
//
// Environment variables give the initial values. Once the user saves from the
// UI, the file wins, so a container restart does not undo a UI change.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MQTT is the broker configuration.
type MQTT struct {
	Enabled         bool   `json:"enabled"`
	URL             string `json:"url"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	ClientID        string `json:"client_id"`
	TopicPrefix     string `json:"topic_prefix"`
	DiscoveryPrefix string `json:"discovery_prefix"`
}

// Default topic prefixes, also used when a field is left empty.
const (
	DefaultTopicPrefix     = "vpowerd"
	DefaultDiscoveryPrefix = "homeassistant"
)

// brokerSchemes are the URL schemes the Paho client accepts.
var brokerSchemes = map[string]bool{
	"tcp": true, "mqtt": true, "ssl": true, "tls": true, "mqtts": true, "ws": true, "wss": true,
}

// Normalized fills empty optional fields and trims topic prefixes.
func (m MQTT) Normalized(deviceID string) MQTT {
	m.URL = strings.TrimSpace(m.URL)
	m.Username = strings.TrimSpace(m.Username)
	m.ClientID = strings.TrimSpace(m.ClientID)
	if m.ClientID == "" {
		m.ClientID = "vpowerd-" + deviceID
	}
	m.TopicPrefix = strings.Trim(strings.TrimSpace(m.TopicPrefix), "/")
	if m.TopicPrefix == "" {
		m.TopicPrefix = DefaultTopicPrefix
	}
	m.DiscoveryPrefix = strings.Trim(strings.TrimSpace(m.DiscoveryPrefix), "/")
	if m.DiscoveryPrefix == "" {
		m.DiscoveryPrefix = DefaultDiscoveryPrefix
	}
	return m
}

// Validate checks a normalized configuration. A disabled configuration may
// keep an incomplete URL, so the user can switch MQTT off without losing it.
func (m MQTT) Validate() error {
	var errs []error
	if m.Enabled {
		u, err := url.Parse(m.URL)
		switch {
		case m.URL == "":
			errs = append(errs, errors.New("broker URL is required"))
		case err != nil:
			errs = append(errs, fmt.Errorf("broker URL: %w", err))
		case !brokerSchemes[u.Scheme]:
			errs = append(errs, fmt.Errorf("broker URL scheme %q: use tcp://, ssl:// or ws://", u.Scheme))
		case u.Hostname() == "":
			errs = append(errs, errors.New("broker URL has no host"))
		}
	}
	for name, prefix := range map[string]string{"topic prefix": m.TopicPrefix, "discovery prefix": m.DiscoveryPrefix} {
		if strings.ContainsAny(prefix, "#+") {
			errs = append(errs, fmt.Errorf("%s %q: wildcards # and + are not allowed", name, prefix))
		}
	}
	if len(m.ClientID) > 128 {
		errs = append(errs, errors.New("client id longer than 128 characters"))
	}
	return errors.Join(errs...)
}

// Source tells where the current values come from.
type Source string

const (
	SourceEnvironment Source = "environment"
	SourceFile        Source = "file"
)

// fileFormat is the on-disk layout. Version allows later migrations.
type fileFormat struct {
	Version int  `json:"version"`
	MQTT    MQTT `json:"mqtt"`
}

const fileVersion = 1

// Store holds the settings and writes them atomically.
type Store struct {
	path string

	mu     sync.RWMutex
	mqtt   MQTT
	source Source
}

// Open loads path, or uses defaults when the file does not exist yet.
// A corrupt file is an error: silently falling back would drop user settings.
func Open(path string, defaults MQTT) (*Store, error) {
	s := &Store{path: path, mqtt: defaults, source: SourceEnvironment}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("%s: unsupported version %d", path, f.Version)
	}
	s.mqtt = f.MQTT
	s.source = SourceFile
	return s, nil
}

// MQTT returns the current broker configuration.
func (s *Store) MQTT() (MQTT, Source) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mqtt, s.source
}

// Path is the settings file location.
func (s *Store) Path() string { return s.path }

// SaveMQTT persists m. The caller validates it first.
func (s *Store) SaveMQTT(m MQTT) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.MarshalIndent(fileFormat{Version: fileVersion, MQTT: m}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(s.path, raw); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	s.mqtt = m
	s.source = SourceFile
	return nil
}

// writeAtomic writes to a temp file in the same directory and renames it, so
// a crash never leaves a half written file. 0600: the file holds a password.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
