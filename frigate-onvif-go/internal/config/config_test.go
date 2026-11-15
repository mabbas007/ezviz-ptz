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
