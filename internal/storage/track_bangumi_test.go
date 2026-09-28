package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestMigration027CreatesTrackBangumiTables(t *testing.T) {
	f := newAlbumMergeFixture(t)
	for _, table := range []string{"track_subject_candidates", "track_enrichment_misses"} {
		var sqlText string
		if err := f.store.db.QueryRowContext(f.ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&sqlText); err != nil || !strings.Contains(sqlText, "STRICT") {
			t.Fatalf("%s sql=%q err=%v", table, sqlText, err)
		}
	}
	var n int
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name='idx_track_subject_candidates_status'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("index=%d err=%v", n, err)
	}
}

func trackIDOf(t *testing.T, f albumMergeFixture, album string) int64 {
	t.Helper()
	var id int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT t.id FROM tracks t JOIN albums a ON a.id=t.album_id WHERE a.title=? ORDER BY t.id LIMIT 1`, album).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func saveOneTrackCandidate(t *testing.T, f albumMergeFixture, trackID int64, external string, ties []BangumiTieup, kind string) TrackSubjectCandidate {
	t.Helper()
	if kind == "" {
		kind = "exact"
	}
	if err := f.store.SaveTrackSubjectCandidates(f.ctx, trackID, []TrackSubjectCandidate{{ExternalID: external, Title: "Music", Artist: "Singer", MatchKind: kind, Tieups: ties}}); err != nil {
		t.Fatal(err)
	}
	stored, err := f.store.TrackSubjectCandidates(f.ctx, trackID)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	return stored[0]
}

func TestH2TracksForBangumiTieupFilters(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "Plain/01.flac", "Plain", "Song", 1, 1)
	f.importFile(t, "Drama/01.flac", "Drama Album", "Talk", 1, 1)
	f.importFile(t, "Blank/01.flac", "Blank", "   ", 1, 1)
	f.importFile(t, "OST/01.flac", "OST Album", "Theme", 1, 1)
	f.importFile(t, "AutoOST/01.flac", "Auto OST", "Theme", 1, 1)
	f.importFile(t, "VA/01.flac", "Compilation", "Song", 1, 1)
	drama := f.albumID(t, "Drama Album")
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE tracks SET track_type='drama_track' WHERE album_id=?`, drama); err != nil {
		t.Fatal(err)
	}
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	ost := f.albumID(t, "OST Album")
	auto := f.albumID(t, "Auto OST")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source) VALUES(? ,?,'ost','bangumi'),(? ,?,'ost','auto')`, ost, work.ID, auto, work.ID); err != nil {
		t.Fatal(err)
	}
	comp := f.albumID(t, "Compilation")
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE albums SET is_compilation=1 WHERE id=?`, comp); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'bangumi:9')`, f.albumID(t, "Plain")); err != nil {
		t.Fatal(err)
	}
	targets, err := f.store.TracksForBangumiTieup(f.ctx, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, v := range targets {
		got[v.AlbumTitle] = true
	}
	if !got["Plain"] || !got["Auto OST"] || !got["Compilation"] {
		t.Fatalf("missing eligible albums: %+v", targets)
	}
	if got["OST Album"] || got["Drama Album"] || got["Blank"] {
		t.Fatalf("excluded album leaked: %+v", targets)
	}
	plain := trackIDOf(t, f, "Plain")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source) VALUES(?,?,'op','bangumi')`, work.ID, plain); err != nil {
		t.Fatal(err)
	}
	targets, err = f.store.TracksForBangumiTieup(f.ctx, true, f.albumID(t, "Plain"))
	if err != nil || len(targets) != 0 {
		t.Fatalf("bangumi row still listed: %+v %v", targets, err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `DELETE FROM work_tracks WHERE track_id=?`, plain); err != nil {
		t.Fatal(err)
	}
	candidate := saveOneTrackCandidate(t, f, plain, "1", nil, "exact")
	targets, err = f.store.TracksForBangumiTieup(f.ctx, false, f.albumID(t, "Plain"))
	if err != nil || len(targets) != 0 {
		t.Fatalf("pending candidate listed: %+v %v", targets, err)
	}
	targets, err = f.store.TracksForBangumiTieup(f.ctx, true, f.albumID(t, "Plain"))
	if err != nil || len(targets) != 1 {
		t.Fatalf("force should revisit pending: %+v %v", targets, err)
	}
	if err = f.store.RejectTrackSubjectCandidate(f.ctx, plain, candidate.ID); err != nil {
		t.Fatal(err)
	}
	targets, err = f.store.TracksForBangumiTieup(f.ctx, true, f.albumID(t, "Plain"))
	if err != nil || len(targets) != 0 {
		t.Fatalf("rejected still listed: %+v %v", targets, err)
	}
}

func TestD12TrackBangumiRecheckInterval(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Recent", "Song", 1, 1)
	f.importFile(t, "B/01.flac", "Old", "Song", 1, 1)
	recent := f.albumID(t, "Recent")
	old := f.albumID(t, "Old")
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET release_date=? WHERE id=?`, time.Now().Add(-10*24*time.Hour).Format("2006-01-02"), recent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET release_date='2001-01-01' WHERE id=?`, old); err != nil {
		t.Fatal(err)
	}
	setting, err := f.store.MetadataSourceSetting(f.ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	setting.CacheDays = 30
	if err = f.store.SaveMetadataSourceSetting(f.ctx, setting); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Recent", "Old"} {
		targets, e := f.store.TracksForBangumiTieup(f.ctx, false, f.albumID(t, title))
		if e != nil || len(targets) != 1 {
			t.Fatalf("%s %+v %v", title, targets, e)
		}
		if e = f.store.SetTrackBangumiMiss(f.ctx, targets[0], "none"); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE track_enrichment_misses SET checked_at=?`, time.Now().Add(-10*24*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	recentTargets, err := f.store.TracksForBangumiTieup(f.ctx, false, recent)
	if err != nil || len(recentTargets) != 1 || !recentTargets[0].Recheck {
		t.Fatalf("recent %+v %v", recentTargets, err)
	}
	oldTargets, err := f.store.TracksForBangumiTieup(f.ctx, false, old)
	if err != nil || len(oldTargets) != 0 {
		t.Fatalf("old still waiting %+v %v", oldTargets, err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE track_enrichment_misses SET checked_at=? WHERE track_id=?`, time.Now().Add(-91*24*time.Hour).UTC().Format(time.RFC3339Nano), trackIDOf(t, f, "Old")); err != nil {
		t.Fatal(err)
	}
	oldTargets, err = f.store.TracksForBangumiTieup(f.ctx, false, old)
	if err != nil || len(oldTargets) != 1 || !oldTargets[0].Recheck {
		t.Fatalf("expired old %+v %v", oldTargets, err)
	}
	oldTargets, err = f.store.TracksForBangumiTieup(f.ctx, true, old)
	if err != nil || len(oldTargets) != 1 {
		t.Fatalf("force %+v %v", oldTargets, err)
	}
}

func TestH4AlbumRefusalBlocksTrackConfirmation(t *testing.T) {
	for _, path := range []string{"suppression", "rejected-candidate"} {
		t.Run(path, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
			album := f.albumID(t, "Album")
			track := trackIDOf(t, f, "Album")
			stored := saveOneTrackCandidate(t, f, track, "77", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}, "exact")
			if path == "suppression" {
				if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'bangumi:77')`, album); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_subject_candidates(album_id,source,external_id,title,status) VALUES(?,'bangumi','77','Music','rejected')`, album); err != nil {
				t.Fatal(err)
			}
			outcome, ids, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", true, []int64{5}, nil)
			if err != nil || outcome != "review" || len(ids) != 0 {
				t.Fatalf("outcome=%s ids=%v err=%v", outcome, ids, err)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='bangumi'`, track); n != 0 {
				t.Fatalf("wrote %d", n)
			}
		})
	}
}

func TestM1TrackLinkSuppressionPaths(t *testing.T) {
	cases := []struct {
		name string
		key  string
		tie  BangumiTieup
	}{
		{name: "exact", key: "bangumi:8:5", tie: BangumiTieup{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}},
		{name: "external", key: "bangumi:1:5", tie: BangumiTieup{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}},
		{name: "title before resolve", key: "show|anime|0", tie: BangumiTieup{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}},
		{name: "identity after resolve", key: "alias|game|0", tie: BangumiTieup{SubjectID: 5, Title: "Different", Type: "anime", Role: "op"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
			track := trackIDOf(t, f, "Album")
			album := f.albumID(t, "Album")
			work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Canonical", Type: "anime"})
			if err != nil {
				t.Fatal(err)
			}
			addFRBangumiProfile(t, f, work.ID, "5", "Canonical", "anime")
			if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_aliases(normalized_key,work_id) VALUES('alias',?)`, work.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,?)`, track, tc.key); err != nil {
				t.Fatal(err)
			}
			tx, err := f.store.db.BeginTx(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			workID := int64(0)
			if tc.name == "identity after resolve" {
				workID = work.ID
			}
			suppressed, err := bangumiTrackLinkSuppressed(f.ctx, tx, track, album, "8", tc.tie, workID)
			if err != nil || !suppressed {
				t.Fatalf("suppressed=%v err=%v", suppressed, err)
			}
		})
	}
}

func TestM1AnyBangumiSuppressionSendsAutomaticConfirmToReview(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	stored := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}, "exact")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,'bangumi:other')`, track); err != nil {
		t.Fatal(err)
	}
	outcome, _, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", true, []int64{5}, nil)
	if err != nil || outcome != "review" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
}

func TestLandBangumiTrackDoesNotOverwriteManualAndDropsAuto(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','manual','keep'),(?,?,'ed','auto','local')`, work.ID, track, work.ID, track); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrote, err := landBangumiTrack(f.ctx, tx, track, work.ID, "op", "bangumi:1:2")
	if err != nil || wrote {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='manual' AND role='op'`, track); n != 1 {
		t.Fatalf("manual=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='auto'`, track); n != 0 {
		t.Fatalf("auto survived=%d", n)
	}
}

func TestD9BangumiConfirmRemovesAutoAndRefreshDoesNotRestore(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	album := f.albumID(t, "Album")
	local, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Local Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'other','auto','local show|anime|0')`, local.ID, track); err != nil {
		t.Fatal(err)
	}
	stored := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Remote", Type: "anime", Role: "op"}}, "exact")
	outcome, ids, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", true, []int64{5}, nil)
	if err != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='auto'`, track); n != 0 {
		t.Fatalf("auto left=%d", n)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE albums SET user_title='Local Show Original Soundtrack',work_fingerprint=NULL WHERE id=?`, album); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE tracks SET title=? WHERE id=?`, "TVアニメ『Other Show』オープニングテーマ", track); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='auto'`, track); n != 0 {
		t.Fatalf("auto restored=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='bangumi' AND role='op'`, track); n != 1 {
		t.Fatalf("bangumi=%d", n)
	}
}

func TestTrackCandidatesFollowAlbumMergeAndCascadeOnDelete(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Keep", "Song", 1, 1)
	f.importFile(t, "B/01.flac", "Drop", "Other", 1, 1)
	keep := f.albumID(t, "Keep")
	drop := f.albumID(t, "Drop")
	track := trackIDOf(t, f, "Drop")
	targets, err := f.store.TracksForBangumiTieup(f.ctx, false, drop)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if err = f.store.SetTrackBangumiMiss(f.ctx, targets[0], "none"); err != nil {
		t.Fatal(err)
	}
	other := trackIDOf(t, f, "Keep")
	saveOneTrackCandidate(t, f, other, "3", nil, "exact")
	if _, err := f.store.MergeAlbums(f.ctx, keep, []int64{drop}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_subject_candidates WHERE track_id=?`, other); n != 1 {
		t.Fatalf("candidate lost=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=?`, track); n != 1 {
		t.Fatalf("miss lost=%d", n)
	}
	var albumID int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT album_id FROM tracks WHERE id=?`, track).Scan(&albumID); err != nil || albumID != keep {
		t.Fatalf("album=%d err=%v", albumID, err)
	}
	if _, err := f.store.DeleteAlbums(f.ctx, []int64{keep}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_subject_candidates`); n != 0 {
		t.Fatalf("candidates left=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_enrichment_misses`); n != 0 {
		t.Fatalf("misses left=%d", n)
	}
}

func TestConfirmTrackSubjectRoleOverrideIsManual(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	stored := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}, {SubjectID: 6, Title: "Other", Type: "anime", Role: "ed"}}, "multi_title")
	outcome, ids, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", false, []int64{5, 6}, map[int64]string{5: "insert"})
	if err != nil || outcome != "succeeded" || len(ids) != 2 {
		t.Fatalf("outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND role='insert' AND source='manual'`, track); n != 1 {
		t.Fatalf("manual override=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND role='ed' AND source='bangumi'`, track); n != 1 {
		t.Fatalf("untouched=%d", n)
	}
}

func TestSaveTrackCandidatesClearsMiss(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	if err := f.store.SetTrackBangumiMiss(f.ctx, TrackBangumiTarget{ID: track, Fingerprint: "fp"}, "none"); err != nil {
		t.Fatal(err)
	}
	saveOneTrackCandidate(t, f, track, "1", nil, "exact")
	if n := f.count(t, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=?`, track); n != 0 {
		t.Fatalf("miss left=%d", n)
	}
}

func TestRejectTrackSubjectWritesExternalSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	stored := saveOneTrackCandidate(t, f, track, "44", nil, "exact")
	if err := f.store.RejectTrackSubjectCandidate(f.ctx, track, stored.ID); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key='bangumi:44'`, track); n != 1 {
		t.Fatalf("suppression=%d", n)
	}
}

func TestH1TrackRejectionBlocksAlbumLevelLanding(t *testing.T) {
	for _, mode := range []string{"automatic", "manual"} {
		t.Run(mode, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
			track := trackIDOf(t, f, "Album")
			album := f.albumID(t, "Album")
			stored := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}, "exact")
			if err := f.store.RejectTrackSubjectCandidate(f.ctx, track, stored.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.store.SaveAlbumSubjectCandidates(f.ctx, album, []AlbumSubjectCandidate{{ExternalID: "8", Title: "Song", Tieups: []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}}}); err != nil {
				t.Fatal(err)
			}
			candidates, err := f.store.AlbumSubjectCandidates(f.ctx, album)
			if err != nil || len(candidates) != 1 {
				t.Fatalf("candidates=%+v err=%v", candidates, err)
			}
			var outcome string
			if mode == "automatic" {
				targets, targetErr := f.store.AlbumsForBangumiTieup(f.ctx, true, album)
				if targetErr != nil || len(targets) != 1 {
					t.Fatalf("targets=%+v err=%v", targets, targetErr)
				}
				outcome, _, err = f.store.ConfirmAlbumSubjectCandidate(f.ctx, album, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{5})
			} else {
				outcome, _, err = f.store.ConfirmAlbumSubjectCandidate(f.ctx, album, candidates[0].ID, 0, "", false, nil)
			}
			if err != nil || outcome != "succeeded" {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, album); n != 1 {
				t.Fatalf("album link=%d", n)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='bangumi'`, track); n != 0 {
				t.Fatalf("rejected track landed %d bangumi rows", n)
			}
		})
	}
}

// M-1: a D10 manual row (role override, inferred_key bangumi:<m>:<w>) removed by
// the user suppresses exactly like a bangumi row, so an album-level automatic
// confirmation of the same entry cannot add it back.
func TestM1RemovedD10ManualRowSuppressesReAdd(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	album := f.albumID(t, "Album")
	stored := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}, "exact")
	outcome, ids, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", false, []int64{5}, map[int64]string{5: "insert"})
	if err != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='manual' AND inferred_key='bangumi:8:5'`, track); n != 1 {
		t.Fatalf("manual row=%d", n)
	}
	if err = f.store.RemoveWorkTrack(f.ctx, ids[0], track, "insert", 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key='bangumi:8:5'`, track); n != 1 {
		t.Fatalf("key suppression=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key LIKE 'show|%'`, track); n != 1 {
		t.Fatalf("title suppression=%d", n)
	}
	if err = f.store.SaveAlbumSubjectCandidates(f.ctx, album, []AlbumSubjectCandidate{{ExternalID: "8", Title: "Song", Tieups: []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, err := f.store.AlbumSubjectCandidates(f.ctx, album)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	targets, err := f.store.AlbumsForBangumiTieup(f.ctx, true, album)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	outcome, _, err = f.store.ConfirmAlbumSubjectCandidate(f.ctx, album, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{5})
	if err != nil || outcome != "succeeded" {
		t.Fatalf("album outcome=%s err=%v", outcome, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, track); n != 0 {
		t.Fatalf("re-added %d rows", n)
	}
}

// M-5: a manual acceptance may reverse the suppressions a removal wrote. The
// whole-entry bangumi:<music> rejection would still block, but title identities
// and bangumi:*:<work> keys are cleared before the link lands.
func TestM5ManualAcceptAfterRemovalClearsSuppressions(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	stored := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}, "exact")
	outcome, ids, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", true, []int64{5}, nil)
	if err != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("auto outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if err = f.store.RemoveWorkTrack(f.ctx, ids[0], track, "op", 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=?`, track); n == 0 {
		t.Fatal("removal wrote no suppression")
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE track_subject_candidates SET status='candidate' WHERE id=?`, stored.ID); err != nil {
		t.Fatal(err)
	}
	outcome, ids, err = f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", false, []int64{5}, nil)
	if err != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("manual outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='bangumi' AND role='op'`, track); n != 1 {
		t.Fatalf("row=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND (inferred_key='bangumi:8:5' OR inferred_key LIKE 'show|%')`, track); n != 0 {
		t.Fatalf("suppressions left=%d", n)
	}
}

// 决策 A: a manual acceptance only lifts the title suppression keys whose type
// matches the work being accepted. Suppressing anime "X" and then accepting a
// game also titled "X" must keep the anime key and still land the game row.
func TestManualAcceptKeepsOtherTypeTitleSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	stored := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Cross", Type: "game", Role: "op"}}, "exact")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,'cross|anime|0')`, track); err != nil {
		t.Fatal(err)
	}
	outcome, ids, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, stored.ID, 0, "", false, []int64{5}, nil)
	if err != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("outcome=%s ids=%v err=%v", outcome, ids, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='bangumi' AND role='op'`, track); n != 1 {
		t.Fatalf("game row=%d", n)
	}
	var typ string
	if err = f.store.db.QueryRowContext(f.ctx, `SELECT type FROM works WHERE id=?`, ids[0]).Scan(&typ); err != nil || typ != "game" {
		t.Fatalf("type=%s err=%v", typ, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key='cross|anime|0'`, track); n != 1 {
		t.Fatalf("anime suppression=%d", n)
	}
}

// L-5: once a track carries a manual or Bangumi row, local inference stops
// before resolveAutoWork, so no orphan auto work is created and WorksCreated is
// not inflated.
func TestL5AuthoritativeTrackSkipsInferenceBeforeResolve(t *testing.T) {
	for _, decided := range []bool{false, true} {
		name := "control creates work"
		if decided {
			name = "manual row skips before resolve"
		}
		t.Run(name, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
			track := trackIDOf(t, f, "Album")
			album := f.albumID(t, "Album")
			if decided {
				other, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Chosen", Type: "anime"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source) VALUES(?,?,'op','manual')`, other.ID, track); err != nil {
					t.Fatal(err)
				}
			}
			before := f.count(t, `SELECT COUNT(*) FROM works`)
			tx, err := f.store.db.BeginTx(f.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			created, err := ensureAutoWorkAssociation(f.ctx, tx, track, album, metadata.WorkAssociation{Title: "Brand New Show", Type: "anime", Role: "op"})
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if created == decided {
				t.Fatalf("created=%v decided=%v", created, decided)
			}
			after := f.count(t, `SELECT COUNT(*) FROM works`)
			if decided && after != before {
				t.Fatalf("orphan work created: before=%d after=%d", before, after)
			}
			if !decided && after != before+1 {
				t.Fatalf("control did not create the work: before=%d after=%d", before, after)
			}
		})
	}
}
