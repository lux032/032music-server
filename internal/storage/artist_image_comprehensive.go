package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ArtistImageSettings controls the multi-tier artist avatar backfill pipeline:
// Tier 1-2 structured sources (Bangumi, Deezer, Wikipedia, Fanart.tv),
// Tier 3 web search + LLM selection, and Tier 4 optional VLM vision check.
type ArtistImageSettings struct {
	BangumiEnabled       bool
	DeezerEnabled        bool
	WikipediaEnabled     bool
	FanartEnabled        bool
	FanartAPIKey         string
	WebFallbackEnabled   bool
	WebAutoApplyHigh     bool
	VisionEnabled        bool
	VisionModel          string
	AIModel              string
	MaxWebRequestsPerRun int
	NegativeCacheDays    int
}

// ArtistImageCandidate is a pending or reviewed candidate avatar discovered by
// web search + AI selection (or manual single-artist web lookup).
type ArtistImageCandidate struct {
	ID         int64
	ArtistID   int64
	ArtistName string
	Source     string
	ImageURL   string
	PageURL    string
	Title      string
	Confidence string
	Reason     string
	Width      int
	Height     int
	Status     string
	CreatedAt  string
	ReviewedAt string
}

// NormalizeArtistImageBackfillMode normalizes a backfill mode to one of
// "matched", "all", "web_fallback", or "rescan_all".
func NormalizeArtistImageBackfillMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "all", "web_fallback", "rescan_all":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "matched"
	}
}

func (s *Store) ArtistImageSettings(ctx context.Context) (ArtistImageSettings, error) {
	var v ArtistImageSettings
	var bangumi, deezer, wiki, fanart, webFB, webAuto, vision int
	err := s.db.QueryRowContext(ctx, `SELECT bangumi_enabled,deezer_enabled,wikipedia_enabled,fanart_enabled,fanart_api_key,web_fallback_enabled,web_auto_apply_high,vision_enabled,vision_model,ai_model,max_web_requests_per_run,negative_cache_days FROM artist_image_settings WHERE id=1`).Scan(
		&bangumi,
		&deezer,
		&wiki,
		&fanart,
		&v.FanartAPIKey,
		&webFB,
		&webAuto,
		&vision,
		&v.VisionModel,
		&v.AIModel,
		&v.MaxWebRequestsPerRun,
		&v.NegativeCacheDays,
	)
	v.BangumiEnabled = bangumi != 0
	v.DeezerEnabled = deezer != 0
	v.WikipediaEnabled = wiki != 0
	v.FanartEnabled = fanart != 0
	v.WebFallbackEnabled = webFB != 0
	v.WebAutoApplyHigh = webAuto != 0
	v.VisionEnabled = vision != 0
	return v, err
}

func (s *Store) SaveArtistImageSettings(ctx context.Context, v ArtistImageSettings) error {
	v.FanartAPIKey = strings.TrimSpace(v.FanartAPIKey)
	v.VisionModel = strings.TrimSpace(v.VisionModel)
	v.AIModel = strings.TrimSpace(v.AIModel)
	if v.MaxWebRequestsPerRun < 0 {
		v.MaxWebRequestsPerRun = 0
	}
	if v.NegativeCacheDays < 1 {
		v.NegativeCacheDays = 30
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artist_image_settings SET bangumi_enabled=?,deezer_enabled=?,wikipedia_enabled=?,fanart_enabled=?,fanart_api_key=?,web_fallback_enabled=?,web_auto_apply_high=?,vision_enabled=?,vision_model=?,ai_model=?,max_web_requests_per_run=?,negative_cache_days=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`,
		boolInt(v.BangumiEnabled),
		boolInt(v.DeezerEnabled),
		boolInt(v.WikipediaEnabled),
		boolInt(v.FanartEnabled),
		v.FanartAPIKey,
		boolInt(v.WebFallbackEnabled),
		boolInt(v.WebAutoApplyHigh),
		boolInt(v.VisionEnabled),
		v.VisionModel,
		v.AIModel,
		v.MaxWebRequestsPerRun,
		v.NegativeCacheDays,
	)
	return err
}

// CreateArtistImageBackfillRunWithMode creates a durable artist image backfill
// run tagged with the requested execution mode.
func (s *Store) CreateArtistImageBackfillRunWithMode(ctx context.Context, mode string, items []ArtistImageBackfillCandidate) (int64, error) {
	if len(items) == 0 {
		return 0, ErrArtistImageBackfillState
	}
	mode = NormalizeArtistImageBackfillMode(mode)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return 0, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_image_backfill_runs WHERE status IN ('running','queued','paused')`).Scan(&active); err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, ErrArtistImageBackfillState
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO artist_image_backfill_runs(status,mode,total_items) VALUES('running',?,?)`, mode, len(items))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO artist_image_backfill_items(run_id,artist_id,artist_name) VALUES(?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, item := range items {
		if _, err = stmt.ExecContext(ctx, id, item.ID, item.Name); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// requireArtistImageFallbackCheckpoint keeps matched runs within their confirmed-identity
// scope even when an identity is removed while a source request is in flight.
func requireArtistImageFallbackCheckpoint(ctx context.Context, tx *sql.Tx, c *ArtistImageBackfillCheckpoint, artistID int64) error {
	if err := requireArtistImageBackfillCheckpoint(ctx, tx, c, artistID); err != nil || c == nil {
		return err
	}
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM artist_image_backfill_runs r JOIN artists a ON a.id=?
		WHERE r.id=? AND a.merged_into_artist_id IS NULL
		AND (r.mode<>'matched' OR EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=a.id))
		AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id)
		AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)
	)`, artistID, c.RunID).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrArtistImageBackfillState
	}
	return nil
}

func (s *Store) beginArtistImageFallbackWrite(ctx context.Context, c ArtistImageBackfillCheckpoint, artistID int64) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = artistImageBackfillWriteLock(ctx, tx); err == nil {
		err = requireArtistImageFallbackCheckpoint(ctx, tx, &c, artistID)
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// ReserveArtistImageWebRequest charges one attempt to the durable run before
// making the request, so restarting or resuming cannot reset the budget.
func (s *Store) ReserveArtistImageWebRequest(ctx context.Context, c ArtistImageBackfillCheckpoint, artistID int64, limit int) (bool, error) {
	tx, err := s.beginArtistImageFallbackWrite(ctx, c, artistID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET web_requests=web_requests+1 WHERE id=? AND (?=0 OR web_requests<?)`, c.RunID, limit, limit)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
}

// ArtistImageBackfillCacheScanForMode returns backfill candidates and their
// current cache paths according to mode:
//   - "matched": confirmed external profile artists only (legacy B+2 scope)
//   - "all" / "web_fallback": all unmerged artists without custom images,
//     skipping those still within negative-cache cooldown
//   - "rescan_all": all unmerged artists without custom images, ignoring cooldown
func (s *Store) ArtistImageBackfillCacheScanForMode(ctx context.Context, mode string) ([]ArtistImageBackfillCacheRow, error) {
	mode = NormalizeArtistImageBackfillMode(mode)
	var extraFilter string
	if mode == "matched" {
		extraFilter = ` AND EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=a.id)`
	}
	var cooldownClause string
	if mode != "rescan_all" {
		cooldownClause = ` AND NOT EXISTS(SELECT 1 FROM artist_image_candidates cand WHERE cand.artist_id=a.id AND cand.status='pending')`
		cooldownClause += ` AND NOT EXISTS(
			SELECT 1 FROM artist_image_attempts att
			WHERE att.artist_id=a.id AND att.cooldown_until>strftime('%Y-%m-%dT%H:%M:%fZ','now')`
		if mode == "web_fallback" {
			cooldownClause += ` AND att.tier_reached='web_fallback'`
		} else {
			cooldownClause += ` AND (att.tier_reached='web_fallback' OR (SELECT web_fallback_enabled FROM artist_image_settings WHERE id=1)=0)`
		}
		cooldownClause += ")"
	}
	query := `SELECT a.id,COALESCE(a.user_display_name,a.display_name),COALESCE((SELECT c.cache_path FROM artist_image_cache c WHERE c.artist_id=a.id LIMIT 1),(SELECT c.cache_path FROM artists ma JOIN artist_image_cache c ON c.artist_id=ma.id WHERE ma.merged_into_artist_id=a.id ORDER BY c.fetched_at DESC LIMIT 1),'') FROM artists a WHERE a.merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)` + extraFilter + cooldownClause + ` ORDER BY a.id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ArtistImageBackfillCacheRow
	for rows.Next() {
		var v ArtistImageBackfillCacheRow
		if err = rows.Scan(&v.Candidate.ID, &v.Candidate.Name, &v.CachePath); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// ArtistImageBackfillSubjectForMode checks real-time eligibility for a single
// artist under the given backfill mode.
func (s *Store) ArtistImageBackfillSubjectForMode(ctx context.Context, artistID int64, mode string) (ArtistImageBackfillCandidate, error) {
	mode = NormalizeArtistImageBackfillMode(mode)
	if mode == "matched" {
		return s.ArtistImageBackfillSubject(ctx, artistID)
	}
	var v ArtistImageBackfillCandidate
	err := s.db.QueryRowContext(ctx, `SELECT a.id,COALESCE(a.user_display_name,a.display_name) FROM artists a WHERE a.id=? AND a.merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)`, artistID).Scan(&v.ID, &v.Name)
	return v, err
}

// SaveArtistImageBackfillGuarded persists an automatically discovered avatar
// from Tier 1-4 sources (Bangumi, Deezer, Wikipedia, Fanart, Web/AI) inside a
// single transaction guarded by unmerged status, absence of custom images, and
// optional durable run checkpoint.
func (s *Store) SaveArtistImageBackfillGuarded(ctx context.Context, image ArtistImageInput, c *ArtistImageBackfillCheckpoint) error {
	return s.saveArtistImageBackfillGuarded(ctx, image, c, 0)
}

func (s *Store) ApproveArtistImageCandidate(ctx context.Context, id int64, image ArtistImageInput) error {
	return s.saveArtistImageBackfillGuarded(ctx, image, nil, id)
}

func (s *Store) saveArtistImageBackfillGuarded(ctx context.Context, image ArtistImageInput, c *ArtistImageBackfillCheckpoint, candidateID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_cache SET artist_id=artist_id WHERE 0`); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=? AND merged_into_artist_id IS NULL)`, image.ArtistID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrArtistImageBackfillState
	}
	var custom bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=? OR ci.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?))`, image.ArtistID, image.ArtistID).Scan(&custom); err != nil {
		return err
	}
	if custom {
		return ErrArtistImageBackfillState
	}
	if err = requireArtistImageFallbackCheckpoint(ctx, tx, c, image.ArtistID); err != nil {
		return err
	}
	if image.Source == "fanart" && candidateID == 0 {
		var hasMB bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_external_profiles WHERE artist_id=? AND source='musicbrainz')`, image.ArtistID).Scan(&hasMB); err != nil {
			return err
		}
		if !hasMB {
			return ErrArtistImageBackfillState
		}
	}
	if candidateID != 0 {
		res, reviewErr := tx.ExecContext(ctx, `UPDATE artist_image_candidates SET status='approved',reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND artist_id=? AND image_url=? AND status='pending'`, candidateID, image.ArtistID, image.RemoteURL)
		if reviewErr != nil {
			return reviewErr
		}
		n, reviewErr := res.RowsAffected()
		if reviewErr != nil {
			return reviewErr
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		if _, reviewErr = tx.ExecContext(ctx, `UPDATE artist_image_candidates SET status='rejected',reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE artist_id=? AND id<>? AND status='pending'`, image.ArtistID, candidateID); reviewErr != nil {
			return reviewErr
		}
	} else if image.Source == "ai_web" {
		var rejected bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_image_candidates WHERE artist_id=? AND image_url=? AND status='rejected')`, image.ArtistID, image.RemoteURL).Scan(&rejected); err != nil {
			return err
		}
		if rejected {
			return ErrArtistImageBackfillState
		}
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_cache(artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET source=excluded.source,remote_url=excluded.remote_url,content_hash=excluded.content_hash,mime_type=excluded.mime_type,cache_path=excluded.cache_path,byte_size=excluded.byte_size,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, image.ArtistID, image.Source, image.RemoteURL, image.Hash, image.MIMEType, image.CachePath, image.ByteSize); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_attempts WHERE artist_id=?`, image.ArtistID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordArtistImageBackfillMiss records a cooldown for an active backfill item.
func (s *Store) RecordArtistImageBackfillMiss(ctx context.Context, checkpoint ArtistImageBackfillCheckpoint, artistID int64, tierReached string, cooldownDays int) error {
	if cooldownDays < 1 {
		cooldownDays = 30
	}
	tx, err := s.beginArtistImageFallbackWrite(ctx, checkpoint, artistID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO artist_image_attempts(artist_id,tier_reached,last_outcome,last_error,checked_at,cooldown_until) VALUES(?,?,'no_url','',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now',?)) ON CONFLICT(artist_id) DO UPDATE SET tier_reached=excluded.tier_reached,last_outcome=excluded.last_outcome,last_error=excluded.last_error,checked_at=excluded.checked_at,cooldown_until=excluded.cooldown_until`,
		artistID, tierReached, fmt.Sprintf("+%d days", cooldownDays))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertArtistImageCandidate inserts or refreshes a candidate avatar in the
// admin review queue. It returns zero for candidates that have already been reviewed.
func (s *Store) UpsertArtistImageCandidate(ctx context.Context, c ArtistImageCandidate) (int64, error) {
	return upsertArtistImageCandidate(ctx, s.db, c)
}

// UpsertArtistImageBackfillCandidate queues a candidate while the item is still eligible.
func (s *Store) UpsertArtistImageBackfillCandidate(ctx context.Context, checkpoint ArtistImageBackfillCheckpoint, c ArtistImageCandidate) (int64, error) {
	tx, err := s.beginArtistImageFallbackWrite(ctx, checkpoint, c.ArtistID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := upsertArtistImageCandidate(ctx, tx, c)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func upsertArtistImageCandidate(ctx context.Context, db queryRower, c ArtistImageCandidate) (int64, error) {
	c.Source = strings.TrimSpace(c.Source)
	c.ImageURL = strings.TrimSpace(c.ImageURL)
	c.PageURL = strings.TrimSpace(c.PageURL)
	c.Title = strings.TrimSpace(c.Title)
	c.Reason = strings.TrimSpace(c.Reason)
	if c.Confidence != "high" && c.Confidence != "medium" && c.Confidence != "low" {
		c.Confidence = "medium"
	}
	if runes := []rune(c.Reason); len(runes) > 500 {
		c.Reason = strings.TrimSpace(string(runes[:500]))
	}
	if c.ArtistID <= 0 || c.ImageURL == "" || c.Source == "" {
		return 0, errors.New("invalid artist image candidate")
	}
	var id int64
	err := db.QueryRowContext(ctx, `INSERT INTO artist_image_candidates(artist_id,source,image_url,page_url,title,confidence,reason,width,height)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(artist_id,image_url) DO UPDATE SET
		source=excluded.source,page_url=excluded.page_url,title=excluded.title,confidence=excluded.confidence,reason=excluded.reason,width=excluded.width,height=excluded.height
		WHERE artist_image_candidates.status='pending' RETURNING id`,
		c.ArtistID, c.Source, c.ImageURL, c.PageURL, c.Title, c.Confidence, c.Reason, c.Width, c.Height).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

const pendingArtistImageCandidates = ` FROM artist_image_candidates c JOIN artists a ON a.id=c.artist_id WHERE c.status='pending' AND a.merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)`

func (s *Store) PendingArtistImageCandidateCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*)`+pendingArtistImageCandidates).Scan(&count)
	return count, err
}

func (s *Store) PendingArtistImageCandidates(ctx context.Context, limit int) ([]ArtistImageCandidate, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.artist_id,COALESCE(a.user_display_name,a.display_name),c.source,c.image_url,c.page_url,c.title,c.confidence,c.reason,c.width,c.height,c.status,c.created_at,COALESCE(c.reviewed_at,'')`+pendingArtistImageCandidates+` ORDER BY c.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistImageCandidate
	for rows.Next() {
		var item ArtistImageCandidate
		if err = rows.Scan(&item.ID, &item.ArtistID, &item.ArtistName, &item.Source, &item.ImageURL, &item.PageURL, &item.Title, &item.Confidence, &item.Reason, &item.Width, &item.Height, &item.Status, &item.CreatedAt, &item.ReviewedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ArtistImageCandidateByID(ctx context.Context, id int64) (ArtistImageCandidate, error) {
	var item ArtistImageCandidate
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.artist_id,COALESCE(a.user_display_name,a.display_name),c.source,c.image_url,c.page_url,c.title,c.confidence,c.reason,c.width,c.height,c.status,c.created_at,COALESCE(c.reviewed_at,'') FROM artist_image_candidates c JOIN artists a ON a.id=c.artist_id WHERE c.id=?`, id).Scan(&item.ID, &item.ArtistID, &item.ArtistName, &item.Source, &item.ImageURL, &item.PageURL, &item.Title, &item.Confidence, &item.Reason, &item.Width, &item.Height, &item.Status, &item.CreatedAt, &item.ReviewedAt)
	return item, err
}

func (s *Store) ReviewArtistImageCandidate(ctx context.Context, id int64, status string) error {
	if status != "approved" && status != "rejected" {
		return errors.New("invalid candidate review status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var artistID int64
	err = tx.QueryRowContext(ctx, `UPDATE artist_image_candidates SET status=?,reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='pending' RETURNING artist_id`, status, id).Scan(&artistID)
	if err != nil {
		return err
	}
	if status == "rejected" {
		var cooldownDays int
		if stErr := tx.QueryRowContext(ctx, `SELECT negative_cache_days FROM artist_image_settings WHERE id=1`).Scan(&cooldownDays); stErr != nil || cooldownDays < 1 {
			cooldownDays = 30
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_attempts(artist_id,tier_reached,last_outcome,last_error,checked_at,cooldown_until) VALUES(?,'web_fallback','rejected','',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now',?)) ON CONFLICT(artist_id) DO UPDATE SET tier_reached=excluded.tier_reached,last_outcome=excluded.last_outcome,last_error=excluded.last_error,checked_at=excluded.checked_at,cooldown_until=excluded.cooldown_until`,
			artistID, fmt.Sprintf("+%d days", cooldownDays)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RejectedArtistImageURLs returns URLs previously rejected for artistID so web
// image discovery does not repeatedly select an already-rejected candidate.
func (s *Store) RejectedArtistImageURLs(ctx context.Context, artistID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT image_url FROM artist_image_candidates WHERE artist_id=? AND status='rejected'`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			return nil, err
		}
		if u = strings.TrimSpace(u); u != "" {
			out[u] = true
		}
	}
	return out, rows.Err()
}

// ArtistConfirmedPageURLs returns confirmed external profile URLs for an artist
// (and merged-in source artists) to anchor web image discovery.
func (s *Store) ArtistConfirmedPageURLs(ctx context.Context, artistID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(page_url,'') FROM artist_external_profiles WHERE (artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)) AND COALESCE(page_url,'')<>'' ORDER BY (artist_id=?) DESC, source='musicbrainz' DESC`, artistID, artistID, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var urls []string
	seen := map[string]bool{}
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			return nil, err
		}
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls, rows.Err()
}

func (s *Store) ArtistExternalProfileRaw(ctx context.Context, artistID int64, source, expectedID string) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(raw_json,'') FROM artist_external_profiles WHERE artist_id=? AND source=? AND external_id=?`, artistID, source, expectedID).Scan(&raw)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, err
	}
	return json.RawMessage(raw), nil
}
