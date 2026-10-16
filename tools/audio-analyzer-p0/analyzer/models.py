"""Model registry for the P0 benchmark.

All models are the official MTG Essentia ONNX exports (https://essentia.upf.edu/models/),
licensed CC BY-NC-SA 4.0 (non-commercial). They are downloaded at runtime into the
models volume and never committed to the repository or baked into the image.
"""
from __future__ import annotations

import json
import os
import sys
import urllib.request
from dataclasses import dataclass, field

BASE = "https://essentia.upf.edu/models"


@dataclass
class ModelSpec:
    key: str
    path: str  # relative to BASE, without extension
    backbone: str  # "effnet" | "musicnn" | "" (backbone itself)
    optional: bool = False
    classes: list[str] = field(default_factory=list)

    @property
    def onnx_url(self) -> str:
        return f"{BASE}/{self.path}.onnx"

    @property
    def json_url(self) -> str:
        return f"{BASE}/{self.path}.json"

    @property
    def filename(self) -> str:
        return os.path.basename(self.path)


SPECS: list[ModelSpec] = [
    ModelSpec("effnet", "feature-extractors/discogs-effnet/discogs-effnet-bsdynamic-1", ""),
    ModelSpec("moodtheme", "classification-heads/mtg_jamendo_moodtheme/mtg_jamendo_moodtheme-discogs-effnet-1", "effnet"),
    ModelSpec("instrument", "classification-heads/mtg_jamendo_instrument/mtg_jamendo_instrument-discogs-effnet-1", "effnet"),
    ModelSpec("voice", "classification-heads/voice_instrumental/voice_instrumental-discogs-effnet-1", "effnet"),
    ModelSpec("danceability", "classification-heads/danceability/danceability-discogs-effnet-1", "effnet"),
    ModelSpec("happy", "classification-heads/mood_happy/mood_happy-discogs-effnet-1", "effnet"),
    ModelSpec("sad", "classification-heads/mood_sad/mood_sad-discogs-effnet-1", "effnet"),
    ModelSpec("relaxed", "classification-heads/mood_relaxed/mood_relaxed-discogs-effnet-1", "effnet"),
    ModelSpec("aggressive", "classification-heads/mood_aggressive/mood_aggressive-discogs-effnet-1", "effnet"),
    ModelSpec("party", "classification-heads/mood_party/mood_party-discogs-effnet-1", "effnet"),
    ModelSpec("engagement", "classification-heads/engagement/engagement_regression-discogs-effnet-1", "effnet", optional=True),
    ModelSpec("approachability", "classification-heads/approachability/approachability_regression-discogs-effnet-1", "effnet", optional=True),
    ModelSpec("musicnn", "feature-extractors/musicnn/msd-musicnn-1", "", optional=True),
    ModelSpec("deam", "classification-heads/deam/deam-msd-musicnn-2", "musicnn", optional=True),
]


def _download(url: str, dest: str) -> None:
    tmp = dest + ".part"
    req = urllib.request.Request(url, headers={"User-Agent": "032music-audio-analyzer-p0"})
    with urllib.request.urlopen(req, timeout=120) as resp, open(tmp, "wb") as fh:
        while True:
            chunk = resp.read(1 << 20)
            if not chunk:
                break
            fh.write(chunk)
    os.replace(tmp, dest)


def ensure_models(models_dir: str, include_optional: bool = True) -> dict[str, ModelSpec]:
    os.makedirs(models_dir, exist_ok=True)
    ready: dict[str, ModelSpec] = {}
    for spec in SPECS:
        if spec.optional and not include_optional:
            continue
        onnx_path = os.path.join(models_dir, spec.filename + ".onnx")
        json_path = os.path.join(models_dir, spec.filename + ".json")
        try:
            for url, dest in ((spec.onnx_url, onnx_path), (spec.json_url, json_path)):
                if not os.path.exists(dest) or os.path.getsize(dest) == 0:
                    print(f"[models] downloading {url}", file=sys.stderr, flush=True)
                    _download(url, dest)
            with open(json_path, encoding="utf-8") as fh:
                spec.classes = list(json.load(fh).get("classes", []))
            ready[spec.key] = spec
        except Exception as exc:  # noqa: BLE001
            if spec.optional:
                print(f"[models] optional model {spec.key} unavailable: {exc}", file=sys.stderr, flush=True)
                continue
            raise
    return ready


def onnx_path(models_dir: str, spec: ModelSpec) -> str:
    return os.path.join(models_dir, spec.filename + ".onnx")
