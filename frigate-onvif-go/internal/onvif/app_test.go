package onvif

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"strings"
	"sync"
	"testing"

	"frigate-onvif-go/internal/config"
	"frigate-onvif-go/internal/sdk"
)

const (
	testWSSE = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	testWSU  = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"
)

type moveArgs struct {
	pan, tilt, zoom float64
	speed           *float64
}

type fakeController struct {
	mu            sync.Mutex
	continuous    []moveArgs
	relative      []moveArgs
	stops         int
	panTiltMoving bool
	zoomMoving    bool
	panicOnMove   bool
}

func cloneSpeed(speed *float64) *float64 {
	if speed == nil {
		return nil
	}
	value := *speed
	return &value
}

func (f *fakeController) ContinuousMove(pan, tilt, zoom float64, speed *float64) {
	if f.panicOnMove {
		panic("boom")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.continuous = append(f.continuous, moveArgs{pan, tilt, zoom, cloneSpeed(speed)})
}

func (f *fakeController) RelativeMove(pan, tilt, zoom float64, speed *float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relative = append(f.relative, moveArgs{pan, tilt, zoom, cloneSpeed(speed)})
}

func (f *fakeController) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

func (f *fakeController) PanTiltMoving() bool { return f.panTiltMoving }
func (f *fakeController) ZoomMoving() bool    { return f.zoomMoving }
func (f *fakeController) Moving() bool        { return f.panTiltMoving || f.zoomMoving }

func testConfig() *config.Config {
	return &config.Config{
		CameraHost:             "192.168.1.10",
		CameraUser:             "admin",
		CameraPassword:         "secret",
		OnvifPort:              8000,
		ProfileToken:           "profile_1",
		VideoWidth:             1920,
		VideoHeight:            1080,
		VideoFps:               25,
		VideoEncoding:          "H264",
		VideoBitrate:           4096,
		RtspPort:               554,
		RtspAdvertiseHost:      "192.168.1.10",
		RtspUser:               "admin",
		RtspPassword:           "secret",
		RtspIncludeCredentials: true,
		ZoomEnabled:            true,
		PanSecondsPerFov:       0.9,
		MinMoveSeconds:         0.10,
		MaxMoveSeconds:         4.0,
	}
}

func newTestApp(t *testing.T, controller Controller) (*App, *config.Config) {
	t.Helper()
	cfg := testConfig()
	return NewApp(cfg, controller, nil), cfg
}

func envelopeXML(header, body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<env:Envelope xmlns:env="` + envNS + `"` +
		` xmlns:tds="` + tdsNS + `"` +
		` xmlns:trt="` + trtNS + `"` +
		` xmlns:tptz="` + tptzNS + `"` +
		` xmlns:tt="` + ttNS + `"` +
		` xmlns:ter="` + terNS + `">` +
		header + `<env:Body>` + body + `</env:Body></env:Envelope>`
}

func authText(user, pass string) string {
	return `<env:Header><wsse:Security xmlns:wsse="` + testWSSE + `" xmlns:wsu="` + testWSU + `">` +
		`<wsse:UsernameToken><wsse:Username>` + user + `</wsse:Username>` +
		`<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">` +
		pass + `</wsse:Password></wsse:UsernameToken></wsse:Security></env:Header>`
}

func authDigest(user, pass string, nonce []byte, created string) string {
	sum := sha1.Sum(append(append(append([]byte{}, nonce...), created...), pass...))
	digest := base64.StdEncoding.EncodeToString(sum[:])
	return `<env:Header><wsse:Security xmlns:wsse="` + testWSSE + `" xmlns:wsu="` + testWSU + `">` +
		`<wsse:UsernameToken><wsse:Username>` + user + `</wsse:Username>` +
		`<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">` +
		digest + `</wsse:Password>` +
		`<wsse:Nonce>` + base64.StdEncoding.EncodeToString(nonce) + `</wsse:Nonce>` +
		`<wsu:Created>` + created + `</wsu:Created>` +
		`</wsse:UsernameToken></wsse:Security></env:Header>`
}

func call(t *testing.T, app *App, path, header, operation string) (int, string) {
	t.Helper()
	status, payload := app.Handle(path, []byte(envelopeXML(header, operation)), "camera.local:8000")
	return status, string(payload)
}

func assertContains(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("response does not contain %q:\n%s", want, body)
	}
}

func assertNotContains(t *testing.T, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Fatalf("response unexpectedly contains %q:\n%s", unwanted, body)
	}
}

func assertWellFormed(t *testing.T, body string) {
	t.Helper()
	var parsed struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("response is not well-formed XML: %v\n%s", err, body)
	}
}

func TestRoutingAndUnknownPaths(t *testing.T) {
	app, _ := newTestApp(t, &fakeController{})

	status, body := call(t, app, "/onvif/device_service/", "", `<tds:GetServices/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, body, "GetServicesResponse")

	status, body = call(t, app, "/onvif/media_service?x=1", "", `<trt:GetProfiles/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, body, "GetProfilesResponse")

	status, body = call(t, app, "/onvif/ptz_service", "", `<tptz:GetNodes/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, body, "GetNodesResponse")

	status, body = call(t, app, "/something/else", "", `<tds:GetServices/>`)
	if status != 404 || body != "not found" {
		t.Fatalf("unknown path = (%d, %q), want (404, not found)", status, body)
	}
}

func TestMalformedAndMissingBody(t *testing.T) {
	app, _ := newTestApp(t, &fakeController{})

	status, payload := app.Handle("/onvif/device_service", []byte("<not-xml"), "")
	if status != 500 || !strings.Contains(string(payload), "Malformed SOAP request") {
		t.Fatalf("malformed body = (%d, %s), want a sender fault", status, payload)
	}

	var body string
	status, body = call(t, app, "/onvif/device_service", "", "")
	if status != 500 || !strings.Contains(body, "Missing SOAP body") {
		t.Fatalf("missing body = (%d, %s), want a sender fault", status, body)
	}

	status, body = call(t, app, "/onvif/device_service", "", `<tds:NoSuchOperation/>`)
	if status != 500 {
		t.Fatalf("unknown operation status = %d, want 500", status)
	}
	assertContains(t, body, "ActionNotSupported")
	assertContains(t, body, "NoSuchOperation is not implemented")
}

func TestAuthentication(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})
	cfg.OnvifUser = "onvif"
	cfg.OnvifPassword = "secret"
	created := "2024-01-02T03:04:05Z"
	nonce := []byte("0123456789abcdef")

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"password text", authText("onvif", "secret"), 200},
		{"password digest", authDigest("onvif", "secret", nonce, created), 200},
		{"wrong password", authText("onvif", "nope"), 500},
		{"wrong digest", authDigest("onvif", "nope", nonce, created), 500},
		{"wrong user", authText("someone", "secret"), 500},
		{"digest with empty Created", authDigest("onvif", "secret", nonce, ""), 500},
		{"missing header", "", 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, app, "/onvif/device_service", tc.header, `<tds:GetServices/>`)
			if status != tc.want {
				t.Fatalf("status = %d, want %d; body:\n%s", status, tc.want, body)
			}
			if tc.want == 200 {
				assertContains(t, body, "GetServicesResponse")
			} else {
				assertContains(t, body, "NotAuthorized")
			}
		})
	}

	// PasswordText without a Type attribute is accepted, as in ONVIF.
	header := `<env:Header><wsse:Security xmlns:wsse="` + testWSSE + `">` +
		`<wsse:UsernameToken><wsse:Username>onvif</wsse:Username>` +
		`<wsse:Password>secret</wsse:Password></wsse:UsernameToken></wsse:Security></env:Header>`
	if status, body := call(t, app, "/onvif/device_service", header, `<tds:GetServices/>`); status != 200 {
		t.Fatalf("password without Type = (%d, %s), want 200", status, body)
	}
}

func TestAuthDisabledWithoutCredentials(t *testing.T) {
	app, _ := newTestApp(t, &fakeController{})
	status, _ := call(t, app, "/onvif/device_service", "", `<tds:GetServices/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200 when no ONVIF credentials are configured", status)
	}
}

func TestFaultShapes(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})
	cfg.OnvifUser = "onvif"
	cfg.OnvifPassword = "secret"

	status, body := call(t, app, "/onvif/device_service", authText("onvif", "wrong"), `<tds:GetServices/>`)
	if status != 500 {
		t.Fatalf("status = %d, want 500", status)
	}
	assertContains(t, body, "<env:Value>env:Sender</env:Value>")
	assertContains(t, body, "<env:Subcode><env:Value>ter:NotAuthorized</env:Value></env:Subcode>")
	assertNotContains(t, body, "ter:ActionNotSupported")

	app2, _ := newTestApp(t, &fakeController{})
	status, body = call(t, app2, "/onvif/ptz_service", "", `<tptz:AbsoluteMove/>`)
	if status != 500 {
		t.Fatalf("AbsoluteMove status = %d, want 500", status)
	}
	assertContains(t, body, "<env:Value>env:Receiver</env:Value>")
	assertContains(t, body, "<env:Subcode><env:Value>ter:ActionNotSupported</env:Value></env:Subcode>")
	assertContains(t, body, "Absolute positioning is not supported")
}

func TestGetServicesBaseURL(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})

	status, body := call(t, app, "/onvif/device_service", "", `<tds:GetServices/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, body, "<tds:XAddr>http://camera.local:8000/onvif/device_service</tds:XAddr>")
	assertContains(t, body, "<tds:XAddr>http://camera.local:8000/onvif/media_service</tds:XAddr>")
	assertContains(t, body, "<tds:XAddr>http://camera.local:8000/onvif/ptz_service</tds:XAddr>")

	cfg.AdvertiseHost = "https://proxy.example/base/"
	_, body = call(t, app, "/onvif/device_service", "", `<tds:GetServices/>`)
	assertContains(t, body, "https://proxy.example/base/onvif/ptz_service")

	cfg.AdvertiseHost = ""
	status, payload := app.Handle("/onvif/device_service",
		[]byte(envelopeXML("", `<tds:GetServices/>`)), "")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, string(payload), "http://127.0.0.1:8000/onvif/device_service")
}

func TestDeviceOperations(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})
	cfg.CameraHost = "cam&1"

	_, body := call(t, app, "/onvif/device_service", "", `<tds:GetCapabilities/>`)
	assertContains(t, body, "/onvif/media_service")
	assertContains(t, body, "/onvif/ptz_service")
	assertContains(t, body, "GetCapabilitiesResponse")

	_, body = call(t, app, "/onvif/device_service", "", `<tds:GetDeviceInformation/>`)
	assertContains(t, body, "EZVIZ")
	assertContains(t, body, "<tds:SerialNumber>cam&amp;1</tds:SerialNumber>")

	_, body = call(t, app, "/onvif/device_service", "", `<tds:GetSystemDateAndTime/>`)
	assertContains(t, body, "UTCDateTime")
	assertContains(t, body, "DateTimeType")

	_, body = call(t, app, "/onvif/device_service", "", `<tds:GetServiceCapabilities/>`)
	assertContains(t, body, `UsernameToken="true"`)
	assertContains(t, body, `HttpDigest="false"`)
}

func TestProfilesAndMedia(t *testing.T) {
	app, _ := newTestApp(t, &fakeController{})

	status, body := call(t, app, "/onvif/media_service", "", `<trt:GetProfiles/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, body, `<trt:Profiles token="profile_1" fixed="true">`)
	assertContains(t, body, "<tt:VideoSourceConfiguration")
	assertContains(t, body, "<tt:VideoEncoderConfiguration")
	assertContains(t, body, "<tt:PTZConfiguration")
	assertContains(t, body, fovSpace)
	assertContains(t, body, "DefaultPTZSpeed")
	assertNotContains(t, body, "AbsoluteZoomPositionSpace")

	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetProfile/>`)
	assertContains(t, body, `<trt:Profile token="profile_1"`)

	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetVideoSources/>`)
	assertContains(t, body, `<trt:VideoSources token="vs_1">`)
	assertContains(t, body, "<tt:Framerate>25</tt:Framerate>")

	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetVideoSourceConfigurations/>`)
	assertContains(t, body, `<trt:Configurations token="vsc_1">`)

	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetVideoEncoderConfigurations/>`)
	assertContains(t, body, `<trt:Configurations token="vec_1">`)

	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetVideoSourceConfiguration/>`)
	assertContains(t, body, `<trt:Configuration token="vsc_1">`)

	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetVideoEncoderConfiguration/>`)
	assertContains(t, body, `<trt:Configuration token="vec_1">`)

	status, body = call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	if status != 200 {
		t.Fatalf("GetStreamUri = (%d, %s), want 200", status, body)
	}
	assertContains(t, body, "<tt:Uri>rtsp://admin:secret@192.168.1.10:554/H.264</tt:Uri>")

	app.cfg.RtspUrl = "rtsp://cam/live?x=1&y=2"
	status, body = call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, body, "rtsp://cam/live?x=1&amp;y=2")
}

func TestGetStreamURIResolution(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})

	// Custom path, non-default port and a URL-quoted password.
	cfg.RtspPath = "/Streaming/Channels/101?transportmode=unicast"
	cfg.RtspPort = 8554
	cfg.RtspAdvertiseHost = "camera.example"
	cfg.RtspUser = "rtsp user"
	cfg.RtspPassword = "p@ss/word"

	status, body := call(t, app, "/onvif/media_service", "",
		`<trt:GetStreamUri><trt:ProfileToken>profile_1</trt:ProfileToken>`+
			`<trt:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream>`+
			`<tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup>`+
			`</trt:GetStreamUri>`)
	if status != 200 {
		t.Fatalf("GetStreamUri = (%d, %s), want 200", status, body)
	}
	assertContains(t, body,
		"<tt:Uri>rtsp://rtsp%20user:p%40ss%2Fword@camera.example:8554/Streaming/Channels/101?transportmode=unicast</tt:Uri>")
	assertWellFormed(t, body)

	// Without credentials the URI is clean and Frigate injects its ONVIF user.
	cfg.RtspIncludeCredentials = false
	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	assertContains(t, body, "<tt:Uri>rtsp://camera.example:8554/Streaming/Channels/101?transportmode=unicast</tt:Uri>")
	assertNotContains(t, body, "rtsp%20user")

	// RTSP_ADVERTISE_HOST may already contain a port.
	cfg.RtspAdvertiseHost = "camera.example:10554"
	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	assertContains(t, body, "rtsp://camera.example:10554/Streaming/Channels/101")

	// IPv6 literals are bracketed in the URL.
	cfg.RtspAdvertiseHost = "[2001:db8::1]"
	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	assertContains(t, body, "rtsp://[2001:db8::1]:8554/Streaming/Channels/101")
}

func TestGetStreamURIPathFromEncoding(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})
	cfg.RtspIncludeCredentials = false

	_, body := call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	assertContains(t, body, "<tt:Uri>rtsp://192.168.1.10:554/H.264</tt:Uri>")

	cfg.VideoEncoding = "H265"
	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	assertContains(t, body, "<tt:Uri>rtsp://192.168.1.10:554/H.265</tt:Uri>")

	// JPEG has no stable vendor path, so RTSP_PATH is required.
	cfg.VideoEncoding = "JPEG"
	status, body := call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	if status != 500 || !strings.Contains(body, "RTSP_PATH") {
		t.Fatalf("GetStreamUri with JPEG and no RTSP_PATH = (%d, %s), want a fault", status, body)
	}
}

func TestGetStreamURIValidation(t *testing.T) {
	app, _ := newTestApp(t, &fakeController{})

	status, body := call(t, app, "/onvif/media_service", "",
		`<trt:GetStreamUri><trt:ProfileToken>nope</trt:ProfileToken></trt:GetStreamUri>`)
	if status != 500 || !strings.Contains(body, "ter:NoProfile") {
		t.Fatalf("unknown profile token = (%d, %s), want a NoProfile fault", status, body)
	}

	status, body = call(t, app, "/onvif/media_service", "",
		`<trt:GetStreamUri><trt:StreamSetup><tt:Transport>`+
			`<tt:Protocol>HTTP</tt:Protocol></tt:Transport></trt:StreamSetup></trt:GetStreamUri>`)
	if status != 500 || !strings.Contains(body, "not supported") {
		t.Fatalf("HTTP protocol = (%d, %s), want a fault", status, body)
	}
}

func TestVideoInfoOverridesProfile(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})
	app.video = fakeVideoInfoProvider{
		info: sdk.VideoInfo{Encoding: "H265", Width: 3840, Height: 2160, Fps: 30, Bitrate: 8192},
	}

	_, body := call(t, app, "/onvif/media_service", "", `<trt:GetProfiles/>`)
	assertContains(t, body, "<tt:Encoding>H265</tt:Encoding>")
	assertContains(t, body, `<tt:Bounds x="0" y="0" width="3840" height="2160"/>`)
	assertContains(t, body, "<tt:FrameRateLimit>30</tt:FrameRateLimit>")
	assertContains(t, body, "<tt:BitrateLimit>8192</tt:BitrateLimit>")

	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetVideoSources/>`)
	assertContains(t, body, "<tt:Framerate>30</tt:Framerate>")
	assertContains(t, body, "<tt:Width>3840</tt:Width><tt:Height>2160</tt:Height>")

	// The SDK encoding also selects the default vendor RTSP path.
	cfg.RtspIncludeCredentials = false
	_, body = call(t, app, "/onvif/media_service", "", `<trt:GetStreamUri/>`)
	assertContains(t, body, "<tt:Uri>rtsp://192.168.1.10:554/H.265</tt:Uri>")
}

type fakeVideoInfoProvider struct {
	info sdk.VideoInfo
}

func (f fakeVideoInfoProvider) VideoInfo() (sdk.VideoInfo, bool) {
	return f.info, true
}

func TestSpacesAndNodes(t *testing.T) {
	app, cfg := newTestApp(t, &fakeController{})

	_, body := call(t, app, "/onvif/ptz_service", "", `<tptz:GetNodes/>`)
	assertContains(t, body, `<tptz:PTZNode token="ptz_node_1"`)
	assertContains(t, body, fovSpace)
	assertContains(t, body, panTiltVelocitySpace)
	assertContains(t, body, zoomVelocitySpace)
	assertContains(t, body, "<tt:MaximumNumberOfPresets>0</tt:MaximumNumberOfPresets>")

	_, body = call(t, app, "/onvif/ptz_service", "", `<tptz:GetConfigurationOptions/>`)
	assertContains(t, body, "<tt:Min>-1</tt:Min><tt:Max>1</tt:Max>")
	assertContains(t, body, "<tt:PTZTimeout>")

	cfg.ZoomEnabled = false
	_, body = call(t, app, "/onvif/ptz_service", "", `<tptz:GetNodes/>`)
	assertNotContains(t, body, zoomVelocitySpace)
	_, body = call(t, app, "/onvif/ptz_service", "", `<tptz:GetConfigurations/>`)
	assertNotContains(t, body, "<tt:Zoom x=")
}

func TestGetStatus(t *testing.T) {
	fake := &fakeController{panTiltMoving: true}
	app, _ := newTestApp(t, fake)

	status, body := call(t, app, "/onvif/ptz_service", "", `<tptz:GetStatus/>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	assertContains(t, body, "<tt:PanTilt>MOVING</tt:PanTilt>")
	assertContains(t, body, "<tt:Zoom>IDLE</tt:Zoom>")
	assertContains(t, body, `<tt:PanTilt x="0" y="0"/>`)
	assertNotContains(t, body, `space="`)
	assertContains(t, body, "<tt:UtcTime>")
	assertWellFormed(t, body)

	fake.panTiltMoving = false
	fake.zoomMoving = true
	_, body = call(t, app, "/onvif/ptz_service", "", `<tptz:GetStatus/>`)
	assertContains(t, body, "<tt:PanTilt>IDLE</tt:PanTilt>")
	assertContains(t, body, "<tt:Zoom>MOVING</tt:Zoom>")
}

func TestContinuousMove(t *testing.T) {
	fake := &fakeController{}
	app, _ := newTestApp(t, fake)

	status, body := call(t, app, "/onvif/ptz_service", "",
		`<tptz:ContinuousMove><tptz:Velocity><tt:PanTilt x="0.5" y="-0.25"/>`+
			`<tt:Zoom x="0.1"/></tptz:Velocity></tptz:ContinuousMove>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200; body:\n%s", status, body)
	}
	if len(fake.continuous) != 1 {
		t.Fatalf("continuous calls = %d, want 1", len(fake.continuous))
	}
	got := fake.continuous[0]
	if got.pan != 0.5 || got.tilt != -0.25 || got.zoom != 0.1 {
		t.Fatalf("continuous args = %+v, want pan=0.5 tilt=-0.25 zoom=0.1", got)
	}
	if got.speed == nil || *got.speed != 0.5 {
		t.Fatalf("continuous speed = %v, want 0.5", got.speed)
	}

	status, body = call(t, app, "/onvif/ptz_service", "", `<tptz:ContinuousMove/>`)
	if status != 500 || !strings.Contains(body, "Velocity is required") {
		t.Fatalf("missing Velocity = (%d, %s), want a fault", status, body)
	}
}

func TestRelativeMove(t *testing.T) {
	fake := &fakeController{}
	app, _ := newTestApp(t, fake)

	status, body := call(t, app, "/onvif/ptz_service", "",
		`<tptz:RelativeMove><tptz:Translation><tt:PanTilt x="0.4" y="-0.2"/>`+
			`</tptz:Translation><tptz:Speed><tt:PanTilt x="0.5" y="0.5"/>`+
			`</tptz:Speed></tptz:RelativeMove>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200; body:\n%s", status, body)
	}
	if len(fake.relative) != 1 {
		t.Fatalf("relative calls = %d, want 1", len(fake.relative))
	}
	got := fake.relative[0]
	if got.pan != 0.4 || got.tilt != -0.2 || got.zoom != 0 {
		t.Fatalf("relative args = %+v, want pan=0.4 tilt=-0.2 zoom=0", got)
	}
	if got.speed == nil || *got.speed != 0.5 {
		t.Fatalf("relative speed = %v, want 0.5", got.speed)
	}

	// No Speed element means "use the default speed" (nil).
	_, _ = call(t, app, "/onvif/ptz_service", "",
		`<tptz:RelativeMove><tptz:Translation><tt:PanTilt x="0.1"/></tptz:Translation></tptz:RelativeMove>`)
	if len(fake.relative) != 2 || fake.relative[1].speed != nil {
		t.Fatalf("relative without Speed = %+v, want a nil speed", fake.relative[1])
	}

	status, body = call(t, app, "/onvif/ptz_service", "", `<tptz:RelativeMove/>`)
	if status != 500 || !strings.Contains(body, "Translation is required") {
		t.Fatalf("missing Translation = (%d, %s), want a fault", status, body)
	}
}

func TestStopAndPresets(t *testing.T) {
	fake := &fakeController{}
	app, _ := newTestApp(t, fake)

	status, body := call(t, app, "/onvif/ptz_service", "",
		`<tptz:Stop><tptz:ProfileToken>profile_1</tptz:ProfileToken>`+
			`<tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom></tptz:Stop>`)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if fake.stops != 1 {
		t.Fatalf("stops = %d, want 1", fake.stops)
	}
	assertContains(t, body, "StopResponse")

	_, body = call(t, app, "/onvif/ptz_service", "", `<tptz:GetPresets/>`)
	assertContains(t, body, "<tptz:GetPresetsResponse/>")
}

func TestPanicIsContained(t *testing.T) {
	fake := &fakeController{panicOnMove: true}
	app, _ := newTestApp(t, fake)

	status, body := call(t, app, "/onvif/ptz_service", "",
		`<tptz:ContinuousMove><tptz:Velocity><tt:PanTilt x="0.5"/></tptz:Velocity></tptz:ContinuousMove>`)
	if status != 500 {
		t.Fatalf("status = %d, want 500 after a handler panic", status)
	}
	assertContains(t, body, "Internal error in the PTZ bridge")
	assertContains(t, body, "env:Receiver")
}

func TestResponsesAreWellFormed(t *testing.T) {
	app, _ := newTestApp(t, &fakeController{})

	requests := []struct {
		path string
		op   string
	}{
		{"/onvif/device_service", `<tds:GetServices/>`},
		{"/onvif/device_service", `<tds:GetCapabilities/>`},
		{"/onvif/device_service", `<tds:GetDeviceInformation/>`},
		{"/onvif/device_service", `<tds:GetSystemDateAndTime/>`},
		{"/onvif/device_service", `<tds:GetServiceCapabilities/>`},
		{"/onvif/media_service", `<trt:GetProfiles/>`},
		{"/onvif/media_service", `<trt:GetProfile/>`},
		{"/onvif/media_service", `<trt:GetVideoSources/>`},
		{"/onvif/media_service", `<trt:GetVideoSourceConfigurations/>`},
		{"/onvif/media_service", `<trt:GetVideoEncoderConfigurations/>`},
		{"/onvif/media_service", `<trt:GetVideoSourceConfiguration/>`},
		{"/onvif/media_service", `<trt:GetVideoEncoderConfiguration/>`},
		{"/onvif/media_service", `<trt:GetStreamUri/>`},
		{"/onvif/media_service", `<trt:GetServiceCapabilities/>`},
		{"/onvif/ptz_service", `<tptz:GetServiceCapabilities/>`},
		{"/onvif/ptz_service", `<tptz:GetNodes/>`},
		{"/onvif/ptz_service", `<tptz:GetNode/>`},
		{"/onvif/ptz_service", `<tptz:GetConfigurations/>`},
		{"/onvif/ptz_service", `<tptz:GetConfiguration/>`},
		{"/onvif/ptz_service", `<tptz:GetConfigurationOptions/>`},
		{"/onvif/ptz_service", `<tptz:GetStatus/>`},
		{"/onvif/ptz_service", `<tptz:GetPresets/>`},
		{"/onvif/ptz_service", `<tptz:Stop/>`},
	}
	for _, request := range requests {
		status, body := call(t, app, request.path, "", request.op)
		if status != 200 {
			t.Fatalf("%s on %s: status = %d, want 200; body:\n%s",
				request.op, request.path, status, body)
		}
		assertWellFormed(t, body)
	}
}
