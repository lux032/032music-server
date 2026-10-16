package storage

import (
	"encoding/binary"
	"testing"
	"time"
)

// embedding builds a 1280-dim float16 vector with value 1.0 (0x3C00) in the given dims.
func embedding(dims ...int) []byte {
	out := make([]byte, 2560)
	for _, d := range dims {
		binary.LittleEndian.PutUint16(out[2*d:], 0x3C00)
	}
	return out
}

func TestAcousticIndexAndHybridSimilarity(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 5)
	if snap, err := s.AcousticIndex(ctx); err != nil || snap != nil || snap.Len() != 0 || snap.Has(1) {
		t.Fatalf("empty index = %v %v", snap, err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	// Tracks 0–2 sound alike (dims 0–9), 3–4 differ (dims 100–109 / 200–209).
	vecs := [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {0, 1, 2, 3, 4, 5, 6, 7, 8}, {1, 2, 3, 4, 5, 6, 7, 8, 9}, {100, 101, 102, 103, 104, 105, 106, 107, 108, 109}, {200, 201, 202, 203, 204, 205, 206, 207, 208, 209}}
	for range ids {
		c, ok, _ := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now)
		if !ok {
			t.Fatal("candidate")
		}
		f := testFeatures(c, "fast")
		idx := 0
		for k, id := range ids {
			if id == c.TrackID {
				idx = k
			}
		}
		f.Embedding = embedding(vecs[idx]...)
		if err := s.SaveTrackAudioFeatures(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	s.acoustic.checkedAt = time.Time{} // force a re-check
	snap, err := s.AcousticIndex(ctx)
	if err != nil || snap.Len() != 5 {
		t.Fatalf("index = %d %v", snap.Len(), err)
	}
	near := snap.Neighbors(ids[0], 2, nil)
	if len(near) != 2 || (near[0].TrackID != ids[1] && near[0].TrackID != ids[2]) || near[0].Cosine < 0.6 {
		t.Fatalf("neighbors = %+v", near)
	}
	if cos, _ := snap.Cosine(ids[0], ids[3]); cos > 0 {
		t.Fatalf("different sounds must not correlate after centring: %.2f", cos)
	}
	if c := snap.Centroid([]int64{ids[0], ids[1]}); c == nil {
		t.Fatal("centroid")
	} else if cos, ok := snap.CosineTo(ids[2], c); !ok || cos < 0.6 {
		t.Fatalf("centroid cosine %.2f", cos)
	}
	if got := snap.Neighbors(ids[0], 5, func(id int64) bool { return id != ids[1] }); len(got) != 3 || got[0].TrackID != ids[2] {
		t.Fatalf("filtered = %+v", got)
	}
	// The cached snapshot is reused within the check interval.
	again, _ := s.AcousticIndex(ctx)
	if again != snap {
		t.Fatal("snapshot must be cached")
	}
	// Hybrid similarity: sound-alikes from other albums/artists appear with soundsSimilar.
	items, err := s.SimilarTracks(ctx, ids[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		for _, r := range it.Reasons {
			if r == "soundsSimilar" && (it.Track.ID == ids[1] || it.Track.ID == ids[2]) {
				found = true
			}
		}
		if it.Track.ID == ids[3] || it.Track.ID == ids[4] {
			for _, r := range it.Reasons {
				if r == "soundsSimilar" {
					t.Fatal("different sounds flagged similar")
				}
			}
		}
	}
	if !found {
		t.Fatalf("hybrid similar = %+v", items)
	}
}
