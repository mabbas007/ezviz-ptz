package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frigate-onvif-go/internal/config"
	"frigate-onvif-go/internal/sdk"
)

type fakeDaemon struct {
	running bool
}

func (d *fakeDaemon) Running() bool { return d.running }

func (d *fakeDaemon) Status() sdk.Status {
	status := sdk.Status{Running: d.running, Socket: "/tmp/ptz_control.sock"}
	if d.running {
		pid := 42
		status.PID = &pid
	}
	return status
}

type fakeController struct {
	moving bool
	stops  int
}

func (c *fakeController) ContinuousMove(_, _, _ float64, _ *float64) {}
func (c *fakeController) RelativeMove(_, _, _ float64, _ *float64)   {}
func (c *fakeController) Stop()                                      { c.stops++ }
func (c *fakeController) PanTiltMoving() bool                        { return c.moving }
func (c *fakeController) ZoomMoving() bool                           { return false }
func (c *fakeController) Moving() bool                               { return c.moving }

func newTestServer(t *testing.T) (*Server, *fakeDaemon, *fakeController) {
	t.Helper()

	cfg := &config.Config{
		OnvifPort:    8000,
		ProfileToken: "profile_1",
		VideoWidth:   1920,
		VideoHeight:  1080,
		VideoFps:     25,
		ZoomEnabled:  true,
	}
	daemon := &fakeDaemon{running: true}
	controller := &fakeController{}
	return New(cfg, daemon, controller, nil), daemon, controller
}

func do(s *Server, method, target, body string, contentLength int64) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}

	request := httptest.NewRequest(method, target, reader)
	if contentLength >= 0 {
		request.ContentLength = contentLength
	}
	recorder := httptest.NewRecorder()
	s.route(recorder, request)
	return recorder
}

func TestHealth(t *testing.T) {
	srv, daemon, controller := newTestServer(t)
	controller.moving = true

	recorder := do(srv, http.MethodGet, "/healthz", "", -1)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var payload struct {
		Status string `json:"status"`
		Daemon struct {
			Running bool   `json:"running"`
			PID     *int   `json:"pid"`
			Socket  string `json:"socket"`
		} `json:"daemon"`
		Moving bool `json:"moving"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("health payload is not JSON: %v", err)
	}
	if payload.Status != "ok" || !payload.Daemon.Running || payload.Daemon.PID == nil ||
		payload.Daemon.Socket != "/tmp/ptz_control.sock" || !payload.Moving {
		t.Fatalf("health payload = %+v, want a healthy, moving daemon", payload)
	}

	daemon.running = false
	controller.moving = false
	recorder = do(srv, http.MethodGet, "/health/", "", -1)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded status = %d, want 503", recorder.Code)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("health payload is not JSON: %v", err)
	}
	if payload.Status != "degraded" || payload.Daemon.Running || payload.Daemon.PID != nil {
		t.Fatalf("degraded payload = %+v", payload)
	}
}

func TestHealthHeadHasNoBody(t *testing.T) {
	srv, _, _ := newTestServer(t)

	recorder := do(srv, http.MethodHead, "/healthz", "", -1)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD response body = %q, want empty", recorder.Body.String())
	}
}

func TestRouting(t *testing.T) {
	srv, _, _ := newTestServer(t)

	recorder := do(srv, http.MethodGet, "/nope", "", -1)
	if recorder.Code != http.StatusNotFound || recorder.Body.String() != "not found" {
		t.Fatalf("GET /nope = (%d, %q)", recorder.Code, recorder.Body.String())
	}

	recorder = do(srv, http.MethodPut, "/healthz", "", -1)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /healthz status = %d, want 405", recorder.Code)
	}
	if recorder.Header().Get("Allow") != "GET, HEAD, POST" {
		t.Fatalf("Allow = %q", recorder.Header().Get("Allow"))
	}
}

func TestSOAPRequest(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := `<?xml version="1.0"?><env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope"` +
		` xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><env:Body><tds:GetServices/>` +
		`</env:Body></env:Envelope>`

	recorder := do(srv, http.MethodPost, "/onvif/device_service", body, int64(len(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/soap+xml" {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(recorder.Body.String(), "GetServicesResponse") {
		t.Fatalf("body does not contain GetServicesResponse:\n%s", recorder.Body.String())
	}

	recorder = do(srv, http.MethodPost, "/unknown", body, int64(len(body)))
	if recorder.Code != http.StatusNotFound || recorder.Body.String() != "not found" {
		t.Fatalf("POST /unknown = (%d, %q), want 404", recorder.Code, recorder.Body.String())
	}
}

func TestSOAPRequestLimits(t *testing.T) {
	srv, _, _ := newTestServer(t)

	recorder := do(srv, http.MethodPost, "/onvif/device_service", "", 0)
	if recorder.Code != http.StatusLengthRequired {
		t.Fatalf("missing Content-Length status = %d, want 411", recorder.Code)
	}

	recorder = do(srv, http.MethodPost, "/onvif/device_service", "<x/>", maxRequestBytes+1)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413", recorder.Code)
	}
}

func TestPostWritesSOAPFault(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := `<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope">` +
		`<env:Body><env:NotAnOperation/></env:Body></env:Envelope>`
	recorder := do(srv, http.MethodPost, "/onvif/device_service", body, int64(len(body)))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "ActionNotSupported") {
		t.Fatalf("fault body:\n%s", recorder.Body.String())
	}
}
