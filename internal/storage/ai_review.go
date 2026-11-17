package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
)

// AIReviewMode controls how AI participates in a review module.
const (
	AIReviewModeOff       = "off"
	AIReviewModeRecommend = "recommend"
	AIReviewModeAuto      = "auto"
)

// Target kinds stored in ai_review_decisions.
const (
	AITargetAlbumSubject     = "album_subject"
	AITargetTrackSubject     = "track_subject"
	AITargetWorkMatch        = "work_match"
	AITargetArtistMatch      = "artist_match"
	AITargetCreditSplit      = "credit_split"
	AITargetSeriesSuggestion = "series_suggestion"
)

// Decision actions stored in ai_review_decisions.
const (
	AIActionAutoConfirm     = "auto_confirm"
	AIActionRecommendAccept = "recommend_accept"
	AIActionRecommendReject = "recommend_reject"
	AIActionUncertain       = "uncertain"
)

type AIReviewSettings struct {
	Enabled          bool
	InlineEnabled    bool
	WebSearchEnabled bool
	Model            string
	AlbumMode        string
	TrackMode        string
	WorkMode         string
	ArtistMode       string
	CreditMode       string
	SeriesMode       string
	UpdatedAt        string
}

func DefaultAIReviewSettings() AIReviewSettings {
	return AIReviewSettings{
		InlineEnabled: true,
		AlbumMode:     AIReviewModeAuto,
		TrackMode:     AIReviewModeAuto,
		WorkMode:      AIReviewModeAuto,
		ArtistMode:    AIReviewModeAuto,
		CreditMode:    AIReviewModeRecommend,
		SeriesMode:    AIReviewModeRecommend,
	}
}

func (s AIReviewSettings) ModeForKind(kind string) string {
	if !s.Enabled {
		return AIReviewModeOff
	}
	switch kind {
	case AITargetAlbumSubject:
		return s.AlbumMode
	case AITargetTrackSubject:
		return s.TrackMode
	case AITargetWorkMatch:
		return s.WorkMode
	case AITargetArtistMatch:
		return s.ArtistMode
	case AITargetCreditSplit:
		return s.CreditMode
	case AITargetSeriesSuggestion:
		return s.SeriesMode
	default:
		return AIReviewModeOff
	}
}

func (s *Store) AIReviewSettings(ctx context.Context) (AIReviewSettings, error) {
	v := DefaultAIReviewSettings()
	var enabled, inline, web int
	err := s.db.QueryRowContext(ctx, `SELECT enabled,inline_enabled,web_search_enabled,model,album_mode,track_mode,work_mode,artist_mode,credit_mode,series_mode,updated_at FROM ai_review_settings WHERE id=1`).
		Scan(&enabled, &inline, &web, &v.Model, &v.AlbumMode, &v.TrackMode, &v.WorkMode, &v.ArtistMode, &v.CreditMode, &v.SeriesMode, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Enabled = enabled != 0
	v.InlineEnabled = inline != 0
	v.WebSearchEnabled = web != 0
	return v, nil
}

func validAIReviewMode(mode string, allowAuto bool) bool {
	switch mode {
	case AIReviewModeOff, AIReviewModeRecommend:
		return true
	case AIReviewModeAuto:
		return allowAuto
	default:
		return false
	}
}

func (s *Store) SaveAIReviewSettings(ctx context.Context, v AIReviewSettings) error {
	if !validAIReviewMode(v.AlbumMode, true) || !validAIReviewMode(v.TrackMode, true) ||
		!validAIReviewMode(v.WorkMode, true) || !validAIReviewMode(v.ArtistMode, true) ||
		!validAIReviewMode(v.CreditMode, false) || !validAIReviewMode(v.SeriesMode, true) {
		return errors.New("无效的 AI 审核策略取值")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_review_settings(id,enabled,inline_enabled,web_search_enabled,model,album_mode,track_mode,work_mode,artist_mode,credit_mode,series_mode,updated_at)
		VALUES(1,?,?,?,?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,inline_enabled=excluded.inline_enabled,web_search_enabled=excluded.web_search_enabled,
		model=excluded.model,album_mode=excluded.album_mode,track_mode=excluded.track_mode,work_mode=excluded.work_mode,
		artist_mode=excluded.artist_mode,credit_mode=excluded.credit_mode,series_mode=excluded.series_mode,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		boolInt(v.Enabled), boolInt(v.InlineEnabled), boolInt(v.WebSearchEnabled), strings.TrimSpace(v.Model),
		v.AlbumMode, v.TrackMode, v.WorkMode, v.ArtistMode, v.CreditMode, v.SeriesMode)
	return err
}

type AIReviewDecision struct {
	ID                  int64           `json:"id"`
	TargetKind          string          `json:"targetKind"`
	TargetID            int64           `json:"targetId"`
	SelectedCandidateID int64           `json:"selectedCandidateId"`
	Fingerprint         string          `json:"fingerprint"`
	Action              string          `json:"action"`
	Payload             json.RawMessage `json:"payload"`
	Confidence          int             `json:"confidence"`
	Reason              string          `json:"reason"`
	Evidence            []string        `json:"evidence"`
	WebSearched         bool            `json:"webSearched"`
	Model               string          `json:"model"`
	Applied             bool            `json:"applied"`
	AppliedSnapshot     json.RawMessage `json:"appliedSnapshot,omitempty"`
	Reverted            bool            `json:"reverted"`
	CreatedAt           string          `json:"createdAt"`
	UpdatedAt           string          `json:"updatedAt"`
}

func (s *Store) SaveAIReviewDecision(ctx context.Context, d AIReviewDecision) (int64, error) {
	evJSON, _ := json.Marshal(d.Evidence)
	if len(d.Evidence) == 0 {
		evJSON = []byte("[]")
	}
	payload := rawOrEmpty(d.Payload)
	snap := rawOrEmpty(d.AppliedSnapshot)
	if d.Confidence < 0 {
		d.Confidence = 0
	}
	if d.Confidence > 100 {
		d.Confidence = 100
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO ai_review_decisions(
		target_kind,target_id,selected_candidate_id,fingerprint,action,payload_json,
		confidence,reason,evidence_json,web_searched,model,applied,applied_snapshot_json,reverted,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(target_kind,target_id) DO UPDATE SET
			selected_candidate_id=excluded.selected_candidate_id,
			fingerprint=excluded.fingerprint,
			action=excluded.action,
			payload_json=excluded.payload_json,
			confidence=excluded.confidence,
			reason=excluded.reason,
			evidence_json=excluded.evidence_json,
			web_searched=excluded.web_searched,
			model=excluded.model,
			applied=excluded.applied,
			applied_snapshot_json=excluded.applied_snapshot_json,
			reverted=excluded.reverted,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		RETURNING id`,
		d.TargetKind, d.TargetID, d.SelectedCandidateID, d.Fingerprint, d.Action, payload,
		d.Confidence, d.Reason, string(evJSON), boolInt(d.WebSearched), d.Model,
		boolInt(d.Applied), snap, boolInt(d.Reverted)).Scan(&id)
	return id, err
}

func scanAIReviewDecision(scanner interface{ Scan(...any) error }) (AIReviewDecision, error) {
	var d AIReviewDecision
	var payload, ev, snap string
	var web, applied, reverted int
	err := scanner.Scan(&d.ID, &d.TargetKind, &d.TargetID, &d.SelectedCandidateID, &d.Fingerprint, &d.Action,
		&payload, &d.Confidence, &d.Reason, &ev, &web, &d.Model, &applied, &snap, &reverted, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return d, err
	}
	d.Payload = json.RawMessage(payload)
	d.AppliedSnapshot = json.RawMessage(snap)
	d.WebSearched = web != 0
	d.Applied = applied != 0
	d.Reverted = reverted != 0
	_ = json.Unmarshal([]byte(ev), &d.Evidence)
	return d, nil
}

const aiDecisionSelectCols = `id,target_kind,target_id,selected_candidate_id,fingerprint,action,payload_json,confidence,reason,evidence_json,web_searched,model,applied,applied_snapshot_json,reverted,created_at,updated_at`

func (s *Store) AIReviewDecisionByTarget(ctx context.Context, kind string, targetID int64) (AIReviewDecision, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+aiDecisionSelectCols+` FROM ai_review_decisions WHERE target_kind=? AND target_id=?`, kind, targetID)
	d, err := scanAIReviewDecision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AIReviewDecision{}, false, nil
	}
	return d, err == nil, err
}

func (s *Store) AIReviewDecisionByID(ctx context.Context, id int64) (AIReviewDecision, error) {
	return scanAIReviewDecision(s.db.QueryRowContext(ctx, `SELECT `+aiDecisionSelectCols+` FROM ai_review_decisions WHERE id=?`, id))
}

func (s *Store) AIReviewDecisionsForTargets(ctx context.Context, kind string, targetIDs []int64) (map[int64]AIReviewDecision, error) {
	out := map[int64]AIReviewDecision{}
	if len(targetIDs) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(targetIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+aiDecisionSelectCols+` FROM ai_review_decisions WHERE target_kind=? AND target_id IN (SELECT value FROM json_each(?))`, kind, string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanAIReviewDecision(rows)
		if err != nil {
			return nil, err
		}
		out[d.TargetID] = d
	}
	return out, rows.Err()
}

type AIReviewAppliedItem struct {
	Decision    AIReviewDecision
	TargetTitle string
	TargetURL   string
}

func (s *Store) RecentAIAppliedDecisions(ctx context.Context, kinds []string, limit int) ([]AIReviewAppliedItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(kinds)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+aiDecisionSelectCols+` FROM ai_review_decisions WHERE applied=1 AND reverted=0 AND target_kind IN (SELECT value FROM json_each(?)) ORDER BY updated_at DESC, id DESC LIMIT ?`, string(encoded), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var decisions []AIReviewDecision
	for rows.Next() {
		d, err := scanAIReviewDecision(rows)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, d)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	items := make([]AIReviewAppliedItem, 0, len(decisions))
	for _, d := range decisions {
		title, href := s.aiDecisionTargetSummary(ctx, d.TargetKind, d.TargetID)
		items = append(items, AIReviewAppliedItem{Decision: d, TargetTitle: title, TargetURL: href})
	}
	return items, nil
}

func (s *Store) aiDecisionTargetSummary(ctx context.Context, kind string, targetID int64) (string, string) {
	var title string
	switch kind {
	case AITargetAlbumSubject:
		_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(user_title,title) FROM albums WHERE id=?`, targetID).Scan(&title)
		if title == "" {
			title = fmt.Sprintf("专辑 #%d", targetID)
		}
		return title, fmt.Sprintf("/admin/albums/%d", targetID)
	case AITargetTrackSubject:
		var albumID int64
		_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(user_title,title),album_id FROM tracks WHERE id=?`, targetID).Scan(&title, &albumID)
		if title == "" {
			title = fmt.Sprintf("曲目 #%d", targetID)
		}
		if albumID > 0 {
			return title, fmt.Sprintf("/admin/albums/%d", albumID)
		}
		return title, "/admin/work-review?tab=tracks"
	case AITargetWorkMatch:
		_ = s.db.QueryRowContext(ctx, `SELECT title FROM works WHERE id=?`, targetID).Scan(&title)
		if title == "" {
			title = fmt.Sprintf("作品 #%d", targetID)
		}
		return title, fmt.Sprintf("/admin/works/%d", targetID)
	case AITargetArtistMatch, AITargetCreditSplit:
		_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=?`, targetID).Scan(&title)
		if title == "" {
			title = fmt.Sprintf("艺术家 #%d", targetID)
		}
		return title, fmt.Sprintf("/admin/artists/%d?view=profile", targetID)
	case AITargetSeriesSuggestion:
		return fmt.Sprintf("系列建议 #%d", targetID), "/admin/work-review?tab=series"
	default:
		return fmt.Sprintf("#%d", targetID), "/admin"
	}
}

// ---------------------------------------------------------------- runs

type AIReviewRun struct {
	ID           int64  `json:"id"`
	Scope        string `json:"scope"`
	Force        bool   `json:"force"`
	Status       string `json:"status"`
	Total        int    `json:"total"`
	Processed    int    `json:"processed"`
	AutoApplied  int    `json:"autoApplied"`
	Recommended  int    `json:"recommended"`
	Uncertain    int    `json:"uncertain"`
	Skipped      int    `json:"skipped"`
	Failed       int    `json:"failed"`
	CurrentLabel string `json:"currentLabel"`
	ErrorMessage string `json:"errorMessage"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	FinishedAt   string `json:"finishedAt"`
}

func scanAIReviewRun(scanner interface{ Scan(...any) error }) (AIReviewRun, error) {
	var r AIReviewRun
	var force int
	var finished sql.NullString
	err := scanner.Scan(&r.ID, &r.Scope, &force, &r.Status, &r.Total, &r.Processed, &r.AutoApplied,
		&r.Recommended, &r.Uncertain, &r.Skipped, &r.Failed, &r.CurrentLabel, &r.ErrorMessage,
		&r.CreatedAt, &r.UpdatedAt, &finished)
	if err != nil {
		return r, err
	}
	r.Force = force != 0
	r.FinishedAt = finished.String
	return r, nil
}

const aiRunSelectCols = `id,scope,force,status,total,processed,auto_applied,recommended,uncertain,skipped,failed,current_label,error_message,created_at,updated_at,finished_at`

func (s *Store) CreateAIReviewRun(ctx context.Context, scope string, force bool, total int) (AIReviewRun, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO ai_review_runs(scope,force,status,total) VALUES(?,?,'running',?) RETURNING id`, scope, boolInt(force), total).Scan(&id)
	if err != nil {
		return AIReviewRun{}, err
	}
	return s.AIReviewRunByID(ctx, id)
}

func (s *Store) AIReviewRunByID(ctx context.Context, id int64) (AIReviewRun, error) {
	return scanAIReviewRun(s.db.QueryRowContext(ctx, `SELECT `+aiRunSelectCols+` FROM ai_review_runs WHERE id=?`, id))
}

func (s *Store) ActiveAIReviewRun(ctx context.Context, scopes []string) (AIReviewRun, bool, error) {
	encoded, _ := json.Marshal(scopes)
	row := s.db.QueryRowContext(ctx, `SELECT `+aiRunSelectCols+` FROM ai_review_runs WHERE status IN ('running','paused') AND scope IN (SELECT value FROM json_each(?)) ORDER BY id DESC LIMIT 1`, string(encoded))
	r, err := scanAIReviewRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AIReviewRun{}, false, nil
	}
	return r, err == nil, err
}

func (s *Store) LatestAIReviewRun(ctx context.Context, scopes []string) (AIReviewRun, bool, error) {
	encoded, _ := json.Marshal(scopes)
	row := s.db.QueryRowContext(ctx, `SELECT `+aiRunSelectCols+` FROM ai_review_runs WHERE scope IN (SELECT value FROM json_each(?)) ORDER BY id DESC LIMIT 1`, string(encoded))
	r, err := scanAIReviewRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AIReviewRun{}, false, nil
	}
	return r, err == nil, err
}

func (s *Store) UpdateAIReviewRun(ctx context.Context, r AIReviewRun) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_review_runs SET total=?,processed=?,auto_applied=?,recommended=?,uncertain=?,skipped=?,failed=?,current_label=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		r.Total, r.Processed, r.AutoApplied, r.Recommended, r.Uncertain, r.Skipped, r.Failed, r.CurrentLabel, r.ID)
	return err
}

func (s *Store) FinishAIReviewRun(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_review_runs SET status=?,error_message=?,current_label='',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, status, errMsg, id)
	return err
}

func (s *Store) PauseAIReviewRun(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ai_review_runs SET status='paused',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='running'`, id)
	return err
}

func (s *Store) ResumeAIReviewRun(ctx context.Context, id int64, total int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE ai_review_runs SET status='running',total=?,processed=0,auto_applied=0,recommended=0,uncertain=0,skipped=0,failed=0,error_message='',finished_at=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='paused'`, total, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) RecoverAIReviewRuns(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE ai_review_runs SET status='cancelled',error_message='服务重启，任务已终止',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status IN ('running','paused')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------- context loaders

func HashJSONFingerprint(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:12])
}

type AlbumAIContextTrack struct {
	Title     string `json:"title"`
	TrackType string `json:"trackType"`
	Composer  string `json:"composer,omitempty"`
	Lyricist  string `json:"lyricist,omitempty"`
}

type AlbumAIContext struct {
	AlbumID     int64                   `json:"albumId"`
	Title       string                  `json:"title"`
	Artist      string                  `json:"artist"`
	ReleaseDate string                  `json:"releaseDate,omitempty"`
	TrackCount  int                     `json:"trackCount"`
	Tracks      []AlbumAIContextTrack   `json:"tracks"`
	Candidates  []AlbumSubjectCandidate `json:"candidates"`
	Fingerprint string                  `json:"-"`
}

func (s *Store) LoadAlbumAIContext(ctx context.Context, albumID int64) (AlbumAIContext, error) {
	var c AlbumAIContext
	c.AlbumID = albumID
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(a.user_title,a.title),COALESCE(a.user_performed_by,a.performed_by,`+bangumiAlbumArtistSQL+`,''),`+bangumiAlbumDateSQL+`,(SELECT COUNT(*) FROM tracks t WHERE t.album_id=a.id) FROM albums a WHERE a.id=?`, albumID).
		Scan(&c.Title, &c.Artist, &c.ReleaseDate, &c.TrackCount)
	if err != nil {
		return c, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(user_title,title),COALESCE(user_track_type,track_type,'regular'),COALESCE(user_composer,composer,''),COALESCE(lyricist,'') FROM tracks WHERE album_id=? ORDER BY COALESCE(user_disc_number,disc_number),COALESCE(user_track_number,track_number),id LIMIT 25`, albumID)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var t AlbumAIContextTrack
		if err = rows.Scan(&t.Title, &t.TrackType, &t.Composer, &t.Lyricist); err != nil {
			rows.Close()
			return c, err
		}
		c.Tracks = append(c.Tracks, t)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return c, err
	}
	rows.Close()

	all, err := s.AlbumSubjectCandidates(ctx, albumID)
	if err != nil {
		return c, err
	}
	for _, cand := range all {
		if cand.Status == "candidate" {
			cand.Payload = nil
			c.Candidates = append(c.Candidates, cand)
		}
	}
	c.Fingerprint = HashJSONFingerprint(c)
	return c, nil
}

type TrackAIContextWork struct {
	Title string `json:"title"`
	Type  string `json:"type"`
	Role  string `json:"role"`
}

type TrackAIContext struct {
	TrackID     int64                   `json:"trackId"`
	AlbumID     int64                   `json:"albumId"`
	Title       string                  `json:"title"`
	Artist      string                  `json:"artist"`
	AlbumTitle  string                  `json:"albumTitle"`
	ReleaseDate string                  `json:"releaseDate,omitempty"`
	TrackType   string                  `json:"trackType"`
	Composer    string                  `json:"composer,omitempty"`
	Lyricist    string                  `json:"lyricist,omitempty"`
	Arranger    string                  `json:"arranger,omitempty"`
	LyricsHead  string                  `json:"lyricsHead,omitempty"`
	AlbumWorks  []TrackAIContextWork    `json:"albumWorks,omitempty"`
	Candidates  []TrackSubjectCandidate `json:"candidates"`
	Fingerprint string                  `json:"-"`
}

func (s *Store) LoadTrackAIContext(ctx context.Context, trackID int64) (TrackAIContext, error) {
	var c TrackAIContext
	c.TrackID = trackID
	var lyrics string
	err := s.db.QueryRowContext(ctx, `SELECT t.album_id,COALESCE(t.user_title,t.title),`+bangumiTrackArtistSQL+`,COALESCE(a.user_title,a.title),`+bangumiAlbumDateSQL+`,COALESCE(t.user_track_type,t.track_type,'regular'),COALESCE(t.user_composer,t.composer,''),COALESCE(t.lyricist,''),COALESCE(t.arranger,''),COALESCE(t.lyrics,'') FROM tracks t JOIN albums a ON a.id=t.album_id WHERE t.id=?`, trackID).
		Scan(&c.AlbumID, &c.Title, &c.Artist, &c.AlbumTitle, &c.ReleaseDate, &c.TrackType, &c.Composer, &c.Lyricist, &c.Arranger, &lyrics)
	if err != nil {
		return c, err
	}
	runes := []rune(strings.TrimSpace(lyrics))
	if len(runes) > 220 {
		runes = runes[:220]
	}
	c.LyricsHead = string(runes)

	awRows, err := s.db.QueryContext(ctx, `SELECT w.title,w.type,aw.role FROM album_works aw JOIN works w ON w.id=aw.work_id WHERE aw.album_id=? ORDER BY aw.work_id`, c.AlbumID)
	if err != nil {
		return c, err
	}
	for awRows.Next() {
		var w TrackAIContextWork
		if err = awRows.Scan(&w.Title, &w.Type, &w.Role); err != nil {
			awRows.Close()
			return c, err
		}
		c.AlbumWorks = append(c.AlbumWorks, w)
	}
	if err = awRows.Err(); err != nil {
		awRows.Close()
		return c, err
	}
	awRows.Close()

	all, err := s.TrackSubjectCandidates(ctx, trackID)
	if err != nil {
		return c, err
	}
	for _, cand := range all {
		if cand.Status == "candidate" {
			cand.Payload = nil
			c.Candidates = append(c.Candidates, cand)
		}
	}
	c.Fingerprint = HashJSONFingerprint(c)
	return c, nil
}

type WorkAIContextAlbum struct {
	Title       string `json:"title"`
	ReleaseYear int    `json:"releaseYear,omitempty"`
	AlbumType   string `json:"albumType,omitempty"`
}

type WorkAIContext struct {
	WorkID          int64                `json:"workId"`
	Title           string               `json:"title"`
	TranslatedTitle string               `json:"translatedTitle,omitempty"`
	Type            string               `json:"type"`
	Year            int                  `json:"year,omitempty"`
	Origin          string               `json:"origin"`
	Albums          []WorkAIContextAlbum `json:"albums,omitempty"`
	SampleTracks    []string             `json:"sampleTracks,omitempty"`
	Candidates      []WorkMatchCandidate `json:"candidates"`
	Fingerprint     string               `json:"-"`
}

func (s *Store) LoadWorkAIContext(ctx context.Context, workID int64) (WorkAIContext, error) {
	var c WorkAIContext
	c.WorkID = workID
	err := s.db.QueryRowContext(ctx, `SELECT title,COALESCE(translated_title,''),type,COALESCE(year,0),origin FROM works WHERE id=?`, workID).
		Scan(&c.Title, &c.TranslatedTitle, &c.Type, &c.Year, &c.Origin)
	if err != nil {
		return c, err
	}
	aRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(a.user_title,a.title),COALESCE(a.user_release_year,a.release_year,0),COALESCE(a.user_album_type,a.album_type,'') FROM albums a WHERE EXISTS(SELECT 1 FROM album_works aw WHERE aw.album_id=a.id AND aw.work_id=?) OR EXISTS(SELECT 1 FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE t.album_id=a.id AND wt.work_id=?) ORDER BY a.id LIMIT 12`, workID, workID)
	if err != nil {
		return c, err
	}
	for aRows.Next() {
		var a WorkAIContextAlbum
		if err = aRows.Scan(&a.Title, &a.ReleaseYear, &a.AlbumType); err != nil {
			aRows.Close()
			return c, err
		}
		c.Albums = append(c.Albums, a)
	}
	if err = aRows.Err(); err != nil {
		aRows.Close()
		return c, err
	}
	aRows.Close()

	tRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(t.user_title,t.title) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE wt.work_id=? ORDER BY t.id LIMIT 15`, workID)
	if err != nil {
		return c, err
	}
	for tRows.Next() {
		var title string
		if err = tRows.Scan(&title); err != nil {
			tRows.Close()
			return c, err
		}
		c.SampleTracks = append(c.SampleTracks, title)
	}
	if err = tRows.Err(); err != nil {
		tRows.Close()
		return c, err
	}
	tRows.Close()

	all, err := s.WorkMatchCandidates(ctx, workID)
	if err != nil {
		return c, err
	}
	for _, cand := range all {
		if cand.Status == "candidate" {
			cand.Payload = nil
			var ownerID int64
			var ownerTitle string
			if err := s.db.QueryRowContext(ctx, `SELECT p.work_id,w.title FROM work_external_profiles p JOIN works w ON w.id=p.work_id WHERE p.source=? AND p.external_id=? AND p.work_id<>?`, cand.Source, cand.ExternalID, workID).Scan(&ownerID, &ownerTitle); err == nil {
				cand.OwnerWorkID = ownerID
				cand.OwnerTitle = ownerTitle
			}
			c.Candidates = append(c.Candidates, cand)
		}
	}
	c.Fingerprint = HashJSONFingerprint(c)
	return c, nil
}

type ArtistCandidateAIView struct {
	ArtistCandidate
	Aliases         []string `json:"aliases,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	ConflictOwnerID int64    `json:"conflictOwnerId,omitempty"`
	ConflictOwner   string   `json:"conflictOwnerName,omitempty"`
}

type ArtistAIContext struct {
	ArtistID       int64                   `json:"artistId"`
	Name           string                  `json:"name"`
	NeedsSplit     bool                    `json:"needsSplit"`
	KnownAliases   []string                `json:"knownAliases,omitempty"`
	SampleAlbums   []string                `json:"sampleAlbums,omitempty"`
	SampleTracks   []string                `json:"sampleTracks,omitempty"`
	Candidates     []ArtistCandidateAIView `json:"candidates"`
	Fingerprint    string                  `json:"-"`
}

func (s *Store) LoadArtistAIContext(ctx context.Context, artistID int64) (ArtistAIContext, error) {
	var c ArtistAIContext
	c.ArtistID = artistID
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=? AND merged_into_artist_id IS NULL`, artistID).Scan(&c.Name)
	if err != nil {
		return c, err
	}
	c.NeedsSplit = metadata.CompositeArtistCredit(c.Name)

	alRows, err := s.db.QueryContext(ctx, `SELECT value FROM artist_names WHERE artist_id=? UNION SELECT value FROM artist_search_aliases WHERE artist_id=? LIMIT 20`, artistID, artistID)
	if err == nil {
		for alRows.Next() {
			var v string
			if alRows.Scan(&v) == nil && v != "" {
				c.KnownAliases = append(c.KnownAliases, v)
			}
		}
		alRows.Close()
	}

	albRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(a.user_title,a.title) || CASE WHEN COALESCE(a.user_release_year,a.release_year,0)>0 THEN ' ('||COALESCE(a.user_release_year,a.release_year)||')' ELSE '' END FROM albums a JOIN tracks t ON t.album_id=a.id JOIN track_artists ta ON ta.track_id=t.id WHERE ta.artist_id=? LIMIT 15`, artistID)
	if err == nil {
		for albRows.Next() {
			var v string
			if albRows.Scan(&v) == nil && v != "" {
				c.SampleAlbums = append(c.SampleAlbums, v)
			}
		}
		albRows.Close()
	}

	trkRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(t.user_title,t.title) FROM tracks t JOIN track_artists ta ON ta.track_id=t.id WHERE ta.artist_id=? LIMIT 15`, artistID)
	if err == nil {
		for trkRows.Next() {
			var v string
			if trkRows.Scan(&v) == nil && v != "" {
				c.SampleTracks = append(c.SampleTracks, v)
			}
		}
		trkRows.Close()
	}

	cRows, err := s.db.QueryContext(ctx, `SELECT id,artist_id,source,external_id,display_name,COALESCE(sort_name,''),COALESCE(disambiguation,''),COALESCE(country,''),COALESCE(artist_type,''),COALESCE(mbid,''),score,evidence_json,COALESCE(json_extract(payload_json,'$.aliases'),'[]'),COALESCE(json_extract(payload_json,'$.tags'),'[]'),status FROM artist_match_candidates WHERE artist_id=? AND status='candidate' ORDER BY score DESC,id`, artistID)
	if err != nil {
		return c, err
	}
	defer cRows.Close()
	for cRows.Next() {
		var cand ArtistCandidateAIView
		var evJSON, alJSON, tagJSON string
		if err = cRows.Scan(&cand.ID, &cand.ArtistID, &cand.Source, &cand.ExternalID, &cand.DisplayName, &cand.SortName, &cand.Disambiguation, &cand.Country, &cand.ArtistType, &cand.MBID, &cand.Score, &evJSON, &alJSON, &tagJSON, &cand.Status); err != nil {
			return c, err
		}
		_ = json.Unmarshal([]byte(evJSON), &cand.Evidence)
		_ = json.Unmarshal([]byte(alJSON), &cand.Aliases)
		_ = json.Unmarshal([]byte(tagJSON), &cand.Tags)
		if conflict, err := s.ArtistIdentityConflict(ctx, artistID, cand.ID); err == nil {
			cand.ConflictOwnerID = conflict.TargetArtistID
			cand.ConflictOwner = conflict.TargetName
		}
		c.Candidates = append(c.Candidates, cand)
	}
	if err = cRows.Err(); err != nil {
		return c, err
	}
	c.Fingerprint = HashJSONFingerprint(c)
	return c, nil
}

type CreditSplitCandidateArtist struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type CreditSplitAIContext struct {
	ArtistID        int64                        `json:"artistId"`
	Name            string                       `json:"name"`
	SampleTracks    []string                     `json:"sampleTracks,omitempty"`
	SampleAlbums    []string                     `json:"sampleAlbums,omitempty"`
	ExistingArtists []CreditSplitCandidateArtist `json:"existingArtists,omitempty"`
	Fingerprint     string                       `json:"-"`
}

func (s *Store) LoadCreditSplitAIContext(ctx context.Context, artistID int64) (CreditSplitAIContext, error) {
	var c CreditSplitAIContext
	c.ArtistID = artistID
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=? AND merged_into_artist_id IS NULL`, artistID).Scan(&c.Name)
	if err != nil {
		return c, err
	}
	tRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(t.user_title,t.title) FROM tracks t JOIN track_artists ta ON ta.track_id=t.id WHERE ta.artist_id=? LIMIT 10`, artistID)
	if err == nil {
		for tRows.Next() {
			var v string
			if tRows.Scan(&v) == nil && v != "" {
				c.SampleTracks = append(c.SampleTracks, v)
			}
		}
		tRows.Close()
	}
	aRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(a.user_title,a.title) FROM albums a JOIN tracks t ON t.album_id=a.id JOIN track_artists ta ON ta.track_id=t.id WHERE ta.artist_id=? LIMIT 10`, artistID)
	if err == nil {
		for aRows.Next() {
			var v string
			if aRows.Scan(&v) == nil && v != "" {
				c.SampleAlbums = append(c.SampleAlbums, v)
			}
		}
		aRows.Close()
	}
	// Pull existing single artists whose names appear inside the composite credit.
	exRows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(user_display_name,display_name) AS nm FROM artists WHERE id<>? AND merged_into_artist_id IS NULL AND length(trim(COALESCE(user_display_name,display_name)))>=2 AND instr(lower(?),lower(COALESCE(user_display_name,display_name)))>0 ORDER BY length(nm) DESC LIMIT 15`, artistID, c.Name)
	if err == nil {
		for exRows.Next() {
			var item CreditSplitCandidateArtist
			if exRows.Scan(&item.ID, &item.Name) == nil {
				c.ExistingArtists = append(c.ExistingArtists, item)
			}
		}
		exRows.Close()
	}
	c.Fingerprint = HashJSONFingerprint(c)
	return c, nil
}

type SeriesSuggestionAIContext struct {
	SuggestionID  int64    `json:"suggestionId"`
	Kind          string   `json:"kind"`
	RelationAB    string   `json:"relationAB"`
	RelationBA    string   `json:"relationBA"`
	WorkA         Work     `json:"workA"`
	WorkB         Work     `json:"workB"`
	SeriesATitle  string   `json:"seriesATitle,omitempty"`
	SeriesAManual bool     `json:"seriesAManual"`
	SeriesAMembers []string `json:"seriesAMembers,omitempty"`
	SeriesBTitle  string   `json:"seriesBTitle,omitempty"`
	SeriesBManual bool     `json:"seriesBManual"`
	SeriesBMembers []string `json:"seriesBMembers,omitempty"`
	Fingerprint   string   `json:"-"`
}

func (s *Store) LoadSeriesSuggestionAIContext(ctx context.Context, suggestionID int64) (SeriesSuggestionAIContext, error) {
	var c SeriesSuggestionAIContext
	c.SuggestionID = suggestionID
	var workAID, workBID int64
	err := s.db.QueryRowContext(ctx, `SELECT work_a,work_b,relation_ab,relation_ba,kind FROM work_series_suggestions WHERE id=?`, suggestionID).
		Scan(&workAID, &workBID, &c.RelationAB, &c.RelationBA, &c.Kind)
	if err != nil {
		return c, err
	}
	works, err := s.WorksByIDs(ctx, []int64{workAID, workBID})
	if err != nil {
		return c, err
	}
	for _, w := range works {
		if w.ID == workAID {
			c.WorkA = w
		} else if w.ID == workBID {
			c.WorkB = w
		}
	}
	loadSeries := func(workID int64, title *string, manual *bool, members *[]string) {
		var seriesID int64
		var src string
		if e := s.db.QueryRowContext(ctx, `SELECT ws.id,ws.title,ws.title_source FROM work_series_members m JOIN work_series ws ON ws.id=m.series_id WHERE m.work_id=?`, workID).Scan(&seriesID, title, &src); e == nil {
			*manual = src == "manual"
			if rows, e2 := s.db.QueryContext(ctx, `SELECT w.title FROM work_series_members m JOIN works w ON w.id=m.work_id WHERE m.series_id=? ORDER BY w.year,w.id LIMIT 15`, seriesID); e2 == nil {
				for rows.Next() {
					var t string
					if rows.Scan(&t) == nil {
						*members = append(*members, t)
					}
				}
				rows.Close()
			}
		}
	}
	loadSeries(workAID, &c.SeriesATitle, &c.SeriesAManual, &c.SeriesAMembers)
	loadSeries(workBID, &c.SeriesBTitle, &c.SeriesBManual, &c.SeriesBMembers)
	c.Fingerprint = HashJSONFingerprint(c)
	return c, nil
}

// ---------------------------------------------------------------- pending target listers

func (s *Store) PendingAlbumReviewTargetIDs(ctx context.Context) ([]int64, error) {
	return s.queryInt64List(ctx, `SELECT DISTINCT album_id FROM album_subject_candidates WHERE status='candidate' ORDER BY album_id`)
}

func (s *Store) PendingTrackReviewTargetIDs(ctx context.Context) ([]int64, error) {
	return s.queryInt64List(ctx, `SELECT DISTINCT track_id FROM track_subject_candidates WHERE status='candidate' ORDER BY track_id`)
}

func (s *Store) PendingWorkReviewTargetIDs(ctx context.Context) ([]int64, error) {
	return s.queryInt64List(ctx, `SELECT DISTINCT work_id FROM work_match_candidates WHERE status='candidate' ORDER BY work_id`)
}

func (s *Store) PendingArtistReviewTargetIDs(ctx context.Context) ([]int64, error) {
	return s.queryInt64List(ctx, `SELECT DISTINCT c.artist_id FROM artist_match_candidates c JOIN artists ar ON ar.id=c.artist_id WHERE c.status='candidate' AND ar.merged_into_artist_id IS NULL ORDER BY c.artist_id`)
}

func (s *Store) PendingCreditSplitArtistIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name) FROM artists ar WHERE ar.merged_into_artist_id IS NULL AND (EXISTS(SELECT 1 FROM artist_match_candidates c WHERE c.artist_id=ar.id AND c.status='candidate') OR NOT EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=ar.id)) ORDER BY ar.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var name string
		if err = rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		if metadata.CompositeArtistCredit(name) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (s *Store) PendingSeriesSuggestionIDs(ctx context.Context) ([]int64, error) {
	return s.queryInt64List(ctx, `SELECT id FROM work_series_suggestions s WHERE NOT EXISTS(SELECT 1 FROM work_series_locks l WHERE l.work_id=s.work_a OR l.work_id=s.work_b) ORDER BY id`)
}

func (s *Store) queryInt64List(ctx context.Context, query string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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
