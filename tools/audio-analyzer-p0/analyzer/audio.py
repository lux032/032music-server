"""Audio decoding via ffmpeg: N windows of W seconds, mono 16 kHz float32.

Matches Essentia's MonoLoader: resample with a sinc resampler (ffmpeg soxr) and
downmix by *averaging* all channels in numpy. ffmpeg's own `-ac 1` downmix scales
stereo by ~sqrt(2), which shifts the log-mel input (cosine vs. Essentia reference
drops from 0.99994 to 0.992), so it is deliberately not used.
"""
from __future__ import annotations

import json
import subprocess
from dataclasses import dataclass

import numpy as np

SR = 16000


class DecodeError(Exception):
    pass


@dataclass
class AudioInfo:
    duration: float
    channels: int


def probe(path: str) -> AudioInfo:
    proc = subprocess.run(
        ["ffprobe", "-v", "error", "-select_streams", "a:0",
         "-show_entries", "stream=channels:format=duration", "-of", "json", path],
        capture_output=True, text=True, timeout=60,
    )
    if proc.returncode != 0:
        raise DecodeError(f"ffprobe failed: {proc.stderr.strip()[:200]}")
    try:
        data = json.loads(proc.stdout)
        duration = float(data["format"]["duration"])
        channels = int(data["streams"][0]["channels"])
    except (KeyError, IndexError, ValueError, TypeError) as exc:
        raise DecodeError("no audio stream or duration") from exc
    if channels <= 0 or duration <= 0:
        raise DecodeError("no audio stream or duration")
    return AudioInfo(duration, channels)


def decode(path: str, start: float, length: float, channels: int) -> np.ndarray:
    cmd = [
        "ffmpeg", "-nostdin", "-v", "error",
        "-ss", f"{max(start, 0.0):.3f}", "-t", f"{length:.3f}", "-i", path,
        "-map", "0:a:0", "-vn", "-af", f"aresample={SR}:resampler=soxr",
        "-f", "f32le", "pipe:1",
    ]
    proc = subprocess.run(cmd, capture_output=True, timeout=120)
    if proc.returncode != 0:
        raise DecodeError(f"ffmpeg failed: {proc.stderr.decode(errors='replace').strip()[:200]}")
    data = np.frombuffer(proc.stdout, dtype=np.float32)
    usable = len(data) - len(data) % channels
    return data[:usable].reshape(-1, channels).mean(axis=1).astype(np.float32)


def window_starts(duration: float, windows: int, length: float) -> list[tuple[float, float]]:
    """Windows centred at 25/50/75% (for 3). Short tracks are analysed whole."""
    if duration <= length * windows or windows <= 0:
        return [(0.0, duration)]
    out = []
    for k in range(windows):
        centre = duration * (k + 1) / (windows + 1)
        start = min(max(centre - length / 2, 0.0), duration - length)
        out.append((start, length))
    return out
