package storage

import (
	"slices"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestTrackVersionsTieredMatchingAndCounts(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	type spec struct {
		path     string
		title    string
		album    string
		year     int
		artists  []string
		composer string
		lyricist string
		arranger string
		durMs    int64
	}
	specs := []spec{
		// Group 1: "Home" - Composer A (original + TV Size cover) vs Composer B (different song, same lyricist/primary/duration!) vs missing-all-credits stranger
		{path: "h1.flac", title: "Home", album: "Single A", year: 2020, artists: []string{"Singer A"}, composer: "Composer A", lyricist: "Shared Lyricist", arranger: "Arr X", durMs: 240_000},
		{path: "h2.flac", title: "home", album: "Cover TV", year: 2021, artists: []string{"Singer Cover"}, composer: "Composer A", lyricist: "Other Lyricist", arranger: "Arr Y", durMs: 90_000},
		{path: "h3.flac", title: "Home", album: "Other Song", year: 2022, artists: []string{"Singer A"}, composer: "Composer B", lyricist: "Shared Lyricist", arranger: "Arr X", durMs: 240_000},
		{path: "h4.flac", title: "Home", album: "No Credits", year: 2023, artists: []string{"Stranger"}, durMs: 240_000},

		// Group 2: "Lyric" - one lacks composer -> Tier 1b lyricist decides
		{path: "l1.flac", title: "Lyric", album: "L1", year: 2018, artists: []string{"P1"}, composer: "Comp L", lyricist: "Lyr Same", durMs: 200_000},
		{path: "l2.flac", title: "Lyric", album: "L2", year: 2019, artists: []string{"P2"}, lyricist: "Lyr Same", durMs: 200_000},
		{path: "l3.flac", title: "Lyric", album: "L3", year: 2020, artists: []string{"P1"}, lyricist: "Lyr Diff", durMs: 200_000},

		// Group 3: "Arrange" - composer/lyricist missing -> Tier 2 arranger matches, disjoint arranger falls through to Tier 3
		{path: "a1.flac", title: "Arrange", album: "A1", year: 2015, artists: []string{"Artist A"}, arranger: "Arr Shared", durMs: 200_000},
		{path: "a2.flac", title: "Arrange", album: "A2", year: 2016, artists: []string{"Artist B"}, arranger: "Arr Shared", durMs: 120_000},
		{path: "a3.flac", title: "Arrange", album: "A3", year: 2017, artists: []string{"Artist A"}, arranger: "Arr Other", durMs: 208_000},

		// Group 4: "Interlude" - no credits, Tier 3 primary + duration tolerance (max(10s, 5%)) and missing duration
		{path: "i1.flac", title: "Interlude", album: "I1", year: 2010, artists: []string{"Artist I"}, durMs: 200_000},
		{path: "i2.flac", title: "Interlude", album: "I2", year: 2011, artists: []string{"Artist I"}, durMs: 210_000},
		{path: "i3.flac", title: "Interlude", album: "I3", year: 2012, artists: []string{"Artist I"}, durMs: 230_000},
		{path: "i4.flac", title: "Interlude", album: "I4", year: 2013, artists: []string{"Artist I"}, durMs: 0},

		// Group 5: "Tieup" - no credits, different primary, shared work_tracks + close duration
		{path: "w1.flac", title: "Tieup", album: "W1", year: 2020, artists: []string{"VA 1"}, durMs: 240_000},
		{path: "w2.flac", title: "Tieup", album: "W2", year: 2021, artists: []string{"VA 2"}, durMs: 250_000},
	}

	idByPath := make(map[string]int64, len(specs))
	for i, sp := range specs {
		if err := s.ImportTrack(ctx, ImportInput{
			LibraryID:    lib.ID,
			RelativePath: sp.path,
			FileSize:     100,
			ModifiedAtNS: int64(i + 1),
			Metadata: metadata.AudioMetadata{
				Title:          sp.title,
				Album:          sp.album,
				Year:           sp.year,
				Artists:        sp.artists,
				AlbumArtists:   sp.artists,
				Composer:       sp.composer,
				Lyricist:       sp.lyricist,
				Arranger:       sp.arranger,
				DiscNumber:     1,
				TrackNumber:    1,
				DurationMillis: sp.durMs,
			},
		}); err != nil {
			t.Fatalf("ImportTrack %s: %v", sp.path, err)
		}
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE relative_path=?`, sp.path).Scan(&id); err != nil {
			t.Fatalf("lookup %s: %v", sp.path, err)
		}
		idByPath[sp.path] = id
	}

	// Link w1 and w2 to a shared work.
	res, err := s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Anime X','anime x','anime','manual')`)
	if err != nil {
		t.Fatal(err)
	}
	workID, _ := res.LastInsertId()
	for _, p := range []string{"w1.flac", "w2.flac"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,source) VALUES(?,?,'op','manual')`, workID, idByPath[p]); err != nil {
			t.Fatal(err)
		}
	}

	checkVersions := func(seedPath string, wantPaths ...string) {
		t.Helper()
		_, items, e := s.TrackVersions(ctx, idByPath[seedPath])
		if e != nil {
			t.Fatalf("TrackVersions(%s): %v", seedPath, e)
		}
		var gotIDs, wantIDs []int64
		for _, it := range items {
			gotIDs = append(gotIDs, it.ID)
		}
		for _, p := range wantPaths {
			wantIDs = append(wantIDs, idByPath[p])
		}
		slices.Sort(gotIDs)
		slices.Sort(wantIDs)
		if !slices.Equal(gotIDs, wantIDs) {
			t.Fatalf("TrackVersions(%s) gotIDs=%v wantIDs=%v", seedPath, gotIDs, wantIDs)
		}
	}

	checkVersions("h1.flac", "h1.flac", "h2.flac")
	checkVersions("h3.flac", "h3.flac")
	checkVersions("h4.flac", "h4.flac")

	checkVersions("l1.flac", "l1.flac", "l2.flac")
	checkVersions("l3.flac", "l3.flac")

	// a1 matches a2 (shared arranger) AND a3 (Tier 3 primary+duration fallback); a2 does not match a3.
	checkVersions("a1.flac", "a1.flac", "a2.flac", "a3.flac")
	checkVersions("a2.flac", "a1.flac", "a2.flac")
	checkVersions("a3.flac", "a1.flac", "a3.flac")

	checkVersions("i1.flac", "i1.flac", "i2.flac")
	checkVersions("i3.flac", "i3.flac")
	checkVersions("i4.flac", "i4.flac")

	checkVersions("w1.flac", "w1.flac", "w2.flac")

	// ListTracks VersionCount must strictly match len(TrackVersions) when > 1, else 0.
	allTracks, err := s.ListTracks(ctx, Filters{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range allTracks {
		_, items, err := s.TrackVersions(ctx, tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantCount := 0
		if len(items) > 1 {
			wantCount = len(items)
		}
		if tr.VersionCount != wantCount {
			t.Fatalf("track %d (%s) VersionCount=%d, want %d", tr.ID, tr.Title, tr.VersionCount, wantCount)
		}
	}
}

func TestTrackVersionsMergedArtistsAndUserComposerOverride(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	for i, tc := range []struct {
		path     string
		composer string
	}{
		{"m1.flac", "Comp Alias"},
		{"m2.flac", "Comp Canonical"},
	} {
		if err := s.ImportTrack(ctx, ImportInput{
			LibraryID:    lib.ID,
			RelativePath: tc.path,
			FileSize:     100,
			ModifiedAtNS: int64(i + 1),
			Metadata: metadata.AudioMetadata{
				Title:        "Merged Song",
				Album:        tc.path,
				Year:         2020 + i,
				Artists:      []string{"Singer " + tc.path},
				AlbumArtists: []string{"Singer " + tc.path},
				Composer:     tc.composer,
				DiscNumber:   1,
				TrackNumber:  1,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var id1, id2, aliasID, canonID int64
	if err := s.db.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE relative_path='m1.flac'`).Scan(&id1); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE relative_path='m2.flac'`).Scan(&id2); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key='comp alias'`).Scan(&aliasID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key='comp canonical'`).Scan(&canonID); err != nil {
		t.Fatal(err)
	}

	// Before merge: disjoint composers -> 1 version each.
	_, items, err := s.TrackVersions(ctx, id1)
	if err != nil || len(items) != 1 {
		t.Fatalf("before merge len=%d err=%v", len(items), err)
	}

	// Merge aliasID -> canonID: now they match!
	if _, err := s.MergeArtists(ctx, aliasID, canonID); err != nil {
		t.Fatal(err)
	}
	_, items, err = s.TrackVersions(ctx, id1)
	if err != nil || len(items) != 2 {
		t.Fatalf("after merge len=%d err=%v", len(items), err)
	}

	// Explicit UpdateTrack user_composer override on m1 replaces scanned composer.
	if err := s.UpdateTrack(ctx, id1, "Merged Song", 1, 1, "Different User Composer", "regular", nil); err != nil {
		t.Fatal(err)
	}
	_, items, err = s.TrackVersions(ctx, id1)
	if err != nil || len(items) != 1 {
		t.Fatalf("after user_composer override len=%d err=%v", len(items), err)
	}

	// Setting user_composer to match m2's canonical name reconnects them.
	if err := s.UpdateTrack(ctx, id1, "Merged Song", 1, 1, "Comp Alias", "regular", nil); err != nil {
		t.Fatal(err)
	}
	_, items, err = s.TrackVersions(ctx, id1)
	if err != nil || len(items) != 2 {
		t.Fatalf("after user_composer matching alias len=%d err=%v", len(items), err)
	}
}
