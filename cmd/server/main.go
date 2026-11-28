package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lux032/032music-server/internal/acquisition"
	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/aireview"
	"github.com/lux032/032music-server/internal/analysis"
	"github.com/lux032/032music-server/internal/artistaliases"
	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/dailymix"
	"github.com/lux032/032music-server/internal/discovery"
	"github.com/lux032/032music-server/internal/enrichment"
	webhttp "github.com/lux032/032music-server/internal/http"
	"github.com/lux032/032music-server/internal/lastfm"
	"github.com/lux032/032music-server/internal/listenbrainz"
	"github.com/lux032/032music-server/internal/loudness"
	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/upnp"
	"github.com/lux032/032music-server/internal/websearch"
)

var version = "dev"
var errRestart = errors.New("restart requested")

func main() {
	if err := run(); err != nil {
		if exitCode(err) == 75 {
			os.Exit(exitCode(err))
		}
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	for _, warning := range cfg.Warnings {
		logger.Warn("config", "warning", warning)
	}

	if cfg.DevMode {
		logger.Warn("MUSIC_SERVER_DEV_MODE is enabled: weak admin passwords are allowed and the media token may fall back to the API token; do not use in production")
	}
	backups := storage.NewBackups(cfg.DatabasePath, cfg.DataDirectory, cfg.MusicDirectory)
	ctx, stopStartup := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopStartup()
	db, err := openStartupDatabase(ctx, cfg, backups, logger)
	if err != nil {
		return err
	}
	if db == nil {
		return runRecoveryMode(ctx, cfg, backups, logger)
	}
	defer db.Close()

	if err := db.EnsureLibrary(context.Background(), cfg.LibraryName, cfg.MusicDirectory); err != nil {
		return err
	}
	library, err := db.LibraryByRoot(context.Background(), cfg.MusicDirectory)
	if err != nil {
		return err
	}
	// rootCtx governs all background work (scans, enrichment). It is
	// cancelled on shutdown so workers stop before the database is closed.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	if err := db.RecoverReleaseGroupRebuild(rootCtx); err != nil {
		logger.Error("recover RG identity", "error", err)
	}
	scannerManager := scanner.New(rootCtx, db, logger, library, cfg.DataDirectory)
	scannerManager.ProtectRestoreScan(backups.ScanProtectionPath())
	enrichmentManager := enrichment.New(rootCtx, db, logger, cfg.DataDirectory)
	gate := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"bangumi": enrichment.BangumiIntervalFromEnv(logger), "musicbrainz": enrichment.MBIntervalFromEnv(logger), "deezer": 150 * time.Millisecond, "coverartarchive": time.Second, "jackett": 3 * time.Second}})
	enrichmentManager.SetSourceGate(gate)
	discoveryService := discovery.New(db, logger, discovery.Options{Bangumi: enrichmentManager, BaseContext: rootCtx, CoverBackfill: true, Scheduler: true, Gate: gate, DataDir: cfg.DataDirectory, UserAgent: enrichmentManager.MetadataUserAgent})
	if err := discoveryService.Start(rootCtx); err != nil {
		logger.Warn("resume discovery", "error", err)
	}

	acquisitionService := acquisition.New(db, logger, acquisition.Options{Gate: gate})
	if err := acquisitionService.Start(rootCtx); err != nil {
		logger.Warn("start acquisition", "error", err)
	}

	// 重启前处于限流等待的任务：恢复倒计时，到点自动继续；人工暂停与存储
	// 错误的任务不会被扫描拾起，仍需在管理页手动处理。
	enrichmentManager.ScanAutoResumeRuns()
	enrichmentManager.SetPosterBackfillEnabled(cfg.WorkPosterBackfill)
	// 批次 8：启动约 30 秒后自动补一次缺失的作品海报（替代原来的立即补全，
	// 避免与启动扫描争抢）；开关关闭时不排期。
	enrichmentManager.ScheduleStartupPosterBackfill(30 * time.Second)

	// NewApp applies MUSIC_SERVER_RESET_CREDENTIALS and loads admin-page
	// credential overrides, so the per-boot media token warning is only
	// meaningful afterwards: a stored override replaces the random token.
	app, err := webhttp.NewApp(cfg, db, scannerManager, enrichmentManager, logger, version)
	if err != nil {
		return err
	}
	app.SetDiscovery(discoveryService)
	app.SetAcquisition(acquisitionService)
	restartCh := make(chan struct{}, 1)
	app.RequestRestart = func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	}
	app.SetBackups(backups)
	backups.Start(rootCtx, app.BackupSettings)
	// L2: collect custom-image orphans at startup and after every completed
	// scan (merges and cascading deletes leave no other trigger).
	go app.GCCustomImages(rootCtx)
	// 缺失文件清理策略：管理页覆盖优先，否则 MUSIC_SERVER_PURGE_MISSING
	// （默认 never，只隐藏不删除）；每次扫描结束时读取。
	scannerManager.SetPurgeMissingPolicy(app.PurgeMissingPolicy)
	// Acoustic analysis (analyzer/ sidecar): off by default; the admin page switch
	// starts the first full run, afterwards every completed scan wakes the worker
	// so new or changed files are analysed automatically. It yields to live
	// transcodes and library scans, which compete for the same CPU and disk.
	var analyzerClient analysis.Client // stays a nil interface when not deployed
	if cfg.AnalyzerURL != "" {
		analyzerClient = analysis.NewHTTPClient(cfg.AnalyzerURL, cfg.AnalyzerToken)
	}
	// AI tagging (OpenAI-compatible model service, configured on the admin page):
	// off by default; once enabled it tags the whole library, then new tracks as
	// soon as their acoustic analysis is saved (or right away when analysis is off).
	var taggingService *tagging.Service
	audioBusy := func() string {
		if app.TranscodesActive() > 0 {
			return "有转码进行中"
		}
		if scannerManager.Running() {
			return "曲库扫描中"
		}
		return ""
	}
	analysisService := analysis.New(db, logger, analysis.Options{
		Client:     analyzerClient,
		OnAnalyzed: func() { taggingService.Wake() },
		Busy:       audioBusy,
	})
	app.SetAudioAnalysis(analysisService)
	loudnessService := loudness.New(db, logger, loudness.Options{
		Measurer: loudness.NewFFmpegMeasurer(cfg.FFmpegPath),
		Workers:  cfg.LoudnessWorkers,
		Busy:     audioBusy,
	})
	app.SetLoudness(loudnessService)
	sharedWebSearch := websearch.NewRegistry()
	app.SetWebSearchRegistry(sharedWebSearch)
	enrichmentManager.SetWebSearchRegistry(sharedWebSearch)
	sharedAILimiter := aiclient.NewLimiter(60)
	if aiSt, aiErr := db.AISettings(context.Background()); aiErr == nil && aiSt.RequestsPerMinute > 0 {
		sharedAILimiter.SetRate(aiSt.RequestsPerMinute)
	}
	sharedWebSearch.SetRateWait(func(ctx context.Context, _ int) error {
		return sharedAILimiter.Wait(ctx)
	})
	sharedWebSearch.SetUsageRecorder(func(ctx context.Context, model, purpose string, usage storage.AIUsage) {
		if err := db.AddAIUsage(ctx, time.Now(), model, purpose, usage); err != nil {
			logger.Error("record native web search usage", "error", err)
		}
	})
	app.SetSharedAILimiter(sharedAILimiter)
	enrichmentManager.SetAILimiter(sharedAILimiter)
	aiReviewService := aireview.New(db, logger, aireview.Options{
		Limiter:     sharedAILimiter,
		WebRegistry: sharedWebSearch,
		OnWorkConfirmed: func(_ context.Context, workID int64) {
			enrichmentManager.QueueWorkPoster(workID)
		},
		OnArtistConfirmed: func(ctx context.Context, artistID int64) {
			enrichmentManager.EnrichAutoConfirmedArtist(ctx, artistID)
		},
	})
	app.SetAIReview(aiReviewService)
	enrichmentManager.SetInlineAIReviewer(aiReviewService.EvaluateInline)
	enrichmentManager.WakeAutoImageQueue()

	taggingService = tagging.New(db, logger, tagging.Options{
		AudioActive: func(ctx context.Context) bool {
			if !analysisService.Configured() {
				return false
			}
			settings, err := db.AudioAnalysisSettings(ctx)
			return err == nil && settings.Enabled
		},
		RateWait: func(ctx context.Context, rpm int) error {
			sharedAILimiter.SetRate(rpm)
			return sharedAILimiter.Wait(ctx)
		},
	})
	app.SetAITagging(taggingService)
	aliasService, err := artistaliases.New(db, logger, artistaliases.Options{
		Sources:     enrichmentManager.LoadArtistAliasSources,
		WebRegistry: sharedWebSearch,
	})
	if err != nil {
		return fmt.Errorf("create artist alias service: %w", err)
	}
	app.SetArtistAliases(aliasService)
	aliasService.Start(rootCtx)
	// Online lyrics (NetEase, LRCLIB) for tracks without local lyrics; HTTP(S)_PROXY applies.
	app.SetOnlineLyrics(lyricsonline.New())
	taggingService.Start(rootCtx)
	// Daily mixes: pre-generated at the configured hour (05:00 UTC+8 by default) and
	// generated on the first request otherwise; titles by the model when configured.
	dailyMixService := dailymix.New(db, logger, dailymix.Options{})
	app.SetDailyMixes(dailyMixService)
	dailyMixService.Start(rootCtx)
	analysisService.Start(rootCtx)
	loudnessService.Start(rootCtx)
	// First-run setup wizard scan controller: defers initial startup scan and
	// directory polling while setup_wizard_state is pending so users can
	// configure metadata sources and AI before the first import runs.
	watcher := scanner.NewWatcher(scannerManager, logger, cfg.WatchInterval)
	app.SetLibraryWatcher(rootCtx, watcher)
	scanCtrl := newFirstRunScanController(
		func() { app.ApplyLibraryWatchSettings(rootCtx) },
		func(startupScan bool) { watcher.Start(rootCtx, startupScan) },
		logger,
	)
	app.SetFirstRunScanReleaser(scanCtrl.Release)

	scannerManager.SetOnComplete(func() {
		if err := db.CompleteReleaseGroupRebuild(rootCtx); err != nil {
			logger.Error("complete RG identity", "error", err)
		}
		if err := db.RefreshReleaseGroupIdentities(rootCtx); err != nil {
			logger.Error("refresh RG identity", "error", err)
		}
		if err := db.ReconcileAlbumLoudness(rootCtx); err != nil {
			logger.Error("reconcile album loudness", "error", err)
		}
		app.GCCustomImages(rootCtx)

		if !scanCtrl.ShouldRunPostScanEnrich() {
			logger.Info("first scan completed in local-only mode; skipping automatic post-scan enrichment for this run")
			return
		}
		acquisitionService.Wake()
		enrichmentManager.StartAuto(rootCtx)
		analysisService.Wake()
		loudnessService.Wake()
		taggingService.Wake()
		aliasService.Wake()
	})
	lastFMService := lastfm.NewService(db, logger)
	lastFMService.Start(rootCtx)
	app.SetLastFM(lastFMService)
	listenBrainzService := listenbrainz.NewService(db, logger)
	listenBrainzService.Start(rootCtx)
	app.SetListenBrainz(listenBrainzService)
	// Sonos/DLNA casting: discovery runs on demand (device list requests).
	castManager := upnp.NewManager(upnp.Options{StaticDevices: cfg.CastDevices, DisableSSDP: !cfg.CastSSDP, Logger: logger})
	defer castManager.Close()
	app.SetCastManager(castManager)
	// Playback sessions: finalizes expired leases and purges ended sessions
	// past their retention; stops with rootCtx at shutdown. Reads derive the
	// effective state on their own, so this only persists the outcome.
	storage.NewPlaybackSweeper(db, logger).Start(rootCtx)
	if cfg.MediaTokenGenerated && app.CredentialSources().MediaToken == webhttp.CredentialSourceEnv {
		logger.Warn("MUSIC_SERVER_MEDIA_TOKEN is not set: generated a random per-boot media token; media URLs change on every restart — set MUSIC_SERVER_MEDIA_TOKEN to a stable random value or set a media token on the admin security page")
	}
	setupState, setupErr := db.SetupWizardState(rootCtx)
	if setupErr != nil {
		logger.Warn("load setup wizard state; proceeding with normal startup scan", "error", setupErr)
	}
	scanCtrl.InitAtStartup(setupErr == nil && setupState.IsPending())

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout is intentionally omitted: a global write timeout
		// kills long-running audio streams for large FLAC files. The
		// streaming handler uses http.ServeContent which handles range
		// requests; per-request context deadlines protect non-stream
		// endpoints instead.
		IdleTimeout: 2 * time.Minute,
	}

	server.RegisterOnShutdown(app.CancelTranscodes)
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverError := make(chan error, 1)
	go func() {
		logger.Info("server started", "address", cfg.ListenAddress, "version", version)
		serverError <- server.ListenAndServe()
	}()

	var serveErr error
	restarting := false
	select {
	case serveErr = <-serverError:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	case <-restartCh:
		restarting = true
		logger.Info("restore restart requested")
	case <-shutdownContext.Done():
		logger.Info("shutdown requested")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(ctx)
	if shutdownErr != nil {
		logger.Error("http shutdown failed", "error", shutdownErr)
		app.CancelTranscodes()
	}

	// Stop background workers and wait for them to finish writing before
	// run() returns and the deferred db.Close() executes.
	rootCancel()
	app.CancelTranscodes()
	workersDone := make(chan struct{})
	go func() {
		scannerManager.Wait()
		enrichmentManager.Wait()
		discoveryService.Wait()
		acquisitionService.Wait()
		lastFMService.Wait()
		listenBrainzService.Wait()
		analysisService.Wait()
		loudnessService.Wait()
		taggingService.Wait()
		aliasService.Wait()
		dailyMixService.Wait()
		backups.Wait()
		app.WaitTranscodes()
		app.WaitBackgroundLyrics()
		close(workersDone)
	}()
	select {
	case <-workersDone:
		logger.Info("background workers stopped")
	case <-time.After(30 * time.Second):
		logger.Warn("timed out waiting for background workers; closing database anyway")
	}

	if serveErr != nil {
		return serveErr
	}
	if restarting && shutdownErr == nil {
		return errRestart
	}
	return shutdownErr
}

func newLogger(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// openStartupDatabase 在任何数据库连接之前检测人工恢复隔离标志。
func openStartupDatabase(ctx context.Context, cfg config.Config, backups *storage.Backups, logger *slog.Logger) (*storage.Store, error) {
	if _, e := backups.ManualRecovery(); e == nil {
		return nil, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e := backups.Initialize(); e != nil {
		return nil, e
	}
	restored, err := backups.PrepareRestore(ctx)
	if _, e := backups.ManualRecovery(); e == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	db, err := storage.Open(cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	if !restored && !cfg.BackupSkipPreupgrade {
		pending, e := db.PendingMigrations(context.Background())
		if e != nil {
			return nil, e
		}
		if pending > 0 {
			settings, e := db.BackupSettings(context.Background(), storage.BackupSettings{Enabled: cfg.BackupEnabled, Time: cfg.BackupTime, KeepDaily: cfg.BackupKeepDaily, KeepPreupgrade: cfg.BackupKeepPreupgrade})
			if e != nil {
				return nil, e
			}
			if _, e = backups.Create(context.Background(), "preupgrade", settings.KeepPreupgrade); e != nil {
				return nil, fmt.Errorf("升级前快照失败，拒绝迁移；检查磁盘/权限，或设置 MUSIC_SERVER_BACKUP_SKIP_PREUPGRADE=1 跳过: %w", e)
			}
		}
	}
	migrationErr := db.Migrate(context.Background())
	if migrationErr == nil && restored {
		migrationErr = backups.CompleteRestore(context.Background(), db)
	}
	if migrationErr != nil {
		if !restored {
			return nil, migrationErr
		}
		_ = db.Close()
		if e := backups.RollbackRestore(context.Background(), migrationErr); e != nil {
			logger.Error("自动回滚失败，停止数据库写入，需离线恢复 prerestore；详见安全隔离页", "error", e)
			if _, markerErr := backups.ManualRecovery(); markerErr == nil {
				return nil, nil
			}
			return nil, fmt.Errorf("无法持久化安全隔离标志，请停止守护进程并离线恢复: %w", e)
		}
		logger.Error("恢复失败，已自动回滚", "error", migrationErr)
		db, err = storage.Open(cfg.DatabasePath)
		if err != nil {
			return nil, err
		}
		if err = db.Migrate(context.Background()); err != nil {
			return nil, err
		}
	}

	ok = true
	return db, nil
}
func exitCode(err error) int {
	if errors.Is(err, errRestart) {
		return 75
	}
	if err != nil {
		return 1
	}
	return 0
}

// firstRunScanController coordinates deferring the initial library watcher and
// startup scan while first-run setup is pending, releasing them once when setup
// finishes or is skipped, and optionally suppressing automatic post-scan
// enrichment for that first local-only scan.
type firstRunScanController struct {
	applyWatch         func()
	startWatcher       func(startupScan bool)
	logger             *slog.Logger
	releaseOnce        sync.Once
	suppressNextEnrich atomic.Bool
}

func newFirstRunScanController(applyWatch func(), startWatcher func(startupScan bool), logger *slog.Logger) *firstRunScanController {
	return &firstRunScanController{
		applyWatch:   applyWatch,
		startWatcher: startWatcher,
		logger:       logger,
	}
}

func (c *firstRunScanController) InitAtStartup(pending bool) {
	if pending {
		if c.logger != nil {
			c.logger.Info("deferring initial library scan and watcher until first-run setup completes or is skipped")
		}
		return
	}
	c.Release(true, true)
}

func (c *firstRunScanController) Release(startScan bool, autoEnrich bool) {
	c.releaseOnce.Do(func() {
		if startScan && !autoEnrich {
			c.suppressNextEnrich.Store(true)
		}
		if c.applyWatch != nil {
			c.applyWatch()
		}
		if c.startWatcher != nil {
			c.startWatcher(startScan)
		}
	})
}

func (c *firstRunScanController) ShouldRunPostScanEnrich() bool {
	return !c.suppressNextEnrich.Swap(false)
}
