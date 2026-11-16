package config

import (
	"strings"
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("CAMERA_HOST", "192.168.1.10")
	t.Setenv("CAMERA_PASSWORD", "verification-code")
}

func TestLoadRequiresCameraHost(t *testing.T) {
	setRequired(t)
	t.Setenv("CAMERA_HOST", "   ")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CAMERA_HOST is required") {
		t.Fatalf("Load() error = %v, want a required CAMERA_HOST error", err)
	}
}

func TestLoadRequiresCameraPassword(t *testing.T) {
	setRequired(t)
	t.Setenv("CAMERA_PASSWORD", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CAMERA_PASSWORD is required") {
		t.Fatalf("Load() error = %v, want a required CAMERA_PASSWORD error", err)
	}
}

func TestLoadRejectsInvalidNumbers(t *testing.T) {
	setRequired(t)

	t.Setenv("PTZ_SPEED", "fast")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), `PTZ_SPEED must be an integer, got "fast"`) {
		t.Fatalf("Load() error = %v, want an integer parse error", err)
	}

	t.Setenv("PTZ_SPEED", "4")
	t.Setenv("MIN_MOVE_SECONDS", "soon")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), `MIN_MOVE_SECONDS must be a number, got "soon"`) {
		t.Fatalf("Load() error = %v, want a float parse error", err)
	}
}

func TestLoadClampsPTZSpeed(t *testing.T) {
	setRequired(t)

	cases := []struct {
		value string
		want  int
	}{
		{"9", 7},
		{"0", 1},
		{"-3", 1},
		{"5", 5},
	}
	for _, tc := range cases {
		t.Run("PTZ_SPEED="+tc.value, func(t *testing.T) {
			t.Setenv("PTZ_SPEED", tc.value)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.DefaultSpeed != tc.want {
				t.Fatalf("DefaultSpeed = %d, want %d", cfg.DefaultSpeed, tc.want)
			}
		})
	}
}

func TestLoadTrimsAndParsesEnvironment(t *testing.T) {
	setRequired(t)
	t.Setenv("CAMERA_HOST", "  192.168.1.11  ")
	t.Setenv("CAMERA_PORT", " 8001 ")
	t.Setenv("PTZ_ZOOM", "false")
	t.Setenv("PAN_SECONDS_PER_FOV", "1.25")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.CameraHost != "192.168.1.11" {
		t.Fatalf("CameraHost = %q, want trimmed host", cfg.CameraHost)
	}
	if cfg.CameraPort != 8001 {
		t.Fatalf("CameraPort = %d, want 8001", cfg.CameraPort)
	}
	if cfg.ZoomEnabled {
		t.Fatal("ZoomEnabled = true, want false")
	}
	if cfg.PanSecondsPerFov != 1.25 {
		t.Fatalf("PanSecondsPerFov = %v, want 1.25", cfg.PanSecondsPerFov)
	}
}

func TestLoadUnknownBoolIsFalse(t *testing.T) {
	setRequired(t)
	t.Setenv("PTZ_ZOOM", "maybe")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.ZoomEnabled {
		t.Fatal("ZoomEnabled = true for an unrecognized value, want Python-style false")
	}
}

func TestLoadRTSPDefaults(t *testing.T) {
	setRequired(t)
	t.Setenv("CAMERA_USER", "camera-user")
	t.Setenv("CAMERA_PASSWORD", "camera-pass")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.VideoEncoding != "H264" {
		t.Fatalf("VideoEncoding = %q, want H264", cfg.VideoEncoding)
	}
	if cfg.VideoBitrate != 4096 {
		t.Fatalf("VideoBitrate = %d, want 4096", cfg.VideoBitrate)
	}
	if cfg.RtspPort != 554 {
		t.Fatalf("RtspPort = %d, want 554", cfg.RtspPort)
	}
	if cfg.RtspAdvertiseHost != cfg.CameraHost {
		t.Fatalf("RtspAdvertiseHost = %q, want the camera host", cfg.RtspAdvertiseHost)
	}
	if cfg.RtspUser != "camera-user" || cfg.RtspPassword != "camera-pass" {
		t.Fatalf("RTSP credentials = %q/%q, want the camera credentials",
			cfg.RtspUser, cfg.RtspPassword)
	}
	if !cfg.RtspIncludeCredentials {
		t.Fatal("RtspIncludeCredentials = false, want true by default")
	}
}

func TestLoadRTSPOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("RTSP_PORT", "8554")
	t.Setenv("RTSP_PATH", "/Streaming/Channels/101")
	t.Setenv("RTSP_ADVERTISE_HOST", "camera.example")
	t.Setenv("RTSP_USER", "rtsp-user")
	t.Setenv("RTSP_PASSWORD", "rtsp-pass")
	t.Setenv("RTSP_INCLUDE_CREDENTIALS", "false")
	t.Setenv("VIDEO_ENCODING", "h265")
	t.Setenv("VIDEO_BITRATE", "2048")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.RtspPort != 8554 || cfg.RtspPath != "/Streaming/Channels/101" ||
		cfg.RtspAdvertiseHost != "camera.example" {
		t.Fatalf("RTSP settings = %+v", cfg)
	}
	if cfg.RtspUser != "rtsp-user" || cfg.RtspPassword != "rtsp-pass" {
		t.Fatalf("RTSP credentials = %q/%q", cfg.RtspUser, cfg.RtspPassword)
	}
	if cfg.RtspIncludeCredentials {
		t.Fatal("RtspIncludeCredentials = true, want false")
	}
	if cfg.VideoEncoding != "H265" || cfg.VideoBitrate != 2048 {
		t.Fatalf("video settings = %q/%d", cfg.VideoEncoding, cfg.VideoBitrate)
	}
}

func TestLoadNormalizesMJPEG(t *testing.T) {
	setRequired(t)
	t.Setenv("VIDEO_ENCODING", "MJPEG")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.VideoEncoding != "JPEG" {
		t.Fatalf("VideoEncoding = %q, want JPEG", cfg.VideoEncoding)
	}
}

func TestLoadRejectsInvalidRTSPSettings(t *testing.T) {
	setRequired(t)

	t.Setenv("RTSP_PORT", "70000")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RTSP_PORT") {
		t.Fatalf("Load() error = %v, want an RTSP_PORT error", err)
	}

	t.Setenv("RTSP_PORT", "554")
	t.Setenv("VIDEO_ENCODING", "VP9")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "VIDEO_ENCODING") {
		t.Fatalf("Load() error = %v, want a VIDEO_ENCODING error", err)
	}
}
