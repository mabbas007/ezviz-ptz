package onvif

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) mediaGetServiceCapabilities(_ *node, _ string) (string, *Fault) {
	return "<trt:GetServiceCapabilitiesResponse>" +
		`<trt:Capabilities SnapshotUri="false" Rotation="false"` +
		` VideoSourceMode="false" OSD="false" TemporaryOSDText="false"` +
		` EXICompression="false"/>` +
		"</trt:GetServiceCapabilitiesResponse>", nil
}

func (a *App) mediaGetProfiles(_ *node, _ string) (string, *Fault) {
	return "<trt:GetProfilesResponse>" + a.profileXML("trt:Profiles") +
		"</trt:GetProfilesResponse>", nil
}

func (a *App) mediaGetProfile(_ *node, _ string) (string, *Fault) {
	return "<trt:GetProfileResponse>" + a.profileXML("trt:Profile") +
		"</trt:GetProfileResponse>", nil
}

func (a *App) mediaGetVideoSources(_ *node, _ string) (string, *Fault) {
	settings := a.videoSettings()
	var b strings.Builder
	b.WriteString("<trt:GetVideoSourcesResponse>")
	fmt.Fprintf(&b, `<trt:VideoSources token="%s">`, videoSourceToken)
	b.WriteString("<tt:Framerate>" + strconv.Itoa(settings.Fps) + "</tt:Framerate>")
	b.WriteString("<tt:Resolution><tt:Width>" + strconv.Itoa(settings.Width) +
		"</tt:Width><tt:Height>" + strconv.Itoa(settings.Height) + "</tt:Height></tt:Resolution>")
	b.WriteString("</trt:VideoSources></trt:GetVideoSourcesResponse>")
	return b.String(), nil
}

func (a *App) mediaGetVideoSourceConfigurations(_ *node, _ string) (string, *Fault) {
	return "<trt:GetVideoSourceConfigurationsResponse>" +
		a.videoSourceConfigXML("trt:Configurations") +
		"</trt:GetVideoSourceConfigurationsResponse>", nil
}

func (a *App) mediaGetVideoEncoderConfigurations(_ *node, _ string) (string, *Fault) {
	return "<trt:GetVideoEncoderConfigurationsResponse>" +
		a.videoEncoderConfigXML("trt:Configurations") +
		"</trt:GetVideoEncoderConfigurationsResponse>", nil
}

func (a *App) mediaGetVideoSourceConfiguration(_ *node, _ string) (string, *Fault) {
	return "<trt:GetVideoSourceConfigurationResponse>" +
		a.videoSourceConfigXML("trt:Configuration") +
		"</trt:GetVideoSourceConfigurationResponse>", nil
}

func (a *App) mediaGetVideoEncoderConfiguration(_ *node, _ string) (string, *Fault) {
	return "<trt:GetVideoEncoderConfigurationResponse>" +
		a.videoEncoderConfigXML("trt:Configuration") +
		"</trt:GetVideoEncoderConfigurationResponse>", nil
}

func (a *App) mediaGetStreamURI(op *node, _ string) (string, *Fault) {
	if fault := a.validateStreamRequest(op); fault != nil {
		return "", fault
	}

	streamURL, fault := a.streamURL()
	if fault != nil {
		return "", fault
	}

	var b strings.Builder
	b.WriteString("<trt:GetStreamUriResponse><trt:MediaUri>")
	b.WriteString("<tt:Uri>" + escape(streamURL) + "</tt:Uri>")
	b.WriteString("<tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>")
	b.WriteString("<tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>")
	b.WriteString("<tt:Timeout>PT0S</tt:Timeout>")
	b.WriteString("</trt:MediaUri></trt:GetStreamUriResponse>")
	return b.String(), nil
}

// validateStreamRequest rejects the two request values the bridge cannot
// honor: another profile token and a non-RTSP transport. A missing profile
// token is accepted as the single configured profile, which is what older
// clients send.
func (a *App) validateStreamRequest(op *node) *Fault {
	if profileToken := op.child("ProfileToken"); profileToken != nil {
		token := strings.TrimSpace(profileToken.Text)
		if token != "" && token != a.cfg.ProfileToken {
			return &Fault{
				Code:    "ter:NoProfile",
				Message: fmt.Sprintf("profile token %q is not available on this bridge", token),
			}
		}
	}

	if protocol := op.find("Protocol"); protocol != nil {
		value := strings.ToUpper(strings.TrimSpace(protocol.Text))
		if value != "" && value != "RTSP" {
			return &Fault{
				Code:    "ter:ActionNotSupported",
				Message: fmt.Sprintf("stream protocol %q is not supported; use RTSP", value),
			}
		}
	}
	return nil
}

// streamURL resolves the RTSP URL advertised to ONVIF clients. RTSP_URL is a
// full override; otherwise the URL is composed from the configured camera
// address, stream path and credentials.
func (a *App) streamURL() (string, *Fault) {
	if a.cfg.RtspUrl != "" {
		return a.cfg.RtspUrl, nil
	}

	path := strings.TrimSpace(a.cfg.RtspPath)
	if path == "" {
		path = pathForEncoding(a.videoSettings().Encoding)
	}
	if path == "" {
		return "", &Fault{
			Code: "ter:ActionNotSupported",
			Message: "the RTSP stream path is unknown for this encoding; " +
				"set RTSP_PATH (for example /H.264 or /Streaming/Channels/101)",
		}
	}

	host := strings.TrimSpace(a.cfg.RtspAdvertiseHost)
	if host == "" {
		host = a.cfg.CameraHost
	}
	host = strings.TrimRight(host, "/")
	host = strings.TrimPrefix(host, "rtsp://")
	host = strings.TrimPrefix(host, "rtsps://")

	port := a.cfg.RtspPort
	if port <= 0 {
		port = 554
	}

	stream := &url.URL{Scheme: "rtsp"}
	// Accept RTSP_ADVERTISE_HOST as either a bare host or host:port.
	if _, _, err := net.SplitHostPort(host); err == nil {
		stream.Host = host
	} else {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		stream.Host = net.JoinHostPort(host, strconv.Itoa(port))
	}

	if a.cfg.RtspIncludeCredentials {
		user := a.cfg.RtspUser
		password := a.cfg.RtspPassword
		if user != "" {
			if password != "" {
				stream.User = url.UserPassword(user, password)
			} else {
				stream.User = url.User(user)
			}
		}
	}

	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		stream.Path = path[:idx]
		stream.RawQuery = path[idx+1:]
	} else {
		stream.Path = path
	}
	if !strings.HasPrefix(stream.Path, "/") {
		stream.Path = "/" + stream.Path
	}

	return stream.String(), nil
}
