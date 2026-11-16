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
	ProfileToken  string
	VideoWidth    int
	VideoHeight   int
	VideoFps      int
	VideoEncoding string
	VideoBitrate  int

	// --- RTSP stream advertised through GetStreamUri -----------------------
	// RtspUrl is a full override; when empty the URL is composed from the
	// fields below. The camera itself only speaks RTSP on 554 with fixed
	// vendor paths (H.264 / H.265 for EZVIZ), so this is configuration, not
	// something the SDK can reliably report.
	RtspUrl                string
	RtspPort               int
	RtspPath               string
	RtspAdvertiseHost      string
	RtspUser               string
	RtspPassword           string
	RtspIncludeCredentials bool

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
	videoBitrate, err := getEnvInt("VIDEO_BITRATE", 4096)
	if err != nil {
		return nil, err
	}
	rtspPort, err := getEnvInt("RTSP_PORT", 554)
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

		ProfileToken:  getEnv("PROFILE_TOKEN", "profile_1"),
		VideoWidth:    videoWidth,
		VideoHeight:   videoHeight,
		VideoFps:      videoFps,
		VideoEncoding: normalizeEncoding(getEnv("VIDEO_ENCODING", "H264")),
		VideoBitrate:  videoBitrate,

		RtspUrl:                getEnv("RTSP_URL", ""),
		RtspPort:               rtspPort,
		RtspPath:               getEnv("RTSP_PATH", ""),
		RtspAdvertiseHost:      getEnv("RTSP_ADVERTISE_HOST", ""),
		RtspUser:               getEnv("RTSP_USER", ""),
		RtspPassword:           getEnv("RTSP_PASSWORD", ""),
		RtspIncludeCredentials: getEnvBool("RTSP_INCLUDE_CREDENTIALS", true),

		LogLevel:       strings.ToUpper(getEnv("LOG_LEVEL", "INFO")),
		StartupTimeout: startupTimeout,
		HealthInterval: healthInterval,
	}

	// The stream normally uses the same account as the SDK. RTSP_USER and
	// RTSP_PASSWORD override it when the camera has a separate RTSP user.
	if cfg.RtspUser == "" {
		cfg.RtspUser = cfg.CameraUser
	}
	if cfg.RtspPassword == "" {
		cfg.RtspPassword = cfg.CameraPassword
	}
	if cfg.RtspAdvertiseHost == "" {
		cfg.RtspAdvertiseHost = cfg.CameraHost
	}

	if cfg.CameraHost == "" {
		return nil, fmt.Errorf("CAMERA_HOST is required (e.g. CAMERA_HOST=192.168.1.10)")
	}
	if cfg.CameraPassword == "" {
		return nil, fmt.Errorf("CAMERA_PASSWORD is required (the camera's verification code)")
	}
	if cfg.RtspPort < 1 || cfg.RtspPort > 65535 {
		return nil, fmt.Errorf("RTSP_PORT must be between 1 and 65535, got %d", cfg.RtspPort)
	}
	if cfg.VideoBitrate <= 0 {
		return nil, fmt.Errorf("VIDEO_BITRATE must be positive, got %d", cfg.VideoBitrate)
	}
	switch cfg.VideoEncoding {
	case "H264", "H265", "JPEG", "MPEG4":
	default:
		return nil, fmt.Errorf("VIDEO_ENCODING must be H264, H265, JPEG or MPEG4, got %q",
			cfg.VideoEncoding)
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

// normalizeEncoding maps the SDK's MJPEG spelling to the ONVIF enumeration.
func normalizeEncoding(value string) string {
	encoding := strings.ToUpper(strings.TrimSpace(value))
	if encoding == "MJPEG" {
		return "JPEG"
	}
	return encoding
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
