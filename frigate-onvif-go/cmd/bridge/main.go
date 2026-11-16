package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"frigate-onvif-go/internal/config"
	"frigate-onvif-go/internal/logging"
	"frigate-onvif-go/internal/ptz"
	"frigate-onvif-go/internal/sdk"
	"frigate-onvif-go/internal/server"
)

func main() {
	// `bridge healthcheck` is used by the Docker HEALTHCHECK; it must not
	// require camera credentials or start the daemon.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}

	logging.Setup(os.Getenv("LOG_LEVEL"))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	logging.Setup(cfg.LogLevel)

	client := sdk.NewClient(cfg.PtzSocket, 5*time.Second)
	controller := ptz.NewController(client, cfg)
	daemon := sdk.NewDaemon(cfg, client)
	srv := server.New(cfg, daemon, controller, daemon)

	// The HTTP server starts immediately and reports "degraded" until the SDK
	// daemon has logged into the camera. Supervision (startup, health checks,
	// restarts) happens in the background.
	daemon.Start()

	addr := fmt.Sprintf("%s:%d", cfg.BindingHost, cfg.OnvifPort)
	warnIfPortBusy(addr)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe(addr)
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	exitCode := 0
	select {
	case err := <-serverErr:
		if err != nil {
			slog.Error("server error", "err", err)
			exitCode = 1
		}
	case sig := <-sigChan:
		slog.Info("shutting down", "signal", sig.String())
	}

	// Keep the total under compose's 15s stop_grace_period (8s HTTP drain +
	// up to 5s for the daemon to exit).
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("HTTP shutdown", "err", err)
	}
	daemon.Stop()

	slog.Info("stopped")
	os.Exit(exitCode)
}

func warnIfPortBusy(addr string) {
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("port is busy; the server will still try to bind", "addr", addr, "err", err)
		return
	}
	_ = probe.Close()
}

// runHealthcheck polls the local /healthz endpoint and returns a process exit
// code suitable for Docker's HEALTHCHECK.
func runHealthcheck() int {
	host := "127.0.0.1"
	if bind := strings.TrimSpace(os.Getenv("ONVIF_BIND")); bind != "" &&
		bind != "0.0.0.0" && bind != "::" && bind != "[::]" {
		host = bind
	}

	port := strings.TrimSpace(os.Getenv("ONVIF_PORT"))
	if port == "" {
		port = "8000"
	}

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return 0
	}
	return 1
}
