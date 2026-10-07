package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

// withFakeBangumi attaches an enrichment manager whose Bangumi API is served
// by handler, and returns a counter of subject requests.
func withFakeBangumi(t *testing.T, app *App, handler http.HandlerFunc) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	manager := enrichment.New(context.Background(), app.store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.SetBangumiBaseURL(server.URL)
	app.enrichment = manager
	return &hits
}

func bangumiSubject(id int64, typ int, name, nameCN, date string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v0/subjects/"+strconv.FormatInt(id, 10)) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "type": typ, "name": name, "name_cn": nameCN, "date": date, "platform": "TV"})
	}
}

func TestAlbumAddWorkByBangumiCreatesAndLinksWork(t *testing.T) {
	app, cookie, csrf, albumID, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	withFakeBangumi(t, app, bangumiSubject(487630, 2, "俺だけレベルアップな件 -ReAwakening-", "我独自升级 第二季", "2025-01-05"))
	handler := app.Handler()
	albumPath := "/admin/albums/" + strconv.FormatInt(albumID, 10) + "/works"

	rec := postForm(handler, cookie, albumPath, url.Values{"csrfToken": {csrf}, "bangumiSubject": {"https://bgm.tv/subject/487630"}, "role": {"ost"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	location, _ := url.Parse(rec.Header().Get("Location"))
	if location.Query().Get("notice") != "已从 Bangumi 创建作品并关联" {
		t.Fatalf("location=%q", rec.Header().Get("Location"))
	}
	works, err := app.store.AlbumLevelWorks(ctx, albumID)
	if err != nil || len(works) != 1 {
		t.Fatalf("album works: %v %+v", err, works)
	}
	work := works[0].Work
	if work.Title != "俺だけレベルアップな件 -ReAwakening-" || work.TranslatedTitle != "我独自升级 第二季" || work.Type != "anime" || work.Year != 2025 || works[0].Role != "ost" {
		t.Fatalf("created work = %+v role=%s", work, works[0].Role)
	}
	if bound, _ := app.store.WorkBangumiExternalID(ctx, work.ID); bound != "487630" {
		t.Fatalf("bangumi binding = %q", bound)
	}

	// Re-submitting the same subject (bare ID) reuses the bound work.
	rec = postForm(handler, cookie, albumPath, url.Values{"csrfToken": {csrf}, "bangumiSubject": {"487630"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("repeat status=%d body=%s", rec.Code, rec.Body.String())
	}
	all, err := app.store.ListWorks(ctx, storage.WorkFilters{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	matching := 0
	for _, w := range all {
		if w.Title == work.Title {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("repeat submission duplicated the work: %d copies", matching)
	}
}

func TestAlbumAddWorkErrorsExplainLocalIDAndBangumiInput(t *testing.T) {
	app, cookie, csrf, albumID, _, _ := setupBatch3HTTPFixture(t)
	withFakeBangumi(t, app, bangumiSubject(1, 1, "A Book", "", "2020-01-01"))
	handler := app.Handler()
	albumPath := "/admin/albums/" + strconv.FormatInt(albumID, 10) + "/works"

	for _, tc := range []struct {
		name   string
		form   url.Values
		status int
		want   string
	}{
		{"unknown local id", url.Values{"workId": {"487630"}}, http.StatusNotFound, "不是 Bangumi ID"},
		{"both filled", url.Values{"workId": {"1"}, "bangumiSubject": {"1"}}, http.StatusBadRequest, "其中一项"},
		{"none filled", url.Values{}, http.StatusBadRequest, "Bangumi 条目"},
		{"bad link", url.Values{"bangumiSubject": {"https://example.com/subject/1"}}, http.StatusBadRequest, "bgm.tv"},
		{"unsupported subject", url.Values{"bangumiSubject": {"1"}}, http.StatusUnprocessableEntity, "动画或游戏"},
		{"missing subject", url.Values{"bangumiSubject": {"2"}}, http.StatusNotFound, "找不到该条目"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.form.Set("csrfToken", csrf)
			rec := postForm(handler, cookie, albumPath, tc.form)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%q, want %d containing %q", rec.Code, rec.Body.String(), tc.status, tc.want)
			}
		})
	}
	if works, _ := app.store.AlbumLevelWorks(context.Background(), albumID); len(works) != 0 {
		t.Fatalf("failed submissions must not link works: %+v", works)
	}
}

func TestAlbumAddWorkByBangumiChecksAlbumFirst(t *testing.T) {
	app, cookie, csrf, _, _, _ := setupBatch3HTTPFixture(t)
	hits := withFakeBangumi(t, app, bangumiSubject(487630, 2, "Never Created", "", "2025-01-05"))
	rec := postForm(app.Handler(), cookie, "/admin/albums/999999/works", url.Values{"csrfToken": {csrf}, "bangumiSubject": {"487630"}})
	if rec.Code != http.StatusNotFound || hits.Load() != 0 {
		t.Fatalf("status=%d hits=%d", rec.Code, hits.Load())
	}
}

func TestCreateWorkFromBangumiPage(t *testing.T) {
	app, cookie, csrf, _, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	withFakeBangumi(t, app, bangumiSubject(9001, 4, "Game Title", "游戏标题", "2019-03-01"))
	handler := app.Handler()

	rec := postForm(handler, cookie, "/admin/works/bangumi", url.Values{"csrfToken": {csrf}, "bangumiSubject": {"9001"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	location, _ := url.Parse(rec.Header().Get("Location"))
	workID := parseInt64(strings.TrimPrefix(location.Path, "/admin/works/"))
	if workID <= 0 || location.Query().Get("notice") != "已从 Bangumi 创建作品" {
		t.Fatalf("location=%q", rec.Header().Get("Location"))
	}
	work, err := app.store.WorkByID(ctx, workID)
	if err != nil || work.Type != "game" || work.TranslatedTitle != "游戏标题" || work.Year != 2019 {
		t.Fatalf("work=%+v err=%v", work, err)
	}

	rec = postForm(handler, cookie, "/admin/works/bangumi", url.Values{"csrfToken": {csrf}, "bangumiSubject": {"https://bangumi.tv/subject/9001"}})
	again, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || again.Path != location.Path || again.Query().Get("notice") != "该 Bangumi 条目已有对应作品" {
		t.Fatalf("repeat status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	rec = postForm(handler, cookie, "/admin/works/bangumi", url.Values{"bangumiSubject": {"9001"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", rec.Code)
	}
}

func TestWorksPageCreateDrawerIsReachableFromHeader(t *testing.T) {
	app, cookie, _, _, _, _ := setupBatch3HTTPFixture(t)
	body := getAdmin(t, app.Handler(), cookie, "/admin/works")
	header := body[strings.Index(body, `<header class="browser-header">`):]
	header = header[:strings.Index(header, "</header>")]
	if !strings.Contains(header, `data-drawer-open="create-work"`) {
		t.Fatalf("header lacks the create-work drawer trigger: %s", header)
	}
	for _, want := range []string{`id="create-work"`, `action="/admin/works/bangumi"`, `name="bangumiSubject"`, `action="/admin/works"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("works page missing %s", want)
		}
	}
}

func TestEnsureBangumiWorkReusesUnboundIdentityAndRejectsTakenIdentity(t *testing.T) {
	app, _, _, _, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	store := app.store
	existing, err := store.CreateWork(ctx, storage.WorkInput{Title: "Same Title", Type: "anime", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}
	id, created, err := store.EnsureBangumiWork(ctx, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "100", Title: "Same Title", Type: "anime", Year: 2020, Payload: json.RawMessage(`{}`)})
	if err != nil || created || id != existing.ID {
		t.Fatalf("reuse: id=%d created=%v err=%v want id=%d", id, created, err, existing.ID)
	}
	_, _, err = store.EnsureBangumiWork(ctx, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "200", Title: "Same Title", Type: "anime", Year: 2020, Payload: json.RawMessage(`{}`)})
	var taken *storage.WorkIdentityTakenError
	if err == nil || !errors.As(err, &taken) || taken.WorkID != existing.ID || taken.ExternalID != "100" {
		t.Fatalf("taken identity err=%v", err)
	}
}
