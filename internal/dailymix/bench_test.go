package dailymix

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// TestLargeLibraryTiming generates a day for a 17k-track library with embeddings
// (the NAS size) and reports the time; it fails only when grossly slow.
func TestLargeLibraryTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	tracks := synthLibrary(17000)
	rng := rand.New(rand.NewPCG(1, 2))
	ids := make([]int64, len(tracks))
	vecs := make([][]float32, len(tracks))
	for i, tr := range tracks {
		ids[i] = tr.ID
		v := make([]float32, 1280)
		for d := range v {
			v[d] = rng.Float32()
		}
		vecs[i] = v
		if i%4 == 0 { // a quarter not yet tagged by the model
			tr.Moods, tr.Styles, tr.TagSource = nil, nil, ""
		}
	}
	snap := storage.NewAcousticSnapshot(ids, vecs)
	start := time.Now()
	b := testBuilder(tracks, 25)
	b.acoustic = snap
	cands := b.Candidates()
	built := time.Since(start)
	env := &selectEnv{now: testNow, day: "2026-10-16", explore: 0.3, length: 25, recentMix: map[int64]string{}, usedToday: map[int64]bool{}, usedTitles: map[string]bool{}, profile: b.profile, acoustic: snap}
	ranked := rank(cands, planInput{day: "2026-10-16", stats: map[string]storage.ThemeStat{}, excludeID: map[string]bool{}, excludeMood: map[string]bool{}, excludeStyle: map[string]bool{}}, seeded("p"))
	n := 0
	for _, c := range choose(shortlist(ranked, 4, 1), 4, 1) {
		if picks := env.build(c.Theme, seeded("m", c.Theme.ID())); picks != nil {
			n++
			for _, p := range picks {
				env.usedToday[p.T.ID] = true
			}
		}
	}
	total := time.Since(start)
	t.Logf("17k tracks: %d candidate themes in %v, %d mixes, total %v", len(cands), built, n, total)
	if n != 5 || total > 20*time.Second {
		t.Fatalf("mixes %d in %v", n, total)
	}
}
