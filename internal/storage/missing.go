package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// MissingFile 是被扫描标记为缺失（status='missing'）的音频文件，供管理页
// “缺失文件”列表展示。TrackHidden 表示该歌曲已没有任何可用文件，正对客户端
// 隐藏；为 false 时歌曲仍有其他可用文件，清理只会删除这条文件记录。
type MissingFile struct {
	ID           int64
	TrackID      int64
	AlbumID      int64
	RelativePath string
	TrackTitle   string
	AlbumTitle   string
	Artist       string
	MissingSince string
	TrackHidden  bool
}

// PurgeResult 汇总一次缺失清理删除的记录数。
type PurgeResult struct {
	Files   int64
	Tracks  int64
	Albums  int64
	Artists int64
}

// ListMissingFiles 分页列出缺失文件，最近缺失的在前。
func (s *Store) ListMissingFiles(ctx context.Context, limit, offset int) ([]MissingFile, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_files WHERE status='missing'`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT af.id,COALESCE(af.track_id,0),COALESCE(t.album_id,0),af.relative_path,
		COALESCE(t.user_title,t.title,''),COALESCE(a.user_title,a.title,''),
		CASE WHEN t.id IS NULL THEN '' ELSE `+trackArtistSQL+` END,
		af.updated_at,
		CASE WHEN t.id IS NULL THEN 1 ELSE NOT `+trackVisibleSQL("t.id")+` END
		FROM audio_files af LEFT JOIN tracks t ON t.id=af.track_id LEFT JOIN albums a ON a.id=t.album_id
		WHERE af.status='missing' ORDER BY af.updated_at DESC,af.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := make([]MissingFile, 0)
	for rows.Next() {
		var v MissingFile
		var hidden int
		if err = rows.Scan(&v.ID, &v.TrackID, &v.AlbumID, &v.RelativePath, &v.TrackTitle, &v.AlbumTitle, &v.Artist, &v.MissingSince, &hidden); err != nil {
			return nil, 0, err
		}
		v.TrackHidden = hidden != 0
		list = append(list, v)
	}
	return list, total, rows.Err()
}

// cleanupOrphansSQL 删除失去全部引用的专辑、歌手与流派（CleanupOrphans 与
// PurgeMissing 共用）。
const cleanupOrphansSQL = `DELETE FROM albums WHERE NOT EXISTS(SELECT 1 FROM tracks t WHERE t.album_id=albums.id); DELETE FROM artists WHERE merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=artists.id) AND NOT EXISTS(SELECT 1 FROM track_artists ta WHERE ta.artist_id=artists.id); DELETE FROM genres WHERE NOT EXISTS(SELECT 1 FROM track_genres tg WHERE tg.genre_id=genres.id) AND NOT EXISTS(SELECT 1 FROM track_genre_overrides tgo WHERE tgo.genre_id=genres.id) AND NOT EXISTS(SELECT 1 FROM album_genre_overrides ago WHERE ago.genre_id=genres.id);`

// purgeChunk 限制单条语句的绑定参数个数。
const purgeChunk = 500

// PurgeMissing 永久删除缺失文件及因此再无任何文件的歌曲（参照 Navidrome 的
// Scanner.PurgeMissing / Missing Files 清理）。
//
//   - libraryID>0 时只处理该曲库的文件；
//   - ids 为空时处理全部缺失文件，否则只处理给定 id 中确实为 'missing' 的
//     文件——可用文件永远不会被删除；
//   - 歌曲仅在其全部音频文件记录都已删除后才删除；仍有其他文件（哪怕也缺
//     失但未选中）的歌曲保留。
//
// 删除歌曲会级联清除其收藏、播放进度、歌单条目、作品与 Bangumi 关联等；受
// 影响歌单的 revision 递增，让客户端的乐观并发检查感知变化。随后在同一事务
// 内清理失去全部引用的专辑、歌手与流派。自定义封面文件由调用方触发的
// custom-images GC 回收。
func (s *Store) PurgeMissing(ctx context.Context, libraryID int64, ids []int64) (PurgeResult, error) {
	var result PurgeResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// L1: take the writer lock before any read in this transaction.
	if _, err = tx.ExecContext(ctx, `UPDATE audio_files SET id=id WHERE 0`); err != nil {
		return result, err
	}

	where := []string{"status='missing'"}
	args := []any{}
	if libraryID > 0 {
		where = append(where, "library_id=?")
		args = append(args, libraryID)
	}
	var fileIDs []int64
	if len(ids) > 0 {
		for start := 0; start < len(ids); start += purgeChunk {
			chunk := ids[start:min(start+purgeChunk, len(ids))]
			placeholders, chunkArgs := inClause(chunk)
			found, e := queryTxIDs(ctx, tx, `SELECT id FROM audio_files WHERE `+strings.Join(where, " AND ")+` AND id IN (`+placeholders+`)`, append(append([]any(nil), args...), chunkArgs...)...)
			if e != nil {
				return result, e
			}
			fileIDs = append(fileIDs, found...)
		}
	} else {
		if fileIDs, err = queryTxIDs(ctx, tx, `SELECT id FROM audio_files WHERE `+strings.Join(where, " AND "), args...); err != nil {
			return result, err
		}
	}
	if len(fileIDs) == 0 {
		return result, nil
	}

	trackSet := map[int64]bool{}
	var trackIDs []int64
	for start := 0; start < len(fileIDs); start += purgeChunk {
		chunk := fileIDs[start:min(start+purgeChunk, len(fileIDs))]
		placeholders, chunkArgs := inClause(chunk)
		owners, e := queryTxIDs(ctx, tx, `SELECT DISTINCT track_id FROM audio_files WHERE track_id IS NOT NULL AND id IN (`+placeholders+`)`, chunkArgs...)
		if e != nil {
			return result, e
		}
		for _, id := range owners {
			if !trackSet[id] {
				trackSet[id] = true
				trackIDs = append(trackIDs, id)
			}
		}
		res, e := tx.ExecContext(ctx, `DELETE FROM audio_files WHERE id IN (`+placeholders+`)`, chunkArgs...)
		if e != nil {
			return result, e
		}
		n, _ := res.RowsAffected()
		result.Files += n
	}

	for start := 0; start < len(trackIDs); start += purgeChunk {
		chunk := trackIDs[start:min(start+purgeChunk, len(trackIDs))]
		placeholders, chunkArgs := inClause(chunk)
		orphaned, e := queryTxIDs(ctx, tx, `SELECT t.id FROM tracks t WHERE t.id IN (`+placeholders+`) AND NOT EXISTS(SELECT 1 FROM audio_files af WHERE af.track_id=t.id)`, chunkArgs...)
		if e != nil {
			return result, e
		}
		if len(orphaned) == 0 {
			continue
		}
		placeholders, chunkArgs = inClause(orphaned)
		if _, e = tx.ExecContext(ctx, `UPDATE playlists SET revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id IN (SELECT playlist_id FROM playlist_items WHERE track_id IN (`+placeholders+`))`, chunkArgs...); e != nil {
			return result, e
		}
		res, e := tx.ExecContext(ctx, `DELETE FROM tracks WHERE id IN (`+placeholders+`)`, chunkArgs...)
		if e != nil {
			return result, e
		}
		n, _ := res.RowsAffected()
		result.Tracks += n
	}

	if result.Tracks > 0 {
		var albumsBefore, artistsBefore, albumsAfter, artistsAfter int64
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM albums),(SELECT COUNT(*) FROM artists)`).Scan(&albumsBefore, &artistsBefore); err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, cleanupOrphansSQL); err != nil {
			return result, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM albums),(SELECT COUNT(*) FROM artists)`).Scan(&albumsAfter, &artistsAfter); err != nil {
			return result, err
		}
		result.Albums, result.Artists = albumsBefore-albumsAfter, artistsBefore-artistsAfter
	}
	if err = tx.Commit(); err != nil {
		return PurgeResult{}, err
	}
	return result, nil
}

func queryTxIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
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

// PurgeMissingSetting 是管理页保存的缺失清理策略覆盖值。
// found=false 表示未覆盖，应使用环境变量 MUSIC_SERVER_PURGE_MISSING。
func (s *Store) PurgeMissingSetting(ctx context.Context) (string, bool, error) {
	var policy string
	err := s.db.QueryRowContext(ctx, `SELECT policy FROM purge_missing_settings WHERE id=1`).Scan(&policy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return policy, true, nil
}

// SavePurgeMissingSetting 写入覆盖值；policy 必须是 never/always/full。
func (s *Store) SavePurgeMissingSetting(ctx context.Context, policy string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO purge_missing_settings(id,policy) VALUES(1,?)
		ON CONFLICT(id) DO UPDATE SET policy=excluded.policy, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, policy)
	return err
}

// ResetPurgeMissingSetting 删除覆盖值，恢复环境变量默认。
func (s *Store) ResetPurgeMissingSetting(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM purge_missing_settings WHERE id=1`)
	return err
}
