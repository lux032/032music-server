package storage

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/metadata"
	"golang.org/x/text/unicode/norm"
)

const albumWorkRuleVersion = "album-work-v5"

const albumWorkInputSQL = `SELECT COALESCE(a.user_title,a.title),COALESCE((SELECT af.relative_path FROM tracks t JOIN audio_files af ON af.track_id=t.id WHERE t.album_id=a.id ORDER BY af.id LIMIT 1),''),(COALESCE(a.user_is_compilation,a.is_compilation,0)=1 OR EXISTS(SELECT 1 FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id AND LOWER(COALESCE(ar.user_display_name,ar.display_name))='various artists')),EXISTS(SELECT 1 FROM tracks t JOIN audio_files af ON af.track_id=t.id JOIN audio_file_tags tag ON tag.audio_file_id=af.id WHERE t.album_id=a.id AND UPPER(tag.field_name) IN ('COMPILATION','TCMP') AND tag.value='1') FROM albums a WHERE a.id=?`

type albumWorkInput struct {
	title, folder string
	compilation   bool
}

func loadAlbumWorkInput(ctx context.Context, tx *sql.Tx, id int64) (albumWorkInput, error) {
	var v albumWorkInput
	var path string
	var va, tag bool
	err := tx.QueryRowContext(ctx, albumWorkInputSQL, id).Scan(&v.title, &path, &va, &tag)
	if err != nil {
		return v, err
	}
	if path != "" {
		v.folder = filepath.Base(filepath.Dir(path))
	}
	v.compilation = va || tag
	return v, nil
}

type RefreshStats struct {
	AlbumsRefreshed       int
	WorksCreated          int
	WorksDeleted          int
	ProtectedUnreferenced int
	AlbumsFailed          int
}
type AlbumWork struct {
	AlbumID int64  `json:"albumId"`
	WorkID  int64  `json:"workId"`
	Title   string `json:"title"`
	Role    string `json:"role"`
	Source  string `json:"source"`
	Season  int    `json:"season"`
}

func inferredWorkKey(a metadata.WorkAssociation) string {
	// The identity strips the season spelling ("Season 2" / "2期" / "第2期")
	// and compares the season number instead, so re-inference of the same
	// season never creates a duplicate work. Display titles keep the original
	// spelling (D1).
	return normalizedWorkIdentity(a.Title) + "|" + a.Type + "|" + strconv.Itoa(a.Season)
}

func splitInferredKey(key string) (title, typ string, season int) {
	last := strings.LastIndex(key, "|")
	if last < 0 {
		return key, "", 0
	}
	parsedSeason, err := strconv.Atoi(key[last+1:])
	if err != nil {
		return key, "", 0
	}
	before := key[:last]
	secondLast := strings.LastIndex(before, "|")
	if secondLast < 0 {
		return key, "", 0
	}
	parsedType := before[secondLast+1:]
	if _, ok := validWorkTypes[parsedType]; !ok {
		return key, "", 0
	}
	return before[:secondLast], parsedType, parsedSeason
}

func normalizedWorkIdentity(title string) string {
	return metadata.Normalize(metadata.StripWorkSeason(title))
}

func workTitleSuppressionKey(title, typ string) string {
	return normalizedWorkIdentity(title) + "|" + typ + "|" + strconv.Itoa(metadata.WorkSeasonNumber(title))
}

func workTitleSuppressionKeyWithSeason(title, typ string, season int) string {
	if titleSeason := metadata.WorkSeasonNumber(title); titleSeason != 0 {
		season = titleSeason
	}
	return normalizedWorkIdentity(title) + "|" + typ + "|" + strconv.Itoa(season)
}

type workSuppressionIdentity struct {
	Title string
	Type  string
}

func workSuppressionIdentities(ctx context.Context, tx *sql.Tx, workID int64) ([]workSuppressionIdentity, error) {
	var title, typ string
	if err := tx.QueryRowContext(ctx, `SELECT title,type FROM works WHERE id=?`, workID).Scan(&title, &typ); err != nil {
		return nil, err
	}
	values := []workSuppressionIdentity{{Title: title, Type: typ}}
	rows, err := tx.QueryContext(ctx, `SELECT normalized_key FROM work_aliases WHERE work_id=? UNION ALL SELECT title FROM work_external_profiles WHERE work_id=? AND COALESCE(title,'')<>'' UNION ALL SELECT translated_title FROM work_external_profiles WHERE work_id=? AND COALESCE(translated_title,'')<>''`, workID, workID, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var identity string
		if err = rows.Scan(&identity); err != nil {
			return nil, err
		}
		values = append(values, workSuppressionIdentity{Title: identity, Type: typ})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]workSuppressionIdentity, 0, len(values))
	for _, value := range values {
		key := workTitleSuppressionKey(value.Title, value.Type)
		if key == "|"+value.Type+"|0" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out, nil
}

func bangumiTypeCompatible(local, inferred string) bool {
	if local == inferred {
		return true
	}
	if local == "other" || inferred == "other" {
		return true
	}
	if local == "game" || inferred == "game" {
		return local == inferred
	}
	return (local == "anime" || local == "movie") && (inferred == "anime" || inferred == "movie")
}

func bangumiExternalSuppressed(keys []string, externalID string) bool {
	for _, key := range keys {
		parts := strings.Split(key, ":")
		if len(parts) == 3 && parts[0] == "bangumi" && parts[2] == externalID {
			return true
		}
	}
	return false
}

func workBangumiExternalID(ctx context.Context, tx *sql.Tx, workID int64) (string, error) {
	var external string
	err := tx.QueryRowContext(ctx, `SELECT external_id FROM work_external_profiles WHERE work_id=? AND source='bangumi'`, workID).Scan(&external)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return external, err
}

// workTitleKeysMatch compares title-based suppression identities using the
// migration-025 user-intent semantics: title and season only. Work type remains
// part of global resolution, but a user's removal suppresses the same identity
// regardless of a later automatic type inference. Pre-v5 keys may retain the
// season spelling in the title, so a zero season column falls back to it.
func workTitleKeysMatch(stored, current string) bool {
	storedTitle, _, storedSeason := splitInferredKey(stored)
	title, _, season := splitInferredKey(current)
	if storedSeason == 0 {
		storedSeason = metadata.WorkSeasonNumber(storedTitle)
	}
	if season == 0 {
		season = metadata.WorkSeasonNumber(title)
	}
	return storedSeason == season && norm.NFKC.String(normalizedWorkIdentity(storedTitle)) == norm.NFKC.String(normalizedWorkIdentity(title))
}

func suppressionKeys(ctx context.Context, tx *sql.Tx, query string, ids ...int64) ([]string, error) {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// A work is protected from automatic cleanup when the user or a reviewer
// touched it: manual origin/links, an external id or fetched profile, or a
// match candidate the user confirmed or rejected. A pending 'candidate'
// match was never reviewed, so it does not protect; it is cascaded away
// with the work and can be regenerated by the next enrichment pass.
const protectedWorkSQL = `(w.origin='manual' OR COALESCE(w.external_id,'')<>'' OR EXISTS(SELECT 1 FROM work_tracks x WHERE x.work_id=w.id AND x.source='manual') OR EXISTS(SELECT 1 FROM album_works x WHERE x.work_id=w.id AND x.source='manual') OR EXISTS(SELECT 1 FROM work_external_profiles x WHERE x.work_id=w.id AND x.source='bangumi') OR EXISTS(SELECT 1 FROM work_match_candidates x WHERE x.work_id=w.id AND x.status IN ('confirmed','rejected')))`

func resolveAutoWork(ctx context.Context, tx *sql.Tx, a metadata.WorkAssociation, albumID int64) (int64, bool, error) {
	key := metadata.Normalize(a.Title)
	inferred := inferredWorkKey(a)
	// B2: resolve the bound identity first, including legacy aliases and
	// distinct season spellings; never let an unrelated season absorb it.
	var boundID int64
	boundRows, e := tx.QueryContext(ctx, `SELECT DISTINCT w.id,w.title,w.type,COALESCE(x.normalized_key,'') FROM works w JOIN work_external_profiles p ON p.work_id=w.id AND p.source='bangumi' LEFT JOIN work_aliases x ON x.work_id=w.id ORDER BY w.id`)
	if e != nil {
		return 0, false, e
	}
	for boundRows.Next() {
		var candidateTitle, candidateType, alias string
		if e = boundRows.Scan(&boundID, &candidateTitle, &candidateType, &alias); e != nil {
			break
		}
		if !bangumiTypeCompatible(candidateType, a.Type) || metadata.WorkSeasonNumber(candidateTitle) != a.Season {
			continue
		}
		if norm.NFKC.String(key) == norm.NFKC.String(metadata.Normalize(candidateTitle)) || norm.NFKC.String(key) == norm.NFKC.String(alias) || bangumiStrictKey(a.Title) == bangumiStrictKey(candidateTitle) || alias != "" && bangumiStrictKey(a.Title) == bangumiStrictKey(alias) || a.Season > 0 && (norm.NFKC.String(normalizedWorkIdentity(a.Title)) == norm.NFKC.String(normalizedWorkIdentity(candidateTitle)) || alias != "" && norm.NFKC.String(normalizedWorkIdentity(a.Title)) == norm.NFKC.String(normalizedWorkIdentity(alias)) && metadata.WorkSeasonNumber(alias) == a.Season) {
			boundRows.Close()
			return boundID, false, nil
		}
	}
	if e == nil {
		e = boundRows.Err()
	}
	boundRows.Close()
	if e != nil {
		return 0, false, e
	}
	var id int64
	if albumID > 0 {
		err := tx.QueryRowContext(ctx, `SELECT work_id FROM album_works WHERE album_id=? AND source='auto' AND inferred_key=?`, albumID, inferred).Scan(&id)
		if err == nil {
			return id, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
	}
	// M4 type discipline: a typed inference only attaches to a work of the
	// same type (or the catch-all 'other'); exact-type matches outrank
	// protected works. An 'other' inference accepts any type, protected first.
	// Aliases left behind by a user edit (key != current title on a manual
	// work) follow the edited work regardless of type, but only for
	// track-level resolution (albumID == 0): album-level carryover after a
	// user edit is already guaranteed by the inferred_key lookup above, and
	// system-written aliases never skip the filter.
	const typeOrder = `CASE WHEN ?='other' THEN CASE WHEN ` + protectedWorkSQL + ` THEN 0 ELSE 1 END ELSE CASE WHEN w.type=? THEN 0 WHEN ` + protectedWorkSQL + ` THEN 1 ELSE 2 END END`
	err := tx.QueryRowContext(ctx, `SELECT w.id FROM work_aliases x JOIN works w ON w.id=x.work_id WHERE x.normalized_key=? AND (?='other' OR w.type IN (?,'other') OR (x.normalized_key<>w.normalized_title AND w.origin='manual' AND ?=0)) ORDER BY `+typeOrder+`,w.id LIMIT 1`, key, a.Type, a.Type, albumID, a.Type, a.Type).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT w.id FROM works w WHERE w.normalized_title=? AND (?='other' OR w.type IN (?,'other')) ORDER BY `+typeOrder+`,w.id LIMIT 1`, key, a.Type, a.Type, a.Type, a.Type).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if errors.Is(err, sql.ErrNoRows) && a.Season > 0 {
		// Season spellings differ ("Season 2" vs "2期"): match on the
		// season-stripped base title plus the season number instead.
		id, err = resolveSeasonCandidate(ctx, tx, a, key, albumID == 0)
	}
	if errors.Is(err, sql.ErrNoRows) {
		r, e := tx.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES(?,?,?,'auto')`, a.Title, key, a.Type)
		if e != nil {
			return 0, false, e
		}
		id, e = r.LastInsertId()
		if e != nil {
			return 0, false, e
		}
		_, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_aliases(normalized_key,work_id) VALUES(?,?)`, key, id)
		return id, true, e
	}
	if err != nil {
		return 0, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_aliases(normalized_key,work_id) VALUES(?,?)`, key, id); err != nil {
		return 0, false, err
	}
	return id, false, nil
}

func resolveSeasonCandidate(ctx context.Context, tx *sql.Tx, a metadata.WorkAssociation, key string, trackLevel bool) (int64, error) {
	base := normalizedWorkIdentity(a.Title)
	if base == "" || base == key {
		return 0, sql.ErrNoRows
	}
	// Prefix match on the base title alone (no trailing space required, e.g.
	// "進撃の巨人2期"); SQLite substr counts characters, so the length is a
	// rune count. The Go-side season-strip and type checks below reject false
	// positives whose suffix is not a season marker.
	baseLen := utf8.RuneCountInString(base)
	rows, err := tx.QueryContext(ctx, `SELECT w.id,k.normalized_key,w.type,w.normalized_title,w.origin,`+protectedWorkSQL+` FROM works w JOIN (SELECT x.normalized_key,x.work_id FROM work_aliases x WHERE substr(x.normalized_key,1,?)=? UNION SELECT w2.normalized_title,w2.id FROM works w2 WHERE substr(w2.normalized_title,1,?)=?) k ON k.work_id=w.id ORDER BY w.id`, baseLen, base, baseLen, base)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	bestID := int64(0)
	bestRank := -1
	bestExact := false
	for rows.Next() {
		var candidateID int64
		var candidateKey, candidateType, currentTitle, origin string
		var protected bool
		if err = rows.Scan(&candidateID, &candidateKey, &candidateType, &currentTitle, &origin, &protected); err != nil {
			return 0, err
		}
		if normalizedWorkIdentity(candidateKey) != base || metadata.WorkSeasonNumber(candidateKey) != a.Season {
			continue
		}
		// A rename alias follows the work across types only when a user made
		// that edit (origin='manual') and only for track-level resolution;
		// system-written aliases never skip the type filter.
		userEdited := candidateKey != currentTitle && origin == "manual" && trackLevel
		if a.Type != "other" && candidateType != a.Type && candidateType != "other" && !userEdited {
			continue
		}
		rank := 2
		if a.Type == "other" {
			if protected {
				rank = 0
			}
		} else if candidateType == a.Type {
			rank = 0
		} else if protected {
			rank = 1
		}
		exact := candidateKey == key
		if bestRank == -1 || rank < bestRank || rank == bestRank && exact && !bestExact {
			bestID, bestRank, bestExact = candidateID, rank, exact
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if bestRank == -1 {
		return 0, sql.ErrNoRows
	}
	return bestID, nil
}

func (s *Store) AlbumsForWork(ctx context.Context, workID int64) ([]AlbumWork, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT aw.album_id,aw.work_id,COALESCE(a.user_title,a.title),COALESCE((SELECT wt.role FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE wt.work_id=aw.work_id AND t.album_id=aw.album_id ORDER BY CASE wt.source WHEN 'manual' THEN 0 WHEN 'bangumi' THEN 1 ELSE 2 END,wt.track_id,wt.role LIMIT 1),aw.role),aw.source,aw.season FROM album_works aw JOIN albums a ON a.id=aw.album_id WHERE aw.work_id=? ORDER BY aw.album_id`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumWork{}
	for rows.Next() {
		var v AlbumWork
		if err = rows.Scan(&v.AlbumID, &v.WorkID, &v.Title, &v.Role, &v.Source, &v.Season); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AddWorkAlbum(ctx context.Context, workID, albumID int64, role string) error {
	if role == "" {
		role = "other"
	}
	if _, ok := validWorkRoles[role]; !ok {
		return ErrInvalidWork
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Keep any previous suppression until the target work is validated.
	input, err := loadAlbumWorkInput(ctx, tx, albumID)
	if err != nil {
		return err
	}
	var existing int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM works WHERE id=?`, workID).Scan(&existing); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM album_work_suppressions WHERE album_id=? AND inferred_key IN (SELECT inferred_key FROM album_works WHERE album_id=? AND work_id=?)`, albumID, albumID, workID)
	if err != nil {
		return err
	}
	inferredKey := ""
	if inferred, ok := metadata.InferAlbumWork(input.title, input.folder, input.compilation); ok {
		var matched int64
		if e := tx.QueryRowContext(ctx, `SELECT w.id FROM works w LEFT JOIN work_aliases x ON x.work_id=w.id WHERE (w.normalized_title=? OR x.normalized_key=?) AND w.id=? LIMIT 1`, metadata.Normalize(inferred.Title), metadata.Normalize(inferred.Title), workID).Scan(&matched); e == nil {
			inferredKey = inferredWorkKey(inferred)
			if _, err = tx.ExecContext(ctx, `DELETE FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, albumID, inferredKey); err != nil {
				return err
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?, ?,?,'manual',NULLIF(?,'')) ON CONFLICT(album_id,work_id) DO UPDATE SET role=excluded.role,source='manual',inferred_key=COALESCE(excluded.inferred_key,album_works.inferred_key)`, albumID, workID, role, inferredKey)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RemoveWorkAlbum(ctx context.Context, workID, albumID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var albumInferredKey sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT inferred_key FROM album_works WHERE album_id=? AND work_id=?`, albumID, workID).Scan(&albumInferredKey); err != nil {
		return err
	}
	if albumInferredKey.Valid {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, albumID, albumInferredKey.String); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE work_id=? AND source='bangumi' AND inferred_key=? AND track_id IN (SELECT id FROM tracks WHERE album_id=?)`, workID, albumInferredKey.String, albumID); err != nil {
			return err
		}
	}
	// Removed automatic links suppress every known title identity. The original
	// inferred_key above is retained for exact rule-version compatibility.
	var automatic bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_works WHERE album_id=? AND work_id=? AND source IN ('auto','bangumi'))`, albumID, workID).Scan(&automatic); err != nil {
		return err
	}
	if automatic {
		identities, identityErr := workSuppressionIdentities(ctx, tx, workID)
		if identityErr != nil {
			return identityErr
		}
		for _, identity := range identities {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, albumID, workTitleSuppressionKey(identity.Title, identity.Type)); err != nil {
				return err
			}
		}
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM album_works WHERE album_id=? AND work_id=?`, albumID, workID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) RefreshAlbumWorks(ctx context.Context, force bool) (RefreshStats, error) {
	var stats RefreshStats
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_title,a.title),COALESCE(a.work_fingerprint,''),COALESCE((SELECT af.relative_path FROM tracks t JOIN audio_files af ON af.track_id=t.id WHERE t.album_id=a.id ORDER BY af.id LIMIT 1),''),(COALESCE(a.user_is_compilation,a.is_compilation,0)=1 OR EXISTS(SELECT 1 FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id AND LOWER(COALESCE(ar.user_display_name,ar.display_name))='various artists')),EXISTS(SELECT 1 FROM tracks t JOIN audio_files af ON af.track_id=t.id JOIN audio_file_tags tag ON tag.audio_file_id=af.id WHERE t.album_id=a.id AND UPPER(tag.field_name) IN ('COMPILATION','TCMP') AND tag.value='1') FROM albums a ORDER BY a.id`)
	if err != nil {
		return stats, err
	}
	type album struct {
		id                       int64
		title, fingerprint, path string
		various, tag             bool
	}
	albums := []album{}
	for rows.Next() {
		var a album
		if err = rows.Scan(&a.id, &a.title, &a.fingerprint, &a.path, &a.various, &a.tag); err != nil {
			rows.Close()
			return stats, err
		}
		albums = append(albums, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return stats, err
	}
	var tx *sql.Tx
	batch := 0
albumLoop:
	for _, a := range albums {
		folder := filepath.Base(filepath.Dir(a.path))
		if a.path == "" {
			folder = ""
		}
		compilation := a.various || a.tag
		fingerprint := fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("%s|%s|%s|%t", albumWorkRuleVersion, a.title, folder, compilation))))
		if !force && fingerprint == a.fingerprint {
			continue
		}
		var e error
		if tx == nil {
			tx, e = s.db.BeginTx(ctx, nil)
			if e != nil {
				return stats, e
			}
		}
		// The first statement is a write, avoiding a stale read transaction
		// when a concurrent scanner or album merge changes this album.
		first, writeErr := tx.ExecContext(ctx, `UPDATE albums SET work_fingerprint=NULL WHERE id=?`, a.id)
		if writeErr != nil {
			tx.Rollback()
			return stats, writeErr
		}
		present, _ := first.RowsAffected()
		if present != 0 {
			if _, e = tx.ExecContext(ctx, `SAVEPOINT album_refresh`); e != nil {
				tx.Rollback()
				return stats, e
			}
		}
		if present == 0 {
			slog.Warn("album removed during work refresh; skipping", "albumId", a.id)
			continue
		}
		// Re-read after acquiring the write lock: an edit or merge may have
		// changed the title, folder or compilation flag since listing albums.
		input, e := loadAlbumWorkInput(ctx, tx, a.id)
		if e != nil {
			tx.Rollback()
			return stats, e
		}
		a.title = input.title
		folder = input.folder
		compilation = input.compilation
		fingerprint = fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprintf("%s|%s|%s|%t", albumWorkRuleVersion, a.title, folder, compilation))))
		assoc, ok := metadata.InferAlbumWork(a.title, folder, compilation)
		createdInAlbum := 0
		var hasBangumiAlbum bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_works WHERE album_id=? AND source='bangumi')`, a.id).Scan(&hasBangumiAlbum); e != nil {
			tx.Rollback()
			return stats, e
		}
		if hasBangumiAlbum {
			if _, e = tx.ExecContext(ctx, `DELETE FROM album_works WHERE album_id=? AND source='auto'`, a.id); e != nil {
				tx.Rollback()
				return stats, e
			}
		}
		// Keep an unchanged auto link until resolution has chosen its work ID.
		suppressed := false
		if ok && !hasBangumiAlbum {
			key := inferredWorkKey(assoc)
			var supKeys []string
			supKeys, e = suppressionKeys(ctx, tx, `SELECT inferred_key FROM album_work_suppressions WHERE album_id=?`, a.id)
			if e != nil {
				tx.Rollback()
				return stats, e
			}
			for _, stored := range supKeys {
				if workTitleKeysMatch(stored, key) {
					suppressed = true
					break
				}
			}
			if !suppressed {
				var id int64
				var created bool
				id, created, e = resolveAutoWork(ctx, tx, assoc, a.id)
				if e != nil {
					if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO album_refresh`); rollbackErr != nil {
						tx.Rollback()
						return stats, rollbackErr
					}
					if _, releaseErr := tx.ExecContext(ctx, `RELEASE album_refresh`); releaseErr != nil {
						tx.Rollback()
						return stats, releaseErr
					}
					stats.AlbumsFailed++
					slog.Error("infer album work", "albumId", a.id, "error", e)
					continue albumLoop
				} else {
					external, externalErr := workBangumiExternalID(ctx, tx, id)
					if externalErr != nil {
						tx.Rollback()
						return stats, externalErr
					}
					if external != "" && bangumiExternalSuppressed(supKeys, external) {
						suppressed = true
					}
					if !suppressed {
						if created {
							createdInAlbum++
						}
						_, e = tx.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,season,source,inferred_key) VALUES(?,?,?,?,'auto',?) ON CONFLICT(album_id,work_id) DO UPDATE SET role=excluded.role,season=excluded.season,inferred_key=excluded.inferred_key WHERE album_works.source='auto'`, a.id, id, assoc.Role, assoc.Season, key)
					}
					if e != nil {
						if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO album_refresh`); rollbackErr != nil {
							tx.Rollback()
							return stats, rollbackErr
						}
						if _, releaseErr := tx.ExecContext(ctx, `RELEASE album_refresh`); releaseErr != nil {
							tx.Rollback()
							return stats, releaseErr
						}
						stats.AlbumsFailed++
						slog.Error("link album work", "albumId", a.id, "error", e)
						continue albumLoop
					}
				}
			}
		}
		if suppressed {
			if _, e = tx.ExecContext(ctx, `DELETE FROM album_works WHERE album_id=? AND source='auto'`, a.id); e != nil {
				tx.Rollback()
				return stats, e
			}
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM album_works WHERE album_id=? AND source='auto' AND (inferred_key IS NULL OR inferred_key<>? OR ?=0 OR EXISTS(SELECT 1 FROM album_work_suppressions sup WHERE sup.album_id=album_works.album_id AND sup.inferred_key=album_works.inferred_key))`, a.id, func() string {
			if ok && !hasBangumiAlbum {
				return inferredWorkKey(assoc)
			}
			return ""
		}(), boolInt(ok && !hasBangumiAlbum)); e != nil {
			tx.Rollback()
			return stats, e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE source='auto' AND track_id IN (SELECT id FROM tracks WHERE album_id=?)`, a.id); e != nil {
			tx.Rollback()
			return stats, e
		}
		trackRows, e := tx.QueryContext(ctx, `SELECT t.id,COALESCE(t.user_title,t.title) FROM tracks t WHERE t.album_id=?`, a.id)
		if e != nil {
			tx.Rollback()
			return stats, e
		}
		type track struct {
			id    int64
			title string
		}
		tracks := []track{}
		for trackRows.Next() {
			var t track
			if e = trackRows.Scan(&t.id, &t.title); e != nil {
				break
			}
			tracks = append(tracks, t)
		}
		trackRows.Close()
		if e != nil {
			tx.Rollback()
			return stats, e
		}
		for _, t := range tracks {
			raw := map[string][]string{}
			tagRows, te := tx.QueryContext(ctx, `SELECT tag.field_name,tag.value FROM audio_file_tags tag JOIN audio_files af ON af.id=tag.audio_file_id WHERE af.track_id=? ORDER BY tag.position`, t.id)
			if te == nil {
				for tagRows.Next() {
					var field, value string
					if te = tagRows.Scan(&field, &value); te != nil {
						break
					}
					raw[field] = append(raw[field], value)
				}
				tagRows.Close()
			}
			if te != nil {
				tx.Rollback()
				return stats, te
			}
			for _, explicit := range metadata.InferTrackWorkFromTags(raw, t.title) {
				created, re := ensureAutoWorkAssociation(ctx, tx, t.id, a.id, explicit)
				if re != nil {
					if _, e = tx.ExecContext(ctx, `ROLLBACK TO album_refresh`); e != nil {
						tx.Rollback()
						return stats, e
					}
					if _, e = tx.ExecContext(ctx, `RELEASE album_refresh`); e != nil {
						tx.Rollback()
						return stats, e
					}
					stats.AlbumsFailed++
					slog.Error("refresh album work tags", "albumId", a.id, "trackId", t.id, "error", re)
					continue albumLoop
				}
				if created {
					createdInAlbum++
				}
			}
		}
		if _, e = tx.ExecContext(ctx, `UPDATE albums SET work_fingerprint=? WHERE id=?`, fingerprint, a.id); e != nil {
			tx.Rollback()
			return stats, e
		}
		if _, e = tx.ExecContext(ctx, `RELEASE album_refresh`); e != nil {
			tx.Rollback()
			return stats, e
		}
		stats.WorksCreated += createdInAlbum
		stats.AlbumsRefreshed++
		batch++
		if batch == 100 {
			if e = tx.Commit(); e != nil {
				return stats, e
			}
			tx = nil
			batch = 0
		}
	}
	if tx != nil {
		if err = tx.Commit(); err != nil {
			return stats, err
		}
	}
	e := s.CleanupAutoWorks(ctx, &stats)
	return stats, e
}
func (s *Store) CleanupAutoWorks(ctx context.Context, stats *RefreshStats) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Running enrichment is only scheduled for referenced works. Its provider
	// rechecks existence before persisting a fetched result.
	const unused = `NOT EXISTS(SELECT 1 FROM album_works x WHERE x.work_id=w.id) AND NOT EXISTS(SELECT 1 FROM work_tracks x WHERE x.work_id=w.id)`
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM works w WHERE `+unused+` AND `+protectedWorkSQL).Scan(&stats.ProtectedUnreferenced); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM enrichment_provenance WHERE entity_type='work' AND entity_id IN (SELECT w.id FROM works w WHERE w.origin='auto' AND NOT `+protectedWorkSQL+` AND `+unused+`)`)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM works WHERE id IN (SELECT w.id FROM works w WHERE w.origin='auto' AND NOT `+protectedWorkSQL+` AND `+unused+`)`)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	stats.WorksDeleted = int(n)
	// Deleted works cascaded out of their series; clean up series that became
	// empty or single-member and recompute orphaned representatives.
	if err = cleanupSeriesAfterWorkRemoval(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
