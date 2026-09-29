package enrichment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrRateLimited marks errors caused by a remote source asking us to back off
// (HTTP 429, or 503 with a Retry-After header, or the Last.fm rate-limit error
// code 29). Run loops stop the current run when they see it instead of
// counting it towards the consecutive-failure circuit breaker.
var ErrRateLimited = errors.New("remote source rate limited")

// RateLimitError describes one rate-limit response from a remote source.
type RateLimitError struct {
	Source     string
	StatusCode int
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s rate limited (HTTP %d), retry after %s", e.Source, e.StatusCode, e.RetryAfter)
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

func asRateLimited(err error) *RateLimitError {
	var rateLimited *RateLimitError
	if errors.As(err, &rateLimited) {
		return rateLimited
	}
	return nil
}

const (
	// defaultRateLimitBackoff applies when Retry-After is missing or invalid.
	defaultRateLimitBackoff = time.Minute
	// maxRateLimitBackoff caps any Retry-After a server sends.
	maxRateLimitBackoff = 10 * time.Minute
)

// parseRetryAfter interprets a Retry-After header value, which is either a
// number of seconds or an HTTP date. Missing or unparsable values fall back
// to defaultRateLimitBackoff; the result is capped at maxRateLimitBackoff.
func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	delay := defaultRateLimitBackoff
	if header != "" {
		if seconds, err := strconv.Atoi(header); err == nil {
			if seconds < 0 {
				seconds = 0
			}
			// Cap before multiplying: seconds*time.Second overflows int64 for
			// huge (but parseable) values.
			if seconds > int(maxRateLimitBackoff/time.Second) {
				return maxRateLimitBackoff
			}
			delay = time.Duration(seconds) * time.Second
		} else if errors.Is(err, strconv.ErrRange) {
			// A number beyond int64 is far beyond the cap.
			return maxRateLimitBackoff
		} else if at, err := http.ParseTime(header); err == nil {
			delay = at.Sub(now)
			if delay < 0 {
				delay = 0
			}
		}
	}
	if delay > maxRateLimitBackoff {
		delay = maxRateLimitBackoff
	}
	return delay
}

// isRateLimitResponse reports whether the response asks the client to back
// off: 429 always does; 503 only when it carries a Retry-After header.
func isRateLimitResponse(status int, header http.Header) bool {
	return status == http.StatusTooManyRequests ||
		(status == http.StatusServiceUnavailable && header.Get("Retry-After") != "")
}

// sleepContext waits d or until ctx is done. It is the default Manager.sleep
// and stays a field so tests can observe waits without really sleeping.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// noteRateLimited pushes back the next allowed request time of a throttled
// source. Only sources with their own throttle (bangumi, musicbrainz) keep a
// blocked-until timestamp; other sources are a no-op here because nothing
// serializes their requests.
func (m *Manager) noteRateLimited(source string, retryAfter time.Duration) {
	until := time.Now().Add(retryAfter)
	switch source {
	case "bangumi":
		m.bangumiMu.Lock()
		if until.After(m.bangumiBlockedUntil) {
			m.bangumiBlockedUntil = until
		}
		m.bangumiMu.Unlock()
	case "musicbrainz":
		m.mbMu.Lock()
		if until.After(m.mbBlockedUntil) {
			m.mbBlockedUntil = until
		}
		m.mbMu.Unlock()
	}
}

// NoteRateLimited records a rate-limit backoff for source. It is exported so
// integration tests outside this package (HTTP handlers) can put a manager
// into the backoff state without a network round trip.
func (m *Manager) NoteRateLimited(source string, retryAfter time.Duration) {
	m.noteRateLimited(source, retryAfter)
}

// SetMusicBrainzBaseURL overrides the MusicBrainz API base URL. 只供测试使用，
// 只能在管理器空闲时调用（没有进行中的匹配/刷新任务，否则会与在读请求产生
// 数据竞争）。
func (m *Manager) SetMusicBrainzBaseURL(base string) { m.musicBrainzBase = base }

// rateLimitedError records the backoff for source and returns the error.
func (m *Manager) rateLimitedError(source string, statusCode int, retryAfter time.Duration) *RateLimitError {
	m.noteRateLimited(source, retryAfter)
	return &RateLimitError{Source: source, StatusCode: statusCode, RetryAfter: retryAfter}
}

// doSourceJSON performs a JSON request for source, translating 429 (and 503
// with Retry-After) into a RateLimitError and pushing back the source's next
// allowed request time.
func (m *Manager) doSourceJSON(client *http.Client, req *http.Request, source string, target any) error {
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if isRateLimitResponse(response.StatusCode, response.Header) {
		retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		return m.rateLimitedError(source, response.StatusCode, retryAfter)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("remote service returned %s", response.Status)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

// sourceDisplayName renders a rate-limited source in Chinese run messages.
func sourceDisplayName(source string) string {
	switch source {
	case "bangumi":
		return "Bangumi"
	case "musicbrainz":
		return "MusicBrainz"
	case "lastfm":
		return "Last.fm"
	case "wikidata":
		return "Wikidata"
	case "wikipedia":
		return "Wikipedia"
	case "spotify":
		return "Spotify"
	case "artist image":
		return "歌手图片"
	case "work poster":
		return "作品海报"
	default:
		return "外部来源"
	}
}

// rateLimitRunMessage is the persisted run message when a run stops because a
// source rate limited it.
func rateLimitRunMessage(err *RateLimitError) string {
	status := err.StatusCode
	if status == 0 {
		status = http.StatusTooManyRequests
	}
	return fmt.Sprintf("%s 限流（%d），已停止本轮，约 %d 分钟后可重试", sourceDisplayName(err.Source), status, rateLimitMinutes(err.RetryAfter))
}

// rateLimitMinutes renders a backoff duration as whole minutes (at least 1).
func rateLimitMinutes(d time.Duration) int {
	minutes := int((d + time.Minute - 1) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return minutes
}

// noticeSourceName renders the source for interactive notices: image
// download labels collapse to "图片源" so messages do not read redundantly
// ("图片/简介因歌手图片限流"). Run messages keep the specific label via
// sourceDisplayName.
func noticeSourceName(source string) string {
	switch source {
	case "artist image", "work poster":
		return "图片源"
	default:
		return sourceDisplayName(source)
	}
}

// RateLimitedSourceName returns the notice-facing Chinese name of the source
// that rate limited the request; ok is false for every other error.
func RateLimitedSourceName(err error) (name string, ok bool) {
	rateLimited := asRateLimited(err)
	if rateLimited == nil {
		return "", false
	}
	return noticeSourceName(rateLimited.Source), true
}

// RateLimitNoticeParts returns the notice-facing Chinese source name and the
// backoff in whole minutes for interactive handler messages.
func RateLimitNoticeParts(err error) (source string, minutes int, ok bool) {
	rateLimited := asRateLimited(err)
	if rateLimited == nil {
		return "", 0, false
	}
	return noticeSourceName(rateLimited.Source), rateLimitMinutes(rateLimited.RetryAfter), true
}

// RateLimitNotice renders a friendly Chinese notice for interactive handlers
// when err is a rate-limit error. ok is false for every other error.
func RateLimitNotice(err error) (notice string, ok bool) {
	source, minutes, ok := RateLimitNoticeParts(err)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s 限流中，约 %d 分钟后再试", source, minutes), true
}

const (
	defaultBangumiInterval = 500 * time.Millisecond
	minBangumiInterval     = 200 * time.Millisecond
	maxBangumiInterval     = 10 * time.Second
)

// parseBangumiIntervalMS parses MUSIC_SERVER_BANGUMI_INTERVAL_MS (milliseconds,
// 200–10000). ok is false for unparsable or out-of-range values.
func parseBangumiIntervalMS(value string) (interval time.Duration, ok bool) {
	ms, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	interval = time.Duration(ms) * time.Millisecond
	if interval < minBangumiInterval || interval > maxBangumiInterval {
		return 0, false
	}
	return interval, true
}

// bangumiIntervalFromEnv reads the Bangumi request interval from
// MUSIC_SERVER_BANGUMI_INTERVAL_MS, falling back to the default (with a
// warning) for missing or invalid values.
func bangumiIntervalFromEnv(logger *slog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv("MUSIC_SERVER_BANGUMI_INTERVAL_MS"))
	if raw == "" {
		return defaultBangumiInterval
	}
	if interval, ok := parseBangumiIntervalMS(raw); ok {
		return interval
	}
	logger.Warn("invalid MUSIC_SERVER_BANGUMI_INTERVAL_MS, using default", "value", raw, "default", defaultBangumiInterval.String())
	return defaultBangumiInterval
}
