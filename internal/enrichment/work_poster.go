package enrichment

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/netfetch"
	"github.com/lux032/032music-server/internal/sourcegate"
)

var posterExtensions = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}

var posterMIMETypesByExt = map[string]string{
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".gif":  "image/gif",
}

type posterCacheEntry struct {
	ext      string
	mimeType string
}

func posterCacheKey(remoteURL string) (string, bool) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", false
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(remoteURL))), true
}

// downloadPublicImage fetches an untrusted remote image URL, refusing
// private/loopback/link-local targets (including across redirects) to prevent
// SSRF, and returns the body with its detected MIME type and file extension.
func (m *Manager) downloadPublicImage(ctx context.Context, remoteURL, label string) ([]byte, string, string, error) {
	if err := m.checkSourceCooldown(label); err != nil {
		return nil, "", "", err
	}
	img, err := netfetch.DownloadImage(ctx, m.client, nil, label, remoteURL, m.metadataUserAgent(ctx))
	var cd *sourcegate.CooldownError
	if errors.As(err, &cd) {
		status := 429
		var imageLimit *netfetch.ImageRateLimitError
		if errors.As(err, &imageLimit) {
			status = imageLimit.StatusCode
		}
		err = m.rateLimitedError(label, status, cd.RetryAfter)
	}
	if err != nil {
		err = fmt.Errorf("%s: %w", label, err)
	}
	return img.Data, img.MIME, img.Ext, err
}
func writeCacheFile(directory, name string, data []byte) (string, error) {
	return netfetch.WriteCacheFile(directory, name, data)
}

func (m *Manager) workPosterDirectory() string {
	return filepath.Join(filepath.Dir(m.imageDirectory), "work-posters")
}

func (m *Manager) refreshPosterCacheIfChanged() {
	dir := m.workPosterDirectory()
	info, statErr := os.Stat(dir)
	m.posterCacheMu.RLock()
	if m.posterCacheLoaded {
		if statErr != nil && os.IsNotExist(statErr) && m.posterCacheDirMod.IsZero() && len(m.posterCache) == 0 {
			m.posterCacheMu.RUnlock()
			return
		}
		if statErr == nil && info.ModTime().Equal(m.posterCacheDirMod) {
			m.posterCacheMu.RUnlock()
			return
		}
	}
	m.posterCacheMu.RUnlock()

	m.posterCacheMu.Lock()
	defer m.posterCacheMu.Unlock()
	dirInfo, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			m.posterCache = map[string]posterCacheEntry{}
			m.posterCacheDirMod = time.Time{}
			m.posterCacheLoaded = true
		}
		return
	}
	if m.posterCacheLoaded && dirInfo.ModTime().Equal(m.posterCacheDirMod) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	next := make(map[string]posterCacheEntry, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		rawExt := filepath.Ext(name)
		ext := strings.ToLower(rawExt)
		mimeType, ok := posterMIMETypesByExt[ext]
		if !ok {
			continue
		}
		stem := strings.TrimSuffix(name, rawExt)
		if stem == "" {
			continue
		}
		next[stem] = posterCacheEntry{ext: rawExt, mimeType: mimeType}
	}
	m.posterCache = next
	m.posterCacheDirMod = dirInfo.ModTime()
	m.posterCacheLoaded = true
}

func (m *Manager) recordCachedWorkPoster(key, extension, mimeType string) {
	ext := strings.ToLower(extension)
	if mimeType == "" {
		mimeType = posterMIMETypesByExt[ext]
	}
	if mimeType == "" {
		return
	}
	dir := m.workPosterDirectory()
	dirInfo, statErr := os.Stat(dir)
	m.posterCacheMu.Lock()
	if m.posterCache == nil {
		m.posterCache = map[string]posterCacheEntry{}
	}
	m.posterCache[key] = posterCacheEntry{ext: extension, mimeType: mimeType}
	if m.posterCacheLoaded && statErr == nil {
		m.posterCacheDirMod = dirInfo.ModTime()
	}
	m.posterCacheMu.Unlock()
}

// InvalidateWorkPoster removes a stale in-memory cache entry (for example when
// opening the cached file returns os.ErrNotExist after external removal).
func (m *Manager) InvalidateWorkPoster(remoteURL string) {
	key, ok := posterCacheKey(remoteURL)
	if !ok {
		return
	}
	m.posterCacheMu.Lock()
	delete(m.posterCache, key)
	m.posterCacheMu.Unlock()
}

// CachedWorkPoster returns the local cached file for a poster URL without any
// network access. It reports os.ErrNotExist when the poster is not cached yet.
func (m *Manager) CachedWorkPoster(remoteURL string) (string, string, error) {
	key, ok := posterCacheKey(remoteURL)
	if !ok {
		return "", "", os.ErrNotExist
	}
	dir := m.workPosterDirectory()
	m.posterCacheMu.RLock()
	entry, hit := m.posterCache[key]
	m.posterCacheMu.RUnlock()
	if hit {
		return filepath.Join(dir, key+entry.ext), entry.mimeType, nil
	}
	// First load or cache miss: validate directory mtime once to pick up
	// externally written poster files without probing every extension.
	m.refreshPosterCacheIfChanged()
	m.posterCacheMu.RLock()
	entry, hit = m.posterCache[key]
	m.posterCacheMu.RUnlock()
	if hit {
		return filepath.Join(dir, key+entry.ext), entry.mimeType, nil
	}
	return "", "", os.ErrNotExist
}

// CacheWorkPoster downloads the work's current poster into the local cache if
// it is not cached yet. Called after a match is confirmed or the poster URL is
// edited, so page views never have to hit Bangumi.
func (m *Manager) CacheWorkPoster(ctx context.Context, workID int64) error {
	work, err := m.store.WorkByID(ctx, workID)
	if err != nil {
		return err
	}
	remoteURL := strings.TrimSpace(work.PosterURL)
	if remoteURL == "" {
		return nil
	}
	if _, _, err = m.CachedWorkPoster(remoteURL); err == nil {
		return nil
	}
	data, mimeType, extension, err := m.downloadPublicImage(ctx, remoteURL, "work poster")
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(remoteURL)))
	_, err = writeCacheFile(m.workPosterDirectory(), key+extension, data)
	if err == nil {
		m.recordCachedWorkPoster(key, extension, mimeType)
	}
	return err
}

// QueueWorkPoster caches a work poster in the background.
func (m *Manager) QueueWorkPoster(workID int64) {
	m.goBackground("work-poster", func() {
		ctx, cancel := context.WithTimeout(m.baseCtx, time.Minute)
		defer cancel()
		if err := m.CacheWorkPoster(ctx, workID); err != nil && m.baseCtx.Err() == nil {
			m.logger.Warn("cache work poster", "workId", workID, "error", err)
		}
	})
}

// PosterBackfillResult 记录上一轮海报补全的结果（内存态，重启后清空）。
type PosterBackfillResult struct {
	Cached      int
	Failed      int
	RateLimited bool
	FinishedAt  time.Time
}

// WorkPosterCacheStats 统计作品海报的本地缓存情况：total 是有海报地址的作品
// 数，cached 是本地已缓存数。只读本地文件，不访问网络。
func (m *Manager) WorkPosterCacheStats(ctx context.Context) (cached, total int, err error) {
	posters, err := m.store.WorkPosterURLs(ctx)
	if err != nil {
		return 0, 0, err
	}
	total = len(posters)
	if total == 0 {
		return 0, 0, nil
	}
	m.refreshPosterCacheIfChanged()
	m.posterCacheMu.RLock()
	for _, item := range posters {
		if key, ok := posterCacheKey(item.URL); ok {
			if _, exists := m.posterCache[key]; exists {
				cached++
			}
		}
	}
	m.posterCacheMu.RUnlock()
	return cached, total, nil
}

// PosterBackfillRunning 报告是否正在补全海报。
func (m *Manager) PosterBackfillRunning() bool {
	m.posterMu.Lock()
	defer m.posterMu.Unlock()
	return m.posterBackfilling
}

// LastPosterBackfill 返回上一轮补全的结果；从未补全过时返回 nil。
func (m *Manager) LastPosterBackfill() *PosterBackfillResult {
	m.posterMu.Lock()
	defer m.posterMu.Unlock()
	if m.posterLastResult == nil {
		return nil
	}
	result := *m.posterLastResult
	return &result
}

// SetPosterBackfillEnabled 开关海报自动补全（启动延迟一次、扫描后、每轮增强
// 结束后）。手动点“补全缺失海报”按钮不受此开关影响。测试与 e2e 关闭它以
// 保证不访问外网。
func (m *Manager) SetPosterBackfillEnabled(enabled bool) {
	m.posterMu.Lock()
	m.posterBackfillEnabled = enabled
	m.posterMu.Unlock()
}

func (m *Manager) posterBackfillAutoEnabled() bool {
	m.posterMu.Lock()
	defer m.posterMu.Unlock()
	return m.posterBackfillEnabled
}

// ScheduleStartupPosterBackfill 在服务启动约 delay 后自动补全一次缺失海报；
// ctx 关闭则不再启动。开关关闭时不排期。
func (m *Manager) ScheduleStartupPosterBackfill(delay time.Duration) {
	if !m.posterBackfillAutoEnabled() {
		return
	}
	m.goBackground("work-poster-backfill-startup", func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-m.baseCtx.Done():
			return
		case <-timer.C:
		}
		m.StartWorkPosterBackfill()
	})
}

// posterFailureTTL 是失败海报 URL 的自动跳过窗口（L3）。
const posterFailureTTL = 6 * time.Hour

// posterRecentlyFailed 报告 URL 是否在 TTL 内失败过（自动补全跳过）。
func (m *Manager) posterRecentlyFailed(remoteURL string) bool {
	m.posterMu.Lock()
	defer m.posterMu.Unlock()
	failedAt, ok := m.posterFailed[remoteURL]
	return ok && time.Since(failedAt) < posterFailureTTL
}

func (m *Manager) notePosterFailure(remoteURL string) {
	m.posterMu.Lock()
	m.posterFailed[remoteURL] = time.Now()
	m.posterMu.Unlock()
}

func (m *Manager) clearPosterFailure(remoteURL string) {
	m.posterMu.Lock()
	delete(m.posterFailed, remoteURL)
	m.posterMu.Unlock()
}

// RetryWorkPosterBackfill 是手动“补全缺失海报”按钮的入口：忽略失败记录，
// 强制重试全部缺失海报。返回值与 StartWorkPosterBackfill 相同。
func (m *Manager) RetryWorkPosterBackfill() bool {
	m.posterMu.Lock()
	m.posterFailed = map[string]time.Time{}
	m.posterMu.Unlock()
	return m.StartWorkPosterBackfill()
}

// StartWorkPosterBackfill caches posters of already-matched works that are
// missing from the local cache. Only one backfill runs at a time; started is
// false when one is already running. URLs that failed within posterFailureTTL
// are skipped (L3); use RetryWorkPosterBackfill to force a retry.
func (m *Manager) StartWorkPosterBackfill() (started bool) {
	m.posterMu.Lock()
	if m.posterBackfilling {
		m.posterMu.Unlock()
		return false
	}
	m.posterBackfilling = true
	m.posterBackfillPending = false
	m.posterMu.Unlock()
	m.launchWorkPosterBackfill()
	return true
}

// requestAutoWorkPosterBackfill is used by automatic triggers (scan completion
// and enrichment run completion). If a backfill pass is already running, it
// marks a follow-up pass so newly added works are scanned before exiting.
func (m *Manager) requestAutoWorkPosterBackfill() (started bool) {
	m.posterMu.Lock()
	if m.posterBackfilling {
		m.posterBackfillPending = true
		m.posterMu.Unlock()
		return false
	}
	m.posterBackfilling = true
	m.posterBackfillPending = false
	m.posterMu.Unlock()
	m.launchWorkPosterBackfill()
	return true
}

func (m *Manager) launchWorkPosterBackfill() {
	m.goBackground("work-poster-backfill", func() {
		result := &PosterBackfillResult{}
		defer func() {
			result.FinishedAt = time.Now()
			m.posterMu.Lock()
			m.posterBackfilling = false
			m.posterBackfillPending = false
			m.posterLastResult = result
			m.posterMu.Unlock()
		}()
		for {
			posters, err := m.store.WorkPosterURLs(m.baseCtx)
			if err != nil {
				m.logger.Warn("list work posters", "error", err)
				return
			}
			if m.testPosterBackfillHook != nil {
				m.testPosterBackfillHook()
			}
			for _, item := range posters {
				if m.baseCtx.Err() != nil {
					return
				}
				if _, _, err = m.CachedWorkPoster(item.URL); err == nil {
					continue
				}
				if m.posterRecentlyFailed(item.URL) {
					continue
				}
				ctx, cancel := context.WithTimeout(m.baseCtx, time.Minute)
				err = m.CacheWorkPoster(ctx, item.WorkID)
				cancel()
				if err != nil {
					if rateLimited := asRateLimited(err); rateLimited != nil {
						// A 429 stops this backfill pass; the next pass retries.
						result.RateLimited = true
						m.logger.Warn("work poster backfill stopped by rate limiting", "workId", item.WorkID, "retryAfter", rateLimited.RetryAfter.String())
						return
					}
					result.Failed++
					m.notePosterFailure(item.URL)
					m.logger.Warn("backfill work poster", "workId", item.WorkID, "error", err)
				} else {
					result.Cached++
					m.clearPosterFailure(item.URL)
				}
				select {
				case <-m.baseCtx.Done():
					return
				case <-time.After(m.bangumiPace):
				}
			}
			m.posterMu.Lock()
			again := m.posterBackfillPending && m.baseCtx.Err() == nil
			m.posterBackfillPending = false
			m.posterMu.Unlock()
			if !again {
				break
			}
		}
		if result.Cached > 0 || result.Failed > 0 {
			m.logger.Info("work poster backfill finished", "cached", result.Cached, "failed", result.Failed)
		}
	})
}
