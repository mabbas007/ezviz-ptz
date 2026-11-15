"""Minimal ONVIF (SOAP) implementation for the Hikvision SDK PTZ bridge.

Only the subset used by Frigate's autotracking is implemented, plus enough of
the device/media services for a client to discover the PTZ service:

  device service:  GetServices, GetCapabilities, GetDeviceInformation,
                   GetSystemDateAndTime, GetServiceCapabilities
  media service:   GetProfiles, GetProfile, GetVideoSources,
                   GetVideoSourceConfigurations, GetVideoEncoderConfigurations,
                   (GetVideoSource|Encoder)Configuration, GetStreamUri,
                   GetServiceCapabilities
  PTZ service:     GetServiceCapabilities, GetNodes, GetNode, GetConfiguration,
                   GetConfigurations, GetConfigurationOptions, GetStatus,
                   GetPresets (always empty — the camera rejects preset
                   operations), ContinuousMove, RelativeMove, Stop

The camera's PTZ is pan/tilt only for our purposes: we advertise a relative
FOV translation space and continuous velocity spaces, but no absolute spaces,
so Frigate disables autotracking zoom on its own.
"""

from __future__ import annotations

import base64
import binascii
import hashlib
import hmac
import logging
import xml.etree.ElementTree as ET
from datetime import datetime, timezone
from xml.sax.saxutils import escape

from .config import Config
from .ptz import PTZController
from .sdk import SdkError

logger = logging.getLogger(__name__)

ENV = "http://www.w3.org/2003/05/soap-envelope"
TDS = "http://www.onvif.org/ver10/device/wsdl"
TRT = "http://www.onvif.org/ver10/media/wsdl"
TPTZ = "http://www.onvif.org/ver20/ptz/wsdl"
TT = "http://www.onvif.org/ver10/schema"
TER = "http://www.onvif.org/ver10/error"
WSSE = (
    "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-"
    "wssecurity-secext-1.0.xsd"
)
WSU = (
    "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-"
    "wssecurity-utility-1.0.xsd"
)

FOV_SPACE = "http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationSpaceFov"
PAN_TILT_VELOCITY_SPACE = (
    "http://www.onvif.org/ver10/tptz/PanTiltSpaces/VelocityGenericSpace"
)
ZOOM_VELOCITY_SPACE = "http://www.onvif.org/ver10/tptz/ZoomSpaces/VelocityGenericSpace"

PASSWORD_TEXT = "PasswordText"
PASSWORD_DIGEST = "PasswordDigest"

VIDEO_SOURCE_TOKEN = "vs_1"
VIDEO_SOURCE_CONFIG_TOKEN = "vsc_1"
VIDEO_ENCODER_CONFIG_TOKEN = "vec_1"
PTZ_CONFIG_TOKEN = "ptz_config_1"
PTZ_NODE_TOKEN = "ptz_node_1"


class OnvifFault(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code
        self.message = message


# ---------------------------------------------------------------------------
# XML helpers
# ---------------------------------------------------------------------------
def _local(tag: str) -> str:
    return tag.rpartition("}")[2]


def _find(element: ET.Element, name: str) -> ET.Element | None:
    """First descendant (or self) with a matching local name, any namespace."""
    for child in element.iter():
        if _local(child.tag) == name:
            return child
    return None


def _number(element: ET.Element, name: str, attr: str = "x") -> float:
    child = _find(element, name)
    if child is None or child.get(attr) is None:
        return 0.0
    try:
        value = float(child.get(attr, "0"))
    except ValueError:
        return 0.0
    return value


def _xml_escape(value: object) -> str:
    return escape(str(value))


def _envelope(body: str) -> bytes:
    return (
        '<?xml version="1.0" encoding="UTF-8"?>'
        f'<env:Envelope xmlns:env="{ENV}" xmlns:tds="{TDS}" xmlns:trt="{TRT}"'
        f' xmlns:tptz="{TPTZ}" xmlns:tt="{TT}" xmlns:ter="{TER}">'
        f"<env:Body>{body}</env:Body></env:Envelope>"
    ).encode("utf-8")


def fault_envelope(code: str, message: str) -> bytes:
    code_ns, _, code_name = code.partition(":")
    ns = TER if code_ns == "ter" else ENV
    return _envelope(
        "<env:Fault>"
        f"<env:Code><env:Value>{escape(code)}</env:Value>"
        f"<env:Subcode><env:Value>ter:ActionNotSupported</env:Value></env:Subcode>"
        "</env:Code>"
        f"<env:Reason><env:Text xml:lang=\"en\">{escape(message)}</env:Text></env:Reason>"
        "</env:Fault>"
    )  # noqa: E501


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


# ---------------------------------------------------------------------------
# ONVIF application
# ---------------------------------------------------------------------------
class OnvifApp:
    def __init__(self, cfg: Config, ptz: PTZController) -> None:
        self.cfg = cfg
        self.ptz = ptz
        self._dispatch = {
            "device": {
                "GetServices": self._device_get_services,
                "GetCapabilities": self._device_get_capabilities,
                "GetDeviceInformation": self._device_get_information,
                "GetSystemDateAndTime": self._device_get_date_time,
                "GetServiceCapabilities": self._device_get_capabilities_service,
            },
            "media": {
                "GetServiceCapabilities": self._media_capabilities,
                "GetProfiles": self._media_get_profiles,
                "GetProfile": self._media_get_profile,
                "GetVideoSources": self._media_get_video_sources,
                "GetVideoSourceConfigurations": self._media_get_video_source_configs,
                "GetVideoEncoderConfigurations": self._media_get_video_encoder_configs,
                "GetVideoSourceConfiguration": self._media_get_video_source_config,
                "GetVideoEncoderConfiguration": self._media_get_video_encoder_config,
                "GetStreamUri": self._media_get_stream_uri,
            },
            "ptz": {
                "GetServiceCapabilities": self._ptz_capabilities,
                "GetNodes": self._ptz_get_nodes,
                "GetNode": self._ptz_get_node,
                "GetConfigurations": self._ptz_get_configurations,
                "GetConfiguration": self._ptz_get_configuration,
                "GetConfigurationOptions": self._ptz_get_configuration_options,
                "GetStatus": self._ptz_get_status,
                "GetPresets": self._ptz_get_presets,
                "ContinuousMove": self._ptz_continuous_move,
                "RelativeMove": self._ptz_relative_move,
                "AbsoluteMove": self._ptz_absolute_move,
                "Stop": self._ptz_stop,
            },
        }

    # -- entry point --------------------------------------------------------
    def handle(
        self, path: str, body: bytes, host_header: str | None
    ) -> tuple[int, bytes]:
        service = self._service_for_path(path)
        if service is None:
            return 404, b"not found"

        try:
            root = ET.fromstring(body)
        except ET.ParseError as exc:
            raise OnvifFault("env:Sender", f"Malformed SOAP request: {exc}") from exc

        if not self._authorized(root):
            raise OnvifFault("ter:NotAuthorized", "Invalid ONVIF credentials")

        body_el = root.find(f"{{{ENV}}}Body")
        if body_el is None or len(body_el) == 0:
            raise OnvifFault("env:Sender", "Missing SOAP body")

        operation = list(body_el)[0]
        op_name = _local(operation.tag)

        handler = self._dispatch.get(service, {}).get(op_name)
        if handler is None:
            logger.debug("Unsupported %s operation %s", service, op_name)
            raise OnvifFault(
                "ter:ActionNotSupported", f"{op_name} is not implemented"
            )

        base_url = self._base_url(host_header)
        return 200, _envelope(handler(operation, base_url))

    # -- auth ---------------------------------------------------------------
    def _authorized(self, root: ET.Element) -> bool:
        if not self.cfg.onvif_user and not self.cfg.onvif_password:
            return True

        token = root.find(f".//{{{WSSE}}}UsernameToken")
        if token is None:
            logger.warning("ONVIF request without WS-Security UsernameToken")
            return False

        username = token.findtext(f"{{{WSSE}}}Username", default="")
        if username != self.cfg.onvif_user:
            logger.warning("ONVIF username mismatch: %r", username)
            return False

        password_el = token.find(f"{{{WSSE}}}Password")
        if password_el is None or password_el.text is None:
            return False
        password_type = password_el.get("Type", "")
        provided = password_el.text.strip()

        if password_type.endswith(PASSWORD_DIGEST):
            nonce_el = token.find(f"{{{WSSE}}}Nonce")
            created = token.findtext(f"{{{WSU}}}Created")
            if nonce_el is None or nonce_el.text is None or not created:
                logger.warning("ONVIF digest auth without Nonce/Created")
                return False
            try:
                nonce = base64.b64decode(nonce_el.text.strip())
            except (binascii.Error, ValueError):
                return False
            expected = base64.b64encode(
                hashlib.sha1(
                    nonce
                    + created.encode("utf-8")
                    + self.cfg.onvif_password.encode("utf-8")
                ).digest()
            ).decode("ascii")
        elif password_type.endswith(PASSWORD_TEXT) or not password_type:
            expected = self.cfg.onvif_password
        else:
            logger.warning("Unsupported ONVIF password type %r", password_type)
            return False

        if not hmac.compare_digest(provided, expected):
            logger.warning("ONVIF password mismatch")
            return False
        return True

    # -- routing ------------------------------------------------------------
    def _service_for_path(self, path: str) -> str | None:
        path = path.split("?", 1)[0].rstrip("/")
        if path.endswith("device_service"):
            return "device"
        if path.endswith("media_service"):
            return "media"
        if path.endswith("ptz_service"):
            return "ptz"
        return None

    def _base_url(self, host_header: str | None) -> str:
        host = self.cfg.advertise_host or host_header or ""
        if not host:
            host = f"127.0.0.1:{self.cfg.onvif_port}"
        if host.startswith(("http://", "https://")):
            return host.rstrip("/")
        return f"http://{host}"

    # -- shared XML fragments ----------------------------------------------
    def _spaces_xml(self) -> str:
        spaces = [
            "<tt:RelativePanTiltTranslationSpace>"
            f"<tt:URI>{FOV_SPACE}</tt:URI>"
            "<tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange>"
            "<tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange>"
            "</tt:RelativePanTiltTranslationSpace>",
            "<tt:ContinuousPanTiltVelocitySpace>"
            f"<tt:URI>{PAN_TILT_VELOCITY_SPACE}</tt:URI>"
            "<tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange>"
            "<tt:YRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:YRange>"
            "</tt:ContinuousPanTiltVelocitySpace>",
        ]
        if self.cfg.zoom_enabled:
            spaces.append(
                "<tt:ContinuousZoomVelocitySpace>"
                f"<tt:URI>{ZOOM_VELOCITY_SPACE}</tt:URI>"
                "<tt:XRange><tt:Min>-1</tt:Min><tt:Max>1</tt:Max></tt:XRange>"
                "</tt:ContinuousZoomVelocitySpace>"
            )
        return "".join(spaces)

    def _ptz_config_xml(self, element: str = "tt:PTZConfiguration") -> str:
        zoom = ""
        if self.cfg.zoom_enabled:
            zoom = (
                "<tt:DefaultContinuousZoomVelocitySpace>"
                f"{ZOOM_VELOCITY_SPACE}"
                "</tt:DefaultContinuousZoomVelocitySpace>"
                '<tt:DefaultPTZSpeed><tt:PanTilt x="0.5" y="0.5"/>'
                '<tt:Zoom x="0.5"/></tt:DefaultPTZSpeed>'
            )
        else:
            zoom = (
                '<tt:DefaultPTZSpeed><tt:PanTilt x="0.5" y="0.5"/>'
                "</tt:DefaultPTZSpeed>"
            )
        return (
            f'<{element} token="{PTZ_CONFIG_TOKEN}">'
            "<tt:Name>PTZConfiguration</tt:Name>"
            "<tt:UseCount>1</tt:UseCount>"
            f"<tt:NodeToken>{PTZ_NODE_TOKEN}</tt:NodeToken>"
            "<tt:DefaultRelativePanTiltTranslationSpace>"
            f"{FOV_SPACE}"
            "</tt:DefaultRelativePanTiltTranslationSpace>"
            "<tt:DefaultContinuousPanTiltVelocitySpace>"
            f"{PAN_TILT_VELOCITY_SPACE}"
            "</tt:DefaultContinuousPanTiltVelocitySpace>"
            f"{zoom}"
            "<tt:DefaultPTZTimeout>PT5S</tt:DefaultPTZTimeout>"
            f"</{element}>"
        )

    def _video_source_config_xml(self, element: str = "tt:VideoSourceConfiguration") -> str:
        return (
            f'<{element} token="{VIDEO_SOURCE_CONFIG_TOKEN}">'
            "<tt:Name>VideoSourceConfiguration</tt:Name>"
            "<tt:UseCount>1</tt:UseCount>"
            f"<tt:SourceToken>{VIDEO_SOURCE_TOKEN}</tt:SourceToken>"
            f'<tt:Bounds x="0" y="0" width="{self.cfg.video_width}" '
            f'height="{self.cfg.video_height}"/>'
            f"</{element}>"
        )

    def _video_encoder_config_xml(
        self, element: str = "tt:VideoEncoderConfiguration"
    ) -> str:
        return (
            f'<{element} token="{VIDEO_ENCODER_CONFIG_TOKEN}">'
            "<tt:Name>MainStream</tt:Name>"
            "<tt:UseCount>1</tt:UseCount>"
            "<tt:Encoding>H264</tt:Encoding>"
            f"<tt:Resolution><tt:Width>{self.cfg.video_width}</tt:Width>"
            f"<tt:Height>{self.cfg.video_height}</tt:Height></tt:Resolution>"
            "<tt:Quality>5</tt:Quality>"
            f"<tt:RateControl><tt:FrameRateLimit>{self.cfg.video_fps}"
            "</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval>"
            "<tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl>"
            "<tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type>"
            "<tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address>"
            "<tt:Port>0</tt:Port><tt:TTL>1</tt:TTL>"
            "<tt:AutoStart>false</tt:AutoStart></tt:Multicast>"
            "<tt:SessionTimeout>PT60S</tt:SessionTimeout>"
            f"</{element}>"
        )

    def _profile_xml(self, element: str = "trt:Profiles") -> str:
        return (
            f'<{element} token="{self.cfg.profile_token}" fixed="true">'
            "<tt:Name>MainStream</tt:Name>"
            f"{self._video_source_config_xml()}"
            f"{self._video_encoder_config_xml()}"
            f"{self._ptz_config_xml()}"
            f"</{element}>"
        )

    # =======================================================================
    # Device service
    # =======================================================================
    def _device_get_services(self, op: ET.Element, base: str) -> str:
        services = [
            (TDS, f"{base}/onvif/device_service", 2, 4),
            (TRT, f"{base}/onvif/media_service", 2, 4),
            (TPTZ, f"{base}/onvif/ptz_service", 2, 4),
        ]
        entries = "".join(
            "<tds:Service>"
            f"<tds:Namespace>{ns}</tds:Namespace>"
            f"<tds:XAddr>{escape(xaddr)}</tds:XAddr>"
            f"<tds:Version><tt:Major>{major}</tt:Major>"
            f"<tt:Minor>{minor}</tt:Minor></tds:Version>"
            "</tds:Service>"
            for ns, xaddr, major, minor in services
        )
        return f"<tds:GetServicesResponse>{entries}</tds:GetServicesResponse>"

    def _device_get_capabilities(self, op: ET.Element, base: str) -> str:
        return (
            "<tds:GetCapabilitiesResponse><tds:Capabilities>"
            f"<tt:Media><tt:XAddr>{base}/onvif/media_service</tt:XAddr>"
            "<tt:StreamingCapabilities>"
            "<tt:RTPMulticast>false</tt:RTPMulticast>"
            "<tt:RTP_TCP>true</tt:RTP_TCP>"
            "<tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP>"
            "</tt:StreamingCapabilities></tt:Media>"
            f"<tt:PTZ><tt:XAddr>{base}/onvif/ptz_service</tt:XAddr></tt:PTZ>"
            "</tds:Capabilities></tds:GetCapabilitiesResponse>"
        )

    def _device_get_information(self, op: ET.Element, base: str) -> str:
        return (
            "<tds:GetDeviceInformationResponse>"
            "<tds:Manufacturer>EZVIZ</tds:Manufacturer>"
            "<tds:Model>Hikvision SDK ONVIF PTZ bridge</tds:Model>"
            "<tds:FirmwareVersion>1.0.0</tds:FirmwareVersion>"
            f"<tds:SerialNumber>{_xml_escape(self.cfg.camera_host)}</tds:SerialNumber>"
            "<tds:HardwareId>ptz-bridge</tds:HardwareId>"
            "</tds:GetDeviceInformationResponse>"
        )

    def _device_get_date_time(self, op: ET.Element, base: str) -> str:
        now = datetime.now(timezone.utc)
        return (
            "<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime>"
            "<tt:DateTimeType>Manual</tt:DateTimeType>"
            "<tt:DaylightSavings>false</tt:DaylightSavings>"
            "<tt:UTCDateTime>"
            f"<tt:Time><tt:Hour>{now.hour}</tt:Hour><tt:Minute>{now.minute}</tt:Minute>"
            f"<tt:Second>{now.second}</tt:Second></tt:Time>"
            f"<tt:Date><tt:Year>{now.year}</tt:Year><tt:Month>{now.month}</tt:Month>"
            f"<tt:Day>{now.day}</tt:Day></tt:Date>"
            "</tt:UTCDateTime>"
            "</tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>"
        )

    def _device_get_capabilities_service(self, op: ET.Element, base: str) -> str:
        return (
            "<tds:GetServiceCapabilitiesResponse><tds:Capabilities>"
            '<tds:Network IPFilter="false" ZeroConfiguration="false"'
            ' IPVersion6="false" DynDNS="false"/>'
            '<tds:Security TLS1.0="false" TLS1.1="false" TLS1.2="false"'
            ' OnboardKeyGeneration="false" AccessPolicyConfig="false"'
            ' DefaultAccessPolicy="false" Dot1X="false" RemoteUserHandling="false"'
            ' X.509Token="false" SAMLToken="false" KerberosToken="false"'
            ' UsernameToken="true" HttpDigest="true" RELToken="false"/>'
            '<tds:System DiscoveryResolve="false" DiscoveryBye="false"'
            ' RemoteDiscovery="false" SystemBackup="false" SystemLogging="false"'
            ' FirmwareUpgrade="false" HttpFirmwareUpgrade="false"'
            ' HttpSystemBackup="false" HttpSystemLogging="false"'
            ' HttpSupportInformation="false"/>'
            "</tds:Capabilities></tds:GetServiceCapabilitiesResponse>"
        )

    # =======================================================================
    # Media service
    # =======================================================================
    def _media_capabilities(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetServiceCapabilitiesResponse>"
            '<trt:Capabilities SnapshotUri="false" Rotation="false"'
            ' VideoSourceMode="false" OSD="false" TemporaryOSDText="false"'
            ' EXICompression="false"/>'
            "</trt:GetServiceCapabilitiesResponse>"
        )

    def _media_get_profiles(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetProfilesResponse>"
            f"{self._profile_xml()}"
            "</trt:GetProfilesResponse>"
        )

    def _media_get_profile(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetProfileResponse>"
            f"{self._profile_xml('trt:Profile')}"
            "</trt:GetProfileResponse>"
        )

    def _media_get_video_sources(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetVideoSourcesResponse>"
            f'<trt:VideoSources token="{VIDEO_SOURCE_TOKEN}">'
            f"<tt:Framerate>{self.cfg.video_fps}</tt:Framerate>"
            f"<tt:Resolution><tt:Width>{self.cfg.video_width}</tt:Width>"
            f"<tt:Height>{self.cfg.video_height}</tt:Height></tt:Resolution>"
            "</trt:VideoSources></trt:GetVideoSourcesResponse>"
        )

    def _media_get_video_source_configs(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetVideoSourceConfigurationsResponse>"
            f"{self._video_source_config_xml('trt:Configurations')}"
            "</trt:GetVideoSourceConfigurationsResponse>"
        )

    def _media_get_video_encoder_configs(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetVideoEncoderConfigurationsResponse>"
            f"{self._video_encoder_config_xml('trt:Configurations')}"
            "</trt:GetVideoEncoderConfigurationsResponse>"
        )

    def _media_get_video_source_config(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetVideoSourceConfigurationResponse>"
            f"{self._video_source_config_xml('trt:Configuration')}"
            "</trt:GetVideoSourceConfigurationResponse>"
        )

    def _media_get_video_encoder_config(self, op: ET.Element, base: str) -> str:
        return (
            "<trt:GetVideoEncoderConfigurationResponse>"
            f"{self._video_encoder_config_xml('trt:Configuration')}"
            "</trt:GetVideoEncoderConfigurationResponse>"
        )

    def _media_get_stream_uri(self, op: ET.Element, base: str) -> str:
        if not self.cfg.rtsp_url:
            raise OnvifFault(
                "ter:ActionNotSupported",
                "RTSP_URL is not configured on this bridge",
            )
        return (
            "<trt:GetStreamUriResponse><trt:MediaUri>"
            f"<tt:Uri>{_xml_escape(self.cfg.rtsp_url)}</tt:Uri>"
            "<tt:InvalidAfterConnect>false</tt:InvalidAfterConnect>"
            "<tt:InvalidAfterReboot>false</tt:InvalidAfterReboot>"
            "<tt:Timeout>PT0S</tt:Timeout>"
            "</trt:MediaUri></trt:GetStreamUriResponse>"
        )

    # =======================================================================
    # PTZ service
    # =======================================================================
    def _ptz_capabilities(self, op: ET.Element, base: str) -> str:
        return (
            "<tptz:GetServiceCapabilitiesResponse>"
            '<tptz:Capabilities EFlip="false" Reverse="false"'
            ' GetCompatibleConfigurations="false" MoveStatus="true">'
            "</tptz:Capabilities></tptz:GetServiceCapabilitiesResponse>"
        )

    def _ptz_node_xml(self, element: str = "tptz:PTZNode") -> str:
        return (
            f'<{element} token="{PTZ_NODE_TOKEN}" FixedHomePosition="false">'
            "<tt:Name>PTZNode</tt:Name>"
            f"<tt:SupportedPTZSpaces>{self._spaces_xml()}</tt:SupportedPTZSpaces>"
            "<tt:MaximumNumberOfPresets>0</tt:MaximumNumberOfPresets>"
            "<tt:HomeSupported>false</tt:HomeSupported>"
            f"</{element}>"
        )

    def _ptz_get_nodes(self, op: ET.Element, base: str) -> str:
        return (
            "<tptz:GetNodesResponse>"
            f"{self._ptz_node_xml()}"
            "</tptz:GetNodesResponse>"
        )

    def _ptz_get_node(self, op: ET.Element, base: str) -> str:
        return (
            "<tptz:GetNodeResponse>"
            f"{self._ptz_node_xml()}"
            "</tptz:GetNodeResponse>"
        )

    def _ptz_get_configurations(self, op: ET.Element, base: str) -> str:
        return (
            "<tptz:GetConfigurationsResponse>"
            f"{self._ptz_config_xml()}"
            "</tptz:GetConfigurationsResponse>"
        )

    def _ptz_get_configuration(self, op: ET.Element, base: str) -> str:
        return (
            "<tptz:GetConfigurationResponse>"
            f"{self._ptz_config_xml()}"
            "</tptz:GetConfigurationResponse>"
        )

    def _ptz_get_configuration_options(self, op: ET.Element, base: str) -> str:
        timeout = (
            "<tt:PTZTimeout><tt:Min>PT1S</tt:Min>"
            "<tt:Max>PT60S</tt:Max></tt:PTZTimeout>"
        )
        return (
            "<tptz:GetConfigurationOptionsResponse>"
            "<tptz:PTZConfigurationOptions>"
            f"<tt:Spaces>{self._spaces_xml()}</tt:Spaces>"
            f"{timeout}"
            "</tptz:PTZConfigurationOptions>"
            "</tptz:GetConfigurationOptionsResponse>"
        )

    def _ptz_get_status(self, op: ET.Element, base: str) -> str:
        pan_tilt = "MOVING" if self.ptz.pan_tilt_moving else "IDLE"
        zoom = "MOVING" if self.ptz.zoom_moving else "IDLE"
        return (
            "<tptz:GetStatusResponse><tptz:PTZStatus>"
            "<tt:Position>"
            f'<tt:PanTilt x="0" y="0" space="{FOV_SPACE}"/>'
            "</tt:Position>"
            "<tt:MoveStatus>"
            f"<tt:PanTilt>{pan_tilt}</tt:PanTilt>"
            f"<tt:Zoom>{zoom}</tt:Zoom>"
            "</tt:MoveStatus>"
            f"<tt:UtcTime>{utc_now()}</tt:UtcTime>"
            "</tptz:PTZStatus></tptz:GetStatusResponse>"
        )

    def _ptz_get_presets(self, op: ET.Element, base: str) -> str:
        # The camera rejects PTZ preset operations over the SDK
        # (NET_DVR_NETWORK_ERRORDATA), so the list is always empty. ONVIF
        # still requires the operation to exist and answer.
        return "<tptz:GetPresetsResponse/>"

    def _ptz_continuous_move(self, op: ET.Element, base: str) -> str:
        velocity = _find(op, "Velocity")
        if velocity is None:
            raise OnvifFault("ter:InvalidArgVal", "Velocity is required")
        pan = _number(velocity, "PanTilt", "x")
        tilt = _number(velocity, "PanTilt", "y")
        zoom = _number(velocity, "Zoom", "x")
        speed = max(abs(pan), abs(tilt), abs(zoom))
        try:
            self.ptz.continuous_move(pan, tilt, zoom, speed)
        except SdkError as exc:
            raise OnvifFault("ter:Action", str(exc)) from exc
        return "<tptz:ContinuousMoveResponse/>"

    def _ptz_relative_move(self, op: ET.Element, base: str) -> str:
        translation = _find(op, "Translation")
        speed_el = _find(op, "Speed")
        if translation is None:
            raise OnvifFault("ter:InvalidArgVal", "Translation is required")
        pan = _number(translation, "PanTilt", "x")
        tilt = _number(translation, "PanTilt", "y")
        zoom = _number(translation, "Zoom", "x")
        speed = _number(speed_el, "PanTilt", "x") if speed_el is not None else None
        try:
            self.ptz.relative_move(pan, tilt, zoom, speed)
        except SdkError as exc:
            raise OnvifFault("ter:Action", str(exc)) from exc
        return "<tptz:RelativeMoveResponse/>"

    def _ptz_absolute_move(self, op: ET.Element, base: str) -> str:
        raise OnvifFault(
            "ter:ActionNotSupported",
            "Absolute positioning is not supported; use RelativeMove or Stop",
        )

    def _ptz_stop(self, op: ET.Element, base: str) -> str:
        self.ptz.stop()
        return "<tptz:StopResponse/>"
