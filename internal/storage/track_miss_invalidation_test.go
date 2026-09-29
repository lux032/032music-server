package storage

import (
	"testing"
)

// D-18/D63: when a manual acceptance lifts an album-level suppression, the
// bangumi misses the suppression caused on the album's tracks are deleted in
// the same transaction, so the next regular (non-force) run re-checks them.
func TestAddWorkAlbumInvalidatesTrackBangumiMisses(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, e := s.ListWorks(ctx, WorkFilters{})
	if e != nil || len(works) != 1 {
		t.Fatalf("works=%+v %v", works, e)
	}
	workID := works[0].ID
	var trackID int64
	if e = s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE album_id=?`, album).Scan(&trackID); e != nil {
		t.Fatal(e)
	}
	// The user removes the automatic link: the suppression is written.
	if e = s.RemoveWorkAlbum(ctx, workID, album); e != nil {
		t.Fatal(e)
	}
	// The next tieup scan finds every matching entry suppressed and records a
	// miss; the fresh miss hides the track from non-force runs. L4: the
	// caller passes the evaluated (music, tieup) links so the storage layer
	// can re-verify the suppression inside its transaction.
	targets, e := s.TracksForBangumiTieup(ctx, false, album)
	if e != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v %v", targets, e)
	}
	var workTitle, workType string
	if e = s.db.QueryRowContext(ctx, `SELECT title,type FROM works WHERE id=?`, workID).Scan(&workTitle, &workType); e != nil {
		t.Fatal(e)
	}
	links := []BangumiSuppressedLink{{Music: "music-1", Tie: BangumiTieup{SubjectID: 42, Title: workTitle, Type: workType}}}
	if e = s.SetSuppressedTrackBangumiMiss(ctx, targets[0], links); e != nil {
		t.Fatal(e)
	}
	if targets, e = s.TracksForBangumiTieup(ctx, false, album); e != nil || len(targets) != 0 {
		t.Fatalf("miss did not hide the track: %+v %v", targets, e)
	}
	// The user re-adds the work by hand: the suppression goes away and the
	// miss goes with it.
	if e = s.AddWorkAlbum(ctx, workID, album, ""); e != nil {
		t.Fatal(e)
	}
	var misses int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=? AND source='bangumi'`, trackID).Scan(&misses); e != nil || misses != 0 {
		t.Fatalf("misses=%d %v", misses, e)
	}
	if targets, e = s.TracksForBangumiTieup(ctx, false, album); e != nil || len(targets) != 1 {
		t.Fatalf("track not re-checked after suppression lifted: %+v %v", targets, e)
	}
}

// D-18/D63 at track level: a manual acceptance of a track candidate drops the
// track's suppressions and its bangumi miss in the same transaction.
func TestConfirmTrackSubjectClearsMiss(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	work, e := f.store.CreateWork(f.ctx, WorkInput{Title: "Track Show", Type: "anime"})
	if e != nil {
		t.Fatal(e)
	}
	// A Bangumi-linked row the user then removes: suppressions (title
	// identities + bangumi:<m>:<w>) are written.
	if _, e = f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','bangumi','bangumi:555:777')`, work.ID, track); e != nil {
		t.Fatal(e)
	}
	if e = f.store.RemoveWorkTrack(f.ctx, work.ID, track, "", 0, 0); e != nil {
		t.Fatal(e)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=?`, track); n == 0 {
		t.Fatal("no suppression written by removal")
	}
	// The candidate is saved first (saving candidates itself clears misses);
	// the miss is recorded afterwards, so only the suppression lifting during
	// the manual acceptance can remove it (D63).
	stored := saveOneTrackCandidate(t, f, track, "555", []BangumiTieup{{SubjectID: 777, Title: "Track Show", Type: "anime", Role: "op"}}, "exact")
	if e = f.store.SetTrackBangumiMiss(f.ctx, TrackBangumiTarget{ID: track, Fingerprint: "fp"}, "all matching entries suppressed"); e != nil {
		t.Fatal(e)
	}
	outcome, ids, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", false, []int64{777}, nil)
	if err != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if ids[0] != work.ID {
		t.Fatalf("work resolved to %d, want %d", ids[0], work.ID)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=? AND source='bangumi'`, track); n != 0 {
		t.Fatalf("miss left=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=?`, track); n != 0 {
		t.Fatalf("suppressions left=%d", n)
	}
}
