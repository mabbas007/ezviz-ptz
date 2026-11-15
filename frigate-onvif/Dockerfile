# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Stage 1: build the ptz_control daemon against the vendor Hikvision SDK.
#
# The vendor libraries are x86-64 only, so the image must be built for
# linux/amd64: `docker build --platform linux/amd64 ...`. Compose and CI both
# pass that flag. It is deliberately not pinned in FROM — a constant
# `--platform` value is flagged by the Dockerfile build checks
# (FromPlatformFlagConstDisallowed) and would override the requested target
# platform. The guard below turns a wrong-architecture build into a clear
# error instead of a linker failure.
# ---------------------------------------------------------------------------
FROM gcc:12-bookworm AS daemon-build

ARG TARGETARCH
RUN if [ "$TARGETARCH" != "amd64" ]; then \
        echo "error: the Hikvision SDK is x86-64 only; build with --platform=linux/amd64 (got $TARGETARCH)" >&2; \
        exit 1; \
    fi

WORKDIR /src
COPY ptz_control.cpp Makefile ./
COPY HCNetSDKCom ./HCNetSDKCom
RUN make

# ---------------------------------------------------------------------------
# Stage 2: build the Go ONVIF bridge.
# ---------------------------------------------------------------------------
FROM golang:1.23-bookworm AS bridge-build

WORKDIR /src
COPY frigate-onvif-go/go.mod ./
COPY frigate-onvif-go/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bridge ./cmd/bridge

# ---------------------------------------------------------------------------
# Stage 3: runtime
# ---------------------------------------------------------------------------
FROM debian:12-slim

# libstdc++6 / libuuid1 are needed by the vendor SDK shared libraries.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libstdc++6 libuuid1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=daemon-build --chown=10001:10001 /src/ptz_control /app/ptz_control
COPY --from=daemon-build --chown=10001:10001 /src/HCNetSDKCom /app/HCNetSDKCom
COPY --from=bridge-build --chown=10001:10001 /out/bridge /app/bridge

RUN useradd --uid 10001 --user-group --no-create-home --home-dir /app \
    --shell /usr/sbin/nologin ptz

USER ptz

ENV PTZ_BINARY=/app/ptz_control \
    PTZ_SOCK=/tmp/ptz_control.sock \
    ONVIF_PORT=8000

EXPOSE 8000

# The health endpoint reports 503 until the SDK daemon has logged into the
# camera, which makes `docker ps` show when the camera connection is broken.
# `bridge healthcheck` avoids adding curl to the runtime image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/app/bridge", "healthcheck"]

ENTRYPOINT ["/app/bridge"]
