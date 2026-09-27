package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type MetadataSourceSetting struct {
	Source, APIKey, Language, ApplicationName, ApplicationVersion, Contact string
	Enabled, AutoMatch                                                     bool
	Priority, CacheDays                                                    int
	HasAPIKey                                                              bool
}

type ArtistMatchInput struct {
	ID               int64
	Name, TaggedMBID string
}

type ArtistCandidate struct {
	ID, ArtistID                                                                                 int64
	Source, ExternalID, DisplayName, SortName, Disambiguation, Country, ArtistType, MBID, Status string
	Score                                                                                        int
	Evidence                                                                                     []string
	Payload                                                                                      json.RawMessage
}

type ExternalArtistProfile struct {
	Source, ExternalID, DisplayName, SortName, PageURL, RemoteImageURL, Biography, Country, ArtistType, Disambiguation string
	Aliases, Tags                                                                                                      []string
	Raw                                                                                                                json.RawMessage
	FetchedAt                                                                                                          string
}

type ArtistImageInput struct {
	ArtistID, ByteSize                           int64
	Source, RemoteURL, Hash, MIMEType, CachePath string
}

type ArtistDetail struct {
	Artist
	Biography, BiographySource, BiographyLanguage, BiographyURL, BiographyFetchedAt string
	Country, ArtistType                                                             string
	MergedIntoID                                                                    int64
	Aliases                                                                         []string
	Profiles                                                                        []ExternalArtistProfile
	Candidates                                                                      []ArtistCandidate
	Biographies                                                                     []ArtistBiography
}

type MergeOperation struct {
	ID, SourceArtistID, TargetArtistID                      int64
	Status, SourceName, TargetName, CreatedAt, RolledBackAt string
}

type ArtistMatchRun struct {
	ID                                        int64
	Status                                    string
	Total, Processed, Matched, Review, Failed int
	Current, ErrorMessage                     string
}

func (s *Store) ListArtistMatchRuns(ctx context.Context, limit int) ([]ArtistMatchRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,status,total_artists,processed_artists,matched_artists,review_artists,failed_artists,COALESCE(current_artist,''),COALESCE(error_message,'') FROM artist_match_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ArtistMatchRun
	for rows.Next() {
		var v ArtistMatchRun
		if err = rows.Scan(&v.ID, &v.Status, &v.Total, &v.Processed, &v.Matched, &v.Review, &v.Failed, &v.Current, &v.ErrorMessage); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// FailRunningArtistMatchRuns closes interrupted tasks on server startup.
func (s *Store) FailRunningArtistMatchRuns(ctx context.Context, message string) (int64, error) {
	if strings.TrimSpace(message) == "" {
		message = "artist matching interrupted by process restart"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE artist_match_runs SET status='failed',error_message=?,current_artist=NULL,finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status IN ('running','queued')`, message)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) CreateArtistMatchRun(ctx context.Context, total int) (int64, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO artist_match_runs(status,total_artists) VALUES('running',?)`, total)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
func (s *Store) UpdateArtistMatchRun(ctx context.Context, id int64, processed, matched, review, failed int, current string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE artist_match_runs SET processed_artists=?,matched_artists=?,review_artists=?,failed_artists=?,current_artist=? WHERE id=?`, processed, matched, review, failed, current, id)
	return err
}
func (s *Store) FinishArtistMatchRun(ctx context.Context, id int64, status, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE artist_match_runs SET status=?,error_message=NULLIF(?,''),current_artist=NULL,finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, status, message, id)
	return err
}

func (s *Store) MetadataSourceSettings(ctx context.Context) ([]MetadataSourceSetting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source,enabled,priority,COALESCE(api_key,''),language,application_name,application_version,COALESCE(contact,''),auto_match,cache_days FROM metadata_source_settings ORDER BY priority,source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []MetadataSourceSetting
	for rows.Next() {
		var v MetadataSourceSetting
		var enabled, auto int
		if err = rows.Scan(&v.Source, &enabled, &v.Priority, &v.APIKey, &v.Language, &v.ApplicationName, &v.ApplicationVersion, &v.Contact, &auto, &v.CacheDays); err != nil {
			return nil, err
		}
		v.Enabled = enabled != 0
		v.AutoMatch = auto != 0
		v.HasAPIKey = v.APIKey != ""
		list = append(list, v)
	}
	return list, rows.Err()
}

func (s *Store) MetadataSourceSetting(ctx context.Context, source string) (MetadataSourceSetting, error) {
	settings, err := s.MetadataSourceSettings(ctx)
	if err != nil {
		return MetadataSourceSetting{}, err
	}
	for _, value := range settings {
		if value.Source == source {
			return value, nil
		}
	}
	return MetadataSourceSetting{}, sql.ErrNoRows
}

func (s *Store) ArtistProfileFresh(ctx context.Context, artistID int64, source string, days int) (bool, error) {
	var fresh bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_external_profiles WHERE artist_id=? AND source=? AND fetched_at >= strftime('%Y-%m-%dT%H:%M:%fZ','now',?) AND image_checked_at IS NOT NULL)`, artistID, source, fmt.Sprintf("-%d days", days)).Scan(&fresh)
	return fresh, err
}

func (s *Store) ExternalProfileOwner(ctx context.Context, source, externalID string) (int64, error) {
	var artistID int64
	err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM artist_external_profiles WHERE source=? AND external_id=?`, source, externalID).Scan(&artistID)
	return artistID, err
}

func (s *Store) SaveMetadataSourceSetting(ctx context.Context, value MetadataSourceSetting) error {
	if value.Priority < 1 {
		value.Priority = 100
	}
	if value.CacheDays < 1 {
		value.CacheDays = 30
	}
	if value.Language == "" {
		value.Language = "zh"
	}
	if strings.EqualFold(value.Language, "jp") {
		value.Language = "ja"
	}
	if value.ApplicationName == "" {
		value.ApplicationName = "032 Music Server"
	}
	if value.ApplicationVersion == "" {
		value.ApplicationVersion = "dev"
	}
	if value.APIKey == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE metadata_source_settings SET enabled=?,priority=?,language=?,application_name=?,application_version=?,contact=?,auto_match=?,cache_days=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE source=?`, boolInt(value.Enabled), value.Priority, value.Language, value.ApplicationName, value.ApplicationVersion, value.Contact, boolInt(value.AutoMatch), value.CacheDays, value.Source)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE metadata_source_settings SET enabled=?,priority=?,api_key=?,language=?,application_name=?,application_version=?,contact=?,auto_match=?,cache_days=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE source=?`, boolInt(value.Enabled), value.Priority, value.APIKey, value.Language, value.ApplicationName, value.ApplicationVersion, value.Contact, boolInt(value.AutoMatch), value.CacheDays, value.Source)
	return err
}

func (s *Store) ArtistsForMatching(ctx context.Context) ([]ArtistMatchInput, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),COALESCE((SELECT aft.value FROM track_artists ta JOIN audio_files af ON af.track_id=ta.track_id JOIN audio_file_tags aft ON aft.audio_file_id=af.id WHERE ta.artist_id=ar.id AND aft.field_name IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID') LIMIT 1),'') FROM artists ar WHERE ar.merged_into_artist_id IS NULL ORDER BY ar.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ArtistMatchInput
	for rows.Next() {
		var v ArtistMatchInput
		if err = rows.Scan(&v.ID, &v.Name, &v.TaggedMBID); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func (s *Store) ArtistForMatching(ctx context.Context, id int64) (ArtistMatchInput, error) {
	var v ArtistMatchInput
	err := s.db.QueryRowContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),COALESCE((SELECT aft.value FROM track_artists ta JOIN audio_files af ON af.track_id=ta.track_id JOIN audio_file_tags aft ON aft.audio_file_id=af.id WHERE ta.artist_id=ar.id AND aft.field_name IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID') LIMIT 1),'') FROM artists ar WHERE ar.merged_into_artist_id IS NULL AND ar.id=?`, id).Scan(&v.ID, &v.Name, &v.TaggedMBID)
	return v, err
}

func (s *Store) ReplaceArtistCandidates(ctx context.Context, artistID int64, candidates []ArtistCandidate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_match_candidates WHERE artist_id=? AND status='candidate'`, artistID); err != nil {
		return err
	}
	for _, v := range candidates {
		evidence, _ := json.Marshal(v.Evidence)
		if len(v.Payload) == 0 {
			v.Payload = json.RawMessage(`{}`)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_match_candidates(artist_id,source,external_id,display_name,sort_name,disambiguation,country,artist_type,mbid,score,evidence_json,payload_json,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'candidate') ON CONFLICT(artist_id,source,external_id) DO UPDATE SET display_name=excluded.display_name,sort_name=excluded.sort_name,disambiguation=excluded.disambiguation,country=excluded.country,artist_type=excluded.artist_type,mbid=excluded.mbid,score=excluded.score,evidence_json=excluded.evidence_json,payload_json=excluded.payload_json,status=artist_match_candidates.status,created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, artistID, v.Source, v.ExternalID, v.DisplayName, v.SortName, v.Disambiguation, v.Country, v.ArtistType, v.MBID, v.Score, string(evidence), string(v.Payload)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ArtistCandidates(ctx context.Context, artistID int64) ([]ArtistCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,artist_id,source,external_id,display_name,COALESCE(sort_name,''),COALESCE(disambiguation,''),COALESCE(country,''),COALESCE(artist_type,''),COALESCE(mbid,''),score,evidence_json,payload_json,status FROM artist_match_candidates WHERE artist_id=? ORDER BY status='confirmed' DESC,score DESC,id`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ArtistCandidate
	for rows.Next() {
		var v ArtistCandidate
		var evidence, payload string
		if err = rows.Scan(&v.ID, &v.ArtistID, &v.Source, &v.ExternalID, &v.DisplayName, &v.SortName, &v.Disambiguation, &v.Country, &v.ArtistType, &v.MBID, &v.Score, &evidence, &payload, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &v.Evidence)
		v.Payload = json.RawMessage(payload)
		list = append(list, v)
	}
	return list, rows.Err()
}

// PendingArtistCandidates returns all status='candidate' matches for every
// artist in a single query (N+1 fix for the match-review page).
func (s *Store) PendingArtistCandidates(ctx context.Context) (map[int64][]ArtistCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,artist_id,source,external_id,display_name,COALESCE(sort_name,''),COALESCE(disambiguation,''),COALESCE(country,''),COALESCE(artist_type,''),COALESCE(mbid,''),score,evidence_json,payload_json,status FROM artist_match_candidates WHERE status='candidate' ORDER BY score DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int64][]ArtistCandidate{}
	for rows.Next() {
		var v ArtistCandidate
		var evidence, payload string
		if err = rows.Scan(&v.ID, &v.ArtistID, &v.Source, &v.ExternalID, &v.DisplayName, &v.SortName, &v.Disambiguation, &v.Country, &v.ArtistType, &v.MBID, &v.Score, &evidence, &payload, &v.Status); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &v.Evidence)
		v.Payload = json.RawMessage(payload)
		result[v.ArtistID] = append(result[v.ArtistID], v)
	}
	return result, rows.Err()
}

func (s *Store) UpsertExternalArtistProfile(ctx context.Context, artistID int64, p ExternalArtistProfile) error {
	aliases, _ := json.Marshal(p.Aliases)
	tags, _ := json.Marshal(p.Tags)
	if p.FetchedAt == "" {
		p.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if len(p.Raw) == 0 {
		p.Raw = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO artist_external_profiles(artist_id,source,external_id,display_name,sort_name,page_url,remote_image_url,image_checked_at,biography,country,artist_type,disambiguation,aliases_json,tags_json,raw_json,fetched_at) VALUES(?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),?,?,?,?,?,?,?,?) ON CONFLICT(artist_id,source) DO UPDATE SET external_id=excluded.external_id,display_name=excluded.display_name,sort_name=excluded.sort_name,page_url=excluded.page_url,remote_image_url=excluded.remote_image_url,image_checked_at=excluded.image_checked_at,biography=excluded.biography,country=excluded.country,artist_type=excluded.artist_type,disambiguation=excluded.disambiguation,aliases_json=excluded.aliases_json,tags_json=excluded.tags_json,raw_json=excluded.raw_json,fetched_at=excluded.fetched_at`, artistID, p.Source, p.ExternalID, p.DisplayName, p.SortName, p.PageURL, p.RemoteImageURL, p.Biography, p.Country, p.ArtistType, p.Disambiguation, string(aliases), string(tags), string(p.Raw), p.FetchedAt)
	return err
}

func (s *Store) ConfirmArtistCandidate(ctx context.Context, artistID, candidateID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var p ExternalArtistProfile
	var aliases, tags, raw string
	err = tx.QueryRowContext(ctx, `SELECT source,external_id,display_name,COALESCE(sort_name,''),COALESCE(json_extract(payload_json,'$.pageUrl'),''),COALESCE(json_extract(payload_json,'$.imageUrl'),''),COALESCE(json_extract(payload_json,'$.biography'),''),COALESCE(country,''),COALESCE(artist_type,''),COALESCE(disambiguation,''),COALESCE(json_extract(payload_json,'$.aliases'),'[]'),COALESCE(json_extract(payload_json,'$.tags'),'[]'),payload_json FROM artist_match_candidates WHERE id=? AND artist_id=?`, candidateID, artistID).Scan(&p.Source, &p.ExternalID, &p.DisplayName, &p.SortName, &p.PageURL, &p.RemoteImageURL, &p.Biography, &p.Country, &p.ArtistType, &p.Disambiguation, &aliases, &tags, &raw)
	if err != nil {
		return err
	}
	_ = json.Unmarshal([]byte(aliases), &p.Aliases)
	_ = json.Unmarshal([]byte(tags), &p.Tags)
	p.Raw = json.RawMessage(raw)
	p.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	aliasesJSON, _ := json.Marshal(p.Aliases)
	tagsJSON, _ := json.Marshal(p.Tags)
	if _, err = tx.ExecContext(ctx, `INSERT INTO artist_external_profiles(artist_id,source,external_id,display_name,sort_name,page_url,remote_image_url,image_checked_at,biography,country,artist_type,disambiguation,aliases_json,tags_json,raw_json,fetched_at) VALUES(?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),?,?,?,?,?,?,?,?) ON CONFLICT(artist_id,source) DO UPDATE SET external_id=excluded.external_id,display_name=excluded.display_name,sort_name=excluded.sort_name,page_url=excluded.page_url,remote_image_url=excluded.remote_image_url,image_checked_at=excluded.image_checked_at,biography=excluded.biography,country=excluded.country,artist_type=excluded.artist_type,disambiguation=excluded.disambiguation,aliases_json=excluded.aliases_json,tags_json=excluded.tags_json,raw_json=excluded.raw_json,fetched_at=excluded.fetched_at`, artistID, p.Source, p.ExternalID, p.DisplayName, p.SortName, p.PageURL, p.RemoteImageURL, p.Biography, p.Country, p.ArtistType, p.Disambiguation, string(aliasesJSON), string(tagsJSON), raw, p.FetchedAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_match_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END WHERE artist_id=?`, candidateID, artistID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RejectArtistCandidate(ctx context.Context, artistID, candidateID int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE artist_match_candidates SET status='rejected' WHERE id=? AND artist_id=? AND status='candidate'`, candidateID, artistID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) MarkArtistCandidatesConfirmed(ctx context.Context, artistID int64, externalID, mbid string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE artist_match_candidates SET status=CASE WHEN external_id=? OR (?<>'' AND mbid=?) THEN 'confirmed' ELSE 'rejected' END WHERE artist_id=? AND status='candidate'`, externalID, mbid, mbid, artistID)
	return err
}

func (s *Store) ArtistImageSource(ctx context.Context, artistID int64) (string, string, error) {
	var source, remoteURL string
	err := s.db.QueryRowContext(ctx, `SELECT p.source,p.remote_image_url FROM artist_external_profiles p JOIN metadata_source_settings s ON s.source=p.source WHERE p.artist_id=? AND COALESCE(p.remote_image_url,'')<>'' ORDER BY s.priority LIMIT 1`, artistID).Scan(&source, &remoteURL)
	return source, remoteURL, err
}

func (s *Store) ArtistExternalID(ctx context.Context, artistID int64, source string) (string, error) {
	var externalID string
	err := s.db.QueryRowContext(ctx, `SELECT external_id FROM artist_external_profiles WHERE artist_id=? AND source=?`, artistID, source).Scan(&externalID)
	return externalID, err
}

func (s *Store) ArtistImageCheckNeeded(ctx context.Context, artistID int64) (bool, error) {
	var needed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=? AND (p.image_checked_at IS NULL OR NOT EXISTS(SELECT 1 FROM artist_image_cache i WHERE i.artist_id=p.artist_id)))`, artistID).Scan(&needed)
	return needed, err
}

func (s *Store) SaveArtistImage(ctx context.Context, image ArtistImageInput) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO artist_image_cache(artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET source=excluded.source,remote_url=excluded.remote_url,content_hash=excluded.content_hash,mime_type=excluded.mime_type,cache_path=excluded.cache_path,byte_size=excluded.byte_size,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, image.ArtistID, image.Source, image.RemoteURL, image.Hash, image.MIMEType, image.CachePath, image.ByteSize)
	return err
}

func (s *Store) ArtistImagePath(ctx context.Context, artistID int64) (string, string, error) {
	var path, mimeType string
	err := s.db.QueryRowContext(ctx, `SELECT cache_path,mime_type FROM artist_image_cache WHERE artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?) ORDER BY artist_id=? DESC,fetched_at DESC LIMIT 1`, artistID, artistID, artistID).Scan(&path, &mimeType)
	return path, mimeType, err
}

func (s *Store) ArtistDetail(ctx context.Context, id int64) (ArtistDetail, error) {
	var d ArtistDetail
	var merged sql.NullInt64
	var hasLocalBiography bool
	var favorite int
	var preferredSource, preferredLanguage string
	err := s.db.QueryRowContext(ctx, `SELECT id,COALESCE(user_display_name,display_name),CASE WHEN EXISTS(SELECT 1 FROM artist_image_cache ai WHERE ai.artist_id=artists.id OR ai.artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=artists.id)) THEN '/api/v1/artists/'||artists.id||'/image' ELSE '' END,COALESCE(user_biography,biography,''),COALESCE(TRIM(user_biography),'')<>'',COALESCE(country,''),COALESCE(artist_type,''),merged_into_artist_id,is_favorite,(SELECT COUNT(DISTINCT album_id) FROM album_artists WHERE artist_id=artists.id),(SELECT COUNT(DISTINCT track_id) FROM track_artists WHERE artist_id=artists.id),COALESCE(preferred_biography_source,''),COALESCE(preferred_biography_language,'') FROM artists WHERE id=?`, id).Scan(&d.ID, &d.Name, &d.ImageURL, &d.Biography, &hasLocalBiography, &d.Country, &d.ArtistType, &merged, &favorite, &d.AlbumCount, &d.TrackCount, &preferredSource, &preferredLanguage)
	if err != nil {
		return d, err
	}
	d.MergedIntoID = merged.Int64
	d.IsFavorite = favorite != 0
	rows, err := s.db.QueryContext(ctx, `SELECT value FROM artist_names WHERE artist_id=? ORDER BY name_type,value`, id)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return d, err
		}
		d.Aliases = append(d.Aliases, v)
	}
	rows.Close()
	profiles, err := s.db.QueryContext(ctx, `SELECT p.source,p.external_id,p.display_name,COALESCE(p.sort_name,''),COALESCE(p.page_url,''),COALESCE(p.remote_image_url,''),COALESCE(p.biography,''),COALESCE(p.country,''),COALESCE(p.artist_type,''),COALESCE(p.disambiguation,''),p.aliases_json,p.tags_json,p.raw_json,p.fetched_at FROM artist_external_profiles p JOIN metadata_source_settings s ON s.source=p.source WHERE p.artist_id=? OR p.artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?) ORDER BY s.priority,p.artist_id=? DESC`, id, id, id)
	if err != nil {
		return d, err
	}
	for profiles.Next() {
		var p ExternalArtistProfile
		var aliases, tags, raw string
		if err = profiles.Scan(&p.Source, &p.ExternalID, &p.DisplayName, &p.SortName, &p.PageURL, &p.RemoteImageURL, &p.Biography, &p.Country, &p.ArtistType, &p.Disambiguation, &aliases, &tags, &raw, &p.FetchedAt); err != nil {
			profiles.Close()
			return d, err
		}
		_ = json.Unmarshal([]byte(aliases), &p.Aliases)
		_ = json.Unmarshal([]byte(tags), &p.Tags)
		p.Raw = json.RawMessage(raw)
		d.Profiles = append(d.Profiles, p)
	}
	profiles.Close()
	d.Candidates, _ = s.ArtistCandidates(ctx, id)
	d.Biographies, _ = s.ArtistBiographies(ctx, id)
	if hasLocalBiography {
		d.BiographySource = "local"
	} else if settings, settingErr := s.BiographySettings(ctx); settingErr == nil {
		if selected, ok := selectBiography(d.Biographies, preferredSource, preferredLanguage, settings); ok {
			d.Biography = selected.Biography
			d.BiographySource = selected.Source
			d.BiographyLanguage = selected.Language
			d.BiographyURL = selected.PageURL
			d.BiographyFetchedAt = selected.FetchedAt
			for index := range d.Biographies {
				d.Biographies[index].Selected = d.Biographies[index].Source == selected.Source && d.Biographies[index].Language == selected.Language
			}
		}
	}
	if d.Biography == "" {
		for _, p := range d.Profiles {
			if p.Biography != "" {
				d.Biography = p.Biography
				d.BiographySource = p.Source
				break
			}
		}
	}
	return d, nil
}

func (s *Store) MergeArtists(ctx context.Context, sourceID, targetID int64) (int64, error) {
	if sourceID == targetID {
		return 0, errors.New("source and target artist must differ")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var sourceName, targetName string
	var sourceMerged, targetMerged sql.NullInt64
	var sourceFavorite, targetFavorite int
	var sourceFavoritedAt sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name),merged_into_artist_id,is_favorite,favorited_at FROM artists WHERE id=?`, sourceID).Scan(&sourceName, &sourceMerged, &sourceFavorite, &sourceFavoritedAt); err != nil {
		return 0, err
	}
	if sourceMerged.Valid {
		return 0, errors.New("source artist is already merged")
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name),merged_into_artist_id,is_favorite FROM artists WHERE id=?`, targetID).Scan(&targetName, &targetMerged, &targetFavorite); err != nil {
		return 0, err
	}
	if targetMerged.Valid {
		return 0, errors.New("target artist is already merged")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO artist_merge_operations(source_artist_id,target_artist_id,status,source_name,target_name) VALUES(?,?,'merged',?,?)`, sourceID, targetID, sourceName, targetName)
	if err != nil {
		return 0, err
	}
	operationID, _ := result.LastInsertId()
	if sourceFavorite == 1 && targetFavorite == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET is_favorite=1,favorited_at=? WHERE id=?`, sourceFavoritedAt, targetID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_merge_operations SET favorite_set_by_merge=1 WHERE id=?`, operationID); err != nil {
			return 0, err
		}
	}
	albumRows, err := tx.QueryContext(ctx, `SELECT album_id,position,join_phrase FROM album_artists WHERE artist_id=?`, sourceID)
	if err != nil {
		return 0, err
	}
	type albumMove struct {
		id       int64
		position int
		join     string
	}
	var albums []albumMove
	for albumRows.Next() {
		var v albumMove
		if err = albumRows.Scan(&v.id, &v.position, &v.join); err != nil {
			albumRows.Close()
			return 0, err
		}
		albums = append(albums, v)
	}
	albumRows.Close()
	for _, v := range albums {
		var exists bool
		_ = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_artists WHERE album_id=? AND artist_id=?)`, v.id, targetID).Scan(&exists)
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_merge_relation_moves(operation_id,relation_type,owner_id,position,join_phrase,target_preexisted) VALUES(?,'album',?,?,?,?)`, operationID, v.id, v.position, v.join, boolInt(exists)); err != nil {
			return 0, err
		}
		if !exists {
			if _, err = tx.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position,join_phrase) VALUES(?,?,?,?)`, v.id, targetID, v.position, v.join); err != nil {
				return 0, err
			}
		}
	}
	trackRows, err := tx.QueryContext(ctx, `SELECT track_id,position,join_phrase,role FROM track_artists WHERE artist_id=?`, sourceID)
	if err != nil {
		return 0, err
	}
	type trackMove struct {
		id         int64
		position   int
		join, role string
	}
	var tracks []trackMove
	for trackRows.Next() {
		var v trackMove
		if err = trackRows.Scan(&v.id, &v.position, &v.join, &v.role); err != nil {
			trackRows.Close()
			return 0, err
		}
		tracks = append(tracks, v)
	}
	trackRows.Close()
	for _, v := range tracks {
		var exists bool
		_ = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM track_artists WHERE track_id=? AND artist_id=? AND role=?)`, v.id, targetID, v.role).Scan(&exists)
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_merge_relation_moves(operation_id,relation_type,owner_id,position,join_phrase,role,target_preexisted) VALUES(?,'track',?,?,?,?,?)`, operationID, v.id, v.position, v.join, v.role, boolInt(exists)); err != nil {
			return 0, err
		}
		if !exists {
			if _, err = tx.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,join_phrase,role) VALUES(?,?,?,?,?)`, v.id, targetID, v.position, v.join, v.role); err != nil {
				return 0, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM album_artists WHERE artist_id=?`, sourceID); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_artists WHERE artist_id=?`, sourceID); err != nil {
		return 0, err
	}
	aliasRows, err := tx.QueryContext(ctx, `SELECT value FROM artist_names WHERE artist_id=? UNION SELECT ?`, sourceID, sourceName)
	if err != nil {
		return 0, err
	}
	var aliases []string
	for aliasRows.Next() {
		var alias string
		if err = aliasRows.Scan(&alias); err != nil {
			aliasRows.Close()
			return 0, err
		}
		aliases = append(aliases, alias)
	}
	aliasRows.Close()
	for _, alias := range aliases {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_names WHERE artist_id=? AND value=?)`, targetID, alias).Scan(&exists); err != nil {
			return 0, err
		}
		if exists {
			continue
		}
		aliasResult, insertErr := tx.ExecContext(ctx, `INSERT INTO artist_names(artist_id,value,name_type) VALUES(?,?,'merged_alias')`, targetID, alias)
		if insertErr != nil {
			return 0, insertErr
		}
		aliasID, insertErr := aliasResult.LastInsertId()
		if insertErr != nil {
			return 0, insertErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_merge_created_aliases(operation_id,artist_name_id) VALUES(?,?)`, operationID, aliasID); err != nil {
			return 0, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, targetID, sourceID); err != nil {
		return 0, err
	}
	return operationID, tx.Commit()
}

func (s *Store) RollbackArtistMerge(ctx context.Context, operationID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sourceID, targetID int64
	var status string
	var favoriteSetByMerge int
	if err = tx.QueryRowContext(ctx, `SELECT source_artist_id,target_artist_id,status,favorite_set_by_merge FROM artist_merge_operations WHERE id=?`, operationID).Scan(&sourceID, &targetID, &status, &favoriteSetByMerge); err != nil {
		return err
	}
	if status != "merged" {
		return errors.New("merge operation is not active")
	}
	var merged sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT merged_into_artist_id FROM artists WHERE id=?`, sourceID).Scan(&merged); err != nil {
		return err
	}
	if !merged.Valid || merged.Int64 != targetID {
		return errors.New("artist merge state has changed")
	}
	rows, err := tx.QueryContext(ctx, `SELECT relation_type,owner_id,position,join_phrase,role,target_preexisted FROM artist_merge_relation_moves WHERE operation_id=? ORDER BY id`, operationID)
	if err != nil {
		return err
	}
	type move struct {
		kind       string
		owner      int64
		position   int
		join, role string
		pre        int
	}
	var moves []move
	for rows.Next() {
		var v move
		if err = rows.Scan(&v.kind, &v.owner, &v.position, &v.join, &v.role, &v.pre); err != nil {
			rows.Close()
			return err
		}
		moves = append(moves, v)
	}
	rows.Close()
	for _, v := range moves {
		if v.kind == "album" {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO album_artists(album_id,artist_id,position,join_phrase) VALUES(?,?,?,?)`, v.owner, sourceID, v.position, v.join); err != nil {
				return err
			}
			if v.pre == 0 {
				_, err = tx.ExecContext(ctx, `DELETE FROM album_artists WHERE album_id=? AND artist_id=? AND position=?`, v.owner, targetID, v.position)
			}
		} else {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO track_artists(track_id,artist_id,position,join_phrase,role) VALUES(?,?,?,?,?)`, v.owner, sourceID, v.position, v.join, v.role); err != nil {
				return err
			}
			if v.pre == 0 {
				_, err = tx.ExecContext(ctx, `DELETE FROM track_artists WHERE track_id=? AND artist_id=? AND position=? AND role=?`, v.owner, targetID, v.position, v.role)
			}
		}
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_names WHERE id IN (SELECT artist_name_id FROM artist_merge_created_aliases WHERE operation_id=?)`, operationID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, sourceID); err != nil {
		return err
	}
	if favoriteSetByMerge == 1 {
		var successorID int64
		err = tx.QueryRowContext(ctx, `SELECT mo.id FROM artist_merge_operations mo JOIN artists source ON source.id=mo.source_artist_id WHERE mo.target_artist_id=? AND mo.id<>? AND mo.status='merged' AND source.is_favorite=1 ORDER BY mo.id LIMIT 1`, targetID, operationID).Scan(&successorID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if successorID != 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE artist_merge_operations SET favorite_set_by_merge=1 WHERE id=?`, successorID); err != nil {
				return err
			}
		} else if _, err = tx.ExecContext(ctx, `UPDATE artists SET is_favorite=0,favorited_at=NULL WHERE id=?`, targetID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_merge_operations SET status='rolled_back',rolled_back_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, operationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MergeOperations(ctx context.Context) ([]MergeOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,source_artist_id,target_artist_id,status,source_name,target_name,created_at,COALESCE(rolled_back_at,'') FROM artist_merge_operations ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []MergeOperation
	for rows.Next() {
		var v MergeOperation
		if err = rows.Scan(&v.ID, &v.SourceArtistID, &v.TargetArtistID, &v.Status, &v.SourceName, &v.TargetName, &v.CreatedAt, &v.RolledBackAt); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func canonicalArtistID(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64) (int64, error) {
	seen := map[int64]bool{}
	for {
		if seen[id] {
			return 0, fmt.Errorf("artist merge cycle detected")
		}
		seen[id] = true
		var next sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT merged_into_artist_id FROM artists WHERE id=?`, id).Scan(&next); err != nil {
			return 0, err
		}
		if !next.Valid {
			return id, nil
		}
		id = next.Int64
	}
}

func normalizeArtistName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}
