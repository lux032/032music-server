package lyricsonline

import (
	"strings"

	"github.com/lux032/032music-server/internal/lyrics"
)

// alignToleranceMs allows tiny timestamp differences between lrc and tlyric.
const alignToleranceMs = 100

// AlignTranslation maps a source translation (NetEase tlyric, LRC) onto the
// original lines by timestamp. The result has len(lines) entries; lines
// without a translation are empty. ok is false when nothing could be aligned
// (e.g. the original is unsynced).
func AlignTranslation(lines []lyrics.LyricLine, translationLRC string) (out []string, ok bool) {
	tr := lyrics.ParseLRC(translationLRC)
	if !tr.Synced || len(tr.Lines) == 0 {
		return nil, false
	}
	out = make([]string, len(lines))
	j := 0
	for i, line := range lines {
		for j < len(tr.Lines) && tr.Lines[j].TimeMs < line.TimeMs-alignToleranceMs {
			j++
		}
		if j < len(tr.Lines) && tr.Lines[j].TimeMs <= line.TimeMs+alignToleranceMs {
			text := strings.TrimSpace(tr.Lines[j].Text)
			if text != "" && strings.TrimSpace(line.Text) != "" {
				out[i] = text
				ok = true
			}
		}
	}
	return out, ok
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
