package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// seriesFixtureServer serves the real api.bgm.tv /v0/subjects/{id}/subjects
// captures used by the series grouping tests. Files captured for this batch
// use the series-<id>-subjects.json name; ids already tracked from earlier
// batches reuse their <id>-subjects.json capture unchanged.
func seriesFixtureServer(t *testing.T, fail map[int64]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tail := strings.TrimPrefix(r.URL.Path, "/v0/subjects/")
		parts := strings.Split(tail, "/")
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || len(parts) != 2 || parts[1] != "subjects" {
			http.NotFound(w, r)
			return
		}
		if fail[id] {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		for _, name := range []string{fmt.Sprintf("series-%d-subjects.json", id), fmt.Sprintf("%d-subjects.json", id)} {
			raw, readErr := os.ReadFile(filepath.Join("testdata", name))
			if readErr == nil {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
				return
			}
		}
		http.NotFound(w, r)
	}))
}

func newSeriesManager(t *testing.T, server *httptest.Server) (*Manager, *storage.Store) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "series.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	phase4DBPath[store] = dbPath
	t.Cleanup(func() {
		delete(phase4DBPath, store)
		_ = store.Close()
	})
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.phaseEndpoints.BangumiAPI = server.URL
	manager.client = server.Client()
	manager.bangumiInterval = 0
	setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
	setting.Enabled = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	return manager, store
}

// bindSeriesWork creates a work bound to a Bangumi type-2 subject with the
// given air date recorded in the cached profile.
func bindSeriesWork(t *testing.T, store *storage.Store, title, workType string, year int, subjectID int64, date string) int64 {
	t.Helper()
	ctx := context.Background()
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: title, Type: workType, Year: year})
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"id":%d,"type":2,"date":%q}`, subjectID, date)
	profile := storage.ExternalWorkProfile{Source: "bangumi", ExternalID: strconv.FormatInt(subjectID, 10), Title: title, Type: workType, Year: year, Raw: json.RawMessage(raw), FetchedAt: "2026-01-01T00:00:00Z"}
	if err = store.UpsertExternalWorkProfile(ctx, work.ID, profile); err != nil {
		t.Fatal(err)
	}
	return work.ID
}

func seriesMembersOf(t *testing.T, store *storage.Store, workID int64) map[int64]string {
	t.Helper()
	series, members, err := store.SeriesForWork(context.Background(), workID)
	if err != nil {
		t.Fatalf("work %d has no series: %v", workID, err)
	}
	if series.ID == 0 {
		t.Fatal("empty series")
	}
	out := map[int64]string{}
	for _, member := range members {
		out[member.Work.ID] = member.Source
	}
	return out
}

func seriesCount(t *testing.T, store *storage.Store) int {
	t.Helper()
	all, err := store.ListSeries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

func TestSeriesKimetsuGrouping(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	tv := bindSeriesWork(t, store, "鬼滅の刃", "anime", 2019, 245665, "2019-04-06")
	movie := bindSeriesWork(t, store, "劇場版 鬼滅の刃 無限列車編", "movie", 2020, 291494, "2020-10-16")
	train := bindSeriesWork(t, store, "鬼滅の刃 無限列車編", "anime", 2021, 350764, "2021-10-10")
	district := bindSeriesWork(t, store, "鬼滅の刃 遊郭編", "anime", 2021, 328195, "2021-12-05")
	outcome, err := manager.enrichBangumiSeries(context.Background(), 0, false)
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	members := seriesMembersOf(t, store, tv)
	if len(members) != 4 || members[movie] != "auto" || members[train] != "auto" || members[district] != "auto" {
		t.Fatalf("members=%v", members)
	}
	series, _, _ := store.SeriesForWork(context.Background(), tv)
	if series.RepresentativeWorkID != tv || series.Title != "鬼滅の刃" || series.TitleSource != "auto" {
		t.Fatalf("series=%+v", series)
	}
	updatedAt := series.UpdatedAt
	// Idempotent rerun: same outcome, no membership churn, no updated_at bump.
	if _, err = manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	series, _, _ = store.SeriesForWork(context.Background(), tv)
	if series.UpdatedAt != updatedAt {
		t.Fatalf("updated_at moved %s -> %s", updatedAt, series.UpdatedAt)
	}
}

func TestSeriesRailgunFourSeasons(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	s1 := bindSeriesWork(t, store, "とある科学の超電磁砲", "anime", 2009, 2585, "2009-10-02")
	s2 := bindSeriesWork(t, store, "とある科学の超電磁砲S", "anime", 2013, 51928, "2013-04-12")
	s3 := bindSeriesWork(t, store, "とある科学の超電磁砲T", "anime", 2020, 262940, "2020-01-10")
	s4 := bindSeriesWork(t, store, "とある科学の超電磁砲 第4期", "anime", 2026, 537743, "")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	members := seriesMembersOf(t, store, s1)
	if len(members) != 4 || members[s2] != "auto" || members[s3] != "auto" || members[s4] != "auto" {
		t.Fatalf("members=%v", members)
	}
	series, _, _ := store.SeriesForWork(context.Background(), s1)
	if series.RepresentativeWorkID != s1 {
		t.Fatalf("representative=%d want %d", series.RepresentativeWorkID, s1)
	}
}

// The library is missing the middle seasons: the chain still connects through
// library-external subjects within the hop budget (D23).
func TestSeriesConnectsThroughExternalNodes(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	s1 := bindSeriesWork(t, store, "とある科学の超電磁砲", "anime", 2009, 2585, "2009-10-02")
	s4 := bindSeriesWork(t, store, "とある科学の超電磁砲 第4期", "anime", 2026, 537743, "")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	members := seriesMembersOf(t, store, s1)
	if len(members) != 2 || members[s4] != "auto" {
		t.Fatalf("members=%v", members)
	}
}

// The two compilation movies never join the TV series (D21: 总集篇 relations
// are not followed).
func TestSeriesBocchiExcludesCompilation(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	tv := bindSeriesWork(t, store, "ぼっち・ざ・ろっく！", "anime", 2022, 328609, "2022-10-08")
	s2 := bindSeriesWork(t, store, "ぼっち・ざ・ろっく！ 第2期", "anime", 2026, 537409, "")
	compilation := bindSeriesWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:", "movie", 2024, 436738, "2024-06-07")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	members := seriesMembersOf(t, store, tv)
	if len(members) != 2 || members[s2] != "auto" {
		t.Fatalf("members=%v", members)
	}
	if _, _, err := store.SeriesForWork(context.Background(), compilation); err != sql.ErrNoRows {
		t.Fatalf("compilation grouped: %v", err)
	}
}

// D22: the Fate/Zero -> stay night chain groups as Bangumi records it.
func TestSeriesFateChain(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	zero := bindSeriesWork(t, store, "Fate/Zero", "anime", 2011, 10639, "2011-10-01")
	stayNight := bindSeriesWork(t, store, "Fate/stay night", "anime", 2006, 290, "2006-01-06")
	ubw2010 := bindSeriesWork(t, store, "Fate/stay night UNLIMITED BLADE WORKS", "movie", 2010, 3484, "2010-01-23")
	ubw := bindSeriesWork(t, store, "Fate/stay night [Unlimited Blade Works]", "anime", 2014, 95225, "2014-10-04")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	members := seriesMembersOf(t, store, zero)
	if len(members) != 4 || members[stayNight] != "auto" || members[ubw] != "auto" || members[ubw2010] != "auto" {
		t.Fatalf("members=%v", members)
	}
	series, _, _ := store.SeriesForWork(context.Background(), zero)
	// D25: the representative is the earliest-aired library work.
	if series.RepresentativeWorkID != stayNight || series.Title != "Fate/stay night" {
		t.Fatalf("series=%+v", series)
	}
}

func TestSeriesKusuriyaGrouping(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	s1 := bindSeriesWork(t, store, "薬屋のひとりごと", "anime", 2023, 420628, "2023-10-21")
	s2 := bindSeriesWork(t, store, "薬屋のひとりごと 第2期", "anime", 2025, 486347, "2025-01-10")
	s3 := bindSeriesWork(t, store, "薬屋のひとりごと 第3期", "anime", 2026, 568244, "")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	if members := seriesMembersOf(t, store, s1); len(members) != 3 || members[s2] != "auto" || members[s3] != "auto" {
		t.Fatalf("members=%v", members)
	}
}

// A failed request marks the component dirty: existing membership is left
// untouched while healthy components in the same run still apply.
func TestSeriesFailureKeepsMembership(t *testing.T) {
	fail := map[int64]bool{}
	server := seriesFixtureServer(t, fail)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	tv := bindSeriesWork(t, store, "鬼滅の刃", "anime", 2019, 245665, "2019-04-06")
	movie := bindSeriesWork(t, store, "劇場版 鬼滅の刃 無限列車編", "movie", 2020, 291494, "2020-10-16")
	train := bindSeriesWork(t, store, "鬼滅の刃 無限列車編", "anime", 2021, 350764, "2021-10-10")
	district := bindSeriesWork(t, store, "鬼滅の刃 遊郭編", "anime", 2021, 328195, "2021-12-05")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if members := seriesMembersOf(t, store, tv); len(members) != 4 {
		t.Fatalf("members=%v", members)
	}
	// The whole 鬼滅 component becomes unreachable: nothing may change.
	for id := range map[int64]bool{245665: true, 291494: true, 350764: true, 328195: true} {
		fail[id] = true
	}
	k1 := bindSeriesWork(t, store, "薬屋のひとりごと", "anime", 2023, 420628, "2023-10-21")
	k2 := bindSeriesWork(t, store, "薬屋のひとりごと 第2期", "anime", 2025, 486347, "2025-01-10")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	members := seriesMembersOf(t, store, tv)
	if len(members) != 4 || members[movie] != "auto" || members[train] != "auto" || members[district] != "auto" {
		t.Fatalf("membership changed on failure: %v", members)
	}
	// The healthy component in the same run was applied normally.
	if members := seriesMembersOf(t, store, k1); len(members) != 2 || members[k2] != "auto" {
		t.Fatalf("kusuriya members=%v", members)
	}
}

// A user-detached work never comes back; a renamed series title and a manual
// member survive automatic reruns (R3, D25).
func TestSeriesUserIntentSurvivesRerun(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	tv := bindSeriesWork(t, store, "鬼滅の刃", "anime", 2019, 245665, "2019-04-06")
	train := bindSeriesWork(t, store, "鬼滅の刃 無限列車編", "anime", 2021, 350764, "2021-10-10")
	district := bindSeriesWork(t, store, "鬼滅の刃 遊郭編", "anime", 2021, 328195, "2021-12-05")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if err := store.DetachWorkFromSeries(context.Background(), district); err != nil {
		t.Fatal(err)
	}
	series, _, _ := store.SeriesForWork(context.Background(), tv)
	if err := store.RenameWorkSeries(context.Background(), series.ID, "鬼灭（用户命名）"); err != nil {
		t.Fatal(err)
	}
	extra, err := store.CreateWork(context.Background(), storage.WorkInput{Title: "鬼滅の刃 刀鍛冶の里編", Type: "anime", Year: 2023})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkToSeries(context.Background(), extra.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SeriesForWork(context.Background(), district); err != sql.ErrNoRows {
		t.Fatalf("detached work regrouped: %v", err)
	}
	series, members, err := store.SeriesForWork(context.Background(), tv)
	if err != nil {
		t.Fatal(err)
	}
	if series.Title != "鬼灭（用户命名）" || series.TitleSource != "manual" {
		t.Fatalf("title overwritten: %+v", series)
	}
	if series.RepresentativeWorkID != tv {
		t.Fatalf("representative=%d", series.RepresentativeWorkID)
	}
	byID := map[int64]string{}
	for _, member := range members {
		byID[member.Work.ID] = member.Source
	}
	if len(byID) != 3 || byID[train] != "auto" || byID[extra.ID] != "manual" {
		t.Fatalf("members=%v", byID)
	}
}

func TestSeriesSingletonDoesNotGroup(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	bindSeriesWork(t, store, "とある科学の超電磁砲 第4期", "anime", 2026, 537743, "")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 0 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
}

// syntheticSeriesServer builds relation graphs inline from chains.
func syntheticSeriesServer(t *testing.T, chains [][]int64) *httptest.Server {
	return syntheticSeriesGraphServer(t, chainEdges(chains), nil)
}

func chainEdges(chains [][]int64) [][2]int64 {
	var edges [][2]int64
	for _, chain := range chains {
		for i := 0; i+1 < len(chain); i++ {
			edges = append(edges, [2]int64{chain[i], chain[i+1]})
		}
	}
	return edges
}

// syntheticSeriesGraphServer serves undirected sequel/prequel relations from
// an edge list (both directions, in edge order). Subjects in fail answer 502.
func syntheticSeriesGraphServer(t *testing.T, edges [][2]int64, fail map[int64]bool) *httptest.Server {
	t.Helper()
	type rel struct {
		ID       int64  `json:"id"`
		Type     int    `json:"type"`
		Name     string `json:"name"`
		Relation string `json:"relation"`
	}
	adjacency := map[int64][]rel{}
	for _, edge := range edges {
		adjacency[edge[0]] = append(adjacency[edge[0]], rel{ID: edge[1], Type: 2, Name: "next", Relation: "续集"})
		adjacency[edge[1]] = append(adjacency[edge[1]], rel{ID: edge[0], Type: 2, Name: "prev", Relation: "前传"})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tail := strings.TrimPrefix(r.URL.Path, "/v0/subjects/")
		parts := strings.Split(tail, "/")
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || len(parts) != 2 || parts[1] != "subjects" {
			http.NotFound(w, r)
			return
		}
		if fail[id] {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		out := adjacency[id]
		if out == nil {
			out = []rel{}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

// A path may leave the library for at most 3 hops (D23): a bridge of three
// external seasons connects, a bridge of four does not.
func TestSeriesOutsideHopLimit(t *testing.T) {
	// Chain 1..5 bridges the two library works with exactly 3 external hops;
	// chain 10..16 would need 5 external hops.
	server := syntheticSeriesServer(t, [][]int64{{1, 2, 3, 4, 5}, {10, 11, 12, 13, 14, 15, 16}})
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	near := bindSeriesWork(t, store, "Chain Near Start", "anime", 2019, 1, "2019-01-01")
	nearEnd := bindSeriesWork(t, store, "Chain Near End", "anime", 2024, 5, "2024-01-01")
	far := bindSeriesWork(t, store, "Chain Far Start", "anime", 2019, 10, "2019-01-01")
	farEnd := bindSeriesWork(t, store, "Chain Far End", "anime", 2024, 16, "2024-01-01")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if members := seriesMembersOf(t, store, near); len(members) != 2 || members[nearEnd] != "auto" {
		t.Fatalf("3-hop bridge not connected: %v", members)
	}
	for _, id := range []int64{far, farEnd} {
		if _, _, err := store.SeriesForWork(context.Background(), id); err != sql.ErrNoRows {
			t.Fatalf("4+-hop work %d grouped: %v", id, err)
		}
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
}

// Components are truncated at 40 nodes: the chain below has a library work
// at every other node, so the 3-hop budget never engages and only the node
// cap can cut the component (M2).
func TestSeriesNodeCapTruncates(t *testing.T) {
	chain := make([]int64, 45)
	for i := range chain {
		chain[i] = int64(i + 1)
	}
	server := syntheticSeriesServer(t, [][]int64{chain})
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	workBySubject := map[int64]int64{}
	for i := 1; i <= 45; i += 2 {
		workBySubject[int64(i)] = bindSeriesWork(t, store, fmt.Sprintf("Cap %d", i), "anime", 2000+i, int64(i), "")
	}
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	// Nodes 1..40 form the first component: the works at 1,3,...,39 (20 works).
	first := seriesMembersOf(t, store, workBySubject[1])
	if len(first) != 20 {
		t.Fatalf("first component members=%d, want 20", len(first))
	}
	if _, ok := first[workBySubject[41]]; ok {
		t.Fatal("work beyond the node cap joined the first component")
	}
	// Nodes 41..45 form a second component with the remaining 3 works.
	second := seriesMembersOf(t, store, workBySubject[41])
	if len(second) != 3 || second[workBySubject[43]] == "" || second[workBySubject[45]] == "" {
		t.Fatalf("second component members=%v", second)
	}
	if seriesCount(t, store) != 2 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
}

// M1: the outside-hop budget must not depend on traversal order. Subject 100
// is first discovered through a 3-hop external path (1-2-3-100) and later
// through the 1-hop path via library work 10; only the smaller count lets the
// chain reach library work 20.
func TestSeriesOutsideHopOrderIndependent(t *testing.T) {
	edges := [][2]int64{{1, 2}, {2, 3}, {3, 100}, {100, 101}, {101, 102}, {102, 20}, {1, 103}, {103, 10}, {10, 100}}
	server := syntheticSeriesGraphServer(t, edges, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	w1 := bindSeriesWork(t, store, "Order Start", "anime", 2019, 1, "2019-01-01")
	w2 := bindSeriesWork(t, store, "Order Middle", "anime", 2021, 10, "2021-01-01")
	w3 := bindSeriesWork(t, store, "Order End", "anime", 2023, 20, "2023-01-01")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	members := seriesMembersOf(t, store, w1)
	if len(members) != 3 || members[w2] != "auto" || members[w3] != "auto" {
		t.Fatalf("members=%v", members)
	}
}

// B2: with the middle of a chain failing, every component touching the
// tainted nodes is skipped — the series, its members and the user's renamed
// title all survive the partial outage.
func TestSeriesPartialFailureKeepsSeriesAndTitle(t *testing.T) {
	fail := map[int64]bool{}
	server := syntheticSeriesGraphServer(t, chainEdges([][]int64{{1, 2, 3}}), fail)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	a := bindSeriesWork(t, store, "Chain A", "anime", 2019, 1, "2019-01-01")
	b := bindSeriesWork(t, store, "Chain B", "anime", 2021, 2, "2021-01-01")
	c := bindSeriesWork(t, store, "Chain C", "anime", 2023, 3, "2023-01-01")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	series, _, _ := store.SeriesForWork(context.Background(), a)
	if err := store.RenameWorkSeries(context.Background(), series.ID, "用户改过的系列名"); err != nil {
		t.Fatal(err)
	}
	fail[2] = true
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	series, members, err := store.SeriesForWork(context.Background(), a)
	if err != nil {
		t.Fatalf("series lost after partial failure: %v", err)
	}
	if series.Title != "用户改过的系列名" {
		t.Fatalf("title lost: %+v", series)
	}
	byID := map[int64]string{}
	for _, member := range members {
		byID[member.Work.ID] = member.Source
	}
	if len(byID) != 3 || byID[b] != "auto" || byID[c] != "auto" {
		t.Fatalf("members=%v", byID)
	}
}

// M3: a work whose binding moved to a non type-2 subject is no longer part of
// the library; its stale automatic membership is cleaned without a lock.
func TestSeriesNonType2BindingCleansMembership(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	tv := bindSeriesWork(t, store, "ぼっち・ざ・ろっく！", "anime", 2022, 328609, "2022-10-08")
	s2 := bindSeriesWork(t, store, "ぼっち・ざ・ろっく！ 第2期", "anime", 2026, 537409, "")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if members := seriesMembersOf(t, store, tv); len(members) != 2 {
		t.Fatalf("members=%v", members)
	}
	// The second season's profile now says the subject is not a type-2 entry.
	profile := storage.ExternalWorkProfile{Source: "bangumi", ExternalID: "537409", Title: "ぼっち・ざ・ろっく！ 第2期", Type: "anime", Raw: json.RawMessage(`{"id":537409,"type":3}`), FetchedAt: "2026-01-01T00:00:00Z"}
	if err := store.UpsertExternalWorkProfile(context.Background(), s2, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SeriesForWork(context.Background(), s2); err != sql.ErrNoRows {
		t.Fatalf("stale membership kept: %v", err)
	}
	if locked, _ := store.WorkSeriesLocked(context.Background(), s2); locked {
		t.Fatal("stale cleanup must not lock the work")
	}
	if seriesCount(t, store) != 0 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
}

// Both compilation movies are in the library: they never join the TV series
// (总集篇 edges are not followed), but their own mutual 续集/前传 relation
// groups them into a separate series (R2: Bangumi is authoritative).
func TestSeriesBocchiCompilationPairFormsOwnSeries(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	tv := bindSeriesWork(t, store, "ぼっち・ざ・ろっく！", "anime", 2022, 328609, "2022-10-08")
	s2 := bindSeriesWork(t, store, "ぼっち・ざ・ろっく！ 第2期", "anime", 2026, 537409, "")
	re := bindSeriesWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:", "movie", 2024, 436738, "2024-06-07")
	rere := bindSeriesWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:Re:", "movie", 2024, 459642, "2024-08-09")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 2 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	tvMembers := seriesMembersOf(t, store, tv)
	if len(tvMembers) != 2 || tvMembers[s2] != "auto" {
		t.Fatalf("tv members=%v", tvMembers)
	}
	movieMembers := seriesMembersOf(t, store, re)
	if len(movieMembers) != 2 || movieMembers[rere] != "auto" {
		t.Fatalf("movie members=%v", movieMembers)
	}
}

// Medium-1: once the circuit breaker trips, the seed type-resolution loop
// stops issuing subject-detail requests, and the abort error reports the
// failure count that tripped it.
func TestSeriesAbortStopsDetailRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	// Profiles without a cached subject type force a detail fetch per seed.
	for i := 0; i < maxConsecutiveFailures+3; i++ {
		bindSeriesWorkRaw(t, store, fmt.Sprintf("Abort Seed %d", i), int64(100+i), `{"id":100}`)
	}
	outcome, err := manager.enrichBangumiSeries(context.Background(), 0, false)
	if err == nil || outcome != "" {
		t.Fatalf("outcome=%q err=%v, want abort error", outcome, err)
	}
	if got := requests.Load(); got != maxConsecutiveFailures {
		t.Fatalf("requests=%d, want exactly %d (breaker must stop the loop)", got, maxConsecutiveFailures)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("after %d consecutive failures", maxConsecutiveFailures)) {
		t.Fatalf("error=%q", err)
	}
}

// bindSeriesWorkRaw binds a work to a subject with a custom cached profile
// payload (e.g. one without a recorded subject type).
func bindSeriesWorkRaw(t *testing.T, store *storage.Store, title string, subjectID int64, raw string) int64 {
	t.Helper()
	ctx := context.Background()
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: title, Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	profile := storage.ExternalWorkProfile{Source: "bangumi", ExternalID: strconv.FormatInt(subjectID, 10), Title: title, Type: "anime", Raw: json.RawMessage(raw), FetchedAt: "2026-01-01T00:00:00Z"}
	if err = store.UpsertExternalWorkProfile(ctx, work.ID, profile); err != nil {
		t.Fatal(err)
	}
	return work.ID
}

// When the circuit breaker aborts the stage, the run hears about it instead
// of recording a silent skip: components completed before the trip are
// applied, untouched series keep members and titles, and the M3 stale-member
// cleanup is suppressed while aborted.
func TestSeriesAbortReturnsError(t *testing.T) {
	type rel struct {
		ID       int64  `json:"id"`
		Type     int    `json:"type"`
		Name     string `json:"name"`
		Relation string `json:"relation"`
	}
	adjacency := map[int64][]rel{}
	addEdge := func(a, b int64) {
		adjacency[a] = append(adjacency[a], rel{ID: b, Type: 2, Name: "next", Relation: "续集"})
		adjacency[b] = append(adjacency[b], rel{ID: a, Type: 2, Name: "prev", Relation: "前传"})
	}
	// Four series: {1,2}, {3,4}, {5,6,7}, {11,12}.
	for _, edge := range [][2]int64{{1, 2}, {3, 4}, {5, 6}, {6, 7}, {11, 12}} {
		addEdge(edge[0], edge[1])
	}
	fail := map[int64]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tail := strings.TrimPrefix(r.URL.Path, "/v0/subjects/")
		parts := strings.Split(tail, "/")
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || len(parts) != 2 || parts[1] != "subjects" {
			http.NotFound(w, r)
			return
		}
		if fail[id] {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		out := adjacency[id]
		if out == nil {
			out = []rel{}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	works := map[int64]int64{}
	titles := []string{"Abort A", "Abort B", "Abort C", "Abort D", "Abort E", "Abort F", "Abort X", "Abort G", "Abort H"}
	for i, subject := range []int64{1, 2, 3, 4, 5, 6, 7, 11, 12} {
		works[subject] = bindSeriesWork(t, store, titles[i], "anime", 2020+i, subject, "")
	}
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if seriesCount(t, store) != 4 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	series1, _, _ := store.SeriesForWork(context.Background(), works[1])
	if err := store.RenameWorkSeries(context.Background(), series1.ID, "熔断前的名字"); err != nil {
		t.Fatal(err)
	}
	// X (subject 7) now reports a non type-2 subject: it leaves the library
	// without any fetch failure, making it an M3 stale-membership candidate.
	profile := storage.ExternalWorkProfile{Source: "bangumi", ExternalID: "7", Title: "Abort X", Type: "anime", Raw: json.RawMessage(`{"id":7,"type":3}`), FetchedAt: "2026-01-01T00:00:00Z"}
	if err := store.UpsertExternalWorkProfile(context.Background(), works[7], profile); err != nil {
		t.Fatal(err)
	}
	// The first component of the failing run succeeds and gains a new member;
	// everything after it fails until the breaker trips (5 consecutive).
	newcomer := bindSeriesWork(t, store, "Abort New", "anime", 2030, 10, "")
	addEdge(1, 10)
	for _, id := range []int64{3, 4, 5, 6, 11, 12} {
		fail[id] = true
	}
	outcome, err := manager.enrichBangumiSeries(context.Background(), 0, true)
	if err == nil || outcome != "" {
		t.Fatalf("outcome=%q err=%v, want abort error", outcome, err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("after %d consecutive failures", maxConsecutiveFailures)) {
		t.Fatalf("error=%q", err)
	}
	// The component that succeeded before the trip was applied: the renamed
	// series gained the newcomer and kept its title.
	series1, members1, err := store.SeriesForWork(context.Background(), works[1])
	if err != nil {
		t.Fatal(err)
	}
	if series1.Title != "熔断前的名字" {
		t.Fatalf("title lost: %+v", series1)
	}
	if len(members1) != 3 || seriesMembersOf(t, store, works[1])[newcomer] != "auto" {
		t.Fatalf("members1=%v", members1)
	}
	// The failing series were not touched at all.
	if seriesCount(t, store) != 4 {
		t.Fatalf("series after abort=%d", seriesCount(t, store))
	}
	if members := seriesMembersOf(t, store, works[3]); len(members) != 2 || members[works[4]] != "auto" {
		t.Fatalf("series2 members=%v", members)
	}
	if members := seriesMembersOf(t, store, works[11]); len(members) != 2 || members[works[12]] != "auto" {
		t.Fatalf("series4 members=%v", members)
	}
	// The aborted run must not clean the stale auto member X (M3 guard).
	if members := seriesMembersOf(t, store, works[5]); len(members) != 3 || members[works[7]] != "auto" {
		t.Fatalf("series3 members=%v, stale member X must survive the aborted run", members)
	}
}

// A relation cycle terminates instead of looping forever.
func TestSeriesCycleTerminates(t *testing.T) {
	server := syntheticSeriesServer(t, [][]int64{{1, 2, 3, 1}})
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	a := bindSeriesWork(t, store, "Cycle A", "anime", 2019, 1, "2019-01-01")
	b := bindSeriesWork(t, store, "Cycle B", "anime", 2021, 2, "2021-01-01")
	c := bindSeriesWork(t, store, "Cycle C", "anime", 2023, 3, "2023-01-01")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	members := seriesMembersOf(t, store, a)
	if len(members) != 3 || members[b] != "auto" || members[c] != "auto" {
		t.Fatalf("members=%v", members)
	}
}
