package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
)

type ArtistReviewFilter struct {
	Query, Source string
	Limit, Offset int
}
type ArtistReviewItem struct {
	Artist                Artist
	Candidates            []ArtistCandidate
	NeedsCreditCorrection bool
}
type ArtistReviewPage struct {
	Items                []ArtistReviewItem
	Total, Limit, Offset int
}

// PendingArtistReviewPage counts candidate-bearing objects before pagination.
// Total includes correction-required objects; merged objects never participate.
func (s *Store) PendingArtistReviewPage(ctx context.Context, f ArtistReviewFilter) (ArtistReviewPage, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	page := ArtistReviewPage{Limit: f.Limit, Offset: f.Offset}
	where := `ar.merged_into_artist_id IS NULL AND (?='' OR instr(lower(COALESCE(ar.user_display_name,ar.display_name)),lower(?))>0) AND EXISTS(SELECT 1 FROM artist_match_candidates c WHERE c.artist_id=ar.id AND c.status='candidate' AND (?='' OR c.source=?))`
	args := []any{strings.TrimSpace(f.Query), strings.TrimSpace(f.Query), f.Source, f.Source}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artists ar WHERE `+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),`+artistImageURLSQL("ar")+`,(SELECT count(DISTINCT album_id) FROM album_artists WHERE artist_id=ar.id),(SELECT count(DISTINCT track_id) FROM track_artists WHERE artist_id=ar.id) FROM artists ar WHERE `+where+` ORDER BY COALESCE(ar.user_display_name,ar.display_name),ar.id LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var ids []int64
	positions := map[int64]int{}
	for rows.Next() {
		var item ArtistReviewItem
		if err = rows.Scan(&item.Artist.ID, &item.Artist.Name, &item.Artist.ImageURL, &item.Artist.AlbumCount, &item.Artist.TrackCount); err != nil {
			return page, err
		}
		item.NeedsCreditCorrection = metadata.CompositeArtistCredit(item.Artist.Name)
		positions[item.Artist.ID] = len(page.Items)
		ids = append(ids, item.Artist.ID)
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	rows.Close()
	if len(ids) == 0 {
		return page, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return page, err
	}
	candidates, err := s.db.QueryContext(ctx, `SELECT id,artist_id,source,external_id,display_name,COALESCE(sort_name,''),COALESCE(disambiguation,''),COALESCE(country,''),COALESCE(artist_type,''),COALESCE(mbid,''),score,evidence_json,status FROM artist_match_candidates WHERE status='candidate' AND artist_id IN (SELECT value FROM json_each(?)) AND (?='' OR source=?) ORDER BY score DESC,id`, string(encoded), f.Source, f.Source)
	if err != nil {
		return page, err
	}
	defer candidates.Close()
	for candidates.Next() {
		var c ArtistCandidate
		var evidence string
		if err = candidates.Scan(&c.ID, &c.ArtistID, &c.Source, &c.ExternalID, &c.DisplayName, &c.SortName, &c.Disambiguation, &c.Country, &c.ArtistType, &c.MBID, &c.Score, &evidence, &c.Status); err != nil {
			return page, err
		}
		if err = json.Unmarshal([]byte(evidence), &c.Evidence); err != nil {
			return page, fmt.Errorf("decode candidate evidence: %w", err)
		}
		i := positions[c.ArtistID]
		page.Items[i].Candidates = append(page.Items[i].Candidates, c)
	}
	return page, candidates.Err()
}
