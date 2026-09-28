package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestMigration028BackfillsManualTypeLocksAndIndex(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "028_" {
			break
		}
		script, readErr := migrationFiles.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		conn, connErr := s.db.Conn(ctx)
		if connErr != nil {
			t.Fatal(connErr)
		}
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
		version := int(entry.Name()[0]-'0')*100 + int(entry.Name()[1]-'0')*10 + int(entry.Name()[2]-'0')
		applyErr := s.applyMigration(ctx, conn, version, entry.Name(), string(script))
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		conn.Close()
		if applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Manual','manual','anime','manual'),('Auto','auto','anime','auto')`); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var manual, auto int
	if err = s.db.QueryRowContext(ctx, `SELECT type_locked FROM works WHERE normalized_title='manual'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT type_locked FROM works WHERE normalized_title='auto'`).Scan(&auto); err != nil {
		t.Fatal(err)
	}
	if manual != 1 || auto != 0 {
		t.Fatalf("locks manual=%d auto=%d", manual, auto)
	}
	if n := countQuery(t, s, ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='idx_album_subject_candidates_status_id'`); n != 1 {
		t.Fatalf("candidate index=%d", n)
	}
}

func countQuery(t *testing.T, s *Store, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestManualAlbumConfirmationBackfillsKeyForLaterRemoval(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Song", "Song", 1, 1)
	album := f.albumID(t, "Song")
	track := trackIDOf(t, f, "Song")
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.AddWorkAlbum(f.ctx, work.ID, album, "other"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveAlbumSubjectCandidates(f.ctx, album, []AlbumSubjectCandidate{{ExternalID: "8", Title: "Song", Tieups: []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidate, _ := f.store.AlbumSubjectCandidates(f.ctx, album)
	outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, album, candidate[0].ID, 0, "", false, []int64{5})
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND work_id=? AND source='manual' AND inferred_key='bangumi:8:5'`, album, work.ID); n != 1 {
		t.Fatalf("manual key not backfilled=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND inferred_key='bangumi:8:5'`, track); n != 1 {
		t.Fatalf("track row=%d", n)
	}
	if err = f.store.RemoveWorkAlbum(f.ctx, work.ID, album); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND inferred_key='bangumi:8:5'`, track); n != 0 {
		t.Fatalf("track row survived removal=%d", n)
	}
}

func TestRemoveWorkAlbumDeletesOnlyItsBangumiTrackRows(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Amore", "Amore", 1, 1)
	f.importFile(t, "A/02.flac", "Amore", "Amore (TV ver.)", 1, 2)
	album := f.albumID(t, "Amore")
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto' WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	addFRBangumiProfile(t, f, work.ID, "5", "Show", "anime")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'other','bangumi','bangumi:8:5')`, album, work.ID); err != nil {
		t.Fatal(err)
	}
	var first, second int64
	rows, err := f.store.db.QueryContext(f.ctx, `SELECT id FROM tracks WHERE album_id=? ORDER BY id`, album)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() || rows.Scan(&first) != nil || !rows.Next() || rows.Scan(&second) != nil {
		t.Fatal("tracks missing")
	}
	rows.Close()
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','bangumi','bangumi:8:5'),(?,?,'op','bangumi','bangumi:8:5'),(?,?,'ed','bangumi','bangumi:9:5'),(?,?,'insert','manual','bangumi:8:5')`, work.ID, first, work.ID, second, work.ID, first, work.ID, second); err != nil {
		t.Fatal(err)
	}
	if err = f.store.RemoveWorkAlbum(f.ctx, work.ID, album); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE inferred_key='bangumi:8:5' AND source='bangumi'`); n != 0 {
		t.Fatalf("album rows left=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE inferred_key='bangumi:9:5' AND source='bangumi'`); n != 1 {
		t.Fatalf("track-confirmed row=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE source='manual'`); n != 1 {
		t.Fatalf("manual row=%d", n)
	}
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = linkBangumiAlbumTracks(f.ctx, tx, album, work.ID, "Amore", "op", "bangumi:8:5"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE inferred_key='bangumi:8:5' AND source='bangumi'`); n != 0 {
		t.Fatalf("removed rows restored=%d", n)
	}
}

func TestManualTrackAcceptClearsLegacyTitleSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	candidate := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}, "exact")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,'show')`, track); err != nil {
		t.Fatal(err)
	}
	outcome, _, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, candidate.ID, 0, "", false, []int64{5}, nil)
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key='show'`, track); n != 0 {
		t.Fatalf("legacy suppression left=%d", n)
	}
}

func TestManualAlbumAcceptClearsExactWorkSuppressionButNotEntryRefusal(t *testing.T) {
	for _, wholeEntry := range []bool{false, true} {
		t.Run(map[bool]string{false: "work-key", true: "entry-refusal"}[wholeEntry], func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
			album := f.albumID(t, "Album")
			if err := f.store.SaveAlbumSubjectCandidates(f.ctx, album, []AlbumSubjectCandidate{{ExternalID: "8", Title: "Song", Tieups: []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}}}); err != nil {
				t.Fatal(err)
			}
			candidate, _ := f.store.AlbumSubjectCandidates(f.ctx, album)
			key := "show"
			if wholeEntry {
				key = "bangumi:8"
			}
			if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, album, key); err != nil {
				t.Fatal(err)
			}
			outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, album, candidate[0].ID, 0, "", false, []int64{5})
			if err != nil {
				t.Fatal(err)
			}
			want := "succeeded"
			if wholeEntry {
				want = "review"
			}
			if outcome != want {
				t.Fatalf("outcome=%s want=%s", outcome, want)
			}
		})
	}
}

func TestUpdateWorkFormOriginalTypePreservesConcurrentCorrection(t *testing.T) {
	f := newAlbumMergeFixture(t)
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET type='movie',type_locked=0 WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateWork(f.ctx, work.ID, WorkInput{Title: "Renamed", Type: "anime", OriginalType: "anime"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE id=? AND type='movie' AND type_locked=0`, work.ID); n != 1 {
		t.Fatalf("concurrent correction overwritten=%d", n)
	}
	if _, err = f.store.UpdateWork(f.ctx, work.ID, WorkInput{Title: "Renamed", Type: "game", OriginalType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE id=? AND type='game' AND type_locked=1`, work.ID); n != 1 {
		t.Fatalf("form type change not locked=%d", n)
	}
}

func TestUpdateWorkLocksOnlyActualTypeChange(t *testing.T) {
	f := newAlbumMergeFixture(t)
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto',type_locked=0 WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateWork(f.ctx, work.ID, WorkInput{Title: "Renamed", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT type_locked FROM works WHERE id=?`, work.ID); n != 0 {
		t.Fatalf("rename locked type=%d", n)
	}
	if _, err = f.store.UpdateWork(f.ctx, work.ID, WorkInput{Title: "Renamed", Type: "movie"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT type_locked FROM works WHERE id=?`, work.ID); n != 1 {
		t.Fatalf("type change lock=%d", n)
	}
}

func TestManualAcceptanceAfterMovieCorrectionClearsAnimeButKeepsGameSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Song", "Song", 1, 1)
	album := f.albumID(t, "Song")
	track := trackIDOf(t, f, "Song")
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto',type_locked=0 WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	addFRBangumiProfile(t, f, work.ID, "5", "X", "anime")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'x|anime|0'),(?,'x|game|0')`, album, album); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CorrectBangumiWorkType(f.ctx, work.ID, "anime", "movie", "5", 0); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveAlbumSubjectCandidates(f.ctx, album, []AlbumSubjectCandidate{{ExternalID: "8", Title: "Song", Tieups: []BangumiTieup{{SubjectID: 5, Title: "X", Type: "movie", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidate, _ := f.store.AlbumSubjectCandidates(f.ctx, album)
	outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, album, candidate[0].ID, 0, "", false, []int64{5})
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND work_id=?`, album, work.ID); n != 1 {
		t.Fatalf("album link=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND work_id=? AND role='op'`, track, work.ID); n != 1 {
		t.Fatalf("track role=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='x|anime|0'`, album); n != 0 {
		t.Fatalf("anime suppression left=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='x|game|0'`, album); n != 1 {
		t.Fatalf("game suppression removed=%d", n)
	}
}

func TestCorrectedTypeSuppressionBlocksRealBangumiRelanding(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Song", "Song", 1, 1)
	album := f.albumID(t, "Song")
	track := trackIDOf(t, f, "Song")
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto',type_locked=0 WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	addFRBangumiProfile(t, f, work.ID, "5", "X", "anime")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'x|anime|0')`, album); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CorrectBangumiWorkType(f.ctx, work.ID, "anime", "movie", "5", 0); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = linkBangumiAlbumTracks(f.ctx, tx, album, work.ID, "Song", "op", "bangumi:8:5"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND work_id=?`, track, work.ID); n != 0 {
		t.Fatalf("suppressed row relanded=%d", n)
	}
}

func TestCorrectBangumiWorkTypeConflictAndSuppressionSurvives(t *testing.T) {
	f := newAlbumMergeFixture(t)
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Show", Type: "anime", Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto',type_locked=0 WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	addFRBangumiProfile(t, f, work.ID, "5", "Show", "anime")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) SELECT id,'show|anime|0' FROM albums LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	changed, err := f.store.CorrectBangumiWorkType(f.ctx, work.ID, "anime", "movie", "5", 0)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !workTitleKeysMatch("show|anime|0", "show|movie|0") {
		t.Fatal("type correction invalidated suppression")
	}
	other, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Collision", Type: "anime", Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto',type_locked=0 WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	addFRBangumiProfile(t, f, other.ID, "6", "Collision", "anime")
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO works(title,normalized_title,type,year,origin) VALUES('Collision','collision','movie',2024,'auto')`); err != nil {
		t.Fatal(err)
	}
	changed, err = f.store.CorrectBangumiWorkType(f.ctx, other.ID, "anime", "movie", "6", 0)
	if err != nil || changed {
		t.Fatalf("conflict changed=%v err=%v", changed, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type_correction_skipped'`, other.ID); n != 1 {
		t.Fatalf("conflict evidence=%d", n)
	}
}

func TestResolveBangumiMovieDoesNotReuseUnboundAnime(t *testing.T) {
	f := newAlbumMergeFixture(t)
	anime, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Same", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	movieID, err := resolveBangumiWork(f.ctx, tx, BangumiTieup{SubjectID: 99, Title: "Same", Type: "movie"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if movieID == anime.ID {
		t.Fatal("unbound anime reused for Bangumi movie")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE id=? AND type='anime'`, anime.ID); n != 1 {
		t.Fatalf("anime changed=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE id=? AND type='movie'`, movieID); n != 1 {
		t.Fatalf("movie missing=%d", n)
	}
}

func TestBoundBangumiAnimeMovieClassReuseButUnboundStrict(t *testing.T) {
	f := newAlbumMergeFixture(t)
	bound, err := f.store.CreateWork(f.ctx, WorkInput{Title: "X", Type: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto',type_locked=0 WHERE id=?`, bound.ID); err != nil {
		t.Fatal(err)
	}
	addFRBangumiProfile(t, f, bound.ID, "5", "X", "movie")
	album := importAlbumTrack(t, f.store, f.ctx, "TVアニメ『X』オリジナルサウンドトラック", "Track", nil)
	if _, err = f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND work_id=?`, album, bound.ID); n != 1 {
		t.Fatalf("bound movie not reused=%d", n)
	}
	unbound, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Y", Type: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET type_locked=0,normalized_title='y' WHERE id=?`, unbound.ID); err != nil {
		t.Fatal(err)
	}
	importAlbumTrack(t, f.store, f.ctx, "TVアニメ『Y』オリジナルサウンドトラック", "Track", nil)
	if _, err = f.store.db.ExecContext(f.ctx, `DELETE FROM work_aliases WHERE work_id=?`, unbound.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works WHERE title='Y'`); n != 2 {
		t.Fatalf("unbound types merged count=%d", n)
	}
}

func TestBangumiTypeCompatibleSameNonACGType(t *testing.T) {
	if !bangumiTypeCompatible("drama", "drama") || !bangumiTypeCompatible("commercial", "commercial") {
		t.Fatal("equal non-ACG types should match")
	}
	if bangumiTypeCompatible("drama", "commercial") {
		t.Fatal("different non-ACG types matched")
	}
}

func TestPipeTitleSuppressionParsingFromRight(t *testing.T) {
	title, typ, season := splitInferredKey("fate|zero|anime|0")
	if title != "fate|zero" || typ != "anime" || season != 0 {
		t.Fatalf("parsed %q %q %d", title, typ, season)
	}
	if !workTitleKeysMatch("fate|zero|anime|0", "fate|zero|game|0") {
		t.Fatal("pipe title suppression did not survive type change")
	}
	if _, typ, _ = splitInferredKey("fate|zero"); typ != "" {
		t.Fatalf("legacy key parsed as typed: %q", typ)
	}
}

func TestSeasonSpellingAliasTrackLevelManualOriginCondition(t *testing.T) {
	f := newAlbumMergeFixture(t)
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "X Season 2", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_aliases(normalized_key,work_id) VALUES('x 2期',?)`, work.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	assoc := metadata.WorkAssociation{Title: "X 2期", Type: "game", Season: 2}
	id, _, err := resolveAutoWork(f.ctx, tx, assoc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id != work.ID {
		t.Fatalf("manual origin alias id=%d want=%d", id, work.ID)
	}
	if _, err = tx.ExecContext(f.ctx, `UPDATE works SET origin='auto' WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	id, created, err := resolveAutoWork(f.ctx, tx, assoc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id == work.ID || !created {
		t.Fatalf("auto alias crossed type id=%d created=%v", id, created)
	}
}

func TestAlbumEligibleForBangumiSearchExcludesCompilations(t *testing.T) {
	for _, mode := range []string{"user", "tag", "various"} {
		t.Run(mode, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
			album := f.albumID(t, "Album")
			switch mode {
			case "user":
				_, _ = f.store.db.ExecContext(f.ctx, `UPDATE albums SET user_is_compilation=1 WHERE id=?`, album)
			case "tag":
				_, _ = f.store.db.ExecContext(f.ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) SELECT af.id,'COMPILATION','1',0 FROM audio_files af JOIN tracks t ON t.id=af.track_id WHERE t.album_id=?`, album)
			case "various":
				_, _ = f.store.db.ExecContext(f.ctx, `UPDATE artists SET display_name='Various Artists' WHERE id IN (SELECT artist_id FROM album_artists WHERE album_id=?)`, album)
			}
			if ok, err := f.store.AlbumEligibleForBangumiSearch(f.ctx, album); err != nil || ok {
				t.Fatalf("mode=%s eligible=%v err=%v", mode, ok, err)
			}
		})
	}
}

func TestAlbumEligibleForBangumiSearchBlockedBySuppressionOrDecision(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	album := f.albumID(t, "Album")
	if ok, err := f.store.AlbumEligibleForBangumiSearch(f.ctx, album); err != nil || !ok {
		t.Fatalf("initial ok=%v err=%v", ok, err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'bangumi:8:5')`, album); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.AlbumEligibleForBangumiSearch(f.ctx, album); err != nil || ok {
		t.Fatalf("suppressed ok=%v err=%v", ok, err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `DELETE FROM album_work_suppressions WHERE album_id=?; INSERT INTO album_subject_candidates(album_id,source,external_id,title,status) VALUES(?,'bangumi','8','Song','confirmed')`, album, album); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.AlbumEligibleForBangumiSearch(f.ctx, album); err != nil || ok {
		t.Fatalf("confirmed ok=%v err=%v", ok, err)
	}
}
