package storage

import (
	"context"
	"database/sql"
	"sort"
	"time"
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
// series it belongs to (SeriesID 0 / "" when none), a cover fallback (the poster is
// preferred by the caller; CoverURL is the first visible linked album's
// artwork) and the per-role counts of its linked tracks.
type HomeWork struct {
	ID          int64
	Title       string
	Type        string
	Year        int
	PosterURL   string
	UpdatedAt   string
	SeriesID    int64
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

// homeSongRolesSQL lists the work_tracks roles that count as songs for the
// “主题曲与插曲” section (never ost/other).
const homeSongRolesSQL = `('op','ed','insert','theme','character','image_song')`

// HomeWorkCandidate is one entry of the works-rail rotation pool: a work with
// at least one linked visible track. TrackCount counts distinct visible
// tracks linked directly (work_tracks) or through a linked album
// (album_works). Played / RecentlyPlayed are derived from the same link set:
// some linked track was ever started (playback_progress.last_played_at set),
// respectively started at or after the caller's cutoff.
type HomeWorkCandidate struct {
	ID             int64
	Type           string
	TrackCount     int64
	Played         bool
	RecentlyPlayed bool
}

// HomeWorkCandidates returns the whole works-rail pool ordered by id, so the
// caller's seeded shuffle is reproducible for the same data.
func (s *Store) HomeWorkCandidates(ctx context.Context, recentSince time.Time) ([]HomeWorkCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `WITH links AS (SELECT work_id,track_id FROM work_tracks UNION SELECT aw.work_id,t.id FROM album_works aw JOIN tracks t ON t.album_id=aw.album_id)
SELECT w.id,w.type,COUNT(*),COALESCE(MAX(p.last_played_at IS NOT NULL),0),COALESCE(MAX(p.last_played_at IS NOT NULL AND julianday(p.last_played_at)>=julianday(?)),0)
FROM works w JOIN links l ON l.work_id=w.id LEFT JOIN playback_progress p ON p.track_id=l.track_id
WHERE `+trackVisibleSQL("l.track_id")+` GROUP BY w.id ORDER BY w.id`, recentSince.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]HomeWorkCandidate, 0)
	for rows.Next() {
		var value HomeWorkCandidate
		var played, recent int
		if err := rows.Scan(&value.ID, &value.Type, &value.TrackCount, &played, &recent); err != nil {
			return nil, err
		}
		value.Played, value.RecentlyPlayed = played != 0, recent != 0
		values = append(values, value)
	}
	return values, rows.Err()
}

// HomeWorksByIDs loads the works-rail cards for the given ids in the given
// order. Works that lost every visible linked track since the pool was built
// are dropped (the page simply comes out shorter).
func (s *Store) HomeWorksByIDs(ctx context.Context, ids []int64) ([]HomeWork, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM (SELECT w.id,w.title,w.type,COALESCE(w.year,0),COALESCE(w.poster_url,''),w.updated_at,`+homeTrackCountSQL+` AS track_count,COALESCE((SELECT m.series_id FROM work_series_members m WHERE m.work_id=w.id),0),COALESCE((SELECT s.title FROM work_series_members m JOIN work_series s ON s.id=m.series_id WHERE m.work_id=w.id),''),COALESCE((SELECT '/api/v1/artwork/'||art.id FROM artworks art JOIN albums al ON al.id=art.album_id WHERE `+albumVisibleSQL("al.id")+` AND art.album_id IN (SELECT alw.album_id FROM album_works alw WHERE alw.work_id=w.id UNION SELECT t2.album_id FROM work_tracks wt2 JOIN tracks t2 ON t2.id=wt2.track_id WHERE wt2.work_id=w.id AND `+trackVisibleSQL("t2.id")+`) ORDER BY (art.source_type='custom') DESC,art.is_primary DESC,art.id LIMIT 1),'') FROM works w WHERE w.id IN (`+placeholders+`)) WHERE track_count>0`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]HomeWork{}
	for rows.Next() {
		var value HomeWork
		if err := rows.Scan(&value.ID, &value.Title, &value.Type, &value.Year, &value.PosterURL, &value.UpdatedAt, &value.TrackCount, &value.SeriesID, &value.SeriesTitle, &value.CoverURL); err != nil {
			return nil, err
		}
		byID[value.ID] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	values := make([]HomeWork, 0, len(byID))
	found := make([]int64, 0, len(byID))
	for _, id := range ids {
		if value, ok := byID[id]; ok {
			values = append(values, value)
			found = append(found, id)
		}
	}
	counts, err := s.homeRoleCounts(ctx, found)
	if err != nil {
		return nil, err
	}
	for i := range values {
		values[i].RoleCounts = counts[values[i].ID]
	}
	return values, nil
}

// homeRoleCounts returns the visible per-role track counts of the given
// works (distinct tracks per role, so several season/sequence rows of one
// song count once), ordered by display rank (WorkRoleRank).
func (s *Store) homeRoleCounts(ctx context.Context, workIDs []int64) (map[int64][]HomeRoleCount, error) {
	out := map[int64][]HomeRoleCount{}
	if len(workIDs) == 0 {
		return out, nil
	}
	placeholders, args := inClause(workIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT wt.work_id,wt.role,COUNT(DISTINCT wt.track_id) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE wt.work_id IN (`+placeholders+`) AND `+trackVisibleSQL("t.id")+` GROUP BY wt.work_id,wt.role`, args...)
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

// HomeRoleCandidate is one song of the “主题曲与插曲” rotation pool: a
// distinct (work, visible track) pair with a song role. When the pair has
// several song roles (or several season/sequence rows) Role is the
// best-ranked one (WorkRoleRank). Played / RecentlyPlayed describe the track.
type HomeRoleCandidate struct {
	WorkID         int64
	TrackID        int64
	Role           string
	Played         bool
	RecentlyPlayed bool
}

// HomeRoleCandidates returns the song pool ordered by (work, track).
func (s *Store) HomeRoleCandidates(ctx context.Context, recentSince time.Time) ([]HomeRoleCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT wt.work_id,wt.track_id,wt.role,p.last_played_at IS NOT NULL,COALESCE(p.last_played_at IS NOT NULL AND julianday(p.last_played_at)>=julianday(?),0) FROM work_tracks wt LEFT JOIN playback_progress p ON p.track_id=wt.track_id WHERE wt.role IN `+homeSongRolesSQL+` AND `+trackVisibleSQL("wt.track_id")+` ORDER BY wt.work_id,wt.track_id,wt.season,wt.sequence,wt.role`, recentSince.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]HomeRoleCandidate, 0)
	for rows.Next() {
		var value HomeRoleCandidate
		var played, recent int
		if err := rows.Scan(&value.WorkID, &value.TrackID, &value.Role, &played, &recent); err != nil {
			return nil, err
		}
		value.Played, value.RecentlyPlayed = played != 0, recent != 0
		if n := len(values); n > 0 && values[n-1].WorkID == value.WorkID && values[n-1].TrackID == value.TrackID {
			if WorkRoleRank(value.Role) < WorkRoleRank(values[n-1].Role) {
				values[n-1].Role = value.Role
			}
			continue
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// HomeRolePick is one song chosen for a “主题曲与插曲” page.
type HomeRolePick struct {
	WorkID  int64
	TrackID int64
	Role    string
}

// HomeRoleTracksFor hydrates the picked songs in order. Picks whose track or
// work disappeared (or whose track lost its available file) since the pool
// was built are dropped.
func (s *Store) HomeRoleTracksFor(ctx context.Context, picks []HomeRolePick) ([]HomeRoleTrack, error) {
	if len(picks) == 0 {
		return nil, nil
	}
	trackIDs := make([]int64, 0, len(picks))
	workIDs := make([]int64, 0, len(picks))
	for _, pick := range picks {
		trackIDs = append(trackIDs, pick.TrackID)
		workIDs = append(workIDs, pick.WorkID)
	}
	type workInfo struct {
		title string
		year  int
	}
	works := map[int64]workInfo{}
	placeholders, args := inClause(workIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,COALESCE(year,0) FROM works WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var info workInfo
		if err := rows.Scan(&id, &info.title, &info.year); err != nil {
			rows.Close()
			return nil, err
		}
		works[id] = info
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	visible := map[int64]bool{}
	placeholders, args = inClause(trackIDs)
	rows, err = s.db.QueryContext(ctx, `SELECT t.id FROM tracks t WHERE t.id IN (`+placeholders+`) AND `+trackVisibleSQL("t.id"), args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		visible[id] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	kept := make([]HomeRolePick, 0, len(picks))
	keptIDs := make([]int64, 0, len(picks))
	for _, pick := range picks {
		if _, ok := works[pick.WorkID]; ok && visible[pick.TrackID] {
			kept = append(kept, pick)
			keptIDs = append(keptIDs, pick.TrackID)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	tracks, err := s.tracksByIDs(ctx, keptIDs)
	if err != nil {
		return nil, err
	}
	values := make([]HomeRoleTrack, 0, len(kept))
	for i, pick := range kept {
		info := works[pick.WorkID]
		values = append(values, HomeRoleTrack{Track: tracks[i], Role: pick.Role, WorkID: pick.WorkID, WorkTitle: info.title, WorkYear: info.year})
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
