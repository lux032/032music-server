package storage

import "context"

// Restrict to this person's historical IDs before expanding performers.
const creditDetailTracks = `WITH RECURSIVE m(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN m ON a.merged_into_artist_id=m.id), ct AS (SELECT DISTINCT t.id,t.album_id FROM track_artists ta JOIN tracks t ON t.id=ta.track_id WHERE ta.role IN ('composer','lyricist','arranger','producer') AND ta.artist_id IN (SELECT id FROM m)) `
const creditDetailPerformers = `, performers(track_id,artist_id) AS (SELECT ct.id,ta.artist_id FROM ct JOIN track_artists ta ON ta.track_id=ct.id AND ta.role='primary' UNION SELECT ct.id,aa.artist_id FROM ct JOIN album_artists aa ON aa.album_id=ct.album_id WHERE NOT EXISTS(SELECT 1 FROM track_artists ta WHERE ta.track_id=ct.id AND ta.role='primary')), resolved(track_id,id,next) AS (SELECT p.track_id,a.id,a.merged_into_artist_id FROM performers p JOIN artists a ON a.id=p.artist_id UNION SELECT r.track_id,a.id,a.merged_into_artist_id FROM resolved r JOIN artists a ON a.id=r.next) `

func (s *Store) CreditCollaborators(ctx context.Context, id int64, limit int) ([]Artist, error) {
	id, err := s.CanonicalArtistID(ctx, id)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, creditDetailTracks+creditDetailPerformers+`SELECT a.id,COALESCE(a.user_display_name,a.display_name),`+artistImageURLSQL("a")+`,COUNT(DISTINCT r.track_id) n FROM resolved r JOIN artists a ON a.id=r.id WHERE r.next IS NULL AND r.id<>? GROUP BY a.id ORDER BY n DESC,COALESCE(a.user_display_name,a.display_name) COLLATE NOCASE,a.id LIMIT ?`, id, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Artist{}
	for rows.Next() {
		var a Artist
		if err = rows.Scan(&a.ID, &a.Name, &a.ImageURL, &a.TrackCount); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}
func (s *Store) CreditSelfPerformedCount(ctx context.Context, id int64) (int64, error) {
	id, err := s.CanonicalArtistID(ctx, id)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.db.QueryRowContext(ctx, creditDetailTracks+creditDetailPerformers+`SELECT COUNT(DISTINCT track_id) FROM resolved WHERE next IS NULL AND id=?`, id, id).Scan(&n)
	return n, err
}
func (s *Store) CreditAlbums(ctx context.Context, id int64, limit int) ([]Album, int64, error) {
	id, err := s.CanonicalArtistID(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 60 {
		limit = 60
	}
	var total int64
	err = s.db.QueryRowContext(ctx, creditDetailTracks+`SELECT COUNT(DISTINCT album_id) FROM ct`, id).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, creditDetailTracks+`SELECT a.id FROM albums a WHERE a.id IN (SELECT album_id FROM ct) ORDER BY `+albumReleaseDateSort+` DESC,a.sort_title,a.id LIMIT ?`, id, limit)
	if err != nil {
		return nil, 0, err
	}
	ids := []int64{}
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	albums, err := s.hydrateAlbums(ctx, ids)
	return albums, total, err
}
