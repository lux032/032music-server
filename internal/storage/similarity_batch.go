package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Batch feature rows are lightweight; full Track JSON is loaded only after ranking.
func (s *Store) similarityBatch(ctx context.Context, ids []int64) (map[int64]*similarityMeta, error) {
	result := make(map[int64]*similarityMeta, len(ids))
	for start := 0; start < len(ids); start += 500 {
		part := ids[start:min(start+500, len(ids))]
		in, args := inClause(part)
		rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.album_id,COALESCE(t.user_title,t.title),COALESCE(NULLIF(t.user_track_type,''),NULLIF(t.track_type,''),'regular'),COALESCE(a.user_release_year,a.release_year,0),COALESCE(pp.play_count,0),COALESCE(pp.skip_count,0) FROM tracks t JOIN albums a ON a.id=t.album_id LEFT JOIN playback_progress pp ON pp.track_id=t.id WHERE t.id IN (`+in+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			m := &similarityMeta{}
			if err = rows.Scan(&m.track.ID, &m.track.AlbumID, &m.track.Title, &m.track.TrackType, &m.track.Year, &m.playCount, &m.skipCount); err != nil {
				break
			}
			result[m.track.ID] = m
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, spec := range []struct {
			q     string
			apply func(*similarityMeta, int64)
		}{
			{`SELECT ta.track_id,COALESCE(ar.merged_into_artist_id,ar.id) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.role='primary' AND ta.track_id IN (` + in + `)`, func(m *similarityMeta, n int64) { m.primary = set(m.primary, n) }},
			{`SELECT ta.track_id,COALESCE(ar.merged_into_artist_id,ar.id) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.role IN ('composer','lyricist','arranger','producer') AND ta.track_id IN (` + in + `)`, func(m *similarityMeta, n int64) { m.credit = set(m.credit, n) }},
			{`SELECT track_id,genre_id FROM track_genre_overrides WHERE track_id IN (` + in + `)`, func(m *similarityMeta, n int64) { m.genre = set(m.genre, n) }},
			{`SELECT g.track_id,g.genre_id FROM track_genres g WHERE g.track_id IN (` + in + `) AND NOT EXISTS(SELECT 1 FROM track_genre_overrides o WHERE o.track_id=g.track_id)`, func(m *similarityMeta, n int64) { m.genre = set(m.genre, n) }},
			{`SELECT track_id,work_id FROM work_tracks WHERE track_id IN (` + in + `)`, func(m *similarityMeta, n int64) { m.work = set(m.work, n) }},
			{`SELECT track_id,playlist_id FROM playlist_items WHERE track_id IN (` + in + `)`, func(m *similarityMeta, n int64) { m.playlists = set(m.playlists, n) }},
			{`SELECT track_id,1 FROM audio_files WHERE status='available' AND track_id IN (` + in + `)`, func(m *similarityMeta, _ int64) { m.available = true }},
		} {
			rows, err = s.db.QueryContext(ctx, spec.q, args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id, n int64
				if err = rows.Scan(&id, &n); err != nil {
					break
				}
				if m := result[id]; m != nil {
					spec.apply(m, n)
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}
func (s *Store) similarityMeta(ctx context.Context, id int64) (similarityMeta, error) {
	m, e := s.similarityBatch(ctx, []int64{id})
	if e != nil {
		return similarityMeta{}, e
	}
	if m[id] == nil {
		return similarityMeta{}, sql.ErrNoRows
	}
	return *m[id], nil
}
func sortedKeys(m map[int64]bool) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
func (s *Store) aliasIDs(ctx context.Context, canonical map[int64]bool) ([]int64, error) {
	keys := sortedKeys(canonical)
	if len(keys) == 0 {
		return nil, nil
	}
	in, args := inClause(keys)
	rows, e := s.db.QueryContext(ctx, `SELECT id FROM artists WHERE id IN (`+in+`) OR merged_into_artist_id IN (`+in+`) ORDER BY id`, append(args, args...)...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func queryIDs(ctx context.Context, s *Store, q string, args ...any) ([]int64, error) {
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) similarityCandidates(ctx context.Context, m similarityMeta, small bool) ([]int64, error) {
	caps := []int{200, 200, 100, 300, 200}
	if small {
		caps = []int{50, 50, 30, 50, 50}
	}
	out := map[int64]bool{}
	add := func(ids []int64) {
		for _, id := range ids {
			if id != m.track.ID {
				out[id] = true
			}
		}
	}
	for index, values := range []map[int64]bool{m.primary, m.credit} {
		aliases, e := s.aliasIDs(ctx, values)
		if e != nil {
			return nil, e
		}
		if len(aliases) == 0 {
			continue
		}
		in, args := inClause(aliases)
		role := "ta.role='primary'"
		if index == 1 {
			role = "ta.role IN ('composer','lyricist','arranger','producer')"
		}
		q := `SELECT DISTINCT ta.track_id FROM track_artists ta WHERE ` + role + ` AND ta.artist_id IN (` + in + `) AND ta.track_id<>? ORDER BY ta.track_id LIMIT ?`
		ids, e := queryIDs(ctx, s, q, append(args, m.track.ID, caps[index])...)
		if e != nil {
			return nil, e
		}
		add(ids)
	}
	collect := func(q string, keys map[int64]bool, cap int) error {
		used := 0
		for i, key := range sortedKeys(keys) {
			if i >= 50 || used >= cap {
				break
			}
			ids, e := queryIDs(ctx, s, q, key, m.track.ID, cap-used)
			if e != nil {
				return e
			}
			add(ids)
			used += len(ids)
		}
		return nil
	}
	if e := collect(`SELECT DISTINCT track_id FROM work_tracks WHERE work_id=? AND track_id<>? ORDER BY track_id LIMIT ?`, m.work, caps[2]); e != nil {
		return nil, e
	}
	used := 0
	for _, key := range sortedKeys(m.genre) {
		if used >= caps[3] {
			break
		}
		for _, variant := range []string{`SELECT track_id FROM track_genre_overrides WHERE genre_id=? AND track_id>=? AND track_id<>? ORDER BY track_id LIMIT ?`, `SELECT track_id FROM track_genres WHERE genre_id=? AND track_id>=? AND track_id<>? ORDER BY track_id LIMIT ?`, `SELECT track_id FROM track_genre_overrides WHERE genre_id=? AND track_id<? AND track_id<>? ORDER BY track_id LIMIT ?`, `SELECT track_id FROM track_genres WHERE genre_id=? AND track_id<? AND track_id<>? ORDER BY track_id LIMIT ?`} {
			if used >= caps[3] {
				break
			}
			ids, e := queryIDs(ctx, s, variant, key, m.track.ID, m.track.ID, caps[3]-used)
			if e != nil {
				return nil, e
			}
			for _, id := range ids {
				if !out[id] {
					used++
				}
			}
			add(ids)
		}
	}
	if e := collect(`SELECT track_id FROM playlist_items WHERE playlist_id=? AND track_id<>? ORDER BY position LIMIT ?`, m.playlists, caps[4]); e != nil {
		return nil, e
	}
	return sortedKeys(out), nil
}

type scoredCandidate struct {
	id      int64
	score   float64
	reasons []string
	meta    *similarityMeta
}

func primaryKey(m *similarityMeta) string {
	if len(m.primary) == 0 {
		return "unknown"
	}
	return fmt.Sprint(sortedKeys(m.primary))
}
func (s *Store) rankedCandidates(ctx context.Context, seed similarityMeta, small bool) ([]scoredCandidate, error) {
	ids, e := s.similarityCandidates(ctx, seed, small)
	if e != nil {
		return nil, e
	}
	batch, e := s.similarityBatch(ctx, ids)
	if e != nil {
		return nil, e
	}
	ranked := []scoredCandidate{}
	for _, id := range ids {
		m := batch[id]
		if m == nil || !m.available || (!nonMain(seed.track.TrackType) && nonMain(m.track.TrackType)) {
			continue
		}
		score, reasons := compareSimilarity(seed, *m)
		ranked = append(ranked, scoredCandidate{id, score, reasons, m})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
	return ranked, nil
}
func dedupeCandidates(seed similarityMeta, ranked []scoredCandidate, limit int) []scoredCandidate {
	seen := map[string]bool{normalizedTitle(seed.track.Title) + "\x00" + primaryKey(&seed): true}
	out := []scoredCandidate{}
	for _, r := range ranked {
		key := normalizedTitle(r.meta.track.Title) + "\x00" + primaryKey(r.meta)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out
}
func (s *Store) SimilarTracks(ctx context.Context, id int64, limit int) ([]SimilarTrack, error) {
	seed, e := s.similarityMeta(ctx, id)
	if e != nil {
		return nil, e
	}
	ranked, e := s.rankedCandidates(ctx, seed, false)
	if e != nil {
		return nil, e
	}
	ranked = dedupeCandidates(seed, ranked, limit)
	ids := make([]int64, len(ranked))
	for i, r := range ranked {
		ids[i] = r.id
	}
	tracks, e := s.tracksByIDs(ctx, ids)
	if e != nil {
		return nil, e
	}
	out := make([]SimilarTrack, 0, len(ids))
	for i, r := range ranked {
		out = append(out, SimilarTrack{Track: tracks[i], Score: r.score, Distance: 1 - r.score, Reasons: r.reasons})
	}
	return out, nil
}
