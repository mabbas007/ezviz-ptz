#!/bin/sh
# Run the end-to-end test without a real camera.
#
#   ./tests/run.sh            # build the bridge, then run the ONVIF client
#
# Requires Docker (the bridge image is linux/amd64 because the vendor SDK is).
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$here/../.." && pwd)
platform=linux/amd64

bridge=ptz-onvif-test-bridge
client=ptz-onvif-test-client
network=ptz-onvif-test-net
fake_dir=$here/.build

cleanup() {
    docker rm -f "$bridge" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

mkdir -p "$fake_dir"

echo "==> checking docker-compose.yml"
(
    cd "$root"
    CAMERA_HOST=127.0.0.1 CAMERA_PASSWORD=dummy docker compose config >/dev/null
)

echo "==> building fake SDK"
docker run --rm --platform "$platform" -v "$here":/src -w /src gcc:12-bookworm \
    gcc -shared -fPIC -O2 -o /src/.build/libfakesdk.so fake_hcnetsdk.c

echo "==> building bridge image"
docker build --platform "$platform" -t ptz-onvif:test -f "$root/frigate-onvif/Dockerfile" "$root"

echo "==> building test client image"
docker build --platform "$platform" -t ptz-onvif-test-client -f "$here/Dockerfile" "$here"

echo "==> starting bridge"
docker network create "$network" >/dev/null
docker run -d --name "$bridge" --platform "$platform" --network "$network" \
    -e CAMERA_HOST=127.0.0.1 \
    -e CAMERA_PASSWORD=dummy \
    -e ONVIF_USER=onvif \
    -e ONVIF_PASSWORD=secret \
    -e LOG_LEVEL=INFO \
    -e LD_PRELOAD=/fake/libfakesdk.so \
    -v "$fake_dir":/fake:ro \
    ptz-onvif:test >/dev/null

echo "==> waiting for the bridge to become healthy"
i=0
while [ $i -lt 60 ]; do
    status=$(docker inspect -f '{{.State.Health.Status}}' "$bridge" 2>/dev/null || echo starting)
    if [ "$status" = healthy ]; then
        break
    fi
    if [ "$status" = unhealthy ]; then
        echo "bridge is unhealthy, logs:" >&2
        docker logs "$bridge" >&2
        exit 1
    fi
    i=$((i + 1))
    sleep 1
done

if [ "$status" != healthy ]; then
    echo "bridge did not become healthy, logs:" >&2
    docker logs "$bridge" >&2
    exit 1
fi

echo "==> running ONVIF client"
set +e
docker run --rm --platform "$platform" --network "$network" \
    -e ONVIF_HOST="$bridge" \
    -e ONVIF_PORT=8000 \
    -e ONVIF_USER=onvif \
    -e ONVIF_PASSWORD=secret \
    ptz-onvif-test-client
rc=$?
set -e

if [ $rc -ne 0 ]; then
    echo "==> client failed, bridge logs:" >&2
    docker logs "$bridge" >&2
fi
exit $rc
