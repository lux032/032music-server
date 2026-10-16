"""Provisional acoustic -> vocabulary mapping (P0 only, to be calibrated).

Mood vocabulary v2 (docs/daily-mix-design.md §2.2.1): 12 theme moods plus internal
negative markers. Lyrics/LLM fusion happens later; P0 only shows what audio alone says.
`nostalgic` and `lonely` are weak acoustically and are mostly decided by lyrics later.
"""
from __future__ import annotations

import math

import numpy as np

MOOD_LABELS = {
    "hotblooded": "热血", "uplifting": "励志振奋", "cheerful": "元气欢快", "adventure": "冒险旅途",
    "playful": "俏皮诙谐", "cool": "帅气律动", "epic": "壮阔史诗", "romantic": "浪漫甜蜜",
    "healing": "治愈温柔", "relaxed": "宁静放松", "dreamy": "梦幻空灵", "nostalgic": "温暖怀旧",
    "neg_sad": "[负面]伤感", "neg_dark": "[负面]黑暗",
}

# mood -> (moodtheme class weights, binary-head weights, energy gate: +1 high, -1 low, 0 none)
MOOD_RULES: dict[str, tuple[dict[str, float], dict[str, float], int]] = {
    "hotblooded": ({"energetic": 1, "powerful": 1, "action": 1, "heavy": .6, "fast": .8, "sport": .6}, {"aggressive": .6}, 1),
    "uplifting": ({"uplifting": 1, "inspiring": 1, "motivational": 1, "hopeful": .8, "positive": .6}, {"happy": .3}, 1),
    "cheerful": ({"happy": 1, "fun": 1, "upbeat": 1, "party": .7, "positive": .7, "groovy": .5}, {"happy": .8, "party": .5}, 1),
    "adventure": ({"adventure": 1, "travel": 1, "summer": .7, "nature": .5}, {}, 0),
    "playful": ({"funny": 1, "fun": .7, "children": .8}, {"happy": .3}, 0),
    "cool": ({"cool": 1, "groovy": 1, "sexy": .8}, {"danceable": .3}, 0),
    "epic": ({"epic": 1, "film": .7, "trailer": .8, "adventure": .5, "powerful": .6, "dramatic": .6}, {}, 1),
    "romantic": ({"romantic": 1, "love": 1, "sexy": .4, "soft": .5}, {}, -1),
    "healing": ({"soft": 1, "calm": .8, "melodic": .6, "hopeful": .6, "emotional": .6}, {"relaxed": .4}, -1),
    "relaxed": ({"relaxing": 1, "calm": 1, "meditative": .8, "background": .7, "nature": .5, "slow": .6}, {"relaxed": 1}, -1),
    "dreamy": ({"dream": 1, "space": .8, "soundscape": .8, "deep": .6, "meditative": .5}, {}, -1),
    # Acoustically weak; decided mainly by lyrics in the LLM fusion step.
    "nostalgic": ({"retro": 1}, {}, 0),
    "neg_sad": ({"sad": 1, "melancholic": 1, "emotional": .3}, {"sad": 1}, -1),
    "neg_dark": ({"dark": 1, "dramatic": .4, "heavy": .4}, {"aggressive": .3}, 0),
}
CONFIDENCE_CAP = {"nostalgic": 0.5}

STYLE_LABELS = {
    "pop": "流行", "rock": "摇滚", "metal": "金属", "punk": "朋克", "electronic": "电子", "dance": "舞曲/EDM",
    "ballad": "抒情慢歌", "idol": "偶像流行", "folk_acoustic": "民谣/原声", "ethnic": "古风/民族",
    "orchestral": "交响/管弦", "piano": "钢琴", "jazz": "爵士/融合", "citypop_funk": "City Pop/放克",
    "hiphop": "说唱/嘻哈", "denpa": "电波/洗脑", "ambient": "氛围/环境", "experimental": "后摇/实验",
}

_DANCE_WORDS = ("House", "Trance", "Techno", "Eurobeat", "Eurodance", "Hardcore", "Hardstyle", "Drum n Bass",
                "Dubstep", "Hands Up", "Electro House", "Dance-pop", "Hi NRG", "Jungle", "Breakbeat", "Garage")
_METAL_WORDS = ("Metal", "Metalcore", "Deathcore", "Grindcore", "Thrash", "Doom", "Sludge")
_PUNK_WORDS = ("Punk", "Pop Punk", "Post-Hardcore", "Melodic Hardcore", "Emo", "Oi", "Crust")
_EXPERIMENTAL_WORDS = ("Post Rock", "Math Rock", "Shoegaze", "Experimental", "Avantgarde", "Krautrock", "IDM", "Glitch", "Noise")


def style_bucket(label: str) -> list[str]:
    genre, style = label.split("---", 1)
    out: list[str] = []
    if genre == "Rock":
        if any(w in style for w in _METAL_WORDS):
            out.append("metal")
        elif any(w == style or w in style for w in _PUNK_WORDS):
            out.append("punk")
        elif any(w in style for w in _EXPERIMENTAL_WORDS):
            out.append("experimental")
        elif style in ("Acoustic", "Folk Rock", "Country Rock"):
            out.append("folk_acoustic")
        elif style in ("Symphonic Rock",):
            out += ["rock", "orchestral"]
        elif style in ("Dream Pop", "Ethereal"):
            out += ["pop", "ambient"]
        else:
            out.append("rock")
    elif genre == "Electronic":
        if style in ("Ambient", "Dark Ambient", "Drone", "New Age", "Chillwave", "Downtempo", "Illbient"):
            out.append("ambient")
        elif style in ("Modern Classical",):
            out.append("orchestral")
        elif any(w in style for w in _EXPERIMENTAL_WORDS) or style in ("Abstract", "Musique Concrète", "Sound Collage"):
            out.append("experimental")
        elif style in ("Chiptune", "Happy Hardcore", "Bubblegum"):
            out += ["electronic", "denpa"]
        elif style in ("Hip Hop", "Trip Hop", "Grime"):
            out.append("hiphop")
        elif style in ("Synth-pop", "Synthwave", "Vaporwave", "Italo-Disco", "Euro-Disco", "Disco", "Nu-Disco"):
            out += ["electronic", "citypop_funk"] if style in ("Synthwave", "Vaporwave", "Nu-Disco") else ["electronic", "pop"]
        elif any(w in style for w in _DANCE_WORDS):
            out.append("dance")
        else:
            out.append("electronic")
    elif genre == "Pop":
        if style == "City Pop":
            out.append("citypop_funk")
        elif style == "Ballad":
            out.append("ballad")
        elif style in ("Bubblegum", "Novelty", "Parody"):
            out += ["pop", "denpa"]
        else:
            # J-pop/K-pop 等一律归为流行；“偶像流行”声学上无法区分，由 LLM 结合元数据判定（P0 反馈：乐队摇滚被误判为偶像）
            out.append("pop")
    elif genre == "Jazz":
        out.append("jazz")
    elif genre == "Funk / Soul":
        out.append("citypop_funk")
    elif genre == "Hip Hop":
        out.append("hiphop")
    elif genre == "Classical":
        out.append("orchestral")
    elif genre == "Stage & Screen":
        out.append("orchestral" if style in ("Score", "Soundtrack", "Theme") else "pop")
    elif genre == "Folk, World, & Country":
        out.append("folk_acoustic" if style in ("Folk", "Country", "Bluegrass", "Celtic", "Nordic") else "ethnic")
    elif genre == "Latin":
        out.append("ethnic")
    elif genre == "Blues":
        out.append("jazz")
    elif genre == "Reggae":
        out.append("pop")
    return out


def styles_from(genre400: np.ndarray, classes: list[str], instruments: dict[str, float], voice: float) -> list[tuple[str, float]]:
    scores: dict[str, float] = {}
    for idx in np.argsort(-genre400)[:10]:
        p = float(genre400[idx])
        for bucket in style_bucket(classes[idx]):
            scores[bucket] = scores.get(bucket, 0.0) + p
    # Instrument evidence for styles the 400 Discogs taxonomy cannot express.
    piano = max(instruments.get("piano", 0), instruments.get("electricpiano", 0) * .7)
    if piano >= 0.35 and voice < 0.5:
        scores["piano"] = scores.get("piano", 0.0) + piano
    orch = max(instruments.get("orchestra", 0), instruments.get("strings", 0), instruments.get("violin", 0) * .8)
    if orch >= 0.35:
        scores["orchestral"] = scores.get("orchestral", 0.0) + orch * .6
    if instruments.get("acousticguitar", 0) >= 0.4:
        scores["folk_acoustic"] = scores.get("folk_acoustic", 0.0) + instruments["acousticguitar"] * .5
    total = sum(scores.values()) or 1.0
    ranked = sorted(((k, v / total) for k, v in scores.items()), key=lambda kv: -kv[1])
    return [kv for kv in ranked if kv[1] >= 0.12][:3] or ranked[:1]


def _sigmoid(x: float) -> float:
    return 1.0 / (1.0 + math.exp(-x))


def raw_mood_scores(moodtheme: dict[str, float], heads: dict[str, float], arousal: float | None) -> dict[str, float]:
    out = {}
    for mood, (cls_w, head_w, gate) in MOOD_RULES.items():
        num = sum(w * moodtheme.get(c, 0.0) for c, w in cls_w.items())
        den = sum(cls_w.values())
        score = num / den
        if head_w:
            score = 0.6 * score + 0.4 * sum(w * heads.get(h, 0.0) for h, w in head_w.items()) / sum(head_w.values())
        if gate and arousal is not None:
            score *= _sigmoid(gate * (arousal - 5.0) / 0.6) * 1.5
        out[mood] = score
    return out


def energy_level(arousal: float | None, heads: dict[str, float]) -> int:
    if arousal is not None:
        base = (arousal - 1.0) / 8.0
    else:
        base = 0.5 * heads.get("aggressive", 0.5) + 0.5 * (1 - heads.get("relaxed", 0.5))
    base = 0.75 * base + 0.25 * heads.get("danceable", 0.5)
    return int(min(5, max(1, round(base * 4 + 1))))


def assign_moods(raw_by_track: list[dict[str, float]]) -> list[list[tuple[str, float]]]:
    """Library-relative: a mood counts when the track is in that mood's top ~25% and clearly
    above its own other moods. Returns per track up to 3 (mood, weight) with weights summing to 1."""
    if not raw_by_track:
        return []
    moods = list(MOOD_RULES)
    matrix = np.array([[r[m] for m in moods] for r in raw_by_track], dtype=np.float64)
    # percentile rank per mood across the sample
    ranks = matrix.argsort(axis=0).argsort(axis=0) / max(len(raw_by_track) - 1, 1)
    results = []
    for i in range(len(raw_by_track)):
        cand = [(moods[j], float(ranks[i, j]) * (0.5 + matrix[i, j] / (matrix[:, j].max() or 1) / 2)) for j in range(len(moods))]
        cand = [c for c in cand if c[1] >= 0.55]
        cand.sort(key=lambda kv: -kv[1])
        cand = cand[:3]
        if not cand:
            j = int(np.argmax(ranks[i]))
            cand = [(moods[j], float(ranks[i, j]))]
        cand = [(m, min(s, CONFIDENCE_CAP.get(m, 1.0))) for m, s in cand]
        cand.sort(key=lambda kv: -kv[1])
        total = sum(s for _, s in cand) or 1.0
        results.append([(m, round(s / total, 2)) for m, s in cand])
    return results
