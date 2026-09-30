package httpapi

import (
	"context"
	"fmt"
	"github.com/lux032/032music-server/internal/storage"
	"net/url"
	"testing"
)

func TestResetArtistIdentityRequiresConfirmation(t *testing.T) {
	_, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	artists, _ := s.ArtistsForMatching(ctx)
	artist := artists[0].ID
	if err := s.UpsertExternalArtistProfile(ctx, artist, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "wrong", DisplayName: "Wrong"}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/admin/artists/%d/identity/reset", artist)
	form := url.Values{"source": {"musicbrainz"}, "expectedID": {"wrong"}}
	rec := postAdminForm(t, handler, cookie, "wrong", path, form)
	if rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	rec = postAdminForm(t, handler, cookie, csrf, path, form)
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	if _, err := s.ArtistExternalID(ctx, artist, "musicbrainz"); err != nil {
		t.Fatal("removed without confirmation")
	}
	form.Set("confirm", "1")
	form.Set("expectedID", "changed")
	postAdminForm(t, handler, cookie, csrf, path, form)
	if _, err := s.ArtistExternalID(ctx, artist, "musicbrainz"); err != nil {
		t.Fatal("stale removed identity")
	}
	form.Set("expectedID", "wrong")
	rec = postAdminForm(t, handler, cookie, csrf, path, form)
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	if _, err := s.ArtistExternalID(ctx, artist, "musicbrainz"); err == nil {
		t.Fatal("identity not removed")
	}
}
