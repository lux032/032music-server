package storage

import (
	"context"
	"github.com/lux032/032music-server/internal/metadata"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompilationSoundtrackRefreshAndRuleVersion(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "compilation.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureLibrary(ctx, "Test", "/music"); e != nil {
		t.Fatal(e)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	cases := []struct{ album, title string }{{"Sekiro: Shadows Die Twice: Original Soundtrack", "Sekiro: Shadows Die Twice"}, {"プリキュア オープニングテーマコレクション2004~2016", ""}, {"PERSONA DANCING P3D & P5D SOUND TRACKS -ADVANCED CD COLLECTOR'S BOX-", ""}}
	for i, tc := range cases {
		input := ImportInput{LibraryID: lib.ID, RelativePath: tc.album + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: tc.album, Artists: []string{"Various Artists"}, AlbumArtists: []string{"Various Artists"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"TCMP": {"1"}}}}
		if e = s.ImportTrack(ctx, input); e != nil {
			t.Fatal(e)
		}
		_ = i
	}
	if _, e = s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	for _, tc := range cases {
		var count int
		e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM album_works aw JOIN albums a ON a.id=aw.album_id WHERE a.title=?`, tc.album).Scan(&count)
		if e != nil || count != map[bool]int{true: 1, false: 0}[tc.title != ""] {
			t.Fatalf("%s links=%d %v", tc.album, count, e)
		}
	}
	var title string
	if e = s.db.QueryRowContext(ctx, `SELECT w.title FROM album_works aw JOIN works w ON w.id=aw.work_id JOIN albums a ON a.id=aw.album_id WHERE a.title=?`, cases[0].album).Scan(&title); e != nil || title != cases[0].title {
		t.Fatalf("VA soundtrack %q err %v", title, e)
	}
	if _, e = s.db.ExecContext(ctx, `UPDATE albums SET work_fingerprint='obsolete-v1'`); e != nil {
		t.Fatal(e)
	}
	stats, e := s.RefreshAlbumWorks(ctx, false)
	if e != nil || stats.AlbumsRefreshed != 3 {
		t.Fatalf("rule bump %+v %v", stats, e)
	}
	if !strings.Contains(albumWorkRuleVersion, "v6") {
		t.Fatal("rule version was not bumped")
	}
}
func TestEditedAutoWorkDeleteSuppresses(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, _ := s.ListWorks(ctx, WorkFilters{})
	if _, e := s.UpdateWork(ctx, works[0].ID, WorkInput{Title: "Edited", Type: "other"}); e != nil {
		t.Fatal(e)
	}
	if e := s.DeleteWork(ctx, works[0].ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM album_work_suppressions WHERE album_id=?`, album).Scan(&count); e != nil || count != 1 {
		t.Fatalf("suppression=%d %v", count, e)
	}
	works, _ = s.ListWorks(ctx, WorkFilters{})
	if len(works) != 0 {
		t.Fatalf("edited work rebuilt %+v", works)
	}
}
func TestProtectedBangumiAutoWithoutReferences(t *testing.T) {
	s, ctx, _ := albumWorkFixture(t, "Unknown Album", "Track")
	for _, title := range []string{"Profile", "Candidate"} {
		r, e := s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES(?,?,'anime','auto')`, title, title)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		if title == "Profile" {
			_, e = s.db.ExecContext(ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,raw_json,fetched_at) VALUES(?,'bangumi','123','Profile','{}','2024-01-01')`, id)
		} else {
			_, e = s.db.ExecContext(ctx, `INSERT INTO work_match_candidates(work_id,source,external_id,title,score,status,payload_json) VALUES(?,'bangumi','456','Candidate',95,'confirmed','{}')`, id)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	var stats RefreshStats
	if e := s.CleanupAutoWorks(ctx, &stats); e != nil {
		t.Fatal(e)
	}
	listed, total, e := s.UnreferencedProtectedWorks(ctx)
	if e != nil || total != 2 || len(listed) != 2 || stats.ProtectedUnreferenced != 2 {
		t.Fatalf("protected %+v total=%d stats=%+v err=%v", listed, total, stats, e)
	}
}
