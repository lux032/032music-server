"""Analysis engine: decode -> log-mel -> effnet (+ heads) [-> musicnn/DEAM in full mode].

Output vectors are float16 little-endian, base64 encoded. ANALYSIS_VERSION identifies
everything that changes the stored numbers (models, windowing); the server re-analyses
tracks whose stored version differs.
"""
from __future__ import annotations

import base64
import time

import numpy as np

from . import audio
from .backend import Runner
from .mel import melspectrogram, patches
from .models import ModelSpec

ANALYSIS_VERSION = "effnet-discogs-1/heads-1/win3x30-1"
WINDOWS = 3
WINDOW_SEC = 30.0
EFFNET_PATCH, EFFNET_HOP = 128, 62
MUSICNN_PATCH, MUSICNN_HOP = 187, 93
BATCH = 16

# head key in the response -> (model key, positive class); regression heads use None
HEADS = {
    "voice": ("voice", "voice"),
    "danceable": ("danceability", "danceable"),
    "happy": ("happy", "happy"),
    "sad": ("sad", "sad"),
    "relaxed": ("relaxed", "relaxed"),
    "aggressive": ("aggressive", "aggressive"),
    "party": ("party", "party"),
    "engagement": ("engagement", None),
    "approachability": ("approachability", None),
}
HEAD_KEYS = list(HEADS)


def f16b64(values: np.ndarray) -> str:
    return base64.b64encode(np.asarray(values, dtype="<f2").tobytes()).decode("ascii")


class Engine:
    def __init__(self, runner: Runner, specs: dict[str, ModelSpec], model_paths: dict[str, str]):
        self.runner = runner
        self.specs = specs
        for key, path in model_paths.items():
            runner.load(key, path)
        self.full_available = runner.has("musicnn") and runner.has("deam")

    def labels(self) -> dict[str, list[str]]:
        return {
            "genre": self.specs["effnet"].classes,
            "moodtheme": self.specs["moodtheme"].classes,
            "instrument": self.specs["instrument"].classes,
            "heads": HEAD_KEYS,
        }

    def _batched(self, key: str, x: np.ndarray) -> list[np.ndarray]:
        outs = [self.runner.run(key, x[i:i + BATCH]) for i in range(0, len(x), BATCH)]
        return [np.concatenate([o[k] for o in outs], axis=0) for k in range(len(outs[0]))]

    @staticmethod
    def _pick(outputs: list[np.ndarray], dim: int) -> np.ndarray:
        for o in outputs:
            if o.shape[-1] == dim:
                return o
        raise ValueError(f"model output with dim {dim} not found")

    def analyze(self, path: str, duration_ms: int | None, channels: int | None, mode: str) -> dict:
        if mode == "full" and not self.full_available:
            raise RuntimeError("full mode unavailable (musicnn/DEAM models missing)")
        t0 = time.perf_counter()
        info = audio.resolve_info(path, duration_ms, channels)
        segments = [audio.decode(path, s, l, info.channels) for s, l in audio.window_starts(info.duration, WINDOWS, WINDOW_SEC)]
        segments = [s for s in segments if len(s) >= audio.SR * 3]
        if not segments:
            raise audio.DecodeError("decoded audio shorter than 3 seconds")
        t_decode = time.perf_counter()

        mels = [melspectrogram(s) for s in segments]
        eff_in = np.concatenate([patches(m, EFFNET_PATCH, EFFNET_HOP) for m in mels], axis=0)
        if len(eff_in) == 0:
            raise audio.DecodeError("audio shorter than one analysis patch")
        eff_out = self._batched("effnet", eff_in)
        genre = self._pick(eff_out, 400)
        emb = self._pick(eff_out, 1280).astype(np.float32)
        t_effnet = time.perf_counter()

        means = {}
        for key, spec in self.specs.items():
            if spec.backbone == "effnet":
                means[key] = self.runner.run(key, emb)[0].mean(axis=0)
        heads = {}
        for name, (key, positive) in HEADS.items():
            vec = means[key]
            heads[name] = round(float(vec[self.specs[key].classes.index(positive)] if positive else vec[0]), 4)

        result = {
            "analysisVersion": ANALYSIS_VERSION,
            "mode": mode,
            "durationMs": int(round(info.duration * 1000)),
            "windows": len(segments),
            "patches": int(len(eff_in)),
            "genre": f16b64(genre.mean(axis=0)),
            "moodtheme": f16b64(means["moodtheme"]),
            "instrument": f16b64(means["instrument"]),
            "embedding": f16b64(emb.mean(axis=0)),
            "heads": heads,
        }
        if mode == "full":
            # musicnn costs ~5x effnet per patch; the middle window is enough for calibration targets.
            mid = mels[len(mels) // 2]
            mn_in = patches(mid, MUSICNN_PATCH, MUSICNN_HOP)
            if len(mn_in) == 0:
                mn_in = np.concatenate([patches(m, MUSICNN_PATCH, MUSICNN_HOP) for m in mels], axis=0)
            if len(mn_in):
                mn_emb = self._pick(self._batched("musicnn", mn_in), 200).astype(np.float32)
                av = self.runner.run("deam", mn_emb)[0].mean(axis=0)
                cls = self.specs["deam"].classes
                result["valence"] = round(float(av[cls.index("valence")]), 4)
                result["arousal"] = round(float(av[cls.index("arousal")]), 4)
        t_end = time.perf_counter()
        result["timings"] = {
            "decode": round(t_decode - t0, 3),
            "effnet": round(t_effnet - t_decode, 3),
            "total": round(t_end - t0, 3),
        }
        return result
