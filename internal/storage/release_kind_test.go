package storage

import "testing"

func TestInferAlbumTypeFromTags(t *testing.T) {
	cases := []struct {
		name   string
		raw    map[string][]string
		kind   string
		tagged bool
	}{
		{"no tags", map[string][]string{}, "album", false},
		{"musicautotagger flac", map[string][]string{"MUSICBRAINZ_ALBUMTYPE": {"single"}}, "single", true},
		{"picard flac", map[string][]string{"RELEASETYPE": {"ep"}}, "ep", true},
		{"picard multi value", map[string][]string{"RELEASETYPE": {"album", "soundtrack"}}, "soundtrack", true},
		{"single soundtrack prefers single", map[string][]string{"RELEASETYPE": {"single; soundtrack"}}, "single", true},
		{"mp3 txxx", map[string][]string{"MUSICBRAINZ ALBUM TYPE": {"EP"}}, "ep", true},
		{"m4a freeform with NUL locale", map[string][]string{"MUSICBRAINZ ALBUM TYPE": {"\x00\x00\x00\x00album;\x00\x00\x00\x00compilation"}}, "compilation", true},
		{"explicit album", map[string][]string{"MUSICBRAINZ_ALBUMTYPE": {"album"}}, "album", true},
		{"primary album plus compilation flag", map[string][]string{"MUSICBRAINZ_ALBUMTYPE": {"album"}, "COMPILATION": {"1"}}, "compilation", true},
		{"compilation flag only (mp3 TCMP)", map[string][]string{"TCMP": {"1"}}, "compilation", true},
		{"compilation flag zero", map[string][]string{"CPIL": {"0"}}, "album", false},
		{"unknown value is not a tag", map[string][]string{"RELEASETYPE": {"deep cuts"}}, "album", false},
		{"no substring match for ep", map[string][]string{"RELEASETYPE": {"repackage"}}, "album", false},
		{"broadcast maps to other", map[string][]string{"RELEASETYPE": {"broadcast"}}, "other", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, tagged := inferAlbumType(tc.raw)
			if kind != tc.kind || tagged != tc.tagged {
				t.Fatalf("inferAlbumType = (%q,%v), want (%q,%v)", kind, tagged, tc.kind, tc.tagged)
			}
		})
	}
}

func TestResolveReleaseKind(t *testing.T) {
	min := int64(60 * 1000)
	cases := []struct {
		name string
		in   releaseKindInput
		want string
	}{
		{"user override wins", releaseKindInput{UserType: "ep", StoredType: "single", Source: "tag", CoreTracks: 12}, "ep"},
		{"tag wins over inference", releaseKindInput{StoredType: "album", Source: "tag", CoreTracks: 2, DurationMillis: 8 * min}, "album"},
		{"enrichment counts as source", releaseKindInput{StoredType: "single", Source: "enrichment", CoreTracks: 10}, "single"},
		{"untagged default album is ignored", releaseKindInput{StoredType: "album", CoreTracks: 2, TotalTracks: 2, DurationMillis: 9 * min}, "single"},
		{"jp single with instrumentals", releaseKindInput{CoreTracks: 2, TotalTracks: 4, DurationMillis: 9 * min}, "single"},
		{"mini album", releaseKindInput{CoreTracks: 5, TotalTracks: 5, DurationMillis: 22 * min}, "ep"},
		{"full album", releaseKindInput{CoreTracks: 12, TotalTracks: 12, DurationMillis: 50 * min}, "album"},
		{"long three movement work", releaseKindInput{CoreTracks: 3, TotalTracks: 3, DurationMillis: 42 * min}, "album"},
		{"multi disc", releaseKindInput{DiscCount: 2, CoreTracks: 4, TotalTracks: 4}, "album"},
		{"unknown duration uses count", releaseKindInput{CoreTracks: 1, TotalTracks: 1}, "single"},
		{"only derivative tracks falls back to total", releaseKindInput{CoreTracks: 0, TotalTracks: 2}, "single"},
		{"no tracks", releaseKindInput{}, "album"},
		{"title live", releaseKindInput{Title: "LiSA LiVE is Smile Always ~Eve&Birth~ at Nippon Budokan", CoreTracks: 20}, "live"},
		{"title live japanese", releaseKindInput{Title: "ワンマンライブ 2024", CoreTracks: 15}, "live"},
		{"love live is not live", releaseKindInput{Title: "ラブライブ!サンシャイン!! オリジナルサウンドトラック", CoreTracks: 30, DurationMillis: 70 * min}, "album"},
		{"love live english", releaseKindInput{Title: "Love Live! Superstar!! Songs", CoreTracks: 10, DurationMillis: 40 * min}, "album"},
		{"live! is not live", releaseKindInput{Title: "LIVE!", CoreTracks: 2, DurationMillis: 8 * min}, "single"},
		{"alive is not live", releaseKindInput{Title: "ALIVE", CoreTracks: 10, DurationMillis: 45 * min}, "album"},
		{"best of", releaseKindInput{Title: "The Best of Aimer", CoreTracks: 16}, "compilation"},
		{"japanese best", releaseKindInput{Title: "ALL TIME BEST ~ベスト~", CoreTracks: 16}, "compilation"},
		{"title single", releaseKindInput{Title: "Butter (Single)", CoreTracks: 5, DurationMillis: 18 * min}, "single"},
		{"title ep", releaseKindInput{Title: "Summer EP", CoreTracks: 8, DurationMillis: 35 * min}, "ep"},
		{"fullwidth mini album", releaseKindInput{Title: "ミニアルバム「夜」", CoreTracks: 7, DurationMillis: 31 * min}, "ep"},
		{"step does not match ep", releaseKindInput{Title: "STEP", CoreTracks: 10, DurationMillis: 45 * min}, "album"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveReleaseKind(tc.in); got != tc.want {
				t.Fatalf("resolveReleaseKind = %q, want %q", got, tc.want)
			}
		})
	}
}
