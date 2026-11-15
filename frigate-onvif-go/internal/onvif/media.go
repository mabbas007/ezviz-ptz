package onvif

import (
	"fmt"
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
	var b strings.Builder
	b.WriteString("<trt:GetVideoSourcesResponse>")
	fmt.Fprintf(&b, `<trt:VideoSources token="%s">`, videoSourceToken)
	b.WriteString("<tt:Framerate>" + strconv.Itoa(a.cfg.VideoFps) + "</tt:Framerate>")
	b.WriteString("<tt:Resolution><tt:Width>" + strconv.Itoa(a.cfg.VideoWidth) +
		"</tt:Width><tt:Height>" + strconv.Itoa(a.cfg.VideoHeight) + "</tt:Height></tt:Resolution>")
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

func (a *App) mediaGetStreamURI(_ *node, _ string) (string, *Fault) {
	if a.cfg.RtspUrl == "" {
		return "", &Fault{
			Code:    "ter:ActionNotSupported",
			Message: "RTSP_URL is not configured on this bridge",
		}
	}

	var b strings.Builder
	b.WriteString("<trt:GetStreamUriResponse><trt:MediaUri>")
	b.WriteString("<tt:Uri>" + escape(a.cfg.RtspUrl) + "</tt:Uri>")
	b.WriteString("<tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>")
	b.WriteString("<tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>")
	b.WriteString("<tt:Timeout>PT0S</tt:Timeout>")
	b.WriteString("</trt:MediaUri></trt:GetStreamUriResponse>")
	return b.String(), nil
}
