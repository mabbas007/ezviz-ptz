# ezviz-ptz

PTZ control for EZVIZ cameras (tested on the EZVIZ C8C and TY1) using the
Hikvision HCNetSDK: a small UNIX-socket PTZ daemon with integrations for
ZoneMinder (control module) and Frigate (ONVIF bridge with autotracking).

## Tested hardware

| Model | ONVIF | Verified on the hardware |
| --- | --- | --- |
| EZVIZ C8C | none | SDK login, continuous pan/tilt, timed relative moves (`RelativeMove`); zoom and presets rejected with `NET_DVR_NETWORK_ERRORDATA` |
| EZVIZ TY1 | none | SDK login, continuous pan/tilt; zoom and presets rejected with `NET_DVR_NETWORK_ERRORDATA` |

Both are Hikvision-OEM devices with **no ONVIF service**: a full TCP scan finds
only RTSP (554), the vendor SDK (8000) and EZVIZ-proprietary ports — 9010/9020
on both, plus 8443 on one unit, which completes a TLS handshake but never
answers HTTP. No HTTP client endpoint responds, so there is no ONVIF or ISAPI
service to talk to. The SDK on port 8000 is the only control path, and this
project exists to give Frigate the ONVIF endpoint its autotracking requires.

Neither model accepts zoom commands, and both reject PTZ preset operations
(`NET_DVR_NETWORK_ERRORDATA`), so zoom and `return_preset` are not available —
see [`frigate-onvif/README.md`](frigate-onvif/README.md#presets).

## Contents

| File | Purpose |
| --- | --- |
| `ptz_control.cpp` | Single binary: PTZ service (`serve`) and thin client |
| `HikVisionExternal.pm` | ZoneMinder control module driving the binary |
| `frigate-onvif/` | ONVIF PTZ wrapper image/config so Frigate can autotrack EZVIZ cameras |
| `frigate-onvif-go/` | Go ONVIF bridge: HTTP/SOAP server, PTZ controller, daemon supervisor |
| `docker-compose.yml` | Runs the ONVIF wrapper for Frigate (`docker compose up -d`) |
| `.env.example` | Camera / ONVIF settings; copy to `.env` for compose |
| `ARCHITECTURE.md` | How the bridge, SDK daemon, tests and deployment fit together |
| `HCNetSDKCom/` | Hikvision HCNetSDK headers and libraries (Linux x86-64) |

## Build

The SDK shipped in `HCNetSDKCom/` is a **Linux x86-64** binary, so the program
must be built on Linux x86-64 (it cannot be built or run on macOS/ARM).

    make

On macOS (Apple Silicon), build inside a Linux x86-64 container:

    docker run --rm --platform linux/amd64 -u "$(id -u):$(id -g)" \
      -v "$PWD":/src -w /src gcc:12 make

The result is `./ptz_control`. Keep the `HCNetSDKCom/lib` directory next to the
binary (next to its parent when installed): the SDK libraries are located through
the `$ORIGIN/HCNetSDKCom/lib` rpath.

The ONVIF bridge itself is Go (`frigate-onvif-go/`, standard library only) and is
built for linux/amd64 by the Docker image. With a Go 1.23 toolchain:

    cd frigate-onvif-go && go build ./... && go test ./...

`make test` runs the end-to-end ONVIF suite against the image (Docker required).

## Usage

    ./ptz_control serve <ip> <port> <user> <pass> [socket]
       -> starts the PTZ service (SDK login, UNIX socket)

    ./ptz_control <CMD> <START|STOP> [speed] [socket]
       -> sends the command to the service (speed 1-7, default 4)

    ./ptz_control POS [socket]       -> raw pan/tilt/zoom position
    ./ptz_control PING [socket]      -> liveness check

    CMD = LEFT | RIGHT | UP | DOWN | ZOOM_IN | ZOOM_OUT

Example:

    ./ptz_control serve 192.168.1.10 8000 admin secret /tmp/ptz_cam1.sock &
    ./ptz_control LEFT START 5 /tmp/ptz_cam1.sock
    ./ptz_control LEFT STOP  /tmp/ptz_cam1.sock

The service logs in once and keeps the session open; each client call only sends
one command, so movement is `START` then `STOP`.

### Socket path

The socket path is resolved in this order:

1. the optional `[socket]` argument,
2. the `PTZ_SOCK` environment variable,
3. `/tmp/ptzd.socket`.

Use a different socket for each camera when running several services. The
ZoneMinder module derives one automatically from the monitor ID, host and port
(`/tmp/ptz_<monitor-id>_<host>_<port>.sock`) and passes it to both the service
and the client, so instances never collide.

The service removes its socket on `SIGTERM`/`SIGINT`, and unlinks a stale socket
before binding on startup.

## ZoneMinder setup

1. Copy `ptz_control` to `/opt/ptz_control/ptz_control` and keep the SDK
   directory at `/opt/ptz_control/HCNetSDKCom/`.
2. Copy `HikVisionExternal.pm` to your ZoneMinder control modules directory
   (e.g. `/usr/share/perl5/ZoneMinder/Control/`).
3. Create or edit the monitor and select `HikVisionExternal` as the control
   type.
4. Set **Control Address** to:

       user:password@host:port

   for example `admin:secret@192.168.1.10:8000`.

The module starts the service when ZoneMinder opens the control and stops it
when the control is closed, logging to `/var/log/zm/ptz_service.log`.

## Frigate (ONVIF autotracking)

Frigate needs ONVIF, not the ZoneMinder module. [`frigate-onvif/`](frigate-onvif/)
packages this same SDK daemon behind a small ONVIF server. The compose file at
the repository root builds and runs it:

    cp .env.example .env      # camera address, verification code, ONVIF port
    docker compose up -d --build

One container controls one camera. `docker compose ps` shows `healthy` once
the SDK has logged into the camera; `curl http://127.0.0.1:8081/healthz`
returns `200` with JSON (and `503` while the camera is unreachable). Then in
Frigate's configuration:

```yaml
cameras:
  c8c:
    ffmpeg:
      inputs:
        - path: rtsp://127.0.0.1:8554/c8c
          roles: [detect, record]
    detect:
      width: 1920
      height: 1080
    onvif:
      # Docker host running the bridge, or the bridge's service name when it
      # shares Frigate's compose network.
      host: 192.168.1.50
      port: 8081             # ONVIF_PORT
      user: frigate          # ONVIF_USER
      password: CHANGE_ME    # ONVIF_PASSWORD
      autotracking:
        enabled: true
        calibrate_on_startup: true
        track:
          - person
          - car
        zooming: disabled    # no zoom position spaces are advertised
```

The bridge advertises continuous pan/tilt and FOV relative movement (so Frigate
can autotrack) but no zoom position spaces, and emulates relative moves as timed
SDK moves.

**Presets are not available.** Frigate needs a stored preset to return the
camera home after tracking, and this camera rejects every preset operation over
the Hikvision SDK (`NET_DVR_NETWORK_ERRORDATA`), so the bridge reports an empty
preset list. Leave `return_preset` out of the configuration: autotracking works,
but the camera stays wherever tracking ended. Do not enable Frigate's
`return_preset` or `PTZ_PRESETS` — they cannot work on this hardware.

For a second camera, copy the `ptz` service with its own
`ONVIF_PORT` and `CAMERA_*` values — see
[`frigate-onvif/docker-compose.example.yml`](frigate-onvif/docker-compose.example.yml)
or merge that block into your Frigate compose file.

### Prebuilt image (GitHub Container Registry)

The workflow [`.github/workflows/docker-publish.yml`](.github/workflows/docker-publish.yml)
tests the bridge, builds the `linux/amd64` image and pushes it to GHCR on every
push to `main` (and on manual runs from `main`). Pull requests run the tests but
do not publish. No secrets or repository settings are needed — it uses
`GITHUB_TOKEN`.

Versions are `MAJOR.MINOR`:

- `VERSION` at the repository root holds the major version (currently `1`).
  Edit it to bump the major.
- The minor is the number of first-parent commits since `VERSION` last changed.
  The build that includes a `VERSION` change is `X.0`; each later commit
  increments it. A push containing several commits advances by that many.
- Commit the major bump as its own commit/push for a clean `2.0`.

```
ghcr.io/mabbas007/ezviz-ptz:1.42       # exact build (pin this for deployment)
ghcr.io/mabbas007/ezviz-ptz:1          # newest 1.x
ghcr.io/mabbas007/ezviz-ptz:latest     # newest overall
ghcr.io/mabbas007/ezviz-ptz:main       # newest main build
ghcr.io/mabbas007/ezviz-ptz:sha-abc1234
```

Bumping `VERSION` to `2` and pushing publishes `2.0`, moves `:2` and `:latest`,
and leaves `:1` at the last 1.x build. Re-running a workflow for the same commit
publishes the same version again.

After the image push succeeds, the workflow also creates a **GitHub Release**
tagged `vX.Y` at that commit with generated release notes. Re-runs reuse an
existing release instead of failing. Image tags do not carry the `v` prefix
(`1.42`), while Git tags and releases do (`v1.42`).

To run the published image, replace the `build:` block in `docker-compose.yml`
with the image:

```yaml
services:
  ptz:
    image: ghcr.io/mabbas007/ezviz-ptz:latest
    # no build: block
```

`docker compose pull && docker compose up -d`. The package is **private by
default**: either create a PAT with `read:packages` and run
`docker login ghcr.io -u mabbas007 --password <PAT>`, or make the package public
under GitHub → your profile → Packages → `ezviz-ptz` → Settings → Change
visibility.

The image bundles the vendor HCNetSDK binaries from `HCNetSDKCom/`; check
Hikvision's SDK terms before making the package public.

The image is `linux/amd64` only because the vendor SDK libraries are x86-64.
See [`frigate-onvif/README.md`](frigate-onvif/README.md) for the full Frigate
setup, environment variables and movement tuning.
