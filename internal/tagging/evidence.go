package tagging

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/lux032/032music-server/internal/analysis"
	"github.com/lux032/032music-server/internal/storage"
)

// LockThreshold：声学映射的风格得分（前 10 个 Discogs 风格概率按词表归并后的和）达到该值，
// 或乐器概率达到该值时，该风格是“声学高置信”结论，模型不能推翻（服务端强制保留）。
const LockThreshold = 0.5

// Labels 是声学向量的类别名（来自分析服务 /v1/labels）。
type Labels struct {
	Genre      []string `json:"genre"`
	MoodTheme  []string `json:"moodtheme"`
	Instrument []string `json:"instrument"`
}

// ParseLabels 解析 audio_analysis_labels.labels_json。
func ParseLabels(raw string) (Labels, error) {
	var l Labels
	err := json.Unmarshal([]byte(raw), &l)
	return l, err
}

// Scored 是一个带概率/得分的类别。
type Scored struct {
	Label string  `json:"label"`
	P     float64 `json:"p"`
}

// AudioEvidence 是交给模型的声学证据（也用于服务端校验与复核判定）。
type AudioEvidence struct {
	Genres      []Scored           `json:"discogsStyles"`         // Discogs 400 类前 8
	MoodTheme   []Scored           `json:"moodTheme"`             // MTG-Jamendo 56 类前 8
	Instruments []Scored           `json:"instruments,omitempty"` // 概率 ≥0.1 的前 5
	Heads       map[string]float64 `json:"heads"`                 // voice、danceable、happy、sad、relaxed、aggressive、party…
	Arousal     *float64           `json:"arousal,omitempty"`     // 唤醒度 1–9
	Valence     *float64           `json:"valence,omitempty"`     // 效价 1–9
	Energy      int                `json:"energyLevel"`           // 1–5（由唤醒度与可舞性推算）
	StyleHints  []Scored           `json:"styleHints"`            // 映射到风格词表的得分
	Locked      []string           `json:"lockedStyles,omitempty"`
	MoodHints   []Scored           `json:"moodHints"` // 映射到情绪词表的参考得分（声学对情绪的把握有限）
}

// BuildAudioEvidence 从一首曲目的声学特征生成证据。
func BuildAudioEvidence(f storage.TrackAudioFeatures, labels Labels) AudioEvidence {
	genre := analysis.Float16Vector(f.Genre)
	mood := analysis.Float16Vector(f.MoodTheme)
	inst := analysis.Float16Vector(f.Instrument)
	heads := map[string]float64{}
	_ = json.Unmarshal([]byte(f.HeadsJSON), &heads)
	ev := AudioEvidence{
		Genres:      topN(genre, labels.Genre, 8, 0),
		MoodTheme:   topN(mood, labels.MoodTheme, 8, 0),
		Instruments: topN(inst, labels.Instrument, 5, 0.1),
		Heads:       map[string]float64{},
		Arousal:     round2p(f.Arousal),
		Valence:     round2p(f.Valence),
	}
	for k, v := range heads {
		ev.Heads[k] = round2(v)
	}
	instMap := toMap(inst, labels.Instrument)
	scores := styleScores(topN(genre, labels.Genre, 10, 0), instMap, heads["voice"])
	ev.StyleHints, ev.Locked = rankStyles(scores)
	ev.MoodHints = moodHints(toMap(mood, labels.MoodTheme), heads, f.Arousal)
	ev.Energy = energyLevel(f.Arousal, heads)
	return ev
}

// AlbumSummary 是专辑的声学摘要（同专辑曲目风格一致性的参考）。
type AlbumSummary struct {
	Tracks     int      `json:"analyzedTracks"`
	StyleHints []Scored `json:"styleHints"`
	ArousalAvg *float64 `json:"arousalMean,omitempty"`
}

// BuildAlbumSummary 汇总专辑内已分析曲目的风格得分与平均唤醒度。
func BuildAlbumSummary(rows []storage.AlbumTrackFeatures, labels Labels) *AlbumSummary {
	if len(rows) < 2 {
		return nil
	}
	sum := map[string]float64{}
	var arousal float64
	var arousalN int
	for _, r := range rows {
		heads := map[string]float64{}
		_ = json.Unmarshal([]byte(r.HeadsJSON), &heads)
		s := styleScores(topN(analysis.Float16Vector(r.Genre), labels.Genre, 10, 0), toMap(analysis.Float16Vector(r.Instrument), labels.Instrument), heads["voice"])
		for k, v := range s {
			sum[k] += v
		}
		if r.Arousal != nil {
			arousal += *r.Arousal
			arousalN++
		}
	}
	for k := range sum {
		sum[k] /= float64(len(rows))
	}
	hints, _ := rankStyles(sum)
	out := &AlbumSummary{Tracks: len(rows), StyleHints: hints}
	if arousalN > 0 {
		out.ArousalAvg = round2p(ptr(arousal / float64(arousalN)))
	}
	return out
}

// ---------------------------------------------------------------- style mapping (ported from P0 mapping.py)

var (
	danceWords        = []string{"House", "Trance", "Techno", "Eurobeat", "Eurodance", "Hardcore", "Hardstyle", "Drum n Bass", "Dubstep", "Hands Up", "Electro House", "Dance-pop", "Hi NRG", "Jungle", "Breakbeat", "Garage"}
	metalWords        = []string{"Metal", "Metalcore", "Deathcore", "Grindcore", "Thrash", "Doom", "Sludge"}
	punkWords         = []string{"Punk", "Pop Punk", "Post-Hardcore", "Melodic Hardcore", "Emo", "Oi", "Crust"}
	experimentalWords = []string{"Post Rock", "Math Rock", "Shoegaze", "Experimental", "Avantgarde", "Krautrock", "IDM", "Glitch", "Noise"}
)

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func in(s string, set ...string) bool {
	for _, v := range set {
		if s == v {
			return true
		}
	}
	return false
}

// StyleBucket 把一个 Discogs 400 类标签（“Genre---Style”）映射到风格词表。
func StyleBucket(label string) []string {
	genre, style, ok := strings.Cut(label, "---")
	if !ok {
		return nil
	}
	switch genre {
	case "Rock":
		switch {
		case containsAny(style, metalWords):
			return []string{"metal"}
		case containsAny(style, punkWords):
			return []string{"punk"}
		case containsAny(style, experimentalWords):
			return []string{"experimental"}
		case in(style, "Acoustic", "Folk Rock", "Country Rock"):
			return []string{"folk_acoustic"}
		case style == "Symphonic Rock":
			return []string{"rock", "orchestral"}
		case in(style, "Dream Pop", "Ethereal"):
			return []string{"pop", "ambient"}
		}
		return []string{"rock"}
	case "Electronic":
		switch {
		case in(style, "Ambient", "Dark Ambient", "Drone", "New Age", "Chillwave", "Downtempo", "Illbient"):
			return []string{"ambient"}
		case style == "Modern Classical":
			return []string{"orchestral"}
		case containsAny(style, experimentalWords) || in(style, "Abstract", "Musique Concrète", "Sound Collage"):
			return []string{"experimental"}
		case in(style, "Chiptune", "Happy Hardcore", "Bubblegum"):
			return []string{"electronic", "denpa"}
		case in(style, "Hip Hop", "Trip Hop", "Grime"):
			return []string{"hiphop"}
		case in(style, "Synthwave", "Vaporwave", "Nu-Disco"):
			return []string{"electronic", "citypop_funk"}
		case in(style, "Synth-pop", "Italo-Disco", "Euro-Disco", "Disco"):
			return []string{"electronic", "pop"}
		case containsAny(style, danceWords):
			return []string{"dance"}
		}
		return []string{"electronic"}
	case "Pop":
		switch {
		case style == "City Pop":
			return []string{"citypop_funk"}
		case style == "Ballad":
			return []string{"ballad"}
		case in(style, "Bubblegum", "Novelty", "Parody"):
			return []string{"pop", "denpa"}
		}
		// J-pop/K-pop 一律归为流行；偶像流行声学上无法区分，由模型结合元数据判定。
		return []string{"pop"}
	case "Jazz", "Blues":
		return []string{"jazz"}
	case "Funk / Soul":
		return []string{"citypop_funk"}
	case "Hip Hop":
		return []string{"hiphop"}
	case "Classical":
		return []string{"orchestral"}
	case "Stage & Screen":
		if in(style, "Score", "Soundtrack", "Theme") {
			return []string{"orchestral"}
		}
		return []string{"pop"}
	case "Folk, World, & Country":
		if in(style, "Folk", "Country", "Bluegrass", "Celtic", "Nordic") {
			return []string{"folk_acoustic"}
		}
		return []string{"ethnic"}
	case "Latin":
		return []string{"ethnic"}
	case "Reggae":
		return []string{"pop"}
	}
	return nil
}

// styleScores 返回各风格的原始得分：前 10 个 Discogs 风格概率按词表归并求和，再加乐器证据。
func styleScores(genres []Scored, inst map[string]float64, voice float64) map[string]float64 {
	scores := map[string]float64{}
	for _, g := range genres {
		for _, b := range StyleBucket(g.Label) {
			scores[b] += g.P
		}
	}
	// 词表中 Discogs 体系表达不了的风格，用乐器补充（纯钢琴、管弦、原声吉他）。
	if piano := math.Max(inst["piano"], inst["electricpiano"]*0.7); piano >= 0.35 && voice < 0.5 {
		scores["piano"] += piano
	}
	if orch := math.Max(math.Max(inst["orchestra"], inst["strings"]), inst["violin"]*0.8); orch >= 0.35 {
		scores["orchestral"] += orch * 0.6
	}
	if g := inst["acousticguitar"]; g >= 0.4 {
		scores["folk_acoustic"] += g * 0.5
	}
	return scores
}

// rankStyles 返回按得分排序的前 4 个风格（得分截断到 1）与高置信锁定的风格。
func rankStyles(scores map[string]float64) ([]Scored, []string) {
	var out []Scored
	for k, v := range scores {
		if v >= 0.05 {
			out = append(out, Scored{Label: k, P: round2(math.Min(v, 1))})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P != out[j].P {
			return out[i].P > out[j].P
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > 4 {
		out = out[:4]
	}
	var locked []string
	for _, s := range out {
		if s.P >= LockThreshold && len(locked) < 2 {
			locked = append(locked, s.Label)
		}
	}
	return out, locked
}

// ---------------------------------------------------------------- mood hints (ported from P0 mapping.py)

type moodRule struct {
	classes map[string]float64
	heads   map[string]float64
	gate    float64 // +1 高唤醒，-1 低唤醒，0 不限
}

var moodRules = map[string]moodRule{
	"hotblooded": {map[string]float64{"energetic": 1, "powerful": 1, "action": 1, "heavy": .6, "fast": .8, "sport": .6}, map[string]float64{"aggressive": .6}, 1},
	"uplifting":  {map[string]float64{"uplifting": 1, "inspiring": 1, "motivational": 1, "hopeful": .8, "positive": .6}, map[string]float64{"happy": .3}, 1},
	"cheerful":   {map[string]float64{"happy": 1, "fun": 1, "upbeat": 1, "party": .7, "positive": .7, "groovy": .5}, map[string]float64{"happy": .8, "party": .5}, 1},
	"adventure":  {map[string]float64{"adventure": 1, "travel": 1, "summer": .7, "nature": .5}, nil, 0},
	"playful":    {map[string]float64{"funny": 1, "fun": .7, "children": .8}, map[string]float64{"happy": .3}, 0},
	"cool":       {map[string]float64{"cool": 1, "groovy": 1, "sexy": .8}, map[string]float64{"danceable": .3}, 0},
	"epic":       {map[string]float64{"epic": 1, "film": .7, "trailer": .8, "adventure": .5, "powerful": .6, "dramatic": .6}, nil, 1},
	"romantic":   {map[string]float64{"romantic": 1, "love": 1, "sexy": .4, "soft": .5}, nil, -1},
	"healing":    {map[string]float64{"soft": 1, "calm": .8, "melodic": .6, "hopeful": .6, "emotional": .6}, map[string]float64{"relaxed": .4}, -1},
	"relaxed":    {map[string]float64{"relaxing": 1, "calm": 1, "meditative": .8, "background": .7, "nature": .5, "slow": .6}, map[string]float64{"relaxed": 1}, -1},
	"dreamy":     {map[string]float64{"dream": 1, "space": .8, "soundscape": .8, "deep": .6, "meditative": .5}, nil, -1},
	"nostalgic":  {map[string]float64{"retro": 1}, nil, 0},
	"sad":        {map[string]float64{"sad": 1, "melancholic": 1, "emotional": .3}, map[string]float64{"sad": 1}, -1},
	"dark":       {map[string]float64{"dark": 1, "dramatic": .4, "heavy": .4}, map[string]float64{"aggressive": .3}, 0},
}

func weighted(values map[string]float64, weights map[string]float64) float64 {
	var num, den float64
	for k, w := range weights {
		num += w * values[k]
		den += w
	}
	if den == 0 {
		return 0
	}
	return num / den
}

func moodHints(mood map[string]float64, heads map[string]float64, arousal *float64) []Scored {
	var out []Scored
	for id, r := range moodRules {
		score := weighted(mood, r.classes)
		if len(r.heads) > 0 {
			score = 0.6*score + 0.4*weighted(heads, r.heads)
		}
		if r.gate != 0 && arousal != nil {
			score *= 1.5 / (1 + math.Exp(-r.gate*(*arousal-5)/0.6))
		}
		out = append(out, Scored{Label: id, P: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P != out[j].P {
			return out[i].P > out[j].P
		}
		return out[i].Label < out[j].Label
	})
	out = out[:4]
	// 相对化：最高者记为 1，便于模型理解相对强弱（绝对值本身没有意义）。
	top := out[0].P
	for i := range out {
		if top > 0 {
			out[i].P = round2(out[i].P / top)
		} else {
			out[i].P = 0
		}
	}
	return out
}

func energyLevel(arousal *float64, heads map[string]float64) int {
	get := func(k string) float64 {
		if v, ok := heads[k]; ok {
			return v
		}
		return 0.5
	}
	var base float64
	if arousal != nil {
		base = (*arousal - 1) / 8
	} else {
		base = 0.5*get("aggressive") + 0.5*(1-get("relaxed"))
	}
	base = 0.75*base + 0.25*get("danceable")
	return int(math.Min(5, math.Max(1, math.Round(base*4+1))))
}

// ---------------------------------------------------------------- helpers

func topN(values []float32, labels []string, n int, min float64) []Scored {
	idx := make([]int, 0, len(values))
	for i := range values {
		if i < len(labels) {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return values[idx[a]] > values[idx[b]] })
	var out []Scored
	for _, i := range idx {
		if len(out) >= n {
			break
		}
		p := float64(values[i])
		if p < min || p <= 0 {
			break
		}
		out = append(out, Scored{Label: labels[i], P: round2(p)})
	}
	return out
}

func toMap(values []float32, labels []string) map[string]float64 {
	m := make(map[string]float64, len(labels))
	for i, l := range labels {
		if i < len(values) {
			m[l] = float64(values[i])
		}
	}
	return m
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func round2p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := round2(*v)
	return &r
}

func ptr(v float64) *float64 { return &v }
