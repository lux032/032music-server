package enrichment

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestTaggedArtistNameMismatchCannotAutoConfirm(t *testing.T) {
	const mbid = "11111111-1111-4111-8111-111111111111"
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) { return http.DefaultTransport.RoundTrip(r) }))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"`+mbid+`","name":"Wrong Performer","sort-name":"Wrong Performer","aliases":[],"tags":[],"relations":[]}`)
	}))
	defer server.Close()
	manager.musicBrainzBase = server.URL
	s := manager.store
	ctx := context.Background()
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"ACE+"}, AlbumArtists: []string{"ACE+"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {mbid}}}}); err != nil {
		t.Fatal(err)
	}
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Enabled = true
	setting.AutoMatch = true
	if err = s.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("%v %v", artists, err)
	}
	result, err := manager.MatchArtist(ctx, artists[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.AutoMatched {
		t.Fatal("mismatched name auto confirmed")
	}
	candidates, _ := s.ArtistCandidates(ctx, artists[0].ID)
	if len(candidates) != 1 || candidates[0].Score >= 92 || candidates[0].Status != "candidate" {
		t.Fatalf("%+v", candidates)
	}
}
func TestArtistProfileNameMatchesAliases(t *testing.T) {
	if !artistProfileNameMatches("ACE+", storage.ExternalArtistProfile{DisplayName: "Other", Aliases: []string{"ACE+"}}) {
		t.Fatal("alias match rejected")
	}
	if artistProfileNameMatches("ACE+", storage.ExternalArtistProfile{DisplayName: "清田愛未"}) {
		t.Fatal("different name accepted")
	}
}
