"""Unit tests for the analyzer service (stdlib unittest; numpy is the only dependency).

Run: cd analyzer && python -m unittest discover -s tests -v

Integration tests that need the ONNX models and ffmpeg run only when
ANALYZER_TEST_MODELS (models dir) and ANALYZER_TEST_AUDIO (an audio file) are set;
ANALYZER_TEST_EFFNET_REF additionally checks parity against Essentia's official
regression activations (effnetdiscogs.activations.npy for techno_loop.wav).
"""
from __future__ import annotations

import base64
import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

import numpy as np

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from musicanalyzer import audio, regression  # noqa: E402
from musicanalyzer.engine import HEAD_KEYS, f16b64  # noqa: E402
from musicanalyzer.server import Settings, State, serve  # noqa: E402


class SafeJoinTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.outside = tempfile.mkdtemp()
        os.makedirs(os.path.join(self.root, "a b"))
        with open(os.path.join(self.root, "a b", "x.flac"), "wb") as fh:
            fh.write(b"x")
        with open(os.path.join(self.outside, "secret.flac"), "wb") as fh:
            fh.write(b"x")
        os.symlink(os.path.join(self.outside, "secret.flac"), os.path.join(self.root, "link.flac"))

    def tearDown(self):
        shutil.rmtree(self.root)
        shutil.rmtree(self.outside)

    def test_resolves_inside_root(self):
        self.assertTrue(audio.safe_join(self.root, "a b/x.flac").endswith("x.flac"))

    def test_rejects_traversal_absolute_and_escaping_symlink(self):
        for bad in ("../x.flac", "a b/../../etc/passwd", "/etc/passwd", "", "a\x00b", "link.flac", "."):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                audio.safe_join(self.root, bad)

    def test_missing_file(self):
        with self.assertRaises(FileNotFoundError):
            audio.safe_join(self.root, "a b/missing.flac")

    def test_window_starts(self):
        self.assertEqual(audio.window_starts(60.0, 3, 30.0), [(0.0, 60.0)])
        starts = audio.window_starts(240.0, 3, 30.0)
        self.assertEqual([round(s) for s, _ in starts], [45, 105, 165])


def synthetic_samples(n: int, informative_embedding: bool, seed: int = 0) -> list[dict]:
    rng = np.random.default_rng(seed)
    z = rng.normal(size=(n, 16))
    if informative_embedding:
        emb = z @ rng.normal(size=(16, 1280)) * 0.05 + rng.normal(scale=0.02, size=(n, 1280))
        heads = rng.random(size=(n, len(HEAD_KEYS)))
    else:
        emb = rng.normal(size=(n, 1280))
        heads = np.clip(0.5 + z[:, :len(HEAD_KEYS)] * 0.15, 0, 1)
    arousal = 5 + z[:, 0] * 0.8 + rng.normal(scale=0.25, size=n)
    valence = 5 + z[:, 1] * 0.7 + rng.normal(scale=0.25, size=n)
    return [{"embedding": f16b64(emb[i]), "heads": dict(zip(HEAD_KEYS, map(float, heads[i]))),
             "valence": float(valence[i]), "arousal": float(arousal[i])} for i in range(n)]


def apply_model(model: dict, sample: dict) -> np.ndarray:
    w = np.frombuffer(base64.b64decode(model["weights"]), dtype="<f4").reshape(model["dims"], 2)
    if model["kind"] == "embedding":
        x = np.frombuffer(base64.b64decode(sample["embedding"]), dtype="<f2").astype(np.float64)
    else:
        x = np.array([sample["heads"][k] for k in model["features"]])
    return x @ w + np.array(model["bias"])


class RegressionTest(unittest.TestCase):
    def test_embedding_model_recovers_signal_and_weights_are_folded(self):
        samples = synthetic_samples(240, informative_embedding=True)
        model = regression.fit(samples)
        self.assertEqual(model["kind"], "embedding")
        self.assertGreater(model["metrics"]["embedding"]["arousalR"], 0.8)
        self.assertGreater(model["metrics"]["embedding"]["valenceR"], 0.8)
        pred = np.array([apply_model(model, s) for s in samples])
        truth = np.array([[s["valence"], s["arousal"]] for s in samples])
        self.assertGreater(np.corrcoef(pred[:, 1], truth[:, 1])[0, 1], 0.9)
        self.assertLess(abs(pred[:, 1].mean() - truth[:, 1].mean()), 0.1)

    def test_falls_back_to_heads_when_embedding_is_uninformative(self):
        model = regression.fit(synthetic_samples(240, informative_embedding=False, seed=1))
        self.assertEqual(model["kind"], "heads")
        self.assertEqual(model["features"], HEAD_KEYS)
        self.assertEqual(model["dims"], len(HEAD_KEYS))

    def test_rejects_too_few_or_invalid_samples(self):
        with self.assertRaises(regression.FitError):
            regression.fit(synthetic_samples(10, True))
        bad = synthetic_samples(60, True)
        bad[3]["embedding"] = base64.b64encode(b"\x00\x01").decode()
        with self.assertRaises(regression.FitError):
            regression.fit(bad)


class FakeEngine:
    full_available = False

    def __init__(self):
        self.calls = []
        self.fail: Exception | None = None
        self.gate: threading.Event | None = None

    def labels(self):
        return {"genre": ["g"], "moodtheme": ["m"], "instrument": ["i"], "heads": HEAD_KEYS}

    def analyze(self, path, duration_ms, channels, mode):
        self.calls.append((path, duration_ms, channels, mode))
        if self.gate:
            self.gate.wait(5)
        if self.fail:
            raise self.fail
        return {"analysisVersion": "test", "embedding": "", "timings": {"total": 0.01}}


class ServerTest(unittest.TestCase):
    def setUp(self):
        self.music = tempfile.mkdtemp()
        with open(os.path.join(self.music, "song.flac"), "wb") as fh:
            fh.write(b"x")
        settings = Settings({"ANALYZER_MUSIC_DIR": self.music, "ANALYZER_LISTEN": "127.0.0.1:0", "ANALYZER_TOKEN": "t0ken"})
        self.state = State(settings)
        self.engine = FakeEngine()
        self.httpd = serve(settings, self.state, load=False)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()

    def tearDown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        shutil.rmtree(self.music)

    def call(self, method, path, body=None, token="t0ken"):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if token:
            req.add_header("Authorization", "Bearer " + token)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, json.loads(resp.read()), resp.headers
        except urllib.error.HTTPError as err:
            return err.code, json.loads(err.read()), err.headers

    def test_health_is_public_and_reports_loading(self):
        status, body, _ = self.call("GET", "/health", token=None)
        self.assertEqual((status, body["status"]), (200, "loading"))
        self.assertNotIn("t0ken", json.dumps(body))

    def test_auth_required(self):
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "song.flac"}, token=None)[0], 401)
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "song.flac"}, token="wrong")[0], 401)
        self.assertEqual(self.call("GET", "/v1/labels", token="wrong")[0], 401)

    def test_loading_returns_503_with_retry_after(self):
        status, body, headers = self.call("POST", "/v1/analyze", {"path": "song.flac"})
        self.assertEqual((status, body["error"]["code"]), (503, "loading"))
        self.assertEqual(headers["Retry-After"], "30")

    def test_validation_and_errors(self):
        self.state.engine = self.engine
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "../etc/passwd"})[1]["error"]["code"], "invalid_path")
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "nope.flac"})[0], 404)
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "song.flac", "mode": "x"})[0], 400)
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "song.flac", "durationMs": -1})[0], 400)
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "song.flac", "mode": "full"})[1]["error"]["code"], "full_mode_unavailable")
        self.engine.fail = audio.DecodeError("broken")
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "song.flac"})[0], 422)
        self.engine.fail = RuntimeError("boom")
        self.assertEqual(self.call("POST", "/v1/analyze", {"path": "song.flac"})[0], 500)
        self.assertEqual(self.call("POST", "/v1/unknown", {})[0], 404)

    def test_analyze_ok_passes_hints(self):
        self.state.engine = self.engine
        status, body, _ = self.call("POST", "/v1/analyze", {"path": "song.flac", "durationMs": 1000, "channels": 2})
        self.assertEqual(status, 200)
        self.assertEqual(self.engine.calls[0][1:], (1000, 2, "fast"))
        self.assertEqual(self.call("GET", "/v1/labels")[1]["labels"]["heads"], HEAD_KEYS)

    def test_busy_returns_503(self):
        self.state.engine = self.engine
        self.engine.gate = threading.Event()
        first = threading.Thread(target=self.call, args=("POST", "/v1/analyze", {"path": "song.flac"}))
        first.start()
        for _ in range(100):
            if self.engine.calls:
                break
            threading.Event().wait(0.02)
        status, body, headers = self.call("POST", "/v1/analyze", {"path": "song.flac"})
        self.engine.gate.set()
        first.join(5)
        self.assertEqual((status, body["error"]["code"], headers["Retry-After"]), (503, "busy", "2"))

    def test_fit_route(self):
        status, body, _ = self.call("POST", "/v1/av/fit", {"samples": synthetic_samples(10, True)})
        self.assertEqual((status, body["error"]["code"]), (422, "fit_failed"))
        status, body, _ = self.call("POST", "/v1/av/fit", {"samples": synthetic_samples(120, True)})
        self.assertEqual(status, 200)
        self.assertIn(body["kind"], ("embedding", "heads"))


@unittest.skipUnless(os.environ.get("ANALYZER_TEST_MODELS") and os.environ.get("ANALYZER_TEST_AUDIO") and shutil.which("ffmpeg"),
                     "set ANALYZER_TEST_MODELS and ANALYZER_TEST_AUDIO (and install ffmpeg) to run")
class EngineIntegrationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        from musicanalyzer.backend import Runner
        from musicanalyzer.engine import Engine
        from musicanalyzer.models import ensure_models, onnx_path

        models = os.environ["ANALYZER_TEST_MODELS"]
        specs = ensure_models(models)
        cls.engine = Engine(Runner("onnxruntime", 2), specs, {k: onnx_path(models, s) for k, s in specs.items()})

    def decode(self, value, dims):
        arr = np.frombuffer(base64.b64decode(value), dtype="<f2")
        self.assertEqual(arr.shape, (dims,))
        self.assertTrue(np.all(np.isfinite(arr)))
        return arr

    def test_fast_and_full(self):
        path = os.environ["ANALYZER_TEST_AUDIO"]
        fast = self.engine.analyze(path, None, None, "fast")
        genre = self.decode(fast["genre"], 400)
        self.decode(fast["moodtheme"], 56)
        self.decode(fast["instrument"], 40)
        self.decode(fast["embedding"], 1280)
        self.assertEqual(sorted(fast["heads"]), sorted(HEAD_KEYS))
        self.assertNotIn("arousal", fast)
        ref = os.environ.get("ANALYZER_TEST_EFFNET_REF")
        if ref:
            expected = np.load(ref)
            cos = float(genre.astype(np.float64) @ expected / np.linalg.norm(genre) / np.linalg.norm(expected))
            self.assertGreater(cos, 0.999)
        if self.engine.full_available:
            full = self.engine.analyze(path, None, None, "full")
            self.assertTrue(1 <= full["arousal"] <= 9 and 1 <= full["valence"] <= 9)


if __name__ == "__main__":
    unittest.main()
