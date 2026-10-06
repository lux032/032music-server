package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

func libraryWatchTestApp(t *testing.T, envInterval time.Duration) (*App, *storage.Store, http.Handler, *scanner.Watcher, *http.Cookie) {
	t.Helper()
	store := credentialTestStore(t)
	cfg := credentialTestConfig(t)
	cfg.WatchInterval = envInterval
	app := newCredentialTestApp(t, cfg, store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := scanner.New(context.Background(), store, logger, storage.Library{RootPath: t.TempDir()}, t.TempDir())
	watcher := scanner.NewWatcher(manager, logger, envInterval)
	app.SetLibraryWatcher(context.Background(), watcher)
	handler := app.Handler()
	return app, store, handler, watcher, mustLogin(t, handler, "admin", testAdminPassword)
}

func TestLibraryWatchDashboardShowsEnvDefault(t *testing.T) {
	_, _, handler, watcher, cookie := libraryWatchTestApp(t, time.Minute)
	if watcher.Interval() != time.Minute {
		t.Fatalf("interval = %s, want env default 1m", watcher.Interval())
	}
	body := getWithCookie(handler, "/admin", cookie).Body.String()
	for _, want := range []string{`id="library-watch"`, `action="/admin/settings/library-watch"`, `name="enabled" checked`, `<option value="60" selected>1 分钟</option>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
	if strings.Contains(body, "/admin/settings/library-watch/reset") {
		t.Fatal("reset control must only appear when overridden")
	}
}

func TestLibraryWatchSaveAppliesAndReset(t *testing.T) {
	app, store, handler, watcher, cookie := libraryWatchTestApp(t, time.Minute)
	ctx := context.Background()

	rec := postSecurity(t, app, handler, cookie, "/admin/settings/library-watch", url.Values{"enabled": {"on"}, "interval_seconds": {"300"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if watcher.Interval() != 5*time.Minute {
		t.Fatalf("interval = %s, want 5m applied immediately", watcher.Interval())
	}
	saved, found, err := store.LibraryWatchSettings(ctx)
	if err != nil || !found || !saved.Enabled || saved.Interval != 5*time.Minute {
		t.Fatalf("saved = %+v found=%v err=%v", saved, found, err)
	}
	body := getWithCookie(handler, "/admin", cookie).Body.String()
	if !strings.Contains(body, `<option value="300" selected>5 分钟</option>`) || !strings.Contains(body, "/admin/settings/library-watch/reset") {
		t.Fatal("dashboard must show the override and a reset control")
	}

	// 关闭：未勾选 enabled。
	postSecurity(t, app, handler, cookie, "/admin/settings/library-watch", url.Values{"interval_seconds": {"300"}})
	if watcher.Interval() != 0 {
		t.Fatalf("interval = %s, want paused", watcher.Interval())
	}

	// 非法间隔被拒绝，设置不变。
	rec = postSecurity(t, app, handler, cookie, "/admin/settings/library-watch", url.Values{"enabled": {"on"}, "interval_seconds": {"3"}})
	if rec.Code != http.StatusSeeOther || watcher.Interval() != 0 {
		t.Fatalf("invalid interval: status=%d interval=%s", rec.Code, watcher.Interval())
	}

	// 恢复默认：删除覆盖，回到环境变量值。
	postSecurity(t, app, handler, cookie, "/admin/settings/library-watch/reset", nil)
	if watcher.Interval() != time.Minute {
		t.Fatalf("interval = %s, want env default after reset", watcher.Interval())
	}
	if _, found, _ := store.LibraryWatchSettings(ctx); found {
		t.Fatal("reset must delete the override")
	}
}

func TestLibraryWatchOverrideAppliedAtStartup(t *testing.T) {
	store := credentialTestStore(t)
	if err := store.SaveLibraryWatchSettings(context.Background(), storage.LibraryWatchSettings{Enabled: true, Interval: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	cfg := credentialTestConfig(t)
	cfg.WatchInterval = 0 // 环境变量关闭，但管理页已开启
	app := newCredentialTestApp(t, cfg, store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	watcher := scanner.NewWatcher(scanner.New(context.Background(), store, logger, storage.Library{RootPath: t.TempDir()}, t.TempDir()), logger, cfg.WatchInterval)
	app.SetLibraryWatcher(context.Background(), watcher)
	if watcher.Interval() != 10*time.Minute {
		t.Fatalf("interval = %s, want admin override 10m", watcher.Interval())
	}
}

func TestLibraryWatchRequiresCSRF(t *testing.T) {
	app, _, handler, watcher, cookie := libraryWatchTestApp(t, time.Minute)
	rec := postSecurity(t, app, handler, cookie, "/admin/settings/library-watch", url.Values{"csrfToken": {"bad"}, "interval_seconds": {"600"}})
	if rec.Code != http.StatusForbidden || watcher.Interval() != time.Minute {
		t.Fatalf("status=%d interval=%s", rec.Code, watcher.Interval())
	}
}

// 空曲库提示随自动入库状态切换。
func TestEmptyLibraryHintFollowsLibraryWatch(t *testing.T) {
	app, _, handler, _, cookie := libraryWatchTestApp(t, time.Minute)
	body := getWithCookie(handler, "/admin", cookie).Body.String()
	if !strings.Contains(body, "已开启自动入库") || !strings.Contains(body, "每 1 分钟") {
		t.Fatal("enabled watcher must show the auto-import hint")
	}
	postSecurity(t, app, handler, cookie, "/admin/settings/library-watch", url.Values{"interval_seconds": {"60"}})
	body = getWithCookie(handler, "/admin", cookie).Body.String()
	if strings.Contains(body, "已开启自动入库") || !strings.Contains(body, "“完整重扫”导入您的音乐") {
		t.Fatal("disabled watcher must fall back to the manual-scan hint")
	}
}
