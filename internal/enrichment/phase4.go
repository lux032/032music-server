package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

type phase4Endpoints struct {
	VGMdbSearch string
	VGMdbAlbum  string
	Bangumi     string
	MusicBrainz string
}

func defaultPhase4Endpoints() phase4Endpoints {
	return phase4Endpoints{
		VGMdbSearch: "https://vgmdb.info/search/%s?format=json",
		VGMdbAlbum:  "https://vgmdb.info/album/%s?format=json",
		Bangumi:     "https://api.bgm.tv/v0/search/subjects",
		MusicBrainz: "https://musicbrainz.org/ws/2",
	}
}

var catalogNonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

// NormalizeCatalogNumber creates a stable exact-match key without making a
// fuzzy title decision. It intentionally ignores punctuation and ASCII case.
func NormalizeCatalogNumber(value string) string {
	return catalogNonAlnum.ReplaceAllString(strings.ToUpper(strings.TrimSpace(value)), "")
}

func normalizeRunRequest(value RunRequest) (RunRequest, error) {
	value.Scope = strings.ToLower(strings.TrimSpace(value.Scope))
	if value.Scope == "" {
		value.Scope = "all"
	}
	switch value.Scope {
	case "all":
		value.TargetID = 0
	case "album", "work", "artist":
		if value.TargetID <= 0 {
			return value, fmt.Errorf("target id is required for %s scope", value.Scope)
		}
	default:
		return value, fmt.Errorf("unsupported enrichment scope %q", value.Scope)
	}
	return value, nil
}

// StartRun creates an observable background run. Only one Phase 4 run may be
// active in a process; artist identity matching keeps its existing lock.
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
	items, err := m.phase4Items(ctx, request)
	if err != nil {
		return storage.EnrichmentRun{}, err
	}
	run, err := m.store.CreateEnrichmentRun(ctx, request.Scope, request.TargetID, request.Force, len(items))
	if err != nil {
		return storage.EnrichmentRun{}, err
	}
	m.phaseRunning = true
	m.goBackground("metadata-enrichment", func() { m.executePhase4Run(run.ID, request, items) })
	return run, nil
}

type phase4Item struct {
	kind   string
	album  storage.AlbumEnrichmentTarget
	work   storage.WorkEnrichmentTarget
	artist storage.ArtistMatchInput
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
	if request.Scope == "all" || request.Scope == "album" {
		if enabled["vgmdb"] {
			albums, e := m.store.AlbumsForEnrichment(ctx, "vgmdb", request.Force, 1000)
			if e != nil {
				return nil, e
			}
			for _, album := range albums {
				if request.TargetID == 0 || request.TargetID == album.ID {
					result = append(result, phase4Item{kind: "album", album: album})
				}
			}
		}
	}
	if request.Scope == "all" || request.Scope == "work" {
		if enabled["bangumi"] {
			works, e := m.store.WorksForEnrichment(ctx, "bangumi", request.Force, 1000)
			if e != nil {
				return nil, e
			}
			for _, work := range works {
				if request.TargetID == 0 || request.TargetID == work.ID {
					result = append(result, phase4Item{kind: "work", work: work})
				}
			}
		}
	}
	if request.Scope == "all" || request.Scope == "artist" {
		if enabled["musicbrainz"] {
			artists, e := m.store.ArtistsForMatching(ctx)
			if e != nil {
				return nil, e
			}
			for _, artist := range artists {
				if request.TargetID == 0 || request.TargetID == artist.ID {
					result = append(result, phase4Item{kind: "artist", artist: artist})
				}
			}
		}
	}
	return result, nil
}

func (m *Manager) executePhase4Run(runID int64, request RunRequest, items []phase4Item) {
	defer func() { m.phaseMu.Lock(); m.phaseRunning = false; m.phaseMu.Unlock() }()
	ctx := m.baseCtx
	counts := storage.EnrichmentRunUpdate{Total: len(items)}
	for _, item := range items {
		if ctx.Err() != nil {
			_ = m.store.FinishEnrichmentRun(context.Background(), runID, "failed", "cancelled by shutdown")
			return
		}
		var outcome string
		var err error
		switch item.kind {
		case "album":
			counts.Current = item.album.Title
			outcome, err = m.enrichVGMdbAlbum(ctx, runID, item.album, request.Force)
		case "work":
			counts.Current = item.work.Title
			outcome, err = m.enrichBangumiWork(ctx, runID, item.work, request.Force)
		case "artist":
			counts.Current = item.artist.Name
			outcome, err = m.enrichMusicBrainzRelations(ctx, item.artist, request.Force)
		}
		counts.Processed++
		if err != nil {
			counts.Failed++
			counts.ErrorMessage = err.Error()
			m.logger.Warn("metadata enrichment item failed", "kind", item.kind, "current", counts.Current, "error", err)
		} else {
			switch outcome {
			case "review":
				counts.Review++
			case "skipped":
				counts.Skipped++
			default:
				counts.Succeeded++
			}
		}
		_ = m.store.UpdateEnrichmentRun(ctx, runID, counts)
	}
	status := "completed"
	message := ""
	if len(items) > 0 && counts.Failed == len(items) {
		status, message = "failed", counts.ErrorMessage
	}
	_ = m.store.FinishEnrichmentRun(ctx, runID, status, message)
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
	// MusicBrainz enforces 1 req/s; cached responses don't hit the network
	// but every live request must respect the limiter regardless of which
	// code path issues it.
	if req.URL != nil && strings.EqualFold(req.URL.Hostname(), "musicbrainz.org") {
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
	return resp.StatusCode, json.Unmarshal(raw, target)
}

type vgmdbSearchResponse struct {
	Results struct {
		Albums []struct{ Link, Catalog, Name string } `json:"albums"`
	} `json:"results"`
}
type vgmdbAlbumResponse struct {
	Link, Catalog, Name, ReleaseDate string
	Organizations                    []struct{ Names map[string]string } `json:"organizations"`
	Discs                            []struct {
		Tracks []struct {
			Names   map[string]string `json:"names"`
			Credits []struct {
				Role  string            `json:"role"`
				Name  string            `json:"name"`
				Names map[string]string `json:"names"`
			} `json:"credits"`
		} `json:"tracks"`
	} `json:"discs"`
	Notes string `json:"notes"`
}

func (m *Manager) enrichVGMdbAlbum(ctx context.Context, runID int64, album storage.AlbumEnrichmentTarget, force bool) (string, error) {
	catalog := NormalizeCatalogNumber(album.CatalogNumber)
	if catalog == "" {
		return "skipped", nil
	}
	setting, err := m.store.MetadataSourceSetting(ctx, "vgmdb")
	if err != nil || !setting.Enabled {
		return "skipped", err
	}
	ep := m.phaseEndpoints
	var search vgmdbSearchResponse
	_, err = m.cachedJSON(ctx, "vgmdb", "search:"+catalog, fmt.Sprintf(ep.VGMdbSearch, url.PathEscape(album.CatalogNumber)), setting, force, nil, &search)
	if errors.Is(err, sql.ErrNoRows) {
		return "skipped", nil
	}
	if err != nil {
		return "", err
	}
	matchID := ""
	for _, value := range search.Results.Albums {
		if NormalizeCatalogNumber(value.Catalog) == catalog {
			matchID = strings.Trim(strings.TrimPrefix(value.Link, "album/"), "/")
			break
		}
	}
	if matchID == "" {
		return "skipped", nil
	}
	var detail vgmdbAlbumResponse
	_, err = m.cachedJSON(ctx, "vgmdb", "album:"+matchID, fmt.Sprintf(ep.VGMdbAlbum, url.PathEscape(matchID)), setting, force, nil, &detail)
	if err != nil {
		return "", err
	}
	if NormalizeCatalogNumber(detail.Catalog) != catalog {
		return "", errors.New("VGMdb catalog mismatch")
	}
	label := ""
	if len(detail.Organizations) > 0 {
		label = firstMapValue(detail.Organizations[0].Names)
	}
	raw, _ := json.Marshal(detail)
	profile := storage.ExternalAlbumProfile{Source: "vgmdb", ExternalID: matchID, PageURL: "https://vgmdb.net/album/" + matchID, Title: detail.Name, ReleaseDate: detail.ReleaseDate, CatalogNumber: detail.Catalog, Label: label, DiscCount: len(detail.Discs), Raw: raw}
	if err = m.store.UpsertExternalAlbumProfile(ctx, album.ID, profile); err != nil {
		return "", err
	}
	year := 0
	if len(detail.ReleaseDate) >= 4 {
		year, _ = strconv.Atoi(detail.ReleaseDate[:4])
	}
	if err = m.store.ConservativelyFillAlbum(ctx, album.ID, storage.AlbumFieldPatch{ReleaseDate: detail.ReleaseDate, ReleaseYear: year, CatalogNumber: detail.Catalog, Label: label, DiscCount: len(detail.Discs)}, "vgmdb", matchID, runID); err != nil {
		return "", err
	}
	// The VGMdb JSON mirror may expose credits per track. Match only by exact
	// disc/track position from the local album; missing or ambiguous positions
	// are deliberately ignored rather than guessed by title.
	var credits []storage.EnrichmentCredit
	localTracks, trackErr := m.store.ListTracks(ctx, storage.Filters{AlbumID: album.ID, Limit: 1000})
	if trackErr == nil {
		byPosition := make(map[string]int64, len(localTracks))
		for _, track := range localTracks {
			byPosition[fmt.Sprintf("%d:%d", track.DiscNumber, track.TrackNumber)] = track.ID
		}
		for discIndex, disc := range detail.Discs {
			for trackIndex, remoteTrack := range disc.Tracks {
				trackID := byPosition[fmt.Sprintf("%d:%d", discIndex+1, trackIndex+1)]
				if trackID == 0 {
					continue
				}
				for position, credit := range remoteTrack.Credits {
					name := strings.TrimSpace(credit.Name)
					if name == "" {
						name = firstMapValue(credit.Names)
					}
					role := storage.NormalizeTrackArtistRole(credit.Role)
					if name == "" || role == "" {
						continue
					}
					credits = append(credits, storage.EnrichmentCredit{TrackID: trackID, Name: name, Role: role, Position: position, ExternalID: matchID})
				}
			}
		}
	}
	if len(credits) > 0 {
		if err = m.store.AddEnrichmentCredits(ctx, album.ID, credits, "vgmdb", runID); err != nil {
			return "", err
		}
	}
	return "succeeded", nil
}

func firstMapValue(values map[string]string) string {
	for _, key := range []string{"ja", "en", "romaji"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	for _, value := range values {
		return value
	}
	return ""
}

type bangumiSearchResponse struct {
	Data []struct {
		ID                 int64 `json:"id"`
		Type               int   `json:"type"`
		Name, NameCN, Date string
		Images             struct{ Large, Common string } `json:"images"`
	} `json:"data"`
}

func scoreBangumi(work storage.WorkEnrichmentTarget, name, translated, date string, subjectType int) (int, []string) {
	score := 0
	var evidence []string
	normalize := func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	}
	wt, wtr := normalize(work.Title), normalize(work.TranslatedTitle)
	if wt != "" && wt == normalize(name) {
		score += 70
		evidence = append(evidence, "原文标题精确匹配")
	} else if wt != "" && (strings.Contains(normalize(name), wt) || strings.Contains(wt, normalize(name))) {
		score += 35
	}
	if wtr != "" && wtr == normalize(translated) {
		score += 20
		evidence = append(evidence, "译名精确匹配")
	}
	if work.Type == "anime" && subjectType == 2 {
		score += 10
		evidence = append(evidence, "动画类型一致")
	}
	if work.Year > 0 && len(date) >= 4 {
		if year, _ := strconv.Atoi(date[:4]); year == work.Year {
			score += 10
			evidence = append(evidence, "年份一致")
		} else {
			score -= 20
		}
	}
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return score, evidence
}

func (m *Manager) enrichBangumiWork(ctx context.Context, runID int64, work storage.WorkEnrichmentTarget, force bool) (string, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil || !setting.Enabled {
		return "skipped", err
	}
	body := map[string]any{"keyword": work.Title, "filter": map[string]any{"type": []int{2}}}
	var response bangumiSearchResponse
	_, err = m.cachedJSON(ctx, "bangumi", "search:"+url.QueryEscape(work.Title), m.phaseEndpoints.Bangumi, setting, force, body, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return "skipped", nil
	}
	if err != nil {
		return "", err
	}
	var candidates []storage.WorkMatchCandidate
	for _, v := range response.Data {
		score, evidence := scoreBangumi(work, v.Name, v.NameCN, v.Date, v.Type)
		raw, _ := json.Marshal(v)
		poster := v.Images.Large
		if poster == "" {
			poster = v.Images.Common
		}
		year := 0
		if len(v.Date) >= 4 {
			year, _ = strconv.Atoi(v.Date[:4])
		}
		candidates = append(candidates, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: strconv.FormatInt(v.ID, 10), Title: v.Name, TranslatedTitle: v.NameCN, Type: "anime", Year: year, PageURL: "https://bgm.tv/subject/" + strconv.FormatInt(v.ID, 10), PosterURL: poster, Score: score, Evidence: evidence, Payload: raw})
	}
	if err = m.store.ReplaceWorkMatchCandidates(ctx, work.ID, candidates); err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "skipped", nil
	}
	stored, err := m.store.WorkMatchCandidates(ctx, work.ID)
	if err != nil {
		return "", err
	}
	if len(stored) > 0 && stored[0].Score >= 90 {
		if err = m.store.ConfirmWorkMatchCandidate(ctx, work.ID, stored[0].ID, runID); err != nil {
			return "", err
		}
		return "succeeded", nil
	}
	return "review", nil
}

func (m *Manager) enrichMusicBrainzRelations(ctx context.Context, artist storage.ArtistMatchInput, force bool) (string, error) {
	mbid, err := m.store.ArtistExternalID(ctx, artist.ID, "musicbrainz")
	if errors.Is(err, sql.ErrNoRows) || mbid == "" {
		return "skipped", nil
	}
	if err != nil {
		return "", err
	}
	setting, err := m.store.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil || !setting.Enabled {
		return "skipped", err
	}
	var response struct {
		ID        string
		Relations []struct {
			Type, Direction string
			Artist          *struct{ ID, Name string } `json:"artist"`
		} `json:"relations"`
	}
	endpoint := m.phaseEndpoints.MusicBrainz + "/artist/" + url.PathEscape(mbid) + "?inc=artist-rels&fmt=json"
	_, err = m.cachedJSON(ctx, "musicbrainz", "artist-relations:"+mbid, endpoint, setting, force, nil, &response)
	if err != nil {
		return "", err
	}
	var candidates []storage.ArtistRelationCandidate
	for _, relation := range response.Relations {
		if relation.Artist == nil || relation.Artist.ID == "" {
			continue
		}
		relationType := strings.ToLower(strings.TrimSpace(relation.Type))
		if relationType != "member of band" && relationType != "is person" && relationType != "collaboration" && relationType != "voice of" {
			continue
		}
		raw, _ := json.Marshal(relation)
		candidates = append(candidates, storage.ArtistRelationCandidate{Source: "musicbrainz", ExternalID: mbid, RelatedExternalID: relation.Artist.ID, RelatedName: relation.Artist.Name, RelationType: relationType, Direction: relation.Direction, Score: 85, Evidence: []string{"MusicBrainz artist relationship"}, Payload: raw})
	}
	if err = m.store.ReplaceArtistRelationCandidates(ctx, artist.ID, candidates); err != nil {
		return "", err
	}
	if len(candidates) > 0 {
		return "review", nil
	}
	return "skipped", nil
}

// StripVGMdbNotes is intentionally conservative and useful for provider tests.
func StripVGMdbNotes(value string) string {
	return strings.TrimSpace(html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(value, " ")))
}
