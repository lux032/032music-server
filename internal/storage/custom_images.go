package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CustomImageInput describes one validated user-uploaded image already
// stored on disk under the data directory (see the HTTP layer). Hash is the
// sha256 hex of the file content and doubles as the cache-busting version.
// FileName is the bare "<hash>.<ext>" name under <data>/custom-images/ —
// never an absolute path, so the database survives the data directory
// moving; readers resolve it against the configured directory.
type CustomImageInput struct {
	Hash     string
	MIMEType string
	FileName string
	Width    int
	Height   int
	ByteSize int64
}

// AlbumExists reports whether the album id is present.
func (s *Store) AlbumExists(ctx context.Context, albumID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM albums WHERE id=?)`, albumID).Scan(&exists)
	return exists, err
}

// ArtistExists reports whether the artist id is present.
func (s *Store) ArtistExists(ctx context.Context, artistID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=?)`, artistID).Scan(&exists)
	return exists, err
}

// ErrCustomImageTarget reports an upload/reset against a missing album or
// artist.
var ErrCustomImageTarget = errors.New("custom image target not found")

// SaveCustomAlbumArtwork replaces the album's custom cover (at most one
// custom row per album) with a new artwork row. Every upload produces a new
// artwork id so the /api/v1/artwork/{id} URL changes and caches invalidate
// naturally; albums.updated_at is bumped so Sync clients re-pull. Returns the
// new artwork id and the file paths of any replaced custom rows (already
// unreferenced; the caller may delete them after the commit).
func (s *Store) SaveCustomAlbumArtwork(ctx context.Context, albumID int64, img CustomImageInput) (int64, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	// L1: take the writer lock before any read in this transaction.
	if _, err = tx.ExecContext(ctx, `UPDATE artworks SET id=id WHERE 0`); err != nil {
		return 0, nil, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM albums WHERE id=?)`, albumID).Scan(&exists); err != nil {
		return 0, nil, err
	}
	if !exists {
		return 0, nil, fmt.Errorf("%w: album %d", ErrCustomImageTarget, albumID)
	}
	oldPaths, err := customArtworkPaths(ctx, tx, albumID)
	if err != nil {
		return 0, nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artworks WHERE album_id=? AND source_type='custom'`, albumID); err != nil {
		return 0, nil, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO artworks(album_id,track_id,source_type,source_path,content_hash,mime_type,width,height,byte_size,is_primary) VALUES(?,NULL,'custom',?,?,?,?,?,?,0)`, albumID, img.FileName, img.Hash, img.MIMEType, img.Width, img.Height, img.ByteSize)
	if err != nil {
		return 0, nil, err
	}
	artworkID, err := result.LastInsertId()
	if err != nil {
		return 0, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE albums SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, albumID); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(); err != nil {
		return 0, nil, err
	}
	return artworkID, oldPaths, nil
}

// ResetCustomAlbumArtwork removes the album's custom cover rows, returning
// their file paths (unreferenced after the commit). albums.updated_at is
// bumped even when there was nothing to remove only if a row was deleted.
func (s *Store) ResetCustomAlbumArtwork(ctx context.Context, albumID int64) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// L1: take the writer lock before any read in this transaction.
	if _, err = tx.ExecContext(ctx, `UPDATE artworks SET id=id WHERE 0`); err != nil {
		return nil, err
	}
	oldPaths, err := customArtworkPaths(ctx, tx, albumID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artworks WHERE album_id=? AND source_type='custom'`, albumID); err != nil {
		return nil, err
	}
	if len(oldPaths) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE albums SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, albumID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return oldPaths, nil
}

func customArtworkPaths(ctx context.Context, tx *sql.Tx, albumID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(source_path,'') FROM artworks WHERE album_id=? AND source_type='custom'`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, rows.Err()
}

// SaveCustomArtistImage upserts the artist's custom image. Custom images live
// in artist_custom_images, never in artist_image_cache, so automatic Last.fm/
// MusicBrainz refreshes (SaveArtistImage) cannot overwrite them. Returns the
// replaced file path, if any (unreferenced after the commit).
func (s *Store) SaveCustomArtistImage(ctx context.Context, artistID int64, img CustomImageInput) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// L1: take the writer lock before any read in this transaction.
	if _, err = tx.ExecContext(ctx, `UPDATE artist_custom_images SET artist_id=artist_id WHERE 0`); err != nil {
		return "", err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=?)`, artistID).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("%w: artist %d", ErrCustomImageTarget, artistID)
	}
	var oldPath string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT file_path FROM artist_custom_images WHERE artist_id=?),'')`, artistID).Scan(&oldPath); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artist_custom_images(artist_id,content_hash,mime_type,file_path,width,height,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET content_hash=excluded.content_hash,mime_type=excluded.mime_type,file_path=excluded.file_path,width=excluded.width,height=excluded.height,byte_size=excluded.byte_size,created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, artistID, img.Hash, img.MIMEType, img.FileName, img.Width, img.Height, img.ByteSize); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artists SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, artistID); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return oldPath, nil
}

// ResetCustomArtistImage (D50) deletes the artist's own custom image row and
// every custom row inherited from merged-in source artists (a merge means
// "same person"): after the reset the artist falls back to the automatic
// cache image. Rolling the merge back afterwards does not bring the source
// rows back — that is accepted, documented behaviour. Returns the removed
// file names (unreferenced after the commit).
func (s *Store) ResetCustomArtistImage(ctx context.Context, artistID int64) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// L1: take the writer lock before any read in this transaction.
	if _, err = tx.ExecContext(ctx, `UPDATE artist_custom_images SET artist_id=artist_id WHERE 0`); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT file_path FROM artist_custom_images WHERE artist_id=? OR artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?)`, artistID, artistID)
	if err != nil {
		return nil, err
	}
	var oldNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		if name != "" {
			oldNames = append(oldNames, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(oldNames) > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM artist_custom_images WHERE artist_id=? OR artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?)`, artistID, artistID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? OR merged_into_artist_id=?`, artistID, artistID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return oldNames, nil
}

// CustomImageFiles returns every custom-image file name still referenced by
// an artworks row (source_type='custom') or an artist_custom_images row. The
// HTTP layer garbage-collects files under custom-images/ whose names are
// absent from this set.
func (s *Store) CustomImageFiles(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_path FROM artworks WHERE source_type='custom' AND source_path IS NOT NULL UNION SELECT file_path FROM artist_custom_images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := map[string]bool{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if path != "" {
			paths[path] = true
		}
	}
	return paths, rows.Err()
}
