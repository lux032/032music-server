package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lux032/032music-server/internal/metadata"
)

// ErrIdentityNotReady means a manual full identity rebuild has not completed.
var ErrIdentityNotReady = errors.New("release group identity not ready")

func (s *Store) requireIdentityReady(ctx context.Context) error {
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM release_group_rebuild WHERE id=1`).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIdentityNotReady
		}
		return err
	}
	if status != "ready" {
		return ErrIdentityNotReady
	}
	return nil
}

func resolveAlbumReleaseGroups(ctx context.Context, tx *sql.Tx, albumID int64) error {
	// Demote first to respect the partial unique index, including manual overrides.
	if _, err := tx.ExecContext(ctx, `UPDATE album_release_groups SET status='observed' WHERE album_id=?`, albumID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE album_release_groups SET status=CASE WHEN
 (SELECT COUNT(*) FROM album_release_groups x WHERE x.album_id=? AND x.source=album_release_groups.source)=1 THEN 'resolved' ELSE 'conflict' END
 WHERE album_id=? AND source=CASE WHEN EXISTS(SELECT 1 FROM album_release_groups m WHERE m.album_id=? AND m.source='manual') THEN 'manual' ELSE 'file' END`, albumID, albumID, albumID)
	return err
}

func refreshAlbumFileReleaseGroups(ctx context.Context, tx *sql.Tx, albumID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM album_release_groups WHERE album_id=? AND source='file'`, albumID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO album_release_groups(album_id,rgid,source,status) SELECT DISTINCT ?,g.rgid,'file','observed' FROM audio_file_release_groups g JOIN audio_files af ON af.id=g.audio_file_id JOIN tracks t ON t.id=af.track_id WHERE t.album_id=? AND af.status='available'`, albumID, albumID); err != nil {
		return err
	}
	return resolveAlbumReleaseGroups(ctx, tx, albumID)
}

func replaceFileReleaseGroups(ctx context.Context, tx *sql.Tx, audioID, albumID int64, raw map[string][]string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM audio_file_release_groups WHERE audio_file_id=?`, audioID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM audio_file_releases WHERE audio_file_id=?`, audioID); err != nil {
		return err
	}
	groups, releases := metadata.MusicBrainzFileIDs(raw)
	for _, id := range groups {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO release_groups(rgid) VALUES(?)`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audio_file_release_groups(audio_file_id,rgid) VALUES(?,?)`, audioID, id); err != nil {
			return err
		}
	}
	for _, id := range releases {
		if _, err := tx.ExecContext(ctx, `INSERT INTO audio_file_releases(audio_file_id,release_id) VALUES(?,?)`, audioID, id); err != nil {
			return err
		}
	}
	return refreshAlbumFileReleaseGroups(ctx, tx, albumID)
}

// AlbumReleaseGroup returns ErrIdentityNotReady before a successful rebuild,
// and sql.ErrNoRows for unknown or conflicting identity once ready.
func (s *Store) AlbumReleaseGroup(ctx context.Context, albumID int64) (string, error) {
	if err := s.requireIdentityReady(ctx); err != nil {
		return "", err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT rgid FROM album_release_groups WHERE album_id=? AND status='resolved'`, albumID).Scan(&id)
	return id, err
}
func (s *Store) ResolvedAlbumsByReleaseGroup(ctx context.Context, rgid string) ([]int64, error) {
	if err := s.requireIdentityReady(ctx); err != nil {
		return nil, err
	}
	id, ok := metadata.NormalizeMusicBrainzID(rgid)
	if !ok {
		return nil, fmt.Errorf("invalid release group ID")
	}
	rgid = id

	rows, err := s.db.QueryContext(ctx, `SELECT album_id FROM album_release_groups WHERE rgid=? AND status='resolved' ORDER BY album_id`, rgid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReleaseGroupEvidence retains the file ID for future evidence_audio_file_id.
// CreatedAt is the SQLite insertion clock, never album added_at or an ID watermark.
type ReleaseGroupEvidence struct {
	AudioFileID, AlbumID int64
	CreatedAt            string
}

func (s *Store) ReleaseGroupFileEvidence(ctx context.Context, rgid, createdAfter string) ([]ReleaseGroupEvidence, error) {
	if err := s.requireIdentityReady(ctx); err != nil {
		return nil, err
	}
	id, ok := metadata.NormalizeMusicBrainzID(rgid)
	if !ok {
		return nil, fmt.Errorf("invalid release group ID")
	}
	rgid = id

	rows, err := s.db.QueryContext(ctx, `SELECT af.id,t.album_id,af.created_at FROM audio_file_release_groups g JOIN audio_files af ON af.id=g.audio_file_id JOIN tracks t ON t.id=af.track_id JOIN album_release_groups a ON a.album_id=t.album_id AND a.rgid=g.rgid AND a.status='resolved' WHERE g.rgid=? AND af.status='available' AND af.created_at>? AND (SELECT COUNT(*) FROM audio_file_release_groups x WHERE x.audio_file_id=af.id)=1 ORDER BY af.created_at,af.id`, rgid, createdAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleaseGroupEvidence
	for rows.Next() {
		var e ReleaseGroupEvidence
		if err = rows.Scan(&e.AudioFileID, &e.AlbumID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AssociateAlbumReleaseGroup requires a real local album (F2). Calling it replaces
// that album's manual choice; merges retain conflicting manual choices instead.
func (s *Store) AssociateAlbumReleaseGroup(ctx context.Context, albumID int64, rgid string) error {
	id, ok := metadata.NormalizeMusicBrainzID(rgid)
	if !ok {
		return fmt.Errorf("invalid release group ID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = associateAlbumReleaseGroupTx(ctx, tx, albumID, id); err != nil {
		return err
	}
	return tx.Commit()
}

// associateAlbumReleaseGroupTx preserves the manual-choice semantics for callers
// that need the association and another CAS write to commit atomically.
func associateAlbumReleaseGroupTx(ctx context.Context, tx *sql.Tx, albumID int64, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM album_release_groups WHERE album_id=? AND source='manual'`, albumID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO release_groups(rgid) VALUES(?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO album_release_groups(album_id,rgid,source,status) VALUES(?,?,'manual','observed')`, albumID, id); err != nil {
		return err
	}
	return resolveAlbumReleaseGroups(ctx, tx, albumID)
}

func (s *Store) SetReleaseGroupIgnored(ctx context.Context, rgid string, ignored bool) error {
	id, ok := metadata.NormalizeMusicBrainzID(rgid)
	if !ok {
		return fmt.Errorf("invalid release group ID")
	}
	if !ignored {
		_, err := s.db.ExecContext(ctx, `DELETE FROM release_group_decisions WHERE rgid=?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO release_group_decisions(rgid,decision) VALUES(?,'ignore')`, id)
	return err
}
func (s *Store) ReleaseGroupIgnored(ctx context.Context, rgid string) (bool, error) {
	id, ok := metadata.NormalizeMusicBrainzID(rgid)
	if !ok {
		return false, fmt.Errorf("invalid release group ID")
	}
	rgid = id

	var ignored bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM release_group_decisions WHERE rgid=?)`, rgid).Scan(&ignored)
	return ignored, err
}

// AlbumImportRule diagnoses grouping-key suppression for a later ATTENTION UI.
func (s *Store) AlbumImportRule(ctx context.Context, libraryID int64, groupingKey string) (action string, targetAlbumID int64, err error) {
	var target sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT action,target_album_id FROM album_key_rules WHERE library_id=? AND grouping_key=?`, libraryID, groupingKey).Scan(&action, &target)
	return action, target.Int64, err
}

func (s *Store) RefreshReleaseGroupIdentities(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE album_release_groups SET status='observed' WHERE source='file'`); err != nil {
		return err
	}
	if err = refreshAllAlbumReleaseGroups(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func refreshAllAlbumReleaseGroups(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM albums`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = refreshAlbumFileReleaseGroups(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
