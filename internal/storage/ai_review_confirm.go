package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

type aiAlbumConfirmSnapshot struct {
	CandidateID          int64   `json:"candidateId"`
	MusicExternalID      string  `json:"musicExternalId"`
	WorkIDs              []int64 `json:"workIds"`
	RejectedCandidateIDs []int64 `json:"rejectedCandidateIds,omitempty"`
}

type aiTrackConfirmSnapshot struct {
	CandidateID          int64            `json:"candidateId"`
	MusicExternalID      string           `json:"musicExternalId"`
	WorkIDs              []int64          `json:"workIds"`
	Roles                map[int64]string `json:"roles,omitempty"`
	RejectedCandidateIDs []int64          `json:"rejectedCandidateIds,omitempty"`
}

type aiWorkConfirmSnapshot struct {
	CandidateID          int64   `json:"candidateId"`
	ExternalID           string  `json:"externalId"`
	RejectedCandidateIDs []int64 `json:"rejectedCandidateIds,omitempty"`
}

type aiArtistConfirmSnapshot struct {
	CandidateID          int64   `json:"candidateId"`
	Source               string  `json:"source"`
	ExternalID           string  `json:"externalId"`
	RejectedCandidateIDs []int64 `json:"rejectedCandidateIds,omitempty"`
}

// ConfirmAlbumSubjectCandidateByAI lands selected works for an album candidate
// on behalf of AI review while enforcing every automatic safety and suppression
// check. It returns a rollback snapshot alongside the created/linked work IDs.
func (s *Store) ConfirmAlbumSubjectCandidateByAI(ctx context.Context, albumID, candidateID int64, selected []int64) (string, []int64, json.RawMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, nil, err
	}
	defer tx.Rollback()

	first, err := tx.ExecContext(ctx, `UPDATE albums SET work_fingerprint=work_fingerprint WHERE id=?`, albumID)
	if err != nil {
		return "", nil, nil, err
	}
	if n, _ := first.RowsAffected(); n == 0 {
		return "skipped", nil, nil, nil
	}

	var music, musicTitle, tieJSON, status string
	if err = tx.QueryRowContext(ctx, `SELECT external_id,title,tieups_json,status FROM album_subject_candidates WHERE album_id=? AND id=?`, albumID, candidateID).
		Scan(&music, &musicTitle, &tieJSON, &status); err != nil {
		return "", nil, nil, err
	}
	if status != "candidate" {
		return "review", nil, nil, nil
	}
	var linked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_works WHERE album_id=? AND source IN ('manual','bangumi'))`, albumID).Scan(&linked); err != nil {
		return "", nil, nil, err
	}
	if linked {
		return "review", nil, nil, nil
	}
	var suppressed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=? AND inferred_key LIKE 'bangumi:%')`, albumID).Scan(&suppressed); err != nil {
		return "", nil, nil, err
	}
	if suppressed {
		return "review", nil, nil, nil
	}

	var ties []BangumiTieup
	if err = json.Unmarshal([]byte(tieJSON), &ties); err != nil {
		return "", nil, nil, err
	}
	chosen := make(map[int64]bool, len(selected))
	for _, id := range selected {
		chosen[id] = true
	}
	if len(chosen) == 0 {
		return "review", nil, nil, nil
	}

	var workIDs []int64
	for _, tie := range ties {
		if !chosen[tie.SubjectID] {
			continue
		}
		if tie.Type != "anime" && tie.Type != "movie" && tie.Type != "game" {
			continue
		}
		workSubjectID := strconv.FormatInt(tie.SubjectID, 10)
		key := bangumiWorkKey(music, workSubjectID)
		var linkSuppressed bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=? AND inferred_key=?) OR EXISTS(SELECT 1 FROM album_subject_candidates WHERE album_id=? AND external_id=? AND status='rejected')`, albumID, key, albumID, music).Scan(&linkSuppressed); err != nil {
			return "", nil, nil, err
		}
		if linkSuppressed {
			return "review", nil, nil, nil
		}
		keys, e := suppressionKeys(ctx, tx, `SELECT inferred_key FROM album_work_suppressions WHERE album_id=?`, albumID)
		if e != nil {
			return "", nil, nil, e
		}
		for _, t := range []string{tie.Title, tie.NameCN} {
			if t == "" {
				continue
			}
			for _, stored := range keys {
				if workTitleKeysMatch(stored, workTitleSuppressionKey(t, tie.Type)) {
					return "review", nil, nil, nil
				}
			}
		}

		id, e := resolveBangumiWork(ctx, tx, tie, 0)
		if errors.Is(e, ErrBangumiWorkIdentityConflict) {
			return "review", nil, nil, nil
		}
		if e != nil {
			return "", nil, nil, e
		}
		identities, e := workSuppressionIdentities(ctx, tx, id)
		if e != nil {
			return "", nil, nil, e
		}
		for _, stored := range keys {
			for _, ident := range identities {
				if workTitleKeysMatch(stored, workTitleSuppressionKey(ident.Title, ident.Type)) {
					return "review", nil, nil, nil
				}
			}
		}

		albumRole := "other"
		if tie.Role == "ost" {
			albumRole = "ost"
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,?,'bangumi',?) ON CONFLICT(album_id,work_id) DO UPDATE SET role=excluded.role,source='bangumi',inferred_key=excluded.inferred_key,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE album_works.source='auto'`, albumID, id, albumRole, key)
		if e != nil {
			return "", nil, nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE album_works SET inferred_key=? WHERE album_id=? AND work_id=? AND source='manual' AND inferred_key IS NULL`, key, albumID, id); e != nil {
			return "", nil, nil, e
		}
		if tie.Role != "ost" && tie.Role != "other" {
			if e = linkBangumiAlbumTracks(ctx, tx, albumID, id, musicTitle, tie.Role, key, false); e != nil {
				return "", nil, nil, e
			}
		}
		workIDs = append(workIDs, id)
	}
	if len(workIDs) == 0 {
		return "review", nil, nil, nil
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM album_works WHERE album_id=? AND source='auto'`, albumID); err != nil {
		return "", nil, nil, err
	}

	rejectedIDs, err := candidateSiblingIDsTx(ctx, tx, `SELECT id FROM album_subject_candidates WHERE album_id=? AND id<>? AND status='candidate'`, albumID, candidateID)
	if err != nil {
		return "", nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE album_subject_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE album_id=? AND status='candidate'`, candidateID, albumID); err != nil {
		return "", nil, nil, err
	}
	if err = RecordEnrichmentEffectTx(ctx, tx, "final", "matched"); err != nil {
		return "", nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return "", nil, nil, err
	}

	snap, _ := json.Marshal(aiAlbumConfirmSnapshot{
		CandidateID:          candidateID,
		MusicExternalID:      music,
		WorkIDs:              workIDs,
		RejectedCandidateIDs: rejectedIDs,
	})
	return "succeeded", workIDs, snap, nil
}

// ConfirmTrackSubjectCandidateByAI lands AI-selected works and refined roles
// (including multi_title disambiguation) with source='bangumi' so manual
// priority (R1) remains intact.
func (s *Store) ConfirmTrackSubjectCandidateByAI(ctx context.Context, trackID, candidateID int64, selected []int64, roles map[int64]string) (string, []int64, json.RawMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, nil, err
	}
	defer tx.Rollback()

	first, err := tx.ExecContext(ctx, `UPDATE tracks SET id=id WHERE id=?`, trackID)
	if err != nil {
		return "", nil, nil, err
	}
	if n, _ := first.RowsAffected(); n == 0 {
		return "skipped", nil, nil, nil
	}

	var albumID int64
	if err = tx.QueryRowContext(ctx, `SELECT album_id FROM tracks WHERE id=?`, trackID).Scan(&albumID); err != nil {
		return "", nil, nil, err
	}
	var music, tieJSON, status string
	if err = tx.QueryRowContext(ctx, `SELECT external_id,tieups_json,status FROM track_subject_candidates WHERE track_id=? AND id=?`, trackID, candidateID).
		Scan(&music, &tieJSON, &status); err != nil {
		return "", nil, nil, err
	}
	if status != "candidate" {
		return "review", nil, nil, nil
	}
	var blocked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM track_work_suppressions WHERE track_id=? AND inferred_key LIKE 'bangumi:%') OR EXISTS(SELECT 1 FROM work_tracks WHERE track_id=? AND source='manual') OR EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=? AND inferred_key=?) OR EXISTS(SELECT 1 FROM album_subject_candidates WHERE album_id=? AND external_id=? AND status='rejected')`,
		trackID, trackID, albumID, "bangumi:"+music, albumID, music).Scan(&blocked); err != nil {
		return "", nil, nil, err
	}
	if blocked {
		return "review", nil, nil, nil
	}

	var ties []BangumiTieup
	if err = json.Unmarshal([]byte(tieJSON), &ties); err != nil {
		return "", nil, nil, err
	}
	chosen := make(map[int64]bool, len(selected))
	for _, id := range selected {
		chosen[id] = true
	}
	if len(chosen) == 0 {
		return "review", nil, nil, nil
	}

	var workIDs []int64
	appliedRoles := map[int64]string{}
	for _, tie := range ties {
		if !chosen[tie.SubjectID] {
			continue
		}
		if tie.Type != "anime" && tie.Type != "movie" && tie.Type != "game" {
			continue
		}
		suppressed, e := bangumiTrackLinkSuppressed(ctx, tx, trackID, albumID, music, tie, 0)
		if e != nil {
			return "", nil, nil, e
		}
		if suppressed {
			return "review", nil, nil, nil
		}
		id, e := resolveBangumiWork(ctx, tx, tie, 0)
		if errors.Is(e, ErrBangumiWorkIdentityConflict) {
			return "review", nil, nil, nil
		}
		if e != nil {
			return "", nil, nil, e
		}
		suppressed, e = bangumiTrackLinkSuppressed(ctx, tx, trackID, albumID, music, tie, id)
		if e != nil {
			return "", nil, nil, e
		}
		if suppressed {
			return "review", nil, nil, nil
		}

		role := tie.Role
		if override := strings.ToLower(strings.TrimSpace(roles[tie.SubjectID])); override != "" {
			if !validBangumiTrackRoles[override] {
				return "", nil, nil, ErrInvalidWork
			}
			// Never downgrade an already specific Bangumi role.
			if tie.Role == "other" || tie.Role == "theme" || tie.Role == "image_song" || tie.Role == override {
				role = override
			}
		}
		key := bangumiWorkKey(music, strconv.FormatInt(tie.SubjectID, 10))
		if _, e = landBangumiTrack(ctx, tx, trackID, id, role, key); e != nil {
			return "", nil, nil, e
		}
		workIDs = append(workIDs, id)
		appliedRoles[id] = role
	}
	if len(workIDs) == 0 {
		return "review", nil, nil, nil
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE track_id=? AND source='auto'`, trackID); err != nil {
		return "", nil, nil, err
	}
	rejectedIDs, err := candidateSiblingIDsTx(ctx, tx, `SELECT id FROM track_subject_candidates WHERE track_id=? AND id<>? AND status='candidate'`, trackID, candidateID)
	if err != nil {
		return "", nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE track_subject_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE track_id=? AND status='candidate'`, candidateID, trackID); err != nil {
		return "", nil, nil, err
	}
	if err = RecordEnrichmentEffectTx(ctx, tx, "final", "matched"); err != nil {
		return "", nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return "", nil, nil, err
	}

	snap, _ := json.Marshal(aiTrackConfirmSnapshot{
		CandidateID:          candidateID,
		MusicExternalID:      music,
		WorkIDs:              workIDs,
		Roles:                appliedRoles,
		RejectedCandidateIDs: rejectedIDs,
	})
	return "succeeded", workIDs, snap, nil
}

func (s *Store) ConfirmWorkMatchCandidateByAI(ctx context.Context, workID, candidateID int64) (json.RawMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var eligible bool
	var externalID string
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_match_candidates WHERE id=? AND work_id=? AND source='bangumi' AND status='candidate') AND NOT EXISTS(SELECT 1 FROM work_external_profiles WHERE work_id=? AND source='bangumi') AND NOT EXISTS(SELECT 1 FROM work_match_candidates WHERE work_id=? AND source='bangumi' AND status='confirmed') AND NOT EXISTS(SELECT 1 FROM work_external_profiles p JOIN work_match_candidates c ON c.id=? AND c.source=p.source AND c.external_id=p.external_id WHERE p.work_id<>?), COALESCE((SELECT external_id FROM work_match_candidates WHERE id=? AND work_id=?),'')`,
		candidateID, workID, workID, workID, candidateID, workID, candidateID, workID).Scan(&eligible, &externalID)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, ErrAutoConfirmConflict
	}
	rejectedIDs, err := candidateSiblingIDsTx(ctx, tx, `SELECT id FROM work_match_candidates WHERE work_id=? AND id<>? AND status='candidate'`, workID, candidateID)
	if err != nil {
		return nil, err
	}
	if err = confirmWorkMatchCandidateTx(ctx, tx, workID, candidateID, 0); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_enrichment_retries WHERE work_id=? AND source='bangumi'`, workID); err != nil {
		return nil, err
	}
	if err = RecordEnrichmentEffectTx(ctx, tx, "final", "matched"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	snap, _ := json.Marshal(aiWorkConfirmSnapshot{
		CandidateID:          candidateID,
		ExternalID:           externalID,
		RejectedCandidateIDs: rejectedIDs,
	})
	return snap, nil
}

func (s *Store) ConfirmArtistCandidateByAI(ctx context.Context, artistID, candidateID int64) (bool, json.RawMessage, error) {
	var bound bool
	var snap json.RawMessage
	err := withBusyRetry(ctx, func() error {
		bound = false
		snap = nil
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var name string
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=? AND merged_into_artist_id IS NULL`, artistID).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if metadata.CompositeArtistCredit(name) {
			return nil
		}

		var p ExternalArtistProfile
		var status, aliases, tags, raw string
		var manualOnly int
		err = tx.QueryRowContext(ctx, `SELECT status,requires_manual_review,source,external_id,display_name,COALESCE(sort_name,''),COALESCE(json_extract(payload_json,'$.pageUrl'),''),COALESCE(json_extract(payload_json,'$.imageUrl'),''),COALESCE(json_extract(payload_json,'$.biography'),''),COALESCE(country,''),COALESCE(artist_type,''),COALESCE(disambiguation,''),COALESCE(json_extract(payload_json,'$.aliases'),'[]'),COALESCE(json_extract(payload_json,'$.tags'),'[]'),payload_json FROM artist_match_candidates WHERE id=? AND artist_id=?`, candidateID, artistID).
			Scan(&status, &manualOnly, &p.Source, &p.ExternalID, &p.DisplayName, &p.SortName, &p.PageURL, &p.RemoteImageURL, &p.Biography, &p.Country, &p.ArtistType, &p.Disambiguation, &aliases, &tags, &raw)
		if errors.Is(err, sql.ErrNoRows) || status != "candidate" || manualOnly != 0 {
			return nil
		}
		if err != nil {
			return err
		}

		var existing string
		err = tx.QueryRowContext(ctx, `SELECT external_id FROM artist_external_profiles WHERE artist_id=? AND source=?`, artistID, p.Source).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			return nil
		}
		if err = artistIdentityOwnerCheck(ctx, tx, artistID, p.Source, p.ExternalID); err != nil {
			var conflict *ArtistExternalIDConflictError
			if errors.As(err, &conflict) {
				return nil
			}
			return err
		}

		_ = json.Unmarshal([]byte(aliases), &p.Aliases)
		_ = json.Unmarshal([]byte(tags), &p.Tags)
		p.Raw = json.RawMessage(raw)
		p.FetchedAt = time.Now().UTC().Format(time.RFC3339)
		if err = upsertExternalArtistProfile(ctx, tx, artistID, p); err != nil {
			return err
		}

		rejectedIDs, err := candidateSiblingIDsTx(ctx, tx, `SELECT id FROM artist_match_candidates WHERE artist_id=? AND id<>? AND status='candidate'`, artistID, candidateID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_match_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END WHERE artist_id=? AND status='candidate'`, candidateID, artistID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		bound = true
		snap, _ = json.Marshal(aiArtistConfirmSnapshot{
			CandidateID:          candidateID,
			Source:               p.Source,
			ExternalID:           p.ExternalID,
			RejectedCandidateIDs: rejectedIDs,
		})
		return nil
	})
	return bound, snap, err
}

func candidateSiblingIDsTx(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
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

// RevertAIReviewDecision undoes an AI auto-confirmed decision if the target has
// not been manually overwritten since, returning its candidates to 'candidate'.
func (s *Store) RevertAIReviewDecision(ctx context.Context, decisionID int64) error {
	d, err := s.AIReviewDecisionByID(ctx, decisionID)
	if err != nil {
		return err
	}
	if !d.Applied || d.Reverted {
		return errors.New("该 AI 决策尚未自动应用或已撤销")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	switch d.TargetKind {
	case AITargetAlbumSubject:
		var snap aiAlbumConfirmSnapshot
		if err = json.Unmarshal(d.AppliedSnapshot, &snap); err != nil {
			return err
		}
		for _, workID := range snap.WorkIDs {
			key := bangumiWorkKey(snap.MusicExternalID, strconv.FormatInt(workID, 10))
			if _, err = tx.ExecContext(ctx, `DELETE FROM album_works WHERE album_id=? AND work_id=? AND source='bangumi' AND inferred_key LIKE ?`, d.TargetID, workID, "bangumi:"+snap.MusicExternalID+":%"); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE track_id IN (SELECT id FROM tracks WHERE album_id=?) AND work_id=? AND source='bangumi' AND inferred_key LIKE ?`, d.TargetID, workID, "bangumi:"+snap.MusicExternalID+":%"); err != nil {
				return err
			}
			_ = key
		}
		restoreIDs := append([]int64{snap.CandidateID}, snap.RejectedCandidateIDs...)
		if err = restoreCandidateStatusTx(ctx, tx, "album_subject_candidates", restoreIDs); err != nil {
			return err
		}

	case AITargetTrackSubject:
		var snap aiTrackConfirmSnapshot
		if err = json.Unmarshal(d.AppliedSnapshot, &snap); err != nil {
			return err
		}
		for _, workID := range snap.WorkIDs {
			if _, err = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE track_id=? AND work_id=? AND source='bangumi' AND inferred_key LIKE ?`, d.TargetID, workID, "bangumi:"+snap.MusicExternalID+":%"); err != nil {
				return err
			}
		}
		restoreIDs := append([]int64{snap.CandidateID}, snap.RejectedCandidateIDs...)
		if err = restoreCandidateStatusTx(ctx, tx, "track_subject_candidates", restoreIDs); err != nil {
			return err
		}

	case AITargetWorkMatch:
		var snap aiWorkConfirmSnapshot
		if err = json.Unmarshal(d.AppliedSnapshot, &snap); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_external_profiles WHERE work_id=? AND source='bangumi' AND external_id=?`, d.TargetID, snap.ExternalID); err != nil {
			return err
		}
		restoreIDs := append([]int64{snap.CandidateID}, snap.RejectedCandidateIDs...)
		if err = restoreCandidateStatusTx(ctx, tx, "work_match_candidates", restoreIDs); err != nil {
			return err
		}

	case AITargetArtistMatch:
		var snap aiArtistConfirmSnapshot
		if err = json.Unmarshal(d.AppliedSnapshot, &snap); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM artist_external_profiles WHERE artist_id=? AND source=? AND external_id=?`, d.TargetID, snap.Source, snap.ExternalID); err != nil {
			return err
		}
		restoreIDs := append([]int64{snap.CandidateID}, snap.RejectedCandidateIDs...)
		if err = restoreCandidateStatusTx(ctx, tx, "artist_match_candidates", restoreIDs); err != nil {
			return err
		}

	default:
		return errors.New("该类型的 AI 操作不支持自动撤销")
	}

	if _, err = tx.ExecContext(ctx, `UPDATE ai_review_decisions SET reverted=1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, decisionID); err != nil {
		return err
	}
	return tx.Commit()
}

func restoreCandidateStatusTx(ctx context.Context, tx *sql.Tx, table string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	// artist_match_candidates has no updated_at column.
	query := fmt.Sprintf(`UPDATE %s SET status='candidate',updated_at=strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now') WHERE id IN (SELECT value FROM json_each(?))`, table)
	if table == "artist_match_candidates" {
		query = `UPDATE artist_match_candidates SET status='candidate' WHERE id IN (SELECT value FROM json_each(?))`
	}
	_, err = tx.ExecContext(ctx, query, string(encoded))
	return err
}

// ApplyCompositeCreditSplit replaces compositeArtistID in primary track credits
// across all tracks where it appears using the existing PrimaryArtistCorrection
// state machine so merges/rescans stay consistent.
func (s *Store) ApplyCompositeCreditSplit(ctx context.Context, compositeArtistID int64, partNames []string) (int, error) {
	var cleaned []string
	seen := map[string]bool{}
	for _, name := range partNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := metadata.Normalize(name)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, name)
	}
	if len(cleaned) == 0 || len(cleaned) > 32 {
		return 0, errors.New("拆分后的艺人数量必须为 1 至 32 位")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	partIDs, err := ensureArtists(ctx, tx, cleaned)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}

	trackIDs, err := s.queryInt64List(ctx, `SELECT DISTINCT track_id FROM track_artists WHERE artist_id=? AND role='primary' ORDER BY track_id`, compositeArtistID)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, trackID := range trackIDs {
		snap, err := s.PrimaryArtistCorrection(ctx, trackID)
		if err != nil {
			return updated, err
		}
		var nextIDs []int64
		seenID := map[int64]bool{}
		replaced := false
		for _, cur := range snap.Artists {
			if cur.ID == compositeArtistID {
				replaced = true
				for _, pid := range partIDs {
					if !seenID[pid] {
						seenID[pid] = true
						nextIDs = append(nextIDs, pid)
					}
				}
				continue
			}
			if !seenID[cur.ID] {
				seenID[cur.ID] = true
				nextIDs = append(nextIDs, cur.ID)
			}
		}
		if !replaced || len(nextIDs) == 0 {
			continue
		}
		if err = s.SetPrimaryArtistCorrection(ctx, trackID, snap.Token, nextIDs, false); err != nil {
			return updated, err
		}
		updated++
	}
	if updated > 0 {
		var remaining int
		if qErr := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM track_artists WHERE artist_id=?)+(SELECT count(*) FROM album_artists WHERE artist_id=?)`, compositeArtistID, compositeArtistID).Scan(&remaining); qErr == nil && remaining == 0 {
			_, _ = s.db.ExecContext(ctx, `UPDATE artist_match_candidates SET status='rejected' WHERE artist_id=? AND status='candidate'`, compositeArtistID)
			_, _ = s.db.ExecContext(ctx, `UPDATE artist_source_match_state SET outcome='rejected' WHERE artist_id=? AND source='_composite'`, compositeArtistID)
		}
	}
	return updated, nil
}
