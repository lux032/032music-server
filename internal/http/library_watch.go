package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

const maxWatchInterval = 24 * time.Hour

// 管理页可选的轮询间隔（秒）。
var watchIntervalPresets = []int{10, 30, 60, 120, 300, 600, 1800, 3600}

type watchIntervalOption struct {
	Seconds  int
	Label    string
	Selected bool
}

// libraryWatchView 是控制台“自动入库”区块的数据。
type libraryWatchView struct {
	Available  bool // 监控器已接入（测试环境可能没有）
	Enabled    bool
	Interval   string
	Overridden bool   // 当前值来自管理页设置，而非环境变量
	EnvSummary string // 环境变量默认值的描述
	Options    []watchIntervalOption
}

// SetLibraryWatcher 接入曲库监控器，并立即应用管理页保存的覆盖设置。
func (a *App) SetLibraryWatcher(ctx context.Context, watcher *scanner.Watcher) {
	a.watcher = watcher
	a.applyLibraryWatchSettings(ctx)
}

// effectiveLibraryWatch 返回生效的设置：管理页覆盖优先，否则取环境变量。
func (a *App) effectiveLibraryWatch(ctx context.Context) (storage.LibraryWatchSettings, bool, error) {
	saved, found, err := a.store.LibraryWatchSettings(ctx)
	if err != nil || found {
		return saved, found, err
	}
	if a.config.WatchInterval > 0 {
		return storage.LibraryWatchSettings{Enabled: true, Interval: a.config.WatchInterval}, false, nil
	}
	return storage.LibraryWatchSettings{Enabled: false, Interval: config.DefaultWatchInterval}, false, nil
}

func (a *App) applyLibraryWatchSettings(ctx context.Context) {
	if a.watcher == nil {
		return
	}
	settings, _, err := a.effectiveLibraryWatch(ctx)
	if err != nil {
		a.logger.Error("load library watch settings", "error", err)
		return
	}
	if settings.Enabled {
		a.watcher.SetInterval(settings.Interval)
	} else {
		a.watcher.SetInterval(0)
	}
}

func (a *App) libraryWatchView(ctx context.Context) libraryWatchView {
	view := libraryWatchView{Available: a.watcher != nil}
	settings, overridden, err := a.effectiveLibraryWatch(ctx)
	if err != nil {
		a.logger.Error("load library watch settings", "error", err)
		view.Available = false
		return view
	}
	view.Enabled = settings.Enabled
	view.Interval = formatWatchInterval(settings.Interval)
	view.Overridden = overridden
	if a.config.WatchInterval > 0 {
		view.EnvSummary = "每 " + formatWatchInterval(a.config.WatchInterval) + "检查"
	} else {
		view.EnvSummary = "关闭"
	}
	seconds := int(settings.Interval / time.Second)
	presets := watchIntervalPresets
	if !slices.Contains(presets, seconds) {
		presets = append(slices.Clone(presets), seconds)
		slices.Sort(presets)
	}
	for _, s := range presets {
		view.Options = append(view.Options, watchIntervalOption{Seconds: s, Label: formatWatchInterval(time.Duration(s) * time.Second), Selected: s == seconds})
	}
	return view
}

func formatWatchInterval(d time.Duration) string {
	seconds := int(d / time.Second)
	switch {
	case seconds%3600 == 0:
		return fmt.Sprintf("%d 小时", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%d 分钟", seconds/60)
	default:
		return fmt.Sprintf("%d 秒", seconds)
	}
}

func (a *App) handleSaveLibraryWatch(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	seconds, err := strconv.Atoi(r.FormValue("interval_seconds"))
	interval := time.Duration(seconds) * time.Second
	if err != nil || interval < config.MinWatchInterval || interval > maxWatchInterval {
		redirectWithNotice(w, r, "/admin", fmt.Sprintf("检查间隔需在 %s 到 %s 之间", formatWatchInterval(config.MinWatchInterval), formatWatchInterval(maxWatchInterval)))
		return
	}
	settings := storage.LibraryWatchSettings{Enabled: r.FormValue("enabled") != "", Interval: interval}
	if err := a.store.SaveLibraryWatchSettings(r.Context(), settings); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.applyLibraryWatchSettings(r.Context())
	if settings.Enabled {
		redirectWithNotice(w, r, "/admin", "自动入库已开启，每 "+formatWatchInterval(interval)+"检查一次")
	} else {
		redirectWithNotice(w, r, "/admin", "自动入库已关闭")
	}
}

func (a *App) handleResetLibraryWatch(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if err := a.store.ResetLibraryWatchSettings(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.applyLibraryWatchSettings(r.Context())
	redirectWithNotice(w, r, "/admin", "自动入库已恢复环境变量默认值")
}
