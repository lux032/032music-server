package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func mustSeriesSuggestion(t *testing.T, store *Store, ctx context.Context, workA, workB, subjectA, subjectB int64, kind string) SeriesSuggestion {
	t.Helper()
	input := SeriesSuggestionInput{WorkA: workA, WorkB: workB, SubjectA: subjectA, SubjectB: subjectB, RelationAB: "游戏", RelationBA: "动画", Kind: kind}
	if err := store.ReplaceSeriesSuggestions(ctx, 9, []SeriesSuggestionInput{input}, []int64{workA, workB}); err != nil {
		t.Fatal(err)
	}
	suggestions, err := store.PendingSeriesSuggestions(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, suggestion := range suggestions {
		if suggestion.WorkA.ID == min(workA, workB) && suggestion.WorkB.ID == max(workA, workB) {
			return suggestion
		}
	}
	t.Fatalf("suggestion for works %d/%d not found", workA, workB)
	return SeriesSuggestion{}
}

func TestReplaceSeriesSuggestionsUpsertAndStaleDelete(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Sug A", 2019)
	b := mustWork(t, store, ctx, "Sug B", 2020)
	c := mustWork(t, store, ctx, "Sug C", 2021)
	input := SeriesSuggestionInput{WorkA: a.ID, WorkB: b.ID, SubjectA: 11, SubjectB: 22, RelationAB: "游戏", RelationBA: "动画", Kind: "cross"}
	if err := store.ReplaceSeriesSuggestions(ctx, 1, []SeriesSuggestionInput{input}, []int64{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	first := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	// Repeating the same run is a no-op: the row keeps its identity.
	if err := store.ReplaceSeriesSuggestions(ctx, 2, []SeriesSuggestionInput{input}, []int64{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	second := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	if first.ID != second.ID || first.CreatedAt != second.CreatedAt {
		t.Fatalf("upsert not idempotent: %+v vs %+v", first, second)
	}
	// A pair whose works were not processed this run survives even though it
	// was not regenerated.
	other := SeriesSuggestionInput{WorkA: a.ID, WorkB: c.ID, SubjectA: 11, SubjectB: 33, RelationAB: "衍生", RelationBA: "主线故事", Kind: "cross"}
	if err := store.ReplaceSeriesSuggestions(ctx, 3, []SeriesSuggestionInput{input, other}, []int64{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSeriesSuggestions(ctx, 4, []SeriesSuggestionInput{input}, []int64{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	// c was not processed: the (a,c) suggestion stays.
	mustSeriesSuggestion(t, store, ctx, a.ID, c.ID, 11, 33, "cross")
	// Now c is processed too and the (a,c) pair is gone from the run: the
	// stale row is deleted.
	if err := store.ReplaceSeriesSuggestions(ctx, 5, []SeriesSuggestionInput{input}, []int64{a.ID, b.ID, c.ID}); err != nil {
		t.Fatal(err)
	}
	suggestions, err := store.PendingSeriesSuggestions(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(suggestions) != 1 || suggestions[0].WorkB.ID != b.ID {
		t.Fatalf("suggestions=%+v, want only (a,b)", suggestions)
	}
}

func TestMergeWorkSeriesMovesMembersAsManualAndClearsLocks(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a1 := mustWork(t, store, ctx, "Keep One", 2019)
	a2 := mustWork(t, store, ctx, "Keep Two", 2021)
	b1 := mustWork(t, store, ctx, "Drop One", 2020)
	b2 := mustWork(t, store, ctx, "Drop Two", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a1.ID, a2.ID}, {b1.ID, b2.ID}}); err != nil {
		t.Fatal(err)
	}
	keep, _, err := store.SeriesForWork(ctx, a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	drop, _, err := store.SeriesForWork(ctx, b1.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A lock on an absorbed member is cleared by the merge (D57). (A lock
	// normally implies detachment; write it directly to prove the cleanup.)
	if _, err = store.db.ExecContext(ctx, `INSERT INTO work_series_locks(work_id) VALUES(?)`, b2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.MergeWorkSeries(ctx, keep.ID, drop.ID, "合并后的系列"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.WorkSeriesByID(ctx, drop.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("drop series still exists: %v", err)
	}
	members := seriesIDs(t, store, ctx, keep.ID)
	if len(members) != 4 || members[a1.ID] != "auto" || members[a2.ID] != "auto" || members[b1.ID] != "manual" || members[b2.ID] != "manual" {
		t.Fatalf("members=%v", members)
	}
	if locked, _ := store.WorkSeriesLocked(ctx, b2.ID); locked {
		t.Fatal("merged member kept its lock")
	}
	fresh, _ := store.WorkSeriesByID(ctx, keep.ID)
	if fresh.Title != "合并后的系列" || fresh.TitleSource != "manual" {
		t.Fatalf("series=%+v", fresh)
	}
	// Provenance records the merge for every moved member.
	var provenance int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrichment_provenance WHERE field_name='series' AND source='manual' AND json_extract(value_json,'$.mergedFrom')=?`, drop.ID).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if provenance != 2 {
		t.Fatalf("provenance rows=%d, want 2", provenance)
	}
}

func TestMergeWorkSeriesTitleRules(t *testing.T) {
	setup := func(t *testing.T, keepTitle, dropTitle string) (*Store, context.Context, int64, int64) {
		t.Helper()
		store, ctx := newSeriesStore(t)
		k1 := mustWork(t, store, ctx, "Keep A", 2019)
		k2 := mustWork(t, store, ctx, "Keep B", 2020)
		d1 := mustWork(t, store, ctx, "Drop A", 2021)
		if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{k1.ID, k2.ID}, {d1.ID, mustWork(t, store, ctx, "Drop B", 2022).ID}}); err != nil {
			t.Fatal(err)
		}
		keep, _, _ := store.SeriesForWork(ctx, k1.ID)
		drop, _, _ := store.SeriesForWork(ctx, d1.ID)
		if keepTitle != "" {
			if err := store.RenameWorkSeries(ctx, keep.ID, keepTitle); err != nil {
				t.Fatal(err)
			}
		}
		if dropTitle != "" {
			if err := store.RenameWorkSeries(ctx, drop.ID, dropTitle); err != nil {
				t.Fatal(err)
			}
		}
		return store, ctx, keep.ID, drop.ID
	}
	t.Run("single manual title wins", func(t *testing.T) {
		store, ctx, keepID, dropID := setup(t, "", "Drop 的名字")
		if _, err := store.MergeWorkSeries(ctx, keepID, dropID, ""); err != nil {
			t.Fatal(err)
		}
		merged, _ := store.WorkSeriesByID(ctx, keepID)
		if merged.Title != "Drop 的名字" || merged.TitleSource != "manual" {
			t.Fatalf("series=%+v", merged)
		}
	})
	t.Run("two manual titles conflict", func(t *testing.T) {
		store, ctx, keepID, dropID := setup(t, "Keep 的名字", "Drop 的名字")
		_, err := store.MergeWorkSeries(ctx, keepID, dropID, "")
		if !errors.Is(err, ErrSeriesTitleConflict) {
			t.Fatalf("err=%v, want ErrSeriesTitleConflict", err)
		}
		// The conflict changed nothing.
		if got := seriesMemberCount(store, ctx, keepID); got != 2 {
			t.Fatalf("keep members=%d", got)
		}
		if _, err = store.WorkSeriesByID(ctx, dropID); err != nil {
			t.Fatalf("drop series gone after conflict: %v", err)
		}
		// An explicit title resolves the conflict.
		if _, err = store.MergeWorkSeries(ctx, keepID, dropID, "用户定的名字"); err != nil {
			t.Fatal(err)
		}
		merged, _ := store.WorkSeriesByID(ctx, keepID)
		if merged.Title != "用户定的名字" {
			t.Fatalf("series=%+v", merged)
		}
	})
	t.Run("two auto titles keep the larger series", func(t *testing.T) {
		store, ctx := newSeriesStore(t)
		big1 := mustWork(t, store, ctx, "Big 1", 2018)
		big2 := mustWork(t, store, ctx, "Big 2", 2019)
		big3 := mustWork(t, store, ctx, "Big 3", 2020)
		small1 := mustWork(t, store, ctx, "Small 1", 2021)
		small2 := mustWork(t, store, ctx, "Small 2", 2022)
		if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{big1.ID, big2.ID, big3.ID}, {small1.ID, small2.ID}}); err != nil {
			t.Fatal(err)
		}
		big, _, _ := store.SeriesForWork(ctx, big1.ID)
		small, _, _ := store.SeriesForWork(ctx, small1.ID)
		// Merging the larger into the smaller swaps the survivor.
		if _, err := store.MergeWorkSeries(ctx, small.ID, big.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := store.WorkSeriesByID(ctx, small.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("smaller series must be absorbed: %v", err)
		}
		if got := seriesMemberCount(store, ctx, big.ID); got != 5 {
			t.Fatalf("survivor members=%d, want 5", got)
		}
		merged, _ := store.WorkSeriesByID(ctx, big.ID)
		if merged.TitleSource != "auto" || merged.Title != "Big 1" {
			t.Fatalf("survivor title=%+v, want auto following representative", merged)
		}
	})
	t.Run("equal size keeps the smaller id", func(t *testing.T) {
		store, ctx, keepID, dropID := setup(t, "", "")
		// keepID < dropID and both have 2 members: keep survives.
		if _, err := store.MergeWorkSeries(ctx, dropID, keepID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := store.WorkSeriesByID(ctx, keepID); err != nil {
			t.Fatalf("smaller id series must survive: %v", err)
		}
	})
	t.Run("self merge and missing series are errors", func(t *testing.T) {
		store, ctx, keepID, _ := setup(t, "", "")
		if _, err := store.MergeWorkSeries(ctx, keepID, keepID, ""); !errors.Is(err, ErrInvalidWork) {
			t.Fatalf("self merge err=%v", err)
		}
		if _, err := store.MergeWorkSeries(ctx, keepID, 9999, ""); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing drop err=%v", err)
		}
	})
}

func seriesMemberCount(store *Store, ctx context.Context, seriesID int64) int {
	members, err := store.SeriesMembers(ctx, seriesID)
	if err != nil {
		return -1
	}
	return len(members)
}

func TestCreateWorkSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Create A", 2019)
	b := mustWork(t, store, ctx, "Create B", 2021)
	c := mustWork(t, store, ctx, "Create C", 2020)
	// b starts in an automatic series; creating a manual series with b moves
	// it out and cleans the degenerate old series.
	d := mustWork(t, store, ctx, "Create D", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{b.ID, d.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkSeries(ctx, "", nil); !errors.Is(err, ErrInvalidWork) {
		t.Fatal("empty member list must fail")
	}
	id, err := store.CreateWorkSeries(ctx, "", []int64{a.ID, b.ID, c.ID})
	if err != nil {
		t.Fatal(err)
	}
	series, err := store.WorkSeriesByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Empty title stays automatic and follows the representative (D61).
	if series.TitleSource != "auto" || series.Title != "Create A" || series.RepresentativeWorkID != a.ID {
		t.Fatalf("series=%+v", series)
	}
	members := seriesIDs(t, store, ctx, id)
	if len(members) != 3 || members[a.ID] != "manual" || members[b.ID] != "manual" || members[c.ID] != "manual" {
		t.Fatalf("members=%v", members)
	}
	// The old automatic series lost b and degenerated to a single member.
	if _, _, err = store.SeriesForWork(ctx, d.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old series must be cleaned: %v", err)
	}
	if _, err = store.CreateWorkSeries(ctx, "名字", []int64{9999}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing work err=%v", err)
	}
}

func TestAcceptSeriesSuggestionFlows(t *testing.T) {
	t.Run("neither in a series creates one", func(t *testing.T) {
		store, ctx := newSeriesStore(t)
		a := mustWork(t, store, ctx, "Acc A", 2019)
		b := mustWork(t, store, ctx, "Acc B", 2021)
		suggestion := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
		if err := store.AcceptSeriesSuggestion(ctx, suggestion.ID, ""); err != nil {
			t.Fatal(err)
		}
		series, members, err := store.SeriesForWork(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if series.TitleSource != "auto" || series.Title != "Acc A" {
			t.Fatalf("series=%+v", series)
		}
		if len(members) != 2 || seriesIDs(t, store, ctx, series.ID)[b.ID] != "manual" {
			t.Fatalf("members=%v", members)
		}
		if got := pendingCount(t, store, ctx); got != 0 {
			t.Fatalf("pending=%d", got)
		}
		assertDecision(t, store, ctx, 11, 22, "accepted")
	})
	t.Run("one in a series adds the other", func(t *testing.T) {
		store, ctx := newSeriesStore(t)
		a := mustWork(t, store, ctx, "Acc A", 2019)
		b := mustWork(t, store, ctx, "Acc B", 2021)
		seriesID, err := store.CreateWorkSeries(ctx, "已有系列", []int64{a.ID})
		if err != nil {
			t.Fatal(err)
		}
		suggestion := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
		if err := store.AcceptSeriesSuggestion(ctx, suggestion.ID, ""); err != nil {
			t.Fatal(err)
		}
		members := seriesIDs(t, store, ctx, seriesID)
		if len(members) != 2 || members[b.ID] != "manual" {
			t.Fatalf("members=%v", members)
		}
		series, _ := store.WorkSeriesByID(ctx, seriesID)
		if series.Title != "已有系列" {
			t.Fatalf("series=%+v", series)
		}
	})
	t.Run("two series merge with D58 title rules", func(t *testing.T) {
		store, ctx := newSeriesStore(t)
		a := mustWork(t, store, ctx, "Acc A", 2019)
		b := mustWork(t, store, ctx, "Acc B", 2021)
		seriesA, _ := store.CreateWorkSeries(ctx, "A 系列", []int64{a.ID})
		seriesB, _ := store.CreateWorkSeries(ctx, "B 系列", []int64{b.ID})
		suggestion := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
		// Both series have manual titles and no merge title was given.
		if err := store.AcceptSeriesSuggestion(ctx, suggestion.ID, ""); !errors.Is(err, ErrSeriesTitleConflict) {
			t.Fatalf("err=%v, want ErrSeriesTitleConflict", err)
		}
		// The conflict kept the suggestion pending and recorded no decision.
		if got := pendingCount(t, store, ctx); got != 1 {
			t.Fatalf("pending=%d, want 1", got)
		}
		if err := store.AcceptSeriesSuggestion(ctx, suggestion.ID, "合并系列"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.WorkSeriesByID(ctx, seriesB); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("merged series left: %v", err)
		}
		merged, _ := store.WorkSeriesByID(ctx, seriesA)
		if merged.Title != "合并系列" || merged.MemberCount != 2 {
			t.Fatalf("merged=%+v", merged)
		}
		assertDecision(t, store, ctx, 11, 22, "accepted")
	})
	t.Run("already in the same series just closes", func(t *testing.T) {
		store, ctx := newSeriesStore(t)
		a := mustWork(t, store, ctx, "Acc A", 2019)
		b := mustWork(t, store, ctx, "Acc B", 2021)
		seriesID, _ := store.CreateWorkSeries(ctx, "同一系列", []int64{a.ID, b.ID})
		// Same-series pairs can no longer be inserted through
		// ReplaceSeriesSuggestions (M2); write the row directly to simulate a
		// suggestion generated before the works joined the same series.
		if _, err := store.db.ExecContext(ctx, `INSERT INTO work_series_suggestions(work_a,work_b,subject_a,subject_b,relation_ab,relation_ba,kind) VALUES(?,?,11,22,'游戏','动画','cross')`, min(a.ID, b.ID), max(a.ID, b.ID)); err != nil {
			t.Fatal(err)
		}
		suggestions, err := store.PendingSeriesSuggestions(ctx, 10, 0)
		if err != nil || len(suggestions) != 1 {
			t.Fatalf("suggestions=%+v %v", suggestions, err)
		}
		if err := store.AcceptSeriesSuggestion(ctx, suggestions[0].ID, ""); err != nil {
			t.Fatal(err)
		}
		if got := seriesMemberCount(store, ctx, seriesID); got != 2 {
			t.Fatalf("members=%d", got)
		}
		if got := pendingCount(t, store, ctx); got != 0 {
			t.Fatalf("pending=%d", got)
		}
		assertDecision(t, store, ctx, 11, 22, "accepted")
	})
	t.Run("missing suggestion", func(t *testing.T) {
		store, ctx := newSeriesStore(t)
		if err := store.AcceptSeriesSuggestion(ctx, 9999, ""); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRejectSeriesSuggestion(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Rej A", 2019)
	b := mustWork(t, store, ctx, "Rej B", 2021)
	suggestion := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 31, 42, "cross")
	if err := store.RejectSeriesSuggestion(ctx, suggestion.ID); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, store, ctx); got != 0 {
		t.Fatalf("pending=%d", got)
	}
	assertDecision(t, store, ctx, 31, 42, "rejected")
	if err := store.RejectSeriesSuggestion(ctx, suggestion.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("double reject err=%v", err)
	}
}

func pendingCount(t *testing.T, store *Store, ctx context.Context) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_series_suggestions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertDecision(t *testing.T, store *Store, ctx context.Context, subjectA, subjectB int64, want string) {
	t.Helper()
	if subjectA > subjectB {
		subjectA, subjectB = subjectB, subjectA
	}
	var decision string
	if err := store.db.QueryRowContext(ctx, `SELECT decision FROM work_series_suggestion_decisions WHERE subject_a=? AND subject_b=?`, subjectA, subjectB).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if decision != want {
		t.Fatalf("decision=%q, want %q", decision, want)
	}
}

func TestPendingSeriesSuggestionsCarryWorksAndSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Pend A", 2019)
	b := mustWork(t, store, ctx, "Pend B", 2021)
	seriesID, err := store.CreateWorkSeries(ctx, "已有", []int64{a.ID})
	if err != nil {
		t.Fatal(err)
	}
	mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	suggestions, err := store.PendingSeriesSuggestions(ctx, 10, 0)
	if err != nil || len(suggestions) != 1 {
		t.Fatalf("suggestions=%+v %v", suggestions, err)
	}
	got := suggestions[0]
	if got.WorkA.Title != "Pend A" || got.WorkB.Title != "Pend B" || got.WorkA.Year != 2019 || got.WorkB.Type == "" {
		t.Fatalf("works not loaded: %+v", got)
	}
	if got.SeriesA == nil || got.SeriesA.ID != seriesID || got.SeriesA.TitleSource != "manual" {
		t.Fatalf("series A=%+v", got.SeriesA)
	}
	if got.SeriesB != nil {
		t.Fatalf("series B=%+v, want nil", got.SeriesB)
	}
	// The series suggestion count joins the work-review totals.
	_, _, _, seriesCount, err := store.PendingWorkReviewCounts(ctx)
	if err != nil || seriesCount != 1 {
		t.Fatalf("seriesCount=%d %v", seriesCount, err)
	}
	total, err := store.PendingWorkReviewTotal(ctx)
	if err != nil || total != 1 {
		t.Fatalf("total=%d %v", total, err)
	}
}

func TestListSeriesPage(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a1 := mustWork(t, store, ctx, "Alpha One", 2019)
	a2 := mustWork(t, store, ctx, "Alpha Two", 2021)
	game := mustWork(t, store, ctx, "Alpha Game", 2020)
	b1 := mustWork(t, store, ctx, "Beta One", 2018)
	b2 := mustWork(t, store, ctx, "Beta Two", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a1.ID, a2.ID}, {b1.ID, b2.ID}}); err != nil {
		t.Fatal(err)
	}
	alpha, _, _ := store.SeriesForWork(ctx, a1.ID)
	if err := store.AddWorkToSeries(ctx, game.ID, alpha.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE works SET type='game' WHERE id=?`, game.ID); err != nil {
		t.Fatal(err)
	}
	rows, total, err := store.ListSeriesPage(ctx, "", 10, 0)
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("rows=%+v total=%d err=%v", rows, total, err)
	}
	if rows[0].Series.ID != alpha.ID || rows[0].Series.MemberCount != 3 {
		t.Fatalf("first row=%+v", rows[0])
	}
	if rows[0].TypeCounts["game"] != 1 || rows[0].TypeCounts["other"] != 2 {
		t.Fatalf("type counts=%v", rows[0].TypeCounts)
	}
	// Search by member title finds the series; unknown text finds nothing.
	rows, total, err = store.ListSeriesPage(ctx, "Beta Two", 10, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Series.Title != "Beta One" {
		t.Fatalf("member search rows=%+v total=%d err=%v", rows, total, err)
	}
	if _, total, err = store.ListSeriesPage(ctx, "不存在", 10, 0); err != nil || total != 0 {
		t.Fatalf("miss total=%d err=%v", total, err)
	}
	// Pagination.
	rows, _, err = store.ListSeriesPage(ctx, "", 1, 1)
	if err != nil || len(rows) != 1 || rows[0].Series.Title != "Beta One" {
		t.Fatalf("page rows=%+v err=%v", rows, err)
	}
}

// M1: suggestions touching a locked work are deleted by the next replace and
// hidden from pending lists immediately.
func TestReplaceSeriesSuggestionsDropsLockedPairs(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Lock A", 2019)
	b := mustWork(t, store, ctx, "Lock B", 2021)
	suggestion := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	// The user detaches b from a series, locking it.
	seriesID, err := store.CreateWorkSeries(ctx, "临时", []int64{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Creating the series does not clear the pair yet (a is not in it).
	if got := pendingCount(t, store, ctx); got != 1 {
		t.Fatalf("pending=%d", got)
	}
	if err = store.DissolveWorkSeries(ctx, seriesID); err != nil {
		t.Fatal(err)
	}
	// Pending hides locked pairs even before the next generation run.
	suggestions, err := store.PendingSeriesSuggestions(ctx, 10, 0)
	if err != nil || len(suggestions) != 0 {
		t.Fatalf("pending suggestions=%+v %v, want none", suggestions, err)
	}
	// The next replace deletes the row outright, even when neither work was
	// processed this run.
	if err = store.ReplaceSeriesSuggestions(ctx, 10, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, store, ctx); got != 0 {
		t.Fatalf("pending after replace=%d", got)
	}
	_ = suggestion
}

// M1: accepting a suggestion whose work was locked since generation fails
// with ErrSeriesSuggestionStale, deletes the suggestion and keeps the lock.
func TestAcceptSeriesSuggestionLockedIsStale(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Stale A", 2019)
	b := mustWork(t, store, ctx, "Stale B", 2021)
	suggestion := mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	seriesID, err := store.CreateWorkSeries(ctx, "临时", []int64{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DissolveWorkSeries(ctx, seriesID); err != nil {
		t.Fatal(err)
	}
	err = store.AcceptSeriesSuggestion(ctx, suggestion.ID, "")
	if !errors.Is(err, ErrSeriesSuggestionStale) {
		t.Fatalf("err=%v, want ErrSeriesSuggestionStale", err)
	}
	if locked, _ := store.WorkSeriesLocked(ctx, b.ID); !locked {
		t.Fatal("acceptance must not clear the lock")
	}
	if got := pendingCount(t, store, ctx); got != 0 {
		t.Fatalf("stale suggestion not deleted, pending=%d", got)
	}
	// No decision was recorded: the pair was never accepted nor rejected.
	var decisions int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_series_suggestion_decisions`).Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("decisions=%d %v", decisions, err)
	}
}

// M2: a subject pair decided between generation and the replace transaction
// is not inserted back.
func TestReplaceSeriesSuggestionsSkipsDecidedAndSameSeries(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Race A", 2019)
	b := mustWork(t, store, ctx, "Race B", 2021)
	c := mustWork(t, store, ctx, "Race C", 2020)
	d := mustWork(t, store, ctx, "Race D", 2022)
	// The (a,b) pair was rejected while the generation run was in flight.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO work_series_suggestion_decisions(subject_a,subject_b,decision) VALUES(11,22,'rejected')`); err != nil {
		t.Fatal(err)
	}
	// c and d joined the same series in the meantime.
	if _, err := store.CreateWorkSeries(ctx, "同系列", []int64{c.ID, d.ID}); err != nil {
		t.Fatal(err)
	}
	inputs := []SeriesSuggestionInput{
		{WorkA: a.ID, WorkB: b.ID, SubjectA: 11, SubjectB: 22, RelationAB: "游戏", RelationBA: "动画", Kind: "cross"},
		{WorkA: c.ID, WorkB: d.ID, SubjectA: 33, SubjectB: 44, RelationAB: "衍生", RelationBA: "主线故事", Kind: "cross"},
	}
	if err := store.ReplaceSeriesSuggestions(ctx, 1, inputs, []int64{a.ID, b.ID, c.ID, d.ID}); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, store, ctx); got != 0 {
		t.Fatalf("decided/same-series pairs inserted, pending=%d", got)
	}
}

// L4: the processed set travels as a JSON array through json_each, so tens of
// thousands of ids do not hit SQLite's variable limit.
func TestReplaceSeriesSuggestionsLargeProcessedSet(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Big A", 2019)
	b := mustWork(t, store, ctx, "Big B", 2021)
	mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	processed := make([]int64, 0, 20002)
	processed = append(processed, a.ID, b.ID)
	for i := int64(0); i < 20000; i++ {
		processed = append(processed, 900000+i)
	}
	// The pair was not regenerated: with both ends processed it is deleted,
	// even with 20002 processed ids.
	if err := store.ReplaceSeriesSuggestions(ctx, 2, nil, processed); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, store, ctx); got != 0 {
		t.Fatalf("stale suggestion survived, pending=%d", got)
	}
}

// L7: a merge drops pending suggestions whose works now share the series.
func TestMergeWorkSeriesDropsSameSeriesSuggestions(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Merge A", 2019)
	b := mustWork(t, store, ctx, "Merge B", 2021)
	seriesA, _ := store.CreateWorkSeries(ctx, "A 系列", []int64{a.ID})
	seriesB, _ := store.CreateWorkSeries(ctx, "B 系列", []int64{b.ID})
	// A second pair inside the keep series plus the merge pair.
	c := mustWork(t, store, ctx, "Merge C", 2020)
	mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	mustSeriesSuggestion(t, store, ctx, a.ID, c.ID, 11, 33, "cross")
	if got := pendingCount(t, store, ctx); got != 2 {
		t.Fatalf("pending=%d", got)
	}
	// c joins keep: the (a,c) pair is now same-series and must go away.
	if err := store.AddWorkToSeries(ctx, c.ID, seriesA); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, store, ctx); got != 1 {
		t.Fatalf("pending after add=%d, want 1", got)
	}
	if _, err := store.MergeWorkSeries(ctx, seriesA, seriesB, "合并"); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, store, ctx); got != 0 {
		t.Fatalf("pending after merge=%d", got)
	}
}

// L-A：PendingWorkReviewCounts 的系列建议计数与 PendingSeriesSuggestions 同
// 口径——任一端被锁定的建议不计入角标（列表里也看不到它）。
func TestPendingWorkReviewCountsFiltersLockedSuggestions(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Count A", 2019)
	b := mustWork(t, store, ctx, "Count B", 2021)
	mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	_, _, _, seriesCount, err := store.PendingWorkReviewCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seriesCount != 1 {
		t.Fatalf("seriesCount=%d, want 1", seriesCount)
	}
	// 用户拆出 b：锁定写入后角标立即归零（不能等到下一轮生成）。
	seriesID, err := store.CreateWorkSeries(ctx, "临时", []int64{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DissolveWorkSeries(ctx, seriesID); err != nil {
		t.Fatal(err)
	}
	_, _, _, seriesCount, err = store.PendingWorkReviewCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seriesCount != 0 {
		t.Fatalf("seriesCount after lock=%d, want 0", seriesCount)
	}
	// 原始行还在（等下一轮生成删除），证明计数靠的是过滤而不是行已消失。
	var raw int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_series_suggestions`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 1 {
		t.Fatalf("raw suggestion rows=%d, want 1", raw)
	}
}

// L-B：一轮生成重复给出了已在库里的建议，但这对作品在生成后、事务前被决定
// 或并入同一系列时，INSERT…SELECT 的复核条件会拦下写入；已存在的旧行也必须
// 当场删除，不能留到下一轮。
func TestReplaceSeriesSuggestionsDeletesBlockedExistingRows(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Blocked A", 2019)
	b := mustWork(t, store, ctx, "Blocked B", 2021)
	c := mustWork(t, store, ctx, "Blocked C", 2020)
	d := mustWork(t, store, ctx, "Blocked D", 2022)
	// 两条建议都已在库中。
	mustSeriesSuggestion(t, store, ctx, a.ID, b.ID, 11, 22, "cross")
	mustSeriesSuggestion(t, store, ctx, c.ID, d.ID, 33, 44, "cross")
	// 竞态一：(a,b) 的 subject 对刚被拒绝。
	if _, err := store.db.ExecContext(ctx, `INSERT INTO work_series_suggestion_decisions(subject_a,subject_b,decision) VALUES(11,22,'rejected')`); err != nil {
		t.Fatal(err)
	}
	// 竞态二：(c,d) 刚并入同一系列（直接写成员，模拟与本事务并发的合并）。
	if _, err := store.db.ExecContext(ctx, `INSERT INTO work_series(title,title_source) VALUES('抢占','auto')`); err != nil {
		t.Fatal(err)
	}
	var grabbed int64
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM work_series WHERE title='抢占'`).Scan(&grabbed); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{c.ID, d.ID} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO work_series_members(work_id,series_id,source) VALUES(?,?,'auto')`, id, grabbed); err != nil {
			t.Fatal(err)
		}
	}
	// 本轮生成“再次”给出这两对（生成期读到的还是旧状态），两端都已处理。
	inputs := []SeriesSuggestionInput{
		{WorkA: a.ID, WorkB: b.ID, SubjectA: 11, SubjectB: 22, RelationAB: "游戏", RelationBA: "动画", Kind: "cross"},
		{WorkA: c.ID, WorkB: d.ID, SubjectA: 33, SubjectB: 44, RelationAB: "衍生", RelationBA: "主线故事", Kind: "cross"},
	}
	if err := store.ReplaceSeriesSuggestions(ctx, 2, inputs, []int64{a.ID, b.ID, c.ID, d.ID}); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, store, ctx); got != 0 {
		t.Fatalf("blocked suggestions survived, pending=%d", got)
	}
}
