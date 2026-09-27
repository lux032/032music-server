package metadata

import "testing"

func TestStripWorkSeasonAndNumber(t *testing.T) {
	cases := []struct {
		title  string
		base   string
		season int
	}{
		{"X Season 2", "X", 2},
		{"X 2期", "X", 2},
		{"X 第2期", "X", 2},
		{"X 第 2 季", "X", 2},
		{"X season 10", "X", 10},
		{"X", "X", 0},
		{"劇場版 X", "劇場版 X", 0},
	}
	for _, tc := range cases {
		if got := StripWorkSeason(tc.title); got != tc.base {
			t.Errorf("StripWorkSeason(%q) = %q, want %q", tc.title, got, tc.base)
		}
		if got := WorkSeasonNumber(tc.title); got != tc.season {
			t.Errorf("WorkSeasonNumber(%q) = %d, want %d", tc.title, got, tc.season)
		}
	}
}
