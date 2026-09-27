package storage

import (
	"database/sql"
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
