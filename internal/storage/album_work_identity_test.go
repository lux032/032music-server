package storage

import (
	"context"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func importAlbumTrack(t *testing.T, s *Store, ctx context.Context, album, track string, raw map[string][]string) int64 {
	t.Helper()
	lib, e := s.LibraryByRoot(ctx, "/music")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: album + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: track, Album: album, Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: raw}}); e != nil {
		t.Fatal(e)
	}
	var id int64
	if e = s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title=?`, album).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}

func workByTitleAndType(t *testing.T, s *Store, ctx context.Context, title, typ string) Work {
	t.Helper()
	var w Work
	if e := s.db.QueryRowContext(ctx, `SELECT id FROM works WHERE title=? AND type=?`, title, typ).Scan(&w.ID); e != nil {
		t.Fatalf("work %q (%s): %v", title, typ, e)
	}
	return w
}

// M4: an auto work inferred from one media type must not absorb an album of
// the same-titled work from a different media type.
func TestAutoWorkTypeDisciplineAcrossMedia(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
	if _, e := s.db.ExecContext(ctx, `DELETE FROM tracks`); e != nil {
		t.Fatal(e)
	}
	animeAlbum := importAlbumTrack(t, s, ctx, "TVアニメ『X』オリジナルサウンドトラック", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	animeWork := workByTitleAndType(t, s, ctx, "X", "anime")
	gameAlbum := importAlbumTrack(t, s, ctx, "ゲーム『X』サウンドトラック", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	gameWork := workByTitleAndType(t, s, ctx, "X", "game")
	if gameWork.ID == animeWork.ID {
		t.Fatal("game album merged into the anime work")
	}
	links, e := s.AlbumsForWork(ctx, gameWork.ID)
	if e != nil || len(links) != 1 || links[0].AlbumID != gameAlbum {
		t.Fatalf("game links %+v err %v", links, e)
	}
	links, e = s.AlbumsForWork(ctx, animeWork.ID)
	if e != nil || len(links) != 1 || links[0].AlbumID != animeAlbum {
		t.Fatalf("anime links %+v err %v", links, e)
	}
	// An 'other' inference still resolves to an existing work of any type.
	otherAlbum := importAlbumTrack(t, s, ctx, "X Original Soundtrack", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var workCount int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works WHERE title='X'`).Scan(&workCount); e != nil || workCount != 2 {
		t.Fatalf("'other' inference created a duplicate: %d %v", workCount, e)
	}
	var linkedType string
	if e = s.db.QueryRowContext(ctx, `SELECT w.type FROM album_works aw JOIN works w ON w.id=aw.work_id WHERE aw.album_id=?`, otherAlbum).Scan(&linkedType); e != nil {
		t.Fatalf("'other' album not linked to an existing work: %v", e)
	}
}

// A manually created work of one type must not absorb a same-titled album of
// another type either.
func TestAutoWorkTypeDisciplineManualWork(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
	manual, e := s.CreateWork(ctx, WorkInput{Title: "X", Type: "anime"})
	if e != nil {
		t.Fatal(e)
	}
	gameAlbum := importAlbumTrack(t, s, ctx, "ゲーム『X』サウンドトラック", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	gameWork := workByTitleAndType(t, s, ctx, "X", "game")
	if gameWork.ID == manual.ID {
		t.Fatal("game album merged into the manual anime work")
	}
	links, e := s.AlbumsForWork(ctx, manual.ID)
	if e != nil || len(links) != 0 {
		t.Fatalf("manual work links %+v err %v", links, e)
	}
	links, e = s.AlbumsForWork(ctx, gameWork.ID)
	if e != nil || len(links) != 1 || links[0].AlbumID != gameAlbum {
		t.Fatalf("game links %+v err %v", links, e)
	}
}

// Season spellings ("Season 2", "第2期", "2期") of the same season are one
// work identity; the display title keeps the first-seen spelling (D1).
func TestAlbumWorkSeasonSpellingIdentity(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
	if _, e := s.db.ExecContext(ctx, `DELETE FROM tracks`); e != nil {
		t.Fatal(e)
	}
	for _, album := range []string{"TVアニメ『X』Season 2 OST", "TVアニメ『X』2期 OST", "TVアニメ『X』第2期 OST"} {
		importAlbumTrack(t, s, ctx, album, "Track", nil)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var workCount int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works`).Scan(&workCount); e != nil || workCount != 1 {
		t.Fatalf("season spellings created %d works, want 1 (err %v)", workCount, e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	if works[0].Title != "X Season 2" {
		t.Fatalf("display title lost its spelling: %q", works[0].Title)
	}
	links, e := s.AlbumsForWork(ctx, works[0].ID)
	if e != nil || len(links) != 3 {
		t.Fatalf("season links %+v err %v", links, e)
	}
}

// A track tag naming the album's own work with a different season spelling
// must not create a second work or a track-level link.
func TestTrackTagSeasonSpellingMatchesAlbumWork(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "TVアニメ『Y』Season 2 OST", "Track")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Y/02.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "TVアニメ『Y』Season 2 OST", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 2, Raw: map[string][]string{"WORKTITLE": {"Y 2期 OST"}}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var workCount, autoTracks int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works`).Scan(&workCount); e != nil || workCount != 1 {
		t.Fatalf("tag season spelling created %d works, want 1 (err %v)", workCount, e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE source='auto' AND track_id IN (SELECT id FROM tracks WHERE album_id=?)`, album).Scan(&autoTracks); e != nil || autoTracks != 0 {
		t.Fatalf("auto work_tracks=%d, want 0 (err %v)", autoTracks, e)
	}
}

// Different season numbers stay different works.
func TestTrackTagDifferentSeasonCreatesWork(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "TVアニメ『Z』OST", "Track")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Z/02.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "TVアニメ『Z』OST", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 2, Raw: map[string][]string{"WORKTITLE": {"Z Season 2 オープニングテーマ"}}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var workCount, autoTracks int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works`).Scan(&workCount); e != nil || workCount != 2 {
		t.Fatalf("different season works=%d, want 2 (err %v)", workCount, e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE source='auto'`).Scan(&autoTracks); e != nil || autoTracks != 1 {
		t.Fatalf("auto work_tracks=%d, want 1 (err %v)", autoTracks, e)
	}
	workByTitleAndType(t, s, ctx, "Z Season 2", "anime")
}

// A rename alias skips the type filter only for user-edited (manual) works:
// after renaming anime X to Y, a game album naming X must create/attach a
// game work X, never the renamed anime Y — regardless of scan order.
func TestAutoWorkAliasCarryoverRequiresManualOrigin(t *testing.T) {
	t.Run("rename before game album", func(t *testing.T) {
		s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
		if _, e := s.db.ExecContext(ctx, `DELETE FROM tracks`); e != nil {
			t.Fatal(e)
		}
		importAlbumTrack(t, s, ctx, "TVアニメ『X』オリジナルサウンドトラック", "Track", nil)
		if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
			t.Fatal(e)
		}
		animeWork := workByTitleAndType(t, s, ctx, "X", "anime")
		if _, e := s.UpdateWork(ctx, animeWork.ID, WorkInput{Title: "Y", Type: "anime"}); e != nil {
			t.Fatal(e)
		}
		gameAlbum := importAlbumTrack(t, s, ctx, "ゲーム『X』オリジナルサウンドトラック", "Track", nil)
		if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
			t.Fatal(e)
		}
		gameWork := workByTitleAndType(t, s, ctx, "X", "game")
		if gameWork.ID == animeWork.ID {
			t.Fatal("game album attached to the renamed anime work")
		}
		links, e := s.AlbumsForWork(ctx, animeWork.ID)
		if e != nil || len(links) != 1 {
			t.Fatalf("anime links %+v err %v", links, e)
		}
		links, e = s.AlbumsForWork(ctx, gameWork.ID)
		if e != nil || len(links) != 1 || links[0].AlbumID != gameAlbum {
			t.Fatalf("game links %+v err %v", links, e)
		}
	})
	t.Run("rename after game work exists", func(t *testing.T) {
		s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
		if _, e := s.db.ExecContext(ctx, `DELETE FROM tracks`); e != nil {
			t.Fatal(e)
		}
		importAlbumTrack(t, s, ctx, "TVアニメ『X』オリジナルサウンドトラック", "Track", nil)
		gameAlbum := importAlbumTrack(t, s, ctx, "ゲーム『X』オリジナルサウンドトラック", "Track", nil)
		if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
			t.Fatal(e)
		}
		animeWork := workByTitleAndType(t, s, ctx, "X", "anime")
		gameWork := workByTitleAndType(t, s, ctx, "X", "game")
		if _, e := s.UpdateWork(ctx, animeWork.ID, WorkInput{Title: "Y", Type: "anime"}); e != nil {
			t.Fatal(e)
		}
		if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
			t.Fatal(e)
		}
		links, e := s.AlbumsForWork(ctx, gameWork.ID)
		if e != nil || len(links) != 1 || links[0].AlbumID != gameAlbum {
			t.Fatalf("game links %+v err %v", links, e)
		}
		links, e = s.AlbumsForWork(ctx, animeWork.ID)
		if e != nil || len(links) != 1 {
			t.Fatalf("anime links %+v err %v", links, e)
		}
	})
}

// A season-spelling alias written by the system (resolveSeasonCandidate) must
// not skip the type filter for a later album of another media type.
func TestSeasonSpellingAliasKeepsTypeFilter(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
	if _, e := s.db.ExecContext(ctx, `DELETE FROM tracks`); e != nil {
		t.Fatal(e)
	}
	importAlbumTrack(t, s, ctx, "TVアニメ『X』Season 2 OST", "Track", nil)
	importAlbumTrack(t, s, ctx, "TVアニメ『X』2期 OST", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var aliasCount int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE normalized_key='x 2期'`).Scan(&aliasCount); e != nil || aliasCount != 1 {
		t.Fatalf("system season alias=%d err %v", aliasCount, e)
	}
	gameAlbum := importAlbumTrack(t, s, ctx, "ゲーム『X』2期 サウンドトラック", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	gameWork := workByTitleAndType(t, s, ctx, "X 2期", "game")
	animeWork := workByTitleAndType(t, s, ctx, "X Season 2", "anime")
	if gameWork.ID == animeWork.ID {
		t.Fatal("game album merged into the anime work via a system-written alias")
	}
	links, e := s.AlbumsForWork(ctx, gameWork.ID)
	if e != nil || len(links) != 1 || links[0].AlbumID != gameAlbum {
		t.Fatalf("game links %+v err %v", links, e)
	}
	links, e = s.AlbumsForWork(ctx, animeWork.ID)
	if e != nil || len(links) != 2 {
		t.Fatalf("anime links %+v err %v", links, e)
	}
}

// Season prefix matching must not require a space before the season marker.
func TestSeasonPrefixMatchWithoutSpace(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
	if _, e := s.db.ExecContext(ctx, `DELETE FROM tracks`); e != nil {
		t.Fatal(e)
	}
	importAlbumTrack(t, s, ctx, "進撃の巨人2期 OST", "Track", nil)
	seasonAlbum := importAlbumTrack(t, s, ctx, "TVアニメ『進撃の巨人』Season 2 OST", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var workCount int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works`).Scan(&workCount); e != nil || workCount != 1 {
		t.Fatalf("no-space season spelling created %d works, want 1 (err %v)", workCount, e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	links, e := s.AlbumsForWork(ctx, works[0].ID)
	if e != nil || len(links) != 2 {
		t.Fatalf("links %+v err %v", links, e)
	}
	var linked int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND work_id=?`, seasonAlbum, works[0].ID).Scan(&linked); e != nil || linked != 1 {
		t.Fatalf("Season 2 album not linked: %d %v", linked, e)
	}
}

// A longer title sharing the base as a prefix is not the same work, and
// different season numbers stay distinct.
func TestSeasonPrefixMatchRejectsNonSeasonSuffix(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Placeholder", "Track")
	if _, e := s.db.ExecContext(ctx, `DELETE FROM tracks`); e != nil {
		t.Fatal(e)
	}
	importAlbumTrack(t, s, ctx, "X 2期 OST", "Track", nil)
	importAlbumTrack(t, s, ctx, "Xyz 2期 OST", "Track", nil)
	importAlbumTrack(t, s, ctx, "X Season 3 OST", "Track", nil)
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var workCount int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works`).Scan(&workCount); e != nil || workCount != 3 {
		t.Fatalf("works=%d, want 3 (err %v)", workCount, e)
	}
	workByTitleAndType(t, s, ctx, "X 2期", "other")
	workByTitleAndType(t, s, ctx, "Xyz 2期", "other")
	workByTitleAndType(t, s, ctx, "X Season 3", "other")
}
