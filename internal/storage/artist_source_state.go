package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ArtistScanMode string

const (
	ArtistScanIncremental ArtistScanMode = "incremental"
	ArtistScanFull        ArtistScanMode = "full"

	compositeStateSource = "_composite"
)

func ParseArtistScanMode(raw string) ArtistScanMode {
	if strings.EqualFold(strings.TrimSpace(raw), string(ArtistScanFull)) {
		return ArtistScanFull
	}
	return ArtistScanIncremental
}

type ArtistSourceSnapshot struct {
	Independent          *bool                            `json:"independent,omitempty"`
	QueriedByMBID        string                           `json:"queriedByMBID,omitempty"`
	UnsafeTaggedIdentity bool                             `json:"unsafeTaggedIdentity,omitempty"`
	Candidates           []ArtistCandidate                `json:"candidates"`
	Profiles             map[string]ExternalArtistProfile `json:"profiles"`
}
type ArtistSourceCheck struct {
	Eligible bool
	Snapshot *ArtistSourceSnapshot
}

func ArtistMatchKeys(input ArtistMatchInput, setting MetadataSourceSetting) (string, string) {
	dependency := ""
	if setting.Source == "lastfm" {
		dependency = input.LastFMQueryMBID
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(input.Name+"\x00"+input.TaggedMBID+"\x00"+dependency))), fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("artist-match-v2|%s|%t|%t|%s|%t", setting.Source, setting.Enabled, setting.AutoMatch, setting.Language, setting.APIKey != ""))))
}

// ArtistMatchQueryContext captures the binding used for a Last.fm request.
// Callers must retain this value through request and checkpoint; never recapture
// a changed identity after HTTP returns.
func (s *Store) ArtistMatchQueryContext(ctx context.Context, input ArtistMatchInput) (ArtistMatchInput, error) {
	if input.LastFMQueryCaptured {
		return input, nil
	}
	mbid, err := s.ArtistEffectiveExternalID(ctx, input.ID, "musicbrainz")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return input, err
	}
	input.LastFMQueryMBID = mbid
	input.LastFMQueryCaptured = true
	return input, nil
}

// ArtistSourceCheckNeeded separates identity eligibility from attachments using
// the source CacheDays cooldown. Review remains stable until its input/config
// changes or candidates disappear.
func (s *Store) ArtistSourceCheckNeeded(ctx context.Context, input ArtistMatchInput, setting MetadataSourceSetting) (ArtistSourceCheck, error) {
	return s.artistSourceCheckNeeded(ctx, input, setting, true, false)
}

// ArtistSourceCheckNeededForMode determines scan eligibility for incremental
// (initial/never-scanned) vs full (re-scan settled unconfirmed artists) runs.
func (s *Store) ArtistSourceCheckNeededForMode(ctx context.Context, input ArtistMatchInput, setting MetadataSourceSetting, mode ArtistScanMode) (ArtistSourceCheck, error) {
	return s.artistSourceCheckNeeded(ctx, input, setting, false, mode == ArtistScanFull)
}

func (s *Store) artistSourceCheckNeeded(ctx context.Context, input ArtistMatchInput, setting MetadataSourceSetting, useTTL, rescanSettled bool) (ArtistSourceCheck, error) {
	var contextErr error
	if setting.Source == "lastfm" {
		input, contextErr = s.ArtistMatchQueryContext(ctx, input)
		if contextErr != nil {
			return ArtistSourceCheck{}, contextErr
		}
	}
	if !setting.Enabled || !setting.AutoMatch {
		return ArtistSourceCheck{}, nil
	}
	_, err := s.ArtistEffectiveExternalID(ctx, input.ID, setting.Source)
	if err == nil {
		return ArtistSourceCheck{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ArtistSourceCheck{}, err
	}
	ik, ck := ArtistMatchKeys(input, setting)
	var storedInput, storedConfig, outcome, checked, snapshot string
	err = s.db.QueryRowContext(ctx, `SELECT input_key,config_key,outcome,checked_at,snapshot_json FROM artist_source_match_state WHERE artist_id=? AND source=?`, input.ID, setting.Source).Scan(&storedInput, &storedConfig, &outcome, &checked, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		if !useTTL && !rescanSettled {
			var settledRejected bool
			if qErr := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM artist_match_candidates WHERE artist_id=? AND source=? AND status='candidate') AND EXISTS(SELECT 1 FROM artist_match_candidates WHERE artist_id=? AND source=? AND status='rejected' AND evidence_json<>'["已手动解除为错误身份"]')`, input.ID, setting.Source, input.ID, setting.Source).Scan(&settledRejected); qErr != nil {
				return ArtistSourceCheck{}, qErr
			}
			if settledRejected {
				return ArtistSourceCheck{Eligible: false}, nil
			}
		}
		return ArtistSourceCheck{Eligible: true}, nil
	}
	if err != nil {
		return ArtistSourceCheck{}, err
	}
	if storedInput != ik || storedConfig != ck || outcome == "error" {
		return ArtistSourceCheck{Eligible: true}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil {
		return ArtistSourceCheck{}, err
	}
	fresh := time.Now().Before(at.Add(time.Duration(setting.CacheDays) * 24 * time.Hour))
	var pending bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_match_candidates WHERE artist_id=? AND source=? AND status='candidate')`, input.ID, setting.Source).Scan(&pending); err != nil {
		return ArtistSourceCheck{}, err
	}
	var eligible bool
	switch {
	case pending:
		eligible = false
	case rescanSettled:
		eligible = true
	case useTTL:
		eligible = !fresh
	default:
		// Incremental mode never re-queues already-scanned artists on TTL expiry.
		eligible = false
	}
	result := ArtistSourceCheck{Eligible: eligible}
	if fresh && outcome != "rejected" {
		var v ArtistSourceSnapshot
		if err = json.Unmarshal([]byte(snapshot), &v); err != nil {
			return result, err
		}
		result.Snapshot = &v
	}
	return result, nil
}

// ArtistCompositeCheckNeeded reports whether a composite-credit artist should
// participate in a scan run of the given mode.
func (s *Store) ArtistCompositeCheckNeeded(ctx context.Context, input ArtistMatchInput, mode ArtistScanMode) (bool, error) {
	if mode == ArtistScanFull {
		return true, nil
	}
	ik, _ := ArtistMatchKeys(input, MetadataSourceSetting{Source: compositeStateSource})
	var storedInput, outcome string
	err := s.db.QueryRowContext(ctx, `SELECT input_key,outcome FROM artist_source_match_state WHERE artist_id=? AND source=?`, input.ID, compositeStateSource).Scan(&storedInput, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		var settledRejected bool
		if qErr := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM artist_match_candidates WHERE artist_id=? AND status='candidate') AND EXISTS(SELECT 1 FROM artist_match_candidates WHERE artist_id=? AND status='rejected' AND evidence_json<>'["已手动解除为错误身份"]')`, input.ID, input.ID).Scan(&settledRejected); qErr != nil {
			return false, qErr
		}
		return !settledRejected, nil
	}
	if err != nil {
		return false, err
	}
	if storedInput != ik || outcome == "error" {
		return true, nil
	}
	return false, nil
}

// SaveArtistCompositeSkip records that a composite-credit artist was skipped so
// incremental scans do not repeatedly re-queue it until its name/tags change.
func (s *Store) SaveArtistCompositeSkip(ctx context.Context, input ArtistMatchInput, checkpoint *ArtistRunCheckpoint) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireArtistCheckpoint(ctx, tx, checkpoint, input.ID); err != nil {
		return err
	}
	if err = upsertArtistMatchStateTx(ctx, tx, input, MetadataSourceSetting{Source: compositeStateSource}, "skipped_composite", time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertArtistMatchStateTx(ctx context.Context, tx *sql.Tx, input ArtistMatchInput, setting MetadataSourceSetting, outcome string, now time.Time) error {
	ik, ck := ArtistMatchKeys(input, setting)
	nextCheck := now
	if setting.CacheDays > 0 {
		nextCheck = now.Add(time.Duration(setting.CacheDays) * 24 * time.Hour)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO artist_source_match_state(artist_id,source,input_key,config_key,outcome,checked_at,next_check_at,snapshot_json) VALUES(?,?,?,?,?,?,?,'{}') ON CONFLICT(artist_id,source) DO UPDATE SET input_key=excluded.input_key,config_key=excluded.config_key,outcome=excluded.outcome,checked_at=excluded.checked_at,next_check_at=excluded.next_check_at`, input.ID, setting.Source, ik, ck, outcome, now.Format(time.RFC3339Nano), nextCheck.Format(time.RFC3339Nano))
	return err
}

// SaveArtistSourceCheck and source-scoped candidate checkpoint commit together.
func (s *Store) SaveArtistSourceCheck(ctx context.Context, input ArtistMatchInput, setting MetadataSourceSetting, v ArtistSourceSnapshot) error {
	return s.saveArtistSourceCheck(ctx, input, setting, v, nil)
}
func (s *Store) SaveArtistRunSourceCheck(ctx context.Context, input ArtistMatchInput, setting MetadataSourceSetting, v ArtistSourceSnapshot, c ArtistRunCheckpoint) error {
	return s.saveArtistSourceCheck(ctx, input, setting, v, &c)
}
func (s *Store) saveArtistSourceCheck(ctx context.Context, input ArtistMatchInput, setting MetadataSourceSetting, v ArtistSourceSnapshot, checkpoint *ArtistRunCheckpoint) error {

	if setting.Source == "lastfm" && !input.LastFMQueryCaptured {
		return errors.New("Last.fm query context not captured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_candidates SET status=status WHERE 0`); err != nil {
		return err
	}
	if err = requireArtistCheckpoint(ctx, tx, checkpoint, input.ID); err != nil {
		return err
	}
	if err = replaceArtistCandidatesTx(ctx, tx, input.ID, v.Candidates, []string{setting.Source}); err != nil {
		return err
	}
	var hasPending, hasRejected bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_match_candidates WHERE artist_id=? AND source=? AND status='candidate'),EXISTS(SELECT 1 FROM artist_match_candidates WHERE artist_id=? AND source=? AND status='rejected')`, input.ID, setting.Source, input.ID, setting.Source).Scan(&hasPending, &hasRejected); err != nil {
		return err
	}
	ik, ck := ArtistMatchKeys(input, setting)
	outcome := "no_result"
	switch {
	case hasPending:
		outcome = "review"
	case hasRejected && len(v.Candidates) > 0:
		outcome = "rejected"
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO artist_source_match_state(artist_id,source,input_key,config_key,outcome,checked_at,next_check_at,snapshot_json) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(artist_id,source) DO UPDATE SET input_key=excluded.input_key,config_key=excluded.config_key,outcome=excluded.outcome,checked_at=excluded.checked_at,next_check_at=excluded.next_check_at,snapshot_json=excluded.snapshot_json`, input.ID, setting.Source, ik, ck, outcome, now.Format(time.RFC3339Nano), now.Add(time.Duration(setting.CacheDays)*24*time.Hour).Format(time.RFC3339Nano), string(encoded))
	if err != nil {
		return err
	}
	if checkpoint != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE artist_match_run_items SET rate_limit_count=0 WHERE id=?`, checkpoint.ItemID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_match_item_sources(item_id,source,input_key,config_key,snapshot_json,checked_at) VALUES(?,?,?,?,?,?) ON CONFLICT(item_id,source) DO UPDATE SET input_key=excluded.input_key,config_key=excluded.config_key,snapshot_json=excluded.snapshot_json,checked_at=excluded.checked_at`, checkpoint.ItemID, setting.Source, ik, ck, string(encoded), now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
