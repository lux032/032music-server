package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func releaseKindStore(t *testing.T) (*Store, context.Context, int64) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "kind.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, lib.ID
}

type kindTrack struct {
	title, trackType string
	minutes          int64
	raw              map[string][]string
}

func importKindAlbum(t *testing.T, s *Store, ctx context.Context, libraryID int64, album string, tracks []kindTrack) int64 {
	t.Helper()
	for i, tr := range tracks {
		m := metadata.AudioMetadata{Title: tr.title, Album: album, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, DurationMillis: tr.minutes * 60 * 1000, TrackType: tr.trackType, Raw: tr.raw}
		if m.Raw == nil {
			m.Raw = map[string][]string{}
		}
		if err := s.ImportTrack(ctx, ImportInput{LibraryID: libraryID, RelativePath: fmt.Sprintf("%s/%02d.flac", album, i+1), FileSize: 1, ModifiedAtNS: 1, Metadata: m}); err != nil {
			t.Fatal(err)
		}
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title=?`, album).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func releaseKindOf(t *testing.T, s *Store, ctx context.Context, albumID int64) (list, detail, sync string) {
	t.Helper()
	albums, err := s.ListAlbums(ctx, Filters{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range albums {
		if a.ID == albumID {
			list = a.ReleaseKind
		}
	}
	a, err := s.AlbumByID(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.SyncAlbums(ctx, SyncAlbumsParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range res.Items {
		if item.ID == albumID {
			sync = item.ReleaseKind
		}
	}
	return list, a.ReleaseKind, sync
}

func assertReleaseKind(t *testing.T, s *Store, ctx context.Context, albumID int64, want string) {
	t.Helper()
	list, detail, sync := releaseKindOf(t, s, ctx, albumID)
	if list != want || detail != want || sync != want {
		t.Fatalf("release kind list=%q detail=%q sync=%q, want %q", list, detail, sync, want)
	}
}

func TestReleaseKindInferredForUntaggedAlbums(t *testing.T) {
	s, ctx, lib := releaseKindStore(t)
	// 日系单曲：两首歌 + 两首伴奏，没有类型标签。
	single := importKindAlbum(t, s, ctx, lib, "Single CD", []kindTrack{{"A", "regular", 4, nil}, {"B", "regular", 4, nil}, {"A (Off Vocal)", "off_vocal", 4, nil}, {"B (Off Vocal)", "off_vocal", 4, nil}})
	assertReleaseKind(t, s, ctx, single, "single")
	// AlbumType 原语义不变：没有标签仍是默认 album。
	if a, _ := s.AlbumByID(ctx, single); a.AlbumType != "album" {
		t.Fatalf("AlbumType = %q, want album", a.AlbumType)
	}

	var tracks []kindTrack
	for i := 0; i < 10; i++ {
		tracks = append(tracks, kindTrack{fmt.Sprintf("T%d", i), "regular", 4, nil})
	}
	full := importKindAlbum(t, s, ctx, lib, "Full Album", tracks)
	assertReleaseKind(t, s, ctx, full, "album")
}

func TestReleaseKindTagAndUserOverride(t *testing.T) {
	s, ctx, lib := releaseKindStore(t)
	tagged := map[string][]string{"MUSICBRAINZ_ALBUMTYPE": {"album"}}
	// 明确标注为 album 的两首曲目专辑不能被推断成单曲。
	id := importKindAlbum(t, s, ctx, lib, "Tagged", []kindTrack{{"A", "regular", 4, tagged}, {"B", "regular", 4, tagged}})
	assertReleaseKind(t, s, ctx, id, "album")

	// 手动纠正优先。
	if err := s.UpdateAlbum(ctx, id, AlbumEdit{Title: "Tagged", AlbumType: "ep"}); err != nil {
		t.Fatal(err)
	}
	assertReleaseKind(t, s, ctx, id, "ep")
	if a, _ := s.AlbumByID(ctx, id); a.UserAlbumType != "ep" {
		t.Fatalf("UserAlbumType = %q, want ep", a.UserAlbumType)
	}
	// 选“自动”（空值）恢复为标签判定。
	if err := s.UpdateAlbum(ctx, id, AlbumEdit{Title: "Tagged", AlbumType: ""}); err != nil {
		t.Fatal(err)
	}
	assertReleaseKind(t, s, ctx, id, "album")
}

func TestReleaseKindEnrichmentSurvivesUntaggedRescan(t *testing.T) {
	s, ctx, lib := releaseKindStore(t)
	var tracks []kindTrack
	for i := 0; i < 8; i++ {
		tracks = append(tracks, kindTrack{fmt.Sprintf("T%d", i), "regular", 5, nil})
	}
	id := importKindAlbum(t, s, ctx, lib, "Enriched", tracks)
	assertReleaseKind(t, s, ctx, id, "album")
	if err := s.ConservativelyFillAlbum(ctx, id, AlbumFieldPatch{AlbumType: "soundtrack"}, "musicbrainz", "x", 0); err != nil {
		t.Fatal(err)
	}
	assertReleaseKind(t, s, ctx, id, "soundtrack")
	importKindAlbum(t, s, ctx, lib, "Enriched", tracks) // 无类型标签的重扫
	assertReleaseKind(t, s, ctx, id, "soundtrack")
}
