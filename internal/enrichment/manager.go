package enrichment

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

type Manager struct {
	baseCtx             context.Context
	store               *storage.Store
	logger              *slog.Logger
	client              *http.Client
	mu                  sync.Mutex
	running             bool
	artistRunID         int64
	artistCancel        context.CancelFunc
	mbMu                sync.Mutex
	mbLast              time.Time
	mbBlockedUntil      time.Time
	phaseMu             sync.Mutex
	phaseRunning        bool
	phaseRunID          int64
	phaseCancel         context.CancelFunc
	phaseEndpoints      phase4Endpoints
	confirmAlbumSubject func(context.Context, int64, int64, int64, string, bool, []int64) (string, []int64, error)
	posterMu            sync.Mutex
	posterBackfilling   bool
	musicBrainzBase     string
	bangumiMu           sync.Mutex
	bangumiLast         time.Time
	bangumiBlockedUntil time.Time
	bangumiInterval     time.Duration
	imageDirectory      string
	wg                  sync.WaitGroup
	// sleep backs waitBangumiRateLimit/waitMBRateLimit; tests replace it to
	// observe backoff waits without really sleeping.
	sleep func(ctx context.Context, d time.Duration) error
	// runMemo remembers request keys already fetched during the current run so a
	// forced refresh still hits the network only once per key (M4).
	runMemo map[string]bool
}
type MatchResult struct {
	AutoMatched    bool
	CandidateCount int
}

func New(baseCtx context.Context, store *storage.Store, logger *slog.Logger, dataDirectory string) *Manager {
	manager := &Manager{baseCtx: baseCtx, store: store, logger: logger, client: &http.Client{Timeout: 20 * time.Second}, phaseEndpoints: defaultPhase4Endpoints(), musicBrainzBase: "https://musicbrainz.org/ws/2", bangumiInterval: bangumiIntervalFromEnv(logger), imageDirectory: filepath.Join(dataDirectory, "artist-images"), sleep: sleepContext}
	if recovered, err := store.FailRunningEnrichmentRuns(context.Background(), "server restarted before the enrichment run completed"); err != nil {
		logger.Warn("recover interrupted enrichment runs", "error", err)
	} else if recovered > 0 {
		logger.Info("recovered interrupted enrichment runs", "count", recovered)
	}
	if recovered, err := store.FailRunningArtistMatchRuns(context.Background(), "server restarted before the artist matching run completed"); err != nil {
		logger.Warn("recover interrupted artist matching runs", "error", err)
	} else if recovered > 0 {
		logger.Info("recovered interrupted artist matching runs", "count", recovered)
	}
	return manager
}

// Wait blocks until all background enrichment work has finished. Call after
// cancelling the base context during shutdown.
func (m *Manager) Wait() { m.wg.Wait() }

// goBackground runs fn as a tracked background worker: panics are recovered
// and logged instead of crashing the process, and Wait() blocks until it
// returns.
func (m *Manager) goBackground(name string, fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				m.logger.Error("panic in background enrichment worker", "worker", name, "panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

func (m *Manager) StartAuto(ctx context.Context) {
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		m.logger.Warn("load automatic metadata settings", "error", err)
		return
	}
	phaseEnabled := false
	identityEnabled := false
	for _, setting := range settings {
		if !setting.Enabled || !setting.AutoMatch {
			continue
		}
		switch setting.Source {
		case "bangumi":
			phaseEnabled = true
		case "musicbrainz", "lastfm":
			identityEnabled = true
		}
	}
	if phaseEnabled {
		if _, err = m.StartRun(ctx, RunRequest{Scope: "all"}); err != nil {
			m.logger.Warn("automatic metadata enrichment was not started", "error", err)
		}
	}
	if identityEnabled {
		if _, err = m.StartAll(ctx); err != nil {
			m.logger.Warn("automatic artist matching was not started", "error", err)
		}
	}
	m.StartWorkPosterBackfill()
}

func (m *Manager) StartAll(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return 0, errors.New("artist matching is already running")
	}
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		return 0, err
	}
	hasAutomaticSource := false
	for _, setting := range settings {
		if setting.Enabled && setting.AutoMatch {
			hasAutomaticSource = true
			break
		}
	}
	if !hasAutomaticSource {
		return 0, errors.New("no automatic metadata source is enabled")
	}
	artists, err := m.store.ArtistsForMatching(ctx)
	if err != nil {
		return 0, err
	}
	runID, err := m.store.CreateArtistMatchRun(ctx, len(artists))
	if err != nil {
		return 0, err
	}
	runCtx, cancel := context.WithCancel(m.baseCtx)
	m.running, m.artistRunID, m.artistCancel = true, runID, cancel
	m.goBackground("artist-matching", func() {
		defer func() {
			if value := recover(); value != nil {
				_ = m.store.FinishArtistMatchRun(context.Background(), runID, "failed", fmt.Sprintf("panic: %v", value))
				m.logger.Error("artist matching panic", "panic", value)
			}
			cancel()
			m.mu.Lock()
			m.running = false
			m.artistRunID = 0
			m.artistCancel = nil
			m.mu.Unlock()
		}()
		finishCancelled := func() {
			if m.baseCtx.Err() != nil {
				_ = m.store.FinishArtistMatchRun(context.Background(), runID, "failed", "cancelled by shutdown")
			} else {
				_ = m.store.FinishArtistMatchRun(context.Background(), runID, "cancelled", "已手动停止")
			}
		}
		var matched, review, failed int
		for index, artist := range artists {
			if runCtx.Err() != nil {
				finishCancelled()
				return
			}
			result, matchErr := m.matchArtist(runCtx, artist.ID, true)
			if runCtx.Err() != nil || errors.Is(matchErr, context.Canceled) || errors.Is(matchErr, context.DeadlineExceeded) {
				finishCancelled()
				return
			}
			if rateLimited := asRateLimited(matchErr); rateLimited != nil {
				if result.AutoMatched {
					// The artist was confirmed before the rate limit hit (e.g. the
					// biography refresh afterwards): count the match and the
					// processed artist before stopping the run.
					matched++
					_ = m.store.UpdateArtistMatchRun(context.Background(), runID, index+1, matched, review, failed, artist.Name)
				}
				message := rateLimitRunMessage(rateLimited)
				m.logger.Warn("artist matching stopped by rate limiting", "source", rateLimited.Source, "retryAfter", rateLimited.RetryAfter.String())
				_ = m.store.FinishArtistMatchRun(context.Background(), runID, "failed", message)
				return
			}
			if matchErr != nil {
				failed++
				m.logger.Warn("artist match failed", "artist", artist.Name, "error", matchErr)
			} else if result.AutoMatched {
				matched++
			} else if result.CandidateCount > 0 {
				review++
			}
			_ = m.store.UpdateArtistMatchRun(context.Background(), runID, index+1, matched, review, failed, artist.Name)
		}
		if runCtx.Err() != nil {
			finishCancelled()
			return
		}
		_ = m.store.FinishArtistMatchRun(context.Background(), runID, "completed", "")
	})
	return runID, nil
}

var ErrRunNotActive = errors.New("run is not active")

func (m *Manager) CancelArtistMatching(runID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running || m.artistRunID != runID || m.artistCancel == nil {
		return ErrRunNotActive
	}
	rows, err := m.store.ListArtistMatchRuns(context.Background(), 1)
	if err != nil || len(rows) == 0 || rows[0].ID != runID || rows[0].Status != "running" {
		return ErrRunNotActive
	}
	m.artistCancel()
	return nil
}

func (m *Manager) MatchArtist(ctx context.Context, artistID int64) (MatchResult, error) {
	return m.matchArtist(ctx, artistID, false)
}

func (m *Manager) matchArtist(ctx context.Context, artistID int64, automatic bool) (MatchResult, error) {
	if automatic {
		if needed, checkErr := m.store.ArtistImageCheckNeeded(ctx, artistID); checkErr == nil && needed {
			if imageErr := m.RefreshConfirmedArtistImage(ctx, artistID); imageErr != nil && !errors.Is(imageErr, sql.ErrNoRows) {
				if asRateLimited(imageErr) != nil {
					return MatchResult{}, imageErr
				}
				m.logger.Warn("backfill confirmed artist image", "artistId", artistID, "error", imageErr)
			}
		}
	}
	artist, err := m.store.ArtistForMatching(ctx, artistID)
	if err != nil {
		return MatchResult{}, err
	}
	if biographyErr := m.RefreshArtistBiographies(ctx, artistID, !automatic); biographyErr != nil && !errors.Is(biographyErr, sql.ErrNoRows) {
		if asRateLimited(biographyErr) != nil {
			return MatchResult{}, biographyErr
		}
		m.logger.Warn("refresh confirmed artist biographies", "artistId", artistID, "error", biographyErr)
	}
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		return MatchResult{}, err
	}
	bySource := map[string]storage.MetadataSourceSetting{}
	for _, setting := range settings {
		if setting.Enabled && (!automatic || setting.AutoMatch) {
			bySource[setting.Source] = setting
		}
	}
	if len(bySource) == 0 {
		return MatchResult{}, errors.New("no metadata source is enabled")
	}
	var candidates []storage.ArtistCandidate
	queriedSources := 0
	successfulSources := 0
	var mbProfiles = map[string]storage.ExternalArtistProfile{}
	var lastProfile *storage.ExternalArtistProfile
	if setting, ok := bySource["musicbrainz"]; ok {
		fresh, _ := m.store.ArtistProfileFresh(ctx, artistID, "musicbrainz", setting.CacheDays)
		if automatic && fresh {
			delete(bySource, "musicbrainz")
		} else {
			queriedSources++
			var found []storage.ArtistCandidate
			if artist.TaggedMBID != "" {
				candidate, profile, e := m.musicBrainzLookup(ctx, artist.TaggedMBID, setting)
				if e == nil {
					successfulSources++
					candidate.ArtistID = artistID
					candidate.Score = 100
					candidate.Evidence = []string{"文件标签包含 MusicBrainz ID"}
					found = append(found, candidate)
					mbProfiles[candidate.MBID] = profile
				} else if asRateLimited(e) != nil {
					return MatchResult{}, e
				}
			} else {
				found, mbProfiles, err = m.musicBrainzSearch(ctx, artist, setting)
				if err != nil {
					if asRateLimited(err) != nil {
						return MatchResult{}, err
					}
					m.logger.Warn("musicbrainz search failed", "artist", artist.Name, "error", err)
				} else {
					successfulSources++
				}
			}
			candidates = append(candidates, found...)
		}
	}
	if setting, ok := bySource["lastfm"]; ok {
		fresh, _ := m.store.ArtistProfileFresh(ctx, artistID, "lastfm", setting.CacheDays)
		if automatic && fresh {
			delete(bySource, "lastfm")
		} else {
			queriedSources++
			candidate, profile, e := m.lastFMInfo(ctx, artist, setting)
			if e != nil {
				if asRateLimited(e) != nil {
					return MatchResult{}, e
				}
				m.logger.Warn("lastfm lookup failed", "artist", artist.Name, "error", e)
			} else {
				successfulSources++
				candidate.ArtistID = artistID
				candidates = append(candidates, candidate)
				lastProfile = &profile
			}
		}
	}
	if automatic && queriedSources == 0 {
		return MatchResult{AutoMatched: true}, nil
	}
	if queriedSources > 0 && successfulSources == 0 {
		return MatchResult{}, errors.New("all enabled metadata sources failed")
	}
	for i := range candidates {
		if candidates[i].MBID != "" && lastProfile != nil && lastProfile.ExternalID == candidates[i].MBID {
			candidates[i].Score = 98
			candidates[i].Evidence = append(candidates[i].Evidence, "MusicBrainz 与 Last.fm 返回相同 MBID")
		}
	}
	if lastProfile != nil && lastProfile.ExternalID != "" {
		for i := range candidates {
			if candidates[i].Source == "lastfm" {
				if _, ok := mbProfiles[lastProfile.ExternalID]; ok {
					candidates[i].Score = 98
					candidates[i].Evidence = append(candidates[i].Evidence, "MusicBrainz 与 Last.fm 返回相同 MBID")
				} else if confirmedMBID, confirmedErr := m.store.ArtistExternalID(ctx, artistID, "musicbrainz"); confirmedErr == nil && confirmedMBID == lastProfile.ExternalID {
					candidates[i].Score = 98
					candidates[i].Evidence = append(candidates[i].Evidence, "Last.fm MBID 与已确认的 MusicBrainz 身份一致")
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if err = m.store.ReplaceArtistCandidates(ctx, artistID, candidates); err != nil {
		return MatchResult{}, err
	}
	auto := false
	if len(candidates) > 0 && candidates[0].Score >= 92 {
		best := candidates[0]
		if existingID, existingErr := m.store.ArtistExternalID(ctx, artistID, best.Source); existingErr == nil && existingID != best.ExternalID {
			return MatchResult{CandidateCount: len(candidates)}, nil
		}
		owner, ownerErr := m.store.ExternalProfileOwner(ctx, best.Source, best.ExternalID)
		if ownerErr == nil && owner != artistID {
			return MatchResult{CandidateCount: len(candidates)}, nil
		}
		if profile, ok := mbProfiles[best.MBID]; ok {
			if profile.RemoteImageURL == "" {
				if setting, enabled := bySource["musicbrainz"]; enabled {
					if _, detailed, lookupErr := m.musicBrainzLookup(ctx, best.MBID, setting); lookupErr == nil {
						profile = detailed
					} else if asRateLimited(lookupErr) != nil {
						return MatchResult{}, lookupErr
					}
				}
			}
			if err = m.store.UpsertExternalArtistProfile(ctx, artistID, profile); err != nil {
				return MatchResult{CandidateCount: len(candidates)}, nil
			}
		}
		if lastProfile != nil && lastProfile.ExternalID == best.MBID {
			if err = m.store.UpsertExternalArtistProfile(ctx, artistID, *lastProfile); err != nil {
				return MatchResult{CandidateCount: len(candidates)}, nil
			}
		}
		if err = m.store.MarkArtistCandidatesConfirmed(ctx, artistID, best.ExternalID, best.MBID); err != nil {
			return MatchResult{}, err
		}
		if err = m.CacheArtistImage(ctx, artistID); err != nil {
			if asRateLimited(err) != nil {
				// The confirmation is already persisted; report AutoMatched so
				// interactive callers can say the match succeeded but the
				// image/biography fetch was deferred by the rate limit.
				return MatchResult{AutoMatched: true, CandidateCount: len(candidates)}, err
			}
			m.logger.Warn("cache artist image failed", "artistId", artistID, "error", err)
		}
		if err = m.RefreshArtistBiographies(ctx, artistID, false); err != nil && !errors.Is(err, sql.ErrNoRows) {
			if asRateLimited(err) != nil {
				return MatchResult{AutoMatched: true, CandidateCount: len(candidates)}, err
			}
			m.logger.Warn("cache artist biographies failed", "artistId", artistID, "error", err)
		}
		auto = true
	}
	return MatchResult{AutoMatched: auto, CandidateCount: len(candidates)}, nil
}

type mbSearchResponse struct {
	Artists []struct {
		ID, Name                      string
		SortName                      string `json:"sort-name"`
		Type, Country, Disambiguation string
		Score                         int
		Aliases                       []struct{ Name string }
		Tags                          []struct{ Name string }
	} `json:"artists"`
}
type mbArtistResponse struct {
	ID, Name                      string
	SortName                      string `json:"sort-name"`
	Type, Country, Disambiguation string
	Aliases                       []struct{ Name string }
	Tags                          []struct{ Name string }
	Relations                     []struct {
		Type string
		URL  struct{ Resource string }
	}
}

func (m *Manager) musicBrainzSearch(ctx context.Context, artist storage.ArtistMatchInput, setting storage.MetadataSourceSetting) ([]storage.ArtistCandidate, map[string]storage.ExternalArtistProfile, error) {
	endpoint := strings.TrimRight(m.musicBrainzBase, "/") + "/artist/?query=" + url.QueryEscape("artist:\""+artist.Name+"\"") + "&fmt=json&limit=5"
	var response mbSearchResponse
	if err := m.mbRequest(ctx, endpoint, setting, &response); err != nil {
		return nil, nil, err
	}
	profiles := map[string]storage.ExternalArtistProfile{}
	var result []storage.ArtistCandidate
	for _, value := range response.Artists {
		score := value.Score
		if normalize(value.Name) == normalize(artist.Name) && score < 85 {
			score = 85
		}
		if score > 91 {
			score = 85
		}
		aliases := make([]string, 0, len(value.Aliases))
		for _, v := range value.Aliases {
			aliases = append(aliases, v.Name)
		}
		tags := make([]string, 0, len(value.Tags))
		for _, v := range value.Tags {
			tags = append(tags, v.Name)
		}
		payload := profilePayload("https://musicbrainz.org/artist/"+value.ID, "", "", aliases, tags)
		result = append(result, storage.ArtistCandidate{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, Disambiguation: value.Disambiguation, Country: value.Country, ArtistType: value.Type, MBID: value.ID, Score: score, Evidence: []string{"MusicBrainz 搜索评分"}, Payload: payload})
		profiles[value.ID] = storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, PageURL: "https://musicbrainz.org/artist/" + value.ID, Country: value.Country, ArtistType: value.Type, Disambiguation: value.Disambiguation, Aliases: aliases, Tags: tags, Raw: payload}
	}
	return result, profiles, nil
}

func (m *Manager) musicBrainzLookup(ctx context.Context, mbid string, setting storage.MetadataSourceSetting) (storage.ArtistCandidate, storage.ExternalArtistProfile, error) {
	var value mbArtistResponse
	endpoint := strings.TrimRight(m.musicBrainzBase, "/") + "/artist/" + url.PathEscape(mbid) + "?inc=aliases+tags+url-rels&fmt=json"
	if err := m.mbRequest(ctx, endpoint, setting, &value); err != nil {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, err
	}
	aliases := make([]string, 0, len(value.Aliases))
	for _, v := range value.Aliases {
		aliases = append(aliases, v.Name)
	}
	tags := make([]string, 0, len(value.Tags))
	for _, v := range value.Tags {
		tags = append(tags, v.Name)
	}
	imageURL := ""
	var spotifyURLs []string
	for _, relation := range value.Relations {
		if relation.Type == "wikidata" {
			var imageErr error
			imageURL, imageErr = m.wikidataImage(ctx, relation.URL.Resource)
			if imageErr != nil {
				// Rate limiting must abort the match; ordinary image lookup
				// failures stay best-effort.
				if asRateLimited(imageErr) != nil {
					return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, imageErr
				}
			}
			if imageURL != "" {
				break
			}
		}
		if strings.Contains(relation.URL.Resource, "open.spotify.com/artist/") {
			spotifyURLs = append(spotifyURLs, relation.URL.Resource)
		}
	}
	if imageURL == "" {
		for _, spotifyURL := range spotifyURLs {
			var imageErr error
			imageURL, imageErr = m.spotifyImage(ctx, spotifyURL)
			if imageErr != nil {
				// Stop the loop on rate limiting instead of hammering the next URL.
				if asRateLimited(imageErr) != nil {
					return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, imageErr
				}
				continue
			}
			if imageURL != "" {
				break
			}
		}
	}
	payload := profilePayload("https://musicbrainz.org/artist/"+value.ID, imageURL, "", aliases, tags)
	profile := storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, PageURL: "https://musicbrainz.org/artist/" + value.ID, RemoteImageURL: imageURL, Country: value.Country, ArtistType: value.Type, Disambiguation: value.Disambiguation, Aliases: aliases, Tags: tags, Raw: payload}
	return storage.ArtistCandidate{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, Disambiguation: value.Disambiguation, Country: value.Country, ArtistType: value.Type, MBID: value.ID, Payload: payload}, profile, nil
}

func (m *Manager) waitBangumiRateLimit(ctx context.Context) error {
	m.bangumiMu.Lock()
	defer m.bangumiMu.Unlock()
	// Never sleep through a rate-limit backoff while holding the lock: report
	// it immediately so callers (run loops and interactive handlers alike)
	// can react instead of being blocked uncancellably.
	if remaining := time.Until(m.bangumiBlockedUntil); remaining > 0 {
		return &RateLimitError{Source: "bangumi", StatusCode: http.StatusTooManyRequests, RetryAfter: remaining}
	}
	if wait := time.Until(m.bangumiLast.Add(m.bangumiInterval)); wait > 0 {
		if err := m.sleep(ctx, wait); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.bangumiLast = time.Now()
	return nil
}

// waitMBRateLimit enforces the MusicBrainz 1 request/second policy. Every
// request to musicbrainz.org — direct or cache-backed — must go through this.
func (m *Manager) waitMBRateLimit(ctx context.Context) error {
	m.mbMu.Lock()
	defer m.mbMu.Unlock()
	if remaining := time.Until(m.mbBlockedUntil); remaining > 0 {
		return &RateLimitError{Source: "musicbrainz", StatusCode: http.StatusTooManyRequests, RetryAfter: remaining}
	}
	if wait := time.Until(m.mbLast.Add(time.Second)); wait > 0 {
		if err := m.sleep(ctx, wait); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mbLast = time.Now()
	return nil
}

func (m *Manager) mbRequest(ctx context.Context, endpoint string, setting storage.MetadataSourceSetting, target any) error {
	if err := m.waitMBRateLimit(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent(setting))
	return m.doSourceJSON(m.client, req, "musicbrainz", target)
}

type lastFMResponse struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
	Artist  struct {
		Name, MBID, URL string
		Image           []struct {
			URL  string `json:"#text"`
			Size string `json:"size"`
		} `json:"image"`
		Bio     struct{ Summary, Content string }
		Tags    struct{ Tag []struct{ Name string } }
		Similar struct{ Artist []struct{ Name string } }
	} `json:"artist"`
}

func (m *Manager) lastFMInfo(ctx context.Context, artist storage.ArtistMatchInput, setting storage.MetadataSourceSetting) (storage.ArtistCandidate, storage.ExternalArtistProfile, error) {
	return m.lastFMInfoLanguage(ctx, artist, setting, setting.Language)
}

func (m *Manager) lastFMInfoLanguage(ctx context.Context, artist storage.ArtistMatchInput, setting storage.MetadataSourceSetting, language string) (storage.ArtistCandidate, storage.ExternalArtistProfile, error) {
	if setting.APIKey == "" {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, errors.New("Last.fm API key is not configured")
	}
	query := url.Values{"method": {"artist.getinfo"}, "artist": {artist.Name}, "api_key": {setting.APIKey}, "format": {"json"}, "autocorrect": {"1"}, "lang": {normalizeLanguage(language)}}
	if mbid, mbidErr := m.store.ArtistExternalID(ctx, artist.ID, "musicbrainz"); mbidErr == nil && mbid != "" {
		query.Del("artist")
		query.Set("mbid", mbid)
	}
	endpoint := "https://ws.audioscrobbler.com/2.0/?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, err
	}
	req.Header.Set("User-Agent", userAgent(setting))
	var response lastFMResponse
	if err = m.doSourceJSON(m.client, req, "lastfm", &response); err != nil {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, err
	}
	if response.Error == 29 {
		// Last.fm error 29 is "rate limit exceeded"; the API reports it with a
		// 200 (or 429) status and a JSON error payload.
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, m.rateLimitedError("lastfm", http.StatusTooManyRequests, defaultRateLimitBackoff)
	}
	if response.Artist.Name == "" {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, errors.New("Last.fm returned no artist")
	}
	var tags []string
	for _, v := range response.Artist.Tags.Tag {
		tags = append(tags, v.Name)
	}
	biography := stripLastFMLink(response.Artist.Bio.Content)
	score := 72
	if normalize(response.Artist.Name) == normalize(artist.Name) {
		score = 82
	}
	externalID := response.Artist.MBID
	if externalID == "" {
		externalID = response.Artist.URL
	}
	imageURL := ""
	for _, image := range response.Artist.Image {
		if strings.TrimSpace(image.URL) != "" {
			imageURL = strings.TrimSpace(image.URL)
		}
	}
	if strings.Contains(imageURL, "2a96cbd8b46e442fc41c2b86b821562f") {
		imageURL = ""
	}
	payload := profilePayload(response.Artist.URL, imageURL, biography, nil, tags)
	profile := storage.ExternalArtistProfile{Source: "lastfm", ExternalID: externalID, DisplayName: response.Artist.Name, PageURL: response.Artist.URL, RemoteImageURL: imageURL, Biography: biography, Tags: tags, Raw: payload}
	candidate := storage.ArtistCandidate{Source: "lastfm", ExternalID: externalID, DisplayName: response.Artist.Name, MBID: response.Artist.MBID, Score: score, Evidence: []string{"Last.fm 名称匹配"}, Payload: payload}
	return candidate, profile, nil
}

// validatePublicImageURL ensures an image URL is http(s) and does not point
// at loopback, private (RFC1918), link-local (incl. 169.254.169.254) or
// otherwise non-public address space.
func validatePublicImageURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return errors.New("artist image source returned an invalid URL")
	}
	hostname := parsed.Hostname()
	if ip := net.ParseIP(hostname); ip != nil {
		return rejectPrivateIP(ip)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(context.Background(), hostname)
	if err != nil {
		return fmt.Errorf("resolve artist image host: %w", err)
	}
	for _, address := range addresses {
		if err = rejectPrivateIP(address.IP); err != nil {
			return err
		}
	}
	return nil
}

func rejectPrivateIP(ip net.IP) error {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("artist image URL resolves to a non-public address %s", ip)
	}
	return nil
}

func profilePayload(pageURL, imageURL, biography string, aliases, tags []string) json.RawMessage {
	value := map[string]any{"pageUrl": pageURL, "imageUrl": imageURL, "biography": biography, "aliases": aliases, "tags": tags}
	data, _ := json.Marshal(value)
	return data
}

func (m *Manager) CacheArtistImage(ctx context.Context, artistID int64) error {
	source, remoteURL, err := m.store.ArtistImageSource(ctx, artistID)
	if err != nil {
		return err
	}
	// The URL originates from upstream metadata (Last.fm, Spotify oEmbed,
	// community-editable MusicBrainz relations), so it is untrusted input:
	// downloadPublicImage refuses private/loopback/link-local targets.
	data, mimeType, extension, err := m.downloadPublicImage(ctx, remoteURL, "artist image")
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	cachePath, err := writeCacheFile(m.imageDirectory, hash+extension, data)
	if err != nil {
		return err
	}
	return m.store.SaveArtistImage(ctx, storage.ArtistImageInput{ArtistID: artistID, ByteSize: int64(len(data)), Source: source, RemoteURL: remoteURL, Hash: hash, MIMEType: mimeType, CachePath: cachePath})
}

func (m *Manager) RefreshConfirmedArtistImage(ctx context.Context, artistID int64) error {
	refreshed := false
	mbid, err := m.store.ArtistExternalID(ctx, artistID, "musicbrainz")
	if err == nil && mbid != "" {
		setting, settingErr := m.store.MetadataSourceSetting(ctx, "musicbrainz")
		if settingErr == nil && setting.Enabled {
			_, profile, lookupErr := m.musicBrainzLookup(ctx, mbid, setting)
			if lookupErr == nil {
				if upsertErr := m.store.UpsertExternalArtistProfile(ctx, artistID, profile); upsertErr != nil {
					return upsertErr
				}
				refreshed = true
			} else if asRateLimited(lookupErr) != nil {
				return lookupErr
			}
		}
	}
	if _, lastFMErr := m.store.ArtistExternalID(ctx, artistID, "lastfm"); lastFMErr == nil {
		setting, settingErr := m.store.MetadataSourceSetting(ctx, "lastfm")
		if settingErr == nil && setting.Enabled {
			artist, artistErr := m.store.ArtistForMatching(ctx, artistID)
			if artistErr == nil {
				_, profile, lookupErr := m.lastFMInfo(ctx, artist, setting)
				if lookupErr == nil {
					if upsertErr := m.store.UpsertExternalArtistProfile(ctx, artistID, profile); upsertErr != nil {
						return upsertErr
					}
					refreshed = true
				} else if asRateLimited(lookupErr) != nil {
					return lookupErr
				}
			}
		}
	}
	if !refreshed {
		return sql.ErrNoRows
	}
	return m.CacheArtistImage(ctx, artistID)
}

func (m *Manager) RefreshArtistBiographies(ctx context.Context, artistID int64, force bool) error {
	settings, err := m.store.BiographySettings(ctx)
	if err != nil {
		return err
	}
	languages := languageList(settings.PreferredLanguages, settings.EnglishFallback)
	if len(languages) == 0 {
		return sql.ErrNoRows
	}

	wikipediaNeeded := settings.WikipediaEnabled
	lastFMSetting, lastFMErr := m.store.MetadataSourceSetting(ctx, "lastfm")
	lastFMNeeded := lastFMErr == nil && lastFMSetting.Enabled && lastFMSetting.APIKey != ""
	if !force {
		wikipediaNeeded = wikipediaNeeded && m.biographySourceNeedsRefresh(ctx, artistID, "wikipedia", languages, settings.CacheDays)
		lastFMNeeded = lastFMNeeded && m.biographySourceNeedsRefresh(ctx, artistID, "lastfm", languages, settings.CacheDays)
	}
	if !wikipediaNeeded && !lastFMNeeded {
		return nil
	}

	artist, err := m.store.ArtistForMatching(ctx, artistID)
	if err != nil {
		return err
	}
	mbid, mbidErr := m.store.ArtistExternalID(ctx, artistID, "musicbrainz")
	if mbidErr != nil && !errors.Is(mbidErr, sql.ErrNoRows) {
		return mbidErr
	}
	if mbid == "" {
		return sql.ErrNoRows
	}

	var wikidataResource string
	wikidataChecked := false
	resolved := false
	if wikipediaNeeded {
		mbSetting, settingErr := m.store.MetadataSourceSetting(ctx, "musicbrainz")
		if settingErr == nil {
			var value mbArtistResponse
			endpoint := strings.TrimRight(m.musicBrainzBase, "/") + "/artist/" + url.PathEscape(mbid) + "?inc=url-rels&fmt=json"
			if lookupErr := m.mbRequest(ctx, endpoint, mbSetting, &value); lookupErr != nil {
				if asRateLimited(lookupErr) != nil {
					return lookupErr
				}
				m.logger.Warn("load MusicBrainz relations for biography", "artistId", artistID, "error", lookupErr)
			} else {
				wikidataChecked = true
				for _, relation := range value.Relations {
					if relation.Type == "wikidata" {
						wikidataResource = relation.URL.Resource
						break
					}
				}
			}
		}
	}

	if wikipediaNeeded && wikidataResource != "" {
		sitelinks, linkErr := m.wikidataSitelinks(ctx, wikidataResource)
		if linkErr != nil {
			if asRateLimited(linkErr) != nil {
				return linkErr
			}
			m.logger.Warn("load Wikidata sitelinks", "artistId", artistID, "error", linkErr)
		} else {
			for _, language := range languages {
				title := sitelinks[language]
				if title == "" {
					if storeErr := m.store.UpsertArtistBiography(ctx, artistID, storage.ArtistBiography{Source: "wikipedia", Language: language, Status: "missing"}); storeErr != nil {
						return storeErr
					}
					resolved = true
					continue
				}
				biography, pageURL, fetchErr := m.wikipediaSummary(ctx, language, title)
				if fetchErr != nil {
					if asRateLimited(fetchErr) != nil {
						return fetchErr
					}
					m.logger.Warn("fetch Wikipedia biography", "artistId", artistID, "language", language, "error", fetchErr)
					continue
				}
				if storeErr := m.store.UpsertArtistBiography(ctx, artistID, storage.ArtistBiography{Source: "wikipedia", Language: language, Biography: biography, PageURL: pageURL}); storeErr != nil {
					return storeErr
				}
				resolved = true
			}
		}
	} else if wikipediaNeeded && wikidataChecked {
		for _, language := range languages {
			if storeErr := m.store.UpsertArtistBiography(ctx, artistID, storage.ArtistBiography{Source: "wikipedia", Language: language, Status: "missing"}); storeErr != nil {
				return storeErr
			}
			resolved = true
		}
	}

	if lastFMNeeded {
		for _, language := range languages {
			_, profile, fetchErr := m.lastFMInfoLanguage(ctx, artist, lastFMSetting, language)
			if fetchErr != nil {
				if asRateLimited(fetchErr) != nil {
					return fetchErr
				}
				m.logger.Warn("fetch Last.fm biography", "artistId", artistID, "language", language, "error", fetchErr)
				continue
			}
			if storeErr := m.store.UpsertArtistBiography(ctx, artistID, storage.ArtistBiography{Source: "lastfm", Language: language, Biography: profile.Biography, PageURL: profile.PageURL}); storeErr != nil {
				return storeErr
			}
			resolved = true
		}
	}
	if !resolved {
		return errors.New("all enabled biography sources failed")
	}
	return nil
}

func (m *Manager) biographySourceNeedsRefresh(ctx context.Context, artistID int64, source string, languages []string, days int) bool {
	for _, language := range languages {
		fresh, err := m.store.ArtistBiographyFresh(ctx, artistID, source, language, days)
		if err != nil || !fresh {
			return true
		}
	}
	return false
}

func (m *Manager) wikidataSitelinks(ctx context.Context, resource string) (map[string]string, error) {
	entityID, err := wikidataEntityID(resource)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.wikidata.org/wiki/Special:EntityData/"+url.PathEscape(entityID)+".json", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var document struct {
		Entities map[string]struct {
			Sitelinks map[string]struct{ Title string } `json:"sitelinks"`
		} `json:"entities"`
	}
	if err = m.doSourceJSON(m.client, request, "wikidata", &document); err != nil {
		return nil, err
	}
	entity, ok := document.Entities[entityID]
	if !ok {
		return nil, errors.New("Wikidata entity was not returned")
	}
	result := map[string]string{}
	for key, value := range entity.Sitelinks {
		if strings.HasSuffix(key, "wiki") && !strings.Contains(key, "_") {
			result[strings.TrimSuffix(key, "wiki")] = value.Title
		}
	}
	return result, nil
}

func (m *Manager) wikipediaSummary(ctx context.Context, language, title string) (string, string, error) {
	pageURL := "https://" + language + ".wikipedia.org/wiki/" + strings.ReplaceAll(url.PathEscape(title), "%20", "_")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+language+".wikipedia.org/api/rest_v1/page/summary/"+url.PathEscape(title), nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var summary struct {
		Extract     string `json:"extract"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	if restErr := m.doSourceJSON(m.client, request, "wikipedia", &summary); restErr == nil && strings.TrimSpace(summary.Extract) != "" {
		if summary.ContentURLs.Desktop.Page != "" {
			pageURL = summary.ContentURLs.Desktop.Page
		}
		return strings.TrimSpace(summary.Extract), pageURL, nil
	} else if restErr != nil && asRateLimited(restErr) != nil {
		// Rate limited: back off instead of hammering the action API fallback.
		return "", "", restErr
	}

	query := url.Values{"action": {"query"}, "prop": {"extracts"}, "exintro": {"1"}, "explaintext": {"1"}, "redirects": {"1"}, "format": {"json"}, "formatversion": {"2"}, "titles": {title}}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://"+language+".wikipedia.org/w/api.php?"+query.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var action struct {
		Query struct {
			Pages []struct {
				Extract string `json:"extract"`
			}
		} `json:"query"`
	}
	if err = m.doSourceJSON(m.client, request, "wikipedia", &action); err != nil {
		return "", "", err
	}
	if len(action.Query.Pages) == 0 || strings.TrimSpace(action.Query.Pages[0].Extract) == "" {
		return "", pageURL, nil
	}
	return strings.TrimSpace(action.Query.Pages[0].Extract), pageURL, nil
}

func (m *Manager) wikidataImage(ctx context.Context, resource string) (string, error) {
	entityID, err := wikidataEntityID(resource)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.wikidata.org/wiki/Special:EntityData/"+url.PathEscape(entityID)+".json", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var document struct {
		Entities map[string]struct {
			Claims map[string][]struct {
				MainSnak struct {
					DataValue struct{ Value string }
				} `json:"mainsnak"`
			} `json:"claims"`
		} `json:"entities"`
	}
	if err = m.doSourceJSON(m.client, request, "wikidata", &document); err != nil {
		return "", err
	}
	entity, ok := document.Entities[entityID]
	if !ok || len(entity.Claims["P18"]) == 0 {
		return "", errors.New("wikidata artist has no P18 image")
	}
	filename := strings.TrimSpace(entity.Claims["P18"][0].MainSnak.DataValue.Value)
	if filename == "" {
		return "", errors.New("wikidata P18 image is empty")
	}
	return "https://commons.wikimedia.org/wiki/Special:Redirect/file/" + url.PathEscape(filename) + "?width=800", nil
}

func (m *Manager) spotifyImage(ctx context.Context, artistURL string) (string, error) {
	parsed, err := url.Parse(artistURL)
	if err != nil || parsed.Hostname() != "open.spotify.com" || !strings.HasPrefix(parsed.Path, "/artist/") {
		return "", errors.New("invalid Spotify artist URL")
	}
	query := url.Values{"url": {artistURL}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.spotify.com/oembed?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var response struct {
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err = m.doSourceJSON(m.client, request, "spotify", &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.ThumbnailURL) == "" {
		return "", errors.New("Spotify oEmbed returned no artist image")
	}
	return strings.TrimSpace(response.ThumbnailURL), nil
}
func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "jp" {
		return "ja"
	}
	if index := strings.IndexAny(value, "-_"); index > 0 {
		value = value[:index]
	}
	return value
}

func languageList(csv string, englishFallback bool) []string {
	var result []string
	for _, value := range strings.Split(csv, ",") {
		value = normalizeLanguage(value)
		if value == "" {
			continue
		}
		found := false
		for _, existing := range result {
			found = found || existing == value
		}
		if !found {
			result = append(result, value)
		}
	}
	if englishFallback {
		found := false
		for _, value := range result {
			found = found || value == "en"
		}
		if !found {
			result = append(result, "en")
		}
	}
	return result
}

func wikidataEntityID(resource string) (string, error) {
	parsed, err := url.Parse(resource)
	if err != nil {
		return "", err
	}
	entityID := strings.TrimSpace(filepath.Base(parsed.Path))
	if !strings.HasPrefix(entityID, "Q") {
		return "", errors.New("wikidata relation has no entity ID")
	}
	return entityID, nil
}
func stripLastFMLink(value string) string {
	if index := strings.Index(value, "<a href="); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}
