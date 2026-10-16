#!/usr/bin/env python3
"""032 Music Server · 声学分析 P0 验证工具

只读音乐目录，不修改 032 的任何数据。输出写入 --out 目录：
  results.jsonl   每首曲目的原始结果（可断点续跑）
  results.csv     便于用表格软件抽查（UTF-8 BOM）
  similarity.md   声学相似度抽查
  summary.md/json 硬件、后端、耗时统计与全库耗时预估
"""
from __future__ import annotations

import argparse
import csv
import json
import os
import random
import re
import resource
import statistics
import sys
import time
import traceback

import numpy as np

from analyzer import audio, mapping
from analyzer.backend import cpu_info, select_backend
from analyzer.mel import melspectrogram, patches
from analyzer.models import ensure_models, onnx_path

AUDIO_EXTS = {".flac", ".mp3", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".ape", ".wv", ".alac", ".aiff", ".aif", ".dsf", ".dff", ".tta", ".mka"}
EFFNET_PATCH, EFFNET_HOP = 128, 62
MUSICNN_PATCH, MUSICNN_HOP = 187, 93
BATCH = 16


def log(msg: str) -> None:
    print(time.strftime("%H:%M:%S ") + msg, file=sys.stderr, flush=True)


def scan(root: str, include: str | None) -> list[str]:
    pattern = re.compile(include) if include else None
    found = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if not d.startswith(".") and not d.startswith("@")]
        for name in filenames:
            if os.path.splitext(name)[1].lower() in AUDIO_EXTS and not name.startswith("._"):
                rel = os.path.relpath(os.path.join(dirpath, name), root)
                if pattern is None or pattern.search(rel):
                    found.append(rel)
    found.sort()
    return found


def batched(runner, key: str, x: np.ndarray) -> list[np.ndarray]:
    outs: list[list[np.ndarray]] = []
    for i in range(0, len(x), BATCH):
        outs.append(runner.run(key, x[i:i + BATCH]))
    return [np.concatenate([o[k] for o in outs], axis=0) for k in range(len(outs[0]))]


def pick(outputs: list[np.ndarray], dim: int) -> np.ndarray:
    for o in outputs:
        if o.shape[-1] == dim:
            return o
    raise ValueError(f"no output with dim {dim}")


def binary(prob: np.ndarray, classes: list[str], positive: str) -> float:
    return float(prob[classes.index(positive)])


def analyze(path: str, rel: str, args, runner, specs) -> dict:
    t0 = time.perf_counter()
    c0 = os.times()
    rec: dict = {"path": rel}
    info = audio.probe(path)
    duration = info.duration
    rec["duration_s"] = round(duration, 1)

    td = time.perf_counter()
    segments = [audio.decode(path, s, l, info.channels) for s, l in audio.window_starts(duration, args.windows, args.window_sec)]
    segments = [s for s in segments if len(s) >= 16000 * 3]
    if not segments:
        raise audio.DecodeError("decoded audio too short")
    rec["decode_s"] = round(time.perf_counter() - td, 3)

    tm = time.perf_counter()
    mels = [melspectrogram(s) for s in segments]
    eff_in = np.concatenate([patches(m, EFFNET_PATCH, EFFNET_HOP) for m in mels], axis=0)
    if len(eff_in) == 0:
        raise audio.DecodeError("audio shorter than one 2s patch")
    rec["mel_s"] = round(time.perf_counter() - tm, 3)
    rec["patches"] = int(len(eff_in))

    te = time.perf_counter()
    eff_out = batched(runner, "effnet", eff_in)
    genre = pick(eff_out, 400)
    emb = pick(eff_out, 1280)
    rec["effnet_s"] = round(time.perf_counter() - te, 3)

    th = time.perf_counter()
    head_mean: dict[str, np.ndarray] = {}
    for key, spec in specs.items():
        if spec.backbone == "effnet":
            head_mean[key] = runner.run(key, emb.astype(np.float32))[0].mean(axis=0)
    rec["heads_s"] = round(time.perf_counter() - th, 3)

    arousal = valence = None
    if "musicnn" in specs and "deam" in specs and not args.no_musicnn:
        tn = time.perf_counter()
        # musicnn costs ~5x effnet per patch; by default only the middle window feeds it.
        mn_mels = mels if args.musicnn_all_windows else [mels[len(mels) // 2]]
        mn_in = np.concatenate([patches(m, MUSICNN_PATCH, MUSICNN_HOP) for m in mn_mels], axis=0)
        if len(mn_in):
            mn_emb = pick(batched(runner, "musicnn", mn_in), 200)
            av = runner.run("deam", mn_emb.astype(np.float32))[0].mean(axis=0)
            cls = specs["deam"].classes
            valence, arousal = float(av[cls.index("valence")]), float(av[cls.index("arousal")])
        rec["musicnn_s"] = round(time.perf_counter() - tn, 3)

    genre_mean = genre.mean(axis=0)
    g_classes = specs["effnet"].classes
    mt_classes = specs["moodtheme"].classes
    in_classes = specs["instrument"].classes
    moodtheme = {c: float(p) for c, p in zip(mt_classes, head_mean["moodtheme"])}
    instruments = {c: float(p) for c, p in zip(in_classes, head_mean["instrument"])}
    heads = {
        "voice": binary(head_mean["voice"], specs["voice"].classes, "voice"),
        "danceable": binary(head_mean["danceability"], specs["danceability"].classes, "danceable"),
        "happy": binary(head_mean["happy"], specs["happy"].classes, "happy"),
        "sad": binary(head_mean["sad"], specs["sad"].classes, "sad"),
        "relaxed": binary(head_mean["relaxed"], specs["relaxed"].classes, "relaxed"),
        "aggressive": binary(head_mean["aggressive"], specs["aggressive"].classes, "aggressive"),
        "party": binary(head_mean["party"], specs["party"].classes, "party"),
    }
    for opt in ("engagement", "approachability"):
        if opt in head_mean:
            heads[opt] = float(head_mean[opt][0])

    rec["top_discogs"] = [[g_classes[i], round(float(genre_mean[i]), 3)] for i in np.argsort(-genre_mean)[:5]]
    rec["top_moodtheme"] = [[c, round(p, 3)] for c, p in sorted(moodtheme.items(), key=lambda kv: -kv[1])[:5]]
    rec["top_instruments"] = [[c, round(p, 3)] for c, p in sorted(instruments.items(), key=lambda kv: -kv[1])[:3]]
    rec["heads"] = {k: round(v, 3) for k, v in heads.items()}
    rec["valence"] = None if valence is None else round(valence, 2)
    rec["arousal"] = None if arousal is None else round(arousal, 2)
    rec["styles"] = [[k, round(v, 2)] for k, v in mapping.styles_from(genre_mean, g_classes, instruments, heads["voice"])]
    rec["energy"] = mapping.energy_level(arousal, heads)
    rec["mood_raw"] = {k: round(v, 4) for k, v in mapping.raw_mood_scores(moodtheme, heads, arousal).items()}
    rec["embedding"] = emb.mean(axis=0).astype(np.float16).tolist()

    c1 = os.times()
    rec["wall_s"] = round(time.perf_counter() - t0, 3)
    rec["cpu_s"] = round((c1.user - c0.user) + (c1.system - c0.system), 3)
    rec["ffmpeg_cpu_s"] = round((c1.children_user - c0.children_user) + (c1.children_system - c0.children_system), 3)
    return rec


def load_done(path: str) -> dict[str, dict]:
    done: dict[str, dict] = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                try:
                    rec = json.loads(line)
                    done[rec["path"]] = rec
                except (ValueError, KeyError):
                    continue
    return done


def fmt_hours(seconds: float) -> str:
    return f"{seconds / 3600:.1f} 小时"


def write_reports(args, records: list[dict], sysinfo: dict) -> None:
    ok = [r for r in records if not r.get("error")]
    failed = [r for r in records if r.get("error")]
    moods = mapping.assign_moods([r["mood_raw"] for r in ok])
    for r, m in zip(ok, moods):
        r["moods"] = m

    def label_list(pairs, labels):
        return " / ".join(f"{labels.get(k, k)} {v:.2f}" for k, v in pairs)

    csv_path = os.path.join(args.out, "results.csv")
    with open(csv_path, "w", encoding="utf-8-sig", newline="") as fh:
        w = csv.writer(fh)
        w.writerow(["路径", "时长(秒)", "推断情绪", "推断风格", "能量1-5", "人声概率", "效价(1-9)", "唤醒度(1-9)",
                    "Discogs风格Top5", "MoodTheme Top5", "乐器Top3", "happy", "sad", "relaxed", "aggressive", "party",
                    "danceable", "耗时(秒)", "解码", "梅尔", "effnet", "分类头", "musicnn", "错误"])
        for r in records:
            if r.get("error"):
                w.writerow([r["path"]] + [""] * 22 + [r["error"]])
                continue
            h = r["heads"]
            w.writerow([
                r["path"], r["duration_s"], label_list(r["moods"], mapping.MOOD_LABELS),
                label_list(r["styles"], mapping.STYLE_LABELS), r["energy"], h["voice"], r["valence"], r["arousal"],
                "; ".join(f"{c} {p}" for c, p in r["top_discogs"]),
                "; ".join(f"{c} {p}" for c, p in r["top_moodtheme"]),
                "; ".join(f"{c} {p}" for c, p in r["top_instruments"]),
                h["happy"], h["sad"], h["relaxed"], h["aggressive"], h["party"], h["danceable"],
                r["wall_s"], r["decode_s"], r["mel_s"], r["effnet_s"], r["heads_s"], r.get("musicnn_s", ""), "",
            ])

    # Similarity spot-check
    sim_lines = ["# 声学相似度抽查", "", "每个查询列出最相似的 10 首（**已排除同一目录/专辑**，避免只看到同专辑曲目）。",
                 "相似度是 effnet 1280 维 embedding 的余弦相似度，只代表\"听起来像\"，没有使用任何元数据。", ""]
    if len(ok) >= 3:
        emb = np.array([r["embedding"] for r in ok], dtype=np.float32)
        emb -= emb.mean(axis=0, keepdims=True)
        emb /= np.linalg.norm(emb, axis=1, keepdims=True) + 1e-9
        rng = random.Random(args.seed + 1)
        queries = list(range(len(ok)))
        if args.similar_include:
            pat = re.compile(args.similar_include)
            queries = [i for i in queries if pat.search(ok[i]["path"])] or queries
        queries = rng.sample(queries, min(args.similar, len(queries)))
        for qi in queries:
            q = ok[qi]
            sims = emb @ emb[qi]
            qdir = os.path.dirname(q["path"])
            order = [j for j in np.argsort(-sims) if j != qi and os.path.dirname(ok[j]["path"]) != qdir][:10]
            sim_lines += [f"## {q['path']}",
                          f"情绪：{label_list(q['moods'], mapping.MOOD_LABELS)}　风格：{label_list(q['styles'], mapping.STYLE_LABELS)}　能量：{q['energy']}", "",
                          "| # | 相似度 | 曲目 | 情绪 | 风格 |", "|---|---|---|---|---|"]
            for n, j in enumerate(order, 1):
                o = ok[j]
                sim_lines.append(f"| {n} | {sims[j]:.3f} | {o['path']} | {label_list(o['moods'][:2], mapping.MOOD_LABELS)} | {label_list(o['styles'][:2], mapping.STYLE_LABELS)} |")
            sim_lines.append("")
    with open(os.path.join(args.out, "similarity.md"), "w", encoding="utf-8") as fh:
        fh.write("\n".join(sim_lines))

    # Summary
    def stats(key):
        vals = [r[key] for r in ok if r.get(key) is not None]
        if not vals:
            return None
        vals.sort()
        return {"mean": round(statistics.fmean(vals), 3), "median": round(statistics.median(vals), 3),
                "p95": round(vals[min(len(vals) - 1, int(len(vals) * 0.95))], 3), "max": round(vals[-1], 3)}

    timing = {k: stats(k) for k in ("wall_s", "cpu_s", "ffmpeg_cpu_s", "decode_s", "mel_s", "effnet_s", "heads_s", "musicnn_s")}
    mood_hist: dict[str, int] = {}
    style_hist: dict[str, int] = {}
    for r in ok:
        if r["moods"]:
            mood_hist[r["moods"][0][0]] = mood_hist.get(r["moods"][0][0], 0) + 1
        if r["styles"]:
            style_hist[r["styles"][0][0]] = style_hist.get(r["styles"][0][0], 0) + 1
    mean_wall = timing["wall_s"]["mean"] if timing["wall_s"] else 0
    projection = args.total_tracks * mean_wall
    summary = {
        "system": sysinfo, "args": {k: v for k, v in vars(args).items()},
        "tracks": {"analyzed": len(ok), "failed": len(failed)},
        "timing": timing, "projection_seconds_single_process": round(projection),
        "peak_rss_mb": round(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024, 1),
        "primary_mood_histogram": mood_hist, "primary_style_histogram": style_hist,
    }
    with open(os.path.join(args.out, "summary.json"), "w", encoding="utf-8") as fh:
        json.dump(summary, fh, ensure_ascii=False, indent=2)

    cpu = sysinfo["cpu"]
    flags = ", ".join(f"{k}={'是' if v else '否'}" for k, v in cpu.get("flags", {}).items())
    md = ["# 声学分析 P0 验证报告", "",
          "## 环境",
          f"- CPU：{cpu.get('model')}（{cpu.get('cores')} 线程可见）；指令集：{flags}",
          f"- 推理后端：{sysinfo['backend']}",
          "- 后端探测：" + "；".join(f"{a['backend']}/{a['device']} {'成功' if a['ok'] else '失败（' + a['reason'] + '）'}" for a in sysinfo["probes"]),
          f"- 参数：每首 {args.windows} 段 × {args.window_sec} 秒，推理线程 {args.threads}，nice {args.nice}，musicnn/DEAM {'关闭' if args.no_musicnn else ('开启（全部窗口）' if args.musicnn_all_windows else '开启（仅中间一段）')}",
          f"- 峰值内存：{summary['peak_rss_mb']} MB",
          "", "## 结果",
          f"- 成功 {len(ok)} 首，失败 {len(failed)} 首",
          ]
    if timing["wall_s"]:
        t = timing
        md += [f"- 每首耗时：平均 {t['wall_s']['mean']} 秒，中位数 {t['wall_s']['median']} 秒，P95 {t['wall_s']['p95']} 秒，最大 {t['wall_s']['max']} 秒",
               "- 分项平均：" + "，".join(f"{name} {t[key]['mean']} 秒" for key, name in
                                       (("decode_s", "解码"), ("mel_s", "梅尔"), ("effnet_s", "effnet"), ("heads_s", "分类头"), ("musicnn_s", "musicnn")) if t.get(key)),
               f"- **全库预估**（{args.total_tracks} 首，单进程 {args.threads} 线程）：约 {fmt_hours(projection)}",
               ]
        if t.get("musicnn_s"):
            without = args.total_tracks * (t["wall_s"]["mean"] - t["musicnn_s"]["mean"])
            md.append(f"- 若关闭 musicnn/DEAM（能量改用 effnet 分类头估算）：约 {fmt_hours(without)}")
    md += ["", "## 主情绪分布（临时映射，仅供参考）"]
    md += [f"- {mapping.MOOD_LABELS.get(k, k)}：{v}" for k, v in sorted(mood_hist.items(), key=lambda kv: -kv[1])]
    md += ["", "## 主风格分布（临时映射，仅供参考）"]
    md += [f"- {mapping.STYLE_LABELS.get(k, k)}：{v}" for k, v in sorted(style_hist.items(), key=lambda kv: -kv[1])]
    if failed:
        md += ["", "## 失败样本（前 20 个）"] + [f"- {r['path']}：{r['error']}" for r in failed[:20]]
    md += ["", "## 请抽查", "1. `results.csv`：情绪/风格/能量/人声是否符合你的听感（ACG、纯音乐尤其重要）",
           "2. `similarity.md`：\"最相似 10 首\"听起来是否真的像", "3. 把本报告、`summary.json`、`results.csv`、`similarity.md` 发回即可（`results.jsonl` 含 embedding，体积较大，可不发）"]
    with open(os.path.join(args.out, "summary.md"), "w", encoding="utf-8") as fh:
        fh.write("\n".join(md) + "\n")
    log(f"[done] 报告已写入 {args.out}/summary.md")


def main() -> int:
    ap = argparse.ArgumentParser(description="032 Music Server 声学分析 P0 验证")
    ap.add_argument("--music", default="/music", help="音乐目录（只读）")
    ap.add_argument("--out", default="/out", help="输出目录")
    ap.add_argument("--models", default="/models", help="模型缓存目录")
    ap.add_argument("--sample", type=int, default=200, help="随机抽取曲目数")
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--include", help="只在相对路径匹配该正则的曲目中抽样")
    ap.add_argument("--paths-file", help="额外必测的曲目清单（每行一个相对路径），会加在随机样本之外")
    ap.add_argument("--threads", type=int, default=2, help="推理线程数")
    ap.add_argument("--windows", type=int, default=3, help="每首分析的窗口数")
    ap.add_argument("--window-sec", type=float, default=30.0, help="每个窗口的秒数")
    ap.add_argument("--backend", default="auto", choices=["auto", "onnxruntime", "openvino", "openvino-gpu"])
    ap.add_argument("--no-musicnn", action="store_true", help="跳过 musicnn/DEAM（唤醒度/效价）")
    ap.add_argument("--musicnn-all-windows", action="store_true", help="musicnn 分析全部窗口（默认只分析中间一段，节省约 2/3 开销）")
    ap.add_argument("--similar", type=int, default=12, help="相似度抽查的查询曲目数")
    ap.add_argument("--similar-include", help="相似度查询只从匹配该正则的曲目中挑选")
    ap.add_argument("--total-tracks", type=int, default=17000, help="用于全库耗时预估的曲目总数")
    ap.add_argument("--nice", type=int, default=19, help="进程优先级（越大越低）")
    ap.add_argument("--fresh", action="store_true", help="忽略已有结果重新开始")
    args = ap.parse_args()

    if args.nice:
        try:
            os.nice(args.nice)
        except OSError:
            pass
    os.makedirs(args.out, exist_ok=True)
    cpu = cpu_info()
    log(f"[cpu] {cpu.get('model')} flags={cpu.get('flags')}")

    specs = ensure_models(args.models, include_optional=True)
    runner, probes = select_backend(onnx_path(args.models, specs["effnet"]), args.backend, args.threads, log)
    for key, spec in specs.items():
        if key in ("musicnn", "deam") and args.no_musicnn:
            continue
        runner.load(key, onnx_path(args.models, spec))
    log(f"[backend] using {runner.describe()}")

    log(f"[scan] {args.music}")
    all_files = scan(args.music, args.include)
    log(f"[scan] found {len(all_files)} audio files")
    if not all_files:
        log("没有找到音频文件，请检查 --music 挂载")
        return 2
    targets = random.Random(args.seed).sample(all_files, min(args.sample, len(all_files)))
    if args.paths_file:
        with open(args.paths_file, encoding="utf-8") as fh:
            extra = [ln.strip().lstrip("/") for ln in fh if ln.strip() and not ln.startswith("#")]
        targets += [p for p in extra if p not in targets]

    jsonl = os.path.join(args.out, "results.jsonl")
    if args.fresh and os.path.exists(jsonl):
        os.remove(jsonl)
    done = load_done(jsonl)
    records = [done[p] for p in targets if p in done]
    todo = [p for p in targets if p not in done]
    log(f"[run] {len(targets)} tracks, {len(records)} already done, {len(todo)} to analyze")

    started = time.perf_counter()
    with open(jsonl, "a", encoding="utf-8") as out:
        for n, rel in enumerate(todo, 1):
            try:
                rec = analyze(os.path.join(args.music, rel), rel, args, runner, specs)
            except Exception as exc:  # noqa: BLE001
                rec = {"path": rel, "error": f"{type(exc).__name__}: {exc}"[:300]}
                if not isinstance(exc, audio.DecodeError):
                    traceback.print_exc()
            out.write(json.dumps(rec, ensure_ascii=False) + "\n")
            out.flush()
            records.append(rec)
            elapsed = time.perf_counter() - started
            eta = elapsed / n * (len(todo) - n)
            status = f"{rec['wall_s']:.1f}s" if "wall_s" in rec else "ERROR " + rec["error"][:80]
            log(f"[{n}/{len(todo)}] {status} | ETA {eta / 60:.1f} min | {rel}")

    sysinfo = {"cpu": cpu, "backend": runner.describe(), "probes": probes}
    write_reports(args, records, sysinfo)
    return 0


if __name__ == "__main__":
    sys.exit(main())
