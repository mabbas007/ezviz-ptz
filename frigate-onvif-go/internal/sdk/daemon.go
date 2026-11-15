package sdk

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"frigate-onvif-go/internal/config"
)

// Status is the daemon state reported by the health endpoint.
type Status struct {
	Running bool   `json:"running"`
	PID     *int   `json:"pid"`
	Socket  string `json:"socket"`
}

// Daemon starts "ptz_control serve" and keeps it alive: it waits for the SDK
// login at startup, pings the daemon periodically, restarts it when it dies or
// stops answering, and backs off exponentially while the camera is unreachable.
//
// This is a port of PtzDaemon in frigate-onvif/app/sdk.py.
type Daemon struct {
	cfg    *config.Config
	client *Client

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}

	started  bool
	stopped  chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once

	// Shared by the supervisor goroutine only.
	failures int
	backoff  time.Duration

	// newCommand is replaced in tests to spawn a fake daemon.
	newCommand func() *exec.Cmd
}

// NewDaemon returns a supervisor for the PTZ daemon described by cfg.
func NewDaemon(cfg *config.Config, client *Client) *Daemon {
	d := &Daemon{
		cfg:     cfg,
		client:  client,
		stopCh:  make(chan struct{}),
		backoff: time.Second,
	}
	d.newCommand = d.command
	return d
}

// Start begins supervising the daemon in the background.
func (d *Daemon) Start() {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	d.started = true
	d.stopped = make(chan struct{})
	d.mu.Unlock()

	go d.supervise()
}

// Stop terminates the daemon and waits for the supervisor to finish.
func (d *Daemon) Stop() {
	d.mu.Lock()
	started := d.started
	stopped := d.stopped
	d.mu.Unlock()
	if !started {
		return
	}

	d.stopOnce.Do(func() { close(d.stopCh) })
	d.terminate()
	<-stopped
}

// Running reports whether the supervised process is alive. It says nothing
// about the camera connection; use Client.Ping for that.
func (d *Daemon) Running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cmd != nil && d.cmd.Process != nil
}

// Status returns the daemon state for the health endpoint.
func (d *Daemon) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()

	status := Status{Socket: d.cfg.PtzSocket}
	if d.cmd != nil && d.cmd.Process != nil {
		pid := d.cmd.Process.Pid
		status.Running = true
		status.PID = &pid
	}
	return status
}

func (d *Daemon) supervise() {
	defer close(d.stopped)

	for {
		if d.stopping() {
			return
		}

		if d.Running() {
			if !d.healthy() {
				slog.Warn("PTZ daemon is not answering, restarting it (check the camera connection)")
				d.terminate()
			}
		} else {
			if d.spawn() {
				d.failures = 0
				d.backoff = time.Second
			} else {
				d.failures++
				d.backoff *= 2
				if d.backoff > 60*time.Second {
					d.backoff = 60 * time.Second
				}
				slog.Error("PTZ daemon did not start",
					"attempt", d.failures,
					"retry_in_s", int(d.backoff.Seconds()),
					"hint", "Login failed 1 means CAMERA_USER / CAMERA_PASSWORD are wrong "+
						"(use the camera's verification code); Login failed 7 means the camera "+
						"is unreachable on CAMERA_HOST:CAMERA_PORT",
				)
			}
		}

		wait := d.backoff
		if d.Running() {
			wait = time.Duration(d.cfg.HealthInterval * float64(time.Second))
		}
		if wait <= 0 {
			wait = time.Millisecond
		}

		timer := time.NewTimer(wait)
		select {
		case <-d.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// healthy pings the daemon. Three consecutive failures restart the process.
func (d *Daemon) healthy() bool {
	if d.client.Ping() {
		d.failures = 0
		return true
	}
	d.failures++
	return d.failures < 3
}

func (d *Daemon) spawn() bool {
	if err := os.Remove(d.cfg.PtzSocket); err != nil && !os.IsNotExist(err) {
		slog.Warn("Could not remove stale socket", "path", d.cfg.PtzSocket, "err", err)
	}

	slog.Info("Starting PTZ daemon",
		"host", d.cfg.CameraHost,
		"port", d.cfg.CameraPort,
		"user", d.cfg.CameraUser,
	)

	cmd := d.newCommand()
	pr, pw, err := os.Pipe()
	if err != nil {
		slog.Error("Could not create PTZ daemon pipe", "err", err)
		return false
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	done := make(chan struct{})
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		slog.Error("Could not start PTZ daemon", "binary", d.cfg.PtzBinary, "err", err)
		return false
	}
	pw.Close()

	// Register the live process before starting the reaper; doing it after
	// Start avoids the reaper clearing a not-yet-registered process.
	d.mu.Lock()
	d.cmd = cmd
	d.done = done
	d.mu.Unlock()

	go d.pumpOutput(pr)
	go func() {
		waitErr := cmd.Wait()

		d.mu.Lock()
		if d.cmd == cmd {
			d.cmd = nil
		}
		d.mu.Unlock()
		close(done)

		if waitErr != nil {
			slog.Warn("PTZ daemon exited", "err", waitErr)
		} else {
			slog.Info("PTZ daemon exited")
		}
	}()

	if d.stopping() {
		d.terminate()
		return false
	}

	deadline := time.Now().Add(time.Duration(d.cfg.StartupTimeout * float64(time.Second)))
	for time.Now().Before(deadline) {
		if d.stopping() {
			d.terminate()
			return false
		}
		if !d.Running() {
			slog.Warn("PTZ daemon exited during startup")
			return false
		}
		if d.client.Ping() {
			slog.Info("PTZ daemon ready", "socket", d.cfg.PtzSocket)
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}

	slog.Error("PTZ daemon did not become ready", "timeout_s", int(d.cfg.StartupTimeout))
	d.terminate()
	return false
}

func (d *Daemon) pumpOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			slog.Info("ptz_control", "line", line)
		}
	}
}

// terminate stops the current process, escalating to SIGKILL after five
// seconds. Safe to call concurrently and when nothing is running.
func (d *Daemon) terminate() {
	d.mu.Lock()
	cmd := d.cmd
	done := d.done
	d.cmd = nil
	d.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	slog.Info("Stopping PTZ daemon", "pid", cmd.Process.Pid)
	_ = cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
		slog.Info("PTZ daemon stopped")
	case <-time.After(5 * time.Second):
		slog.Warn("PTZ daemon did not exit, killing", "pid", cmd.Process.Pid)
		_ = cmd.Process.Kill()
		<-done
	}
}

func (d *Daemon) stopping() bool {
	select {
	case <-d.stopCh:
		return true
	default:
		return false
	}
}

func (d *Daemon) command() *exec.Cmd {
	return exec.Command(
		d.cfg.PtzBinary,
		"serve",
		d.cfg.CameraHost,
		strconv.Itoa(d.cfg.CameraPort),
		d.cfg.CameraUser,
		d.cfg.CameraPassword,
		d.cfg.PtzSocket,
	)
}
