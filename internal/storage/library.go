package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

type Library struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"rootPath"`
}

type ArtworkInput struct {
	Hash, MIMEType, CachePath, SourceType, SourcePath string
	ByteSize                                          int64
}

type ImportInput struct {
	LibraryID              int64
	RelativePath           string
	FileSize, ModifiedAtNS int64
	Metadata               metadata.AudioMetadata
	Artwork                *ArtworkInput
	AudioProps             metadata.AudioProps
	HasExternalLRC         bool
}

type ScanJob struct {
	ID                  int64  `json:"id"`
	LibraryID           int64  `json:"libraryId"`
	ScanType            string `json:"scanType"`
	Status              string `json:"status"`
	DiscoveredFiles     int64  `json:"discoveredFiles"`
	ProcessedFiles      int64  `json:"processedFiles"`
	SkippedFiles        int64  `json:"skippedFiles"`
	FailedFiles         int64  `json:"failedFiles"`
	MissingFiles        int64  `json:"missingFiles"`
	CurrentRelativePath string `json:"currentRelativePath"`
	ErrorMessage        string `json:"errorMessage"`
	CreatedAt           string `json:"createdAt"`
	StartedAt           string `json:"startedAt"`
	FinishedAt          string `json:"finishedAt"`
}

type Artist struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	ImageURL   string `json:"imageUrl"`
	AlbumCount int64  `json:"albumCount"`
	TrackCount int64  `json:"trackCount"`
	IsFavorite bool   `json:"isFavorite"`
}

type Album struct {
	ID                  int64  `json:"id"`
	Title               string `json:"title"`
	Artist              string `json:"artist"`
	Genres              string `json:"genres"`
	ArtworkURL          string `json:"artworkUrl"`
	Year                int    `json:"year"`
	DiscCount           int    `json:"discCount"`
	TrackCount          int64  `json:"trackCount"`
	PerformedBy         string `json:"performedBy"`
	AlbumType           string `json:"albumType"`
	Version             string `json:"version"`
	ReleaseDate         string `json:"releaseDate"`
	OriginalReleaseDate string `json:"originalReleaseDate"`
	Label               string `json:"label"`
	CatalogNumber       string `json:"catalogNumber"`
	Country             string `json:"country"`
	Review              string `json:"review"`
	Compilation         bool   `json:"compilation"`
	Live                bool   `json:"live"`
	Bootleg             bool   `json:"bootleg"`
	Formats             string `json:"formats"`
	TotalBytes          int64  `json:"totalBytes"`
	TotalSize           string `json:"totalSize"`
	AddedAt             string `json:"addedAt"`
	UpdatedAt           string `json:"updatedAt"`
	LastPlayedAt        string `json:"lastPlayedAt,omitempty"`
	IsFavorite          bool   `json:"isFavorite"`
}

type AlbumEdit struct {
	Title, PerformedBy, AlbumType, Version, ReleaseDate, OriginalReleaseDate string
	Label, CatalogNumber, Country, Review                                    string
	Year                                                                     int
	Compilation, Live, Bootleg                                               bool
	Genres                                                                   []string
}

type Track struct {
	ID             int64  `json:"id"`
	AlbumID        int64  `json:"albumId"`
	Title          string `json:"title"`
	Album          string `json:"album"`
	Artist         string `json:"artist"`
	Genres         string `json:"genres"`
	Composer       string `json:"composer"`
	Lyricist       string `json:"lyricist"`
	Arranger       string `json:"arranger"`
	TrackType      string `json:"trackType"`
	Container      string `json:"container"`
	MIMEType       string `json:"mimeType"`
	RelativePath   string `json:"relativePath"`
	ArtworkURL     string `json:"artworkUrl"`
	Year           int    `json:"year"`
	DiscNumber     int    `json:"discNumber"`
	TrackNumber    int    `json:"trackNumber"`
	FileSize       int64  `json:"fileSize"`
	DurationMillis int64  `json:"durationMillis"`
	StreamURL      string `json:"streamUrl"`
	AddedAt        string `json:"addedAt"`
	UpdatedAt      string `json:"updatedAt"`
	LastPlayedAt   string `json:"lastPlayedAt,omitempty"`
	PositionMillis int64  `json:"positionMillis"`
	PlayCount      int64  `json:"playCount"`
	IsFavorite     bool   `json:"isFavorite"`
	TrackExtras
	Artists []Artist `json:"artists,omitempty"`
}

type Filters struct {
	Query, Genre, Sort, ArtistRole, Index string
	ArtistID, AlbumID                     int64
	Year, Limit, Offset                   int
	HideInstrumental, Favorite            bool
	favoriteOrder                         bool
}

func (s *Store) LibraryByRoot(ctx context.Context, root string) (Library, error) {
	var value Library
	err := s.db.QueryRowContext(ctx, `SELECT id, name, root_path FROM libraries WHERE root_path = ?`, root).Scan(&value.ID, &value.Name, &value.RootPath)
	if err != nil {
		return Library{}, fmt.Errorf("query library: %w", err)
	}
	return value, nil
}

func (s *Store) AudioFileUnchanged(ctx context.Context, libraryID int64, relativePath string, size, modified int64) (bool, error) {
	var unchanged bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audio_files af JOIN tracks t ON t.id=af.track_id WHERE af.library_id=? AND af.relative_path=? AND af.file_size=? AND af.modified_at_ns=? AND af.status='available' AND t.duration_ms IS NOT NULL)`, libraryID, relativePath, size, modified).Scan(&unchanged)
	return unchanged, err
}

const AudioProbeVersion = 1

func (s *Store) AudioProbeNeeded(ctx context.Context, libraryID int64, relativePath string) (bool, error) {
	var needed bool
	err := s.db.QueryRowContext(ctx, `SELECT audio_probe_version < ? FROM audio_files WHERE library_id=? AND relative_path=?`, AudioProbeVersion, libraryID, relativePath).Scan(&needed)
	return needed, err
}
func (s *Store) UpdateAudioProbe(ctx context.Context, libraryID int64, relativePath string, props metadata.AudioProps, lrc bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE audio_files SET codec=?,sample_rate=?,bit_depth=?,bitrate=?,channels=?,audio_probe_version=?,has_external_lrc=? WHERE library_id=? AND relative_path=?`, nullableString(props.Codec), nullableInt(props.SampleRate), nullableInt(props.BitDepth), nullableInt(props.BitrateKbps), nullableInt(props.Channels), AudioProbeVersion, boolInt(lrc), libraryID, relativePath)
	return err
}
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func (s *Store) TouchAudioFile(ctx context.Context, libraryID int64, relativePath string, lrc bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE audio_files SET status='available', has_external_lrc=?,last_scanned_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE library_id=? AND relative_path=?`, boolInt(lrc), libraryID, relativePath)
	return err
}

func (s *Store) CreateScanJob(ctx context.Context, libraryID int64, scanType string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO scan_jobs(library_id,scan_type,status) VALUES(?,?,'queued')`, libraryID, scanType)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) StartScanJob(ctx context.Context, id, discovered int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scan_jobs SET status='running', discovered_files=?, started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, discovered, id)
	return err
}

func (s *Store) UpdateScanJob(ctx context.Context, id, processed, skipped, failed int64, current string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scan_jobs SET processed_files=?,skipped_files=?,failed_files=?,current_relative_path=? WHERE id=?`, processed, skipped, failed, current, id)
	return err
}

func (s *Store) AddScanError(ctx context.Context, id int64, relative, code, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO scan_errors(scan_job_id,relative_path,error_code,message) VALUES(?,?,?,?)`, id, relative, code, message)
	return err
}

func (s *Store) FinishScanJob(ctx context.Context, id int64, status string, missing int64, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scan_jobs SET status=?,missing_files=?,error_message=NULLIF(?,''),current_relative_path=NULL,finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, status, missing, message, id)
	return err
}

func (s *Store) LatestScanJob(ctx context.Context) (ScanJob, error) {
	var j ScanJob
	var current, message, created, started, finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,library_id,scan_type,status,discovered_files,processed_files,skipped_files,failed_files,missing_files,current_relative_path,error_message,created_at,started_at,finished_at FROM scan_jobs ORDER BY id DESC LIMIT 1`).Scan(&j.ID, &j.LibraryID, &j.ScanType, &j.Status, &j.DiscoveredFiles, &j.ProcessedFiles, &j.SkippedFiles, &j.FailedFiles, &j.MissingFiles, &current, &message, &created, &started, &finished)
	if err != nil {
		return j, err
	}
	j.CurrentRelativePath = current.String
	j.ErrorMessage = message.String
	j.CreatedAt = created.String
	j.StartedAt = started.String
	j.FinishedAt = finished.String
	return j, nil
}

func (s *Store) ImportTrack(ctx context.Context, input ImportInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m := input.Metadata
	groupDir := albumFolder(input.RelativePath)
	groupKey := metadata.Normalize(groupDir) + "|" + metadata.Normalize(m.Album) + "|" + strconv.Itoa(m.Year)
	discCount := m.DiscTotal
	if discCount < m.DiscNumber {
		discCount = m.DiscNumber
	}
	if discCount < 1 {
		discCount = 1
	}
	performedBy := strings.Join(m.AlbumArtists, ", ")
	label := rawFirst(m.Raw, "LABEL", "ORGANIZATION", "PUBLISHER")
	catalog := rawFirst(m.Raw, "CATALOGNUMBER", "CATALOG NUMBER", "CATALOGID")
	version := rawFirst(m.Raw, "VERSION", "ALBUMVERSION", "EDITION")
	country := rawFirst(m.Raw, "RELEASECOUNTRY", "COUNTRY")
	releaseDate := rawFirst(m.Raw, "DATE", "RELEASEDATE")
	originalDate := rawFirst(m.Raw, "ORIGINALDATE", "ORIGINALYEAR")
	albumType := inferAlbumType(m.Raw)
	_, err = tx.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key,release_year,disc_count,performed_by,album_type,version,release_date,original_release_date,label,catalog_number,country,is_compilation,is_live,is_bootleg) VALUES(?,?,?,?,NULLIF(?,0),?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?) ON CONFLICT(library_id,grouping_key) DO UPDATE SET title=excluded.title,sort_title=excluded.sort_title,release_year=excluded.release_year,disc_count=MAX(albums.disc_count,excluded.disc_count),performed_by=excluded.performed_by,album_type=excluded.album_type,version=COALESCE(excluded.version,albums.version),release_date=COALESCE(excluded.release_date,albums.release_date),original_release_date=COALESCE(excluded.original_release_date,albums.original_release_date),label=COALESCE(excluded.label,albums.label),catalog_number=COALESCE(excluded.catalog_number,albums.catalog_number),country=COALESCE(excluded.country,albums.country),is_compilation=excluded.is_compilation,is_live=excluded.is_live,is_bootleg=excluded.is_bootleg,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, input.LibraryID, m.Album, metadata.Normalize(m.Album), groupKey, m.Year, discCount, performedBy, albumType, version, releaseDate, originalDate, label, catalog, country, boolInt(albumType == "compilation"), boolInt(albumType == "live"), boolInt(albumType == "bootleg"))
	if err != nil {
		return fmt.Errorf("upsert album: %w", err)
	}
	var albumID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM albums WHERE library_id=? AND grouping_key=?`, input.LibraryID, groupKey).Scan(&albumID); err != nil {
		return err
	}

	albumArtistIDs, err := ensureArtists(ctx, tx, m.AlbumArtists)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM album_artists WHERE album_id=?`, albumID); err != nil {
		return err
	}
	for i, id := range albumArtistIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,?)`, albumID, id, i); err != nil {
			return err
		}
	}

	// Set artist reading_name from sort tags (only when single artist to avoid misattribution)
	if m.AlbumArtistSort != "" && len(albumArtistIDs) == 1 {
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET reading_name=? WHERE id=? AND reading_name IS NULL`, m.AlbumArtistSort, albumArtistIDs[0]); err != nil {
			return err
		}
	}
	if m.ArtistSort != "" && len(m.Artists) == 1 {
		trackArtistIDs, taErr := ensureArtists(ctx, tx, m.Artists)
		if taErr == nil && len(trackArtistIDs) == 1 {
			if _, err = tx.ExecContext(ctx, `UPDATE artists SET reading_name=? WHERE id=? AND reading_name IS NULL`, m.ArtistSort, trackArtistIDs[0]); err != nil {
				return err
			}
		}
	}

	// Derive sort/reading keys
	trackSortTitle := metadata.Normalize(m.Title)
	trackReadingTitle := m.TitleSort // from TITLESORT tag (furigana/romaji)
	albumReadingTitle := m.AlbumSort // from ALBUMSORT tag

	var trackID int64
	err = tx.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE library_id=? AND relative_path=?`, input.LibraryID, input.RelativePath).Scan(&trackID)
	if err == sql.ErrNoRows || trackID == 0 {
		result, e := tx.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,reading_title,disc_number,track_number,duration_ms,release_date,composer,lyricist,arranger,track_type,lyrics) VALUES(?,?,?,NULLIF(?,''),?,?,?,NULLIF(?,''),?,?,?,?,?)`, albumID, m.Title, trackSortTitle, trackReadingTitle, m.DiscNumber, m.TrackNumber, m.DurationMillis, yearDate(m.Year), m.Composer, m.Lyricist, m.Arranger, m.TrackType, m.Lyrics)
		if e != nil {
			return e
		}
		trackID, _ = result.LastInsertId()
	} else if err != nil {
		return err
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE tracks SET album_id=?,title=?,sort_title=?,reading_title=NULLIF(?,''),disc_number=?,track_number=?,duration_ms=?,release_date=NULLIF(?,''),composer=?,lyricist=?,arranger=?,track_type=?,lyrics=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, albumID, m.Title, trackSortTitle, trackReadingTitle, m.DiscNumber, m.TrackNumber, m.DurationMillis, yearDate(m.Year), m.Composer, m.Lyricist, m.Arranger, m.TrackType, m.Lyrics, trackID)
		if err != nil {
			return err
		}
	}

	// Update album reading_title from sort tags
	if albumReadingTitle != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE albums SET reading_title=? WHERE id=? AND reading_title IS NULL`, albumReadingTitle, albumID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,container,mime_type,codec,sample_rate,bit_depth,bitrate,channels,audio_probe_version,has_external_lrc,status,last_scanned_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'available',strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(library_id,relative_path) DO UPDATE SET track_id=excluded.track_id,file_size=excluded.file_size,modified_at_ns=excluded.modified_at_ns,container=excluded.container,mime_type=excluded.mime_type,codec=excluded.codec,sample_rate=excluded.sample_rate,bit_depth=excluded.bit_depth,bitrate=excluded.bitrate,channels=excluded.channels,audio_probe_version=excluded.audio_probe_version,has_external_lrc=excluded.has_external_lrc,status='available',last_scanned_at=excluded.last_scanned_at,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, input.LibraryID, trackID, input.RelativePath, input.FileSize, input.ModifiedAtNS, m.Container, m.MIMEType, nullableString(input.AudioProps.Codec), nullableInt(input.AudioProps.SampleRate), nullableInt(input.AudioProps.BitDepth), nullableInt(input.AudioProps.BitrateKbps), nullableInt(input.AudioProps.Channels), AudioProbeVersion, boolInt(input.HasExternalLRC))
	if err != nil {
		return err
	}
	audioID, _ := result.LastInsertId()
	if audioID == 0 {
		if err = tx.QueryRowContext(ctx, `SELECT id FROM audio_files WHERE library_id=? AND relative_path=?`, input.LibraryID, input.RelativePath).Scan(&audioID); err != nil {
			return err
		}
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM track_artists WHERE track_id=?`, trackID); err != nil {
		return err
	}
	// Primary performing artists
	artistIDs, err := ensureArtists(ctx, tx, m.Artists)
	if err != nil {
		return err
	}
	for i, id := range artistIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,?,?)`, trackID, id, i, "primary"); err != nil {
			return err
		}
	}
	// Credits: lyricist, composer, arranger, producer (each as separate roles)
	type creditEntry struct {
		role  string
		names string
	}
	credits := []creditEntry{
		{"composer", m.Composer},
		{"lyricist", m.Lyricist},
		{"arranger", m.Arranger},
		{"producer", m.Producer},
	}
	for _, credit := range credits {
		if credit.names == "" {
			continue
		}
		creditNames := metadata.SplitPeople(credit.names)
		creditIDs, cErr := ensureArtists(ctx, tx, creditNames)
		if cErr != nil {
			return cErr
		}
		for i, id := range creditIDs {
			// Use INSERT OR IGNORE to handle same artist appearing in multiple credit roles.
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,?,?)`, trackID, id, i, credit.role); err != nil {
				return err
			}
		}
	}
	// Replace scanner-owned structured involved-people credits while retaining
	// remote/manual source rows. Rematerializing all sources after the scanner's
	// track_artists rebuild keeps VGMdb/MusicBrainz credits across rescans.
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_artist_sources WHERE track_id=? AND source='file_tag'`, trackID); err != nil {
		return err
	}
	involvedPositions := make(map[string]int)
	involvedSeen := make(map[string]struct{})
	for _, person := range m.InvolvedPeople {
		name := strings.TrimSpace(person.Name)
		role := NormalizeTrackArtistRole(person.Role)
		key := metadata.Normalize(name)
		if name == "" || role == "" || key == "" {
			continue
		}
		duplicateKey := role + "\x00" + key
		if _, ok := involvedSeen[duplicateKey]; ok {
			continue
		}
		involvedSeen[duplicateKey] = struct{}{}
		ids, involvedErr := ensureArtists(ctx, tx, []string{name})
		if involvedErr != nil {
			return involvedErr
		}
		if len(ids) == 0 {
			continue
		}
		position := involvedPositions[role]
		involvedPositions[role]++
		if err = ensureTrackArtistSource(ctx, tx, trackID, ids[0], position, role, "", "file_tag", "", 0); err != nil {
			return err
		}
	}
	if err = materializeTrackArtistSources(ctx, tx, trackID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_genres WHERE track_id=?`, trackID); err != nil {
		return err
	}
	for i, name := range m.Genres {
		if _, err = tx.ExecContext(ctx, `INSERT INTO genres(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO track_genres(track_id,genre_id,position) SELECT ?,id,? FROM genres WHERE name=? COLLATE NOCASE`, trackID, i, name); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM audio_file_tags WHERE audio_file_id=?`, audioID); err != nil {
		return err
	}
	for field, values := range m.Raw {
		for i, value := range values {
			if value == "" {
				continue
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) VALUES(?,?,?,?)`, audioID, field, value, i); err != nil {
				return err
			}
		}
	}
	if input.Artwork != nil {
		a := input.Artwork
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artworks(album_id,track_id,source_type,source_path,content_hash,mime_type,byte_size,is_primary) VALUES(?,NULL,?,?,?,?,?,CASE WHEN EXISTS(SELECT 1 FROM artworks WHERE album_id=? AND is_primary=1) THEN 0 ELSE 1 END)`, albumID, a.SourceType, a.CachePath, a.Hash, a.MIMEType, a.ByteSize, albumID); err != nil {
			return err
		}
	}
	// Rebuild only scanner-created associations. Manual links are preserved.
	if _, err = tx.ExecContext(ctx, `DELETE FROM work_tracks WHERE track_id=? AND source='auto'`, trackID); err != nil {
		return err
	}
	for _, association := range metadata.InferWorkAssociations(m.Title, m.Album, input.RelativePath, m.Raw) {
		if err = ensureAutoWorkAssociation(ctx, tx, trackID, association); err != nil {
			return fmt.Errorf("infer work association: %w", err)
		}
	}
	return tx.Commit()
}

func ensureArtists(ctx context.Context, tx *sql.Tx, names []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		key := metadata.Normalize(name)
		if key == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?) ON CONFLICT(identity_key) DO NOTHING`, name, key, key)
		if err != nil {
			return nil, err
		}
		var id int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key=?`, key).Scan(&id); err != nil {
			return nil, err
		}
		id, err = canonicalArtistID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Store) CountAvailableAudioFiles(ctx context.Context, libraryID int64) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_files WHERE library_id=? AND status='available'`, libraryID).Scan(&count)
	return count, err
}

func (s *Store) MarkMissing(ctx context.Context, libraryID int64, scanStarted string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE audio_files SET status='missing',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE library_id=? AND (last_scanned_at IS NULL OR last_scanned_at < ?) AND status='available'`, libraryID, scanStarted)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CleanupOrphans removes albums/artists/genres that lost all references.
// Tracks are deliberately NEVER deleted here: a track whose files are all
// 'missing' still carries client data (favorites, play counts, playlist
// memberships, playback progress) that must survive remounts and file
// reorganisation. The scanner guards reconciliation with plausibility
// thresholds before MarkMissing runs.
func (s *Store) CleanupOrphans(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM albums WHERE NOT EXISTS(SELECT 1 FROM tracks t WHERE t.album_id=albums.id); DELETE FROM artists WHERE merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=artists.id) AND NOT EXISTS(SELECT 1 FROM track_artists ta WHERE ta.artist_id=artists.id); DELETE FROM genres WHERE NOT EXISTS(SELECT 1 FROM track_genres tg WHERE tg.genre_id=genres.id) AND NOT EXISTS(SELECT 1 FROM track_genre_overrides tgo WHERE tgo.genre_id=genres.id) AND NOT EXISTS(SELECT 1 FROM album_genre_overrides ago WHERE ago.genre_id=genres.id);`)
	return err
}

func (s *Store) ListArtists(ctx context.Context, f Filters) ([]Artist, error) {
	limit, offset := page(f)
	role := normalizeArtistRole(f.ArtistRole)
	order := "name COLLATE NOCASE"
	if f.Sort == "albums" {
		order = "album_count DESC, name COLLATE NOCASE"
	} else if f.Sort == "tracks" {
		order = "track_count DESC, name COLLATE NOCASE"
	} else if f.favoriteOrder {
		order = "ar.favorited_at DESC, ar.id DESC"
	}
	where, args := artistWhere(f, role)
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name) name,CASE WHEN EXISTS(SELECT 1 FROM artist_image_cache ai WHERE ai.artist_id=ar.id OR ai.artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=ar.id)) THEN '/api/v1/artists/'||ar.id||'/image' ELSE '' END,(SELECT COUNT(DISTINCT aa.album_id) FROM album_artists aa WHERE aa.artist_id=ar.id) album_count,(SELECT COUNT(DISTINCT ta.track_id) FROM track_artists ta WHERE ta.artist_id=ar.id) track_count,ar.is_favorite FROM artists ar WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]Artist, 0)
	for rows.Next() {
		var v Artist
		var favorite int
		if err = rows.Scan(&v.ID, &v.Name, &v.ImageURL, &v.AlbumCount, &v.TrackCount, &favorite); err != nil {
			return nil, err
		}
		v.IsFavorite = favorite != 0
		list = append(list, v)
	}
	return list, rows.Err()
}

// ListAlbums runs a two-stage paginated browse: stage one resolves the page
// of album ids with the shared albumWhere predicate and a stable ORDER BY,
// stage two hydrates display fields in batch. This keeps list and count
// semantics identical and avoids per-row correlated subqueries over the
// whole table before pagination.
func (s *Store) ListAlbums(ctx context.Context, f Filters) ([]Album, error) {
	limit, offset := page(f)
	where, args := albumWhere(f)
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT a.id FROM albums a WHERE `+where+` ORDER BY `+albumOrder(f)+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(ids) == 0 {
		return []Album{}, nil
	}
	return s.hydrateAlbums(ctx, ids)
}

func (s *Store) CountArtists(ctx context.Context, f Filters) (int64, error) {
	var total int64
	role := normalizeArtistRole(f.ArtistRole)
	where, args := artistWhere(f, role)
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists ar WHERE `+where, args...).Scan(&total)
	return total, err
}

func artistWhere(f Filters, role string) (string, []any) {
	clauses := []string{"ar.merged_into_artist_id IS NULL", "(?='all' OR (?='album' AND EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=ar.id)) OR (?='track' AND EXISTS(SELECT 1 FROM track_artists ta WHERE ta.artist_id=ar.id)))"}
	args := []any{role, role, role}
	if f.Favorite {
		clauses = append(clauses, "ar.is_favorite=1")
	}
	variants := SearchVariants(f.Query)
	if len(variants) > 0 {
		parts := make([]string, 0, len(variants)*2)
		for _, variant := range variants {
			parts = append(parts, "COALESCE(ar.user_display_name,ar.display_name) LIKE '%'||?||'%'", "COALESCE(ar.reading_name,'') LIKE '%'||?||'%'")
			args = append(args, variant, variant)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if condition, indexArgs := IndexCondition("COALESCE(NULLIF(ar.reading_name,''),ar.sort_name,COALESCE(ar.user_display_name,ar.display_name))", f.Index); condition != "" {
		clauses = append(clauses, condition)
		args = append(args, indexArgs...)
	}
	return strings.Join(clauses, " AND "), args
}

func normalizeArtistRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "album":
		return "album"
	case "track":
		return "track"
	default:
		return "all"
	}
}

func (s *Store) CountAlbums(ctx context.Context, f Filters) (int64, error) {
	var total int64
	where, args := albumWhere(f)
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM albums a WHERE `+where, args...).Scan(&total)
	return total, err
}

func (s *Store) CountTracks(ctx context.Context, f Filters) (int64, error) {
	var total int64
	where, args := trackWhere(f)
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks t JOIN albums a ON a.id=t.album_id WHERE `+where, args...).Scan(&total)
	return total, err
}

const albumByIDSelect = `SELECT
	a.id, COALESCE(a.user_title,a.title),
	COALESCE(a.user_performed_by,a.performed_by,(SELECT GROUP_CONCAT(COALESCE(ar.user_display_name,ar.display_name),', ') FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id),'Unknown Artist'),
	COALESCE(a.user_release_year,a.release_year,0), a.disc_count,
	(SELECT COUNT(*) FROM tracks t WHERE t.album_id=a.id),
	COALESCE((SELECT GROUP_CONCAT(g.name,',' ORDER BY ago.position) FROM album_genre_overrides ago JOIN genres g ON g.id=ago.genre_id WHERE ago.album_id=a.id),(SELECT GROUP_CONCAT(name,',' ORDER BY gid) FROM (SELECT t.album_id, g.id AS gid, g.name FROM tracks t JOIN track_genre_overrides ox ON ox.track_id=t.id JOIN genres g ON g.id=ox.genre_id WHERE t.album_id=a.id UNION SELECT t.album_id, g.id, g.name FROM tracks t JOIN track_genres tg ON tg.track_id=t.id JOIN genres g ON g.id=tg.genre_id WHERE t.album_id=a.id AND NOT EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=t.id))),''),
	COALESCE((SELECT '/api/v1/artwork/'||id FROM artworks aw WHERE aw.album_id=a.id ORDER BY is_primary DESC,id LIMIT 1),''),
	COALESCE(a.user_album_type,a.album_type,'album'), COALESCE(a.user_version,a.version,''),
	COALESCE(a.user_release_date,a.release_date,''), COALESCE(a.user_original_release_date,a.original_release_date,''),
	COALESCE(a.user_label,a.label,''), COALESCE(a.user_catalog_number,a.catalog_number,''), COALESCE(a.user_country,a.country,''), COALESCE(a.user_review,a.review,''),
	COALESCE(a.user_is_compilation,a.is_compilation,0), COALESCE(a.user_is_live,a.is_live,0), COALESCE(a.user_is_bootleg,a.is_bootleg,0),
	COALESCE((SELECT GROUP_CONCAT(DISTINCT UPPER(af.container)) FROM tracks t JOIN audio_files af ON af.track_id=t.id AND af.status='available' WHERE t.album_id=a.id),''),
	COALESCE((SELECT SUM(af.file_size) FROM tracks t JOIN audio_files af ON af.track_id=t.id AND af.status='available' WHERE t.album_id=a.id),0),
	a.added_at,a.updated_at,a.is_favorite,COALESCE((SELECT MAX(pp.last_played_at) FROM tracks pt JOIN playback_progress pp ON pp.track_id=pt.id WHERE pt.album_id=a.id),'')
	FROM albums a`

func scanAlbum(row interface{ Scan(...any) error }) (Album, error) {
	var a Album
	var compilation, live, bootleg, favorite int
	err := row.Scan(&a.ID, &a.Title, &a.PerformedBy, &a.Year, &a.DiscCount, &a.TrackCount, &a.Genres, &a.ArtworkURL, &a.AlbumType, &a.Version, &a.ReleaseDate, &a.OriginalReleaseDate, &a.Label, &a.CatalogNumber, &a.Country, &a.Review, &compilation, &live, &bootleg, &a.Formats, &a.TotalBytes, &a.AddedAt, &a.UpdatedAt, &favorite, &a.LastPlayedAt)
	a.Artist = a.PerformedBy
	a.Compilation = compilation != 0
	a.Live = live != 0
	a.Bootleg = bootleg != 0
	a.IsFavorite = favorite != 0
	a.TotalSize = formatBytes(a.TotalBytes)
	return a, err
}

func (s *Store) AlbumByID(ctx context.Context, id int64) (Album, error) {
	return scanAlbum(s.db.QueryRowContext(ctx, albumByIDSelect+` WHERE a.id=?`, id))
}

// albumsByIDs loads many albums in a single query (N+1 fix) and returns them
// in the order of the given ids. A missing id yields sql.ErrNoRows.
func (s *Store) albumsByIDs(ctx context.Context, ids []int64) ([]Album, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx, albumByIDSelect+` WHERE a.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]Album{}
	for rows.Next() {
		album, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		byID[album.ID] = album
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Album, 0, len(ids))
	for _, id := range ids {
		album, ok := byID[id]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result = append(result, album)
	}
	return result, nil
}

func inClause(ids []int64) (string, []any) {
	placeholders := make([]byte, 0, len(ids)*2)
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}
	return string(placeholders), args
}

func (s *Store) ArtistsForAlbum(ctx context.Context, albumID int64) ([]Artist, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),CASE WHEN EXISTS(SELECT 1 FROM artist_image_cache ai WHERE ai.artist_id=ar.id OR ai.artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=ar.id)) THEN '/api/v1/artists/'||ar.id||'/image' ELSE '' END,(SELECT COUNT(DISTINCT album_id) FROM album_artists WHERE artist_id=ar.id),(SELECT COUNT(DISTINCT track_id) FROM track_artists WHERE artist_id=ar.id) FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=? AND ar.merged_into_artist_id IS NULL ORDER BY aa.position,ar.id`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	artists := make([]Artist, 0)
	for rows.Next() {
		var artist Artist
		if err = rows.Scan(&artist.ID, &artist.Name, &artist.ImageURL, &artist.AlbumCount, &artist.TrackCount); err != nil {
			return nil, err
		}
		artists = append(artists, artist)
	}
	return artists, rows.Err()
}

func (s *Store) ArtistsForAlbumTracks(ctx context.Context, albumID int64) (map[int64][]Artist, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ta.track_id,ar.id,COALESCE(ar.user_display_name,ar.display_name),CASE WHEN EXISTS(SELECT 1 FROM artist_image_cache ai WHERE ai.artist_id=ar.id OR ai.artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=ar.id)) THEN '/api/v1/artists/'||ar.id||'/image' ELSE '' END,(SELECT COUNT(DISTINCT album_id) FROM album_artists WHERE artist_id=ar.id),(SELECT COUNT(DISTINCT track_id) FROM track_artists WHERE artist_id=ar.id) FROM tracks t JOIN track_artists ta ON ta.track_id=t.id JOIN artists ar ON ar.id=ta.artist_id WHERE t.album_id=? AND ar.merged_into_artist_id IS NULL ORDER BY ta.track_id,ta.position,ar.id`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64][]Artist{}
	for rows.Next() {
		var trackID int64
		var artist Artist
		if err = rows.Scan(&trackID, &artist.ID, &artist.Name, &artist.ImageURL, &artist.AlbumCount, &artist.TrackCount); err != nil {
			return nil, err
		}
		result[trackID] = append(result[trackID], artist)
	}
	return result, rows.Err()
}

func (s *Store) ListTracks(ctx context.Context, f Filters) ([]Track, error) {
	limit, offset := page(f)
	order := "COALESCE(a.user_title,a.title),COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),t.id"
	if f.Sort == "title" {
		order = "COALESCE(t.user_title,t.title) COLLATE NOCASE,t.id"
	} else if f.Sort == "year" {
		order = "COALESCE(a.user_release_year,a.release_year,0) DESC," + order
	} else if f.Sort == "recentlyPlayed" {
		order = "COALESCE(pp.last_played_at,'') DESC," + order
	}
	where, args := trackWhere(f)
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,a.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),`+trackArtistSQL+`,COALESCE(a.user_release_year,a.release_year,0),COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),COALESCE(t.user_composer,t.composer,''),COALESCE(t.lyricist,''),COALESCE(t.arranger,''),COALESCE(t.user_track_type,t.track_type,'regular'),COALESCE((SELECT GROUP_CONCAT(gx.name,',' ORDER BY ox.position, gx.id) FROM track_genre_overrides ox JOIN genres gx ON gx.id=ox.genre_id WHERE ox.track_id=t.id),(SELECT GROUP_CONCAT(gx.name,',' ORDER BY rx.position, gx.id) FROM track_genres rx JOIN genres gx ON gx.id=rx.genre_id WHERE rx.track_id=t.id),''),COALESCE(af.container,''),COALESCE(af.mime_type,''),COALESCE(af.relative_path,''),COALESCE(af.file_size,0),CASE WHEN aw.id IS NULL THEN '' ELSE '/api/v1/artwork/'||aw.id END,COALESCE(t.duration_ms,0),'/api/v1/tracks/'||t.id||'/stream',t.added_at,t.updated_at,t.is_favorite,COALESCE(pp.last_played_at,''),COALESCE(pp.position_ms,0),COALESCE(pp.play_count,0) FROM tracks t JOIN albums a ON a.id=t.album_id LEFT JOIN audio_files af ON af.track_id=t.id AND af.status='available' LEFT JOIN artworks aw ON aw.album_id=a.id AND aw.is_primary=1 LEFT JOIN playback_progress pp ON pp.track_id=t.id WHERE `+where+` GROUP BY t.id ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]Track, 0)
	for rows.Next() {
		var v Track
		var favorite int
		if err = rows.Scan(&v.ID, &v.AlbumID, &v.Title, &v.Album, &v.Artist, &v.Year, &v.DiscNumber, &v.TrackNumber, &v.Composer, &v.Lyricist, &v.Arranger, &v.TrackType, &v.Genres, &v.Container, &v.MIMEType, &v.RelativePath, &v.FileSize, &v.ArtworkURL, &v.DurationMillis, &v.StreamURL, &v.AddedAt, &v.UpdatedAt, &favorite, &v.LastPlayedAt, &v.PositionMillis, &v.PlayCount); err != nil {
			return nil, err
		}
		v.IsFavorite = favorite != 0
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return list, s.hydrateTracks(ctx, list)
}
func (s *Store) Genres(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM genres ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []string
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}
func (s *Store) Years(ctx context.Context) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(user_release_year,release_year) FROM albums WHERE COALESCE(user_release_year,release_year) IS NOT NULL ORDER BY 1 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []int
	for rows.Next() {
		var v int
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func (s *Store) UpdateArtist(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE artists SET user_display_name=NULLIF(?,''),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, strings.TrimSpace(name), id)
	return err
}
func (s *Store) UpdateAlbum(ctx context.Context, id int64, edit AlbumEdit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE albums SET user_title=NULLIF(?,''),user_performed_by=NULLIF(?,''),user_album_type=NULLIF(?,''),user_version=NULLIF(?,''),user_release_year=NULLIF(?,0),user_release_date=NULLIF(?,''),user_original_release_date=NULLIF(?,''),user_label=NULLIF(?,''),user_catalog_number=NULLIF(?,''),user_country=NULLIF(?,''),user_review=NULLIF(?,''),user_is_compilation=?,user_is_live=?,user_is_bootleg=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, strings.TrimSpace(edit.Title), strings.TrimSpace(edit.PerformedBy), strings.TrimSpace(edit.AlbumType), strings.TrimSpace(edit.Version), edit.Year, strings.TrimSpace(edit.ReleaseDate), strings.TrimSpace(edit.OriginalReleaseDate), strings.TrimSpace(edit.Label), strings.TrimSpace(edit.CatalogNumber), strings.TrimSpace(edit.Country), strings.TrimSpace(edit.Review), boolInt(edit.Compilation), boolInt(edit.Live), boolInt(edit.Bootleg), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM album_genre_overrides WHERE album_id=?`, id); err != nil {
		return err
	}
	for position, name := range edit.Genres {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO genres(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_genre_overrides(album_id,genre_id,position) SELECT ?,id,? FROM genres WHERE name=? COLLATE NOCASE`, id, position, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) UpdateTrack(ctx context.Context, id int64, title string, disc, number int, composer, trackType string, genres []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if disc < 1 {
		disc = 1
	}
	if number < 0 {
		number = 0
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tracks SET user_title=NULLIF(?,''),user_disc_number=CASE WHEN ?=disc_number THEN NULL ELSE ? END,user_track_number=CASE WHEN ?=track_number THEN NULL ELSE ? END,user_composer=NULLIF(?,''),user_track_type=NULLIF(?,''),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, strings.TrimSpace(title), disc, disc, number, number, strings.TrimSpace(composer), strings.TrimSpace(trackType), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_genre_overrides WHERE track_id=?`, id); err != nil {
		return err
	}
	for i, name := range genres {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO genres(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_genre_overrides(track_id,genre_id,position) SELECT ?,id,? FROM genres WHERE name=? COLLATE NOCASE`, id, i, name); err != nil {
			return err
		}
	}
	// Orphan cleanup must consider all three genre references: raw track tags,
	// track-level overrides AND album-level overrides. Otherwise editing a
	// track would delete a genre only referenced by an album override and
	// cascade-delete that override.
	if _, err = tx.ExecContext(ctx, `DELETE FROM genres WHERE NOT EXISTS(SELECT 1 FROM track_genres raw WHERE raw.genre_id=genres.id) AND NOT EXISTS(SELECT 1 FROM track_genre_overrides user WHERE user.genre_id=genres.id) AND NOT EXISTS(SELECT 1 FROM album_genre_overrides ago WHERE ago.genre_id=genres.id)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AudioPath(ctx context.Context, trackID int64) (string, string, error) {
	var root, rel, mime string
	err := s.db.QueryRowContext(ctx, `SELECT l.root_path,af.relative_path,COALESCE(af.mime_type,'application/octet-stream') FROM audio_files af JOIN libraries l ON l.id=af.library_id WHERE af.track_id=? AND af.status='available' ORDER BY af.id LIMIT 1`, trackID).Scan(&root, &rel, &mime)
	return filepath.Join(root, filepath.FromSlash(rel)), mime, err
}

// AudioFilePath returns the full filesystem path of the audio file for a track.
func (s *Store) AudioFilePath(ctx context.Context, trackID int64) (string, error) {
	var root, rel string
	err := s.db.QueryRowContext(ctx, `SELECT l.root_path,af.relative_path FROM audio_files af JOIN libraries l ON l.id=af.library_id WHERE af.track_id=? AND af.status='available' ORDER BY af.id LIMIT 1`, trackID).Scan(&root, &rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

// TrackLyrics returns the embedded lyrics text stored in the tracks table.
func (s *Store) TrackLyrics(ctx context.Context, trackID int64) (string, error) {
	var lyrics sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT lyrics FROM tracks WHERE id=?`, trackID).Scan(&lyrics)
	if err != nil {
		return "", err
	}
	return lyrics.String, nil
}
func (s *Store) ArtworkPath(ctx context.Context, id int64) (string, string, error) {
	var path, mime string
	err := s.db.QueryRowContext(ctx, `SELECT source_path,mime_type FROM artworks WHERE id=?`, id).Scan(&path, &mime)
	return path, mime, err
}

func page(f Filters) (int, int) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return limit, f.Offset
}
func yearDate(year int) string {
	if year <= 0 {
		return ""
	}
	return fmt.Sprintf("%04d-01-01", year)
}
func rawFirst(raw map[string][]string, keys ...string) string {
	for _, key := range keys {
		if values := raw[key]; len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}
func inferAlbumType(raw map[string][]string) string {
	value := strings.ToLower(rawFirst(raw, "RELEASETYPE", "MUSICBRAINZ_ALBUMTYPE", "ALBUMTYPE"))
	for _, kind := range []string{"single", "ep", "soundtrack", "compilation", "live", "bootleg"} {
		if strings.Contains(value, kind) {
			return kind
		}
	}
	return "album"
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}
func albumFolder(relative string) string {
	dir := filepath.ToSlash(filepath.Dir(relative))
	base := strings.ToLower(filepath.Base(dir))
	for _, prefix := range []string{"cd", "disc", "disk"} {
		tail := strings.TrimSpace(strings.TrimPrefix(base, prefix))
		tail = strings.TrimLeft(tail, " _-")
		if tail != "" {
			if _, err := strconv.Atoi(tail); err == nil {
				return filepath.ToSlash(filepath.Dir(dir))
			}
		}
	}
	return dir
}
func ScanTimestamp() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
