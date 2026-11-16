package onvif

import (
	"fmt"
	"strconv"
	"strings"
)

// spacesXML advertises only the movement spaces the bridge can honor:
// relative FOV translation, continuous pan/tilt velocity and (when enabled)
// continuous zoom velocity. No absolute position or relative/absolute zoom
// spaces, so Frigate disables autotracking zoom by itself.
func (a *App) spacesXML() string {
	var b strings.Builder
	b.WriteString("<tt:RelativePanTiltTranslationSpace>")
	b.WriteString("<tt:URI>" + fovSpace + "</tt:URI>")
	b.WriteString("<tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange>")
	b.WriteString("<tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange>")
	b.WriteString("</tt:RelativePanTiltTranslationSpace>")

	b.WriteString("<tt:ContinuousPanTiltVelocitySpace>")
	b.WriteString("<tt:URI>" + panTiltVelocitySpace + "</tt:URI>")
	b.WriteString("<tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange>")
	b.WriteString("<tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange>")
	b.WriteString("</tt:ContinuousPanTiltVelocitySpace>")

	if a.cfg.ZoomEnabled {
		b.WriteString("<tt:ContinuousZoomVelocitySpace>")
		b.WriteString("<tt:URI>" + zoomVelocitySpace + "</tt:URI>")
		b.WriteString("<tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange>")
		b.WriteString("</tt:ContinuousZoomVelocitySpace>")
	}
	return b.String()
}

func (a *App) ptzConfigXML(element string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s">`, element, ptzConfigToken)
	b.WriteString("<tt:Name>PTZConfiguration</tt:Name>")
	b.WriteString("<tt:UseCount>1</tt:UseCount>")
	b.WriteString("<tt:NodeToken>" + ptzNodeToken + "</tt:NodeToken>")
	b.WriteString("<tt:DefaultRelativePanTiltTranslationSpace>" + fovSpace +
		"</tt:DefaultRelativePanTiltTranslationSpace>")
	b.WriteString("<tt:DefaultContinuousPanTiltVelocitySpace>" + panTiltVelocitySpace +
		"</tt:DefaultContinuousPanTiltVelocitySpace>")
	if a.cfg.ZoomEnabled {
		b.WriteString("<tt:DefaultContinuousZoomVelocitySpace>" + zoomVelocitySpace +
			"</tt:DefaultContinuousZoomVelocitySpace>")
		b.WriteString(`<tt:DefaultPTZSpeed><tt:PanTilt x="0.5" y="0.5"/><tt:Zoom x="0.5"/></tt:DefaultPTZSpeed>`)
	} else {
		b.WriteString(`<tt:DefaultPTZSpeed><tt:PanTilt x="0.5" y="0.5"/></tt:DefaultPTZSpeed>`)
	}
	b.WriteString("<tt:DefaultPTZTimeout>PT5S</tt:DefaultPTZTimeout>")
	fmt.Fprintf(&b, "</%s>", element)
	return b.String()
}

func (a *App) videoSourceConfigXML(element string) string {
	settings := a.videoSettings()
	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s">`, element, videoSourceConfigToken)
	b.WriteString("<tt:Name>VideoSourceConfiguration</tt:Name>")
	b.WriteString("<tt:UseCount>1</tt:UseCount>")
	b.WriteString("<tt:SourceToken>" + videoSourceToken + "</tt:SourceToken>")
	fmt.Fprintf(&b, `<tt:Bounds x="0" y="0" width="%d" height="%d"/>`,
		settings.Width, settings.Height)
	fmt.Fprintf(&b, "</%s>", element)
	return b.String()
}

func (a *App) videoEncoderConfigXML(element string) string {
	settings := a.videoSettings()
	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s">`, element, videoEncoderConfigToken)
	b.WriteString("<tt:Name>MainStream</tt:Name>")
	b.WriteString("<tt:UseCount>1</tt:UseCount>")
	b.WriteString("<tt:Encoding>" + escape(settings.Encoding) + "</tt:Encoding>")
	b.WriteString("<tt:Resolution><tt:Width>" + strconv.Itoa(settings.Width) +
		"</tt:Width><tt:Height>" + strconv.Itoa(settings.Height) + "</tt:Height></tt:Resolution>")
	b.WriteString("<tt:Quality>5</tt:Quality>")
	b.WriteString("<tt:RateControl><tt:FrameRateLimit>" + strconv.Itoa(settings.Fps) +
		"</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval>" +
		"<tt:BitrateLimit>" + strconv.Itoa(settings.Bitrate) + "</tt:BitrateLimit></tt:RateControl>")
	b.WriteString("<tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type>" +
		"<tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address>" +
		"<tt:Port>0</tt:Port><tt:TTL>1</tt:TTL>" +
		"<tt:AutoStart>false</tt:AutoStart></tt:Multicast>")
	b.WriteString("<tt:SessionTimeout>PT60S</tt:SessionTimeout>")
	fmt.Fprintf(&b, "</%s>", element)
	return b.String()
}

func (a *App) profileXML(element string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s" fixed="true">`, element, escape(a.cfg.ProfileToken))
	b.WriteString("<tt:Name>MainStream</tt:Name>")
	b.WriteString(a.videoSourceConfigXML("tt:VideoSourceConfiguration"))
	b.WriteString(a.videoEncoderConfigXML("tt:VideoEncoderConfiguration"))
	b.WriteString(a.ptzConfigXML("tt:PTZConfiguration"))
	fmt.Fprintf(&b, "</%s>", element)
	return b.String()
}

func (a *App) ptzNodeXML(element string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s" FixedHomePosition="false">`, element, ptzNodeToken)
	b.WriteString("<tt:Name>PTZNode</tt:Name>")
	b.WriteString("<tt:SupportedPTZSpaces>" + a.spacesXML() + "</tt:SupportedPTZSpaces>")
	b.WriteString("<tt:MaximumNumberOfPresets>0</tt:MaximumNumberOfPresets>")
	b.WriteString("<tt:HomeSupported>false</tt:HomeSupported>")
	fmt.Fprintf(&b, "</%s>", element)
	return b.String()
}
