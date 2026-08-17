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
	scannerManager := scanner.New(db, logger, library, cfg.DataDirectory)
	enrichmentManager := enrichment.New(db, logger, cfg.DataDirectory)
	scannerManager.SetOnComplete(func() { enrichmentManager.StartAuto(context.Background()) })

	app, err := webhttp.NewApp(cfg, db, scannerManager, enrichmentManager, logger, version)
	if err != nil {
		return err
	}
	if _, err := scannerManager.Start(context.Background(), "incremental"); err != nil {
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

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverError := make(chan error, 1)
	go func() {
		logger.Info("server started", "address", cfg.ListenAddress, "version", version)
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-shutdownContext.Done():
		logger.Info("shutdown requested")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return err
	}

	return nil
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
