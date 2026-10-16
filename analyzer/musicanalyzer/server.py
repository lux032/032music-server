"""HTTP service used by 032 Music Server (internal network only).

  GET  /health          status (no auth; used by container health checks)
  GET  /v1/labels       class labels of the stored vectors
  POST /v1/analyze      {"path": "<relative to music dir>", "durationMs": int?, "channels": int?, "mode": "fast"|"full"}
  POST /v1/av/fit       {"samples": [{"embedding", "heads", "valence", "arousal"}], "folds": 5}

One analysis runs at a time (the server drives a single worker); concurrent requests
get 503 + Retry-After. When ANALYZER_TOKEN is set every /v1 route requires
"Authorization: Bearer <token>".
"""
from __future__ import annotations

import hmac
import json
import logging
import os
import sys
import threading
import time
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from . import audio, regression
from .backend import select_backend
from .engine import ANALYSIS_VERSION, Engine
from .models import ensure_models, onnx_path

MAX_BODY = 32 << 20
log = logging.getLogger("musicanalyzer")


class Settings:
    def __init__(self, env: dict[str, str] | None = None):
        env = os.environ if env is None else env
        self.music_dir = env.get("ANALYZER_MUSIC_DIR", "/music")
        self.models_dir = env.get("ANALYZER_MODELS_DIR", "/models")
        self.listen = env.get("ANALYZER_LISTEN", "0.0.0.0:8000")
        self.token = env.get("ANALYZER_TOKEN", "").strip()
        self.threads = max(1, int(env.get("ANALYZER_THREADS", "2") or 2))
        self.backend = env.get("ANALYZER_BACKEND", "auto")


class State:
    """Shared service state; `engine` is set once models are loaded."""

    def __init__(self, settings: Settings):
        self.settings = settings
        self.status = "loading"
        self.error = ""
        self.backend = ""
        self.engine: Engine | None = None
        self.analyze_lock = threading.Lock()
        self.started = time.time()

    def health(self) -> dict:
        return {
            "status": self.status,
            "analysisVersion": ANALYSIS_VERSION,
            "backend": self.backend,
            "fullModeAvailable": bool(self.engine and self.engine.full_available),
            "threads": self.settings.threads,
            "error": self.error,
        }

    def load_forever(self) -> None:
        delay = 60
        while True:
            try:
                specs = ensure_models(self.settings.models_dir)
                runner, _ = select_backend(onnx_path(self.settings.models_dir, specs["effnet"]),
                                           self.settings.backend, self.settings.threads, log.info)
                paths = {k: onnx_path(self.settings.models_dir, s) for k, s in specs.items()}
                self.engine = Engine(runner, specs, paths)
                self.backend = runner.describe()
                self.status, self.error = "ready", ""
                log.info("analyzer ready: %s, full mode %s", self.backend, "available" if self.engine.full_available else "unavailable")
                return
            except Exception as exc:  # noqa: BLE001
                self.status, self.error = "error", str(exc)[:500]
                log.error("model loading failed (retry in %ss): %s", delay, exc)
                time.sleep(delay)
                delay = min(delay * 2, 1800)


def make_handler(state: State):
    class Handler(BaseHTTPRequestHandler):
        server_version = "032-analyzer"
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt: str, *args) -> None:  # route access logs through logging
            log.debug("%s - %s", self.address_string(), fmt % args)

        # ---------- helpers ----------
        def _send(self, status: int, payload: dict, headers: dict[str, str] | None = None) -> None:
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            for k, v in (headers or {}).items():
                self.send_header(k, v)
            self.end_headers()
            self.wfile.write(body)

        def _error(self, status: int, code: str, message: str, retry_after: int | None = None) -> None:
            headers = {"Retry-After": str(retry_after)} if retry_after else None
            self._send(status, {"error": {"code": code, "message": message}}, headers)

        def _authorized(self) -> bool:
            token = state.settings.token
            if not token:
                return True
            header = self.headers.get("Authorization", "")
            supplied = header[7:] if header.startswith("Bearer ") else ""
            return hmac.compare_digest(supplied.encode(), token.encode())

        def _body(self) -> dict | None:
            try:
                length = int(self.headers.get("Content-Length", ""))
            except ValueError:
                self._error(411, "length_required", "Content-Length required")
                return None
            if length < 0 or length > MAX_BODY:
                self._error(413, "too_large", "request body too large")
                return None
            try:
                value = json.loads(self.rfile.read(length) or b"{}")
            except ValueError:
                self._error(400, "invalid_json", "body is not valid JSON")
                return None
            if not isinstance(value, dict):
                self._error(400, "invalid_json", "body must be a JSON object")
                return None
            return value

        def _ready_engine(self) -> Engine | None:
            if state.engine is None:
                self._error(503, "loading" if state.status == "loading" else "unavailable",
                            state.error or "models are loading", retry_after=30)
                return None
            return state.engine

        # ---------- routes ----------
        def do_GET(self) -> None:  # noqa: N802
            if self.path == "/health":
                self._send(200, state.health())
                return
            if not self._authorized():
                self._error(401, "unauthorized", "invalid or missing token")
                return
            if self.path == "/v1/labels":
                engine = self._ready_engine()
                if engine:
                    self._send(200, {"analysisVersion": ANALYSIS_VERSION, "labels": engine.labels()})
                return
            self._error(404, "not_found", "unknown route")

        def do_POST(self) -> None:  # noqa: N802
            if not self._authorized():
                self._error(401, "unauthorized", "invalid or missing token")
                return
            if self.path == "/v1/analyze":
                self._analyze()
            elif self.path == "/v1/av/fit":
                self._fit()
            else:
                self._error(404, "not_found", "unknown route")

        def _analyze(self) -> None:
            body = self._body()
            if body is None:
                return
            mode = body.get("mode", "fast")
            if mode not in ("fast", "full"):
                self._error(400, "invalid_request", "mode must be fast or full")
                return
            duration_ms, channels = body.get("durationMs"), body.get("channels")
            if not all(v is None or (isinstance(v, int) and not isinstance(v, bool) and v >= 0) for v in (duration_ms, channels)):
                self._error(400, "invalid_request", "durationMs and channels must be non-negative integers")
                return
            try:
                path = audio.safe_join(state.settings.music_dir, str(body.get("path", "")))
            except ValueError as exc:
                self._error(400, "invalid_path", str(exc))
                return
            except FileNotFoundError:
                self._error(404, "file_not_found", "file does not exist in the music directory")
                return
            engine = self._ready_engine()
            if engine is None:
                return
            if mode == "full" and not engine.full_available:
                self._error(409, "full_mode_unavailable", "musicnn/DEAM models are not available")
                return
            if not state.analyze_lock.acquire(blocking=False):
                self._error(503, "busy", "another analysis is running", retry_after=2)
                return
            try:
                result = engine.analyze(path, duration_ms, channels, mode)
            except audio.DecodeError as exc:
                self._error(422, "decode_failed", str(exc)[:300])
                return
            except Exception as exc:  # noqa: BLE001
                log.exception("analysis failed: %s", body.get("path"))
                self._error(500, "analysis_failed", str(exc)[:300])
                return
            finally:
                state.analyze_lock.release()
            log.info("analyzed %s mode=%s %.2fs", body.get("path"), mode, result["timings"]["total"])
            self._send(200, result)

        def _fit(self) -> None:
            body = self._body()
            if body is None:
                return
            samples = body.get("samples")
            if not isinstance(samples, list):
                self._error(400, "invalid_request", "samples must be a list")
                return
            try:
                result = regression.fit(samples, int(body.get("folds", 5)))
            except (regression.FitError, ValueError) as exc:
                self._error(422, "fit_failed", str(exc)[:300])
                return
            result["analysisVersion"] = ANALYSIS_VERSION
            log.info("calibration fitted: kind=%s samples=%d metrics=%s", result["kind"], result["samples"], result["metrics"])
            self._send(200, result)

    return Handler


def serve(settings: Settings, state: State | None = None, load: bool = True) -> ThreadingHTTPServer:
    state = state or State(settings)
    if load:
        threading.Thread(target=state.load_forever, name="model-loader", daemon=True).start()
    host, _, port = settings.listen.rpartition(":")
    httpd = ThreadingHTTPServer((host or "0.0.0.0", int(port)), make_handler(state))
    httpd.daemon_threads = True
    return httpd


def main() -> None:
    logging.basicConfig(level=os.environ.get("ANALYZER_LOG_LEVEL", "INFO").upper(),
                        format="%(asctime)s %(levelname)s %(name)s %(message)s", stream=sys.stdout)
    nice = int(os.environ.get("ANALYZER_NICE", "10") or 0)
    if nice:
        try:
            os.nice(nice)
        except OSError:
            pass
    settings = Settings()
    if not os.path.isdir(settings.music_dir):
        log.warning("music directory %s does not exist", settings.music_dir)
    httpd = serve(settings)
    log.info("listening on %s (token %s)", settings.listen, "required" if settings.token else "not set")
    httpd.serve_forever()


if __name__ == "__main__":
    main()
