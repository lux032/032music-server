package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestArtistCreditDisplayAcrossBrowseAndSync(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: library.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "DEMON'S SHOT", Album: "Single", DiscNumber: 1, TrackNumber: 1,
			Raw: map[string][]string{
				"ARTIST":       {"Aiobahn +81 feat. DEMONDICE & C"},
				"ARTISTS":      {"Aiobahn +81", "DEMONDICE", "C"},
				"ALBUMARTIST":  {"Aiobahn +81 feat. DEMONDICE"},
				"ALBUMARTISTS": {"Aiobahn +81", "DEMONDICE"},
			},
		},
	}
	if err := s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	check := func(trackCredit, albumCredit, firstJoin string) Track {
		t.Helper()
		tracks, err := s.ListTracks(ctx, Filters{})
		if err != nil || len(tracks) != 1 || tracks[0].Artist != trackCredit || len(tracks[0].Artists) != 3 {
			t.Fatalf("track display: %+v, %v", tracks, err)
		}
		track := tracks[0]
		trackSync, err := s.SyncTracks(ctx, SyncTracksParams{})
		if err != nil || len(trackSync.Items) != 1 || trackSync.Items[0].Artist != trackCredit ||
			artistRefDisplay(trackSync.Items[0].Artists) != trackCredit {
			t.Fatalf("track sync: %+v, %v", trackSync, err)
		}
		if firstJoin != "" && (track.Artists[1].JoinPhrase == nil || *track.Artists[1].JoinPhrase != firstJoin) {
			t.Fatalf("track link separator: %+v", track.Artists)
		}
		albums, err := s.ListAlbums(ctx, Filters{})
		if err != nil || len(albums) != 1 || albums[0].Artist != albumCredit {
			t.Fatalf("album display: %+v, %v", albums, err)
		}
		albumSync, err := s.SyncAlbums(ctx, SyncAlbumsParams{})
		if err != nil || len(albumSync.Items) != 1 || albumSync.Items[0].Artist != albumCredit ||
			artistRefDisplay(albumSync.Items[0].Artists) != albumCredit {
			t.Fatalf("album sync: %+v, %v", albumSync, err)
		}
		return track
	}
	track := check("Aiobahn +81 feat. DEMONDICE & C", "Aiobahn +81 feat. DEMONDICE", " feat. ")
	if err := s.UpdateArtist(ctx, track.Artists[1].ID, "Demon"); err != nil {
		t.Fatal(err)
	}
	check("Aiobahn +81 feat. Demon & C", "Aiobahn +81 feat. Demon", " feat. ")

	correction, err := s.PrimaryArtistCorrection(ctx, track.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{track.Artists[0].ID, track.Artists[2].ID, track.Artists[1].ID}
	if err := s.SetPrimaryArtistCorrection(ctx, track.ID, correction.Token, ids, false); err != nil {
		t.Fatal(err)
	}
	check("Aiobahn +81, C, Demon", "Aiobahn +81 feat. Demon", "")

	// A separate file scan can retain a track credit independently of the VA album policy.
	input.Metadata.Raw = map[string][]string{
		"ARTIST": {"Aiobahn +81 feat. DEMONDICE & C"}, "ARTISTS": {"Aiobahn +81", "DEMONDICE", "C"},
		"ALBUMARTIST": {"Various Artists"}, "ALBUMARTISTS": {"Various Artists"},
	}
	input.ModifiedAtNS++
	if err := s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	correction, err = s.PrimaryArtistCorrection(ctx, track.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetPrimaryArtistCorrection(ctx, track.ID, correction.Token, nil, false); err != nil {
		t.Fatal(err)
	}
	check("Aiobahn +81 feat. Demon & C", "Various Artists", " feat. ")

	input.Metadata.Raw["ARTIST"] = []string{"Aiobahn +81, DEMONDICE, C"}
	input.ModifiedAtNS++
	if err := s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	check("Aiobahn +81, Demon, C", "Various Artists", "")
}
