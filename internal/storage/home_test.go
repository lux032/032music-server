package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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

func TestHomeWorkCandidatesAndCards(t *testing.T) {
	store, ctx := newHomeFixture(t)
	albumID, tracks := addHomeAlbum(t, store, ctx, "Album A", 2024, "2024-05-01T00:00:00Z", "OP Song", "ED Song", "OST 1")
	anime := addHomeWork(t, store, ctx, "Anime A", "anime", 2024)
	game := addHomeWork(t, store, ctx, "Game B", "game", 2020)
	addHomeWork(t, store, ctx, "No Tracks", "anime", 2023) // never listed: no linked tracks

	if err := store.AddWorkTrack(ctx, anime, WorkTrackInput{TrackID: tracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	// The same song linked twice as OP (two seasons) counts once.
	if err := store.AddWorkTrack(ctx, anime, WorkTrackInput{TrackID: tracks[0], Role: "op", Season: 2}); err != nil {
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
	// Play history: ED (anime) long ago, OST 1 (game only) recently.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO playback_progress(track_id,last_played_at) VALUES(?,'2024-01-01T00:00:00.000Z'),(?,'2024-06-09T00:00:00.000Z')`, tracks[1], tracks[2]); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.HomeWorkCandidates(ctx, time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := []HomeWorkCandidate{
		{ID: anime, Type: "anime", TrackCount: 2, Played: true},
		{ID: game, Type: "game", TrackCount: 3, Played: true, RecentlyPlayed: true},
	}
	if len(candidates) != 2 || candidates[0] != want[0] || candidates[1] != want[1] {
		t.Fatalf("candidates = %+v, want %+v", candidates, want)
	}

	// Cards come back in the requested order; unknown ids are dropped.
	works, err := store.HomeWorksByIDs(ctx, []int64{anime, 9999, game})
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 2 || works[0].ID != anime || works[1].ID != game {
		t.Fatalf("HomeWorksByIDs order = %+v", works)
	}
	if works[0].TrackCount != 2 || works[1].TrackCount != 3 || works[0].UpdatedAt == "" {
		t.Fatalf("cards = %+v", works)
	}
	if len(works[0].RoleCounts) != 2 || works[0].RoleCounts[0] != (HomeRoleCount{Role: "op", Count: 1}) || works[0].RoleCounts[1].Role != "ed" {
		t.Fatalf("role counts = %+v", works[0].RoleCounts)
	}
	if works[0].CoverURL == "" {
		t.Fatal("anime has a linked album (via tracks' album) cover fallback expected")
	}

	// A track that lost its file no longer counts.
	if _, err := store.db.ExecContext(ctx, `UPDATE audio_files SET status='missing' WHERE track_id IN (?,?)`, tracks[0], tracks[1]); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.HomeWorkCandidates(ctx, time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC))
	if err != nil || len(candidates) != 1 || candidates[0].ID != game || candidates[0].TrackCount != 1 {
		t.Fatalf("after missing = %+v, %v", candidates, err)
	}
	if works, err := store.HomeWorksByIDs(ctx, []int64{anime, game}); err != nil || len(works) != 1 || works[0].ID != game {
		t.Fatalf("cards after missing = %+v, %v", works, err)
	}
}

func TestHomeRoleCandidatesAndHydration(t *testing.T) {
	store, ctx := newHomeFixture(t)
	_, tracks := addHomeAlbum(t, store, ctx, "Album A", 2024, "2024-05-01T00:00:00Z", "OP", "ED", "OST", "Other", "Shared")
	anime := addHomeWork(t, store, ctx, "Anime A", "anime", 2024)
	movie := addHomeWork(t, store, ctx, "Movie B", "movie", 2021)
	roles := []string{"op", "ed", "ost", "other"}
	for i, role := range roles {
		if err := store.AddWorkTrack(ctx, anime, WorkTrackInput{TrackID: tracks[i], Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	// One song that is both insert and ED of the anime, and the movie's theme.
	for _, link := range []struct {
		work int64
		role string
	}{{anime, "insert"}, {anime, "ed"}, {movie, "theme"}} {
		if err := store.AddWorkTrack(ctx, link.work, WorkTrackInput{TrackID: tracks[4], Role: link.role}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO playback_progress(track_id,last_played_at) VALUES(?,'2024-06-09T00:00:00.000Z')`, tracks[0]); err != nil {
		t.Fatal(err)
	}

	list, err := store.HomeRoleCandidates(ctx, time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := []HomeRoleCandidate{
		{WorkID: anime, TrackID: tracks[0], Role: "op", Played: true, RecentlyPlayed: true},
		{WorkID: anime, TrackID: tracks[1], Role: "ed"},
		{WorkID: anime, TrackID: tracks[4], Role: "ed"}, // insert+ed merged to the better rank
		{WorkID: movie, TrackID: tracks[4], Role: "theme"},
	}
	if len(list) != len(want) {
		t.Fatalf("candidates = %+v (ost/other excluded, pairs deduped)", list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("candidate %d = %+v, want %+v", i, list[i], want[i])
		}
	}

	rows, err := store.HomeRoleTracksFor(ctx, []HomeRolePick{{WorkID: movie, TrackID: tracks[4], Role: "theme"}, {WorkID: anime, TrackID: tracks[0], Role: "op"}, {WorkID: 9999, TrackID: tracks[1], Role: "ed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].WorkTitle != "Movie B" || rows[0].Track.ID != tracks[4] || rows[1].Role != "op" || rows[1].Track.DurationMillis != 240000 {
		t.Fatalf("hydrated = %+v", rows)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE audio_files SET status='missing' WHERE track_id=?`, tracks[4]); err != nil {
		t.Fatal(err)
	}
	rows, err = store.HomeRoleTracksFor(ctx, []HomeRolePick{{WorkID: movie, TrackID: tracks[4], Role: "theme"}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("missing track must be dropped: %+v, %v", rows, err)
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
	if works, err := store.HomeWorkCandidates(ctx, time.Now()); err != nil || len(works) != 0 {
		t.Fatalf("HomeWorkCandidates = %v, %v", works, err)
	}
	if works, err := store.HomeWorksByIDs(ctx, nil); err != nil || len(works) != 0 {
		t.Fatalf("HomeWorksByIDs = %v, %v", works, err)
	}
	if list, err := store.HomeRoleCandidates(ctx, time.Now()); err != nil || len(list) != 0 {
		t.Fatalf("HomeRoleCandidates = %v, %v", list, err)
	}
	if list, err := store.UnfinishedPlayback(ctx, 5); err != nil || len(list) != 0 {
		t.Fatalf("UnfinishedPlayback = %v, %v", list, err)
	}
	if _, err := store.RandomAlbum(ctx, 0); err == nil {
		t.Fatal("RandomAlbum on empty library should return ErrNoRows")
	}
}
