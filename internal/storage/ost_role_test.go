package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestAlbumRolesDoNotCreateTrackOverrides(t *testing.T) {
	for _, tc := range []struct{ album, track string }{{"ENDER MAGNOLIA Original Soundtrack", "Finale (ED)"}, {"マリア様がみてる オープニングテーマ", "c/w"}, {"TVアニメ「X」キャラクターソング", "Opening"}} {
		t.Run(tc.album, func(t *testing.T) {
			s, ctx, album := albumWorkFixture(t, tc.album, tc.track)
			lib, _ := s.LibraryByRoot(ctx, "/music")
			if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "Album/02.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "another song", Album: tc.album, Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 2}}); e != nil {
				t.Fatal(e)
			}
			if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
				t.Fatal(e)
			}
			var count int
			if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE t.album_id=? AND wt.source='auto'`, album).Scan(&count); e != nil || count != 0 {
				t.Fatalf("unexpected auto track links %d %v", count, e)
			}
		})
	}
}
func TestRefreshRemovesLegacyRoleOverrides(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "ENDER MAGNOLIA Original Soundtrack", "Opening")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var work, track int64
	if e := s.db.QueryRowContext(ctx, `SELECT work_id FROM album_works WHERE album_id=?`, album).Scan(&work); e != nil {
		t.Fatal(e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE album_id=?`, album).Scan(&track); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,source) VALUES(?,?,'other','auto')`, work, track); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(ctx, `UPDATE albums SET work_fingerprint='album-work-v3' WHERE id=?`, album); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, false); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='auto'`, track).Scan(&count); e != nil || count != 0 {
		t.Fatalf("old role overrides %d %v", count, e)
	}
}
