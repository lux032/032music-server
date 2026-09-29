package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestBatch95ParseBangumiSubjectID(t *testing.T) {
	for _, raw := range []string{"55770", "https://bgm.tv/subject/55770", "http://bangumi.tv/subject/55770", "https://chii.in/subject/55770", "https://bgm.tv/subject/55770?from=test", "https://bgm.tv/subject/55770#section"} {
		id, err := parseBangumiSubjectID(raw)
		if err != nil || id != 55770 {
			t.Fatalf("parse %q = %d, %v", raw, id, err)
		}
	}
	for _, raw := range []string{"", "abc", "-55770", "999999999999999999999999999999999999", "https://example.com/subject/55770", "https://bgm.tv/person/55770", "https://bgm.tv/subject/x"} {
		if _, err := parseBangumiSubjectID(raw); err == nil {
			t.Fatalf("parse %q unexpectedly succeeded", raw)
		}
	}
}

func TestBatch95DeleteWorkRequiresConfirm(t *testing.T) {
	app, store, _ := setupTestApp(t)
	work, err := store.CreateWork(context.Background(), storage.WorkInput{Title: "Delete me", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, app)
	csrf := csrfOf(t, app, cookie)
	post := func(confirm string) *httptest.ResponseRecorder {
		form := url.Values{"csrfToken": {csrf}}
		if confirm != "" {
			form.Set("confirm", confirm)
		}
		req := httptest.NewRequest(http.MethodPost, "/admin/works/"+strconv.FormatInt(work.ID, 10)+"/delete", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post(""); rec.Code != http.StatusBadRequest {
		t.Fatalf("without confirm status=%d", rec.Code)
	}
	if _, err = store.WorkByID(context.Background(), work.ID); err != nil {
		t.Fatalf("work deleted without confirmation: %v", err)
	}
	if rec := post("1"); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBatch95FavoritesGridCookie(t *testing.T) {
	app, _, _ := setupTestApp(t)
	cookie := adminCookie(t, app)
	req := httptest.NewRequest(http.MethodGet, "/admin/favorites", nil)
	req.AddCookie(cookie)
	req.AddCookie(&http.Cookie{Name: albumGridColsCookie, Value: "7"})
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `data-grid-cols`) || !strings.Contains(body, `value="7"`) || !strings.Contains(body, `--album-cols:7`) {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
}

func TestBatch95AlbumCandidateConflictHandlerMessage(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, _ := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err := store.SaveAlbumSubjectCandidates(ctx, albums[0].ID, []storage.AlbumSubjectCandidate{{ExternalID: "9500", Title: "No writable relation", Score: 80, Tieups: []storage.BangumiTieup{}}}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.AlbumSubjectCandidates(ctx, albums[0].ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%v err=%v", candidates, err)
	}
	candidateID := candidates[0].ID
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrichment/albums/"+strconv.FormatInt(albums[0].ID, 10)+"/subjects/"+strconv.FormatInt(candidateID, 10)+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "没有可写入的作品关联：该条目可能已被拒绝、抑制，或存在作品身份冲突") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBatch95AddWorkAlbumEmptySelectionRedirectsWithNotice(t *testing.T) {
	app, store, _ := setupTestApp(t)
	work, err := store.CreateWork(context.Background(), storage.WorkInput{Title: "Album picker", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, app)
	csrf := csrfOf(t, app, cookie)
	form := url.Values{"csrfToken": {csrf}, "albumId": {"0"}, "role": {"other"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/works/"+strconv.FormatInt(work.ID, 10)+"/albums", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	location, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || location.Path != "/admin/works/"+strconv.FormatInt(work.ID, 10) || location.Query().Get("notice") != "请从搜索结果中选择专辑" {
		t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestBatch95ConflictWorkLinkRendersOnBothManualEntryPages(t *testing.T) {
	app, store, _ := setupTestApp(t)
	owner, err := store.CreateWork(context.Background(), storage.WorkInput{Title: `Owner <script>alert(1)</script>`, Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateWork(context.Background(), storage.WorkInput{Title: "Target", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, app)
	for _, path := range []string{
		"/admin/works/" + strconv.FormatInt(target.ID, 10) + "?notice=ignored&conflictWorkId=" + strconv.FormatInt(owner.ID, 10),
		"/admin/work-review?tab=works&notice=ignored&conflictWorkId=" + strconv.FormatInt(owner.ID, 10),
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		body := rec.Body.String()
		wantLink := `href="/admin/works/` + strconv.FormatInt(owner.ID, 10) + `"`
		if rec.Code != http.StatusOK || !strings.Contains(body, wantLink) || !strings.Contains(body, "该条目已对齐到作品") {
			t.Fatalf("path=%s status=%d body=%s", path, rec.Code, body)
		}
		if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, ">ignored</div>") {
			t.Fatalf("unsafe or stale notice rendered: %s", body)
		}
	}
}

func TestBatch95ConflictRedirectKeepsSafeReturnTo(t *testing.T) {
	got := adminURLWithParam("/admin/work-review?tab=works", "conflictWorkId", "42")
	u, err := url.Parse(got)
	if err != nil || u.Path != "/admin/work-review" || u.Query().Get("tab") != "works" || u.Query().Get("conflictWorkId") != "42" {
		t.Fatalf("url=%q err=%v", got, err)
	}
}

func TestBatch95WorkUpdatePreservesLegacyExternalIDWhenFieldOmitted(t *testing.T) {
	app, store, _ := setupTestApp(t)
	work, err := store.CreateWork(context.Background(), storage.WorkInput{Title: "Legacy", Type: "anime", ExternalID: "old-value"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, app)
	csrf := csrfOf(t, app, cookie)
	form := url.Values{"csrfToken": {csrf}, "title": {"Renamed"}, "type": {"anime"}, "originalType": {"anime"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/works/"+strconv.FormatInt(work.ID, 10), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	updated, err := store.WorkByID(context.Background(), work.ID)
	if rec.Code != http.StatusSeeOther || err != nil || updated.ExternalID != "old-value" {
		t.Fatalf("status=%d updated=%+v err=%v", rec.Code, updated, err)
	}
}
