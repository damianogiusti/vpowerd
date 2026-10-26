// Package api serves the REST API and the embedded web UI.
package api

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/damianogiusti/vpowerd/internal/monitor"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

//go:embed web
var webFS embed.FS

// Backend is the part of the monitor the API needs.
type Backend interface {
	Snapshot() monitor.Snapshot
	History() []monitor.Sample
	Events() []monitor.Event
	Execute(ctx context.Context, cmd ups.Command) error
	Raw(ctx context.Context, query string) (string, error)
}

// Options configures the server.
type Options struct {
	AllowControl bool
	// APIToken protects commands and raw queries when not empty.
	APIToken string
	Logger   *slog.Logger
}

// rawQueries are read-only diagnostics. Single-letter commands such as "Q"
// (beeper toggle) or "T" (self test) change state and are excluded on purpose.
var rawQueries = map[string]bool{
	"M": true, "QS": true, "Q1": true, "F": true, "I": true, "QI": true,
	"QPI": true, "QID": true, "QWS": true, "QFS": true, "QMD": true, "QBV": true,
	"GM": true, "FW?": true, "FW2?": true, "FW3?": true, "BL?": true, "BT?": true,
}

// NewHandler returns the HTTP handler.
func NewHandler(b Backend, m MQTTSettings, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &server{b: b, mqtt: m, opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/capabilities", s.capabilities)
	mux.HandleFunc("POST /api/command", s.requireToken(s.command))
	mux.HandleFunc("GET /api/raw", s.requireToken(s.raw))
	mux.HandleFunc("GET /api/settings/mqtt", s.mqttSettings)
	mux.HandleFunc("PUT /api/settings/mqtt", s.requireToken(s.updateMQTTSettings))
	mux.HandleFunc("POST /api/settings/mqtt/test", s.requireToken(s.testMQTTSettings))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", s.ready)

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // embedded at build time: cannot fail at runtime
	}
	mux.Handle("GET /", http.FileServerFS(static))
	return securityHeaders(mux)
}

type server struct {
	b    Backend
	mqtt MQTTSettings
	opts Options
}

func (s *server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.b.Snapshot())
}

func (s *server) history(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.b.History())
}

func (s *server) events(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.b.Events())
}

type capabilitiesResponse struct {
	ControlEnabled bool `json:"control_enabled"`
	TokenRequired  bool `json:"token_required"`
}

func (s *server) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, capabilitiesResponse{
		ControlEnabled: s.opts.AllowControl,
		TokenRequired:  s.opts.APIToken != "",
	})
}

func (s *server) ready(w http.ResponseWriter, _ *http.Request) {
	if !s.b.Snapshot().Connected {
		writeError(w, http.StatusServiceUnavailable, "UPS not connected")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// commandRequest is the POST /api/command body.
type commandRequest struct {
	Kind string `json:"kind"`
	// DelaySeconds and RestoreMinutes are required for "shutdown" only.
	DelaySeconds   *int `json:"delay_seconds"`
	RestoreMinutes *int `json:"restore_minutes"`
}

func (s *server) command(w http.ResponseWriter, r *http.Request) {
	if !s.opts.AllowControl {
		writeError(w, http.StatusForbidden, "control commands are disabled (VPOWERD_ALLOW_CONTROL=false)")
		return
	}
	var req commandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	kind, ok := ups.ParseCommandKind(req.Kind)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown command kind "+req.Kind)
		return
	}
	cmd := ups.Command{Kind: kind}
	if kind == ups.CmdShutdown {
		if req.DelaySeconds == nil || req.RestoreMinutes == nil {
			writeError(w, http.StatusBadRequest, "shutdown needs delay_seconds and restore_minutes")
			return
		}
		cmd.ShutdownDelay = time.Duration(*req.DelaySeconds) * time.Second
		cmd.RestoreAfter = time.Duration(*req.RestoreMinutes) * time.Minute
	}
	s.opts.Logger.Info("command requested", "kind", kind, "remote", r.RemoteAddr)
	if err := s.b.Execute(r.Context(), cmd); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, monitor.ErrNotConnected) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type rawResponse struct {
	Query string `json:"query"`
	Reply string `json:"reply"`
}

func (s *server) raw(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if !rawQueries[q] {
		writeError(w, http.StatusBadRequest, "query not in the read-only allow list")
		return
	}
	reply, err := s.b.Raw(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rawResponse{Query: q, Reply: reply})
}

func (s *server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.APIToken != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.APIToken)) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or wrong Bearer token")
				return
			}
		}
		next(w, r)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeJSON[T any](w http.ResponseWriter, status int, v T) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
