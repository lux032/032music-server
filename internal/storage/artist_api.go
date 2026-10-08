package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CanonicalArtistID follows merge links; the original ID remains available to callers.
func (s *Store) CanonicalArtistID(ctx context.Context, id int64) (int64, error) {
	return canonicalArtistID(ctx, s.db, id)
}

func (s *Store) SetArtistFavorite(ctx context.Context, id int64, favorite bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	canonical, err := canonicalArtistID(ctx, tx, id)
	if err != nil {
		return err
	}
	var result sql.Result
	if favorite {
		result, err = tx.ExecContext(ctx, `UPDATE artists SET is_favorite=1,favorited_at=CASE WHEN is_favorite=1 THEN favorited_at ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END WHERE id=?`, canonical)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE artists SET is_favorite=0,favorited_at=NULL WHERE id=?`, canonical)
	}
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_merge_operations SET favorite_set_by_merge=0 WHERE target_artist_id=? AND status='merged'`, canonical); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FavoriteArtists(ctx context.Context, limit, offset int) ([]Artist, int64, error) {
	f := Filters{Favorite: true, Limit: limit, Offset: offset, favoriteOrder: true}
	total, err := s.CountArtists(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	items, err := s.ListArtists(ctx, f)
	return items, total, err
}

// FavoriteArtist is one favorited artist on the web favorites page, with the
// counts that decide whether it shows as a singer, a credited person, or both.
type FavoriteArtist struct {
	Artist
	PerformedTrackCount, CreditTrackCount int64
}

// FavoriteArtistCards lists favorited artists, newest favorite first. Counts
// follow ArtistDetail: performed = primary credits plus album-artist tracks;
// credits = composer/lyricist/arranger/producer across the merge chain.
func (s *Store) FavoriteArtistCards(ctx context.Context, limit int) ([]FavoriteArtist, int64, error) {
	f := Filters{Favorite: true, Limit: limit, favoriteOrder: true, PerformerOnly: true}
	total, err := s.CountArtists(ctx, Filters{Favorite: true})
	if err != nil {
		return nil, 0, err
	}
	artists, err := s.ListArtists(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	result := make([]FavoriteArtist, 0, len(artists))
	for _, artist := range artists {
		item := FavoriteArtist{Artist: artist, PerformedTrackCount: artist.TrackCount}
		if err := s.db.QueryRowContext(ctx, `WITH RECURSIVE m(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN m ON a.merged_into_artist_id=m.id) SELECT COUNT(DISTINCT track_id) FROM track_artists WHERE role IN ('composer','lyricist','arranger','producer') AND artist_id IN (SELECT id FROM m) AND `+trackVisibleSQL("track_id"), artist.ID).Scan(&item.CreditTrackCount); err != nil {
			return nil, 0, err
		}
		result = append(result, item)
	}
	return result, total, nil
}

// ArtistAlbums returns albums where this artist has the album-artist role.
func (s *Store) ArtistAlbums(ctx context.Context, id int64) ([]Album, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id FROM albums a WHERE EXISTS(SELECT 1 FROM album_artists aa WHERE aa.album_id=a.id AND aa.artist_id=?) AND `+albumVisibleSQL("a.id")+` ORDER BY `+albumOrder(Filters{Sort: "date"}), id)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var albumID int64
		if err = rows.Scan(&albumID); err != nil {
			break
		}
		ids = append(ids, albumID)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	albums, err := s.albumsByIDs(ctx, ids)
	if albums == nil {
		albums = []Album{}
	}
	return albums, err
}

// Indexed role and album relations are unioned before loading the capped detail rows.
// Tracks without an available file are hidden (see visibility.go).
var artistTrackIDs = `SELECT track_id FROM (SELECT track_id FROM track_artists WHERE role='primary' AND artist_id=? UNION SELECT t.id FROM album_artists aa JOIN tracks t ON t.album_id=aa.album_id WHERE aa.artist_id=?) atm WHERE ` + trackVisibleSQL("atm.track_id")

// Explicit projection for detail tracks, including the user-overridden credit fields.
var artistTrackSelect = `SELECT
 t.id,a.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),
 ` + trackArtistSQL + `,
 COALESCE(a.user_release_year,a.release_year,0),COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),COALESCE(t.user_composer,t.composer,''),COALESCE(t.lyricist,''),COALESCE(t.arranger,''),COALESCE(NULLIF(t.user_track_type,''),NULLIF(t.track_type,''),'regular'),
 COALESCE((SELECT GROUP_CONCAT(gx.name,',' ORDER BY ox.position, gx.id) FROM track_genre_overrides ox JOIN genres gx ON gx.id=ox.genre_id WHERE ox.track_id=t.id),(SELECT GROUP_CONCAT(gx.name,',' ORDER BY rx.position, gx.id) FROM track_genres rx JOIN genres gx ON gx.id=rx.genre_id WHERE rx.track_id=t.id),''),
 COALESCE((SELECT af.container FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
 COALESCE((SELECT af.mime_type FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
 COALESCE((SELECT af.relative_path FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
 COALESCE((SELECT af.file_size FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),0),
 ` + albumArtworkURLSQL + `,
 COALESCE(t.duration_ms,0),'/api/v1/tracks/'||t.id||'/stream',t.added_at,t.updated_at,t.is_favorite,
 COALESCE(pp.last_played_at,''),COALESCE(pp.position_ms,0),COALESCE(pp.play_count,0)
 FROM (` + artistTrackIDs + `) matches JOIN tracks t ON t.id=matches.track_id JOIN albums a ON a.id=t.album_id LEFT JOIN playback_progress pp ON pp.track_id=t.id`

// ArtistTrackQuery selects one page of an artist's tracks. Sort is one of
// ArtistTrackSorts ("" means album); Order is "", "asc" or "desc", where ""
// uses the sort's natural direction.
type ArtistTrackQuery struct {
	Sort   string
	Order  string
	Limit  int
	Offset int
}

// ArtistTopTrackLimit caps the most-played tracks embedded in artist detail.
const ArtistTopTrackLimit = 10

// artistAlbumOrder is the discography order; %[1]s is the direction.
const artistAlbumOrder = `COALESCE(a.user_release_year,a.release_year,0) %[1]s,COALESCE(a.user_title,a.title) COLLATE NOCASE %[1]s,a.id %[1]s,COALESCE(t.user_disc_number,t.disc_number) %[1]s,COALESCE(t.user_track_number,t.track_number) %[1]s,t.id %[1]s`

// artistTrackSorts maps each sort to its primary keys (%[1]s is the
// direction) and its natural direction. Ties fall back to album order.
var artistTrackSorts = map[string]struct {
	keys string
	desc bool
}{
	"album": {"", false},
	"plays": {"COALESCE(pp.play_count,0) %[1]s,COALESCE(pp.last_played_at,'') %[1]s", true},
	// Never-played tracks stay last in both directions.
	"recent":   {"COALESCE(pp.last_played_at,'')='' ASC,COALESCE(pp.last_played_at,'') %[1]s", true},
	"title":    {"COALESCE(t.user_title,t.title) COLLATE NOCASE %[1]s", false},
	"added":    {"t.added_at %[1]s", true},
	"duration": {"COALESCE(t.duration_ms,0) %[1]s", false},
}

// ArtistTrackSorts lists the accepted ArtistTrackQuery.Sort values.
var ArtistTrackSorts = []string{"album", "plays", "recent", "title", "added", "duration"}

func artistTrackOrder(sortKey, order string) (string, error) {
	if sortKey == "" {
		sortKey = "album"
	}
	spec, ok := artistTrackSorts[sortKey]
	if !ok {
		return "", fmt.Errorf("invalid sort %q: must be one of %s", sortKey, strings.Join(ArtistTrackSorts, ", "))
	}
	desc := spec.desc
	switch order {
	case "":
	case "asc":
		desc = false
	case "desc":
		desc = true
	default:
		return "", fmt.Errorf("invalid order %q: must be asc or desc", order)
	}
	direction := "ASC"
	if desc {
		direction = "DESC"
	}
	if spec.keys == "" {
		return fmt.Sprintf(artistAlbumOrder, direction), nil
	}
	return fmt.Sprintf(spec.keys, direction) + "," + fmt.Sprintf(artistAlbumOrder, "ASC"), nil
}

// ArtistTracks returns one sorted page of the tracks the artist performs
// (primary credit or album artist) and the total count.
func (s *Store) ArtistTracks(ctx context.Context, id int64, q ArtistTrackQuery) ([]Track, int64, error) {
	order, err := artistTrackOrder(q.Sort, q.Order)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.ArtistTrackCount(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	tracks, err := s.queryArtistTracks(ctx, artistTrackSelect+` ORDER BY `+order+` LIMIT ? OFFSET ?`, id, id, q.Limit, q.Offset)
	return tracks, total, err
}

// ArtistTrackCount counts the tracks ArtistTracks pages through.
func (s *Store) ArtistTrackCount(ctx context.Context, id int64) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+artistTrackIDs+`)`, id, id).Scan(&total)
	return total, err
}

// ArtistTopTracks returns the artist's most-played tracks; never-played
// tracks are excluded, so the result may be shorter than limit or empty.
func (s *Store) ArtistTopTracks(ctx context.Context, id int64, limit int) ([]Track, error) {
	order, err := artistTrackOrder("plays", "")
	if err != nil {
		return nil, err
	}
	return s.queryArtistTracks(ctx, artistTrackSelect+` WHERE COALESCE(pp.play_count,0)>0 ORDER BY `+order+` LIMIT ?`, id, id, limit)
}

// ArtistHighlightTracks returns the tracks shown at the top of an artist's
// detail: the most-played ones when any were played (popular=true),
// otherwise the first tracks in discography order.
func (s *Store) ArtistHighlightTracks(ctx context.Context, id int64, limit int) (tracks []Track, popular bool, err error) {
	if tracks, err = s.ArtistTopTracks(ctx, id, limit); err != nil || len(tracks) > 0 {
		return tracks, len(tracks) > 0, err
	}
	tracks, _, err = s.ArtistTracks(ctx, id, ArtistTrackQuery{Limit: limit})
	return tracks, false, err
}

func (s *Store) queryArtistTracks(ctx context.Context, query string, args ...any) ([]Track, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tracks := make([]Track, 0)
	for rows.Next() {
		track, err := scanArtistTrack(rows)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, track)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return tracks, s.hydrateTracks(ctx, tracks)
}

func scanArtistTrack(row interface{ Scan(...any) error }) (Track, error) {
	var v Track
	var favorite int
	err := row.Scan(&v.ID, &v.AlbumID, &v.Title, &v.Album, &v.Artist, &v.Year, &v.DiscNumber, &v.TrackNumber, &v.Composer, &v.Lyricist, &v.Arranger, &v.TrackType, &v.Genres, &v.Container, &v.MIMEType, &v.RelativePath, &v.FileSize, &v.ArtworkURL, &v.DurationMillis, &v.StreamURL, &v.AddedAt, &v.UpdatedAt, &favorite, &v.LastPlayedAt, &v.PositionMillis, &v.PlayCount)
	v.IsFavorite = favorite != 0
	return v, err
}

// favoriteArtistRoleClause restricts favorited artists to one favorites tab:
// singers perform (album-artist albums or primary track credits), credits
// hold composer/lyricist/arranger/producer roles across the merge chain.
func favoriteArtistRoleClause(role string) string {
	if role == "credits" {
		return `EXISTS(SELECT 1 FROM track_artists fcra WHERE fcra.role IN ('composer','lyricist','arranger','producer') AND fcra.artist_id IN (WITH RECURSIVE favm(id) AS (SELECT ar.id UNION SELECT fa2.id FROM artists fa2 JOIN favm ON fa2.merged_into_artist_id=favm.id) SELECT id FROM favm) AND ` + trackVisibleSQL("fcra.track_id") + `)`
	}
	return `(EXISTS(SELECT 1 FROM album_artists fsaa WHERE fsaa.artist_id=ar.id AND ` + albumVisibleSQL("fsaa.album_id") + `) OR EXISTS(SELECT 1 FROM track_artists fsta WHERE fsta.artist_id=ar.id AND fsta.role='primary' AND ` + trackVisibleSQL("fsta.track_id") + `))`
}

func (s *Store) countFavoriteArtistRole(ctx context.Context, role string) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists ar WHERE ar.merged_into_artist_id IS NULL AND ar.is_favorite=1 AND `+artistVisibleSQL("ar.id")+` AND (`+favoriteArtistRoleClause(role)+`)`).Scan(&total)
	return total, err
}

// FavoriteArtistCardsPage returns one searchable, sortable page of favorited
// artists for a single web favorites tab (role "singers" or "credits").
// Sort: recent (default) / old / title.
func (s *Store) FavoriteArtistCardsPage(ctx context.Context, role, query, sort string, limit, offset int) ([]FavoriteArtist, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	where := `ar.merged_into_artist_id IS NULL AND ar.is_favorite=1 AND ` + artistVisibleSQL("ar.id") + ` AND (` + favoriteArtistRoleClause(role) + `)`
	args := []any{}
	if variants := SearchVariants(query); len(variants) > 0 {
		parts := make([]string, 0, len(variants))
		for _, variant := range variants {
			parts = append(parts, "(COALESCE(ar.user_display_name,ar.display_name) LIKE '%'||?||'%' OR COALESCE(ar.reading_name,'') LIKE '%'||?||'%')")
			args = append(args, variant, variant)
		}
		where += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists ar WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "ar.favorited_at DESC, ar.id DESC"
	switch sort {
	case "old":
		order = "ar.favorited_at ASC, ar.id ASC"
	case "title":
		order = "COALESCE(ar.user_display_name,ar.display_name) COLLATE NOCASE, ar.id"
	}
	trackCount := "(SELECT COUNT(*) FROM (SELECT track_id FROM track_artists WHERE role='primary' AND artist_id=ar.id UNION SELECT t.id FROM album_artists aa JOIN tracks t ON t.album_id=aa.album_id WHERE aa.artist_id=ar.id) pt WHERE " + trackVisibleSQL("pt.track_id") + ")"
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name) name,`+artistImageURLSQL("ar")+`,(SELECT COUNT(DISTINCT aa.album_id) FROM album_artists aa WHERE aa.artist_id=ar.id AND `+albumVisibleSQL("aa.album_id")+`) album_count,`+trackCount+` track_count,ar.is_favorite FROM artists ar WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	artists := make([]Artist, 0)
	for rows.Next() {
		var v Artist
		var favorite int
		if err = rows.Scan(&v.ID, &v.Name, &v.ImageURL, &v.AlbumCount, &v.TrackCount, &favorite); err != nil {
			rows.Close()
			return nil, 0, err
		}
		v.IsFavorite = favorite != 0
		artists = append(artists, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	result := make([]FavoriteArtist, 0, len(artists))
	for _, artist := range artists {
		item := FavoriteArtist{Artist: artist, PerformedTrackCount: artist.TrackCount}
		if err := s.db.QueryRowContext(ctx, `WITH RECURSIVE m(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN m ON a.merged_into_artist_id=m.id) SELECT COUNT(DISTINCT track_id) FROM track_artists WHERE role IN ('composer','lyricist','arranger','producer') AND artist_id IN (SELECT id FROM m) AND `+trackVisibleSQL("track_id"), artist.ID).Scan(&item.CreditTrackCount); err != nil {
			return nil, 0, err
		}
		result = append(result, item)
	}
	return result, total, nil
}
