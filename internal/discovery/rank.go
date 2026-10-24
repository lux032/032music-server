package discovery

import (
	"crypto/sha256"
	"fmt"
	"github.com/lux032/032music-server/internal/storage"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	MaxPerArtist        = 3
	RecommendationLimit = 60
)

var versionPatterns = []*regexp.Regexp{regexp.MustCompile(`(?i)(初回限定|通常盤|限定盤|期間生産|deluxe|edition|remaster|\btype[ -]?[a-z]\b|盤)`)}
var versionBrackets = [][2]string{{"(", ")"}, {"[", "]"}, {"【", "】"}, {"〔", "〕"}}

func NormalizeTitle(title string) string {
	title = norm.NFKC.String(title)
	for {
		changed := false
		title = strings.TrimSpace(title)
		for _, pair := range versionBrackets {
			if !strings.HasSuffix(title, pair[1]) {
				continue
			}
			i := strings.LastIndex(title, pair[0])
			if i < 0 {
				continue
			}
			inside := title[i+len(pair[0]) : len(title)-len(pair[1])]
			for _, p := range versionPatterns {
				if p.MatchString(inside) {
					title = title[:i]
					changed = true
					break
				}
			}
			if changed {
				break
			}
		}
		if !changed {
			break
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return r
	}, cases.Fold().String(title))
}
func WeakMatches(items []storage.DiscoveryCandidate, albums []storage.DiscoveryLocalAlbum, rejected map[string]map[int64]bool) []storage.DiscoveryMatch {
	out := []storage.DiscoveryMatch{}
	seen := map[string]bool{}
	for _, v := range items {
		title := NormalizeTitle(v.Title)
		if title == "" {
			continue
		}
		for _, a := range albums {
			if a.CurrentRG == v.RGID || rejected[v.RGID][a.ID] || NormalizeTitle(a.Title) != title {
				continue
			}
			has := false
			for _, id := range a.ArtistIDs {
				if id == v.SeedArtistID {
					has = true
				}
			}
			key := fmt.Sprintf("%s:%d", v.RGID, a.ID)
			if !has || seen[key] {
				continue
			}
			seen[key] = true
			reason := "title_artist"
			if a.CurrentRG != "" {
				reason = "other_rg"
			}
			out = append(out, storage.DiscoveryMatch{RGID: v.RGID, AlbumID: a.ID, Reason: reason})
		}
	}
	return out
}
func Affinity(v storage.DiscoverySignals) float64 {
	fav := 0.
	if v.FavArtist {
		fav = 4
	}
	return 3*math.Log1p(v.Plays90d) + math.Log1p(float64(v.PlaysAll)) + fav + 1.5*math.Log1p(float64(v.FavAlbums)) + math.Log1p(float64(v.FavTracks)) + .5*math.Log1p(float64(v.OwnedAlbums))
}
func kindWeight(kind string) float64 {
	return map[string]float64{"Album": 1, "OST": .9, "EP": .8, "Single": .6}[kind]
}
func recency(date string, now time.Time) float64 {
	d := ReleaseDate(date)
	if d.IsZero() {
		return .7
	}
	if d.Year() >= now.Year()-2 {
		return 1.15
	}
	return 1
}

// Rank retains excluded/unselected candidates with rank=0 (stored as SQL NULL).
func Rank(input []storage.DiscoveryCandidate, signals map[int64]storage.DiscoverySignals, ownership map[string]storage.Ownership, matches []storage.DiscoveryMatch, now time.Time) []storage.DiscoveryCandidate {
	aff := map[int64]float64{}
	for _, v := range input {
		aff[v.SeedArtistID] = Affinity(signals[v.SeedArtistID])
	}
	artists := []int64{}
	for id := range aff {
		artists = append(artists, id)
	}
	sort.Slice(artists, func(i, j int) bool {
		if aff[artists[i]] != aff[artists[j]] {
			return aff[artists[i]] > aff[artists[j]]
		}
		return artists[i] < artists[j]
	})
	top := map[int64]bool{}
	for i, id := range artists {
		if i < int(math.Ceil(float64(len(artists))*.3)) {
			top[id] = true
		}
	}
	possible := map[string]bool{}
	for _, m := range matches {
		possible[m.RGID] = true
	}
	unique := map[string]storage.DiscoveryCandidate{}
	for _, v := range input {
		s := signals[v.SeedArtistID]
		v.Score = aff[v.SeedArtistID] * kindWeight(v.Kind) * recency(v.FirstReleaseDate, now)
		v.Rank = 0
		v.Generation = 0
		v.Lane = "explore"
		if top[v.SeedArtistID] || s.FavArtist || s.Plays90d > 0 {
			v.Lane = "exploit"
		}
		v.Reasons = []storage.DiscoveryReason{}
		if s.FavArtist {
			v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "favorite_artist", Artist: v.Artist})
		}
		if s.Plays90d > 0 {
			v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "plays_90d", Artist: v.Artist, N: int(math.Ceil(s.Plays90d))})
		}
		if s.OwnedAlbums > 0 {
			v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "owned_albums", Artist: v.Artist, N: s.OwnedAlbums})
		}
		if d := ReleaseDate(v.FirstReleaseDate); !d.IsZero() && d.Year() >= now.Year()-2 {
			v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "new_release", Year: d.Year()})
		}
		if v.Lane == "explore" {
			v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "explore", Artist: v.Artist})
		}
		old, ok := unique[v.RGID]
		if !ok || v.Score > old.Score || v.Score == old.Score && v.SeedArtistID < old.SeedArtistID {
			unique[v.RGID] = v
		}
	}
	all := []storage.DiscoveryCandidate{}
	for _, v := range unique {
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].RGID < all[j].RGID
	})
	exploit, explore := []int{}, []int{}
	for i, v := range all {
		o := ownership[v.RGID]
		if len(o.Albums) > 0 || o.Ignored || possible[v.RGID] {
			continue
		}
		if v.Lane == "exploit" {
			exploit = append(exploit, i)
		} else {
			explore = append(explore, i)
		}
	}
	sort.SliceStable(explore, func(i, j int) bool {
		a, b := all[explore[i]], all[explore[j]]
		wa, wb := kindWeight(a.Kind)*recency(a.FirstReleaseDate, now), kindWeight(b.Kind)*recency(b.FirstReleaseDate, now)
		if wa != wb {
			return wa > wb
		}
		ha, hb := sha256.Sum256([]byte(a.RGID+now.Format("2006-01-02"))), sha256.Sum256([]byte(b.RGID+now.Format("2006-01-02")))
		return string(ha[:]) < string(hb[:])
	})
	counts := map[int64]int{}
	take := func(pool *[]int) int {
		for len(*pool) > 0 {
			i := (*pool)[0]
			*pool = (*pool)[1:]
			if counts[all[i].SeedArtistID] < MaxPerArtist {
				return i
			}
		}
		return -1
	}
	for rank := 1; rank <= RecommendationLimit; rank++ {
		first, second := &exploit, &explore
		if rank%5 == 0 {
			first, second = second, first
		}
		i := take(first)
		if i < 0 {
			i = take(second)
		}
		if i < 0 {
			break
		}
		all[i].Rank = rank
		counts[all[i].SeedArtistID]++
	}
	return all
}
