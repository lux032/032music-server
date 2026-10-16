"""Inference backends: ONNX Runtime first, OpenVINO as fallback.

Celeron J4125 (Goldmont Plus) has SSE4.2 but no AVX. A wheel that unconditionally
uses AVX dies with SIGILL, which cannot be caught in-process. Every backend is
therefore probed in a child process first; only a backend whose probe exits 0 is used.
"""
from __future__ import annotations

import os
import subprocess
import sys
from typing import Callable

import numpy as np

PROBE_CODE = r"""
import sys, numpy as np
backend, model = sys.argv[1], sys.argv[2]
x = np.random.default_rng(0).standard_normal((2, 128, 96)).astype(np.float32)
if backend == "onnxruntime":
    import onnxruntime as ort
    s = ort.InferenceSession(model, providers=["CPUExecutionProvider"])
    out = s.run(None, {s.get_inputs()[0].name: x})
else:
    import openvino as ov
    c = ov.Core().compile_model(model, sys.argv[3] if len(sys.argv) > 3 else "CPU")
    out = list(c(x).values())
assert any(o.shape[-1] == 400 for o in out)
print("ok")
"""


def probe(backend: str, effnet_path: str, device: str = "CPU", timeout: int = 300) -> tuple[bool, str]:
    args = [sys.executable, "-c", PROBE_CODE, backend, effnet_path]
    if backend == "openvino":
        args.append(device)
    try:
        proc = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return False, "timeout"
    if proc.returncode == 0 and "ok" in proc.stdout:
        return True, "ok"
    if proc.returncode < 0:
        sig = -proc.returncode
        reason = "SIGILL (CPU 指令集不支持)" if sig == 4 else f"signal {sig}"
        return False, reason
    tail = (proc.stderr or proc.stdout).strip().splitlines()[-1:] or ["unknown error"]
    return False, tail[0][:300]


class Runner:
    """Uniform `run(model_key, input) -> list[np.ndarray]` over ORT or OpenVINO."""

    def __init__(self, backend: str, threads: int, device: str = "CPU"):
        self.backend = backend
        self.threads = threads
        self.device = device
        self._sessions: dict[str, Callable[[np.ndarray], list[np.ndarray]]] = {}

    def load(self, key: str, path: str) -> None:
        if self.backend == "onnxruntime":
            import onnxruntime as ort

            opts = ort.SessionOptions()
            opts.intra_op_num_threads = self.threads
            opts.inter_op_num_threads = 1
            opts.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
            opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
            sess = ort.InferenceSession(path, opts, providers=["CPUExecutionProvider"])
            name = sess.get_inputs()[0].name
            self._sessions[key] = lambda x, s=sess, n=name: s.run(None, {n: x})
        else:
            import openvino as ov

            core = ov.Core()
            config = {}
            if self.device == "CPU":
                config = {"INFERENCE_NUM_THREADS": self.threads, "PERFORMANCE_HINT": "LATENCY"}
            compiled = core.compile_model(path, self.device, config)
            self._sessions[key] = lambda x, c=compiled: list(c(x).values())

    def run(self, key: str, x: np.ndarray) -> list[np.ndarray]:
        return self._sessions[key](x)

    def describe(self) -> str:
        if self.backend == "onnxruntime":
            import onnxruntime as ort

            return f"onnxruntime {ort.__version__} threads={self.threads}"
        import openvino as ov

        return f"openvino {ov.__version__} device={self.device} threads={self.threads}"


def select_backend(effnet_path: str, prefer: str, threads: int, log: Callable[[str], None]) -> tuple[Runner, list[dict]]:
    order = {
        "auto": [("onnxruntime", "CPU"), ("openvino", "CPU")],
        "onnxruntime": [("onnxruntime", "CPU")],
        "openvino": [("openvino", "CPU")],
        "openvino-gpu": [("openvino", "GPU"), ("openvino", "CPU")],
    }[prefer]
    attempts = []
    for backend, device in order:
        ok, reason = probe(backend, effnet_path, device)
        attempts.append({"backend": backend, "device": device, "ok": ok, "reason": reason})
        log(f"[backend] probe {backend}/{device}: {'OK' if ok else 'FAILED - ' + reason}")
        if ok:
            return Runner(backend, threads, device), attempts
    raise RuntimeError("没有可用的推理后端：" + "; ".join(f"{a['backend']}/{a['device']}: {a['reason']}" for a in attempts))


def cpu_info() -> dict:
    info = {"model": "", "flags": {}, "cores": os.cpu_count()}
    try:
        with open("/proc/cpuinfo", encoding="utf-8") as fh:
            text = fh.read()
        for line in text.splitlines():
            if line.startswith("model name") and not info["model"]:
                info["model"] = line.split(":", 1)[1].strip()
            if line.startswith("flags"):
                flags = set(line.split(":", 1)[1].split())
                info["flags"] = {f: f in flags for f in ("sse4_2", "avx", "avx2", "avx512f", "fma")}
                break
    except OSError:
        pass
    return info
