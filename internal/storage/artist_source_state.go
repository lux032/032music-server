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

// FilterArtistsForScan evaluates incremental/full scan eligibility across all
// candidate artists in 3 bulk queries instead of O(N) per-artist queries and
// snapshot_json unmarshaling. Results match ArtistCompositeCheckNeeded and
// ArtistSourceCheckNeededForMode.
func (s *Store) FilterArtistsForScan(ctx context.Context, artists []ArtistMatchInput, settings []MetadataSourceSetting, mode ArtistScanMode) (eligible, composites []ArtistMatchInput, err error) {
	if len(artists) == 0 {
		return nil, nil, nil
	}
	rescanSettled := mode == ArtistScanFull
	var activeSettings []MetadataSourceSetting
	for _, setting := range settings {
		if (setting.Source == "musicbrainz" || setting.Source == "lastfm") && setting.Enabled && setting.AutoMatch {
			activeSettings = append(activeSettings, setting)
		}
	}

	// 1. Effective confirmed external IDs for every unmerged artist (own first,
	// then recursively inherited by smallest source artist ID).
	confirmed := map[int64]map[string]string{}
	confRows, err := s.db.QueryContext(ctx, `WITH RECURSIVE
		merge_chain(root_id, src_id) AS (
			SELECT id, id FROM artists WHERE merged_into_artist_id IS NULL
			UNION ALL
			SELECT mc.root_id, a.id FROM merge_chain mc JOIN artists a ON a.merged_into_artist_id = mc.src_id
		)
		SELECT mc.root_id, p.source, p.external_id
		FROM merge_chain mc
		JOIN artist_external_profiles p ON p.artist_id = mc.src_id
		ORDER BY mc.root_id, p.source, (p.artist_id = mc.root_id) DESC, p.artist_id ASC`)
	if err != nil {
		return nil, nil, err
	}
	for confRows.Next() {
		var rootID int64
		var src, extID string
		if err = confRows.Scan(&rootID, &src, &extID); err != nil {
			confRows.Close()
			return nil, nil, err
		}
		m := confirmed[rootID]
		if m == nil {
			m = map[string]string{}
			confirmed[rootID] = m
		}
		if _, exists := m[src]; !exists {
			m[src] = extID
		}
	}
	if err = confRows.Err(); err != nil {
		confRows.Close()
		return nil, nil, err
	}
	confRows.Close()

	// 2. Stored scan state metadata (excluding snapshot_json).
	type stateMeta struct {
		inputKey, configKey, outcome string
	}
	type artistSourceKey struct {
		artistID int64
		source   string
	}
	states := map[artistSourceKey]stateMeta{}
	stRows, err := s.db.QueryContext(ctx, `SELECT artist_id,source,input_key,config_key,outcome FROM artist_source_match_state`)
	if err != nil {
		return nil, nil, err
	}
	for stRows.Next() {
		var k artistSourceKey
		var v stateMeta
		if err = stRows.Scan(&k.artistID, &k.source, &v.inputKey, &v.configKey, &v.outcome); err != nil {
			stRows.Close()
			return nil, nil, err
		}
		states[k] = v
	}
	if err = stRows.Err(); err != nil {
		stRows.Close()
		return nil, nil, err
	}
	stRows.Close()

	// 3. Candidate pending / non-manual rejected flags per (artist, source) and per artist.
	type candFlags struct {
		hasPending, hasNonManualRejected bool
	}
	candBySource := map[artistSourceKey]candFlags{}
	candByArtist := map[int64]candFlags{}
	candRows, err := s.db.QueryContext(ctx, `SELECT artist_id,source,MAX(CASE WHEN status='candidate' THEN 1 ELSE 0 END),MAX(CASE WHEN status='rejected' AND evidence_json<>'["已手动解除为错误身份"]' THEN 1 ELSE 0 END) FROM artist_match_candidates GROUP BY artist_id,source`)
	if err != nil {
		return nil, nil, err
	}
	for candRows.Next() {
		var k artistSourceKey
		var pend, rej int
		if err = candRows.Scan(&k.artistID, &k.source, &pend, &rej); err != nil {
			candRows.Close()
			return nil, nil, err
		}
		f := candFlags{hasPending: pend != 0, hasNonManualRejected: rej != 0}
		candBySource[k] = f
		agg := candByArtist[k.artistID]
		agg.hasPending = agg.hasPending || f.hasPending
		agg.hasNonManualRejected = agg.hasNonManualRejected || f.hasNonManualRejected
		candByArtist[k.artistID] = agg
	}
	if err = candRows.Err(); err != nil {
		candRows.Close()
		return nil, nil, err
	}
	candRows.Close()

	exempt, err := compositeExemptArtistIDs(ctx, s.db)
	if err != nil {
		return nil, nil, err
	}
	for _, artist := range artists {
		if m := confirmed[artist.ID]; m != nil {
			artist.LastFMQueryMBID = m["musicbrainz"]
		}
		artist.LastFMQueryCaptured = true

		if isCompositeWithExemptions(exempt, artist.ID, artist.Name) {
			if rescanSettled {
				composites = append(composites, artist)
				continue
			}
			ik, _ := ArtistMatchKeys(artist, MetadataSourceSetting{Source: compositeStateSource})
			st, hasState := states[artistSourceKey{artistID: artist.ID, source: compositeStateSource}]
			if !hasState {
				agg := candByArtist[artist.ID]
				if !(!agg.hasPending && agg.hasNonManualRejected) {
					composites = append(composites, artist)
				}
			} else if st.inputKey != ik || st.outcome == "error" {
				composites = append(composites, artist)
			}
			continue
		}

		for _, setting := range activeSettings {
			if m := confirmed[artist.ID]; m != nil {
				if _, ok := m[setting.Source]; ok {
					continue
				}
			}
			ik, ck := ArtistMatchKeys(artist, setting)
			key := artistSourceKey{artistID: artist.ID, source: setting.Source}
			st, hasState := states[key]
			cf := candBySource[key]
			var isEligible bool
			if !hasState {
				if !rescanSettled && !cf.hasPending && cf.hasNonManualRejected {
					isEligible = false
				} else {
					isEligible = true
				}
			} else if st.inputKey != ik || st.configKey != ck || st.outcome == "error" {
				isEligible = true
			} else if cf.hasPending {
				isEligible = false
			} else if rescanSettled {
				isEligible = true
			}
			if isEligible {
				eligible = append(eligible, artist)
				break
			}
		}
	}
	return eligible, composites, nil
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
