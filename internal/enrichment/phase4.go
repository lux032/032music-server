package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// RunRequest describes one Phase 4 metadata enrichment run.
type RunRequest struct {
	Scope    string `json:"scope"`
	TargetID int64  `json:"targetId"`
	Force    bool   `json:"force"`
}

type phase4Endpoints struct{ Bangumi, BangumiAPI string }

func defaultPhase4Endpoints() phase4Endpoints {
	return phase4Endpoints{Bangumi: "https://api.bgm.tv/v0/search/subjects", BangumiAPI: "https://api.bgm.tv"}
}

// maxConsecutiveFailures aborts a run once a remote source is clearly down,
// instead of hammering it for every remaining item.
const maxConsecutiveFailures = 5

func normalizeRunRequest(value RunRequest) (RunRequest, error) {
	value.Scope = strings.ToLower(strings.TrimSpace(value.Scope))
	if value.Scope == "" {
		value.Scope = "all"
	}
	switch value.Scope {
	case "all", "albums", "tracks", "works":
		value.TargetID = 0
	case "work", "album":
		if value.TargetID <= 0 {
			return value, fmt.Errorf("target id is required for %s scope", value.Scope)
		}
	default:
		return value, fmt.Errorf("unsupported enrichment scope %q", value.Scope)
	}
	return value, nil
}

// StartRun creates an observable background run. Only one Phase 4 run may be
// active in a process. Item collection happens in the background so the HTTP
// request returns immediately.
func (m *Manager) StartRun(ctx context.Context, request RunRequest) (storage.EnrichmentRun, error) {
	request, err := normalizeRunRequest(request)
	if err != nil {
		return storage.EnrichmentRun{}, err
	}
	m.phaseMu.Lock()
	defer m.phaseMu.Unlock()
	if m.phaseRunning {
		return storage.EnrichmentRun{}, errors.New("metadata enrichment is already running")
	}
	run, err := m.store.CreateEnrichmentRun(ctx, request.Scope, request.TargetID, request.Force, 0)
	if err != nil {
		return storage.EnrichmentRun{}, err
	}
	runCtx, cancel := context.WithCancel(m.baseCtx)
	m.phaseRunning, m.phaseRunID, m.phaseCancel = true, run.ID, cancel
	m.goBackground("metadata-enrichment", func() { m.executePhase4Run(runCtx, run.ID, request) })
	return run, nil
}

func (m *Manager) CancelRun(runID int64) error {
	m.phaseMu.Lock()
	defer m.phaseMu.Unlock()
	if !m.phaseRunning || m.phaseRunID != runID || m.phaseCancel == nil {
		return ErrRunNotActive
	}
	run, err := m.store.EnrichmentRun(context.Background(), runID)
	if err != nil || run.Status != "running" {
		return ErrRunNotActive
	}
	m.phaseCancel()
	return nil
}

type phase4Item struct {
	work  storage.WorkEnrichmentTarget
	album storage.AlbumBangumiTarget
	track storage.TrackBangumiTarget
}

// worksLinkedToAlbum keeps only works already associated with one album, so an
// album-scoped run does not search the whole library's works.
func worksLinkedToAlbum(ctx context.Context, store *storage.Store, albumID int64, works []storage.WorkEnrichmentTarget) []storage.WorkEnrichmentTarget {
	if albumID <= 0 || len(works) == 0 {
		return nil
	}
	linked, err := store.WorksForAlbum(ctx, albumID)
	if err != nil {
		return nil
	}
	keep := map[int64]bool{}
	for _, work := range linked {
		keep[work.ID] = true
	}
	var out []storage.WorkEnrichmentTarget
	for _, work := range works {
		if keep[work.ID] {
			out = append(out, work)
		}
	}
	return out
}

func (m *Manager) phase4Items(ctx context.Context, request RunRequest) ([]phase4Item, error) {
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, setting := range settings {
		enabled[setting.Source] = setting.Enabled
	}
	var result []phase4Item
	if enabled["bangumi"] && (request.Scope == "all" || request.Scope == "albums" || request.Scope == "album") {
		albums, e := m.store.AlbumsForBangumiTieup(ctx, request.Force, func() int64 {
			if request.Scope == "album" {
				return request.TargetID
			}
			return 0
		}())
		if e != nil {
			return nil, e
		}
		for _, album := range albums {
			result = append(result, phase4Item{album: album})
		}
	}
	if request.Scope == "work" {
		if enabled["bangumi"] {
			works, e := m.store.WorksForEnrichment(ctx, "bangumi", request.Force, 0, request.TargetID)
			if e != nil {
				return nil, e
			}
			for _, work := range works {
				result = append(result, phase4Item{work: work})
			}
		}
	}
	return result, nil
}

func (m *Manager) executePhase4Run(ctx context.Context, runID int64, request RunRequest) {
	defer func() {
		if value := recover(); value != nil {
			_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", fmt.Sprintf("panic: %v", value))
			m.logger.Error("metadata enrichment panic", "panic", value)
		}
		m.phaseMu.Lock()
		if m.phaseCancel != nil {
			m.phaseCancel()
		}
		m.phaseRunning = false
		m.phaseRunID = 0
		m.phaseCancel = nil
		m.phaseMu.Unlock()
	}()
	finishCancelled := func() {
		if m.baseCtx.Err() != nil {
			_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", "cancelled by shutdown")
		} else {
			_ = m.store.FinishEnrichmentRun(context.Background(), runID, "cancelled", "已手动停止")
		}
	}
	items, err := m.phase4Items(ctx, request)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		finishCancelled()
		return
	}
	if err != nil {
		_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", err.Error())
		return
	}
	counts := storage.EnrichmentRunUpdate{Total: len(items)}
	_ = m.store.UpdateEnrichmentRun(context.Background(), runID, counts)
	consecutiveFailures := 0
	bangumiEnabled := false
	if setting, e := m.store.MetadataSourceSetting(ctx, "bangumi"); e == nil {
		bangumiEnabled = setting.Enabled
	}
	// M4: one forced run reuses a response it already fetched instead of
	// bypassing the cache for the same key over and over.
	m.runMemo = map[string]bool{}
	defer func() { m.runMemo = nil }()
	albumTarget := int64(0)
	if request.Scope == "album" {
		albumTarget = request.TargetID
	}
	next := func() ([]phase4Item, error) {
		if !bangumiEnabled {
			return nil, nil
		}
		tracks, e := m.store.TracksForBangumiTieup(ctx, request.Force, albumTarget)
		if e != nil {
			return nil, e
		}
		out := make([]phase4Item, 0, len(tracks))
		for _, track := range tracks {
			out = append(out, phase4Item{track: track})
		}
		return out, nil
	}
	if request.Scope == "tracks" {
		items = nil
	}
	wantTracks := request.Scope == "all" || request.Scope == "tracks" || request.Scope == "album"
	// Work-level search is the last item stage. A single-album run still
	// refreshes that album's linked works after its tracks (S3 includes the
	// track stage). The "works" scope runs the work stage plus series grouping.
	wantWorks := request.Scope == "all" || request.Scope == "album" || request.Scope == "works"
	// Series grouping (4.5.3) runs after the work alignment stage so freshly
	// bound works are grouped in the same run.
	wantSeries := request.Scope == "all" || request.Scope == "works"
	// Stages are appended only after the previous stage is fully processed, so
	// a growing slice is safe. Collecting inside the same loop used to skip the
	// items just appended (the index had already moved past them).
	stages := []func() ([]phase4Item, error){func() ([]phase4Item, error) { return items, nil }}
	if wantTracks {
		stages = append(stages, next)
	}
	if wantWorks {
		stages = append(stages, func() ([]phase4Item, error) {
			if !bangumiEnabled {
				return nil, nil
			}
			works, e := m.store.WorksForEnrichment(ctx, "bangumi", request.Force, 0)
			if e != nil {
				return nil, e
			}
			if request.Scope == "album" {
				works = worksLinkedToAlbum(ctx, m.store, request.TargetID, works)
			}
			out := make([]phase4Item, 0, len(works))
			for _, work := range works {
				out = append(out, phase4Item{work: work})
			}
			return out, nil
		})
	}
	var queue []phase4Item
	for _, stage := range stages {
		if ctx.Err() != nil {
			finishCancelled()
			return
		}
		batch, e := stage()
		if e != nil {
			_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", e.Error())
			return
		}
		queue = append(queue, batch...)
		counts.Total = len(queue)
		_ = m.store.UpdateEnrichmentRun(context.Background(), runID, counts)
		for _, item := range queue[len(queue)-len(batch):] {
			var outcome string
			switch {
			case item.album.ID != 0:
				counts.Current = item.album.Title
				outcome, err = m.enrichBangumiAlbum(ctx, runID, item.album, request.Force)
			case item.track.ID != 0:
				counts.Current = item.track.Title + " — " + item.track.AlbumTitle
				outcome, err = m.enrichBangumiTrack(ctx, runID, item.track, request.Force)
			default:
				counts.Current = item.work.Title
				outcome, err = m.enrichBangumiWork(ctx, runID, item.work, request.Force)
			}
			// A provider error that arrived together with cancellation is a stop, not
			// one more failure toward the circuit breaker.
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				finishCancelled()
				return
			}
			// Rate limiting stops the whole run immediately: the remaining items
			// would hit the same wall, and the error must not feed the
			// consecutive-failure breaker.
			if rateLimited := asRateLimited(err); rateLimited != nil {
				message := rateLimitRunMessage(rateLimited)
				m.logger.Warn("metadata enrichment stopped by rate limiting", "runId", runID, "source", rateLimited.Source, "retryAfter", rateLimited.RetryAfter.String())
				_ = m.store.UpdateEnrichmentRun(context.Background(), runID, counts)
				_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", message)
				return
			}
			counts.Processed++
			if err != nil {
				counts.Failed++
				counts.ErrorMessage = err.Error()
				m.logger.Warn("metadata enrichment item failed", "current", counts.Current, "error", err)
				consecutiveFailures++
				if consecutiveFailures >= maxConsecutiveFailures {
					message := fmt.Sprintf("aborted after %d consecutive failures: %v", consecutiveFailures, err)
					m.logger.Warn("metadata enrichment aborted", "runId", runID, "error", message)
					_ = m.store.UpdateEnrichmentRun(context.Background(), runID, counts)
					_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", message)
					return
				}
			} else {
				consecutiveFailures = 0
				switch outcome {
				case "review":
					counts.Review++
				case "skipped":
					counts.Skipped++
				default:
					counts.Succeeded++
				}
			}
			_ = m.store.UpdateEnrichmentRun(context.Background(), runID, counts)
		}
	}
	if ctx.Err() != nil {
		finishCancelled()
		return
	}
	if wantSeries && bangumiEnabled {
		counts.Current = "作品系列归组"
		_ = m.store.UpdateEnrichmentRun(context.Background(), runID, counts)
		_, seriesErr := m.enrichBangumiSeries(ctx, runID, request.Force)
		if ctx.Err() != nil {
			finishCancelled()
			return
		}
		if rateLimited := asRateLimited(seriesErr); rateLimited != nil {
			message := rateLimitRunMessage(rateLimited)
			m.logger.Warn("bangumi series grouping stopped by rate limiting", "runId", runID, "retryAfter", rateLimited.RetryAfter.String())
			_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", message)
			return
		}
		if seriesErr != nil {
			counts.Failed++
			counts.ErrorMessage = seriesErr.Error()
			m.logger.Warn("bangumi series grouping failed", "runId", runID, "error", seriesErr)
		} else {
			counts.Succeeded++
		}
		counts.Total++
		counts.Processed++
		counts.Current = ""
		_ = m.store.UpdateEnrichmentRun(context.Background(), runID, counts)
	}
	status := "completed"
	message := ""
	// Total counts every processed unit, including the series stage: a run is
	// failed only when everything failed, not when one stage out of many did.
	if counts.Total > 0 && counts.Failed == counts.Total {
		status, message = "failed", counts.ErrorMessage
	}
	// A completed run with failures keeps the last error message so a failed
	// stage (e.g. series grouping) stays visible in the run record.
	if status == "completed" && counts.Failed > 0 {
		message = counts.ErrorMessage
	}
	_ = m.store.FinishEnrichmentRun(context.Background(), runID, status, message)
}

func (m *Manager) cachedJSON(ctx context.Context, source, key, endpoint string, setting storage.MetadataSourceSetting, force bool, body any, target any) (int, error) {
	// M4: force skips the persistent cache, but a key already fetched in this run
	// is reused so one refresh does not repeat the same request.
	if force && m.runMemo != nil && m.runMemo[source+"\x00"+key] {
		force = false
	}
	if !force {
		if cached, err := m.store.GetHTTPResponseCache(ctx, source, key); err == nil {
			if expires, e := time.Parse(time.RFC3339Nano, cached.ExpiresAt); e == nil && time.Now().UTC().Before(expires) {
				if cached.Status == http.StatusNotFound {
					return cached.Status, sql.ErrNoRows
				}
				if cached.Status < 200 || cached.Status >= 300 {
					return cached.Status, fmt.Errorf("cached %s status %d", source, cached.Status)
				}
				return cached.Status, json.Unmarshal(cached.Body, target)
			}
		}
	}
	var reader io.Reader
	method := http.MethodGet
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = strings.NewReader(string(raw))
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", userAgent(setting))
	// Cached responses do not consume the source's request interval.
	if source == "bangumi" {
		if err = m.waitBangumiRateLimit(ctx); err != nil {
			return 0, err
		}
	} else if source == "musicbrainz" {
		if err = m.waitMBRateLimit(ctx); err != nil {
			return 0, err
		}
	}
	resp, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}
	defer resp.Body.Close()
	// 429 (or 503 with Retry-After) pushes back the source's next allowed
	// request time and is reported as a recognizable error; the response is
	// never cached.
	if isRateLimitResponse(resp.StatusCode, resp.Header) {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return resp.StatusCode, m.rateLimitedError(source, resp.StatusCode, retryAfter)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	// Only successful responses and definitive 404s are cached. Transient
	// failures (429, 5xx, gateway flaps) must never be frozen into a 30-day
	// cache entry.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err = json.Unmarshal(raw, target); err != nil {
			return resp.StatusCode, err
		}
	}
	if (resp.StatusCode >= 200 && resp.StatusCode < 300) || resp.StatusCode == http.StatusNotFound {
		if m.runMemo != nil {
			m.runMemo[source+"\x00"+key] = true
		}
		now := time.Now().UTC()
		ttl := setting.CacheDays
		if ttl < 1 {
			ttl = 30
		}
		_ = m.store.PutHTTPResponseCache(ctx, storage.HTTPResponseCacheEntry{Source: source, Key: key, Status: resp.StatusCode, Body: raw, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), FetchedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Duration(ttl) * 24 * time.Hour).Format(time.RFC3339Nano)})
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, sql.ErrNoRows
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s returned HTTP %d", source, resp.StatusCode)
	}
	return resp.StatusCode, nil
}
