package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

type EnrichmentRun struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"`
	Scope        string `json:"scope"`
	Current      string `json:"current"`
	ErrorMessage string `json:"errorMessage"`
	TargetID     int64  `json:"targetId"`
	Force        bool   `json:"force"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	Succeeded    int    `json:"succeeded"`
	Skipped      int    `json:"skipped"`
	Review       int    `json:"review"`
	Failed       int    `json:"failed"`
	// 批次 8：分阶段进度。Stage 是正在处理的阶段（albums/tracks/works/series），
	// StageAlbums/StageTracks/StageWorks 是各阶段收集到的条目数，-1 表示尚未
	// 统计或不属于本轮范围。
	Stage       string `json:"stage"`
	StageAlbums int    `json:"stageAlbums"`
	StageTracks int    `json:"stageTracks"`
	StageWorks  int    `json:"stageWorks"`
	CreatedAt   string `json:"createdAt"`
	StartedAt   string `json:"startedAt"`
	UpdatedAt   string `json:"updatedAt"`
	FinishedAt  string `json:"finishedAt"`
}

type EnrichmentRunUpdate struct {
	Total, Processed, Succeeded, Skipped, Review, Failed int
	Current, ErrorMessage                                string
	Stage                                                string
	// StageAlbums/StageTracks/StageWorks：-1 表示该阶段尚未统计（页面显示
	// “待统计”），0 表示统计过但为 0。零值结构体会把“未统计”误写成 0，
	// 新建载荷请用 NewEnrichmentRunUpdate。
	StageAlbums, StageTracks, StageWorks int
}

// NewEnrichmentRunUpdate 返回阶段计数均为“未统计”（-1）的更新载荷。
func NewEnrichmentRunUpdate() EnrichmentRunUpdate {
	return EnrichmentRunUpdate{StageAlbums: -1, StageTracks: -1, StageWorks: -1}
}

type HTTPResponseCacheEntry struct {
	Source, Key, ETag, LastModified, FetchedAt, ExpiresAt string
	Status                                                int
	Body                                                  []byte
}

type AlbumEnrichmentTarget struct {
	ID, TrackCount                            int64
	Title, Artist, CatalogNumber, ReleaseDate string
}

type WorkEnrichmentTarget struct {
	ID                     int64
	Title, TranslatedTitle string
	Type                   string
	Year                   int
	BangumiExternalID      string
	BangumiProfileType     string
	BangumiRaw             json.RawMessage
}

type ExternalAlbumProfile struct {
	Source, ExternalID, PageURL, Title, ReleaseDate, OriginalReleaseDate string
	CatalogNumber, Label, Country, AlbumType, PerformedBy                string
	DiscCount                                                            int
	Raw                                                                  json.RawMessage
	FetchedAt                                                            string
}

type AlbumFieldPatch struct {
	Title, ReleaseDate, OriginalReleaseDate, CatalogNumber string
	Label, Country, AlbumType, PerformedBy                 string
	ReleaseYear, DiscCount                                 int
}

type EnrichmentCredit struct {
	TrackID    int64
	Name, Role string
	Position   int
	JoinPhrase string
	ExternalID string
}

type ExternalWorkProfile struct {
	Source, ExternalID, PageURL, Title, OriginalTitle, TranslatedTitle string
	Type, PosterURL                                                    string
	Year                                                               int
	Raw                                                                json.RawMessage
	FetchedAt                                                          string
}

type WorkMatchCandidate struct {
	ID, WorkID                                                      int64
	Source, ExternalID, Title, OriginalTitle, TranslatedTitle, Type string
	PageURL, PosterURL, Status                                      string
	Year, Score                                                     int
	Evidence                                                        []string
	Payload                                                         json.RawMessage
}

type ArtistRelationCandidate struct {
	ID, ArtistID, TargetArtistID                       int64
	Source, ExternalID, RelatedExternalID, RelatedName string
	RelationType, Direction, Status                    string
	Score                                              int
	Evidence                                           []string
	Payload                                            json.RawMessage
}

func (s *Store) CreateEnrichmentRun(ctx context.Context, scope string, targetID int64, force bool, total int) (EnrichmentRun, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" || targetID < 0 || total < 0 {
		return EnrichmentRun{}, fmt.Errorf("invalid enrichment run")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO enrichment_runs(status,scope,target_id,force,total,started_at) VALUES('running',?,NULLIF(?,0),?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, scope, targetID, boolInt(force), total)
	if err != nil {
		return EnrichmentRun{}, err
	}
	id, _ := result.LastInsertId()
	return s.EnrichmentRun(ctx, id)
}

func (s *Store) UpdateEnrichmentRun(ctx context.Context, id int64, value EnrichmentRunUpdate) error {
	if value.Total < 0 || value.Processed < 0 || value.Succeeded < 0 || value.Skipped < 0 || value.Review < 0 || value.Failed < 0 {
		return fmt.Errorf("invalid enrichment run counters")
	}
	if value.StageAlbums < -1 || value.StageTracks < -1 || value.StageWorks < -1 {
		return fmt.Errorf("invalid enrichment run stage counters")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE enrichment_runs SET total=?,processed=?,succeeded=?,skipped=?,review=?,failed=?,current=NULLIF(?,''),error_message=NULLIF(?,''),stage=?,stage_albums=?,stage_tracks=?,stage_works=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, value.Total, value.Processed, value.Succeeded, value.Skipped, value.Review, value.Failed, value.Current, value.ErrorMessage, value.Stage, value.StageAlbums, value.StageTracks, value.StageWorks, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) FinishEnrichmentRun(ctx context.Context, id int64, status, message string) error {
	if status != "completed" && status != "failed" && status != "cancelled" {
		return fmt.Errorf("invalid terminal enrichment status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE enrichment_runs SET status=?,current=NULL,error_message=NULLIF(?,''),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, status, message, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// FailRunningEnrichmentRuns closes jobs left running by a previous process.
// Managers should call it during startup before accepting new enrichment work.
func (s *Store) FailRunningEnrichmentRuns(ctx context.Context, message string) (int64, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "enrichment interrupted by process restart"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE enrichment_runs SET status='failed',current=NULL,error_message=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status='running'`, message)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func scanEnrichmentRun(scanner interface{ Scan(...any) error }) (EnrichmentRun, error) {
	var v EnrichmentRun
	var target sql.NullInt64
	var force int
	err := scanner.Scan(&v.ID, &v.Status, &v.Scope, &target, &force, &v.Total, &v.Processed, &v.Succeeded, &v.Skipped, &v.Review, &v.Failed, &v.Current, &v.ErrorMessage, &v.Stage, &v.StageAlbums, &v.StageTracks, &v.StageWorks, &v.CreatedAt, &v.StartedAt, &v.UpdatedAt, &v.FinishedAt)
	v.TargetID, v.Force = target.Int64, force != 0
	return v, err
}

const enrichmentRunColumns = `id,status,scope,target_id,force,total,processed,succeeded,skipped,review,failed,COALESCE(current,''),COALESCE(error_message,''),stage,stage_albums,stage_tracks,stage_works,created_at,COALESCE(started_at,''),updated_at,COALESCE(finished_at,'')`

func (s *Store) EnrichmentRun(ctx context.Context, id int64) (EnrichmentRun, error) {
	return scanEnrichmentRun(s.db.QueryRowContext(ctx, `SELECT `+enrichmentRunColumns+` FROM enrichment_runs WHERE id=?`, id))
}

func (s *Store) ListEnrichmentRuns(ctx context.Context, limit, offset int) ([]EnrichmentRun, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+enrichmentRunColumns+` FROM enrichment_runs ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []EnrichmentRun
	for rows.Next() {
		v, scanErr := scanEnrichmentRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (s *Store) GetHTTPResponseCache(ctx context.Context, source, key string) (HTTPResponseCacheEntry, error) {
	var v HTTPResponseCacheEntry
	err := s.db.QueryRowContext(ctx, `SELECT source,cache_key,status_code,body,COALESCE(etag,''),COALESCE(last_modified,''),fetched_at,expires_at FROM http_response_cache WHERE source=? AND cache_key=?`, source, key).Scan(&v.Source, &v.Key, &v.Status, &v.Body, &v.ETag, &v.LastModified, &v.FetchedAt, &v.ExpiresAt)
	return v, err
}

func (s *Store) PutHTTPResponseCache(ctx context.Context, value HTTPResponseCacheEntry) error {
	if value.FetchedAt == "" {
		value.FetchedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if value.ExpiresAt == "" {
		return fmt.Errorf("cache expiry is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO http_response_cache(source,cache_key,status_code,body,etag,last_modified,fetched_at,expires_at) VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?) ON CONFLICT(source,cache_key) DO UPDATE SET status_code=excluded.status_code,body=excluded.body,etag=excluded.etag,last_modified=excluded.last_modified,fetched_at=excluded.fetched_at,expires_at=excluded.expires_at`, value.Source, value.Key, value.Status, value.Body, value.ETag, value.LastModified, value.FetchedAt, value.ExpiresAt)
	return err
}

func (s *Store) AlbumsForEnrichment(ctx context.Context, source string, force bool, limit int) ([]AlbumEnrichmentTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_title,a.title),COALESCE((SELECT GROUP_CONCAT(COALESCE(ar.user_display_name,ar.display_name),', ') FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id),''),COALESCE(a.user_catalog_number,a.catalog_number,''),COALESCE(a.user_release_date,a.release_date,''),(SELECT COUNT(*) FROM tracks t WHERE t.album_id=a.id) FROM albums a WHERE ? OR NOT EXISTS(SELECT 1 FROM album_external_profiles p WHERE p.album_id=a.id AND p.source=?) ORDER BY a.id LIMIT ?`, boolInt(force), source, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []AlbumEnrichmentTarget
	for rows.Next() {
		var v AlbumEnrichmentTarget
		if err := rows.Scan(&v.ID, &v.Title, &v.Artist, &v.CatalogNumber, &v.ReleaseDate, &v.TrackCount); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// WorksForEnrichment schedules only referenced works; an unlinked manual work
// is not automatically enriched until associated with a track or album.
func (s *Store) WorksForEnrichment(ctx context.Context, source string, force bool, limit int, targetID ...int64) ([]WorkEnrichmentTarget, error) {
	query := `SELECT w.id,w.title,COALESCE(w.translated_title,''),w.type,COALESCE(w.year,0),COALESCE(p.external_id,''),COALESCE(p.type,''),COALESCE(p.raw_json,'{}') FROM works w LEFT JOIN work_external_profiles p ON p.work_id=w.id AND p.source=? WHERE (EXISTS(SELECT 1 FROM album_works aw WHERE aw.work_id=w.id) OR EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.work_id=w.id)) AND (? OR (p.work_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM enrichment_provenance ep WHERE ep.entity_type='work' AND ep.entity_id=w.id AND ep.field_name='type_correction_skipped' AND ep.source='bangumi') AND CASE WHEN json_valid(p.raw_json) THEN (json_extract(p.raw_json,'$.type') IS NULL AND json_extract(p.raw_json,'$.id') IS NOT NULL OR json_extract(p.raw_json,'$.type')=4 AND w.type<>'game' OR json_extract(p.raw_json,'$.type')=2 AND (json_extract(p.raw_json,'$.platform') IS NULL OR w.type<>CASE WHEN json_extract(p.raw_json,'$.platform') LIKE '%剧场版%' OR json_extract(p.raw_json,'$.platform') LIKE '%劇場版%' THEN 'movie' ELSE 'anime' END)) ELSE 0 END) OR (p.work_id IS NULL AND NOT EXISTS(SELECT 1 FROM work_match_candidates c WHERE c.work_id=w.id AND c.source=? AND c.status='confirmed') AND (EXISTS(SELECT 1 FROM work_enrichment_retries r WHERE r.work_id=w.id AND r.source=?) OR (NOT EXISTS(SELECT 1 FROM work_match_candidates c WHERE c.work_id=w.id AND c.source=?) AND NOT EXISTS(SELECT 1 FROM work_enrichment_misses m WHERE m.work_id=w.id AND m.source=?)))))`
	args := []any{source, boolInt(force), source, source, source, source}
	if len(targetID) > 0 && targetID[0] > 0 {
		query += ` AND w.id=?`
		args = append(args, targetID[0])
	}
	query += ` ORDER BY w.id`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []WorkEnrichmentTarget
	for rows.Next() {
		var v WorkEnrichmentTarget
		var raw string
		if err := rows.Scan(&v.ID, &v.Title, &v.TranslatedTitle, &v.Type, &v.Year, &v.BangumiExternalID, &v.BangumiProfileType, &raw); err != nil {
			return nil, err
		}
		v.BangumiRaw = json.RawMessage(raw)
		values = append(values, v)
	}
	return values, rows.Err()
}

func (s *Store) UpdateBangumiWorkProfileRaw(ctx context.Context, workID int64, raw json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE work_external_profiles SET raw_json=? WHERE work_id=? AND source='bangumi' AND raw_json<>?`, rawOrEmpty(raw), workID, rawOrEmpty(raw))
	return err
}

func (s *Store) RecordBangumiTypeCorrectionEvidence(ctx context.Context, workID int64, externalID, reason string, runID int64) error {
	// Enrichment runs are optional in direct/provider tests. Preserve the
	// evidence without violating the provenance run foreign key.
	if runID > 0 {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM enrichment_runs WHERE id=?)`, runID).Scan(&exists); err != nil {
			return err
		} else if !exists {
			runID = 0
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = putProvenance(ctx, tx, "work", workID, "type_correction_skipped", "bangumi", externalID, runID, map[string]string{"reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetWorkEnrichmentMiss(ctx context.Context, workID int64, source string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_enrichment_misses(work_id,source,checked_at) VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(work_id,source) DO UPDATE SET checked_at=excluded.checked_at`, workID, source)
	return err
}

func (s *Store) DeleteWorkEnrichmentRetry(ctx context.Context, workID int64, source string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM work_enrichment_retries WHERE work_id=? AND source=?`, workID, source)
	return err
}

func (s *Store) DeleteWorkEnrichmentMiss(ctx context.Context, workID int64, source string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM work_enrichment_misses WHERE work_id=? AND source=?`, workID, source)
	return err
}

// WorkBangumiConfirmed protects confirmed matches even during forced runs.
func (s *Store) WorkBangumiConfirmed(ctx context.Context, workID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_external_profiles WHERE work_id=? AND source='bangumi') OR EXISTS(SELECT 1 FROM work_match_candidates WHERE work_id=? AND source='bangumi' AND status='confirmed')`, workID, workID).Scan(&exists)
	return exists, err
}

func rawOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}
func fetchedOrNow(value string) string {
	if value != "" {
		return value
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func (s *Store) UpsertExternalAlbumProfile(ctx context.Context, albumID int64, p ExternalAlbumProfile) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO album_external_profiles(album_id,source,external_id,page_url,title,release_date,original_release_date,catalog_number,label,country,album_type,performed_by,disc_count,raw_json,fetched_at) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,0),?,?) ON CONFLICT(album_id,source) DO UPDATE SET external_id=excluded.external_id,page_url=excluded.page_url,title=excluded.title,release_date=excluded.release_date,original_release_date=excluded.original_release_date,catalog_number=excluded.catalog_number,label=excluded.label,country=excluded.country,album_type=excluded.album_type,performed_by=excluded.performed_by,disc_count=excluded.disc_count,raw_json=excluded.raw_json,fetched_at=excluded.fetched_at`, albumID, p.Source, p.ExternalID, p.PageURL, p.Title, p.ReleaseDate, p.OriginalReleaseDate, p.CatalogNumber, p.Label, p.Country, p.AlbumType, p.PerformedBy, p.DiscCount, rawOrEmpty(p.Raw), fetchedOrNow(p.FetchedAt))
	return err
}

func (s *Store) UpsertExternalWorkProfile(ctx context.Context, workID int64, p ExternalWorkProfile) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,page_url,title,original_title,translated_title,type,year,poster_url,raw_json,fetched_at) VALUES(?,?,?,NULLIF(?,''),?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,0),NULLIF(?,''),?,?) ON CONFLICT(work_id,source) DO UPDATE SET external_id=excluded.external_id,page_url=excluded.page_url,title=excluded.title,original_title=excluded.original_title,translated_title=excluded.translated_title,type=excluded.type,year=excluded.year,poster_url=excluded.poster_url,raw_json=excluded.raw_json,fetched_at=excluded.fetched_at`, workID, p.Source, p.ExternalID, p.PageURL, p.Title, p.OriginalTitle, p.TranslatedTitle, p.Type, p.Year, p.PosterURL, rawOrEmpty(p.Raw), fetchedOrNow(p.FetchedAt))
	return err
}

func putProvenance(ctx context.Context, tx *sql.Tx, entityType string, entityID int64, field, source, externalID string, runID int64, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO enrichment_provenance(entity_type,entity_id,field_name,source,external_id,run_id,value_json) VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,0),?) ON CONFLICT(entity_type,entity_id,field_name,source) DO UPDATE SET external_id=excluded.external_id,run_id=excluded.run_id,value_json=excluded.value_json,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, entityType, entityID, field, source, externalID, runID, string(raw))
	return err
}

func (s *Store) ConservativelyFillAlbum(ctx context.Context, albumID int64, patch AlbumFieldPatch, source, externalID string, runID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type field struct {
		name, condition, value string
		arg                    any
	}
	fields := []field{
		{"title", `COALESCE(NULLIF(user_title,''),NULLIF(title,'')) IS NULL`, patch.Title, patch.Title},
		{"release_date", `COALESCE(NULLIF(user_release_date,''),NULLIF(release_date,'')) IS NULL`, patch.ReleaseDate, patch.ReleaseDate},
		{"original_release_date", `COALESCE(NULLIF(user_original_release_date,''),NULLIF(original_release_date,'')) IS NULL`, patch.OriginalReleaseDate, patch.OriginalReleaseDate},
		{"catalog_number", `COALESCE(NULLIF(user_catalog_number,''),NULLIF(catalog_number,'')) IS NULL`, patch.CatalogNumber, patch.CatalogNumber},
		{"label", `COALESCE(NULLIF(user_label,''),NULLIF(label,'')) IS NULL`, patch.Label, patch.Label},
		{"country", `COALESCE(NULLIF(user_country,''),NULLIF(country,'')) IS NULL`, patch.Country, patch.Country},
		{"album_type", `user_album_type IS NULL AND (album_type IS NULL OR album_type='' OR album_type='album')`, patch.AlbumType, patch.AlbumType},
		{"performed_by", `COALESCE(NULLIF(user_performed_by,''),NULLIF(performed_by,'')) IS NULL`, patch.PerformedBy, patch.PerformedBy},
	}
	for _, f := range fields {
		if strings.TrimSpace(f.value) == "" {
			continue
		}
		result, e := tx.ExecContext(ctx, `UPDATE albums SET `+f.name+`=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND `+f.condition, f.arg, albumID)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n > 0 {
			if e = putProvenance(ctx, tx, "album", albumID, f.name, source, externalID, runID, f.arg); e != nil {
				return e
			}
		}
	}
	if patch.ReleaseYear > 0 {
		result, e := tx.ExecContext(ctx, `UPDATE albums SET release_year=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND user_release_year IS NULL AND release_year IS NULL`, patch.ReleaseYear, albumID)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n > 0 {
			if e = putProvenance(ctx, tx, "album", albumID, "release_year", source, externalID, runID, patch.ReleaseYear); e != nil {
				return e
			}
		}
	}
	if patch.DiscCount > 0 {
		result, e := tx.ExecContext(ctx, `UPDATE albums SET disc_count=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND disc_count=1`, patch.DiscCount, albumID)
		if e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n > 0 {
			if e = putProvenance(ctx, tx, "album", albumID, "disc_count", source, externalID, runID, patch.DiscCount); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}

// NormalizeTrackArtistRole maps the four structured credit classes to their
// canonical names. Other involved-person roles are stored as instrument:<role>.
func NormalizeTrackArtistRole(role string) string {
	normalized := metadata.Normalize(strings.TrimSpace(role))
	switch normalized {
	case "composer", "composed by", "music":
		return "composer"
	case "lyricist", "lyrics", "words", "text", "written by":
		return "lyricist"
	case "arranger", "arranged by", "orchestrator", "orchestration":
		return "arranger"
	case "producer", "produced by":
		return "producer"
	}
	if strings.HasPrefix(normalized, "instrument:") {
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "instrument:"))
	}
	if normalized == "" {
		return ""
	}
	normalized = strings.NewReplacer(":", "_", "/", "_", "\\", "_", " ", "_").Replace(normalized)
	for strings.Contains(normalized, "__") {
		normalized = strings.ReplaceAll(normalized, "__", "_")
	}
	normalized = strings.Trim(normalized, "_")
	if normalized == "" {
		return ""
	}
	return "instrument:" + normalized
}

func validTrackArtistSource(source string) bool {
	switch source {
	case "file_tag", "musicbrainz", "vgmdb", "bangumi", "manual":
		return true
	default:
		return false
	}
}

func ensureTrackArtistSource(ctx context.Context, tx *sql.Tx, trackID, artistID int64, position int, role, joinPhrase, source, externalID string, runID int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO track_artist_sources(track_id,artist_id,position,role,join_phrase,source,external_id,run_id) VALUES(?,?,?,?,?,?,?,NULLIF(?,0)) ON CONFLICT(track_id,artist_id,position,role,source,external_id) DO UPDATE SET join_phrase=excluded.join_phrase,run_id=excluded.run_id,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, trackID, artistID, position, role, joinPhrase, source, externalID, runID)
	return err
}

func materializeTrackArtistSources(ctx context.Context, tx *sql.Tx, trackID int64) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_artists(track_id,artist_id,position,join_phrase,role) SELECT s.track_id,s.artist_id,COALESCE((SELECT MIN(p.position) FROM track_artists p WHERE p.track_id=s.track_id AND p.role=s.role AND p.artist_id=s.artist_id),s.position),s.join_phrase,s.role FROM track_artist_sources s WHERE s.track_id=? ORDER BY CASE s.source WHEN 'manual' THEN 0 WHEN 'file_tag' THEN 1 ELSE 2 END,s.position`, trackID)
	return err
}

// SaveTrackInvolvedPeople persists structured TIPL/TMCL-style credits and
// materializes only missing track_artists rows. file_tag is replace-on-rescan;
// remote/manual sources are additive and therefore survive scanner rebuilds in
// track_artist_sources and can be restored without replacing tagged credits.
func (s *Store) SaveTrackInvolvedPeople(ctx context.Context, trackID int64, people []metadata.InvolvedPerson, source string, runID int64) error {
	if trackID <= 0 || !validTrackArtistSource(source) {
		return fmt.Errorf("invalid track involved-people source %q", source)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if source == "file_tag" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM track_artist_sources WHERE track_id=? AND source='file_tag'`, trackID); err != nil {
			return err
		}
	}
	positions := make(map[string]int)
	seen := make(map[string]struct{})
	for _, person := range people {
		name := strings.TrimSpace(person.Name)
		role := NormalizeTrackArtistRole(person.Role)
		key := metadata.Normalize(name)
		if name == "" || role == "" || key == "" {
			continue
		}
		duplicateKey := role + "\x00" + key
		if _, ok := seen[duplicateKey]; ok {
			continue
		}
		seen[duplicateKey] = struct{}{}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?) ON CONFLICT(identity_key) DO NOTHING`, name, key, key); err != nil {
			return err
		}
		var artistID int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key=?`, key).Scan(&artistID); err != nil {
			return err
		}
		position := positions[role]
		positions[role]++
		if err = ensureTrackArtistSource(ctx, tx, trackID, artistID, position, role, "", source, "", runID); err != nil {
			return err
		}
		if err = putProvenance(ctx, tx, "track_credit", trackID, role+":"+fmt.Sprint(artistID), source, "", runID, map[string]any{"artistId": artistID, "name": name, "role": role, "position": position}); err != nil {
			return err
		}
	}
	if err = materializeTrackArtistSources(ctx, tx, trackID); err != nil {
		return err
	}
	return tx.Commit()
}

// RestoreTrackEnrichmentCredits rematerializes remotely sourced credits after a
// scanner rebuild. It never removes or updates an existing track_artists row.
func (s *Store) RestoreTrackEnrichmentCredits(ctx context.Context, trackID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = materializeTrackArtistSources(ctx, tx, trackID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddEnrichmentCredits(ctx context.Context, albumID int64, credits []EnrichmentCredit, source string, runID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !validTrackArtistSource(source) || source == "file_tag" {
		return fmt.Errorf("invalid enrichment credit source %q", source)
	}
	for _, c := range credits {
		name := strings.TrimSpace(c.Name)
		role := NormalizeTrackArtistRole(c.Role)
		if name == "" || role == "" {
			continue
		}
		key := metadata.Normalize(name)
		if _, err = tx.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?) ON CONFLICT(identity_key) DO NOTHING`, name, key, key); err != nil {
			return err
		}
		var artistID int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key=?`, key).Scan(&artistID); err != nil {
			return err
		}
		pos := c.Position
		if pos < 0 {
			pos = 0
		}
		entityType, entityID := "album_credit", albumID
		if c.TrackID > 0 {
			entityType, entityID = "track_credit", c.TrackID
			if err = ensureTrackArtistSource(ctx, tx, c.TrackID, artistID, pos, role, c.JoinPhrase, source, c.ExternalID, runID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,join_phrase,role) VALUES(?,?,?,?,?) ON CONFLICT(track_id,artist_id,position,role) DO NOTHING`, c.TrackID, artistID, pos, c.JoinPhrase, role)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position,join_phrase) VALUES(?,?,?,?) ON CONFLICT(album_id,artist_id,position) DO NOTHING`, albumID, artistID, pos, c.JoinPhrase)
		}
		if err != nil {
			return err
		}
		if err = putProvenance(ctx, tx, entityType, entityID, role+":"+fmt.Sprint(artistID), source, c.ExternalID, runID, map[string]any{"artistId": artistID, "name": name, "role": role, "position": pos}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ReplaceWorkMatchCandidates(ctx context.Context, workID int64, candidates []WorkMatchCandidate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_match_candidates WHERE work_id=? AND status='candidate'`, workID); err != nil {
		return err
	}
	for _, v := range candidates {
		evidence, _ := json.Marshal(v.Evidence)
		_, err = tx.ExecContext(ctx, `INSERT INTO work_match_candidates(work_id,source,external_id,title,original_title,translated_title,type,year,page_url,poster_url,score,evidence_json,payload_json,status) VALUES(?,?,?, ?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,0),NULLIF(?,''),NULLIF(?,''),?,?,?,'candidate') ON CONFLICT(work_id,source,external_id) DO UPDATE SET title=excluded.title,original_title=excluded.original_title,translated_title=excluded.translated_title,type=excluded.type,year=excluded.year,page_url=excluded.page_url,poster_url=excluded.poster_url,score=excluded.score,evidence_json=excluded.evidence_json,payload_json=excluded.payload_json,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, workID, v.Source, v.ExternalID, v.Title, v.OriginalTitle, v.TranslatedTitle, v.Type, v.Year, v.PageURL, v.PosterURL, v.Score, string(evidence), rawOrEmpty(v.Payload))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetWorkMatchCandidateStatus(ctx context.Context, workID, candidateID int64, status string) error {
	if status != "confirmed" && status != "rejected" {
		return fmt.Errorf("invalid work candidate status %q", status)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE work_match_candidates SET status=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND work_id=?`, status, candidateID, workID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) WorkMatchCandidates(ctx context.Context, workID int64) ([]WorkMatchCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,work_id,source,external_id,title,COALESCE(original_title,''),COALESCE(translated_title,''),COALESCE(type,''),COALESCE(year,0),COALESCE(page_url,''),COALESCE(poster_url,''),score,evidence_json,payload_json,status FROM work_match_candidates WHERE work_id=? ORDER BY status='confirmed' DESC,score DESC,id`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []WorkMatchCandidate
	for rows.Next() {
		var v WorkMatchCandidate
		var evidence, payload string
		if err = rows.Scan(&v.ID, &v.WorkID, &v.Source, &v.ExternalID, &v.Title, &v.OriginalTitle, &v.TranslatedTitle, &v.Type, &v.Year, &v.PageURL, &v.PosterURL, &v.Score, &evidence, &payload, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &v.Evidence)
		v.Payload = json.RawMessage(payload)
		values = append(values, v)
	}
	return values, rows.Err()
}

var ErrAutoConfirmConflict = fmt.Errorf("work candidate is no longer eligible for automatic confirmation")

// WorkExternalIDConflictError reports that the external subject selected for a
// work is already bound to a different local work (UNIQUE(source,external_id)).
type WorkExternalIDConflictError struct {
	Source      string
	ExternalID  string
	OwnerWorkID int64
	OwnerTitle  string
}

func (e *WorkExternalIDConflictError) Error() string {
	return fmt.Sprintf("%s 条目 %s 已绑定到作品 #%d「%s」，同一外部条目只能关联一个作品；请先解除该作品的绑定或合并重复作品", e.Source, e.ExternalID, e.OwnerWorkID, e.OwnerTitle)
}

func (s *Store) AutoConfirmWorkMatchCandidate(ctx context.Context, workID, candidateID, runID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_match_candidates WHERE id=? AND work_id=? AND source='bangumi' AND status='candidate') AND NOT EXISTS(SELECT 1 FROM work_external_profiles WHERE work_id=? AND source='bangumi') AND NOT EXISTS(SELECT 1 FROM work_match_candidates WHERE work_id=? AND source='bangumi' AND status='confirmed') AND NOT EXISTS(SELECT 1 FROM work_external_profiles p JOIN work_match_candidates c ON c.id=? AND c.source=p.source AND c.external_id=p.external_id WHERE p.work_id<>?)`, candidateID, workID, workID, workID, candidateID, workID).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrAutoConfirmConflict
	}
	if err = confirmWorkMatchCandidateTx(ctx, tx, workID, candidateID, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConfirmWorkMatchCandidate(ctx context.Context, workID, candidateID, runID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = confirmWorkMatchCandidateTx(ctx, tx, workID, candidateID, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func confirmWorkMatchCandidateTx(ctx context.Context, tx *sql.Tx, workID, candidateID, runID int64) error {
	var err error
	var p ExternalWorkProfile
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT source,external_id,COALESCE(page_url,''),title,COALESCE(original_title,''),COALESCE(translated_title,''),COALESCE(type,''),COALESCE(year,0),COALESCE(poster_url,''),payload_json FROM work_match_candidates WHERE id=? AND work_id=?`, candidateID, workID).Scan(&p.Source, &p.ExternalID, &p.PageURL, &p.Title, &p.OriginalTitle, &p.TranslatedTitle, &p.Type, &p.Year, &p.PosterURL, &raw); err != nil {
		return err
	}
	p.Raw = json.RawMessage(raw)
	p.FetchedAt = fetchedOrNow("")
	if err = applyWorkExternalProfileTx(ctx, tx, workID, p, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_match_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE work_id=?`, candidateID, workID); err != nil {
		return err
	}
	return nil
}

// M2: shared binding and provenance for work and album confirmations.
func (s *Store) CorrectBangumiWorkType(ctx context.Context, workID int64, oldType, correctedType, externalID string, runID int64) (bool, error) {
	if correctedType != "anime" && correctedType != "movie" && correctedType != "game" {
		return false, ErrInvalidWork
	}
	if oldType == correctedType {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE work_external_profiles SET type=? WHERE work_id=? AND source='bangumi' AND COALESCE(type,'')<>?`, correctedType, workID, correctedType); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE works SET type=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND type=? AND type<>? AND type_locked=0 AND NOT EXISTS(SELECT 1 FROM works other JOIN works current ON current.id=? WHERE other.id<>current.id AND other.normalized_title=current.normalized_title AND other.type=? AND IFNULL(other.year,0)=IFNULL(current.year,0))`, correctedType, workID, oldType, correctedType, workID, correctedType)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	if changed > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type_correction_skipped' AND source='bangumi'`, workID); err != nil {
			return false, err
		}
		if err = putProvenance(ctx, tx, "work", workID, "type", "bangumi", externalID, runID, correctedType); err != nil {
			return false, err
		}
	} else if oldType != correctedType {
		if err = putProvenance(ctx, tx, "work", workID, "type_correction_skipped", "bangumi", externalID, runID, map[string]string{"from": oldType, "to": correctedType, "reason": "type locked, changed concurrently, or identity conflict"}); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return changed > 0, nil
}

func applyWorkExternalProfileTx(ctx context.Context, tx *sql.Tx, workID int64, p ExternalWorkProfile, runID int64) error {
	var err error
	var ownerID int64
	var ownerTitle string
	err = tx.QueryRowContext(ctx, `SELECT w.id,w.title FROM work_external_profiles p JOIN works w ON w.id=p.work_id WHERE p.source=? AND p.external_id=? AND p.work_id<>?`, p.Source, p.ExternalID, workID).Scan(&ownerID, &ownerTitle)
	if err == nil {
		return &WorkExternalIDConflictError{Source: p.Source, ExternalID: p.ExternalID, OwnerWorkID: ownerID, OwnerTitle: ownerTitle}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,page_url,title,original_title,translated_title,type,year,poster_url,raw_json,fetched_at) VALUES(?,?,?,NULLIF(?,''),?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,0),NULLIF(?,''),?,?) ON CONFLICT(work_id,source) DO UPDATE SET external_id=excluded.external_id,page_url=excluded.page_url,title=excluded.title,original_title=excluded.original_title,translated_title=excluded.translated_title,type=excluded.type,year=excluded.year,poster_url=excluded.poster_url,raw_json=excluded.raw_json,fetched_at=excluded.fetched_at`, workID, p.Source, p.ExternalID, p.PageURL, p.Title, p.OriginalTitle, p.TranslatedTitle, p.Type, p.Year, p.PosterURL, rawOrEmpty(p.Raw), p.FetchedAt)
	if err != nil {
		return err
	}
	type wf struct {
		name, value, condition string
		arg                    any
	}
	fields := []wf{{"translated_title", p.TranslatedTitle, `COALESCE(translated_title,'')=''`, p.TranslatedTitle}, {"type", p.Type, `type='other' AND type_locked=0`, p.Type}, {"poster_url", p.PosterURL, `COALESCE(poster_url,'')=''`, p.PosterURL}}
	for _, f := range fields {
		if strings.TrimSpace(f.value) == "" {
			continue
		}
		condition := f.condition
		if f.name == "type" {
			condition += ` AND NOT EXISTS(SELECT 1 FROM works other JOIN works current ON current.id=? WHERE other.id<>current.id AND other.normalized_title=current.normalized_title AND other.type=? AND IFNULL(other.year,0)=IFNULL(current.year,0))`
		}
		args := []any{f.arg, workID}
		if f.name == "type" {
			args = append(args, workID, f.arg)
		}
		r, e := tx.ExecContext(ctx, `UPDATE works SET `+f.name+`=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND `+condition, args...)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n > 0 {
			if e = putProvenance(ctx, tx, "work", workID, f.name, p.Source, p.ExternalID, runID, f.arg); e != nil {
				return e
			}
		}
	}
	if p.Year > 0 {
		r, e := tx.ExecContext(ctx, `UPDATE works SET year=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND year IS NULL`, p.Year, workID)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n > 0 {
			if e = putProvenance(ctx, tx, "work", workID, "year", p.Source, p.ExternalID, runID, p.Year); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Store) ReplaceArtistRelationCandidates(ctx context.Context, artistID int64, candidates []ArtistRelationCandidate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_relation_candidates WHERE artist_id=? AND status='candidate'`, artistID); err != nil {
		return err
	}
	for _, v := range candidates {
		evidence, _ := json.Marshal(v.Evidence)
		direction := v.Direction
		if direction == "" {
			direction = "forward"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO artist_relation_candidates(artist_id,source,external_id,related_external_id,related_name,relation_type,direction,target_artist_id,score,evidence_json,payload_json,status) VALUES(?,?,?,?,?,?,?,NULLIF(?,0),?,?,?,'candidate') ON CONFLICT(artist_id,source,external_id,related_external_id,relation_type,direction) DO UPDATE SET related_name=excluded.related_name,target_artist_id=excluded.target_artist_id,score=excluded.score,evidence_json=excluded.evidence_json,payload_json=excluded.payload_json,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, artistID, v.Source, v.ExternalID, v.RelatedExternalID, v.RelatedName, v.RelationType, direction, v.TargetArtistID, v.Score, string(evidence), rawOrEmpty(v.Payload))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ArtistRelationCandidates(ctx context.Context, artistID int64) ([]ArtistRelationCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,artist_id,source,external_id,related_external_id,related_name,relation_type,direction,COALESCE(target_artist_id,0),score,evidence_json,payload_json,status FROM artist_relation_candidates WHERE artist_id=? ORDER BY status='confirmed' DESC,score DESC,id`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ArtistRelationCandidate
	for rows.Next() {
		var v ArtistRelationCandidate
		var evidence, payload string
		if err = rows.Scan(&v.ID, &v.ArtistID, &v.Source, &v.ExternalID, &v.RelatedExternalID, &v.RelatedName, &v.RelationType, &v.Direction, &v.TargetArtistID, &v.Score, &evidence, &payload, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &v.Evidence)
		v.Payload = json.RawMessage(payload)
		values = append(values, v)
	}
	return values, rows.Err()
}

// PendingWorkMatchCandidates returns all status='candidate' work matches in
// a single query (N+1 fix for the enrichment review page).
func (s *Store) PendingWorkMatchCandidates(ctx context.Context, workIDs ...int64) (map[int64][]WorkMatchCandidate, error) {
	query := `SELECT id,work_id,source,external_id,title,COALESCE(original_title,''),COALESCE(translated_title,''),COALESCE(type,''),COALESCE(year,0),COALESCE(page_url,''),COALESCE(poster_url,''),score,evidence_json,payload_json,status FROM work_match_candidates WHERE status='candidate'`
	var args []any
	if len(workIDs) > 0 {
		query += ` AND work_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(workIDs)), ",") + `)`
		for _, id := range workIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY score DESC,id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64][]WorkMatchCandidate{}
	for rows.Next() {
		var v WorkMatchCandidate
		var evidence, payload string
		if err = rows.Scan(&v.ID, &v.WorkID, &v.Source, &v.ExternalID, &v.Title, &v.OriginalTitle, &v.TranslatedTitle, &v.Type, &v.Year, &v.PageURL, &v.PosterURL, &v.Score, &evidence, &payload, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &v.Evidence)
		v.Payload = json.RawMessage(payload)
		result[v.WorkID] = append(result[v.WorkID], v)
	}
	return result, rows.Err()
}

// PendingArtistRelationCandidates returns all status='candidate' artist
// relations in a single query (N+1 fix for the enrichment review page).
func (s *Store) PendingArtistRelationCandidates(ctx context.Context, artistIDs ...int64) (map[int64][]ArtistRelationCandidate, error) {
	query := `SELECT id,artist_id,source,external_id,related_external_id,related_name,relation_type,direction,COALESCE(target_artist_id,0),score,evidence_json,payload_json,status FROM artist_relation_candidates WHERE status='candidate'`
	var args []any
	if len(artistIDs) > 0 {
		query += ` AND artist_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(artistIDs)), ",") + `)`
		for _, id := range artistIDs {
			args = append(args, id)
		}
	}
	query += ` ORDER BY score DESC,id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64][]ArtistRelationCandidate{}
	for rows.Next() {
		var v ArtistRelationCandidate
		var evidence, payload string
		if err = rows.Scan(&v.ID, &v.ArtistID, &v.Source, &v.ExternalID, &v.RelatedExternalID, &v.RelatedName, &v.RelationType, &v.Direction, &v.TargetArtistID, &v.Score, &evidence, &payload, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &v.Evidence)
		v.Payload = json.RawMessage(payload)
		result[v.ArtistID] = append(result[v.ArtistID], v)
	}
	return result, rows.Err()
}

func (s *Store) SetArtistRelationCandidateStatus(ctx context.Context, artistID, candidateID int64, status string) error {
	if status != "confirmed" && status != "rejected" {
		return fmt.Errorf("invalid artist relation status %q", status)
	}
	r, err := s.db.ExecContext(ctx, `UPDATE artist_relation_candidates SET status=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND artist_id=?`, status, candidateID, artistID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
