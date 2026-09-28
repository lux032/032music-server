package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	target, err := loadAlbumRuleSource(ctx, tx, targetID)
	if err != nil {
		return 0, err
	}
	var targetHasArtwork bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artworks WHERE album_id=?)`, targetID).Scan(&targetHasArtwork); err != nil {
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,season,source,inferred_key) SELECT ?,work_id,role,season,source,inferred_key FROM album_works WHERE album_id=? AND source IN ('manual','bangumi') ON CONFLICT(album_id,work_id) DO UPDATE SET source=excluded.source,role=excluded.role,season=excluded.season,inferred_key=excluded.inferred_key,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE CASE album_works.source WHEN 'manual' THEN 3 WHEN 'bangumi' THEN 2 ELSE 1 END < CASE excluded.source WHEN 'manual' THEN 3 WHEN 'bangumi' THEN 2 ELSE 1 END`, targetID, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_work_suppressions(album_id,inferred_key) SELECT ?,inferred_key FROM album_work_suppressions WHERE album_id=?`, targetID, id); err != nil {
			return 0, err
		}
		// B3: preserve reviewed status on collisions; pending candidates may move.
		if _, err = tx.ExecContext(ctx, `INSERT INTO album_subject_candidates(album_id,source,external_id,title,release_date,artist,score,evidence_json,tieups_json,payload_json,status,created_at,updated_at) SELECT ?,source,external_id,title,release_date,artist,score,evidence_json,tieups_json,payload_json,status,created_at,updated_at FROM album_subject_candidates WHERE album_id=? ON CONFLICT(album_id,source,external_id) DO UPDATE SET status=excluded.status WHERE album_subject_candidates.status='candidate' AND excluded.status IN ('confirmed','rejected')`, targetID, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tracks SET album_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE album_id=?`, targetID, id); err != nil {
			return 0, err
		}
		if !targetHasArtwork {
			if _, err = tx.ExecContext(ctx, `UPDATE artworks SET album_id=? WHERE album_id=? AND track_id IS NULL`, targetID, id); err != nil {
				return 0, err
			}
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artworks WHERE album_id=?)`, targetID).Scan(&targetHasArtwork); err != nil {
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
