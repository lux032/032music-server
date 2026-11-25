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
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/lyrics"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// scanProgressFlushInterval bounds how often per-file progress is written to
// the scan_jobs table; one UPDATE per file is far too chatty on large
// libraries.
const (
	scanProgressFlushInterval = time.Second
	scanProgressFlushEvery    = 100
	scanBatchSize             = 50
	scanBatchFlushInterval    = 250 * time.Millisecond
	scanMinWorkers            = 2
	scanMaxWorkers            = 4
)

// missingGuardRatio is the safety threshold for S1: if a scan would mark more
// than this fraction of previously available files as missing, the library
// root is almost certainly empty/unmounted (forgotten volume, dead SMB/NFS
// mount) and destructive reconciliation is refused.
const missingGuardRatio = 0.5

type Manager struct {
	baseCtx               context.Context
	store                 *storage.Store
	logger                *slog.Logger
	library               storage.Library
	artworkDirectory      string
	workers               int
	mu                    sync.Mutex
	running               bool
	onComplete            func()
	readMetadata          func(string) (metadata.AudioMetadata, error)
	probeAudio            func(string, string) metadata.AudioProps
	updateAudioProbe      func(context.Context, int64, string, metadata.AudioProps, bool) error
	purgePolicy           func(context.Context) string
	restoreScanProtection string
	wg                    sync.WaitGroup
}

func (m *Manager) workerCount() int {
	m.mu.Lock()
	w := m.workers
	m.mu.Unlock()
	if w > 0 {
		return w
	}
	n := runtime.NumCPU()
	if n < scanMinWorkers {
		return scanMinWorkers
	}
	if n > scanMaxWorkers {
		return scanMaxWorkers
	}
	return n
}

// SetPurgeMissingPolicy 设置缺失文件清理策略的来源（管理页覆盖优先，否则取
// MUSIC_SERVER_PURGE_MISSING）。每次扫描结束时读取一次，因此运行期修改对下
// 一次扫描立即生效。未设置时等同 never。
func (m *Manager) SetPurgeMissingPolicy(policy func(context.Context) string) {
	m.mu.Lock()
	m.purgePolicy = policy
	m.mu.Unlock()
}

// ProtectRestoreScan 直到一次完整扫描抵达清理阶段前保持恢复保护。
func (m *Manager) ProtectRestoreScan(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restoreScanProtection = path
}

// shouldPurge 判断本次扫描结束后是否永久删除缺失条目。
func (m *Manager) shouldPurge(ctx context.Context, scanType string) bool {
	m.mu.Lock()
	policy := m.purgePolicy
	protection := m.restoreScanProtection
	m.mu.Unlock()
	if protection != "" {
		if _, err := os.Stat(protection); err == nil {
			if err = os.Remove(protection); err != nil {
				m.logger.Warn("restore scan protection removal failed", "error", err)
			}
			return false
		} else if !os.IsNotExist(err) {
			return false
		}
	}
	if policy == nil {
		return false
	}
	switch policy(ctx) {
	case config.PurgeMissingAlways:
		return true
	case config.PurgeMissingFull:
		return scanType == "full"
	default:
		return false
	}
}

func (m *Manager) SetOnComplete(callback func()) { m.mu.Lock(); m.onComplete = callback; m.mu.Unlock() }

// Running reports whether a scan or album-work refresh is in progress; background
// jobs that read audio files (e.g. acoustic analysis) yield while it is true.
func (m *Manager) Running() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.running }

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

func (m *Manager) StartIdentityRebuild(ctx context.Context) (int64, error) {
	return m.start(ctx, "full", true, false)
}

func (m *Manager) Start(ctx context.Context, scanType string) (int64, error) {
	return m.start(ctx, scanType, false, false)
}

// StartLegacyCreatorConversion is an explicit one-shot compatibility scan.
func (m *Manager) StartLegacyCreatorConversion(ctx context.Context) (int64, error) {
	return m.start(ctx, "full", false, true)
}

func (m *Manager) start(ctx context.Context, scanType string, identity, legacyCreators bool) (int64, error) {
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
	if identity {
		if err = m.store.BeginReleaseGroupRebuild(ctx, jobID); err != nil {
			return 0, err
		}
	}
	m.running = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				m.logger.Error("panic in library scan", "jobId", jobID, "panic", recovered, "stack", string(debug.Stack()))
				m.fail(context.Background(), jobID, fmt.Errorf("internal panic: %v", recovered))
				_ = m.store.CheckReleaseGroupRebuildFailure(context.Background(), jobID)
			}
		}()
		released := false
		defer func() {
			if !released {
				m.mu.Lock()
				m.running = false
				m.mu.Unlock()
			}
		}()
		completed := m.run(m.baseCtx, jobID, scanType, legacyCreators)
		_ = m.store.CheckReleaseGroupRebuildFailure(m.baseCtx, jobID)
		m.mu.Lock()
		m.running = false
		released = true
		callback := m.onComplete
		m.mu.Unlock()
		if completed && callback != nil {
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
	}()
	return jobID, nil
}

type discoveredFile struct {
	path      string
	rel       string
	size      int64
	modTimeNS int64
	statErr   error
}

type discoveredLibrary struct {
	files    []discoveredFile
	lrcPaths map[string]struct{}
}

type preparedImport struct {
	index   int
	rel     string
	input   storage.ImportInput
	errCode string
	err     error
}

type scanArtworkCache struct {
	mu          sync.Mutex
	folder      map[string]*storage.ArtworkInput
	knownHashes map[string]string
	dirReady    bool
}

func newScanArtworkCache() *scanArtworkCache {
	return &scanArtworkCache{
		folder:      make(map[string]*storage.ArtworkInput),
		knownHashes: make(map[string]string),
	}
}

func (m *Manager) run(ctx context.Context, jobID int64, scanType string, legacyCreators bool) bool {
	discovered, err := m.discover()
	if err != nil {
		m.fail(ctx, jobID, err)
		return false
	}
	// S1 guard: never reconcile against an empty discovery. A scan that found
	// zero files while the library previously had available files means the
	// volume is unmounted or misconfigured — not that the user deleted their
	// entire collection.
	if len(discovered.files) == 0 {
		available, countErr := m.store.CountAvailableAudioFiles(ctx, m.library.ID)
		if countErr != nil {
			m.fail(ctx, jobID, countErr)
			return false
		}
		if available > 0 {
			m.fail(ctx, jobID, fmt.Errorf("scan discovered 0 files but the library has %d available files; refusing to mark the library missing (is %s mounted?)", available, m.library.RootPath))
			return false
		}
	}
	if err = m.store.StartScanJob(ctx, jobID, int64(len(discovered.files))); err != nil {
		m.fail(ctx, jobID, err)
		return false
	}
	snapshot, err := m.store.LoadScanFileSnapshot(ctx, m.library.ID)
	if err != nil {
		m.fail(ctx, jobID, err)
		return false
	}

	var processed, skipped, failed int64
	lastFlush := time.Now()
	sinceFlush := 0
	flushProgress := func(current string) {
		sinceFlush++
		if sinceFlush >= scanProgressFlushEvery || time.Since(lastFlush) >= scanProgressFlushInterval {
			_ = m.store.UpdateScanJob(ctx, jobID, processed, skipped, failed, current)
			sinceFlush = 0
			lastFlush = time.Now()
		}
	}

	seenRel := make(map[string]struct{}, len(discovered.files))
	toImport := make([]discoveredFile, 0)
	lrcUpdates := make([]storage.ExternalLRCUpdate, 0)
	cancelled := false

	for _, df := range discovered.files {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		if df.statErr != nil {
			failed++
			if df.rel != "" {
				m.recordError(ctx, jobID, df.rel, "stat_failed", df.statErr)
			}
			flushProgress(df.rel)
			continue
		}
		snap, exists := snapshot[df.rel]
		unchanged := scanType == "incremental" && exists && snap.Status == "available" && snap.HasDuration && snap.FileSize == df.size && snap.ModifiedAtNS == df.modTimeNS
		if unchanged {
			// Unchanged available files always remain seen (even if probe
			// backfill fails) so MarkMissingByIDs never hides them.
			seenRel[df.rel] = struct{}{}
			lrc := externalLRCIfPresent(df.path, discovered.lrcPaths)
			if snap.AudioProbeVersion < storage.AudioProbeVersion {
				update := m.updateAudioProbe
				if update == nil {
					update = m.store.UpdateAudioProbe
				}
				if e := update(ctx, m.library.ID, df.rel, m.probe(df.path, strings.TrimPrefix(strings.ToLower(filepath.Ext(df.path)), ".")), lrc); e != nil {
					failed++
					m.recordError(ctx, jobID, df.rel, "probe_failed", e)
					if lrc != snap.HasExternalLRC {
						lrcUpdates = append(lrcUpdates, storage.ExternalLRCUpdate{AudioFileID: snap.AudioFileID, HasExternalLRC: lrc})
					}
				} else {
					skipped++
				}
			} else {
				if lrc != snap.HasExternalLRC {
					lrcUpdates = append(lrcUpdates, storage.ExternalLRCUpdate{AudioFileID: snap.AudioFileID, HasExternalLRC: lrc})
				}
				skipped++
			}
			flushProgress(df.rel)
			continue
		}
		toImport = append(toImport, df)
	}

	if !cancelled && len(lrcUpdates) > 0 {
		if err = m.store.BatchUpdateExternalLRC(ctx, lrcUpdates); err != nil {
			if ctx.Err() != nil {
				cancelled = true
			} else {
				m.logger.Warn("batch update external lrc failed", "count", len(lrcUpdates), "error", err)
			}
		}
	}

	if !cancelled && len(toImport) > 0 {
		pipeCancelled := m.runImportPipeline(ctx, jobID, toImport, discovered.lrcPaths, legacyCreators, seenRel, &processed, &failed, flushProgress)
		if pipeCancelled {
			cancelled = true
		}
	}

	_ = m.store.UpdateScanJob(ctx, jobID, processed, skipped, failed, "")
	if cancelled {
		// A partial scan must never feed MarkMissing/CleanupOrphans: every
		// file not yet visited would be wrongly marked missing.
		m.fail(context.Background(), jobID, errors.New("scan cancelled by shutdown"))
		return false
	}

	// S1 guard: refuse destructive reconciliation when an implausible share
	// of the library would go missing (mount dropped mid-scan, path changed).
	available, err := m.store.CountAvailableAudioFiles(ctx, m.library.ID)
	if err != nil {
		m.fail(ctx, jobID, err)
		return false
	}
	if available > 0 && float64(len(discovered.files)) < float64(available)*(1-missingGuardRatio) {
		m.fail(ctx, jobID, fmt.Errorf("scan discovered %d files but the library has %d available files (>%d%% would be marked missing); refusing reconciliation (is %s fully mounted?)", len(discovered.files), available, int(missingGuardRatio*100), m.library.RootPath))
		return false
	}

	var missingIDs []int64
	for rel, snap := range snapshot {
		if snap.Status == "available" {
			if _, ok := seenRel[rel]; !ok {
				missingIDs = append(missingIDs, snap.AudioFileID)
			}
		}
	}
	missing, err := m.store.MarkMissingByIDs(ctx, m.library.ID, missingIDs)
	if err != nil {
		m.fail(ctx, jobID, err)
		return false
	}
	// 只有通过上面的 S1 保护（未取消、未疑似掉盘）后才会走到这里，因此
	// 永久删除不会在挂载失效时把整个曲库清空。
	var purgedFiles int64
	if m.shouldPurge(ctx, scanType) {
		purged, purgeErr := m.store.PurgeMissing(ctx, m.library.ID, nil)
		if purgeErr != nil {
			m.logger.Error("purge missing files", "jobId", jobID, "error", purgeErr)
		} else if purged.Files > 0 {
			purgedFiles = purged.Files
			m.logger.Info("purged missing files", "jobId", jobID, "files", purged.Files, "tracks", purged.Tracks, "albums", purged.Albums, "artists", purged.Artists)
		}
	}
	if processed > 0 || missing > 0 || purgedFiles > 0 {
		if err = m.store.CleanupOrphans(ctx); err != nil {
			m.fail(ctx, jobID, err)
			return false
		}
	}
	if _, refreshErr := m.store.RefreshAlbumWorks(ctx, false); refreshErr != nil {
		m.logger.Error("refresh album works", "error", refreshErr)
	}
	if err = m.store.FinishScanJob(ctx, jobID, "completed", missing, ""); err != nil {
		m.logger.Error("finish scan job", "error", err)
	}
	m.logger.Info("library scan completed", "jobId", jobID, "discovered", len(discovered.files), "processed", processed, "skipped", skipped, "failed", failed, "missing", missing)
	return true
}

func (m *Manager) runImportPipeline(
	ctx context.Context,
	jobID int64,
	toImport []discoveredFile,
	lrcPaths map[string]struct{},
	legacyCreators bool,
	seenRel map[string]struct{},
	processed, failed *int64,
	flushProgress func(current string),
) bool {
	readMetadata := m.readMetadata
	if readMetadata == nil {
		readMetadata = metadata.Read
	}
	workers := m.workerCount()
	if workers > len(toImport) {
		workers = len(toImport)
	}
	if workers < 1 {
		workers = 1
	}

	type importJob struct {
		index int
		file  discoveredFile
	}

	pipeCtx, pipeCancel := context.WithCancel(ctx)
	defer pipeCancel()

	artCache := newScanArtworkCache()
	jobs := make(chan importJob)
	results := make(chan preparedImport, workers*4)

	var workerWG sync.WaitGroup
	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for job := range jobs {
				if pipeCtx.Err() != nil {
					return
				}
				meta, readErr := readMetadata(job.file.path)
				var res preparedImport
				if readErr != nil {
					res = preparedImport{index: job.index, rel: job.file.rel, errCode: "metadata_failed", err: readErr}
				} else {
					art, artErr := m.cacheArtworkConcurrent(job.file.path, meta, artCache)
					if artErr != nil {
						m.logger.Warn("cache artwork failed", "path", job.file.rel, "error", artErr)
					}
					res = preparedImport{
						index: job.index,
						rel:   job.file.rel,
						input: storage.ImportInput{
							LibraryID:               m.library.ID,
							RelativePath:            job.file.rel,
							FileSize:                job.file.size,
							ModifiedAtNS:            job.file.modTimeNS,
							Metadata:                meta,
							Artwork:                 art,
							AudioProps:              m.probe(job.file.path, meta.Container),
							HasExternalLRC:          externalLRCIfPresent(job.file.path, lrcPaths),
							LegacyCreatorConversion: legacyCreators,
						},
					}
				}
				select {
				case results <- res:
				case <-pipeCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for idx, df := range toImport {
			select {
			case jobs <- importJob{index: idx, file: df}:
			case <-pipeCtx.Done():
				return
			}
		}
	}()

	go func() {
		workerWG.Wait()
		close(results)
	}()

	batch := make([]preparedImport, 0, scanBatchSize)
	cancelled := false
	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		inputs := make([]storage.ImportInput, len(batch))
		for i := range batch {
			inputs[i] = batch[i].input
		}
		errs, txErr := m.store.ImportTrackBatch(ctx, inputs)
		if txErr != nil {
			if ctx.Err() != nil {
				cancelled = true
				pipeCancel()
				batch = batch[:0]
				return
			}
			for _, item := range batch {
				*failed++
				m.recordError(ctx, jobID, item.rel, "import_failed", txErr)
				flushProgress(item.rel)
			}
			batch = batch[:0]
			return
		}
		for i, item := range batch {
			if errs[i] != nil {
				*failed++
				m.recordError(ctx, jobID, item.rel, "import_failed", errs[i])
			} else {
				*processed++
				seenRel[item.rel] = struct{}{}
			}
			flushProgress(item.rel)
		}
		batch = batch[:0]
	}

	// Preserve deterministic discovery order when committing batches so
	// album-level tag precedence matches serial scanning exactly.
	pending := make(map[int]preparedImport, workers*4)
	nextIdx := 0
	ticker := time.NewTicker(scanBatchFlushInterval)
	defer ticker.Stop()

	drainOrdered := func() {
		for {
			item, ok := pending[nextIdx]
			if !ok {
				return
			}
			delete(pending, nextIdx)
			nextIdx++
			if item.err != nil {
				flushBatch()
				if cancelled {
					return
				}
				*failed++
				m.recordError(ctx, jobID, item.rel, item.errCode, item.err)
				flushProgress(item.rel)
				continue
			}
			batch = append(batch, item)
			if len(batch) >= scanBatchSize {
				flushBatch()
				if cancelled {
					return
				}
			}
		}
	}

loop:
	for {
		select {
		case <-ctx.Done():
			cancelled = true
			pipeCancel()
			break loop
		case <-ticker.C:
			flushBatch()
			if cancelled {
				break loop
			}
		case res, ok := <-results:
			if !ok {
				break loop
			}
			pending[res.index] = res
			drainOrdered()
			if cancelled {
				break loop
			}
		}
	}

	if cancelled {
		for range results {
		}
		workerWG.Wait()
		return true
	}
	drainOrdered()
	flushBatch()
	workerWG.Wait()
	return cancelled || ctx.Err() != nil
}

func (m *Manager) discover() (discoveredLibrary, error) {
	result := discoveredLibrary{
		lrcPaths: make(map[string]struct{}),
	}
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
		if strings.EqualFold(filepath.Ext(path), ".lrc") {
			result.lrcPaths[strings.ToLower(path)] = struct{}{}
			return nil
		}
		if metadata.IsSupported(path) {
			rel, relErr := filepath.Rel(m.library.RootPath, path)
			if relErr != nil {
				result.files = append(result.files, discoveredFile{path: path, statErr: relErr})
				return nil
			}
			rel = filepath.ToSlash(rel)
			info, infoErr := entry.Info()
			if infoErr != nil {
				result.files = append(result.files, discoveredFile{path: path, rel: rel, statErr: infoErr})
				return nil
			}
			result.files = append(result.files, discoveredFile{
				path:      path,
				rel:       rel,
				size:      info.Size(),
				modTimeNS: info.ModTime().UnixNano(),
			})
		}
		return nil
	})
	if err != nil {
		return discoveredLibrary{}, fmt.Errorf("discover music files: %w", err)
	}
	return result, nil
}

// cacheArtworkConcurrent resolves artwork safely across concurrent scanner
// workers, deduplicating folder-cover reads per directory and artwork cache
// disk writes by content hash.
func (m *Manager) cacheArtworkConcurrent(audioPath string, meta metadata.AudioMetadata, cache *scanArtworkCache) (*storage.ArtworkInput, error) {
	data, mimeType, sourceType, sourcePath := meta.Artwork, meta.ArtworkMIME, "embedded", audioPath
	if len(data) == 0 {
		directory := filepath.Dir(audioPath)
		cache.mu.Lock()
		cached, ok := cache.folder[directory]
		cache.mu.Unlock()
		if ok {
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
			cache.mu.Lock()
			cache.folder[directory] = nil
			cache.mu.Unlock()
			return nil, nil
		}
		artwork, err := m.writeArtworkCached(data, mimeType, sourceType, sourcePath, cache)
		if err != nil {
			return nil, err
		}
		cache.mu.Lock()
		cache.folder[directory] = artwork
		cache.mu.Unlock()
		return artwork, nil
	}
	return m.writeArtworkCached(data, mimeType, sourceType, sourcePath, cache)
}

func (m *Manager) writeArtworkCached(data []byte, mimeType, sourceType, sourcePath string, cache *scanArtworkCache) (*storage.ArtworkInput, error) {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])
	ext := extension(mimeType)
	key := hash + ext

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cachePath, ok := cache.knownHashes[key]; ok {
		return &storage.ArtworkInput{Hash: hash, MIMEType: mimeType, CachePath: cachePath, SourceType: sourceType, SourcePath: sourcePath, ByteSize: int64(len(data))}, nil
	}
	if !cache.dirReady {
		if err := os.MkdirAll(m.artworkDirectory, 0o750); err != nil {
			return nil, err
		}
		cache.dirReady = true
	}
	cachePath := filepath.Join(m.artworkDirectory, key)
	if _, err := os.Stat(cachePath); errors.Is(err, os.ErrNotExist) {
		tmp, err := os.CreateTemp(m.artworkDirectory, ".artwork-*")
		if err != nil {
			return nil, err
		}
		tmpName := tmp.Name()
		if _, err = tmp.Write(data); err != nil {
			tmp.Close()
			_ = os.Remove(tmpName)
			return nil, err
		}
		if err = tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return nil, err
		}
		if err = os.Chmod(tmpName, 0o640); err != nil {
			_ = os.Remove(tmpName)
			return nil, err
		}
		if err = os.Rename(tmpName, cachePath); err != nil {
			_ = os.Remove(tmpName)
			return nil, err
		}
	}
	cache.knownHashes[key] = cachePath
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

func externalLRCIfPresent(audioPath string, lrcPaths map[string]struct{}) bool {
	if len(lrcPaths) == 0 {
		return false
	}
	if _, ok := lrcPaths[strings.ToLower(lyrics.DetectLRCPath(audioPath))]; !ok {
		return false
	}
	return externalLRC(audioPath)
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
