package storage

import (
	"context"
	"database/sql"
	"sort"
)

// home.go holds the read-only queries behind the web home page
// (/admin/home). Everything here is aggregated from existing tables and
// follows the same visibility rules as the browse pages: albums and tracks
// without available files never appear.

// HomeRoleCount is one work_tracks role bucket of a work ("OP × 2").
type HomeRoleCount struct {
	Role  string
	Count int
}

// HomeWork is one card of the home page's works rail: the work itself, the
// series it belongs to ("" when none), a cover fallback (the poster is
// preferred by the caller; CoverURL is the first linked album's artwork) and
// the per-role counts of its linked tracks.
type HomeWork struct {
	ID          int64
	Title       string
	Type        string
	Year        int
	PosterURL   string
	SeriesTitle string
	CoverURL    string
	TrackCount  int64
	RoleCounts  []HomeRoleCount
}

// HomeRoleTrack is one work-linked song row of the home page's
// “主题曲与插曲” section: a track carrying its role within the work.
type HomeRoleTrack struct {
	Track
	Role      string
	WorkID    int64
	WorkTitle string
	WorkYear  int
}

// HomeSpotlightWork is one member work of the home page's series spotlight.
type HomeSpotlightWork struct {
	Work
	RoleCounts []HomeRoleCount
	Albums     []AlbumWork
}

// HomeSpotlight is the “作品聚焦” card: a series (SeriesID > 0) or, when the
// library has no series with linked albums, the single work with the most
// linked tracks (SeriesID == 0, SeriesTitle == "").
type HomeSpotlight struct {
	SeriesID    int64
	SeriesTitle string
	Works       []HomeSpotlightWork
}

// homeTrackCountSQL counts the distinct visible tracks linked to a work,
// either directly (work_tracks) or through a linked album (album_works).
// Requires the works table alias `w`.
var homeTrackCountSQL = `(SELECT COUNT(*) FROM (SELECT track_id FROM work_tracks WHERE work_id=w.id UNION SELECT t.id FROM tracks t JOIN album_works aw ON aw.album_id=t.album_id WHERE aw.work_id=w.id) wtc WHERE ` + trackVisibleSQL("wtc.track_id") + `)`

// HomeWorks lists works that have at least one linked visible track, ordered
// by linked-track count. workType filters by works.type ("" = all).
func (s *Store) HomeWorks(ctx context.Context, workType string, limit int) ([]HomeWork, error) {
	if limit <= 0 || limit > 100 {
		limit = 24
	}
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM (SELECT w.id,w.title,w.type,COALESCE(w.year,0) AS year,COALESCE(w.poster_url,''),`+homeTrackCountSQL+` AS track_count,COALESCE((SELECT s.title FROM work_series_members m JOIN work_series s ON s.id=m.series_id WHERE m.work_id=w.id),'') AS series_title,COALESCE((SELECT '/api/v1/artwork/'||art.id FROM artworks art JOIN albums al ON al.id=art.album_id WHERE `+albumVisibleSQL("al.id")+` AND art.album_id IN (SELECT alw.album_id FROM album_works alw WHERE alw.work_id=w.id UNION SELECT t2.album_id FROM work_tracks wt2 JOIN tracks t2 ON t2.id=wt2.track_id WHERE wt2.work_id=w.id) ORDER BY (art.source_type='custom') DESC,art.is_primary DESC,art.id LIMIT 1),'') AS cover_url FROM works w WHERE (?='' OR w.type=?)) WHERE track_count>0 ORDER BY track_count DESC,year DESC,id LIMIT ?`, workType, workType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]HomeWork, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var value HomeWork
		if err := rows.Scan(&value.ID, &value.Title, &value.Type, &value.Year, &value.PosterURL, &value.TrackCount, &value.SeriesTitle, &value.CoverURL); err != nil {
			return nil, err
		}
		values = append(values, value)
		ids = append(ids, value.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts, err := s.homeRoleCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range values {
		values[i].RoleCounts = counts[values[i].ID]
	}
	return values, nil
}

// homeRoleCounts returns the visible per-role track counts of the given
// works, ordered by display rank (WorkRoleRank).
func (s *Store) homeRoleCounts(ctx context.Context, workIDs []int64) (map[int64][]HomeRoleCount, error) {
	out := map[int64][]HomeRoleCount{}
	if len(workIDs) == 0 {
		return out, nil
	}
	placeholders, args := inClause(workIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT wt.work_id,wt.role,COUNT(*) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE wt.work_id IN (`+placeholders+`) AND `+trackVisibleSQL("t.id")+` GROUP BY wt.work_id,wt.role`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var workID int64
		var value HomeRoleCount
		if err := rows.Scan(&workID, &value.Role, &value.Count); err != nil {
			return nil, err
		}
		out[workID] = append(out[workID], value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return WorkRoleRank(list[i].Role) < WorkRoleRank(list[j].Role) })
	}
	return out, nil
}

// HomeRoleTracks lists work-linked tracks with a song role (op/ed/insert/
// theme/…— never ost/other), newest work first, one row per work-track link.
func (s *Store) HomeRoleTracks(ctx context.Context, limit int) ([]HomeRoleTrack, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT wt.track_id,wt.role,w.id,w.title,COALESCE(w.year,0) FROM work_tracks wt JOIN works w ON w.id=wt.work_id JOIN tracks t ON t.id=wt.track_id WHERE wt.role IN ('op','ed','insert','theme','character','image_song') AND `+trackVisibleSQL("t.id")+` ORDER BY COALESCE(w.year,0) DESC,w.id,CASE wt.role WHEN 'op' THEN 0 WHEN 'ed' THEN 1 WHEN 'insert' THEN 2 WHEN 'theme' THEN 3 WHEN 'character' THEN 4 ELSE 5 END,wt.season,wt.sequence,wt.track_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type link struct {
		role      string
		workID    int64
		workTitle string
		workYear  int
	}
	links := make([]link, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var l link
		var trackID int64
		if err := rows.Scan(&trackID, &l.role, &l.workID, &l.workTitle, &l.workYear); err != nil {
			return nil, err
		}
		links = append(links, l)
		ids = append(ids, trackID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	tracks, err := s.tracksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	values := make([]HomeRoleTrack, 0, len(links))
	for i, l := range links {
		values = append(values, HomeRoleTrack{Track: tracks[i], Role: l.role, WorkID: l.workID, WorkTitle: l.workTitle, WorkYear: l.workYear})
	}
	return values, nil
}

// UnfinishedPlayback lists tracks that were started but not finished
// (completion resets position_ms to 0, so a positive position below the
// duration is exactly "中途停下"), most recently played first.
func (s *Store) UnfinishedPlayback(ctx context.Context, limit int) ([]PlaybackRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pp.track_id,pp.position_ms,pp.duration_ms,pp.play_count,COALESCE(pp.last_played_at,''),pp.updated_at FROM playback_progress pp WHERE pp.last_played_at IS NOT NULL AND pp.position_ms>0 AND pp.duration_ms>0 AND pp.position_ms < CAST(pp.duration_ms*0.98 AS INTEGER) AND EXISTS(SELECT 1 FROM tracks t WHERE t.id=pp.track_id AND `+trackVisibleSQL("t.id")+`) ORDER BY pp.last_played_at DESC,pp.track_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PlaybackRecord, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var value PlaybackRecord
		if err := rows.Scan(&value.TrackID, &value.PositionMillis, &value.DurationMillis, &value.PlayCount, &value.LastPlayedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		ids = append(ids, value.TrackID)
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	tracks, err := s.tracksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Track = tracks[i]
	}
	return result, nil
}

// RandomAlbum returns one random visible album, excluding the given id when
// possible (so "换一张" never returns the same album while others exist).
func (s *Store) RandomAlbum(ctx context.Context, exclude int64) (Album, error) {
	for _, withExclude := range []bool{true, false} {
		where := albumVisibleSQL("a.id")
		args := []any{}
		if withExclude && exclude > 0 {
			where += " AND a.id<>?"
			args = append(args, exclude)
		}
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT a.id FROM albums a WHERE `+where+` ORDER BY RANDOM() LIMIT 1`, args...).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return Album{}, err
		}
		albums, err := s.hydrateAlbums(ctx, []int64{id})
		if err != nil {
			return Album{}, err
		}
		if len(albums) == 0 {
			return Album{}, sql.ErrNoRows
		}
		return albums[0], nil
	}
	return Album{}, sql.ErrNoRows
}

// homeSpotlightWorks assembles the member works of a spotlight: role counts
// plus linked albums for each.
func (s *Store) homeSpotlightWorks(ctx context.Context, works []Work) ([]HomeSpotlightWork, error) {
	ids := make([]int64, 0, len(works))
	for _, w := range works {
		ids = append(ids, w.ID)
	}
	counts, err := s.homeRoleCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	values := make([]HomeSpotlightWork, 0, len(works))
	for _, w := range works {
		albums, err := s.AlbumsForWork(ctx, w.ID)
		if err != nil {
			return nil, err
		}
		if len(albums) > 4 {
			albums = albums[:4]
		}
		values = append(values, HomeSpotlightWork{Work: w, RoleCounts: counts[w.ID], Albums: albums})
	}
	return values, nil
}

// HomeSeriesSpotlight picks the series whose member works most recently had
// an album added to the library (deterministic, explainable). When no series
// has any linked album, it falls back to the single work with the most
// linked tracks. Returns (nil, nil) when the library has neither.
func (s *Store) HomeSeriesSpotlight(ctx context.Context) (*HomeSpotlight, error) {
	var seriesID int64
	err := s.db.QueryRowContext(ctx, `SELECT m.series_id,MAX(a.added_at) FROM work_series_members m JOIN album_works aw ON aw.work_id=m.work_id JOIN albums a ON a.id=aw.album_id WHERE `+albumVisibleSQL("a.id")+` GROUP BY m.series_id ORDER BY 2 DESC LIMIT 1`).Scan(&seriesID, new(string))
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		series, err := s.WorkSeriesByID(ctx, seriesID)
		if err != nil {
			return nil, err
		}
		members, err := s.SeriesMembers(ctx, seriesID)
		if err != nil {
			return nil, err
		}
		works := make([]Work, 0, len(members))
		for _, member := range members {
			if member.Work.TrackCount > 0 {
				works = append(works, member.Work)
			}
			if len(works) >= 6 {
				break
			}
		}
		if len(works) > 0 {
			spotlightWorks, err := s.homeSpotlightWorks(ctx, works)
			if err != nil {
				return nil, err
			}
			return &HomeSpotlight{SeriesID: series.ID, SeriesTitle: series.Title, Works: spotlightWorks}, nil
		}
	}
	// Fallback: the work with the most linked visible tracks.
	var workID int64
	err = s.db.QueryRowContext(ctx, `SELECT w.id FROM works w WHERE `+homeTrackCountSQL+` > 0 ORDER BY `+homeTrackCountSQL+` DESC,w.id LIMIT 1`).Scan(&workID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	work, err := s.WorkByID(ctx, workID)
	if err != nil {
		return nil, err
	}
	spotlightWorks, err := s.homeSpotlightWorks(ctx, []Work{work})
	if err != nil {
		return nil, err
	}
	return &HomeSpotlight{Works: spotlightWorks}, nil
}
