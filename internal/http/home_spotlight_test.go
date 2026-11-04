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

func TestHomeSpotlightOrderRotatesDaily(t *testing.T) {
	ids := []int64{3, 7, 11, 19, 42}
	day := func(i int) string { return time.Date(2026, 10, 1+i, 0, 0, 0, 0, homeZone).Format("2006-01-02") }
	seen := map[int64]bool{}
	prev := int64(0)
	for i := 0; i < len(ids); i++ {
		order := homeSpotlightOrder(ids, day(i))
		if len(order) != len(ids) {
			t.Fatalf("order %v must keep every id", order)
		}
		if order[0] == prev {
			t.Fatalf("day %d repeats yesterday's %d", i, prev)
		}
		seen[order[0]] = true
		prev = order[0]
		if again := homeSpotlightOrder(ids, day(i)); fmt.Sprint(again) != fmt.Sprint(order) {
			t.Fatalf("same day must give the same order: %v vs %v", order, again)
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("%d consecutive days must show every entry once, saw %v", len(ids), seen)
	}
	// Input order does not matter, and the next cycle starts over.
	if fmt.Sprint(homeSpotlightOrder([]int64{42, 19, 11, 7, 3}, day(0))) != fmt.Sprint(homeSpotlightOrder(ids, day(0))) {
		t.Fatal("order must not depend on the pool's input order")
	}
	if homeSpotlightOrder(ids, day(len(ids)))[0] != homeSpotlightOrder(ids, day(0))[0] {
		t.Fatal("rotation must wrap after the whole pool")
	}
	if homeSpotlightOrder(nil, day(0)) != nil {
		t.Fatal("empty pool")
	}
	if got := homeSpotlightOrder([]int64{5}, day(3)); len(got) != 1 || got[0] != 5 {
		t.Fatalf("single entry = %v", got)
	}
}

// seedSpotlightSeries creates n series of one work each, every work linked to
// a song only at track level.
func seedSpotlightSeries(t *testing.T, app *App, store *storage.Store, n int) []string {
	t.Helper()
	ctx := context.Background()
	_, tracks := seedHomeLibrary(t, app, store)
	titles := make([]string, 0, n)
	for i := 0; i < n; i++ {
		work, err := store.CreateWork(ctx, storage.WorkInput{Title: fmt.Sprintf("Work %d", i), Type: "anime", Year: 2020 + i})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: tracks[i%len(tracks)], Role: "op"}); err != nil {
			t.Fatal(err)
		}
		title := fmt.Sprintf("Series %d", i)
		if _, err := store.CreateWorkSeries(ctx, title, []int64{work.ID}); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	return titles
}

// Issue #5: track-level-only series are spotlighted (the page used to fail
// to render for them), and the card changes from one day to the next.
func TestHomeSpotlightTrackLevelSeriesRotates(t *testing.T) {
	app, store, _ := setupTestApp(t)
	titles := seedSpotlightSeries(t, app, store, 3)
	cookie := loginCookie(t, app)
	now := time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC)
	app.homeRotation.now = func() time.Time { return now }

	shown := func() string {
		t.Helper()
		rec := getHome(t, app, cookie, "/admin/home")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		start := strings.Index(body, `data-focus="series"`)
		if start < 0 {
			t.Fatal("作品聚焦 pane missing")
		}
		pane := body[start:]
		var found []string
		for _, title := range titles {
			if strings.Contains(pane, `<h2 title="`+title+`">`) {
				found = append(found, title)
			}
		}
		if len(found) != 1 {
			t.Fatalf("pane must show exactly one series, got %v", found)
		}
		if !strings.Contains(pane, "关联专辑") || !strings.Contains(pane, "1 张关联专辑") {
			t.Fatal("the album holding the linked song must be listed")
		}
		return found[0]
	}
	first := shown()
	if again := shown(); again != first {
		t.Fatalf("same day must keep the card: %q then %q", first, again)
	}
	seen := map[string]bool{first: true}
	prev := first
	for i := 1; i < len(titles); i++ {
		now = now.Add(24 * time.Hour)
		cur := shown()
		if cur == prev {
			t.Fatalf("day %d repeats %q", i, cur)
		}
		seen[cur] = true
		prev = cur
	}
	if len(seen) != len(titles) {
		t.Fatalf("every series must come up within %d days, saw %v", len(titles), seen)
	}

	// The JSON API serves the same card.
	var home homeAPIWire
	decodeHomeJSON(t, apiGet(t, app, "/api/v1/home", testAppAPIToken, nil), &home)
	if home.Spotlight == nil || home.Spotlight.Series == nil || home.Spotlight.Series.Title != prev || !home.Spotlight.Series.IsSeries || home.Spotlight.Series.AlbumCount != 1 {
		t.Fatalf("api series pane = %+v, want %q", home.Spotlight, prev)
	}
}

// A series emptied after the day's pool was frozen is skipped for the next
// entry of the day's order.
func TestHomeSpotlightSkipsVanishedEntry(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	seedSpotlightSeries(t, app, store, 2)
	app.homeRotation.now = func() time.Time { return time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC) }
	first, err := app.homeSpotlight(ctx)
	if err != nil || first == nil || len(first.Works) != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if err := store.DeleteWork(ctx, first.Works[0].Work.ID); err != nil {
		t.Fatal(err)
	}
	next, err := app.homeSpotlight(ctx)
	if err != nil || next == nil || next.SeriesID == first.SeriesID {
		t.Fatalf("after delete = %+v, %v (was %d)", next, err, first.SeriesID)
	}
}

// Links made after an empty pool was frozen show up the same day.
func TestHomeSpotlightFrozenPoolRecovers(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	_, tracks := seedHomeLibrary(t, app, store)
	cookie := loginCookie(t, app)
	app.homeRotation.now = func() time.Time { return time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC) }

	if spot, err := app.homeSpotlight(ctx); err != nil || spot != nil {
		t.Fatalf("no links yet: %+v, %v", spot, err)
	}
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Late Work", Type: "anime", Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: tracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	rec := getHome(t, app, cookie, "/admin/home")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-focus-tab="series"`) || !strings.Contains(rec.Body.String(), `<h2 title="Late Work">`) {
		t.Fatalf("new link must show the same day (status %d)", rec.Code)
	}
}
