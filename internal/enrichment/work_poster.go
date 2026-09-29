package enrichment

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var posterExtensions = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}

// downloadPublicImage fetches an untrusted remote image URL, refusing
// private/loopback/link-local targets (including across redirects) to prevent
// SSRF, and returns the body with its detected MIME type and file extension.
func (m *Manager) downloadPublicImage(ctx context.Context, remoteURL, label string) ([]byte, string, string, error) {
	if err := validatePublicImageURL(remoteURL); err != nil {
		return nil, "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	client := *m.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return validatePublicImageURL(req.URL.String())
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", "", err
	}
	defer response.Body.Close()
	if isRateLimitResponse(response.StatusCode, response.Header) {
		retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		return nil, "", "", m.rateLimitedError(label, response.StatusCode, retryAfter)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("%s returned %s", label, response.Status)
	}
	const maxImageBytes = 10 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return nil, "", "", fmt.Errorf("%s is empty or exceeds 10 MiB", label)
	}
	mimeType := http.DetectContentType(data)
	extension, ok := posterExtensions[mimeType]
	if !ok {
		return nil, "", "", fmt.Errorf("unsupported %s type %s", label, mimeType)
	}
	return data, mimeType, extension, nil
}

// writeCacheFile atomically stores data at directory/name unless it exists.
func writeCacheFile(directory, name string, data []byte) (string, error) {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	cachePath := filepath.Join(directory, name)
	if _, statErr := os.Stat(cachePath); statErr == nil {
		return cachePath, nil
	}
	temporary, err := os.CreateTemp(directory, "download-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Chmod(0o640)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return cachePath, os.Rename(temporaryPath, cachePath)
}

func (m *Manager) workPosterDirectory() string {
	return filepath.Join(filepath.Dir(m.imageDirectory), "work-posters")
}

// CachedWorkPoster returns the local cached file for a poster URL without any
// network access. It reports os.ErrNotExist when the poster is not cached yet.
func (m *Manager) CachedWorkPoster(remoteURL string) (string, string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", "", os.ErrNotExist
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(remoteURL)))
	for mimeType, extension := range posterExtensions {
		path := filepath.Join(m.workPosterDirectory(), key+extension)
		if _, err := os.Stat(path); err == nil {
			return path, mimeType, nil
		}
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
	data, _, extension, err := m.downloadPublicImage(ctx, remoteURL, "work poster")
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(remoteURL)))
	_, err = writeCacheFile(m.workPosterDirectory(), key+extension, data)
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
	for _, item := range posters {
		if _, _, statErr := m.CachedWorkPoster(item.URL); statErr == nil {
			cached++
		}
	}
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
	m.posterMu.Unlock()
	m.goBackground("work-poster-backfill", func() {
		result := &PosterBackfillResult{}
		defer func() {
			result.FinishedAt = time.Now()
			m.posterMu.Lock()
			m.posterBackfilling = false
			m.posterLastResult = result
			m.posterMu.Unlock()
		}()
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
			case <-time.After(m.bangumiInterval):
			}
		}
		if result.Cached > 0 || result.Failed > 0 {
			m.logger.Info("work poster backfill finished", "cached", result.Cached, "failed", result.Failed)
		}
	})
	return true
}
