package lyricsonline

import (
	"strings"

	"github.com/lux032/032music-server/internal/lyrics"
)

// alignToleranceMs allows minor timestamp drift between lrc and tlyric while
// neighboring-line ownership prevents stealing a translation meant for another line.
const alignToleranceMs = 250

// AlignTranslation maps a source translation (NetEase tlyric, LRC) onto the
// original lines by timestamp. The result has len(lines) entries; lines
// without a translation are empty. ok is false when nothing could be aligned
// (e.g. the translation is unsynced).
func AlignTranslation(lines []lyrics.LyricLine, translationLRC string) (out []string, ok bool) {
	tr := lyrics.ParseLRC(translationLRC)
	if !tr.Synced || len(tr.Lines) == 0 {
		return nil, false
	}
	trLines := make([]lyrics.LyricLine, 0, len(tr.Lines))
	for _, l := range tr.Lines {
		if text := strings.TrimSpace(l.Text); text != "" {
			trLines = append(trLines, lyrics.LyricLine{TimeMs: l.TimeMs, Text: text})
		}
	}
	if len(trLines) == 0 {
		return nil, false
	}

	origIdx := make([]int, 0, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l.Text) != "" {
			origIdx = append(origIdx, i)
		}
	}
	out = make([]string, len(lines))
	if len(origIdx) == 0 {
		return out, false
	}

	// Track nearest strictly earlier / strictly later non-empty original timestamps.
	// Lines sharing the exact same timestamp inherit the same ownership bounds so
	// they can consume matching translation lines in order.
	prevTime := make([]int64, len(origIdx))
	hasPrev := make([]bool, len(origIdx))
	for p := 1; p < len(origIdx); p++ {
		if lines[origIdx[p]].TimeMs > lines[origIdx[p-1]].TimeMs {
			prevTime[p] = lines[origIdx[p-1]].TimeMs
			hasPrev[p] = true
		} else {
			prevTime[p] = prevTime[p-1]
			hasPrev[p] = hasPrev[p-1]
		}
	}
	nextTime := make([]int64, len(origIdx))
	hasNext := make([]bool, len(origIdx))
	for p := len(origIdx) - 2; p >= 0; p-- {
		if lines[origIdx[p]].TimeMs < lines[origIdx[p+1]].TimeMs {
			nextTime[p] = lines[origIdx[p+1]].TimeMs
			hasNext[p] = true
		} else {
			nextTime[p] = nextTime[p+1]
			hasNext[p] = hasNext[p+1]
		}
	}

	j := 0
	for p, idx := range origIdx {
		t := lines[idx].TimeMs
		for j < len(trLines) {
			cand := trLines[j].TimeMs
			if cand < t-alignToleranceMs || (hasPrev[p] && absInt64(cand-t) > absInt64(cand-prevTime[p])) {
				j++
				continue
			}
			break
		}
		bestIdx := -1
		var bestDist int64
		for k := j; k < len(trLines) && trLines[k].TimeMs <= t+alignToleranceMs; k++ {
			cand := trLines[k].TimeMs
			dist := absInt64(cand - t)
			if hasNext[p] && dist > absInt64(cand-nextTime[p]) {
				break
			}
			if bestIdx < 0 || dist < bestDist {
				bestIdx = k
				bestDist = dist
			}
		}
		if bestIdx >= 0 {
			out[idx] = trLines[bestIdx].Text
			ok = true
			j = bestIdx + 1
		}
	}
	return out, ok
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// IsSimplifiedChinese reports whether a target language code asks for
// Simplified Chinese, the language of NetEase's own translations.
func IsSimplifiedChinese(code string) bool {
	c := strings.ToLower(code)
	switch {
	case c == "zh", c == "zh-cn", c == "zh-sg", c == "zh-hans", strings.HasPrefix(c, "zh-hans-"):
		return true
	}
	return false
}
