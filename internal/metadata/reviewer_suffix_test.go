package metadata

import (
	"testing"
	"unicode/utf8"
)

func TestRVM2StripTrackTypeSuffixMatchesInferTrackTypeRules(t *testing.T) {
	for _, tc := range []struct {
		title, typ, want string
	}{
		{"Amore (TVサイズ)", "tv_size", "Amore"},
		{"Amore [tvサイズ]", "tv_size", "Amore"},
		{"Amore (backing track)", "off_vocal", "Amore"},
		{"Amore (minus one)", "off_vocal", "Amore"},
		{"Amore instrumental", "instrumental", "Amore"},
		{"Amore -ver.2022-", "tv_size", "Amore -ver.2022-"},
		{"Amore -version2020-", "instrumental", "Amore -version2020-"},
		{"K instrumental", "instrumental", "K"},
	} {
		if got := StripTrackTypeSuffix(tc.title, tc.typ); got != tc.want {
			t.Errorf("StripTrackTypeSuffix(%q,%q)=%q want %q", tc.title, tc.typ, got, tc.want)
		} else if !utf8.ValidString(got) {
			t.Errorf("StripTrackTypeSuffix(%q,%q) returned invalid UTF-8", tc.title, tc.typ)
		}
	}
}
