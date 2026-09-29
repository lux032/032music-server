package metadata

import "testing"

// TestInferAlbumWorkEdgeTrim covers the v6 edge cleanup (D70) with the real
// titles that v5 truncated in the user library, plus the regression cases that
// pin the trailing-dash and bracket rules.
func TestInferAlbumWorkEdgeTrim(t *testing.T) {
	cases := []struct{ name, title, typ, role string }{
		// A bracketed segment containing an OST keyword is dropped whole; the
		// mutation "no bracket-group removal" yields "Lazarus (Adult Swim".
		{"Lazarus (Adult Swim original series soundtrack)", "Lazarus", "other", "ost"},
		{"Cyberpunk: Edgerunners (Original Series Soundtrack)", "Cyberpunk: Edgerunners", "other", "ost"},
		{"X (Original Game Soundtrack)", "X", "other", "ost"},
		// A bracketed segment without an OST keyword survives with its closing
		// bracket; the mutation "always strip edge brackets" truncates it.
		{"Fate/stay night [Unlimited Blade Works] Original Soundtrack", "Fate/stay night [Unlimited Blade Works]", "other", "ost"},
		{"X (Deluxe Edition) Original Soundtrack", "X (Deluxe Edition)", "other", "ost"},
		// "The" belongs to the OST suffix, not to the work title.
		{"Panty & Stocking with Garterbelt: The Original Soundtrack", "Panty & Stocking with Garterbelt", "other", "ost"},
		{"New PANTY & STOCKING with GARTERBELT The Soundtrack", "New PANTY & STOCKING with GARTERBELT", "other", "ost"},
		// コンプリート belongs to the サウンドトラック suffix.
		{"キルラキル コンプリートサウンドトラック", "キルラキル", "other", "ost"},
		// The " -X-" connector keeps the trailing dash; the mutations "always
		// strip" and "any earlier dash counts" both fail here.
		{"TVアニメ「艦隊これくしょん -艦これ-」キャラクターソング “艦娘乃歌”", "艦隊これくしょん -艦これ-", "anime", "character"},
		{"イースX -NORDICS- オリジナルサウンドトラック", "イースX -NORDICS-", "other", "ost"},
		{"月姫 -A piece of blue glass moon- Original Soundtrack", "月姫 -A piece of blue glass moon-", "other", "ost"},
		{"月姫 -A piece of blue glass moon- THEME SONG E.P.", "月姫 -A piece of blue glass moon-", "anime", "theme"},
		{"ロード・エルメロイⅡ世の事件簿 -魔眼蒐集列車 Grace note- Original Soundtrack", "ロード・エルメロイⅡ世の事件簿 -魔眼蒐集列車 Grace note-", "other", "ost"},
		{"Fate/strange Fake -Whispers of Dawn- Original Soundtrack EP", "Fate/strange Fake -Whispers of Dawn-", "other", "ost"},
		// No spaced connector: the trailing dash is dropped; the mutation "any
		// earlier dash counts" would keep it.
		{"86-エイティシックス- ORIGINAL SOUNDTRACK", "86-エイティシックス", "other", "ost"},
		{"かすかなはな - Kasuka na Hana- (OP Theme to Hell's Paradise: Jigokuraku Season 2)", "かすかなはな - Kasuka na Hana", "anime", "op"},
		// A dangling " -" before the keyword is always dropped; the mutation
		// "always keep the trailing dash" fails here.
		{"Hollow Knight: Silksong - Official Soundtrack", "Hollow Knight: Silksong", "other", "ost"},
		// B1: the first OST bracket group starts the release-metadata tail and
		// everything after it is cut too (v5 behavior).
		{"SPY×FAMILY (Original Soundtrack) Vol.2", "SPY×FAMILY", "other", "ost"},
		{"Dune (Original Motion Picture Soundtrack) [Deluxe Edition]", "Dune", "other", "ost"},
		{"X (Original Soundtrack) - EP", "X", "other", "ost"},
		// B2: without \b, "The"/media words match inside other words.
		{"Breathe Original Soundtrack", "Breathe", "other", "ost"},
		{"Scythe Original Soundtrack", "Scythe", "other", "ost"},
		{"Endgame Soundtrack", "Endgame", "other", "ost"},
		// High-1: after cutting at the OST bracket group, an unbracketed keyword
		// in the remaining prefix must still be cut.
		{"Title OST (Original Soundtrack)", "Title", "other", "ost"},
		{"イースX -NORDICS- オリジナルサウンドトラック (Original Soundtrack)", "イースX -NORDICS-", "other", "ost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := InferAlbumWork(tc.name, "", false)
			if !ok || got.Title != tc.title || got.Type != tc.typ || got.Role != tc.role {
				t.Fatalf("got %+v %v, want %q %q %q", got, ok, tc.title, tc.typ, tc.role)
			}
		})
	}
}

// TestInferAlbumWorkGenericResidue covers the word-based generic check (D70):
// keyword removal that consumes the whole title must not leave a "work" named
// "Album" or "Vol.2".
func TestInferAlbumWorkGenericResidue(t *testing.T) {
	for _, title := range []string{
		"Soundtrack Album",
		"Original Soundtrack Album",
		"Original Soundtrack Vol.2",
		"Original Soundtrack Disc 1",
		"Soundtrack EP",
		"The Soundtrack",
		"OST 2",
		"Complete Soundtrack Box",
		"コンプリートサウンドトラック アルバム",
	} {
		t.Run(title, func(t *testing.T) {
			if got, ok := InferAlbumWork(title, "", false); ok {
				t.Fatalf("generic residue inferred a work: %+v", got)
			}
		})
	}
	// Real work names after the keyword must still survive the word-based check.
	for title, want := range map[string]string{
		"オリジナル・サウンドトラック 屍姫 赫":                                "屍姫 赫",
		"STELLAR BLADE ARRANGE TRACKS (Original Soundtrack)": "STELLAR BLADE ARRANGE TRACKS",
	} {
		t.Run(want, func(t *testing.T) {
			got, ok := InferAlbumWork(title, "", false)
			if !ok || got.Title != want {
				t.Fatalf("got %+v %v, want %q", got, ok, want)
			}
		})
	}
}

// TestInferAlbumWorkDigitsAreWorkNames (H2): a pure-digit title is a valid
// work name everywhere except the "keyword at start, residue only" branch.
func TestInferAlbumWorkDigitsAreWorkNames(t *testing.T) {
	if got, ok := InferAlbumWork("TVアニメ「86」OP", "", false); !ok || got.Title != "86" || got.Role != "op" {
		t.Fatalf("86: %+v %v", got, ok)
	}
	if got, ok := InferAlbumWork("1917 (Original Motion Picture Soundtrack)", "", false); !ok || got.Title != "1917" || got.Role != "ost" {
		t.Fatalf("1917: %+v %v", got, ok)
	}
	for _, title := range []string{"OST 2", "Original Soundtrack Vol.2"} {
		if got, ok := InferAlbumWork(title, "", false); ok {
			t.Fatalf("%q must not infer: %+v", title, got)
		}
	}
}

// TestTrimWorkTitleEdgesTildePairing (L2): a trailing tilde is kept only when
// the title holds an even number of tildes.
func TestTrimWorkTitleEdgesTildePairing(t *testing.T) {
	for in, want := range map[string]string{
		"ゼルダの伝説~風のタクト~": "ゼルダの伝説~風のタクト~",
		"X~":            "X",
		"A~B~C~":        "A~B~C",
		"A～B～":          "A～B～",
		"A~B～":          "A~B～",
	} {
		if got := trimWorkTitleEdges(in); got != want {
			t.Fatalf("%q → %q, want %q", in, got, want)
		}
	}
}

// TestInferTrackWorkFromTagsV6 pins the explicit-tag path: free-form composer
// names never inferred a work, "Soundtrack Album" must stop producing the
// "Album" work, and a lowercase "Ed" (Ed Sheeran) is not an ED marker (F4).
func TestInferTrackWorkFromTagsV6(t *testing.T) {
	for name, value := range map[string]string{
		"contentgroup soundtrack album": "Soundtrack Album",
		"grouping composer ryo":         "ryo",
		"grouping composer tanaka":      "Tanaka Hidekazu",
		"grouping composers pair":       "Nagatani Takao, Okabe Keiichi",
		"grouping ed sheeran":           "Ed Sheeran",
	} {
		t.Run(name, func(t *testing.T) {
			key := "GROUPING"
			if name == "contentgroup soundtrack album" {
				key = "CONTENTGROUP"
			}
			if got := InferTrackWorkFromTags(map[string][]string{key: {value}}, "Song"); len(got) != 0 {
				t.Fatalf("%s=%q inferred %+v", key, value, got)
			}
		})
	}
	// Uppercase standalone OP/ED still mark a role.
	got := InferTrackWorkFromTags(map[string][]string{"CONTENTGROUP": {"TVアニメ「葬送のフリーレン」OP1"}}, "勇者")
	if len(got) != 1 || got[0].Role != "op" {
		t.Fatalf("uppercase OP: %+v", got)
	}
}

// TestInferAlbumWorkV5Golden freezes the v5 behavior the rule-upgrade
// carryover (D71) matches against, including its known truncation bugs.
func TestInferAlbumWorkV5Golden(t *testing.T) {
	cases := []struct{ name, title string }{
		{"Cyberpunk: Edgerunners (Original Series Soundtrack)", "Cyberpunk: Edgerunners (Original Series"},
		{"Soundtrack Album", "Album"},
		{"Fate/stay night [Unlimited Blade Works] Original Soundtrack", "Fate/stay night [Unlimited Blade Works"},
		{"イースX -NORDICS- オリジナルサウンドトラック", "イースX -NORDICS"},
		{"Panty & Stocking with Garterbelt: The Original Soundtrack", "Panty & Stocking with Garterbelt: The"},
		{"キルラキル コンプリートサウンドトラック", "キルラキル コンプリート"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := InferAlbumWorkV5(tc.name, "", false)
			if !ok || got.Title != tc.title {
				t.Fatalf("v5 got %+v %v, want %q", got, ok, tc.title)
			}
		})
	}
	if got := InferTrackWorkFromTagsV5(map[string][]string{"CONTENTGROUP": {"Soundtrack Album"}}, "Song"); len(got) != 1 || got[0].Title != "Album" {
		t.Fatalf("v5 tags golden: %+v", got)
	}
	if got := InferTrackWorkFromTagsV5(map[string][]string{"GROUPING": {"Ed Sheeran"}}, "Song"); len(got) != 1 || got[0].Title != "Sheeran" {
		t.Fatalf("v5 Ed Sheeran golden: %+v", got)
	}
	if got, ok := InferAlbumWorkV5("TVアニメ「X」op1", "", false); !ok || got.Title != "X" || got.Role != "op" {
		t.Fatalf("v5 lowercase op1 golden: %+v %v", got, ok)
	}
}
