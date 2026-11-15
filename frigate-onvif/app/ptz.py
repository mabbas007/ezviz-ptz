"""PTZ state machine on top of the Hikvision SDK daemon.

The camera only understands "start moving in direction X" / "stop", so ONVIF
relative moves are emulated here: start, wait for the requested fraction of a
field of view, stop.  The wrapper therefore knows whether the motor is moving,
which is what Frigate's autotracking needs (moving frames are skipped and
camera motion is compensated in software).
"""

from __future__ import annotations

import logging
import math
import threading

from .config import Config
from .sdk import SdkClient, SdkError

logger = logging.getLogger(__name__)

CONTINUOUS = "continuous"
ZOOM_DIRECTIONS = ("ZOOM_IN", "ZOOM_OUT")


def _clamp(value: float, low: float, high: float) -> float:
    return max(low, min(high, value))


class PTZController:
    def __init__(self, client: SdkClient, cfg: Config) -> None:
        self._client = client
        self._cfg = cfg
        self._lock = threading.RLock()
        # direction -> owner ("continuous" or the timed move's Event object)
        self._active: dict[str, object] = {}
        # True while a timed (relative) move thread is running.
        self._timed = False
        self._cancel: threading.Event | None = None

    # -- status -------------------------------------------------------------
    @property
    def pan_tilt_moving(self) -> bool:
        with self._lock:
            return (
                any(direction not in ZOOM_DIRECTIONS for direction in self._active)
                or self._timed
            )

    @property
    def zoom_moving(self) -> bool:
        with self._lock:
            return any(direction in ZOOM_DIRECTIONS for direction in self._active)

    @property
    def moving(self) -> bool:
        return self.pan_tilt_moving or self.zoom_moving

    def stop(self) -> None:
        """Stop every active movement (ONVIF ``Stop``)."""
        with self._lock:
            self._cancel_timed_locked()
            self._stop_active_locked()

    # -- continuous movement ------------------------------------------------
    def continuous_move(
        self, pan: float, tilt: float, zoom: float, speed: float | None
    ) -> None:
        """Start/stop directions so the active set matches the request."""
        speed_sdk = self._sdk_speed(speed)
        desired: dict[str, int] = {}

        if abs(pan) >= 0.01:
            desired["RIGHT" if pan > 0 else "LEFT"] = speed_sdk
        if abs(tilt) >= 0.01:
            desired["UP" if tilt > 0 else "DOWN"] = speed_sdk
        if self._cfg.zoom_enabled and abs(zoom) >= 0.01:
            desired["ZOOM_IN" if zoom > 0 else "ZOOM_OUT"] = speed_sdk

        with self._lock:
            self._cancel_timed_locked()

            if not desired:
                self._stop_active_locked()
                return

            for direction in list(self._active):
                if direction not in desired:
                    self._stop_locked(direction)
                    self._active.pop(direction, None)
                else:
                    # Take over from a timed move that we just cancelled.
                    self._active[direction] = CONTINUOUS

            for direction, sdk_speed in desired.items():
                if direction in self._active:
                    continue
                try:
                    self._client.command(direction, "START", sdk_speed)
                except SdkError as exc:
                    logger.warning("ContinuousMove %s failed: %s", direction, exc)
                    continue
                self._active[direction] = CONTINUOUS

    # -- relative movement --------------------------------------------------
    def relative_move(
        self, pan: float, tilt: float, zoom: float, speed: float | None
    ) -> None:
        """Emulate ONVIF ``RelativeMove`` (FOV space) with a timed move.

        ``pan``/``tilt`` are in units of the current field of view, matching
        the TranslationSpaceFov we advertise (range -1..1).
        """
        if abs(zoom) >= 0.01:
            logger.debug("Ignoring relative zoom %s (only continuous zoom is supported)", zoom)

        pan = _clamp(pan, -1.0, 1.0)
        tilt = _clamp(tilt, -1.0, 1.0)
        if abs(pan) < 0.02:
            pan = 0.0
        if abs(tilt) < 0.02:
            tilt = 0.0

        speed_sdk = self._sdk_speed(speed)

        with self._lock:
            self._cancel_timed_locked()
            self._stop_active_locked()

            if pan == 0.0 and tilt == 0.0:
                return

            cancel = threading.Event()
            self._cancel = cancel
            self._timed = True

        thread = threading.Thread(
            target=self._timed_move,
            args=(pan, tilt, speed_sdk, cancel),
            name="ptz-relative-move",
            daemon=True,
        )
        thread.start()

    def _timed_move(
        self, pan: float, tilt: float, speed_sdk: int, cancel: threading.Event
    ) -> None:
        steps: list[tuple[str, float]] = []
        if pan:
            steps.append(
                (
                    "RIGHT" if pan > 0 else "LEFT",
                    self._duration(abs(pan), self._cfg.pan_seconds_per_fov),
                )
            )
        if tilt:
            steps.append(
                (
                    "UP" if tilt > 0 else "DOWN",
                    self._duration(abs(tilt), self._cfg.tilt_seconds_per_fov),
                )
            )

        try:
            for direction, seconds in steps:
                with self._lock:
                    if cancel.is_set():
                        break
                    try:
                        self._client.command(direction, "START", speed_sdk)
                    except SdkError as exc:
                        logger.warning("RelativeMove %s failed: %s", direction, exc)
                        continue
                    self._active[direction] = cancel

                cancel.wait(seconds)

                with self._lock:
                    # Stop only if this move still owns the direction; a newer
                    # command may have taken it over while we were waiting.
                    if self._active.get(direction) is cancel:
                        self._stop_locked(direction)
                        self._active.pop(direction, None)
        finally:
            with self._lock:
                if self._cancel is cancel:
                    self._cancel = None
                    self._timed = False

    def _duration(self, fraction_of_fov: float, seconds_per_fov: float) -> float:
        duration = fraction_of_fov * seconds_per_fov
        if duration <= 0:
            return 0.0
        return _clamp(
            duration, self._cfg.min_move_seconds, self._cfg.max_move_seconds
        )

    # -- helpers ------------------------------------------------------------
    def _sdk_speed(self, value: float | None) -> int:
        if value is None:
            return self._cfg.default_speed
        try:
            speed = abs(float(value))
        except (TypeError, ValueError):
            return self._cfg.default_speed
        if not math.isfinite(speed) or speed <= 0:
            return self._cfg.default_speed
        # ONVIF velocities are normalized -1..1; the camera takes 1..7.
        return int(_clamp(round(speed * 7), 1, 7))

    def _stop_locked(self, direction: str) -> None:
        try:
            self._client.command(direction, "STOP")
        except SdkError as exc:
            logger.debug("Stop %s failed: %s", direction, exc)

    def _stop_active_locked(self) -> None:
        for direction in list(self._active):
            self._stop_locked(direction)
            self._active.pop(direction, None)

    def _cancel_timed_locked(self) -> None:
        if self._cancel is not None:
            self._cancel.set()
            self._cancel = None
        self._timed = False
