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

// StartWorkPosterBackfill caches posters of already-matched works that are
// missing from the local cache. Only one backfill runs at a time.
func (m *Manager) StartWorkPosterBackfill() {
	m.posterMu.Lock()
	if m.posterBackfilling {
		m.posterMu.Unlock()
		return
	}
	m.posterBackfilling = true
	m.posterMu.Unlock()
	m.goBackground("work-poster-backfill", func() {
		defer func() {
			m.posterMu.Lock()
			m.posterBackfilling = false
			m.posterMu.Unlock()
		}()
		posters, err := m.store.WorkPosterURLs(m.baseCtx)
		if err != nil {
			m.logger.Warn("list work posters", "error", err)
			return
		}
		cached, failed := 0, 0
		for _, item := range posters {
			if m.baseCtx.Err() != nil {
				return
			}
			if _, _, err = m.CachedWorkPoster(item.URL); err == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(m.baseCtx, time.Minute)
			err = m.CacheWorkPoster(ctx, item.WorkID)
			cancel()
			if err != nil {
				if rateLimited := asRateLimited(err); rateLimited != nil {
					// A 429 stops this backfill pass; the next pass retries.
					m.logger.Warn("work poster backfill stopped by rate limiting", "workId", item.WorkID, "retryAfter", rateLimited.RetryAfter.String())
					return
				}
				failed++
				m.logger.Warn("backfill work poster", "workId", item.WorkID, "error", err)
			} else {
				cached++
			}
			select {
			case <-m.baseCtx.Done():
				return
			case <-time.After(m.bangumiInterval):
			}
		}
		if cached > 0 || failed > 0 {
			m.logger.Info("work poster backfill finished", "cached", cached, "failed", failed)
		}
	})
}
