"""Inference backends: ONNX Runtime first, OpenVINO CPU as fallback.

Low-end NAS CPUs (e.g. Celeron J4125) have SSE4.2 but no AVX. A wheel that uses AVX
unconditionally dies with SIGILL, which cannot be caught in-process, so every backend
is probed in a child process first. The integrated GPU is deliberately not used: on
the P0 hardware it had extreme latency tails and pushed system load up.
"""
from __future__ import annotations

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
    out = list(ov.Core().compile_model(model, "CPU")(x).values())
assert any(o.shape[-1] == 400 for o in out)
print("ok")
"""


def probe(backend: str, effnet_path: str, timeout: int = 300) -> tuple[bool, str]:
    try:
        proc = subprocess.run([sys.executable, "-c", PROBE_CODE, backend, effnet_path],
                              capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return False, "timeout"
    if proc.returncode == 0 and "ok" in proc.stdout:
        return True, "ok"
    if proc.returncode < 0:
        return False, "SIGILL (unsupported CPU instructions)" if proc.returncode == -4 else f"signal {-proc.returncode}"
    tail = (proc.stderr or proc.stdout).strip().splitlines()[-1:] or ["unknown error"]
    return False, tail[0][:300]


class Runner:
    """Uniform `run(model_key, input) -> list[np.ndarray]` over ONNX Runtime or OpenVINO."""

    def __init__(self, backend: str, threads: int):
        self.backend = backend
        self.threads = threads
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

            compiled = ov.Core().compile_model(path, "CPU", {"INFERENCE_NUM_THREADS": self.threads, "PERFORMANCE_HINT": "LATENCY"})
            self._sessions[key] = lambda x, c=compiled: list(c(x).values())

    def has(self, key: str) -> bool:
        return key in self._sessions

    def run(self, key: str, x: np.ndarray) -> list[np.ndarray]:
        return self._sessions[key](x)

    def describe(self) -> str:
        if self.backend == "onnxruntime":
            import onnxruntime as ort

            return f"onnxruntime {ort.__version__}"
        import openvino as ov

        return f"openvino {ov.__version__}"


def select_backend(effnet_path: str, prefer: str, threads: int, log: Callable[[str], None]) -> tuple[Runner, list[dict]]:
    order = {"auto": ["onnxruntime", "openvino"], "onnxruntime": ["onnxruntime"], "openvino": ["openvino"]}.get(prefer, ["onnxruntime", "openvino"])
    attempts = []
    for backend in order:
        ok, reason = probe(backend, effnet_path)
        attempts.append({"backend": backend, "ok": ok, "reason": reason})
        log(f"backend probe {backend}: {'OK' if ok else 'FAILED - ' + reason}")
        if ok:
            return Runner(backend, threads), attempts
    raise RuntimeError("no usable inference backend: " + "; ".join(f"{a['backend']}: {a['reason']}" for a in attempts))
