"""Audio decoding via ffmpeg: N windows of W seconds, mono 16 kHz float32.

Matches Essentia's MonoLoader: sinc resampling (ffmpeg soxr) and downmix by *averaging*
all channels in numpy. ffmpeg's own `-ac 1` downmix scales stereo by ~sqrt(2), which
shifts the log-mel input (cosine vs. the Essentia reference drops from 0.99994 to
0.992), so it is deliberately not used.

The server passes duration and channel count from its database, which saves one
ffprobe per track; ffprobe is only the fallback when they are unknown.
"""
from __future__ import annotations

import json
import os
import subprocess
from dataclasses import dataclass

import numpy as np

SR = 16000


class DecodeError(Exception):
    """The file exists but cannot be decoded (permanent for this file version)."""


@dataclass
class AudioInfo:
    duration: float
    channels: int


def probe(path: str) -> AudioInfo:
    proc = subprocess.run(
        ["ffprobe", "-v", "error", "-select_streams", "a:0",
         "-show_entries", "stream=channels:format=duration", "-of", "json", "file:" + path],
        capture_output=True, text=True, timeout=60,
    )
    if proc.returncode != 0:
        raise DecodeError(f"ffprobe failed: {proc.stderr.strip()[:200]}")
    try:
        data = json.loads(proc.stdout)
        info = AudioInfo(float(data["format"]["duration"]), int(data["streams"][0]["channels"]))
    except (KeyError, IndexError, ValueError, TypeError) as exc:
        raise DecodeError("no audio stream or duration") from exc
    if info.channels <= 0 or info.duration <= 0:
        raise DecodeError("no audio stream or duration")
    return info


def resolve_info(path: str, duration_ms: int | None, channels: int | None) -> AudioInfo:
    if duration_ms and duration_ms > 0 and channels and channels > 0:
        return AudioInfo(duration_ms / 1000.0, int(channels))
    return probe(path)


def decode(path: str, start: float, length: float, channels: int) -> np.ndarray:
    cmd = [
        "ffmpeg", "-nostdin", "-v", "error",
        "-ss", f"{max(start, 0.0):.3f}", "-t", f"{length:.3f}",
        "-protocol_whitelist", "file", "-i", "file:" + path,
        "-map", "0:a:0", "-vn", "-af", f"aresample={SR}:resampler=soxr",
        "-f", "f32le", "pipe:1",
    ]
    proc = subprocess.run(cmd, capture_output=True, timeout=180)
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


def safe_join(root: str, relative: str) -> str:
    """Resolves `relative` under `root`; rejects absolute paths, traversal and symlinks
    escaping the root. Raises ValueError for invalid input, FileNotFoundError if missing."""
    if not relative or "\x00" in relative or os.path.isabs(relative):
        raise ValueError("invalid path")
    root_real = os.path.realpath(root)
    target = os.path.realpath(os.path.join(root_real, relative))
    if os.path.commonpath([root_real, target]) != root_real or target == root_real:
        raise ValueError("path escapes the music directory")
    if not os.path.isfile(target):
        raise FileNotFoundError(relative)
    return target
