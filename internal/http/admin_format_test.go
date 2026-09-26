package httpapi

import (
	"testing"
	"time"
)

func TestFormatTime(t *testing.T) {
	original := time.Local
	time.Local = time.FixedZone("fixture", 8*60*60)
	defer func() { time.Local = original }()
	for _, tc := range []struct{ input, want string }{
		{"2026-09-26T07:07:30Z", "2026-09-26 15:07"},
		{"2026-09-26T07:07:30.123Z", "2026-09-26 15:07"},
		{"", "—"},
		{"unexpected", "unexpected"},
	} {
		if got := formatTime(tc.input); got != tc.want {
			t.Errorf("formatTime(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAlbumTypeLabel(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"album", "专辑"}, {"single", "单曲"}, {"ep", "EP"},
		{"compilation", "合辑"}, {"soundtrack", "原声"}, {"live", "现场"},
		{"bootleg", "非官方发行"}, {"other", "其他"}, {"custom", "custom"},
	} {
		if got := albumTypeLabel(tc.input); got != tc.want {
			t.Errorf("albumTypeLabel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestWorkTypeLabel(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"anime", "动画"}, {"drama", "电视剧"}, {"movie", "电影"},
		{"game", "游戏"}, {"commercial", "广告"}, {"other", "其他"}, {"custom", "custom"},
	} {
		if got := workTypeLabel(tc.input); got != tc.want {
			t.Errorf("workTypeLabel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
