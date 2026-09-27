package storage

import (
	"context"
	"database/sql"
	"github.com/lux032/032music-server/internal/metadata"
	"path/filepath"
	"testing"
)

func albumWorkFixture(t *testing.T, album, title string) (*Store, context.Context, int64) {
	t.Helper()
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "work.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureLibrary(ctx, "Music", "/music"); e != nil {
		t.Fatal(e)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e = s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: album, Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); e != nil {
		t.Fatal(e)
	}
	var id int64
	if e = s.db.QueryRowContext(ctx, `SELECT id FROM albums LIMIT 1`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return s, ctx, id
}
func TestAlbumWorkAliasAfterTypeAndTitleEdit(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	id := works[0].ID
	if _, e := s.db.ExecContext(ctx, `UPDATE works SET type='game' WHERE id=?`, id); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	if _, e := s.UpdateWork(ctx, id, WorkInput{Title: "Hollow Knight (edited)", Type: "game"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ = s.ListWorks(ctx, WorkFilters{})
	links, _ := s.AlbumsForWork(ctx, id)
	if len(works) != 1 || len(links) != 1 || links[0].AlbumID != album {
		t.Fatalf("alias works=%+v links=%+v", works, links)
	}
}
func TestAlbumWorkRemoveManualAddAndUserTitle(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	id := works[0].ID
	if e := s.RemoveWorkAlbum(ctx, id, album); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	links, _ := s.AlbumsForWork(ctx, id)
	if len(links) != 0 {
		t.Fatalf("removed link returned %+v", links)
	}
	manual, e := s.CreateWork(ctx, WorkInput{Title: "Manual link", Type: "other"})
	if e != nil {
		t.Fatal(e)
	}
	if e := s.AddWorkAlbum(ctx, manual.ID, album, "theme"); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM album_work_suppressions WHERE album_id=?`, album).Scan(&count); e != nil || count != 1 {
		t.Fatalf("suppression retained %d %v", count, e)
	}
	if _, e = s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var autoCount int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='auto'`, album).Scan(&autoCount); e != nil || autoCount != 0 {
		t.Fatalf("automatic work recreated: %d %v", autoCount, e)
	}
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM album_work_suppressions WHERE album_id=?`, album).Scan(&count); e != nil || count != 1 {
		t.Fatalf("suppression after refresh %d %v", count, e)
	}
	if e := s.UpdateAlbum(ctx, album, AlbumEdit{Title: "NieR:Automata Original Soundtrack"}); e != nil {
		t.Fatal(e)
	}
	var fingerprint sql.NullString
	if e := s.db.QueryRowContext(ctx, `SELECT work_fingerprint FROM albums WHERE id=?`, album).Scan(&fingerprint); e != nil || fingerprint.Valid {
		t.Fatalf("fingerprint %+v %v", fingerprint, e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, false); e != nil {
		t.Fatal(e)
	}
	works, _ = s.ListWorks(ctx, WorkFilters{})
	var nieRCount int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works aw JOIN works w ON w.id=aw.work_id WHERE aw.album_id=? AND w.title='NieR:Automata'`, album).Scan(&nieRCount); e != nil || nieRCount != 1 {
		t.Fatalf("user title recompute: links=%d works=%+v err=%v", nieRCount, works, e)
	}
}
func TestAutoWorkCleanupProtectsManual(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Unknown Album", "Track")
	manual, e := s.CreateWork(ctx, WorkInput{Title: "Manual", Type: "other"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Unused Auto','unused auto','other','auto')`)
	if e != nil {
		t.Fatal(e)
	}
	var autoID int64
	if e = s.db.QueryRowContext(ctx, `SELECT id FROM works WHERE origin='auto'`).Scan(&autoID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.ExecContext(ctx, `INSERT INTO enrichment_provenance(entity_type,entity_id,field_name,source,value_json) VALUES('work',?,'title','bangumi','"test"')`, autoID); e != nil {
		t.Fatal(e)
	}
	var stats RefreshStats
	if e = s.CleanupAutoWorks(ctx, &stats); e != nil {
		t.Fatal(e)
	}
	if stats.WorksDeleted != 1 || stats.ProtectedUnreferenced != 1 {
		t.Fatalf("cleanup stats %+v", stats)
	}
	if _, e = s.WorkByID(ctx, manual.ID); e != nil {
		t.Fatal(e)
	}
	var provenance int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrichment_provenance WHERE entity_type='work' AND entity_id=?`, autoID).Scan(&provenance); e != nil || provenance != 0 {
		t.Fatalf("orphan provenance=%d err=%v", provenance, e)
	}
}
