package onvif

import (
	"math"
)

func (a *App) ptzGetServiceCapabilities(_ *node, _ string) (string, *Fault) {
	return "<tptz:GetServiceCapabilitiesResponse>" +
		`<tptz:Capabilities EFlip="false" Reverse="false"` +
		` GetCompatibleConfigurations="false" MoveStatus="true">` +
		"</tptz:Capabilities></tptz:GetServiceCapabilitiesResponse>", nil
}

func (a *App) ptzGetNodes(_ *node, _ string) (string, *Fault) {
	return "<tptz:GetNodesResponse>" + a.ptzNodeXML("tptz:PTZNode") +
		"</tptz:GetNodesResponse>", nil
}

func (a *App) ptzGetNode(_ *node, _ string) (string, *Fault) {
	return "<tptz:GetNodeResponse>" + a.ptzNodeXML("tptz:PTZNode") +
		"</tptz:GetNodeResponse>", nil
}

func (a *App) ptzGetConfigurations(_ *node, _ string) (string, *Fault) {
	return "<tptz:GetConfigurationsResponse>" + a.ptzConfigXML("tt:PTZConfiguration") +
		"</tptz:GetConfigurationsResponse>", nil
}

func (a *App) ptzGetConfiguration(_ *node, _ string) (string, *Fault) {
	return "<tptz:GetConfigurationResponse>" + a.ptzConfigXML("tt:PTZConfiguration") +
		"</tptz:GetConfigurationResponse>", nil
}

func (a *App) ptzGetConfigurationOptions(_ *node, _ string) (string, *Fault) {
	return "<tptz:GetConfigurationOptionsResponse><tptz:PTZConfigurationOptions>" +
		"<tt:Spaces>" + a.spacesXML() + "</tt:Spaces>" +
		"<tt:PTZTimeout><tt:Min>PT1S</tt:Min><tt:Max>PT60S</tt:Max></tt:PTZTimeout>" +
		"</tptz:PTZConfigurationOptions></tptz:GetConfigurationOptionsResponse>", nil
}

func (a *App) ptzGetStatus(_ *node, _ string) (string, *Fault) {
	panTilt := "IDLE"
	if a.ptz.PanTiltMoving() {
		panTilt = "MOVING"
	}
	zoom := "IDLE"
	if a.ptz.ZoomMoving() {
		zoom = "MOVING"
	}

	// No absolute position space is advertised (the camera has no reliable
	// position feedback over the SDK), so report the origin without claiming a
	// translation space for it.
	return "<tptz:GetStatusResponse><tptz:PTZStatus>" +
		`<tt:Position><tt:PanTilt x="0" y="0"/></tt:Position>` +
		"<tt:MoveStatus>" +
		"<tt:PanTilt>" + panTilt + "</tt:PanTilt>" +
		"<tt:Zoom>" + zoom + "</tt:Zoom>" +
		"</tt:MoveStatus>" +
		"<tt:UtcTime>" + utcNow() + "</tt:UtcTime>" +
		"</tptz:PTZStatus></tptz:GetStatusResponse>", nil
}

func (a *App) ptzGetPresets(_ *node, _ string) (string, *Fault) {
	// The camera rejects PTZ preset operations over the SDK
	// (NET_DVR_NETWORK_ERRORDATA), so the list is always empty. ONVIF still
	// requires the operation to exist and answer.
	return "<tptz:GetPresetsResponse/>", nil
}

func (a *App) ptzContinuousMove(op *node, _ string) (string, *Fault) {
	velocity := op.find("Velocity")
	if velocity == nil {
		return "", &Fault{Code: "ter:InvalidArgVal", Message: "Velocity is required"}
	}

	pan := number(velocity, "PanTilt", "x")
	tilt := number(velocity, "PanTilt", "y")
	zoom := number(velocity, "Zoom", "x")
	speed := math.Max(math.Abs(pan), math.Max(math.Abs(tilt), math.Abs(zoom)))

	a.ptz.ContinuousMove(pan, tilt, zoom, &speed)
	return "<tptz:ContinuousMoveResponse/>", nil
}

func (a *App) ptzRelativeMove(op *node, _ string) (string, *Fault) {
	translation := op.find("Translation")
	if translation == nil {
		return "", &Fault{Code: "ter:InvalidArgVal", Message: "Translation is required"}
	}

	pan := number(translation, "PanTilt", "x")
	tilt := number(translation, "PanTilt", "y")
	zoom := number(translation, "Zoom", "x")

	var speed *float64
	if speedElement := op.find("Speed"); speedElement != nil {
		value := number(speedElement, "PanTilt", "x")
		speed = &value
	}

	a.ptz.RelativeMove(pan, tilt, zoom, speed)
	return "<tptz:RelativeMoveResponse/>", nil
}

func (a *App) ptzAbsoluteMove(_ *node, _ string) (string, *Fault) {
	return "", &Fault{
		Code:    "ter:ActionNotSupported",
		Message: "Absolute positioning is not supported; use RelativeMove or Stop",
	}
}

func (a *App) ptzStop(_ *node, _ string) (string, *Fault) {
	a.ptz.Stop()
	return "<tptz:StopResponse/>", nil
}
