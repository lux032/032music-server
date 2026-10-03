package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrArtistRunState = errors.New("artist run state conflict")
var ErrArtistWaitBudget = errors.New("artist run rate limit budget exhausted")

type ArtistRunCheckpoint struct {
	ExpectedName  string
	RunID, ItemID int64
	ClaimToken    int64
}

// ArtistRunItemInput snapshots identity input and non-secret source parameters.
// API keys remain live configuration and never enter a durable run snapshot.
type ArtistRunItemInput struct {
	Artist  ArtistMatchInput
	Sources []ArtistRunSource
}
type ArtistRunSource struct {
	Source, Language, InputKey, ConfigKey string
	CacheDays                             int
	Enabled, AutoMatch, HasAPIKey         bool
}
type ArtistRunItem struct {
	ID, RunID, ObjectID int64
	ClaimToken          int64
	Status, Outcome     string
	MatchedFact         bool
	RateLimitCount      int
	Input               ArtistRunItemInput
}
type DurableArtistRun struct {
	ArtistMatchRun
	DurableVersion                        int
	PauseReason, WaitSource, WaitingUntil string
	WaitTotalMS, BudgetBaselineMS         int64
	AutoResumeCount                       int
}

// MaxArtistAutoResumeRounds caps automatic rate-limit recovery per run.
const MaxArtistAutoResumeRounds = 3

// ArtistRunAutoResumeEligible reports whether a paused run may be resumed by
// the automatic rate-limit scheduler: it carries a rate-limit deadline and a
// pause cause that is never a human or storage decision. Manual pauses,
// storage errors and exhausted rounds always wait for the user.
func ArtistRunAutoResumeEligible(r DurableArtistRun) bool {
	if r.Status != "paused" || r.WaitingUntil == "" || r.AutoResumeCount >= MaxArtistAutoResumeRounds {
		return false
	}
	switch r.PauseReason {
	case "rate_limit_count", "rate_limit_wait_budget", "server_restart":
		return true
	}
	return false
}

func artistRunWriteLock(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE artist_match_runs SET status=status WHERE 0`)
	return err
}
func requireArtistCheckpoint(ctx context.Context, tx *sql.Tx, c *ArtistRunCheckpoint, artistID int64) error {
	if c == nil {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_run_items i JOIN artist_match_runs r ON r.id=i.run_id WHERE i.id=? AND r.id=? AND i.object_id=? AND i.status='in_progress' AND r.status='running' AND r.durable_version=1 AND i.claim_token=?`, c.ItemID, c.RunID, artistID, c.ClaimToken).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrArtistRunState
	}
	if c.ExpectedName != "" {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=? AND merged_into_artist_id IS NULL`, artistID).Scan(&name); err != nil {
			return ErrArtistRunState
		}
		if name != c.ExpectedName {
			return ErrArtistRunState
		}
	}

	return nil
}
func (s *Store) CreateDurableArtistRun(ctx context.Context, items []ArtistRunItemInput) (int64, error) {
	if len(items) == 0 {
		return 0, ErrArtistRunState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = artistRunWriteLock(ctx, tx); err != nil {
		return 0, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_runs WHERE status IN ('running','queued','paused')`).Scan(&active); err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, ErrArtistRunState
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO artist_match_runs(status,total_artists,durable_version) VALUES('running',?,1)`, len(items))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		encoded, e := json.Marshal(item)
		if e != nil {
			return 0, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_match_run_items(run_id,object_id,input_json) VALUES(?,?,?)`, id, item.Artist.ID, string(encoded)); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
func (s *Store) ClaimArtistRunItem(ctx context.Context, runID int64) (ArtistRunItem, error) {
	var item ArtistRunItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if err = artistRunWriteLock(ctx, tx); err != nil {
		return item, err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_match_runs WHERE id=? AND durable_version=1`, runID).Scan(&status); err != nil {
		return item, err
	}
	if status != "running" {
		return item, ErrArtistRunState
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,object_id,status,COALESCE(outcome,''),matched_fact,rate_limit_count,input_json,claim_token FROM artist_match_run_items WHERE run_id=? AND status='pending' ORDER BY id LIMIT 1`, runID).Scan(&item.ID, &item.RunID, &item.ObjectID, &item.Status, &item.Outcome, &item.MatchedFact, &item.RateLimitCount, &raw, &item.ClaimToken)
	if err != nil {
		return item, err
	}
	if err = json.Unmarshal([]byte(raw), &item.Input); err != nil {
		return item, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_run_items SET status='in_progress',claim_token=claim_token+1 WHERE id=?`, item.ID); err != nil {
		return item, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET current_artist=? WHERE id=?`, item.Input.Artist.Name, runID); err != nil {
		return item, err
	}
	item.Status = "in_progress"
	item.ClaimToken++
	return item, tx.Commit()
}
func (s *Store) CompleteArtistRunItem(ctx context.Context, c ArtistRunCheckpoint, outcome string) error {
	switch outcome {
	case "matched", "review", "no_result", "skipped", "failed":
	default:
		return ErrArtistRunState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistRunWriteLock(ctx, tx); err != nil {
		return err
	}
	var status string
	var matched bool
	var objectID int64
	if err = tx.QueryRowContext(ctx, `SELECT status,matched_fact,object_id FROM artist_match_run_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&status, &matched, &objectID); err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if err = requireArtistCheckpoint(ctx, tx, &c, objectID); err != nil {
		return err
	}
	if matched {
		outcome = "matched"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_run_items SET status='completed',outcome=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, outcome, c.ItemID); err != nil {
		return err
	}
	for _, field := range []struct{ column, outcome string }{{"matched_artists", "matched"}, {"review_artists", "review"}, {"no_result_artists", "no_result"}, {"skipped_artists", "skipped"}, {"failed_artists", "failed"}} {
		if _, err = tx.ExecContext(ctx, "UPDATE artist_match_runs SET "+field.column+"=(SELECT count(*) FROM artist_match_run_items WHERE run_id=? AND outcome=?) WHERE id=?", c.RunID, field.outcome, c.RunID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET processed_artists=(SELECT count(*) FROM artist_match_run_items WHERE run_id=? AND status='completed'),wait_source='',waiting_until=NULL WHERE id=?`, c.RunID, c.RunID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) TransitionArtistRun(ctx context.Context, id int64, action, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistRunWriteLock(ctx, tx); err != nil {
		return err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_match_runs WHERE id=? AND durable_version=1`, id).Scan(&status); err != nil {
		return err
	}
	switch action {
	case "resume":
		if status != "paused" {
			return ErrArtistRunState
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_runs WHERE status IN ('running','queued') AND id<>?`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrArtistRunState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_match_run_items SET status='pending',rate_limit_count=0,claim_token=claim_token+1 WHERE run_id=? AND status<>'completed'`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET status='running',pause_reason='',budget_baseline_ms=wait_total_ms WHERE id=?`, id)
	case "auto_resume":
		// Automatic recovery re-validates the persisted state instead of
		// trusting the scheduler: a user pause/cancel that landed first wins.
		if status != "paused" {
			return ErrArtistRunState
		}
		var reason, waiting string
		var autoCount int
		if err = tx.QueryRowContext(ctx, `SELECT pause_reason,COALESCE(waiting_until,''),auto_resume_count FROM artist_match_runs WHERE id=?`, id).Scan(&reason, &waiting, &autoCount); err != nil {
			return err
		}
		eligible := DurableArtistRun{ArtistMatchRun: ArtistMatchRun{Status: status}, PauseReason: reason, WaitingUntil: waiting, AutoResumeCount: autoCount}
		if !ArtistRunAutoResumeEligible(eligible) {
			return ErrArtistRunState
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_runs WHERE status IN ('running','queued') AND id<>?`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrArtistRunState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_match_run_items SET status='pending',rate_limit_count=0,claim_token=claim_token+1 WHERE run_id=? AND status<>'completed'`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET status='running',pause_reason='',budget_baseline_ms=wait_total_ms,auto_resume_count=auto_resume_count+1 WHERE id=?`, id)
	case "pause", "cancel", "fail":
		if status != "running" && !(action == "cancel" && status == "paused") {
			return ErrArtistRunState
		}
		next := "paused"
		if action == "fail" {
			next = "failed"
		}
		if action == "cancel" {
			next = "cancelled"
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET status=?,pause_reason=?,finished_at=CASE WHEN ? IN ('cancelled','failed') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE NULL END WHERE id=?`, next, reason, next, id)
	case "complete":
		if status != "running" {
			return ErrArtistRunState
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_run_items WHERE run_id=? AND status<>'completed'`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return ErrArtistRunState
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET status='completed',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),wait_source='',waiting_until=NULL WHERE id=?`, id)
	default:
		return ErrArtistRunState
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverDurableArtistRuns never resumes network work and preserves deadlines
// and historical wait. An existing pause cause is preserved so the automatic
// rate-limit scheduler can still tell a waiting run apart from a manual stop.
// A run that already spent its auto-resume rounds is marked exhausted: its
// rounds are genuinely used up, and the page must not promise an automatic
// continuation the backend will never perform.
func (s *Store) RecoverDurableArtistRuns(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = artistRunWriteLock(ctx, tx); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE artist_match_runs SET status='paused',pause_reason=CASE WHEN pause_reason='' AND waiting_until IS NOT NULL AND waiting_until<>'' AND auto_resume_count>=? THEN 'rate_limit_exhausted' WHEN pause_reason='' THEN 'server_restart' ELSE pause_reason END WHERE durable_version=1 AND status IN ('running','queued')`, MaxArtistAutoResumeRounds)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}
func (s *Store) DurableArtistRun(ctx context.Context, id int64) (DurableArtistRun, error) {
	var r DurableArtistRun
	err := s.db.QueryRowContext(ctx, `SELECT id,status,total_artists,processed_artists,matched_artists,review_artists,failed_artists,skipped_artists,no_result_artists,COALESCE(current_artist,''),COALESCE(error_message,''),durable_version,pause_reason,wait_source,COALESCE(waiting_until,''),wait_total_ms,budget_baseline_ms,auto_resume_count FROM artist_match_runs WHERE id=?`, id).Scan(&r.ID, &r.Status, &r.Total, &r.Processed, &r.Matched, &r.Review, &r.Failed, &r.Skipped, &r.NoResult, &r.Current, &r.ErrorMessage, &r.DurableVersion, &r.PauseReason, &r.WaitSource, &r.WaitingUntil, &r.WaitTotalMS, &r.BudgetBaselineMS, &r.AutoResumeCount)
	return r, err
}

// RecordArtistRunWait records one rate-limit response independently of sleep.
// Shared-deadline extensions pass response=false and do not consume attempts.
func (s *Store) RecordArtistRunWait(ctx context.Context, c ArtistRunCheckpoint, source string, until time.Time, elapsed time.Duration, response bool) error {
	if elapsed < 0 {
		return ErrArtistRunState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistRunWriteLock(ctx, tx); err != nil {
		return err
	}
	var objectID int64
	if err = tx.QueryRowContext(ctx, `SELECT object_id FROM artist_match_run_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&objectID); err != nil {
		return err
	}
	var runStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_match_runs WHERE id=?`, c.RunID).Scan(&runStatus); err != nil {
		return err
	}
	if err = requireArtistCheckpoint(ctx, tx, &c, objectID); err != nil {
		if response || (runStatus != "paused" && runStatus != "cancelled") {
			return err
		}
		var token int64
		var itemStatus string
		if e := tx.QueryRowContext(ctx, `SELECT claim_token,status FROM artist_match_run_items WHERE id=?`, c.ItemID).Scan(&token, &itemStatus); e != nil {
			return e
		}
		if token != c.ClaimToken || itemStatus != "in_progress" {
			return ErrArtistRunState
		}
	}
	increment := 0
	if response {
		increment = 1
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_run_items SET rate_limit_count=rate_limit_count+? WHERE id=?`, increment, c.ItemID); err != nil {
		return err
	}
	var current, currentSource string
	var total, baseline int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(waiting_until,''),wait_source,wait_total_ms,budget_baseline_ms FROM artist_match_runs WHERE id=?`, c.RunID).Scan(&current, &currentSource, &total, &baseline); err != nil {
		return err
	}
	if current != "" {
		old, e := time.Parse(time.RFC3339Nano, current)
		if e != nil {
			return e
		}
		if old.After(until) {
			until = old
			source = currentSource
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET wait_source=?,waiting_until=?,wait_total_ms=wait_total_ms+? WHERE id=?`, source, until.UTC().Format(time.RFC3339Nano), elapsed.Milliseconds(), c.RunID); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT rate_limit_count FROM artist_match_run_items WHERE id=?`, c.ItemID).Scan(&count); err != nil {
		return err
	}
	reason := ""
	if count >= 3 {
		reason = "rate_limit_count"
	}
	if total+elapsed.Milliseconds()-baseline >= int64((30*time.Minute)/time.Millisecond) {
		reason = "rate_limit_wait_budget"
	}
	if reason != "" && runStatus == "running" {
		var autoCount int
		if err = tx.QueryRowContext(ctx, `SELECT auto_resume_count FROM artist_match_runs WHERE id=?`, c.RunID).Scan(&autoCount); err != nil {
			return err
		}
		if autoCount >= MaxArtistAutoResumeRounds {
			// 自动恢复轮次用尽：标记为需人工处理，绝不无限自动重试。
			reason = "rate_limit_exhausted"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_match_runs SET status='paused',pause_reason=? WHERE id=?`, reason, c.RunID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if reason != "" {
		return ErrArtistWaitBudget
	}
	return nil
}

// ArtistRunItemSource returns a checkpoint only if its evidence keys still
// match the caller's current input/config. Unknown snapshots retain the
// provenance fail-closed rules from source-state matching.
func (s *Store) ArtistRunItemSource(ctx context.Context, itemID int64, source, inputKey, configKey string, cacheDays int) (ArtistSourceSnapshot, error) {
	var snapshot ArtistSourceSnapshot
	var raw, checked string
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_json,checked_at FROM artist_match_item_sources WHERE item_id=? AND source=? AND input_key=? AND config_key=?`, itemID, source, inputKey, configKey).Scan(&raw, &checked)
	if err != nil {
		return snapshot, err
	}
	at, e := time.Parse(time.RFC3339Nano, checked)
	if e != nil {
		return snapshot, e
	}
	if !time.Now().Before(at.Add(time.Duration(cacheDays) * 24 * time.Hour)) {
		return snapshot, sql.ErrNoRows
	}
	err = json.Unmarshal([]byte(raw), &snapshot)
	return snapshot, err
}

func (s *Store) ListDurableArtistRuns(ctx context.Context, limit, offset int) ([]DurableArtistRun, int, error) {
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, ErrArtistRunState
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_runs`).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM artist_match_runs ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	var runs []DurableArtistRun
	for _, id := range ids {
		r, e := s.DurableArtistRun(ctx, id)
		if e != nil {
			return nil, 0, e
		}
		runs = append(runs, r)
	}
	return runs, count, nil
}
func (s *Store) UnfinishedDurableArtistRun(ctx context.Context) (DurableArtistRun, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM artist_match_runs WHERE durable_version=1 AND status IN ('running','paused','queued') ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return DurableArtistRun{}, err
	}
	return s.DurableArtistRun(ctx, id)
}

// ArtistRunsAwaitingAutoResume lists paused rate-limit runs the automatic
// scheduler should arm a timer for, including runs recovered after a restart.
func (s *Store) ArtistRunsAwaitingAutoResume(ctx context.Context) ([]DurableArtistRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM artist_match_runs WHERE durable_version=1 AND status='paused' AND waiting_until IS NOT NULL AND waiting_until<>'' AND pause_reason IN ('rate_limit_count','rate_limit_wait_budget','server_restart') AND auto_resume_count<? ORDER BY id`, MaxArtistAutoResumeRounds)
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var runs []DurableArtistRun
	for _, id := range ids {
		run, e := s.DurableArtistRun(ctx, id)
		if e != nil {
			return nil, e
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// ArtistRunItemHasSource distinguishes unfinished work in this run from a
// pre-existing stable review. Existence deliberately ignores keys and TTL.
func (s *Store) ArtistRunItemHasSource(ctx context.Context, itemID int64, source string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_match_item_sources WHERE item_id=? AND source=?)`, itemID, source).Scan(&exists)
	return exists, err
}
