package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

type Manager struct {
	store            *storage.Store
	logger           *slog.Logger
	library          storage.Library
	artworkDirectory string
	mu               sync.Mutex
	running          bool
	onComplete       func()
}

func (m *Manager) SetOnComplete(callback func()) { m.mu.Lock(); m.onComplete = callback; m.mu.Unlock() }

func New(store *storage.Store, logger *slog.Logger, library storage.Library, dataDirectory string) *Manager {
	return &Manager{store: store, logger: logger, library: library, artworkDirectory: filepath.Join(dataDirectory, "artwork")}
}

func (m *Manager) Start(ctx context.Context, scanType string) (int64, error) {
	if scanType != "full" {
		scanType = "incremental"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return 0, errors.New("a scan is already running")
	}
	jobID, err := m.store.CreateScanJob(ctx, m.library.ID, scanType)
	if err != nil {
		return 0, err
	}
	m.running = true
	go func() {
		defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
		m.run(context.Background(), jobID, scanType)
	}()
	return jobID, nil
}

func (m *Manager) run(ctx context.Context, jobID int64, scanType string) {
	started := storage.ScanTimestamp()
	paths, err := m.discover()
	if err != nil {
		m.fail(ctx, jobID, err)
		return
	}
	if err = m.store.StartScanJob(ctx, jobID, int64(len(paths))); err != nil {
		m.fail(ctx, jobID, err)
		return
	}
	var processed, skipped, failed int64
	for _, path := range paths {
		rel, relErr := filepath.Rel(m.library.RootPath, path)
		if relErr != nil {
			failed++
			continue
		}
		rel = filepath.ToSlash(rel)
		info, statErr := os.Stat(path)
		if statErr != nil {
			failed++
			m.recordError(ctx, jobID, rel, "stat_failed", statErr)
			continue
		}
		unchanged, _ := m.store.AudioFileUnchanged(ctx, m.library.ID, rel, info.Size(), info.ModTime().UnixNano())
		if scanType == "incremental" && unchanged {
			_ = m.store.TouchAudioFile(ctx, m.library.ID, rel)
			skipped++
		} else {
			meta, readErr := metadata.Read(path)
			if readErr != nil {
				failed++
				m.recordError(ctx, jobID, rel, "metadata_failed", readErr)
			} else {
				art, artErr := m.cacheArtwork(path, meta)
				if artErr != nil {
					m.logger.Warn("cache artwork failed", "path", rel, "error", artErr)
				}
				if importErr := m.store.ImportTrack(ctx, storage.ImportInput{LibraryID: m.library.ID, RelativePath: rel, FileSize: info.Size(), ModifiedAtNS: info.ModTime().UnixNano(), Metadata: meta, Artwork: art}); importErr != nil {
					failed++
					m.recordError(ctx, jobID, rel, "import_failed", importErr)
				} else {
					processed++
				}
			}
		}
		_ = m.store.UpdateScanJob(ctx, jobID, processed, skipped, failed, rel)
	}
	missing, err := m.store.MarkMissing(ctx, m.library.ID, started)
	if err != nil {
		m.fail(ctx, jobID, err)
		return
	}
	if err = m.store.CleanupOrphans(ctx); err != nil {
		m.fail(ctx, jobID, err)
		return
	}
	if err = m.store.FinishScanJob(ctx, jobID, "completed", missing, ""); err != nil {
		m.logger.Error("finish scan job", "error", err)
	}
	m.logger.Info("library scan completed", "jobId", jobID, "discovered", len(paths), "processed", processed, "skipped", skipped, "failed", failed, "missing", missing)
	m.mu.Lock()
	callback := m.onComplete
	m.mu.Unlock()
	if callback != nil {
		go callback()
	}
}

func (m *Manager) discover() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(m.library.RootPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != m.library.RootPath && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if metadata.IsSupported(path) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover music files: %w", err)
	}
	return paths, nil
}

func (m *Manager) cacheArtwork(audioPath string, meta metadata.AudioMetadata) (*storage.ArtworkInput, error) {
	data, mimeType, sourceType, sourcePath := meta.Artwork, meta.ArtworkMIME, "embedded", audioPath
	if len(data) == 0 {
		for _, name := range []string{"cover.jpg", "cover.jpeg", "cover.png", "folder.jpg", "folder.png", "front.jpg", "front.png"} {
			candidate := filepath.Join(filepath.Dir(audioPath), name)
			content, err := os.ReadFile(candidate)
			if err == nil {
				data = content
				mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(candidate)))
				sourceType = "folder"
				sourcePath = candidate
				break
			}
		}
	}
	if len(data) == 0 {
		return nil, nil
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])
	ext := extension(mimeType)
	if err := os.MkdirAll(m.artworkDirectory, 0o750); err != nil {
		return nil, err
	}
	cachePath := filepath.Join(m.artworkDirectory, hash+ext)
	if _, err := os.Stat(cachePath); errors.Is(err, os.ErrNotExist) {
		if err = os.WriteFile(cachePath, data, 0o640); err != nil {
			return nil, err
		}
	}
	return &storage.ArtworkInput{Hash: hash, MIMEType: mimeType, CachePath: cachePath, SourceType: sourceType, SourcePath: sourcePath, ByteSize: int64(len(data))}, nil
}

func (m *Manager) recordError(ctx context.Context, jobID int64, relative, code string, err error) {
	m.logger.Warn("scan file failed", "path", relative, "error", err)
	_ = m.store.AddScanError(ctx, jobID, relative, code, err.Error())
}
func (m *Manager) fail(ctx context.Context, jobID int64, err error) {
	m.logger.Error("library scan failed", "jobId", jobID, "error", err)
	_ = m.store.FinishScanJob(ctx, jobID, "failed", 0, err.Error())
}
func extension(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".img"
	}
}
