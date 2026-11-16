# Architecture — EZVIZ/Hikvision ONVIF PTZ Bridge

This document describes how the pieces in this repository fit together: the
ONVIF bridge that Frigate talks to, the Hikvision SDK daemon that drives the
camera, the ZoneMinder control module, and the build/test/deploy pipeline.

It is written for someone who has to debug, extend or operate the system, not
just run it. For setup instructions see [README.md](README.md) and
[frigate-onvif/README.md](frigate-onvif/README.md).

---

## Overview

**The problem.** EZVIZ cameras (tested on the C8C and TY1) are Hikvision OEM
devices that do not speak ONVIF at all. A full TCP scan of both finds only RTSP
(554), the vendor SDK (8000) and EZVIZ-proprietary ports (9010/9020, plus 8443
on one unit — TLS completes but no HTTP request is ever answered); no HTTP
client endpoint responds, so there is neither an ONVIF nor an ISAPI service to
talk to. Frigate, however, can only drive PTZ over ONVIF. The Hikvision
HCNetSDK does control the cameras reliably on TCP port 8000 — it is simply
speaking a protocol Frigate cannot use.

**The solution.** A small bridge that speaks ONVIF to Frigate and HCNetSDK to
the camera, providing the ONVIF service the hardware is missing. Frigate thinks
it is talking to a PTZ-capable ONVIF camera; the bridge translates each ONVIF
operation into SDK PTZ commands and emulates the parts ONVIF needs but the SDK
does not provide (notably relative moves and `MoveStatus`).

**The shape.** One container controls one camera. Inside the container a Go
process serves ONVIF over HTTP and supervises a C++ daemon (`ptz_control`) that
holds the single SDK login. The daemon and the Go bridge talk over a UNIX
socket.

```mermaid
flowchart LR
    FR["Frigate<br/>autotracking"]:::client
    T["ONVIF test tools<br/>(zeep, onvif-tt, …)"]:::client

    subgraph HOST["Docker host"]
        subgraph CTR["ptz-onvif container · linux/amd64"]
            OV["ONVIF server<br/>HTTP + SOAP"]
            CT["PTZ controller<br/>timed moves, state"]
            SUP["Daemon supervisor"]
            DMN["ptz_control serve<br/>C++ · HCNetSDK"]
            OV --> CT
            CT -->|"UNIX socket"| DMN
            SUP -->|"spawn, supervise"| DMN
        end
    end

    CAM[("EZVIZ camera<br/>SDK :8000 · RTSP :554")]:::device

    FR -->|"SOAP/ONVIF tcp/8081"| OV
    T -->|"SOAP/ONVIF"| OV
    DMN -->|"HCNetSDK tcp/8000"| CAM

    classDef client fill:#e1f5fe,stroke:#0288d1
    classDef device fill:#fff3e0,stroke:#ef6c00
```

The ZoneMinder path is a separate deployment of the same daemon and is
described in [ZoneMinder control module](#zoneminder-control-module).

---

## Table of contents

1. [Key concepts](#key-concepts)
2. [Components](#components)
3. [Runtime lifecycle](#runtime-lifecycle)
4. [Request flows](#request-flows)
5. [PTZ movement model](#ptz-movement-model)
6. [ONVIF surface](#onvif-surface)
7. [SDK daemon protocol](#sdk-daemon-protocol)
8. [Configuration](#configuration)
9. [Concurrency and failure handling](#concurrency-and-failure-handling)
10. [Security model](#security-model)
11. [Deployment topologies](#deployment-topologies)
12. [Build, test and release pipeline](#build-test-and-release-pipeline)
13. [Design decisions and trade-offs](#design-decisions-and-trade-offs)
14. [Limitations and roadmap](#limitations-and-roadmap)
15. [Source map](#source-map)

---

## Key concepts

| Term | Meaning in this system |
| --- | --- |
| **ONVIF bridge** | The Go process (`frigate-onvif-go/cmd/bridge`) exposing ONVIF over HTTP. |
| **SDK daemon** | `ptz_control serve`: a C++ process that logs into the camera once and executes PTZ commands from the UNIX socket. |
| **Profile** | ONVIF media profile (`profile_1` by default). Frigate reads its PTZ configuration and spaces. |
| **FOV relative space** | `TranslationSpaceFov`: a `RelativeMove` translation is expressed as a fraction of the current field of view (−1..1). |
| **Timed move** | Open-loop emulation of a relative move: start a direction, wait a computed duration, stop. |
| **Preset** | ONVIF preset token. The bridge reports none: this camera rejects every preset operation over the SDK, so `GetPresets` is always empty. |
| **`MoveStatus`** | ONVIF status (`MOVING`/`IDLE`) that Frigate uses to skip moving frames and compensate camera motion. |

---

## Components

### Container diagram

```mermaid
flowchart TB
    subgraph CONTAINER["ptz-onvif container (uid 10001, linux/amd64)"]
        MAIN["cmd/bridge<br/>net/http · routes · health"]
        ONVIF["internal/onvif<br/>SOAP dispatch · WS-Security · XML"]
        PTZ["internal/ptz<br/>Controller state machine"]
        SDK["internal/sdk<br/>Client + Daemon supervisor"]
        BIN["/app/ptz_control<br/>C++ daemon"]
        LIBS["/app/HCNetSDKCom<br/>vendor .so files"]

        MAIN --> ONVIF --> PTZ
        MAIN --> SDK
        PTZ --> SDK
        SDK -->|"spawn, supervise, restart"| BIN
        SDK <-->|"/tmp/ptz_control.sock"| BIN
        BIN -.->|"dlopen"| LIBS
    end

    FR["Frigate"] -->|"POST /onvif/*_service"| MAIN
    HC["Docker healthcheck"] -->|"GET /healthz"| MAIN
    BIN -->|"tcp/8000"| CAM[("camera")]
```

### Go packages

| Package | Responsibility |
| --- | --- |
| `internal/config` | Read and validate environment variables into a `Config`. Clamps speed and fails fast when `CAMERA_HOST` / `CAMERA_PASSWORD` are missing or a number is malformed. |
| `cmd/bridge` | Entry point: config, logging, signals, graceful shutdown, the `healthcheck` subcommand, and wiring the packages together. |
| `internal/server` | HTTP front end: routing, readiness/health JSON, request size limits, `HEAD`, and handing SOAP bodies to `internal/onvif`. |
| `internal/onvif` | All ONVIF/SOAP logic: routing by URL suffix, WS-Security verification, hand-built XML, operation dispatch, faults. |
| `internal/ptz` | Movement state machine: continuous moves, timed relative moves, stop, `MOVING`/`IDLE` bookkeeping. |
| `internal/sdk` | `sdk.Client` (one newline-delimited command per UNIX-socket connection) and `sdk.Daemon` (spawn, readiness, health checks, restart with backoff). |
| `internal/logging` | Configures `log/slog` from `LOG_LEVEL`. |

### Relationship between the classes

```mermaid
classDiagram
    direction LR

    class Config {
        +CameraHost
        +CameraPort
        +OnvifPort
        +Load()
    }

    class App {
        +Handle(path, body, host)
        -authorized(root) bool
        -serviceForPath(path) string
    }

    class Controller {
        +Moving() bool
        +PanTiltMoving() bool
        +ZoomMoving() bool
        +ContinuousMove(pan, tilt, zoom, speed)
        +RelativeMove(pan, tilt, zoom, speed)
        +Stop()
    }

    class Client {
        +Ping() bool
        +Command(direction, action, speed)
    }

    class Daemon {
        +Start()
        +Stop()
        +Running() bool
        +Status()
    }

    App --> Controller
    App --> Config
    Controller --> Client
    Controller --> Config
    Daemon --> Client
    Daemon --> Config
```

### The C++ daemon (`ptz_control`)

One binary, two modes:

- **Service mode** — `ptz_control serve <ip> <port> <user> <pass> [socket]`
  calls `NET_DVR_Init`, logs in with `NET_DVR_Login_V30`, keeps the session
  open, binds a UNIX socket (`chmod 0666`), and answers one newline-terminated
  command per connection.
- **Client mode** — `ptz_control <CMD> <START|STOP> [speed] [socket]`,
  `ptz_control POS [socket]`, `ptz_control PING [socket]`. Used by the
  ZoneMinder module and for manual debugging; the Go bridge talks to the
  socket directly instead of spawning a client per command.

Socket path resolution: explicit argument → `PTZ_SOCK` → `/tmp/ptzd.socket`.
`SIGTERM`/`SIGINT` unlink the socket before exiting, and a stale socket is
unlinked at startup.

---

## Runtime lifecycle

### Startup

```mermaid
sequenceDiagram
    autonumber
    participant DC as docker compose
    participant M as cmd/bridge
    participant H as net/http server
    participant D as sdk.Daemon
    participant B as ptz_control
    participant C as camera

    DC->>M: /app/bridge
    M->>M: config.Load() + logging.Setup()
    M->>H: bind ONVIF_BIND:ONVIF_PORT
    M->>D: Start()
    D->>B: spawn serve host port user pass socket
    B->>C: NET_DVR_Init + NET_DVR_Login_V30
    alt login succeeds
        B->>B: bind + listen UNIX socket
    else login fails
        B-->>D: exit(1) with "Login failed N"
        D->>D: backoff 1s → 60s, retry
    end
    loop until STARTUP_TIMEOUT
        D->>B: PING
        B-->>D: PONG
    end
    D->>D: mark ready, reset backoff
    M->>H: ListenAndServe()
    M-->>DC: "Starting ONVIF server on ..."
```

Notes:

- The HTTP server starts **before** the camera is reachable. Frigate can read
  profiles and configuration while the daemon is still logging in or backing
  off, which keeps Frigate's startup deterministic.
- The server binds one process-wide daemon because one container owns one
  camera.

### Health

`GET /healthz` returns JSON:

```json
{
  "status": "ok",
  "daemon": { "running": true, "pid": 42, "socket": "/tmp/ptz_control.sock" },
  "moving": false
}
```

- `200` while the supervised daemon process is alive.
- `503` when the daemon is down or restarting (for example after a failed SDK
  login; the process exits and the supervisor schedules a retry).
- The Dockerfile `HEALTHCHECK` runs `/app/bridge healthcheck`, which polls this
  endpoint on `ONVIF_PORT` (no curl or Python in the image), which is why
  `docker ps` shows `unhealthy` while the daemon is down or restarting.

The supervisor probes the camera every `HEALTH_INTERVAL` by sending `POS`
through the daemon. `PING` only proves the UNIX socket is alive; `POS`
exercises the SDK session, so it also detects a camera link that dropped after
login. Three consecutive probe failures terminate the daemon and force a fresh
login; spawn failures use exponential backoff (1 s doubling to 60 s). `PING` is
still used only for startup readiness.

---

## Request flows

### ONVIF initialization (what Frigate does on startup)

```mermaid
sequenceDiagram
    autonumber
    participant F as Frigate
    participant O as onvif.App
    participant P as ptz.Controller

    F->>O: GetServices (device_service)
    O-->>F: XAddrs for device, media, ptz
    F->>O: GetCapabilities
    O-->>F: Media + PTZ endpoints
    F->>O: GetProfiles
    O-->>F: profile_1 (source, encoder, PTZ config)
    F->>O: GetConfigurationOptions
    O-->>F: relative FOV space + velocity spaces
    F->>O: GetStatus
    O->>P: pan_tilt_moving / zoom_moving
    P-->>O: IDLE
    O-->>F: Position(0,0) + MoveStatus IDLE
```

The profile deliberately advertises only what the bridge can honor:

- continuous pan/tilt velocity space (`VelocityGenericSpace`),
- relative pan/tilt translation space (`TranslationSpaceFov`, −1..1),
- continuous zoom velocity space only when `PTZ_ZOOM=true`,
- **no absolute position spaces and no relative/absolute zoom spaces**.

Frigate reads those spaces and disables autotracking zoom by itself.

### Autotracking move (`RelativeMove`)

```mermaid
sequenceDiagram
    autonumber
    participant F as Frigate
    participant O as onvif.App
    participant P as ptz.Controller
    participant S as sdk.Client
    participant B as ptz_control
    participant C as camera

    F->>O: RelativeMove(PanTilt.x=0.4, y=0.0, Speed=0.5)
    O->>P: relative_move(pan, tilt, speed)
    P->>P: cancel previous timed move, stop active
    P->>P: spawn ptz-relative-move thread
    P-->>O: return immediately
    O-->>F: 200 RelativeMoveResponse

    P->>S: RIGHT START 4
    S->>B: "RIGHT START 4\n"
    B->>C: NET_DVR_PTZControlWithSpeed_Other
    Note over P: wait |pan| × PAN_SECONDS_PER_FOV, clamped to MIN/MAX
    P->>S: RIGHT STOP
    S->>B: "RIGHT STOP\n"
    B->>C: NET_DVR_PTZControl_Other(stop)

    loop until IDLE
        F->>O: GetStatus
        O->>P: pan_tilt_moving
        P-->>O: MOVING while the timed thread runs
        O-->>F: MoveStatus
    end
```

Key properties:

- The SOAP call returns immediately; the movement continues in a background
  thread, which is what Frigate expects.
- A pan **and** tilt translation is executed sequentially (pan first, then
  tilt), not diagonally.
- A new move cancels the running timed move and takes over ownership of the
  direction, so replies and stops cannot race a newer command.

### Continuous move and stop

`ContinuousMove` starts the requested directions and stops directions that are
no longer requested; `Stop` cancels any timed move and stops every active
direction. `Stop` ignores the request's
`PanTilt`/`Zoom` flags and stops everything.

```mermaid
stateDiagram-v2
    [*] --> IDLE
    IDLE --> MOVING: ContinuousMove / RelativeMove
    MOVING --> MOVING: new command takes over
    MOVING --> IDLE: Stop
    MOVING --> IDLE: timed move finishes
```

Pan/tilt and zoom expose separate axes in `MoveStatus`. A timed relative move
affects pan/tilt; continuous zoom affects zoom.

### Presets are not supported

Measured against both supported models (EZVIZ C8C and TY1), not assumed:

| Test | Result |
| --- | --- |
| `GOTO_PRESET` for all 300 slots | `NET_DVR_ERRORDATA` (11) on every index |
| `SET_PRESET`, `CLE_PRESET` | same error |
| `POS` (`NET_DVR_GET_PTZPOS`) | succeeds, reports `0 0 0` |
| ISAPI over HTTP | ports 80/443 closed; no HTTP listener on 8000/9010 |

The camera accepts continuous pan/tilt on the same channel (`byStartChan`) that
presets fail on, so this is a firmware limitation, not a channel or credentials
problem. With no ISAPI endpoint there is no second protocol to fall back to.

Consequences for the bridge:

- `GetPresets` exists for ONVIF compliance and always returns an empty list;
  the PTZ node advertises `MaximumNumberOfPresets` `0`.
- `SetPreset`, `RemovePreset` and `GotoPreset` are not implemented, and the
  daemon has no `PRESET` verb.
- Frigate's `return_preset` cannot work. Autotracking still runs; the camera
  simply stays where tracking ended.
- No dead-reckoning fallback is provided: without position feedback a virtual
  home position would drift with every move and never be correctable.

---

## PTZ movement model

The camera exposes "start moving in direction X" and "stop"; it exposes no
usable feedback loop, so relative moves are open-loop:

```text
duration = clamp(|translation| × seconds_per_fov, MIN_MOVE_SECONDS, MAX_MOVE_SECONDS)
```

| Input | Behaviour |
| --- | --- |
| `translation` | Clamped to ±1 FOV; values below 0.02 are treated as zero. |
| `speed` | ONVIF −1..1 is mapped to SDK 1..7 (`round(abs(v) × 7)`); absent/zero uses `PTZ_SPEED`. |
| `zoom` in `RelativeMove` | Ignored (no relative zoom space is advertised). |
| Multiple axes | Sequential moves, pan then tilt. |
| New command during a move | Cancels the old move and stops the old direction before starting the new one. |

Tuning matters: **too small** `PAN_SECONDS_PER_FOV`/`TILT_SECONDS_PER_FOV`
makes the camera lag behind the target; **too large** makes autotracking
overshoot and oscillate. Frigate's `calibrate_on_startup` measures its own
expectations, but the bridge's timing comes from these environment values.

---

## ONVIF surface

Service selection is by URL suffix — the bridge does not serve WSDL or
WS-Discovery:

| Suffix | Service |
| --- | --- |
| `/onvif/device_service` | Device |
| `/onvif/media_service` | Media |
| `/onvif/ptz_service` | PTZ |

| Service | Implemented operations |
| --- | --- |
| **Device** | `GetServices`, `GetCapabilities`, `GetDeviceInformation`, `GetSystemDateAndTime`, `GetServiceCapabilities` |
| **Media** | `GetServiceCapabilities`, `GetProfiles`, `GetProfile`, `GetVideoSources`, `GetVideoSourceConfigurations`, `GetVideoEncoderConfigurations`, `GetVideoSourceConfiguration`, `GetVideoEncoderConfiguration`, `GetStreamUri` (auto-built or `RTSP_URL`) |
| **PTZ** | `GetServiceCapabilities`, `GetNodes`, `GetNode`, `GetConfigurations`, `GetConfiguration`, `GetConfigurationOptions`, `GetStatus`, `GetPresets` (always empty), `ContinuousMove`, `RelativeMove`, `Stop` |
| **PTZ (fault)** | `AbsoluteMove` returns `ter:ActionNotSupported` |

Static identifiers:

| Token | Value |
| --- | --- |
| Profile | `PROFILE_TOKEN` (default `profile_1`) |
| Video source / source config / encoder config | `vs_1`, `vsc_1`, `vec_1` |
| PTZ config / node | `ptz_config_1`, `ptz_node_1` |

Faults are spec-shaped SOAP 1.2 envelopes: ONVIF `ter:` codes are subcodes of
`env:Sender` / `env:Receiver` (for example `env:Sender` + `ter:NotAuthorized`),
without the bogus `ActionNotSupported` subcode the Python bridge attached to
every fault. Unsupported operations return `ter:ActionNotSupported`; malformed
XML returns `env:Sender`; bad credentials return `ter:NotAuthorized`. Requests
are capped at 1 MiB.

`GetStatus` always returns `Position(0, 0)` without a `space` attribute: the
bridge advertises no absolute position space and the SDK position is not usable
feedback. Only `MoveStatus` and `UtcTime` are meaningful. This is sufficient for
Frigate, which seeds the `RelativeMove` translation from the returned `PanTilt`
element and overwrites `x`/`y`.

---

## SDK daemon protocol

Newline-terminated ASCII over a UNIX stream socket, one request/reply per
connection:

| Request | Reply |
| --- | --- |
| `PING` | `PONG` |
| `POS` | `POS <pan> <tilt> <zoom>` or `ERR <code>` |
| `LEFT START [1-7]` | `OK` or `ERR <code>` |
| `LEFT STOP` | `OK` or `ERR <code>` |
| `RIGHT`, `UP`, `DOWN`, `ZOOM_IN`, `ZOOM_OUT` | same as above |

Speed defaults to the SDK's default when omitted. `POS` is used as the
camera-aware health probe; on the tested hardware it answers `0 0 0`, but an
unreachable camera returns `ERR <code>`.

---

## Configuration

Everything is environment-driven; `.env.example` documents the full set.

| Group | Variables |
| --- | --- |
| Camera | `CAMERA_HOST` (required), `CAMERA_PORT`, `CAMERA_USER`, `CAMERA_PASSWORD` (required) |
| ONVIF server | `ONVIF_BIND`, `ONVIF_PORT`, `ONVIF_USER`, `ONVIF_PASSWORD`, `ONVIF_ADVERTISE_HOST` |
| Movement | `PTZ_SPEED`, `PAN_SECONDS_PER_FOV`, `TILT_SECONDS_PER_FOV`, `MIN_MOVE_SECONDS`, `MAX_MOVE_SECONDS`, `PTZ_ZOOM` |
| Media profile | `PROFILE_TOKEN`, `VIDEO_ENCODING`, `VIDEO_BITRATE`, `VIDEO_WIDTH`, `VIDEO_HEIGHT`, `VIDEO_FPS` |
| RTSP URL | `RTSP_URL` (override), `RTSP_PORT`, `RTSP_PATH`, `RTSP_ADVERTISE_HOST`, `RTSP_USER`, `RTSP_PASSWORD`, `RTSP_INCLUDE_CREDENTIALS` |
| Daemon / housekeeping | `PTZ_SOCK`, `PTZ_BINARY`, `STARTUP_TIMEOUT`, `HEALTH_INTERVAL`, `LOG_LEVEL` |

Authentication is enabled when **either** `ONVIF_USER` or `ONVIF_PASSWORD` is
non-empty; leave both empty to accept anonymous clients (not recommended).

---

## Concurrency and failure handling

```mermaid
flowchart LR
    subgraph Goroutines
        A["HTTP request handlers<br/>(net/http)"]
        B["PTZ daemon<br/>supervisor"]
        C["timed relative move<br/>(one per move)"]
        D["ptz_control output pump"]
    end

    A -->|"sdk.Client mutex"| S["UNIX socket"]
    C -->|"sdk.Client mutex"| S
    B --> L["process lifecycle"]
```

- **HTTP** — one goroutine per request; handlers may block on the SDK socket.
- **SDK calls** — serialized by a mutex in `sdk.Client`; one connection per
  command, 5 s timeout. An empty reply or `ERR` becomes an error, which
  surfaces as a SOAP fault or a log line.
- **PTZ state** — the `ptz.Controller` mutex guards `active`, `timed` and
  `cancel`. The timed goroutine checks ownership before stopping a direction so
  a newer command is never interrupted by an older one.
- **Supervisor** — a dedicated goroutine loops on `HEALTH_INTERVAL`, probes the
  camera through the daemon with `POS`, and restarts the daemon after three
  consecutive failures. Spawn attempts are retried with exponential backoff,
  capped at 60 s.
- **Shutdown** — `SIGTERM`/`SIGINT` drains the HTTP server (8 s) and stops the
  daemon (`SIGTERM`, then `SIGKILL` after 5 s), within compose's 15 s grace.

Failure matrix:

| Failure | Symptom | Behaviour |
| --- | --- | --- |
| Wrong camera credentials | daemon exits with `Login failed 1` | supervisor retries with backoff; `/healthz` 503 while down |
| Camera unreachable | daemon exits with `Login failed 7` | same; the ONVIF server stays up so profiles remain readable |
| Daemon hangs | `POS` probe times out | after 3 checks it is terminated and restarted |
| Camera link drops after login | `POS` returns `ERR` | after 3 probes the daemon is restarted and logs in again |
| ONVIF port busy | startup log warning | server attempts to bind and fails, container restarts |
| Bad SOAP / unknown operation | SOAP fault | request fails; server stays up |
| Camera moves out of range | no detection | open-loop moves have no position feedback |

---

## Security model

- **ONVIF authentication** — WS-Security `UsernameToken`; `PasswordText` or
  `PasswordDigest` (`base64(SHA1(nonce + created + password))`). Comparison is
  constant-time (`crypto/subtle`). Missing/invalid credentials return
  `ter:NotAuthorized` as a spec-shaped SOAP fault. Only WS-Security is
  implemented, so device capabilities advertise `HttpDigest="false"` rather
  than claiming it (unlike the old Python bridge).
- **Transport** — plain HTTP inside the LAN; there is no TLS listener. Put the
  bridge on a trusted network or behind a reverse proxy if that matters.
- **Container** — runs as uid 10001 with no home and a nologin shell; the Go
  bridge and `ptz_control` are the only application processes.
- **UNIX socket** — `chmod 0666` inside the container, so any process in the
  container can control PTZ; there are no other processes by design.
- **Secrets** — camera credentials and ONVIF credentials come from the
  environment. `.env` is git-ignored and excluded from the Docker build
  context. The camera password is passed as `argv` to the daemon, so it is
  visible in `/proc/<pid>/cmdline` **inside** the container (not from other
  containers).
- **Exposure** — the image bundles the vendor HCNetSDK binaries. Check
  Hikvision's SDK terms before publishing a public image or package.

---

## Deployment topologies

### Compose, one service per camera

```mermaid
flowchart LR
    subgraph HOST["Docker host"]
        subgraph NET["compose network"]
            FR["Frigate"]
            P1["ptz-camera1<br/>ONVIF_PORT=8081"]
            P2["ptz-camera2<br/>ONVIF_PORT=8082"]
        end
    end
    C1[("C8C")]:::device
    C2[("TY1")]:::device

    FR -->|"onvif.host=ptz-camera1<br/>port 8081"| P1
    FR -->|"onvif.host=ptz-camera2<br/>port 8082"| P2
    P1 --> C1
    P2 --> C2
    classDef device fill:#fff3e0,stroke:#ef6c00
```

- Default: publish `${ONVIF_PORT}:${ONVIF_PORT}` and point Frigate at the
  Docker host.
- Same compose network: drop `ports`, use the service name and the
  in-container port.
- Host networking: `network_mode: host`, `onvif.host: 127.0.0.1`.
- macOS/Windows: Docker Desktop emulates `linux/amd64` (the vendor SDK is
  x86-64); PTZ traffic is tiny, so the overhead is irrelevant.

### Prebuilt image

GitHub Actions publishes `ghcr.io/mabbas007/ezviz-ptz` (tags `latest`, `main`,
`vX.Y.Z`, `sha-…`). See [README.md](README.md#prebuilt-image-github-container-registry).

### ZoneMinder control module

A second, independent consumer of `ptz_control` — no Go bridge or container
involved:

```mermaid
flowchart LR
    ZM["ZoneMinder<br/>HikVisionExternal.pm"]
    D["ptz_control serve<br/>(one per monitor)<br/>socket /tmp/ptz_ID_HOST_PORT.sock"]
    ZM -->|"open(): nohup ptz_control serve"| D
    ZM <-->|"system(): ptz_control CMD START/STOP<br/>over the monitor's socket"| D
    D -->|"tcp/8000"| C[("camera")]
```

- `ControlAddress` must be `user:password@host:port`.
- `open()` starts a detached daemon with one socket per monitor, `close()`
  sends `SIGTERM`.
- `sendPTZCommand()` invokes the binary's client mode; `moveStop` reuses the
  last direction.

---

## Build, test and release pipeline

```mermaid
flowchart LR
    PR["Pull request"] --> GO["Go build · vet · unit tests<br/>gofmt + go test -race"]
    MAIN["Push main"] --> GO
    MAN["Manual dispatch from main"] --> GO
    PR --> TEST["Integration test<br/>fake HCNetSDK + onvif-zeep-async 4.3.0"]
    MAIN --> TEST
    MAN --> TEST
    GO --> BUILD["buildx<br/>linux/amd64<br/>MAJOR.MINOR tags"]
    TEST -->|"all checks pass, main only"| BUILD
    BUILD --> GHCR[("ghcr.io/mabbas007/ezviz-ptz")]
```

- **Image build** — multi-stage: `gcc:12-bookworm` compiles `ptz_control`
  against `HCNetSDKCom/`, `golang:1.23-bookworm` builds a static bridge binary,
  and `debian:12-slim` is the runtime, with the binary and vendor libs at
  `/app` so `$ORIGIN/HCNetSDKCom/lib` resolves.
- **Go checks** — `go vet ./...` and `go test -race ./...` run on every pull
  request; `gofmt -l .` must be empty.
- **Tests** — `frigate-onvif/tests/run.sh` validates the compose file, builds a
  fake HCNetSDK (`LD_PRELOAD`), starts the real image, waits for health, and
  drives it with Frigate's pinned ONVIF client. Covered: discovery via
  `GetServices`, capabilities, profiles, spaces, `MoveStatus`, relative and
  continuous moves, zoom axis, the empty preset list, `AbsoluteMove` fault and
  auth failure.
- **Publish** — only after the Go and integration jobs pass and only from
  `main`; uses `GITHUB_TOKEN` with `packages: write`. The major version comes
  from the root `VERSION` file and the minor is the first-parent commit count
  since `VERSION` changed, so image tags are `MAJOR.MINOR`, `MAJOR`, `latest`,
  `main` and `sha-...`. After a successful push it creates a GitHub Release
  tagged `vMAJOR.MINOR` with generated notes (idempotent on re-runs).

---

## Design decisions and trade-offs

| Decision | Why | Cost |
| --- | --- | --- |
| Drive the camera with HCNetSDK and supply ONVIF ourselves | The cameras have no ONVIF service at all (no HTTP port), and the SDK has no relative-move primitive | Linux x86-64 only; vendor blobs; proprietary protocol |
| One camera per container/process | Frigate's `onvif.host`/`onvif.port` cannot carry a path; isolation of failures and credentials | N ports and N processes for N cameras |
| Emulate relative moves with timed start/stop | The SDK has no relative-move primitive | Open-loop, tuning-dependent, pan and tilt sequential |
| Advertise no absolute or zoom position spaces | Prevents Frigate from using operations the bridge cannot honor | No absolute positioning; Frigate disables zoom autotracking |
| Report no presets instead of storing them | The camera rejects every SDK preset operation (`NET_DVR_NETWORK_ERRORDATA`, all 300 slots) and exposes no ISAPI endpoint, so no implementation can work | Frigate's `return_preset` is unusable; the camera does not return home after tracking |
| Go standard library HTTP server | Single static binary, no third-party Go dependencies, small image | No HTTP/2, TLS or WSDL serving; one SOAP op per POST |
| No WS-Discovery | Frigate uses explicit host/port; discovery adds UDP/broadcast complexity | Discovery-based tools cannot auto-find the bridge |
| SDK-derived media profile metadata, URL composed from config | Frigate gets the real codec/resolution/FPS/bitrate from the SDK when available and ffprobes the returned URL; no HTTP/ONVIF endpoint is needed on the camera | Firmware that rejects the config read falls back to `VIDEO_*` |
| Health = supervisor state | Makes broken camera links visible in `docker ps` | Brief `200` while the daemon is still logging in |

---

## Limitations and roadmap

**Known limitations**

- Single camera per process; no profile-token routing across cameras.
- No absolute PTZ, no zoom position spaces, no relative zoom.
- `GetStatus` position is fixed at `0,0`; no closed-loop correction.
- No WS-Discovery, events, imaging, recordings, snapshots or proxied streaming
  (`GetStreamUri` points clients at the camera's RTSP server).
- Plain HTTP/WS-Security only; no TLS.
- Relative move accuracy depends on manual calibration of
  `PAN_SECONDS_PER_FOV` / `TILT_SECONDS_PER_FOV`.
- `linux/amd64` only; macOS and arm64 hosts need emulation.

**Possible next steps**

1. **Multi-camera mode** — one container exposing one profile per camera;
   Frigate can select a profile with `onvif.profile`. Requires a camera
   registry, per-camera daemon/socket, and token routing in `onvif.py`.
2. **ISAPI backend** — plain HTTP PTZ (`/ISAPI/PTZCtrl/...`) for cameras that
   expose it; would run natively on macOS and arm64. The C8C and TY1 have no
   HTTP port open by default.
3. **WS-Discovery responder** — lets GUI tools and ONVIF scanners find the
   bridge.
4. **Closed-loop moves** — use `POS` plus calibration to correct overshoot.
5. **TLS and per-camera ONVIF credentials** for stricter deployments.

---

## Source map

| Path | Purpose |
| --- | --- |
| `ptz_control.cpp` | SDK daemon and command-line client |
| `HikVisionExternal.pm` | ZoneMinder control module |
| `HCNetSDKCom/` | Vendor headers and shared libraries (Linux x86-64) |
| `frigate-onvif-go/internal/config` | Environment configuration and validation |
| `frigate-onvif-go/cmd/bridge` | Entry point, healthcheck subcommand, signals |
| `frigate-onvif-go/internal/server` | HTTP server, routing, health, limits |
| `frigate-onvif-go/internal/onvif` | ONVIF/SOAP implementation |
| `frigate-onvif-go/internal/ptz` | Movement state machine |
| `frigate-onvif-go/internal/sdk` | Daemon client and supervisor |
| `frigate-onvif-go/internal/logging` | `LOG_LEVEL` setup |
| `frigate-onvif/Dockerfile` | Multi-stage amd64 image |
| `frigate-onvif/tests/` | Fake SDK, image test, Frigate-style client |
| `frigate-onvif/tests/fake_hcnetsdk.c` | `LD_PRELOAD` stub of the SDK |
| `docker-compose.yml` | Single-camera deployment |
| `frigate-onvif/docker-compose.example.yml` | Multi-camera / merge example |
| `.env.example` | Documented configuration template |
| `.github/workflows/docker-publish.yml` | Test, build and GHCR publishing |
