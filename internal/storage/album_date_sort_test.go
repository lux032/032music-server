package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestAlbumsSortByReleaseDate(t *testing.T) {
	f := newAlbumMergeFixture(t)
	add := func(album, date string, year int) {
		t.Helper()
		raw := map[string][]string{}
		if date != "" {
			raw["DATE"] = []string{date}
		}
		input := ImportInput{LibraryID: f.library, RelativePath: album + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: album, Album: album, Year: year, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Raw: raw}}
		if err := f.store.ImportTrack(f.ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	add("May", "2004-05-12", 2004)
	add("Dec", "2004.12.01", 2004) // dotted separator must still sort as a date
	add("YearOnly", "", 2004)
	add("Older", "2001/03/03", 2001)
	add("Undated", "", 0)
	add("Edited", "1999-01-01", 1999)
	edited := f.albumID(t, "Edited")
	if err := f.store.UpdateAlbum(f.ctx, edited, AlbumEdit{Title: "Edited", ReleaseDate: "2004-08-01"}); err != nil {
		t.Fatal(err)
	}

	albums, err := f.store.ListAlbums(f.ctx, Filters{Sort: "date", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range albums {
		got = append(got, a.Title)
	}
	want := []string{"Dec", "Edited", "May", "YearOnly", "Older", "Undated"}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	tracks, err := f.store.ListTracks(f.ctx, Filters{Sort: "date", Limit: 50})
	if err != nil || len(tracks) != 6 || tracks[0].Album != "Dec" {
		t.Fatalf("track date order first = %+v, err = %v", tracks, err)
	}
}

func TestListAlbumsHydratesArtistLinks(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "Duet/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Duet", Album: "Duet", Artists: []string{"A"}, AlbumArtists: []string{"A", "B"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albums, err := f.store.ListAlbums(f.ctx, Filters{Limit: 10})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums = %+v, err = %v", albums, err)
	}
	links := albums[0].AlbumArtists
	if len(links) != 2 || links[0].Name != "A" || links[1].Name != "B" || links[0].ID == 0 {
		t.Fatalf("artist links = %+v", links)
	}
}

// TestSyncAlbumsExposesEffectiveReleaseDate: the App sync feed carries the same
// effective date the web "date" sort uses, so clients can sort by full date.
func TestSyncAlbumsExposesEffectiveReleaseDate(t *testing.T) {
	f := newAlbumMergeFixture(t)
	add := func(album, date string, year int) {
		t.Helper()
		raw := map[string][]string{}
		if date != "" {
			raw["DATE"] = []string{date}
		}
		input := ImportInput{LibraryID: f.library, RelativePath: album + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: album, Album: album, Year: year, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Raw: raw}}
		if err := f.store.ImportTrack(f.ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	add("Dotted", "2004.12.01", 2004)
	add("YearOnly", "", 2001)
	add("Undated", "", 0)
	add("Edited", "1999-01-01", 1999)
	if err := f.store.UpdateAlbum(f.ctx, f.albumID(t, "Edited"), AlbumEdit{Title: "Edited", ReleaseDate: "2004/08/01"}); err != nil {
		t.Fatal(err)
	}
	result, err := f.store.SyncAlbums(f.ctx, SyncAlbumsParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range result.Items {
		got[item.Title] = item.ReleaseDate
	}
	want := map[string]string{"Dotted": "2004-12-01", "YearOnly": "2001", "Undated": "", "Edited": "2004-08-01"}
	for title, date := range want {
		if got[title] != date {
			t.Fatalf("releaseDate[%s] = %q, want %q (all = %v)", title, got[title], date, got)
		}
	}
}
