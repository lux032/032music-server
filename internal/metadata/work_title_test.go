package metadata

import "testing"

func TestInferAlbumWorkAlbumFirst(t *testing.T) {
	cases := []struct{ name, title, typ, role string }{
		{"TVアニメ『葬送のフリーレン』Original Soundtrack", "葬送のフリーレン", "anime", "ost"},
		{"TVアニメ『葬送のフリーレン』Season2 Original Soundtrack", "葬送のフリーレン Season2", "anime", "ost"},
		{"Fate/Grand Order Original Soundtrack II", "Fate/Grand Order", "other", "ost"},
		{"FINAL FANTASY XV Original Soundtrack", "FINAL FANTASY XV", "other", "ost"},
		{"Cytus II Original Soundtrack", "Cytus II", "other", "ost"},
		{"Kingdom Hearts III Original Soundtrack", "Kingdom Hearts III", "other", "ost"},
		{"Xenoblade Chronicles X Original Soundtrack", "Xenoblade Chronicles X", "other", "ost"},
		{"X (Original Game Soundtrack)", "X", "other", "ost"},
		{"X Original Motion Picture Soundtrack", "X", "other", "ost"},
		{"X Original Soundtracks", "X", "other", "ost"},
		{"TVアニメ『SPY×FAMILY』キャラクターソング", "SPY×FAMILY", "anime", "character"},
		{"TVアニメ「A&B」オリジナルサウンドトラック", "A&B", "anime", "ost"},
		{"TVアニメ「A」＆TVアニメ「B」コレクション", "", "", ""},
		{"HUNTER×HUNTER Original Soundtrack", "HUNTER×HUNTER", "other", "ost"},
		{"TIGER & BUNNY Original Soundtrack", "TIGER & BUNNY", "other", "ost"},
		{"TVアニメ「X」キャラクターソングコレクション", "X", "anime", "character"},
		{"X Original Soundtrack Collection", "X", "other", "ost"},
		{"アニメ「X」ベストアルバム", "X", "anime", "other"},
		{"X テーマソング", "X", "anime", "theme"},
		{"GAME OVER", "", "", ""},
		{"Love Game", "", "", ""},
		{"Movie Star", "", "", ""},
		{"アニメじゃない", "", "", ""},
		{"Hollow Knight: Silksong - Official Soundtrack", "Hollow Knight: Silksong", "other", "ost"},
		{"NieR:Automata Original Soundtrack", "NieR:Automata", "other", "ost"},
		{"Sekiro: Shadows Die Twice: Original Soundtrack", "Sekiro: Shadows Die Twice", "other", "ost"},
		{"ENDER MAGNOLIA: Bloom in the Mist Original Soundtrack", "ENDER MAGNOLIA: Bloom in the Mist", "other", "ost"},
		{"オリジナル・サウンドトラック 屍姫 赫", "屍姫 赫", "other", "ost"},
		{"ドラマCD「エル・カザド」第1巻", "エル・カザド", "other", "other"},
		{"劇場版「ヴァイオレット・エヴァーガーデン」オリジナルサウンドトラック", "劇場版 ヴァイオレット・エヴァーガーデン", "movie", "ost"},
		{"ぼっち・ざ・ろっく! オリジナルサウンドトラックvol.1", "ぼっち・ざ・ろっく!", "other", "ost"},
		{"ゼルダの伝説~風のタクト~オリジナル・サウンド・トラックス", "ゼルダの伝説~風のタクト~", "other", "ost"},
		{"ペルソナ5 オリジナル・サウンドトラック", "ペルソナ5", "other", "ost"},
		{"TVアニメ「BRAVE10」オープニングテーマ", "BRAVE10", "anime", "op"},
		{"アニメ「こぴはん」オープニングテーマ「ループ」", "こぴはん", "anime", "op"},
		{"エンディングテーマ「感情グラス」上伊那ぼたん（CV.鈴代紗弓）", "", "", ""},
		{"「unforever」-「劇場版BEM～BECOME HUMAN～」主題歌-", "劇場版BEM～BECOME HUMAN～", "movie", "theme"},
		{"ブルーアーカイブ 青春あんさんぶる Vol.2 「ヴェリタス」", "", "", ""},
		{"ソウルイーター キャラクターソング①", "ソウルイーター", "anime", "character"},
		{"Free! キャラクターソング #03", "Free!", "anime", "character"},
		{"ドラマCD わたしたちの田村くん", "わたしたちの田村くん", "other", "other"},
		{"マリア様がみてる オープニングテーマ", "マリア様がみてる", "anime", "op"},
		{"りぜるまいん 主題歌", "りぜるまいん", "anime", "theme"},
		{"KILL LA KILL Complete Soundtrack", "KILL LA KILL", "other", "ost"},
		{"Cytus II‐Paff (original soundtrack)", "Cytus II-Paff", "other", "ost"},
		{"衛宮さんちの今日のごはん オリジナルサウンドトラック", "衛宮さんちの今日のごはん", "other", "ost"},
		{"TV Anime “Attack on Titan” Original Soundtrack", "Attack on Titan", "anime", "ost"},
		{"魔法使いの夜 ORIGINAL SOUNDTRACK REPETITION", "魔法使いの夜", "other", "ost"},
		{"Steins;Gate Symphonic Material", "", "", ""},
		{"プリキュア オープニングテーマコレクション2004~2016", "", "", ""},
		{"東方アレンジコレクション3", "", "", ""},
		{"アニソン神曲ROCK!!", "", "", ""},
		{"Penny Rain", "", "", ""},
		{"Memory-Go-Round", "", "", ""},
		{"POP | CULTURE 8", "", "", ""},
		{"FLICK", "", "", ""},
		{"GOLD", "", "", ""},
		{"Endless Summer", "", "", ""},
		{"Anison Piano2", "", "", ""},
		{"infinite synthesis 6", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := InferAlbumWork(tc.name, "", false)
			if ok != (tc.title != "") || (ok && (got.Title != tc.title || got.Type != tc.typ || got.Role != tc.role)) {
				t.Fatalf("got %+v %v, want %q %q %q", got, ok, tc.title, tc.typ, tc.role)
			}
		})
	}
}
func TestInferAlbumWorkCompilationWeakEvidence(t *testing.T) {
	cases := []struct{ title, want string }{{"TVアニメ『SPY×FAMILY』キャラクターソング", "SPY×FAMILY"}, {"TVアニメ「A&B」オリジナルサウンドトラック", "A&B"}, {"TVアニメ「A」＆TVアニメ「B」コレクション", ""}, {"HUNTER×HUNTER Original Soundtrack", "HUNTER×HUNTER"}, {"TIGER & BUNNY Original Soundtrack", "TIGER & BUNNY"}, {"TVアニメ「X」キャラクターソングコレクション", "X"}, {"X Original Soundtrack Collection", "X"}, {"アニメ「X」ベストアルバム", "X"}, {"プリキュア オープニングテーマコレクション2004~2016", ""}, {"PERSONA DANCING P3D & P5D SOUND TRACKS -ADVANCED CD COLLECTOR'S BOX-", ""}}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got, ok := InferAlbumWork(tc.title, "", true)
			if ok != (tc.want != "") || ok && got.Title != tc.want {
				t.Fatalf("compilation %+v %v, want %q", got, ok, tc.want)
			}
		})
	}
}
func TestInferTrackWorkFromTagsWhitelist(t *testing.T) {
	for _, tag := range []string{"DESCRIPTION", "SUBTITLE", "COMMENT"} {
		if got := InferTrackWorkFromTags(map[string][]string{tag: {"TVアニメ『葬送のフリーレン』Original Soundtrack"}}, "Opening"); len(got) != 0 {
			t.Fatalf("%s: %+v", tag, got)
		}
	}
	if got := InferTrackWorkFromTags(map[string][]string{"WORK": {"Opening"}}, "Opening"); len(got) != 0 {
		t.Fatalf("self WORK: %+v", got)
	}
	if got := InferTrackWorkFromTags(map[string][]string{"WORKTITLE": {"TVアニメ『葬送のフリーレン』Original Soundtrack"}}, "Opening"); len(got) != 1 {
		t.Fatalf("explicit WORKTITLE: %+v", got)
	}
}
