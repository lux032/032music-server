package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// newHomeFixture builds a small library: two albums (one linked to a series
// of two works, one unlinked) with songs carrying work roles.
func newHomeFixture(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Test','/music')`); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func addHomeAlbum(t *testing.T, store *Store, ctx context.Context, title string, year int, addedAt string, tracks ...string) (int64, []int64) {
	t.Helper()
	result, err := store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key,release_year,added_at) VALUES(1,?,?,?,?,?)`, title, title, title, year, addedAt)
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := result.LastInsertId()
	ids := make([]int64, 0, len(tracks))
	for i, title := range tracks {
		result, err := store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number,duration_ms) VALUES(?,?,?,1,?,240000)`, albumID, title, title, i+1)
		if err != nil {
			t.Fatal(err)
		}
		trackID, _ := result.LastInsertId()
		ids = append(ids, trackID)
	}
	giveTracksFiles(t, store)
	return albumID, ids
}

func addHomeWork(t *testing.T, store *Store, ctx context.Context, title, workType string, year int) int64 {
	t.Helper()
	work, err := store.CreateWork(ctx, WorkInput{Title: title, Type: workType, Year: year})
	if err != nil {
		t.Fatal(err)
	}
	return work.ID
}

func TestHomeWorksRoleCountsAndCovers(t *testing.T) {
	store, ctx := newHomeFixture(t)
	albumID, tracks := addHomeAlbum(t, store, ctx, "Album A", 2024, "2024-05-01T00:00:00Z", "OP Song", "ED Song", "OST 1")
	anime := addHomeWork(t, store, ctx, "Anime A", "anime", 2024)
	game := addHomeWork(t, store, ctx, "Game B", "game", 2020)
	addHomeWork(t, store, ctx, "No Tracks", "anime", 2023) // never listed: no linked tracks

	if err := store.AddWorkTrack(ctx, anime, WorkTrackInput{TrackID: tracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkTrack(ctx, anime, WorkTrackInput{TrackID: tracks[1], Role: "ed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkAlbum(ctx, game, albumID, "ost"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO artworks(album_id,source_type,content_hash,mime_type,byte_size,is_primary) VALUES(?,'embedded','hash-a','image/jpeg',10,1)`, albumID); err != nil {
		t.Fatal(err)
	}

	works, err := store.HomeWorks(ctx, "", 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 2 {
		t.Fatalf("HomeWorks = %d, want 2", len(works))
	}
	// game links the whole album (3 tracks) and outranks anime (2 tracks).
	if works[0].Title != "Game B" || works[0].TrackCount != 3 {
		t.Fatalf("first work = %+v", works[0])
	}
	if works[1].Title != "Anime A" || len(works[1].RoleCounts) != 2 {
		t.Fatalf("second work = %+v", works[1])
	}
	if works[1].RoleCounts[0].Role != "op" || works[1].RoleCounts[1].Role != "ed" {
		t.Fatalf("role order = %+v", works[1].RoleCounts)
	}
	if works[1].CoverURL == "" {
		t.Fatal("anime has a linked album (via tracks' album) cover fallback expected")
	}

	animeOnly, err := store.HomeWorks(ctx, "anime", 24)
	if err != nil || len(animeOnly) != 1 || animeOnly[0].Title != "Anime A" {
		t.Fatalf("HomeWorks(anime) = %+v, err=%v", animeOnly, err)
	}
}

func TestHomeRoleTracksFiltersAndOrder(t *testing.T) {
	store, ctx := newHomeFixture(t)
	_, tracks := addHomeAlbum(t, store, ctx, "Album A", 2024, "2024-05-01T00:00:00Z", "OP", "ED", "OST", "Other")
	anime := addHomeWork(t, store, ctx, "Anime A", "anime", 2024)
	roles := []string{"op", "ed", "ost", "other"}
	for i, role := range roles {
		if err := store.AddWorkTrack(ctx, anime, WorkTrackInput{TrackID: tracks[i], Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.HomeRoleTracks(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("HomeRoleTracks = %d rows, want 2 (ost/other excluded)", len(list))
	}
	if list[0].Role != "op" || list[1].Role != "ed" || list[0].WorkTitle != "Anime A" {
		t.Fatalf("rows = %+v", list)
	}
	if list[0].Track.ID != tracks[0] || list[0].Track.DurationMillis != 240000 {
		t.Fatalf("track hydration = %+v", list[0].Track)
	}
}

func TestUnfinishedPlayback(t *testing.T) {
	store, ctx := newHomeFixture(t)
	_, tracks := addHomeAlbum(t, store, ctx, "Album A", 2024, "2024-05-01T00:00:00Z", "T1", "T2", "T3", "T4")
	_ = tracks
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// unfinished: halfway through
	exec(`INSERT INTO playback_progress(track_id,position_ms,duration_ms,last_played_at) VALUES(?,120000,240000,'2024-06-01T00:00:00Z')`, tracks[0])
	// finished: position reset to 0 by completion
	exec(`INSERT INTO playback_progress(track_id,position_ms,duration_ms,last_played_at) VALUES(?,0,240000,'2024-06-02T00:00:00Z')`, tracks[1])
	// effectively finished: 99% through
	exec(`INSERT INTO playback_progress(track_id,position_ms,duration_ms,last_played_at) VALUES(?,238000,240000,'2024-06-03T00:00:00Z')`, tracks[2])
	// most recent unfinished
	exec(`INSERT INTO playback_progress(track_id,position_ms,duration_ms,last_played_at) VALUES(?,60000,240000,'2024-06-04T00:00:00Z')`, tracks[3])

	list, err := store.UnfinishedPlayback(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("UnfinishedPlayback = %d rows, want 2", len(list))
	}
	if list[0].TrackID != tracks[3] || list[1].TrackID != tracks[0] {
		t.Fatalf("order = %d,%d", list[0].TrackID, list[1].TrackID)
	}
	if list[0].Track.Title != "T4" || list[0].PositionMillis != 60000 {
		t.Fatalf("hydration = %+v", list[0])
	}
}

func TestRandomAlbumExclusion(t *testing.T) {
	store, ctx := newHomeFixture(t)
	a, _ := addHomeAlbum(t, store, ctx, "A", 2020, "2024-01-01T00:00:00Z", "t")
	b, _ := addHomeAlbum(t, store, ctx, "B", 2021, "2024-01-02T00:00:00Z", "t")

	album, err := store.RandomAlbum(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if album.ID != b {
		t.Fatalf("RandomAlbum(exclude=%d) = %d, want %d", a, album.ID, b)
	}
	// Excluding the only album falls back to returning it anyway.
	album, err = store.RandomAlbum(ctx, -1)
	if err != nil || (album.ID != a && album.ID != b) {
		t.Fatalf("RandomAlbum() = %d, err=%v", album.ID, err)
	}
	if _, err := store.RandomAlbum(ctx, a+b+100); err != nil {
		t.Fatalf("unknown exclude should still return an album: %v", err)
	}
}

func TestHomeSeriesSpotlight(t *testing.T) {
	store, ctx := newHomeFixture(t)
	oldAlbum, _ := addHomeAlbum(t, store, ctx, "Old", 2019, "2024-01-01T00:00:00Z", "o1")
	newAlbum, newTracks := addHomeAlbum(t, store, ctx, "New", 2024, "2024-09-01T00:00:00Z", "n1", "n2")
	w1 := addHomeWork(t, store, ctx, "Work One", "anime", 2019)
	w2 := addHomeWork(t, store, ctx, "Work Two", "anime", 2024)
	w3 := addHomeWork(t, store, ctx, "Solo", "game", 2018)
	if _, err := store.CreateWorkSeries(ctx, "Old Series", []int64{w1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkSeries(ctx, "New Series", []int64{w2, w3}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkAlbum(ctx, w1, oldAlbum, "ost"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkAlbum(ctx, w2, newAlbum, "ost"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkTrack(ctx, w2, WorkTrackInput{TrackID: newTracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}

	spot, err := store.HomeSeriesSpotlight(ctx)
	if err != nil || spot == nil {
		t.Fatalf("spotlight = %+v, err=%v", spot, err)
	}
	// New Series wins: its member work has the most recently added album.
	if spot.SeriesTitle != "New Series" {
		t.Fatalf("series = %q, want New Series", spot.SeriesTitle)
	}
	if len(spot.Works) != 1 || spot.Works[0].Work.Title != "Work Two" {
		t.Fatalf("works = %+v", spot.Works)
	}
	var w2view *HomeSpotlightWork
	for i := range spot.Works {
		if spot.Works[i].Work.ID == w2 {
			w2view = &spot.Works[i]
		}
	}
	if w2view == nil || len(w2view.Albums) != 1 || w2view.Albums[0].AlbumID != newAlbum {
		t.Fatalf("w2 albums = %+v", w2view)
	}
	if len(w2view.RoleCounts) != 1 || w2view.RoleCounts[0].Role != "op" {
		t.Fatalf("w2 roles = %+v", w2view.RoleCounts)
	}
}

func TestHomeSeriesSpotlightFallback(t *testing.T) {
	store, ctx := newHomeFixture(t)
	_, tracks := addHomeAlbum(t, store, ctx, "A", 2020, "2024-01-01T00:00:00Z", "t1", "t2")
	workID := addHomeWork(t, store, ctx, "No Series Work", "anime", 2020)
	if err := store.AddWorkTrack(ctx, workID, WorkTrackInput{TrackID: tracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	spot, err := store.HomeSeriesSpotlight(ctx)
	if err != nil || spot == nil {
		t.Fatalf("spotlight = %+v, err=%v", spot, err)
	}
	if spot.SeriesID != 0 || len(spot.Works) != 1 || spot.Works[0].Work.ID != workID {
		t.Fatalf("fallback = %+v", spot)
	}

	// Empty library: no spotlight at all.
	store2, ctx2 := newHomeFixture(t)
	spot, err = store2.HomeSeriesSpotlight(ctx2)
	if err != nil || spot != nil {
		t.Fatalf("empty library spotlight = %+v, err=%v", spot, err)
	}
}

func TestHomeQueriesEmptyLibrary(t *testing.T) {
	store, ctx := newHomeFixture(t)
	if works, err := store.HomeWorks(ctx, "", 24); err != nil || len(works) != 0 {
		t.Fatalf("HomeWorks = %v, %v", works, err)
	}
	if list, err := store.HomeRoleTracks(ctx, 20); err != nil || len(list) != 0 {
		t.Fatalf("HomeRoleTracks = %v, %v", list, err)
	}
	if list, err := store.UnfinishedPlayback(ctx, 5); err != nil || len(list) != 0 {
		t.Fatalf("UnfinishedPlayback = %v, %v", list, err)
	}
	if _, err := store.RandomAlbum(ctx, 0); err == nil {
		t.Fatal("RandomAlbum on empty library should return ErrNoRows")
	}
}
