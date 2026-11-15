package onvif

import (
	"fmt"
	"strings"
	"time"
)

func (a *App) deviceGetServices(_ *node, base string) (string, *Fault) {
	services := []struct {
		namespace string
		xaddr     string
		major     int
		minor     int
	}{
		{tdsNS, base + "/onvif/device_service", 2, 4},
		{trtNS, base + "/onvif/media_service", 2, 4},
		{tptzNS, base + "/onvif/ptz_service", 2, 4},
	}

	var b strings.Builder
	b.WriteString("<tds:GetServicesResponse>")
	for _, service := range services {
		b.WriteString("<tds:Service>")
		b.WriteString("<tds:Namespace>" + escape(service.namespace) + "</tds:Namespace>")
		b.WriteString("<tds:XAddr>" + escape(service.xaddr) + "</tds:XAddr>")
		fmt.Fprintf(&b, "<tds:Version><tt:Major>%d</tt:Major><tt:Minor>%d</tt:Minor></tds:Version>",
			service.major, service.minor)
		b.WriteString("</tds:Service>")
	}
	b.WriteString("</tds:GetServicesResponse>")
	return b.String(), nil
}

func (a *App) deviceGetCapabilities(_ *node, base string) (string, *Fault) {
	var b strings.Builder
	b.WriteString("<tds:GetCapabilitiesResponse><tds:Capabilities>")
	b.WriteString("<tt:Media><tt:XAddr>" + escape(base+"/onvif/media_service") + "</tt:XAddr>")
	b.WriteString("<tt:StreamingCapabilities>")
	b.WriteString("<tt:RTPMulticast>false</tt:RTPMulticast>")
	b.WriteString("<tt:RTP_TCP>true</tt:RTP_TCP>")
	b.WriteString("<tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP>")
	b.WriteString("</tt:StreamingCapabilities></tt:Media>")
	b.WriteString("<tt:PTZ><tt:XAddr>" + escape(base+"/onvif/ptz_service") + "</tt:XAddr></tt:PTZ>")
	b.WriteString("</tds:Capabilities></tds:GetCapabilitiesResponse>")
	return b.String(), nil
}

func (a *App) deviceGetInformation(_ *node, _ string) (string, *Fault) {
	var b strings.Builder
	b.WriteString("<tds:GetDeviceInformationResponse>")
	b.WriteString("<tds:Manufacturer>EZVIZ</tds:Manufacturer>")
	b.WriteString("<tds:Model>Hikvision SDK ONVIF PTZ bridge</tds:Model>")
	b.WriteString("<tds:FirmwareVersion>1.0.0</tds:FirmwareVersion>")
	b.WriteString("<tds:SerialNumber>" + escape(a.cfg.CameraHost) + "</tds:SerialNumber>")
	b.WriteString("<tds:HardwareId>ptz-bridge</tds:HardwareId>")
	b.WriteString("</tds:GetDeviceInformationResponse>")
	return b.String(), nil
}

func (a *App) deviceGetDateAndTime(_ *node, _ string) (string, *Fault) {
	now := time.Now().UTC()

	var b strings.Builder
	b.WriteString("<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime>")
	b.WriteString("<tt:DateTimeType>Manual</tt:DateTimeType>")
	b.WriteString("<tt:DaylightSavings>false</tt:DaylightSavings>")
	b.WriteString("<tt:UTCDateTime>")
	fmt.Fprintf(&b, "<tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time>",
		now.Hour(), now.Minute(), now.Second())
	fmt.Fprintf(&b, "<tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date>",
		now.Year(), int(now.Month()), now.Day())
	b.WriteString("</tt:UTCDateTime>")
	b.WriteString("</tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>")
	return b.String(), nil
}

func (a *App) deviceGetServiceCapabilities(_ *node, _ string) (string, *Fault) {
	// HttpDigest is deliberately false: only WS-Security UsernameToken is
	// implemented, so clients must not be told HTTP Digest will work.
	return "<tds:GetServiceCapabilitiesResponse><tds:Capabilities>" +
		`<tds:Network IPFilter="false" ZeroConfiguration="false" IPVersion6="false" DynDNS="false"/>` +
		`<tds:Security TLS1.0="false" TLS1.1="false" TLS1.2="false"` +
		` OnboardKeyGeneration="false" AccessPolicyConfig="false"` +
		` DefaultAccessPolicy="false" Dot1X="false" RemoteUserHandling="false"` +
		` X.509Token="false" SAMLToken="false" KerberosToken="false"` +
		` UsernameToken="true" HttpDigest="false" RELToken="false"/>` +
		`<tds:System DiscoveryResolve="false" DiscoveryBye="false"` +
		` RemoteDiscovery="false" SystemBackup="false" SystemLogging="false"` +
		` FirmwareUpgrade="false" HttpFirmwareUpgrade="false"` +
		` HttpSystemBackup="false" HttpSystemLogging="false"` +
		` HttpSupportInformation="false"/>` +
		"</tds:Capabilities></tds:GetServiceCapabilitiesResponse>", nil
}
