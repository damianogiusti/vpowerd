package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/damianogiusti/vpowerd/internal/mqtt"
	"github.com/damianogiusti/vpowerd/internal/settings"
)

// MQTTSettings is the part of the MQTT manager the API needs.
type MQTTSettings interface {
	Settings() (settings.MQTT, settings.Source)
	Status() mqtt.Status
	Update(next settings.MQTT) error
	TestConnection(ctx context.Context, cfg settings.MQTT) error
}

const mqttTestTimeout = 20 * time.Second

// mqttSettingsResponse never contains the password, only whether one is set.
type mqttSettingsResponse struct {
	Enabled         bool            `json:"enabled"`
	URL             string          `json:"url"`
	Username        string          `json:"username"`
	PasswordSet     bool            `json:"password_set"`
	ClientID        string          `json:"client_id"`
	TopicPrefix     string          `json:"topic_prefix"`
	DiscoveryPrefix string          `json:"discovery_prefix"`
	Source          settings.Source `json:"source"`
	Status          mqtt.Status     `json:"status"`
}

// mqttSettingsRequest is the body of PUT and test requests.
// A nil Password keeps the stored one; an empty string clears it.
type mqttSettingsRequest struct {
	Enabled         bool    `json:"enabled"`
	URL             string  `json:"url"`
	Username        string  `json:"username"`
	Password        *string `json:"password"`
	ClientID        string  `json:"client_id"`
	TopicPrefix     string  `json:"topic_prefix"`
	DiscoveryPrefix string  `json:"discovery_prefix"`
}

type testResponse struct {
	OK bool `json:"ok"`
}

func (s *server) mqttSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.mqttResponse())
}

func (s *server) updateMQTTSettings(w http.ResponseWriter, r *http.Request) {
	next, ok := s.decodeMQTTRequest(w, r)
	if !ok {
		return
	}
	if err := s.mqtt.Update(next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Logger.Info("MQTT settings changed from the web UI", "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, s.mqttResponse())
}

func (s *server) testMQTTSettings(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.decodeMQTTRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mqttTestTimeout)
	defer cancel()
	if err := s.mqtt.TestConnection(ctx, cfg); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, testResponse{OK: true})
}

func (s *server) decodeMQTTRequest(w http.ResponseWriter, r *http.Request) (settings.MQTT, bool) {
	var req mqttSettingsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return settings.MQTT{}, false
	}
	current, _ := s.mqtt.Settings()
	password := current.Password
	if req.Password != nil {
		password = *req.Password
	}
	return settings.MQTT{
		Enabled:         req.Enabled,
		URL:             req.URL,
		Username:        req.Username,
		Password:        password,
		ClientID:        req.ClientID,
		TopicPrefix:     req.TopicPrefix,
		DiscoveryPrefix: req.DiscoveryPrefix,
	}, true
}

func (s *server) mqttResponse() mqttSettingsResponse {
	cfg, source := s.mqtt.Settings()
	return mqttSettingsResponse{
		Enabled:         cfg.Enabled,
		URL:             cfg.URL,
		Username:        cfg.Username,
		PasswordSet:     cfg.Password != "",
		ClientID:        cfg.ClientID,
		TopicPrefix:     cfg.TopicPrefix,
		DiscoveryPrefix: cfg.DiscoveryPrefix,
		Source:          source,
		Status:          s.mqtt.Status(),
	}
}
