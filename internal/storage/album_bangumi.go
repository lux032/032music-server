package storage

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lux032/032music-server/internal/metadata"
	"golang.org/x/text/unicode/norm"
)

// AlbumBangumiTarget is the stable snapshot used for search and optimistic confirmation.
type AlbumBangumiTarget struct {
	ID                                      int64
	Title, Artist, ReleaseDate, Fingerprint string
	TrackCount                              int
	Artists, Credits                        []string
	Recheck                                 bool
}

type BangumiTieup struct {
	SubjectID int64           `json:"subjectId"`
	Title     string          `json:"title"`
	NameCN    string          `json:"nameCn"`
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Date      string          `json:"date"`
	PosterURL string          `json:"posterUrl"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

var ErrBangumiWorkIdentityConflict = errors.New("bangumi work identity conflicts with another local work")

type AlbumSubjectCandidate struct {
	ID          int64           `json:"id"`
	AlbumID     int64           `json:"albumId"`
	ExternalID  string          `json:"externalId"`
	AlbumTitle  string          `json:"albumTitle"`
	Title       string          `json:"title"`
	ReleaseDate string          `json:"releaseDate"`
	Artist      string          `json:"artist"`
	Score       int             `json:"score"`
	Evidence    []string        `json:"evidence"`
	Tieups      []BangumiTieup  `json:"tieups"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
}

func albumBangumiFingerprint(title, artist, date string) string {
	return fmt.Sprintf("%x", sha1.Sum([]byte("album-bangumi-v1|"+title+"|"+artist+"|"+date)))
}

const bangumiAlbumArtistSQL = `COALESCE((SELECT GROUP_CONCAT(COALESCE(ar.user_display_name,ar.display_name),', ' ORDER BY aa.position,ar.id) FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id),'')`
const bangumiAlbumDateSQL = `COALESCE(a.user_release_date,a.release_date,a.user_original_release_date,a.original_release_date,'')`

// M4: force bypasses pending candidates and misses, but never bypasses user decisions,
// existing links, or compilation safeguards. A targeted album bypasses the anime filter.
func (s *Store) AlbumsForBangumiTieup(ctx context.Context, force bool, targetID int64) ([]AlbumBangumiTarget, error) {
	cacheDays := 30
	if setting, e := s.MetadataSourceSetting(ctx, "bangumi"); e == nil && setting.CacheDays > 0 {
		cacheDays = setting.CacheDays
	}
	query := `SELECT a.id,COALESCE(a.user_title,a.title),` + bangumiAlbumArtistSQL + `,` + bangumiAlbumDateSQL + `,(SELECT COUNT(*) FROM tracks t WHERE t.album_id=a.id),COALESCE((SELECT GROUP_CONCAT(DISTINCT COALESCE(ar.user_display_name,ar.display_name)) FROM tracks t JOIN track_artists ta ON ta.track_id=t.id AND ta.role='primary' JOIN artists ar ON ar.id=ta.artist_id WHERE t.album_id=a.id),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT COALESCE(ar.user_display_name,ar.display_name)) FROM tracks t JOIN track_artists ta ON ta.track_id=t.id AND ta.role IN ('composer','lyricist','arranger') JOIN artists ar ON ar.id=ta.artist_id WHERE t.album_id=a.id),''),COALESCE((SELECT m.fingerprint||'|'||m.checked_at FROM album_enrichment_misses m WHERE m.album_id=a.id AND m.source='bangumi'),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT an.value) FROM artist_names an JOIN album_artists aa ON aa.artist_id=an.artist_id WHERE aa.album_id=a.id),''),COALESCE((SELECT GROUP_CONCAT(DISTINCT an.value) FROM artist_names an JOIN track_artists ta ON ta.artist_id=an.artist_id AND ta.role='primary' JOIN tracks t ON t.id=ta.track_id WHERE t.album_id=a.id),'') FROM albums a WHERE NOT EXISTS(SELECT 1 FROM album_works aw WHERE aw.album_id=a.id AND aw.source IN ('manual','bangumi')) AND COALESCE(a.user_is_compilation,a.is_compilation,0)=0 AND NOT EXISTS(SELECT 1 FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id AND LOWER(COALESCE(ar.user_display_name,ar.display_name))='various artists') AND NOT EXISTS(SELECT 1 FROM tracks t JOIN audio_files af ON af.track_id=t.id JOIN audio_file_tags tag ON tag.audio_file_id=af.id WHERE t.album_id=a.id AND UPPER(tag.field_name) IN ('COMPILATION','TCMP') AND tag.value='1') AND NOT EXISTS(SELECT 1 FROM album_work_suppressions sup WHERE sup.album_id=a.id AND sup.inferred_key LIKE 'bangumi:%') AND NOT EXISTS(SELECT 1 FROM album_subject_candidates c WHERE c.album_id=a.id AND c.status IN ('confirmed','rejected'))`
	args := []any{}
	if !force {
		query += ` AND NOT EXISTS(SELECT 1 FROM album_subject_candidates c WHERE c.album_id=a.id AND c.status='candidate')`
	}
	if targetID > 0 {
		query += ` AND a.id=?`
		args = append(args, targetID)
	}
	if !force && targetID == 0 {
		query += ` AND (EXISTS(SELECT 1 FROM tracks t WHERE t.album_id=a.id AND COALESCE(t.user_track_type,t.track_type)='tv_size') OR EXISTS(SELECT 1 FROM tracks t JOIN track_genres tg ON tg.track_id=t.id JOIN genres g ON g.id=tg.genre_id WHERE t.album_id=a.id AND (LOWER(g.name) LIKE '%anime%' OR g.name LIKE '%アニメ%' OR g.name LIKE '%アニソン%')) OR EXISTS(SELECT 1 FROM album_genre_overrides ago JOIN genres g ON g.id=ago.genre_id WHERE ago.album_id=a.id AND (LOWER(g.name) LIKE '%anime%' OR g.name LIKE '%アニメ%' OR g.name LIKE '%アニソン%')))`
	}
	query += ` ORDER BY a.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AlbumBangumiTarget
	for rows.Next() {
		var v AlbumBangumiTarget
		var primary, credits, miss, aliases, trackAliases string
		if err = rows.Scan(&v.ID, &v.Title, &v.Artist, &v.ReleaseDate, &v.TrackCount, &primary, &credits, &miss, &aliases, &trackAliases); err != nil {
			return nil, err
		}
		v.Fingerprint = albumBangumiFingerprint(v.Title, v.Artist, v.ReleaseDate)
		v.Artists = append(append(append(splitAlbumBangumiNames(v.Artist), splitAlbumBangumiNames(primary)...), splitAlbumBangumiNames(aliases)...), splitAlbumBangumiNames(trackAliases)...)
		v.Credits = splitAlbumBangumiNames(credits)
		if miss != "" {
			fingerprint, checked, _ := strings.Cut(miss, "|")
			if fingerprint == v.Fingerprint {
				t, e := time.Parse(time.RFC3339Nano, checked)
				days := cacheDays
				if parsed, e2 := time.Parse("2006-01-02", v.ReleaseDate); e2 == nil && time.Since(parsed) <= 180*24*time.Hour {
					days = 7
				}
				if e == nil && time.Since(t) < time.Duration(days)*24*time.Hour && !force {
					continue
				}
				v.Recheck = true
			}
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func splitAlbumBangumiNames(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
func (s *Store) SetAlbumBangumiMiss(ctx context.Context, a AlbumBangumiTarget) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO album_enrichment_misses(album_id,source,fingerprint) VALUES(?,'bangumi',?) ON CONFLICT(album_id,source) DO UPDATE SET fingerprint=excluded.fingerprint,checked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, a.ID, a.Fingerprint)
	return err
}

func (s *Store) SaveAlbumSubjectCandidates(ctx context.Context, albumID int64, candidates []AlbumSubjectCandidate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM album_subject_candidates WHERE album_id=? AND status='candidate'`, albumID); err != nil {
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
		_, err = tx.ExecContext(ctx, `INSERT INTO album_subject_candidates(album_id,source,external_id,title,release_date,artist,score,evidence_json,tieups_json,payload_json) VALUES(?,'bangumi',?,?,?,?,?,?,?,?) ON CONFLICT(album_id,source,external_id) DO NOTHING`, albumID, v.ExternalID, v.Title, v.ReleaseDate, v.Artist, v.Score, string(ev), string(tie), rawOrEmpty(v.Payload))
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM album_enrichment_misses WHERE album_id=? AND source='bangumi'`, albumID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AlbumSubjectCandidates(ctx context.Context, albumID int64) ([]AlbumSubjectCandidate, error) {
	if albumID > 0 {
		if _, err := s.AlbumByID(ctx, albumID); err != nil {
			return nil, err
		}
	}
	query := `SELECT c.id,c.album_id,c.external_id,COALESCE(a.user_title,a.title),c.title,COALESCE(c.release_date,''),COALESCE(c.artist,''),c.score,c.evidence_json,c.tieups_json,c.payload_json,c.status FROM album_subject_candidates c JOIN albums a ON a.id=c.album_id WHERE (?=0 OR c.album_id=?) ORDER BY c.score DESC,c.id`
	rows, err := s.db.QueryContext(ctx, query, albumID, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumSubjectCandidate{}
	for rows.Next() {
		var v AlbumSubjectCandidate
		var ev, tie, raw string
		if err = rows.Scan(&v.ID, &v.AlbumID, &v.ExternalID, &v.AlbumTitle, &v.Title, &v.ReleaseDate, &v.Artist, &v.Score, &ev, &tie, &raw, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ev), &v.Evidence)
		_ = json.Unmarshal([]byte(tie), &v.Tieups)
		v.Payload = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) PendingAlbumSubjectCandidates(ctx context.Context, limit int) ([]AlbumSubjectCandidate, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.album_id,c.external_id,COALESCE(a.user_title,a.title),c.title,COALESCE(c.release_date,''),COALESCE(c.artist,''),c.score,c.evidence_json,c.tieups_json,'{}',c.status FROM album_subject_candidates c JOIN albums a ON a.id=c.album_id WHERE c.status='candidate' ORDER BY c.score DESC,c.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumSubjectCandidate{}
	for rows.Next() {
		var v AlbumSubjectCandidate
		var ev, tie, raw string
		if err = rows.Scan(&v.ID, &v.AlbumID, &v.ExternalID, &v.AlbumTitle, &v.Title, &v.ReleaseDate, &v.Artist, &v.Score, &ev, &tie, &raw, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ev), &v.Evidence)
		_ = json.Unmarshal([]byte(tie), &v.Tieups)
		v.Payload = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RejectAlbumSubjectCandidate(ctx context.Context, albumID, candidateID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var external, status string
	if err = tx.QueryRowContext(ctx, `SELECT external_id,status FROM album_subject_candidates WHERE album_id=? AND id=?`, albumID, candidateID).Scan(&external, &status); err != nil {
		return err
	}
	if status != "candidate" {
		return ErrCandidateNotPending
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, albumID, "bangumi:"+external); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE album_subject_candidates SET status='rejected',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, candidateID); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrCandidateNotPending = errors.New("candidate is no longer pending")

func bangumiWorkKey(music, work string) string { return "bangumi:" + music + ":" + work }

// H5: the first statement in this transaction obtains the SQLite write lock.
// The snapshot and all user intent are checked only after that lock is held.
func (s *Store) ConfirmAlbumSubjectCandidate(ctx context.Context, albumID, candidateID, runID int64, fingerprint string, automatic bool, selected []int64) (string, []int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	first, err := tx.ExecContext(ctx, `UPDATE albums SET work_fingerprint=work_fingerprint WHERE id=?`, albumID)
	if err != nil {
		return "", nil, err
	}
	n, _ := first.RowsAffected()
	if n == 0 {
		return "skipped", nil, nil
	}
	var title, artist, date string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(a.user_title,a.title),`+bangumiAlbumArtistSQL+`,`+bangumiAlbumDateSQL+` FROM albums a WHERE a.id=?`, albumID).Scan(&title, &artist, &date); err != nil {
		return "", nil, err
	}
	if automatic && fingerprint != albumBangumiFingerprint(title, artist, date) {
		return "review", nil, nil
	}
	var music, musicTitle, tieJSON, status string
	if err = tx.QueryRowContext(ctx, `SELECT external_id,title,tieups_json,status FROM album_subject_candidates WHERE album_id=? AND id=?`, albumID, candidateID).Scan(&music, &musicTitle, &tieJSON, &status); err != nil {
		return "", nil, err
	}
	if status != "candidate" {
		return "review", nil, nil
	}
	if automatic {
		var linked bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_works WHERE album_id=? AND source IN ('manual','bangumi'))`, albumID).Scan(&linked)
		if err != nil {
			return "", nil, err
		}
		if linked {
			return "review", nil, nil
		}
	}
	var suppressed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=? AND inferred_key=?)`, albumID, "bangumi:"+music).Scan(&suppressed)
	if err != nil {
		return "", nil, err
	}
	if suppressed {
		return "review", nil, nil
	}
	if automatic {
		var anyBangumiSuppression bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=? AND inferred_key LIKE 'bangumi:%')`, albumID).Scan(&anyBangumiSuppression); err != nil {
			return "", nil, err
		}
		if anyBangumiSuppression {
			return "review", nil, nil
		}
	}
	var ties []BangumiTieup
	if err = json.Unmarshal([]byte(tieJSON), &ties); err != nil {
		return "", nil, err
	}
	chosen := map[int64]bool{}
	for _, v := range selected {
		chosen[v] = true
	}
	if !automatic && selected == nil {
		// Album review keeps its designed "accept every specific work" action.
		// (The empty-selection error is a track-level rule, M-2.)
		for _, tie := range ties {
			if isSpecificBangumiRole(tie.Role) {
				chosen[tie.SubjectID] = true
			}
		}
		if len(chosen) == 0 {
			for _, tie := range ties {
				chosen[tie.SubjectID] = true
			}
		}
	}
	var workIDs []int64
	for _, tie := range ties {
		if automatic && len(selected) == 1 && tie.SubjectID != selected[0] || !automatic && !chosen[tie.SubjectID] {
			continue
		}
		workID := strconv.FormatInt(tie.SubjectID, 10)
		key := bangumiWorkKey(music, workID)
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=? AND inferred_key=?)`, albumID, key).Scan(&suppressed)
		if err != nil {
			return "", nil, err
		}
		if suppressed {
			return "review", nil, nil
		}
		var rejected bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_subject_candidates c WHERE c.album_id=? AND c.external_id=? AND c.status='rejected')`, albumID, music).Scan(&rejected)
		if err != nil {
			return "", nil, err
		}
		if rejected {
			return "review", nil, nil
		}
		if automatic {
			keys, e := suppressionKeys(ctx, tx, `SELECT inferred_key FROM album_work_suppressions WHERE album_id=?`, albumID)
			if e != nil {
				return "", nil, e
			}
			for _, title := range []string{tie.Title, tie.NameCN} {
				if title == "" {
					continue
				}
				for _, stored := range keys {
					if workTitleKeysMatch(stored, workTitleSuppressionKey(title, tie.Type)) {
						return "review", nil, nil
					}
				}
			}
		}
		if tie.Type != "anime" && tie.Type != "movie" && tie.Type != "game" {
			return "", nil, ErrInvalidWork
		}
		id, e := resolveBangumiWork(ctx, tx, tie, runID)
		if errors.Is(e, ErrBangumiWorkIdentityConflict) {
			_ = tx.Rollback()
			_, updateErr := s.db.ExecContext(ctx, `UPDATE album_subject_candidates SET evidence_json=CASE WHEN json_valid(evidence_json) AND json_type(evidence_json)='array' THEN CASE WHEN EXISTS(SELECT 1 FROM json_each(evidence_json) WHERE value=?) THEN evidence_json ELSE json_insert(evidence_json,'$[#]',?) END ELSE json_array(?) END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='candidate'`, "本地作品身份冲突，需人工审核", "本地作品身份冲突，需人工审核", "本地作品身份冲突，需人工审核", candidateID)
			if updateErr != nil {
				return "", nil, updateErr
			}
			return "review", nil, nil
		}
		if e != nil {
			return "", nil, e
		}
		keys, e := suppressionKeys(ctx, tx, `SELECT inferred_key FROM album_work_suppressions WHERE album_id=?`, albumID)
		if e != nil {
			return "", nil, e
		}
		identities, e := workSuppressionIdentities(ctx, tx, id)
		if e != nil {
			return "", nil, e
		}
		var matchedTitleKeys []string
		for _, stored := range keys {
			storedParts := strings.Split(stored, "|")
			for _, identity := range identities {
				identityKey := workTitleSuppressionKey(identity.Title, identity.Type)
				if workTitleKeysMatch(stored, identityKey) {
					if automatic {
						return "review", nil, nil
					}
					// Manual acceptance only reverses a typed removal for the type
					// being accepted. Legacy non-three-part keys have no reliable
					// type segment, so retain their historical title-only behavior.
					if len(storedParts) != 3 || storedParts[1] == identity.Type {
						matchedTitleKeys = append(matchedTitleKeys, stored)
					}
				}
			}
		}
		if !automatic {
			for _, stored := range matchedTitleKeys {
				if _, e = tx.ExecContext(ctx, `DELETE FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, albumID, stored); e != nil {
					return "", nil, e
				}
			}
		}
		albumRole := "other"
		if tie.Role == "ost" {
			albumRole = "ost"
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,?,'bangumi',?) ON CONFLICT(album_id,work_id) DO UPDATE SET role=excluded.role,source='bangumi',inferred_key=excluded.inferred_key,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE album_works.source='auto'`, albumID, id, albumRole, key)
		if e != nil {
			return "", nil, e
		}
		if tie.Role != "ost" && tie.Role != "other" {
			if e = linkBangumiAlbumTracks(ctx, tx, albumID, id, musicTitle, tie.Role, key); e != nil {
				return "", nil, e
			}
		}
		workIDs = append(workIDs, id)
	}
	if len(workIDs) == 0 {
		return "review", nil, nil
	}
	// D2: a confirmed Bangumi association replaces all album-level local inference.
	if _, err = tx.ExecContext(ctx, `DELETE FROM album_works WHERE album_id=? AND source='auto'`, albumID); err != nil {
		return "", nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE album_subject_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE album_id=? AND status='candidate'`, candidateID, albumID); err != nil {
		return "", nil, err
	}
	if err = tx.Commit(); err != nil {
		return "", nil, err
	}
	return "succeeded", workIDs, nil
}
func isSpecificBangumiRole(role string) bool {
	switch role {
	case "op", "ed", "insert", "ost", "character":
		return true
	}
	return false
}

// BangumiTrackTitleKey compares a track title with a Bangumi music entry name.
// The loose form keeps only letters and numbers so punctuation cannot hide a match.
func BangumiTrackTitleKey(s string, loose bool) string { return bangumiTrackTitleKey(s, loose) }

func bangumiTrackTitleKey(s string, loose bool) string {
	s = strings.ToLower(norm.NFKC.String(strings.TrimSpace(s)))
	if !loose {
		return strings.Join(strings.Fields(s), " ")
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return -1
	}, s)
}

func bangumiTrackTitleMatches(trackTitle, trackType, musicTitle string) bool {
	matches := func(a, b string) bool {
		return bangumiTrackTitleKey(a, false) != "" && (bangumiTrackTitleKey(a, false) == bangumiTrackTitleKey(b, false) || bangumiTrackTitleKey(a, true) == bangumiTrackTitleKey(b, true))
	}
	if matches(trackTitle, musicTitle) {
		return true
	}
	if trackType != "tv_size" && trackType != "instrumental" && trackType != "off_vocal" {
		return false
	}
	base := metadata.StripTrackTypeSuffix(trackTitle, trackType)
	return base != trackTitle && matches(base, musicTitle)
}

func linkBangumiAlbumTracks(ctx context.Context, tx *sql.Tx, albumID, workID int64, musicTitle, role, inferredKey string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(user_title,title),COALESCE(user_track_type,track_type,'regular') FROM tracks WHERE album_id=? ORDER BY id`, albumID)
	if err != nil {
		return err
	}
	type track struct {
		id               int64
		title, trackType string
	}
	var tracks []track
	for rows.Next() {
		var v track
		if err = rows.Scan(&v.id, &v.title, &v.trackType); err != nil {
			rows.Close()
			return err
		}
		tracks = append(tracks, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, v := range tracks {
		if !bangumiTrackTitleMatches(v.title, v.trackType, musicTitle) {
			continue
		}
		suppressed, err := bangumiTrackLinkSuppressed(ctx, tx, v.id, albumID, strings.Split(inferredKey, ":")[1], BangumiTieup{}, workID)
		if err != nil {
			return err
		}
		if suppressed {
			continue
		}
		if _, err = landBangumiTrack(ctx, tx, v.id, workID, role, inferredKey); err != nil {
			return err
		}
	}
	return nil
}

// bangumiTrackLinkSuppressed reports whether a Bangumi track link would revive a
// user decision. It checks the exact bangumi:<music>:<work> key, any external-id
// key, and every known title identity of the resolved work (or, before a work is
// resolved, the remote title and translation). Type is ignored, matching 025.
func bangumiTrackLinkSuppressed(ctx context.Context, tx *sql.Tx, trackID, albumID int64, music string, tie BangumiTieup, workID int64) (bool, error) {
	trackKeys, err := suppressionKeys(ctx, tx, `SELECT inferred_key FROM track_work_suppressions WHERE track_id=?`, trackID)
	if err != nil {
		return false, err
	}
	// RejectTrackSubjectCandidate writes the two-part key bangumi:<music>.
	// That refusal covers every work of the entry, including album-level landing.
	for _, stored := range trackKeys {
		if stored == "bangumi:"+music {
			return true, nil
		}
	}
	albumKeys, err := suppressionKeys(ctx, tx, `SELECT inferred_key FROM album_work_suppressions WHERE album_id=?`, albumID)
	if err != nil {
		return false, err
	}
	keys := append(trackKeys, albumKeys...)
	if workID != 0 {
		external, e := workBangumiExternalID(ctx, tx, workID)
		if e != nil {
			return false, e
		}
		if bangumiWorkKey(music, external) != "bangumi:"+music+":" {
			for _, stored := range keys {
				if stored == bangumiWorkKey(music, external) {
					return true, nil
				}
			}
		}
		if bangumiExternalSuppressed(keys, external) {
			return true, nil
		}
		identities, e := workSuppressionIdentities(ctx, tx, workID)
		if e != nil {
			return false, e
		}
		for _, stored := range keys {
			for _, identity := range identities {
				if workTitleKeysMatch(stored, workTitleSuppressionKey(identity.Title, identity.Type)) {
					return true, nil
				}
			}
		}
		return false, nil
	}
	workKey := strconv.FormatInt(tie.SubjectID, 10)
	for _, stored := range keys {
		if stored == bangumiWorkKey(music, workKey) {
			return true, nil
		}
	}
	if bangumiExternalSuppressed(keys, workKey) {
		return true, nil
	}
	for _, stored := range keys {
		for _, title := range []string{tie.Title, tie.NameCN} {
			if title != "" && workTitleKeysMatch(stored, workTitleSuppressionKey(title, tie.Type)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// landBangumiTrack writes one Bangumi track association. A manual row for the same
// work and track is never overwritten. Local auto rows for that pair are removed
// so Bangumi replaces inference (D9). The conflict update also refuses a manual row.
func landBangumiTrack(ctx context.Context, tx *sql.Tx, trackID, workID int64, role, key string) (bool, error) {
	var manual bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_tracks WHERE work_id=? AND track_id=? AND source='manual')`, workID, trackID).Scan(&manual); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE work_id=? AND track_id=? AND source='auto'`, workID, trackID); err != nil {
		return false, err
	}
	if manual {
		return false, nil
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,?,'bangumi',?) ON CONFLICT(work_id,track_id,role,season,sequence) DO UPDATE SET source='bangumi',inferred_key=excluded.inferred_key WHERE work_tracks.source<>'manual'`, workID, trackID, role, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// B1/B2: only typed anime/movie/game entries reach this resolver. External ID
// ownership wins; local unbound identity is checked with NFKC and season parity.
func resolveBangumiWork(ctx context.Context, tx *sql.Tx, tie BangumiTieup, runID int64) (int64, error) {
	external := strconv.FormatInt(tie.SubjectID, 10)
	var id int64
	bound := false
	err := tx.QueryRowContext(ctx, `SELECT work_id FROM work_external_profiles WHERE source='bangumi' AND external_id=?`, external).Scan(&id)
	if err == nil {
		bound = true
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		rows, e := tx.QueryContext(ctx, `SELECT DISTINCT w.id,w.title,w.type,COALESCE(x.normalized_key,'') FROM works w LEFT JOIN work_aliases x ON x.work_id=w.id WHERE w.type IN (?,'other') AND NOT EXISTS(SELECT 1 FROM work_external_profiles p WHERE p.work_id=w.id AND p.source='bangumi') AND NOT EXISTS(SELECT 1 FROM work_match_candidates c WHERE c.work_id=w.id AND c.source='bangumi' AND c.external_id=? AND c.status='rejected') ORDER BY CASE WHEN w.type=? THEN 0 ELSE 1 END,w.id`, tie.Type, external, tie.Type)
		if e != nil {
			return 0, e
		}
		for rows.Next() {
			var candidate int64
			var title, typ, alias string
			if e = rows.Scan(&candidate, &title, &typ, &alias); e != nil {
				break
			}
			if metadata.WorkSeasonNumber(title) != metadata.WorkSeasonNumber(tie.Title) {
				continue
			}
			for _, remote := range []string{tie.Title, tie.NameCN} {
				if remote != "" && metadata.WorkSeasonNumber(remote) == metadata.WorkSeasonNumber(title) && (norm.NFKC.String(metadata.Normalize(title)) == norm.NFKC.String(metadata.Normalize(remote)) || bangumiStrictKey(title) == bangumiStrictKey(remote) || alias != "" && metadata.WorkSeasonNumber(alias) == metadata.WorkSeasonNumber(remote) && (norm.NFKC.String(alias) == norm.NFKC.String(metadata.Normalize(remote)) || bangumiStrictKey(alias) == bangumiStrictKey(remote))) {
					id = candidate
					break
				}
			}
			if id != 0 {
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return 0, e
		}
	}
	if id == 0 {
		var collision bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM works WHERE normalized_title=? AND type=? AND COALESCE(year,0)=?)`, metadata.Normalize(tie.Title), tie.Type, yearFromDate(tie.Date)).Scan(&collision); err != nil {
			return 0, err
		}
		if collision {
			return 0, ErrBangumiWorkIdentityConflict
		}
		r, e := tx.ExecContext(ctx, `INSERT INTO works(title,normalized_title,translated_title,type,year,origin) VALUES(?,?,NULLIF(?,''),?,NULLIF(?,0),'auto')`, tie.Title, metadata.Normalize(tie.Title), tie.NameCN, tie.Type, yearFromDate(tie.Date))
		if e != nil {
			return 0, e
		}
		id, _ = r.LastInsertId()
	}
	raw := tie.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	p := ExternalWorkProfile{Source: "bangumi", ExternalID: external, PageURL: "https://bgm.tv/subject/" + external, Title: tie.Title, TranslatedTitle: tie.NameCN, Type: tie.Type, Year: yearFromDate(tie.Date), PosterURL: tie.PosterURL, Raw: raw, FetchedAt: fetchedOrNow("")}
	if !bound {
		if err = applyWorkExternalProfileTx(ctx, tx, id, p, runID); err != nil {
			return 0, err
		}
	}
	for _, name := range []string{tie.Title, tie.NameCN} {
		if name == "" {
			continue
		}
		for _, key := range []string{metadata.Normalize(name), metadata.Normalize(norm.NFKC.String(name)), bangumiStrictKey(name)} {
			if key == "" {
				continue
			}
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_aliases(normalized_key,work_id) VALUES(?,?)`, key, id); err != nil {
				return 0, err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM work_match_candidates WHERE work_id=? AND source='bangumi' AND status='candidate'`, id)
	return id, err
}
func bangumiStrictKey(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	s = strings.NewReplacer("〜", "~", "‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "−", "-").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
func yearFromDate(s string) int {
	if len(s) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(s[:4])
	return y
}
