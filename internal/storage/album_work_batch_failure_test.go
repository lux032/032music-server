package storage

import "testing"

func TestRefreshAlbumWorksContinuesAfterAlbumLinkFailure(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "X Original Soundtrack", "Track", 1, 1)
	f.importFile(t, "B/01.flac", "Y Original Soundtrack", "Track", 1, 1)
	failed := f.albumID(t, "X Original Soundtrack")
	ok := f.albumID(t, "Y Original Soundtrack")
	if _, err := f.store.db.ExecContext(f.ctx, `CREATE TRIGGER fail_one_album_work BEFORE INSERT ON album_works WHEN NEW.source='auto' AND NEW.album_id=`+intString(failed)+` BEGIN SELECT RAISE(FAIL,'injected album failure'); END`); err != nil {
		t.Fatal(err)
	}
	stats, err := f.store.RefreshAlbumWorks(f.ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AlbumsFailed != 1 || stats.AlbumsRefreshed != 1 || stats.WorksCreated != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, failed); n != 0 {
		t.Fatalf("failed album rows=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, ok); n != 1 {
		t.Fatalf("successful album rows=%d", n)
	}
}

func intString(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
