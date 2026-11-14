package storage

import "context"

// ArtistRelease separates ownership from release type. Relation is personal,
// collaboration (joint album credit or a jointly performed track), or appearance.
// These are read-time classifications; no existing credits are rewritten.
type ArtistRelease struct {
	Album
	Relation string `json:"relation"`
}

// ArtistDiscography is deliberately narrower than the general album search:
// composer/lyricist/instrument credits do not imply a vocal release appearance.
// Select before hydrating so dates use the same effective-date ordering as browse.
func (s *Store) ArtistDiscography(ctx context.Context, id int64) ([]ArtistRelease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,
CASE WHEN EXISTS(
 SELECT 1 FROM track_artists performer JOIN tracks duet ON duet.id=performer.track_id
 WHERE duet.album_id=a.id AND performer.artist_id=? AND performer.role='primary'
 AND `+trackVisibleSQL("duet.id")+`
 AND (SELECT COUNT(DISTINCT partner.artist_id) FROM track_artists partner WHERE partner.track_id=duet.id AND partner.role='primary')>1
) THEN 'joint_track'
WHEN EXISTS(SELECT 1 FROM album_artists aa WHERE aa.album_id=a.id AND aa.artist_id=?)
THEN CASE WHEN (SELECT COUNT(DISTINCT artist_id) FROM album_artists aa WHERE aa.album_id=a.id)>1 THEN 'collaboration' ELSE 'personal' END
ELSE 'appearance' END
FROM albums a WHERE a.id IN (SELECT aa.album_id FROM album_artists aa WHERE aa.artist_id=?
UNION SELECT t.album_id FROM track_artists ta JOIN tracks t ON t.id=ta.track_id WHERE ta.artist_id=? AND ta.role='primary' AND `+trackVisibleSQL("t.id")+`)
AND `+albumVisibleSQL("a.id")+`
ORDER BY `+albumOrder(Filters{Sort: "date"}), id, id, id, id)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	relations := map[int64]string{}
	for rows.Next() {
		var albumID int64
		var relation string
		if err = rows.Scan(&albumID, &relation); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, albumID)
		relations[albumID] = relation
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []ArtistRelease{}
	if len(ids) == 0 {
		return result, nil
	}
	albums, err := s.hydrateAlbums(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, album := range albums {
		relation := relations[album.ID]
		// Track-level collaborations are independent of album credit and type:
		// a VA compilation can contain a duet just like a studio album.
		if relation == "joint_track" {
			relation = "collaboration"
		} else if relation == "collaboration" && album.ReleaseKind == "compilation" {
			// Merely listing multiple album artists is not evidence of a duet.
			relation = "appearance"
		}
		result = append(result, ArtistRelease{Album: album, Relation: relation})
	}
	return result, nil
}
