package enrichment

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// bindSubjectWork creates a work bound to a Bangumi subject of the given
// subject type (2 = anime, 4 = game) recorded in the cached profile.
func bindSubjectWork(t *testing.T, store *storage.Store, title, workType string, year int, subjectID int64, subjectType int, date string) int64 {
	t.Helper()
	ctx := context.Background()
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: title, Type: workType, Year: year})
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"id":%d,"type":%d,"date":%q}`, subjectID, subjectType, date)
	profile := storage.ExternalWorkProfile{Source: "bangumi", ExternalID: strconv.FormatInt(subjectID, 10), Title: title, Type: workType, Year: year, Raw: json.RawMessage(raw), FetchedAt: "2026-01-01T00:00:00Z"}
	if err = store.UpsertExternalWorkProfile(ctx, work.ID, profile); err != nil {
		t.Fatal(err)
	}
	return work.ID
}

func pendingSuggestionPairs(t *testing.T, store *storage.Store) map[[2]int64]storage.SeriesSuggestion {
	t.Helper()
	suggestions, err := store.PendingSeriesSuggestions(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[[2]int64]storage.SeriesSuggestion{}
	for _, suggestion := range suggestions {
		out[[2]int64{suggestion.WorkA.ID, suggestion.WorkB.ID}] = suggestion
	}
	return out
}

// D52: real api.bgm.tv captures decide cross-media suggestions. Whitelisted
// bidirectional pairs produce suggestions; the one-way 蛋仔派对 registration
// (鬼滅 registers it as 游戏, 蛋仔派对 registers the reverse as 联动) does not.
func TestSeriesSuggestionCrossPairsRealFixtures(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	kimetsu := bindSubjectWork(t, store, "鬼滅の刃", "anime", 2019, 245665, 2, "2019-04-06")
	hinokami := bindSubjectWork(t, store, "鬼滅の刃 ヒノカミ血風譚", "game", 2021, 302688, 4, "2021-10-14")
	eggy := bindSubjectWork(t, store, "蛋仔派对", "game", 2022, 351722, 4, "2022-05-27")
	index := bindSubjectWork(t, store, "とある魔術の禁書目録", "anime", 2008, 1014, 2, "2008-10-04")
	railgun := bindSubjectWork(t, store, "とある科学の超電磁砲", "anime", 2009, 2585, 2, "2009-10-02")
	bocchi := bindSubjectWork(t, store, "ぼっち・ざ・ろっく！", "anime", 2022, 328609, 2, "2022-10-08")
	bocchiRe := bindSubjectWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:", "movie", 2024, 436738, 2, "2024-06-07")
	stayNight := bindSubjectWork(t, store, "Fate/stay night", "anime", 2006, 290, 2, "2006-01-06")
	hf := bindSubjectWork(t, store, "劇場版 Fate/stay night [Heaven's Feel] I.presage flower", "movie", 2017, 109375, 2, "2017-10-14")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	pairs := pendingSuggestionPairs(t, store)
	if len(pairs) != 4 {
		t.Fatalf("suggestions=%d %+v, want exactly 4", len(pairs), pairs)
	}
	want := map[[2]int64][2]string{
		{kimetsu, hinokami}: {"游戏", "动画"},
		{index, railgun}:    {"衍生", "主线故事"},
		{bocchi, bocchiRe}:  {"总集篇", "全集"},
		{stayNight, hf}:     {"不同演绎", "不同演绎"},
	}
	for key, relations := range want {
		suggestion, ok := pairs[key]
		if !ok {
			t.Fatalf("missing suggestion for works %v", key)
		}
		if suggestion.Kind != "cross" {
			t.Fatalf("pair %v kind=%q, want cross", key, suggestion.Kind)
		}
		got := [2]string{suggestion.RelationAB, suggestion.RelationBA}
		if got != relations && got != [2]string{relations[1], relations[0]} {
			t.Fatalf("pair %v relations=%v, want %v", key, got, relations)
		}
	}
	// 蛋仔派对: 鬼滅 side registers 游戏, the reverse is 联动 — not a
	// whitelisted pair, so no suggestion (this is the D52 mutation guard).
	for key := range pairs {
		if key[0] == eggy || key[1] == eggy {
			t.Fatalf("蛋仔派对 must not be suggested, got %v", key)
		}
	}
	// The type-4 game was never pulled into automatic grouping: no series.
	if seriesCount(t, store) != 0 {
		t.Fatalf("series=%d, want 0", seriesCount(t, store))
	}
}

// 薬屋のひとりごと 剧场版 (599894) is registered as 衍生 of the TV series; the
// 衍生↔主线故事 whitelist pair produces a suggestion — the only way it can
// enter a series, since automatic grouping follows 续集/前传 only.
func TestSeriesSuggestionKusuriyaMovie(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	tv := bindSubjectWork(t, store, "薬屋のひとりごと", "anime", 2023, 420628, 2, "2023-10-21")
	movie := bindSubjectWork(t, store, "薬屋のひとりごと 劇場版", "movie", 2026, 599894, 2, "")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	pairs := pendingSuggestionPairs(t, store)
	key := [2]int64{tv, movie}
	if key[0] > key[1] {
		key = [2]int64{movie, tv}
	}
	suggestion, ok := pairs[key]
	if !ok || suggestion.Kind != "cross" {
		t.Fatalf("missing cross suggestion for kusuriya movie: %+v", pairs)
	}
	if seriesCount(t, store) != 0 {
		t.Fatalf("series=%d, want 0 (剧场版 must not auto-group)", seriesCount(t, store))
	}
}

// D60: a work with a 续集/前传 relation to a manual series member gets a
// sequel suggestion, covering the merged-chain new season gap.
func TestSeriesSuggestionSequelToManualMember(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	re := bindSubjectWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:", "movie", 2024, 436738, 2, "2024-06-07")
	rere := bindSubjectWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:Re:", "movie", 2024, 459642, 2, "2024-08-09")
	if _, err := store.CreateWorkSeries(context.Background(), "総集編（手动）", []int64{re}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	pairs := pendingSuggestionPairs(t, store)
	key := [2]int64{re, rere}
	if key[0] > key[1] {
		key = [2]int64{rere, re}
	}
	suggestion, ok := pairs[key]
	if !ok {
		t.Fatalf("missing sequel suggestion: %+v", pairs)
	}
	if suggestion.Kind != "sequel" {
		t.Fatalf("kind=%q, want sequel", suggestion.Kind)
	}
	// The manual member stayed alone in its series; the automatic pass did
	// not group the pair.
	members := seriesMembersOf(t, store, re)
	if len(members) != 1 || members[re] != "manual" {
		t.Fatalf("members=%v", members)
	}
}

// D56: locked works (here: both members of a dissolved series) never take
// part in suggestions.
func TestSeriesSuggestionSkipsLockedWorks(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	re := bindSubjectWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:", "movie", 2024, 436738, 2, "2024-06-07")
	bindSubjectWork(t, store, "劇場総集編ぼっち・ざ・ろっく！ Re:Re:", "movie", 2024, 459642, 2, "2024-08-09")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	// The compilation pair auto-grouped (续集/前传); dissolve locks both.
	series, _, err := store.SeriesForWork(context.Background(), re)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DissolveWorkSeries(context.Background(), series.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if pairs := pendingSuggestionPairs(t, store); len(pairs) != 0 {
		t.Fatalf("locked works suggested: %+v", pairs)
	}
}

// D55: a rejected subject pair is never suggested again.
func TestSeriesSuggestionRejectRemembers(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	kimetsu := bindSubjectWork(t, store, "鬼滅の刃", "anime", 2019, 245665, 2, "2019-04-06")
	hinokami := bindSubjectWork(t, store, "鬼滅の刃 ヒノカミ血風譚", "game", 2021, 302688, 4, "2021-10-14")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	pairs := pendingSuggestionPairs(t, store)
	key := [2]int64{kimetsu, hinokami}
	if key[0] > key[1] {
		key = [2]int64{hinokami, kimetsu}
	}
	suggestion, ok := pairs[key]
	if !ok {
		t.Fatalf("missing suggestion: %+v", pairs)
	}
	if err := store.RejectSeriesSuggestion(context.Background(), suggestion.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if pairs := pendingSuggestionPairs(t, store); len(pairs) != 0 {
		t.Fatalf("rejected pair suggested again: %+v", pairs)
	}
}

// A failed type-4 relation fetch keeps the work's old suggestions in place.
func TestSeriesSuggestionFailureKeepsOldSuggestions(t *testing.T) {
	fail := map[int64]bool{}
	server := seriesFixtureServer(t, fail)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	bindSubjectWork(t, store, "鬼滅の刃", "anime", 2019, 245665, 2, "2019-04-06")
	bindSubjectWork(t, store, "鬼滅の刃 ヒノカミ血風譚", "game", 2021, 302688, 4, "2021-10-14")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if pairs := pendingSuggestionPairs(t, store); len(pairs) != 1 {
		t.Fatalf("suggestions=%+v, want 1", pairs)
	}
	// The game's relation fetch fails next run: the pair cannot be verified,
	// but the old suggestion must survive untouched.
	fail[302688] = true
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if pairs := pendingSuggestionPairs(t, store); len(pairs) != 1 {
		t.Fatalf("suggestion lost after failed fetch: %+v", pairs)
	}
	// Once both ends are fetched again and the edge is gone from the game's
	// relations, the suggestion would be dropped; here it simply heals.
	fail[302688] = false
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if pairs := pendingSuggestionPairs(t, store); len(pairs) != 1 {
		t.Fatalf("suggestion lost after recovery: %+v", pairs)
	}
}

// seriesSuggestionPairs is pure: whitelist pairs, narrowed sequel pairs
// (D67), one-way relations, locked works, shared series and decided pairs.
func TestSeriesSuggestionPairsPure(t *testing.T) {
	rel := func(id int64, relation string) musicRelation {
		return musicRelation{ID: id, Type: 2, Relation: relation}
	}
	rel4 := func(id int64, relation string) musicRelation {
		return musicRelation{ID: id, Type: 4, Relation: relation}
	}
	relations := map[int64][]musicRelation{
		1:  {rel(2, "游戏")},    // 1 -> 2 whitelisted with reverse below
		2:  {rel(1, "动画")},    // pair (1,2): cross
		3:  {rel(4, "续集")},    // pair (3,4): sequel — 103 is a manual
		4:  {rel(3, "前传")},    //   series member (D67a)
		5:  {rel(6, "游戏")},    // one-way only: 6 has no relation back
		6:  {},                //
		7:  {rel(8, "联动")},    // not whitelisted
		8:  {rel(7, "游戏")},    //
		9:  {rel(10, "衍生")},   // same series: skipped
		10: {rel(9, "主线故事")},  //
		11: {rel(12, "衍生")},   // locked work: skipped
		12: {rel(11, "主线故事")}, //
		13: {rel(14, "番外篇")},  // decided pair: skipped
		14: {rel(13, "主线故事")}, //
		15: {rel(16, "游戏")},   // reverse not fetched: skipped
		17: {rel(18, "续集")},   // plain type-2 sequel pair without a manual
		18: {rel(17, "前传")},   //   member: automatic grouping owns it (D67)
		19: {rel4(20, "续集")},  // both type 4: game sequels still get a
		20: {rel4(19, "前传")},  //   suggestion (D67b)
	}
	lib := map[int64]int64{1: 101, 2: 102, 3: 103, 4: 104, 5: 105, 6: 106, 7: 107, 8: 108, 9: 109, 10: 110, 11: 111, 12: 112, 13: 113, 14: 114, 15: 115, 16: 116, 17: 117, 18: 118, 19: 119, 20: 120}
	workSeries := map[int64]int64{109: 7, 110: 7, 103: 9}
	manualMember := map[int64]bool{103: true}
	type4 := map[int64]bool{19: true, 20: true}
	locked := map[int64]bool{111: true}
	decided := map[[2]int64]bool{{13, 14}: true}
	pairs := seriesSuggestionPairs(relations, lib, workSeries, manualMember, type4, locked, decided)
	got := map[[2]int64]string{}
	for _, pair := range pairs {
		got[[2]int64{pair.subjectA, pair.subjectB}] = pair.kind
	}
	want := map[[2]int64]string{{1, 2}: "cross", {3, 4}: "sequel", {19, 20}: "sequel"}
	if len(got) != len(want) {
		t.Fatalf("pairs=%v, want %v", got, want)
	}
	for key, kind := range want {
		if got[key] != kind {
			t.Fatalf("pair %v kind=%q, want %q (all pairs: %v)", key, got[key], kind, got)
		}
	}
}

// M1: once the game work is locked (detached), the next run deletes its old
// suggestions instead of keeping them forever.
func TestSeriesSuggestionLockedGameCleanedNextRun(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	bindSubjectWork(t, store, "鬼滅の刃", "anime", 2019, 245665, 2, "2019-04-06")
	hinokami := bindSubjectWork(t, store, "鬼滅の刃 ヒノカミ血風譚", "game", 2021, 302688, 4, "2021-10-14")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if pairs := pendingSuggestionPairs(t, store); len(pairs) != 1 {
		t.Fatalf("suggestions=%+v, want 1", pairs)
	}
	// The user detaches the game: a lock is written.
	seriesID, err := store.CreateWorkSeries(context.Background(), "血風譚", []int64{hinokami})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DissolveWorkSeries(context.Background(), seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if pairs := pendingSuggestionPairs(t, store); len(pairs) != 0 {
		t.Fatalf("locked game's suggestion survived: %+v", pairs)
	}
	if n := countSuggestions(t, store); n != 0 {
		t.Fatalf("suggestion row left=%d", n)
	}
}

func countSuggestions(t *testing.T, store *storage.Store) int {
	t.Helper()
	suggestions, err := store.PendingSeriesSuggestions(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(suggestions)
}

// M1: a work rebound to a non type-2/4 subject counts as processed, so its
// old suggestion is cleaned instead of lingering forever.
func TestSeriesSuggestionReboundSubjectCleaned(t *testing.T) {
	server := seriesFixtureServer(t, nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	bindSubjectWork(t, store, "鬼滅の刃", "anime", 2019, 245665, 2, "2019-04-06")
	hinokami := bindSubjectWork(t, store, "鬼滅の刃 ヒノカミ血風譚", "game", 2021, 302688, 4, "2021-10-14")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if n := countSuggestions(t, store); n != 1 {
		t.Fatalf("suggestions=%d, want 1", n)
	}
	// The binding is corrected to a music entry (type 3): the game leaves the
	// suggestion library without any request failure.
	profile := storage.ExternalWorkProfile{Source: "bangumi", ExternalID: "302688", Title: "鬼滅の刃 ヒノカミ血風譚", Type: "game", Raw: json.RawMessage(`{"id":302688,"type":3}`), FetchedAt: "2026-01-01T00:00:00Z"}
	if err := store.UpsertExternalWorkProfile(context.Background(), hinokami, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	if n := countSuggestions(t, store); n != 0 {
		t.Fatalf("rebound work's suggestion survived: %d", n)
	}
}

// D67(a): a plain type-2 sequel pair whose run is tainted gets no
// suggestion — automatic grouping owns it and the next healthy run heals.
func TestSeriesSuggestionTaintedRunProducesNothing(t *testing.T) {
	fail := map[int64]bool{}
	server := syntheticSeriesGraphServer(t, chainEdges([][]int64{{1, 2, 3}}), fail)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	bindSeriesWork(t, store, "Taint A", "anime", 2019, 1, "2019-01-01")
	bindSeriesWork(t, store, "Taint B", "anime", 2021, 2, "2021-01-01")
	bindSeriesWork(t, store, "Taint C", "anime", 2023, 3, "2023-01-01")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	fail[2] = true
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if n := countSuggestions(t, store); n != 0 {
		t.Fatalf("tainted run produced %d suggestions", n)
	}
}

// D67(b): two type-4 works linked by reciprocal 续集/前传 get a sequel
// suggestion — the BFS never walks games, so suggestions are the only way.
func TestSeriesSuggestionGameSequelPair(t *testing.T) {
	server := syntheticSeriesGraphServer(t, chainEdges([][]int64{{200, 201}}), nil)
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	g1 := bindSubjectWork(t, store, "Game One", "game", 2020, 200, 4, "2020-01-01")
	g2 := bindSubjectWork(t, store, "Game Two", "game", 2022, 201, 4, "2022-01-01")
	if _, err := manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	pairs := pendingSuggestionPairs(t, store)
	key := [2]int64{g1, g2}
	if key[0] > key[1] {
		key = [2]int64{g2, g1}
	}
	suggestion, ok := pairs[key]
	if !ok || suggestion.Kind != "sequel" {
		t.Fatalf("missing game sequel suggestion: %+v", pairs)
	}
	if seriesCount(t, store) != 0 {
		t.Fatalf("games auto-grouped: series=%d", seriesCount(t, store))
	}
}

// D66: a series holding only manual members (both renamed) cannot be claimed
// or frozen by an automatic component; two new seasons form their own auto
// series, and sequel suggestions bridge them to the manual works.
func TestSeriesManualOnlySeriesNotStolenAndSequelSuggested(t *testing.T) {
	server := syntheticSeriesServer(t, [][]int64{{1, 2, 3, 4}})
	defer server.Close()
	manager, store := newSeriesManager(t, server)
	m1 := bindSeriesWork(t, store, "Chain Old One", "anime", 2015, 1, "2015-01-01")
	w2 := bindSeriesWork(t, store, "Chain New Two", "anime", 2020, 2, "2020-01-01")
	w3 := bindSeriesWork(t, store, "Chain New Three", "anime", 2022, 3, "2022-01-01")
	m4 := bindSeriesWork(t, store, "Chain Old Four", "anime", 2017, 4, "2017-01-01")
	s1, err := store.CreateWorkSeries(context.Background(), "", []int64{m1})
	if err != nil {
		t.Fatal(err)
	}
	s4, err := store.CreateWorkSeries(context.Background(), "", []int64{m4})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RenameWorkSeries(context.Background(), s1, "改名甲"); err != nil {
		t.Fatal(err)
	}
	if err = store.RenameWorkSeries(context.Background(), s4, "改名乙"); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.enrichBangumiSeries(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	// The two renamed manual-only series were neither claimed nor frozen
	// (B1/D66): they keep their names and single manual members.
	fresh1, err := store.WorkSeriesByID(context.Background(), s1)
	if err != nil || fresh1.Title != "改名甲" || fresh1.MemberCount != 1 {
		t.Fatalf("s1=%+v err=%v", fresh1, err)
	}
	fresh4, err := store.WorkSeriesByID(context.Background(), s4)
	if err != nil || fresh4.Title != "改名乙" || fresh4.MemberCount != 1 {
		t.Fatalf("s4=%+v err=%v", fresh4, err)
	}
	// The new seasons formed their own auto series (the chain is temporarily
	// split, by design D66).
	newSeries, _, err := store.SeriesForWork(context.Background(), w2)
	if err != nil {
		t.Fatalf("new seasons not grouped: %v", err)
	}
	members := seriesMembersOf(t, store, w2)
	if len(members) != 2 || members[w3] != "auto" || newSeries.TitleSource != "auto" {
		t.Fatalf("new series=%+v members=%v", newSeries, members)
	}
	if seriesCount(t, store) != 3 {
		t.Fatalf("series=%d, want 3", seriesCount(t, store))
	}
	// Sequel suggestions bridge the manual ends to the new auto series.
	pairs := pendingSuggestionPairs(t, store)
	for _, key := range [][2]int64{{m1, w2}, {w3, m4}} {
		if key[0] > key[1] {
			key = [2]int64{key[1], key[0]}
		}
		suggestion, ok := pairs[key]
		if !ok || suggestion.Kind != "sequel" {
			t.Fatalf("missing sequel suggestion %v: %+v", key, pairs)
		}
	}
	if len(pairs) != 2 {
		t.Fatalf("suggestions=%+v, want exactly 2", pairs)
	}
}
