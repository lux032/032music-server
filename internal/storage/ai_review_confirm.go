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

var (
	ErrInvalidCreditSplit    = errors.New("无效的复合署名拆分名单")
	ErrNoCompositeReferences = errors.New("该复合署名当前没有可更新的曲目或专辑关联")
)

type CreditSplitResult struct {
	TracksUpdated int
	AlbumsUpdated int
	RolesUpdated  int
	PartIDs       []int64
	PartNames     []string
}

type creditSplitArtistRow struct {
	ArtistID   int64  `json:"artistId"`
	Position   int    `json:"position"`
	JoinPhrase string `json:"joinPhrase,omitempty"`
}

type creditSplitPrimaryUndo struct {
	TrackID          int64   `json:"trackId"`
	ExpectedRevision int64   `json:"expectedRevision"`
	BeforeActive     bool    `json:"beforeActive"`
	BeforeIDs        []int64 `json:"beforeIds,omitempty"`
}

type creditSplitTrackRoleUndo struct {
	TrackID int64                  `json:"trackId"`
	Role    string                 `json:"role"`
	Rows    []creditSplitArtistRow `json:"rows"`
}

type creditSplitSourceUndo struct {
	TrackID    int64  `json:"trackId"`
	ArtistID   int64  `json:"artistId"`
	Position   int    `json:"position"`
	Role       string `json:"role"`
	JoinPhrase string `json:"joinPhrase,omitempty"`
	Source     string `json:"source"`
	ExternalID string `json:"externalId,omitempty"`
	RunID      int64  `json:"runId,omitempty"`
}

type creditSplitAlbumUndo struct {
	AlbumID      int64                  `json:"albumId"`
	ArtistCredit string                 `json:"artistCredit,omitempty"`
	Rows         []creditSplitArtistRow `json:"rows"`
}

type aiCreditSplitSnapshot struct {
	CompositeArtistID    int64                      `json:"compositeArtistId"`
	PartIDs              []int64                    `json:"partIds"`
	PartNames            []string                   `json:"partNames"`
	PrimaryTracks        []creditSplitPrimaryUndo   `json:"primaryTracks,omitempty"`
	NonPrimaryRoles      []creditSplitTrackRoleUndo `json:"nonPrimaryRoles,omitempty"`
	SourceRows           []creditSplitSourceUndo    `json:"sourceRows,omitempty"`
	Albums               []creditSplitAlbumUndo     `json:"albums,omitempty"`
	RejectedCandidateIDs []int64                    `json:"rejectedCandidateIds,omitempty"`
	HadCompositeSkip     bool                       `json:"hadCompositeSkip,omitempty"`
	CompositeSkipInput   string                     `json:"compositeSkipInput,omitempty"`
	CompositeSkipConfig  string                     `json:"compositeSkipConfig,omitempty"`
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
		if composite, cErr := isCompositeArtistTx(ctx, tx, artistID, name); cErr != nil {
			return cErr
		} else if composite {
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

	case AITargetCreditSplit:
		var snap aiCreditSplitSnapshot
		if err = json.Unmarshal(d.AppliedSnapshot, &snap); err != nil {
			return err
		}
		if err = revertCompositeCreditSplitTx(ctx, tx, d.TargetID, snap); err != nil {
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

// ApplyCompositeCreditSplit replaces compositeArtistID across all track roles
// (primary + backstage/involved-people), track_artist_sources, and album_artists,
// persisting an active split rule so subsequent library rescans preserve the split.
func (s *Store) ApplyCompositeCreditSplit(ctx context.Context, compositeArtistID int64, partNames []string) (CreditSplitResult, error) {
	res, _, err := s.applyCompositeCreditSplit(ctx, compositeArtistID, partNames, "manual", 1)
	return res, err
}

// ApplyCompositeCreditSplitByAI performs a composite credit split on behalf of
// AI review (requiring at least 2 distinct canonical artists) and returns the
// rollback snapshot for RevertAIReviewDecision.
func (s *Store) ApplyCompositeCreditSplitByAI(ctx context.Context, compositeArtistID int64, partNames []string) (CreditSplitResult, json.RawMessage, error) {
	return s.applyCompositeCreditSplit(ctx, compositeArtistID, partNames, "ai", 2)
}

type creditSplitSourceGroupKey struct {
	TrackID    int64
	Role       string
	Source     string
	ExternalID string
}

func (s *Store) applyCompositeCreditSplit(ctx context.Context, compositeArtistID int64, partNames []string, source string, minParts int) (CreditSplitResult, json.RawMessage, error) {
	var cleaned []string
	seenName := map[string]bool{}
	for _, name := range partNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := metadata.Normalize(name)
		if key == "" || seenName[key] {
			continue
		}
		seenName[key] = true
		cleaned = append(cleaned, name)
	}
	if len(cleaned) < minParts || len(cleaned) > 32 {
		if minParts > 1 {
			return CreditSplitResult{}, nil, fmt.Errorf("%w：拆分后的艺人数量必须为 %d 至 32 位", ErrInvalidCreditSplit, minParts)
		}
		return CreditSplitResult{}, nil, fmt.Errorf("%w：拆分后的艺人数量必须为 1 至 32 位", ErrInvalidCreditSplit)
	}

	var result CreditSplitResult
	var snapRaw json.RawMessage
	var noRefs bool

	err := withBusyRetry(ctx, func() error {
		result = CreditSplitResult{}
		snapRaw = nil
		noRefs = false

		tx, err := s.beginWrite(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var compositeName string
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=? AND merged_into_artist_id IS NULL`, compositeArtistID).Scan(&compositeName); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				noRefs = true
				return nil
			}
			return err
		}

		rawPartIDs, err := ensureArtists(ctx, tx, cleaned)
		if err != nil {
			return err
		}
		var partIDs []int64
		var canonicalNames []string
		seenPartID := map[int64]bool{}
		for i, rawID := range rawPartIDs {
			cid, cErr := canonicalArtistID(ctx, tx, rawID)
			if cErr != nil {
				return cErr
			}
			if cid == compositeArtistID {
				return fmt.Errorf("%w：拆分结果不能包含原复合署名自身", ErrInvalidCreditSplit)
			}
			if seenPartID[cid] {
				continue
			}
			seenPartID[cid] = true
			partIDs = append(partIDs, cid)
			var dispName string
			if qErr := tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=?`, cid).Scan(&dispName); qErr == nil && strings.TrimSpace(dispName) != "" {
				canonicalNames = append(canonicalNames, dispName)
			} else {
				canonicalNames = append(canonicalNames, cleaned[i])
			}
		}
		if len(partIDs) < minParts {
			return fmt.Errorf("%w：去重归一化后的独立艺人数量不足 %d 位", ErrInvalidCreditSplit, minParts)
		}

		snap := aiCreditSplitSnapshot{
			CompositeArtistID: compositeArtistID,
			PartIDs:           partIDs,
			PartNames:         canonicalNames,
		}
		updatedTracks := map[int64]bool{}
		updatedAlbums := map[int64]bool{}
		touchAlbumsForTracks := map[int64]bool{}

		// 1. Primary track credits (using PrimaryArtistCorrection state machine).
		primaryTrackIDs, err := queryTxIDs(ctx, tx, `SELECT DISTINCT track_id FROM track_artists WHERE artist_id=? AND role='primary' ORDER BY track_id`, compositeArtistID)
		if err != nil {
			return err
		}
		for _, trackID := range primaryTrackIDs {
			curSnap, err := correctionSnapshot(ctx, tx, trackID)
			if err != nil {
				return err
			}
			var beforeIDs []int64
			if curSnap.Active {
				var beforeRaw string
				if qErr := tx.QueryRowContext(ctx, `SELECT artists_json FROM track_primary_artist_state WHERE track_id=?`, trackID).Scan(&beforeRaw); qErr == nil {
					_ = json.Unmarshal([]byte(beforeRaw), &beforeIDs)
				}
			}
			var nextIDs []int64
			seenID := map[int64]bool{}
			replaced := false
			for _, cur := range curSnap.Artists {
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
				cid, cErr := canonicalArtistID(ctx, tx, cur.ID)
				if cErr != nil {
					return cErr
				}
				if !seenID[cid] {
					seenID[cid] = true
					nextIDs = append(nextIDs, cid)
				}
			}
			if !replaced || len(nextIDs) == 0 {
				continue
			}
			nextRev, err := setPrimaryArtistCorrectionTx(ctx, tx, trackID, curSnap, nextIDs, true)
			if err != nil {
				return err
			}
			snap.PrimaryTracks = append(snap.PrimaryTracks, creditSplitPrimaryUndo{
				TrackID:          trackID,
				ExpectedRevision: nextRev,
				BeforeActive:     curSnap.Active,
				BeforeIDs:        beforeIDs,
			})
			updatedTracks[trackID] = true
			result.RolesUpdated++
		}

		// 2. Structured track_artist_sources MUST be updated before non-primary
		// track_artists deletions so restore_sourced_track_artist_after_delete
		// never resurrects removed composite rows.
		srcGroupRows, err := tx.QueryContext(ctx, `SELECT DISTINCT track_id,role,source,external_id FROM track_artist_sources WHERE artist_id=? ORDER BY track_id,role,source,external_id`, compositeArtistID)
		if err != nil {
			return err
		}
		var srcGroups []creditSplitSourceGroupKey
		for srcGroupRows.Next() {
			var g creditSplitSourceGroupKey
			if err = srcGroupRows.Scan(&g.TrackID, &g.Role, &g.Source, &g.ExternalID); err != nil {
				srcGroupRows.Close()
				return err
			}
			srcGroups = append(srcGroups, g)
		}
		if err = srcGroupRows.Err(); err != nil {
			srcGroupRows.Close()
			return err
		}
		srcGroupRows.Close()

		for _, g := range srcGroups {
			rows, qErr := tx.QueryContext(ctx, `SELECT artist_id,position,join_phrase,COALESCE(run_id,0) FROM track_artist_sources WHERE track_id=? AND role=? AND source=? AND external_id=? ORDER BY position,artist_id`, g.TrackID, g.Role, g.Source, g.ExternalID)
			if qErr != nil {
				return qErr
			}
			var existing []creditSplitSourceUndo
			for rows.Next() {
				var r creditSplitSourceUndo
				r.TrackID, r.Role, r.Source, r.ExternalID = g.TrackID, g.Role, g.Source, g.ExternalID
				if qErr = rows.Scan(&r.ArtistID, &r.Position, &r.JoinPhrase, &r.RunID); qErr != nil {
					rows.Close()
					return qErr
				}
				existing = append(existing, r)
			}
			if qErr = rows.Err(); qErr != nil {
				rows.Close()
				return qErr
			}
			rows.Close()
			snap.SourceRows = append(snap.SourceRows, existing...)

			if _, err = tx.ExecContext(ctx, `DELETE FROM track_artist_sources WHERE track_id=? AND role=? AND source=? AND external_id=?`, g.TrackID, g.Role, g.Source, g.ExternalID); err != nil {
				return err
			}
			seenSrc := map[int64]bool{}
			pos := 0
			for _, r := range existing {
				if r.ArtistID == compositeArtistID {
					for _, pid := range partIDs {
						if seenSrc[pid] {
							continue
						}
						seenSrc[pid] = true
						if err = ensureTrackArtistSource(ctx, tx, g.TrackID, pid, pos, g.Role, "", g.Source, g.ExternalID, r.RunID); err != nil {
							return err
						}
						pos++
					}
					continue
				}
				cid, cErr := canonicalArtistID(ctx, tx, r.ArtistID)
				if cErr != nil {
					return cErr
				}
				if seenSrc[cid] {
					continue
				}
				seenSrc[cid] = true
				if err = ensureTrackArtistSource(ctx, tx, g.TrackID, cid, pos, g.Role, r.JoinPhrase, g.Source, g.ExternalID, r.RunID); err != nil {
					return err
				}
				pos++
			}
			updatedTracks[g.TrackID] = true
			touchAlbumsForTracks[g.TrackID] = true
		}

		// 3. Non-primary track_artists roles (composer, lyricist, arranger, producer, instrument:*, ...).
		type trackRoleKey struct {
			trackID int64
			role    string
		}
		npRows, err := tx.QueryContext(ctx, `SELECT DISTINCT track_id,role FROM track_artists WHERE artist_id=? AND role<>'primary' ORDER BY track_id,role`, compositeArtistID)
		if err != nil {
			return err
		}
		var npKeys []trackRoleKey
		for npRows.Next() {
			var k trackRoleKey
			if err = npRows.Scan(&k.trackID, &k.role); err != nil {
				npRows.Close()
				return err
			}
			npKeys = append(npKeys, k)
		}
		if err = npRows.Err(); err != nil {
			npRows.Close()
			return err
		}
		npRows.Close()

		for _, k := range npKeys {
			rows, qErr := tx.QueryContext(ctx, `SELECT artist_id,position,join_phrase FROM track_artists WHERE track_id=? AND role=? ORDER BY position,artist_id`, k.trackID, k.role)
			if qErr != nil {
				return qErr
			}
			var existing []creditSplitArtistRow
			for rows.Next() {
				var r creditSplitArtistRow
				if qErr = rows.Scan(&r.ArtistID, &r.Position, &r.JoinPhrase); qErr != nil {
					rows.Close()
					return qErr
				}
				existing = append(existing, r)
			}
			if qErr = rows.Err(); qErr != nil {
				rows.Close()
				return qErr
			}
			rows.Close()
			snap.NonPrimaryRoles = append(snap.NonPrimaryRoles, creditSplitTrackRoleUndo{
				TrackID: k.trackID,
				Role:    k.role,
				Rows:    existing,
			})

			if _, err = tx.ExecContext(ctx, `DELETE FROM track_artists WHERE track_id=? AND role=?`, k.trackID, k.role); err != nil {
				return err
			}
			seenRole := map[int64]bool{}
			pos := 0
			for _, r := range existing {
				if r.ArtistID == compositeArtistID {
					for _, pid := range partIDs {
						if seenRole[pid] {
							continue
						}
						seenRole[pid] = true
						if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_artists(track_id,artist_id,position,join_phrase,role) VALUES(?,?,?,?,?)`, k.trackID, pid, pos, "", k.role); err != nil {
							return err
						}
						pos++
					}
					continue
				}
				cid, cErr := canonicalArtistID(ctx, tx, r.ArtistID)
				if cErr != nil {
					return cErr
				}
				if seenRole[cid] {
					continue
				}
				seenRole[cid] = true
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_artists(track_id,artist_id,position,join_phrase,role) VALUES(?,?,?,?,?)`, k.trackID, cid, pos, r.JoinPhrase, k.role); err != nil {
					return err
				}
				pos++
			}
			updatedTracks[k.trackID] = true
			touchAlbumsForTracks[k.trackID] = true
			result.RolesUpdated++
		}

		// 4. Album artists.
		albumIDs, err := queryTxIDs(ctx, tx, `SELECT DISTINCT album_id FROM album_artists WHERE artist_id=? ORDER BY album_id`, compositeArtistID)
		if err != nil {
			return err
		}
		for _, albumID := range albumIDs {
			var prevCredit string
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(artist_credit,'') FROM albums WHERE id=?`, albumID).Scan(&prevCredit)
			rows, qErr := tx.QueryContext(ctx, `SELECT artist_id,position,join_phrase FROM album_artists WHERE album_id=? ORDER BY position,artist_id`, albumID)
			if qErr != nil {
				return qErr
			}
			var existing []creditSplitArtistRow
			for rows.Next() {
				var r creditSplitArtistRow
				if qErr = rows.Scan(&r.ArtistID, &r.Position, &r.JoinPhrase); qErr != nil {
					rows.Close()
					return qErr
				}
				existing = append(existing, r)
			}
			if qErr = rows.Err(); qErr != nil {
				rows.Close()
				return qErr
			}
			rows.Close()
			snap.Albums = append(snap.Albums, creditSplitAlbumUndo{
				AlbumID:      albumID,
				ArtistCredit: prevCredit,
				Rows:         existing,
			})

			if _, err = tx.ExecContext(ctx, `DELETE FROM album_artists WHERE album_id=?`, albumID); err != nil {
				return err
			}
			seenAlbum := map[int64]bool{}
			pos := 0
			for _, r := range existing {
				if r.ArtistID == compositeArtistID {
					for _, pid := range partIDs {
						if seenAlbum[pid] {
							continue
						}
						seenAlbum[pid] = true
						if _, err = tx.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position,join_phrase) VALUES(?,?,?,?)`, albumID, pid, pos, ""); err != nil {
							return err
						}
						pos++
					}
					continue
				}
				cid, cErr := canonicalArtistID(ctx, tx, r.ArtistID)
				if cErr != nil {
					return cErr
				}
				if seenAlbum[cid] {
					continue
				}
				seenAlbum[cid] = true
				if _, err = tx.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position,join_phrase) VALUES(?,?,?,?)`, albumID, cid, pos, r.JoinPhrase); err != nil {
					return err
				}
				pos++
			}
			if _, err = tx.ExecContext(ctx, `UPDATE albums SET artist_credit=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, albumID); err != nil {
				return err
			}
			updatedAlbums[albumID] = true
			result.RolesUpdated++
		}

		// Touch timestamps for non-primary track updates so clients refresh.
		for trackID := range touchAlbumsForTracks {
			if _, err = tx.ExecContext(ctx, `UPDATE tracks SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, trackID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE albums SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=(SELECT album_id FROM tracks WHERE id=?)`, trackID); err != nil {
				return err
			}
		}

		if len(updatedTracks) == 0 && len(updatedAlbums) == 0 {
			// Clear stale review queue state when composite artist already has zero refs.
			_, _ = tx.ExecContext(ctx, `UPDATE artist_match_candidates SET status='rejected' WHERE artist_id=? AND status='candidate'`, compositeArtistID)
			_, _ = tx.ExecContext(ctx, `UPDATE artist_source_match_state SET outcome='rejected' WHERE artist_id=? AND source='_composite'`, compositeArtistID)
			if err = tx.Commit(); err != nil {
				return err
			}
			noRefs = true
			return nil
		}

		// 5. Clear pending candidates & composite skip state once no refs remain.
		var remaining int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM track_artists WHERE artist_id=?)+(SELECT count(*) FROM album_artists WHERE artist_id=?)`, compositeArtistID, compositeArtistID).Scan(&remaining); err != nil {
			return err
		}
		if remaining == 0 {
			rejectedIDs, err := candidateSiblingIDsTx(ctx, tx, `SELECT id FROM artist_match_candidates WHERE artist_id=? AND status='candidate'`, compositeArtistID)
			if err != nil {
				return err
			}
			snap.RejectedCandidateIDs = rejectedIDs
			var compOutcome, compInput, compConfig string
			if qErr := tx.QueryRowContext(ctx, `SELECT outcome,input_key,config_key FROM artist_source_match_state WHERE artist_id=? AND source='_composite'`, compositeArtistID).Scan(&compOutcome, &compInput, &compConfig); qErr == nil && compOutcome == "skipped_composite" {
				snap.HadCompositeSkip = true
				snap.CompositeSkipInput = compInput
				snap.CompositeSkipConfig = compConfig
			}
			if _, err = tx.ExecContext(ctx, `UPDATE artist_match_candidates SET status='rejected' WHERE artist_id=? AND status='candidate'`, compositeArtistID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE artist_source_match_state SET outcome='rejected' WHERE artist_id=? AND source='_composite'`, compositeArtistID); err != nil {
				return err
			}
		}

		partIDsJSON, _ := json.Marshal(partIDs)
		partNamesJSON, _ := json.Marshal(canonicalNames)
		encodedSnap, _ := json.Marshal(snap)

		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_credit_splits(composite_artist_id,part_ids_json,part_names_json,status,source,snapshot_json,updated_at)
			VALUES(?,?,?,'active',?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			ON CONFLICT(composite_artist_id) DO UPDATE SET
				part_ids_json=excluded.part_ids_json,
				part_names_json=excluded.part_names_json,
				status='active',
				source=excluded.source,
				snapshot_json=excluded.snapshot_json,
				updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			compositeArtistID, string(partIDsJSON), string(partNamesJSON), source, string(encodedSnap)); err != nil {
			return err
		}

		if err = tx.Commit(); err != nil {
			return err
		}
		result.TracksUpdated = len(updatedTracks)
		result.AlbumsUpdated = len(updatedAlbums)
		result.PartIDs = partIDs
		result.PartNames = canonicalNames
		snapRaw = encodedSnap
		return nil
	})
	if err != nil {
		return CreditSplitResult{}, nil, err
	}
	if noRefs {
		return CreditSplitResult{}, nil, ErrNoCompositeReferences
	}
	return result, snapRaw, nil
}

// LinkCreditSplitDecision associates a persisted AI decision ID with the active
// artist_credit_splits rule so audit/revert metadata stays linked.
func (s *Store) LinkCreditSplitDecision(ctx context.Context, compositeArtistID, decisionID int64) error {
	if compositeArtistID <= 0 || decisionID <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artist_credit_splits SET decision_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE composite_artist_id=? AND status='active'`, decisionID, compositeArtistID)
	return err
}

func revertCompositeCreditSplitTx(ctx context.Context, tx *sql.Tx, compositeArtistID int64, snap aiCreditSplitSnapshot) error {
	if compositeArtistID <= 0 {
		compositeArtistID = snap.CompositeArtistID
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE id=?`, compositeArtistID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return errors.New("原复合署名艺人记录已不存在，无法撤销")
	}

	if _, err := tx.ExecContext(ctx, `UPDATE artist_credit_splits SET status='reverted',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE composite_artist_id=?`, compositeArtistID); err != nil {
		return err
	}

	// 1. Restore primary track corrections only when no newer manual edit intervened.
	for _, pt := range snap.PrimaryTracks {
		var trackExists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id=?`, pt.TrackID).Scan(&trackExists); err != nil {
			return err
		}
		if trackExists == 0 {
			continue
		}
		curSnap, err := correctionSnapshot(ctx, tx, pt.TrackID)
		if err != nil {
			return err
		}
		if curSnap.Revision != pt.ExpectedRevision {
			// Skip tracks that were manually edited after the AI split.
			continue
		}
		if _, err = setPrimaryArtistCorrectionTx(ctx, tx, pt.TrackID, curSnap, pt.BeforeIDs, pt.BeforeActive); err != nil {
			return err
		}
	}

	// 2. Restore track_artist_sources BEFORE touching non-primary track_artists so
	// restore_sourced_track_artist_after_delete never reinserts split parts.
	restoredSrcGroups := map[creditSplitSourceGroupKey]bool{}
	for _, r := range snap.SourceRows {
		g := creditSplitSourceGroupKey{TrackID: r.TrackID, Role: r.Role, Source: r.Source, ExternalID: r.ExternalID}
		if !restoredSrcGroups[g] {
			restoredSrcGroups[g] = true
			if _, err := tx.ExecContext(ctx, `DELETE FROM track_artist_sources WHERE track_id=? AND role=? AND source=? AND external_id=?`, g.TrackID, g.Role, g.Source, g.ExternalID); err != nil {
				return err
			}
		}
	}
	for _, r := range snap.SourceRows {
		var trackExists, artistExists int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id=?`, r.TrackID).Scan(&trackExists)
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE id=?`, r.ArtistID).Scan(&artistExists)
		if trackExists == 0 || artistExists == 0 {
			continue
		}
		runID := r.RunID
		if runID > 0 {
			var runExists int
			_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrichment_runs WHERE id=?`, runID).Scan(&runExists)
			if runExists == 0 {
				runID = 0
			}
		}
		if err := ensureTrackArtistSource(ctx, tx, r.TrackID, r.ArtistID, r.Position, r.Role, r.JoinPhrase, r.Source, r.ExternalID, runID); err != nil {
			return err
		}
	}

	// 3. Restore non-primary track_artists roles.
	touchedTracks := map[int64]bool{}
	for _, nr := range snap.NonPrimaryRoles {
		var trackExists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id=?`, nr.TrackID).Scan(&trackExists); err != nil {
			return err
		}
		if trackExists == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM track_artists WHERE track_id=? AND role=?`, nr.TrackID, nr.Role); err != nil {
			return err
		}
		for _, r := range nr.Rows {
			var artistExists int
			_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE id=?`, r.ArtistID).Scan(&artistExists)
			if artistExists == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_artists(track_id,artist_id,position,join_phrase,role) VALUES(?,?,?,?,?)`, nr.TrackID, r.ArtistID, r.Position, r.JoinPhrase, nr.Role); err != nil {
				return err
			}
		}
		touchedTracks[nr.TrackID] = true
	}
	for trackID := range touchedTracks {
		if _, err := tx.ExecContext(ctx, `UPDATE tracks SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, trackID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=(SELECT album_id FROM tracks WHERE id=?)`, trackID); err != nil {
			return err
		}
	}

	// 4. Restore album_artists.
	for _, al := range snap.Albums {
		var albumExists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM albums WHERE id=?`, al.AlbumID).Scan(&albumExists); err != nil {
			return err
		}
		if albumExists == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM album_artists WHERE album_id=?`, al.AlbumID); err != nil {
			return err
		}
		for _, r := range al.Rows {
			var artistExists int
			_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE id=?`, r.ArtistID).Scan(&artistExists)
			if artistExists == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_artists(album_id,artist_id,position,join_phrase) VALUES(?,?,?,?)`, al.AlbumID, r.ArtistID, r.Position, r.JoinPhrase); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET artist_credit=NULLIF(?,''),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, al.ArtistCredit, al.AlbumID); err != nil {
			return err
		}
	}

	// 5. Restore candidates and composite skip state so the artist returns to Tab 2.
	if err := restoreCandidateStatusTx(ctx, tx, "artist_match_candidates", snap.RejectedCandidateIDs); err != nil {
		return err
	}
	if snap.HadCompositeSkip || len(snap.RejectedCandidateIDs) == 0 {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO artist_source_match_state(artist_id,source,input_key,config_key,outcome,checked_at,next_check_at,snapshot_json)
			VALUES(?,'_composite',?,?,'skipped_composite',?,?,'{}')
			ON CONFLICT(artist_id,source) DO UPDATE SET outcome='skipped_composite',checked_at=excluded.checked_at,next_check_at=excluded.next_check_at`,
			compositeArtistID, snap.CompositeSkipInput, snap.CompositeSkipConfig, now, now); err != nil {
			return err
		}
	}
	return nil
}

// expandActiveCreditSplitsTx expands any artist IDs that have an active rule in
// artist_credit_splits into their canonical target IDs, preserving order and
// deduplicating without cycles.
func expandActiveCreditSplitsTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]int64, bool, error) {
	if len(ids) == 0 {
		return ids, false, nil
	}
	placeholders, args := inClause(ids)
	rows, err := tx.QueryContext(ctx, `SELECT composite_artist_id,part_ids_json FROM artist_credit_splits WHERE status='active' AND composite_artist_id IN (`+placeholders+`)`, args...)
	if err != nil {
		if strings.Contains(err.Error(), "no such table: artist_credit_splits") {
			return ids, false, nil
		}
		return nil, false, err
	}
	rules := map[int64][]int64{}
	for rows.Next() {
		var compID int64
		var raw string
		if err = rows.Scan(&compID, &raw); err != nil {
			rows.Close()
			return nil, false, err
		}
		var parts []int64
		if json.Unmarshal([]byte(raw), &parts) == nil && len(parts) > 0 {
			rules[compID] = parts
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	if len(rules) == 0 {
		return ids, false, nil
	}
	out := make([]int64, 0, len(ids)+len(rules))
	seen := map[int64]bool{}
	for _, id := range ids {
		if parts, ok := rules[id]; ok {
			for _, pid := range parts {
				cid, cErr := canonicalArtistID(ctx, tx, pid)
				if cErr != nil {
					continue
				}
				if cid == id || seen[cid] {
					continue
				}
				seen[cid] = true
				out = append(out, cid)
			}
			continue
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return ids, false, nil
	}
	return out, true, nil
}
