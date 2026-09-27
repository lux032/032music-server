package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
)

var ErrInvalidWork = errors.New("invalid work")

var validWorkTypes = map[string]struct{}{
	"anime": {}, "drama": {}, "movie": {}, "game": {}, "commercial": {}, "other": {},
}

var validWorkRoles = map[string]struct{}{
	"op": {}, "ed": {}, "insert": {}, "theme": {}, "character": {}, "ost": {}, "image_song": {}, "other": {},
}

type Work struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	ReadingTitle    string `json:"readingTitle"`
	TranslatedTitle string `json:"translatedTitle"`
	Type            string `json:"type"`
	Year            int    `json:"year"`
	PosterURL       string `json:"posterUrl"`
	ExternalID      string `json:"externalId"`
	TrackCount      int64  `json:"trackCount"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type WorkInput struct {
	Title           string `json:"title"`
	ReadingTitle    string `json:"readingTitle"`
	TranslatedTitle string `json:"translatedTitle"`
	Type            string `json:"type"`
	Year            int    `json:"year"`
	PosterURL       string `json:"posterUrl"`
	ExternalID      string `json:"externalId"`
}

type WorkTrackInput struct {
	TrackID  int64  `json:"trackId"`
	Role     string `json:"role"`
	Season   int    `json:"season"`
	Sequence int    `json:"sequence"`
}

type WorkTrack struct {
	Track
	Role     string `json:"role"`
	Season   int    `json:"season"`
	Sequence int    `json:"sequence"`
	Source   string `json:"source"`
}

type WorkFilters struct {
	Query, Type, Index, Sort string
	Year, Limit, Offset      int
}

func normalizeWorkInput(input WorkInput) (WorkInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.ReadingTitle = strings.TrimSpace(input.ReadingTitle)
	input.TranslatedTitle = strings.TrimSpace(input.TranslatedTitle)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.PosterURL = strings.TrimSpace(input.PosterURL)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	if input.Title == "" {
		return WorkInput{}, fmt.Errorf("%w: title is required", ErrInvalidWork)
	}
	if input.Type == "" {
		input.Type = "other"
	}
	if _, ok := validWorkTypes[input.Type]; !ok {
		return WorkInput{}, fmt.Errorf("%w: unsupported work type %q", ErrInvalidWork, input.Type)
	}
	if input.Year != 0 && (input.Year < 1800 || input.Year > 9999) {
		return WorkInput{}, fmt.Errorf("%w: year is out of range", ErrInvalidWork)
	}
	return input, nil
}

func normalizeWorkTrackInput(input WorkTrackInput) (WorkTrackInput, error) {
	input.Role = strings.ToLower(strings.TrimSpace(input.Role))
	if input.Role == "" {
		input.Role = "other"
	}
	if input.TrackID <= 0 || input.Season < 0 || input.Sequence < 0 {
		return WorkTrackInput{}, fmt.Errorf("%w: invalid track association", ErrInvalidWork)
	}
	if _, ok := validWorkRoles[input.Role]; !ok {
		return WorkTrackInput{}, fmt.Errorf("%w: unsupported work role %q", ErrInvalidWork, input.Role)
	}
	return input, nil
}

func (s *Store) CreateWork(ctx context.Context, input WorkInput) (Work, error) {
	input, err := normalizeWorkInput(input)
	if err != nil {
		return Work{}, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,reading_title,translated_title,type,year,poster_url,external_id) VALUES(?,?,NULLIF(?,''),NULLIF(?,''),?,NULLIF(?,0),NULLIF(?,''),NULLIF(?,''))`, input.Title, metadata.Normalize(input.Title), input.ReadingTitle, input.TranslatedTitle, input.Type, input.Year, input.PosterURL, input.ExternalID)
	if err != nil {
		return Work{}, fmt.Errorf("create work: %w", err)
	}
	id, _ := result.LastInsertId()
	return s.WorkByID(ctx, id)
}

func (s *Store) UpdateWork(ctx context.Context, id int64, input WorkInput) (Work, error) {
	input, err := normalizeWorkInput(input)
	if err != nil {
		return Work{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Work{}, err
	}
	defer tx.Rollback()
	var previousTitle, previousType string
	if err = tx.QueryRowContext(ctx, `SELECT normalized_title,type FROM works WHERE id=?`, id).Scan(&previousTitle, &previousType); err != nil {
		return Work{}, err
	}
	key := metadata.Normalize(input.Title)
	_, err = tx.ExecContext(ctx, `UPDATE works SET title=?,normalized_title=?,reading_title=NULLIF(?,''),translated_title=NULLIF(?,''),type=?,year=NULLIF(?,0),poster_url=NULLIF(?,''),external_id=NULLIF(?,''),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, input.Title, key, input.ReadingTitle, input.TranslatedTitle, input.Type, input.Year, input.PosterURL, input.ExternalID, id)
	if err != nil {
		return Work{}, fmt.Errorf("update work: %w", err)
	}
	if previousTitle != key || previousType != input.Type {
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_enrichment_misses WHERE work_id=? AND source='bangumi'`, id); err != nil {
			return Work{}, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM work_match_candidates WHERE work_id=? AND source='bangumi' AND status='candidate'`, id); err != nil {
			return Work{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO work_enrichment_retries(work_id,source,requested_at) SELECT ?,'bangumi',strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE NOT EXISTS(SELECT 1 FROM work_external_profiles WHERE work_id=? AND source='bangumi') AND NOT EXISTS(SELECT 1 FROM work_match_candidates WHERE work_id=? AND source='bangumi' AND status='confirmed') ON CONFLICT(work_id,source) DO UPDATE SET requested_at=excluded.requested_at`, id, id, id); err != nil {
			return Work{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Work{}, err
	}
	return s.WorkByID(ctx, id)
}

func (s *Store) DeleteWork(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM works WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) WorkByID(ctx context.Context, id int64) (Work, error) {
	var value Work
	err := s.db.QueryRowContext(ctx, `SELECT id,title,COALESCE(reading_title,''),COALESCE(translated_title,''),type,COALESCE(year,0),COALESCE(poster_url,''),COALESCE(external_id,''),(SELECT COUNT(DISTINCT track_id) FROM work_tracks WHERE work_id=works.id),created_at,updated_at FROM works WHERE id=?`, id).Scan(&value.ID, &value.Title, &value.ReadingTitle, &value.TranslatedTitle, &value.Type, &value.Year, &value.PosterURL, &value.ExternalID, &value.TrackCount, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (s *Store) ListWorks(ctx context.Context, filter WorkFilters) ([]Work, error) {
	limit, offset := workPage(filter)
	where, args := workWhere(filter)
	order := "COALESCE(NULLIF(reading_title,''),title) COLLATE NOCASE,id"
	if filter.Sort == "year" {
		order = "COALESCE(year,0) DESC," + order
	} else if filter.Sort == "updated" {
		order = "updated_at DESC,id DESC"
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,COALESCE(reading_title,''),COALESCE(translated_title,''),type,COALESCE(year,0),COALESCE(poster_url,''),COALESCE(external_id,''),(SELECT COUNT(DISTINCT track_id) FROM work_tracks WHERE work_id=works.id),created_at,updated_at FROM works WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Work, 0)
	for rows.Next() {
		var value Work
		if err := rows.Scan(&value.ID, &value.Title, &value.ReadingTitle, &value.TranslatedTitle, &value.Type, &value.Year, &value.PosterURL, &value.ExternalID, &value.TrackCount, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) CountWorks(ctx context.Context, filter WorkFilters) (int64, error) {
	where, args := workWhere(filter)
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works WHERE `+where, args...).Scan(&total)
	return total, err
}

func workWhere(filter WorkFilters) (string, []any) {
	clauses := []string{"(?='' OR type=?)", "(?=0 OR year=?)"}
	args := []any{filter.Type, filter.Type, filter.Year, filter.Year}
	variants := SearchVariants(filter.Query)
	if len(variants) > 0 {
		parts := make([]string, 0, len(variants)*3)
		for _, variant := range variants {
			parts = append(parts, "title LIKE '%'||?||'%'", "COALESCE(reading_title,'') LIKE '%'||?||'%'", "COALESCE(translated_title,'') LIKE '%'||?||'%'")
			args = append(args, variant, variant, variant)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if condition, indexArgs := IndexCondition("COALESCE(NULLIF(reading_title,''),title)", filter.Index); condition != "" {
		clauses = append(clauses, condition)
		args = append(args, indexArgs...)
	}
	return strings.Join(clauses, " AND "), args
}

func workPage(filter WorkFilters) (int, int) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 48
	}
	if limit > 500 {
		limit = 500
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return limit, filter.Offset
}

func (s *Store) AddWorkTrack(ctx context.Context, workID int64, input WorkTrackInput) error {
	input, err := normalizeWorkTrackInput(input)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,season,sequence,source) VALUES(?,?,?,?,?,'manual') ON CONFLICT(work_id,track_id,role,season,sequence) DO UPDATE SET source='manual'`, workID, input.TrackID, input.Role, input.Season, input.Sequence)
	return err
}

func (s *Store) RemoveWorkTrack(ctx context.Context, workID, trackID int64, role string, season, sequence int) error {
	query := `DELETE FROM work_tracks WHERE work_id=? AND track_id=?`
	args := []any{workID, trackID}
	if strings.TrimSpace(role) != "" {
		query += ` AND role=? AND season=? AND sequence=?`
		args = append(args, strings.ToLower(strings.TrimSpace(role)), season, sequence)
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) TracksForWork(ctx context.Context, workID int64) ([]WorkTrack, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,a.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),`+trackArtistSQL+`,COALESCE(a.user_release_year,a.release_year,0),COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),COALESCE(t.user_composer,t.composer,''),COALESCE(t.lyricist,''),COALESCE(t.arranger,''),COALESCE(t.user_track_type,t.track_type,'regular'),COALESCE((SELECT GROUP_CONCAT(gx.name,',' ORDER BY ox.position, gx.id) FROM track_genre_overrides ox JOIN genres gx ON gx.id=ox.genre_id WHERE ox.track_id=t.id),(SELECT GROUP_CONCAT(gx.name,',' ORDER BY rx.position, gx.id) FROM track_genres rx JOIN genres gx ON gx.id=rx.genre_id WHERE rx.track_id=t.id),''),COALESCE(af.container,''),COALESCE(af.mime_type,''),COALESCE(af.relative_path,''),COALESCE(af.file_size,0),COALESCE((SELECT '/api/v1/artwork/'||id FROM artworks aw WHERE aw.album_id=a.id ORDER BY is_primary DESC,id LIMIT 1),''),COALESCE(t.duration_ms,0),'/api/v1/tracks/'||t.id||'/stream',t.added_at,t.updated_at,t.is_favorite,COALESCE(pp.last_played_at,''),COALESCE(pp.position_ms,0),COALESCE(pp.play_count,0),wt.role,wt.season,wt.sequence,wt.source FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id JOIN albums a ON a.id=t.album_id LEFT JOIN audio_files af ON af.track_id=t.id AND af.status='available' LEFT JOIN playback_progress pp ON pp.track_id=t.id WHERE wt.work_id=? ORDER BY wt.season,wt.role,wt.sequence,a.sort_title,COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),t.id`, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]WorkTrack, 0)
	for rows.Next() {
		var value WorkTrack
		var favorite int
		if err := rows.Scan(&value.ID, &value.AlbumID, &value.Title, &value.Album, &value.Artist, &value.Year, &value.DiscNumber, &value.TrackNumber, &value.Composer, &value.Lyricist, &value.Arranger, &value.TrackType, &value.Genres, &value.Container, &value.MIMEType, &value.RelativePath, &value.FileSize, &value.ArtworkURL, &value.DurationMillis, &value.StreamURL, &value.AddedAt, &value.UpdatedAt, &favorite, &value.LastPlayedAt, &value.PositionMillis, &value.PlayCount, &value.Role, &value.Season, &value.Sequence, &value.Source); err != nil {
			return nil, err
		}
		value.IsFavorite = favorite != 0
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	ptrs := make([]*Track, len(values))
	for i := range values {
		ptrs[i] = &values[i].Track
	}
	if err := hydrateTrackExtras(ctx, s, ptrs); err != nil {
		return nil, err
	}
	return values, nil
}

func ensureAutoWorkAssociation(ctx context.Context, tx *sql.Tx, trackID int64, association metadata.WorkAssociation) error {
	if strings.TrimSpace(association.Title) == "" {
		return nil
	}
	workType := association.Type
	if _, ok := validWorkTypes[workType]; !ok {
		workType = "other"
	}
	role := association.Role
	if _, ok := validWorkRoles[role]; !ok {
		role = "other"
	}
	key := metadata.Normalize(association.Title)
	// Work identity is (normalized_title, type, year). Auto-inference has no
	// year, so it attaches to an existing work of the same title+type when
	// one exists (preferring an exact NULL year, then the oldest row), and
	// only creates a new work otherwise. Same-titled works of a different
	// type or year are deliberately NOT merged (M4).
	var workID int64
	var existingType string
	err := tx.QueryRowContext(ctx, `SELECT id,type FROM works WHERE normalized_title=? AND (type=? OR type='other') ORDER BY CASE WHEN type=? THEN 0 ELSE 1 END,CASE WHEN year IS NULL THEN 0 ELSE 1 END,id LIMIT 1`, key, workType, workType).Scan(&workID, &existingType)
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type) VALUES(?,?,?)`, association.Title, key, workType)
		if insertErr != nil {
			return insertErr
		}
		workID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if existingType == "other" && workType != "other" {
		if _, err = tx.ExecContext(ctx, `UPDATE works SET type=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, workType, workID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,season,sequence,source) VALUES(?,?,?,?,?,'auto') ON CONFLICT(work_id,track_id,role,season,sequence) DO NOTHING`, workID, trackID, role, association.Season, association.Sequence)
	return err
}
