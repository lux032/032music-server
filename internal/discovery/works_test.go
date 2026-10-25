package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/bangumi"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
)

type fakeBangumi struct {
	enabled   bool
	relations func(int64) ([]bangumi.Relation, error)
	subject   func(int64) (bangumi.MusicSubject, error)
}

func (f fakeBangumi) BangumiEnabled(context.Context) (bool, error) { return f.enabled, nil }
func (f fakeBangumi) BangumiRelations(_ context.Context, id int64) ([]bangumi.Relation, error) {
	return f.relations(id)
}
func (f fakeBangumi) BangumiMusicSubject(_ context.Context, id int64) (bangumi.MusicSubject, error) {
	return f.subject(id)
}
func workFixture(t *testing.T, f fixture, id, subject int64) storage.DiscoveryWorkSeed {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO works(id,title,normalized_title) VALUES(?,?,?)`, []any{id, fmt.Sprint("Work", id), fmt.Sprint("work", id)}},
		{`INSERT INTO work_external_profiles(work_id,source,external_id,title,fetched_at) VALUES(?,'bangumi',?,'Work','2026-01-01')`, []any{id, fmt.Sprint(subject)}},
		{`INSERT INTO album_works(album_id,work_id,source) VALUES(?,?,'manual')`, []any{f.album, id}},
	} {
		if _, e := f.raw.Exec(q.sql, q.args...); e != nil {
			t.Fatal(e)
		}
	}
	return storage.DiscoveryWorkSeed{WorkID: id, SubjectID: subject, WorkTitle: fmt.Sprint("Work", id)}
}
func TestWorkStorageDecisionsGuardsAndAcquisition(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	now := time.Now()
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, nil, true, now, []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	groups := []storage.DiscoveryReleaseGroup{{RGID: testRG, Title: "Remote", PrimaryType: "Album", Kind: "Album"}, {RGID: testMB, Title: "Other", PrimaryType: "Album", Kind: "Album"}}
	music := []storage.DiscoveryMusic{{SubjectID: 2000, Name: "Remote", Relation: "原声集", SearchStatus: "hits"}}
	mappings := []storage.DiscoveryMapping{{SubjectID: 2000, RGID: testRG, Status: "suggested"}, {SubjectID: 2000, RGID: testMB, Status: "suggested"}}
	save := func() error {
		return f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, music, groups, mappings, "done", "", now)
	}
	if e = save(); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.AcquisitionReleaseGroup(f.ctx, testRG); !errors.Is(e, storage.ErrMappingUnconfirmed) {
		t.Fatal(e)
	}
	for _, decision := range []string{"reject", "restore", "confirm"} {
		if e = f.store.DiscoveryMappingDecision(f.ctx, 2000, testRG, decision); e != nil {
			t.Fatal(e)
		}
		if decision == "reject" {
			if e = save(); e != nil {
				t.Fatal(e)
			}
			var status string
			f.raw.QueryRow(`SELECT status FROM discovery_bangumi_mappings WHERE rgid=?`, testRG).Scan(&status)
			if status != "rejected" {
				t.Fatal(status)
			}
		}
	}
	if e = f.store.DiscoveryMappingDecision(f.ctx, 2000, testMB, "confirm"); !errors.Is(e, storage.ErrMappingSubjectConfirmed) {
		t.Fatal(e)
	}
	if e = save(); e != nil {
		t.Fatal(e)
	}
	items, counts, e := f.store.ListDiscovery(f.ctx, "mapping", "all", 60, 0)
	if e != nil || len(items) != 0 || counts.Mapping != 0 {
		t.Fatal(items, counts, e)
	}
	if _, e = f.store.AcquisitionReleaseGroup(f.ctx, testRG); e != nil {
		t.Fatal(e)
	}
	conn, e := f.raw.Conn(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = conn.ExecContext(f.ctx, `PRAGMA foreign_keys=ON`); e != nil {
		t.Fatal(e)
	}
	_, e = conn.ExecContext(f.ctx, `DELETE FROM discovery_release_groups WHERE rgid=?`, testRG)
	conn.Close()
	if e == nil {
		t.Fatal("user mapping cascaded")
	}
	candidate := storage.DiscoveryCandidate{DiscoveryReleaseGroup: groups[0], SeedWorkID: seed.WorkID, SubjectID: 2000, Lane: "exploit", Rank: 1}
	if e = f.store.SaveDiscoveryCandidates(f.ctx, g, nil, []storage.DiscoveryCandidate{candidate}, nil, []storage.DiscoveryWorkSeed{seed}); e != nil {
		t.Fatal(e)
	}
	items, _, e = f.store.ListDiscovery(f.ctx, "recommended", "work", 60, 0)
	if e != nil || len(items) != 1 || items[0].SeedWorkID != seed.WorkID {
		t.Fatal(items, e)
	}
	// A rejected mapping with an artist provenance remains acquirable, as does an S2-only RG.
	if _, e = f.raw.Exec(`INSERT INTO discovery_artist_release_groups(artist_id,rgid) VALUES(?,?)`, f.seed.ArtistID, testMB); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.AcquisitionReleaseGroup(f.ctx, testMB); e != nil {
		t.Fatal(e)
	}
	if _, e = f.raw.Exec(`UPDATE work_external_profiles SET external_id='1001' WHERE work_id=?`, seed.WorkID); e != nil {
		t.Fatal(e)
	}
	if e = save(); !errors.Is(e, storage.ErrDiscoveryStale) {
		t.Fatal(e)
	}
	if e = f.store.SaveDiscoveryCandidates(f.ctx, g, nil, nil, nil, []storage.DiscoveryWorkSeed{seed}); !errors.Is(e, storage.ErrDiscoveryStale) {
		t.Fatal(e)
	}
	f.raw.Exec(`UPDATE work_external_profiles SET external_id='1000' WHERE work_id=?`, seed.WorkID)
	if e = f.store.CancelDiscovery(f.ctx); e != nil {
		t.Fatal(e)
	} // completed is unchanged; explicitly begin/cancel to invalidate.
	g, e = f.store.BeginDiscoveryRefresh(f.ctx, nil, true, now, []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.CancelDiscovery(f.ctx); e != nil {
		t.Fatal(e)
	}
	if e = save(); !errors.Is(e, storage.ErrDiscoveryStale) {
		t.Fatal(e)
	}
	g, e = f.store.BeginDiscoveryRefresh(f.ctx, nil, true, now, []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g-1, seed, music, groups, mappings, "done", "", now); !errors.Is(e, storage.ErrDiscoveryStale) {
		t.Fatal(e)
	}
	f.raw.Exec(`DELETE FROM works WHERE id=?`, seed.WorkID)
	if e = save(); !errors.Is(e, storage.ErrDiscoveryStale) {
		t.Fatal(e)
	}
}
func TestNewWorkEntrypointsIdentityReady(t *testing.T) {
	f := setup(t)
	c := f.ctx
	checks := []func() error{
		func() error { _, e := f.store.DiscoveryWorkSeedSnapshot(c); return e }, func() error { _, e := f.store.DiscoveryWorkSeeds(c, 1); return e }, func() error { _, _, e := f.store.DiscoveryMusicSearchState(c, 1); return e }, func() error { _, e := f.store.DiscoveryLocalBangumiMusic(c); return e }, func() error { _, e := f.store.DiscoveryWorkCatalog(c); return e }, func() error { _, e := f.store.DiscoveryWorkSignals(c); return e }, func() error {
			return f.store.SaveDiscoveryWorkSeed(c, 1, storage.DiscoveryWorkSeed{}, nil, nil, nil, "done", "", time.Now())
		}, func() error { return f.store.DiscoveryMappingDecision(c, 1, testRG, "confirm") }, func() error { _, e := f.store.AcquisitionReleaseGroup(c, testRG); return e }, func() error { return f.store.DiscoveryMessage(c, 1, "") },
	}
	for i, check := range checks {
		if e := check(); !errors.Is(e, storage.ErrIdentityNotReady) {
			t.Fatal(i, e)
		}
	}
}
func TestWorkRefreshRerankAndLocalSkip(t *testing.T) {
	f := setup(t)
	f.ready(t)
	workFixture(t, f, 100, 1000)
	var searches atomic.Int64
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("artist") {
			w.Write([]byte(`{"release-groups":[]}`))
			return
		}
		searches.Add(1)
		w.Write([]byte(`{"release-groups":[{"id":"11111111-1111-1111-1111-111111111111","title":"Remote","primary-type":"Album","score":99}]}`))
	})
	s.bangumi = fakeBangumi{enabled: true, relations: func(int64) ([]bangumi.Relation, error) {
		return []bangumi.Relation{{ID: 2000, Type: 3, Name: "Remote", Relation: "原声集"}, {ID: 2001, Type: 3, Name: "Owned", Relation: "片头曲"}, {ID: 2002, Type: 3, Name: "Excluded", Relation: "其他"}}, nil
	}, subject: func(id int64) (bangumi.MusicSubject, error) {
		if id != 2000 {
			t.Error("unexpected subject", id)
		}
		return bangumi.MusicSubject{ID: id, Name: "Remote"}, nil
	}}
	if _, e := f.raw.Exec(`INSERT INTO album_subject_candidates(album_id,source,external_id,title,status) VALUES(?,'bangumi','2001','Owned','confirmed')`, f.album); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	state, _ := s.Status(f.ctx)
	if state.Status != "completed" {
		t.Fatal(state)
	}
	items, c, e := f.store.ListDiscovery(f.ctx, "mapping", "work", 60, 0)
	if e != nil || len(items) != 1 || c.Mapping != 1 || c.Recommended != 0 {
		t.Fatal(items, c, e)
	}
	if reranked, e := s.MappingDecision(f.ctx, 2000, testRG, "confirm"); e != nil || !reranked {
		t.Fatal(reranked, e)
	}
	items, _, e = f.store.ListDiscovery(f.ctx, "recommended", "work", 60, 0)
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	if _, e := s.MappingDecision(f.ctx, 2000, testRG, "reject"); e != nil {
		t.Fatal(e)
	}
	items, _, e = f.store.ListDiscovery(f.ctx, "ignored", "work", 60, 0)
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	if _, e := s.MappingDecision(f.ctx, 2000, testRG, "restore"); e != nil {
		t.Fatal(e)
	}
	items, _, e = f.store.ListDiscovery(f.ctx, "mapping", "work", 60, 0)
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	if searches.Load() != 1 {
		t.Fatal("rerank searched", searches.Load())
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if reranked, e := s.Rerank(f.ctx); reranked || e != nil {
		t.Fatal(reranked, e)
	}
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}
func TestBangumiDisabledAndCooldown(t *testing.T) {
	for _, cooldown := range []bool{false, true} {
		t.Run(fmt.Sprint(cooldown), func(t *testing.T) {
			f := setup(t)
			f.ready(t)
			workFixture(t, f, 100, 1000)
			s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"release-groups":[]}`)) })
			s.bangumi = fakeBangumi{enabled: cooldown, relations: func(int64) ([]bangumi.Relation, error) {
				return nil, &sourcegate.CooldownError{Source: "bangumi", RetryAfter: time.Minute}
			}}
			if _, e := s.Refresh(f.ctx, true); e != nil {
				t.Fatal(e)
			}
			s.Wait()
			state, _ := s.Status(f.ctx)
			if cooldown {
				if state.Status != "completed" || !strings.Contains(state.ErrorMessage, "持续限流") {
					t.Fatal(state)
				}
			} else if state.Status != "completed" || !strings.Contains(state.ErrorMessage, "Bangumi 未启用") {
				t.Fatal(state)
			}
		})
	}
}

func TestWorkSearchBudgetContinuesNextRefresh(t *testing.T) {
	f := setup(t)
	f.ready(t)
	for i := int64(1); i <= 24; i++ {
		workFixture(t, f, 100+i, 1000+i)
	}
	var searches atomic.Int64
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("query") {
			searches.Add(1)
		}
		w.Write([]byte(`{"release-groups":[]}`))
	})
	s.bangumi = fakeBangumi{enabled: true, relations: func(id int64) ([]bangumi.Relation, error) {
		out := []bangumi.Relation{}
		for i := int64(0); i < 6; i++ {
			out = append(out, bangumi.Relation{ID: id*10 + i, Type: 3, Name: fmt.Sprint(id*10 + i), Relation: "原声集"})
		}
		return out, nil
	}, subject: func(id int64) (bangumi.MusicSubject, error) {
		return bangumi.MusicSubject{ID: id, Name: fmt.Sprint(id)}, nil
	}}
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	state, _ := s.Status(f.ctx)
	if state.Status != "completed" || !strings.Contains(state.ErrorMessage, "预算") || searches.Load() != 120 {
		t.Fatal(state, searches.Load())
	}
	if _, e := s.Refresh(f.ctx, false); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	state, _ = s.Status(f.ctx)
	if state.Status != "completed" || searches.Load() != 144 {
		t.Fatal(state, searches.Load())
	}
	var pending int
	if e := f.raw.QueryRow(`SELECT count(*) FROM discovery_bangumi_music WHERE search_status='pending'`).Scan(&pending); e != nil || pending != 0 {
		t.Fatal(pending, e)
	}
}
func TestWorkSearchFailureKeepsCheckpoint(t *testing.T) {
	f := setup(t)
	f.ready(t)
	workFixture(t, f, 100, 1000)
	workFixture(t, f, 101, 1001)
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"release-groups":[]}`)) })
	s.bangumi = fakeBangumi{enabled: true, relations: func(id int64) ([]bangumi.Relation, error) {
		return []bangumi.Relation{{ID: id * 10, Type: 3, Name: "Remote", Relation: "原声集"}}, nil
	}, subject: func(id int64) (bangumi.MusicSubject, error) {
		if id == 10010 {
			return bangumi.MusicSubject{}, &sourcegate.CooldownError{Source: "bangumi", RetryAfter: time.Minute}
		}
		return bangumi.MusicSubject{ID: id, Name: "Remote"}, nil
	}}
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	state, _ := s.Status(f.ctx)
	if state.Status != "completed" || !strings.Contains(state.ErrorMessage, "持续限流") {
		t.Fatal(state)
	}
	var status string
	if e := f.raw.QueryRow(`SELECT status FROM discovery_work_seeds WHERE work_id=100`).Scan(&status); e != nil || status != "done" {
		t.Fatal(status, e)
	}
	if e := f.raw.QueryRow(`SELECT search_status FROM discovery_bangumi_music WHERE subject_id=10010`).Scan(&status); e != nil || status != "pending" {
		t.Fatal(status, e)
	}
}

func TestWorkMappingRefreshDecisionsAndSuggestionCap(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	now := time.Now()
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, nil, true, now, []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	music := []storage.DiscoveryMusic{{SubjectID: 2000, Name: "Remote", Relation: "原声集", SearchStatus: "hits", SearchUpdated: true, SearchedAt: now.Format(time.RFC3339)}}
	groups := []storage.DiscoveryReleaseGroup{}
	mappings := []storage.DiscoveryMapping{}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("%08d-1111-1111-1111-111111111111", i)
		groups = append(groups, storage.DiscoveryReleaseGroup{RGID: id, Title: "Remote", Kind: "Album", PrimaryType: "Album"})
		mappings = append(mappings, storage.DiscoveryMapping{SubjectID: 2000, RGID: id, Status: "suggested", Score: float64(i)})
	}
	save := func() error {
		return f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, music, groups, mappings, "done", "", now)
	}
	if e = save(); e != nil {
		t.Fatal(e)
	}
	items, c, e := f.store.ListDiscovery(f.ctx, "mapping", "all", 60, 0)
	if e != nil || len(items) != 1 || len(items[0].MappingOptions) != 3 || c.Mapping != 1 {
		t.Fatal(items, c, e)
	}
	if e = f.store.DiscoveryMappingDecision(f.ctx, 2000, groups[0].RGID, "reject"); e != nil {
		t.Fatal(e)
	}
	if e = f.store.DiscoveryMappingDecision(f.ctx, 2000, groups[1].RGID, "confirm"); e != nil {
		t.Fatal(e)
	}
	g, e = f.store.BeginDiscoveryRefresh(f.ctx, nil, true, now.Add(time.Hour), []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	if e = save(); e != nil {
		t.Fatal(e)
	}
	for i, want := range []string{"rejected", "confirmed"} {
		var got string
		if e = f.raw.QueryRow(`SELECT status FROM discovery_bangumi_mappings WHERE subject_id=2000 AND rgid=?`, groups[i].RGID).Scan(&got); e != nil || got != want {
			t.Fatal(got, want, e)
		}
	}
	items, c, e = f.store.ListDiscovery(f.ctx, "mapping", "all", 60, 0)
	if e != nil || len(items) != 0 || c.Mapping != 0 {
		t.Fatal(items, c, e)
	}
	if e = f.store.DiscoveryMappingDecision(f.ctx, 2000, groups[0].RGID, "restore"); e != nil {
		t.Fatal(e)
	}
	if e = f.store.DiscoveryMappingDecision(f.ctx, 2000, groups[0].RGID, "restore"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
}
func TestRerankBeforeCompletedDoesNothing(t *testing.T) {
	f := setup(t)
	f.ready(t)
	s := serviceFor(t, f, func(http.ResponseWriter, *http.Request) { t.Error("rerank used network") })
	if ran, e := s.Rerank(f.ctx); e != nil || ran {
		t.Fatal(ran, e)
	}
}

func TestWorkMappingCoversDownloadBeforeConfirmation(t *testing.T) {
	f := setup(t)
	f.ready(t)
	workFixture(t, f, 100, 1000)
	var covers atomic.Int64
	client := &http.Client{Transport: coverTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"release-groups":[]}`
		if r.URL.Query().Has("query") {
			body = `{"release-groups":[{"id":"` + testRG + `","title":"Remote","primary-type":"Album","score":99},{"id":"` + testMB + `","title":"Remote","primary-type":"Album","score":90},{"id":"99999999-9999-4999-8999-999999999999","title":"Remote","primary-type":"Album","score":80}]}`
		} else if r.URL.Host != "mb.example" {
			covers.Add(1)
			body = "\x89PNG\r\n\x1a\nrest"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := New(f.store, nil, Options{HTTPClient: client, MBBaseURL: "http://mb.example", CAAURL: "http://93.184.216.34", DataDir: t.TempDir(), Gate: sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0, "coverartarchive": 0}}), Bangumi: fakeBangumi{enabled: true, relations: func(int64) ([]bangumi.Relation, error) {
		return []bangumi.Relation{{ID: 2000, Name: "Remote", Type: 3, Relation: "原声集"}}, nil
	}, subject: func(id int64) (bangumi.MusicSubject, error) { return bangumi.MusicSubject{ID: id, Name: "Remote"}, nil }}})
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	if covers.Load() != 3 {
		t.Fatal(covers.Load())
	}
	if _, _, e := s.CachedCover(testRG); e != nil {
		t.Fatal(e)
	}
	items, counts, e := f.store.ListDiscovery(f.ctx, "mapping", "all", 60, 0)
	if e != nil || len(items) != 1 || !items[0].CoverAvailable || len(items[0].MappingOptions) != 3 || counts.Recommended != 0 {
		t.Fatal(items, counts, e)
	}
	for _, option := range items[0].MappingOptions {
		if _, _, e := s.CachedCover(option.RGID); e != nil {
			t.Fatal(option.RGID, e)
		}
	}
}
func TestAcquisitionReleaseGroupWithoutMappingKeepsS2Behavior(t *testing.T) {
	f := setup(t)
	f.ready(t)
	if _, e := f.raw.Exec(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,fetched_at) VALUES(?,'Remote','Album','Album','2026-01-01')`, testRG); e != nil {
		t.Fatal(e)
	}
	if _, e := f.store.AcquisitionReleaseGroup(f.ctx, testRG); e != nil {
		t.Fatal(e)
	}
}

func TestMappingRejectHidesSnapshotWhenRerankSkipped(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	now := time.Now()
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, nil, true, now, []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	group := storage.DiscoveryReleaseGroup{RGID: testRG, Title: "Remote", Kind: "Album", PrimaryType: "Album"}
	if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, []storage.DiscoveryMusic{{SubjectID: 2000, Name: "Remote", Relation: "原声集"}}, []storage.DiscoveryReleaseGroup{group}, []storage.DiscoveryMapping{{SubjectID: 2000, RGID: testRG, Status: "auto"}}, "done", "", now); e != nil {
		t.Fatal(e)
	}
	if e = f.store.SaveDiscoveryCandidates(f.ctx, g, nil, []storage.DiscoveryCandidate{{DiscoveryReleaseGroup: group, SeedWorkID: 100, SubjectID: 2000, Lane: "exploit", Rank: 1}}, nil, []storage.DiscoveryWorkSeed{seed}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.raw.Exec(`UPDATE discovery_refresh_state SET status='failed'`); e != nil {
		t.Fatal(e)
	}
	s := serviceFor(t, f, func(http.ResponseWriter, *http.Request) { t.Error("network") })
	if ran, e := s.MappingDecision(f.ctx, 2000, testRG, "reject"); e != nil || ran {
		t.Fatal(ran, e)
	}
	items, c, e := f.store.ListDiscovery(f.ctx, "recommended", "all", 60, 0)
	if e != nil || len(items) != 0 || c.Recommended != 0 {
		t.Fatal(items, c, e)
	}
}
func TestMappingDecisionStaleRerankStillSucceeds(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, []storage.DiscoverySeed{f.seed}, true, time.Now(), []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	group := storage.DiscoveryReleaseGroup{RGID: testRG, Title: "Remote", Kind: "Album", PrimaryType: "Album"}
	if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, []storage.DiscoveryMusic{{SubjectID: 2000, Name: "Remote", Relation: "原声集"}}, []storage.DiscoveryReleaseGroup{group}, []storage.DiscoveryMapping{{SubjectID: 2000, RGID: testRG, Status: "rejected"}}, "done", "", time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.raw.Exec(`UPDATE discovery_refresh_state SET status='completed';UPDATE artist_external_profiles SET external_id='bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb' WHERE source='musicbrainz'`); e != nil {
		t.Fatal(e)
	}
	s := serviceFor(t, f, func(http.ResponseWriter, *http.Request) { t.Error("network") })
	if ran, e := s.MappingDecision(f.ctx, 2000, testRG, "restore"); e != nil || ran {
		t.Fatal(ran, e)
	}
	var status string
	if e = f.raw.QueryRow(`SELECT status FROM discovery_bangumi_mappings WHERE subject_id=2000`).Scan(&status); e != nil || status != "suggested" {
		t.Fatal(status, e)
	}
}
func TestTTLRetiresOnlyUnreferencedMappingGroups(t *testing.T) {
	for _, reference := range []string{"none", "task", "search", "decision", "artist", "match_rejection"} {
		t.Run(reference, func(t *testing.T) {
			f := setup(t)
			f.ready(t)
			seed := workFixture(t, f, 100, 1000)
			now := time.Now()
			g, e := f.store.BeginDiscoveryRefresh(f.ctx, nil, true, now, []storage.DiscoveryWorkSeed{seed})
			if e != nil {
				t.Fatal(e)
			}
			old := storage.DiscoveryReleaseGroup{RGID: testRG, Title: "Old", Kind: "Album", PrimaryType: "Album"}
			music := []storage.DiscoveryMusic{{SubjectID: 2000, Name: "Old", Relation: "原声集", SearchStatus: "hits"}}
			if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, music, []storage.DiscoveryReleaseGroup{old}, []storage.DiscoveryMapping{{SubjectID: 2000, RGID: testRG, Status: "suggested"}}, "done", "", now); e != nil {
				t.Fatal(e)
			}
			switch reference {
			case "match_rejection":
				_, e = f.raw.Exec(`INSERT INTO discovery_match_rejections(rgid,album_id) VALUES(?,?)`, testRG, f.album)
			case "task":
				_, e = f.raw.Exec(`INSERT INTO acquisition_tasks(public_id,idempotency_key,rgid,title,artist,kind,torrent_title,state) VALUES(?, 'old',?,'Old','Singer','Album','torrent','CANCELLED')`, testMB, testRG)
			case "search":
				_, e = f.raw.Exec(`INSERT INTO acquisition_searches(id,rgid,indexer,query,query_key,result_count) VALUES('old',?,'fake','Old','old',0)`, testRG)
			case "decision":
				_, e = f.raw.Exec(`INSERT INTO release_groups(rgid) VALUES(?)`, testRG)
				if e == nil {
					_, e = f.raw.Exec(`INSERT INTO release_group_decisions(rgid,decision) VALUES(?,'ignore')`, testRG)
				}
			case "artist":
				_, e = f.raw.Exec(`INSERT INTO discovery_artist_release_groups(artist_id,rgid) VALUES(?,?)`, f.seed.ArtistID, testRG)
			}
			if e != nil {
				t.Fatal(e)
			}
			music[0].SearchUpdated = true
			music[0].SearchStatus = "none"
			music[0].SearchedAt = now.Add(MappingTTL).Format(time.RFC3339)
			if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, music, nil, nil, "done", "", now.Add(MappingTTL)); e != nil {
				t.Fatal(e)
			}
			var n int
			if e = f.raw.QueryRow(`SELECT count(*) FROM discovery_release_groups WHERE rgid=?`, testRG).Scan(&n); e != nil {
				t.Fatal(e)
			}
			if reference == "none" {
				if n != 0 {
					t.Fatal("orphan retained")
				}
				return
			}
			if n != 1 {
				t.Fatal("referenced RG removed")
			}
			if reference == "match_rejection" {
				if e = f.raw.QueryRow(`SELECT count(*) FROM discovery_match_rejections WHERE rgid=? AND album_id=?`, testRG, f.album).Scan(&n); e != nil || n != 1 {
					t.Fatal("match rejection removed", n, e)
				}
			}
			if reference == "task" || reference == "search" {
				if _, e = f.store.AcquisitionReleaseGroup(f.ctx, testRG); !errors.Is(e, storage.ErrMappingUnconfirmed) {
					t.Fatal(e)
				}
				if e = f.raw.QueryRow(`SELECT count(*) FROM discovery_bangumi_mappings WHERE rgid=? AND status='suggested'`, testRG).Scan(&n); e != nil || n != 1 {
					t.Fatal(n, e)
				}
			}
		})
	}
}
func TestWorkFallbackDedupAndUnmappedHits(t *testing.T) {
	f := setup(t)
	f.ready(t)
	workFixture(t, f, 100, 1000)
	var searches atomic.Int64
	s := serviceFor(t, f, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("artist") {
			w.Write([]byte(`{"release-groups":[]}`))
			return
		}
		searches.Add(1)
		if strings.Contains(r.URL.Query().Get("query"), "Original") {
			w.Write([]byte(`{"release-groups":[]}`))
			return
		}
		w.Write([]byte(`{"release-groups":[{"id":"` + testRG + `","title":"中文标题","primary-type":"Album","score":100},{"id":"` + testMB + `","title":"Unrelated","primary-type":"Album","score":80}]}`))
	})
	s.bangumi = fakeBangumi{enabled: true, relations: func(int64) ([]bangumi.Relation, error) {
		return []bangumi.Relation{{ID: 2000, Type: 3, Name: "Original", Relation: "原声集"}, {ID: 2000, Type: 3, Name: "Original", Relation: "原声集"}}, nil
	}, subject: func(id int64) (bangumi.MusicSubject, error) {
		return bangumi.MusicSubject{ID: id, Name: "Original", NameCN: "中文标题"}, nil
	}}
	if _, e := s.Refresh(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	s.Wait()
	state, _ := s.Status(f.ctx)
	if state.Status != "completed" || searches.Load() != 2 {
		t.Fatal(state, searches.Load())
	}
	var n int
	if e := f.raw.QueryRow(`SELECT count(*) FROM discovery_release_groups WHERE rgid=?`, testMB).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	items, _, e := f.store.ListDiscovery(f.ctx, "mapping", "all", 60, 0)
	if e != nil || len(items) != 1 || items[0].MappingStatus != "suggested" {
		t.Fatal(items, e)
	}
}

func TestStoredAutoConflictDowngradesBothSubjects(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, nil, true, time.Now(), []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	group := storage.DiscoveryReleaseGroup{RGID: testRG, Title: "Remote", Kind: "Album", PrimaryType: "Album"}
	for _, subject := range []int64{2000, 2001} {
		if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, []storage.DiscoveryMusic{{SubjectID: subject, Name: "Remote", Relation: "原声集"}}, []storage.DiscoveryReleaseGroup{group}, []storage.DiscoveryMapping{{SubjectID: subject, RGID: testRG, Status: "auto"}}, "done", "", time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	var n int
	if e = f.raw.QueryRow(`SELECT count(*) FROM discovery_bangumi_mappings WHERE rgid=? AND status='suggested' AND evidence_json LIKE '%rg_conflict%'`, testRG).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}

func TestWorkSignalsExcludeInvisibleTracks(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	before, e := f.store.DiscoveryWorkSignals(f.ctx)
	if e != nil || before[seed.WorkID].OwnedAlbums != 1 {
		t.Fatal(before, e)
	}
	if _, e = f.raw.Exec(`UPDATE audio_files SET status='missing'`); e != nil {
		t.Fatal(e)
	}
	after, e := f.store.DiscoveryWorkSignals(f.ctx)
	if e != nil || after[seed.WorkID].OwnedAlbums != 0 {
		t.Fatal(after, e)
	}
}

func TestStoredBatchAutoConflictAndShadowedAcquisition(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, nil, true, time.Now(), []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	groups := []storage.DiscoveryReleaseGroup{{RGID: testRG, Title: "Remote", Kind: "Album", PrimaryType: "Album"}, {RGID: testMB, Title: "Other", Kind: "Album", PrimaryType: "Album"}}
	music := []storage.DiscoveryMusic{{SubjectID: 2000, Name: "Remote", Relation: "原声集"}, {SubjectID: 2001, Name: "Remote", Relation: "原声集"}}
	mappings := []storage.DiscoveryMapping{{SubjectID: 2000, RGID: testRG, Status: "auto"}, {SubjectID: 2001, RGID: testRG, Status: "auto"}}
	if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, music, groups, mappings, "failed", "", time.Now()); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.raw.QueryRow(`SELECT count(*) FROM discovery_bangumi_mappings WHERE status='suggested' AND evidence_json LIKE '%rg_conflict%'`).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	if _, e = f.raw.Exec(`DELETE FROM discovery_bangumi_mappings WHERE subject_id=2001;UPDATE discovery_bangumi_mappings SET status='auto' WHERE subject_id=2000`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.AcquisitionReleaseGroup(f.ctx, testRG); e != nil {
		t.Fatal(e)
	}
	if _, e = f.raw.Exec(`INSERT INTO discovery_bangumi_mappings(subject_id,rgid,status) VALUES(2000,?,'confirmed')`, testMB); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.AcquisitionReleaseGroup(f.ctx, testRG); !errors.Is(e, storage.ErrMappingUnconfirmed) {
		t.Fatal(e)
	}
}

func TestMappingDecisionNonStaleRerankErrorStillSucceeds(t *testing.T) {
	f := setup(t)
	f.ready(t)
	seed := workFixture(t, f, 100, 1000)
	g, e := f.store.BeginDiscoveryRefresh(f.ctx, nil, true, time.Now(), []storage.DiscoveryWorkSeed{seed})
	if e != nil {
		t.Fatal(e)
	}
	group := storage.DiscoveryReleaseGroup{RGID: testRG, Title: "Remote", Kind: "Album", PrimaryType: "Album"}
	if e = f.store.SaveDiscoveryWorkSeed(f.ctx, g, seed, []storage.DiscoveryMusic{{SubjectID: 2000, Name: "Remote", Relation: "原声集"}}, []storage.DiscoveryReleaseGroup{group}, []storage.DiscoveryMapping{{SubjectID: 2000, RGID: testRG, Status: "suggested"}}, "done", "", time.Now()); e != nil {
		t.Fatal(e)
	}
	// Invalid catalog JSON makes Rerank fail after the mapping transaction commits.
	if _, e = f.raw.Exec(`UPDATE discovery_refresh_state SET status='completed'; UPDATE discovery_release_groups SET secondary_types_json='broken'`); e != nil {
		t.Fatal(e)
	}
	s := serviceFor(t, f, func(http.ResponseWriter, *http.Request) { t.Error("network") })
	if ran, e := s.MappingDecision(f.ctx, 2000, testRG, "confirm"); e != nil || ran {
		t.Fatal(ran, e)
	}
	var status string
	if e = f.raw.QueryRow(`SELECT status FROM discovery_bangumi_mappings WHERE subject_id=2000`).Scan(&status); e != nil || status != "confirmed" {
		t.Fatal(status, e)
	}
}
