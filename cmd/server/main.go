package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/enrichment"
	webhttp "github.com/lux032/032music-server/internal/http"
	"github.com/lux032/032music-server/internal/lastfm"
	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
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
	db, err := storage.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(context.Background()); err != nil {
		return err
	}

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

	scannerManager := scanner.New(rootCtx, db, logger, library, cfg.DataDirectory)
	enrichmentManager := enrichment.New(rootCtx, db, logger, cfg.DataDirectory)
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
	// L2: collect custom-image orphans at startup and after every completed
	// scan (merges and cascading deletes leave no other trigger).
	go app.GCCustomImages(rootCtx)
	scannerManager.SetOnComplete(func() {
		enrichmentManager.StartAuto(rootCtx)
		app.GCCustomImages(rootCtx)
	})
	lastFMService := lastfm.NewService(db, logger)
	lastFMService.Start(rootCtx)
	app.SetLastFM(lastFMService)
	if cfg.MediaTokenGenerated && app.CredentialSources().MediaToken == webhttp.CredentialSourceEnv {
		logger.Warn("MUSIC_SERVER_MEDIA_TOKEN is not set: generated a random per-boot media token; media URLs change on every restart — set MUSIC_SERVER_MEDIA_TOKEN to a stable random value or set a media token on the admin security page")
	}
	if _, err := scannerManager.Start(rootCtx, "incremental"); err != nil {
		logger.Warn("automatic startup scan was not started", "error", err)
	}

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
	select {
	case serveErr = <-serverError:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
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
		lastFMService.Wait()
		app.WaitTranscodes()
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
