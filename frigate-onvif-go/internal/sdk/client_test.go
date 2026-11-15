package sdk

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortSocketPath keeps the UNIX socket path below the platform limit
// (108 bytes on Linux, 104 on macOS), which t.TempDir can exceed.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ptzsock")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// serveSocket starts a one-command-per-connection fake daemon. reply returns
// the response line; an empty response leaves the client without a reply.
func serveSocket(t *testing.T, path string, reply func(message string) string) {
	t.Helper()

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
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
				if out := reply(strings.TrimSpace(line)); out != "" {
					fmt.Fprintln(conn, out)
				}
			}(conn)
		}
	}()
}

func TestClientPingAndCommand(t *testing.T) {
	path := shortSocketPath(t)

	var mu sync.Mutex
	var seen []string
	serveSocket(t, path, func(message string) string {
		mu.Lock()
		seen = append(seen, message)
		mu.Unlock()
		if message == "PING" {
			return "PONG"
		}
		return "OK"
	})

	client := NewClient(path, time.Second)
	if !client.Ping() {
		t.Fatal("Ping() = false, want true")
	}
	if err := client.Command(DirectionRight, "START", 4); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if err := client.Command(DirectionLeft, "STOP", 4); err != nil {
		t.Fatalf("Command: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"PING", "RIGHT START 4", "LEFT STOP"}
	if len(seen) != len(want) {
		t.Fatalf("messages = %q, want %q", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("messages = %q, want %q", seen, want)
		}
	}
}

func TestClientErrorReply(t *testing.T) {
	path := shortSocketPath(t)
	serveSocket(t, path, func(string) string { return "ERR 7" })

	client := NewClient(path, time.Second)
	err := client.Command(DirectionUp, "START", 3)
	if err == nil {
		t.Fatal("Command() = nil, want error for ERR reply")
	}
	if !strings.Contains(err.Error(), "ERR 7") {
		t.Fatalf("error = %q, want it to contain the daemon reply", err)
	}
	if client.Ping() {
		t.Fatal("Ping() = true, want false for an ERR reply")
	}
}

func TestClientEmptyReply(t *testing.T) {
	path := shortSocketPath(t)
	serveSocket(t, path, func(string) string { return "" })

	client := NewClient(path, time.Second)
	err := client.Command(DirectionDown, "STOP", 0)
	if err == nil {
		t.Fatal("Command() = nil, want error for an empty reply")
	}
	if !strings.Contains(err.Error(), "empty reply") {
		t.Fatalf("error = %q, want it to mention the empty reply", err)
	}
}

func TestClientRejectsUnknownDirection(t *testing.T) {
	client := NewClient(shortSocketPath(t), time.Second)
	if err := client.Command("SIDEWAYS", "START", 1); err == nil {
		t.Fatal("Command() = nil, want error for an unknown direction")
	}
}

func TestClientMissingSocket(t *testing.T) {
	client := NewClient(shortSocketPath(t), time.Second)
	if err := client.Command(DirectionLeft, "START", 1); err == nil {
		t.Fatal("Command() = nil, want error when the daemon is not running")
	}
	if client.Ping() {
		t.Fatal("Ping() = true, want false when the daemon is not running")
	}
}

func TestClientCameraProbe(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		path := shortSocketPath(t)
		serveSocket(t, path, func(message string) string {
			if message == "POS" {
				return "POS 1 2 3"
			}
			return "ERR 7"
		})

		if err := NewClient(path, time.Second).CameraProbe(); err != nil {
			t.Fatalf("CameraProbe() = %v, want nil", err)
		}
	})

	t.Run("daemon error", func(t *testing.T) {
		path := shortSocketPath(t)
		serveSocket(t, path, func(string) string { return "ERR 7" })

		err := NewClient(path, time.Second).CameraProbe()
		if err == nil || !strings.Contains(err.Error(), "ERR 7") {
			t.Fatalf("CameraProbe() = %v, want the daemon error", err)
		}
	})

	t.Run("unexpected reply", func(t *testing.T) {
		path := shortSocketPath(t)
		serveSocket(t, path, func(string) string { return "OK" })

		err := NewClient(path, time.Second).CameraProbe()
		if err == nil || !strings.Contains(err.Error(), "unexpected POS reply") {
			t.Fatalf("CameraProbe() = %v, want an unexpected-reply error", err)
		}
	})
}

func TestClientTimesOut(t *testing.T) {
	path := shortSocketPath(t)
	serveSocket(t, path, func(string) string {
		time.Sleep(500 * time.Millisecond)
		return "OK"
	})

	client := NewClient(path, 50*time.Millisecond)
	start := time.Now()
	if err := client.Command(DirectionLeft, "START", 1); err == nil {
		t.Fatal("Command() = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("Command blocked for %v, want it to time out", elapsed)
	}
}
