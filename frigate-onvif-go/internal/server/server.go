package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"frigate-onvif-go/internal/config"
	"frigate-onvif-go/internal/onvif"
	"frigate-onvif-go/internal/sdk"
)

const maxRequestBytes = 1024 * 1024

// Daemon is the supervision state the health endpoint reports.
type Daemon interface {
	Running() bool
	Status() sdk.Status
}

// Server is the HTTP/SOAP front end of the bridge.
type Server struct {
	daemon Daemon
	app    *onvif.App

	mu   sync.Mutex
	http *http.Server
}

// New returns a server for cfg, reporting daemon health and driving controller
// through the ONVIF application. video may be nil.
func New(cfg *config.Config, daemon Daemon, controller onvif.Controller, video onvif.VideoInfoProvider) *Server {
	return &Server{
		daemon: daemon,
		app:    onvif.NewApp(cfg, controller, video),
	}
}

// ListenAndServe serves the bridge until Shutdown is called.
func (s *Server) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.mu.Lock()
	s.http = srv
	s.mu.Unlock()

	slog.Info("Starting ONVIF server", "addr", addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.http
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("panic while serving HTTP request", "path", r.URL.Path, "panic", recovered)
			plain(w, http.StatusInternalServerError, "internal error")
		}
	}()

	path := strings.TrimRight(r.URL.Path, "/")

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if path == "/healthz" || path == "/health" {
			s.handleHealth(w, r)
			return
		}
		plain(w, http.StatusNotFound, "not found")
	case http.MethodPost:
		s.handleSOAP(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		plain(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	running := s.daemon.Running()

	status := "ok"
	code := http.StatusOK
	if !running {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	payload, err := json.Marshal(struct {
		Status string     `json:"status"`
		Daemon sdk.Status `json:"daemon"`
		Moving bool       `json:"moving"`
	}{
		Status: status,
		Daemon: s.daemon.Status(),
		Moving: s.app.Moving(),
	})
	if err != nil {
		plain(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(payload)
	}
}

func (s *Server) handleSOAP(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength <= 0 {
		plain(w, http.StatusLengthRequired, "Content-Length required")
		return
	}
	if r.ContentLength > maxRequestBytes {
		plain(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		plain(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if len(body) > maxRequestBytes {
		plain(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}

	code, payload := s.app.Handle(r.URL.Path, body, r.Host)

	w.Header().Set("Content-Type", "application/soap+xml")
	w.WriteHeader(code)
	_, _ = w.Write(payload)
}

func plain(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, message)
}
