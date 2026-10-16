"""Calibration: predict DEAM valence/arousal from effnet outputs (ridge regression).

Validated on the P0 NAS run (224 tracks, 5-fold CV): embedding ridge reached
arousal r=0.85 / valence r=0.91; heads-only 0.81 / 0.86. Weights are fitted per
deployment from the library's own calibration tracks (musicnn outputs are CC BY-NC
derived, so no pretrained weights are shipped).

The returned model is purely linear with standardisation folded in:
    [valence, arousal] = bias + x @ weights
so the server can apply it with a dot product.
"""
from __future__ import annotations

import base64

import numpy as np

from .engine import HEAD_KEYS

ALPHAS = [float(a) for a in np.logspace(-1, 5, 13)]
MIN_SAMPLES = 40
EMBEDDING_MIN_AROUSAL_R = 0.75


class FitError(ValueError):
    pass


def _fit(x: np.ndarray, y: np.ndarray, alpha: float) -> tuple[np.ndarray, np.ndarray]:
    mu, sd = x.mean(0), x.std(0) + 1e-6
    xs = (x - mu) / sd
    ym = y.mean(0)
    n, p = xs.shape
    if p > n:  # dual form: only an n x n system
        w = xs.T @ np.linalg.solve(xs @ xs.T + alpha * np.eye(n), y - ym)
    else:
        w = np.linalg.solve(xs.T @ xs + alpha * np.eye(p), xs.T @ (y - ym))
    folded = w / sd[:, None]
    return folded, ym - mu @ folded


def _cv_predict(x: np.ndarray, y: np.ndarray, alpha: float, folds: int, seed: int) -> np.ndarray:
    idx = np.random.default_rng(seed).permutation(len(x))
    pred = np.zeros_like(y)
    for k in range(folds):
        test = idx[k::folds]
        train = np.setdiff1d(idx, test)
        w, b = _fit(x[train], y[train], alpha)
        pred[test] = x[test] @ w + b
    return pred


def _r(a: np.ndarray, b: np.ndarray) -> float:
    if a.std() < 1e-9 or b.std() < 1e-9:
        return 0.0
    return float(np.corrcoef(a, b)[0, 1])


def _evaluate(x: np.ndarray, y: np.ndarray, folds: int) -> dict:
    def score(alpha: float) -> float:
        p = _cv_predict(x, y, alpha, folds, 1)
        return (_r(p[:, 0], y[:, 0]) + _r(p[:, 1], y[:, 1])) / 2

    alpha = max(ALPHAS, key=score)
    pred = _cv_predict(x, y, alpha, folds, 2)
    return {
        "alpha": alpha,
        "valenceR": round(_r(pred[:, 0], y[:, 0]), 4),
        "arousalR": round(_r(pred[:, 1], y[:, 1]), 4),
        "valenceMae": round(float(np.abs(pred[:, 0] - y[:, 0]).mean()), 4),
        "arousalMae": round(float(np.abs(pred[:, 1] - y[:, 1]).mean()), 4),
    }


def _decode_f16(value: str, dims: int) -> np.ndarray:
    raw = base64.b64decode(value, validate=True)
    if len(raw) != dims * 2:
        raise FitError(f"embedding must be {dims} float16 values")
    arr = np.frombuffer(raw, dtype="<f2").astype(np.float64)
    if not np.all(np.isfinite(arr)):
        raise FitError("embedding contains non-finite values")
    return arr


def fit(samples: list[dict], folds: int = 5) -> dict:
    if len(samples) < MIN_SAMPLES:
        raise FitError(f"need at least {MIN_SAMPLES} samples, got {len(samples)}")
    folds = max(2, min(folds, 10))
    try:
        emb = np.stack([_decode_f16(s["embedding"], 1280) for s in samples])
        heads = np.array([[float(s["heads"][k]) for k in HEAD_KEYS] for s in samples])
        y = np.array([[float(s["valence"]), float(s["arousal"])] for s in samples])
    except (KeyError, TypeError, ValueError) as exc:
        raise FitError(f"invalid sample: {exc}") from exc
    if not np.all(np.isfinite(y)) or not np.all(np.isfinite(heads)):
        raise FitError("non-finite targets or heads")

    metrics = {"embedding": _evaluate(emb, y, folds), "heads": _evaluate(heads, y, folds)}
    e, h = metrics["embedding"], metrics["heads"]
    if e["arousalR"] >= EMBEDDING_MIN_AROUSAL_R or (e["valenceR"] + e["arousalR"]) >= (h["valenceR"] + h["arousalR"]):
        kind, x, features = "embedding", emb, None
    else:
        kind, x, features = "heads", heads, HEAD_KEYS
    weights, bias = _fit(x, y, metrics[kind]["alpha"])
    return {
        "kind": kind,
        "features": features,
        "dims": int(x.shape[1]),
        # row-major [dims][2] = (valence, arousal) per input dimension
        "weights": base64.b64encode(weights.astype("<f4").tobytes()).decode("ascii"),
        "bias": [round(float(bias[0]), 6), round(float(bias[1]), 6)],
        "samples": len(samples),
        "folds": folds,
        "metrics": metrics,
    }
