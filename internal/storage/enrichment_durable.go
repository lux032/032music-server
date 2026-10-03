package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

var ErrEnrichmentRunState = errors.New("enrichment run state conflict")
var ErrEnrichmentWaitBudget = errors.New("enrichment rate limit budget exhausted")

type EnrichmentCheckpoint struct{ RunID, ItemID, Epoch, Token int64 }
type EnrichmentItemInput struct {
	ObjectID   int64
	Parameters json.RawMessage
}
type DurableEnrichmentItem struct {
	ID, ObjectID, Token int64
	Stage               string
	Parameters          json.RawMessage
}
type DurableEnrichmentRun struct {
	EnrichmentRun
	Version                               int
	Epoch                                 int64
	PauseReason, WaitSource, WaitingUntil string
	WaitTotalMS, BudgetBaselineMS         int64
}

func enrichmentWriteLock(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE enrichment_runs SET status=status WHERE 0`)
	return err
}
func requireEnrichmentEpoch(ctx context.Context, tx *sql.Tx, id, epoch int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM enrichment_runs WHERE id=? AND epoch=? AND durable_version=1 AND status='running'`, id, epoch).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrEnrichmentRunState
	}
	return nil
}
func requireEnrichmentCheckpoint(ctx context.Context, tx *sql.Tx, c EnrichmentCheckpoint) error {
	if err := requireEnrichmentEpoch(ctx, tx, c.RunID, c.Epoch); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM enrichment_run_items WHERE id=? AND run_id=? AND claim_token=? AND status='in_progress'`, c.ItemID, c.RunID, c.Token).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrEnrichmentRunState
	}
	return nil
}
func (s *Store) CreateDurableEnrichmentRun(ctx context.Context, scope string, target int64, force bool, stages []string) (DurableEnrichmentRun, error) {
	var run DurableEnrichmentRun
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return run, err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return run, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM enrichment_runs WHERE status IN ('queued','running','paused')`).Scan(&active); err != nil {
		return run, err
	}
	if active > 0 || len(stages) == 0 {
		return run, ErrEnrichmentRunState
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO enrichment_runs(status,scope,target_id,force,durable_version,started_at) VALUES('running',?,NULLIF(?,0),?,1,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, scope, target, boolInt(force))
	if err != nil {
		return run, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return run, err
	}
	for _, stage := range stages {
		if _, err = tx.ExecContext(ctx, `INSERT INTO enrichment_run_stages(run_id,stage) VALUES(?,?)`, id, stage); err != nil {
			return run, err
		}
	}
	if err = tx.Commit(); err != nil {
		return run, err
	}
	return s.DurableEnrichmentRun(ctx, id)
}

// PrepareEnrichmentStage freezes the complete object snapshot exactly once.
// Tracks retain their original sequence independently of later tie-up sorting.
func (s *Store) PrepareEnrichmentStage(ctx context.Context, runID, epoch int64, stage string, items []EnrichmentItemInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return err
	}
	if err = requireEnrichmentEpoch(ctx, tx, runID, epoch); err != nil {
		return err
	}
	var prepared bool
	if err = tx.QueryRowContext(ctx, `SELECT prepared FROM enrichment_run_stages WHERE run_id=? AND stage=?`, runID, stage).Scan(&prepared); err != nil {
		return err
	}
	if prepared {
		return nil
	}
	for _, item := range items {
		if !json.Valid(item.Parameters) {
			return ErrEnrichmentRunState
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO enrichment_run_items(run_id,stage,object_id,parameters_json) VALUES(?,?,?,?)`, runID, stage, item.ObjectID, string(item.Parameters)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE enrichment_run_stages SET prepared=1 WHERE run_id=? AND stage=?`, runID, stage); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE enrichment_runs SET total=(SELECT count(*) FROM enrichment_run_items WHERE run_id=?),stage=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, runID, stage, runID); err != nil {
		return err
	}
	column := ""
	switch stage {
	case "albums":
		column = "stage_albums"
	case "tracks":
		column = "stage_tracks"
	case "works":
		column = "stage_works"
	}
	if column != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE enrichment_runs SET "+column+"=? WHERE id=?", len(items), runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) EnrichmentStagePrepared(ctx context.Context, runID int64, stage string) (bool, error) {
	var value bool
	err := s.db.QueryRowContext(ctx, `SELECT prepared FROM enrichment_run_stages WHERE run_id=? AND stage=?`, runID, stage).Scan(&value)
	return value, err
}
func (s *Store) ClaimEnrichmentItem(ctx context.Context, runID, epoch int64, stage string) (DurableEnrichmentItem, error) {
	var item DurableEnrichmentItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return item, err
	}
	if err = requireEnrichmentEpoch(ctx, tx, runID, epoch); err != nil {
		return item, err
	}
	var prepared bool
	if err = tx.QueryRowContext(ctx, `SELECT prepared FROM enrichment_run_stages WHERE run_id=? AND stage=?`, runID, stage).Scan(&prepared); err != nil {
		return item, err
	}
	if !prepared {
		return item, ErrEnrichmentRunState
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT id,object_id,claim_token,parameters_json FROM enrichment_run_items WHERE run_id=? AND stage=? AND status='pending' ORDER BY id LIMIT 1`, runID, stage).Scan(&item.ID, &item.ObjectID, &item.Token, &raw)
	if err != nil {
		return item, err
	}
	item.Stage = stage
	item.Token++
	item.Parameters = json.RawMessage(raw)
	if _, err = tx.ExecContext(ctx, `UPDATE enrichment_run_items SET status='in_progress',claim_token=claim_token+1 WHERE id=?`, item.ID); err != nil {
		return item, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE enrichment_runs SET stage=? WHERE id=?`, stage, runID); err != nil {
		return item, err
	}
	return item, tx.Commit()
}

// CompleteEnrichmentItemTx is the seam for committing SQLite side effects and
// durable outcomes together. It does not make remote HTTP exactly-once.
func CompleteEnrichmentItemTx(ctx context.Context, tx *sql.Tx, c EnrichmentCheckpoint, outcome string) error {
	if err := enrichmentWriteLock(ctx, tx); err != nil {
		return err
	}
	switch outcome {
	case "matched", "skipped", "review", "failed":
	default:
		return ErrEnrichmentRunState
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM enrichment_run_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&status); err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if err := requireEnrichmentCheckpoint(ctx, tx, c); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE enrichment_run_items SET status='completed',outcome=? WHERE id=?`, outcome, c.ItemID); err != nil {
		return err
	}
	for _, field := range []struct{ column, outcome string }{{"succeeded", "matched"}, {"skipped", "skipped"}, {"review", "review"}, {"failed", "failed"}} {
		if _, err := tx.ExecContext(ctx, "UPDATE enrichment_runs SET "+field.column+"=(SELECT count(*) FROM enrichment_run_items WHERE run_id=? AND outcome=?) WHERE id=?", c.RunID, field.outcome, c.RunID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE enrichment_runs SET processed=(SELECT count(*) FROM enrichment_run_items WHERE run_id=? AND status='completed'),current=NULL,waiting_until=NULL,wait_source='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, c.RunID, c.RunID)
	return err
}
func (s *Store) CompleteEnrichmentItem(ctx context.Context, c EnrichmentCheckpoint, outcome string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return err
	}
	if err = CompleteEnrichmentItemTx(ctx, tx, c, outcome); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DurableEnrichmentRun(ctx context.Context, id int64) (DurableEnrichmentRun, error) {
	var r DurableEnrichmentRun
	base, err := s.EnrichmentRun(ctx, id)
	if err != nil {
		return r, err
	}
	r.EnrichmentRun = base
	err = s.db.QueryRowContext(ctx, `SELECT durable_version,epoch,pause_reason,wait_source,COALESCE(waiting_until,''),wait_total_ms,budget_baseline_ms FROM enrichment_runs WHERE id=?`, id).Scan(&r.Version, &r.Epoch, &r.PauseReason, &r.WaitSource, &r.WaitingUntil, &r.WaitTotalMS, &r.BudgetBaselineMS)
	return r, err
}
func (s *Store) TransitionEnrichmentRun(ctx context.Context, id int64, action, reason string) error {
	return s.transitionEnrichmentRun(ctx, id, 0, action, reason)
}

// TransitionEnrichmentWorker rejects a stale worker before any state change.
func (s *Store) TransitionEnrichmentWorker(ctx context.Context, id, epoch int64, action, reason string) error {
	if action != "complete" && action != "pause" && action != "fail" {
		return ErrEnrichmentRunState
	}
	return s.transitionEnrichmentRun(ctx, id, epoch, action, reason)
}
func (s *Store) transitionEnrichmentRun(ctx context.Context, id, epoch int64, action, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return err
	}
	if epoch != 0 {
		if err = requireEnrichmentEpoch(ctx, tx, id, epoch); err != nil {
			return err
		}
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM enrichment_runs WHERE id=? AND durable_version=1`, id).Scan(&status); err != nil {
		return err
	}
	next := ""
	switch action {
	case "resume":
		if status != "paused" {
			return ErrEnrichmentRunState
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM enrichment_runs WHERE id<>? AND status IN ('queued','running')`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrEnrichmentRunState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE enrichment_run_items SET status='pending',claim_token=claim_token+1,rate_limit_count=0 WHERE run_id=? AND status<>'completed'`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE enrichment_runs SET status='running',epoch=epoch+1,pause_reason='',budget_baseline_ms=wait_total_ms WHERE id=?`, id)
	case "pause", "cancel", "fail":
		if status != "running" && !(action == "cancel" && status == "paused") {
			return ErrEnrichmentRunState
		}
		next = "paused"
		if action == "cancel" {
			next = "cancelled"
		}
		if action == "fail" {
			next = "failed"
		}
		_, err = tx.ExecContext(ctx, `UPDATE enrichment_runs SET status=?,pause_reason=?,error_message=CASE WHEN ?='failed' THEN ? ELSE error_message END,finished_at=CASE WHEN ? IN ('failed','cancelled') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE NULL END WHERE id=?`, next, reason, next, reason, next, id)
	case "complete":
		if status != "running" {
			return ErrEnrichmentRunState
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM enrichment_run_stages WHERE run_id=? AND prepared=0`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return ErrEnrichmentRunState
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM enrichment_run_items WHERE run_id=? AND status<>'completed'`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return ErrEnrichmentRunState
		}
		_, err = tx.ExecContext(ctx, `UPDATE enrichment_runs SET status='completed',error_message=NULLIF(?,''),finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),waiting_until=NULL,wait_source='' WHERE id=?`, reason, id)
	default:
		return ErrEnrichmentRunState
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RecoverDurableEnrichmentRuns(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE enrichment_runs SET status='paused',pause_reason='server_restart' WHERE durable_version=1 AND status IN ('queued','running')`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// SaveEnrichmentRequest checkpoints a validated response under the active
// generation. Successful responses reset consecutive response limits.
// SaveEnrichmentRequest preserves the compatibility API for non-secret keys.
func (s *Store) SaveEnrichmentRequest(ctx context.Context, c EnrichmentCheckpoint, source, key, endpoint string, status int, body []byte) error {
	return s.SaveEnrichmentRequestChecked(ctx, c, source, key, endpoint, "", "", status, body)
}
func (s *Store) SaveEnrichmentRequestChecked(ctx context.Context, c EnrichmentCheckpoint, source, key, endpoint, inputKey, configKey string, status int, body []byte) error {
	if err := validateEnrichmentRequestURL(endpoint); err != nil {
		return err
	}
	if status < 100 || status > 599 {
		return ErrEnrichmentRunState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return err
	}
	if err = requireEnrichmentEpoch(ctx, tx, c.RunID, c.Epoch); err != nil {
		return err
	}
	if c.ItemID != 0 {
		if err = requireEnrichmentCheckpoint(ctx, tx, c); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO enrichment_run_requests(run_id,source,request_key,request_url,input_key,config_key,status_code,body,checked_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,source,request_key,request_url) DO UPDATE SET input_key=excluded.input_key,config_key=excluded.config_key,status_code=excluded.status_code,body=excluded.body,checked_at=excluded.checked_at`, c.RunID, source, key, endpoint, inputKey, configKey, status, body, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if c.ItemID != 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE enrichment_run_items SET rate_limit_count=0 WHERE id=?`, c.ItemID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) EnrichmentRequest(ctx context.Context, runID int64, source, key, endpoint string) (int, []byte, error) {
	return s.EnrichmentRequestChecked(ctx, runID, source, key, endpoint, "", "", 30)
}
func (s *Store) EnrichmentRequestChecked(ctx context.Context, runID int64, source, key, endpoint, inputKey, configKey string, cacheDays int) (int, []byte, error) {
	var status int
	var body []byte
	var checked string
	err := s.db.QueryRowContext(ctx, `SELECT status_code,body,checked_at FROM enrichment_run_requests WHERE run_id=? AND source=? AND request_key=? AND request_url=? AND input_key=? AND config_key=?`, runID, source, key, endpoint, inputKey, configKey).Scan(&status, &body, &checked)
	if err != nil {
		return 0, nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil {
		return 0, nil, err
	}
	if !time.Now().Before(at.Add(time.Duration(cacheDays) * 24 * time.Hour)) {
		return 0, nil, sql.ErrNoRows
	}
	return status, body, nil
}
func (s *Store) RecordEnrichmentWait(ctx context.Context, c EnrichmentCheckpoint, source string, until time.Time, elapsed time.Duration, response bool) error {
	if elapsed < 0 {
		return ErrEnrichmentRunState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return err
	}
	if err = requireEnrichmentCheckpoint(ctx, tx, c); err != nil {
		if !response {
			tx.Rollback()
			return s.RecordInterruptedEnrichmentWait(ctx, c.RunID, c.Epoch, elapsed)
		}
		return err
	}
	increment := 0
	if response {
		increment = 1
	}
	if _, err = tx.ExecContext(ctx, `UPDATE enrichment_run_items SET rate_limit_count=rate_limit_count+? WHERE id=?`, increment, c.ItemID); err != nil {
		return err
	}
	var previous, previousSource string
	var total, baseline int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(waiting_until,''),wait_source,wait_total_ms,budget_baseline_ms FROM enrichment_runs WHERE id=?`, c.RunID).Scan(&previous, &previousSource, &total, &baseline); err != nil {
		return err
	}
	if previous != "" {
		old, e := time.Parse(time.RFC3339Nano, previous)
		if e != nil {
			return e
		}
		if old.After(until) {
			until = old
			source = previousSource
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE enrichment_runs SET waiting_until=?,wait_source=?,wait_total_ms=wait_total_ms+? WHERE id=?`, until.UTC().Format(time.RFC3339Nano), source, elapsed.Milliseconds(), c.RunID); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT rate_limit_count FROM enrichment_run_items WHERE id=?`, c.ItemID).Scan(&count); err != nil {
		return err
	}
	reason := ""
	if count >= 3 {
		reason = "rate_limit_count"
	}
	if total+elapsed.Milliseconds()-baseline >= int64(30*time.Minute/time.Millisecond) {
		reason = "rate_limit_wait_budget"
	}
	if reason != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE enrichment_runs SET status='paused',pause_reason=? WHERE id=?`, reason, c.RunID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if reason != "" {
		return ErrEnrichmentWaitBudget
	}
	return nil
}

// ApplyEnrichmentItem commits the caller's SQLite side effect and its completed
// outcome together. A completed item never re-enters apply. Callbacks must use
// the supplied tx, not Store methods that open a second transaction.
func (s *Store) ApplyEnrichmentItem(ctx context.Context, c EnrichmentCheckpoint, outcome string, apply func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enrichmentWriteLock(ctx, tx); err != nil {
		return err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM enrichment_run_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&status); err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if err = requireEnrichmentCheckpoint(ctx, tx, c); err != nil {
		return err
	}
	if apply != nil {
		if err = apply(tx); err != nil {
			return err
		}
	}
	if err = CompleteEnrichmentItemTx(ctx, tx, c, outcome); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordInterruptedEnrichmentWait records actual elapsed after user pause or
// cancel, but never changes terminal state or grants a new budget. The epoch
// is retained until resume, so an older generation cannot charge its successor.
func (s *Store) RecordInterruptedEnrichmentWait(ctx context.Context, runID, epoch int64, elapsed time.Duration) error {
	if elapsed < 0 {
		return ErrEnrichmentRunState
	}
	result, err := s.db.ExecContext(ctx, `UPDATE enrichment_runs SET wait_total_ms=wait_total_ms+? WHERE id=? AND epoch=? AND durable_version=1 AND status IN ('paused','cancelled')`, elapsed.Milliseconds(), runID, epoch)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrEnrichmentRunState
	}
	return nil
}
func (s *Store) SetEnrichmentCurrent(ctx context.Context, runID, epoch int64, stage, current string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE enrichment_runs SET stage=?,current=NULLIF(?,''),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND epoch=? AND durable_version=1 AND status='running'`, stage, current, runID, epoch)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrEnrichmentRunState
	}
	return nil
}

// URLs in durable checkpoints must never persist credentials, even hashed.
func validateEnrichmentRequestURL(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if parsed.User != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return ErrEnrichmentRunState
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		if strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "password") || strings.Contains(normalized, "api_key") || normalized == "apikey" || normalized == "key" {
			return ErrEnrichmentRunState
		}
	}
	return nil
}

type enrichmentCheckpointContextKey struct{}

func WithEnrichmentCheckpoint(ctx context.Context, c EnrichmentCheckpoint) context.Context {
	return context.WithValue(ctx, enrichmentCheckpointContextKey{}, c)
}
func EnrichmentCheckpointFromContext(ctx context.Context) (EnrichmentCheckpoint, bool) {
	c, ok := ctx.Value(enrichmentCheckpointContextKey{}).(EnrichmentCheckpoint)
	return c, ok
}

// RecordEnrichmentEffectTx records an idempotent phase or final outcome in the
// SAME transaction as the underlying mutation. It never marks work complete.
func RecordEnrichmentEffectTx(ctx context.Context, tx *sql.Tx, name, outcome string) error {
	c, ok := EnrichmentCheckpointFromContext(ctx)
	if !ok {
		return nil
	}
	if err := requireEnrichmentCheckpoint(ctx, tx, c); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO enrichment_item_effects(item_id,name,outcome) VALUES(?,?,?) ON CONFLICT(item_id,name) DO UPDATE SET outcome=excluded.outcome`, c.ItemID, name, outcome)
	return err
}
func (s *Store) EnrichmentEffect(ctx context.Context, c EnrichmentCheckpoint, name string) (string, error) {
	var outcome string
	err := s.db.QueryRowContext(ctx, `SELECT outcome FROM enrichment_item_effects WHERE item_id=? AND name=?`, c.ItemID, name).Scan(&outcome)
	return outcome, err
}
func (s *Store) UnfinishedDurableEnrichmentRun(ctx context.Context) (DurableEnrichmentRun, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM enrichment_runs WHERE durable_version=1 AND status IN ('running','queued','paused') ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return DurableEnrichmentRun{}, err
	}
	return s.DurableEnrichmentRun(ctx, id)
}

func (s *Store) CountEnrichmentRuns(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM enrichment_runs`).Scan(&count)
	return count, err
}
