package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func renderAlbumPage(t *testing.T, app *App, cookie *http.Cookie, albumID int64) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/albums/"+strconv.FormatInt(albumID, 10), nil)
	req.SetPathValue("id", strconv.FormatInt(albumID, 10))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestAlbumPageBangumiBlockMessages(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(context.Context, *App, int64)
	}{
		{
			name: "rejected",
			want: "已拒绝 Bangumi 候选，请在作品页手动关联",
			setup: func(ctx context.Context, app *App, albumID int64) {
				if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{{ExternalID: "8", Title: "Song"}}); err != nil {
					t.Fatal(err)
				}
				candidates, _ := app.store.AlbumSubjectCandidates(ctx, albumID)
				if err := app.store.RejectAlbumSubjectCandidate(ctx, albumID, candidates[0].ID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unlinked",
			want: "已解除 Bangumi 关联，请在作品页手动关联",
			setup: func(ctx context.Context, app *App, albumID int64) {
				if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{{ExternalID: "8", Title: "Song", Tieups: []storage.BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}}}); err != nil {
					t.Fatal(err)
				}
				candidates, _ := app.store.AlbumSubjectCandidates(ctx, albumID)
				if _, _, err := app.store.ConfirmAlbumSubjectCandidate(ctx, albumID, candidates[0].ID, 0, "", false, []int64{5}); err != nil {
					t.Fatal(err)
				}
				works, _ := app.store.WorksForAlbum(ctx, albumID)
				if err := app.store.RemoveWorkAlbum(ctx, works[0].ID, albumID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, cookie, _ := csrfSessionApp(t)
			ctx := context.Background()
			if err := app.store.EnsureLibrary(ctx, "Music", "/"+tc.name); err != nil {
				t.Fatal(err)
			}
			lib, _ := app.store.LibraryByRoot(ctx, "/"+tc.name)
			if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: tc.name, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
				t.Fatal(err)
			}
			albums, _ := app.store.ListAlbums(ctx, storage.Filters{Query: tc.name, Limit: 10})
			albumID := albums[0].ID
			tc.setup(ctx, app, albumID)
			body := renderAlbumPage(t, app, cookie, albumID)
			if !strings.Contains(body, tc.want) || strings.Contains(body, ">从 Bangumi 查找<") {
				t.Fatalf("body=%s", body)
			}
		})
	}
}

func TestAlbumPageWithBangumiLinkShowsNoBlockedMessage(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	if err := app.store.EnsureLibrary(ctx, "Music", "/linked"); err != nil {
		t.Fatal(err)
	}
	lib, _ := app.store.LibraryByRoot(ctx, "/linked")
	if err := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Linked", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, _ := app.store.ListAlbums(ctx, storage.Filters{Query: "Linked", Limit: 10})
	albumID := albums[0].ID
	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}
	if err = app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{{ExternalID: "8", Title: "Song", Tieups: []storage.BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := app.store.AlbumSubjectCandidates(ctx, albumID)
	if _, _, err = app.store.ConfirmAlbumSubjectCandidate(ctx, albumID, candidates[0].ID, 0, "", false, []int64{5}); err != nil {
		t.Fatal(err)
	}
	body := renderAlbumPage(t, app, cookie, albumID)
	if strings.Contains(body, "已解除 Bangumi") || strings.Contains(body, "已拒绝 Bangumi") {
		t.Fatalf("linked album showed blocked message: %s", body)
	}
}
