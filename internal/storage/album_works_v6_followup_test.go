package storage

import (
	"context"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

// TestRefreshAlbumWorksV6SuppressionRewriteNoResurrect (H1): the suppression
// carryover rewrites the old key (writes the v6 key, deletes the v5 row), so
// lifting the suppression afterwards cannot resurrect it on the next refresh.
// The lift is exercised through both manual paths: AddWorkAlbum and a manual
// candidate confirm (clearManualSuppressions). The "copy instead of rewrite"
// mutation re-copies the v6 key from the surviving v5 row and fails this test.
func TestRefreshAlbumWorksV6SuppressionRewriteNoResurrect(t *testing.T) {
	seedSuppressed := func(t *testing.T) (*Store, context.Context, int64) {
		t.Helper()
		s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
		oldAssoc, _ := metadata.InferAlbumWorkV5(v6CyberpunkAlbum, "", false)
		newAssoc, _ := metadata.InferAlbumWork(v6CyberpunkAlbum, "", false)
		oldKey, newKey := inferredWorkKey(oldAssoc), inferredWorkKey(newAssoc)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, album, oldKey); err != nil {
			t.Fatal(err)
		}
		stats, err := s.RefreshAlbumWorks(ctx, true)
		if err != nil || stats.AlbumsFailed != 0 {
			t.Fatalf("refresh %+v %v", stats, err)
		}
		var oldRows, newRows int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, album, oldKey).Scan(&oldRows); err != nil || oldRows != 0 {
			t.Fatalf("old suppression key survived the rewrite: %d %v", oldRows, err)
		}
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, album, newKey).Scan(&newRows); err != nil || newRows != 1 {
			t.Fatalf("v6 suppression missing after rewrite: %d %v", newRows, err)
		}
		return s, ctx, album
	}
	assertClean := func(t *testing.T, s *Store, ctx context.Context, album int64) {
		t.Helper()
		if _, err := s.RefreshAlbumWorks(ctx, true); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=?`, album).Scan(&n); err != nil || n != 0 {
			t.Fatalf("suppression resurrected: %d rows %v", n, err)
		}
		var links int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, album).Scan(&links); err != nil || links != 1 {
			t.Fatalf("links=%d, want 1 %v", links, err)
		}
	}
	t.Run("lift via AddWorkAlbum", func(t *testing.T) {
		s, ctx, album := seedSuppressed(t)
		var workID int64
		if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Cyberpunk: Edgerunners','cyberpunk: edgerunners','other','manual') RETURNING id`).Scan(&workID); err != nil {
			t.Fatal(err)
		}
		if err := s.AddWorkAlbum(ctx, workID, album, "ost"); err != nil {
			t.Fatal(err)
		}
		assertClean(t, s, ctx, album)
	})
	t.Run("lift via manual confirm", func(t *testing.T) {
		s, ctx, album := seedSuppressed(t)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO album_subject_candidates(album_id,source,external_id,title,tieups_json,status) VALUES(?,'bangumi','999','Cyberpunk: Edgerunners','[{"subjectId":309311,"title":"Cyberpunk: Edgerunners","type":"anime","role":"ost"}]','candidate')`, album); err != nil {
			t.Fatal(err)
		}
		var candID int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM album_subject_candidates WHERE album_id=?`, album).Scan(&candID); err != nil {
			t.Fatal(err)
		}
		outcome, _, err := s.ConfirmAlbumSubjectCandidate(ctx, album, candID, 0, "", false, nil)
		if err != nil || outcome != "succeeded" {
			t.Fatalf("confirm outcome=%q %v", outcome, err)
		}
		assertClean(t, s, ctx, album)
	})
}

// TestRefreshAlbumWorksV6ProtectedUnboundMergesIntoBound (M1, D71 exception):
// a protected but unbound work whose new identity already belongs to a bound
// work is NOT carried; the album merges into the bound work (R5) and the old
// work becomes protected-but-unreferenced for the user to handle.
func TestRefreshAlbumWorksV6ProtectedUnboundMergesIntoBound(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	var boundID int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Cyberpunk: Edgerunners','cyberpunk: edgerunners','anime','auto') RETURNING id`).Scan(&boundID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,raw_json,fetched_at) VALUES(?,'bangumi','309311','Cyberpunk: Edgerunners','{}','2024-01-01')`, boundID); err != nil {
		t.Fatal(err)
	}
	oldID, _, _ := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
	if _, err := s.db.ExecContext(ctx, `INSERT INTO work_series_locks(work_id) VALUES(?)`, oldID); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	workID, _, _ := v6AlbumLink(t, s, ctx, album)
	if workID != boundID {
		t.Fatalf("album linked to %d, want bound work %d", workID, boundID)
	}
	var title string
	if err = s.db.QueryRowContext(ctx, `SELECT title FROM works WHERE id=?`, oldID).Scan(&title); err != nil || title != "Cyberpunk: Edgerunners (Original Series" {
		t.Fatalf("protected unbound work was carried/renamed: %q %v", title, err)
	}
	var aliases int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE work_id=? AND normalized_key='cyberpunk: edgerunners'`, oldID).Scan(&aliases); err != nil || aliases != 0 {
		t.Fatalf("alias carried for merged work: %d %v", aliases, err)
	}
	if stats.ProtectedUnreferenced != 1 {
		t.Fatalf("ProtectedUnreferenced=%d, want 1", stats.ProtectedUnreferenced)
	}
}

// TestRefreshAlbumWorksV6CarryOverRollback (M4): a failing UPDATE works in the
// middle of the carryover rolls the whole album step back — no alias, no
// re-keyed link, no rewritten suppression survives, and the album is counted
// as failed. After the failure is gone the next refresh carries over cleanly.
func TestRefreshAlbumWorksV6CarryOverRollback(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	oldID, oldKey, newKey := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
	if _, err := s.db.ExecContext(ctx, `UPDATE works SET external_id='446296' WHERE id=?`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, album, oldKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_works_update BEFORE UPDATE ON works BEGIN SELECT RAISE(FAIL,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AlbumsFailed != 1 || stats.AlbumsRefreshed != 0 {
		t.Fatalf("stats %+v, want AlbumsFailed=1 AlbumsRefreshed=0", stats)
	}
	var aliases int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE work_id=? AND normalized_key='cyberpunk: edgerunners'`, oldID).Scan(&aliases); err != nil || aliases != 0 {
		t.Fatalf("alias partially written: %d %v", aliases, err)
	}
	var linkKey string
	if err = s.db.QueryRowContext(ctx, `SELECT inferred_key FROM album_works WHERE album_id=? AND work_id=?`, album, oldID).Scan(&linkKey); err != nil || linkKey != oldKey {
		t.Fatalf("link re-keyed despite rollback: %q %v", linkKey, err)
	}
	var supNew int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, album, newKey).Scan(&supNew); err != nil || supNew != 0 {
		t.Fatalf("suppression partially rewritten: %d %v", supNew, err)
	}
	var supOld int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, album, oldKey).Scan(&supOld); err != nil || supOld != 1 {
		t.Fatalf("old suppression lost despite rollback: %d %v", supOld, err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER fail_works_update`); err != nil {
		t.Fatal(err)
	}
	// Recovery: the carryover completes, the suppression is rewritten, and the
	// auto link is then removed because the identity is suppressed (a real
	// suppression would never coexist with the link it blocks).
	stats, err = s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("second refresh %+v %v", stats, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE work_id=? AND normalized_key='cyberpunk: edgerunners'`, oldID).Scan(&aliases); err != nil || aliases != 1 {
		t.Fatalf("alias missing after recovery: %d %v", aliases, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, album, newKey).Scan(&supNew); err != nil || supNew != 1 {
		t.Fatalf("suppression not rewritten after recovery: %d %v", supNew, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=?`, album).Scan(&supOld); err != nil || supOld != 1 {
		t.Fatalf("suppression rows=%d, want exactly 1 %v", supOld, err)
	}
	var links int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, album).Scan(&links); err != nil || links != 0 {
		t.Fatalf("suppressed link survived recovery: %d %v", links, err)
	}
}

// TestRefreshAlbumWorksV6CarryOverTrackManualRow (M4): the track-level
// carryover re-keys a manual work_tracks row and carries its (protected) work.
func TestRefreshAlbumWorksV6CarryOverTrackManualRow(t *testing.T) {
	const tagValue = "Lazarus (Adult Swim original series soundtrack)"
	s, ctx, _ := albumWorkFixture(t, "Plain Album", "Track")
	var trackID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM tracks LIMIT 1`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) SELECT af.id,'CONTENTGROUP',?,0 FROM audio_files af WHERE af.track_id=?`, tagValue, trackID); err != nil {
		t.Fatal(err)
	}
	oldAssoc, ok := metadata.InferAlbumWorkV5(tagValue, "", false)
	if !ok {
		t.Fatal("v5 no longer infers the tag value")
	}
	newAssoc, _ := metadata.InferAlbumWork(tagValue, "", false)
	var workID int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES(?,?,'other','auto') RETURNING id`, oldAssoc.Title, metadata.Normalize(oldAssoc.Title)).Scan(&workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'ost','manual',?)`, workID, trackID, inferredWorkKey(oldAssoc)); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	var gotWork int64
	var key, source string
	if err = s.db.QueryRowContext(ctx, `SELECT work_id,inferred_key,source FROM work_tracks WHERE track_id=?`, trackID).Scan(&gotWork, &key, &source); err != nil {
		t.Fatal(err)
	}
	if gotWork != workID || source != "manual" || key != inferredWorkKey(newAssoc) {
		t.Fatalf("track link = (%d,%q,%s), want carried manual row with v6 key", gotWork, key, source)
	}
	var title string
	if err = s.db.QueryRowContext(ctx, `SELECT title FROM works WHERE id=?`, workID).Scan(&title); err != nil || title != "Lazarus" {
		t.Fatalf("carried work not renamed: %q %v", title, err)
	}
	var aliases int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE work_id=? AND normalized_key='lazarus'`, workID).Scan(&aliases); err != nil || aliases != 1 {
		t.Fatalf("alias missing: %d %v", aliases, err)
	}
	var rows int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, trackID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("track rows=%d, want 1 (no auto row next to the manual one)", rows)
	}
}

// TestRefreshAlbumWorksV6ManualRowNoDuplicateAuto (L4): a manual link row is
// visible to resolution, so a carryover that could not rename (identity
// collision) does not produce an extra auto row next to the manual one.
func TestRefreshAlbumWorksV6ManualRowNoDuplicateAuto(t *testing.T) {
	const albumTitle = "TVアニメ「艦隊これくしょん -艦これ-」キャラクターソング “艦娘乃歌”"
	s, ctx, album := albumWorkFixture(t, albumTitle, "Track")
	oldAssoc, ok := metadata.InferAlbumWorkV5(albumTitle, "", false)
	if !ok {
		t.Fatal("v5 no longer infers the album")
	}
	newAssoc, ok := metadata.InferAlbumWork(albumTitle, "", false)
	if !ok || inferredWorkKey(oldAssoc) == inferredWorkKey(newAssoc) {
		t.Fatal("test requires differing v5/v6 keys")
	}
	// The colliding work owns the new name for the carried work's type, so the
	// rename is skipped; the carried work's type deliberately mismatches the
	// inference so the alias/type lookups cannot find it either.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES(?,?,'movie','auto')`, newAssoc.Title, metadata.Normalize(newAssoc.Title)); err != nil {
		t.Fatal(err)
	}
	var workID int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES(?,?,'movie','auto') RETURNING id`, oldAssoc.Title, metadata.Normalize(oldAssoc.Title)).Scan(&workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'character','manual',?)`, album, workID, inferredWorkKey(oldAssoc)); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	gotWork, key, source := v6AlbumLink(t, s, ctx, album)
	if gotWork != workID || source != "manual" || key != inferredWorkKey(newAssoc) {
		t.Fatalf("link = (%d,%q,%s), want carried manual row", gotWork, key, source)
	}
	var autoRows int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='auto'`, album).Scan(&autoRows); err != nil || autoRows != 0 {
		t.Fatalf("duplicate auto row created: %d %v", autoRows, err)
	}
	if n := v6WorkCount(t, s, ctx); n != 1 {
		t.Fatalf("works=%d, want 1 (no extra work created; the dangling collision work is cleaned)", n)
	}
}

// TestImportTrackV6SuppressionCarryOver (L3): re-importing a track performs
// the same suppression rewrite as RefreshAlbumWorks, so a v5 suppression keeps
// working under v6 and is not resurrected later.
func TestImportTrackV6SuppressionCarryOver(t *testing.T) {
	const tagValue = "Lazarus (Adult Swim original series soundtrack)"
	s, ctx, _ := albumWorkFixture(t, "Plain Album", "Track")
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := func(mtime int64) ImportInput {
		return ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: mtime, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Plain Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"CONTENTGROUP": {tagValue}}}}
	}
	if err = s.ImportTrack(ctx, input(2)); err != nil {
		t.Fatal(err)
	}
	var trackID int64
	if err = s.db.QueryRowContext(ctx, `SELECT id FROM tracks LIMIT 1`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	var links int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, trackID).Scan(&links); err != nil || links != 1 {
		t.Fatalf("first import links=%d, want 1 %v", links, err)
	}
	oldAssoc, _ := metadata.InferAlbumWorkV5(tagValue, "", false)
	newAssoc, _ := metadata.InferAlbumWork(tagValue, "", false)
	oldKey, newKey := inferredWorkKey(oldAssoc), inferredWorkKey(newAssoc)
	if _, err = s.db.ExecContext(ctx, `DELETE FROM work_tracks WHERE track_id=?`, trackID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,?)`, trackID, oldKey); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportTrack(ctx, input(3)); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, trackID).Scan(&links); err != nil || links != 0 {
		t.Fatalf("suppressed link recreated on re-import: %d %v", links, err)
	}
	var oldRows, newRows int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key=?`, trackID, oldKey).Scan(&oldRows); err != nil || oldRows != 0 {
		t.Fatalf("old suppression key survived the rewrite: %d %v", oldRows, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key=?`, trackID, newKey).Scan(&newRows); err != nil || newRows != 1 {
		t.Fatalf("v6 suppression missing after re-import: %d %v", newRows, err)
	}
}

// TestCarryOverInferredKeyKeepsNewKeySuppression (Low-1): workTitleKeysMatch
// compares title+season only, so when a rule change alters just the type
// segment a stored row can already BE the new key; the rewrite must not
// delete it. Removing the stored==newKey guard fails this test.
func TestCarryOverInferredKeyKeepsNewKeySuppression(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Plain Album", "Track")
	oldAssoc := metadata.WorkAssociation{Title: "X", Type: "other", Role: "ost"}
	newAssoc := metadata.WorkAssociation{Title: "X", Type: "anime", Role: "ost"}
	oldKey, newKey := inferredWorkKey(oldAssoc), inferredWorkKey(newAssoc)
	if oldKey == newKey || !workTitleKeysMatch(newKey, oldKey) {
		t.Fatalf("test setup: keys %q / %q", oldKey, newKey)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, album, newKey); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = carryOverInferredKey(ctx, tx, oldAssoc, newAssoc, oldKey, newKey,
		`SELECT inferred_key FROM album_work_suppressions WHERE album_id=?`,
		`INSERT OR IGNORE INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`,
		`DELETE FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`,
		`SELECT aw.work_id FROM album_works aw WHERE aw.album_id=? AND aw.inferred_key=?`,
		`UPDATE album_works SET inferred_key=? WHERE album_id=? AND inferred_key=? AND work_id=?`,
		album)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, album, newKey).Scan(&n); err != nil || n != 1 {
		t.Fatalf("new-key suppression deleted by the rewrite: %d %v", n, err)
	}
}
