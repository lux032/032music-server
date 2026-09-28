package storage

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TrackBangumiTarget is the snapshot used to search and confirm one track.
type TrackBangumiTarget struct {
	ID                                   int64
	AlbumID                              int64
	Title, AlbumTitle, TrackType, Artist string
	Fingerprint                          string
	ReleaseDate                          string
	Artists                              []string
	Recheck                              bool
	Unlinked                             bool
	DiscNumber, TrackNumber              int
}

// TrackSubjectCandidate is one Bangumi music entry proposed for a track.
type TrackSubjectCandidate struct {
	ID         int64           `json:"id"`
	TrackID    int64           `json:"trackId"`
	AlbumID    int64           `json:"albumId"`
	TrackTitle string          `json:"trackTitle"`
	AlbumTitle string          `json:"albumTitle"`
	ExternalID string          `json:"externalId"`
	Title      string          `json:"title"`
	Artist     string          `json:"artist"`
	MatchKind  string          `json:"matchKind"`
	Evidence   []string        `json:"evidence"`
	Tieups     []BangumiTieup  `json:"tieups"`
	Payload    json.RawMessage `json:"payload"`
	Status     string          `json:"status"`
}

const bangumiTrackArtistSQL = `COALESCE((SELECT GROUP_CONCAT(COALESCE(ar.user_display_name,ar.display_name),', ' ORDER BY ta.position,ar.id) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.track_id=t.id AND ta.role='primary'),'')`

func trackBangumiFingerprint(title, trackType string, artists []string) string {
	names := append([]string(nil), artists...)
	sort.Strings(names)
	return fmt.Sprintf("%x", sha1.Sum([]byte("track-bangumi-v1|"+title+"|"+trackType+"|"+strings.Join(names, "|"))))
}

// D12: ordinary misses wait at least 90 days (or the configured cache, whichever
// is longer). An album released within 180 days is retried after 7 days.
func trackBangumiRecheckDays(cacheDays int, releaseDate string) int {
	days := cacheDays
	if days < 90 {
		days = 90
	}
	if parsed, err := time.Parse("2006-01-02", releaseDate); err == nil && time.Since(parsed) <= 180*24*time.Hour {
		return 7
	}
	return days
}

// TracksForBangumiTieup lists tracks eligible for a per-track Bangumi lookup.
// It is deliberately wider than the album lookup: compilations and albums the
// user suppressed at album level are still searched song by song (H2).
// Only albums whose album-level association is already a confirmed ost
// (manual or bangumi) are skipped (D5, D13).
func (s *Store) TracksForBangumiTieup(ctx context.Context, force bool, albumID int64) ([]TrackBangumiTarget, error) {
	cacheDays := 30
	if setting, e := s.MetadataSourceSetting(ctx, "bangumi"); e == nil && setting.CacheDays > 0 {
		cacheDays = setting.CacheDays
	}
	query := `SELECT t.id,t.album_id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),COALESCE(t.user_track_type,t.track_type,'regular'),` + bangumiTrackArtistSQL + `,` + bangumiAlbumDateSQL + `,COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),NOT EXISTS(SELECT 1 FROM album_works aw WHERE aw.album_id=a.id) AND NOT EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.track_id=t.id),COALESCE((SELECT m.fingerprint||'|'||m.checked_at FROM track_enrichment_misses m WHERE m.track_id=t.id AND m.source='bangumi'),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT an.value) FROM artist_names an JOIN track_artists ta ON ta.artist_id=an.artist_id AND ta.role='primary' WHERE ta.track_id=t.id),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT COALESCE(c.user_display_name,c.display_name)) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id JOIN artists c ON c.id=ar.merged_into_artist_id WHERE ta.track_id=t.id AND ta.role='primary'),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT an.value) FROM artist_names an JOIN track_artists ta ON ta.artist_id=an.artist_id AND ta.role='primary' JOIN artists ar ON ar.id=ta.artist_id JOIN artists c ON c.id=ar.merged_into_artist_id WHERE ta.track_id=t.id AND an.artist_id=c.id),'') FROM tracks t JOIN albums a ON a.id=t.album_id WHERE COALESCE(t.user_track_type,t.track_type,'regular')<>'drama_track' AND NOT EXISTS(SELECT 1 FROM album_works aw WHERE aw.album_id=a.id AND aw.role='ost' AND aw.source IN ('manual','bangumi')) AND NOT EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.track_id=t.id AND wt.source='bangumi') AND NOT EXISTS(SELECT 1 FROM track_subject_candidates c WHERE c.track_id=t.id AND c.status IN ('confirmed','rejected'))`
	args := []any{}
	if !force {
		query += ` AND NOT EXISTS(SELECT 1 FROM track_subject_candidates c WHERE c.track_id=t.id AND c.status='candidate')`
	}
	if albumID > 0 {
		query += ` AND t.album_id=?`
		args = append(args, albumID)
	}
	query += ` ORDER BY CASE WHEN NOT EXISTS(SELECT 1 FROM album_works aw WHERE aw.album_id=a.id) AND NOT EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.track_id=t.id) THEN 0 ELSE 1 END,a.id,COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),t.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TrackBangumiTarget
	for rows.Next() {
		var v TrackBangumiTarget
		var miss, aliases, merged, mergedAliases string
		var unlinked int
		if err = rows.Scan(&v.ID, &v.AlbumID, &v.Title, &v.AlbumTitle, &v.TrackType, &v.Artist, &v.ReleaseDate, &v.DiscNumber, &v.TrackNumber, &unlinked, &miss, &aliases, &merged, &mergedAliases); err != nil {
			return nil, err
		}
		v.Unlinked = unlinked == 1
		v.Artists = append(append(append(splitAlbumBangumiNames(v.Artist), splitAlbumBangumiNames(aliases)...), splitAlbumBangumiNames(merged)...), splitAlbumBangumiNames(mergedAliases)...)
		if BangumiTrackTitleKey(v.Title, false) == "" {
			continue
		}
		v.Fingerprint = trackBangumiFingerprint(v.Title, v.TrackType, v.Artists)
		if miss != "" {
			fingerprint, checked, _ := strings.Cut(miss, "|")
			if fingerprint == v.Fingerprint {
				checkedAt, e := time.Parse(time.RFC3339Nano, checked)
				if e == nil && time.Since(checkedAt) < time.Duration(trackBangumiRecheckDays(cacheDays, v.ReleaseDate))*24*time.Hour && !force {
					continue
				}
				v.Recheck = true
			}
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// TrackBangumiLink summarizes the Bangumi association written for one track.
func (s *Store) TrackBangumiLink(ctx context.Context, trackID int64) (role, externalID, key string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT wt.role,wep.external_id,COALESCE(wt.inferred_key,'') FROM work_tracks wt JOIN work_external_profiles wep ON wep.work_id=wt.work_id AND wep.source='bangumi' WHERE wt.track_id=? AND wt.source='bangumi'`, trackID).Scan(&role, &externalID, &key)
	return role, externalID, key, err
}

func (s *Store) BangumiTrackTieupSuppressed(ctx context.Context, trackID, albumID int64, music string, tie BangumiTieup) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	return bangumiTrackLinkSuppressed(ctx, tx, trackID, albumID, music, tie, 0)
}

func (s *Store) SetTrackBangumiMiss(ctx context.Context, t TrackBangumiTarget, reason string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO track_enrichment_misses(track_id,source,fingerprint,reason) VALUES(?,'bangumi',?,?) ON CONFLICT(track_id,source) DO UPDATE SET fingerprint=excluded.fingerprint,reason=excluded.reason,checked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, t.ID, t.Fingerprint, reason)
	return err
}

func (s *Store) SetSuppressedTrackBangumiMiss(ctx context.Context, t TrackBangumiTarget) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_subject_candidates WHERE track_id=? AND status='candidate'`, t.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO track_enrichment_misses(track_id,source,fingerprint,reason) VALUES(?,'bangumi',?,'all matching entries suppressed') ON CONFLICT(track_id,source) DO UPDATE SET fingerprint=excluded.fingerprint,reason=excluded.reason,checked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, t.ID, t.Fingerprint); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveTrackSubjectCandidates(ctx context.Context, trackID int64, candidates []TrackSubjectCandidate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_subject_candidates WHERE track_id=? AND status='candidate'`, trackID); err != nil {
		return err
	}
	for _, v := range candidates {
		evidence := v.Evidence
		if evidence == nil {
			evidence = []string{}
		}
		ev, _ := json.Marshal(evidence)
		tieups := v.Tieups
		if tieups == nil {
			tieups = []BangumiTieup{}
		}
		tie, _ := json.Marshal(tieups)
		_, err = tx.ExecContext(ctx, `INSERT INTO track_subject_candidates(track_id,source,external_id,title,artist,match_kind,evidence_json,tieups_json,payload_json) VALUES(?,'bangumi',?,?,?,?,?,?,?) ON CONFLICT(track_id,source,external_id) DO NOTHING`, trackID, v.ExternalID, v.Title, v.Artist, v.MatchKind, string(ev), string(tie), rawOrEmpty(v.Payload))
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_enrichment_misses WHERE track_id=? AND source='bangumi'`, trackID); err != nil {
		return err
	}
	return tx.Commit()
}

const trackCandidateSelect = `SELECT c.id,c.track_id,t.album_id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),c.external_id,c.title,COALESCE(c.artist,''),c.match_kind,c.evidence_json,c.tieups_json,c.payload_json,c.status FROM track_subject_candidates c JOIN tracks t ON t.id=c.track_id JOIN albums a ON a.id=t.album_id`

func scanTrackCandidates(rows *sql.Rows) ([]TrackSubjectCandidate, error) {
	out := []TrackSubjectCandidate{}
	for rows.Next() {
		var v TrackSubjectCandidate
		var ev, tie, raw string
		if err := rows.Scan(&v.ID, &v.TrackID, &v.AlbumID, &v.TrackTitle, &v.AlbumTitle, &v.ExternalID, &v.Title, &v.Artist, &v.MatchKind, &ev, &tie, &raw, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ev), &v.Evidence)
		_ = json.Unmarshal([]byte(tie), &v.Tieups)
		v.Payload = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) TrackSubjectCandidates(ctx context.Context, trackID int64) ([]TrackSubjectCandidate, error) {
	if trackID > 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id=?`, trackID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			return nil, sql.ErrNoRows
		}
	}
	rows, err := s.db.QueryContext(ctx, trackCandidateSelect+` WHERE (?=0 OR c.track_id=?) ORDER BY c.id`, trackID, trackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrackCandidates(rows)
}

func (s *Store) PendingTrackSubjectCandidates(ctx context.Context, limit int) ([]TrackSubjectCandidate, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, trackCandidateSelect+` WHERE c.status='candidate' ORDER BY c.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrackCandidates(rows)
}

// RejectTrackSubjectCandidate remembers the whole music entry for this track.
func (s *Store) RejectTrackSubjectCandidate(ctx context.Context, trackID, candidateID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var external, status string
	if err = tx.QueryRowContext(ctx, `SELECT external_id,status FROM track_subject_candidates WHERE track_id=? AND id=?`, trackID, candidateID).Scan(&external, &status); err != nil {
		return err
	}
	if status != "candidate" {
		return ErrCandidateNotPending
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_work_suppressions(track_id,inferred_key) VALUES(?,?)`, trackID, "bangumi:"+external); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE track_subject_candidates SET status='rejected',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, candidateID); err != nil {
		return err
	}
	return tx.Commit()
}

var validBangumiTrackRoles = map[string]bool{"op": true, "ed": true, "insert": true, "theme": true, "character": true, "ost": true, "image_song": true, "other": true}

// clearManualSuppressions drops title identities a manual acceptance may
// reverse and bangumi:*:<work>. Bound Bangumi anime/movie works use their
// shared media class; unbound works remain strict. Legacy title keys retain
// their historical title-only behavior. Whole-entry bangumi:<music> remains.
func clearManualSuppressions(ctx context.Context, tx *sql.Tx, table, idColumn string, ownerID, workID int64, workSubject string) error {
	query := `SELECT inferred_key FROM ` + table + ` WHERE ` + idColumn + `=?`
	keys, err := suppressionKeys(ctx, tx, query, ownerID)
	if err != nil {
		return err
	}
	identities, err := workSuppressionIdentities(ctx, tx, workID)
	if err != nil {
		return err
	}
	boundExternal, err := workBangumiExternalID(ctx, tx, workID)
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, stored := range keys {
		_, storedType, _ := splitInferredKey(stored)
		for _, identity := range identities {
			if !workTitleKeysMatch(stored, workTitleSuppressionKey(identity.Title, identity.Type)) {
				continue
			}
			if storedType == "" || storedType == identity.Type || storedType == "other" || boundExternal != "" && bangumiTypeCompatible(storedType, identity.Type) {
				drop[stored] = true
			}
		}
		parts := strings.Split(stored, ":")
		if len(parts) == 3 && parts[0] == "bangumi" && parts[2] == workSubject {
			drop[stored] = true
		}
	}
	for stored := range drop {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+idColumn+`=? AND inferred_key=?`, ownerID, stored); err != nil {
			return err
		}
	}
	return nil
}

func clearManualTrackSuppressions(ctx context.Context, tx *sql.Tx, trackID, workID int64, workSubject string) error {
	return clearManualSuppressions(ctx, tx, "track_work_suppressions", "track_id", trackID, workID, workSubject)
}

// ConfirmTrackSubjectCandidate lands the selected works for one track candidate.
// roles overrides the Bangumi role per work subject id; an overridden role is
// stored as a manual association (D10). The first statement takes the write lock
// and the track fingerprint is rechecked before anything automatic is written.
func (s *Store) ConfirmTrackSubjectCandidate(ctx context.Context, trackID, candidateID, runID int64, fingerprint string, automatic bool, selected []int64, roles map[int64]string) (string, []int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	first, err := tx.ExecContext(ctx, `UPDATE tracks SET id=id WHERE id=?`, trackID)
	if err != nil {
		return "", nil, err
	}
	if n, _ := first.RowsAffected(); n == 0 {
		return "skipped", nil, nil
	}
	var title, trackType, artist, date string
	var albumID int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(t.user_title,t.title),COALESCE(t.user_track_type,t.track_type,'regular'),`+bangumiTrackArtistSQL+`,`+bangumiAlbumDateSQL+`,t.album_id FROM tracks t JOIN albums a ON a.id=t.album_id WHERE t.id=?`, trackID).Scan(&title, &trackType, &artist, &date, &albumID); err != nil {
		return "", nil, err
	}
	var aliases, merged, mergedAliases string
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT GROUP_CONCAT(DISTINCT an.value) FROM artist_names an JOIN track_artists ta ON ta.artist_id=an.artist_id AND ta.role='primary' WHERE ta.track_id=?),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT COALESCE(c.user_display_name,c.display_name)) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id JOIN artists c ON c.id=ar.merged_into_artist_id WHERE ta.track_id=? AND ta.role='primary'),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT an.value) FROM artist_names an JOIN track_artists ta ON ta.artist_id=an.artist_id AND ta.role='primary' JOIN artists ar ON ar.id=ta.artist_id JOIN artists c ON c.id=ar.merged_into_artist_id WHERE ta.track_id=? AND an.artist_id=c.id),'')`, trackID, trackID, trackID).Scan(&aliases, &merged, &mergedAliases)
	names := append(append(append(splitAlbumBangumiNames(artist), splitAlbumBangumiNames(aliases)...), splitAlbumBangumiNames(merged)...), splitAlbumBangumiNames(mergedAliases)...)
	if automatic && fingerprint != "" && fingerprint != trackBangumiFingerprint(title, trackType, names) {
		return "review", nil, nil
	}
	var music, tieJSON, status, matchKind string
	if err = tx.QueryRowContext(ctx, `SELECT external_id,tieups_json,status,match_kind FROM track_subject_candidates WHERE track_id=? AND id=?`, trackID, candidateID).Scan(&music, &tieJSON, &status, &matchKind); err != nil {
		return "", nil, err
	}
	if status != "candidate" || automatic && matchKind == "multi_title" {
		return "review", nil, nil
	}
	var suppressed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM track_work_suppressions WHERE track_id=? AND inferred_key=?)`, trackID, "bangumi:"+music).Scan(&suppressed); err != nil {
		return "", nil, err
	}
	if suppressed {
		return "review", nil, nil
	}
	if automatic {
		var anyBangumi bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM track_work_suppressions WHERE track_id=? AND inferred_key LIKE 'bangumi:%')`, trackID).Scan(&anyBangumi); err != nil {
			return "", nil, err
		}
		if anyBangumi {
			return "review", nil, nil
		}
		var manual bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_tracks WHERE track_id=? AND source='manual')`, trackID).Scan(&manual); err != nil {
			return "", nil, err
		}
		if manual {
			return "review", nil, nil
		}
	}
	// H4: an album-level refusal of this same music entry blocks the track.
	var albumRefused bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=? AND inferred_key=?) OR EXISTS(SELECT 1 FROM album_subject_candidates WHERE album_id=? AND external_id=? AND status='rejected')`, albumID, "bangumi:"+music, albumID, music).Scan(&albumRefused); err != nil {
		return "", nil, err
	}
	if albumRefused {
		return "review", nil, nil
	}
	var ties []BangumiTieup
	if err = json.Unmarshal([]byte(tieJSON), &ties); err != nil {
		return "", nil, err
	}
	chosen := map[int64]bool{}
	for _, v := range selected {
		chosen[v] = true
	}
	// An empty manual selection is a caller error, not "accept every work".
	if !automatic && len(selected) == 0 {
		return "", nil, fmt.Errorf("%w: select at least one work", ErrInvalidWork)
	}
	// Every selected work subject must belong to this candidate. A foreign id
	// is a caller error (HTTP 400), not something to silently skip.
	tieIDs := make(map[int64]bool, len(ties))
	for _, tie := range ties {
		tieIDs[tie.SubjectID] = true
	}
	for _, id := range selected {
		if !tieIDs[id] {
			return "", nil, fmt.Errorf("%w: work is not part of this candidate", ErrInvalidWork)
		}
	}
	var workIDs []int64
	for _, tie := range ties {
		if automatic && (len(selected) != 1 || tie.SubjectID != selected[0]) || !automatic && !chosen[tie.SubjectID] {
			continue
		}
		suppressed, e := bangumiTrackLinkSuppressed(ctx, tx, trackID, albumID, music, tie, 0)
		if e != nil {
			return "", nil, e
		}
		// Automatic confirmation never revives a suppressed link. A manual
		// acceptance reverses the matched title identities and bangumi:*:<work>
		// keys after the work resolves (M-5, R1). Only the whole-entry
		// bangumi:<music> rejection checked above still blocks a manual accept.
		if suppressed && automatic {
			return "review", nil, nil
		}
		if tie.Type != "anime" && tie.Type != "movie" && tie.Type != "game" {
			return "", nil, ErrInvalidWork
		}
		id, e := resolveBangumiWork(ctx, tx, tie, runID)
		if errors.Is(e, ErrBangumiWorkIdentityConflict) {
			_ = tx.Rollback()
			_, updateErr := s.db.ExecContext(ctx, `UPDATE track_subject_candidates SET evidence_json=CASE WHEN json_valid(evidence_json) AND json_type(evidence_json)='array' THEN CASE WHEN EXISTS(SELECT 1 FROM json_each(evidence_json) WHERE value=?) THEN evidence_json ELSE json_insert(evidence_json,'$[#]',?) END ELSE json_array(?) END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='candidate'`, "本地作品身份冲突，需人工审核", "本地作品身份冲突，需人工审核", "本地作品身份冲突，需人工审核", candidateID)
			if updateErr != nil {
				return "", nil, updateErr
			}
			return "review", nil, nil
		}
		if e != nil {
			return "", nil, e
		}
		suppressed, e = bangumiTrackLinkSuppressed(ctx, tx, trackID, albumID, music, tie, id)
		if e != nil {
			return "", nil, e
		}
		if suppressed && automatic {
			return "review", nil, nil
		}
		if suppressed && !automatic {
			if e = clearManualTrackSuppressions(ctx, tx, trackID, id, strconv.FormatInt(tie.SubjectID, 10)); e != nil {
				return "", nil, e
			}
		}
		role := tie.Role
		source := "bangumi"
		if override := strings.ToLower(strings.TrimSpace(roles[tie.SubjectID])); override != "" && override != tie.Role {
			if !validBangumiTrackRoles[override] {
				return "", nil, ErrInvalidWork
			}
			role = override
			source = "manual"
		}
		key := bangumiWorkKey(music, strconv.FormatInt(tie.SubjectID, 10))
		if source == "manual" {
			if _, e = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE work_id=? AND track_id=? AND source='auto'`, id, trackID); e != nil {
				return "", nil, e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,?,'manual',?) ON CONFLICT(work_id,track_id,role,season,sequence) DO UPDATE SET source='manual',inferred_key=excluded.inferred_key`, id, trackID, role, key); e != nil {
				return "", nil, e
			}
		} else if _, e = landBangumiTrack(ctx, tx, trackID, id, role, key); e != nil {
			return "", nil, e
		}
		workIDs = append(workIDs, id)
	}
	if len(workIDs) == 0 {
		if !automatic {
			return "", nil, fmt.Errorf("%w: work is not part of this candidate", ErrInvalidWork)
		}
		return "review", nil, nil
	}
	// D9: a confirmed Bangumi track association replaces every local inference on it.
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE track_id=? AND source='auto'`, trackID); err != nil {
		return "", nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE track_subject_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE track_id=? AND status='candidate'`, candidateID, trackID); err != nil {
		return "", nil, err
	}
	if err = tx.Commit(); err != nil {
		return "", nil, err
	}
	return "succeeded", workIDs, nil
}
