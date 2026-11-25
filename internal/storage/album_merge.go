package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
)

// ErrAlbumSelection reports an invalid album merge/delete selection.
var ErrAlbumSelection = errors.New("invalid album selection")

// albumKeyRule is the rescan rule for one scanner grouping key.
type albumKeyRule struct {
	action string
	target int64
}

func lookupAlbumKeyRule(ctx context.Context, tx *sql.Tx, libraryID int64, groupKey string) (albumKeyRule, bool, error) {
	var rule albumKeyRule
	var target sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT action,target_album_id FROM album_key_rules WHERE library_id=? AND grouping_key=?`, libraryID, groupKey).Scan(&rule.action, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return rule, false, nil
	}
	if err != nil {
		return rule, false, err
	}
	rule.target = target.Int64
	return rule, true, nil
}

type albumRuleSource struct {
	id        int64
	libraryID int64
	key       string
	title     string
	favorite  int
}

func loadAlbumRuleSource(ctx context.Context, tx *sql.Tx, id int64) (albumRuleSource, error) {
	v := albumRuleSource{id: id}
	err := tx.QueryRowContext(ctx, `SELECT library_id,grouping_key,COALESCE(user_title,title),is_favorite FROM albums WHERE id=?`, id).Scan(&v.libraryID, &v.key, &v.title, &v.favorite)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("%w: album %d not found", ErrAlbumSelection, id)
	}
	return v, err
}

func distinctAlbumIDs(ids []int64) ([]int64, error) {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: invalid album id %d", ErrAlbumSelection, id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// MergeAlbums moves every track of sourceIDs into targetID and removes the
// source albums. The scanner grouping keys of the sources (and of anything
// previously merged into them) are redirected to the target so a rescan
// keeps the merge. Source favorites and artwork carry over when the target
// has none. Returns the number of merged source albums.
func (s *Store) MergeAlbums(ctx context.Context, targetID int64, sourceIDs []int64) (int, error) {
	sources, err := distinctAlbumIDs(sourceIDs)
	if err != nil {
		return 0, err
	}
	if targetID <= 0 || len(sources) == 0 {
		return 0, fmt.Errorf("%w: need a target and at least one source album", ErrAlbumSelection)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// L1: take the writer lock before any read in this transaction.
	if _, err = tx.ExecContext(ctx, `UPDATE albums SET id=id WHERE 0`); err != nil {
		return 0, err
	}
	target, err := loadAlbumRuleSource(ctx, tx, targetID)
	if err != nil {
		return 0, err
	}
	var targetHasNonCustom, targetHasCustom bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artworks WHERE album_id=? AND source_type<>'custom'),EXISTS(SELECT 1 FROM artworks WHERE album_id=? AND source_type='custom')`, targetID, targetID).Scan(&targetHasNonCustom, &targetHasCustom); err != nil {
		return 0, err
	}
	for _, id := range sources {
		if id == targetID {
			return 0, fmt.Errorf("%w: target album cannot also be a source", ErrAlbumSelection)
		}
		source, loadErr := loadAlbumRuleSource(ctx, tx, id)
		if loadErr != nil {
			return 0, loadErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_release_groups(album_id,rgid,source,status) SELECT ?,rgid,source,'observed' FROM album_release_groups WHERE album_id=?`, targetID, id); err != nil {
			return 0, err
		}

		if _, err = tx.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,season,source,inferred_key) SELECT ?,work_id,role,season,source,inferred_key FROM album_works WHERE album_id=? AND source IN ('manual','bangumi') ON CONFLICT(album_id,work_id) DO UPDATE SET source=excluded.source,role=excluded.role,season=excluded.season,inferred_key=excluded.inferred_key,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE CASE album_works.source WHEN 'manual' THEN 3 WHEN 'bangumi' THEN 2 ELSE 1 END < CASE excluded.source WHEN 'manual' THEN 3 WHEN 'bangumi' THEN 2 ELSE 1 END`, targetID, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_work_suppressions(album_id,inferred_key) SELECT ?,inferred_key FROM album_work_suppressions WHERE album_id=?`, targetID, id); err != nil {
			return 0, err
		}
		// B3: preserve reviewed status on collisions; pending candidates may move.
		// L3: a pending candidate is dropped (not moved) when the target already
		// has a confirmed Bangumi candidate or a manual/bangumi album-work
		// association — the association decision is already made for the merged
		// album, and moving the pending row could yield two confirmed
		// candidates. Reviewed rows always move so their status is preserved.
		if _, err = tx.ExecContext(ctx, `INSERT INTO album_subject_candidates(album_id,source,external_id,title,release_date,artist,score,evidence_json,tieups_json,payload_json,status,created_at,updated_at) SELECT ?,source,external_id,title,release_date,artist,score,evidence_json,tieups_json,payload_json,status,created_at,updated_at FROM album_subject_candidates WHERE album_id=? AND (status<>'candidate' OR (NOT EXISTS(SELECT 1 FROM album_subject_candidates done WHERE done.album_id=? AND done.status='confirmed') AND NOT EXISTS(SELECT 1 FROM album_works linked WHERE linked.album_id=? AND linked.source IN ('manual','bangumi')))) ON CONFLICT(album_id,source,external_id) DO UPDATE SET status=excluded.status WHERE album_subject_candidates.status='candidate' AND excluded.status IN ('confirmed','rejected')`, targetID, id, targetID, targetID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tracks SET album_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE album_id=?`, targetID, id); err != nil {
			return 0, err
		}
		// Custom covers (4.5.7): the target's own custom cover always stays;
		// when it has none, the first source's custom cover transfers. Custom
		// rows of later sources are cascade-deleted with their albums and
		// their files garbage-collected.
		if !targetHasCustom {
			if _, err = tx.ExecContext(ctx, `UPDATE artworks SET album_id=? WHERE album_id=? AND track_id IS NULL AND source_type='custom'`, targetID, id); err != nil {
				return 0, err
			}
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artworks WHERE album_id=? AND source_type='custom')`, targetID).Scan(&targetHasCustom); err != nil {
				return 0, err
			}
		}
		// Non-custom artwork keeps the historical rule, but the gate is
		// non-custom artwork only: a transferred custom cover must not block
		// the source's plain covers, otherwise "恢复默认" would leave the
		// target with no cover at all (L-c). Custom rows are handled above
		// so a later source's custom cover never slips in through this move.
		if !targetHasNonCustom {
			if _, err = tx.ExecContext(ctx, `UPDATE artworks SET album_id=? WHERE album_id=? AND track_id IS NULL AND source_type<>'custom'`, targetID, id); err != nil {
				return 0, err
			}
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artworks WHERE album_id=? AND source_type<>'custom')`, targetID).Scan(&targetHasNonCustom); err != nil {
				return 0, err
			}
		}
		if source.favorite == 1 && target.favorite == 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE albums SET is_favorite=1 WHERE id=?`, targetID); err != nil {
				return 0, err
			}
			target.favorite = 1
		}
		// Keys already redirected to the source now follow it into the target.
		if _, err = tx.ExecContext(ctx, `UPDATE album_key_rules SET target_album_id=? WHERE target_album_id=?`, targetID, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO album_key_rules(library_id,grouping_key,action,target_album_id,source_title) VALUES(?,?,'merge',?,?) ON CONFLICT(library_id,grouping_key) DO UPDATE SET action='merge',target_album_id=excluded.target_album_id,source_title=excluded.source_title,created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, source.libraryID, source.key, targetID, source.title); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM albums WHERE id=?`, id); err != nil {
			return 0, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE albums SET work_fingerprint=NULL,disc_count=MAX(disc_count,COALESCE((SELECT MAX(disc_number) FROM tracks WHERE album_id=?),1)),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, targetID, targetID); err != nil {
		return 0, err
	}
	// L3/Low-1：收尾统一清理——只要合并后的目标已有 confirmed Bangumi 候选
	// 或 manual/bangumi 专辑关联，就删掉目标上所有待审候选，让最终结果不
	// 依赖源专辑的合并顺序（待审先于 confirmed 搬入的情况也被收回）。
	if _, err = tx.ExecContext(ctx, `DELETE FROM album_subject_candidates WHERE album_id=? AND status='candidate' AND (EXISTS(SELECT 1 FROM album_subject_candidates done WHERE done.album_id=? AND done.status='confirmed') OR EXISTS(SELECT 1 FROM album_works linked WHERE linked.album_id=? AND linked.source IN ('manual','bangumi')))`, targetID, targetID, targetID); err != nil {
		return 0, err
	}
	if err = resolveAlbumReleaseGroups(ctx, tx, targetID); err != nil {
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(sources), nil
}

// DeleteAlbums removes albums and their tracks from the library. Audio files
// on disk are untouched; their grouping keys are recorded so a rescan does
// not import them again. Returns the number of deleted albums.
func (s *Store) DeleteAlbums(ctx context.Context, ids []int64) (int, error) {
	albums, err := distinctAlbumIDs(ids)
	if err != nil {
		return 0, err
	}
	if len(albums) == 0 {
		return 0, fmt.Errorf("%w: no album selected", ErrAlbumSelection)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// L1: take the writer lock before any read in this transaction.
	if _, err = tx.ExecContext(ctx, `UPDATE albums SET id=id WHERE 0`); err != nil {
		return 0, err
	}
	for _, id := range albums {
		album, loadErr := loadAlbumRuleSource(ctx, tx, id)
		if loadErr != nil {
			return 0, loadErr
		}
		// Keys merged into this album belong to the deleted content as well.
		if _, err = tx.ExecContext(ctx, `UPDATE album_key_rules SET action='delete',target_album_id=NULL WHERE target_album_id=?`, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO album_key_rules(library_id,grouping_key,action,source_title) VALUES(?,?,'delete',?) ON CONFLICT(library_id,grouping_key) DO UPDATE SET action='delete',target_album_id=NULL,source_title=excluded.source_title,created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, album.libraryID, album.key, album.title); err != nil {
			return 0, err
		}
		// Dropping the audio_files rows keeps the files from being reported
		// as 'missing' and forces the scanner through ImportTrack, which
		// honours the delete rule.
		if _, err = tx.ExecContext(ctx, `DELETE FROM audio_files WHERE track_id IN (SELECT id FROM tracks WHERE album_id=?)`, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM tracks WHERE album_id=?`, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM albums WHERE id=?`, id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	// Artists/genres left without references disappear right away instead
	// of waiting for the next scan.
	return len(albums), s.CleanupOrphans(ctx)
}

// AlbumKeyRule represents a persisted delete or merge rule in album_key_rules.
type AlbumKeyRule struct {
	LibraryID        int64
	GroupingKey      string
	Action           string
	TargetAlbumID    int64
	TargetAlbumTitle string
	SourceTitle      string
	CreatedAt        string
}

// AlbumKeyRuleKey uniquely identifies one row in album_key_rules.
type AlbumKeyRuleKey struct {
	LibraryID   int64
	GroupingKey string
}

// ListAlbumKeyRules returns all stored delete and merge rules, delete rules
// first and newest first within each action type.
func (s *Store) ListAlbumKeyRules(ctx context.Context) ([]AlbumKeyRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.library_id,r.grouping_key,r.action,COALESCE(r.target_album_id,0),COALESCE(a.user_title,a.title,''),r.source_title,r.created_at FROM album_key_rules r LEFT JOIN albums a ON a.id=r.target_album_id ORDER BY CASE r.action WHEN 'delete' THEN 0 ELSE 1 END,r.created_at DESC,r.grouping_key ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]AlbumKeyRule, 0)
	for rows.Next() {
		var r AlbumKeyRule
		if err = rows.Scan(&r.LibraryID, &r.GroupingKey, &r.Action, &r.TargetAlbumID, &r.TargetAlbumTitle, &r.SourceTitle, &r.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// CountAlbumKeyRules returns the counts of active delete and merge rules.
func (s *Store) CountAlbumKeyRules(ctx context.Context) (deleteCount, mergeCount int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN action='delete' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN action='merge' THEN 1 ELSE 0 END),0) FROM album_key_rules`).Scan(&deleteCount, &mergeCount)
	return deleteCount, mergeCount, err
}

// DeleteAlbumKeyRules removes the specified album_key_rules entries so future
// scans no longer suppress or redirect those grouping keys.
func (s *Store) DeleteAlbumKeyRules(ctx context.Context, keys []AlbumKeyRuleKey) (int64, error) {
	seen := make(map[AlbumKeyRuleKey]struct{}, len(keys))
	unique := make([]AlbumKeyRuleKey, 0, len(keys))
	for _, k := range keys {
		if k.LibraryID <= 0 || k.GroupingKey == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		unique = append(unique, k)
	}
	if len(unique) == 0 {
		return 0, nil
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	for _, k := range unique {
		res, execErr := tx.ExecContext(ctx, `DELETE FROM album_key_rules WHERE library_id=? AND grouping_key=?`, k.LibraryID, k.GroupingKey)
		if execErr != nil {
			return 0, execErr
		}
		n, raErr := res.RowsAffected()
		if raErr != nil {
			return 0, raErr
		}
		total += n
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

// PruneStaleDeleteAlbumRules removes action='delete' rules for libraryID whose
// normalized album directory has zero discovered supported audio files in a
// completed, non-cancelled, S1-validated scan. Rules whose directory still has
// discovered audio files, rules at or beneath any unreadable directory, and
// all merge rules are preserved. Returns the pruned rules without acquiring a
// write lock when nothing is stale.
func (s *Store) PruneStaleDeleteAlbumRules(ctx context.Context, libraryID int64, discoveredAlbumDirs map[string]struct{}, unreadableDirs []string) ([]AlbumKeyRule, error) {
	if libraryID <= 0 || len(discoveredAlbumDirs) == 0 {
		return nil, nil
	}
	normUnreadable := make([]string, 0, len(unreadableDirs))
	for _, u := range unreadableDirs {
		norm := metadata.Normalize(strings.TrimSuffix(strings.TrimSpace(u), "/"))
		if norm == "" || norm == "." {
			// Library root was unreadable; refuse to prune any delete rule.
			return nil, nil
		}
		normUnreadable = append(normUnreadable, norm)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT grouping_key,source_title,created_at FROM album_key_rules WHERE library_id=? AND action='delete'`, libraryID)
	if err != nil {
		return nil, err
	}
	var candidates []AlbumKeyRule
	for rows.Next() {
		r := AlbumKeyRule{LibraryID: libraryID, Action: "delete"}
		if err = rows.Scan(&r.GroupingKey, &r.SourceTitle, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if !deleteRuleDirActive(r.GroupingKey, discoveredAlbumDirs, normUnreadable) {
			candidates = append(candidates, r)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(candidates) == 0 {
		return nil, nil
	}

	tx, err := s.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	pruned := make([]AlbumKeyRule, 0, len(candidates))
	for _, r := range candidates {
		res, execErr := tx.ExecContext(ctx, `DELETE FROM album_key_rules WHERE library_id=? AND action='delete' AND grouping_key=?`, libraryID, r.GroupingKey)
		if execErr != nil {
			return nil, execErr
		}
		if n, raErr := res.RowsAffected(); raErr != nil {
			return nil, raErr
		} else if n > 0 {
			pruned = append(pruned, r)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return pruned, nil
}

func deleteRuleDirActive(groupingKey string, discoveredAlbumDirs map[string]struct{}, unreadableDirs []string) bool {
	ruleDir, _, hasSep := strings.Cut(groupingKey, "|")
	if !hasSep {
		return true
	}
	if _, ok := discoveredAlbumDirs[ruleDir]; ok {
		return true
	}
	extraPipes := strings.Count(groupingKey, "|") > 2
	if extraPipes {
		for dir := range discoveredAlbumDirs {
			if strings.HasPrefix(groupingKey, dir+"|") {
				return true
			}
		}
	}
	for _, u := range unreadableDirs {
		if ruleDir == u || strings.HasPrefix(ruleDir, u+"/") {
			return true
		}
		if extraPipes && (strings.HasPrefix(groupingKey, u+"|") || strings.HasPrefix(groupingKey, u+"/")) {
			return true
		}
	}
	return false
}
