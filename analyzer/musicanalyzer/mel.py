"""Numpy re-implementation of Essentia's TensorflowInputMusiCNN front-end.

Matches essentia/src/algorithms/spectral/tensorflowinputmusicnn.cpp:
  FrameCutter(frameSize=512, hopSize=256, zero-centred first frame)
  -> Windowing(type=hann, symmetric, normalized=False)   (zeroPhase only rotates, |FFT| unchanged)
  -> Spectrum (|rfft|, 257 bins)
  -> MelBands(96, sr=16000, 0..8000 Hz, slaneyMel warping, linear weighting,
              normalize=unit_tri, type=power  <- MelBands default, squares the magnitude)
  -> log10(1 + 10000 * x)

Used by both EffnetDiscogs (patch 128 / hop 62) and MusiCNN (patch 187 / hop 93).
Pure numpy so it cannot hit illegal-instruction crashes on CPUs without AVX.
"""
from __future__ import annotations

import math

import numpy as np

SAMPLE_RATE = 16000
FRAME_SIZE = 512
HOP_SIZE = 256
N_BANDS = 96


def _hz2mel_slaney(hz: float) -> float:
    min_log_hz, lin_slope = 1000.0, 3 / 200.0
    if hz < min_log_hz:
        return hz * lin_slope
    return min_log_hz * lin_slope + math.log(hz / min_log_hz) / (math.log(6.4) / 27.0)


def _mel2hz_slaney(mel: float) -> float:
    min_log_hz, lin_slope = 1000.0, 3 / 200.0
    min_log_mel = min_log_hz * lin_slope
    if mel < min_log_mel:
        return mel / lin_slope
    return min_log_hz * math.exp((mel - min_log_mel) * (math.log(6.4) / 27.0))


def _filterbank() -> np.ndarray:
    spectrum_size = FRAME_SIZE // 2 + 1
    low, high = _hz2mel_slaney(0.0), _hz2mel_slaney(SAMPLE_RATE / 2.0)
    step = (high - low) / (N_BANDS + 1)
    # Essentia accumulates melFreq += step (float32 "Real"); reproduce in float32.
    freqs = []
    mel = np.float32(low)
    for _ in range(N_BANDS + 2):
        freqs.append(_mel2hz_slaney(float(mel)))
        mel = np.float32(mel + np.float32(step))
    scale = (SAMPLE_RATE / 2.0) / (spectrum_size - 1)
    fb = np.zeros((N_BANDS, spectrum_size), dtype=np.float64)
    for i in range(N_BANDS):
        f0, f1, f2 = freqs[i], freqs[i + 1], freqs[i + 2]
        step1, step2 = f1 - f0, f2 - f1
        jbegin, jend = math.ceil(f0 / scale), math.floor(f2 / scale)
        for j in range(jbegin, jend + 1):
            b = j * scale
            fb[i, j] = (b - f0) / step1 if b < f1 else (f2 - b) / step2
        fb[i] /= (step1 + step2) / 2.0  # unit_tri
    return fb.astype(np.float32)


_FB = _filterbank()
_WINDOW = (0.5 - 0.5 * np.cos(2.0 * np.pi * np.arange(FRAME_SIZE) / (FRAME_SIZE - 1.0))).astype(np.float32)


def frame_count(n_samples: int) -> int:
    return 1 + math.ceil((n_samples - FRAME_SIZE / 2.0) / HOP_SIZE)


def melspectrogram(audio: np.ndarray) -> np.ndarray:
    """audio: mono float32 at 16 kHz -> (n_frames, 96) float32."""
    audio = np.asarray(audio, dtype=np.float32)
    n_frames = max(frame_count(len(audio)), 1)
    half = FRAME_SIZE // 2
    padded = np.zeros(half + (n_frames - 1) * HOP_SIZE + FRAME_SIZE, dtype=np.float32)
    padded[half:half + len(audio)] = audio
    idx = np.arange(FRAME_SIZE)[None, :] + HOP_SIZE * np.arange(n_frames)[:, None]
    frames = padded[idx] * _WINDOW
    power = np.abs(np.fft.rfft(frames, axis=1)).astype(np.float32) ** 2
    bands = power @ _FB.T
    return np.log10(1.0 + 10000.0 * bands).astype(np.float32)


def patches(mel: np.ndarray, size: int, hop: int) -> np.ndarray:
    """Essentia lastPatchMode='discard'. Returns (n, size, 96); empty if too short."""
    n = mel.shape[0]
    if n < size:
        return np.zeros((0, size, mel.shape[1]), dtype=np.float32)
    count = 1 + (n - size) // hop
    idx = np.arange(size)[None, :] + hop * np.arange(count)[:, None]
    return mel[idx]
