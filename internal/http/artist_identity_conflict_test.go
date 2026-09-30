package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestArtistIdentityConflictVerifyAndExplicitMerge(t *testing.T) {
	_, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	ownerName := "Owner <script>alert(1)</script>"
	if err = s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Owner/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Owner Song", Album: "Owner Album", Artists: []string{ownerName}, AlbumArtists: []string{ownerName}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var source, owner int64
	for _, artist := range artists {
		if artist.Name == "Artist" {
			source = artist.ID
		}
		if artist.Name == ownerName {
			owner = artist.ID
		}
	}
	if source == 0 || owner == 0 {
		t.Fatalf("artists=%+v", artists)
	}
	if err = s.UpsertExternalArtistProfile(ctx, owner, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "shared", DisplayName: ownerName}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceArtistCandidates(ctx, source, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: "shared", DisplayName: ownerName, Score: 100}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := s.ArtistCandidates(ctx, source)
	candidate := candidates[0].ID
	confirmPath := fmt.Sprintf("/admin/artists/%d/confirm/%d", source, candidate)
	rec := postAdminForm(t, handler, cookie, csrf, confirmPath, nil)
	page := fmt.Sprintf("/admin/artists/%d?view=profile&identityConflict=%d", source, candidate)
	if rec.Code != 303 || rec.Header().Get("Location") != page {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	render := func(path string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	body := render(page)
	for _, part := range []string{`target="_blank" rel="noopener noreferrer"`, `data-dialog-open="identity-merge-dialog"`, `name="expectedTarget"`, `name="confirm" value="1"`, `Owner &lt;script&gt;alert(1)&lt;/script&gt;`} {
		if !strings.Contains(body, part) {
			t.Fatalf("missing %s", part)
		}
	}
	if strings.Contains(body, ownerName) {
		t.Fatal("unescaped artist name")
	}
	mergePath := fmt.Sprintf("/admin/artists/%d/identity-conflict/%d/merge", source, candidate)
	rec = postAdminForm(t, handler, cookie, "wrong", mergePath, url.Values{"confirm": {"1"}, "expectedTarget": {fmt.Sprint(owner)}})
	if rec.Code != 403 {
		t.Fatalf("csrf %d", rec.Code)
	}
	rec = postAdminForm(t, handler, cookie, csrf, mergePath, url.Values{"expectedTarget": {fmt.Sprint(owner)}})
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	detail, _ := s.ArtistDetail(ctx, source)
	if detail.MergedIntoID != 0 {
		t.Fatal("merged without second confirm")
	}
	rec = postAdminForm(t, handler, cookie, csrf, mergePath, url.Values{"confirm": {"1"}, "expectedTarget": {fmt.Sprint(source)}})
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	operations, _ := s.MergeOperations(ctx)
	if len(operations) != 0 {
		t.Fatal("stale created merge")
	}
	rec = postAdminForm(t, handler, cookie, csrf, mergePath, url.Values{"confirm": {"1"}, "expectedTarget": {fmt.Sprint(owner)}})
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), fmt.Sprintf("/admin/artists/%d?", owner)) {
		t.Fatalf("merge %d %s", rec.Code, rec.Header().Get("Location"))
	}
	operations, _ = s.MergeOperations(ctx)
	if len(operations) != 1 {
		t.Fatal(operations)
	}
	candidates, _ = s.ArtistCandidates(ctx, source)
	if candidates[0].Status != "candidate" {
		t.Fatal("candidate was confirmed by merge")
	}
	if err = s.RollbackArtistMerge(ctx, operations[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RejectArtistCandidate(ctx, source, candidate); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(render(page), `id="identity-merge-dialog"`) {
		t.Fatal("stale panel offers merge")
	}
}
