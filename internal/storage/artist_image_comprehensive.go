package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ArtistImageSettings controls the multi-tier artist avatar backfill and AI
// audit pipeline: Tier 1-2 structured sources (Bangumi, Deezer, Wikipedia,
// Fanart.tv), Tier 3 web search + LLM selection, Tier 4 VLM vision check, and
// full-library historical avatar AI audit/replacement.
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
	AuditAutoConfidence  int
}

// ArtistImageCandidate is a pending or reviewed candidate avatar discovered by
// web search + AI selection, manual lookup, or an AI audit review item.
type ArtistImageCandidate struct {
	ID              int64
	ArtistID        int64
	ArtistName      string
	Source          string
	ImageURL        string
	PageURL         string
	Title           string
	Confidence      string
	Reason          string
	Width           int
	Height          int
	Status          string
	Kind            string // "replacement" | "audit_review"
	IssueType       string
	ConfidenceScore int
	CurrentImageURL string
	CreatedAt       string
	ReviewedAt      string
}

// ArtistEffectiveCacheRow represents a single persisted artist_image_cache row
// along with its owning artist ID (canonical artist or merged-in source).
type ArtistEffectiveCacheRow struct {
	OwnerArtistID int64  `json:"ownerArtistId"`
	Source        string `json:"source"`
	RemoteURL     string `json:"remoteUrl"`
	ContentHash   string `json:"contentHash"`
	MIMEType      string `json:"mimeType"`
	CachePath     string `json:"cachePath"`
	ByteSize      int64  `json:"byteSize"`
}

// ArtistImageReview records the latest AI or manual review verdict for an
// artist's cached avatar content hash to avoid redundant re-audits.
type ArtistImageReview struct {
	ArtistID   int64
	ImageHash  string
	Verdict    string // "keep" | "replace"
	IssueType  string
	Confidence int
	Reason     string
	Model      string
	ReviewedAt string
}

// ArtistImageRejectionSet holds URLs and content hashes previously rejected
// for an artist across all tiers.
type ArtistImageRejectionSet struct {
	URLs   map[string]bool
	Hashes map[string]bool
}

// ArtistImageAuditAction records an automatic (or manual audit) replacement or
// clearance along with a lossless snapshot of previous cache rows for undo.
type ArtistImageAuditAction struct {
	ID             int64                     `json:"id"`
	RunID          int64                     `json:"runId"`
	ArtistID       int64                     `json:"artistId"`
	ArtistName     string                    `json:"artistName"`
	Action         string                    `json:"action"` // "replaced" | "cleared"
	IssueType      string                    `json:"issueType"`
	Confidence     int                       `json:"confidence"`
	Reason         string                    `json:"reason"`
	Model          string                    `json:"model"`
	PrevCache      []ArtistEffectiveCacheRow `json:"prevCache"`
	NewSource      string                    `json:"newSource"`
	NewRemoteURL   string                    `json:"newRemoteUrl"`
	NewContentHash string                    `json:"newContentHash"`
	NewCachePath   string                    `json:"newCachePath"`
	Reverted       bool                      `json:"reverted"`
	CreatedAt      string                    `json:"createdAt"`
	RevertedAt     string                    `json:"revertedAt,omitempty"`
}

// ArtistImageAuditActionInput carries metadata recorded alongside an automatic
// audit replacement or clearance.
type ArtistImageAuditActionInput struct {
	RunID      int64
	IssueType  string
	Confidence int
	Reason     string
	Model      string
}

// NormalizeArtistImageBackfillMode normalizes a backfill mode to one of
// "matched", "all", "web_fallback", "rescan_all", "audit_all", or "audit_force".
func NormalizeArtistImageBackfillMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "all", "web_fallback", "rescan_all", "audit_all", "audit_force":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "matched"
	}
}

// IsArtistImageAuditMode reports whether mode audits existing cached avatars.
func IsArtistImageAuditMode(mode string) bool {
	m := NormalizeArtistImageBackfillMode(mode)
	return m == "audit_all" || m == "audit_force"
}

func (s *Store) ArtistImageSettings(ctx context.Context) (ArtistImageSettings, error) {
	var v ArtistImageSettings
	var bangumi, deezer, wiki, fanart, webFB, webAuto, vision int
	err := s.db.QueryRowContext(ctx, `SELECT bangumi_enabled,deezer_enabled,wikipedia_enabled,fanart_enabled,fanart_api_key,web_fallback_enabled,web_auto_apply_high,vision_enabled,vision_model,ai_model,max_web_requests_per_run,negative_cache_days,audit_auto_confidence FROM artist_image_settings WHERE id=1`).Scan(
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
		&v.AuditAutoConfidence,
	)
	v.BangumiEnabled = bangumi != 0
	v.DeezerEnabled = deezer != 0
	v.WikipediaEnabled = wiki != 0
	v.FanartEnabled = fanart != 0
	v.WebFallbackEnabled = webFB != 0
	v.WebAutoApplyHigh = webAuto != 0
	v.VisionEnabled = vision != 0
	if v.AuditAutoConfidence < 50 || v.AuditAutoConfidence > 100 {
		v.AuditAutoConfidence = 85
	}
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
	if v.AuditAutoConfidence == 0 {
		v.AuditAutoConfidence = 85
	}
	if v.AuditAutoConfidence < 50 {
		v.AuditAutoConfidence = 50
	}
	if v.AuditAutoConfidence > 100 {
		v.AuditAutoConfidence = 100
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artist_image_settings SET bangumi_enabled=?,deezer_enabled=?,wikipedia_enabled=?,fanart_enabled=?,fanart_api_key=?,web_fallback_enabled=?,web_auto_apply_high=?,vision_enabled=?,vision_model=?,ai_model=?,max_web_requests_per_run=?,negative_cache_days=?,audit_auto_confidence=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`,
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
		v.AuditAutoConfidence,
	)
	return err
}

// CreateArtistImageBackfillRunWithMode creates a durable artist image backfill
// or audit run tagged with the requested execution mode.
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

func buildArtistImageBackfillCacheScanQuery(mode string, webFallbackEnabled bool) string {
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
		if mode == "web_fallback" || webFallbackEnabled {
			cooldownClause += ` AND att.tier_reached='web_fallback'`
		}
		cooldownClause += ")"
	}
	return `SELECT a.id,COALESCE(a.user_display_name,a.display_name),COALESCE((SELECT c.cache_path FROM artist_image_cache c WHERE c.artist_id=a.id LIMIT 1),(SELECT c.cache_path FROM artists ma JOIN artist_image_cache c ON c.artist_id=ma.id WHERE ma.merged_into_artist_id=a.id ORDER BY c.fetched_at DESC LIMIT 1),'') FROM artists a WHERE a.merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)` + extraFilter + cooldownClause + ` ORDER BY a.id`
}

func buildArtistImageAuditScanQuery(force bool) string {
	pendingFilter := ""
	if !force {
		pendingFilter = ` AND NOT EXISTS(SELECT 1 FROM artist_image_candidates cand WHERE cand.artist_id=a.id AND cand.status='pending')`
	}
	return `SELECT a.id,COALESCE(a.user_display_name,a.display_name),` +
		`COALESCE((SELECT c.cache_path FROM artist_image_cache c WHERE c.artist_id=a.id LIMIT 1),(SELECT c.cache_path FROM artists ma JOIN artist_image_cache c ON c.artist_id=ma.id WHERE ma.merged_into_artist_id=a.id ORDER BY c.fetched_at DESC LIMIT 1),''),` +
		`COALESCE((SELECT c.content_hash FROM artist_image_cache c WHERE c.artist_id=a.id LIMIT 1),(SELECT c.content_hash FROM artists ma JOIN artist_image_cache c ON c.artist_id=ma.id WHERE ma.merged_into_artist_id=a.id ORDER BY c.fetched_at DESC LIMIT 1),''),` +
		`COALESCE((SELECT r.image_hash FROM artist_image_reviews r WHERE r.artist_id=a.id AND r.verdict='keep'),''),` +
		`EXISTS(SELECT 1 FROM artist_image_attempts att WHERE att.artist_id=a.id AND att.cooldown_until>strftime('%Y-%m-%dT%H:%M:%fZ','now') AND att.tier_reached='web_fallback')` +
		` FROM artists a WHERE a.merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)` + pendingFilter + ` ORDER BY a.id`
}

// ArtistImageBackfillCacheScanForMode returns backfill or audit candidates and
// their current cache metadata according to mode:
//   - "matched": confirmed external profile artists only (legacy B+2 scope)
//   - "all" / "web_fallback": all unmerged artists without custom images,
//     skipping those still within negative-cache cooldown
//   - "rescan_all": all unmerged artists without custom images, ignoring cooldown
//   - "audit_all": all unmerged artists without custom images and without pending
//     review cards, returning hash + keep-review hash for idempotent auditing
//   - "audit_force": all unmerged artists without custom images, ignoring
//     previous keep reviews and cooldowns
func (s *Store) ArtistImageBackfillCacheScanForMode(ctx context.Context, mode string) ([]ArtistImageBackfillCacheRow, error) {
	mode = NormalizeArtistImageBackfillMode(mode)
	if mode == "audit_all" || mode == "audit_force" {
		rows, err := s.db.QueryContext(ctx, buildArtistImageAuditScanQuery(mode == "audit_force"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var list []ArtistImageBackfillCacheRow
		for rows.Next() {
			var v ArtistImageBackfillCacheRow
			var cooldown int
			if err = rows.Scan(&v.Candidate.ID, &v.Candidate.Name, &v.CachePath, &v.ContentHash, &v.ReviewedKeepHash, &cooldown); err != nil {
				return nil, err
			}
			v.MissingCooldown = cooldown != 0
			list = append(list, v)
		}
		return list, rows.Err()
	}

	webFallbackEnabled := false
	if mode != "rescan_all" && mode != "web_fallback" {
		if settings, err := s.ArtistImageSettings(ctx); err == nil {
			webFallbackEnabled = settings.WebFallbackEnabled
		}
	}
	query := buildArtistImageBackfillCacheScanQuery(mode, webFallbackEnabled)
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

// ArtistEffectiveImageCache resolves the effective cached avatar row for an
// artist (own row first, then latest merged-in source row).
func (s *Store) ArtistEffectiveImageCache(ctx context.Context, artistID int64) (ArtistEffectiveCacheRow, error) {
	return queryArtistEffectiveImageCache(ctx, s.db, artistID)
}

func queryArtistEffectiveImageCache(ctx context.Context, db queryRower, artistID int64) (ArtistEffectiveCacheRow, error) {
	var row ArtistEffectiveCacheRow
	err := db.QueryRowContext(ctx, `SELECT artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size
		FROM artist_image_cache
		WHERE artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)
		ORDER BY (artist_id=?) DESC, fetched_at DESC LIMIT 1`, artistID, artistID, artistID).
		Scan(&row.OwnerArtistID, &row.Source, &row.RemoteURL, &row.ContentHash, &row.MIMEType, &row.CachePath, &row.ByteSize)
	return row, err
}

func queryAllArtistImageCacheRows(ctx context.Context, tx *sql.Tx, artistID int64) ([]ArtistEffectiveCacheRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size
		FROM artist_image_cache
		WHERE artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)
		ORDER BY (artist_id=?) DESC, fetched_at DESC`, artistID, artistID, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ArtistEffectiveCacheRow
	for rows.Next() {
		var item ArtistEffectiveCacheRow
		if err = rows.Scan(&item.OwnerArtistID, &item.Source, &item.RemoteURL, &item.ContentHash, &item.MIMEType, &item.CachePath, &item.ByteSize); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, rows.Err()
}

func insertArtistImageRejectionTx(ctx context.Context, tx *sql.Tx, artistID int64, imageURL, contentHash, reason string) error {
	imageURL = strings.TrimSpace(imageURL)
	contentHash = strings.TrimSpace(contentHash)
	if imageURL == "" && contentHash == "" {
		return nil
	}
	if r := []rune(strings.TrimSpace(reason)); len(r) > 300 {
		reason = strings.TrimSpace(string(r[:300]))
	} else {
		reason = strings.TrimSpace(reason)
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_image_rejections WHERE artist_id=? AND image_url=? AND content_hash=?)`, artistID, imageURL, contentHash).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO artist_image_rejections(artist_id,image_url,content_hash,reason) VALUES(?,?,?,?)`, artistID, imageURL, contentHash, reason)
	return err
}

func upsertArtistImageReviewTx(ctx context.Context, tx *sql.Tx, r ArtistImageReview) error {
	r.ImageHash = strings.TrimSpace(r.ImageHash)
	if r.ArtistID <= 0 || r.ImageHash == "" {
		return nil
	}
	if r.Verdict != "keep" && r.Verdict != "replace" {
		return errors.New("invalid artist image review verdict")
	}
	if r.Confidence < 0 {
		r.Confidence = 0
	}
	if r.Confidence > 100 {
		r.Confidence = 100
	}
	if runes := []rune(strings.TrimSpace(r.Reason)); len(runes) > 500 {
		r.Reason = strings.TrimSpace(string(runes[:500]))
	} else {
		r.Reason = strings.TrimSpace(r.Reason)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO artist_image_reviews(artist_id,image_hash,verdict,issue_type,confidence,reason,model,reviewed_at)
		VALUES(?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(artist_id) DO UPDATE SET
			image_hash=excluded.image_hash,
			verdict=excluded.verdict,
			issue_type=excluded.issue_type,
			confidence=excluded.confidence,
			reason=excluded.reason,
			model=excluded.model,
			reviewed_at=excluded.reviewed_at`,
		r.ArtistID, r.ImageHash, r.Verdict, strings.TrimSpace(r.IssueType), r.Confidence, r.Reason, strings.TrimSpace(r.Model))
	return err
}

// SaveArtistImageReview persists a review verdict guarded by an optional
// durable backfill checkpoint and upfront write lock.
func (s *Store) SaveArtistImageReview(ctx context.Context, r ArtistImageReview, c *ArtistImageBackfillCheckpoint) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return err
	}
	if c != nil {
		if err = requireArtistImageFallbackCheckpoint(ctx, tx, c, r.ArtistID); err != nil {
			return err
		}
	}
	if err = upsertArtistImageReviewTx(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}

// ArtistImageReviewByArtist returns the latest review row for artistID, if any.
func (s *Store) ArtistImageReviewByArtist(ctx context.Context, artistID int64) (ArtistImageReview, bool, error) {
	var r ArtistImageReview
	err := s.db.QueryRowContext(ctx, `SELECT artist_id,image_hash,verdict,issue_type,confidence,reason,model,reviewed_at FROM artist_image_reviews WHERE artist_id=?`, artistID).
		Scan(&r.ArtistID, &r.ImageHash, &r.Verdict, &r.IssueType, &r.Confidence, &r.Reason, &r.Model, &r.ReviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtistImageReview{}, false, nil
	}
	return r, err == nil, err
}

// ArtistImageRejections loads both rejected URLs and rejected content hashes
// for artistID across artist_image_rejections and legacy rejected candidates.
func (s *Store) ArtistImageRejections(ctx context.Context, artistID int64) (ArtistImageRejectionSet, error) {
	out := ArtistImageRejectionSet{
		URLs:   map[string]bool{},
		Hashes: map[string]bool{},
	}
	rows, err := s.db.QueryContext(ctx, `SELECT image_url, content_hash FROM artist_image_rejections WHERE artist_id=?`, artistID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var u, h string
		if err = rows.Scan(&u, &h); err != nil {
			rows.Close()
			return out, err
		}
		if u = strings.TrimSpace(u); u != "" {
			out.URLs[u] = true
		}
		if h = strings.TrimSpace(h); h != "" {
			out.Hashes[h] = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	legacyURLs, err := s.RejectedArtistImageURLs(ctx, artistID)
	if err != nil {
		return out, err
	}
	for u := range legacyURLs {
		out.URLs[u] = true
	}
	return out, nil
}

// ReplaceArtistImageByAudit atomically snapshots prior cache rows, replaces
// artist_image_cache with the audited replacement image, blacklists the old
// image URL+hash, and records a keep-review for the new image hash.
func (s *Store) ReplaceArtistImageByAudit(ctx context.Context, checkpoint ArtistImageBackfillCheckpoint, image ArtistImageInput, audit ArtistImageAuditActionInput) (int64, error) {
	tx, err := s.beginArtistImageFallbackWrite(ctx, checkpoint, image.ArtistID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	prevRows, err := queryAllArtistImageCacheRows(ctx, tx, image.ArtistID)
	if err != nil {
		return 0, err
	}
	prevJSON, err := json.Marshal(prevRows)
	if err != nil {
		return 0, err
	}
	reason := strings.TrimSpace(audit.Reason)
	if runes := []rune(reason); len(runes) > 500 {
		reason = strings.TrimSpace(string(runes[:500]))
	}
	conf := audit.Confidence
	if conf < 0 {
		conf = 0
	}
	if conf > 100 {
		conf = 100
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO artist_image_audit_actions(run_id,artist_id,action,issue_type,confidence,reason,model,prev_cache_json,new_source,new_remote_url,new_content_hash,new_cache_path)
		VALUES(?,?,'replaced',?,?,?,?,?,?,?,?,?)`,
		audit.RunID, image.ArtistID, strings.TrimSpace(audit.IssueType), conf, reason, strings.TrimSpace(audit.Model),
		string(prevJSON), image.Source, image.RemoteURL, image.Hash, image.CachePath)
	if err != nil {
		return 0, err
	}
	actionID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_cache(artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET source=excluded.source,remote_url=excluded.remote_url,content_hash=excluded.content_hash,mime_type=excluded.mime_type,cache_path=excluded.cache_path,byte_size=excluded.byte_size,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		image.ArtistID, image.Source, image.RemoteURL, image.Hash, image.MIMEType, image.CachePath, image.ByteSize); err != nil {
		return 0, err
	}
	for _, old := range prevRows {
		if old.ContentHash == image.Hash && old.RemoteURL == image.RemoteURL {
			continue
		}
		if err = insertArtistImageRejectionTx(ctx, tx, image.ArtistID, old.RemoteURL, old.ContentHash, "audit_replaced:"+strings.TrimSpace(audit.IssueType)); err != nil {
			return 0, err
		}
	}
	if err = upsertArtistImageReviewTx(ctx, tx, ArtistImageReview{
		ArtistID:   image.ArtistID,
		ImageHash:  image.Hash,
		Verdict:    "keep",
		IssueType:  "none",
		Confidence: conf,
		Reason:     reason,
		Model:      audit.Model,
	}); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_attempts WHERE artist_id=?`, image.ArtistID); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_candidates SET status='rejected',reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE artist_id=? AND status='pending'`, image.ArtistID); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return actionID, nil
}

// ClearArtistImageByAudit atomically snapshots and clears all cached avatar
// rows for artistID and its merged-in source artists (1.A garbage policy),
// blacklists their URL+hash, and records a negative-cache cooldown.
func (s *Store) ClearArtistImageByAudit(ctx context.Context, checkpoint ArtistImageBackfillCheckpoint, artistID int64, audit ArtistImageAuditActionInput, cooldownDays int) (int64, error) {
	if cooldownDays < 1 {
		cooldownDays = 30
	}
	tx, err := s.beginArtistImageFallbackWrite(ctx, checkpoint, artistID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	actionID, err := clearArtistImageRowsTx(ctx, tx, artistID, audit, cooldownDays)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return actionID, nil
}

func clearArtistImageRowsTx(ctx context.Context, tx *sql.Tx, artistID int64, audit ArtistImageAuditActionInput, cooldownDays int) (int64, error) {
	prevRows, err := queryAllArtistImageCacheRows(ctx, tx, artistID)
	if err != nil {
		return 0, err
	}
	if len(prevRows) == 0 {
		return 0, nil
	}
	prevJSON, err := json.Marshal(prevRows)
	if err != nil {
		return 0, err
	}
	reason := strings.TrimSpace(audit.Reason)
	if runes := []rune(reason); len(runes) > 500 {
		reason = strings.TrimSpace(string(runes[:500]))
	}
	conf := audit.Confidence
	if conf < 0 {
		conf = 0
	}
	if conf > 100 {
		conf = 100
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO artist_image_audit_actions(run_id,artist_id,action,issue_type,confidence,reason,model,prev_cache_json)
		VALUES(?,?,'cleared',?,?,?,?,?)`,
		audit.RunID, artistID, strings.TrimSpace(audit.IssueType), conf, reason, strings.TrimSpace(audit.Model), string(prevJSON))
	if err != nil {
		return 0, err
	}
	actionID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_cache WHERE artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)`, artistID, artistID); err != nil {
		return 0, err
	}
	for _, old := range prevRows {
		if err = insertArtistImageRejectionTx(ctx, tx, artistID, old.RemoteURL, old.ContentHash, "audit_cleared:"+strings.TrimSpace(audit.IssueType)); err != nil {
			return 0, err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_reviews WHERE artist_id=?`, artistID); err != nil {
		return 0, err
	}
	if cooldownDays > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_attempts(artist_id,tier_reached,last_outcome,last_error,checked_at,cooldown_until)
			VALUES(?,'web_fallback','cleared',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now',?))
			ON CONFLICT(artist_id) DO UPDATE SET tier_reached=excluded.tier_reached,last_outcome=excluded.last_outcome,last_error=excluded.last_error,checked_at=excluded.checked_at,cooldown_until=excluded.cooldown_until`,
			artistID, reason, fmt.Sprintf("+%d days", cooldownDays)); err != nil {
			return 0, err
		}
	}
	return actionID, nil
}

// RevertArtistImageAuditAction restores the previous cached avatar row(s) from
// an audit action snapshot, removes their rejection entries, and pins the
// restored hash as manually kept so subsequent audit_all runs skip it.
func (s *Store) RevertArtistImageAuditAction(ctx context.Context, actionID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_audit_actions SET id=id WHERE 0`); err != nil {
		return err
	}
	var artistID int64
	var action, prevJSON string
	var reverted int
	err = tx.QueryRowContext(ctx, `SELECT artist_id,action,prev_cache_json,reverted FROM artist_image_audit_actions WHERE id=?`, actionID).
		Scan(&artistID, &action, &prevJSON, &reverted)
	if err != nil {
		return err
	}
	if reverted != 0 {
		return ErrArtistImageBackfillState
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=? AND merged_into_artist_id IS NULL)`, artistID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrArtistImageBackfillState
	}
	var custom bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=? OR ci.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?))`, artistID, artistID).Scan(&custom); err != nil {
		return err
	}
	if custom {
		return ErrArtistImageBackfillState
	}

	var prevRows []ArtistEffectiveCacheRow
	if err = json.Unmarshal([]byte(prevJSON), &prevRows); err != nil {
		return fmt.Errorf("parse audit snapshot: %w", err)
	}
	for _, row := range prevRows {
		if row.CachePath == "" {
			return errors.New("旧头像缓存文件路径缺失，无法撤销")
		}
		if _, statErr := os.Stat(row.CachePath); statErr != nil {
			return errors.New("旧头像缓存文件已不存在于磁盘，无法撤销")
		}
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_cache WHERE artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)`, artistID, artistID); err != nil {
		return err
	}
	var primaryHash string
	for i, row := range prevRows {
		ownerID := row.OwnerArtistID
		if ownerID <= 0 {
			ownerID = artistID
		}
		var ownerExists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=?)`, ownerID).Scan(&ownerExists); err != nil {
			return err
		}
		if !ownerExists {
			ownerID = artistID
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_cache(artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET source=excluded.source,remote_url=excluded.remote_url,content_hash=excluded.content_hash,mime_type=excluded.mime_type,cache_path=excluded.cache_path,byte_size=excluded.byte_size,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			ownerID, row.Source, row.RemoteURL, row.ContentHash, row.MIMEType, row.CachePath, row.ByteSize); err != nil {
			return err
		}
		if i == 0 {
			primaryHash = row.ContentHash
		}
		if row.RemoteURL != "" {
			if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_rejections WHERE artist_id=? AND image_url=?`, artistID, row.RemoteURL); err != nil {
				return err
			}
		}
		if row.ContentHash != "" {
			if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_rejections WHERE artist_id=? AND content_hash=?`, artistID, row.ContentHash); err != nil {
				return err
			}
		}
	}
	if primaryHash != "" {
		if err = upsertArtistImageReviewTx(ctx, tx, ArtistImageReview{
			ArtistID:   artistID,
			ImageHash:  primaryHash,
			Verdict:    "keep",
			IssueType:  "none",
			Confidence: 100,
			Reason:     "人工撤销恢复保留",
			Model:      "manual_revert",
		}); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_attempts WHERE artist_id=?`, artistID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_audit_actions SET reverted=1,reverted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, actionID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecentArtistImageAuditActions lists recent non-reverted audit replacement
// and clearance actions for admin review and one-click undo.
func (s *Store) RecentArtistImageAuditActions(ctx context.Context, limit int) ([]ArtistImageAuditAction, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT act.id,COALESCE(act.run_id,0),act.artist_id,COALESCE(a.user_display_name,a.display_name),act.action,act.issue_type,act.confidence,act.reason,act.model,act.prev_cache_json,act.new_source,act.new_remote_url,act.new_content_hash,act.new_cache_path,act.reverted,act.created_at,COALESCE(act.reverted_at,'')
		FROM artist_image_audit_actions act
		JOIN artists a ON a.id=act.artist_id
		WHERE act.reverted=0 AND a.merged_into_artist_id IS NULL
		ORDER BY act.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistImageAuditAction
	for rows.Next() {
		var item ArtistImageAuditAction
		var prevRaw string
		var reverted int
		if err = rows.Scan(&item.ID, &item.RunID, &item.ArtistID, &item.ArtistName, &item.Action, &item.IssueType, &item.Confidence, &item.Reason, &item.Model, &prevRaw, &item.NewSource, &item.NewRemoteURL, &item.NewContentHash, &item.NewCachePath, &reverted, &item.CreatedAt, &item.RevertedAt); err != nil {
			return nil, err
		}
		item.Reverted = reverted != 0
		_ = json.Unmarshal([]byte(prevRaw), &item.PrevCache)
		out = append(out, item)
	}
	return out, rows.Err()
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
	var prevRows []ArtistEffectiveCacheRow
	if candidateID != 0 {
		prevRows, _ = queryAllArtistImageCacheRows(ctx, tx, image.ArtistID)
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
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_image_candidates WHERE artist_id=? AND image_url=? AND status='rejected') OR EXISTS(SELECT 1 FROM artist_image_rejections WHERE artist_id=? AND (image_url=? OR (content_hash<>'' AND content_hash=?)))`, image.ArtistID, image.RemoteURL, image.ArtistID, image.RemoteURL, image.Hash).Scan(&rejected); err != nil {
			return err
		}
		if rejected {
			return ErrArtistImageBackfillState
		}
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_cache(artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET source=excluded.source,remote_url=excluded.remote_url,content_hash=excluded.content_hash,mime_type=excluded.mime_type,cache_path=excluded.cache_path,byte_size=excluded.byte_size,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, image.ArtistID, image.Source, image.RemoteURL, image.Hash, image.MIMEType, image.CachePath, image.ByteSize); err != nil {
		return err
	}
	if candidateID != 0 {
		for _, old := range prevRows {
			if old.ContentHash == image.Hash && old.RemoteURL == image.RemoteURL {
				continue
			}
			if err = insertArtistImageRejectionTx(ctx, tx, image.ArtistID, old.RemoteURL, old.ContentHash, "manual_replaced"); err != nil {
				return err
			}
		}
		if err = upsertArtistImageReviewTx(ctx, tx, ArtistImageReview{
			ArtistID:   image.ArtistID,
			ImageHash:  image.Hash,
			Verdict:    "keep",
			IssueType:  "none",
			Confidence: 100,
			Reason:     "人工采纳候选头像",
			Model:      "manual_approve",
		}); err != nil {
			return err
		}
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
	c.IssueType = strings.TrimSpace(c.IssueType)
	if c.Kind != "audit_review" {
		c.Kind = "replacement"
	}
	if c.Confidence != "high" && c.Confidence != "medium" && c.Confidence != "low" {
		c.Confidence = "medium"
	}
	if c.ConfidenceScore < 0 {
		c.ConfidenceScore = 0
	}
	if c.ConfidenceScore > 100 {
		c.ConfidenceScore = 100
	}
	if runes := []rune(c.Reason); len(runes) > 500 {
		c.Reason = strings.TrimSpace(string(runes[:500]))
	}
	if c.ArtistID <= 0 || c.ImageURL == "" || c.Source == "" {
		return 0, errors.New("invalid artist image candidate")
	}
	var id int64
	err := db.QueryRowContext(ctx, `INSERT INTO artist_image_candidates(artist_id,source,image_url,page_url,title,confidence,reason,width,height,kind,issue_type,confidence_score)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(artist_id,image_url) DO UPDATE SET
		source=excluded.source,page_url=excluded.page_url,title=excluded.title,confidence=excluded.confidence,reason=excluded.reason,width=excluded.width,height=excluded.height,kind=excluded.kind,issue_type=excluded.issue_type,confidence_score=excluded.confidence_score
		WHERE artist_image_candidates.status='pending' RETURNING id`,
		c.ArtistID, c.Source, c.ImageURL, c.PageURL, c.Title, c.Confidence, c.Reason, c.Width, c.Height, c.Kind, c.IssueType, c.ConfidenceScore).Scan(&id)
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
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.artist_id,COALESCE(a.user_display_name,a.display_name),c.source,c.image_url,c.page_url,c.title,c.confidence,c.reason,c.width,c.height,c.status,c.kind,c.issue_type,c.confidence_score,`+artistImageURLSQL("a")+`,c.created_at,COALESCE(c.reviewed_at,'')`+pendingArtistImageCandidates+` ORDER BY c.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistImageCandidate
	for rows.Next() {
		var item ArtistImageCandidate
		if err = rows.Scan(&item.ID, &item.ArtistID, &item.ArtistName, &item.Source, &item.ImageURL, &item.PageURL, &item.Title, &item.Confidence, &item.Reason, &item.Width, &item.Height, &item.Status, &item.Kind, &item.IssueType, &item.ConfidenceScore, &item.CurrentImageURL, &item.CreatedAt, &item.ReviewedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ArtistImageCandidateByID(ctx context.Context, id int64) (ArtistImageCandidate, error) {
	var item ArtistImageCandidate
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.artist_id,COALESCE(a.user_display_name,a.display_name),c.source,c.image_url,c.page_url,c.title,c.confidence,c.reason,c.width,c.height,c.status,c.kind,c.issue_type,c.confidence_score,`+artistImageURLSQL("a")+`,c.created_at,COALESCE(c.reviewed_at,'') FROM artist_image_candidates c JOIN artists a ON a.id=c.artist_id WHERE c.id=?`, id).
		Scan(&item.ID, &item.ArtistID, &item.ArtistName, &item.Source, &item.ImageURL, &item.PageURL, &item.Title, &item.Confidence, &item.Reason, &item.Width, &item.Height, &item.Status, &item.Kind, &item.IssueType, &item.ConfidenceScore, &item.CurrentImageURL, &item.CreatedAt, &item.ReviewedAt)
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
	var imageURL, kind string
	err = tx.QueryRowContext(ctx, `UPDATE artist_image_candidates SET status=?,reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='pending' RETURNING artist_id,image_url,kind`, status, id).Scan(&artistID, &imageURL, &kind)
	if err != nil {
		return err
	}
	if status == "rejected" {
		if kind == "replacement" && strings.TrimSpace(imageURL) != "" {
			if err = insertArtistImageRejectionTx(ctx, tx, artistID, imageURL, "", "manual_rejected"); err != nil {
				return err
			}
		}
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

// KeepArtistImageCandidate resolves a pending audit_review item by confirming
// the artist's existing cached avatar as kept so future audit_all runs skip it.
func (s *Store) KeepArtistImageCandidate(ctx context.Context, candidateID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var artistID int64
	if err = tx.QueryRowContext(ctx, `UPDATE artist_image_candidates SET status='rejected',reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='pending' RETURNING artist_id`, candidateID).Scan(&artistID); err != nil {
		return err
	}
	if eff, e := queryArtistEffectiveImageCache(ctx, tx, artistID); e == nil && eff.ContentHash != "" {
		if err = upsertArtistImageReviewTx(ctx, tx, ArtistImageReview{
			ArtistID:   artistID,
			ImageHash:  eff.ContentHash,
			Verdict:    "keep",
			IssueType:  "none",
			Confidence: 100,
			Reason:     "人工确认保留原图",
			Model:      "manual_keep",
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearArtistImageCandidate resolves a pending audit_review item by clearing
// the artist's cached avatar (with a reversible audit snapshot).
func (s *Store) ClearArtistImageCandidate(ctx context.Context, candidateID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_cache SET artist_id=artist_id WHERE 0`); err != nil {
		return err
	}
	var artistID int64
	var issueType, reason string
	var confScore int
	if err = tx.QueryRowContext(ctx, `UPDATE artist_image_candidates SET status='approved',reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='pending' RETURNING artist_id,issue_type,reason,confidence_score`, candidateID).Scan(&artistID, &issueType, &reason, &confScore); err != nil {
		return err
	}
	var custom bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=? OR ci.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?))`, artistID, artistID).Scan(&custom); err != nil {
		return err
	}
	if custom {
		return ErrArtistImageBackfillState
	}
	var cooldownDays int
	if stErr := tx.QueryRowContext(ctx, `SELECT negative_cache_days FROM artist_image_settings WHERE id=1`).Scan(&cooldownDays); stErr != nil || cooldownDays < 1 {
		cooldownDays = 30
	}
	if _, err = clearArtistImageRowsTx(ctx, tx, artistID, ArtistImageAuditActionInput{
		IssueType:  issueType,
		Confidence: confScore,
		Reason:     reason,
		Model:      "manual_clear",
	}, cooldownDays); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_candidates SET status='rejected',reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE artist_id=? AND id<>? AND status='pending'`, artistID, candidateID); err != nil {
		return err
	}
	return tx.Commit()
}

// RejectedArtistImageURLs returns URLs previously rejected for artistID so web
// image discovery does not repeatedly select an already-rejected candidate.
func (s *Store) RejectedArtistImageURLs(ctx context.Context, artistID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT image_url FROM artist_image_candidates WHERE artist_id=? AND status='rejected' AND kind='replacement'`, artistID)
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
