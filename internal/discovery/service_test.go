package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/discovery/mb"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testRG = "11111111-1111-1111-1111-111111111111"
const testMB = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

type fixture struct {
	store *storage.Store
	raw   *sql.DB
	ctx   context.Context
	seed  storage.DiscoverySeed
	album int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	raw, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { raw.Close() })
	if e = s.EnsureLibrary(ctx, "test", "/music"); e != nil {
		t.Fatal(e)
	}
	lib, e := s.LibraryByRoot(ctx, "/music")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "one.flac", Metadata: metadata.AudioMetadata{Title: "song", Album: "Local", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}); e != nil {
		t.Fatal(e)
	}
	var artist, album int64
	if e = raw.QueryRow(`SELECT id FROM artists WHERE display_name='Singer'`).Scan(&artist); e != nil {
		t.Fatal(e)
	}
	if e = raw.QueryRow(`SELECT id FROM albums LIMIT 1`).Scan(&album); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`INSERT INTO artist_external_profiles(artist_id,source,external_id,display_name,raw_json,fetched_at) VALUES(?,'musicbrainz',?,'Singer','{}','2026-01-01')`, artist, testMB); e != nil {
		t.Fatal(e)
	}
	return fixture{s, raw, ctx, storage.DiscoverySeed{ArtistID: artist, Artist: "Singer", MBID: testMB}, album}
}
func (f fixture) ready(t *testing.T) {
	t.Helper()
	if _, e := f.raw.Exec(`UPDATE release_group_rebuild SET status='ready'`); e != nil {
		t.Fatal(e)
	}
}

// fakeClock advances only when the service sleeps through a cooldown.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps int
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleeps++
	return ctx.Err()
}
func (c *fakeClock) Sleeps() int { c.mu.Lock(); defer c.mu.Unlock(); return c.sleeps }

func serviceFor(t *testing.T, f fixture, handler http.HandlerFunc) *Service {
	s, _ := serviceWithClock(t, f, handler)
	return s
}
func serviceWithClock(t *testing.T, f fixture, handler http.HandlerFunc) (*Service, *fakeClock) {
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	clock := &fakeClock{now: time.Now()}
	gate := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0, "coverartarchive": 0}, Now: clock.Now})
	return New(f.store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{MBBaseURL: server.URL, CAAURL: server.URL, Gate: gate, HTTPClient: server.Client(), DataDir: t.TempDir(), Now: clock.Now, Sleep: clock.Sleep}), clock
}
func respond(w http.ResponseWriter) {
	json.NewEncoder(w).Encode(map[string]any{"release-groups": []mb.ReleaseGroup{{ID: testRG, Title: "Remote", PrimaryType: "Album"}}, "release-group-count": 1})
}
func TestServiceIdentityNotReady(t *testing.T) {
	f := setup(t)
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) { t.Error("network before ready") })
	if _, e := s.Refresh(f.ctx, true); !errors.Is(e, storage.ErrIdentityNotReady) {
		t.Fatal(e)
	}
	if _, _, e := f.store.ListDiscovery(f.ctx, "recommended", "all", 60, 0); !errors.Is(e, storage.ErrIdentityNotReady) {
		t.Fatal(e)
	}
	if _, e := f.store.DiscoveryOwnership(f.ctx, nil); !errors.Is(e, storage.ErrIdentityNotReady) {
		t.Fatal(e)
	}
}
func TestServiceCancelAndIdentityDriftRejectLateWrites(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprint(drift), func(t *testing.T) {
			f := setup(t)
			f.ready(t)
			entered, release := make(chan struct{}), make(chan struct{})
			s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; respond(w) })
			if _, e := s.Refresh(f.ctx, true); e != nil {
				t.Fatal(e)
			}
			<-entered
			if _, e := s.Refresh(f.ctx, true); !errors.Is(e, ErrRunning) {
				t.Fatal(e)
			}
			if drift {
				if _, e := f.raw.Exec(`UPDATE artist_external_profiles SET external_id='bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb' WHERE source='musicbrainz'`); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := s.Cancel(f.ctx); e != nil {
					t.Fatal(e)
				}
			}
			close(release)
			s.Wait()
			state, err := s.Status(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if drift {
				want = "failed"
				if state.ErrorMessage != "艺人或作品身份已变化，请重新刷新" {
					t.Fatal(state)
				}
			}
			if state.Status != want {
				t.Fatal(state)
			}
			if err = s.Start(f.ctx); err != nil {
				t.Fatal(err)
			}
			s.Wait()
			for _, table := range []string{"discovery_release_groups", "discovery_candidates"} {
				var n int
				if e := f.raw.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 0 {
					t.Fatal(table, n, e)
				}
			}
		})
	}
}
func TestDiscoveryGuardTransactionsAndDecisions(t *testing.T) {
	f := setup(t)
	f.ready(t)
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, []storage.DiscoverySeed{f.seed}, true, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	group := storage.DiscoveryReleaseGroup{RGID: testRG, Title: "Local", Kind: "Album", PrimaryType: "Album"}
	if e = f.store.SaveDiscoverySeed(f.ctx, g, f.seed, []storage.DiscoveryReleaseGroup{group}, "done", "", time.Now()); e != nil {
		t.Fatal(e)
	}
	candidate := storage.DiscoveryCandidate{DiscoveryReleaseGroup: group, SeedArtistID: f.seed.ArtistID, Lane: "exploit", Rank: 1, Reasons: []storage.DiscoveryReason{}}
	matches := []storage.DiscoveryMatch{{RGID: testRG, AlbumID: f.album, Reason: "title_artist"}}
	if e = f.store.SaveDiscoveryCandidates(f.ctx, g, []storage.DiscoverySeed{f.seed}, []storage.DiscoveryCandidate{candidate}, matches); e != nil {
		t.Fatal(e)
	}
	items, counts, e := f.store.ListDiscovery(f.ctx, "possible", "all", 60, 0)
	if e != nil || len(items) != 1 || counts.Possible != 1 || len(items[0].Possible) != 1 {
		t.Fatal(items, counts, e)
	}
	if e = f.store.DiscoveryNotSame(f.ctx, testRG, f.album); e != nil {
		t.Fatal(e)
	}
	g, e = f.store.BeginDiscoveryRefresh(f.ctx, []storage.DiscoverySeed{f.seed}, true, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.SaveDiscoveryCandidates(f.ctx, g, []storage.DiscoverySeed{f.seed}, []storage.DiscoveryCandidate{candidate}, matches); e != nil {
		t.Fatal(e)
	}
	items, counts, e = f.store.ListDiscovery(f.ctx, "recommended", "all", 60, 0)
	if e != nil || len(items) != 1 || counts.Possible != 0 {
		t.Fatal(items, counts, e)
	}
	for _, ignored := range []bool{true, false} {
		if e = f.store.DiscoveryIgnore(f.ctx, testRG, ignored); e != nil {
			t.Fatal(e)
		}
		_, counts, e = f.store.ListDiscovery(f.ctx, "ignored", "all", 60, 0)
		if e != nil || ignored != (counts.Ignored == 1) {
			t.Fatal(counts, e)
		}
	}
	other := "22222222-2222-2222-2222-222222222222"
	if e = f.store.AssociateAlbumReleaseGroup(f.ctx, f.album, other); e != nil {
		t.Fatal(e)
	}
	e = f.store.DiscoveryOwned(f.ctx, testRG, f.album, false)
	var replace *storage.ErrReplacesReleaseGroup
	if !errors.As(e, &replace) || replace.RGID != other {
		t.Fatal(e)
	}
	if e = f.store.DiscoveryOwned(f.ctx, testRG, f.album, true); e != nil {
		t.Fatal(e)
	}
	id, e := f.store.AlbumReleaseGroup(f.ctx, f.album)
	if e != nil || id != testRG {
		t.Fatal(id, e)
	}
	owned, e := f.store.DiscoveryOwnership(f.ctx, []string{testRG})
	if e != nil || len(owned[testRG].Albums) != 1 || len(owned[testRG].Possible) != 0 {
		t.Fatal(owned, e)
	}
	if e = f.store.CancelDiscovery(f.ctx); e != nil {
		t.Fatal(e)
	}
	g2, e := f.store.BeginDiscoveryRefresh(f.ctx, []storage.DiscoverySeed{f.seed}, true, time.Now())
	if e != nil || g2 <= g {
		t.Fatal(g2, e)
	}
	if e = f.store.SaveDiscoverySeed(f.ctx, g, f.seed, []storage.DiscoveryReleaseGroup{group}, "done", "", time.Now()); !errors.Is(e, storage.ErrDiscoveryStale) {
		t.Fatal(e)
	}
	if _, e = f.raw.Exec(`UPDATE artist_external_profiles SET external_id='bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb'`); e != nil {
		t.Fatal(e)
	}
	if e = f.store.SaveDiscoverySeed(f.ctx, g2, f.seed, nil, "done", "", time.Now()); !errors.Is(e, storage.ErrDiscoveryStale) {
		t.Fatal(e)
	}
}
func TestServiceCompleteCooldownAndSpecialArtist(t *testing.T) {
	for _, status := range []int{200, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := setup(t)
			f.ready(t)
			s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) {
				if status != 200 {
					w.WriteHeader(status)
					return
				}
				respond(w)
			})
			if _, e := s.Refresh(f.ctx, true); e != nil {
				t.Fatal(e)
			}
			s.Wait()
			state, e := s.Status(f.ctx)
			if e != nil {
				t.Fatal(e)
			}
			if status == 200 {
				if state.Status != "completed" {
					t.Fatal(state)
				}
				items, _, e := f.store.ListDiscovery(f.ctx, "recommended", "all", 60, 0)
				if e != nil || len(items) != 1 {
					t.Fatal(items, e)
				}
			} else {
				if state.Status != "failed" || state.ErrorMessage != "MusicBrainz 限流，约 1 分钟后可重试" {
					t.Fatal(state)
				}
			}
		})
	}
	// A transient cooldown is waited out instead of failing the round.
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprint("transient-", status), func(t *testing.T) {
			f := setup(t)
			f.ready(t)
			var calls atomic.Int32
			s, clock := serviceWithClock(t, f, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(status)
					return
				}
				respond(w)
			})
			if _, e := s.Refresh(f.ctx, true); e != nil {
				t.Fatal(e)
			}
			s.Wait()
			state, e := s.Status(f.ctx)
			if e != nil || state.Status != "completed" || clock.Sleeps() == 0 {
				t.Fatal(state, e, clock.Sleeps())
			}
		})
	}
	f := setup(t)
	f.ready(t)
	if _, e := f.raw.Exec(`UPDATE artist_external_profiles SET external_id='89ad4ac3-39f7-470e-963a-56509c546377'`); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	if calls.Load() != 0 {
		t.Fatal("special artist requested")
	}
}

func TestServiceResumeOnlyRunningAndFreshSeedCache(t *testing.T) {
	f := setup(t)
	f.ready(t)
	var calls atomic.Int32
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); respond(w) })
	if e := s.Start(f.ctx); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	if calls.Load() != 0 {
		t.Fatal("idle startup fetched")
	}
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, []storage.DiscoverySeed{f.seed}, true, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Start(f.ctx); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	state, e := s.Status(f.ctx)
	if e != nil || state.Generation != g || state.Status != "completed" || calls.Load() != 1 {
		t.Fatal(state, calls.Load(), e)
	}
	if e = s.Start(f.ctx); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	if calls.Load() != 1 {
		t.Fatal("completed startup fetched")
	}
	if _, e = s.Refresh(f.ctx, false); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	if calls.Load() != 1 {
		t.Fatal("fresh seed fetched")
	}
}

type coverTransport func(*http.Request) (*http.Response, error)

func (f coverTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestServiceDownloadsCoverAndCachedCoverIsDiskOnly(t *testing.T) {
	f := setup(t)
	f.ready(t)
	var requests atomic.Int32
	client := &http.Client{Transport: coverTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		body := "\x89PNG\r\n\x1a\nrest"
		if r.URL.Host == "mb.example" {
			body = `{"release-groups":[{"id":"` + testRG + `","title":"Remote","primary-type":"Album"}],"release-group-count":1}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := New(f.store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{MBBaseURL: "http://mb.example", CAAURL: "http://93.184.216.34", HTTPClient: client, DataDir: t.TempDir(), Gate: sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0, "coverartarchive": 0}})})
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	if requests.Load() != 2 {
		t.Fatal(requests.Load())
	}
	path, mime, e := s.CachedCover(testRG)
	if e != nil || mime != "image/png" {
		t.Fatal(path, mime, e)
	}
	for i := 0; i < 3; i++ {
		if _, _, e = s.CachedCover(testRG); e != nil {
			t.Fatal(e)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("cover read touched network")
	}
	items, _, e := f.store.ListDiscovery(f.ctx, "recommended", "all", 60, 0)
	if e != nil || len(items) != 1 || !items[0].CoverAvailable {
		t.Fatal(items, e)
	}
	if _, _, e = s.CachedCover("../../outside"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}

func TestDiscoveryCoverCooldownAndNonRunningCancel(t *testing.T) {
	f := setup(t)
	f.ready(t)
	client := &http.Client{Transport: coverTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "mb.example" {
			return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"release-groups":[{"id":"` + testRG + `","title":"Remote","primary-type":"Album"}],"release-group-count":1}`))}, nil
	})}
	s := New(f.store, nil, Options{HTTPClient: client, MBBaseURL: "http://mb.example", CAAURL: "http://93.184.216.34", DataDir: t.TempDir(), Gate: sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0, "coverartarchive": 0}})})
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	before, e := s.Status(f.ctx)
	if e != nil || before.Status != "completed" {
		t.Fatal(before, e)
	}
	var cover string
	if e = f.raw.QueryRow(`SELECT cover_status FROM discovery_release_groups WHERE rgid=?`, testRG).Scan(&cover); e != nil || cover != "unknown" {
		t.Fatal(cover, e)
	}
	if e = f.store.FinishDiscovery(f.ctx, before.Generation, "failed", "should not overwrite"); e != nil {
		t.Fatal(e)
	}
	if e = s.Cancel(f.ctx); e != nil {
		t.Fatal(e)
	}
	after, e := s.Status(f.ctx)
	if e != nil || before != after {
		t.Fatal(before, after, e)
	}
}
func TestDiscoveryRefreshRequestContextDoesNotOwnWorker(t *testing.T) {
	f := setup(t)
	f.ready(t)
	entered, release := make(chan struct{}), make(chan struct{})
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; respond(w) })
	ctx, cancel := context.WithCancel(f.ctx)
	if _, e := s.Refresh(ctx, true); e != nil {
		t.Fatal(e)
	}
	<-entered
	cancel()
	close(release)
	s.Wait()
	state, e := s.Status(f.ctx)
	if e != nil || state.Status != "completed" {
		t.Fatal(state, e)
	}
}
func TestDiscoveryMissingCachedCoverIsRebuilt(t *testing.T) {
	f := setup(t)
	f.ready(t)
	var images atomic.Int32
	client := &http.Client{Transport: coverTransport(func(r *http.Request) (*http.Response, error) {
		body := "\x89PNG\r\n\x1a\nrest"
		if r.URL.Host == "mb.example" {
			body = `{"release-groups":[{"id":"` + testRG + `","title":"Remote","primary-type":"Album"}],"release-group-count":1}`
		} else {
			images.Add(1)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := New(f.store, nil, Options{HTTPClient: client, MBBaseURL: "http://mb.example", CAAURL: "http://93.184.216.34", DataDir: t.TempDir(), Gate: sourcegate.New(sourcegate.Options{})})
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	path, _, e := s.CachedCover(testRG)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	items, _, e := s.Items(f.ctx, "recommended", 60, 0)
	if e != nil || len(items) != 1 || items[0].CoverAvailable {
		t.Fatal(items, e)
	}
	if _, e = s.Refresh(f.ctx, false); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	if images.Load() != 2 {
		t.Fatal(images.Load())
	}
	if _, _, e = s.CachedCover(testRG); e != nil {
		t.Fatal(e)
	}
}

func TestDiscoveryUTCAndRecursiveMergeSignals(t *testing.T) {
	f := setup(t)
	f.ready(t)
	var middle, current int64
	for i, name := range []string{"Middle", "Current"} {
		res, e := f.raw.Exec(`INSERT INTO artists(display_name,sort_name,identity_key,is_favorite) VALUES(?,?,?,?)`, name, name, name, i)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := res.LastInsertId()
		if i == 0 {
			middle = id
		} else {
			current = id
		}
	}
	if _, e := f.raw.Exec(`UPDATE artists SET merged_into_artist_id=? WHERE id=?`, middle, f.seed.ArtistID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.raw.Exec(`UPDATE artists SET merged_into_artist_id=? WHERE id=?`, current, middle); e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.store.DiscoverySeedSnapshot(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	// Performer qualification intentionally remains the established biography
	// predicate, so move one album relation to the root as normal merge does.
	if _, e = f.raw.Exec(`INSERT INTO album_artists(album_id,artist_id) VALUES(?,?)`, f.album, current); e != nil {
		t.Fatal(e)
	}
	snapshot, e = f.store.DiscoverySeedSnapshot(f.ctx)
	if e != nil || len(snapshot) != 1 || snapshot[0].ArtistID != current || snapshot[0].MBID != testMB {
		t.Fatal(snapshot, e)
	}
	albums, e := f.store.DiscoveryLocalAlbums(f.ctx)
	if e != nil || len(albums) != 1 {
		t.Fatal(albums, e)
	}
	for _, id := range albums[0].ArtistIDs {
		if id != current {
			t.Fatal("not canonical", id)
		}
	}
	signals, e := f.store.DiscoveryArtistSignals(f.ctx)
	if e != nil || !signals[current].FavArtist || signals[current].OwnedAlbums != 1 {
		t.Fatal(signals, e)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("offset", 9*3600))
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, snapshot, true, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.SaveDiscoverySeed(f.ctx, g, snapshot[0], nil, "done", "", now); e != nil {
		t.Fatal(e)
	}
	var fetched, started string
	if e = f.raw.QueryRow(`SELECT fetched_at FROM discovery_seeds WHERE artist_id=?`, current).Scan(&fetched); e != nil {
		t.Fatal(e)
	}
	state, e := f.store.DiscoveryStatus(f.ctx)
	started = state.StartedAt
	if e != nil || fetched != "2026-10-24T03:00:00Z" || started != fetched {
		t.Fatal(fetched, started, e)
	}
}
