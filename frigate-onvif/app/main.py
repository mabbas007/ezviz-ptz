"""HTTP entry point: runs the SOAP server and supervises the SDK daemon."""

from __future__ import annotations

import json
import logging
import signal
import socket
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from .config import Config
from .onvif import OnvifApp, OnvifFault, fault_envelope
from .ptz import PTZController
from .sdk import PtzDaemon, SdkClient

logger = logging.getLogger("ptz-bridge")

MAX_REQUEST_BYTES = 1024 * 1024


class _Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True
    onvif_app: OnvifApp
    daemon: PtzDaemon
    stopping: threading.Event

    def handle_error(self, request, client_address) -> None:  # type: ignore[no-untyped-def]
        exc = sys.exc_info()[1]
        if isinstance(exc, (ConnectionResetError, BrokenPipeError)):
            return
        logger.exception("Unhandled error while serving %s", client_address)


class Handler(BaseHTTPRequestHandler):
    server: _Server
    protocol_version = "HTTP/1.1"
    server_version = "ptz-onvif-bridge/1.0"

    # -- plumbing -----------------------------------------------------------
    def log_message(self, fmt: str, *args) -> None:  # type: ignore[no-untyped-def]
        logger.debug("%s - %s", self.address_string(), fmt % args)

    def _reply(
        self, code: int, body: bytes, content_type: str = "application/soap+xml"
    ) -> None:
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def _health(self) -> None:
        daemon_ok = self.server.daemon.running
        body = json.dumps(
            {
                "status": "ok" if daemon_ok else "degraded",
                "daemon": self.server.daemon.status(),
                "moving": self.server.onvif_app.ptz.moving,
            }
        ).encode("utf-8")
        self._reply(200 if daemon_ok else 503, body, "application/json")

    # -- HTTP verbs ---------------------------------------------------------
    def do_GET(self) -> None:  # noqa: N802
        if self.path.split("?", 1)[0].rstrip("/") in ("/healthz", "/health"):
            self._health()
            return
        self._reply(404, b"not found", "text/plain; charset=utf-8")

    def do_HEAD(self) -> None:  # noqa: N802
        self.do_GET()

    def do_POST(self) -> None:  # noqa: N802
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        if length <= 0:
            self._reply(411, b"Content-Length required", "text/plain; charset=utf-8")
            return
        if length > MAX_REQUEST_BYTES:
            self._reply(413, b"request too large", "text/plain; charset=utf-8")
            return

        body = self.rfile.read(length)
        try:
            code, payload = self.server.onvif_app.handle(
                self.path, body, self.headers.get("Host")
            )
        except OnvifFault as exc:
            code, payload = 500, fault_envelope(exc.code, exc.message)
        except Exception:  # noqa: BLE001 - never kill the server thread silently
            logger.exception("Internal error handling %s", self.path)
            code, payload = 500, fault_envelope(
                "env:Receiver", "Internal error in the PTZ bridge"
            )
        self._reply(code, payload)


def _free_port_warning(cfg: Config) -> None:
    probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        probe.bind((cfg.binding_host, cfg.onvif_port))
    except OSError as exc:
        logger.warning(
            "Port %s on %s is busy (%s); the server will still try to bind",
            cfg.onvif_port,
            cfg.binding_host,
            exc,
        )
    finally:
        probe.close()


def main() -> int:
    cfg = Config.from_env()
    logging.basicConfig(
        level=getattr(logging, cfg.log_level, logging.INFO),
        format="%(asctime)s %(levelname)-7s %(name)s: %(message)s",
        datefmt="%Y-%m-%dT%H:%M:%S",
    )

    client = SdkClient(cfg.ptz_socket)
    controller = PTZController(client, cfg)
    daemon = PtzDaemon(cfg, client)
    app = OnvifApp(cfg, controller)

    _free_port_warning(cfg)
    server = _Server((cfg.binding_host, cfg.onvif_port), Handler)
    server.onvif_app = app
    server.daemon = daemon
    server.stopping = threading.Event()

    def _shutdown(signum, _frame) -> None:
        if server.stopping.is_set():
            return
        logger.info("Received signal %s, shutting down", signum)
        server.stopping.set()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    daemon.start()

    logger.info(
        "ONVIF PTZ bridge listening on %s:%s (camera %s:%s)",
        cfg.binding_host,
        cfg.onvif_port,
        cfg.camera_host,
        cfg.camera_port,
    )
    try:
        server.serve_forever(poll_interval=0.5)
    finally:
        daemon.stop()
        server.server_close()
        logger.info("Stopped")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
