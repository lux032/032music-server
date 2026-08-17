package metadata

import "testing"

func TestInferTrackType(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		path     string
		raw      map[string][]string
		expected string
	}{
		// Title-based detection
		{"regular track", "花の塔", "/music/album/01 花の塔.flac", nil, "regular"},
		{"instrumental parenthesized", "花の塔 (Instrumental)", "/music/album/02 花の塔 (Instrumental).flac", nil, "instrumental"},
		{"instrumental bracketed", "花の塔 [Instrumental]", "/music/album/02 花の塔 [Instrumental].flac", nil, "instrumental"},
		{"inst abbreviated", "花の塔 (Inst)", "/music/album/02 花の塔 (Inst).flac", nil, "instrumental"},
		{"inst dot abbreviated", "花の塔 (inst.)", "/music/album/02 花の塔 (inst.).flac", nil, "instrumental"},
		{"off vocal", "花の塔 (Off Vocal)", "/music/album/03 花の塔 (Off Vocal).flac", nil, "off_vocal"},
		{"off-vocal hyphen", "花の塔 (Off-Vocal)", "/music/album/03 花の塔 (Off-Vocal).flac", nil, "off_vocal"},
		{"backing track", "花の塔 (Backing Track)", "/music/album/03 花の塔 (Backing Track).flac", nil, "off_vocal"},
		{"minus one", "花の塔 (Minus One)", "/music/album/03 花の塔 (Minus One).flac", nil, "off_vocal"},
		{"karaoke katakana", "花の塔 (カラオケ)", "/music/album/03 花の塔 (カラオケ).flac", nil, "off_vocal"},
		{"tv size", "花の塔 (TV Size)", "/music/album/04 花の塔 (TV Size).flac", nil, "tv_size"},
		{"tv ver", "花の塔 (TV ver.)", "/music/album/04 花の塔 (TV ver.).flac", nil, "tv_size"},
		{"anime ver", "花の塔 (Anime ver.)", "/music/album/04 花の塔 (Anime ver.).flac", nil, "tv_size"},
		{"short ver", "花の塔 (Short Ver.)", "/music/album/04 花の塔 (Short Ver.).flac", nil, "tv_size"},
		{"tv size katakana", "花の塔 (TVサイズ)", "/music/album/04 花の塔 (TVサイズ).flac", nil, "tv_size"},
		{"drama", "第一話 (Drama)", "/music/album/05 第一話 (Drama).flac", nil, "drama_track"},
		{"drama katakana", "第一話 (ドラマ)", "/music/album/05 第一話 (ドラマ).flac", nil, "drama_track"},
		{"skit", "第一話 (Skit)", "/music/album/05 第一話 (Skit).flac", nil, "drama_track"},
		{"remix", "花の塔 (Remix)", "/music/album/06 花の塔 (Remix).flac", nil, "remix"},

		// Folder-based detection
		{"instrumental folder", "花の塔", "/music/album/Instrumental/01 花の塔.flac", nil, "instrumental"},
		{"off vocal folder", "花の塔", "/music/album/Off Vocal/01 花の塔.flac", nil, "off_vocal"},
		{"off-vocal folder", "花の塔", "/music/album/Off-Vocal/01 花の塔.flac", nil, "off_vocal"},
		{"karaoke folder", "花の塔", "/music/album/カラオケ/01 花の塔.flac", nil, "off_vocal"},

		// Tag-based detection
		{"tag instrumental", "花の塔", "/music/album/01 花の塔.flac",
			map[string][]string{"CONTENTTYPE": {"instrumental"}}, "instrumental"},
		{"tag drama", "花の塔", "/music/album/01 花の塔.flac",
			map[string][]string{"CONTENTTYPE": {"drama track"}}, "drama_track"},
		{"tag remix", "花の塔", "/music/album/01 花の塔.flac",
			map[string][]string{"CONTENTTYPE": {"remix"}}, "remix"},

		// Case insensitive
		{"case insensitive instrumental", "花の塔 (INSTRUMENTAL)", "/music/album/02 花の塔.flac", nil, "instrumental"},
		{"case insensitive off vocal", "花の塔 (OFF VOCAL)", "/music/album/03 花の塔.flac", nil, "off_vocal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.raw
			if raw == nil {
				raw = map[string][]string{}
			}
			result := InferTrackType(tt.title, tt.path, raw)
			if result != tt.expected {
				t.Errorf("InferTrackType(%q, %q) = %q, want %q", tt.title, tt.path, result, tt.expected)
			}
		})
	}
}
