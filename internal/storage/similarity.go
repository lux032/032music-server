package storage

import (
	"math"
	"strings"
	"unicode"
)

type SimilarTrack struct {
	Track    Track    `json:"track"`
	Distance float64  `json:"distance"`
	Score    float64  `json:"score"`
	Reasons  []string `json:"reasons"`
}
type similarityMeta struct {
	track                        Track
	primary, credit, genre, work map[int64]bool
	playlists                    map[int64]bool
	available                    bool
}
type similarityFactors struct {
	artist, credit, genre, era, playlist, work float64
	sameAlbum                                  bool
	skipFactor                                 float64
}

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }
func scoreSimilarity(f similarityFactors) (float64, []string) {
	score := .30*clamp(f.artist) + .20*clamp(f.credit) + .20*clamp(f.genre) + .10*clamp(f.era) + .10*clamp(f.playlist) + .10*clamp(f.work)
	reasons := []string{}
	if .30*clamp(f.artist) >= .01 {
		reasons = append(reasons, "sameArtist")
	}
	if .20*clamp(f.credit) >= .01 {
		reasons = append(reasons, "sharedCredit")
	}
	if .20*clamp(f.genre) >= .01 {
		reasons = append(reasons, "genre")
	}
	if .10*clamp(f.era) >= .01 {
		reasons = append(reasons, "era")
	}
	if .10*clamp(f.playlist) >= .01 {
		reasons = append(reasons, "coPlaylist")
	}
	if .10*clamp(f.work) >= .01 {
		reasons = append(reasons, "sameWork")
	}
	if f.sameAlbum {
		score *= .5
		reasons = append(reasons, "sameAlbum")
	}
	if f.skipFactor > 0 {
		score *= clamp(f.skipFactor)
	}
	return clamp(score), reasons
}
func overlap(a, b map[int64]bool) int {
	n := 0
	for id := range a {
		if b[id] {
			n++
		}
	}
	return n
}
func jaccard(a, b map[int64]bool) float64 {
	n := overlap(a, b)
	if n == 0 {
		return 0
	}
	return float64(n) / float64(len(a)+len(b)-n)
}
func compareSimilarity(a, b similarityMeta) (float64, []string) {
	f := similarityFactors{artist: clamp(float64(overlap(a.primary, b.primary))), credit: clamp(float64(overlap(a.credit, b.credit))), genre: jaccard(a.genre, b.genre), work: clamp(float64(overlap(a.work, b.work))), sameAlbum: a.track.AlbumID == b.track.AlbumID}
	if a.track.Year > 0 && b.track.Year > 0 {
		f.era = math.Exp(-math.Abs(float64(a.track.Year-b.track.Year)) / 5)
	}
	f.playlist = clamp(float64(overlap(a.playlists, b.playlists)) / 2)
	return scoreSimilarity(f)
}
func nonMain(t string) bool {
	switch t {
	case "instrumental", "off_vocal", "tv_size", "drama_track":
		return true
	}
	return false
}

var suffixes = []string{"tv size", "tv ver", "instrumental", "off vocal", "inst", "karaoke", "remaster", "remastered", "radio edit", "short ver", "movie ver", "伴奏", "纯音乐", "オフボーカル", "インスト", "カラオケ"}

func normalizedTitle(title string) string {
	var b strings.Builder
	for _, r := range title {
		if r >= 0xff01 && r <= 0xff5e {
			r -= 0xfee0
		}
		if r == 0x3000 {
			r = ' '
		}
		b.WriteRune(unicode.ToLower(r))
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	original := s
	pairs := map[rune]rune{'(': ')', '[': ']', '（': '）', '【': '】', '「': '」'}
	for len(s) > 0 {
		r := []rune(s)
		close := r[len(r)-1]
		depth := 0
		at := -1
		for i := len(r) - 1; i >= 0; i-- {
			if r[i] == close {
				depth++
			}
			if end, ok := pairs[r[i]]; ok && end == close {
				depth--
				if depth == 0 {
					at = i
					break
				}
			}
		}
		if at < 0 {
			break
		}
		part := strings.Trim(strings.TrimSpace(string(r[at+1:len(r)-1])), " .-_!！。")
		for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}, {"（", "）"}, {"【", "】"}} {
			part = strings.ReplaceAll(part, pair[0], " ")
			part = strings.ReplaceAll(part, pair[1], " ")
		}
		part = strings.Join(strings.Fields(part), " ")
		matched := false
		for _, word := range suffixes {
			if part == word || strings.HasPrefix(part, word+" ") {
				matched = true
				break
			}
		}
		if !matched {
			break
		}
		s = strings.TrimSpace(string(r[:at]))
		if s == "" {
			return original
		}
	}
	return s
}
func set(m map[int64]bool, id int64) map[int64]bool {
	if m == nil {
		m = map[int64]bool{}
	}
	m[id] = true
	return m
}
