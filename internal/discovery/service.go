package discovery

import (
	"context"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/discovery/mb"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/netfetch"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrRunning = errors.New("discovery refresh already running")

type Options struct {
	Bangumi                    BangumiSource
	BaseContext                context.Context
	Gate                       *sourcegate.Gate
	HTTPClient                 *http.Client
	MBBaseURL, CAAURL, DataDir string
	Now                        func() time.Time
	UserAgent                  func(context.Context) string
	// CooldownRetries is how many source cooldowns one refresh waits out
	// before failing (0 = default, negative = fail on the first cooldown).
	CooldownRetries int
	// Sleep waits for a cooldown; tests inject a fake.
	Sleep func(context.Context, time.Duration) error
	// CoverBackfill downloads missing covers in the background at startup and
	// after a re-rank (production only; tests keep downloads explicit).
	CoverBackfill bool
}

// DefaultCooldownRetries lets a long refresh survive transient MusicBrainz /
// Bangumi throttling (shared with metadata enrichment) instead of discarding
// the whole round; waits are capped by sourcegate's Retry-After ceiling.
const DefaultCooldownRetries = 12

type Service struct {
	bangumi      BangumiSource
	baseCtx      context.Context
	store        *storage.Store
	logger       *slog.Logger
	mb           *mb.Client
	images       *http.Client
	gate         *sourcegate.Gate
	now          func() time.Time
	ua           func(context.Context) string
	dataDir, caa string
	legacyDir    string
	autoCovers   bool
	coverBusy    atomic.Bool
	cooldowns    int
	sleep        func(context.Context, time.Duration) error
	mu           sync.Mutex
	running      bool
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

func New(store *storage.Store, logger *slog.Logger, o Options) *Service {
	if o.BaseContext == nil {
		o.BaseContext = context.Background()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Gate == nil {
		o.Gate = sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 2 * time.Second, "coverartarchive": time.Second}})
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if o.UserAgent == nil {
		o.UserAgent = func(context.Context) string {
			return "032-Music-Server/dev (https://github.com/lux032/032music-server)"
		}
	}
	if o.MBBaseURL == "" {
		o.MBBaseURL = sourcegate.BaseURLFromEnv(logger, "MUSIC_SERVER_MUSICBRAINZ_BASE_URL", "https://musicbrainz.org/ws/2")
	}
	if o.CAAURL == "" {
		o.CAAURL = sourcegate.BaseURLFromEnv(logger, "MUSIC_SERVER_COVERART_BASE_URL", "https://coverartarchive.org")
	}
	if o.CooldownRetries == 0 {
		o.CooldownRetries = DefaultCooldownRetries
	}
	if o.Sleep == nil {
		o.Sleep = sleepContext
	}
	return &Service{cooldowns: o.CooldownRetries, sleep: o.Sleep, bangumi: o.Bangumi, baseCtx: o.BaseContext, store: store, logger: logger, mb: &mb.Client{BaseURL: o.MBBaseURL, HTTP: o.HTTPClient, Gate: o.Gate, UA: o.UserAgent, Now: o.Now, Logger: logger}, images: o.HTTPClient, gate: o.Gate, now: o.Now, ua: o.UserAgent, dataDir: filepath.Join(o.DataDir, coverCacheDir), legacyDir: filepath.Join(o.DataDir, "discovery-covers"), autoCovers: o.CoverBackfill, caa: o.CAAURL}
}

// coverCacheDir holds 500px Cover Art Archive thumbnails. The directory name
// is versioned so covers cached at the old 250px size are fetched again.
const coverCacheDir = "discovery-covers-500"

// coverMissingRetry lets a release group without CAA art be checked again,
// since artwork is often uploaded after release.
const coverMissingRetry = 30 * 24 * time.Hour

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// withCooldown retries fn after waiting out source cooldowns, up to the
// service's per-call retry limit. Other errors are returned unchanged.
func withCooldown[T any](ctx context.Context, s *Service, fn func() (T, error)) (T, error) {
	return withCooldownLimit(ctx, s, s.cooldowns, fn)
}

// workCooldownRetries bounds waits per call in the optional works stage so a
// throttled MusicBrainz search defers the rest instead of stalling the round.
const workCooldownRetries = 2

func isCooldown(e error) bool {
	var cd *sourcegate.CooldownError
	return errors.As(e, &cd)
}

func withCooldownLimit[T any](ctx context.Context, s *Service, limit int, fn func() (T, error)) (T, error) {
	if limit > s.cooldowns {
		limit = s.cooldowns
	}
	for attempt := 0; ; attempt++ {
		v, e := fn()
		var cd *sourcegate.CooldownError
		if e == nil || !errors.As(e, &cd) || attempt >= limit {
			return v, e
		}
		wait := cd.RetryAfter
		if wait <= 0 {
			wait = time.Minute
		}
		wait += time.Second
		s.logger.Info("discovery waiting for source cooldown", "source", cd.Source, "wait", wait.String())
		if err := s.sleep(ctx, wait); err != nil {
			return v, err
		}
	}
}

func (s *Service) Status(ctx context.Context) (storage.DiscoveryStatus, error) {
	return s.store.DiscoveryStatus(ctx)
}

// Start resumes only an interrupted running generation, never a completed/failed one.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrRunning
	}
	s.baseCtx = ctx
	v, e := s.store.DiscoveryStatus(ctx)
	if e != nil {
		return e
	}
	if v.Status != "running" {
		if s.autoCovers && v.Status == "completed" && s.store.IdentityReady(ctx) == nil {
			s.BackfillCovers()
		}
		return nil
	}
	if e = s.store.IdentityReady(ctx); e != nil {
		return e
	}
	s.launch(ctx, v.Generation)
	return nil
}

// Refresh uses ctx only for synchronous storage calls. Background work uses
// Options.BaseContext, or the lifecycle context most recently supplied to Start.
func (s *Service) Refresh(ctx context.Context, force bool) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return 0, ErrRunning
	}
	seeds, e := s.store.DiscoverySeedSnapshot(ctx)
	if e != nil {
		return 0, e
	}
	filtered := seeds[:0]
	for _, v := range seeds {
		if !mb.IsSpecialArtist(v.MBID) {
			filtered = append(filtered, v)
		}
	}
	works, e := s.workSnapshot(ctx)
	if e != nil {
		return 0, e
	}
	g, e := s.store.BeginDiscoveryRefresh(ctx, filtered, force, s.now(), works)
	if e != nil {
		return 0, e
	}
	s.launch(s.baseCtx, g)
	return g, nil
}

// The worker uses the base context saved by New/Start, never the request context.
func (s *Service) launch(ctx context.Context, g int64) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.running = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { s.mu.Lock(); s.running = false; s.cancel = nil; s.mu.Unlock() }()
		e := s.execute(ctx, g)
		if e == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(e, storage.ErrDiscoveryStale) {
			state, err := s.store.DiscoveryStatus(ctx)
			if err == nil && state.Generation == g {
				_ = s.store.FinishDiscovery(ctx, g, "failed", "艺人或作品身份已变化，请重新刷新")
			}
			return
		}
		message := "发现目录刷新失败"
		var cd *sourcegate.CooldownError
		if errors.As(e, &cd) {
			n := int((cd.RetryAfter + time.Minute - 1) / time.Minute)
			if n < 1 {
				n = 1
			}
			name := "MusicBrainz"
			if cd.Source == "bangumi" {
				name = "Bangumi"
			}
			if cd.Source == "coverartarchive" {
				name = "Cover Art Archive"
			}
			message = fmt.Sprintf("%s 限流，约 %d 分钟后可重试", name, n)
		}
		if err := s.store.FinishDiscovery(ctx, g, "failed", message); err != nil {
			s.logger.Error("finish discovery refresh", "error", err)
		}
		s.logger.Warn("discovery refresh stopped", "error", e)
	}()
}

// Cancel is a storage no-op outside running status; it still cancels any
// in-memory cover work without rewriting a completed result.
func (s *Service) Cancel(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.store.CancelDiscovery(ctx); e != nil {
		return e
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}
func (s *Service) Wait() { s.wg.Wait() }
func (s *Service) execute(ctx context.Context, g int64) error {
	seeds, e := s.store.DiscoverySeeds(ctx, g)
	if e != nil {
		return e
	}
	artistThrottled := false
	for _, seed := range seeds {
		if e = ctx.Err(); e != nil {
			return e
		}
		if seed.Status != "pending" {
			continue
		}
		groups, e := withCooldown(ctx, s, func() ([]mb.ReleaseGroup, error) { return s.mb.BrowseArtistReleaseGroups(ctx, seed.MBID) })
		status := "done"
		message := ""
		if errors.Is(e, mb.ErrGone) {
			status = "gone"
			groups = nil
		} else if isCooldown(e) && s.cooldowns > 0 {
			// Persistent throttling: keep finished checkpoints, rank what we
			// have, and let the next refresh resume the pending seeds.
			artistThrottled = true
			break
		} else if e != nil {
			return e
		}
		items := []storage.DiscoveryReleaseGroup{}
		for _, rg := range groups {
			kind, ok := Eligible(rg, s.now())
			if !ok {
				continue
			}
			credit := []string{}
			for _, c := range rg.ArtistCredit {
				name := c.Name
				if name == "" {
					name = c.Artist.Name
				}
				credit = append(credit, name)
			}
			items = append(items, storage.DiscoveryReleaseGroup{RGID: rg.ID, Title: rg.Title, PrimaryType: rg.PrimaryType, SecondaryTypes: rg.SecondaryTypes, Kind: kind, FirstReleaseDate: rg.FirstReleaseDate, Disambiguation: rg.Disambiguation, ArtistCredit: strings.Join(credit, ", ")})
		}
		if e = s.store.SaveDiscoverySeed(ctx, g, seed, items, status, message, s.now()); e != nil {
			return e
		}
	}
	message, e := s.executeWorks(ctx, g, artistThrottled)
	if e != nil {
		return e
	}
	if artistThrottled {
		message = "MusicBrainz 持续限流，部分艺人尚未抓取，再次刷新会从断点继续"
	}
	ranked, e := s.rankAndSave(ctx, g)
	if e != nil {
		return e
	}
	if message != "" {
		if e = s.store.DiscoveryMessage(ctx, g, message); e != nil {
			return e
		}
	}
	return s.downloadCovers(ctx, g, ranked)
}

// downloadCovers fetches covers for the visible recommendations plus the
// first mapping options. It is shared by refreshes and cover backfills; a
// newer generation makes UpdateDiscoveryCover fail with ErrDiscoveryStale.
func (s *Service) downloadCovers(ctx context.Context, g int64, ranked []storage.DiscoveryCandidate) error {
	var seeds []storage.DiscoverySeed
	mapping, _, e := s.store.ListDiscovery(ctx, "mapping", "all", 200, 0)
	if e != nil {
		return e
	}
	// Possible-match cards are compared visually, so they get covers too.
	possible, _, e := s.store.ListDiscovery(ctx, "possible", "all", RecommendationLimit, 0)
	if e != nil {
		return e
	}
	for _, v := range possible {
		c := v.DiscoveryCandidate
		c.Rank = 1
		ranked = append(ranked, c)
	}
	mappingCovers := map[string]bool{}
	for _, group := range mapping {
		for _, option := range group.MappingOptions {
			if mappingCovers[option.RGID] {
				continue
			}
			if len(mappingCovers) >= 30 {
				break
			}
			mappingCovers[option.RGID] = true
			candidate := option.DiscoveryCandidate
			candidate.Rank = 1
			ranked = append(ranked, candidate)
		}
	}

	attempted := map[string]bool{}
	for _, v := range ranked {
		if e = ctx.Err(); e != nil {
			return e
		}
		if v.Rank <= 0 || v.Rank > RecommendationLimit || attempted[v.RGID] {
			continue
		}
		attempted[v.RGID] = true
		if v.CoverStatus == "cached" {
			if _, _, err := coverFile(s.dataDir, v.RGID); err == nil {
				continue
			}
		}
		if v.CoverStatus == "missing" {
			if t, e := time.Parse(time.RFC3339, v.CoverCheckedAt); e == nil && s.now().Sub(t) < coverMissingRetry {
				continue
			}
		}
		if v.CoverStatus == "failed" {
			if t, e := time.Parse(time.RFC3339, v.CoverCheckedAt); e == nil && s.now().Sub(t) < 7*24*time.Hour {
				continue
			}
		}
		image, e := netfetch.DownloadImage(ctx, s.images, s.gate, "coverartarchive", strings.TrimRight(s.caa, "/")+"/release-group/"+v.RGID+"/front-500", s.ua(ctx))
		if errors.Is(e, netfetch.ErrNotFound) && v.SubjectID > 0 {
			// CAA has no art for this release group: fall back to the cover of
			// the Bangumi music subject it was matched from.
			if fallback, ok := s.bangumiCover(ctx, v.SubjectID); ok {
				image, e = fallback, nil
			}
		}
		status, ext := "cached", image.Ext
		if errors.Is(e, netfetch.ErrNotFound) {
			status = "missing"
		} else if e != nil {
			if errors.Is(e, sourcegate.ErrRateLimited) {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			status = "failed"
		}
		if status == "cached" {
			if _, e = netfetch.WriteCacheFile(s.dataDir, v.RGID+ext, image.Data); e != nil {
				status = "failed"
				ext = ""
			}
		}
		if e = s.store.UpdateDiscoveryCover(ctx, g, seeds, v.RGID, status, ext, s.now()); e != nil {
			return e
		}
	}
	return nil
}

// BackfillCovers downloads missing or outdated covers for the current
// catalog without refetching MusicBrainz, e.g. after an upgrade changed the
// cover size or after a mapping confirmation re-ranked the list.
func (s *Service) BackfillCovers() {
	if !s.coverBusy.CompareAndSwap(false, true) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.coverBusy.Store(false)
		ctx := s.baseCtx
		state, e := s.store.DiscoveryStatus(ctx)
		if e != nil || state.Status != "completed" {
			return
		}
		items, _, e := s.store.ListDiscovery(ctx, "recommended", "all", RecommendationLimit, 0)
		if e != nil {
			return
		}
		ranked := make([]storage.DiscoveryCandidate, 0, len(items))
		for _, v := range items {
			c := v.DiscoveryCandidate
			if c.Rank <= 0 {
				c.Rank = 1
			}
			ranked = append(ranked, c)
		}
		if e = s.downloadCovers(ctx, state.Generation, ranked); e != nil && !errors.Is(e, storage.ErrDiscoveryStale) && ctx.Err() == nil {
			s.logger.Warn("discovery cover backfill stopped", "error", e)
		}
	}()
}

// bangumiCover downloads a Bangumi subject's artwork. Any failure (including
// throttling) simply reports no fallback so CAA's result stands.
func (s *Service) bangumiCover(ctx context.Context, subjectID int64) (netfetch.Image, bool) {
	if s.bangumi == nil {
		return netfetch.Image{}, false
	}
	subject, e := s.bangumi.BangumiMusicSubject(ctx, subjectID)
	if e != nil || subject.Image == "" {
		return netfetch.Image{}, false
	}
	image, e := netfetch.DownloadImage(ctx, s.images, s.gate, "bangumi", subject.Image, s.ua(ctx))
	if e != nil {
		s.logger.Debug("bangumi cover fallback failed", "subject", subjectID, "error", e)
		return netfetch.Image{}, false
	}
	return image, true
}

// CachedCover reads only disk. No network or database call is made. Covers
// cached by older versions (250px) are served until the 500px copy exists.
func (s *Service) CachedCover(raw string) (path, mime string, err error) {
	if path, mime, err = coverFile(s.dataDir, raw); err == nil {
		return path, mime, nil
	}
	return coverFile(s.legacyDir, raw)
}

func coverFile(dir, raw string) (path, mime string, err error) {
	id, ok := metadata.NormalizeMusicBrainzID(raw)
	if !ok || dir == "" {
		return "", "", os.ErrNotExist
	}
	for _, v := range []struct{ ext, mime string }{{".jpg", "image/jpeg"}, {".png", "image/png"}, {".webp", "image/webp"}, {".gif", "image/gif"}} {
		path = filepath.Join(dir, id+v.ext)
		if st, e := os.Stat(path); e == nil && st.Mode().IsRegular() {
			return path, v.mime, nil
		}
	}
	return "", "", os.ErrNotExist
}

// Items adds disk availability to the storage list; it never fetches images.
func (s *Service) Items(ctx context.Context, tab string, limit, offset int, sources ...string) ([]storage.DiscoveryItem, storage.DiscoveryCounts, error) {
	source := "all"
	if len(sources) > 0 {
		source = sources[0]
	}
	items, counts, err := s.store.ListDiscovery(ctx, tab, source, limit, offset)
	if err != nil {
		return nil, counts, err
	}
	for i := range items {
		_, _, e := s.CachedCover(items[i].RGID)
		items[i].CoverAvailable = e == nil
		for j := range items[i].MappingOptions {
			_, _, err := s.CachedCover(items[i].MappingOptions[j].RGID)
			items[i].MappingOptions[j].CoverAvailable = err == nil
		}
	}
	return items, counts, nil
}

// BangumiEnabled reports the shared enrichment setting for the discovery status UI.
func (s *Service) BangumiEnabled(ctx context.Context) (bool, error) {
	if s.bangumi == nil {
		return false, nil
	}
	return s.bangumi.BangumiEnabled(ctx)
}
