"""Model registry.

All models are the official MTG Essentia ONNX exports (https://essentia.upf.edu/models/),
licensed CC BY-NC-SA 4.0 (non-commercial use only). They are downloaded at runtime into
the models volume and are never committed to the repository or baked into the image.
"""
from __future__ import annotations

import json
import logging
import os
import urllib.request
from dataclasses import dataclass, field

BASE = "https://essentia.upf.edu/models"
PROXY_SECRET = "/run/secrets/proxy_url"
log = logging.getLogger("musicanalyzer.models")


@dataclass
class ModelSpec:
    key: str
    path: str  # relative to BASE, without extension
    backbone: str  # "effnet" | "musicnn" | "" (a backbone itself)
    full_only: bool = False  # only needed for mode=full (calibration)
    classes: list[str] = field(default_factory=list)

    @property
    def filename(self) -> str:
        return os.path.basename(self.path)


def specs() -> list[ModelSpec]:
    return [
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
        ModelSpec("engagement", "classification-heads/engagement/engagement_regression-discogs-effnet-1", "effnet"),
        ModelSpec("approachability", "classification-heads/approachability/approachability_regression-discogs-effnet-1", "effnet"),
        ModelSpec("musicnn", "feature-extractors/musicnn/msd-musicnn-1", "", full_only=True),
        ModelSpec("deam", "classification-heads/deam/deam-msd-musicnn-2", "musicnn", full_only=True),
    ]


def _opener() -> urllib.request.OpenerDirector:
    """Proxy from a mounted secret file (may carry credentials), else urllib's default
    HTTP(S)_PROXY / NO_PROXY handling. The proxy address is never logged."""
    url = ""
    if os.path.isfile(PROXY_SECRET):
        with open(PROXY_SECRET, encoding="utf-8") as fh:
            url = fh.readline().strip()
    if url:
        return urllib.request.build_opener(urllib.request.ProxyHandler({"http": url, "https": url}))
    return urllib.request.build_opener()


def _download(opener: urllib.request.OpenerDirector, url: str, dest: str) -> None:
    tmp = dest + ".part"
    req = urllib.request.Request(url, headers={"User-Agent": "032music-analyzer"})
    with opener.open(req, timeout=120) as resp, open(tmp, "wb") as fh:
        while True:
            chunk = resp.read(1 << 20)
            if not chunk:
                break
            fh.write(chunk)
    os.replace(tmp, dest)


def onnx_path(models_dir: str, spec: ModelSpec) -> str:
    return os.path.join(models_dir, spec.filename + ".onnx")


def ensure_models(models_dir: str) -> dict[str, ModelSpec]:
    """Downloads missing models. Full-mode-only models are best effort: without them the
    service still works, only calibration (mode=full) becomes unavailable."""
    os.makedirs(models_dir, exist_ok=True)
    opener = _opener()
    ready: dict[str, ModelSpec] = {}
    for spec in specs():
        onnx = onnx_path(models_dir, spec)
        meta = os.path.join(models_dir, spec.filename + ".json")
        try:
            for ext, dest in (("onnx", onnx), ("json", meta)):
                if not os.path.exists(dest) or os.path.getsize(dest) == 0:
                    url = f"{BASE}/{spec.path}.{ext}"
                    log.info("downloading model %s", url)
                    _download(opener, url, dest)
            with open(meta, encoding="utf-8") as fh:
                spec.classes = list(json.load(fh).get("classes", []))
            ready[spec.key] = spec
        except Exception as exc:  # noqa: BLE001
            if spec.full_only:
                log.warning("optional model %s unavailable: %s", spec.key, exc)
                continue
            raise
    return ready
