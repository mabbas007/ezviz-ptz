package sdk

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frigate-onvif-go/internal/config"
)

// TestDaemonHelperProcess is not a real test: the daemon tests re-execute the
// test binary with PTZ_HELPER=1 to obtain a fake ptz_control process that
// speaks the daemon protocol on PTZ_HELPER_SOCK.
func TestDaemonHelperProcess(t *testing.T) {
	if os.Getenv("PTZ_HELPER") != "1" {
		t.Skip("helper process for the daemon tests")
	}

	if os.Getenv("PTZ_HELPER_NO_LISTEN") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}

	socket := os.Getenv("PTZ_HELPER_SOCK")
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: listen on %s: %v\n", socket, err)
		os.Exit(1)
	}

	exitOn := os.Getenv("PTZ_HELPER_EXIT_ON")
	posErr := os.Getenv("PTZ_HELPER_POS_ERR") == "1"
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				return
			}
			message := strings.TrimSpace(line)
			if exitOn != "" && message == exitOn {
				os.Exit(3)
			}
			switch {
			case message == "PING":
				fmt.Fprintln(conn, "PONG")
			case message == "INFO":
				fmt.Fprintln(conn, "INFO enc=H265 width=3840 height=2160 fps=30 bitrate=4096")
			case strings.HasPrefix(message, "POS"):
				if posErr {
					fmt.Fprintln(conn, "ERR 7")
				} else {
					fmt.Fprintln(conn, "POS 0 0 0")
				}
			default:
				fmt.Fprintln(conn, "OK")
			}
		}(conn)
	}
}

func testDaemonConfig(socket string) *config.Config {
	return &config.Config{
		CameraHost:     "127.0.0.1",
		CameraPort:     8000,
		CameraUser:     "admin",
		CameraPassword: "secret",
		PtzSocket:      socket,
		PtzBinary:      "/nonexistent/ptz_control",
		StartupTimeout: 5.0,
		HealthInterval: 0.05,
	}
}

func helperCommand(socket, exitOn string, spawns *int32) func() *exec.Cmd {
	return func() *exec.Cmd {
		atomic.AddInt32(spawns, 1)
		cmd := exec.Command(os.Args[0], "-test.run=TestDaemonHelperProcess")
		cmd.Env = append(os.Environ(),
			"PTZ_HELPER=1",
			"PTZ_HELPER_SOCK="+socket,
		)
		if exitOn != "" {
			cmd.Env = append(cmd.Env, "PTZ_HELPER_EXIT_ON="+exitOn)
		}
		return cmd
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

func TestDaemonStartsCleansSocketAndStops(t *testing.T) {
	socket := shortSocketPath(t)
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create stale socket file: %v", err)
	}

	cfg := testDaemonConfig(socket)
	client := NewClient(socket, 2*time.Second)
	daemon := NewDaemon(cfg, client)

	var spawns int32
	daemon.newCommand = helperCommand(socket, "", &spawns)
	daemon.Start()
	t.Cleanup(daemon.Stop)

	waitFor(t, 5*time.Second, "the daemon to answer PING", client.Ping)
	if !daemon.Running() {
		t.Fatal("Running() = false, want true")
	}

	status := daemon.Status()
	if !status.Running {
		t.Fatal("Status().Running = false, want true")
	}
	if status.PID == nil || *status.PID <= 0 {
		t.Fatalf("Status().PID = %v, want a live pid", status.PID)
	}
	if status.Socket != socket {
		t.Fatalf("Status().Socket = %q, want %q", status.Socket, socket)
	}

	daemon.Stop()
	if daemon.Running() {
		t.Fatal("Running() = true after Stop")
	}
	status = daemon.Status()
	if status.Running || status.PID != nil {
		t.Fatalf("Status() after Stop = %+v, want not running", status)
	}
}

func TestDaemonCachesVideoInfo(t *testing.T) {
	socket := shortSocketPath(t)
	cfg := testDaemonConfig(socket)
	client := NewClient(socket, 2*time.Second)
	daemon := NewDaemon(cfg, client)

	var spawns int32
	daemon.newCommand = helperCommand(socket, "", &spawns)
	daemon.Start()
	t.Cleanup(daemon.Stop)

	waitFor(t, 5*time.Second, "the camera video info", func() bool {
		info, ok := daemon.VideoInfo()
		return ok && info.Encoding == "H265" && info.Width == 3840 &&
			info.Height == 2160 && info.Fps == 30 && info.Bitrate == 4096
	})
}

func TestDaemonRestartsAfterCrash(t *testing.T) {
	socket := shortSocketPath(t)
	cfg := testDaemonConfig(socket)
	client := NewClient(socket, 2*time.Second)
	daemon := NewDaemon(cfg, client)

	var spawns int32
	daemon.newCommand = helperCommand(socket, "LEFT START 4", &spawns)
	daemon.Start()
	t.Cleanup(daemon.Stop)

	waitFor(t, 5*time.Second, "the daemon to answer PING", client.Ping)

	// The helper exits before replying, so this call fails; the supervisor
	// must notice and spawn a fresh process.
	if err := client.Command(DirectionLeft, "START", 4); err == nil {
		t.Fatal("Command() = nil, want an error after the helper exited")
	}
	waitFor(t, 5*time.Second, "the daemon to be restarted", func() bool {
		return atomic.LoadInt32(&spawns) >= 2
	})
	waitFor(t, 5*time.Second, "the restarted daemon to answer PING", client.Ping)
	if !daemon.Running() {
		t.Fatal("Running() = false after restart")
	}
}

func TestDaemonStopImmediately(t *testing.T) {
	for i := 0; i < 5; i++ {
		socket := shortSocketPath(t)
		cfg := testDaemonConfig(socket)
		client := NewClient(socket, 200*time.Millisecond)
		daemon := NewDaemon(cfg, client)

		var spawns int32
		daemon.newCommand = helperCommand(socket, "", &spawns)

		daemon.Start()
		daemon.Stop()

		if daemon.Running() {
			t.Fatalf("iteration %d: Running() = true after Stop", i)
		}
		if status := daemon.Status(); status.Running || status.PID != nil {
			t.Fatalf("iteration %d: Status() = %+v after Stop", i, status)
		}
	}
}

func TestDaemonRestartsWhenCameraProbeFails(t *testing.T) {
	socket := shortSocketPath(t)
	cfg := testDaemonConfig(socket)
	cfg.HealthInterval = 0.05

	client := NewClient(socket, 2*time.Second)
	daemon := NewDaemon(cfg, client)

	var spawns int32
	daemon.newCommand = func() *exec.Cmd {
		atomic.AddInt32(&spawns, 1)
		cmd := exec.Command(os.Args[0], "-test.run=TestDaemonHelperProcess")
		cmd.Env = append(os.Environ(),
			"PTZ_HELPER=1",
			"PTZ_HELPER_SOCK="+socket,
			"PTZ_HELPER_POS_ERR=1",
		)
		return cmd
	}

	daemon.Start()
	t.Cleanup(daemon.Stop)

	// PING still succeeds, but the camera probe returns ERR 7. The supervisor
	// must treat that as an unhealthy camera and restart the daemon.
	waitFor(t, 5*time.Second, "the daemon to answer PING", client.Ping)
	waitFor(t, 5*time.Second, "a restart triggered by the camera probe", func() bool {
		return atomic.LoadInt32(&spawns) >= 2
	})
}

func TestDaemonStartupTimeout(t *testing.T) {
	socket := shortSocketPath(t)
	cfg := testDaemonConfig(socket)
	cfg.StartupTimeout = 0.3

	client := NewClient(socket, 200*time.Millisecond)
	daemon := NewDaemon(cfg, client)

	var spawns int32
	daemon.newCommand = func() *exec.Cmd {
		atomic.AddInt32(&spawns, 1)
		cmd := exec.Command(os.Args[0], "-test.run=TestDaemonHelperProcess")
		cmd.Env = append(os.Environ(),
			"PTZ_HELPER=1",
			"PTZ_HELPER_NO_LISTEN=1",
			"PTZ_HELPER_SOCK="+socket,
		)
		return cmd
	}
	daemon.Start()

	waitFor(t, 3*time.Second, "the first spawn attempt", func() bool {
		return atomic.LoadInt32(&spawns) >= 1
	})
	start := time.Now()
	daemon.Stop()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Stop took %v, want it to kill the stuck daemon promptly", elapsed)
	}
	if daemon.Running() {
		t.Fatal("Running() = true after Stop")
	}
}
