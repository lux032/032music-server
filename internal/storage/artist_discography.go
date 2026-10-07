package storage

import "context"

// ArtistRelease separates ownership from release type. Relation is personal,
// collaboration (joint album credit), or appearance (primary track credit only).
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
CASE WHEN EXISTS(SELECT 1 FROM album_artists aa WHERE aa.album_id=a.id AND aa.artist_id=?)
THEN CASE WHEN (SELECT COUNT(DISTINCT artist_id) FROM album_artists aa WHERE aa.album_id=a.id)>1 THEN 'collaboration' ELSE 'personal' END
ELSE 'appearance' END
FROM albums a WHERE (EXISTS(SELECT 1 FROM album_artists aa WHERE aa.album_id=a.id AND aa.artist_id=?)
OR EXISTS(SELECT 1 FROM tracks t JOIN track_artists ta ON ta.track_id=t.id WHERE t.album_id=a.id AND ta.artist_id=? AND ta.role='primary' AND `+trackVisibleSQL("t.id")+`))
AND `+albumVisibleSQL("a.id")+`
ORDER BY `+albumOrder(Filters{Sort: "date"}), id, id, id)
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
		// Multiple album credits on a compilation denote participation, not
		// a jointly owned studio album. A solo artist's best-of stays personal.
		if relation == "collaboration" && album.ReleaseKind == "compilation" {
			relation = "appearance"
		}
		result = append(result, ArtistRelease{Album: album, Relation: relation})
	}
	return result, nil
}
