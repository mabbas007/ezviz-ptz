package onvif

import "frigate-onvif-go/internal/sdk"

// VideoInfoProvider reports the encoder metadata the bridge learned from the
// camera. *sdk.Daemon implements it; nil is allowed (the bridge then uses the
// VIDEO_* configuration values only).
type VideoInfoProvider interface {
	VideoInfo() (sdk.VideoInfo, bool)
}

// videoSettings is the resolved metadata used to build the ONVIF media
// profile. SDK values win when the camera reported them, otherwise the
// configured defaults are used.
type videoSettings struct {
	Encoding string
	Width    int
	Height   int
	Fps      int
	Bitrate  int
}

func (a *App) videoSettings() videoSettings {
	settings := videoSettings{
		Encoding: a.cfg.VideoEncoding,
		Width:    a.cfg.VideoWidth,
		Height:   a.cfg.VideoHeight,
		Fps:      a.cfg.VideoFps,
		Bitrate:  a.cfg.VideoBitrate,
	}
	if settings.Encoding == "" {
		settings.Encoding = "H264"
	}
	if settings.Bitrate <= 0 {
		settings.Bitrate = 4096
	}

	if a.video == nil {
		return settings
	}
	info, ok := a.video.VideoInfo()
	if !ok {
		return settings
	}
	if info.Encoding != "" {
		settings.Encoding = info.Encoding
	}
	if info.Width > 0 {
		settings.Width = info.Width
	}
	if info.Height > 0 {
		settings.Height = info.Height
	}
	if info.Fps > 0 {
		settings.Fps = info.Fps
	}
	if info.Bitrate > 0 {
		settings.Bitrate = info.Bitrate
	}
	return settings
}

// pathForEncoding is the default Hikvision/EZVIZ RTSP path when RTSP_PATH is
// not configured. Only the two encodings these cameras stream on have a
// stable vendor path; anything else must set RTSP_PATH explicitly.
func pathForEncoding(encoding string) string {
	switch encoding {
	case "H264":
		return "/H.264"
	case "H265":
		return "/H.265"
	default:
		return ""
	}
}
