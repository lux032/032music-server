package tagclean

import "testing"

// D-19: the shared cleaning rule (also storage D62): split on NUL, first
// non-empty segment, strip C0 controls, tab folds to a space, TrimSpace.
func TestValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"leading NULs", "\x00\x00\x00\x00ARCD0012", "ARCD0012"},
		{"trailing NUL", "ARCD0012\x00", "ARCD0012"},
		{"only NULs", "\x00\x00\x00", ""},
		{"NUL inside japanese", "前\x00後", "前"},
		{"tab folds to space", "作詞\t太郎", "作詞 太郎"},
		{"other C0 stripped", "\x01\x02Label\x1f", "Label"},
		{"empty", "", ""},
		{"plain", "SVWC-70658", "SVWC-70658"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Value(tc.value); got != tc.want {
				t.Fatalf("Value(%q)=%q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestFirstFallsBackWithinOneKey(t *testing.T) {
	if got := First([]string{"\x00", "Fallback"}); got != "Fallback" {
		t.Fatalf("First fallback=%q", got)
	}
	if got := First([]string{"\x00", "\x01"}); got != "" {
		t.Fatalf("First all-dirty=%q", got)
	}
}

func TestFirstKeySemantics(t *testing.T) {
	// A key whose values all clean to empty suppresses later keys.
	if got := FirstKey(map[string][]string{"LABEL": {"\x00"}, "PUBLISHER": {"Publisher"}}, "LABEL", "PUBLISHER"); got != "" {
		t.Fatalf("key order changed: %q", got)
	}
	// A missing key falls through to the next key.
	if got := FirstKey(map[string][]string{"PUBLISHER": {"\x00Publisher\x00"}}, "LABEL", "PUBLISHER"); got != "Publisher" {
		t.Fatalf("second key=%q", got)
	}
	// An empty value list behaves like a missing key.
	if got := FirstKey(map[string][]string{"LABEL": {}, "PUBLISHER": {"Publisher"}}, "LABEL", "PUBLISHER"); got != "Publisher" {
		t.Fatalf("empty list=%q", got)
	}
}
