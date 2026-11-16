package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrAliasJobState = errors.New("中文别名任务或歌手身份已变化，请刷新")
var ErrAliasBudget = errors.New("本轮该歌手的调用次数已用尽")

type ArtistAliasSettings struct {
	Enabled     bool   `json:"enabled"`
	Paused      bool   `json:"paused"`
	DeepSeekKey string `json:"-"`
	SearchLimit int    `json:"searchLimit"`
	FetchLimit  int    `json:"fetchLimit"`
	ModelLimit  int    `json:"modelLimit"`
	RPM         int    `json:"rpm"`
}

func (s *Store) ArtistAliasSettings(ctx context.Context) (v ArtistAliasSettings, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT enabled,paused,deepseek_key,search_limit,fetch_limit,model_limit,rpm FROM artist_alias_settings WHERE id=1`).Scan(&v.Enabled, &v.Paused, &v.DeepSeekKey, &v.SearchLimit, &v.FetchLimit, &v.ModelLimit, &v.RPM)
	return
}
func (s *Store) SaveArtistAliasSettings(ctx context.Context, v ArtistAliasSettings) error {
	if v.SearchLimit < 1 || v.SearchLimit > 10 || v.FetchLimit < 1 || v.FetchLimit > 20 || v.ModelLimit < 1 || v.ModelLimit > 5 || v.RPM < 1 || v.RPM > 60 {
		return errors.New("每位歌手的请求上限或请求速度无效")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artist_alias_settings SET enabled=?,paused=?,deepseek_key=?,search_limit=?,fetch_limit=?,model_limit=?,rpm=? WHERE id=1`, v.Enabled, v.Paused, v.DeepSeekKey, v.SearchLimit, v.FetchLimit, v.ModelLimit, v.RPM)
	return err
}

type ArtistAliasSubject struct {
	ID           int64
	Name         string
	Identity     ArtistBiographyIdentity
	KnownAliases []string
}

// Only new artists may be automatically queued. Empty external identity is
// retained in the observation table until a later confirmation, without calls.
func (s *Store) ArtistAliasSubjects(ctx context.Context, onlyNew bool) ([]ArtistAliasSubject, error) {
	extra := ""
	if onlyNew {
		extra = ` AND a.id IN(SELECT artist_id FROM artist_alias_new_artists) AND NOT EXISTS(SELECT 1 FROM artist_alias_jobs j WHERE j.artist_id=a.id)`
	}
	rows, err := s.db.QueryContext(ctx, artistMergeChainSQL+`, identities AS MATERIALIZED (
 SELECT c.root,p.artist_id,p.external_id,ROW_NUMBER() OVER(PARTITION BY c.root ORDER BY (p.artist_id=c.root) DESC,p.artist_id) AS choice
 FROM chain c JOIN artist_external_profiles p ON p.artist_id=c.id WHERE p.source='musicbrainz'
), known AS MATERIALIZED (
 SELECT c.root,json_group_array(x.value) AS names FROM chain c JOIN artist_search_aliases x ON x.artist_id=c.id GROUP BY c.root
) SELECT a.id,COALESCE(a.user_display_name,a.display_name),i.artist_id,i.external_id,COALESCE(k.names,'[]') FROM artists a
 JOIN identities i ON i.root=a.id AND i.choice=1 LEFT JOIN known k ON k.root=a.id
 WHERE a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+extra+` ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtistAliasSubject{}
	for rows.Next() {
		var v ArtistAliasSubject
		var names string
		if err = rows.Scan(&v.ID, &v.Name, &v.Identity.OwnerID, &v.Identity.MBID, &names); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(names), &v.KnownAliases); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) EnqueueArtistAlias(ctx context.Context, v ArtistAliasSubject, origin string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO artist_alias_jobs(artist_id,owner_id,mbid,origin) VALUES(?,?,?,?)`, v.ID, v.Identity.OwnerID, v.Identity.MBID, origin)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

type ArtistAliasJob struct {
	ID          int64  `json:"id"`
	ArtistID    int64  `json:"artistId"`
	OwnerID     int64  `json:"ownerId"`
	MBID        string `json:"mbid"`
	Name        string `json:"name"`
	Origin      string `json:"origin"`
	Status      string `json:"status"`
	SearchCalls int    `json:"searchCalls"`
	FetchCalls  int    `json:"fetchCalls"`
	ModelCalls  int    `json:"modelCalls"`
	Epoch       int64  `json:"epoch"`
	Detail      string `json:"detail"`
}

const aliasJobColumns = `j.id,j.artist_id,j.owner_id,j.mbid,COALESCE(a.user_display_name,a.display_name),j.origin,j.status,j.search_calls,j.fetch_calls,j.model_calls,j.epoch,j.detail`

func scanAliasJob(row interface{ Scan(...any) error }) (v ArtistAliasJob, err error) {
	err = row.Scan(&v.ID, &v.ArtistID, &v.OwnerID, &v.MBID, &v.Name, &v.Origin, &v.Status, &v.SearchCalls, &v.FetchCalls, &v.ModelCalls, &v.Epoch, &v.Detail)
	return
}
func (s *Store) RecoverArtistAliasJobs(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `UPDATE artist_alias_jobs SET status='queued',epoch=epoch+1 WHERE status='running'`)
	return e
}
func (s *Store) ClaimArtistAliasJob(ctx context.Context) (ArtistAliasJob, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ArtistAliasJob{}, e
	}
	defer tx.Rollback()
	var id int64
	e = tx.QueryRowContext(ctx, `UPDATE artist_alias_jobs SET status='running',epoch=epoch+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=(SELECT id FROM artist_alias_jobs WHERE status='queued' ORDER BY id LIMIT 1) AND EXISTS(SELECT 1 FROM artist_alias_settings WHERE enabled=1 AND paused=0) RETURNING id`).Scan(&id)
	if e != nil {
		return ArtistAliasJob{}, e
	}
	v, e := scanAliasJob(tx.QueryRowContext(ctx, `SELECT `+aliasJobColumns+` FROM artist_alias_jobs j JOIN artists a ON a.id=j.artist_id WHERE j.id=?`, id))
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (s *Store) FinishArtistAliasJob(ctx context.Context, j ArtistAliasJob, status, detail string) error {
	r, e := s.db.ExecContext(ctx, `UPDATE artist_alias_jobs SET status=?,detail=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND epoch=? AND status='running'`, status, detail, j.ID, j.Epoch)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e == nil && n != 1 {
		return ErrAliasJobState
	}
	return e
}

// Reserve before dispatch and keep counts across pause, shutdown and failures.
func (s *Store) ReserveArtistAliasCall(ctx context.Context, j ArtistAliasJob, kind string, limit int) error {
	column := map[string]string{"search": "search_calls", "fetch": "fetch_calls", "model": "model_calls"}[kind]
	if column == "" {
		return ErrAliasBudget
	}
	r, e := s.db.ExecContext(ctx, `UPDATE artist_alias_jobs SET `+column+`=`+column+`+1 WHERE id=? AND epoch=? AND status='running' AND `+column+`<? AND EXISTS(SELECT 1 FROM artist_alias_settings WHERE enabled=1 AND paused=0)`, j.ID, j.Epoch, limit)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return ErrAliasBudget
	}
	return nil
}
func (s *Store) ArtistAliasJobs(ctx context.Context, limit int) ([]ArtistAliasJob, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+aliasJobColumns+` FROM artist_alias_jobs j JOIN artists a ON a.id=j.artist_id ORDER BY CASE j.status WHEN 'running' THEN 0 WHEN 'failed' THEN 1 ELSE 2 END,j.id DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ArtistAliasJob{}
	for rows.Next() {
		v, e := scanAliasJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type ArtistAliasEvidence struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	Source      string `json:"source"`
	Language    string `json:"language"`
	RetrievedAt string `json:"retrievedAt"`
}
type ArtistAliasCandidate struct {
	ID            int64                 `json:"id"`
	JobID         int64                 `json:"jobId"`
	ArtistID      int64                 `json:"artistId"`
	OwnerID       int64                 `json:"ownerId"`
	MBID          string                `json:"mbid"`
	Name          string                `json:"name"`
	Value         string                `json:"value"`
	OriginalValue string                `json:"originalValue"`
	Language      string                `json:"language"`
	Evidence      []ArtistAliasEvidence `json:"evidence"`
	Status        string                `json:"status"`
	Reason        string                `json:"reason"`
}

func aliasIdentityGuard(ctx context.Context, tx *sql.Tx, artistID, ownerID int64, mbid string) error {
	var canonical bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists a WHERE a.id=? AND a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+`)`, artistID).Scan(&canonical); e != nil {
		return e
	}
	if !canonical {
		return ErrAliasJobState
	}
	var owner int64
	var current string
	e := tx.QueryRowContext(ctx, artistConfirmedIdentitySQL, artistID, artistID, artistID).Scan(&owner, &current)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrAliasJobState
	}
	if e != nil {
		return e
	}
	if owner != ownerID || current != mbid {
		return ErrAliasJobState
	}
	return nil
}
func aliasRejected(ctx context.Context, tx *sql.Tx, artistID int64, value string) (bool, error) {
	var v bool
	e := tx.QueryRowContext(ctx, `WITH RECURSIVE members(id) AS(SELECT ? UNION SELECT a.id FROM artists a JOIN members m ON a.merged_into_artist_id=m.id) SELECT EXISTS(SELECT 1 FROM artist_alias_rejections r JOIN members m ON m.id=r.artist_id WHERE r.value=? COLLATE NOCASE)`, artistID, value).Scan(&v)
	return v, e
}

// Candidate and active search alias are committed together under identity and
// worker fences. Automatic names remain owned by the original identity row so
// merge rollback restores them just like manually supplied search aliases.
func (s *Store) SaveArtistAliasCandidate(ctx context.Context, j ArtistAliasJob, v ArtistAliasCandidate, accept bool) error {
	if strings.TrimSpace(v.Value) == "" || utf8.RuneCountInString(v.Value) > 200 || len(v.Evidence) == 0 {
		return ErrInvalidSearchAlias
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE artist_alias_jobs SET id=id WHERE id=?`, j.ID); e != nil {
		return e
	}
	var live bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_alias_jobs WHERE id=? AND epoch=? AND status='running') AND EXISTS(SELECT 1 FROM artist_alias_settings WHERE enabled=1 AND paused=0)`, j.ID, j.Epoch).Scan(&live); e != nil {
		return e
	}
	if !live {
		return ErrAliasJobState
	}
	if e = aliasIdentityGuard(ctx, tx, j.ArtistID, j.OwnerID, j.MBID); e != nil {
		return e
	}
	rejected, e := aliasRejected(ctx, tx, j.ArtistID, v.Value)
	if e != nil {
		return e
	}
	if rejected {
		return nil
	}
	raw, e := json.Marshal(v.Evidence)
	if e != nil {
		return e
	}
	r, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO artist_alias_candidates(job_id,artist_id,owner_id,mbid,value,original_value,language,evidence_json,reason) VALUES(?,?,?,?,?,?,?,?,?)`, j.ID, j.ArtistID, j.OwnerID, j.MBID, v.Value, v.OriginalValue, v.Language, string(raw), v.Reason)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return tx.Commit()
	}
	id, e := r.LastInsertId()
	if e != nil {
		return e
	}
	if accept {
		if e = acceptAliasCandidate(ctx, tx, id, j.OwnerID, v.Value); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func acceptAliasCandidate(ctx context.Context, tx *sql.Tx, id, ownerID int64, value string) error {
	if _, e := tx.ExecContext(ctx, `INSERT INTO artist_search_aliases(artist_id,value,source) VALUES(?,?,'automatic') ON CONFLICT DO NOTHING`, ownerID, value); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `UPDATE artist_alias_candidates SET status='accepted',alias_id=(SELECT id FROM artist_search_aliases WHERE artist_id=? AND value=? COLLATE NOCASE),value=? WHERE id=?`, ownerID, value, value, id)
	return e
}
func (s *Store) ReviewArtistAlias(ctx context.Context, id int64, accept bool, value string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE artist_alias_candidates SET id=id WHERE id=?`, id); e != nil {
		return e
	}
	var v ArtistAliasCandidate
	e = tx.QueryRowContext(ctx, `SELECT artist_id,owner_id,mbid,value FROM artist_alias_candidates WHERE id=? AND status='pending'`, id).Scan(&v.ArtistID, &v.OwnerID, &v.MBID, &v.Value)
	if e != nil {
		return e
	}
	if e = aliasIdentityGuard(ctx, tx, v.ArtistID, v.OwnerID, v.MBID); e != nil {
		return e
	}
	if !accept {
		if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artist_alias_rejections(artist_id,value) VALUES(?,?)`, v.OwnerID, v.Value); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE artist_alias_candidates SET status='rejected' WHERE id=?`, id)
	} else {
		value = strings.TrimSpace(value)
		if value == "" {
			value = v.Value
		}
		if utf8.RuneCountInString(value) > 200 {
			return ErrInvalidSearchAlias
		}
		// An edit explicitly rejects the original suggestion as well.
		if value != v.Value {
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artist_alias_rejections(artist_id,value) VALUES(?,?)`, v.OwnerID, v.Value); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM artist_alias_rejections WHERE artist_id=? AND value=?`, v.OwnerID, value); e != nil {
			return e
		}
		e = acceptAliasCandidate(ctx, tx, id, v.OwnerID, value)
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) ArtistAliasCandidates(ctx context.Context, status string, artistID int64, limit int) ([]ArtistAliasCandidate, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT c.id,c.job_id,c.artist_id,c.owner_id,c.mbid,COALESCE(a.user_display_name,a.display_name),c.value,c.original_value,c.language,c.evidence_json,c.status,c.reason FROM artist_alias_candidates c JOIN artists a ON a.id=c.artist_id WHERE (?='' OR c.status=?) AND (?=0 OR c.artist_id=?) ORDER BY c.id DESC LIMIT ?`, status, status, artistID, artistID, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ArtistAliasCandidate{}
	for rows.Next() {
		var v ArtistAliasCandidate
		var raw string
		if e = rows.Scan(&v.ID, &v.JobID, &v.ArtistID, &v.OwnerID, &v.MBID, &v.Name, &v.Value, &v.OriginalValue, &v.Language, &raw, &v.Status, &v.Reason); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &v.Evidence); e != nil {
			return nil, fmt.Errorf("alias evidence: %w", e)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type ArtistAliasStats struct {
	Queued, Running, Filled, Missing, Review, Failed, Skipped int
	Searches, Fetches, Models                                 int
	Pending                                                   int
}

func (s *Store) ArtistAliasStats(ctx context.Context) (v ArtistAliasStats, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='queued'),0),COALESCE(SUM(status='running'),0),COALESCE(SUM(status='filled'),0),COALESCE(SUM(status='missing'),0),COALESCE(SUM(status='review'),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status='skipped'),0),COALESCE(SUM(search_calls),0),COALESCE(SUM(fetch_calls),0),COALESCE(SUM(model_calls),0),(SELECT COUNT(*) FROM artist_alias_candidates WHERE status='pending') FROM artist_alias_jobs`).Scan(&v.Queued, &v.Running, &v.Filled, &v.Missing, &v.Review, &v.Failed, &v.Skipped, &v.Searches, &v.Fetches, &v.Models, &v.Pending)
	return
}

// Source data carries language metadata, never inferred from Han characters.
type ArtistAliasSourceName struct{ Value, Language, URL, Source string }
type ArtistAliasSourceData struct {
	Names                       []ArtistAliasSourceName
	IdentityNames, OfficialURLs []string
}

// Pause just the flag, preserving concurrent edits to credentials and limits.
func (s *Store) PauseArtistAliasJobs(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `UPDATE artist_alias_settings SET paused=1 WHERE id=1`)
	return e
}

// AliasSourceRetry shares the existing metadata cooldown with the alias queue.
type AliasSourceRetry struct{ After time.Duration }

func (e *AliasSourceRetry) Error() string {
	return "歌手数据来源正在限流，稍后自动继续"
}
