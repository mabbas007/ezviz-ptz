// Package sdk talks to the ptz_control daemon and supervises it.
//
// The daemon logs into the camera with the Hikvision SDK once and then accepts
// newline-delimited commands on a UNIX socket:
//
//	PING                        -> PONG
//	POS                         -> POS <pan> <tilt> <zoom> | ERR <code>
//	INFO                        -> INFO enc=... width=... height=... fps=... bitrate=... | ERR <code>
//	LEFT START [speed]          -> OK | ERR <code>
//	LEFT STOP                   -> OK | ERR <code>
//
// This is a port of frigate-onvif/app/sdk.py.
package sdk

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PTZ directions understood by the daemon.
const (
	DirectionLeft    = "LEFT"
	DirectionRight   = "RIGHT"
	DirectionUp      = "UP"
	DirectionDown    = "DOWN"
	DirectionZoomIn  = "ZOOM_IN"
	DirectionZoomOut = "ZOOM_OUT"
)

var validDirections = map[string]struct{}{
	DirectionLeft:    {},
	DirectionRight:   {},
	DirectionUp:      {},
	DirectionDown:    {},
	DirectionZoomIn:  {},
	DirectionZoomOut: {},
}

// Client sends one command per connection to the PTZ daemon.
//
// Calls are serialized: the daemon handles a single client at a time and the
// bridge must not interleave a START with the STOP of the same axis.
type Client struct {
	socketPath string
	timeout    time.Duration
	mu         sync.Mutex
}

// NewClient returns a client for the daemon listening on socketPath. A
// non-positive timeout falls back to five seconds, matching the Python bridge.
func NewClient(socketPath string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{socketPath: socketPath, timeout: timeout}
}

// SocketPath is the daemon socket this client talks to.
func (c *Client) SocketPath() string { return c.socketPath }

// Ping reports whether the daemon answers PING with PONG.
func (c *Client) Ping() bool {
	reply, err := c.call("PING")
	return err == nil && reply == "PONG"
}

// CameraProbe verifies that the daemon can still reach the camera by asking it
// for the current PTZ position. PING only proves the UNIX socket is alive; POS
// exercises the SDK session, so it also detects a camera link that dropped
// after login.
func (c *Client) CameraProbe() error {
	reply, err := c.call("POS")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "POS") {
		return fmt.Errorf("unexpected POS reply %q", reply)
	}
	return nil
}

// Command sends "<DIRECTION> <START|STOP> [speed]".
//
// The speed is only appended for START, like the Python client. Any ERR reply
// or transport failure is returned as an error.
func (c *Client) Command(direction, action string, speed int) error {
	if _, ok := validDirections[direction]; !ok {
		return fmt.Errorf("unknown PTZ direction %q", direction)
	}

	message := direction + " " + action
	if action != "STOP" && speed > 0 {
		message += " " + strconv.Itoa(speed)
	}

	_, err := c.call(message)
	return err
}

// VideoInfo is the encoder metadata the daemon reads from the camera. Zero
// fields mean "the camera did not report it"; callers fall back to the
// configured VIDEO_* values.
type VideoInfo struct {
	Encoding string
	Width    int
	Height   int
	Fps      int
	Bitrate  int
}

// Info asks the daemon for the current encoder configuration. The reply is a
// single line such as:
//
//	INFO enc=H265 width=3840 height=2160 fps=30 bitrate=4096
func (c *Client) Info() (VideoInfo, error) {
	reply, err := c.call("INFO")
	if err != nil {
		return VideoInfo{}, err
	}
	return parseInfo(reply)
}

func parseInfo(reply string) (VideoInfo, error) {
	fields := strings.Fields(reply)
	if len(fields) == 0 || fields[0] != "INFO" {
		return VideoInfo{}, fmt.Errorf("unexpected INFO reply %q", reply)
	}

	var info VideoInfo
	for _, field := range fields[1:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(value)
		switch key {
		case "enc":
			encoding := strings.ToUpper(value)
			if encoding == "MJPEG" {
				encoding = "JPEG"
			}
			switch encoding {
			case "H264", "H265", "JPEG", "MPEG4":
				info.Encoding = encoding
			}
		case "width":
			if err == nil {
				info.Width = number
			}
		case "height":
			if err == nil {
				info.Height = number
			}
		case "fps":
			if err == nil {
				info.Fps = number
			}
		case "bitrate":
			if err == nil {
				info.Bitrate = number
			}
		}
	}
	return info, nil
}

// call writes one newline-terminated message and reads one reply line.
func (c *Client) call(message string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, err := net.DialTimeout("unix", c.socketPath, c.timeout)
	if err != nil {
		return "", fmt.Errorf("%s: %w", message, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return "", fmt.Errorf("%s: %w", message, err)
	}

	if _, err := conn.Write([]byte(message + "\n")); err != nil {
		return "", fmt.Errorf("%s: %w", message, err)
	}

	line, err := bufio.NewReader(conn).ReadString('\n')
	// Like the Python client, a partial final line is still a valid reply and
	// an immediate EOF is reported as an empty reply rather than an I/O error.
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%s: %w", message, err)
	}

	text := strings.TrimSpace(line)
	if text == "" {
		return "", fmt.Errorf("%s: empty reply from PTZ daemon", message)
	}
	if strings.HasPrefix(text, "ERR") {
		return "", fmt.Errorf("%s: %s", message, text)
	}
	return text, nil
}
