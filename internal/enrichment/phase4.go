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

type phase4Endpoints struct{ Bangumi string }

func defaultPhase4Endpoints() phase4Endpoints {
	return phase4Endpoints{Bangumi: "https://api.bgm.tv/v0/search/subjects"}
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
	case "all":
		value.TargetID = 0
	case "work":
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
	work storage.WorkEnrichmentTarget
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
	if request.Scope == "all" || request.Scope == "work" {
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
		m.phaseCancel()
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
	for _, item := range items {
		if ctx.Err() != nil {
			finishCancelled()
			return
		}
		counts.Current = item.work.Title
		outcome, err := m.enrichBangumiWork(ctx, runID, item.work, request.Force)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			finishCancelled()
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
	if ctx.Err() != nil {
		finishCancelled()
		return
	}
	status := "completed"
	message := ""
	if len(items) > 0 && counts.Failed == len(items) {
		status, message = "failed", counts.ErrorMessage
	}
	_ = m.store.FinishEnrichmentRun(context.Background(), runID, status, message)
}

func (m *Manager) cachedJSON(ctx context.Context, source, key, endpoint string, setting storage.MetadataSourceSetting, force bool, body any, target any) (int, error) {
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
	contact := strings.TrimSpace(setting.Contact)
	if contact == "" {
		contact = "self-hosted"
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s/%s (%s)", setting.ApplicationName, setting.ApplicationVersion, contact))
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
		return 0, err
	}
	defer resp.Body.Close()
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
