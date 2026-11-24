package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueryPlansNoOptionalGuardFullScans(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	whereTrack, argsTrack := trackWhere(Filters{AlbumID: 42})
	trackPlan := explainQueryPlan(t, s, `SELECT t.id FROM tracks t JOIN albums a ON a.id=t.album_id LEFT JOIN playback_progress pp ON pp.track_id=t.id WHERE `+whereTrack+` ORDER BY `+trackOrder(Filters{AlbumID: 42})+` LIMIT 50 OFFSET 0`, argsTrack...)
	if strings.Contains(trackPlan, "SCAN a") || strings.Contains(trackPlan, "SCAN t") {
		t.Fatalf("ListTracks(AlbumID) still full-scans:\n%s", trackPlan)
	}

	whereAlbum, argsAlbum := albumWhere(Filters{ArtistID: 7})
	albumPlan := explainQueryPlan(t, s, `SELECT a.id FROM albums a WHERE `+whereAlbum+` ORDER BY `+albumOrder(Filters{ArtistID: 7})+` LIMIT 50 OFFSET 0`, argsAlbum...)
	if strings.Contains(albumPlan, "SCAN a") {
		t.Fatalf("ListAlbums(ArtistID) still full-scans albums:\n%s", albumPlan)
	}

	whereWork, argsWork := workWhere(WorkFilters{Type: "anime", Year: 2024})
	workPlan := explainQueryPlan(t, s, `SELECT id FROM works WHERE `+whereWork, argsWork...)
	if !strings.Contains(workPlan, "idx_works_type_year") {
		t.Fatalf("ListWorks(Type,Year) did not use idx_works_type_year:\n%s", workPlan)
	}

	artistImgPlan := explainQueryPlan(t, s, `SELECT ar.id,`+artistImageURLSQL("ar")+` FROM artists ar WHERE ar.id=?`, 1)
	if strings.Contains(artistImgPlan, "SCAN ma") || strings.Contains(artistImgPlan, "SCAN ci") || strings.Contains(artistImgPlan, "SCAN ai") {
		t.Fatalf("artistImageURLSQL still scans:\n%s", artistImgPlan)
	}

	recentPlan := explainQueryPlan(t, s, `WITH album_lp AS MATERIALIZED (SELECT pt.album_id,MAX(pp.last_played_at) AS lp FROM playback_progress pp JOIN tracks pt ON pt.id=pp.track_id WHERE pp.last_played_at IS NOT NULL GROUP BY pt.album_id) SELECT a.id FROM albums a LEFT JOIN album_lp al ON al.album_id=a.id WHERE `+albumVisibleSQL("a.id")+` ORDER BY COALESCE(al.lp,'')='' ASC,COALESCE(al.lp,'') DESC,a.sort_title,a.id LIMIT 50 OFFSET 0`)
	if strings.Contains(recentPlan, "CORRELATED SCALAR SUBQUERY 1") || strings.Count(recentPlan, "SCAN pp") > 1 {
		t.Fatalf("recentlyPlayed still evaluates playback_progress per album:\n%s", recentPlan)
	}
	if !strings.Contains(recentPlan, "MATERIALIZE album_lp") {
		t.Fatalf("recentlyPlayed did not materialize album_lp once:\n%s", recentPlan)
	}

	effPlan := explainQueryPlan(t, s, `SELECT track_id,ended_at,COALESCE(end_reason,'') FROM (SELECT track_id,ended_at,end_reason,ROW_NUMBER() OVER (PARTITION BY track_id ORDER BY ended_at DESC, rowid DESC) AS rn FROM playback_sessions WHERE track_id IN (?,?) AND ended_at IS NOT NULL) WHERE rn=1`, 1, 2)
	if strings.Contains(effPlan, "CORRELATED SCALAR SUBQUERY") {
		t.Fatalf("effectivePlaybackStates still uses correlated subquery:\n%s", effPlan)
	}
}

func TestEffectivePlaybackStatesManyEndedSessions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "eff.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO libraries(id,name,root_path) VALUES(1,'lib','/music')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO albums(id,library_id,title,sort_title,grouping_key) VALUES(1,1,'Alb','alb','alb')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tracks(id,album_id,title,sort_title) VALUES(1,1,'T1','t1'),(2,1,'T2','t2')`); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format(playbackTimeLayout)
		reason := "stopped"
		if i == 59 {
			reason = "completed"
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO playback_sessions(session_id,chain_id,client_id,client_kind,track_id,state,seq,started_at,last_heartbeat_at,ended_at,end_reason) VALUES(?,?,?,'web',1,'ended',1,?,?,?,?)`,
			fmt.Sprintf("s1-%03d", i), fmt.Sprintf("c1-%03d", i), "cli", ts, ts, ts, reason); err != nil {
			t.Fatal(err)
		}
	}
	// Tie on ended_at for track 2: higher rowid must win.
	sameTS := base.Add(2 * time.Hour).Format(playbackTimeLayout)
	for i, reason := range []string{"stopped", "skipped"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO playback_sessions(session_id,chain_id,client_id,client_kind,track_id,state,seq,started_at,last_heartbeat_at,ended_at,end_reason) VALUES(?,?,?,'web',2,'ended',1,?,?,?,?)`,
			fmt.Sprintf("s2-%d", i), fmt.Sprintf("c2-%d", i), "cli", sameTS, sameTS, sameTS, reason); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.effectivePlaybackStates(ctx, []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "completed" {
		t.Fatalf("track 1 state = %q, want completed", got[1])
	}
	if got[2] != "skipped" {
		t.Fatalf("track 2 tie-break state = %q, want skipped", got[2])
	}
}

func TestRandomAlbumExclusionAndFallback(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "rand.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO libraries(id,name,root_path) VALUES(1,'lib','/music')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO albums(id,library_id,title,sort_title,grouping_key) VALUES(1,1,'A1','a1','a1'),(2,1,'A2','a2','a2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tracks(id,album_id,title,sort_title) VALUES(1,1,'T1','t1'),(2,2,'T2','t2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,status) VALUES(1,1,'1.flac',1,1,'available')`); err != nil {
		t.Fatal(err)
	}
	// Only album 1 is visible: excluding 1 must fall back to 1.
	a, err := s.RandomAlbum(ctx, 1)
	if err != nil || a.ID != 1 {
		t.Fatalf("fallback album = %+v err=%v, want id=1", a, err)
	}
	// Make album 2 visible too: excluding 1 must always return 2.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,status) VALUES(1,2,'2.flac',1,1,'available')`); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		a, err := s.RandomAlbum(ctx, 1)
		if err != nil || a.ID != 2 {
			t.Fatalf("excluded album = %+v err=%v, want id=2", a, err)
		}
	}
}

func TestFixExpiredPlaybackSessionsIdleSkipsWriteLock(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "sweeper-idle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Hold the SQLite write lock on a separate transaction while calling
	// FixExpiredPlaybackSessions with no expired rows: a read-only check must
	// return 0 immediately without waiting on or failing against the write lock.
	tx, err := s.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	fixed, err := s.FixExpiredPlaybackSessions(shortCtx)
	if err != nil || fixed != 0 {
		t.Fatalf("idle FixExpiredPlaybackSessions while write lock held = (%d, %v), want (0, nil)", fixed, err)
	}
}

func TestSeriesAlbumAndTrackCountsMatchesFullHydration(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "series-counts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO libraries(id,name,root_path) VALUES(1,'L','/music')`,
		`INSERT INTO albums(id,library_id,title,sort_title,grouping_key) VALUES(1,1,'Shared OST','Shared OST','a1'),(2,1,'Direct Only Album','Direct Only Album','a2'),(3,1,'Missing Album','Missing Album','a3')`,
		`INSERT INTO tracks(id,album_id,title,sort_title,disc_number,track_number) VALUES(1,1,'T1','T1',1,1),(2,1,'T2','T2',1,2),(3,1,'T3 Missing','T3 Missing',1,3),(4,2,'Direct T4','Direct T4',1,1),(5,3,'Hidden T5','Hidden T5',1,1)`,
		`INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,status) VALUES(1,1,'1.flac',1,1,'available'),(1,2,'2.flac',1,1,'available'),(1,3,'3.flac',1,1,'missing'),(1,4,'4.flac',1,1,'available'),(1,5,'5.flac',1,1,'missing')`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	w1, err := s.CreateWork(ctx, WorkInput{Title: "W1", Type: "anime", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.CreateWork(ctx, WorkInput{Title: "W2", Type: "anime", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}
	// Both works link Shared OST (album 1) and Missing Album (album 3);
	// w1 also directly links T1 (overlap with album 1) and T4 (from album 2, no album_works row);
	// w2 also directly links missing T3.
	if err := s.AddWorkAlbum(ctx, w1.ID, 1, "ost"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkAlbum(ctx, w2.ID, 1, "ost"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkAlbum(ctx, w2.ID, 3, "ost"); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct {
		workID  int64
		trackID int64
	}{
		{w1.ID, 1},
		{w1.ID, 4},
		{w2.ID, 3},
	} {
		if err := s.AddWorkTrack(ctx, link.workID, WorkTrackInput{TrackID: link.trackID, Role: "op"}); err != nil {
			t.Fatal(err)
		}
	}
	seriesID, err := s.CreateWorkSeries(ctx, "Test Saga", []int64{w1.ID, w2.ID})
	if err != nil {
		t.Fatal(err)
	}

	gotAlbums, gotTracks, err := s.SeriesAlbumAndTrackCounts(ctx, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	members, err := s.SeriesMembers(ctx, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	seenAlbums := map[int64]bool{}
	seenTracks := map[int64]bool{}
	for _, m := range members {
		albums, err := s.AlbumsForWork(ctx, m.Work.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range albums {
			seenAlbums[a.AlbumID] = true
		}
		tracks, err := s.TracksForWork(ctx, m.Work.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range tracks {
			seenTracks[tr.ID] = true
		}
	}
	// Expected: 1 visible album (album 1; album 3 has only missing audio) and 3 visible tracks (1, 2, 4).
	if gotAlbums != len(seenAlbums) || gotTracks != len(seenTracks) || gotAlbums != 1 || gotTracks != 3 {
		t.Fatalf("SeriesAlbumAndTrackCounts = (%d, %d), hydrated = (%d, %d), want (1, 3)", gotAlbums, gotTracks, len(seenAlbums), len(seenTracks))
	}

	// Empty series returns (0, 0).
	if _, err := s.db.ExecContext(ctx, `DELETE FROM work_series_members WHERE series_id=?`, seriesID); err != nil {
		t.Fatal(err)
	}
	gotAlbums, gotTracks, err = s.SeriesAlbumAndTrackCounts(ctx, seriesID)
	if err != nil || gotAlbums != 0 || gotTracks != 0 {
		t.Fatalf("empty SeriesAlbumAndTrackCounts = (%d, %d, %v), want (0, 0, nil)", gotAlbums, gotTracks, err)
	}
}
