package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damianogiusti/vpowerd/internal/monitor"
	"github.com/damianogiusti/vpowerd/internal/mqtt"
	"github.com/damianogiusti/vpowerd/internal/settings"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

type fakeBackend struct {
	snap     monitor.Snapshot
	executed []ups.Command
	rawSeen  []string
}

func (f *fakeBackend) Snapshot() monitor.Snapshot { return f.snap }
func (f *fakeBackend) History() []monitor.Sample  { return nil }
func (f *fakeBackend) Events() []monitor.Event    { return nil }
func (f *fakeBackend) Execute(_ context.Context, c ups.Command) error {
	f.executed = append(f.executed, c)
	return nil
}
func (f *fakeBackend) Raw(_ context.Context, q string) (string, error) {
	f.rawSeen = append(f.rawSeen, q)
	return "(reply", nil
}

func newTestServer(b Backend, opts Options) *httptest.Server {
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptest.NewServer(NewHandler(b, &fakeMQTT{}, opts))
}

func do(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestStatus(t *testing.T) {
	b := &fakeBackend{snap: monitor.Snapshot{Connected: true, Status: &ups.Status{Mode: ups.ModeLine, InputVoltage: 230}}}
	srv := newTestServer(b, Options{})
	defer srv.Close()

	resp := do(t, http.MethodGet, srv.URL+"/api/status", "", "")
	var got monitor.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Connected || got.Status.InputVoltage != 230 {
		t.Errorf("got %+v", got)
	}
}

func TestCommandDisabledByDefault(t *testing.T) {
	b := &fakeBackend{}
	srv := newTestServer(b, Options{})
	defer srv.Close()
	resp := do(t, http.MethodPost, srv.URL+"/api/command", "", `{"kind":"test_10s"}`)
	if resp.StatusCode != http.StatusForbidden || len(b.executed) != 0 {
		t.Errorf("status %d, executed %v", resp.StatusCode, b.executed)
	}
}

func TestCommandNeedsToken(t *testing.T) {
	b := &fakeBackend{}
	srv := newTestServer(b, Options{AllowControl: true, APIToken: "secret"})
	defer srv.Close()

	if resp := do(t, http.MethodPost, srv.URL+"/api/command", "wrong", `{"kind":"test_10s"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, srv.URL+"/api/command", "secret", `{"kind":"test_10s"}`); resp.StatusCode != http.StatusNoContent {
		t.Errorf("right token: status %d", resp.StatusCode)
	}
	if len(b.executed) != 1 || b.executed[0].Kind != ups.CmdTest10s {
		t.Errorf("executed %v", b.executed)
	}
}

func TestShutdownValidation(t *testing.T) {
	b := &fakeBackend{}
	srv := newTestServer(b, Options{AllowControl: true})
	defer srv.Close()

	if resp := do(t, http.MethodPost, srv.URL+"/api/command", "", `{"kind":"shutdown"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing delays: status %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, srv.URL+"/api/command", "", `{"kind":"shutdown","delay_seconds":120,"restore_minutes":1,"extra":1}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field: status %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, srv.URL+"/api/command", "", `{"kind":"shutdown","delay_seconds":120,"restore_minutes":1}`); resp.StatusCode != http.StatusNoContent {
		t.Errorf("valid: status %d", resp.StatusCode)
	}
	want := ups.Command{Kind: ups.CmdShutdown, ShutdownDelay: 2 * time.Minute, RestoreAfter: time.Minute}
	if len(b.executed) != 1 || b.executed[0] != want {
		t.Errorf("executed %+v", b.executed)
	}
}

func TestRawAllowList(t *testing.T) {
	b := &fakeBackend{}
	srv := newTestServer(b, Options{})
	defer srv.Close()

	if resp := do(t, http.MethodGet, srv.URL+"/api/raw?q=QS", "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("QS: status %d", resp.StatusCode)
	}
	// "Q" toggles the beeper: it must never pass as a diagnostic query.
	if resp := do(t, http.MethodGet, srv.URL+"/api/raw?q=Q", "", ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Q: status %d", resp.StatusCode)
	}
	if len(b.rawSeen) != 1 || b.rawSeen[0] != "QS" {
		t.Errorf("raw seen %v", b.rawSeen)
	}
}

func TestWebUIServed(t *testing.T) {
	srv := newTestServer(&fakeBackend{}, Options{})
	defer srv.Close()
	resp := do(t, http.MethodGet, srv.URL+"/", "", "")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<title>") {
		t.Errorf("status %d", resp.StatusCode)
	}
}

type fakeMQTT struct {
	cfg     settings.MQTT
	updated []settings.MQTT
	tested  []settings.MQTT
}

func (f *fakeMQTT) Settings() (settings.MQTT, settings.Source) { return f.cfg, settings.SourceFile }
func (f *fakeMQTT) Status() mqtt.Status                        { return mqtt.Status{State: mqtt.StateDisabled} }
func (f *fakeMQTT) Update(next settings.MQTT) error {
	f.updated = append(f.updated, next)
	f.cfg = next
	return nil
}
func (f *fakeMQTT) TestConnection(_ context.Context, cfg settings.MQTT) error {
	f.tested = append(f.tested, cfg)
	return nil
}

func TestMQTTSettingsHidePassword(t *testing.T) {
	m := &fakeMQTT{cfg: settings.MQTT{Enabled: true, URL: "tcp://ha:1883", Password: "hunter2"}}
	srv := httptest.NewServer(NewHandler(&fakeBackend{}, m, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	defer srv.Close()

	resp := do(t, http.MethodGet, srv.URL+"/api/settings/mqtt", "", "")
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "hunter2") || !strings.Contains(string(body), `"password_set":true`) {
		t.Errorf("body %s", body)
	}
}

func TestMQTTSettingsKeepPasswordWhenOmitted(t *testing.T) {
	m := &fakeMQTT{cfg: settings.MQTT{Password: "hunter2"}}
	srv := httptest.NewServer(NewHandler(&fakeBackend{}, m, Options{APIToken: "s", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	defer srv.Close()

	if resp := do(t, http.MethodPut, srv.URL+"/api/settings/mqtt", "", `{"enabled":true,"url":"tcp://ha:1883"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without token: status %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPut, srv.URL+"/api/settings/mqtt", "s", `{"enabled":true,"url":"tcp://ha:1883"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := m.updated[0].Password; got != "hunter2" {
		t.Errorf("password %q, want kept", got)
	}
	if resp := do(t, http.MethodPut, srv.URL+"/api/settings/mqtt", "s", `{"enabled":true,"url":"tcp://ha:1883","password":""}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := m.updated[1].Password; got != "" {
		t.Errorf("password %q, want cleared", got)
	}
}
