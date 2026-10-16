package storage

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Acoustic index: the effnet embeddings of the latest analysis version, kept in
// memory for hybrid similarity and daily mixes. Vectors are mean-centred over the
// library (raw effnet embeddings are all positive, so plain cosine barely separates
// tracks), L2-normalised and quantised to int8: 1280 bytes per track, about 22 MB
// for 17k tracks. A dot product of two quantised vectors divided by 127² is the
// cosine similarity within ~1%.
//
// The snapshot is immutable; it is rebuilt when the analysed set changes (checked at
// most every acousticCheckInterval) and swapped atomically.

const acousticCheckInterval = 30 * time.Second

// acousticRebuildInterval spaces out rebuilds while the analysis is still adding
// tracks (a rebuild decodes every embedding; seconds on a NAS).
const acousticRebuildInterval = 10 * time.Minute

// AcousticSnapshot is an immutable view of the acoustic index.
type AcousticSnapshot struct {
	Version string
	dims    int
	ids     []int64
	pos     map[int64]int
	vecs    []int8
	stamp   string
}

type acousticCache struct {
	mu        sync.Mutex
	snap      *AcousticSnapshot
	checkedAt time.Time
	builtAt   time.Time
}

// Len returns the number of indexed tracks.
func (a *AcousticSnapshot) Len() int {
	if a == nil {
		return 0
	}
	return len(a.ids)
}

// Has reports whether the track has an embedding.
func (a *AcousticSnapshot) Has(id int64) bool {
	if a == nil {
		return false
	}
	_, ok := a.pos[id]
	return ok
}

func (a *AcousticSnapshot) row(i int) []int8 { return a.vecs[i*a.dims : (i+1)*a.dims] }

func dotI8(x, y []int8) int32 {
	var sum int32
	for i := range x {
		sum += int32(x[i]) * int32(y[i])
	}
	return sum
}

// Cosine returns the cosine similarity of two tracks' centred embeddings.
func (a *AcousticSnapshot) Cosine(x, y int64) (float64, bool) {
	if a == nil {
		return 0, false
	}
	i, ok1 := a.pos[x]
	j, ok2 := a.pos[y]
	if !ok1 || !ok2 {
		return 0, false
	}
	return float64(dotI8(a.row(i), a.row(j))) / (127 * 127), true
}

// AcousticScore maps a cosine of centred embeddings to 0–1 (0 below 0.2, 1 at 0.8+).
func AcousticScore(cos float64) float64 { return clamp((cos - 0.2) / 0.6) }

// Neighbor is one acoustic neighbour.
type Neighbor struct {
	TrackID int64
	Cosine  float64
}

// Neighbors returns the k nearest tracks to id (excluding itself), best first.
func (a *AcousticSnapshot) Neighbors(id int64, k int, keep func(int64) bool) []Neighbor {
	if a == nil {
		return nil
	}
	i, ok := a.pos[id]
	if !ok {
		return nil
	}
	return a.nearest(a.row(i), k, func(t int64) bool { return t != id && (keep == nil || keep(t)) })
}

// Centroid returns the normalised, quantised mean of the given tracks (nil when none indexed).
func (a *AcousticSnapshot) Centroid(ids []int64) []int8 {
	if a == nil {
		return nil
	}
	sum := make([]float64, a.dims)
	n := 0
	for _, id := range ids {
		i, ok := a.pos[id]
		if !ok {
			continue
		}
		for d, v := range a.row(i) {
			sum[d] += float64(v)
		}
		n++
	}
	if n == 0 {
		return nil
	}
	return quantize(sum)
}

// CosineTo returns the cosine between a track and a centroid.
func (a *AcousticSnapshot) CosineTo(id int64, centroid []int8) (float64, bool) {
	if a == nil || centroid == nil {
		return 0, false
	}
	i, ok := a.pos[id]
	if !ok {
		return 0, false
	}
	return float64(dotI8(a.row(i), centroid)) / (127 * 127), true
}

func (a *AcousticSnapshot) nearest(q []int8, k int, keep func(int64) bool) []Neighbor {
	if k <= 0 {
		return nil
	}
	best := make([]Neighbor, 0, k+1)
	for i, id := range a.ids {
		if !keep(id) {
			continue
		}
		c := float64(dotI8(a.row(i), q)) / (127 * 127)
		if len(best) == k && c <= best[k-1].Cosine {
			continue
		}
		at := sort.Search(len(best), func(j int) bool { return best[j].Cosine < c })
		best = append(best, Neighbor{})
		copy(best[at+1:], best[at:])
		best[at] = Neighbor{id, c}
		if len(best) > k {
			best = best[:k]
		}
	}
	return best
}

func quantize(v []float64) []int8 {
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	out := make([]int8, len(v))
	if norm == 0 {
		return out
	}
	norm = math.Sqrt(norm)
	for i, x := range v {
		out[i] = int8(math.Round(x / norm * 127))
	}
	return out
}

func f16(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch exp {
	case 0:
		if frac == 0 {
			return math.Float32frombits(sign)
		}
		e := uint32(127 - 15 + 1)
		for frac&0x400 == 0 {
			frac <<= 1
			e--
		}
		return math.Float32frombits(sign | e<<23 | (frac&0x3ff)<<13)
	case 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | frac<<13)
}

// AcousticIndex returns the current snapshot (nil when no track has been analysed).
func (s *Store) AcousticIndex(ctx context.Context) (*AcousticSnapshot, error) {
	c := &s.acoustic
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snap != nil && time.Since(c.checkedAt) < acousticCheckInterval {
		return c.snap, nil
	}
	version, err := s.LatestAudioAnalysisVersion(ctx)
	if err != nil || version == "" {
		c.snap, c.checkedAt = nil, time.Now()
		return nil, err
	}
	var count int64
	var latest string
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(analyzed_at), '') FROM track_audio_features WHERE analysis_version = ?`, version).Scan(&count, &latest); err != nil {
		return nil, err
	}
	stamp := version + "|" + latest + "|" + strconv.FormatInt(count, 10)
	c.checkedAt = time.Now()
	if c.snap != nil && (c.snap.stamp == stamp || time.Since(c.builtAt) < acousticRebuildInterval) {
		return c.snap, nil
	}
	if count == 0 {
		c.snap = nil
		return nil, nil
	}
	snap, err := s.buildAcoustic(ctx, version, stamp)
	if err != nil {
		return nil, err
	}
	c.snap, c.builtAt = snap, time.Now()
	return snap, nil
}

func (s *Store) buildAcoustic(ctx context.Context, version, stamp string) (*AcousticSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT track_id, embedding FROM track_audio_features WHERE analysis_version = ? ORDER BY track_id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	var raw []float32
	dims := 0
	for rows.Next() {
		var id int64
		var blob []byte
		if err = rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		if len(blob) < 4 || (dims != 0 && len(blob) != 2*dims) {
			continue
		}
		if dims == 0 {
			dims = len(blob) / 2
		}
		for i := 0; i < dims; i++ {
			raw = append(raw, f16(binary.LittleEndian.Uint16(blob[2*i:])))
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	mean := make([]float64, dims)
	for i := range ids {
		for d := 0; d < dims; d++ {
			mean[d] += float64(raw[i*dims+d])
		}
	}
	for d := range mean {
		mean[d] /= float64(len(ids))
	}
	snap := &AcousticSnapshot{Version: version, dims: dims, ids: ids, pos: make(map[int64]int, len(ids)), vecs: make([]int8, len(ids)*dims), stamp: stamp}
	tmp := make([]float64, dims)
	for i, id := range ids {
		for d := 0; d < dims; d++ {
			tmp[d] = float64(raw[i*dims+d]) - mean[d]
		}
		copy(snap.vecs[i*dims:], quantize(tmp))
		snap.pos[id] = i
	}
	return snap, nil
}

// NewAcousticSnapshot builds a snapshot from raw vectors (tests and benchmarks).
func NewAcousticSnapshot(ids []int64, vecs [][]float32) *AcousticSnapshot {
	if len(ids) == 0 {
		return nil
	}
	dims := len(vecs[0])
	snap := &AcousticSnapshot{dims: dims, ids: ids, pos: make(map[int64]int, len(ids)), vecs: make([]int8, len(ids)*dims)}
	mean := make([]float64, dims)
	for _, v := range vecs {
		for d, x := range v {
			mean[d] += float64(x) / float64(len(vecs))
		}
	}
	tmp := make([]float64, dims)
	for i, v := range vecs {
		for d, x := range v {
			tmp[d] = float64(x) - mean[d]
		}
		copy(snap.vecs[i*dims:], quantize(tmp))
		snap.pos[ids[i]] = i
	}
	return snap
}
