package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

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
	mbid, err := s.ArtistExternalID(ctx, input.ID, "musicbrainz")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return input, err
	}
	input.LastFMQueryMBID = mbid
	input.LastFMQueryCaptured = true
	return input, nil
}

// ArtistSourceCheckNeeded separates identity eligibility from attachments.
// Review remains stable until its input/config changes or candidates disappear.
func (s *Store) ArtistSourceCheckNeeded(ctx context.Context, input ArtistMatchInput, setting MetadataSourceSetting) (ArtistSourceCheck, error) {
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
	_, err := s.ArtistExternalID(ctx, input.ID, setting.Source)
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
	result := ArtistSourceCheck{Eligible: !(fresh || outcome == "review" && pending)}
	if fresh {
		var v ArtistSourceSnapshot
		if err = json.Unmarshal([]byte(snapshot), &v); err != nil {
			return result, err
		}
		result.Snapshot = &v
	}
	return result, nil
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
	ik, ck := ArtistMatchKeys(input, setting)
	outcome := "no_result"
	if len(v.Candidates) > 0 {
		outcome = "review"
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
