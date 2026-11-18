package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
	Conflicts             map[int64]ArtistIdentityConflict
	PageURLs              map[int64]string
	NeedsCreditCorrection bool
}

func candidateExternalURL(source, externalID, mbid, displayName, rawPageURL string) string {
	if trimmed := strings.TrimSpace(rawPageURL); trimmed != "" {
		if parsed, err := url.Parse(trimmed); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			return trimmed
		}
	}
	if strings.EqualFold(strings.TrimSpace(source), "musicbrainz") {
		if id := strings.TrimSpace(externalID); id != "" {
			return "https://musicbrainz.org/artist/" + url.PathEscape(id)
		}
	}
	if trimmedMBID := strings.TrimSpace(mbid); trimmedMBID != "" {
		return "https://musicbrainz.org/artist/" + url.PathEscape(trimmedMBID)
	}
	if strings.EqualFold(strings.TrimSpace(source), "lastfm") {
		if name := strings.TrimSpace(displayName); name != "" {
			return "https://www.last.fm/music/" + url.PathEscape(name)
		}
	}
	return ""
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
	effectiveProfileMissingSQL := `NOT EXISTS(WITH RECURSIVE src(id) AS (SELECT a.id FROM artists a WHERE a.merged_into_artist_id=c.artist_id UNION SELECT a.id FROM artists a JOIN src s ON a.merged_into_artist_id=s.id) SELECT 1 FROM artist_external_profiles p WHERE p.source=c.source AND (p.artist_id=c.artist_id OR p.artist_id IN (SELECT id FROM src)))`
	where := `ar.merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM ai_review_decisions d WHERE d.target_kind='artist_match' AND d.target_id=ar.id AND d.applied=1 AND d.reverted=0) AND (?='' OR instr(lower(COALESCE(ar.user_display_name,ar.display_name)),lower(?))>0) AND EXISTS(SELECT 1 FROM artist_match_candidates c WHERE c.artist_id=ar.id AND c.status='candidate' AND (?='' OR c.source=?) AND ` + effectiveProfileMissingSQL + `)`
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
	candidates, err := s.db.QueryContext(ctx, `SELECT c.id,c.artist_id,c.source,c.external_id,c.display_name,COALESCE(c.sort_name,''),COALESCE(c.disambiguation,''),COALESCE(c.country,''),COALESCE(c.artist_type,''),COALESCE(c.mbid,''),c.score,c.evidence_json,c.status,COALESCE(json_extract(c.payload_json,'$.pageUrl'),'') FROM artist_match_candidates c WHERE c.status='candidate' AND c.artist_id IN (SELECT value FROM json_each(?)) AND (?='' OR c.source=?) AND `+effectiveProfileMissingSQL+` ORDER BY c.score DESC,c.id`, string(encoded), f.Source, f.Source)
	if err != nil {
		return page, err
	}
	defer candidates.Close()
	for candidates.Next() {
		var c ArtistCandidate
		var evidence, rawPageURL string
		if err = candidates.Scan(&c.ID, &c.ArtistID, &c.Source, &c.ExternalID, &c.DisplayName, &c.SortName, &c.Disambiguation, &c.Country, &c.ArtistType, &c.MBID, &c.Score, &evidence, &c.Status, &rawPageURL); err != nil {
			return page, err
		}
		if err = json.Unmarshal([]byte(evidence), &c.Evidence); err != nil {
			return page, fmt.Errorf("decode candidate evidence: %w", err)
		}
		i := positions[c.ArtistID]
		page.Items[i].Candidates = append(page.Items[i].Candidates, c)
		if extURL := candidateExternalURL(c.Source, c.ExternalID, c.MBID, c.DisplayName, rawPageURL); extURL != "" {
			if page.Items[i].PageURLs == nil {
				page.Items[i].PageURLs = map[int64]string{}
			}
			page.Items[i].PageURLs[c.ID] = extURL
		}
		if conflict, conflictErr := artistIdentityConflict(ctx, s.db, c.ArtistID, c.ID); conflictErr == nil {
			if page.Items[i].Conflicts == nil {
				page.Items[i].Conflicts = map[int64]ArtistIdentityConflict{}
			}
			page.Items[i].Conflicts[c.ID] = conflict
		}
	}
	return page, candidates.Err()
}
