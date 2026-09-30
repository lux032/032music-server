package storage

import (
	"reflect"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestArtistDiscographyRelationsAndDateOrder(t *testing.T) {
	f := newAlbumMergeFixture(t)
	add := func(title, date string, year int, albumArtists, artists []string, raw map[string][]string) {
		t.Helper()
		if raw == nil {
			raw = map[string][]string{}
		}
		if date != "" {
			raw["DATE"] = []string{date}
		}
		if err := f.store.ImportTrack(f.ctx, ImportInput{LibraryID: f.library, RelativePath: title + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: title, Year: year, Artists: artists, AlbumArtists: albumArtists, DiscNumber: 1, TrackNumber: 1, Raw: raw}}); err != nil {
			t.Fatal(err)
		}
	}
	add("Older personal", "2021-01-01", 2021, []string{"Singer"}, []string{"Singer"}, nil)
	add("Joint", "2024/05/01", 2024, []string{"Singer", "Other"}, []string{"Singer"}, nil)
	add("Guest", "2024.12.01", 2024, []string{"Other"}, []string{"Singer"}, nil)
	add("Various", "2023-01-01", 2023, []string{"Various Artists"}, []string{"Singer"}, nil)
	add("Multi compilation", "2023-02-01", 2023, []string{"Singer", "Other"}, []string{"Singer"}, map[string][]string{"COMPILATION": {"1"}})
	add("Solo best", "2022-01-01", 2022, []string{"Singer"}, []string{"Singer"}, map[string][]string{"RELEASETYPE": {"compilation"}})
	add("Year only", "", 2024, []string{"Singer"}, []string{"Singer"}, nil)
	add("Undated", "", 0, []string{"Singer"}, []string{"Singer"}, nil)
	add("Edited", "2000-01-01", 2000, []string{"Singer"}, []string{"Singer"}, nil)
	add("Composer only", "2026-01-01", 2026, []string{"Other"}, []string{"Other"}, nil)
	add("Unrelated", "2027-01-01", 2027, []string{"Other"}, []string{"Other"}, nil)
	var singer int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT id FROM artists WHERE display_name='Singer'`).Scan(&singer); err != nil {
		t.Fatal(err)
	}
	composer := f.albumID(t, "Composer only")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) SELECT id,?,0,'composer' FROM tracks WHERE album_id=?`, singer, composer); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateAlbum(f.ctx, f.albumID(t, "Edited"), AlbumEdit{Title: "Edited", ReleaseDate: "2025-01-01"}); err != nil {
		t.Fatal(err)
	}
	releases, err := f.store.ArtistDiscography(f.ctx, singer)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range releases {
		got = append(got, r.Title+":"+r.Relation)
	}
	want := []string{"Edited:personal", "Guest:appearance", "Joint:collaboration", "Year only:personal", "Multi compilation:appearance", "Various:appearance", "Solo best:personal", "Older personal:personal", "Undated:personal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("releases = %v, want %v", got, want)
	}
	// The existing API keeps its album-credit-only contract, but shares date ordering.
	albums, err := f.store.ArtistAlbums(f.ctx, singer)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, a := range albums {
		got = append(got, a.Title)
	}
	if want := []string{"Edited", "Joint", "Year only", "Multi compilation", "Solo best", "Older personal", "Undated"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("API albums = %v, want %v", got, want)
	}
	empty, err := f.store.ArtistDiscography(f.ctx, 999999)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %v, err = %v", empty, err)
	}
}
