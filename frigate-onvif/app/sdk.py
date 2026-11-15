"""Client for the ``ptz_control`` daemon and a supervisor that keeps it alive.

The daemon is a small C++ program that logs into the camera with the Hikvision
SDK once and then accepts newline-delimited commands on a UNIX socket:

    PING                        -> PONG
    POS                         -> POS <pan> <tilt> <zoom> | ERR <code>
    LEFT START [speed]          -> OK | ERR <code>
    LEFT STOP                   -> OK | ERR <code>

The camera rejects PTZ preset operations (``NET_DVR_PTZPreset_Other``) with
``NET_DVR_NETWORK_ERRORDATA``, so the bridge does not expose presets at all.
"""

from __future__ import annotations

import logging
import os
import socket
import subprocess
import threading
import time

from .config import Config

logger = logging.getLogger(__name__)

DIRECTIONS = ("LEFT", "RIGHT", "UP", "DOWN", "ZOOM_IN", "ZOOM_OUT")


class SdkError(RuntimeError):
    """Raised when the PTZ daemon is unreachable or rejected a command."""


class SdkClient:
    def __init__(self, socket_path: str, timeout: float = 5.0) -> None:
        self.socket_path = socket_path
        self.timeout = timeout
        self._lock = threading.Lock()

    def _call(self, message: str) -> str:
        payload = (message + "\n").encode("ascii", "replace")
        with self._lock:
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            sock.settimeout(self.timeout)
            try:
                sock.connect(self.socket_path)
                sock.sendall(payload)
                reply = b""
                while b"\n" not in reply:
                    chunk = sock.recv(256)
                    if not chunk:
                        break
                    reply += chunk
            except OSError as exc:
                raise SdkError(f"{message}: {exc}") from exc
            finally:
                sock.close()

        text = reply.decode("ascii", "replace").strip()
        if not text:
            raise SdkError(f"{message}: empty reply from PTZ daemon")
        if text.startswith("ERR"):
            raise SdkError(f"{message}: {text}")
        return text

    def ping(self) -> bool:
        try:
            return self._call("PING") == "PONG"
        except SdkError:
            return False

    def command(self, direction: str, action: str, speed: int | None = None) -> str:
        if direction not in DIRECTIONS:
            raise ValueError(f"unknown PTZ direction {direction!r}")
        message = f"{direction} {action}"
        if speed is not None and action != "STOP":
            message += f" {int(speed)}"
        return self._call(message)


class PtzDaemon:
    """Starts ``ptz_control serve`` and restarts it if it dies or hangs."""

    def __init__(self, cfg: Config, client: SdkClient) -> None:
        self.cfg = cfg
        self.client = client
        self._proc: subprocess.Popen[str] | None = None
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._failures = 0
        self._backoff = 1.0

    # -- public API ---------------------------------------------------------
    def start(self) -> None:
        self._thread = threading.Thread(
            target=self._supervise, name="ptz-daemon", daemon=True
        )
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        self._terminate()

    @property
    def running(self) -> bool:
        return self._proc is not None and self._proc.poll() is None

    def status(self) -> dict[str, object]:
        proc = self._proc
        return {
            "running": self.running,
            "pid": proc.pid if proc else None,
            "socket": self.cfg.ptz_socket,
        }

    # -- internals ----------------------------------------------------------
    def _supervise(self) -> None:
        while not self._stop.is_set():
            if self.running:
                if not self._healthy():
                    logger.warning(
                        "PTZ daemon is not answering, restarting it "
                        "(check the camera connection)"
                    )
                    self._terminate()
            else:
                if self._spawn():
                    self._failures = 0
                    self._backoff = 1.0
                else:
                    self._failures += 1
                    self._backoff = min(self._backoff * 2, 60.0)
                    logger.error(
                        "PTZ daemon did not start (attempt %d). Retrying in %.0fs. "
                        "If the log above says 'Login failed 1', CAMERA_USER / "
                        "CAMERA_PASSWORD are wrong (use the camera's verification "
                        "code). 'Login failed 7' means the camera is unreachable on "
                        "CAMERA_HOST:CAMERA_PORT.",
                        self._failures,
                        self._backoff,
                    )
            self._stop.wait(self.cfg.health_interval if self.running else self._backoff)

    def _spawn(self) -> bool:
        args = [
            self.cfg.ptz_binary,
            "serve",
            self.cfg.camera_host,
            str(self.cfg.camera_port),
            self.cfg.camera_user,
            self.cfg.camera_password,
            self.cfg.ptz_socket,
        ]
        try:
            os.unlink(self.cfg.ptz_socket)
        except FileNotFoundError:
            pass
        except OSError as exc:
            logger.warning("Could not remove stale socket %s: %s", self.cfg.ptz_socket, exc)

        logger.info(
            "Starting PTZ daemon for %s:%s (user %s)",
            self.cfg.camera_host,
            self.cfg.camera_port,
            self.cfg.camera_user,
        )
        try:
            proc = subprocess.Popen(
                args,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
            )
        except OSError as exc:
            logger.error("Could not start %s: %s", self.cfg.ptz_binary, exc)
            return False

        self._proc = proc
        if self._stop.is_set():
            self._terminate()
            return False
        threading.Thread(
            target=self._pump_output, args=(proc,), name="ptz-daemon-log", daemon=True
        ).start()

        deadline = time.monotonic() + self.cfg.startup_timeout
        while time.monotonic() < deadline:
            if self._stop.is_set():
                self._terminate()
                return False
            if proc.poll() is not None:
                logger.error(
                    "PTZ daemon exited with code %s during startup", proc.returncode
                )
                return False
            if self.client.ping():
                logger.info("PTZ daemon ready on %s", self.cfg.ptz_socket)
                return True
            time.sleep(0.25)

        logger.error(
            "PTZ daemon did not become ready within %.0fs", self.cfg.startup_timeout
        )
        self._terminate()
        return False

    def _pump_output(self, proc: subprocess.Popen[str]) -> None:
        if proc.stdout is None:
            return
        for line in proc.stdout:
            line = line.rstrip()
            if line:
                logger.info("[ptz_control] %s", line)

    def _healthy(self) -> bool:
        if self.client.ping():
            self._failures = 0
            return True
        self._failures += 1
        return self._failures < 3

    def _terminate(self) -> None:
        proc = self._proc
        self._proc = None
        if proc is None or proc.poll() is not None:
            return
        logger.info("Stopping PTZ daemon (pid %s)", proc.pid)
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
