"""End-to-end ONVIF test that mirrors Frigate's autotracking setup.

Run against a bridge container that uses tests/fake_hcnetsdk.c (see run.sh):

    ONVIF_HOST=ptz-test ONVIF_USER=onvif ONVIF_PASSWORD=secret \
        python test_frigate_init.py
"""

from __future__ import annotations

import asyncio
import os
import sys
import time

from onvif import ONVIFCamera

HOST = os.environ.get("ONVIF_HOST", "127.0.0.1")
PORT = int(os.environ.get("ONVIF_PORT", "8000"))
USER = os.environ.get("ONVIF_USER", "onvif")
PASSWORD = os.environ.get("ONVIF_PASSWORD", "secret")

PROFILE_TOKEN = os.environ.get("PROFILE_TOKEN", "profile_1")

checks = 0


def check(condition: object, message: str) -> None:
    global checks
    checks += 1
    if not condition:
        raise AssertionError(message)
    print(f"  ok  {message}")


def find_by_key(dictionary, target_key):
    """Copy of frigate.util.builtin.find_by_key (used for MoveStatus)."""
    if target_key in dictionary:
        return dictionary[target_key]
    for value in dictionary.values():
        if isinstance(value, dict):
            result = find_by_key(value, target_key)
            if result is not None:
                return result
    return None


async def wait_until_idle(ptz, expect_moving: bool = True, timeout: float = 10.0):
    deadline = time.monotonic() + timeout
    saw_moving = False
    while time.monotonic() < deadline:
        status = await ptz.GetStatus({"ProfileToken": PROFILE_TOKEN})
        state = status.MoveStatus.PanTilt
        if state == "MOVING":
            saw_moving = True
        elif state == "IDLE":
            if expect_moving:
                check(
                    saw_moving,
                    "GetStatus reported MOVING during the move",
                )
            return status
        await asyncio.sleep(0.05)
    raise AssertionError("camera never returned to IDLE")


async def main() -> int:
    print(f"connecting to {HOST}:{PORT} as {USER}")
    cam = ONVIFCamera(HOST, PORT, USER, PASSWORD)
    try:
        await cam.update_xaddrs()
        check(True, "update_xaddrs / GetServices")

        # Frigate only calls this on the GetServices fallback path, but a
        # malformed GetCapabilities response would still break real clients.
        capabilities = await cam.get_capabilities()
        check(
            isinstance(capabilities, dict)
            and capabilities.get("PTZ") is not None,
            "device GetCapabilities reports a PTZ service",
        )

        media = await cam.create_media_service()
        profiles = await media.GetProfiles()
        check(len(profiles) == 1, "media.GetProfiles returns one profile")
        profile = profiles[0]
        check(profile.token == PROFILE_TOKEN, f"profile token is {PROFILE_TOKEN}")
        check(
            profile.VideoEncoderConfiguration is not None,
            "profile has a VideoEncoderConfiguration",
        )
        check(
            profile.PTZConfiguration is not None,
            "profile has a PTZConfiguration",
        )

        ptz_config = profile.PTZConfiguration
        fov_uri = "TranslationSpaceFov"
        check(
            ptz_config.DefaultContinuousPanTiltVelocitySpace,
            "profile advertises continuous pan/tilt velocity space",
        )
        check(
            fov_uri in (ptz_config.DefaultRelativePanTiltTranslationSpace or ""),
            "profile advertises the FOV relative translation space",
        )
        check(
            not getattr(ptz_config, "DefaultAbsoluteZoomPositionSpace", None),
            "profile does NOT advertise an absolute zoom space (zoom disabled)",
        )
        check(
            ptz_config.DefaultPTZSpeed is not None,
            "profile has a DefaultPTZSpeed (needed to seed RelativeMove)",
        )

        video_sources = await media.GetVideoSources()
        check(len(video_sources) == 1, "media.GetVideoSources returns one source")
        check(
            video_sources[0].token == "vs_1",
            "video source token is vs_1",
        )
        check(
            profile.VideoEncoderConfiguration.Encoding in ("H264", "H265", "JPEG", "MPEG4"),
            "profile reports a supported video encoding from the SDK",
        )
        check(
            (profile.VideoEncoderConfiguration.Resolution.Width or 0) > 0,
            "profile reports a resolution",
        )

        # Frigate's Add Camera wizard uses GetStreamUri, then ffprobe.
        stream_request = media.create_type("GetStreamUri")
        stream_request.ProfileToken = PROFILE_TOKEN
        stream_request.StreamSetup = {
            "Stream": "RTP-Unicast",
            "Transport": {"Protocol": "RTSP"},
        }
        stream = await media.GetStreamUri(stream_request)
        check(
            stream.Uri.startswith("rtsp://")
            and stream.Uri.rstrip("/").endswith(("/H.264", "/H.265")),
            f"GetStreamUri returns an RTSP stream URL ({stream.Uri})",
        )

        ptz = await cam.create_ptz_service()

        caps = await ptz.GetServiceCapabilities()
        move_status = find_by_key(vars(caps), "MoveStatus")
        check(
            bool(move_status) and str(move_status).lower() == "true",
            f"PTZ service advertises MoveStatus (got {move_status!r})",
        )

        options_request = ptz.create_type("GetConfigurationOptions")
        options_request.ConfigurationToken = ptz_config.token
        options = await ptz.GetConfigurationOptions(options_request)
        relative = options.Spaces.RelativePanTiltTranslationSpace
        check(
            isinstance(relative, list) and len(relative) == 1,
            "GetConfigurationOptions lists RelativePanTiltTranslationSpace",
        )
        check(
            fov_uri in relative[0]["URI"],
            "relative pan/tilt space URI contains TranslationSpaceFov",
        )
        check(
            relative[0]["XRange"]["Min"] == -1
            and relative[0]["XRange"]["Max"] == 1,
            "relative FOV range is -1..1 on X",
        )
        check(
            not getattr(options.Spaces, "RelativeZoomTranslationSpace", None),
            "no relative zoom translation space is advertised",
        )

        status = await ptz.GetStatus({"ProfileToken": PROFILE_TOKEN})
        check(status.Position is not None, "GetStatus returns a Position")
        check(
            status.Position.PanTilt is not None,
            "GetStatus Position has PanTilt (Frigate seeds RelativeMove from it)",
        )
        check(
            status.MoveStatus.PanTilt == "IDLE",
            "GetStatus MoveStatus is IDLE while the motor is stopped",
        )
        check(status.UtcTime is not None, "GetStatus returns UtcTime")

        presets = await ptz.GetPresets({"ProfileToken": PROFILE_TOKEN})
        check(len(presets) == 0, "GetPresets reports no presets (camera cannot store them)")

        # --- replicate Frigate's init sequence for relative moves ----------
        move_request = ptz.create_type("ContinuousMove")
        move_request.ProfileToken = PROFILE_TOKEN

        relative_request = ptz.create_type("RelativeMove")
        relative_request.ProfileToken = PROFILE_TOKEN
        relative_request.Translation = status.Position
        relative_request.Translation.PanTilt.space = relative[0]["URI"]
        if "Zoom" in relative_request["Translation"]:
            del relative_request["Translation"]["Zoom"]
        if relative_request.Speed is None:
            relative_request.Speed = ptz_config.DefaultPTZSpeed

        # --- relative move (pan right) -------------------------------------
        relative_request.Translation.PanTilt.x = 0.5
        relative_request.Translation.PanTilt.y = 0.0
        relative_request.Speed.PanTilt.x = 0.5
        relative_request.Speed.PanTilt.y = 0.5
        started = time.monotonic()
        await ptz.RelativeMove(relative_request)
        check(
            time.monotonic() - started < 1.0,
            "RelativeMove returns immediately (move is timed, not blocking)",
        )
        await wait_until_idle(ptz)
        print(f"  ..  relative move took {time.monotonic() - started:.2f}s")

        # --- relative move in both axes ------------------------------------
        relative_request.Translation.PanTilt.x = -0.5
        relative_request.Translation.PanTilt.y = 0.5
        await ptz.RelativeMove(relative_request)
        await wait_until_idle(ptz)

        # --- continuous move + stop ----------------------------------------
        move_request.Velocity = {"PanTilt": {"x": 0.5, "y": 0.0}}
        await ptz.ContinuousMove(move_request)
        moving = await ptz.GetStatus({"ProfileToken": PROFILE_TOKEN})
        check(
            moving.MoveStatus.PanTilt == "MOVING",
            "ContinuousMove makes GetStatus report MOVING",
        )
        await ptz.Stop(
            {"ProfileToken": PROFILE_TOKEN, "PanTilt": True, "Zoom": True}
        )
        await wait_until_idle(ptz, expect_moving=False)

        # --- continuous zoom reports its own MoveStatus axis ----------------
        if not getattr(options.Spaces, "RelativeZoomTranslationSpace", None):
            print("  .. skipping zoom tests (not supported by camera)")
        else:
            move_request.Velocity = {"Zoom": {"x": 0.5}}
            await ptz.ContinuousMove(move_request)
            moving = await ptz.GetStatus({"ProfileToken": PROFILE_TOKEN})
            check(
                moving.MoveStatus.Zoom == "MOVING",
                "continuous zoom makes GetStatus report Zoom MOVING",
            )
            await ptz.Stop(
                {"ProfileToken": PROFILE_TOKEN, "PanTilt": True, "Zoom": True}
            )
            await wait_until_idle(ptz, expect_moving=False)
            zoom_idle = await ptz.GetStatus({"ProfileToken": PROFILE_TOKEN})
            check(
                zoom_idle.MoveStatus.Zoom == "IDLE",
                "Stop makes GetStatus report Zoom IDLE",
            )

        # --- unsupported absolute move must fail cleanly --------------------
        try:
            await ptz.AbsoluteMove(
                {
                    "ProfileToken": PROFILE_TOKEN,
                    "Position": {"PanTilt": {"x": 0.5, "y": 0.5}},
                }
            )
        except Exception as exc:  # noqa: BLE001 - any SOAP fault is fine
            check(
                "ActionNotSupported" in str(exc) or "Absolute" in str(exc),
                f"AbsoluteMove fails with an ONVIF fault ({type(exc).__name__})",
            )
        else:
            raise AssertionError("AbsoluteMove unexpectedly succeeded")

        # --- authentication -------------------------------------------------
        bad = ONVIFCamera(HOST, PORT, USER, PASSWORD + "-wrong")
        try:
            try:
                await bad.update_xaddrs()
                bad_media = await bad.create_media_service()
                await bad_media.GetProfiles()
            except Exception:  # noqa: BLE001
                check(True, "wrong ONVIF password is rejected")
            else:
                raise AssertionError("wrong ONVIF password was accepted")
        finally:
            await bad.close()

        print(f"\nAll {checks} checks passed.")
        return 0
    finally:
        await cam.close()


if __name__ == "__main__":
    try:
        raise SystemExit(asyncio.run(main()))
    except AssertionError as exc:
        print(f"\nFAILED: {exc}", file=sys.stderr)
        raise SystemExit(1)
