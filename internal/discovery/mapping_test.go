package discovery

import (
	"fmt"
	"github.com/lux032/032music-server/internal/bangumi"
	"github.com/lux032/032music-server/internal/discovery/mb"
	"github.com/lux032/032music-server/internal/storage"
	"math"
	"testing"
	"time"
)

func TestClassifyMapping(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := bangumi.MusicSubject{ID: 1, Name: "君の名は。", Date: "2016-08-24", Artists: []string{"RADWIMPS"}}
	hit := mb.ReleaseGroup{ID: testRG, Title: base.Name, PrimaryType: "Album", FirstReleaseDate: base.Date, ArtistCredit: []mb.Credit{{Name: "RADWIMPS"}}}
	for _, tc := range []struct {
		name           string
		mutate         func(*bangumi.MusicSubject, *mb.ReleaseGroup)
		sibling, extra bool
		want           string
	}{
		{name: "strict", want: "auto"},
		{name: "punctuation", mutate: func(s *bangumi.MusicSubject, r *mb.ReleaseGroup) { s.Name = "君の名は" }, want: "suggested"},
		{name: "multi title", mutate: func(s *bangumi.MusicSubject, r *mb.ReleaseGroup) { s.Name = "A / B"; r.Title = s.Name }, want: "suggested"},
		{name: "sibling", sibling: true, want: "suggested"},
		{name: "duplicate hit", extra: true, want: "suggested"},
		{name: "year only", mutate: func(s *bangumi.MusicSubject, r *mb.ReleaseGroup) { s.Date = "2016" }, want: "suggested"},
		{name: "no artist", mutate: func(s *bangumi.MusicSubject, r *mb.ReleaseGroup) { s.Artists = nil }, want: "suggested"},
		{name: "filtered", mutate: func(s *bangumi.MusicSubject, r *mb.ReleaseGroup) {
			r.SecondaryTypes = []string{"Compilation", "Soundtrack"}
		}},
		{name: "different", mutate: func(s *bangumi.MusicSubject, r *mb.ReleaseGroup) { r.Title = "different" }},
		{name: "casefold NFKC", mutate: func(s *bangumi.MusicSubject, r *mb.ReleaseGroup) { s.Name = "Ａ Ｂ"; r.Title = "a b" }, want: "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := base, hit
			if tc.mutate != nil {
				tc.mutate(&s, &r)
			}
			hits := []mb.ReleaseGroup{r}
			if tc.extra {
				r.ID = "22222222-2222-2222-2222-222222222222"
				hits = append(hits, r)
			}
			got := ClassifyMapping(s, hits, tc.sibling, now)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatal(got)
				}
				return
			}
			if len(got) == 0 || got[0].Status != tc.want {
				t.Fatal(got)
			}
		})
	}
	a := ClassifyMapping(base, []mb.ReleaseGroup{hit}, false, now)
	base.ID = 2
	b := ClassifyMapping(base, []mb.ReleaseGroup{hit}, false, now)
	all := append(a, b...)
	reconcileMappingConflicts(all)
	for _, m := range all {
		if m.Status != "suggested" || m.Evidence[len(m.Evidence)-1] != "rg_conflict" {
			t.Fatal(all)
		}
	}
}
func TestRelationWeights(t *testing.T) {
	for relation, want := range map[string]float64{"原声集": 1, "片头曲": .9, "片尾曲": .9, "主题歌": .9, "插入歌": .8, "角色歌": .6, "印象曲": .6, "意象歌": .6, "其他": 0, "广播剧": 0, "unknown": 0} {
		if got := RelationWeight(relation); got != want {
			t.Fatalf("%s: %v", relation, got)
		}
	}
}

func TestRankWorkSourcesSeriesCapAndTitleWork(t *testing.T) {
	now := time.Now()
	signals := map[int64]storage.DiscoverySignals{1: {Plays90d: 10}}
	works := map[int64]storage.DiscoverySignals{1: {Plays90d: 100}, 2: {Plays90d: 90}}
	input := []storage.DiscoveryCandidate{}
	for i := 0; i < 6; i++ {
		work := int64(1 + i%2)
		input = append(input, storage.DiscoveryCandidate{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: fmt.Sprintf("rg%d", i), Title: fmt.Sprint("Title", i), Kind: "Album"}, SeedKind: "work", SeedWorkID: work, SeriesID: 9, WorkTitle: fmt.Sprint("Work", work), Relation: "原声集", MappingStatus: "auto"})
	}
	input = append(input, storage.DiscoveryCandidate{DiscoveryReleaseGroup: input[0].DiscoveryReleaseGroup, SeedArtistID: 1, Artist: "Singer"})
	ranked := Rank(input, signals, nil, nil, now, works)
	selected := 0
	for _, v := range ranked {
		if v.Rank > 0 {
			selected++
		}
		if v.RGID == "rg0" {
			if v.SeedWorkID == 0 || len(v.Reasons) > 3 || v.Reasons[0].Code != "work_plays" {
				t.Fatal(v)
			}
		}
	}
	if len(ranked) != 6 || selected != 3 {
		t.Fatal(ranked)
	}
	matches := WeakMatches(input, []storage.DiscoveryLocalAlbum{{ID: 1, Title: "Title0", WorkIDs: []int64{1}}}, nil)
	if len(matches) != 1 || matches[0].Reason != "title_work" {
		t.Fatal(matches)
	}
}

func TestRankMixedPoolQuota(t *testing.T) {
	input := []storage.DiscoveryCandidate{}
	artists := map[int64]storage.DiscoverySignals{}
	works := map[int64]storage.DiscoverySignals{}
	for id := int64(1); id <= 40; id++ {
		artists[id] = storage.DiscoverySignals{OwnedAlbums: 1, FavArtist: id <= 20}
		works[id] = storage.DiscoverySignals{OwnedAlbums: 1}
		if id <= 20 {
			works[id] = storage.DiscoverySignals{Plays90d: 10}
		}
		for n := 0; n < 3; n++ {
			input = append(input, storage.DiscoveryCandidate{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: fmt.Sprintf("a%d-%d", id, n), Kind: "Album"}, SeedArtistID: id}, storage.DiscoveryCandidate{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: fmt.Sprintf("w%d-%d", id, n), Kind: "Album"}, SeedWorkID: id, Relation: "原声集"})
		}
	}
	out := Rank(input, artists, nil, nil, time.Now(), works)
	ranks := map[int]string{}
	counts := map[string]int{}
	for _, v := range out {
		if v.Rank > 0 {
			ranks[v.Rank] = v.Lane
			counts[capKey(v)]++
		}
	}
	if len(ranks) != 60 {
		t.Fatal(len(ranks))
	}
	for r, lane := range ranks {
		want := "exploit"
		if r%5 == 0 {
			want = "explore"
		}
		if lane != want {
			t.Fatal(r, lane)
		}
	}
	for key, n := range counts {
		if n > 3 {
			t.Fatal(key, n)
		}
	}
}
func TestRankSeriesAffinityIncludesWorksWithoutCandidates(t *testing.T) {
	input := []storage.DiscoveryCandidate{{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: "x", Kind: "Album"}, SeedWorkID: 1, SeriesID: 5, Relation: "原声集"}}
	signals := map[int64]storage.DiscoverySignals{1: {SeriesID: 5}, 2: {SeriesID: 5, Plays90d: 100}}
	out := Rank(input, nil, nil, nil, time.Now(), signals)
	want := .5 * Affinity(signals[2]) * .7
	if len(out) != 1 || math.Abs(out[0].Score-want) > .00001 {
		t.Fatal(out, want)
	}
}
func TestMergeReasonsKeepsPrimaryOrder(t *testing.T) {
	primary := []storage.DiscoveryReason{{Code: "one"}, {Code: "two"}, {Code: "three"}}
	secondary := []storage.DiscoveryReason{{Code: "other"}}
	got := mergeReasons(primary, secondary)
	if len(got) != 3 || got[2].Code != "three" {
		t.Fatal(got)
	}
	got = mergeReasons(primary[:2], secondary)
	if len(got) != 3 || got[2].Code != "other" {
		t.Fatal(got)
	}
}

func TestRankDoesNotMergeSameSourceReasons(t *testing.T) {
	group := storage.DiscoveryReleaseGroup{RGID: "same", Kind: "Album"}
	input := []storage.DiscoveryCandidate{{DiscoveryReleaseGroup: group, SeedArtistID: 1, Artist: "First"}, {DiscoveryReleaseGroup: group, SeedArtistID: 2, Artist: "Second"}}
	out := Rank(input, map[int64]storage.DiscoverySignals{1: {FavArtist: true}, 2: {Plays90d: 1}}, nil, nil, time.Now())
	if len(out) != 1 {
		t.Fatal(out)
	}
	for _, r := range out[0].Reasons {
		if r.Artist != "" && r.Artist != out[0].Artist {
			t.Fatal(out)
		}
	}
}
