package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
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

const artistInheritedProfilesCTE = `WITH RECURSIVE
	inherited_profile_rows(artist_id, source) AS (
		SELECT a.merged_into_artist_id, p.source
		FROM artists a
		JOIN artist_external_profiles p ON p.artist_id = a.id
		WHERE a.merged_into_artist_id > 0
		UNION
		SELECT a.merged_into_artist_id, ip.source
		FROM inherited_profile_rows ip
		JOIN artists a ON a.id = ip.artist_id
		WHERE a.merged_into_artist_id > 0
	),
	inherited_profile_keys(k) AS MATERIALIZED (
		SELECT DISTINCT artist_id || ':' || source FROM inherited_profile_rows
	) `

const artistCandidateUnmatchedSQL = `NOT EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=c.artist_id AND p.source=c.source) AND (c.artist_id || ':' || c.source) NOT IN (SELECT k FROM inherited_profile_keys)`

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
	where := `ar.merged_into_artist_id IS NULL AND NOT EXISTS(SELECT 1 FROM ai_review_decisions d WHERE d.target_kind='artist_match' AND d.target_id=ar.id AND d.applied=1 AND d.reverted=0) AND (?='' OR instr(lower(COALESCE(ar.user_display_name,ar.display_name)),lower(?))>0) AND EXISTS(SELECT 1 FROM artist_match_candidates c WHERE c.artist_id=ar.id AND c.status='candidate' AND (?='' OR c.source=?) AND ` + artistCandidateUnmatchedSQL + `)`
	args := []any{strings.TrimSpace(f.Query), strings.TrimSpace(f.Query), f.Source, f.Source}
	if err := s.db.QueryRowContext(ctx, artistInheritedProfilesCTE+`SELECT count(*) FROM artists ar WHERE `+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, artistInheritedProfilesCTE+`SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),`+artistImageURLSQL("ar")+`,(SELECT count(DISTINCT album_id) FROM album_artists WHERE artist_id=ar.id),(SELECT count(DISTINCT track_id) FROM track_artists WHERE artist_id=ar.id) FROM artists ar WHERE `+where+` ORDER BY COALESCE(ar.user_display_name,ar.display_name),ar.id LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
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
	candidates, err := s.db.QueryContext(ctx, artistInheritedProfilesCTE+`SELECT c.id,c.artist_id,c.source,c.external_id,c.display_name,COALESCE(c.sort_name,''),COALESCE(c.disambiguation,''),COALESCE(c.country,''),COALESCE(c.artist_type,''),COALESCE(c.mbid,''),c.score,c.evidence_json,c.status,COALESCE(json_extract(c.payload_json,'$.pageUrl'),'') FROM artist_match_candidates c WHERE c.status='candidate' AND c.artist_id IN (SELECT value FROM json_each(?)) AND (?='' OR c.source=?) AND `+artistCandidateUnmatchedSQL+` ORDER BY c.score DESC,c.id`, string(encoded), f.Source, f.Source)
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
	}
	if err = candidates.Err(); err != nil {
		return page, err
	}
	if err = s.attachBatchArtistIdentityConflicts(ctx, page.Items); err != nil {
		return page, err
	}
	return page, nil
}

// PendingArtistReviewSplitPages splits regular identity-review artists and
// composite-credit artists prior to pagination so each workbench tab gets an
// accurate total and independent page slice.
func (s *Store) PendingArtistReviewSplitPages(ctx context.Context, f ArtistReviewFilter, creditsLimit, creditsOffset int) (identityPage, creditsPage ArtistReviewPage, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if creditsLimit <= 0 {
		creditsLimit = 50
	}
	if creditsLimit > 200 {
		creditsLimit = 200
	}
	if creditsOffset < 0 {
		creditsOffset = 0
	}
	identityPage = ArtistReviewPage{Limit: f.Limit, Offset: f.Offset}
	creditsPage = ArtistReviewPage{Limit: creditsLimit, Offset: creditsOffset}

	q := strings.TrimSpace(f.Query)
	// Drive from indexed artist_match_candidates(status, artist_id, source)
	// instead of scanning the entire artists table when few candidates exist.
	rows, err := s.db.QueryContext(ctx, artistInheritedProfilesCTE+`SELECT DISTINCT ar.id,COALESCE(ar.user_display_name,ar.display_name)
		FROM artist_match_candidates c
		JOIN artists ar ON ar.id=c.artist_id
		WHERE c.status='candidate'
		  AND (?='' OR c.source=?)
		  AND ar.merged_into_artist_id IS NULL
		  AND NOT EXISTS(SELECT 1 FROM ai_review_decisions d WHERE d.target_kind='artist_match' AND d.target_id=ar.id AND d.applied=1 AND d.reverted=0)
		  AND (?='' OR instr(lower(COALESCE(ar.user_display_name,ar.display_name)),lower(?))>0)
		  AND `+artistCandidateUnmatchedSQL+`
		ORDER BY COALESCE(ar.user_display_name,ar.display_name),ar.id`, f.Source, f.Source, q, q)
	if err != nil {
		return identityPage, creditsPage, err
	}
	type namedID struct {
		id   int64
		name string
	}
	var identityIDs []int64
	var creditEntries []namedID
	creditSeen := map[int64]bool{}
	for rows.Next() {
		var id int64
		var name string
		if err = rows.Scan(&id, &name); err != nil {
			rows.Close()
			return identityPage, creditsPage, err
		}
		if metadata.CompositeArtistCredit(name) {
			creditEntries = append(creditEntries, namedID{id: id, name: name})
			creditSeen[id] = true
		} else {
			identityIDs = append(identityIDs, id)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return identityPage, creditsPage, err
	}
	rows.Close()

	// Automatic scans skip MusicBrainz/Last.fm for composite credits and record
	// skipped_composite (or an AI credit_split decision) without creating
	// artist_match_candidates rows. Include those still-referenced composite
	// artists on Tab 2 when no candidate-source filter is active.
	if f.Source == "" {
		compRows, compErr := s.db.QueryContext(ctx, `WITH flagged_composites(id) AS (
				SELECT artist_id FROM artist_source_match_state WHERE source='_composite' AND outcome='skipped_composite'
				UNION
				SELECT target_id FROM ai_review_decisions WHERE target_kind='credit_split' AND reverted=0
			)
			SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name)
			FROM flagged_composites fc
			JOIN artists ar ON ar.id=fc.id
			WHERE ar.merged_into_artist_id IS NULL
			  AND (?='' OR instr(lower(COALESCE(ar.user_display_name,ar.display_name)),lower(?))>0)
			  AND NOT EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=ar.id)
			  AND (EXISTS(SELECT 1 FROM track_artists ta WHERE ta.artist_id=ar.id) OR EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=ar.id))`, q, q)
		if compErr != nil {
			return identityPage, creditsPage, compErr
		}
		addedFlagged := false
		for compRows.Next() {
			var id int64
			var name string
			if err = compRows.Scan(&id, &name); err != nil {
				compRows.Close()
				return identityPage, creditsPage, err
			}
			if !creditSeen[id] && metadata.CompositeArtistCredit(name) {
				creditEntries = append(creditEntries, namedID{id: id, name: name})
				creditSeen[id] = true
				addedFlagged = true
			}
		}
		if err = compRows.Err(); err != nil {
			compRows.Close()
			return identityPage, creditsPage, err
		}
		compRows.Close()
		if addedFlagged {
			sort.Slice(creditEntries, func(i, j int) bool {
				if creditEntries[i].name != creditEntries[j].name {
					return creditEntries[i].name < creditEntries[j].name
				}
				return creditEntries[i].id < creditEntries[j].id
			})
		}
	}

	creditIDs := make([]int64, len(creditEntries))
	for i, e := range creditEntries {
		creditIDs[i] = e.id
	}

	identityPage.Total = len(identityIDs)
	creditsPage.Total = len(creditIDs)

	sliceIDs := func(all []int64, offset, limit int) []int64 {
		if offset >= len(all) {
			return nil
		}
		end := offset + limit
		if end > len(all) {
			end = len(all)
		}
		return all[offset:end]
	}

	if identityPage.Items, err = s.hydrateArtistReviewItems(ctx, sliceIDs(identityIDs, f.Offset, f.Limit), f.Source, false); err != nil {
		return identityPage, creditsPage, err
	}
	if creditsPage.Items, err = s.hydrateArtistReviewItems(ctx, sliceIDs(creditIDs, creditsOffset, creditsLimit), f.Source, true); err != nil {
		return identityPage, creditsPage, err
	}
	return identityPage, creditsPage, nil
}

func (s *Store) hydrateArtistReviewItems(ctx context.Context, ids []int64, source string, isCredit bool) ([]ArtistReviewItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),`+artistImageURLSQL("ar")+`,(SELECT count(DISTINCT album_id) FROM album_artists WHERE artist_id=ar.id),(SELECT count(DISTINCT track_id) FROM track_artists WHERE artist_id=ar.id) FROM artists ar WHERE ar.id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]ArtistReviewItem, len(ids))
	for rows.Next() {
		var item ArtistReviewItem
		if err = rows.Scan(&item.Artist.ID, &item.Artist.Name, &item.Artist.ImageURL, &item.Artist.AlbumCount, &item.Artist.TrackCount); err != nil {
			rows.Close()
			return nil, err
		}
		item.NeedsCreditCorrection = isCredit
		byID[item.Artist.ID] = item
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	items := make([]ArtistReviewItem, 0, len(ids))
	positions := make(map[int64]int, len(ids))
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			positions[id] = len(items)
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil, nil
	}

	candidates, err := s.db.QueryContext(ctx, artistInheritedProfilesCTE+`SELECT c.id,c.artist_id,c.source,c.external_id,c.display_name,COALESCE(c.sort_name,''),COALESCE(c.disambiguation,''),COALESCE(c.country,''),COALESCE(c.artist_type,''),COALESCE(c.mbid,''),c.score,c.evidence_json,c.status,COALESCE(json_extract(c.payload_json,'$.pageUrl'),'') FROM artist_match_candidates c WHERE c.status='candidate' AND c.artist_id IN (SELECT value FROM json_each(?)) AND (?='' OR c.source=?) AND `+artistCandidateUnmatchedSQL+` ORDER BY c.score DESC,c.id`, string(encoded), source, source)
	if err != nil {
		return nil, err
	}
	defer candidates.Close()
	for candidates.Next() {
		var c ArtistCandidate
		var evidence, rawPageURL string
		if err = candidates.Scan(&c.ID, &c.ArtistID, &c.Source, &c.ExternalID, &c.DisplayName, &c.SortName, &c.Disambiguation, &c.Country, &c.ArtistType, &c.MBID, &c.Score, &evidence, &c.Status, &rawPageURL); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(evidence), &c.Evidence); err != nil {
			return nil, fmt.Errorf("decode candidate evidence: %w", err)
		}
		i, ok := positions[c.ArtistID]
		if !ok {
			continue
		}
		items[i].Candidates = append(items[i].Candidates, c)
		if extURL := candidateExternalURL(c.Source, c.ExternalID, c.MBID, c.DisplayName, rawPageURL); extURL != "" {
			if items[i].PageURLs == nil {
				items[i].PageURLs = map[int64]string{}
			}
			items[i].PageURLs[c.ID] = extURL
		}
	}
	if err = candidates.Err(); err != nil {
		return nil, err
	}
	if !isCredit {
		if err = s.attachBatchArtistIdentityConflicts(ctx, items); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// attachBatchArtistIdentityConflicts resolves identity conflicts for all
// candidates across a review page in one batch query instead of N+1 queries.
func (s *Store) attachBatchArtistIdentityConflicts(ctx context.Context, items []ArtistReviewItem) error {
	var candIDs []int64
	positions := make(map[int64]int, len(items))
	for i := range items {
		positions[items[i].Artist.ID] = i
		for _, c := range items[i].Candidates {
			candIDs = append(candIDs, c.ID)
		}
	}
	if len(candIDs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(candIDs)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.artist_id,c.source,c.external_id,c.requires_manual_review,p.artist_id,COALESCE(a.user_display_name,a.display_name)
		FROM artist_match_candidates c
		JOIN artist_external_profiles p ON p.source=c.source AND p.external_id=c.external_id
		JOIN artists a ON a.id=p.artist_id
		WHERE c.id IN (SELECT value FROM json_each(?)) AND c.status='candidate' AND p.artist_id<>c.artist_id`, string(encoded))
	if err != nil {
		return err
	}
	type rawConflict struct {
		artistID int64
		manual   int
		conflict ArtistIdentityConflict
	}
	var raw []rawConflict
	for rows.Next() {
		var rc rawConflict
		if err = rows.Scan(&rc.conflict.CandidateID, &rc.artistID, &rc.conflict.Source, &rc.conflict.ExternalID, &rc.manual, &rc.conflict.OwnerArtistID, &rc.conflict.OwnerName); err != nil {
			rows.Close()
			return err
		}
		raw = append(raw, rc)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(raw) == 0 {
		return nil
	}

	canonicalCache := map[int64]int64{}
	nameCache := map[int64]string{}
	for _, rc := range raw {
		pos, ok := positions[rc.artistID]
		if !ok {
			continue
		}
		targetID, cached := canonicalCache[rc.conflict.OwnerArtistID]
		if !cached {
			targetID, err = canonicalArtistID(ctx, s.db, rc.conflict.OwnerArtistID)
			if err != nil {
				continue
			}
			canonicalCache[rc.conflict.OwnerArtistID] = targetID
		}
		targetName, hasName := nameCache[targetID]
		if !hasName {
			if targetID == rc.conflict.OwnerArtistID {
				targetName = rc.conflict.OwnerName
			} else if qErr := s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=?`, targetID).Scan(&targetName); qErr != nil {
				continue
			}
			nameCache[targetID] = targetName
		}
		rc.conflict.TargetArtistID = targetID
		rc.conflict.TargetName = targetName
		sourceName := items[pos].Artist.Name
		rc.conflict.CanMerge = rc.manual == 0 && targetID != rc.artistID && !metadata.CompositeArtistCredit(targetName) && !metadata.CompositeArtistCredit(sourceName)
		if items[pos].Conflicts == nil {
			items[pos].Conflicts = map[int64]ArtistIdentityConflict{}
		}
		items[pos].Conflicts[rc.conflict.CandidateID] = rc.conflict
	}
	return nil
}
