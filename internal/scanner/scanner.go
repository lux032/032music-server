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
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/lyrics"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// scanProgressFlushInterval bounds how often per-file progress is written to
// the scan_jobs table; one UPDATE per file is far too chatty on large
// libraries.
const scanProgressFlushInterval = time.Second
const scanProgressFlushEvery = 100

// missingGuardRatio is the safety threshold for S1: if a scan would mark more
// than this fraction of previously available files as missing, the library
// root is almost certainly empty/unmounted (forgotten volume, dead SMB/NFS
// mount) and destructive reconciliation is refused.
const missingGuardRatio = 0.5

type Manager struct {
	baseCtx          context.Context
	store            *storage.Store
	logger           *slog.Logger
	library          storage.Library
	artworkDirectory string
	mu               sync.Mutex
	running          bool
	onComplete       func()
	readMetadata     func(string) (metadata.AudioMetadata, error)
	probeAudio       func(string, string) metadata.AudioProps
	updateAudioProbe func(context.Context, int64, string, metadata.AudioProps, bool) error
	wg               sync.WaitGroup
}

func (m *Manager) SetOnComplete(callback func()) { m.mu.Lock(); m.onComplete = callback; m.mu.Unlock() }

// SetMetadataReader overrides the per-file metadata reader. Intended for tests
// in other packages that need to hold a scan in the running state
// deterministically; must be called before Start.
func (m *Manager) SetMetadataReader(reader func(string) (metadata.AudioMetadata, error)) {
	m.mu.Lock()
	m.readMetadata = reader
	m.mu.Unlock()
}

func New(baseCtx context.Context, store *storage.Store, logger *slog.Logger, library storage.Library, dataDirectory string) *Manager {
	return &Manager{baseCtx: baseCtx, store: store, logger: logger, library: library, artworkDirectory: filepath.Join(dataDirectory, "artwork")}
}

var ErrScanRunning = errors.New("a scan is already running")

// RefreshAlbumWorks holds the same mutex and running flag as Start, so scans
// and manual refreshes cannot overwrite each other's associations.
func (m *Manager) RefreshAlbumWorks(ctx context.Context) (storage.RefreshStats, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return storage.RefreshStats{}, ErrScanRunning
	}
	m.running = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
	refreshCtx, cancel := context.WithCancel(m.baseCtx)
	defer cancel()
	return m.store.RefreshAlbumWorks(refreshCtx, true)
}

// Wait blocks until the currently running scan (if any) has finished. Call
// after cancelling the base context during shutdown.
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) Start(ctx context.Context, scanType string) (int64, error) {
	if scanType != "full" {
		scanType = "incremental"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return 0, ErrScanRunning
	}
	jobID, err := m.store.CreateScanJob(ctx, m.library.ID, scanType)
	if err != nil {
		return 0, err
	}
	m.running = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				m.logger.Error("panic in library scan", "jobId", jobID, "panic", recovered, "stack", string(debug.Stack()))
				m.fail(context.Background(), jobID, fmt.Errorf("internal panic: %v", recovered))
			}
		}()
		defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
		m.run(m.baseCtx, jobID, scanType)
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
	// S1 guard: never reconcile against an empty discovery. A scan that found
	// zero files while the library previously had available files means the
	// volume is unmounted or misconfigured — not that the user deleted their
	// entire collection.
	if len(paths) == 0 {
		available, countErr := m.store.CountAvailableAudioFiles(ctx, m.library.ID)
		if countErr != nil {
			m.fail(ctx, jobID, countErr)
			return
		}
		if available > 0 {
			m.fail(ctx, jobID, fmt.Errorf("scan discovered 0 files but the library has %d available files; refusing to mark the library missing (is %s mounted?)", available, m.library.RootPath))
			return
		}
	}
	if err = m.store.StartScanJob(ctx, jobID, int64(len(paths))); err != nil {
		m.fail(ctx, jobID, err)
		return
	}
	var processed, skipped, failed int64
	lastFlush := time.Now()
	sinceFlush := 0
	folderArtwork := map[string]*storage.ArtworkInput{}
	cancelled := false
	for _, path := range paths {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
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
			lrc := externalLRC(path)
			probeFailed := false
			needed, e := m.store.AudioProbeNeeded(ctx, m.library.ID, rel)
			if e != nil {
				probeFailed = true
				m.recordError(ctx, jobID, rel, "probe_check_failed", e)
			} else if needed {
				update := m.updateAudioProbe
				if update == nil {
					update = m.store.UpdateAudioProbe
				}
				if e = update(ctx, m.library.ID, rel, m.probe(path, strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")), lrc); e != nil {
					probeFailed = true
					m.recordError(ctx, jobID, rel, "probe_failed", e)
				}
			}
			// Even a failed probe must touch the file to prevent MarkMissing from hiding it.
			if e = m.store.TouchAudioFile(ctx, m.library.ID, rel, lrc); e != nil {
				probeFailed = true
				m.recordError(ctx, jobID, rel, "touch_failed", e)
			}
			if probeFailed {
				failed++
			} else {
				skipped++
			}
		} else {
			readMetadata := m.readMetadata
			if readMetadata == nil {
				readMetadata = metadata.Read
			}
			meta, readErr := readMetadata(path)
			if readErr != nil {
				failed++
				m.recordError(ctx, jobID, rel, "metadata_failed", readErr)
			} else {
				art, artErr := m.cacheArtwork(path, meta, folderArtwork)
				if artErr != nil {
					m.logger.Warn("cache artwork failed", "path", rel, "error", artErr)
				}
				if importErr := m.store.ImportTrack(ctx, storage.ImportInput{LibraryID: m.library.ID, RelativePath: rel, FileSize: info.Size(), ModifiedAtNS: info.ModTime().UnixNano(), Metadata: meta, Artwork: art, AudioProps: m.probe(path, meta.Container), HasExternalLRC: externalLRC(path)}); importErr != nil {
					failed++
					m.recordError(ctx, jobID, rel, "import_failed", importErr)
				} else {
					processed++
				}
			}
		}
		sinceFlush++
		if sinceFlush >= scanProgressFlushEvery || time.Since(lastFlush) >= scanProgressFlushInterval {
			_ = m.store.UpdateScanJob(ctx, jobID, processed, skipped, failed, rel)
			sinceFlush = 0
			lastFlush = time.Now()
		}
	}
	_ = m.store.UpdateScanJob(ctx, jobID, processed, skipped, failed, "")
	if cancelled {
		// A partial scan must never feed MarkMissing/CleanupOrphans: every
		// file not yet visited would be wrongly marked missing.
		m.fail(context.Background(), jobID, errors.New("scan cancelled by shutdown"))
		return
	}

	// S1 guard: refuse destructive reconciliation when an implausible share
	// of the library would go missing (mount dropped mid-scan, path changed).
	available, err := m.store.CountAvailableAudioFiles(ctx, m.library.ID)
	if err != nil {
		m.fail(ctx, jobID, err)
		return
	}
	if available > 0 && float64(len(paths)) < float64(available)*(1-missingGuardRatio) {
		m.fail(ctx, jobID, fmt.Errorf("scan discovered %d files but the library has %d available files (>%d%% would be marked missing); refusing reconciliation (is %s fully mounted?)", len(paths), available, int(missingGuardRatio*100), m.library.RootPath))
		return
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
	if _, refreshErr := m.store.RefreshAlbumWorks(ctx, false); refreshErr != nil {
		m.logger.Error("refresh album works", "error", refreshErr)
	}
	if err = m.store.FinishScanJob(ctx, jobID, "completed", missing, ""); err != nil {
		m.logger.Error("finish scan job", "error", err)
	}
	m.logger.Info("library scan completed", "jobId", jobID, "discovered", len(paths), "processed", processed, "skipped", skipped, "failed", failed, "missing", missing)
	m.mu.Lock()
	callback := m.onComplete
	m.mu.Unlock()
	if callback != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					m.logger.Error("panic in scan completion callback", "panic", recovered, "stack", string(debug.Stack()))
				}
			}()
			callback()
		}()
	}
}

func (m *Manager) discover() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(m.library.RootPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// A single unreadable entry (permissions, dangling mount) must
			// not abort the whole scan; skip it and keep walking.
			m.logger.Warn("scan skipped unreadable path", "path", path, "error", err)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
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

// cacheArtwork resolves the artwork for one audio file. folderArtwork caches
// folder-level covers (cover.jpg etc.) per directory so an album folder with
// N tracks reads and hashes its cover once instead of N times. Entries are
// only cached for successful folder lookups; embedded artwork is always
// per-file.
func (m *Manager) cacheArtwork(audioPath string, meta metadata.AudioMetadata, folderArtwork map[string]*storage.ArtworkInput) (*storage.ArtworkInput, error) {
	data, mimeType, sourceType, sourcePath := meta.Artwork, meta.ArtworkMIME, "embedded", audioPath
	if len(data) == 0 {
		directory := filepath.Dir(audioPath)
		if cached, ok := folderArtwork[directory]; ok {
			if cached == nil {
				return nil, nil
			}
			return cached, nil
		}
		for _, name := range []string{"cover.jpg", "cover.jpeg", "cover.png", "folder.jpg", "folder.png", "front.jpg", "front.png"} {
			candidate := filepath.Join(directory, name)
			content, err := os.ReadFile(candidate)
			if err == nil {
				data = content
				mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(candidate)))
				sourceType = "folder"
				sourcePath = candidate
				break
			}
		}
		if len(data) == 0 {
			folderArtwork[directory] = nil
			return nil, nil
		}
		artwork, err := m.writeArtwork(data, mimeType, sourceType, sourcePath)
		if err != nil {
			return nil, err
		}
		folderArtwork[directory] = artwork
		return artwork, nil
	}
	return m.writeArtwork(data, mimeType, sourceType, sourcePath)
}

func (m *Manager) writeArtwork(data []byte, mimeType, sourceType, sourcePath string) (*storage.ArtworkInput, error) {
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

func externalLRC(path string) bool {
	info, err := os.Stat(lyrics.DetectLRCPath(path))
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	// BOM-only files have no usable lyrics. Checking at most three bytes is enough.
	f, err := os.Open(lyrics.DetectLRCPath(path))
	if err != nil {
		return false
	}
	defer f.Close()
	var prefix [3]byte
	n, _ := f.Read(prefix[:])
	if info.Size() <= 3 && n == 3 && prefix == [3]byte{0xef, 0xbb, 0xbf} {
		return false
	}
	return !(info.Size() == 2 && n >= 2 && ((prefix[0] == 0xff && prefix[1] == 0xfe) || (prefix[0] == 0xfe && prefix[1] == 0xff)))
}

func (m *Manager) probe(path, container string) metadata.AudioProps {
	if m.probeAudio != nil {
		return m.probeAudio(path, container)
	}
	return metadata.ProbeAudio(path, container)
}
