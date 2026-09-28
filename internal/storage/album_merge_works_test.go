package storage

import (
	"database/sql"
	"strings"
	"testing"
)

func TestMergeAlbumsPreservesManualWorkAndSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "A Original Soundtrack", "One", 1, 1)
	f.importFile(t, "B/01.flac", "B Original Soundtrack", "Two", 1, 1)
	target, source := f.albumID(t, "A Original Soundtrack"), f.albumID(t, "B Original Soundtrack")
	w, e := f.store.CreateWork(f.ctx, WorkInput{Title: "Manual", Type: "other"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.AddWorkAlbum(f.ctx, w.ID, source, "theme"); e != nil {
		t.Fatal(e)
	}
	_, e = f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'b|other|0')`, source)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.MergeAlbums(f.ctx, target, []int64{source}); e != nil {
		t.Fatal(e)
	}
	links, e := f.store.AlbumsForWork(f.ctx, w.ID)
	if e != nil || len(links) != 1 || links[0].AlbumID != target || links[0].Source != "manual" {
		t.Fatalf("links=%+v err=%v", links, e)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=?`, target); n != 1 {
		t.Fatalf("suppressions=%d", n)
	}
	var fingerprint sql.NullString
	if e = f.store.db.QueryRowContext(f.ctx, `SELECT work_fingerprint FROM albums WHERE id=?`, target).Scan(&fingerprint); e != nil || fingerprint.Valid {
		t.Fatalf("fingerprint=%+v err=%v", fingerprint, e)
	}
}

func TestB3MergeAlbumsPreservesBangumiLinkCandidateAndSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Target", "One", 1, 1)
	f.importFile(t, "B/01.flac", "Source", "Two", 1, 1)
	target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
	work, e := f.store.CreateWork(f.ctx, WorkInput{Title: "Bangumi Work", Type: "anime"})
	if e != nil {
		t.Fatal(e)
	}
	for _, statement := range []string{
		`INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','bangumi:10:20')`,
		`INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'bangumi:10:21')`,
		`INSERT INTO album_subject_candidates(album_id,source,external_id,title,status) VALUES(?,'bangumi','10','Music','rejected')`,
	} {
		var err error
		if strings.Contains(statement, "INSERT INTO album_works(") {
			_, err = f.store.db.ExecContext(f.ctx, statement, source, work.ID)
		} else {
			_, err = f.store.db.ExecContext(f.ctx, statement, source)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, e = f.store.MergeAlbums(f.ctx, target, []int64{source}); e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{`SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='bangumi'`, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='bangumi:10:21'`, `SELECT COUNT(*) FROM album_subject_candidates WHERE album_id=? AND status='rejected'`} {
		if n := f.count(t, query, target); n != 1 {
			t.Fatalf("%s: %d", query, n)
		}
	}
}
