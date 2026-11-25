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
	mu            sync.Mutex
	snap          *AcousticSnapshot
	checkedAt     time.Time
	builtAt       time.Time
	hasEmpty      bool
	rebuilding    bool
	buildDone     chan struct{}
	buildErr      error
	testBuildHook func()
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

// IndexOf returns the dense row index of track id in the snapshot, or (-1, false) when absent.
func (a *AcousticSnapshot) IndexOf(id int64) (int, bool) {
	if a == nil {
		return -1, false
	}
	i, ok := a.pos[id]
	if !ok {
		return -1, false
	}
	return i, true
}

func dotI8(x, y []int8) int32 {
	n := len(x)
	if n == 0 || len(y) < n {
		var sum int32
		for i := 0; i < n && i < len(y); i++ {
			sum += int32(x[i]) * int32(y[i])
		}
		return sum
	}
	_ = y[n-1]
	var s0, s1, s2, s3 int32
	i := 0
	for ; i+3 < n; i += 4 {
		s0 += int32(x[i]) * int32(y[i])
		s1 += int32(x[i+1]) * int32(y[i+1])
		s2 += int32(x[i+2]) * int32(y[i+2])
		s3 += int32(x[i+3]) * int32(y[i+3])
	}
	sum := (s0 + s1) + (s2 + s3)
	for ; i < n; i++ {
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

// CosineToIndex returns the cosine between the track at dense index idx (from IndexOf)
// and a centroid without a map lookup.
func (a *AcousticSnapshot) CosineToIndex(idx int, centroid []int8) (float64, bool) {
	if a == nil || centroid == nil || idx < 0 || idx >= len(a.ids) {
		return 0, false
	}
	return float64(dotI8(a.row(idx), centroid)) / (127 * 127), true
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
	out := make([]int8, len(v))
	quantizeInto(out, v)
	return out
}

func quantizeInto(out []int8, v []float64) {
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if norm == 0 {
		clear(out)
		return
	}
	norm = math.Sqrt(norm)
	for i, x := range v {
		out[i] = int8(math.Round(x / norm * 127))
	}
}

var f16Table = func() [65536]float32 {
	var tbl [65536]float32
	for i := 0; i < 65536; i++ {
		tbl[i] = f16Decode(uint16(i))
	}
	return tbl
}()

func f16(h uint16) float32 { return f16Table[h] }

func f16Decode(h uint16) float32 {
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

// invalidateAcousticEmpty clears the negative empty-library cache once an
// embedding is saved so HasAcousticFeatures and AcousticIndex immediately see
// the first analysed track without waiting for acousticCheckInterval.
func (s *Store) invalidateAcousticEmpty() {
	c := &s.acoustic
	c.mu.Lock()
	if c.hasEmpty {
		c.hasEmpty = false
		c.checkedAt = time.Time{}
	}
	c.mu.Unlock()
}

// HasAcousticFeatures cheaply checks whether any analysed acoustic embeddings exist,
// without triggering an index build.
func (s *Store) HasAcousticFeatures(ctx context.Context) bool {
	c := &s.acoustic
	c.mu.Lock()
	if c.snap != nil && len(c.snap.ids) > 0 {
		c.mu.Unlock()
		return true
	}
	if c.hasEmpty && !c.checkedAt.IsZero() && time.Since(c.checkedAt) < acousticCheckInterval {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()
	var one int
	return s.db.QueryRowContext(ctx, `SELECT 1 FROM track_audio_vectors WHERE length(embedding) >= 4 LIMIT 1`).Scan(&one) == nil
}

// AcousticIndex returns the current snapshot (nil when no track has been analysed).
// Transient errors or caller timeouts never discard a valid cached snapshot, empty
// library state is negatively cached for acousticCheckInterval, and rebuild I/O
// runs outside the cache mutex so concurrent readers serving an existing snapshot
// never stall.
func (s *Store) AcousticIndex(ctx context.Context) (*AcousticSnapshot, error) {
	c := &s.acoustic
	for {
		c.mu.Lock()
		if !c.checkedAt.IsZero() && time.Since(c.checkedAt) < acousticCheckInterval && (c.snap != nil || c.hasEmpty) {
			snap := c.snap
			c.mu.Unlock()
			return snap, nil
		}
		if c.rebuilding {
			if c.snap != nil {
				snap := c.snap
				c.mu.Unlock()
				return snap, nil
			}
			done := c.buildDone
			c.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			c.mu.Lock()
			if c.snap != nil || (c.hasEmpty && !c.checkedAt.IsZero() && time.Since(c.checkedAt) < acousticCheckInterval) {
				snap := c.snap
				c.mu.Unlock()
				return snap, nil
			}
			if c.buildErr != nil {
				err := c.buildErr
				c.mu.Unlock()
				return nil, err
			}
			c.mu.Unlock()
			continue
		}
		c.rebuilding = true
		done := make(chan struct{})
		c.buildDone = done
		c.buildErr = nil
		prevSnap := c.snap
		prevBuiltAt := c.builtAt
		buildHook := c.testBuildHook
		c.mu.Unlock()

		finished := false
		finish := func(snap *AcousticSnapshot, empty, updateChecked, updateBuilt bool, err error) (*AcousticSnapshot, error) {
			c.mu.Lock()
			if err == nil {
				c.snap = snap
				c.hasEmpty = empty
				if updateChecked {
					c.checkedAt = time.Now()
				}
				if updateBuilt {
					c.builtAt = time.Now()
				}
			}
			c.buildErr = err
			c.rebuilding = false
			close(done)
			finished = true
			c.mu.Unlock()
			return snap, err
		}
		defer func() {
			if !finished {
				c.mu.Lock()
				c.rebuilding = false
				close(done)
				c.mu.Unlock()
			}
		}()

		version, err := s.LatestAudioAnalysisVersion(ctx)
		if err != nil {
			return finish(nil, false, false, false, err)
		}
		if version == "" {
			return finish(nil, true, true, false, nil)
		}
		var count int64
		var latest string
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(analyzed_at), '') FROM track_audio_features WHERE analysis_version = ?`, version).Scan(&count, &latest); err != nil {
			return finish(nil, false, false, false, err)
		}
		if count == 0 {
			return finish(nil, true, true, false, nil)
		}
		stamp := version + "|" + latest + "|" + strconv.FormatInt(count, 10)
		if prevSnap != nil && (prevSnap.stamp == stamp || time.Since(prevBuiltAt) < acousticRebuildInterval) {
			return finish(prevSnap, false, true, false, nil)
		}
		resultSnap, resultErr := s.buildAcoustic(ctx, version, stamp, int(count), buildHook)
		if resultErr != nil {
			return finish(nil, false, false, false, resultErr)
		}
		return finish(resultSnap, resultSnap == nil, true, resultSnap != nil, nil)
	}
}

func (s *Store) buildAcoustic(ctx context.Context, version, stamp string, countHint int, hook func()) (*AcousticSnapshot, error) {
	if hook != nil {
		hook()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.track_id, v.embedding FROM track_audio_features f JOIN track_audio_vectors v ON v.track_id = f.track_id WHERE f.analysis_version = ? ORDER BY f.track_id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, max(0, countHint))
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
			if countHint > 0 {
				raw = make([]float32, 0, countHint*dims)
			}
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
		base := i * dims
		for d := 0; d < dims; d++ {
			mean[d] += float64(raw[base+d])
		}
	}
	invN := 1.0 / float64(len(ids))
	for d := range mean {
		mean[d] *= invN
	}
	snap := &AcousticSnapshot{Version: version, dims: dims, ids: ids, pos: make(map[int64]int, len(ids)), vecs: make([]int8, len(ids)*dims), stamp: stamp}
	tmp := make([]float64, dims)
	for i, id := range ids {
		base := i * dims
		for d := 0; d < dims; d++ {
			tmp[d] = float64(raw[base+d]) - mean[d]
		}
		quantizeInto(snap.vecs[base:base+dims], tmp)
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
