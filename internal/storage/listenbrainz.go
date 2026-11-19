package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

const ListenBrainzDefaultServerURL = "https://api.listenbrainz.org"

// ListenBrainzTrackFingerprint identifies a track's artist/title/album tuple so
// cached recording MBID/MSID lookups are invalidated if metadata changes.
func ListenBrainzTrackFingerprint(artist, track, album string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(artist)) + "\x00" + strings.ToLower(strings.TrimSpace(track)) + "\x00" + strings.ToLower(strings.TrimSpace(album))))
	return hex.EncodeToString(sum[:])
}

type queryContexter interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ListenBrainzTrack is the metadata submitted to ListenBrainz for one listen.
type ListenBrainzTrack struct {
	ID              int64
	TrackID         int64
	Artist          string
	Track           string
	Album           string
	AlbumArtist     string
	TrackNumber     int
	DurationSeconds int64
	RecordingMBID   string
	ReleaseMBID     string
	ArtistMBIDs     []string
	StartedAt       int64
	Attempts        int
}

func enqueueListenBrainzScrobble(ctx context.Context, tx *sql.Tx, trackID, duration int64, startedAt time.Time) (bool, error) {
	var enabled int
	var serverURL, userToken string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,server_url,user_token FROM listenbrainz_settings WHERE id=1`).Scan(&enabled, &serverURL, &userToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if enabled == 0 || strings.TrimSpace(serverURL) == "" || strings.TrimSpace(userToken) == "" {
		return false, nil
	}
	if duration > 0 && duration <= lastFMMinimumDurationMillis {
		return false, nil
	}
	meta, err := listenBrainzTrackMetadata(ctx, tx, trackID)
	if err != nil {
		return false, err
	}
	if meta.Artist == "" || meta.Track == "" || strings.EqualFold(meta.Artist, "Unknown Artist") {
		return false, nil
	}
	artistMBIDsJSON, err := json.Marshal(meta.ArtistMBIDs)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO listenbrainz_scrobble_queue(track_id,artist,track,album,album_artist,track_number,duration_seconds,recording_mbid,release_mbid,artist_mbids_json,started_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		trackID, meta.Artist, meta.Track, meta.Album, meta.AlbumArtist, meta.TrackNumber, duration/1000, meta.RecordingMBID, meta.ReleaseMBID, string(artistMBIDsJSON), startedAt.Unix())
	return err == nil, err
}

func listenBrainzTrackMetadata(ctx context.Context, db queryContexter, trackID int64) (ListenBrainzTrack, error) {
	var value ListenBrainzTrack
	var albumID int64
	err := db.QueryRowContext(ctx, `SELECT t.id,a.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),`+trackArtistSQL+`,
		COALESCE(a.user_performed_by,a.performed_by,(SELECT GROUP_CONCAT(COALESCE(ar.user_display_name,ar.display_name),', ') FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id),''),
		COALESCE(t.user_track_number,t.track_number,0),COALESCE(t.duration_ms,0)/1000
		FROM tracks t JOIN albums a ON a.id=t.album_id WHERE t.id=?`, trackID).Scan(&value.TrackID, &albumID, &value.Track, &value.Album, &value.Artist, &value.AlbumArtist, &value.TrackNumber, &value.DurationSeconds)
	if err != nil {
		return value, err
	}
	value.Artist = strings.TrimSpace(value.Artist)
	value.Track = strings.TrimSpace(value.Track)
	value.Album = strings.TrimSpace(value.Album)
	value.AlbumArtist = strings.TrimSpace(value.AlbumArtist)
	if value.AlbumArtist == value.Artist {
		value.AlbumArtist = ""
	}
	value.RecordingMBID = lookupTagRecordingMBID(ctx, db, trackID)
	if value.RecordingMBID == "" {
		fp := ListenBrainzTrackFingerprint(value.Artist, value.Track, value.Album)
		var cachedMBID string
		if err := db.QueryRowContext(ctx, `SELECT recording_mbid FROM listenbrainz_track_ids WHERE track_id=? AND meta_fingerprint=?`, trackID, fp).Scan(&cachedMBID); err == nil {
			if id, ok := metadata.NormalizeMusicBrainzID(cachedMBID); ok {
				value.RecordingMBID = id
			}
		}
	}
	value.ReleaseMBID = lookupTrackReleaseMBID(ctx, db, trackID, albumID)
	value.ArtistMBIDs = lookupTrackArtistMBIDs(ctx, db, trackID)
	return value, nil
}

func lookupTagRecordingMBID(ctx context.Context, db queryContexter, trackID int64) string {
	rows, err := db.QueryContext(ctx, `SELECT tag.value FROM audio_files af JOIN audio_file_tags tag ON tag.audio_file_id=af.id
		WHERE af.track_id=? AND UPPER(TRIM(tag.field_name)) IN ('MUSICBRAINZ_TRACKID','MUSICBRAINZ TRACK ID')
		ORDER BY CASE af.status WHEN 'available' THEN 0 ELSE 1 END, af.id, tag.position`, trackID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		if id, ok := metadata.NormalizeMusicBrainzID(raw); ok {
			return id
		}
	}
	return ""
}

func lookupTrackReleaseMBID(ctx context.Context, db queryContexter, trackID, albumID int64) string {
	var externalID string
	if err := db.QueryRowContext(ctx, `SELECT external_id FROM album_external_profiles WHERE album_id=? AND source='musicbrainz' LIMIT 1`, albumID).Scan(&externalID); err == nil {
		if id, ok := metadata.NormalizeMusicBrainzID(externalID); ok {
			return id
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT tag.value FROM audio_files af JOIN audio_file_tags tag ON tag.audio_file_id=af.id
		WHERE af.track_id=? AND UPPER(TRIM(tag.field_name)) IN ('MUSICBRAINZ_ALBUMID','MUSICBRAINZ ALBUM ID')
		ORDER BY CASE af.status WHEN 'available' THEN 0 ELSE 1 END, af.id, tag.position`, trackID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		if id, ok := metadata.NormalizeMusicBrainzID(raw); ok {
			return id
		}
	}
	return ""
}

func lookupTrackArtistMBIDs(ctx context.Context, db queryContexter, trackID int64) []string {
	var ids []string
	appendID := func(raw string) {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' }) {
			if id, ok := metadata.NormalizeMusicBrainzID(part); ok && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT ep.external_id FROM track_artists ta JOIN artist_external_profiles ep ON ep.artist_id=ta.artist_id AND ep.source='musicbrainz'
		WHERE ta.track_id=? AND ta.role='primary' ORDER BY ta.position, ta.artist_id`, trackID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ext string
			if rows.Scan(&ext) == nil {
				appendID(ext)
			}
		}
	}
	if len(ids) > 0 {
		return ids
	}
	tagRows, err := db.QueryContext(ctx, `SELECT tag.value FROM audio_files af JOIN audio_file_tags tag ON tag.audio_file_id=af.id
		WHERE af.track_id=? AND UPPER(TRIM(tag.field_name)) IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID')
		ORDER BY CASE af.status WHEN 'available' THEN 0 ELSE 1 END, af.id, tag.position`, trackID)
	if err != nil {
		return nil
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var raw string
		if tagRows.Scan(&raw) == nil {
			appendID(raw)
		}
	}
	return ids
}

// ListenBrainzNowPlayingTrack loads track metadata and embedded MBIDs for a playing_now submission.
func (s *Store) ListenBrainzNowPlayingTrack(ctx context.Context, trackID int64) (ListenBrainzTrack, error) {
	return listenBrainzTrackMetadata(ctx, s.db, trackID)
}

type ListenBrainzSettings struct {
	Enabled, NowPlaying, SyncLoves bool
	ServerURL                      string
	UserToken, Username            string
	LookupUnsupported              bool
	FeedbackUnsupported            bool
	LastSuccessAt, LastError       string
	LastErrorAt                    string
	LastLoveError, LastLoveErrorAt string
	PendingCount                   int64
	OldestPendingStartedAtUnix     int64
}

func (v ListenBrainzSettings) HasToken() bool  { return strings.TrimSpace(v.UserToken) != "" }
func (v ListenBrainzSettings) Connected() bool { return v.HasToken() && v.Username != "" }
func (v ListenBrainzSettings) Ready() bool {
	return v.Enabled && strings.TrimSpace(v.ServerURL) != "" && v.HasToken()
}
func (v ListenBrainzSettings) LoveSyncReady() bool {
	return v.SyncLoves && strings.TrimSpace(v.ServerURL) != "" && v.HasToken() && !v.FeedbackUnsupported
}

func (s *Store) ListenBrainzSettings(ctx context.Context) (ListenBrainzSettings, error) {
	var value ListenBrainzSettings
	var enabled, nowPlaying, syncLoves, lookupUnsupported, feedbackUnsupported int
	err := s.db.QueryRowContext(ctx, `SELECT l.enabled,l.now_playing,l.sync_loves,l.server_url,l.user_token,l.username,l.lookup_unsupported,l.feedback_unsupported,
		COALESCE(l.last_success_at,''),l.last_error,COALESCE(l.last_error_at,''),l.last_love_error,COALESCE(l.last_love_error_at,''),
		(SELECT COUNT(*) FROM listenbrainz_scrobble_queue),COALESCE((SELECT MIN(started_at) FROM listenbrainz_scrobble_queue),0)
		FROM listenbrainz_settings l WHERE l.id=1`).Scan(
		&enabled, &nowPlaying, &syncLoves, &value.ServerURL, &value.UserToken, &value.Username,
		&lookupUnsupported, &feedbackUnsupported,
		&value.LastSuccessAt, &value.LastError, &value.LastErrorAt, &value.LastLoveError, &value.LastLoveErrorAt,
		&value.PendingCount, &value.OldestPendingStartedAtUnix,
	)
	value.Enabled = enabled != 0
	value.NowPlaying = nowPlaying != 0
	value.SyncLoves = syncLoves != 0
	value.LookupUnsupported = lookupUnsupported != 0
	value.FeedbackUnsupported = feedbackUnsupported != 0
	if strings.TrimSpace(value.ServerURL) == "" {
		value.ServerURL = ListenBrainzDefaultServerURL
	}
	return value, err
}

func (s *Store) SaveListenBrainzPreferences(ctx context.Context, enabled, nowPlaying bool, serverURL string) error {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		serverURL = ListenBrainzDefaultServerURL
	}
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings
		SET enabled=?,now_playing=?,
			lookup_unsupported=CASE WHEN server_url<>? THEN 0 ELSE lookup_unsupported END,
			feedback_unsupported=CASE WHEN server_url<>? THEN 0 ELSE feedback_unsupported END,
			server_url=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id=1`, boolInt(enabled), boolInt(nowPlaying), serverURL, serverURL, serverURL)
	return err
}

func (s *Store) SetListenBrainzConnection(ctx context.Context, serverURL, userToken, username string) error {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		serverURL = ListenBrainzDefaultServerURL
	}
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings
		SET server_url=?,user_token=?,username=?,lookup_unsupported=0,feedback_unsupported=0,
			last_error='',last_error_at=NULL,last_love_error='',last_love_error_at=NULL,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id=1`, serverURL, userToken, username)
	return err
}

func (s *Store) ClearListenBrainzConnection(ctx context.Context, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings
		SET user_token='',
			username=CASE WHEN ?='' THEN '' ELSE username END,
			lookup_unsupported=0,feedback_unsupported=0,
			last_error=?,
			last_error_at=CASE WHEN ?='' THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id=1`, reason, reason, reason)
	return err
}

func (s *Store) SetListenBrainzStatus(ctx context.Context, success bool, message string) error {
	if success {
		_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings SET last_success_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),last_error='',last_error_at=NULL WHERE id=1`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings SET last_error=?,last_error_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, message)
	return err
}

func (s *Store) SetListenBrainzLookupUnsupported(ctx context.Context, unsupported bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings SET lookup_unsupported=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, boolInt(unsupported))
	return err
}

func (s *Store) SetListenBrainzFeedbackUnsupported(ctx context.Context, unsupported bool, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings
		SET feedback_unsupported=?,last_love_error=?,
			last_love_error_at=CASE WHEN ?='' THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id=1`, boolInt(unsupported), message, message)
	return err
}

func (s *Store) DueListenBrainzScrobbles(ctx context.Context, now time.Time, limit int) ([]ListenBrainzTrack, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(track_id,0),artist,track,album,album_artist,track_number,duration_seconds,recording_mbid,release_mbid,artist_mbids_json,started_at,attempts
		FROM listenbrainz_scrobble_queue WHERE next_attempt_at<=? ORDER BY started_at,id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ListenBrainzTrack
	for rows.Next() {
		var v ListenBrainzTrack
		var artistMBIDsJSON string
		if err := rows.Scan(&v.ID, &v.TrackID, &v.Artist, &v.Track, &v.Album, &v.AlbumArtist, &v.TrackNumber, &v.DurationSeconds, &v.RecordingMBID, &v.ReleaseMBID, &artistMBIDsJSON, &v.StartedAt, &v.Attempts); err != nil {
			return nil, err
		}
		if artistMBIDsJSON != "" && artistMBIDsJSON != "[]" {
			_ = json.Unmarshal([]byte(artistMBIDsJSON), &v.ArtistMBIDs)
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func (s *Store) NextListenBrainzScrobbleAttempt(ctx context.Context) (time.Time, error) {
	var next sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(next_attempt_at) FROM listenbrainz_scrobble_queue`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}, err
	}
	return time.Unix(next.Int64, 0), nil
}

func (s *Store) DeleteListenBrainzScrobbles(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := inClause(ids)
	_, err := s.db.ExecContext(ctx, `DELETE FROM listenbrainz_scrobble_queue WHERE id IN (`+placeholders+`)`, args...)
	return err
}

func (s *Store) DeferListenBrainzScrobbles(ctx context.Context, ids []int64, next time.Time, message string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := inClause(ids)
	args = append([]any{next.Unix(), message}, args...)
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_scrobble_queue SET attempts=attempts+1,next_attempt_at=?,last_error=? WHERE id IN (`+placeholders+`)`, args...)
	return err
}

func (s *Store) RetryListenBrainzScrobblesNow(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_scrobble_queue SET next_attempt_at=0`)
	return err
}

type ListenBrainzTrackIDCache struct {
	RecordingMBID   string
	RecordingMSID   string
	LookupCheckedAt int64
}

func (s *Store) ListenBrainzTrackIDCache(ctx context.Context, trackID int64, artist, track, album string) (ListenBrainzTrackIDCache, error) {
	var cache ListenBrainzTrackIDCache
	fp := ListenBrainzTrackFingerprint(artist, track, album)
	err := s.db.QueryRowContext(ctx, `SELECT recording_mbid,recording_msid,lookup_checked_at FROM listenbrainz_track_ids WHERE track_id=? AND meta_fingerprint=?`, trackID, fp).
		Scan(&cache.RecordingMBID, &cache.RecordingMSID, &cache.LookupCheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ListenBrainzTrackIDCache{}, nil
	}
	return cache, err
}

func (s *Store) UpsertListenBrainzRecordingMSID(ctx context.Context, trackID int64, artist, track, album, msid string) error {
	msid, ok := metadata.NormalizeMusicBrainzID(msid)
	if !ok {
		return nil
	}
	fp := ListenBrainzTrackFingerprint(artist, track, album)
	_, err := s.db.ExecContext(ctx, `INSERT INTO listenbrainz_track_ids(track_id,meta_fingerprint,recording_msid) VALUES(?,?,?)
		ON CONFLICT(track_id) DO UPDATE SET
			recording_mbid=CASE WHEN listenbrainz_track_ids.meta_fingerprint=excluded.meta_fingerprint THEN listenbrainz_track_ids.recording_mbid ELSE '' END,
			lookup_checked_at=CASE WHEN listenbrainz_track_ids.meta_fingerprint=excluded.meta_fingerprint THEN listenbrainz_track_ids.lookup_checked_at ELSE 0 END,
			meta_fingerprint=excluded.meta_fingerprint,
			recording_msid=excluded.recording_msid,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, trackID, fp, msid)
	return err
}

func (s *Store) UpsertListenBrainzLookupResult(ctx context.Context, trackID int64, artist, track, album, mbid string, checkedAt time.Time) error {
	if mbid != "" {
		normalized, ok := metadata.NormalizeMusicBrainzID(mbid)
		if !ok {
			normalized = ""
		}
		mbid = normalized
	}
	fp := ListenBrainzTrackFingerprint(artist, track, album)
	_, err := s.db.ExecContext(ctx, `INSERT INTO listenbrainz_track_ids(track_id,meta_fingerprint,recording_mbid,lookup_checked_at) VALUES(?,?,?,?)
		ON CONFLICT(track_id) DO UPDATE SET
			recording_msid=CASE WHEN listenbrainz_track_ids.meta_fingerprint=excluded.meta_fingerprint THEN listenbrainz_track_ids.recording_msid ELSE '' END,
			meta_fingerprint=excluded.meta_fingerprint,
			recording_mbid=excluded.recording_mbid,
			lookup_checked_at=excluded.lookup_checked_at,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, trackID, fp, mbid, checkedAt.Unix())
	return err
}
