package onvif

// SOAP and ONVIF namespaces.
const (
	envNS  = "http://www.w3.org/2003/05/soap-envelope"
	tdsNS  = "http://www.onvif.org/ver10/device/wsdl"
	trtNS  = "http://www.onvif.org/ver10/media/wsdl"
	tptzNS = "http://www.onvif.org/ver20/ptz/wsdl"
	ttNS   = "http://www.onvif.org/ver10/schema"
	terNS  = "http://www.onvif.org/ver10/error"
)

// PTZ spaces. Only relative FOV translation and generic velocities are
// advertised: the camera has no absolute position feedback and no usable zoom
// position spaces.
const (
	fovSpace             = "http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationSpaceFov"
	panTiltVelocitySpace = "http://www.onvif.org/ver10/tptz/PanTiltSpaces/VelocityGenericSpace"
	zoomVelocitySpace    = "http://www.onvif.org/ver10/tptz/ZoomSpaces/VelocityGenericSpace"
)

// WS-Security password types.
const (
	passwordText   = "PasswordText"
	passwordDigest = "PasswordDigest"
)

// Fixed tokens advertised to clients.
const (
	videoSourceToken        = "vs_1"
	videoSourceConfigToken  = "vsc_1"
	videoEncoderConfigToken = "vec_1"
	ptzConfigToken          = "ptz_config_1"
	ptzNodeToken            = "ptz_node_1"
)
