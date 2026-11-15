"""Configuration for the EZVIZ/Hikvision -> ONVIF PTZ bridge.

Everything is read from environment variables so the container needs no
config file. See ../README.md for the full list.
"""

from __future__ import annotations

import os
from dataclasses import dataclass


def _str(name: str, default: str = "") -> str:
    value = os.environ.get(name)
    return default if value is None else value.strip()


def _int(name: str, default: int) -> int:
    raw = _str(name)
    if not raw:
        return default
    try:
        return int(raw)
    except ValueError as exc:
        raise SystemExit(f"{name} must be an integer, got {raw!r}") from exc


def _float(name: str, default: float) -> float:
    raw = _str(name)
    if not raw:
        return default
    try:
        return float(raw)
    except ValueError as exc:
        raise SystemExit(f"{name} must be a number, got {raw!r}") from exc


def _bool(name: str, default: bool) -> bool:
    raw = _str(name)
    if not raw:
        return default
    return raw.lower() in ("1", "true", "yes", "on")


@dataclass
class Config:
    # --- camera / Hikvision SDK -------------------------------------------
    camera_host: str = ""
    camera_port: int = 8000
    camera_user: str = "admin"
    camera_password: str = ""
    # Inside the container one camera == one process, so a fixed path is fine.
    ptz_socket: str = "/tmp/ptz_control.sock"
    ptz_binary: str = "/app/ptz_control"

    # --- ONVIF server ------------------------------------------------------
    binding_host: str = "0.0.0.0"
    onvif_port: int = 8000
    onvif_user: str = ""
    onvif_password: str = ""
    # Overrides the host used in the XAddrs sent to clients (default: Host header)
    advertise_host: str = ""

    # --- PTZ behaviour -----------------------------------------------------
    default_speed: int = 4  # Hikvision speed 1..7
    # How long one full field of view of movement takes. Tune per camera:
    # a slower camera needs a larger value, which makes relative moves longer.
    pan_seconds_per_fov: float = 0.9
    tilt_seconds_per_fov: float = 0.7
    min_move_seconds: float = 0.10
    max_move_seconds: float = 4.0
    zoom_enabled: bool = True

    # --- media profile -----------------------------------------------------
    profile_token: str = "profile_1"
    video_width: int = 1920
    video_height: int = 1080
    video_fps: int = 25
    rtsp_url: str = ""

    # --- housekeeping ------------------------------------------------------
    log_level: str = "INFO"
    startup_timeout: float = 20.0
    health_interval: float = 20.0

    @classmethod
    def from_env(cls) -> "Config":
        cfg = cls(
            camera_host=_str("CAMERA_HOST"),
            camera_port=_int("CAMERA_PORT", 8000),
            camera_user=_str("CAMERA_USER", "admin"),
            camera_password=_str("CAMERA_PASSWORD"),
            ptz_socket=_str("PTZ_SOCK", "/tmp/ptz_control.sock"),
            ptz_binary=_str("PTZ_BINARY", "/app/ptz_control"),
            binding_host=_str("ONVIF_BIND", "0.0.0.0"),
            onvif_port=_int("ONVIF_PORT", 8000),
            onvif_user=_str("ONVIF_USER"),
            onvif_password=_str("ONVIF_PASSWORD"),
            advertise_host=_str("ONVIF_ADVERTISE_HOST"),
            default_speed=_int("PTZ_SPEED", 4),
            pan_seconds_per_fov=_float("PAN_SECONDS_PER_FOV", 0.9),
            tilt_seconds_per_fov=_float("TILT_SECONDS_PER_FOV", 0.7),
            min_move_seconds=_float("MIN_MOVE_SECONDS", 0.10),
            max_move_seconds=_float("MAX_MOVE_SECONDS", 4.0),
            zoom_enabled=_bool("PTZ_ZOOM", True),
            profile_token=_str("PROFILE_TOKEN", "profile_1"),
            video_width=_int("VIDEO_WIDTH", 1920),
            video_height=_int("VIDEO_HEIGHT", 1080),
            video_fps=_int("VIDEO_FPS", 25),
            rtsp_url=_str("RTSP_URL"),
            log_level=_str("LOG_LEVEL", "INFO").upper(),
            startup_timeout=_float("STARTUP_TIMEOUT", 20.0),
            health_interval=_float("HEALTH_INTERVAL", 20.0),
        )

        if not cfg.camera_host:
            raise SystemExit(
                "CAMERA_HOST is required (e.g. CAMERA_HOST=192.168.1.10)"
            )
        if not cfg.camera_password:
            raise SystemExit(
                "CAMERA_PASSWORD is required (the camera's verification code)"
            )
        cfg.default_speed = max(1, min(7, cfg.default_speed))
        return cfg
