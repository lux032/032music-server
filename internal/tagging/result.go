package tagging

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

// ErrInvalidOutput 表示模型输出无法解析或没有任何有效标签。
var ErrInvalidOutput = errors.New("invalid model output")

// MoodScore / StyleScore 是校验后的标签。
type MoodScore struct {
	Tag        string  `json:"tag"`
	Weight     float64 `json:"weight"`
	Confidence float64 `json:"confidence"`
}

type StyleScore struct {
	Tag        string  `json:"tag"`
	Confidence float64 `json:"confidence"`
}

// Result 是校验后的打标结果。
type Result struct {
	Moods  []MoodScore  `json:"moods"`
	Styles []StyleScore `json:"styles"`
	Idol   string       `json:"idol"`
	Vocal  string       `json:"vocal"`
	Energy int          `json:"energy"`
	// Forced 是模型漏掉、由服务端按声学证据补上的锁定风格。
	Forced []string `json:"forced,omitempty"`
	// Dropped 是模型给出的词表外标签（已丢弃）。
	Dropped []string `json:"dropped,omitempty"`
}

type rawResult struct {
	Moods []struct {
		Tag        string  `json:"tag"`
		Weight     float64 `json:"weight"`
		Confidence float64 `json:"confidence"`
	} `json:"moods"`
	Styles []struct {
		Tag        string  `json:"tag"`
		Confidence float64 `json:"confidence"`
	} `json:"styles"`
	Idol   string  `json:"idol"`
	Vocal  string  `json:"vocal"`
	Energy float64 `json:"energy"`
}

// ParseResult 解析并校验模型输出：丢弃词表外标签、截断数值、限制数量、补上锁定风格、
// 归一化情绪权重。ev 可以为 nil（没有声学证据）。
func ParseResult(content string, ev *AudioEvidence) (Result, error) {
	var raw rawResult
	text := stripFences(content)
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	var r Result
	seen := map[string]bool{}
	for _, m := range raw.Moods {
		tag := strings.ToLower(strings.TrimSpace(m.Tag))
		t, ok := MoodByID(tag)
		if !ok {
			r.Dropped = append(r.Dropped, m.Tag)
			continue
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		conf := clamp(m.Confidence)
		if t.MaxConfidence > 0 && conf > t.MaxConfidence {
			conf = t.MaxConfidence
		}
		w := m.Weight
		if math.IsNaN(w) || w < 0 {
			w = 0
		}
		r.Moods = append(r.Moods, MoodScore{Tag: tag, Weight: w, Confidence: conf})
	}
	if len(r.Moods) == 0 {
		return Result{}, fmt.Errorf("%w: no valid mood", ErrInvalidOutput)
	}
	sort.SliceStable(r.Moods, func(i, j int) bool { return r.Moods[i].Weight > r.Moods[j].Weight })
	if len(r.Moods) > 3 {
		r.Moods = r.Moods[:3]
	}
	var sum float64
	for _, m := range r.Moods {
		sum += m.Weight
	}
	for i := range r.Moods {
		if sum > 0 {
			r.Moods[i].Weight = round2(r.Moods[i].Weight / sum)
		} else {
			r.Moods[i].Weight = round2(1 / float64(len(r.Moods)))
		}
		r.Moods[i].Confidence = round2(r.Moods[i].Confidence)
	}

	seen = map[string]bool{}
	for _, s := range raw.Styles {
		tag := strings.ToLower(strings.TrimSpace(s.Tag))
		if _, ok := StyleByID(tag); !ok {
			r.Dropped = append(r.Dropped, s.Tag)
			continue
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		r.Styles = append(r.Styles, StyleScore{Tag: tag, Confidence: round2(clamp(s.Confidence))})
	}
	sort.SliceStable(r.Styles, func(i, j int) bool { return r.Styles[i].Confidence > r.Styles[j].Confidence })

	r.Idol = strings.ToLower(strings.TrimSpace(raw.Idol))
	if r.Idol != "yes" && r.Idol != "no" && r.Idol != "unsure" {
		r.Idol = "unsure"
	}
	switch {
	case r.Idol == "yes" && !seen["idol"]:
		r.Styles = append(r.Styles, StyleScore{Tag: "idol", Confidence: 0.6})
		seen["idol"] = true
	case r.Idol == "no" && seen["idol"]:
		r.Styles = removeStyle(r.Styles, "idol")
		delete(seen, "idol")
	case r.Idol == "unsure" && seen["idol"]:
		r.Idol = "yes"
	}

	// 声学高置信的风格不能被推翻：模型漏掉时补上，并排在最前。
	var locked []StyleScore
	if ev != nil {
		for _, id := range ev.Locked {
			score := 0.0
			for _, h := range ev.StyleHints {
				if h.Label == id {
					score = h.P
				}
			}
			if seen[id] {
				for i := range r.Styles {
					if r.Styles[i].Tag == id && r.Styles[i].Confidence < score {
						r.Styles[i].Confidence = round2(score)
					}
				}
			} else {
				r.Forced = append(r.Forced, id)
				locked = append(locked, StyleScore{Tag: id, Confidence: round2(score)})
				seen[id] = true
			}
		}
	}
	r.Styles = append(locked, r.Styles...)
	sort.SliceStable(r.Styles, func(i, j int) bool { return r.Styles[i].Confidence > r.Styles[j].Confidence })
	if len(r.Styles) > 3 {
		keep := map[string]bool{}
		if ev != nil {
			for _, id := range ev.Locked {
				keep[id] = true
			}
		}
		if r.Idol == "yes" {
			keep["idol"] = true
		}
		var out []StyleScore
		for _, s := range r.Styles {
			if keep[s.Tag] {
				out = append(out, s)
			}
		}
		for _, s := range r.Styles {
			if len(out) >= 3 {
				break
			}
			if !keep[s.Tag] {
				out = append(out, s)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
		r.Styles = out
	}
	if len(r.Styles) == 0 && ev != nil && len(ev.StyleHints) > 0 {
		r.Styles = []StyleScore{{Tag: ev.StyleHints[0].Label, Confidence: round2(math.Min(ev.StyleHints[0].P, 0.5))}}
		r.Forced = append(r.Forced, ev.StyleHints[0].Label)
	}

	r.Vocal = strings.ToLower(strings.TrimSpace(raw.Vocal))
	if r.Vocal != "vocal" && r.Vocal != "instrumental" && r.Vocal != "mixed" {
		r.Vocal = ""
	}
	r.Energy = int(math.Round(raw.Energy))
	if r.Energy < 1 || r.Energy > 5 {
		r.Energy = 0
		if ev != nil {
			r.Energy = ev.Energy
		}
	}
	return r, nil
}

// ReviewReasons 返回需要复核的原因（空表示不需要）：
// low_confidence 主情绪把握低；idol_unsure 有人声的流行类曲目无法判断是否偶像；
// energy_conflict 主情绪与唤醒度明显矛盾；locked_style 模型漏掉了声学高置信的风格。
func ReviewReasons(r Result, ev *AudioEvidence) []string {
	var out []string
	if len(r.Moods) > 0 && r.Moods[0].Confidence < 0.5 {
		out = append(out, "low_confidence")
	}
	if r.Idol == "unsure" && r.Vocal != "instrumental" {
		for _, s := range r.Styles {
			if s.Tag == "pop" || s.Tag == "dance" || s.Tag == "denpa" {
				out = append(out, "idol_unsure")
				break
			}
		}
	}
	if ev != nil && ev.Arousal != nil && len(r.Moods) > 0 {
		t, _ := MoodByID(r.Moods[0].Tag)
		if (t.Energy > 0 && *ev.Arousal < 3.5) || (t.Energy < 0 && *ev.Arousal > 6.5) {
			out = append(out, "energy_conflict")
		}
	}
	if len(r.Forced) > 0 {
		out = append(out, "locked_style")
	}
	return out
}

// ReviewReasonLabel 返回复核原因的中文说明。
func ReviewReasonLabel(code string) string {
	switch code {
	case "low_confidence":
		return "主情绪把握低"
	case "idol_unsure":
		return "无法判断是否偶像"
	case "energy_conflict":
		return "情绪与唤醒度矛盾"
	case "locked_style":
		return "漏掉了声学高置信风格"
	}
	return code
}

// Tags 把结果转为存储用的标签（source=llm）。
func (r Result) Tags() []storage.TrackTag {
	var out []storage.TrackTag
	for _, m := range r.Moods {
		out = append(out, storage.TrackTag{Kind: "mood", Tag: m.Tag, Weight: m.Weight, Confidence: m.Confidence, Source: "llm"})
	}
	n := float64(len(r.Styles))
	for _, s := range r.Styles {
		out = append(out, storage.TrackTag{Kind: "style", Tag: s.Tag, Weight: round2(1 / n), Confidence: s.Confidence, Source: "llm"})
	}
	return out
}

// AudioTags 把声学证据转为存储用的标签（source=audio）：风格得分与情绪参考。
func (ev *AudioEvidence) AudioTags() []storage.TrackTag {
	if ev == nil {
		return nil
	}
	var out []storage.TrackTag
	for _, s := range ev.StyleHints {
		out = append(out, storage.TrackTag{Kind: "style", Tag: s.Label, Weight: math.Min(s.P, 1), Confidence: math.Min(s.P, 1), Source: "audio"})
	}
	var sum float64
	for _, m := range ev.MoodHints {
		sum += m.P
	}
	for _, m := range ev.MoodHints {
		w := 0.0
		if sum > 0 {
			w = round2(m.P / sum)
		}
		out = append(out, storage.TrackTag{Kind: "mood", Tag: m.Label, Weight: w, Confidence: 0.3, Source: "audio"})
	}
	return out
}

func removeStyle(list []StyleScore, tag string) []StyleScore {
	out := list[:0]
	for _, s := range list {
		if s.Tag != tag {
			out = append(out, s)
		}
	}
	return out
}

func clamp(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```JSON")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i > 0 && j > i {
		s = s[i : j+1]
	}
	return strings.TrimSpace(s)
}
