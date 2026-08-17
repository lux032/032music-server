package lyrics

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LyricLine represents a single line of lyrics, optionally with a timestamp.
type LyricLine struct {
	TimeMs int64  `json:"timeMs"`
	Text   string `json:"text"`
}

// LyricsResult contains parsed lyrics and whether they are time-synced.
type LyricsResult struct {
	Synced bool        `json:"synced"`
	Lines  []LyricLine `json:"lines"`
}

// lrcTimestamp matches [mm:ss.xx] or [mm:ss.xxx] or [mm:ss]
var lrcTimestamp = regexp.MustCompile(`\[(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?\]`)

// ParseLRC parses LRC format lyrics content into structured data.
// It supports standard [mm:ss.xx] timestamps and multiple timestamps per line.
// Lines without timestamps are returned as unsynced lyrics.
func ParseLRC(content string) LyricsResult {
	content = strings.TrimSpace(content)
	if content == "" {
		return LyricsResult{}
	}

	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")

	var synced []LyricLine
	var unsynced []LyricLine
	hasTimed := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try to extract timestamps
		matches := lrcTimestamp.FindAllStringSubmatchIndex(line, -1)
		if len(matches) == 0 {
			// Skip LRC metadata tags like [ti:xxx], [ar:xxx], etc.
			if strings.HasPrefix(line, "[") && strings.Contains(line, ":") && strings.Contains(line, "]") {
				// Check if it looks like a metadata tag (non-numeric after [)
				inner := line[1:strings.Index(line, "]")]
				parts := strings.SplitN(inner, ":", 2)
				if len(parts) == 2 {
					tag := strings.TrimSpace(parts[0])
					if !isNumeric(tag) {
						continue // skip metadata
					}
				}
			}
			unsynced = append(unsynced, LyricLine{Text: line})
			continue
		}

		// Extract the text after all timestamps
		lastMatchEnd := 0
		var timestamps []int64
		for _, match := range matches {
			if match[1] > lastMatchEnd {
				lastMatchEnd = match[1]
			}
			ms := parseTimestamp(line[match[2]:match[3]], line[match[4]:match[5]], safeSubstring(line, match[6], match[7]))
			timestamps = append(timestamps, ms)
		}
		text := strings.TrimSpace(line[lastMatchEnd:])

		for _, ts := range timestamps {
			hasTimed = true
			synced = append(synced, LyricLine{TimeMs: ts, Text: text})
		}
	}

	if hasTimed && len(synced) > 0 {
		// Sort by timestamp
		sort.Slice(synced, func(i, j int) bool {
			return synced[i].TimeMs < synced[j].TimeMs
		})
		return LyricsResult{Synced: true, Lines: synced}
	}

	if len(unsynced) > 0 {
		return LyricsResult{Synced: false, Lines: unsynced}
	}

	return LyricsResult{}
}

// parseTimestamp converts mm, ss, and optional centiseconds/milliseconds to total milliseconds.
func parseTimestamp(minuteStr, secondStr, fracStr string) int64 {
	minutes, _ := strconv.ParseInt(minuteStr, 10, 64)
	seconds, _ := strconv.ParseInt(secondStr, 10, 64)

	var fracMs int64
	if fracStr != "" {
		frac, _ := strconv.ParseInt(fracStr, 10, 64)
		switch len(fracStr) {
		case 1:
			fracMs = frac * 100
		case 2:
			fracMs = frac * 10
		case 3:
			fracMs = frac
		}
	}

	return minutes*60*1000 + seconds*1000 + fracMs
}

func safeSubstring(s string, start, end int) string {
	if start < 0 || end < 0 {
		return ""
	}
	return s[start:end]
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// DetectLRCPath returns the path of an LRC file if one exists alongside
// the audio file (same directory, same base name, .lrc extension).
func DetectLRCPath(audioPath string) string {
	dir := filepath.Dir(audioPath)
	base := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	candidate := filepath.Join(dir, base+".lrc")
	return candidate
}
