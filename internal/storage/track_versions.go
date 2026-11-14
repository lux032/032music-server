package storage

import (
	"context"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
)

// albumDiscCountSQL counts distinct visible disc numbers on the album instead of
// trusting external enrichment or DISCTOTAL tags that may exceed local files.
var albumDiscCountSQL = `COALESCE(NULLIF((SELECT COUNT(DISTINCT COALESCE(NULLIF(t.user_disc_number,0),NULLIF(t.disc_number,0),1)) FROM tracks t WHERE t.album_id=a.id AND ` + trackVisibleSQL("t.id") + `),0),a.disc_count,1)`

const (
	trackVersionsLimit              = 200
	versionDurationMinToleranceMs   = int64(10_000)
	versionDurationTolerancePercent = int64(5)
)

type versionTrackMeta struct {
	id         int64
	titleKey   string
	durationMs int64
	composers  map[string]struct{}
	lyricists  map[string]struct{}
	arrangers  map[string]struct{}
	primaries  map[string]struct{}
	works      map[int64]struct{}
}

type rawTrackVersionRow struct {
	id           int64
	titleKey     string
	durationMs   int64
	userComposer string
	rawComposer  string
	rawLyricist  string
	rawArranger  string
}

// asciiLowerTrim matches SQLite's LOWER(TRIM(...)), which folds only ASCII A-Z.
func asciiLowerTrim(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			b.WriteByte(c + ('a' - 'A'))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func canonicalArtistToken(artistID int64) string {
	return "id:" + strconv.FormatInt(artistID, 10)
}

func addToken(set *map[string]struct{}, token string) {
	if token == "" {
		return
	}
	if *set == nil {
		*set = make(map[string]struct{})
	}
	(*set)[token] = struct{}{}
}

func addWorkID(set *map[int64]struct{}, workID int64) {
	if workID <= 0 {
		return
	}
	if *set == nil {
		*set = make(map[int64]struct{})
	}
	(*set)[workID] = struct{}{}
}

func hasStringIntersection(a, b map[string]struct{}) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	for k := range a {
		if _, ok := b[k]; ok {
			return true
		}
	}
	return false
}

func hasInt64Intersection(a, b map[int64]struct{}) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	for k := range a {
		if _, ok := b[k]; ok {
			return true
		}
	}
	return false
}

func versionDurationsClose(aMs, bMs int64) bool {
	if aMs <= 0 || bMs <= 0 {
		return false
	}
	longer := max(aMs, bMs)
	diff := max(aMs, bMs) - min(aMs, bMs)
	tolerance := max(versionDurationMinToleranceMs, (longer*versionDurationTolerancePercent)/100)
	return diff <= tolerance
}

// isSameTrackVersion decides whether two same-title tracks belong to the same
// musical piece using tiered metadata matching:
//  1. Composer intersection (disjoint composers reject immediately).
//  2. Lyricist intersection when composer is missing on at least one side
//     (disjoint lyricists reject immediately).
//  3. Arranger intersection as positive evidence when Tier 1 is inconclusive
//     (disjoint arrangers fall through rather than rejecting).
//  4. Fallback when credits are insufficient: shared primary artist or shared
//     work plus close duration (max(10s, 5% of longer)).
func isSameTrackVersion(a, b versionTrackMeta) bool {
	if a.id == b.id {
		return true
	}
	if len(a.composers) > 0 && len(b.composers) > 0 {
		return hasStringIntersection(a.composers, b.composers)
	}
	if len(a.lyricists) > 0 && len(b.lyricists) > 0 {
		return hasStringIntersection(a.lyricists, b.lyricists)
	}
	if len(a.arrangers) > 0 && len(b.arrangers) > 0 && hasStringIntersection(a.arrangers, b.arrangers) {
		return true
	}
	if !hasStringIntersection(a.primaries, b.primaries) && !hasInt64Intersection(a.works, b.works) {
		return false
	}
	return versionDurationsClose(a.durationMs, b.durationMs)
}

func collectPendingCreatorKeys(raw string, pending map[string]struct{}) {
	for _, name := range metadata.SplitPeople(raw) {
		if norm := metadata.Normalize(name); norm != "" {
			pending[norm] = struct{}{}
		}
	}
}

func splitCreatorTokens(raw string, known map[string]int64) map[string]struct{} {
	var tokens map[string]struct{}
	for _, name := range metadata.SplitPeople(raw) {
		norm := metadata.Normalize(name)
		if norm == "" {
			continue
		}
		if id, ok := known[norm]; ok {
			addToken(&tokens, canonicalArtistToken(id))
		} else {
			addToken(&tokens, "name:"+norm)
		}
	}
	return tokens
}

func (s *Store) resolveCanonicalArtistsByIdentityKeys(ctx context.Context, keys []string) (map[string]int64, error) {
	resolved := make(map[string]int64, len(keys))
	if len(keys) == 0 {
		return resolved, nil
	}
	for start := 0; start < len(keys); start += 200 {
		end := min(start+200, len(keys))
		chunk := keys[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, k := range chunk {
			args[i] = k
		}
		rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE resolved(key, id, next) AS (
			SELECT identity_key, id, merged_into_artist_id FROM artists WHERE identity_key IN (`+placeholders+`)
			UNION
			SELECT r.key, a.id, a.merged_into_artist_id FROM resolved r JOIN artists a ON a.id=r.next
		)
		SELECT key, id FROM resolved WHERE next IS NULL`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var id int64
			if err = rows.Scan(&key, &id); err != nil {
				rows.Close()
				return nil, err
			}
			resolved[key] = id
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return resolved, nil
}

func (s *Store) loadVersionTrackMetas(ctx context.Context, ids []int64) (map[int64]versionTrackMeta, error) {
	uniqueIDs := make([]int64, 0, len(ids))
	seenID := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seenID[id]; ok {
			continue
		}
		seenID[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	metas := make(map[int64]versionTrackMeta, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return metas, nil
	}

	rawRows := make(map[int64]rawTrackVersionRow, len(uniqueIDs))
	for start := 0; start < len(uniqueIDs); start += 200 {
		end := min(start+200, len(uniqueIDs))
		chunk := uniqueIDs[start:end]
		placeholders, args := inClause(chunk)

		tRows, err := s.db.QueryContext(ctx, `SELECT t.id, LOWER(TRIM(COALESCE(t.user_title,t.title))), COALESCE(t.duration_ms,0), COALESCE(t.user_composer,''), COALESCE(t.composer,''), COALESCE(t.lyricist,''), COALESCE(t.arranger,'') FROM tracks t WHERE t.id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for tRows.Next() {
			var r rawTrackVersionRow
			if err = tRows.Scan(&r.id, &r.titleKey, &r.durationMs, &r.userComposer, &r.rawComposer, &r.rawLyricist, &r.rawArranger); err != nil {
				tRows.Close()
				return nil, err
			}
			rawRows[r.id] = r
			metas[r.id] = versionTrackMeta{
				id:         r.id,
				titleKey:   r.titleKey,
				durationMs: r.durationMs,
			}
		}
		if err = tRows.Err(); err != nil {
			tRows.Close()
			return nil, err
		}
		tRows.Close()

		// Resolve merged artists to their canonical IDs for track-level roles.
		taQueryArgs := make([]any, 0, len(args)*2)
		taQueryArgs = append(taQueryArgs, args...)
		taQueryArgs = append(taQueryArgs, args...)
		taRows, err := s.db.QueryContext(ctx, `WITH RECURSIVE resolved(old, id, next) AS (
			SELECT id, id, merged_into_artist_id FROM artists
			WHERE id IN (SELECT DISTINCT artist_id FROM track_artists WHERE track_id IN (`+placeholders+`) AND role IN ('primary','composer','lyricist','arranger'))
			UNION
			SELECT r.old, a.id, a.merged_into_artist_id FROM resolved r JOIN artists a ON a.id=r.next
		)
		SELECT ta.track_id, ta.role, r.id
		FROM track_artists ta
		JOIN resolved r ON r.old=ta.artist_id AND r.next IS NULL
		WHERE ta.track_id IN (`+placeholders+`) AND ta.role IN ('primary','composer','lyricist','arranger')`, taQueryArgs...)
		if err != nil {
			return nil, err
		}
		for taRows.Next() {
			var trackID, artistID int64
			var role string
			if err = taRows.Scan(&trackID, &role, &artistID); err != nil {
				taRows.Close()
				return nil, err
			}
			m, ok := metas[trackID]
			if !ok {
				continue
			}
			tok := canonicalArtistToken(artistID)
			switch role {
			case "primary":
				addToken(&m.primaries, tok)
			case "composer":
				addToken(&m.composers, tok)
			case "lyricist":
				addToken(&m.lyricists, tok)
			case "arranger":
				addToken(&m.arrangers, tok)
			}
			metas[trackID] = m
		}
		if err = taRows.Err(); err != nil {
			taRows.Close()
			return nil, err
		}
		taRows.Close()

		wRows, err := s.db.QueryContext(ctx, `SELECT track_id, work_id FROM work_tracks WHERE track_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for wRows.Next() {
			var trackID, workID int64
			if err = wRows.Scan(&trackID, &workID); err != nil {
				wRows.Close()
				return nil, err
			}
			if m, ok := metas[trackID]; ok {
				addWorkID(&m.works, workID)
				metas[trackID] = m
			}
		}
		if err = wRows.Err(); err != nil {
			wRows.Close()
			return nil, err
		}
		wRows.Close()
	}

	// Fallback to album_artists for tracks that lack primary track_artists rows.
	var missingPrimaryIDs []int64
	for _, id := range uniqueIDs {
		if m, ok := metas[id]; ok && len(m.primaries) == 0 {
			missingPrimaryIDs = append(missingPrimaryIDs, id)
		}
	}
	for start := 0; start < len(missingPrimaryIDs); start += 200 {
		end := min(start+200, len(missingPrimaryIDs))
		chunk := missingPrimaryIDs[start:end]
		placeholders, args := inClause(chunk)
		aaArgs := make([]any, 0, len(args)*2)
		aaArgs = append(aaArgs, args...)
		aaArgs = append(aaArgs, args...)
		aaRows, err := s.db.QueryContext(ctx, `WITH RECURSIVE resolved(old, id, next) AS (
			SELECT id, id, merged_into_artist_id FROM artists
			WHERE id IN (SELECT DISTINCT aa.artist_id FROM tracks t JOIN album_artists aa ON aa.album_id=t.album_id WHERE t.id IN (`+placeholders+`))
			UNION
			SELECT r.old, a.id, a.merged_into_artist_id FROM resolved r JOIN artists a ON a.id=r.next
		)
		SELECT t.id, r.id
		FROM tracks t
		JOIN album_artists aa ON aa.album_id=t.album_id
		JOIN resolved r ON r.old=aa.artist_id AND r.next IS NULL
		WHERE t.id IN (`+placeholders+`)`, aaArgs...)
		if err != nil {
			return nil, err
		}
		for aaRows.Next() {
			var trackID, artistID int64
			if err = aaRows.Scan(&trackID, &artistID); err != nil {
				aaRows.Close()
				return nil, err
			}
			if m, ok := metas[trackID]; ok {
				addToken(&m.primaries, canonicalArtistToken(artistID))
				metas[trackID] = m
			}
		}
		if err = aaRows.Err(); err != nil {
			aaRows.Close()
			return nil, err
		}
		aaRows.Close()
	}

	// Collect any user_composer overrides or legacy raw creator text columns.
	pendingKeys := make(map[string]struct{})
	for _, r := range rawRows {
		m := metas[r.id]
		if strings.TrimSpace(r.userComposer) != "" {
			collectPendingCreatorKeys(r.userComposer, pendingKeys)
		} else if len(m.composers) == 0 && strings.TrimSpace(r.rawComposer) != "" {
			collectPendingCreatorKeys(r.rawComposer, pendingKeys)
		}
		if len(m.lyricists) == 0 && strings.TrimSpace(r.rawLyricist) != "" {
			collectPendingCreatorKeys(r.rawLyricist, pendingKeys)
		}
		if len(m.arrangers) == 0 && strings.TrimSpace(r.rawArranger) != "" {
			collectPendingCreatorKeys(r.rawArranger, pendingKeys)
		}
	}
	if len(pendingKeys) > 0 {
		keysSlice := make([]string, 0, len(pendingKeys))
		for k := range pendingKeys {
			keysSlice = append(keysSlice, k)
		}
		known, err := s.resolveCanonicalArtistsByIdentityKeys(ctx, keysSlice)
		if err != nil {
			return nil, err
		}
		for id, r := range rawRows {
			m := metas[id]
			if strings.TrimSpace(r.userComposer) != "" {
				// Explicit user edit overrides scanned track_artists composer credits.
				m.composers = splitCreatorTokens(r.userComposer, known)
			} else if len(m.composers) == 0 && strings.TrimSpace(r.rawComposer) != "" {
				m.composers = splitCreatorTokens(r.rawComposer, known)
			}
			if len(m.lyricists) == 0 && strings.TrimSpace(r.rawLyricist) != "" {
				m.lyricists = splitCreatorTokens(r.rawLyricist, known)
			}
			if len(m.arrangers) == 0 && strings.TrimSpace(r.rawArranger) != "" {
				m.arrangers = splitCreatorTokens(r.rawArranger, known)
			}
			metas[id] = m
		}
	}

	return metas, nil
}

func (s *Store) hydrateTrackVersionCounts(ctx context.Context, items []Track) error {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	keys := make([]string, 0, len(items))
	for _, item := range items {
		k := asciiLowerTrim(item.Title)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}

	dupKeys := make([]string, 0, len(keys))
	for start := 0; start < len(keys); start += 200 {
		end := min(start+200, len(keys))
		chunk := keys[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, k := range chunk {
			args[i] = k
		}
		rows, err := s.db.QueryContext(ctx, `SELECT LOWER(TRIM(COALESCE(t.user_title,t.title))) FROM tracks t WHERE `+trackVisibleSQL("t.id")+` AND LOWER(TRIM(COALESCE(t.user_title,t.title))) IN (`+placeholders+`) GROUP BY LOWER(TRIM(COALESCE(t.user_title,t.title))) HAVING COUNT(*) > 1`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var title string
			if err = rows.Scan(&title); err != nil {
				rows.Close()
				return err
			}
			dupKeys = append(dupKeys, title)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if len(dupKeys) == 0 {
		return nil
	}

	candidateIDs := make([]int64, 0, len(dupKeys)*2)
	for start := 0; start < len(dupKeys); start += 200 {
		end := min(start+200, len(dupKeys))
		chunk := dupKeys[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, k := range chunk {
			args[i] = k
		}
		rows, err := s.db.QueryContext(ctx, `SELECT t.id FROM tracks t WHERE `+trackVisibleSQL("t.id")+` AND LOWER(TRIM(COALESCE(t.user_title,t.title))) IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			candidateIDs = append(candidateIDs, id)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if len(candidateIDs) == 0 {
		return nil
	}

	metas, err := s.loadVersionTrackMetas(ctx, candidateIDs)
	if err != nil {
		return err
	}
	groups := make(map[string][]versionTrackMeta, len(dupKeys))
	for _, id := range candidateIDs {
		if m, ok := metas[id]; ok && m.titleKey != "" {
			groups[m.titleKey] = append(groups[m.titleKey], m)
		}
	}

	for i := range items {
		key := asciiLowerTrim(items[i].Title)
		group := groups[key]
		if len(group) <= 1 {
			continue
		}
		seed, ok := metas[items[i].ID]
		if !ok {
			continue
		}
		count := 0
		for _, cand := range group {
			if isSameTrackVersion(seed, cand) {
				count++
				if count >= trackVersionsLimit {
					break
				}
			}
		}
		if count > 1 {
			items[i].VersionCount = count
		}
	}
	return nil
}

// TrackVersions returns the seed track and all visible tracks representing
// different versions of the same musical piece across the library, ordered by
// release year and album.
func (s *Store) TrackVersions(ctx context.Context, trackID int64) (Track, []Track, error) {
	seed, err := s.TrackByID(ctx, trackID)
	if err != nil {
		return Track{}, nil, err
	}
	key := asciiLowerTrim(seed.Title)
	if key == "" {
		return seed, []Track{seed}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id FROM tracks t JOIN albums a ON a.id=t.album_id WHERE `+trackVisibleSQL("t.id")+` AND LOWER(TRIM(COALESCE(t.user_title,t.title)))=? ORDER BY COALESCE(a.user_release_year,a.release_year,0) ASC, a.id ASC, COALESCE(t.user_disc_number,t.disc_number) ASC, COALESCE(t.user_track_number,t.track_number) ASC, t.id ASC`, key)
	if err != nil {
		return Track{}, nil, err
	}
	defer rows.Close()
	var candidateIDs []int64
	hasSeed := false
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return Track{}, nil, err
		}
		if id == seed.ID {
			hasSeed = true
		}
		candidateIDs = append(candidateIDs, id)
	}
	if err = rows.Err(); err != nil {
		return Track{}, nil, err
	}
	rows.Close()
	if len(candidateIDs) == 0 {
		return seed, []Track{seed}, nil
	}

	metaIDs := candidateIDs
	if !hasSeed {
		metaIDs = append(append([]int64(nil), candidateIDs...), seed.ID)
	}
	metas, err := s.loadVersionTrackMetas(ctx, metaIDs)
	if err != nil {
		return Track{}, nil, err
	}
	seedMeta, hasSeedMeta := metas[seed.ID]
	matchedIDs := make([]int64, 0, min(len(candidateIDs), trackVersionsLimit))
	for _, id := range candidateIDs {
		candMeta, hasCandMeta := metas[id]
		if hasSeedMeta && hasCandMeta && !isSameTrackVersion(seedMeta, candMeta) {
			continue
		}
		matchedIDs = append(matchedIDs, id)
		if len(matchedIDs) >= trackVersionsLimit {
			break
		}
	}
	if len(matchedIDs) == 0 {
		return seed, []Track{seed}, nil
	}
	items, err := s.tracksByIDs(ctx, matchedIDs)
	if err != nil {
		return Track{}, nil, err
	}
	if err := s.AttachTrackAlbumArtistRefs(ctx, items); err != nil {
		return Track{}, nil, err
	}
	return seed, items, nil
}
