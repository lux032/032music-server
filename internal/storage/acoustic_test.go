package storage

import (
	"context"
	"encoding/binary"
	"errors"
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

func TestAcousticCacheResilienceNegativeCacheAndNonBlockingRebuild(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 2)

	// 1. Negative caching: once empty state is observed, a cancelled context still
	// hits the in-memory empty cache within acousticCheckInterval (no DB query).
	if snap, err := s.AcousticIndex(ctx); err != nil || snap != nil {
		t.Fatalf("initial empty = %v %v", snap, err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if snap, err := s.AcousticIndex(canceledCtx); err != nil || snap != nil {
		t.Fatalf("expected negative cache hit without DB query, got snap=%v err=%v", snap, err)
	}

	// Insert one analyzed track and force a check.
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c1, ok, _ := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now)
	if !ok {
		t.Fatal("expected first candidate")
	}
	f1 := testFeatures(c1, "fast")
	f1.Embedding = embedding(0, 1, 2)
	if err := s.SaveTrackAudioFeatures(ctx, f1); err != nil {
		t.Fatal(err)
	}
	s.acoustic.mu.Lock()
	s.acoustic.checkedAt = time.Time{}
	s.acoustic.mu.Unlock()

	snap1, err := s.AcousticIndex(ctx)
	if err != nil || snap1 == nil || snap1.Len() != 1 {
		t.Fatalf("snap1 = %v err=%v", snap1, err)
	}

	// 2. Transient error / cancelled caller after check interval MUST NOT clear snap1.
	s.acoustic.mu.Lock()
	s.acoustic.checkedAt = time.Time{}
	s.acoustic.mu.Unlock()
	if _, err := s.AcousticIndex(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	s.acoustic.mu.Lock()
	preserved := s.acoustic.snap
	s.acoustic.mu.Unlock()
	if preserved != snap1 {
		t.Fatal("transient cancellation cleared valid cached snapshot")
	}

	// 3. Rebuild runs outside the mutex so concurrent readers immediately receive snap1.
	c2, ok, _ := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now.Add(time.Minute))
	if !ok {
		t.Fatal("expected second candidate")
	}
	f2 := testFeatures(c2, "fast")
	f2.Embedding = embedding(3, 4, 5)
	if err := s.SaveTrackAudioFeatures(ctx, f2); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	s.acoustic.mu.Lock()
	s.acoustic.checkedAt = time.Time{}
	s.acoustic.builtAt = time.Now().Add(-acousticRebuildInterval - time.Second)
	s.acoustic.testBuildHook = func() {
		close(entered)
		<-release
	}
	s.acoustic.mu.Unlock()

	rebuildDone := make(chan *AcousticSnapshot, 1)
	go func() {
		sn, e := s.AcousticIndex(ctx)
		if e != nil {
			t.Errorf("rebuild err: %v", e)
		}
		rebuildDone <- sn
	}()

	<-entered
	// Concurrent reader must NOT block waiting for release and must get snap1 immediately.
	concurrentSnap, err := s.AcousticIndex(ctx)
	if err != nil || concurrentSnap != snap1 {
		t.Fatalf("concurrent read during rebuild got %v err=%v, want snap1", concurrentSnap, err)
	}
	if !s.HasAcousticFeatures(ctx) {
		t.Fatal("HasAcousticFeatures should return true immediately during rebuild")
	}
	close(release)
	snap2 := <-rebuildDone
	if snap2 == nil || snap2.Len() != len(ids) {
		t.Fatalf("rebuilt snap = %v, want len %d", snap2, len(ids))
	}
}
