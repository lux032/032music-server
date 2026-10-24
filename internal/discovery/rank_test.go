package discovery

import (
	"fmt"
	"github.com/lux032/032music-server/internal/discovery/mb"
	"github.com/lux032/032music-server/internal/storage"
	"testing"
	"time"
)

func TestEligible(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.Local)
	cases := []struct {
		p       string
		s       []string
		d, kind string
		ok      bool
	}{{"Album", nil, "", "Album", true}, {"EP", nil, "bad", "EP", true}, {"Single", nil, "2026", "Single", true}, {"Album", []string{"Soundtrack"}, "2026-05-02", "OST", true}, {"Album", nil, "2026-05-03", "Album", false}, {"Album", nil, "2026-06", "Album", false}, {"Album", nil, "2027", "Album", false}, {"EP", []string{"Soundtrack"}, "", "", false}, {"Single", []string{"Soundtrack"}, "", "", false}, {"Broadcast", nil, "", "", false}, {"Album", []string{"Compilation", "Soundtrack"}, "", "", false}}
	for _, s := range []string{"Live", "Compilation", "Remix", "DJ-mix", "Audio drama", "Spokenword", "Interview", "Demo", "Mixtape"} {
		cases = append(cases, struct {
			p       string
			s       []string
			d, kind string
			ok      bool
		}{"Album", []string{s}, "", "", false})
	}
	for _, v := range cases {
		k, ok := Eligible(mb.ReleaseGroup{PrimaryType: v.p, SecondaryTypes: v.s, FirstReleaseDate: v.d}, now)
		if k != v.kind || ok != v.ok {
			t.Errorf("%+v got %s %v", v, k, ok)
		}
	}
	if recency("", now) != .7 {
		t.Fatal("unknown date penalty")
	}
}
func TestNormalizeTitleAndWeakMatches(t *testing.T) {
	for _, s := range []string{"ＴＨＥ　ＢＯＯＫ（初回限定盤）", "The Book [Deluxe Edition] (Remaster)", "the book【通常盤】", "THE BOOK〔Type-A〕"} {
		if v := NormalizeTitle(s); v != "thebook" {
			t.Errorf("%q: %q", s, v)
		}
	}
	if NormalizeTitle("The Book 2") == NormalizeTitle("The Book") {
		t.Fatal("substring matching")
	}
	items := []storage.DiscoveryCandidate{{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: "x", Title: "The Book"}, SeedArtistID: 1}}
	albums := []storage.DiscoveryLocalAlbum{{ID: 2, Title: "THE BOOK (初回限定盤)", ArtistIDs: []int64{1}, CurrentRG: "other"}}
	m := WeakMatches(items, albums, nil)
	if len(m) != 1 || m[0].Reason != "other_rg" {
		t.Fatal(m)
	}
	if len(WeakMatches(items, albums, map[string]map[int64]bool{"x": {2: true}})) != 0 {
		t.Fatal("rejection ignored")
	}
}
func TestRankQuotaAndPerArtistCap(t *testing.T) {
	in := []storage.DiscoveryCandidate{}
	signals := map[int64]storage.DiscoverySignals{}
	for a := 1; a <= 40; a++ {
		id := int64(a)
		signals[id] = storage.DiscoverySignals{OwnedAlbums: 1, FavArtist: a <= 20}
		for j := 0; j < 5; j++ {
			in = append(in, storage.DiscoveryCandidate{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: fmt.Sprintf("%02d-%d", a, j), Kind: "Album"}, SeedArtistID: id})
		}
	}
	out := Rank(in, signals, nil, nil, time.Now())
	counts := map[int64]int{}
	ranks := map[int]string{}
	for _, v := range out {
		if v.Rank > 0 {
			counts[v.SeedArtistID]++
			ranks[v.Rank] = v.Lane
		}
	}
	if len(ranks) != 60 {
		t.Fatal(len(ranks))
	}
	for id, n := range counts {
		if n > 3 {
			t.Fatal(id, n)
		}
	}
	for r := 1; r <= 60; r++ {
		want := "exploit"
		if r%5 == 0 {
			want = "explore"
		}
		if ranks[r] != want {
			t.Fatal(r, ranks[r], want)
		}
	}
}

func TestRankPoolExhaustionAndOwnershipExclusions(t *testing.T) {
	now := time.Now()
	input := []storage.DiscoveryCandidate{}
	signals := map[int64]storage.DiscoverySignals{}
	for a := int64(1); a <= 4; a++ {
		signals[a] = storage.DiscoverySignals{FavArtist: true}
		for n := 0; n < 3; n++ {
			input = append(input, storage.DiscoveryCandidate{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: fmt.Sprintf("%d-%d", a, n), Kind: "Album"}, SeedArtistID: a})
		}
	}
	owned := map[string]storage.Ownership{"1-0": {Albums: []int64{1}}, "1-1": {Ignored: true}}
	matches := []storage.DiscoveryMatch{{RGID: "1-2", AlbumID: 2}}
	out := Rank(input, signals, owned, matches, now)
	ranks := map[int]bool{}
	for _, v := range out {
		if v.SeedArtistID == 1 && v.Rank != 0 {
			t.Fatal("excluded candidate ranked", v)
		}
		if v.Rank > 0 {
			ranks[v.Rank] = true
		}
	}
	if len(ranks) != 9 || !ranks[9] {
		t.Fatal("exhausted explore pool did not fall back", ranks)
	}
}

func TestDiscoveryNormalizeTitleTypeBoundaries(t *testing.T) {
	for _, title := range []string{"Song (Prototype Mix)", "Song (Typeface)"} {
		if NormalizeTitle(title) == NormalizeTitle("Song") {
			t.Fatal("stripped meaningful suffix", title)
		}
	}
	if NormalizeTitle("Song (Type-A)") != NormalizeTitle("Song") {
		t.Fatal("version not stripped")
	}
}
