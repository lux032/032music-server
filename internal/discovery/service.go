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
	"time"
)

var ErrRunning = errors.New("discovery refresh already running")

type Options struct {
	BaseContext                context.Context
	Gate                       *sourcegate.Gate
	HTTPClient                 *http.Client
	MBBaseURL, CAAURL, DataDir string
	Now                        func() time.Time
	UserAgent                  func(context.Context) string
}
type Service struct {
	baseCtx      context.Context
	store        *storage.Store
	logger       *slog.Logger
	mb           *mb.Client
	images       *http.Client
	gate         *sourcegate.Gate
	now          func() time.Time
	ua           func(context.Context) string
	dataDir, caa string
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
	return &Service{baseCtx: o.BaseContext, store: store, logger: logger, mb: &mb.Client{BaseURL: o.MBBaseURL, HTTP: o.HTTPClient, Gate: o.Gate, UA: o.UserAgent, Now: o.Now, Logger: logger}, images: o.HTTPClient, gate: o.Gate, now: o.Now, ua: o.UserAgent, dataDir: filepath.Join(o.DataDir, "discovery-covers"), caa: o.CAAURL}
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
	g, e := s.store.BeginDiscoveryRefresh(ctx, filtered, force, s.now())
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
				_ = s.store.FinishDiscovery(ctx, g, "failed", "艺人身份已变化，请重新刷新")
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
	for _, seed := range seeds {
		if e = ctx.Err(); e != nil {
			return e
		}
		if seed.Status != "pending" {
			continue
		}
		groups, e := s.mb.BrowseArtistReleaseGroups(ctx, seed.MBID)
		status := "done"
		message := ""
		if errors.Is(e, mb.ErrGone) {
			status = "gone"
			groups = nil
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
	catalog, e := s.store.DiscoveryCatalog(ctx)
	if e != nil {
		return e
	}
	signals, e := s.store.DiscoveryArtistSignals(ctx)
	if e != nil {
		return e
	}
	albums, e := s.store.DiscoveryLocalAlbums(ctx)
	if e != nil {
		return e
	}
	rejections, e := s.store.DiscoveryRejections(ctx)
	if e != nil {
		return e
	}
	matches := WeakMatches(catalog, albums, rejections)
	ids := []string{}
	for _, v := range catalog {
		ids = append(ids, v.RGID)
	}
	owned, e := s.store.DiscoveryOwnership(ctx, ids)
	if e != nil {
		return e
	}
	ranked := Rank(catalog, signals, owned, matches, s.now())
	if e = s.store.SaveDiscoveryCandidates(ctx, g, seeds, ranked, matches); e != nil {
		return e
	}
	for _, v := range ranked {
		if e = ctx.Err(); e != nil {
			return e
		}
		if v.Rank <= 0 || v.Rank > RecommendationLimit {
			continue
		}
		if v.CoverStatus == "cached" {
			if _, _, err := s.CachedCover(v.RGID); err == nil {
				continue
			}
		}
		if v.CoverStatus == "missing" {
			continue
		}
		if v.CoverStatus == "failed" {
			if t, e := time.Parse(time.RFC3339, v.CoverCheckedAt); e == nil && s.now().Sub(t) < 7*24*time.Hour {
				continue
			}
		}
		image, e := netfetch.DownloadImage(ctx, s.images, s.gate, "coverartarchive", strings.TrimRight(s.caa, "/")+"/release-group/"+v.RGID+"/front-250", s.ua(ctx))
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

// CachedCover reads only disk. No network or database call is made.
func (s *Service) CachedCover(raw string) (path, mime string, err error) {
	id, ok := metadata.NormalizeMusicBrainzID(raw)
	if !ok {
		return "", "", os.ErrNotExist
	}
	for _, v := range []struct{ ext, mime string }{{".jpg", "image/jpeg"}, {".png", "image/png"}, {".webp", "image/webp"}, {".gif", "image/gif"}} {
		path = filepath.Join(s.dataDir, id+v.ext)
		if st, e := os.Stat(path); e == nil && st.Mode().IsRegular() {
			return path, v.mime, nil
		}
	}
	return "", "", os.ErrNotExist
}

// Items adds disk availability to the storage list; it never fetches images.
func (s *Service) Items(ctx context.Context, tab string, limit, offset int) ([]storage.DiscoveryItem, storage.DiscoveryCounts, error) {
	items, counts, err := s.store.ListDiscovery(ctx, tab, limit, offset)
	if err != nil {
		return nil, counts, err
	}
	for i := range items {
		_, _, e := s.CachedCover(items[i].RGID)
		items[i].CoverAvailable = e == nil
	}
	return items, counts, nil
}
