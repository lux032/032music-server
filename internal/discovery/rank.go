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
			workMatch := false
			for _, id := range a.WorkIDs {
				if v.SeedWorkID != 0 && id == v.SeedWorkID {
					has = true
					workMatch = true
				}
			}
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
			if workMatch {
				reason = "title_work"
			}
			if a.CurrentRG != "" && !workMatch {
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
func Rank(input []storage.DiscoveryCandidate, signals map[int64]storage.DiscoverySignals, ownership map[string]storage.Ownership, matches []storage.DiscoveryMatch, now time.Time, workSignalsArg ...map[int64]storage.DiscoverySignals) []storage.DiscoveryCandidate {
	workSignals := map[int64]storage.DiscoverySignals{}
	if len(workSignalsArg) > 0 {
		workSignals = workSignalsArg[0]
	}
	aff := map[string]float64{}
	for _, v := range input {
		key := seedKey(v)
		if v.SeedWorkID == 0 {
			aff[key] = Affinity(signals[v.SeedArtistID])
		} else {
			aff[key] = WorkAffinity(v.SeedWorkID, v.SeriesID, workSignals)
		}
	}
	top := map[string]bool{}
	for _, kind := range []string{"a:", "w:"} {
		keys := []string{}
		for key := range aff {
			if strings.HasPrefix(key, kind) {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if aff[keys[i]] != aff[keys[j]] {
				return aff[keys[i]] > aff[keys[j]]
			}
			return keys[i] < keys[j]
		})
		for i, key := range keys {
			if i < int(math.Ceil(float64(len(keys))*.3)) {
				top[key] = true
			}
		}
	}
	possible := map[string]bool{}
	for _, m := range matches {
		possible[m.RGID] = true
	}
	unique := map[string]storage.DiscoveryCandidate{}
	for _, v := range input {
		s := signals[v.SeedArtistID]
		if v.SeedWorkID != 0 {
			s = workSignals[v.SeedWorkID]
			s.FavArtist = false
		}
		v.Score = aff[seedKey(v)] * kindWeight(v.Kind) * recency(v.FirstReleaseDate, now)
		if v.SeedWorkID != 0 {
			v.Score *= RelationWeight(v.Relation)
		}
		v.Rank = 0
		v.Generation = 0
		v.Lane = "explore"
		if top[seedKey(v)] || s.FavArtist || s.Plays90d > 0 {
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
		if v.SeedWorkID != 0 {
			v.Reasons = nil
			if s.Plays90d > 0 {
				v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "work_plays", Work: v.WorkTitle, N: int(math.Ceil(s.Plays90d))})
			}
			if v.SeriesID != 0 && aff[seedKey(v)] > Affinity(s) {
				v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "work_series", Work: v.WorkTitle})
			}
			v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "work_relation", Work: v.WorkTitle, Relation: v.Relation})
			if v.Lane == "explore" {
				v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "explore_work", Work: v.WorkTitle})
			}
			if v.MappingStatus == "auto" {
				v.Reasons = append(v.Reasons, storage.DiscoveryReason{Code: "mapping_auto"})
			}
		}
		old, ok := unique[v.RGID]
		if !ok || v.Score > old.Score || v.Score == old.Score && v.SeedArtistID < old.SeedArtistID {
			if ok && discoverySourceKind(v) != discoverySourceKind(old) {
				v.Reasons = mergeReasons(v.Reasons, old.Reasons)
			}
			unique[v.RGID] = v
		} else {
			if discoverySourceKind(v) != discoverySourceKind(old) {
				old.Reasons = mergeReasons(old.Reasons, v.Reasons)
			}
			unique[v.RGID] = old
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
	counts := map[string]int{}
	take := func(pool *[]int) int {
		for len(*pool) > 0 {
			i := (*pool)[0]
			*pool = (*pool)[1:]
			if counts[capKey(all[i])] < MaxPerSeed {
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
		counts[capKey(all[i])]++
	}
	return all
}

func seedKey(v storage.DiscoveryCandidate) string {
	if v.SeedWorkID != 0 {
		return fmt.Sprintf("w:%d", v.SeedWorkID)
	}
	return fmt.Sprintf("a:%d", v.SeedArtistID)
}
func capKey(v storage.DiscoveryCandidate) string {
	if v.SeedWorkID != 0 && v.SeriesID != 0 {
		return fmt.Sprintf("s:%d", v.SeriesID)
	}
	return seedKey(v)
}
func mergeReasons(primary, secondary []storage.DiscoveryReason) []storage.DiscoveryReason {
	if len(secondary) > 0 {
		primary = append(primary, secondary[0])
	}
	if len(primary) > 3 {
		primary = primary[:3]
	}
	return primary
}

func discoverySourceKind(v storage.DiscoveryCandidate) string {
	if v.SeedWorkID != 0 {
		return "work"
	}
	return "artist"
}
func WorkAffinity(workID, seriesID int64, signals map[int64]storage.DiscoverySignals) float64 {
	bonus := 0.
	if seriesID != 0 {
		for id, v := range signals {
			if id != workID && v.SeriesID == seriesID {
				bonus = math.Max(bonus, Affinity(v))
			}
		}
	}
	return Affinity(signals[workID]) + .5*bonus
}
