#!/usr/bin/env python3
"""验证能否用 effnet 输出代替 musicnn/DEAM 预测唤醒度/效价（arousal/valence）。

背景：P0 实测 musicnn 占每首耗时约 63%，只为得到 arousal/valence。本脚本读取主测试的
results.jsonl（同时含 effnet embedding 和 DEAM 输出），用岭回归 + K 折交叉验证比较：
  A. effnet 1280 维 embedding -> arousal/valence
  B. 仅用 effnet 分类头（voice/danceable/happy/sad/relaxed/aggressive/party/engagement/approachability）
只读输入，纯 numpy，几秒内完成。用法：
  python distill_av.py /out/results.jsonl [--out /out/distill_report.md]
"""
from __future__ import annotations

import argparse
import json
import sys

import numpy as np

HEAD_KEYS = ["voice", "danceable", "happy", "sad", "relaxed", "aggressive", "party", "engagement", "approachability"]
# 维度（1280）远大于样本数（约 200）时需要很强的正则，网格覆盖到 1e5
ALPHAS = [float(a) for a in np.logspace(-1, 5, 13)]


def energy_from_arousal(a: np.ndarray, danceable: np.ndarray) -> np.ndarray:
    # 与 analyzer/mapping.energy_level 一致
    base = 0.75 * (a - 1.0) / 8.0 + 0.25 * danceable
    return np.clip(np.round(base * 4 + 1), 1, 5)


def ridge_fit(x: np.ndarray, y: np.ndarray, alpha: float):
    mu, sd = x.mean(0), x.std(0) + 1e-6
    xs = (x - mu) / sd
    ym = y.mean(0)
    n, p = xs.shape
    if p > n:
        # 对偶形式：w = Xᵀ (X Xᵀ + αI)⁻¹ y，只解 n×n 的方程
        w = xs.T @ np.linalg.solve(xs @ xs.T + alpha * np.eye(n), y - ym)
    else:
        w = np.linalg.solve(xs.T @ xs + alpha * np.eye(p), xs.T @ (y - ym))
    return lambda z: ((z - mu) / sd) @ w + ym


def cv_predict(x: np.ndarray, y: np.ndarray, alpha: float, folds: int, seed: int) -> np.ndarray:
    idx = np.random.default_rng(seed).permutation(len(x))
    pred = np.zeros_like(y)
    for k in range(folds):
        test = idx[k::folds]
        train = np.setdiff1d(idx, test)
        pred[test] = ridge_fit(x[train], y[train], alpha)(x[test])
    return pred


def pearson(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.corrcoef(a, b)[0, 1])


def evaluate(name: str, x: np.ndarray, y: np.ndarray, danceable: np.ndarray, folds: int) -> dict:
    # 先用内层 CV 选 alpha（按两个目标的平均相关系数），再用不同随机种子做外层评估
    def score(a: float) -> float:
        p = cv_predict(x, y, a, folds, 1)
        return float(np.mean([pearson(p[:, j], y[:, j]) for j in range(2)]))

    best = max(ALPHAS, key=score)
    pred = cv_predict(x, y, best, folds, 2)
    true_e = energy_from_arousal(y[:, 1], danceable)
    pred_e = energy_from_arousal(pred[:, 1], danceable)
    return {
        "name": name, "alpha": best, "n": len(x), "dims": x.shape[1],
        "valence_r": round(pearson(pred[:, 0], y[:, 0]), 3),
        "arousal_r": round(pearson(pred[:, 1], y[:, 1]), 3),
        "valence_mae": round(float(np.abs(pred[:, 0] - y[:, 0]).mean()), 3),
        "arousal_mae": round(float(np.abs(pred[:, 1] - y[:, 1]).mean()), 3),
        "energy_exact": round(float((pred_e == true_e).mean()), 3),
        "energy_within1": round(float((np.abs(pred_e - true_e) <= 1).mean()), 3),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("jsonl")
    ap.add_argument("--out", help="报告输出路径（Markdown）")
    ap.add_argument("--folds", type=int, default=5)
    args = ap.parse_args()

    rows = []
    with open(args.jsonl, encoding="utf-8") as fh:
        for line in fh:
            try:
                r = json.loads(line)
            except ValueError:
                continue
            if r.get("error") or r.get("arousal") is None or not r.get("embedding"):
                continue
            rows.append(r)
    if len(rows) < max(20, args.folds * 4):
        print(f"可用样本太少：{len(rows)}（需要同时有 embedding 和 DEAM 输出，即开启 musicnn 的主测试结果）", file=sys.stderr)
        return 2

    y = np.array([[r["valence"], r["arousal"]] for r in rows], dtype=np.float64)
    emb = np.array([r["embedding"] for r in rows], dtype=np.float64)
    heads = np.array([[r["heads"].get(k, 0.0) for k in HEAD_KEYS] for r in rows], dtype=np.float64)
    danceable = heads[:, HEAD_KEYS.index("danceable")]

    results = [
        evaluate("A. effnet embedding（1280 维）", emb, y, danceable, args.folds),
        evaluate("B. 仅分类头（9 维）", heads, y, danceable, args.folds),
        evaluate("C. embedding + 分类头", np.hstack([emb, heads]), y, danceable, args.folds),
    ]
    spread = {"valence_std": round(float(y[:, 0].std()), 3), "arousal_std": round(float(y[:, 1].std()), 3)}

    lines = ["# 用 effnet 代替 musicnn 预测唤醒度/效价：验证报告", "",
             f"- 样本：{len(rows)} 首（{args.folds} 折交叉验证，结果都是没参与训练的曲目上的表现）",
             f"- DEAM 原始分布：效价标准差 {spread['valence_std']}，唤醒度标准差 {spread['arousal_std']}（量程 1–9）", "",
             "| 方案 | 维度 | alpha | 唤醒度 r | 唤醒度 MAE | 效价 r | 效价 MAE | 能量档完全一致 | 能量档相差≤1 |",
             "|---|---|---|---|---|---|---|---|---|"]
    for r in results:
        lines.append(f"| {r['name']} | {r['dims']} | {r['alpha']:.3g} | {r['arousal_r']} | {r['arousal_mae']} | "
                     f"{r['valence_r']} | {r['valence_mae']} | {r['energy_exact']:.0%} | {r['energy_within1']:.0%} |")
    lines += ["", "判读参考：r ≥ 0.85 可以直接代替 musicnn；0.7–0.85 可用于能量分档，但象限边界附近会有误差；< 0.7 不建议代替。",
              "能量档是 1–5 档，「相差≤1」表示排序时基本不会出错。"]
    report = "\n".join(lines) + "\n"
    print(report)
    print(json.dumps({"results": results, "spread": spread}, ensure_ascii=False))
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
