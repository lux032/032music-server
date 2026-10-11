package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func rotationWorkPool() []storage.HomeWorkCandidate {
	pool := make([]storage.HomeWorkCandidate, 0, 100)
	id := int64(1)
	add := func(n int, typ string, tracks int64) {
		for i := 0; i < n; i++ {
			pool = append(pool, storage.HomeWorkCandidate{ID: id, Type: typ, TrackCount: tracks})
			id++
		}
	}
	add(40, "other", 300) // heavy works that used to monopolise the rail
	add(30, "anime", 60)
	add(20, "game", 150)
	add(5, "movie", 2) // light works ranked far below the old top 24
	add(5, "drama", 10)
	return pool
}

func TestHomeWorkPagesWalkEveryWorkOnce(t *testing.T) {
	pool := rotationWorkPool()
	pages := homeWorkPages(homeRNG("2026-10-11", "works:all"), pool, "", 24, 3, homeWorkFilters)
	if len(pages) != 5 {
		t.Fatalf("pages = %d, want ceil(100/24)=5", len(pages))
	}
	seen := map[int64]int{}
	for i, page := range pages {
		if i < len(pages)-1 && len(page) != 24 {
			t.Fatalf("page %d size = %d", i, len(page))
		}
		for _, id := range page {
			seen[id]++
		}
	}
	if len(pages[4]) != 4 {
		t.Fatalf("last page must stay short, got %d", len(pages[4]))
	}
	for _, c := range pool {
		if seen[c.ID] != 1 {
			t.Fatalf("work %d shown %d times in one walk", c.ID, seen[c.ID])
		}
	}
	// Page 0 of “全部” guarantees each filter type a few cards.
	types := map[string]int{}
	for _, id := range pages[0] {
		types[pool[id-1].Type]++
	}
	for _, typ := range homeWorkFilters {
		if types[typ] < 3 {
			t.Fatalf("page 0 has %d %s works, want >=3 (%v)", types[typ], typ, types)
		}
	}
	// Same day + view => same pages.
	again := homeWorkPages(homeRNG("2026-10-11", "works:all"), pool, "", 24, 3, homeWorkFilters)
	if fmt.Sprint(again) != fmt.Sprint(pages) {
		t.Fatal("rotation is not reproducible for the same day")
	}
	other := homeWorkPages(homeRNG("2026-10-12", "works:all"), pool, "", 24, 3, homeWorkFilters)
	if fmt.Sprint(other[0]) == fmt.Sprint(pages[0]) {
		t.Fatal("next day should rotate page 0")
	}
}

func TestHomeWorkPagesTypeFilter(t *testing.T) {
	pool := rotationWorkPool()
	pages := homeWorkPages(homeRNG("d", "works:movie"), pool, "movie", 24, 3, homeWorkFilters)
	if len(pages) != 1 || len(pages[0]) != 5 {
		t.Fatalf("movie pages = %v", pages)
	}
	for _, id := range pages[0] {
		if pool[id-1].Type != "movie" {
			t.Fatalf("non-movie %d in movie view", id)
		}
	}
	if pages := homeWorkPages(homeRNG("d", "x"), pool, "commercial", 24, 3, homeWorkFilters); len(pages) != 0 {
		t.Fatalf("empty filter pages = %v", pages)
	}
	// Exactly one page: no quota reshuffle, single page.
	exact := pool[:24]
	if pages := homeWorkPages(homeRNG("d", "x"), exact, "", 24, 3, homeWorkFilters); len(pages) != 1 || len(pages[0]) != 24 {
		t.Fatalf("exact page = %v", pages)
	}
	if pages := homeWorkPages(homeRNG("d", "x"), pool[:25], "", 24, 3, homeWorkFilters); len(pages) != 2 || len(pages[1]) != 1 {
		t.Fatalf("page+1 = %v", pages)
	}
}

func TestHomeWeightedOrderPrefersHeavierAndDemotesRecentPlays(t *testing.T) {
	heavyFirst, recentFirst := 0, 0
	for seed := 0; seed < 400; seed++ {
		rng := homeRNG("seed", fmt.Sprint(seed))
		if homeWeightedOrder(rng, []float64{1, 5})[0] == 1 {
			heavyFirst++
		}
		rng = homeRNG("seed2", fmt.Sprint(seed))
		played := homePlayFactor(true, true)
		fresh := homePlayFactor(false, false)
		if homeWeightedOrder(rng, []float64{played, fresh})[0] == 0 {
			recentFirst++
		}
	}
	// Expected 5/6 ≈ 333 and 0.3/1.8 ≈ 67; generous bounds keep it stable.
	if heavyFirst < 290 {
		t.Fatalf("heavier item first %d/400", heavyFirst)
	}
	if recentFirst > 110 {
		t.Fatalf("recently played item first %d/400", recentFirst)
	}
}

func TestHomeSongPagesGroupsAndDedup(t *testing.T) {
	var pool []storage.HomeRoleCandidate
	track := int64(1000)
	for work := int64(1); work <= 30; work++ {
		n := 1 + int(work%4) // 1..4 songs per work
		for i := 0; i < n; i++ {
			role := []string{"op", "ed", "insert", "character"}[i]
			pool = append(pool, storage.HomeRoleCandidate{WorkID: work, TrackID: track, Role: role})
			track++
		}
	}
	// Track 1000 (work 1's OP) is also work 2's theme.
	pool = append(pool, storage.HomeRoleCandidate{WorkID: 2, TrackID: 1000, Role: "theme"})
	pages := homeSongPages(homeRNG("2026-10-11", "songs"), pool, 20, 2)
	tracks := map[int64]bool{}
	works := map[int64]int{}
	for p, page := range pages {
		if len(page) > 20 || len(page) == 0 {
			t.Fatalf("page %d size %d", p, len(page))
		}
		pageWorks := map[int64]bool{}
		for i, pick := range page {
			if tracks[pick.TrackID] {
				t.Fatalf("track %d shown twice", pick.TrackID)
			}
			tracks[pick.TrackID] = true
			if !pageWorks[pick.WorkID] {
				works[pick.WorkID]++ // count pages a work appears on
				pageWorks[pick.WorkID] = true
			}
			if i > 0 && page[i-1].WorkID == pick.WorkID && storage.WorkRoleRank(page[i-1].Role) > storage.WorkRoleRank(pick.Role) {
				t.Fatalf("group not ordered by role: %+v", page)
			}
		}
	}
	for work := int64(1); work <= 30; work++ {
		if works[work] != 1 {
			t.Fatalf("work %d on %d pages (groups must not split, every work shown)", work, works[work])
		}
	}
	perWork := map[int64]int{}
	for _, page := range pages {
		for _, pick := range page {
			perWork[pick.WorkID]++
		}
	}
	for work, n := range perWork {
		if n > 2 {
			t.Fatalf("work %d has %d songs", work, n)
		}
	}
	if homeSongPages(homeRNG("d", "songs"), nil, 20, 2) != nil {
		t.Fatal("empty pool must give no pages")
	}
}

func TestHomeBatchIndexWraps(t *testing.T) {
	for _, c := range []struct{ batch, pages, want int }{{0, 0, 0}, {3, 0, 0}, {0, 5, 0}, {4, 5, 4}, {5, 5, 0}, {12, 5, 2}, {-1, 5, 0}} {
		if got := homeBatchIndex(c.batch, c.pages); got != c.want {
			t.Fatalf("homeBatchIndex(%d,%d) = %d, want %d", c.batch, c.pages, got, c.want)
		}
	}
}

func TestHomeRotationFreezesPerDay(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC) // 23:00 UTC+8
	rotation := newHomeRotation(func() time.Time { return now })
	builds := 0
	build := func(day string, _ time.Time) (*homeRotationView, error) {
		builds++
		return &homeRotationView{workPages: [][]int64{{int64(builds)}}}, nil
	}
	first, _ := rotation.view("works:all", build)
	again, _ := rotation.view("works:all", build)
	if builds != 1 || first != again {
		t.Fatalf("same day must reuse the frozen view (builds=%d)", builds)
	}
	now = now.Add(time.Hour) // 00:00 next day in UTC+8
	next, _ := rotation.view("works:all", build)
	if builds != 2 || next.Generation == first.Generation || !strings.HasPrefix(next.Generation, "2026-10-12.") {
		t.Fatalf("new day must rebuild: builds=%d gen=%q→%q", builds, first.Generation, next.Generation)
	}
	failing := func(string, time.Time) (*homeRotationView, error) { return nil, fmt.Errorf("boom") }
	if _, err := rotation.view("songs", failing); err == nil {
		t.Fatal("build error must surface")
	}
	if _, err := rotation.view("songs", build); err != nil || builds != 3 {
		t.Fatal("failed builds must not be cached")
	}
}

// seedRotationWorks links 26 heavy anime works and one light movie so the
// movie ranks below the old top-24 cut.
func seedRotationWorks(t *testing.T, app *App, store *storage.Store) (movieID int64) {
	t.Helper()
	ctx := context.Background()
	albumID, tracks := seedHomeLibrary(t, app, store)
	for i := 0; i < 26; i++ {
		work, err := store.CreateWork(ctx, storage.WorkInput{Title: fmt.Sprintf("Anime %02d", i), Type: "anime", Year: 2020})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddWorkAlbum(ctx, work.ID, albumID, "ost"); err != nil {
			t.Fatal(err)
		}
		if err := store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: tracks[i%2], Role: []string{"op", "ed"}[i%2]}); err != nil {
			t.Fatal(err)
		}
	}
	movie, err := store.CreateWork(ctx, storage.WorkInput{Title: "Light Movie", Type: "movie", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkTrack(ctx, movie.ID, storage.WorkTrackInput{TrackID: tracks[2], Role: "theme"}); err != nil {
		t.Fatal(err)
	}
	return movie.ID
}

func TestHomeWorksFragment(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedRotationWorks(t, app, store)
	app.homeRotation.now = func() time.Time { return time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC) }
	cookie := loginCookie(t, app)

	home := getHome(t, app, cookie, "/admin/home").Body.String()
	if !strings.Contains(home, `data-rotation-next="works"`) || strings.Contains(home, `data-rotation-next="works" aria-label="换一批作品" hidden`) {
		t.Fatal("27 works need a visible 换一批 button")
	}
	if !strings.Contains(home, "27 部作品") || !strings.Contains(home, "Light Movie") {
		t.Fatal("page 0 of 全部 must count the pool and guarantee the movie")
	}

	rec := getHome(t, app, cookie, "/admin/home/works?type=movie")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Light Movie") || strings.Contains(body, "Anime 0") || !strings.Contains(body, `data-pages="1"`) {
		t.Fatalf("movie filter: %d %s", rec.Code, body)
	}
	rec = getHome(t, app, cookie, "/admin/home/works?type=game")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data-works-empty") {
		t.Fatalf("empty filter: %d %s", rec.Code, rec.Body.String())
	}

	page0 := getHome(t, app, cookie, "/admin/home/works?type=all&batch=0").Body.String()
	page1 := getHome(t, app, cookie, "/admin/home/works?type=all&batch=1").Body.String()
	wrapped := getHome(t, app, cookie, "/admin/home/works?type=all&batch=2").Body.String()
	if !strings.Contains(page1, `data-batch="1"`) || strings.Count(page1, `class="h-work"`) != 3 {
		t.Fatalf("page 1 must hold the remaining 3 works: %s", page1)
	}
	if wrapped != page0 {
		t.Fatal("batch past the last page wraps to page 0")
	}
	for _, id := range workIDsIn(page1) {
		if strings.Contains(page0, `data-play-work="`+id+`"`) {
			t.Fatalf("work %s repeated across pages", id)
		}
	}

	rec = getHome(t, app, cookie, "/admin/home/works?type=all&batch=1&gen=2026-01-01.stale")
	if rec.Header().Get("X-Home-Rotation") != "renewed" || !strings.Contains(rec.Body.String(), `data-batch="0"`) {
		t.Fatalf("stale generation must reset to page 0: %v", rec.Header())
	}
	for _, path := range []string{"/admin/home/works?type=other", "/admin/home/works?batch=-1", "/admin/home/works?batch=abc", "/admin/home/works?batch=99999999999999999999", "/admin/home/songs?batch=x"} {
		if rec := getHome(t, app, cookie, path); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
	if rec := getHome(t, app, nil, "/admin/home/works"); rec.Code == http.StatusOK {
		t.Fatal("fragment must require auth")
	}
}

func TestHomeSongsFragmentFrozenAgainstPlays(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	seedRotationWorks(t, app, store)
	app.homeRotation.now = func() time.Time { return time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC) }
	cookie := loginCookie(t, app)

	before := getHome(t, app, cookie, "/admin/home/songs")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), "h-trow") {
		t.Fatalf("songs fragment: %d %s", before.Code, before.Body.String())
	}
	// 3 distinct songs across 27 works: each track is shown once per day.
	if n := strings.Count(before.Body.String(), `class="h-trow"`); n != 3 {
		t.Fatalf("songs = %d, want 3 (tracks deduped across works)", n)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for i, track := range tracks {
		session := fmt.Sprintf("rotation-play-%d", i)
		if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{ClientID: "test", ClientKind: "web", SessionID: session, Seq: 1, Type: "start", TrackID: track.ID, State: "playing", DurationMillis: 240000}); err != nil {
			t.Fatal(err)
		}
	}
	after := getHome(t, app, cookie, "/admin/home/songs")
	// Hydrated details (e.g. duration learnt from playback) may change; the
	// picked songs and their works may not.
	if got, want := songPicksIn(after.Body.String()), songPicksIn(before.Body.String()); got != want {
		t.Fatalf("listening during the day must not reshuffle the frozen rotation: %s vs %s", want, got)
	}
}

func songPicksIn(body string) string {
	var picks []string
	for _, part := range strings.Split(body, `data-track-id="`)[1:] {
		id := part[:strings.Index(part, `"`)]
		work := part[strings.Index(part, `href="/admin/works/`)+len(`href="/admin/works/`):]
		picks = append(picks, id+"@"+work[:strings.Index(work, `"`)])
	}
	return strings.Join(picks, ",")
}

func workIDsIn(body string) []string {
	var ids []string
	for _, part := range strings.Split(body, `data-play-work="`)[1:] {
		ids = append(ids, part[:strings.Index(part, `"`)])
	}
	return ids
}
