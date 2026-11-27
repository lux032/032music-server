package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestAlbumArtistTaggedIdentityCaptureAndGuards(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	var libID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM libraries WHERE root_path='/music'`).Scan(&libID); err != nil {
		t.Fatalf("lookup library id: %v", err)
	}
	lib := struct{ ID int64 }{ID: libID}
	var err error

	const (
		albumMBID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		singerMBID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		coAlbumMBID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	)

	// 1. Artist appearing ONLY as album_artist gets TaggedMBID via album_artist role.
	if err = s.ImportTrack(ctx, ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "ost/01.flac",
		FileSize:     1000,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Track 1",
			Album:        "OST Vol.1",
			DiscNumber:   1,
			TrackNumber:  1,
			Artists:      []string{"Singer A"},
			AlbumArtists: []string{"Yuki Kajiura"},
			Container:    "flac",
			Raw: map[string][]string{
				"ARTIST":                    {"Singer A"},
				"MUSICBRAINZ_ARTISTID":      {singerMBID},
				"ALBUMARTIST":               {"Yuki Kajiura"},
				"MUSICBRAINZ_ALBUMARTISTID": {albumMBID},
			},
		},
	}); err != nil {
		t.Fatalf("import track 1: %v", err)
	}

	artists, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatalf("ArtistsForMatching: %v", err)
	}
	byName := map[string]ArtistMatchInput{}
	for _, a := range artists {
		byName[a.Name] = a
	}
	if got := byName["Yuki Kajiura"].TaggedMBID; got != albumMBID {
		t.Fatalf("Yuki Kajiura TaggedMBID = %q, want %q", got, albumMBID)
	}
	if got := byName["Singer A"].TaggedMBID; got != singerMBID {
		t.Fatalf("Singer A TaggedMBID = %q, want %q", got, singerMBID)
	}
	// Verify single-artist query parity.
	single, err := s.ArtistForMatching(ctx, byName["Yuki Kajiura"].ID)
	if err != nil || single.TaggedMBID != albumMBID {
		t.Fatalf("single ArtistForMatching = %+v, err=%v", single, err)
	}

	// 2. Multi-album-artist alignment works when counts strictly match.
	if err = s.ImportTrack(ctx, ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "ost/02.flac",
		FileSize:     1000,
		ModifiedAtNS: 2,
		Metadata: metadata.AudioMetadata{
			Title:        "Track 2",
			Album:        "Collab Album",
			DiscNumber:   1,
			TrackNumber:  2,
			Artists:      []string{"Singer A"},
			AlbumArtists: []string{"Yuki Kajiura", "FictionJunction"},
			Container:    "flac",
			Raw: map[string][]string{
				"ARTIST":                    {"Singer A"},
				"MUSICBRAINZ_ARTISTID":      {singerMBID},
				"ALBUMARTISTS":              {"Yuki Kajiura", "FictionJunction"},
				"MUSICBRAINZ_ALBUMARTISTID": {albumMBID, coAlbumMBID},
			},
		},
	}); err != nil {
		t.Fatalf("import track 2: %v", err)
	}
	artists, err = s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatalf("ArtistsForMatching after track 2: %v", err)
	}
	byName = map[string]ArtistMatchInput{}
	for _, a := range artists {
		byName[a.Name] = a
	}
	if got := byName["FictionJunction"].TaggedMBID; got != coAlbumMBID {
		t.Fatalf("FictionJunction TaggedMBID = %q, want %q", got, coAlbumMBID)
	}

	// 3. Mismatched / invalid album_artist MBID blocks album_artist role without tainting primary role.
	if err = s.ImportTrack(ctx, ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "ost/03.flac",
		FileSize:     1000,
		ModifiedAtNS: 3,
		Metadata: metadata.AudioMetadata{
			Title:        "Track 3",
			Album:        "Broken Album",
			DiscNumber:   1,
			TrackNumber:  3,
			Artists:      []string{"Singer B"},
			AlbumArtists: []string{"Various Artists"},
			Container:    "flac",
			Raw: map[string][]string{
				"ARTIST":                    {"Singer B"},
				"MUSICBRAINZ_ARTISTID":      {"dddddddd-dddd-4ddd-8ddd-dddddddddddd"},
				"ALBUMARTIST":               {"Various Artists"},
				"MUSICBRAINZ_ALBUMARTISTID": {albumMBID + "; " + coAlbumMBID},
			},
		},
	}); err != nil {
		t.Fatalf("import track 3: %v", err)
	}
	artists, err = s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatalf("ArtistsForMatching after track 3: %v", err)
	}
	byName = map[string]ArtistMatchInput{}
	for _, a := range artists {
		byName[a.Name] = a
	}
	if got := byName["Various Artists"].TaggedMBID; got != "" {
		t.Fatalf("expected Various Artists TaggedMBID to be blocked, got %q", got)
	}
	if got := byName["Singer B"].TaggedMBID; got != "dddddddd-dddd-4ddd-8ddd-dddddddddddd" {
		t.Fatalf("primary role on track 3 should remain intact, got %q", got)
	}

	// 4. Legacy pre-095 file (no album_artist check row) safely derives single album artist MBID from audio_file_tags.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM audio_file_artist_identities WHERE role='album_artist'`); err != nil {
		t.Fatalf("clear album_artist identities: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM audio_file_identity_checks WHERE role='album_artist'`); err != nil {
		t.Fatalf("clear album_artist checks: %v", err)
	}
	single, err = s.ArtistForMatching(ctx, byName["Yuki Kajiura"].ID)
	if err != nil {
		t.Fatalf("legacy single ArtistForMatching: %v", err)
	}
	if single.TaggedMBID != albumMBID {
		t.Fatalf("legacy single-album-artist fallback TaggedMBID = %q, want %q", single.TaggedMBID, albumMBID)
	}
	// Multi-album-artist album (FictionJunction) must NOT use legacy single-artist fallback.
	multiLegacy, err := s.ArtistForMatching(ctx, byName["FictionJunction"].ID)
	if err != nil {
		t.Fatalf("legacy multi ArtistForMatching: %v", err)
	}
	if multiLegacy.TaggedMBID != "" {
		t.Fatalf("expected multi-album-artist legacy fallback empty, got %q", multiLegacy.TaggedMBID)
	}
}
