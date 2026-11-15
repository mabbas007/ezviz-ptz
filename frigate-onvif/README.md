# EZVIZ / Hikvision → ONVIF PTZ bridge

Frigate can only talk to cameras over ONVIF for PTZ. The EZVIZ C8C and TY1 do
not expose ONVIF at all: no HTTP endpoint answers on either camera (only RTSP
554, the SDK on 8000 and EZVIZ-proprietary ports), so there is neither an ONVIF
nor an ISAPI service, and the Hikvision SDK on port 8000 is the only control
path.

This directory contains a small wrapper that speaks ONVIF to Frigate and the
Hikvision SDK to the camera:

```
Frigate ──SOAP/ONVIF──▶ /app/bridge  (Go: internal/server + internal/onvif,
                          │             WS-Security auth)
                          ├── internal/ptz   timed "relative" moves,
                          │                  MOVING/IDLE state
                          └── UNIX socket ──▶ ptz_control serve
                                               (Hikvision SDK, logs in once)
                                                    │
                                                    ▼
                                              camera:8000
```

One container controls one camera. The image is `linux/amd64` only because the
vendor SDK libraries are x86-64.

## What Frigate gets

- Media profile with a video source, H.264 encoder and a PTZ configuration.
- PTZ service advertising **continuous pan/tilt**, **FOV relative pan/tilt**
  (`TranslationSpaceFov`, range -1..1) and `MoveStatus`.
- No absolute position spaces, and no relative/absolute zoom spaces. Frigate
  therefore disables autotracking zoom by itself (see
  [Frigate's ONVIF docs](https://docs.frigate.video/configuration/autotracking)).
- `RelativeMove` is emulated by a timed start → sleep → stop on the SDK. The
  wrapper knows when the motor is moving and reports it through `GetStatus`,
  which is what autotracking needs.
- No preset support: `GetPresets` always returns an empty list because the
  camera rejects every preset operation (see [Presets](#presets) below).
- `Stop`, `ContinuousMove`, `GetNodes`, `GetConfigurations`,
  `GetConfigurationOptions`, `GetServiceCapabilities`, `GetStatus`.

**Not implemented:** absolute moves (returns a SOAP fault), imaging/focus,
events, snapshots. Frigate does not need them for autotracking.

## Quick start (Docker Compose)

From the repository root (the build context must contain `HCNetSDKCom/`):

```sh
cp .env.example .env      # camera address, verification code, ONVIF port
docker compose up -d --build
```

That runs one camera. The container listens on `ONVIF_PORT` (8081 by default)
and publishes the same port on the host; `docker compose ps` shows `healthy`
once the SDK is logged into the camera.

To skip the local build, use the image published by GitHub Actions
(`ghcr.io/mabbas007/ezviz-ptz:latest`) by replacing the `build:` block in
`docker-compose.yml` with `image: ghcr.io/mabbas007/ezviz-ptz:latest`; see the
root README for tags, login and package visibility.

Point Frigate at the Docker host — `onvif.host: 192.168.1.50` in the example
below. On Docker Desktop (macOS/Windows) use `host.docker.internal`; if Frigate
uses `network_mode: host`, set the same on the `ptz` service (and drop `ports`)
and use `127.0.0.1`. When the bridge joins Frigate's compose network, use the
service name and the in-container port instead (`onvif.host: ptz`,
`onvif.port: 8081`).

### More cameras

Frigate's `onvif.host`/`onvif.port` cannot contain a path, so every camera gets
its own service and port. Copy the `ptz` service in `docker-compose.yml` with a
new name, `ONVIF_PORT` and `CAMERA_*` values, or use
[`docker-compose.example.yml`](docker-compose.example.yml), which shows two
cameras and can also be merged into the compose file you use for Frigate.

### Without compose

The image is `linux/amd64` only, so the platform flag is required when building
on an arm64 host (it is a no-op on x86-64):

```sh
docker build --platform linux/amd64 -f frigate-onvif/Dockerfile -t ezviz-ptz-onvif:latest .
```

Then run one container per camera:

```sh
docker run -d --name ptz-camera1 --restart unless-stopped \
  --platform linux/amd64 \
  --network host \
  -e CAMERA_HOST=192.168.1.10 \
  -e CAMERA_PASSWORD=CHANGE_ME \
  -e ONVIF_PORT=8081 \
  -e ONVIF_USER=frigate \
  -e ONVIF_PASSWORD=CHANGE_ME \
  ezviz-ptz-onvif:latest
```

Health: `curl http://127.0.0.1:8081/healthz`. It returns `200` when the SDK
daemon is logged into the camera and `503` while the camera is unreachable.

## Frigate configuration

```yaml
cameras:
  c8c:
    ffmpeg:
      inputs:
        - path: rtsp://127.0.0.1:8554/c8c    # your normal Frigate input
          roles: [detect, record]
    detect:
      width: 1920
      height: 1080
    onvif:
      # Docker host running the bridge, or the service name when the bridge
      # shares this compose network. See "Quick start" above.
      host: 192.168.1.50
      port: 8081             # ONVIF_PORT
      user: frigate          # ONVIF_USER
      password: change-me    # ONVIF_PASSWORD
      autotracking:
        enabled: true
        calibrate_on_startup: true
        track:
          - person
          - car
        zooming: disabled    # no zoom spaces are advertised, keep it off
```

Do **not** set `return_preset`. Frigate uses it to drive the camera back to a
stored preset after tracking, and this camera cannot store presets (see
[Presets](#presets)), so the wrapper reports none and the option cannot work.

Frigate's `onvif.host`/`onvif.port` cannot contain a path, which is why each
camera needs its own wrapper port (and therefore its own container).

## Environment variables

### Camera (Hikvision SDK)

| Variable | Default | Meaning |
| --- | --- | --- |
| `CAMERA_HOST` | *(required)* | Camera IP, e.g. `192.168.1.10`. |
| `CAMERA_PORT` | `8000` | SDK port (not the RTSP or HTTP port). |
| `CAMERA_USER` | `admin` | Camera user. |
| `CAMERA_PASSWORD` | *(required)* | EZVIZ verification code (on the camera label). |

### ONVIF server

| Variable | Default | Meaning |
| --- | --- | --- |
| `ONVIF_PORT` | `8000` | Listen port (what Frigate uses as `onvif.port`). |
| `ONVIF_USER` | *(empty)* | Username for WS-Security auth. Empty = no auth required. |
| `ONVIF_PASSWORD` | *(empty)* | Password for WS-Security auth. Empty = no auth required. |
| `ONVIF_ADVERTISE_HOST` | *(empty)* | Override the host used in XAddrs (default: request `Host` header). |

### Movement tuning

| Variable | Default | Meaning |
| --- | --- | --- |
| `PTZ_SPEED` | `4` | SDK speed 1–7 used when the client sends no/zero speed. |
| `PAN_SECONDS_PER_FOV` | `0.9` | Seconds to pan one full field of view at `PTZ_SPEED`. |
| `TILT_SECONDS_PER_FOV` | `0.7` | Seconds to tilt one full field of view at `PTZ_SPEED`. |
| `PTZ_ZOOM` | `true` | Advertise continuous zoom and forward it to the SDK. Not needed by Frigate autotracking; set `false` for cameras that reject zoom commands (EZVIZ C8C and TY1 answer `NET_DVR_NETWORK_ERRORDATA`). |

### Advanced (not wired into the sample compose files)

These have working defaults. To change one, add the variable to the service's
`environment:` block as well — the sample `docker-compose.yml` only forwards
the variables above.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ONVIF_BIND` | `0.0.0.0` | Listen address. |
| `MIN_MOVE_SECONDS` | `0.10` | Shortest timed move (keeps tiny corrections from being dropped). |
| `MAX_MOVE_SECONDS` | `4.0` | Longest timed move; also bounds how long `MOVING` can last. |
| `STARTUP_TIMEOUT` | `20` | Seconds to wait for the SDK login at startup. |
| `HEALTH_INTERVAL` | `20` | Seconds between daemon health checks. |
| `LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARNING`, … |
| `PTZ_SOCK` | `/tmp/ptz_control.sock` | UNIX socket for the SDK daemon (inside the container). |
| `PTZ_BINARY` | `/app/ptz_control` | Path to the SDK daemon binary. |
| `PROFILE_TOKEN` | `profile_1` | Media profile token reported to clients. |
| `VIDEO_WIDTH` / `VIDEO_HEIGHT` / `VIDEO_FPS` | `1920` / `1080` / `25` | Cosmetic media-profile metadata only (bounds, resolution, framerate). It never changes the video; Frigate uses its own ffmpeg input. |
| `RTSP_URL` | *(empty)* | If set, `GetStreamUri` returns it; otherwise that operation faults. Frigate does not call it for PTZ. |

### Calibrating `PAN_SECONDS_PER_FOV` / `TILT_SECONDS_PER_FOV`

A timed relative move is only as accurate as these numbers. Time how long the
camera takes to pan from one edge of the image to the other at `PTZ_SPEED`, and
put that in `PAN_SECONDS_PER_FOV`; do the same vertically for
`TILT_SECONDS_PER_FOV`.

- Too small: requested moves are shorter than Frigate expects, so autotracking
  lags behind a moving object.
- Too large: the camera overshoots and autotracking oscillates.

Speeds other than `PTZ_SPEED` are scaled linearly from these values, which is
approximate. Frigate's `calibrate_on_startup` still runs and measures the
actual movement, but the wrapper's own timing comes from this environment.

## Presets

Not supported. Every preset operation (`NET_DVR_PTZPreset_Other` with
`SET_PRESET`, `CLE_PRESET` or `GOTO_PRESET`) fails with
`NET_DVR_NETWORK_ERRORDATA` on this camera family, for all 300 slots and on the
channel the camera itself reports. The camera exposes no ISAPI/HTTP endpoint
either (ports 80 and 443 are closed), so there is no alternative protocol to
store presets with.

Consequences:

- `GetPresets` is implemented for ONVIF compliance but always returns an empty
  list, and the PTZ node advertises `MaximumNumberOfPresets` `0`.
- `SetPreset`, `RemovePreset` and `GotoPreset` are not implemented.
- Frigate's `return_preset` cannot be used. Autotracking works, but the camera
  stays wherever tracking ended instead of returning home.

## The daemon (`ptz_control`)

The Go bridge supervises `/app/ptz_control serve`, which logs into the camera
once and then accepts newline-terminated commands on the UNIX socket:

```
PING                        -> PONG
POS                         -> POS <pan> <tilt> <zoom> | ERR <code>
LEFT START [speed]          -> OK | ERR <code>
LEFT STOP                   -> OK | ERR <code>
```

If the daemon dies or stops answering `PING` three times in a row, it is
restarted with exponential backoff. If the camera is unreachable, the ONVIF
server stays up (Frigate can still read the profile) and `/healthz` reports
`degraded`.

## Tests

The integration test needs no camera: it preloads a fake Hikvision SDK, then
drives the bridge with the same `onvif-zeep-async==4.3.0` that Frigate pins,
replicating Frigate's init sequence and autotracking calls. It also validates
the root `docker-compose.yml`.

```sh
./tests/run.sh        # or: make test
```

It builds the bridge image, starts it with `tests/fake_hcnetsdk.c`, and checks
discovery, profiles, spaces, `MoveStatus`, status, relative and
continuous moves, the empty preset list, and authentication.

The bridge itself is Go and has unit tests for the ONVIF surface, WS-Security,
PTZ state machine, HTTP contract and configuration:

```sh
cd frigate-onvif-go && go test ./...
```

## Troubleshooting

**`Login failed 1`** in the container log: wrong `CAMERA_USER` /
`CAMERA_PASSWORD`. Use the EZVIZ verification code (six letters, on the camera
label), not the EZVIZ account password.

**`Login failed 7`**: the camera did not answer on `CAMERA_HOST:CAMERA_PORT`.
Check the port is 8000 (SDK), the camera is powered and on the same LAN, and
nothing blocks the container from reaching it.

**Frigate logs "does not support the ONVIF GetStatus method"**: this wrapper
always reports it. If it appears, check the bridge log for exceptions and
confirm `GET /healthz` is `200`.

**Autotracking oscillates or lags**: tune `PAN_SECONDS_PER_FOV` /
`TILT_SECONDS_PER_FOV`.

**Camera never returns home after tracking**: expected. Presets are unsupported
on this hardware, so `return_preset` cannot be used — see [Presets](#presets).

**Container is healthy but Frigate cannot connect**: check the port mapping /
host network and `ONVIF_ADVERTISE_HOST` if you are behind NAT or a reverse
proxy. XAddrs must be reachable from Frigate.
