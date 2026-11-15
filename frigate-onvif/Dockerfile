# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Stage 1: build the ptz_control daemon against the vendor Hikvision SDK.
# The vendor libraries are x86-64 only, so the whole image is linux/amd64.
# ---------------------------------------------------------------------------
FROM --platform=linux/amd64 gcc:12-bookworm AS build

WORKDIR /src
COPY ptz_control.cpp Makefile ./
COPY HCNetSDKCom ./HCNetSDKCom
RUN make

# ---------------------------------------------------------------------------
# Stage 2: runtime
# ---------------------------------------------------------------------------
FROM --platform=linux/amd64 python:3.12-slim-bookworm

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

# libstdc++6 / libuuid1 are needed by the vendor SDK shared libraries.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libstdc++6 libuuid1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build --chown=10001:10001 /src/ptz_control /app/ptz_control
COPY --from=build --chown=10001:10001 /src/HCNetSDKCom /app/HCNetSDKCom
COPY --chown=10001:10001 frigate-onvif/app /app/app

RUN useradd --uid 10001 --user-group --no-create-home --home-dir /app \
    --shell /usr/sbin/nologin ptz

USER ptz

ENV PTZ_BINARY=/app/ptz_control \
    PTZ_SOCK=/tmp/ptz_control.sock \
    ONVIF_PORT=8000

EXPOSE 8000

# The health endpoint reports 503 until the SDK daemon has logged into the
# camera, which makes `docker ps` show when the camera connection is broken.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["python", "-c", "import os, sys, urllib.request; port = os.environ.get('ONVIF_PORT', '8000'); sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:' + port + '/healthz', timeout=4).status == 200 else 1)"]

ENTRYPOINT ["python", "-m", "app.main"]
