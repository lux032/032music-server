package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrSeriesTitleConflict is returned when a merge needs a user-chosen title:
// both series carry a manual title and no title was given (D58).
var ErrSeriesTitleConflict = errors.New("series title conflict: both series have manual titles")

// SeriesSuggestionInput is one suggested work pair produced by the Bangumi
// relation scan. WorkA < WorkB; RelationAB is the relation registered on
// work A's subject towards work B's subject, RelationBA the reverse.
type SeriesSuggestionInput struct {
	WorkA, WorkB           int64
	SubjectA, SubjectB     int64
	RelationAB, RelationBA string
	Kind                   string // "cross" | "sequel"
}

// SeriesSuggestion is a pending suggestion with both works and their current
// series (nil when the work belongs to no series) for the review UI.
type SeriesSuggestion struct {
	ID                     int64
	WorkA, WorkB           Work
	SubjectA, SubjectB     int64
	RelationAB, RelationBA string
	Kind                   string
	SeriesA, SeriesB       *WorkSeries
	CreatedAt              string
}

// SeriesPageRow is one series on the management page: the series itself plus
// the member count per work type.
type SeriesPageRow struct {
	Series     WorkSeries
	TypeCounts map[string]int
}

func clampSeriesPage(limit, offset int) (int, int) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// ReplaceSeriesSuggestions upserts this run's suggestions and removes stale
// ones. Only pairs where both works were processed this run (their subjects'
// relations were fetched successfully, or they were resolved but are no
// longer suggestable) are eligible for deletion, so a work whose fetch
// failed keeps its old suggestions untouched. Suggestions touching a locked
// work are always deleted (D56). Every insert re-checks inside the
// transaction that the subject pair was not decided and the works are not
// locked or already in the same series, closing the generation race (M2).
// Unchanged rows are left alone: a repeated run writes nothing.
func (s *Store) ReplaceSeriesSuggestions(ctx context.Context, runID int64, suggestions []SeriesSuggestionInput, processedWorks []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// L6/M1: the first statement is a write so the transaction takes the
	// SQLite writer lock immediately instead of upgrading from a stale read
	// snapshot (BUSY_SNAPSHOT). It doubles as the D56 cleanup: suggestions
	// touching a locked work never survive a replace.
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_suggestions WHERE work_a IN (SELECT work_id FROM work_series_locks) OR work_b IN (SELECT work_id FROM work_series_locks)`); err != nil {
		return err
	}
	current := map[[2]int64]bool{}
	for _, suggestion := range suggestions {
		if suggestion.WorkA <= 0 || suggestion.WorkB <= 0 || suggestion.WorkA == suggestion.WorkB {
			continue
		}
		if suggestion.WorkA > suggestion.WorkB {
			suggestion.WorkA, suggestion.WorkB = suggestion.WorkB, suggestion.WorkA
			suggestion.SubjectA, suggestion.SubjectB = suggestion.SubjectB, suggestion.SubjectA
			suggestion.RelationAB, suggestion.RelationBA = suggestion.RelationBA, suggestion.RelationAB
		}
		current[[2]int64{suggestion.WorkA, suggestion.WorkB}] = true
		// M2: the candidate pair was computed from pre-transaction reads; the
		// WHERE clause re-verifies it against decisions, locks and current
		// series membership inside this transaction. L2: a work deleted since
		// the generation pass is filtered here too, so its foreign key cannot
		// roll back the whole replace.
		if _, err = tx.ExecContext(ctx, `INSERT INTO work_series_suggestions(work_a,work_b,subject_a,subject_b,relation_ab,relation_ba,kind,run_id) SELECT ?,?,?,?,?,?,?,NULLIF(?,0) WHERE NOT EXISTS(SELECT 1 FROM work_series_suggestion_decisions d WHERE d.subject_a=MIN(?,?) AND d.subject_b=MAX(?,?)) AND NOT EXISTS(SELECT 1 FROM work_series_locks l WHERE l.work_id IN (?,?)) AND NOT EXISTS(SELECT 1 FROM work_series_members ma JOIN work_series_members mb ON mb.series_id=ma.series_id WHERE ma.work_id=? AND mb.work_id=?) AND EXISTS(SELECT 1 FROM works wa WHERE wa.id=?) AND EXISTS(SELECT 1 FROM works wb WHERE wb.id=?) ON CONFLICT(work_a,work_b) DO UPDATE SET subject_a=excluded.subject_a,subject_b=excluded.subject_b,relation_ab=excluded.relation_ab,relation_ba=excluded.relation_ba,kind=excluded.kind,run_id=excluded.run_id,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE work_series_suggestions.subject_a!=excluded.subject_a OR work_series_suggestions.subject_b!=excluded.subject_b OR work_series_suggestions.relation_ab!=excluded.relation_ab OR work_series_suggestions.relation_ba!=excluded.relation_ba OR work_series_suggestions.kind!=excluded.kind`, suggestion.WorkA, suggestion.WorkB, suggestion.SubjectA, suggestion.SubjectB, suggestion.RelationAB, suggestion.RelationBA, suggestion.Kind, runID, suggestion.SubjectA, suggestion.SubjectB, suggestion.SubjectA, suggestion.SubjectB, suggestion.WorkA, suggestion.WorkB, suggestion.WorkA, suggestion.WorkB, suggestion.WorkA, suggestion.WorkB); err != nil {
			return err
		}
		// 生成期竞态的收尾：这对作品在生成后、本事务前被决定（接受/拒绝）
		// 或并入了同一系列时，上面的 INSERT…SELECT 会被复核条件拦下，但已
		// 存在的旧行不能因为“本轮又生成了它”而留下来——按相同条件当场删除，
		// 不留到下一轮（锁定已由事务首句统一处理）。
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_suggestions WHERE work_a=? AND work_b=? AND (EXISTS(SELECT 1 FROM work_series_suggestion_decisions d WHERE d.subject_a=MIN(?,?) AND d.subject_b=MAX(?,?)) OR EXISTS(SELECT 1 FROM work_series_members ma JOIN work_series_members mb ON mb.series_id=ma.series_id WHERE ma.work_id=? AND mb.work_id=?))`, suggestion.WorkA, suggestion.WorkB, suggestion.SubjectA, suggestion.SubjectB, suggestion.SubjectA, suggestion.SubjectB, suggestion.WorkA, suggestion.WorkB); err != nil {
			return err
		}
	}
	if len(processedWorks) > 0 {
		// L4: pass the processed set as a JSON array through json_each instead
		// of expanding one placeholder per work (SQLite's variable limit).
		processedJSON, marshalErr := json.Marshal(processedWorks)
		if marshalErr != nil {
			return marshalErr
		}
		// Both endpoints were processed: work_a < work_b, so both appear in
		// the same processed set.
		rows, err := tx.QueryContext(ctx, `SELECT id,work_a,work_b FROM work_series_suggestions WHERE work_a IN (SELECT value FROM json_each(?)) AND work_b IN (SELECT value FROM json_each(?))`, string(processedJSON), string(processedJSON))
		if err != nil {
			return err
		}
		var stale []int64
		for rows.Next() {
			var id, a, b int64
			if err = rows.Scan(&id, &a, &b); err != nil {
				rows.Close()
				return err
			}
			if !current[[2]int64{a, b}] {
				stale = append(stale, id)
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range stale {
			if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_suggestions WHERE id=?`, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// PendingSeriesSuggestions lists unresolved suggestions oldest first, with
// both works (title, type, year, poster) and the series each work currently
// belongs to (nil when none). Suggestions touching a locked work are hidden
// (D56); ReplaceSeriesSuggestions deletes them outright on the next run.
func (s *Store) PendingSeriesSuggestions(ctx context.Context, limit, offset int) ([]SeriesSuggestion, error) {
	limit, offset = clampSeriesPage(limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id,work_a,work_b,subject_a,subject_b,relation_ab,relation_ba,kind,created_at FROM work_series_suggestions s WHERE NOT EXISTS(SELECT 1 FROM work_series_locks l WHERE l.work_id=s.work_a OR l.work_id=s.work_b) ORDER BY id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []SeriesSuggestion
	workIDs := map[int64]bool{}
	for rows.Next() {
		var value SeriesSuggestion
		if err = rows.Scan(&value.ID, &value.WorkA.ID, &value.WorkB.ID, &value.SubjectA, &value.SubjectB, &value.RelationAB, &value.RelationBA, &value.Kind, &value.CreatedAt); err != nil {
			return nil, err
		}
		workIDs[value.WorkA.ID] = true
		workIDs[value.WorkB.ID] = true
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return values, nil
	}
	ids := make([]int64, 0, len(workIDs))
	for id := range workIDs {
		ids = append(ids, id)
	}
	workMap, err := s.worksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	placeholders, args := int64Placeholders(ids)
	memberRows, err := s.db.QueryContext(ctx, `SELECT work_id,series_id FROM work_series_members WHERE work_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	seriesOf := map[int64]int64{}
	seriesIDs := map[int64]bool{}
	for memberRows.Next() {
		var workID, seriesID int64
		if err = memberRows.Scan(&workID, &seriesID); err != nil {
			memberRows.Close()
			return nil, err
		}
		seriesOf[workID] = seriesID
		seriesIDs[seriesID] = true
	}
	if err = memberRows.Err(); err != nil {
		memberRows.Close()
		return nil, err
	}
	memberRows.Close()
	seriesIDList := make([]int64, 0, len(seriesIDs))
	for id := range seriesIDs {
		seriesIDList = append(seriesIDList, id)
	}
	seriesMap, err := s.workSeriesByIDs(ctx, seriesIDList)
	if err != nil {
		return nil, err
	}
	for i := range values {
		if work, ok := workMap[values[i].WorkA.ID]; ok {
			values[i].WorkA = work
		}
		if work, ok := workMap[values[i].WorkB.ID]; ok {
			values[i].WorkB = work
		}
		if seriesID, ok := seriesOf[values[i].WorkA.ID]; ok {
			if series, ok2 := seriesMap[seriesID]; ok2 {
				values[i].SeriesA = &series
			}
		}
		if seriesID, ok := seriesOf[values[i].WorkB.ID]; ok {
			if series, ok2 := seriesMap[seriesID]; ok2 {
				values[i].SeriesB = &series
			}
		}
	}
	return values, nil
}

// ErrSeriesSuggestionStale is returned when a suggestion no longer matches
// reality at accept time — currently: one of the works was locked after the
// suggestion was generated (D56). The suggestion is deleted and the lock is
// never cleared by acceptance.
var ErrSeriesSuggestionStale = errors.New("series suggestion is stale")

// AcceptSeriesSuggestion applies a suggestion by the works' current
// membership (D53): neither in a series creates one, one in a series adds the
// other as a manual member, two different series merge them (title per D58,
// ErrSeriesTitleConflict when both have manual titles and none is given), and
// already-shared series only closes the suggestion. A suggestion touching a
// work locked since its generation is stale: it is deleted and
// ErrSeriesSuggestionStale returned (M1). The decision is remembered by
// subject pair (D55).
func (s *Store) AcceptSeriesSuggestion(ctx context.Context, id int64, mergeTitle string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// L6: take the writer lock immediately with a harmless write.
	if _, err = tx.ExecContext(ctx, `UPDATE work_series_suggestions SET id=id WHERE id=?`, id); err != nil {
		return err
	}
	var workA, workB, subjectA, subjectB int64
	if err = tx.QueryRowContext(ctx, `SELECT work_a,work_b,subject_a,subject_b FROM work_series_suggestions WHERE id=?`, id).Scan(&workA, &workB, &subjectA, &subjectB); err != nil {
		return err
	}
	// M1: a work locked after generation must not be unlocked by acceptance.
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_series_locks WHERE work_id IN (?,?))`, workA, workB).Scan(&locked); err != nil {
		return err
	}
	if locked {
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_suggestions WHERE id=?`, id); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrSeriesSuggestionStale
	}
	seriesOf := func(workID int64) (int64, error) {
		var seriesID int64
		err := tx.QueryRowContext(ctx, `SELECT series_id FROM work_series_members WHERE work_id=?`, workID).Scan(&seriesID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return seriesID, err
	}
	seriesA, err := seriesOf(workA)
	if err != nil {
		return err
	}
	seriesB, err := seriesOf(workB)
	if err != nil {
		return err
	}
	switch {
	case seriesA == 0 && seriesB == 0:
		if _, err = createWorkSeriesTx(ctx, tx, "", []int64{workA, workB}); err != nil {
			return err
		}
	case seriesA == seriesB:
		// Already in the same series: nothing to change.
	case seriesB == 0:
		if err = addWorkToSeriesTx(ctx, tx, workB, seriesA); err != nil {
			return err
		}
	case seriesA == 0:
		if err = addWorkToSeriesTx(ctx, tx, workA, seriesB); err != nil {
			return err
		}
	default:
		if _, err = mergeWorkSeriesTx(ctx, tx, seriesA, seriesB, mergeTitle); err != nil {
			return err
		}
	}
	if err = putSeriesSuggestionDecision(ctx, tx, subjectA, subjectB, "accepted"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_suggestions WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RejectSeriesSuggestion remembers the rejection by subject pair (D55) and
// closes the suggestion.
func (s *Store) RejectSeriesSuggestion(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// L6：首条语句为写操作，立刻拿到 SQLite 写锁，避免从过期读快照升级
	// （BUSY_SNAPSHOT）。
	if _, err = tx.ExecContext(ctx, `UPDATE work_series_suggestions SET id=id WHERE id=?`, id); err != nil {
		return err
	}
	var subjectA, subjectB int64
	if err = tx.QueryRowContext(ctx, `SELECT subject_a,subject_b FROM work_series_suggestions WHERE id=?`, id).Scan(&subjectA, &subjectB); err != nil {
		return err
	}
	if err = putSeriesSuggestionDecision(ctx, tx, subjectA, subjectB, "rejected"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_suggestions WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func putSeriesSuggestionDecision(ctx context.Context, tx *sql.Tx, subjectA, subjectB int64, decision string) error {
	if subjectA > subjectB {
		subjectA, subjectB = subjectB, subjectA
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO work_series_suggestion_decisions(subject_a,subject_b,decision) VALUES(?,?,?) ON CONFLICT(subject_a,subject_b) DO UPDATE SET decision=excluded.decision`, subjectA, subjectB, decision)
	return err
}

// SeriesSuggestionDecidedPairs returns every decided subject pair (D55), so
// the suggestion scan never proposes them again.
func (s *Store) SeriesSuggestionDecidedPairs(ctx context.Context) (map[[2]int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject_a,subject_b FROM work_series_suggestion_decisions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]int64]bool{}
	for rows.Next() {
		var a, b int64
		if err = rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out[[2]int64{a, b}] = true
	}
	return out, rows.Err()
}

// WorkSeriesMembership maps every member work to its series.
func (s *Store) WorkSeriesMembership(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT work_id,series_id FROM work_series_members`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var workID, seriesID int64
		if err = rows.Scan(&workID, &seriesID); err != nil {
			return nil, err
		}
		out[workID] = seriesID
	}
	return out, rows.Err()
}

// WorkSeriesManualMemberIDs returns every work holding manual series
// membership.
func (s *Store) WorkSeriesManualMemberIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT work_id FROM work_series_members WHERE source='manual'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// WorkSeriesLockedIDs returns every work the automatic pass must leave alone.
func (s *Store) WorkSeriesLockedIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT work_id FROM work_series_locks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// deleteSameSeriesSuggestionsTx drops pending suggestions whose two works
// are already in the same series (L7): after a merge/create/add they would
// otherwise linger until the next generation run.
func deleteSameSeriesSuggestionsTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM work_series_suggestions WHERE EXISTS(SELECT 1 FROM work_series_members ma JOIN work_series_members mb ON mb.series_id=ma.series_id WHERE ma.work_id=work_series_suggestions.work_a AND mb.work_id=work_series_suggestions.work_b)`)
	return err
}

// addWorkToSeriesTx is the transactional body of AddWorkToSeries.
func addWorkToSeriesTx(ctx context.Context, tx *sql.Tx, workID, seriesID int64) error {
	// L6: take the writer lock before any read in this transaction.
	if _, err := tx.ExecContext(ctx, `UPDATE work_series SET id=id WHERE 0`); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM works WHERE id=?)`, workID).Scan(&exists); err != nil {
		return err
	} else if !exists {
		return sql.ErrNoRows
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_series WHERE id=?)`, seriesID).Scan(&exists); err != nil {
		return err
	} else if !exists {
		return sql.ErrNoRows
	}
	var previous int64
	previousErr := tx.QueryRowContext(ctx, `SELECT series_id FROM work_series_members WHERE work_id=?`, workID).Scan(&previous)
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		return previousErr
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_series_locks WHERE work_id=?`, workID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_series_members WHERE work_id=?`, workID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_series_members(work_id,series_id,source) VALUES(?,?,'manual')`, workID, seriesID); err != nil {
		return err
	}
	if err := deleteSameSeriesSuggestionsTx(ctx, tx); err != nil {
		return err
	}
	if previousErr == nil && previous != seriesID {
		deleted, delErr := deleteDegenerateSeries(ctx, tx, previous)
		if delErr != nil {
			return delErr
		}
		if !deleted {
			if err := refreshSeriesRow(ctx, tx, previous); err != nil {
				return err
			}
		}
	}
	return refreshSeriesRow(ctx, tx, seriesID)
}

// CreateWorkSeries creates a series from user-chosen works (D61). Members are
// manual and their locks are cleared; a work coming from another series moves
// out and the old series is cleaned up or refreshed. An empty title keeps
// title_source='auto', following the representative work.
func (s *Store) CreateWorkSeries(ctx context.Context, title string, workIDs []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := createWorkSeriesTx(ctx, tx, title, workIDs)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func createWorkSeriesTx(ctx context.Context, tx *sql.Tx, title string, workIDs []int64) (int64, error) {
	title = strings.TrimSpace(title)
	seen := map[int64]bool{}
	var unique []int64
	for _, id := range workIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return 0, fmt.Errorf("%w: a series needs at least one work", ErrInvalidWork)
	}
	// L6: take the writer lock before any read in this transaction.
	if _, err := tx.ExecContext(ctx, `UPDATE work_series SET id=id WHERE 0`); err != nil {
		return 0, err
	}
	for _, id := range unique {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM works WHERE id=?)`, id).Scan(&exists); err != nil {
			return 0, err
		} else if !exists {
			return 0, sql.ErrNoRows
		}
	}
	titleSource := "auto"
	if title != "" {
		titleSource = "manual"
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO work_series(title,title_source) VALUES(?,?)`, title, titleSource)
	if err != nil {
		return 0, err
	}
	seriesID, _ := result.LastInsertId()
	previousSeries := map[int64]bool{}
	for _, workID := range unique {
		var previous int64
		previousErr := tx.QueryRowContext(ctx, `SELECT series_id FROM work_series_members WHERE work_id=?`, workID).Scan(&previous)
		if previousErr == nil {
			previousSeries[previous] = true
		} else if !errors.Is(previousErr, sql.ErrNoRows) {
			return 0, previousErr
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_locks WHERE work_id=?`, workID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_members WHERE work_id=?`, workID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO work_series_members(work_id,series_id,source) VALUES(?,?,'manual')`, workID, seriesID); err != nil {
			return 0, err
		}
	}
	if err = deleteSameSeriesSuggestionsTx(ctx, tx); err != nil {
		return 0, err
	}
	for previous := range previousSeries {
		deleted, delErr := deleteDegenerateSeries(ctx, tx, previous)
		if delErr != nil {
			return 0, delErr
		}
		if !deleted {
			if err = refreshSeriesRow(ctx, tx, previous); err != nil {
				return 0, err
			}
		}
	}
	if err = refreshSeriesRow(ctx, tx, seriesID); err != nil {
		return 0, err
	}
	return seriesID, nil
}

// MergeWorkSeries merges dropID into keepID (D57): every member of the
// absorbed series moves over as a manual member with its lock cleared, the
// absorbed series is deleted, and the keep side's automatic members stay
// automatic. The title follows D58: an explicit title wins; a single manual
// title is kept; two manual titles conflict (ErrSeriesTitleConflict); two
// automatic titles keep the larger series (the smaller id on ties) and stay
// automatic. It returns the id of the series that survived (which may be
// dropID after the automatic swap).
func (s *Store) MergeWorkSeries(ctx context.Context, keepID, dropID int64, title string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	keptID, err := mergeWorkSeriesTx(ctx, tx, keepID, dropID, title)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return keptID, nil
}

func mergeWorkSeriesTx(ctx context.Context, tx *sql.Tx, keepID, dropID int64, title string) (int64, error) {
	if keepID == dropID {
		return 0, fmt.Errorf("%w: cannot merge a series into itself", ErrInvalidWork)
	}
	// L6: take the writer lock before any read in this transaction.
	if _, err := tx.ExecContext(ctx, `UPDATE work_series SET id=id WHERE id IN (?,?)`, keepID, dropID); err != nil {
		return 0, err
	}
	load := func(id int64) (seriesRow, int, error) {
		var row seriesRow
		var count int
		err := tx.QueryRowContext(ctx, `SELECT id,title,title_source,COALESCE(representative_work_id,0),(SELECT COUNT(*) FROM work_series_members m WHERE m.series_id=work_series.id) FROM work_series WHERE id=?`, id).Scan(&row.id, &row.title, &row.titleSource, &row.repWorkID, &count)
		return row, count, err
	}
	keep, keepCount, err := load(keepID)
	if err != nil {
		return 0, err
	}
	drop, dropCount, err := load(dropID)
	if err != nil {
		return 0, err
	}
	title = strings.TrimSpace(title)
	if title == "" && keep.titleSource == "manual" && drop.titleSource == "manual" {
		return 0, ErrSeriesTitleConflict
	}
	if title == "" && keep.titleSource == "auto" && drop.titleSource == "auto" {
		// Both automatic: the larger series survives (smaller id on a tie).
		if dropCount > keepCount || dropCount == keepCount && drop.id < keep.id {
			keep, drop = drop, keep
		}
	}
	var moved []int64
	movedRows, err := tx.QueryContext(ctx, `SELECT work_id FROM work_series_members WHERE series_id=? ORDER BY work_id`, drop.id)
	if err != nil {
		return 0, err
	}
	for movedRows.Next() {
		var workID int64
		if err = movedRows.Scan(&workID); err != nil {
			movedRows.Close()
			return 0, err
		}
		moved = append(moved, workID)
	}
	if err = movedRows.Err(); err != nil {
		movedRows.Close()
		return 0, err
	}
	movedRows.Close()
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_series_locks WHERE work_id IN (SELECT work_id FROM work_series_members WHERE series_id=?)`, drop.id); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_series_members SET series_id=?,source='manual' WHERE series_id=?`, keep.id, drop.id); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_series WHERE id=?`, drop.id); err != nil {
		return 0, err
	}
	// L7: pairs that ended up in the same series through the merge are no
	// longer suggestions.
	if err = deleteSameSeriesSuggestionsTx(ctx, tx); err != nil {
		return 0, err
	}
	switch {
	case title != "":
		if _, err = tx.ExecContext(ctx, `UPDATE work_series SET title=?,title_source='manual',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, title, keep.id); err != nil {
			return 0, err
		}
	case keep.titleSource == "auto" && drop.titleSource == "manual":
		if _, err = tx.ExecContext(ctx, `UPDATE work_series SET title=?,title_source='manual',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, drop.title, keep.id); err != nil {
			return 0, err
		}
	}
	if err = refreshSeriesRow(ctx, tx, keep.id); err != nil {
		return 0, err
	}
	for _, workID := range moved {
		if err = putProvenance(ctx, tx, "work", workID, "series", "manual", "", 0, map[string]int64{"series": keep.id, "mergedFrom": drop.id}); err != nil {
			return 0, err
		}
	}
	return keep.id, nil
}

// ListSeriesPage lists series for the management page with member counts per
// work type. q matches the series title or any member's titles. The total is
// the filtered series count.
func (s *Store) ListSeriesPage(ctx context.Context, q string, limit, offset int) ([]SeriesPageRow, int, error) {
	limit, offset = clampSeriesPage(limit, offset)
	q = strings.TrimSpace(q)
	like := "%" + q + "%"
	where := ` WHERE (?='' OR s.title LIKE ? OR EXISTS(SELECT 1 FROM work_series_members m JOIN works w ON w.id=m.work_id WHERE m.series_id=s.id AND (w.title LIKE ? OR COALESCE(w.reading_title,'') LIKE ? OR COALESCE(w.translated_title,'') LIKE ?)))`
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_series s`+where, q, like, like, like, like).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.title,s.title_source,COALESCE(s.representative_work_id,0),(SELECT COUNT(*) FROM work_series_members m WHERE m.series_id=s.id),s.created_at,s.updated_at FROM work_series s`+where+` ORDER BY s.title COLLATE NOCASE,s.id LIMIT ? OFFSET ?`, q, like, like, like, like, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	var values []SeriesPageRow
	var ids []int64
	for rows.Next() {
		var value SeriesPageRow
		if err = rows.Scan(&value.Series.ID, &value.Series.Title, &value.Series.TitleSource, &value.Series.RepresentativeWorkID, &value.Series.MemberCount, &value.Series.CreatedAt, &value.Series.UpdatedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		value.TypeCounts = map[string]int{}
		ids = append(ids, value.Series.ID)
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	if len(ids) == 0 {
		return values, total, nil
	}
	placeholders, args := int64Placeholders(ids)
	typeRows, err := s.db.QueryContext(ctx, `SELECT m.series_id,w.type,COUNT(*) FROM work_series_members m JOIN works w ON w.id=m.work_id WHERE m.series_id IN (`+placeholders+`) GROUP BY m.series_id,w.type`, args...)
	if err != nil {
		return nil, 0, err
	}
	counts := map[int64]map[string]int{}
	for typeRows.Next() {
		var seriesID int64
		var workType string
		var count int
		if err = typeRows.Scan(&seriesID, &workType, &count); err != nil {
			typeRows.Close()
			return nil, 0, err
		}
		if counts[seriesID] == nil {
			counts[seriesID] = map[string]int{}
		}
		counts[seriesID][workType] = count
	}
	if err = typeRows.Err(); err != nil {
		typeRows.Close()
		return nil, 0, err
	}
	typeRows.Close()
	for i := range values {
		values[i].TypeCounts = counts[values[i].Series.ID]
	}
	return values, total, nil
}
