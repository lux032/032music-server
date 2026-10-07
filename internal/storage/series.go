package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// WorkSeries is a single-layer group of works (D24). The title is automatic
// (representative work's title) until the user renames it (D25).
type WorkSeries struct {
	ID                   int64  `json:"id"`
	Title                string `json:"title"`
	TitleSource          string `json:"titleSource"`
	RepresentativeWorkID int64  `json:"representativeWorkId"`
	MemberCount          int    `json:"memberCount"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

// WorkSeriesMember is one work's membership in a series.
type WorkSeriesMember struct {
	SeriesID int64  `json:"seriesId"`
	Source   string `json:"source"`
	Work     Work   `json:"work"`
}

// SeriesApplyStats summarizes one ApplyAutoSeries run.
type SeriesApplyStats struct {
	SeriesCreated  int
	SeriesDeleted  int
	MembersAdded   int
	MembersRemoved int
}

type seriesRow struct {
	id          int64
	title       string
	titleSource string
	repWorkID   int64
}

type seriesMemberRow struct {
	workID int64
	source string
}

// ApplyAutoSeries reconciles automatic series membership with the connected
// components computed from Bangumi sequel/prequel relations. components must
// cover every successfully processed work (singletons included): a processed
// work absent from its previous series' component leaves that series, while
// unprocessed works (failed fetches) keep their current membership untouched
// so a later successful run can heal. Manual members and locked works are
// never modified (R3). The whole pass is a single transaction and idempotent:
// a repeated run with the same components writes nothing.
func (s *Store) ApplyAutoSeries(ctx context.Context, runID int64, components [][]int64) (SeriesApplyStats, error) {
	var stats SeriesApplyStats
	// Normalize: drop duplicates and empty components, sort for determinism.
	seen := map[int64]bool{}
	var comps [][]int64
	for _, comp := range components {
		var cleaned []int64
		for _, id := range comp {
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			cleaned = append(cleaned, id)
		}
		if len(cleaned) > 0 {
			sort.Slice(cleaned, func(i, j int) bool { return cleaned[i] < cleaned[j] })
			comps = append(comps, cleaned)
		}
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i][0] < comps[j][0] })
	processed := map[int64]int{}
	for i, comp := range comps {
		for _, id := range comp {
			processed[id] = i
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	// The first statement is a write so the transaction takes the SQLite
	// writer lock immediately instead of upgrading from a stale read snapshot
	// (BeginTx itself is busy-retried by retryDB). It doubles as the safety
	// net that a locked work never holds automatic membership.
	removedLocked, err := tx.ExecContext(ctx, `DELETE FROM work_series_members WHERE source='auto' AND work_id IN (SELECT work_id FROM work_series_locks)`)
	if err != nil {
		return stats, err
	}
	if n, _ := removedLocked.RowsAffected(); n > 0 {
		stats.MembersRemoved += int(n)
	}
	locks := map[int64]bool{}
	lockRows, err := tx.QueryContext(ctx, `SELECT work_id FROM work_series_locks`)
	if err != nil {
		return stats, err
	}
	for lockRows.Next() {
		var id int64
		if err = lockRows.Scan(&id); err != nil {
			lockRows.Close()
			return stats, err
		}
		locks[id] = true
	}
	if err = lockRows.Err(); err != nil {
		lockRows.Close()
		return stats, err
	}
	lockRows.Close()
	var series []seriesRow
	seriesRows, err := tx.QueryContext(ctx, `SELECT id,title,title_source,COALESCE(representative_work_id,0) FROM work_series ORDER BY id`)
	if err != nil {
		return stats, err
	}
	for seriesRows.Next() {
		var row seriesRow
		if err = seriesRows.Scan(&row.id, &row.title, &row.titleSource, &row.repWorkID); err != nil {
			seriesRows.Close()
			return stats, err
		}
		series = append(series, row)
	}
	if err = seriesRows.Err(); err != nil {
		seriesRows.Close()
		return stats, err
	}
	seriesRows.Close()
	members := map[int64][]seriesMemberRow{}
	memberRows, err := tx.QueryContext(ctx, `SELECT series_id,work_id,source FROM work_series_members ORDER BY series_id,work_id`)
	if err != nil {
		return stats, err
	}
	for memberRows.Next() {
		var seriesID int64
		var member seriesMemberRow
		if err = memberRows.Scan(&seriesID, &member.workID, &member.source); err != nil {
			memberRows.Close()
			return stats, err
		}
		members[seriesID] = append(members[seriesID], member)
	}
	if err = memberRows.Err(); err != nil {
		memberRows.Close()
		return stats, err
	}
	memberRows.Close()
	// Match components to existing series by member overlap. All candidate
	// pairs are ordered by overlap (then larger component, then manually
	// renamed series, then smallest series id and component head) and claimed
	// greedily, so a renamed title stays attached to the most-overlapping
	// component and each series is claimed at most once (D41).
	type seriesMatch struct {
		compIndex int
		seriesID  int64
		overlap   int
	}
	manualTitle := map[int64]bool{}
	for _, row := range series {
		if row.titleSource == "manual" {
			manualTitle[row.id] = true
		}
	}
	var matches []seriesMatch
	for i, comp := range comps {
		compSet := map[int64]bool{}
		for _, id := range comp {
			compSet[id] = true
		}
		for _, row := range series {
			// B1/D59: overlap counts only unlocked automatic members. Manual
			// members are user intent pinned to this series (P3); counting them
			// would let a component made purely of manual additions claim the
			// series away from the component holding its automatic members.
			// The D41 conflict detection below shares this same counting.
			overlap := 0
			for _, member := range members[row.id] {
				if member.source != "auto" || locks[member.workID] {
					continue
				}
				if compSet[member.workID] {
					overlap++
				}
			}
			if overlap > 0 {
				matches = append(matches, seriesMatch{compIndex: i, seriesID: row.id, overlap: overlap})
			}
		}
	}
	// D41-A1: a component overlapping two or more manually renamed series is
	// not merged automatically. The component is skipped like a failed fetch:
	// its works count as unprocessed, so every involved series keeps its
	// members and its name for the user to resolve by hand.
	skippedComp := map[int]bool{}
	frozenSeries := map[int64]bool{}
	{
		compManualSeries := map[int]map[int64]bool{}
		for _, match := range matches {
			if !manualTitle[match.seriesID] {
				continue
			}
			if compManualSeries[match.compIndex] == nil {
				compManualSeries[match.compIndex] = map[int64]bool{}
			}
			compManualSeries[match.compIndex][match.seriesID] = true
		}
		for compIndex, seriesSet := range compManualSeries {
			if len(seriesSet) < 2 {
				continue
			}
			skippedComp[compIndex] = true
			seriesIDs := make([]int64, 0, len(seriesSet))
			for id := range seriesSet {
				seriesIDs = append(seriesIDs, id)
			}
			sort.Slice(seriesIDs, func(i, j int) bool { return seriesIDs[i] < seriesIDs[j] })
			for _, workID := range comps[compIndex] {
				delete(processed, workID)
				if err := putSeriesMergeSkippedProvenance(ctx, tx, workID, runID, seriesIDs); err != nil {
					return stats, err
				}
			}
		}
		// Every series overlapping a skipped component is frozen this run: it
		// is not claimable, its membership is not touched, and it is exempt
		// from degenerate deletion (a renamed title must survive, D41-A1).
		for _, match := range matches {
			if skippedComp[match.compIndex] {
				frozenSeries[match.seriesID] = true
			}
		}
		// Resolved conflicts: works in components that were not skipped this
		// run no longer carry a merge-conflict record.
		for i, comp := range comps {
			if skippedComp[i] {
				continue
			}
			for _, workID := range comp {
				if _, err := tx.ExecContext(ctx, `DELETE FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='series_merge_skipped' AND source='bangumi'`, workID); err != nil {
					return stats, err
				}
			}
		}
	}
	var filteredMatches []seriesMatch
	for _, match := range matches {
		if !skippedComp[match.compIndex] && !frozenSeries[match.seriesID] {
			filteredMatches = append(filteredMatches, match)
		}
	}
	// D41: overlap desc, then larger component, then manually renamed series
	// first, then smallest series id, then component head.
	sort.Slice(filteredMatches, func(i, j int) bool {
		left, right := filteredMatches[i], filteredMatches[j]
		if left.overlap != right.overlap {
			return left.overlap > right.overlap
		}
		if len(comps[left.compIndex]) != len(comps[right.compIndex]) {
			return len(comps[left.compIndex]) > len(comps[right.compIndex])
		}
		if manualTitle[left.seriesID] != manualTitle[right.seriesID] {
			return manualTitle[left.seriesID]
		}
		if left.seriesID != right.seriesID {
			return left.seriesID < right.seriesID
		}
		return comps[left.compIndex][0] < comps[right.compIndex][0]
	})
	claimedSeries := map[int64]bool{}
	claimedComp := map[int]bool{}
	compSeries := map[int]int64{}
	for _, match := range filteredMatches {
		if claimedSeries[match.seriesID] || claimedComp[match.compIndex] {
			continue
		}
		claimedSeries[match.seriesID] = true
		claimedComp[match.compIndex] = true
		compSeries[match.compIndex] = match.seriesID
	}
	recordProvenance := func(workID, seriesID int64) error {
		return putProvenance(ctx, tx, "work", workID, "series", "bangumi", "", runID, map[string]int64{"series": seriesID})
	}
	// P3: a manual member is like a locked work for the automatic pass — it
	// never counts as an automatic candidate and is never moved or removed.
	manualMember := map[int64]bool{}
	for _, memberList := range members {
		for _, member := range memberList {
			if member.source == "manual" {
				manualMember[member.workID] = true
			}
		}
	}
	// Members of a frozen series are not automatic candidates anywhere this
	// run: they stay exactly where the user can see them (D41-A1).
	frozenMember := map[int64]bool{}
	for seriesID := range frozenSeries {
		for _, member := range members[seriesID] {
			frozenMember[member.workID] = true
		}
	}
	candidatesOf := func(comp []int64) []int64 {
		var candidates []int64
		for _, id := range comp {
			if !locks[id] && !manualMember[id] && !frozenMember[id] {
				candidates = append(candidates, id)
			}
		}
		return candidates
	}
	// Compute the desired automatic membership of every existing series and
	// the creations first, then apply deletes and inserts in two phases: a
	// work moving between series is deleted everywhere before it is inserted
	// anywhere, which keeps the work_id primary key intact.
	targets := map[int64]map[int64]bool{}
	for _, row := range series {
		target := map[int64]bool{}
		if frozenSeries[row.id] {
			// A frozen series is completely untouched this run (D41-A1).
			for _, member := range members[row.id] {
				if member.source == "auto" {
					target[member.workID] = true
				}
			}
			targets[row.id] = target
			continue
		}
		claimedBy := -1
		for compIndex, seriesID := range compSeries {
			if seriesID == row.id {
				claimedBy = compIndex
				break
			}
		}
		if claimedBy >= 0 {
			candidates := candidatesOf(comps[claimedBy])
			if len(candidates) >= 2 {
				for _, id := range candidates {
					target[id] = true
				}
			} else if manualTitle[row.id] {
				// H1 (D69 方案 A)：改名的系列候选不足 2 个时（例如拆出/删除后
				// 只剩一个未锁定的 auto 成员），保留“候选 ∩ 现有 auto 成员”，
				// 不让第 1 阶段把它删成 0 成员。auto 名字的系列维持原逻辑，
				// 新建系列仍然要求 ≥2 个候选（见下方 newSeries）。
				autoMembers := map[int64]bool{}
				for _, member := range members[row.id] {
					if member.source == "auto" {
						autoMembers[member.workID] = true
					}
				}
				for _, id := range candidates {
					if autoMembers[id] {
						target[id] = true
					}
				}
			}
		}
		// Auto members not processed this run keep their membership whether
		// the series was claimed or not: their fetches failed or were never
		// attempted, so nothing is known about them (B2).
		for _, member := range members[row.id] {
			if member.source == "auto" && !locks[member.workID] {
				if _, wasProcessed := processed[member.workID]; !wasProcessed {
					target[member.workID] = true
				}
			}
		}
		targets[row.id] = target
	}
	var newSeries [][]int64
	for i, comp := range comps {
		if _, matched := compSeries[i]; matched {
			continue
		}
		if skippedComp[i] {
			// D41-A1: never auto-create a series from a conflicted component.
			continue
		}
		if candidates := candidatesOf(comp); len(candidates) >= 2 {
			newSeries = append(newSeries, candidates)
		}
	}
	// Phase 1: delete every automatic membership that is no longer wanted.
	for _, row := range series {
		target := targets[row.id]
		for _, member := range members[row.id] {
			if member.source != "auto" || target[member.workID] {
				continue
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_members WHERE series_id=? AND work_id=? AND source='auto'`, row.id, member.workID); err != nil {
				return stats, err
			}
			stats.MembersRemoved++
			if err = recordProvenance(member.workID, 0); err != nil {
				return stats, err
			}
		}
	}
	// Phase 2: insert missing automatic memberships into existing series.
	for _, row := range series {
		current := map[int64]bool{}
		for _, member := range members[row.id] {
			if member.source == "auto" {
				current[member.workID] = true
			}
		}
		for workID := range targets[row.id] {
			if current[workID] {
				continue
			}
			// L2: the component was computed before this transaction; a work
			// deleted in the meantime must be skipped instead of failing the
			// whole stage on its foreign key.
			res, insertErr := tx.ExecContext(ctx, `INSERT INTO work_series_members(work_id,series_id,source) SELECT ?,?,'auto' WHERE EXISTS(SELECT 1 FROM works WHERE id=?)`, workID, row.id, workID)
			if insertErr != nil {
				return stats, insertErr
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			stats.MembersAdded++
			if err = recordProvenance(workID, row.id); err != nil {
				return stats, err
			}
		}
	}
	// Phase 3: create series for unmatched components with at least two
	// candidates.
	for _, candidates := range newSeries {
		result, execErr := tx.ExecContext(ctx, `INSERT INTO work_series(title,title_source) VALUES('','auto')`)
		if execErr != nil {
			return stats, execErr
		}
		seriesID, _ := result.LastInsertId()
		stats.SeriesCreated++
		for _, id := range candidates {
			// L2: skip works deleted after the component was computed (same
			// foreign-key race as phase 2).
			res, insertErr := tx.ExecContext(ctx, `INSERT INTO work_series_members(work_id,series_id,source) SELECT ?,?,'auto' WHERE EXISTS(SELECT 1 FROM works WHERE id=?)`, id, seriesID, id)
			if insertErr != nil {
				return stats, insertErr
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			stats.MembersAdded++
			if err = recordProvenance(id, seriesID); err != nil {
				return stats, err
			}
		}
	}
	// Delete degenerate series (D69 predicate: empty always goes; a renamed
	// single-member series stays). Frozen series are exempt (D41-A1).
	degenerateRows, err := tx.QueryContext(ctx, `SELECT s.id FROM work_series s WHERE `+degenerateSeriesSQL)
	if err != nil {
		return stats, err
	}
	var degenerate []int64
	for degenerateRows.Next() {
		var id int64
		if err = degenerateRows.Scan(&id); err != nil {
			degenerateRows.Close()
			return stats, err
		}
		degenerate = append(degenerate, id)
	}
	if err = degenerateRows.Err(); err != nil {
		degenerateRows.Close()
		return stats, err
	}
	degenerateRows.Close()
	for _, id := range degenerate {
		if frozenSeries[id] {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_series WHERE id=?`, id); err != nil {
			return stats, err
		}
		stats.SeriesDeleted++
	}
	// Recompute representative and automatic title for every surviving
	// series. Writes happen only on change so a repeated run stays a no-op.
	seriesRows, err = tx.QueryContext(ctx, `SELECT id,title,title_source,COALESCE(representative_work_id,0) FROM work_series ORDER BY id`)
	if err != nil {
		return stats, err
	}
	series = series[:0]
	for seriesRows.Next() {
		var row seriesRow
		if err = seriesRows.Scan(&row.id, &row.title, &row.titleSource, &row.repWorkID); err != nil {
			seriesRows.Close()
			return stats, err
		}
		series = append(series, row)
	}
	if err = seriesRows.Err(); err != nil {
		seriesRows.Close()
		return stats, err
	}
	seriesRows.Close()
	for _, row := range series {
		if err = refreshSeriesRow(ctx, tx, row.id); err != nil {
			return stats, err
		}
	}
	if err = RecordEnrichmentEffectTx(ctx, tx, "series_members", "matched"); err != nil {
		return stats, err
	}
	if err = tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}

// putSeriesMergeSkippedProvenance records a D41-A1 merge conflict, but only
// when the record does not already exist with the same content: a conflict
// that persists across runs must not refresh run_id or updated_at, keeping a
// repeated run a true no-op. When the conflict resolves, ApplyAutoSeries
// deletes the record instead.
func putSeriesMergeSkippedProvenance(ctx context.Context, tx *sql.Tx, workID, runID int64, seriesIDs []int64) error {
	value := map[string]any{"series": seriesIDs, "reason": "component overlaps multiple manually renamed series"}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT value_json FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='series_merge_skipped' AND source='bangumi'`, workID).Scan(&existing)
	if err == nil && existing == string(raw) {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return putProvenance(ctx, tx, "work", workID, "series_merge_skipped", "bangumi", "", runID, value)
}

// seriesRepresentative picks the earliest-aired member (D25): by year, then
// by the Bangumi air date, then by work id.
func seriesRepresentative(ctx context.Context, tx *sql.Tx, seriesID int64) (int64, string, error) {
	var workID int64
	var title string
	err := tx.QueryRowContext(ctx, `SELECT w.id,w.title FROM work_series_members m JOIN works w ON w.id=m.work_id LEFT JOIN work_external_profiles p ON p.work_id=w.id AND p.source='bangumi' WHERE m.series_id=? ORDER BY COALESCE(NULLIF(w.year,0),9999),COALESCE(CASE WHEN json_valid(p.raw_json) THEN NULLIF(json_extract(p.raw_json,'$.date'),'') END,'9999'),w.id LIMIT 1`, seriesID).Scan(&workID, &title)
	return workID, title, err
}

// refreshSeriesRow recomputes the representative work and — while the title
// is still automatic — the series title. updated_at only moves when the row
// actually changed, so an idempotent pass leaves the row untouched.
func refreshSeriesRow(ctx context.Context, tx *sql.Tx, seriesID int64) error {
	var title, titleSource string
	var repWorkID int64
	if err := tx.QueryRowContext(ctx, `SELECT title,title_source,COALESCE(representative_work_id,0) FROM work_series WHERE id=?`, seriesID).Scan(&title, &titleSource, &repWorkID); err != nil {
		return err
	}
	newRep, newTitle, err := seriesRepresentative(ctx, tx, seriesID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	wantTitle := title
	if titleSource == "auto" {
		wantTitle = newTitle
	}
	if newRep == repWorkID && wantTitle == title {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE work_series SET representative_work_id=?,title=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, newRep, wantTitle, seriesID)
	return err
}

// degenerateSeriesSQL matches a series that no longer needs to exist. An
// empty series never survives. A single-member series survives when the
// user renamed it (D69, consistent with D61 allowing one-work series) or
// when the remaining member is manual; only automatic-titled series with no
// manual members degenerate at one member. D41-frozen series are exempted
// by the caller (ApplyAutoSeries), not by this predicate.
const degenerateSeriesSQL = `(SELECT COUNT(*) FROM work_series_members m2 WHERE m2.series_id=s.id)=0 OR ((SELECT COUNT(*) FROM work_series_members m2 WHERE m2.series_id=s.id)=1 AND s.title_source='auto' AND NOT EXISTS(SELECT 1 FROM work_series_members m WHERE m.series_id=s.id AND m.source='manual'))`

// deleteDegenerateSeries removes a series matching degenerateSeriesSQL. It
// reports whether the series was deleted.
func deleteDegenerateSeries(ctx context.Context, tx *sql.Tx, seriesID int64) (bool, error) {
	var degenerate bool
	if err := tx.QueryRowContext(ctx, `SELECT `+degenerateSeriesSQL+` FROM work_series s WHERE s.id=?`, seriesID).Scan(&degenerate); err != nil {
		return false, err
	}
	if !degenerate {
		return false, nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM work_series WHERE id=?`, seriesID)
	return err == nil, err
}

// cleanupSeriesAfterWorkRemoval runs after works were deleted (membership and
// representative references already cascaded): degenerate series go away and
// series that lost their representative get a fresh one.
func cleanupSeriesAfterWorkRemoval(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.id FROM work_series s WHERE `+degenerateSeriesSQL)
	if err != nil {
		return err
	}
	var degenerate []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		degenerate = append(degenerate, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range degenerate {
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_series WHERE id=?`, id); err != nil {
			return err
		}
	}
	orphanRows, err := tx.QueryContext(ctx, `SELECT id FROM work_series WHERE representative_work_id IS NULL AND EXISTS(SELECT 1 FROM work_series_members m WHERE m.series_id=work_series.id)`)
	if err != nil {
		return err
	}
	var orphans []int64
	for orphanRows.Next() {
		var id int64
		if err = orphanRows.Scan(&id); err != nil {
			orphanRows.Close()
			return err
		}
		orphans = append(orphans, id)
	}
	if err = orphanRows.Err(); err != nil {
		orphanRows.Close()
		return err
	}
	orphanRows.Close()
	for _, id := range orphans {
		if err = refreshSeriesRow(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// DetachWorkFromSeries removes the work from its series and locks it so the
// automatic pass never groups it again (R3).
func (s *Store) DetachWorkFromSeries(ctx context.Context, workID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// L1: the first statement is a write so the transaction takes the SQLite
	// writer lock immediately instead of upgrading from a stale read snapshot.
	if _, err = tx.ExecContext(ctx, `UPDATE work_series SET id=id WHERE 0`); err != nil {
		return err
	}
	var seriesID int64
	if err = tx.QueryRowContext(ctx, `SELECT series_id FROM work_series_members WHERE work_id=?`, workID).Scan(&seriesID); errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_members WHERE work_id=?`, workID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_series_locks(work_id) VALUES(?)`, workID); err != nil {
		return err
	}
	deleted, err := deleteDegenerateSeries(ctx, tx, seriesID)
	if err != nil {
		return err
	}
	if !deleted {
		if err = refreshSeriesRow(ctx, tx, seriesID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddWorkToSeries manually places a work into a series, clearing any detach
// lock and moving it out of a previous series (D24: one series per work).
func (s *Store) AddWorkToSeries(ctx context.Context, workID, seriesID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = addWorkToSeriesTx(ctx, tx, workID, seriesID); err != nil {
		return err
	}
	return tx.Commit()
}

// RenameWorkSeries sets a user-chosen title; the automatic pass keeps it
// (title_source='manual') while still updating the representative work (D25).
func (s *Store) RenameWorkSeries(ctx context.Context, seriesID int64, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("%w: series title is required", ErrInvalidWork)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE work_series SET title=?,title_source='manual',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, title, seriesID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DissolveWorkSeries removes the series and locks every member so the
// automatic pass never rebuilds it (R3). Locks are used instead of a
// "dissolved" flag because they are the existing per-work user-intent
// mechanism: a component made only of locked works never creates a series.
func (s *Store) DissolveWorkSeries(ctx context.Context, seriesID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_series_locks(work_id) SELECT work_id FROM work_series_members WHERE series_id=?`, seriesID); err != nil {
		return err
	}
	deleteResult, err := tx.ExecContext(ctx, `DELETE FROM work_series WHERE id=?`, seriesID)
	if err != nil {
		return err
	}
	if n, _ := deleteResult.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// SeriesForWork returns the series a work belongs to, sql.ErrNoRows when the
// work is not a member of any series.
func (s *Store) SeriesForWork(ctx context.Context, workID int64) (WorkSeries, []WorkSeriesMember, error) {
	var seriesID int64
	if err := s.db.QueryRowContext(ctx, `SELECT series_id FROM work_series_members WHERE work_id=?`, workID).Scan(&seriesID); err != nil {
		return WorkSeries{}, nil, err
	}
	series, err := s.WorkSeriesByID(ctx, seriesID)
	if err != nil {
		return WorkSeries{}, nil, err
	}
	members, err := s.SeriesMembers(ctx, seriesID)
	return series, members, err
}

func (s *Store) WorkSeriesByID(ctx context.Context, id int64) (WorkSeries, error) {
	var value WorkSeries
	err := s.db.QueryRowContext(ctx, `SELECT id,title,title_source,COALESCE(representative_work_id,0),(SELECT COUNT(*) FROM work_series_members m WHERE m.series_id=work_series.id),created_at,updated_at FROM work_series WHERE id=?`, id).Scan(&value.ID, &value.Title, &value.TitleSource, &value.RepresentativeWorkID, &value.MemberCount, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

// ListSeries returns every series ordered by title for pickers.
func (s *Store) ListSeries(ctx context.Context) ([]WorkSeries, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,title_source,COALESCE(representative_work_id,0),(SELECT COUNT(*) FROM work_series_members m WHERE m.series_id=work_series.id),created_at,updated_at FROM work_series ORDER BY title COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []WorkSeries
	for rows.Next() {
		var value WorkSeries
		if err = rows.Scan(&value.ID, &value.Title, &value.TitleSource, &value.RepresentativeWorkID, &value.MemberCount, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ListSeriesOptions searches series by title for the management page's
// series picker (parameterized LIKE, ordered by title).
func (s *Store) ListSeriesOptions(ctx context.Context, query string, limit int) ([]WorkSeries, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,title_source,COALESCE(representative_work_id,0),(SELECT COUNT(*) FROM work_series_members m WHERE m.series_id=work_series.id),created_at,updated_at FROM work_series WHERE (?='' OR title LIKE '%'||?||'%') ORDER BY title COLLATE NOCASE,id LIMIT ?`, strings.TrimSpace(query), strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []WorkSeries
	for rows.Next() {
		value, scanErr := scanWorkSeries(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// SeriesByWork returns the series each given work belongs to (works without
// a series are absent from the map). Used by the options endpoint to render
// “将从《Z》移入” hints.
func (s *Store) SeriesByWork(ctx context.Context, workIDs []int64) (map[int64]WorkSeries, error) {
	out := map[int64]WorkSeries{}
	if len(workIDs) == 0 {
		return out, nil
	}
	placeholders, args := int64Placeholders(workIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT m.work_id,s.id,s.title,s.title_source,COALESCE(s.representative_work_id,0),(SELECT COUNT(*) FROM work_series_members m2 WHERE m2.series_id=s.id),s.created_at,s.updated_at FROM work_series_members m JOIN work_series s ON s.id=m.series_id WHERE m.work_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var workID int64
		var series WorkSeries
		if err = rows.Scan(&workID, &series.ID, &series.Title, &series.TitleSource, &series.RepresentativeWorkID, &series.MemberCount, &series.CreatedAt, &series.UpdatedAt); err != nil {
			return nil, err
		}
		out[workID] = series
	}
	return out, rows.Err()
}

// SeriesMembers lists a series' members ordered like the representative rule:
// earliest first (year, then Bangumi date, then id).
func (s *Store) SeriesMembers(ctx context.Context, seriesID int64) ([]WorkSeriesMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.title,COALESCE(w.reading_title,''),COALESCE(w.translated_title,''),w.type,w.type_locked,w.origin,COALESCE(w.year,0),COALESCE(w.poster_url,''),COALESCE(w.external_id,''),(SELECT COUNT(*) FROM (SELECT track_id FROM work_tracks WHERE work_id=w.id UNION SELECT t.id FROM tracks t JOIN album_works aw ON aw.album_id=t.album_id WHERE aw.work_id=w.id) wtc WHERE `+trackVisibleSQL("wtc.track_id")+`),w.created_at,w.updated_at,m.source FROM work_series_members m JOIN works w ON w.id=m.work_id LEFT JOIN work_external_profiles p ON p.work_id=w.id AND p.source='bangumi' WHERE m.series_id=? ORDER BY COALESCE(NULLIF(w.year,0),9999),COALESCE(CASE WHEN json_valid(p.raw_json) THEN NULLIF(json_extract(p.raw_json,'$.date'),'') END,'9999'),w.id`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []WorkSeriesMember
	for rows.Next() {
		var value WorkSeriesMember
		var typeLocked int
		value.SeriesID = seriesID
		if err = rows.Scan(&value.Work.ID, &value.Work.Title, &value.Work.ReadingTitle, &value.Work.TranslatedTitle, &value.Work.Type, &typeLocked, &value.Work.Origin, &value.Work.Year, &value.Work.PosterURL, &value.Work.ExternalID, &value.Work.TrackCount, &value.Work.CreatedAt, &value.Work.UpdatedAt, &value.Source); err != nil {
			return nil, err
		}
		value.Work.TypeLocked = typeLocked != 0
		values = append(values, value)
	}
	return values, rows.Err()
}

// BangumiSeriesSeed pairs a work with its bound Bangumi subject. SubjectType
// is 0 when the cached profile does not record the subject type yet.
type BangumiSeriesSeed struct {
	WorkID      int64
	SubjectID   int64
	SubjectType int
}

// BangumiSeriesSeeds lists every work bound to a Bangumi subject.
func (s *Store) BangumiSeriesSeeds(ctx context.Context) ([]BangumiSeriesSeed, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,p.external_id,CASE WHEN json_valid(p.raw_json) THEN COALESCE(json_extract(p.raw_json,'$.type'),0) ELSE 0 END FROM works w JOIN work_external_profiles p ON p.work_id=w.id AND p.source='bangumi' ORDER BY w.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []BangumiSeriesSeed
	for rows.Next() {
		var value BangumiSeriesSeed
		var external string
		if err = rows.Scan(&value.WorkID, &external, &value.SubjectType); err != nil {
			return nil, err
		}
		value.SubjectID, _ = strconv.ParseInt(external, 10, 64)
		if value.SubjectID > 0 {
			values = append(values, value)
		}
	}
	return values, rows.Err()
}

// AutoSeriesMemberWorkIDs lists every work holding automatic series
// membership (used to reconcile stale membership, M3).
func (s *Store) AutoSeriesMemberWorkIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT work_id FROM work_series_members WHERE source='auto' ORDER BY work_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		values = append(values, id)
	}
	return values, rows.Err()
}

// WorkSeriesLocked reports whether the work was detached by the user and is
// excluded from automatic grouping.
func (s *Store) WorkSeriesLocked(ctx context.Context, workID int64) (bool, error) {
	var locked bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_series_locks WHERE work_id=?)`, workID).Scan(&locked)
	return locked, err
}

// WorkListRow is one folded row of the works list: either a standalone work
// or a series collapsed onto its representative work.
type WorkListRow struct {
	Work           *Work
	Series         *WorkSeries
	Representative Work
	Members        []WorkSeriesMember
	// MatchedCount 是 D51 类型筛选下符合类型的成员数（此时 Members 只含符合
	// 类型的成员、Representative 为第一部符合类型的成员）；未按类型筛选时为 0。
	MatchedCount int
}

const workFoldGroupSQL = ` FROM works w LEFT JOIN work_series_members m ON m.work_id=w.id LEFT JOIN work_series s ON s.id=m.series_id WHERE `

// foldedRowIndexExpr is the leading-letter index expression for folded rows:
// series rows filter and sort by the series title that is displayed.
const foldedRowIndexExpr = `CASE WHEN s.id IS NOT NULL THEN s.title ELSE COALESCE(NULLIF(w.reading_title,''),w.title) END`

// ListWorksFolded collapses series into single rows for the admin works page.
// Pagination counts folded rows, so a 3-work series occupies one row. All
// reads share one read-only transaction, so a concurrent delete cannot leave
// a row pointing at a missing series or work.
func (s *Store) ListWorksFolded(ctx context.Context, filter WorkFilters) ([]WorkListRow, error) {
	limit, offset := workPage(filter)
	where, args := workWherePrefix(filter, "w.", foldedRowIndexExpr, "COALESCE(s.title,'')")
	order := "MIN(" + foldedRowIndexExpr + ") COLLATE NOCASE,row_key"
	if filter.Sort == "year" {
		order = "MIN(NULLIF(COALESCE(w.year,0),0)) DESC," + order
	} else if filter.Sort == "updated" {
		order = "MAX(CASE WHEN s.id IS NOT NULL THEN s.updated_at ELSE w.updated_at END) DESC,row_key DESC"
	}
	args = append(args, limit, offset)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN m.series_id IS NULL THEN -w.id ELSE m.series_id END AS row_key,COALESCE(m.series_id,0)`+workFoldGroupSQL+where+` GROUP BY row_key ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	type rowRef struct {
		workID, seriesID int64
	}
	var refs []rowRef
	for rows.Next() {
		var key, seriesID int64
		if err = rows.Scan(&key, &seriesID); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, rowRef{workID: -key, seriesID: seriesID})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// Batch the row loads: one query for the series, one for all members, one
	// for standalone and representative works.
	var seriesIDs, workIDs []int64
	for _, ref := range refs {
		if ref.seriesID != 0 {
			seriesIDs = append(seriesIDs, ref.seriesID)
		} else {
			workIDs = append(workIDs, ref.workID)
		}
	}
	seriesMap, err := queryWorkSeriesByIDs(ctx, tx, seriesIDs)
	if err != nil {
		return nil, err
	}
	memberMap, err := querySeriesMembersByIDs(ctx, tx, seriesIDs)
	if err != nil {
		return nil, err
	}
	for _, series := range seriesMap {
		if series.RepresentativeWorkID != 0 {
			workIDs = append(workIDs, series.RepresentativeWorkID)
		}
	}
	workMap, err := queryWorksByIDs(ctx, tx, workIDs)
	if err != nil {
		return nil, err
	}
	values := make([]WorkListRow, 0, len(refs))
	for _, ref := range refs {
		if ref.seriesID == 0 {
			work, ok := workMap[ref.workID]
			if !ok {
				return nil, sql.ErrNoRows
			}
			values = append(values, WorkListRow{Work: &work})
			continue
		}
		series, ok := seriesMap[ref.seriesID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		row := WorkListRow{Series: &series, Members: memberMap[ref.seriesID]}
		if series.RepresentativeWorkID != 0 {
			row.Representative = workMap[series.RepresentativeWorkID]
		}
		if filter.Type != "" {
			// D51：类型是筛选而不是层级。系列行只展开符合类型的成员，海报取
			// 第一部符合类型的成员，并记录“其中 K 部”的计数（行本身能出现，
			// 说明 WHERE 已保证至少一名成员符合，计数与分页口径一致）。
			matched := make([]WorkSeriesMember, 0, len(row.Members))
			for _, member := range row.Members {
				if member.Work.Type == filter.Type {
					matched = append(matched, member)
				}
			}
			if len(matched) > 0 {
				row.Members = matched
				row.MatchedCount = len(matched)
				row.Representative = matched[0].Work
			}
		}
		values = append(values, row)
	}
	return values, nil
}

// CountWorksFolded counts folded rows (a series counts once).
func (s *Store) CountWorksFolded(ctx context.Context, filter WorkFilters) (int64, error) {
	where, args := workWherePrefix(filter, "w.", foldedRowIndexExpr, "COALESCE(s.title,'')")
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT CASE WHEN m.series_id IS NULL THEN -w.id ELSE m.series_id END AS row_key`+workFoldGroupSQL+where+` GROUP BY row_key)`, args...).Scan(&total)
	return total, err
}

func int64Placeholders(ids []int64) (string, []any) {
	parts := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		parts[i] = "?"
		args[i] = id
	}
	return strings.Join(parts, ","), args
}

const workSeriesColumns = `id,title,title_source,COALESCE(representative_work_id,0),(SELECT COUNT(*) FROM work_series_members m WHERE m.series_id=work_series.id),created_at,updated_at`

func scanWorkSeries(rows *sql.Rows) (WorkSeries, error) {
	var value WorkSeries
	err := rows.Scan(&value.ID, &value.Title, &value.TitleSource, &value.RepresentativeWorkID, &value.MemberCount, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (s *Store) workSeriesByIDs(ctx context.Context, ids []int64) (map[int64]WorkSeries, error) {
	return queryWorkSeriesByIDs(ctx, s.db, ids)
}

type sqlQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryWorkSeriesByIDs(ctx context.Context, q sqlQuerier, ids []int64) (map[int64]WorkSeries, error) {
	out := map[int64]WorkSeries{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := int64Placeholders(ids)
	rows, err := q.QueryContext(ctx, `SELECT `+workSeriesColumns+` FROM work_series WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		value, scanErr := scanWorkSeries(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out[value.ID] = value
	}
	return out, rows.Err()
}

// seriesMembersByIDs batches SeriesMembers for many series; member order
// within a series matches SeriesMembers (earliest first).
func (s *Store) seriesMembersByIDs(ctx context.Context, ids []int64) (map[int64][]WorkSeriesMember, error) {
	return querySeriesMembersByIDs(ctx, s.db, ids)
}

func querySeriesMembersByIDs(ctx context.Context, q sqlQuerier, ids []int64) (map[int64][]WorkSeriesMember, error) {
	out := map[int64][]WorkSeriesMember{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := int64Placeholders(ids)
	rows, err := q.QueryContext(ctx, `SELECT m.series_id,w.id,w.title,COALESCE(w.reading_title,''),COALESCE(w.translated_title,''),w.type,w.type_locked,w.origin,COALESCE(w.year,0),COALESCE(w.poster_url,''),COALESCE(w.external_id,''),(SELECT COUNT(*) FROM (SELECT track_id FROM work_tracks WHERE work_id=w.id UNION SELECT t.id FROM tracks t JOIN album_works aw ON aw.album_id=t.album_id WHERE aw.work_id=w.id) wtc WHERE `+trackVisibleSQL("wtc.track_id")+`),w.created_at,w.updated_at,m.source FROM work_series_members m JOIN works w ON w.id=m.work_id LEFT JOIN work_external_profiles p ON p.work_id=w.id AND p.source='bangumi' WHERE m.series_id IN (`+placeholders+`) ORDER BY m.series_id,COALESCE(NULLIF(w.year,0),9999),COALESCE(CASE WHEN json_valid(p.raw_json) THEN NULLIF(json_extract(p.raw_json,'$.date'),'') END,'9999'),w.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var value WorkSeriesMember
		var typeLocked int
		if err = rows.Scan(&value.SeriesID, &value.Work.ID, &value.Work.Title, &value.Work.ReadingTitle, &value.Work.TranslatedTitle, &value.Work.Type, &typeLocked, &value.Work.Origin, &value.Work.Year, &value.Work.PosterURL, &value.Work.ExternalID, &value.Work.TrackCount, &value.Work.CreatedAt, &value.Work.UpdatedAt, &value.Source); err != nil {
			return nil, err
		}
		value.Work.TypeLocked = typeLocked != 0
		out[value.SeriesID] = append(out[value.SeriesID], value)
	}
	return out, rows.Err()
}

// worksByIDs loads works with the same column set as WorkByID.
func (s *Store) worksByIDs(ctx context.Context, ids []int64) (map[int64]Work, error) {
	return queryWorksByIDs(ctx, s.db, ids)
}

func queryWorksByIDs(ctx context.Context, q sqlQuerier, ids []int64) (map[int64]Work, error) {
	out := map[int64]Work{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := int64Placeholders(ids)
	rows, err := q.QueryContext(ctx, `SELECT id,title,COALESCE(reading_title,''),COALESCE(translated_title,''),type,type_locked,origin,COALESCE(year,0),COALESCE(poster_url,''),COALESCE(external_id,''),(SELECT COUNT(*) FROM (SELECT track_id FROM work_tracks WHERE work_id=works.id UNION SELECT t.id FROM tracks t JOIN album_works aw ON aw.album_id=t.album_id WHERE aw.work_id=works.id) wtc WHERE `+trackVisibleSQL("wtc.track_id")+`),created_at,updated_at FROM works WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var value Work
		var typeLocked int
		if err = rows.Scan(&value.ID, &value.Title, &value.ReadingTitle, &value.TranslatedTitle, &value.Type, &typeLocked, &value.Origin, &value.Year, &value.PosterURL, &value.ExternalID, &value.TrackCount, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		value.TypeLocked = typeLocked != 0
		out[value.ID] = value
	}
	return out, rows.Err()
}
