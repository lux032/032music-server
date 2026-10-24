package discovery

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/lux032/032music-server/internal/bangumi"
	"github.com/lux032/032music-server/internal/discovery/mb"
	"github.com/lux032/032music-server/internal/storage"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxWorkSeeds             = 24
	MaxMusicPerWork          = 6
	MaxMBSearchesPerRefresh  = 120
	MappingTTL               = 30 * 24 * time.Hour
	MaxSuggestionsPerSubject = 3
	MaxPerSeed               = 3
)

func RelationWeight(relation string) float64 {
	return map[string]float64{"原声集": 1, "片头曲": .9, "片尾曲": .9, "主题歌": .9, "插入歌": .8, "角色歌": .6, "印象曲": .6, "意象歌": .6}[relation]
}
func strictTitle(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, cases.Fold().String(norm.NFKC.String(s)))
}
func artistOverlap(subject bangumi.MusicSubject, rg mb.ReleaseGroup) bool {
	tokens := map[string]bool{}
	for _, a := range subject.Artists {
		for t := range bangumi.ArtistTokens(a) {
			tokens[t] = true
		}
	}
	for _, c := range rg.ArtistCredit {
		for _, a := range []string{c.Name, c.Artist.Name} {
			for t := range bangumi.ArtistTokens(a) {
				if tokens[t] {
					return true
				}
			}
		}
	}
	return false
}

// ClassifyMapping considers only eligible results; callers reconcile cross-subject RG conflicts.
func ClassifyMapping(subject bangumi.MusicSubject, hits []mb.ReleaseGroup, duplicateSibling bool, now time.Time) []storage.DiscoveryMapping {
	eligible := []mb.ReleaseGroup{}
	same := 0
	title := NormalizeTitle(subject.Name)
	for _, rg := range hits {
		if _, ok := Eligible(rg, now); ok {
			eligible = append(eligible, rg)
			if NormalizeTitle(rg.Title) == title {
				same++
			}
		}
	}
	out := []storage.DiscoveryMapping{}
	multi := bangumi.SplitMultiTitle(subject.Name) != nil
	for _, rg := range eligible {
		normalized := NormalizeTitle(rg.Title)
		strict := strictTitle(subject.Name) == strictTitle(rg.Title)
		equal := title != "" && normalized == title
		overlap := artistOverlap(subject, rg)
		contains := title != "" && normalized != "" && (strings.Contains(title, normalized) || strings.Contains(normalized, title))
		if !equal && !(contains && overlap) {
			continue
		}
		v := storage.DiscoveryMapping{SubjectID: subject.ID, RGID: rg.ID, Status: "suggested"}
		if strict {
			v.Score += 50
			v.Evidence = append(v.Evidence, "title_strict")
		} else if equal {
			v.Score += 35
			v.Evidence = append(v.Evidence, "title_normalized")
		} else {
			v.Score += 15
			v.Evidence = append(v.Evidence, "title_contains")
		}
		if overlap {
			v.Score += 30
			v.Evidence = append(v.Evidence, "artist_overlap")
		}
		day := false
		if len(subject.Date) == 10 {
			if _, e := time.Parse("2006-01-02", subject.Date); e == nil && subject.Date == rg.FirstReleaseDate {
				day = true
			}
		}
		if day {
			v.Score += 20
			v.Evidence = append(v.Evidence, "date_day")
		} else if len(subject.Date) >= 7 && len(rg.FirstReleaseDate) >= 7 && subject.Date[:7] == rg.FirstReleaseDate[:7] {
			v.Score += 12
			v.Evidence = append(v.Evidence, "date_month")
		} else if len(subject.Date) >= 4 && len(rg.FirstReleaseDate) >= 4 && subject.Date[:4] == rg.FirstReleaseDate[:4] {
			v.Score += 6
			v.Evidence = append(v.Evidence, "date_year")
		}
		if multi {
			v.Evidence = append(v.Evidence, "multi_title")
		}
		if duplicateSibling {
			v.Evidence = append(v.Evidence, "duplicate_sibling")
		}
		if same > 1 {
			v.Evidence = append(v.Evidence, "duplicate_hit")
		}
		if !multi && strict && overlap && day && same == 1 && !duplicateSibling {
			v.Status = "auto"
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].RGID < out[j].RGID
	})
	if len(out) > MaxSuggestionsPerSubject {
		out = out[:MaxSuggestionsPerSubject]
	}
	return out
}
func reconcileMappingConflicts(mappings []storage.DiscoveryMapping) {
	subjects := map[string]map[int64]bool{}
	for _, m := range mappings {
		if m.Status == "auto" {
			if subjects[m.RGID] == nil {
				subjects[m.RGID] = map[int64]bool{}
			}
			subjects[m.RGID][m.SubjectID] = true
		}
	}
	for i := range mappings {
		if mappings[i].Status == "auto" && len(subjects[mappings[i].RGID]) > 1 {
			mappings[i].Status = "suggested"
			mappings[i].Evidence = append(mappings[i].Evidence, "rg_conflict")
		}
	}
}
