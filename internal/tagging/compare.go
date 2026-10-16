package tagging

import (
	"encoding/json"
	"math"

	"github.com/lux032/032music-server/internal/storage"
)

// Comparison 汇总抽样对比中主模型与复核模型的一致程度。
type Comparison struct {
	N                 int             `json:"n"`                 // 两个模型都有结果的曲目数
	Errors            int             `json:"errors"`            // 任一模型失败的曲目数
	PrimaryMoodAgree  float64         `json:"primaryMoodAgree"`  // 主情绪相同的比例
	PrimaryStyleAgree float64         `json:"primaryStyleAgree"` // 主风格相同的比例
	MoodJaccard       float64         `json:"moodJaccard"`       // 情绪集合的平均 Jaccard 相似度
	StyleJaccard      float64         `json:"styleJaccard"`
	IdolAgree         float64         `json:"idolAgree"`
	NegativeAgree     float64         `json:"negativeAgree"` // 是否含负面标记的一致比例
	Rows              []ComparisonRow `json:"rows"`
}

// ComparisonRow 是一首曲目的对比（供抽查）。
type ComparisonRow struct {
	TrackID int64   `json:"trackId"`
	Title   string  `json:"title"`
	Artist  string  `json:"artist"`
	Primary *Result `json:"primary,omitempty"`
	Review  *Result `json:"review,omitempty"`
	Error   string  `json:"error,omitempty"`
	Same    bool    `json:"samePrimaryMood"`
}

// Compare 计算抽样对比结果。
func Compare(items []storage.AISampleItem) Comparison {
	var c Comparison
	var mood, style, moodJ, styleJ, idol, neg float64
	for _, it := range items {
		row := ComparisonRow{TrackID: it.TrackID, Title: it.Title, Artist: it.Artist, Error: it.Error}
		var p, r Result
		pOK := it.PrimaryJSON != "" && json.Unmarshal([]byte(it.PrimaryJSON), &p) == nil
		rOK := it.ReviewJSON != "" && json.Unmarshal([]byte(it.ReviewJSON), &r) == nil
		if pOK {
			row.Primary = &p
		}
		if rOK {
			row.Review = &r
		}
		if !pOK || !rOK || len(p.Moods) == 0 || len(r.Moods) == 0 {
			if it.Error != "" || it.PrimaryJSON != "" {
				c.Errors++
			}
			c.Rows = append(c.Rows, row)
			continue
		}
		c.N++
		if p.Moods[0].Tag == r.Moods[0].Tag {
			mood++
			row.Same = true
		}
		if len(p.Styles) > 0 && len(r.Styles) > 0 && p.Styles[0].Tag == r.Styles[0].Tag {
			style++
		}
		moodJ += jaccard(moodTags(p), moodTags(r))
		styleJ += jaccard(styleTags(p), styleTags(r))
		if p.Idol == r.Idol {
			idol++
		}
		if hasNegative(p) == hasNegative(r) {
			neg++
		}
		c.Rows = append(c.Rows, row)
	}
	if c.N > 0 {
		n := float64(c.N)
		c.PrimaryMoodAgree, c.PrimaryStyleAgree = round2(mood/n), round2(style/n)
		c.MoodJaccard, c.StyleJaccard = round2(moodJ/n), round2(styleJ/n)
		c.IdolAgree, c.NegativeAgree = round2(idol/n), round2(neg/n)
	}
	return c
}

func moodTags(r Result) []string {
	out := make([]string, len(r.Moods))
	for i, m := range r.Moods {
		out[i] = m.Tag
	}
	return out
}

func styleTags(r Result) []string {
	out := make([]string, len(r.Styles))
	for i, s := range r.Styles {
		out[i] = s.Tag
	}
	return out
}

func hasNegative(r Result) bool {
	for _, m := range r.Moods {
		if t, ok := MoodByID(m.Tag); ok && t.Negative {
			return true
		}
	}
	return false
}

func jaccard(a, b []string) float64 {
	set := map[string]int{}
	for _, x := range a {
		set[x] |= 1
	}
	for _, x := range b {
		set[x] |= 2
	}
	if len(set) == 0 {
		return 1
	}
	inter := 0
	for _, v := range set {
		if v == 3 {
			inter++
		}
	}
	return math.Round(float64(inter)/float64(len(set))*1000) / 1000
}
