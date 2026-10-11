package httpapi

import (
	"context"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// home_rotation.go implements the daily rotation of the home page's
// “按作品漫游” and “主题曲与插曲” sections.
//
// Contract:
//   - Each view (works:all / works:anime / … / songs) is built once per
//     calendar day (homeZone) on first request and then frozen: candidates,
//     play-history weights and the full page split are kept in memory, so
//     listening during the day, new imports or link edits never reshuffle
//     pages already being browsed. A process restart starts a new generation.
//   - The order is a seeded weighted permutation of the whole pool
//     (Efraimidis–Spirakis, key = -ln(U)/w), so “换一批” walks pages that never
//     repeat until every candidate was shown, then wraps to page 0.
//   - Requests carry the generation they were rendered from; a stale one
//     (new day or restart) is answered with page 0 of the current generation.

// homeZone fixes the shared rotation day boundary to UTC+8.
// The header's server fallback uses this zone; browsers display their local day.
// China has no DST, so a fixed zone is exact and needs no tzdata in the image.
var homeZone = time.FixedZone("UTC+8", 8*60*60)

const (
	homeWorksPageSize    = 24
	homeWorksTypeQuota   = 3
	homeSongsPageSize    = 20
	homeSongsPerWork     = 2
	homeRecentPlayWindow = 7 * 24 * time.Hour
	homeRecentPlayFactor = 0.3
	homeUnplayedFactor   = 1.5
	homeKeySongRoleBoost = 3.0
)

// homeWorkFilters are the type filter buttons of the works rail besides
// “全部”; they are also the types guaranteed a few cards on page 0 of “全部”.
var homeWorkFilters = []string{"anime", "game", "movie"}

func homeWorkFilterValid(key string) bool {
	if key == "all" {
		return true
	}
	for _, value := range homeWorkFilters {
		if value == key {
			return true
		}
	}
	return false
}

// homeRotationView is one frozen view of the day.
type homeRotationView struct {
	Generation string
	Count      int // works in the pool, or songs across all pages
	workPages  [][]int64
	songPages  [][]storage.HomeRolePick
}

func (v *homeRotationView) Pages() int {
	if v.workPages != nil {
		return len(v.workPages)
	}
	return len(v.songPages)
}

// homeRotation caches the views of the current day.
type homeRotation struct {
	mu    sync.Mutex
	now   func() time.Time
	nonce string
	day   string
	views map[string]*homeRotationView
}

func newHomeRotation(now func() time.Time) *homeRotation {
	return &homeRotation{now: now, nonce: strconv.FormatInt(time.Now().UnixNano()%1_000_000_007, 36), views: map[string]*homeRotationView{}}
}

func homeDay(t time.Time) string { return t.In(homeZone).Format("2006-01-02") }

// view returns the frozen view for key, building it once per day. A failed
// build is not cached. Building holds the lock: the pools are small and this
// keeps concurrent first requests from building twice.
func (r *homeRotation) view(key string, build func(day string, now time.Time) (*homeRotationView, error)) (*homeRotationView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	day := homeDay(now)
	if day != r.day {
		r.day = day
		r.views = map[string]*homeRotationView{}
	}
	if view, ok := r.views[key]; ok {
		return view, nil
	}
	view, err := build(day, now)
	if err != nil {
		return nil, err
	}
	view.Generation = day + "." + r.nonce
	r.views[key] = view
	return view, nil
}

// homeRNG derives an independent, reproducible random source per day/view.
func homeRNG(day, key string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(day + "|" + key))
	seed := h.Sum64()
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

func homePlayFactor(played, recent bool) float64 {
	switch {
	case recent:
		return homeRecentPlayFactor
	case !played:
		return homeUnplayedFactor
	}
	return 1
}

// homeWeightedOrder returns indices 0..n-1 in weighted random order without
// replacement: heavier items tend to come first, every item appears once.
// One draw per index in index order keeps it reproducible; ties keep index
// order. Non-positive weights sort last.
func homeWeightedOrder(rng *rand.Rand, weights []float64) []int {
	keys := make([]float64, len(weights))
	order := make([]int, len(weights))
	for i, w := range weights {
		u := 1 - rng.Float64() // (0,1]
		if w > 0 {
			keys[i] = -math.Log(u) / w
		} else {
			keys[i] = math.Inf(1)
		}
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return keys[order[a]] < keys[order[b]] })
	return order
}

// homeWorkPages splits the works pool into rotation pages. typeFilter ""
// means “全部”: page 0 then holds up to quota works of each quotaTypes type
// (fewer when the type has fewer), filled up by weight; later pages hold the
// rest in weighted order, so the full walk shows every work exactly once.
func homeWorkPages(rng *rand.Rand, candidates []storage.HomeWorkCandidate, typeFilter string, pageSize, quota int, quotaTypes []string) [][]int64 {
	pool := make([]storage.HomeWorkCandidate, 0, len(candidates))
	for _, c := range candidates {
		if typeFilter == "" || c.Type == typeFilter {
			pool = append(pool, c)
		}
	}
	weights := make([]float64, len(pool))
	for i, c := range pool {
		weights[i] = math.Log1p(float64(c.TrackCount)) * homePlayFactor(c.Played, c.RecentlyPlayed)
	}
	order := homeWeightedOrder(rng, weights)
	if typeFilter == "" && quota > 0 && len(order) > pageSize {
		first := map[int]bool{}
		for _, typ := range quotaTypes {
			taken := 0
			for _, idx := range order {
				if taken >= quota || len(first) >= pageSize {
					break
				}
				if pool[idx].Type == typ {
					first[idx] = true
					taken++
				}
			}
		}
		for _, idx := range order {
			if len(first) >= pageSize {
				break
			}
			first[idx] = true
		}
		head := make([]int, 0, pageSize)
		tail := make([]int, 0, len(order)-pageSize)
		for _, idx := range order {
			if first[idx] {
				head = append(head, idx)
			} else {
				tail = append(tail, idx)
			}
		}
		order = append(head, tail...)
	}
	var pages [][]int64
	for start := 0; start < len(order); start += pageSize {
		end := min(start+pageSize, len(order))
		page := make([]int64, 0, end-start)
		for _, idx := range order[start:end] {
			page = append(page, pool[idx].ID)
		}
		pages = append(pages, page)
	}
	return pages
}

// homeSongPages builds the “主题曲与插曲” pages: works in uniform random
// order (discovery: a work with many songs is not favoured), up to perWork
// songs per work (OP/ED softly preferred, played songs softly demoted), a
// track shown once per generation even when linked to several works, and a
// work's songs never split across pages (a page may hold fewer than
// pageSize). Songs of a work beyond perWork are not shown that day.
func homeSongPages(rng *rand.Rand, candidates []storage.HomeRoleCandidate, pageSize, perWork int) [][]storage.HomeRolePick {
	byWork := map[int64][]storage.HomeRoleCandidate{}
	workIDs := make([]int64, 0)
	for _, c := range candidates {
		if _, ok := byWork[c.WorkID]; !ok {
			workIDs = append(workIDs, c.WorkID)
		}
		byWork[c.WorkID] = append(byWork[c.WorkID], c)
	}
	sort.Slice(workIDs, func(i, j int) bool { return workIDs[i] < workIDs[j] })
	rng.Shuffle(len(workIDs), func(i, j int) { workIDs[i], workIDs[j] = workIDs[j], workIDs[i] })

	claimed := map[int64]bool{}
	var pages [][]storage.HomeRolePick
	var current []storage.HomeRolePick
	for _, workID := range workIDs {
		songs := byWork[workID]
		weights := make([]float64, len(songs))
		for i, s := range songs {
			w := 1.0
			if s.Role == "op" || s.Role == "ed" {
				w = homeKeySongRoleBoost
			}
			weights[i] = w * homePlayFactor(s.Played, s.RecentlyPlayed)
		}
		group := make([]storage.HomeRolePick, 0, perWork)
		for _, idx := range homeWeightedOrder(rng, weights) {
			if len(group) >= perWork {
				break
			}
			if claimed[songs[idx].TrackID] {
				continue
			}
			claimed[songs[idx].TrackID] = true
			group = append(group, storage.HomeRolePick{WorkID: workID, TrackID: songs[idx].TrackID, Role: songs[idx].Role})
		}
		if len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			return storage.WorkRoleRank(group[i].Role) < storage.WorkRoleRank(group[j].Role)
		})
		if len(current)+len(group) > pageSize && len(current) > 0 {
			pages = append(pages, current)
			current = nil
		}
		current = append(current, group...)
	}
	if len(current) > 0 {
		pages = append(pages, current)
	}
	return pages
}

// homeBatchIndex maps a requested batch onto an existing page, wrapping
// after the last one. Returns 0 for an empty view.
func homeBatchIndex(batch, pages int) int {
	if pages <= 0 || batch < 0 {
		return 0
	}
	return batch % pages
}

func (a *App) homeWorksRotation(ctx context.Context, filter string) (*homeRotationView, error) {
	return a.homeRotation.view("works:"+filter, func(day string, now time.Time) (*homeRotationView, error) {
		candidates, err := a.store.HomeWorkCandidates(ctx, now.Add(-homeRecentPlayWindow))
		if err != nil {
			return nil, err
		}
		typeFilter := filter
		if filter == "all" {
			typeFilter = ""
		}
		pages := homeWorkPages(homeRNG(day, "works:"+filter), candidates, typeFilter, homeWorksPageSize, homeWorksTypeQuota, homeWorkFilters)
		view := &homeRotationView{workPages: pages}
		if view.workPages == nil {
			view.workPages = [][]int64{}
		}
		for _, page := range pages {
			view.Count += len(page)
		}
		return view, nil
	})
}

func (a *App) homeSongsRotation(ctx context.Context) (*homeRotationView, error) {
	return a.homeRotation.view("songs", func(day string, now time.Time) (*homeRotationView, error) {
		candidates, err := a.store.HomeRoleCandidates(ctx, now.Add(-homeRecentPlayWindow))
		if err != nil {
			return nil, err
		}
		pages := homeSongPages(homeRNG(day, "songs"), candidates, homeSongsPageSize, homeSongsPerWork)
		view := &homeRotationView{songPages: pages}
		if view.songPages == nil {
			view.songPages = [][]storage.HomeRolePick{}
		}
		for _, page := range pages {
			view.Count += len(page)
		}
		return view, nil
	})
}
