package bangumi

import "testing"

func TestArtistTokens(t *testing.T) {
	tokens := ArtistTokens(" ＬｉＳＡ feat. Straße / A B ")
	for _, key := range []string{"lisa", "strasse", "ab"} {
		if !tokens[key] {
			t.Fatalf("missing %s: %v", key, tokens)
		}
	}
}
func TestSplitMultiTitle(t *testing.T) {
	for _, s := range []string{"A / B", "A／B", "Fate/stay night"} {
		if len(SplitMultiTitle(s)) != 2 {
			t.Fatal(s)
		}
	}
	if SplitMultiTitle("single") != nil {
		t.Fatal("single title split")
	}
}
func TestRelationRole(t *testing.T) {
	for label, want := range map[string]string{"片头曲": "op", "片尾曲": "ed", "插入歌": "insert", "原声集": "ost", "角色歌": "character", "主题歌": "theme", "印象曲": "image_song", "意象歌": "image_song", "其他": "other"} {
		if got := RelationRole(label); got != want {
			t.Fatalf("%s: %s", label, got)
		}
	}
}
