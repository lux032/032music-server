package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func newSeriesStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "series.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func mustWork(t *testing.T, store *Store, ctx context.Context, title string, year int) Work {
	t.Helper()
	work, err := store.CreateWork(ctx, WorkInput{Title: title, Year: year})
	if err != nil {
		t.Fatal(err)
	}
	return work
}

func seriesIDs(t *testing.T, store *Store, ctx context.Context, seriesID int64) map[int64]string {
	t.Helper()
	members, err := store.SeriesMembers(ctx, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, member := range members {
		out[member.Work.ID] = member.Source
	}
	return out
}

func onlySeries(t *testing.T, store *Store, ctx context.Context) WorkSeries {
	t.Helper()
	all, err := store.ListSeries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("series count=%d, want 1 (%+v)", len(all), all)
	}
	return all[0]
}

func TestApplyAutoSeriesCreatesAndIdempotent(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "First", 2019)
	b := mustWork(t, store, ctx, "Second", 2021)
	c := mustWork(t, store, ctx, "Third", 2023)
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SeriesCreated != 1 || stats.MembersAdded != 3 {
		t.Fatalf("stats=%+v", stats)
	}
	series := onlySeries(t, store, ctx)
	if series.RepresentativeWorkID != a.ID || series.Title != "First" || series.TitleSource != "auto" {
		t.Fatalf("series=%+v", series)
	}
	// A repeated run is a no-op: no stats, no updated_at bump.
	again, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if again != (SeriesApplyStats{}) {
		t.Fatalf("second run stats=%+v", again)
	}
	fresh := onlySeries(t, store, ctx)
	if fresh.UpdatedAt != series.UpdatedAt {
		t.Fatalf("updated_at moved: %s -> %s", series.UpdatedAt, fresh.UpdatedAt)
	}
}

func TestApplyAutoSeriesSingletonDoesNotCreate(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Alone", 2020)
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats != (SeriesApplyStats{}) {
		t.Fatalf("stats=%+v", stats)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("series=%+v", all)
	}
}

func TestApplyAutoSeriesReusesExistingSeriesByOverlap(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Alpha", 2019)
	b := mustWork(t, store, ctx, "Beta", 2021)
	c := mustWork(t, store, ctx, "Gamma", 2023)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.RenameWorkSeries(ctx, series.ID, "用户改过的名字"); err != nil {
		t.Fatal(err)
	}
	// The next run sees a differently-shaped component that still overlaps the
	// existing series: the series (and its manual title) is reused.
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{b.ID, c.ID}, {a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SeriesCreated != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	all, _ := store.ListSeries(ctx)
	if len(all) != 1 || all[0].ID != series.ID {
		t.Fatalf("series=%+v", all)
	}
	if all[0].Title != "用户改过的名字" || all[0].TitleSource != "manual" {
		t.Fatalf("title overwritten: %+v", all[0])
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 2 || members[b.ID] != "auto" || members[c.ID] != "auto" {
		t.Fatalf("members=%v", members)
	}
	// The representative follows the earliest member even with a manual title.
	if all[0].RepresentativeWorkID != b.ID {
		t.Fatalf("representative=%d want %d", all[0].RepresentativeWorkID, b.ID)
	}
}

func TestApplyAutoSeriesLockedWorkStaysOut(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "One", 2019)
	b := mustWork(t, store, ctx, "Two", 2021)
	c := mustWork(t, store, ctx, "Three", 2023)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DetachWorkFromSeries(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	locked, _ := store.WorkSeriesLocked(ctx, c.ID)
	if !locked {
		t.Fatal("work not locked after detach")
	}
	// The same component comes back from Bangumi; the locked work must not
	// regain membership.
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 2 || members[a.ID] != "auto" || members[b.ID] != "auto" {
		t.Fatalf("members=%v", members)
	}
}

func TestApplyAutoSeriesManualMemberSurvives(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Auto A", 2019)
	b := mustWork(t, store, ctx, "Auto B", 2021)
	extra := mustWork(t, store, ctx, "Manual Extra", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.AddWorkToSeries(ctx, extra.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	// The manual member is not in any Bangumi component; it must stay.
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 3 || members[extra.ID] != "manual" {
		t.Fatalf("members=%v", members)
	}
	// A locked manual add clears the lock.
	if err := store.DetachWorkFromSeries(ctx, extra.ID); err != nil {
		t.Fatal(err)
	}
	if locked, _ := store.WorkSeriesLocked(ctx, extra.ID); !locked {
		t.Fatal("not locked")
	}
	if err := store.AddWorkToSeries(ctx, extra.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	if locked, _ := store.WorkSeriesLocked(ctx, extra.ID); locked {
		t.Fatal("lock not cleared by manual add")
	}
}

func TestApplyAutoSeriesFailureKeepsMembership(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Keep A", 2019)
	b := mustWork(t, store, ctx, "Keep B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	// A run whose fetches failed passes no components at all: nothing changes.
	stats, err := store.ApplyAutoSeries(ctx, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (SeriesApplyStats{}) {
		t.Fatalf("stats=%+v", stats)
	}
	if members := seriesIDs(t, store, ctx, series.ID); len(members) != 2 {
		t.Fatalf("members=%v", members)
	}
}

func TestApplyAutoSeriesDeletesDegenerate(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Split A", 2019)
	b := mustWork(t, store, ctx, "Split B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	// Bangumi no longer connects the two works: both components are
	// singletons, so the auto series dissolves.
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID}, {b.ID}}); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("series left=%+v", all)
	}
}

func TestDissolveWorkSeriesLocksMembers(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Gone A", 2019)
	b := mustWork(t, store, ctx, "Gone B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.DissolveWorkSeries(ctx, series.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("series left=%+v", all)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if locked, _ := store.WorkSeriesLocked(ctx, id); !locked {
			t.Fatalf("work %d not locked after dissolve", id)
		}
	}
	// The automatic pass must not rebuild a dissolved series.
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("series rebuilt=%+v", all)
	}
}

func TestDeleteWorkCleansSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Rep", 2019)
	b := mustWork(t, store, ctx, "Other", 2021)
	c := mustWork(t, store, ctx, "Third", 2023)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if series.RepresentativeWorkID != a.ID {
		t.Fatalf("representative=%d", series.RepresentativeWorkID)
	}
	// Deleting the representative recomputes it.
	if err := store.DeleteWork(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	fresh := onlySeries(t, store, ctx)
	if fresh.RepresentativeWorkID != b.ID || fresh.Title != "Other" {
		t.Fatalf("after delete=%+v", fresh)
	}
	// Deleting down to a single auto member dissolves the series.
	if err := store.DeleteWork(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWork(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("series left=%+v", all)
	}
}

func TestCleanupAutoWorksCleansSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	insertAuto := func(title string) int64 {
		t.Helper()
		result, err := store.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES(?,?,'anime','auto')`, title, metadata.Normalize(title))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	a, b := insertAuto("Auto One"), insertAuto("Auto Two")
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a, b}}); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 1 {
		t.Fatalf("series=%+v", all)
	}
	var stats RefreshStats
	if err := store.CleanupAutoWorks(ctx, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.WorksDeleted != 2 {
		t.Fatalf("deleted=%d", stats.WorksDeleted)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("series left=%+v", all)
	}
}

func TestListWorksFoldedPaginationAndCount(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Grouped One", 2019)
	b := mustWork(t, store, ctx, "Grouped Two", 2021)
	c := mustWork(t, store, ctx, "Grouped Three", 2023)
	solo1 := mustWork(t, store, ctx, "Solo One", 2020)
	solo2 := mustWork(t, store, ctx, "Solo Two", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}}); err != nil {
		t.Fatal(err)
	}
	total, err := store.CountWorksFolded(ctx, WorkFilters{})
	if err != nil || total != 3 {
		t.Fatalf("folded total=%d err=%v", total, err)
	}
	rows, err := store.ListWorksFolded(ctx, WorkFilters{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	var seriesRow *WorkListRow
	standalone := 0
	for i := range rows {
		if rows[i].Series != nil {
			seriesRow = &rows[i]
		} else {
			standalone++
		}
	}
	if seriesRow == nil || standalone != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	if seriesRow.Series.MemberCount != 3 || len(seriesRow.Members) != 3 {
		t.Fatalf("series row=%+v", seriesRow.Series)
	}
	if seriesRow.Representative.ID != a.ID {
		t.Fatalf("representative=%d", seriesRow.Representative.ID)
	}
	// Pagination counts folded rows.
	page1, err := store.ListWorksFolded(ctx, WorkFilters{Limit: 2})
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1=%d err=%v", len(page1), err)
	}
	page2, err := store.ListWorksFolded(ctx, WorkFilters{Limit: 2, Offset: 2})
	if err != nil || len(page2) != 1 {
		t.Fatalf("page2=%d err=%v", len(page2), err)
	}
	// A query matching one member folds the whole series into one row.
	filtered, err := store.CountWorksFolded(ctx, WorkFilters{Query: "Grouped"})
	if err != nil || filtered != 1 {
		t.Fatalf("filtered=%d err=%v", filtered, err)
	}
	_ = solo1
	_ = solo2
}

// P3: a work the user detached and then manually re-added keeps its manual
// membership; the automatic pass must not try to insert an automatic row for
// the same work (work_id is the primary key) and must not move it.
func TestApplyAutoSeriesManualReAddStaysManual(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Readd A", 2019)
	b := mustWork(t, store, ctx, "Readd B", 2021)
	c := mustWork(t, store, ctx, "Readd C", 2023)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.DetachWorkFromSeries(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkToSeries(ctx, b.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats != (SeriesApplyStats{}) {
		t.Fatalf("expected no-op run, stats=%+v", stats)
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 3 || members[a.ID] != "auto" || members[b.ID] != "manual" || members[c.ID] != "auto" {
		t.Fatalf("members=%v", members)
	}
}

// P3: a work manually added to another series is excluded from automatic
// candidates, so its Bangumi component cannot create a conflicting series.
func TestApplyAutoSeriesManualMemberElsewhereNotMoved(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Home A", 2019)
	b := mustWork(t, store, ctx, "Home B", 2021)
	c := mustWork(t, store, ctx, "Away C", 2020)
	d := mustWork(t, store, ctx, "Away D", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.AddWorkToSeries(ctx, c.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}, {c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSeries(ctx)
	if len(all) != 1 || all[0].ID != series.ID {
		t.Fatalf("series=%+v", all)
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 3 || members[c.ID] != "manual" {
		t.Fatalf("members=%v", members)
	}
	if _, _, err := store.SeriesForWork(ctx, d.ID); err != sql.ErrNoRows {
		t.Fatalf("d grouped: %v", err)
	}
}

// Bangumi added a relation that merges two existing series into one
// component: the auto members move between series inside one transaction
// without violating the work_id primary key, and the claimed series keeps
// its (manually renamed) title.
func TestApplyAutoSeriesMergesTwoExistingSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Merge A", 2019)
	b := mustWork(t, store, ctx, "Merge B", 2020)
	c := mustWork(t, store, ctx, "Merge C", 2021)
	d := mustWork(t, store, ctx, "Merge D", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}, {c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSeries(ctx)
	if len(all) != 2 {
		t.Fatalf("series=%+v", all)
	}
	first := all[0]
	if all[1].ID < first.ID {
		first = all[1]
	}
	if err := store.RenameWorkSeries(ctx, first.ID, "合并后的主系列"); err != nil {
		t.Fatal(err)
	}
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID, d.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SeriesDeleted != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	series := onlySeries(t, store, ctx)
	if series.ID != first.ID || series.Title != "合并后的主系列" {
		t.Fatalf("series=%+v", series)
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 4 {
		t.Fatalf("members=%v", members)
	}
	for _, id := range []int64{a.ID, b.ID, c.ID, d.ID} {
		if members[id] != "auto" {
			t.Fatalf("work %d source=%s", id, members[id])
		}
	}
}

// Folded rows filter by the displayed series title, so the leading-letter
// index matches what the user sees.
func TestListWorksFoldedIndexUsesSeriesTitle(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Grouped One", 2019)
	b := mustWork(t, store, ctx, "Grouped Two", 2021)
	mustWork(t, store, ctx, "Solo", 2020)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.RenameWorkSeries(ctx, series.ID, "Zebra"); err != nil {
		t.Fatal(err)
	}
	total, err := store.CountWorksFolded(ctx, WorkFilters{Index: "Z"})
	if err != nil || total != 1 {
		t.Fatalf("index Z total=%d err=%v", total, err)
	}
	rows, err := store.ListWorksFolded(ctx, WorkFilters{Index: "Z"})
	if err != nil || len(rows) != 1 || rows[0].Series == nil || rows[0].Series.Title != "Zebra" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	// The member titles no longer drive the folded row's index letter.
	if total, err = store.CountWorksFolded(ctx, WorkFilters{Index: "G"}); err != nil || total != 0 {
		t.Fatalf("index G total=%d err=%v", total, err)
	}
}

// The folded list's keyword search also matches the series title, and the
// folded count agrees with the list.
func TestListWorksFoldedQueryMatchesSeriesTitle(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Grouped One", 2019)
	b := mustWork(t, store, ctx, "Grouped Two", 2021)
	mustWork(t, store, ctx, "Solo", 2020)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.RenameWorkSeries(ctx, series.ID, "Zebra"); err != nil {
		t.Fatal(err)
	}
	// Neither member title contains the query, so only the series title can
	// match.
	rows, err := store.ListWorksFolded(ctx, WorkFilters{Query: "Zebra"})
	if err != nil || len(rows) != 1 || rows[0].Series == nil || rows[0].Series.Title != "Zebra" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	total, err := store.CountWorksFolded(ctx, WorkFilters{Query: "Zebra"})
	if err != nil || total != 1 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	// A query matching a member still folds the series into one row.
	if total, err = store.CountWorksFolded(ctx, WorkFilters{Query: "Grouped"}); err != nil || total != 1 {
		t.Fatalf("member query total=%d err=%v", total, err)
	}
}

// D41: when Bangumi merges two series and the renamed one has the larger id,
// the renamed series still wins the claim and keeps its title.
func TestApplyAutoSeriesMergeKeepsRenamedSeriesWithLargerID(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Keep A", 2019)
	b := mustWork(t, store, ctx, "Keep B", 2020)
	c := mustWork(t, store, ctx, "Keep C", 2021)
	d := mustWork(t, store, ctx, "Keep D", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}, {c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSeries(ctx)
	if len(all) != 2 {
		t.Fatalf("series=%+v", all)
	}
	// The renamed series is the one with the larger id.
	renamed := all[0]
	if all[1].ID > renamed.ID {
		renamed = all[1]
	}
	if err := store.RenameWorkSeries(ctx, renamed.ID, "后来改名的系列"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if series.ID != renamed.ID || series.Title != "后来改名的系列" || series.TitleSource != "manual" {
		t.Fatalf("series=%+v", series)
	}
	if members := seriesIDs(t, store, ctx, series.ID); len(members) != 4 {
		t.Fatalf("members=%v", members)
	}
}

// D41-A1: a component overlapping two renamed series is not merged at all —
// both series keep their members and titles, and the conflict is recorded.
func TestApplyAutoSeriesMergeConflictOfTwoRenamedSeriesSkipped(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Conflict A", 2019)
	b := mustWork(t, store, ctx, "Conflict B", 2020)
	c := mustWork(t, store, ctx, "Conflict C", 2021)
	d := mustWork(t, store, ctx, "Conflict D", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}, {c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSeries(ctx)
	if len(all) != 2 {
		t.Fatalf("series=%+v", all)
	}
	if err := store.RenameWorkSeries(ctx, all[0].ID, "改名系列一"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameWorkSeries(ctx, all[1].ID, "改名系列二"); err != nil {
		t.Fatal(err)
	}
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, c.ID, d.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MembersAdded != 0 || stats.MembersRemoved != 0 || stats.SeriesCreated != 0 || stats.SeriesDeleted != 0 {
		t.Fatalf("conflicted component must be a no-op: stats=%+v", stats)
	}
	all, _ = store.ListSeries(ctx)
	if len(all) != 2 || all[0].Title != "改名系列一" && all[1].Title != "改名系列一" || all[0].Title != "改名系列二" && all[1].Title != "改名系列二" {
		t.Fatalf("series=%+v", all)
	}
	for _, series := range all {
		if members := seriesIDs(t, store, ctx, series.ID); len(members) != 2 {
			t.Fatalf("series %d members=%v", series.ID, members)
		}
	}
	// The skip is recorded for later inspection.
	var evidence int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrichment_provenance WHERE field_name='series_merge_skipped'`).Scan(&evidence); err != nil || evidence == 0 {
		t.Fatalf("evidence=%d err=%v", evidence, err)
	}
}

// D41-A1 boundary: a singleton component overlapping a frozen series must
// not shrink it either. S1={a,z} and S2={c,d} are both renamed; the skipped
// component {a,c,d} freezes both series, so {z} cannot claim S1, z stays,
// and S1 is not degenerate-deleted — members and titles are all unchanged.
func TestApplyAutoSeriesMergeConflictFreezesInvolvedSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Frozen A", 2019)
	z := mustWork(t, store, ctx, "Frozen Z", 2020)
	c := mustWork(t, store, ctx, "Frozen C", 2021)
	d := mustWork(t, store, ctx, "Frozen D", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, z.ID}, {c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSeries(ctx)
	if len(all) != 2 {
		t.Fatalf("series=%+v", all)
	}
	s1, s2 := all[0], all[1]
	if s2.ID < s1.ID {
		s1, s2 = s2, s1
	}
	if err := store.RenameWorkSeries(ctx, s1.ID, "冻结系列一"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameWorkSeries(ctx, s2.ID, "冻结系列二"); err != nil {
		t.Fatal(err)
	}
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, c.ID, d.ID}, {z.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats != (SeriesApplyStats{}) {
		t.Fatalf("frozen run must be a no-op: stats=%+v", stats)
	}
	all, _ = store.ListSeries(ctx)
	if len(all) != 2 {
		t.Fatalf("frozen series deleted: %+v", all)
	}
	titles := map[int64]string{all[0].ID: all[0].Title, all[1].ID: all[1].Title}
	if titles[s1.ID] != "冻结系列一" || titles[s2.ID] != "冻结系列二" {
		t.Fatalf("titles=%v", titles)
	}
	if members := seriesIDs(t, store, ctx, s1.ID); len(members) != 2 || members[a.ID] != "auto" || members[z.ID] != "auto" {
		t.Fatalf("s1 members=%v", members)
	}
	if members := seriesIDs(t, store, ctx, s2.ID); len(members) != 2 || members[c.ID] != "auto" || members[d.ID] != "auto" {
		t.Fatalf("s2 members=%v", members)
	}
}

// D41-A1 provenance lifecycle: a persisting conflict does not refresh the
// record, and a resolved conflict deletes it.
func TestApplyAutoSeriesMergeConflictProvenanceLifecycle(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Prov A", 2019)
	b := mustWork(t, store, ctx, "Prov B", 2020)
	c := mustWork(t, store, ctx, "Prov C", 2021)
	d := mustWork(t, store, ctx, "Prov D", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}, {c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSeries(ctx)
	if len(all) != 2 {
		t.Fatalf("series=%+v", all)
	}
	for _, series := range all {
		if err := store.RenameWorkSeries(ctx, series.ID, fmt.Sprintf("改名 %d", series.ID)); err != nil {
			t.Fatal(err)
		}
	}
	run, err := store.CreateEnrichmentRun(ctx, "works", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyAutoSeries(ctx, run.ID, [][]int64{{a.ID, b.ID, c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	type provRow struct {
		runID     int64
		updatedAt string
	}
	readRows := func() map[int64]provRow {
		t.Helper()
		rows, err := store.db.QueryContext(ctx, `SELECT entity_id,COALESCE(run_id,0),updated_at FROM enrichment_provenance WHERE entity_type='work' AND field_name='series_merge_skipped' ORDER BY entity_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int64]provRow{}
		for rows.Next() {
			var id, runID int64
			var updated string
			if err := rows.Scan(&id, &runID, &updated); err != nil {
				t.Fatal(err)
			}
			out[id] = provRow{runID: runID, updatedAt: updated}
		}
		return out
	}
	before := readRows()
	if len(before) != 4 {
		t.Fatalf("provenance rows=%d", len(before))
	}
	// A second conflicting run must not refresh run_id or updated_at.
	time.Sleep(20 * time.Millisecond)
	run2, err := store.CreateEnrichmentRun(ctx, "works", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyAutoSeries(ctx, run2.ID, [][]int64{{a.ID, b.ID, c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	after := readRows()
	if len(after) != len(before) {
		t.Fatalf("rows after=%d", len(after))
	}
	for id, row := range before {
		if after[id] != row {
			t.Fatalf("work %d provenance refreshed: %+v -> %+v", id, row, after[id])
		}
	}
	// Once the components no longer overlap both renamed series, the conflict
	// is resolved and the records are deleted.
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}, {c.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	if remaining := readRows(); len(remaining) != 0 {
		t.Fatalf("provenance left=%v", remaining)
	}
}

// D38: a manually created work with an explicit type is locked against
// automatic Bangumi type correction; automatic flows keep type_locked=0.
func TestCreateWorkExplicitTypeLocks(t *testing.T) {
	store, ctx := newSeriesStore(t)
	locked, err := store.CreateWork(ctx, WorkInput{Title: "Manual Typed", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	var typeLocked bool
	if err := store.db.QueryRowContext(ctx, `SELECT type_locked=1 FROM works WHERE id=?`, locked.ID).Scan(&typeLocked); err != nil || !typeLocked {
		t.Fatalf("type_locked=%v err=%v", typeLocked, err)
	}
	changed, err := store.CorrectBangumiWorkType(ctx, locked.ID, "anime", "movie", "5", 0)
	if err != nil || changed {
		t.Fatalf("correction changed=%v err=%v", changed, err)
	}
	unlocked, err := store.CreateWork(ctx, WorkInput{Title: "Manual Untyped"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT type_locked=0 AND type='other' FROM works WHERE id=?`, unlocked.ID).Scan(&typeLocked); err != nil || !typeLocked {
		t.Fatalf("untyped work locked err=%v", err)
	}
	changed, err = store.CorrectBangumiWorkType(ctx, unlocked.ID, "other", "anime", "7", 0)
	if err != nil || !changed {
		t.Fatalf("untyped correction changed=%v err=%v", changed, err)
	}
	// Automatic resolution keeps the type unlocked.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	autoID, created, err := resolveAutoWork(ctx, tx, metadata.WorkAssociation{Title: "Auto Created", Type: "anime"}, 0)
	if err != nil || !created {
		tx.Rollback()
		t.Fatalf("resolveAutoWork created=%v err=%v", created, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT type_locked=0 FROM works WHERE id=?`, autoID).Scan(&typeLocked); err != nil || !typeLocked {
		t.Fatalf("auto work locked err=%v", err)
	}
	changed, err = store.CorrectBangumiWorkType(ctx, autoID, "anime", "movie", "6", 0)
	if err != nil || !changed {
		t.Fatalf("auto correction changed=%v err=%v", changed, err)
	}
}

// B1/D59: overlap matching counts only unlocked automatic members. A
// component made purely of works the user added by hand must not steal the
// series away from the component holding its automatic members.
func TestApplyAutoSeriesManualAdditionsDoNotStealSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a1 := mustWork(t, store, ctx, "Auto One", 2019)
	a2 := mustWork(t, store, ctx, "Auto Two", 2021)
	b1 := mustWork(t, store, ctx, "Manual One", 2018)
	b2 := mustWork(t, store, ctx, "Manual Two", 2020)
	b3 := mustWork(t, store, ctx, "Manual Three", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a1.ID, a2.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	for _, work := range []Work{b1, b2, b3} {
		if err := store.AddWorkToSeries(ctx, work.ID, series.ID); err != nil {
			t.Fatal(err)
		}
	}
	// The manual additions already refreshed the representative (earliest
	// member) and the automatic title follows it (D25); capture that state.
	series = onlySeries(t, store, ctx)
	// The next run reports two components: the automatic pair and the three
	// works the user added by hand (e.g. they are sequel-linked to each
	// other). The series must stay exactly as the user arranged it.
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a1.ID, a2.ID}, {b1.ID, b2.ID, b3.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SeriesCreated != 0 || stats.SeriesDeleted != 0 || stats.MembersAdded != 0 || stats.MembersRemoved != 0 {
		t.Fatalf("stats=%+v, want a no-op", stats)
	}
	fresh := onlySeries(t, store, ctx)
	if fresh.ID != series.ID || fresh.Title != series.Title {
		t.Fatalf("series changed: before=%+v after=%+v", series, fresh)
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 5 {
		t.Fatalf("members=%v, want all 5 works", members)
	}
	for _, id := range []int64{a1.ID, a2.ID} {
		if members[id] != "auto" {
			t.Fatalf("work %d source=%q, want auto", id, members[id])
		}
	}
	for _, id := range []int64{b1.ID, b2.ID, b3.ID} {
		if members[id] != "manual" {
			t.Fatalf("work %d source=%q, want manual", id, members[id])
		}
	}
}

// D51：类型是筛选而不是层级。按类型筛选时，系列行只展开符合类型的成员、
// 计数为“其中 K 部”、代表作（海报）取第一部符合类型的成员；未筛选时不变。
func TestListWorksFoldedTypeFilterNarrowsSeriesRows(t *testing.T) {
	store, ctx := newSeriesStore(t)
	mk := func(title, typ string, year int) Work {
		work, err := store.CreateWork(ctx, WorkInput{Title: title, Type: typ, Year: year})
		if err != nil {
			t.Fatal(err)
		}
		return work
	}
	anime1 := mk("Fold Anime One", "anime", 2019)
	movie := mk("Fold Movie", "movie", 2021)
	anime2 := mk("Fold Anime Two", "anime", 2023)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{anime1.ID, movie.ID, anime2.ID}}); err != nil {
		t.Fatal(err)
	}
	// 无筛选：全部成员、原代表作、无匹配计数。
	rows, err := store.ListWorksFolded(ctx, WorkFilters{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if got := len(rows[0].Members); got != 3 || rows[0].MatchedCount != 0 || rows[0].Representative.ID != anime1.ID {
		t.Fatalf("unfiltered row members=%d matched=%d rep=%d", got, rows[0].MatchedCount, rows[0].Representative.ID)
	}
	// 按 movie 筛选：折叠行仍是一个；成员只剩 movie；计数与分页口径一致。
	rows, err = store.ListWorksFolded(ctx, WorkFilters{Type: "movie"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("filtered rows=%d err=%v", len(rows), err)
	}
	row := rows[0]
	if row.Series == nil || row.Series.MemberCount != 3 {
		t.Fatalf("filtered series=%+v", row.Series)
	}
	if row.MatchedCount != 1 || len(row.Members) != 1 || row.Members[0].Work.ID != movie.ID {
		t.Fatalf("matched=%d members=%+v", row.MatchedCount, row.Members)
	}
	if row.Representative.ID != movie.ID {
		t.Fatalf("representative=%d, want first matching member %d", row.Representative.ID, movie.ID)
	}
	total, err := store.CountWorksFolded(ctx, WorkFilters{Type: "movie"})
	if err != nil || total != 1 {
		t.Fatalf("folded filtered total=%d err=%v", total, err)
	}
	// 按 anime 筛选：两名成员符合，代表作仍是按规则排最前的动画成员。
	rows, err = store.ListWorksFolded(ctx, WorkFilters{Type: "anime"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("anime rows=%d err=%v", len(rows), err)
	}
	if rows[0].MatchedCount != 2 || len(rows[0].Members) != 2 || rows[0].Representative.ID != anime1.ID {
		t.Fatalf("anime matched=%d members=%d rep=%d", rows[0].MatchedCount, len(rows[0].Members), rows[0].Representative.ID)
	}
	// 系列没有任何 drama 成员：整行消失（WHERE 已下推，不留空系列行）。
	rows, err = store.ListWorksFolded(ctx, WorkFilters{Type: "drama"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("drama rows=%d err=%v", len(rows), err)
	}
}

// 系列搜索选项：按标题过滤、按标题排序、尊重上限（系列管理页的合并目标选择器）。
func TestListSeriesOptions(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Opt Alpha", 2019)
	b := mustWork(t, store, ctx, "Opt Beta", 2020)
	c := mustWork(t, store, ctx, "Opt Gamma", 2021)
	if _, err := store.CreateWorkSeries(ctx, "命运之夜", []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkSeries(ctx, "鬼灭之刃", []int64{b.ID, c.ID}); err != nil {
		t.Fatal(err)
	}
	all, err := store.ListSeriesOptions(ctx, "", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%d err=%v", len(all), err)
	}
	// 中文按标题 COLLATE NOCASE 排序：命运(U+547D) < 鬼(U+9B3C)。
	if all[0].Title != "命运之夜" || all[1].Title != "鬼灭之刃" || all[1].MemberCount != 2 {
		t.Fatalf("all=%+v", all)
	}
	filtered, err := store.ListSeriesOptions(ctx, "鬼灭", 10)
	if err != nil || len(filtered) != 1 || filtered[0].Title != "鬼灭之刃" {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
	// 参数化：引号注入不会命中任何行（与 ListWorkOptions 同一口径，
	// LIKE 通配符仍按 LIKE 语义工作）。
	none, err := store.ListSeriesOptions(ctx, "' OR 1=1--", 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("injection filtered=%+v err=%v", none, err)
	}
	limited, err := store.ListSeriesOptions(ctx, "", 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limited=%+v err=%v", limited, err)
	}
}

// SeriesByWork：只返回属于系列的作品的系列（“加入作品”移入提示）。
func TestSeriesByWork(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Titled A", 2019)
	b := mustWork(t, store, ctx, "Titled B", 2020)
	seriesID, err := store.CreateWorkSeries(ctx, "命运系列", []int64{a.ID})
	if err != nil {
		t.Fatal(err)
	}
	byWork, err := store.SeriesByWork(ctx, []int64{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byWork) != 1 || byWork[a.ID].Title != "命运系列" || byWork[a.ID].ID != seriesID {
		t.Fatalf("byWork=%v", byWork)
	}
	empty, err := store.SeriesByWork(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
}
