package dailymix

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestIndexedBuilderMatchesDirectScan(t *testing.T) {
	tracks := synthLibrary(480)
	// 加入多标签、轻度悲伤、未打标、老歌与作品/作曲/声学特征，覆盖全部倒排分支
	rng := rand.New(rand.NewPCG(42, 99))
	ids := make([]int64, len(tracks))
	vecs := make([][]float32, len(tracks))
	for i, tr := range tracks {
		ids[i] = tr.ID
		v := make([]float32, 32)
		for d := range v {
			v[d] = rng.Float32()
		}
		vecs[i] = v
		if i%10 == 0 {
			tr.Moods["sad"] = storage.RecTag{Weight: 0.35, Confidence: 0.9, Source: "llm"}
		}
		if i%7 == 0 {
			tr.Moods["uplifting"] = storage.RecTag{Weight: 0.5, Confidence: 0.9, Source: "llm"}
		}
		if i%11 == 0 {
			tr.Styles["ballad"] = storage.RecTag{Weight: 1, Confidence: 0.85, Source: "llm"}
		}
		if i%5 == 0 {
			tr.Moods, tr.Styles, tr.TagSource = nil, nil, ""
		}
		if i%4 == 0 {
			tr.PlayCount = 5
			tr.LastPlayed = testNow.AddDate(0, 0, -90)
		}
	}
	snap := storage.NewAcousticSnapshot(ids, vecs)

	b := testBuilder(tracks, 25)
	b.acoustic = snap
	b.computeGeneric()

	// 验证 moodPool 与朴素全表扫描逐项完全一致（含顺序与 Rel）
	for _, mood := range []string{"healing", "hotblooded", "cheerful", "uplifting", "epic"} {
		got := b.moodPool(mood, true)
		var want []Candidate
		for _, tr := range tracks {
			if mildlySad(tr, b.allowSad) && !lowEnergyMoods[mood] {
				continue
			}
			if r := moodRel(tr, mood); r >= minRel {
				want = append(want, Candidate{tr, r, "tag"})
				continue
			}
			var best float64
			for _, adj := range adjacentMoods[mood] {
				best = math.Max(best, moodRel(tr, adj))
			}
			if best >= 0.6 {
				want = append(want, Candidate{tr, 0.45 * best, "adjacent"})
			}
		}
		want = capAdjacent(want)
		if len(got) != len(want) {
			t.Fatalf("moodPool(%s) len = %d, want %d", mood, len(got), len(want))
		}
		for i := range want {
			if got[i].T.ID != want[i].T.ID || got[i].Via != want[i].Via || math.Abs(got[i].Rel-want[i].Rel) > 1e-12 {
				t.Fatalf("moodPool(%s)[%d] = %+v, want %+v", mood, i, got[i], want[i])
			}
		}
	}

	// 验证 stylePool 与朴素全表扫描逐项完全一致
	for _, st := range []string{"pop", "rock", "piano", "orchestral", "ballad"} {
		got := b.stylePool(st, true)
		var want []Candidate
		for _, tr := range tracks {
			if mildlySad(tr, b.allowSad) {
				continue
			}
			if r := styleRel(tr, st); r >= minStyleRel {
				want = append(want, Candidate{tr, r, "tag"})
				continue
			}
			var best float64
			for _, adj := range adjacentStyles[st] {
				if !b.generic[adj] {
					best = math.Max(best, styleRel(tr, adj))
				}
			}
			if best >= 0.75 {
				want = append(want, Candidate{tr, 0.4 * best, "adjacent"})
			}
		}
		want = capAdjacent(want)
		if len(got) != len(want) {
			t.Fatalf("stylePool(%s) len = %d, want %d", st, len(got), len(want))
		}
		for i := range want {
			if got[i].T.ID != want[i].T.ID || got[i].Via != want[i].Via || math.Abs(got[i].Rel-want[i].Rel) > 1e-12 {
				t.Fatalf("stylePool(%s)[%d] = %+v, want %+v", st, i, got[i], want[i])
			}
		}
	}

	// 验证 selectEnv 缓存日期切分与原始 DayAfter 一致
	env := &selectEnv{now: testNow, day: "2026-10-16", recentMix: map[int64]string{
		1: "2026-10-12", // within 7 days -> 0.3
		2: "2026-10-05", // within 14 days -> 0.6
		3: "2026-09-20", // older -> 1.0
	}}
	if env.novelty(&storage.RecTrack{ID: 1}) != 0.3 || env.novelty(&storage.RecTrack{ID: 2}) != 0.6 || env.novelty(&storage.RecTrack{ID: 3}) != 1.0 {
		t.Fatalf("novelty cutoffs mismatch: %v %v %v", env.novelty(&storage.RecTrack{ID: 1}), env.novelty(&storage.RecTrack{ID: 2}), env.novelty(&storage.RecTrack{ID: 3}))
	}
}
