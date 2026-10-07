package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func missingFilesTestApp(t *testing.T, envPolicy string) (*App, *storage.Store, http.Handler, *http.Cookie) {
	t.Helper()
	store := credentialTestStore(t)
	cfg := credentialTestConfig(t)
	cfg.PurgeMissing = envPolicy
	app := newCredentialTestApp(t, cfg, store)
	handler := app.Handler()
	return app, store, handler, mustLogin(t, handler, "admin", testAdminPassword)
}

func TestPurgeMissingPolicySettings(t *testing.T) {
	app, store, handler, cookie := missingFilesTestApp(t, config.PurgeMissingFull)
	ctx := context.Background()
	if got := app.PurgeMissingPolicy(ctx); got != config.PurgeMissingFull {
		t.Fatalf("env policy = %q", got)
	}
	body := getWithCookie(handler, "/admin", cookie).Body.String()
	if !strings.Contains(body, `id="library-missing"`) || !strings.Contains(body, "仅完整重扫后自动删除") || !strings.Contains(body, `href="/admin/library/missing"`) {
		t.Fatal("dashboard must link the missing-files page and show the policy")
	}

	rec := postSecurity(t, app, handler, cookie, "/admin/settings/purge-missing", url.Values{"policy": {"always"}})
	if rec.Code != http.StatusSeeOther || app.PurgeMissingPolicy(ctx) != config.PurgeMissingAlways {
		t.Fatalf("save: status=%d policy=%q", rec.Code, app.PurgeMissingPolicy(ctx))
	}
	page := getWithCookie(handler, missingFilesPath, cookie).Body.String()
	if !strings.Contains(page, `<option value="always" selected>`) || !strings.Contains(page, "/admin/settings/purge-missing/reset") {
		t.Fatal("missing page must show the override and a reset control")
	}

	// 非法值被拒绝，设置不变。
	postSecurity(t, app, handler, cookie, "/admin/settings/purge-missing", url.Values{"policy": {"sometimes"}})
	if saved, found, err := store.PurgeMissingSetting(ctx); err != nil || !found || saved != config.PurgeMissingAlways {
		t.Fatalf("invalid policy changed the setting: %q found=%v err=%v", saved, found, err)
	}

	postSecurity(t, app, handler, cookie, "/admin/settings/purge-missing/reset", nil)
	if got := app.PurgeMissingPolicy(ctx); got != config.PurgeMissingFull {
		t.Fatalf("after reset = %q, want env default", got)
	}
	if strings.Contains(getWithCookie(handler, missingFilesPath, cookie).Body.String(), "/admin/settings/purge-missing/reset") {
		t.Fatal("reset control must only appear when overridden")
	}

	// 缺省/非法环境变量一律按 never 处理。
	app.config.PurgeMissing = ""
	if got := app.PurgeMissingPolicy(ctx); got != config.PurgeMissingNever {
		t.Fatalf("empty env policy = %q", got)
	}
}

func TestMissingFilesPageListsAndPurges(t *testing.T) {
	app, store, handler, cookie := missingFilesTestApp(t, "")
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/missing-page"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/missing-page")
	if err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{"Gone/01.flac", "Gone/02.flac"} {
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: path, FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song %d", i+1), Album: "Gone", Artists: []string{"Leaver"}, AlbumArtists: []string{"Leaver"}, DiscNumber: 1, TrackNumber: i + 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if body := getWithCookie(handler, missingFilesPath, cookie).Body.String(); !strings.Contains(body, "没有缺失文件") {
		t.Fatal("empty state expected before anything goes missing")
	}
	if _, err := store.MarkMissing(ctx, library.ID, "9999"); err != nil {
		t.Fatal(err)
	}

	page := getWithCookie(handler, missingFilesPath, cookie)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "Gone/01.flac") || !strings.Contains(body, "Gone/02.flac") || !strings.Contains(body, "已隐藏") {
		t.Fatalf("missing page status=%d", page.Code)
	}
	if strings.Contains(getWithCookie(handler, "/admin/albums", cookie).Body.String(), ">Gone<") {
		t.Fatal("album without available files must not be listed")
	}
	files, _, err := store.ListMissingFiles(ctx, 10, 0)
	if err != nil || len(files) != 2 {
		t.Fatalf("missing files = %+v err=%v", files, err)
	}

	// 未确认、未勾选的请求不删除任何东西。
	postSecurity(t, app, handler, cookie, "/admin/library/missing/purge", url.Values{"id": {fmt.Sprint(files[0].ID)}})
	postSecurity(t, app, handler, cookie, "/admin/library/missing/purge", url.Values{"confirm": {"selected"}})
	if _, total, _ := store.ListMissingFiles(ctx, 10, 0); total != 2 {
		t.Fatalf("unconfirmed purge deleted files: total=%d", total)
	}
	// 缺少 CSRF 令牌被拒绝。
	if rec := postSecurity(t, app, handler, cookie, "/admin/library/missing/purge", url.Values{"csrfToken": {"bad"}, "confirm": {"all"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF status = %d", rec.Code)
	}

	rec := postSecurity(t, app, handler, cookie, "/admin/library/missing/purge", url.Values{"confirm": {"selected"}, "id": {fmt.Sprint(files[0].ID)}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), url.QueryEscape("已删除 1 个缺失文件")) {
		t.Fatalf("selected purge: status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := store.TrackByID(ctx, files[0].TrackID); err == nil {
		t.Fatal("selected track must be deleted")
	}
	if _, err := store.TrackByID(ctx, files[1].TrackID); err != nil {
		t.Fatalf("unselected track must be kept: %v", err)
	}

	postSecurity(t, app, handler, cookie, "/admin/library/missing/purge", url.Values{"confirm": {"all"}})
	if _, total, _ := store.ListMissingFiles(ctx, 10, 0); total != 0 {
		t.Fatalf("purge all left %d files", total)
	}
	if _, err := store.TrackByID(ctx, files[1].TrackID); err == nil {
		t.Fatal("purge all must delete the remaining track")
	}
}
