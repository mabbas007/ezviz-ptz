// Package config loads the bridge configuration from environment variables.
//
// Everything is read from the environment so the container needs no config
// file. The defaults and validation mirror frigate-onvif/app/config.py.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// --- camera / Hikvision SDK -------------------------------------------
	CameraHost     string
	CameraPort     int
	CameraUser     string
	CameraPassword string
	// Inside the container one camera == one process, so a fixed path is fine.
	PtzSocket string
	PtzBinary string

	// --- ONVIF server ------------------------------------------------------
	BindingHost   string
	OnvifPort     int
	OnvifUser     string
	OnvifPassword string
	// Overrides the host used in the XAddrs sent to clients (default: Host header)
	AdvertiseHost string

	// --- PTZ behaviour -----------------------------------------------------
	DefaultSpeed      int // Hikvision speed 1..7
	PanSecondsPerFov  float64
	TiltSecondsPerFov float64
	MinMoveSeconds    float64
	MaxMoveSeconds    float64
	ZoomEnabled       bool

	// --- media profile -----------------------------------------------------
	ProfileToken string
	VideoWidth   int
	VideoHeight  int
	VideoFps     int
	RtspUrl      string

	// --- housekeeping ------------------------------------------------------
	LogLevel       string
	StartupTimeout float64
	HealthInterval float64
}

// Load reads the environment and validates it. Errors match the Python
// bridge's fail-fast behaviour so a bad deployment is visible immediately.
func Load() (*Config, error) {
	cameraPort, err := getEnvInt("CAMERA_PORT", 8000)
	if err != nil {
		return nil, err
	}
	onvifPort, err := getEnvInt("ONVIF_PORT", 8000)
	if err != nil {
		return nil, err
	}
	defaultSpeed, err := getEnvInt("PTZ_SPEED", 4)
	if err != nil {
		return nil, err
	}
	videoWidth, err := getEnvInt("VIDEO_WIDTH", 1920)
	if err != nil {
		return nil, err
	}
	videoHeight, err := getEnvInt("VIDEO_HEIGHT", 1080)
	if err != nil {
		return nil, err
	}
	videoFps, err := getEnvInt("VIDEO_FPS", 25)
	if err != nil {
		return nil, err
	}
	panSeconds, err := getEnvFloat("PAN_SECONDS_PER_FOV", 0.9)
	if err != nil {
		return nil, err
	}
	tiltSeconds, err := getEnvFloat("TILT_SECONDS_PER_FOV", 0.7)
	if err != nil {
		return nil, err
	}
	minMove, err := getEnvFloat("MIN_MOVE_SECONDS", 0.10)
	if err != nil {
		return nil, err
	}
	maxMove, err := getEnvFloat("MAX_MOVE_SECONDS", 4.0)
	if err != nil {
		return nil, err
	}
	startupTimeout, err := getEnvFloat("STARTUP_TIMEOUT", 20.0)
	if err != nil {
		return nil, err
	}
	healthInterval, err := getEnvFloat("HEALTH_INTERVAL", 20.0)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		CameraHost:     getEnv("CAMERA_HOST", ""),
		CameraPort:     cameraPort,
		CameraUser:     getEnv("CAMERA_USER", "admin"),
		CameraPassword: getEnv("CAMERA_PASSWORD", ""),
		PtzSocket:      getEnv("PTZ_SOCK", "/tmp/ptz_control.sock"),
		PtzBinary:      getEnv("PTZ_BINARY", "/app/ptz_control"),

		BindingHost:   getEnv("ONVIF_BIND", "0.0.0.0"),
		OnvifPort:     onvifPort,
		OnvifUser:     getEnv("ONVIF_USER", ""),
		OnvifPassword: getEnv("ONVIF_PASSWORD", ""),
		AdvertiseHost: getEnv("ONVIF_ADVERTISE_HOST", ""),

		DefaultSpeed:      defaultSpeed,
		PanSecondsPerFov:  panSeconds,
		TiltSecondsPerFov: tiltSeconds,
		MinMoveSeconds:    minMove,
		MaxMoveSeconds:    maxMove,
		ZoomEnabled:       getEnvBool("PTZ_ZOOM", true),

		ProfileToken: getEnv("PROFILE_TOKEN", "profile_1"),
		VideoWidth:   videoWidth,
		VideoHeight:  videoHeight,
		VideoFps:     videoFps,
		RtspUrl:      getEnv("RTSP_URL", ""),

		LogLevel:       strings.ToUpper(getEnv("LOG_LEVEL", "INFO")),
		StartupTimeout: startupTimeout,
		HealthInterval: healthInterval,
	}

	if cfg.CameraHost == "" {
		return nil, fmt.Errorf("CAMERA_HOST is required (e.g. CAMERA_HOST=192.168.1.10)")
	}
	if cfg.CameraPassword == "" {
		return nil, fmt.Errorf("CAMERA_PASSWORD is required (the camera's verification code)")
	}

	// The camera accepts speeds 1..7.
	if cfg.DefaultSpeed < 1 {
		cfg.DefaultSpeed = 1
	}
	if cfg.DefaultSpeed > 7 {
		cfg.DefaultSpeed = 7
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func getEnvInt(key string, fallback int) (int, error) {
	raw := getEnv(key, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	return value, nil
}

func getEnvFloat(key string, fallback float64) (float64, error) {
	raw := getEnv(key, "")
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", key, raw)
	}
	return value, nil
}

// getEnvBool treats "1", "true", "yes" and "on" as true and anything else as
// false, like the Python bridge. An unset or empty value uses the fallback.
func getEnvBool(key string, fallback bool) bool {
	raw := strings.ToLower(getEnv(key, ""))
	if raw == "" {
		return fallback
	}
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}
