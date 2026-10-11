package httpapi

import (
	"testing"
	"time"
)

func TestFormatTime(t *testing.T) {
	zone := time.FixedZone("fixture", 8*60*60)
	for _, tc := range []struct{ input, want string }{
		{"2026-09-26T07:07:30Z", "2026-09-26 15:07"},
		{"2026-09-26T07:07:30.123Z", "2026-09-26 15:07"},
		{"", "—"},
		{"unexpected", "unexpected"},
	} {
		if got := formatAdminTimeIn(tc.input, zone); got != tc.want {
			t.Errorf("formatAdminTimeIn(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestLocalTimeMarkup(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"2026-09-26T07:07:30.123Z", `<time datetime="2026-09-26T07:07:30.123Z" data-local-time>2026-09-26 07:07 UTC</time>`},
		{"2026-09-26T15:07:30+08:00", `<time datetime="2026-09-26T07:07:30Z" data-local-time>2026-09-26 07:07 UTC</time>`},
		{"", "—"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"2026-09-26", "2026-09-26"},
	} {
		if got := string(localTimeMarkup(tc.input)); got != tc.want {
			t.Errorf("localTimeMarkup(%q) = %q, want %q", tc.input, got, tc.want)
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
