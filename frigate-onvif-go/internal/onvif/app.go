// Package onvif implements the subset of ONVIF used by Frigate's
// autotracking, plus enough device/media discovery for a client to find the
// PTZ service.
//
// It is a port of frigate-onvif/app/onvif.py. Responses are built by hand
// (like the Python bridge) because encoding/xml cannot express the optional
// attributes and empty elements ONVIF clients expect.
package onvif

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"

	"frigate-onvif-go/internal/config"
)

// Controller is the PTZ state machine the ONVIF handlers drive.
type Controller interface {
	ContinuousMove(pan, tilt, zoom float64, speed *float64)
	RelativeMove(pan, tilt, zoom float64, speed *float64)
	Stop()
	PanTiltMoving() bool
	ZoomMoving() bool
	Moving() bool
}

type handlerFunc func(op *node, baseURL string) (string, *Fault)

// App dispatches ONVIF SOAP requests.
type App struct {
	cfg      *config.Config
	ptz      Controller
	video    VideoInfoProvider
	dispatch map[string]map[string]handlerFunc
}

// NewApp returns an ONVIF application for cfg, driving ptz and reading camera
// metadata from video (which may be nil).
func NewApp(cfg *config.Config, ptz Controller, video VideoInfoProvider) *App {
	app := &App{cfg: cfg, ptz: ptz, video: video}
	app.dispatch = map[string]map[string]handlerFunc{
		"device": {
			"GetServices":            app.deviceGetServices,
			"GetCapabilities":        app.deviceGetCapabilities,
			"GetDeviceInformation":   app.deviceGetInformation,
			"GetSystemDateAndTime":   app.deviceGetDateAndTime,
			"GetServiceCapabilities": app.deviceGetServiceCapabilities,
		},
		"media": {
			"GetServiceCapabilities":        app.mediaGetServiceCapabilities,
			"GetProfiles":                   app.mediaGetProfiles,
			"GetProfile":                    app.mediaGetProfile,
			"GetVideoSources":               app.mediaGetVideoSources,
			"GetVideoSourceConfigurations":  app.mediaGetVideoSourceConfigurations,
			"GetVideoEncoderConfigurations": app.mediaGetVideoEncoderConfigurations,
			"GetVideoSourceConfiguration":   app.mediaGetVideoSourceConfiguration,
			"GetVideoEncoderConfiguration":  app.mediaGetVideoEncoderConfiguration,
			"GetStreamUri":                  app.mediaGetStreamURI,
		},
		"ptz": {
			"GetServiceCapabilities":  app.ptzGetServiceCapabilities,
			"GetNodes":                app.ptzGetNodes,
			"GetNode":                 app.ptzGetNode,
			"GetConfigurations":       app.ptzGetConfigurations,
			"GetConfiguration":        app.ptzGetConfiguration,
			"GetConfigurationOptions": app.ptzGetConfigurationOptions,
			"GetStatus":               app.ptzGetStatus,
			"GetPresets":              app.ptzGetPresets,
			"ContinuousMove":          app.ptzContinuousMove,
			"RelativeMove":            app.ptzRelativeMove,
			"AbsoluteMove":            app.ptzAbsoluteMove,
			"Stop":                    app.ptzStop,
		},
	}
	return app
}

// Moving reports whether any PTZ axis is moving.
func (a *App) Moving() bool {
	return a.ptz.PanTiltMoving() || a.ptz.ZoomMoving()
}

// Handle processes one SOAP request and returns the HTTP status and body.
func (a *App) Handle(path string, body []byte, host string) (status int, payload []byte) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("panic while handling ONVIF request",
				"path", path,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
			status = http.StatusInternalServerError
			payload = faultEnvelope(&Fault{
				Code:    "env:Receiver",
				Message: "Internal error in the PTZ bridge",
			})
		}
	}()

	service := serviceForPath(path)
	if service == "" {
		return http.StatusNotFound, []byte("not found")
	}

	root, err := parseNode(body)
	if err != nil {
		return http.StatusInternalServerError, faultEnvelope(&Fault{
			Code:    "env:Sender",
			Message: "Malformed SOAP request: " + err.Error(),
		})
	}

	if !a.authorized(root) {
		return http.StatusInternalServerError, faultEnvelope(&Fault{
			Code:    "ter:NotAuthorized",
			Message: "Invalid ONVIF credentials",
		})
	}

	bodyElement := root.child("Body")
	if bodyElement == nil || len(bodyElement.Children) == 0 {
		return http.StatusInternalServerError, faultEnvelope(&Fault{
			Code:    "env:Sender",
			Message: "Missing SOAP body",
		})
	}

	op := &bodyElement.Children[0]
	handler := a.dispatch[service][op.XMLName.Local]
	if handler == nil {
		slog.Debug("Unsupported ONVIF operation",
			"service", service,
			"operation", op.XMLName.Local,
		)
		return http.StatusInternalServerError, faultEnvelope(&Fault{
			Code:    "ter:ActionNotSupported",
			Message: op.XMLName.Local + " is not implemented",
		})
	}

	response, fault := handler(op, a.baseURL(host))
	if fault != nil {
		return http.StatusInternalServerError, faultEnvelope(fault)
	}
	return http.StatusOK, envelope(response)
}

func serviceForPath(path string) string {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	path = strings.TrimRight(path, "/")

	switch {
	case strings.HasSuffix(path, "device_service"):
		return "device"
	case strings.HasSuffix(path, "media_service"):
		return "media"
	case strings.HasSuffix(path, "ptz_service"):
		return "ptz"
	default:
		return ""
	}
}

// baseURL is the scheme://host used in XAddrs. It follows the request's Host
// header unless ONVIF_ADVERTISE_HOST overrides it.
func (a *App) baseURL(hostHeader string) string {
	host := a.cfg.AdvertiseHost
	if host == "" {
		host = hostHeader
	}
	if host == "" {
		host = "127.0.0.1:" + strconv.Itoa(a.cfg.OnvifPort)
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}
	return "http://" + host
}
