package storage

import (
	"context"
	"sort"
	"time"
)

// TrackPath searches a width-three beam; a request-local cache avoids repeated neighborhoods.
func (s *Store) TrackPath(ctx context.Context, from, to int64, limit int, budget time.Duration) ([]Track, bool, error) {
	start, e := s.TrackByID(ctx, from)
	if e != nil {
		return nil, false, e
	}
	_, e = s.TrackByID(ctx, to)
	if e != nil {
		return nil, false, e
	}
	if from == to {
		return []Track{start}, true, nil
	}
	target, e := s.similarityMeta(ctx, to)
	if e != nil {
		return nil, false, e
	}
	seed, e := s.similarityMeta(ctx, from)
	if e != nil {
		return nil, false, e
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type branch struct {
		ids  []int64
		rank float64
	}
	beam := []branch{{ids: []int64{from}}}
	best := beam[0]
	nodes := map[int64]similarityMeta{from: seed, to: target}
	neighbors := map[int64][]scoredCandidate{}
	for step := 0; step < limit-1; step++ {
		next := []branch{}
		for _, b := range beam {
			if ctx.Err() != nil {
				break
			}
			cur := b.ids[len(b.ids)-1]
			candidates, ok := neighbors[cur]
			if !ok {
				m := nodes[cur]
				ranked, err := s.rankedCandidates(ctx, m, true)
				if err != nil {
					if ctx.Err() != nil {
						break
					}
					return nil, false, err
				}
				candidates = dedupeCandidates(m, ranked, 50)
				neighbors[cur] = candidates
			}
			for _, candidate := range candidates {
				if ctx.Err() != nil {
					break
				}
				visited := false
				for _, v := range b.ids {
					if v == candidate.id {
						visited = true
						break
					}
				}
				if visited {
					continue
				}
				nodes[candidate.id] = *candidate.meta
				toward, _ := compareSimilarity(*candidate.meta, target)
				nb := branch{ids: append(append([]int64{}, b.ids...), candidate.id), rank: b.rank + .5*(candidate.score+toward)}
				if candidate.id == to {
					tracks, err := s.tracksByIDs(context.WithoutCancel(ctx), nb.ids)
					if err != nil {
						return nil, false, err
					}
					return tracks, true, nil
				}
				if len(nb.ids) < limit {
					next = append(next, nb)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		sort.Slice(next, func(i, j int) bool {
			if next[i].rank == next[j].rank {
				return next[i].ids[len(next[i].ids)-1] < next[j].ids[len(next[j].ids)-1]
			}
			return next[i].rank > next[j].rank
		})
		if len(next) > 3 {
			next = next[:3]
		}
		beam = next
		best = beam[0]
	}
	if ctx.Err() != nil && ctx.Err() != context.DeadlineExceeded {
		return nil, false, ctx.Err()
	}
	ids := append(append([]int64{}, best.ids...), to)
	if len(ids) > limit {
		ids = append(ids[:limit-1], to)
	}
	tracks, e := s.tracksByIDs(context.WithoutCancel(ctx), ids)
	return tracks, false, e
}
