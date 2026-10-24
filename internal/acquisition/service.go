// Package acquisition coordinates confirmed torrents and conservative library evidence.
package acquisition

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/acquisition/jackett"
	"github.com/lux032/032music-server/internal/acquisition/qbit"
	"github.com/lux032/032music-server/internal/acquisition/torrent"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
)

const LibraryTimeout = 72 * time.Hour

var ErrRunning = errors.New("acquisition already running")

// Error is a safe application error suitable for HTTP code mapping.
type Error struct{ Code string }

func (e *Error) Error() string  { return e.Code }
func failure(code string) error { return &Error{Code: code} }

// Now must share SQLite wall-clock coordinates in production. Tests may inject
// any epoch but must seed SQLite-owned first-event clocks explicitly when aging them.
type Options struct {
	Gate *sourcegate.Gate
	Now  func() time.Time
	// After permits deterministic lifecycle tests without sleeping.
	After func(time.Duration) <-chan time.Time
}
type Service struct {
	store  *storage.Store
	logger *slog.Logger

	gate    *sourcegate.Gate
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
	mu      sync.Mutex
	running bool
	wg      sync.WaitGroup
	wake    chan struct{}
	// op serializes reconciliation passes only. Confirm and user actions use CAS. Cancellation
	// still takes effect during network calls through the database version CAS.
	op        sync.Mutex
	clientsMu sync.Mutex
	qb        *qbit.Client
	jackett   *jackett.Client
	category  string
	pendingMu sync.Mutex
	pending   map[int64][]byte
	// beforeCreate is an instance-local test seam for the unique-index race.
	beforeCreate func()
}

func New(store *storage.Store, logger *slog.Logger, o Options) *Service {

	if logger == nil {
		logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	return &Service{store: store, logger: logger, gate: o.Gate, now: o.Now, after: o.After, wake: make(chan struct{}, 1), pending: map[int64][]byte{}}
}
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrRunning
	}
	s.running = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { s.pendingMu.Lock(); clear(s.pending); s.pendingMu.Unlock() }()
		defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
		s.reconcile(ctx, true, true)
		nextEvidence := s.now().Add(5 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				s.reconcile(ctx, false, true)
			case <-s.after(30 * time.Second):
				evidence := !s.now().Before(nextEvidence)
				s.reconcile(ctx, false, evidence)
				if evidence {
					nextEvidence = s.now().Add(5 * time.Minute)
				}
			}
		}
	}()
	return nil
}
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Wait() { s.wg.Wait() }
func (s *Service) SettingsChanged() {
	s.clientsMu.Lock()
	s.qb = nil
	s.jackett = nil
	s.clientsMu.Unlock()
	s.Wake()
}
func (s *Service) clients(ctx context.Context) (*qbit.Client, *jackett.Client, string, error) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	if s.qb != nil && s.jackett != nil {
		return s.qb, s.jackett, s.category, nil
	}
	v, e := s.store.AcquisitionSettings(ctx)
	if e != nil {
		return nil, nil, "", e
	}
	c, e := s.store.AcquisitionCredentials(ctx)
	if e != nil {
		return nil, nil, "", e
	}
	q, e := qbit.New(qbit.Config{BaseURL: v.QBURL, Username: v.QBUsername, Password: c.QBPassword})
	if e != nil {
		return nil, nil, "", failure("invalid_settings")
	}
	j, e := jackett.New(jackett.Config{BaseURL: v.JackettURL, APIKey: c.JackettAPIKey, Indexer: v.JackettIndexer}, s.gate, nil)
	if e != nil {
		return nil, nil, "", failure("invalid_settings")
	}
	s.qb = q
	s.jackett = j
	s.category = v.QBCategory
	return q, j, v.QBCategory, nil
}
func (s *Service) enabled(ctx context.Context) error {
	v, e := s.store.AcquisitionSettings(ctx)
	if e != nil {
		return e
	}
	if !v.Enabled {
		return failure("disabled")
	}
	return nil
}

type SearchResult struct {
	Title          string   `json:"title"`
	Category       string   `json:"category"`
	Size           int64    `json:"size"`
	Seeders        *int64   `json:"seeders"`
	Peers          *int64   `json:"peers"`
	Grabs          *int64   `json:"grabs"`
	DownloadFactor *float64 `json:"downloadFactor"`
	PublishedAt    string   `json:"publishedAt"`
	MagnetOnly     bool     `json:"magnetOnly"`
	Badges         []string `json:"badges"`
}
type SearchResponse struct {
	SearchID string         `json:"searchId"`
	Query    string         `json:"query"`
	Results  []SearchResult `json:"results"`
}

func searchView(v storage.AcquisitionSearch) SearchResponse {
	out := SearchResponse{SearchID: v.ID, Query: v.Query, Results: []SearchResult{}}
	for _, r := range v.Results {
		out.Results = append(out.Results, SearchResult{r.Title, r.Category, r.Size, r.Seeders, r.Peers, r.Grabs, r.DownloadFactor, r.PublishedAt, r.MagnetOnly, jackett.Badges(r.Title)})
	}
	return out
}
func (s *Service) Search(ctx context.Context, rgid, q string, force bool) (SearchResponse, error) {
	if e := s.enabled(ctx); e != nil {
		return SearchResponse{}, e
	}
	id, ok := metadata.NormalizeMusicBrainzID(rgid)
	if !ok {
		return SearchResponse{}, failure("invalid_rgid")
	}
	rgid = id
	rg, e := s.store.AcquisitionReleaseGroup(ctx, rgid)
	if e != nil {
		return SearchResponse{}, e
	}
	if strings.TrimSpace(q) == "" {
		q = rg.ArtistCredit + " " + rg.Title
	}
	q = strings.Join(strings.Fields(q), " ")
	settings, e := s.store.AcquisitionSettings(ctx)
	if e != nil {
		return SearchResponse{}, e
	}
	if !force {
		v, e := s.store.CachedAcquisitionSearch(ctx, rgid, settings.JackettIndexer, q)
		if e == nil {
			return searchView(v), nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return SearchResponse{}, e
		}
	}
	_, j, _, e := s.clients(ctx)
	if e != nil {
		return SearchResponse{}, e
	}
	r, e := j.Search(ctx, q)
	if e != nil {
		return SearchResponse{}, e
	}
	v, e := s.store.SaveAcquisitionSearch(ctx, storage.AcquisitionSearch{RGID: rgid, Indexer: settings.JackettIndexer, Query: q, Results: r})
	return searchView(v), e
}
func (s *Service) Confirm(ctx context.Context, rgid, searchID string, resultIdx int, key string) (storage.AcquisitionTask, error) {
	var zero storage.AcquisitionTask
	if e := s.enabled(ctx); e != nil {
		return zero, e
	}
	if e := s.store.IdentityReady(ctx); e != nil {
		return zero, e
	}
	id, ok := metadata.NormalizeMusicBrainzID(rgid)
	if !ok {
		return zero, failure("invalid_rgid")
	}
	rgid = id
	// An exact idempotency replay wins over the active/owned checks caused by itself.
	if key == "" {
		return zero, failure("invalid_idempotency_key")
	}
	if old, e := s.store.AcquisitionTaskByIdempotencyKey(ctx, key); e == nil {
		if old.RGID != rgid {
			return zero, failure("idempotency_conflict")
		}
		return old, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return zero, e
	}
	own, e := s.store.DiscoveryOwnership(ctx, []string{rgid})
	if e != nil {
		return zero, e
	}
	if len(own[rgid].Albums) > 0 {
		return zero, failure("already_owned")
	}
	if own[rgid].Ignored {
		return zero, failure("ignored")
	}
	tasks, e := s.store.ListAcquisitionTasks(ctx)
	if e != nil {
		return zero, e
	}
	for _, t := range tasks {
		if t.RGID == rgid && active(t) {
			return zero, storage.ErrAcquisitionRGActive
		}
	}
	v, e := s.store.AcquisitionSearch(ctx, searchID)
	if e != nil {
		return zero, e
	}
	if v.RGID != rgid || resultIdx < 0 || resultIdx >= len(v.Results) {
		return zero, failure("invalid_result")
	}
	r := v.Results[resultIdx]
	if r.MagnetOnly {
		return zero, jackett.ErrMagnetOnly
	}
	q, j, _, e := s.clients(ctx)
	if e != nil {
		return zero, e
	}
	if e = q.Probe(ctx); e != nil {
		return zero, e
	}
	b, e := j.Download(ctx, r.DownloadRef)
	if e != nil {
		return zero, e
	}
	m, e := torrent.Parse(b)
	if e != nil {
		return zero, e
	}
	if _, err := s.store.ActiveAcquisitionByHash(ctx, m.InfoHash); err == nil {
		return s.hashBound(ctx, m.InfoHash)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	rg, e := s.store.AcquisitionReleaseGroup(ctx, rgid)
	if e != nil {
		return zero, e
	}
	if s.beforeCreate != nil {
		s.beforeCreate()
	}
	t, e := s.store.CreateAcquisitionTask(ctx, storage.AcquisitionTask{RGID: rgid, IdempotencyKey: key, Title: rg.Title, Artist: rg.ArtistCredit, Kind: rg.Kind, TorrentTitle: r.Title, TorrentSize: r.Size, InfoHash: m.InfoHash, DownloadRef: r.DownloadRef})
	if e != nil {
		if errors.Is(e, storage.ErrAcquisitionIgnored) {
			return zero, failure("ignored")
		}
		if errors.Is(e, storage.ErrAcquisitionOwned) {
			return zero, failure("already_owned")
		}
		if errors.Is(e, storage.ErrAcquisitionHashBound) {
			return s.hashBound(ctx, m.InfoHash)
		}
		return zero, e
	}
	s.pendingMu.Lock()
	s.pending[t.ID] = b
	s.pendingMu.Unlock()
	s.Wake()
	return t, nil
}
func active(t storage.AcquisitionTask) bool { return t.State != "IN_LIBRARY" && t.State != "CANCELLED" }

// userUpdate retries only stale active rows; terminal decisions are never undone.
func (s *Service) userUpdate(ctx context.Context, id string, fn func(storage.AcquisitionTask) (storage.AcquisitionTask, error)) (storage.AcquisitionTask, error) {
	var t storage.AcquisitionTask
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		t, err = s.store.AcquisitionTaskByPublicID(ctx, id)
		if err != nil {
			return t, err
		}
		if !active(t) {
			return t, storage.ErrAcquisitionStale
		}
		t, err = fn(t)
		if !errors.Is(err, storage.ErrAcquisitionStale) {
			return t, err
		}
	}
	return t, err
}
func (s *Service) Cancel(ctx context.Context, id string) (storage.AcquisitionTask, error) {
	t, err := s.userUpdate(ctx, id, func(t storage.AcquisitionTask) (storage.AcquisitionTask, error) {
		t.State = "CANCELLED"
		return s.store.CASAcquisitionTask(ctx, t)
	})
	if err == nil {
		s.dropPending(t.ID)
	}
	return t, err
}
func (s *Service) Retry(ctx context.Context, id string) (storage.AcquisitionTask, error) {
	t, err := s.userUpdate(ctx, id, func(t storage.AcquisitionTask) (storage.AcquisitionTask, error) {
		if t.State != "ATTENTION" || !(strings.HasPrefix(t.AttentionReason, "qb_") || strings.HasPrefix(t.AttentionReason, "torrent_")) {
			return t, failure("not_retryable")
		}
		t.State = "QB_SUBMITTING"
		t.AttentionReason = ""
		t.AddOutcome = ""
		t.NextCheckAt = ""
		t.QBMissingSince = ""
		if t.DownloadRef == "" {
			t.State = "DOWNLOADING"
		}
		t.QBUnreachableSince = ""
		t.AddUnknownSince = ""
		t.SubmitAttempts = 0
		return s.store.CASAcquisitionTask(ctx, t)
	})
	if err == nil {
		s.Wake()
	}
	return t, err
}
func (s *Service) Link(ctx context.Context, id string, albumID int64, confirmReplace bool) (storage.AcquisitionTask, error) {
	return s.link(ctx, id, albumID, confirmReplace, "manual")
}
func (s *Service) MarkOwned(ctx context.Context, id string, albumID int64, confirmReplace bool) (storage.AcquisitionTask, error) {
	return s.link(ctx, id, albumID, confirmReplace, "owned")
}
func (s *Service) link(ctx context.Context, id string, albumID int64, confirmReplace bool, source string) (storage.AcquisitionTask, error) {
	return s.userUpdate(ctx, id, func(t storage.AcquisitionTask) (storage.AcquisitionTask, error) {
		return s.store.LinkAcquisition(ctx, t, albumID, confirmReplace, source)
	})
}
func (s *Service) reconcile(ctx context.Context, restart, evidence bool) {
	s.op.Lock()
	defer s.op.Unlock()
	if ctx.Err() != nil {
		return
	}
	tasks, e := s.store.ListAcquisitionTasks(ctx)
	if e != nil {
		return
	}
	_ = s.store.CleanupAcquisitionSearches(ctx)
	for _, t := range tasks {
		if ctx.Err() != nil {
			return
		}
		if !active(t) {
			continue
		}
		checkEvidence := evidence || (t.State == "AWAITING_LIBRARY" && elapsed(s.now(), t.DownloadCompletedAt) >= LibraryTimeout) || (t.State == "DOWNLOADING" && elapsed(s.now(), t.QBMissingSince) >= LibraryTimeout)
		if checkEvidence && t.State != "QB_SUBMITTING" {
			files, e := s.store.AcquisitionFileEvidence(ctx, t)
			if errors.Is(e, storage.ErrIdentityNotReady) {
				continue
			}
			if e != nil {
				continue
			}
			if len(files) > 0 {
				_, _ = s.store.CompleteAcquisitionWithEvidence(ctx, t, storage.AcquisitionEvidence{Source: "file", AlbumID: files[0].AlbumID, AudioFileID: files[0].AudioFileID})
				continue
			}
		}
		if t.State == "ATTENTION" {
			s.dropPending(t.ID)
			continue
		}
		if t.State == "AWAITING_LIBRARY" {
			if elapsed(s.now(), t.DownloadCompletedAt) >= LibraryTimeout {
				reason, e := s.store.DiagnoseAcquisition(ctx, t)
				if e == nil {
					s.attention(ctx, t, reason)
				}
			}
			continue
		}
		if !restart && t.NextCheckAt != "" && s.now().Before(parseTime(t.NextCheckAt)) {
			continue
		}
		s.poll(ctx, t)
	}
	// Only retire entries positively identified by this snapshot. Confirm may
	// have inserted new entries while the network work above was in flight.
	for _, t := range tasks {
		if t.State != "QB_SUBMITTING" {
			s.dropPending(t.ID)
		}
	}
}
func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func elapsed(now time.Time, v string) time.Duration {
	if v == "" {
		return 0
	}
	return now.Sub(parseTime(v))
}
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func (s *Service) save(ctx context.Context, t storage.AcquisitionTask) {
	_, err := s.store.CASAcquisitionTask(ctx, t)
	if err != nil && !errors.Is(err, storage.ErrAcquisitionStale) && ctx.Err() == nil {
		s.logger.Warn("acquisition state write deferred", "task", t.PublicID)
	}
}
func (s *Service) attention(ctx context.Context, t storage.AcquisitionTask, reason string) {
	t.State = "ATTENTION"
	t.AttentionReason = reason
	t.NextCheckAt = ""
	s.save(ctx, t)
	s.dropPending(t.ID)
}
func (s *Service) qbError(ctx context.Context, t storage.AcquisitionTask, e error) {
	s.logger.Warn("acquisition qB check deferred", "task", t.PublicID, "authentication", errors.Is(e, qbit.ErrAuth))
	if errors.Is(e, qbit.ErrAuth) {
		s.attention(ctx, t, "qb_auth")
		return
	}
	if t.QBUnreachableSince == "" {
		t.QBUnreachableSince = stamp(s.now())
	}
	if elapsed(s.now(), t.QBUnreachableSince) >= 30*time.Minute {
		s.attention(ctx, t, "qb_unreachable")
		return
	}
	delay := 30 * time.Second
	age := elapsed(s.now(), t.QBUnreachableSince)
	if age >= 30*time.Second {
		delay = time.Minute
	}
	if age >= 90*time.Second {
		delay = 2 * time.Minute
	}
	if age >= 3*time.Minute {
		delay = 5 * time.Minute
	}
	t.NextCheckAt = stamp(s.now().Add(delay))
	s.save(ctx, t)
}
func (s *Service) found(ctx context.Context, t storage.AcquisitionTask, q *qbit.Client, r qbit.Torrent) {
	has := false
	for _, tag := range strings.Split(r.Tags, ",") {
		if strings.TrimSpace(tag) == qbit.Tag(t.PublicID) {
			has = true
		}
	}
	if !has {
		if e := q.AddTags(ctx, t.InfoHash, qbit.Tag(t.PublicID)); e != nil {
			s.qbError(ctx, t, e)
			return
		}
	}
	t.QBSeenAt = stamp(s.now())
	t.QBMissingSince = ""
	t.QBUnreachableSince = ""
	t.AddUnknownSince = ""
	t.NextCheckAt = stamp(s.now().Add(time.Minute))
	t.State = "DOWNLOADING"
	if qbit.Complete(r) {
		t.State = "AWAITING_LIBRARY"
		t.DownloadCompletedAt = stamp(s.now())
	}
	t.DownloadRef = ""
	s.save(ctx, t)
	s.dropPending(t.ID)
}
func (s *Service) poll(ctx context.Context, t storage.AcquisitionTask) {
	q, j, category, e := s.clients(ctx)
	if e != nil {
		s.qbError(ctx, t, e)
		return
	}
	// Always look up the hash before adding, especially after restart or an unknown add.
	rows, e := q.Info(ctx, t.InfoHash, "")
	if e != nil {
		s.qbError(ctx, t, e)
		return
	}
	if len(rows) > 0 {
		s.found(ctx, t, q, rows[0])
		return
	}
	t.QBUnreachableSince = ""
	if t.State == "DOWNLOADING" {
		t.NextCheckAt = stamp(s.now().Add(time.Minute))
		if t.QBMissingSince == "" {
			t.QBMissingSince = stamp(s.now())
		}
		if elapsed(s.now(), t.QBMissingSince) >= LibraryTimeout {
			s.attention(ctx, t, "qb_missing")
		} else {
			s.save(ctx, t)
		}
		return
	}
	if t.AddOutcome == "unknown" && t.SubmitAttempts > 0 {

		if elapsed(s.now(), t.AddUnknownSince) < 2*time.Minute {
			delay := 2 * time.Minute
			if elapsed(s.now(), t.AddUnknownSince) < 90*time.Second {
				delay = time.Minute
			}
			if elapsed(s.now(), t.AddUnknownSince) < 30*time.Second {
				delay = 30 * time.Second
			}
			t.NextCheckAt = stamp(s.now().Add(delay))
			s.save(ctx, t)
			return
		}
	}
	if t.SubmitAttempts >= 2 {
		s.attention(ctx, t, "qb_rejected")
		return
	}
	if t.DownloadRef == "" {
		t.NextCheckAt = stamp(s.now().Add(time.Minute))
		s.save(ctx, t)
		return
	}

	s.pendingMu.Lock()
	b := s.pending[t.ID]
	delete(s.pending, t.ID)
	s.pendingMu.Unlock()
	if len(b) == 0 {
		b, e = j.Download(ctx, t.DownloadRef)
		if e != nil {
			s.downloadError(ctx, t, e)
			return
		}
		m, e := torrent.Parse(b)
		if e != nil || m.InfoHash != t.InfoHash {
			s.attention(ctx, t, "torrent_unavailable")
			return
		}
	}
	// SQLite resets the unknown clock atomically when the attempt increments.
	// Persist the attempt BEFORE network I/O: a crash must reconcile, not blindly add.
	t.SubmitAttempts++
	t.AddOutcome = "unknown"
	if t.AddUnknownSince == "" {
		t.AddUnknownSince = stamp(s.now())
	}
	t.NextCheckAt = stamp(s.now().Add(30 * time.Second))
	t, e = s.store.CASAcquisitionTask(ctx, t)
	if e != nil {
		return
	}
	outcome, addErr := q.AddTorrent(ctx, b, t.PublicID, category)
	s.logger.Info("acquisition submitted", "task", t.PublicID, "outcome", string(outcome))
	rows, infoErr := q.Info(ctx, t.InfoHash, "")
	if infoErr == nil && len(rows) > 0 {
		s.found(ctx, t, q, rows[0])
		return
	}
	if errors.Is(addErr, qbit.ErrAuth) || errors.Is(infoErr, qbit.ErrAuth) {
		s.attention(ctx, t, "qb_auth")
		return
	}
	if infoErr != nil {
		s.qbError(ctx, t, infoErr)
		return
	}
	if outcome == qbit.AddRejected {
		s.attention(ctx, t, "qb_rejected")
		return
	}
	// Even an Ok response without an observable hash remains unknown.
	t.AddOutcome = "unknown"
	s.save(ctx, t)
}

// TestJackett tests the currently saved connection without exposing credentials.
func (s *Service) TestJackett(ctx context.Context) error {
	_, j, _, err := s.clients(ctx)
	if err != nil {
		return err
	}
	_, err = j.Search(ctx, "032 acquisition connection test")
	return err
}

// TestQB verifies authentication and that the configured category exists.
func (s *Service) TestQB(ctx context.Context) error {
	q, _, category, err := s.clients(ctx)
	if err != nil {
		return err
	}
	if err = q.Probe(ctx); err != nil {
		return err
	}
	categories, err := q.Categories(ctx)
	if err != nil {
		return err
	}
	if category != "" {
		if _, ok := categories[category]; !ok {
			return failure("category_missing")
		}
	}
	return nil
}

func (s *Service) dropPending(id int64) {
	s.pendingMu.Lock()
	delete(s.pending, id)
	s.pendingMu.Unlock()
}
func (s *Service) downloadError(ctx context.Context, t storage.AcquisitionTask, err error) {
	if ctx.Err() != nil {
		return
	}
	delay := time.Minute
	transient := errors.Is(err, sourcegate.ErrRateLimited) || errors.Is(err, context.DeadlineExceeded)
	var cooldown *sourcegate.CooldownError
	if errors.As(err, &cooldown) {
		transient = true
		delay = cooldown.RetryAfter
	}
	var h *jackett.HTTPError
	if errors.As(err, &h) && (h.Status == 429 || h.Status >= 500 && h.Status < 600) {
		transient = true
		delay = h.RetryAfter
	}
	var api *jackett.APIError
	if errors.As(err, &api) && (api.Code == 500 || api.Code == 501) {
		transient = true
		delay = api.RetryAfter
	}
	var network net.Error
	if errors.As(err, &network) {
		transient = true
	}
	var classified interface{ NetworkFailure() bool }
	if errors.As(err, &classified) {
		transient = classified.NetworkFailure() || errors.Is(err, context.DeadlineExceeded)
	}
	if !transient {
		s.attention(ctx, t, "torrent_unavailable")
		return
	}
	if delay <= 0 {
		delay = time.Minute
	}
	t.NextCheckAt = stamp(s.now().Add(delay))
	s.save(ctx, t)
}

// hashBound returns only a still-active binding; callers must retain the conflict.
func (s *Service) hashBound(ctx context.Context, hash string) (storage.AcquisitionTask, error) {
	t, err := s.store.ActiveAcquisitionByHash(ctx, hash)
	if err != nil {
		t = storage.AcquisitionTask{}
	}
	return t, storage.ErrAcquisitionHashBound
}
