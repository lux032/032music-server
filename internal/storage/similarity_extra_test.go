package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSimilarityGraphFactorsAndSeedDedupe(t *testing.T) {
	s := similarityFixture(t)
	for i, title := range []string{"Song", "Song (Remastered)", "other", "third"} {
		addSimilarityTrack(t, s, i+1, title, "regular", true)
	}
	ctx := context.Background()
	for _, q := range []string{`INSERT INTO track_artists(track_id,artist_id,role) VALUES(1,1,'composer'),(3,1,'composer')`, `INSERT INTO genres(id,name) VALUES(1,'A'),(2,'B')`, `INSERT INTO track_genres(track_id,genre_id) VALUES(1,1),(3,1)`, `INSERT INTO track_genre_overrides(track_id,genre_id) VALUES(3,2)`, `INSERT INTO works(id,title,normalized_title,type) VALUES(1,'Work','work','anime')`, `INSERT INTO work_tracks(work_id,track_id,role,source) VALUES(1,1,'op','manual'),(1,3,'op','manual')`, `INSERT INTO playlists(id,name) VALUES(1,'Playlist'),(2,'Playlist 2')`, `INSERT INTO playlist_items(playlist_id,track_id,position) VALUES(1,1,0),(1,3,1),(2,1,0),(2,3,1)`} {
		if _, e := s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	items, e := s.SimilarTracks(ctx, 1, 30)
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range items {
		if item.Track.ID == 2 {
			t.Fatal("seed version was recommended")
		}
		if item.Track.ID == 3 {
			if !strings.Contains(strings.Join(item.Reasons, ","), "sharedCredit") || !strings.Contains(strings.Join(item.Reasons, ","), "sameWork") || !strings.Contains(strings.Join(item.Reasons, ","), "coPlaylist") || strings.Contains(strings.Join(item.Reasons, ","), "genre") {
				t.Fatalf("reasons %v", item.Reasons)
			}
		}
	}
	path, complete, e := s.TrackPath(ctx, 1, 3, 3, time.Second)
	if e != nil || !complete || len(path) > 3 {
		t.Fatalf("path=%v complete=%v err=%v", path, complete, e)
	}
	// Destination shares only the intermediate's singer: it cannot be reached directly.
	if _, e = s.db.ExecContext(ctx, `INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(3,'Bridge','bridge','bridge')`); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`DELETE FROM track_artists WHERE track_id=4`, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(4,3,'primary'),(3,3,'primary')`, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(3,3,'composer'),(4,3,'composer')`, `INSERT INTO work_tracks(work_id,track_id) VALUES(1,4)`} {
		if _, e = s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	// 1 and 4 share a work; 4 and 3 share primary singer. Remove the direct 1→3 link.
	for _, q := range []string{`DELETE FROM track_artists WHERE track_id=3 AND artist_id=1 AND role='primary'`, `DELETE FROM work_tracks WHERE track_id=3`, `DELETE FROM track_genre_overrides WHERE track_id=3`, `DELETE FROM track_genres WHERE track_id=3`, `DELETE FROM track_artists WHERE track_id=3 AND artist_id=1 AND role='composer'`, `DELETE FROM playlist_items WHERE track_id=3`} {
		if _, e = s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	path, complete, e = s.TrackPath(ctx, 1, 3, 3, time.Second)
	if e != nil || !complete || len(path) != 3 || path[0].ID != 1 || path[1].ID != 4 || path[2].ID != 3 {
		t.Fatalf("limit=3 path=%v complete=%v err=%v", path, complete, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.SimilarTracks(cancelled, 1, 30); e == nil {
		t.Fatal("expected cancelled query")
	}
}

func TestSimilarTracksBlocksExcludeAndDiversity(t *testing.T) {
	s := similarityFixture(t)
	ctx := context.Background()
	// Add a third primary artist so we can test artist diversity capping.
	if _, e := s.db.ExecContext(ctx, `INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(3,'Other','other','other')`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(ctx, `INSERT INTO genres(id,name) VALUES(1,'Pop')`); e != nil {
		t.Fatal(e)
	}
	// Tracks 1..5 all share artist 1 & genre 1; track 6 has artist 3 & genre 1.
	for i, title := range []string{"Seed", "A1", "A2", "A3", "Blocked", "B1"} {
		addSimilarityTrack(t, s, i+1, title, "regular", true)
		if _, e := s.db.ExecContext(ctx, `INSERT INTO track_genres(track_id,genre_id) VALUES(?,1)`, i+1); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.db.ExecContext(ctx, `UPDATE track_artists SET artist_id=3 WHERE track_id=6`); e != nil {
		t.Fatal(e)
	}
	// Block track 5 via recommendation_track_blocks.
	if e := s.SetRecommendationBlock(ctx, 5, true); e != nil {
		t.Fatal(e)
	}
	// Blocked track must never appear; excluded track 2 must be omitted.
	items, e := s.SimilarTracksWithOptions(ctx, 1, 10, SimilarTracksOptions{
		Exclude:      map[int64]bool{2: true},
		MaxPerArtist: 1,
	})
	if e != nil {
		t.Fatal(e)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items (3,4,6), got %d", len(items))
	}
	for _, it := range items {
		if it.Track.ID == 5 {
			t.Fatal("blocked track appeared in similar results")
		}
		if it.Track.ID == 2 {
			t.Fatal("excluded track appeared in similar results")
		}
	}
	// With MaxPerArtist=1, artist 2 (track 6) must be promoted ahead of the second artist-1 track (track 4).
	if items[0].Track.ID != 3 || items[1].Track.ID != 6 || items[2].Track.ID != 4 {
		t.Fatalf("expected diversity order [3, 6, 4], got [%d, %d, %d]", items[0].Track.ID, items[1].Track.ID, items[2].Track.ID)
	}
}

func TestSimilarTracksExcludesAccompanimentAndInstrumentalSignals(t *testing.T) {
	s := similarityFixture(t)
	ctx := context.Background()
	// 1: vocal seed
	// 2: valid vocal song whose title starts with "Instinct" (must NOT be false-positive filtered)
	// 3..8: various accompaniment / instrumental tracks marked only as track_type='regular'
	// 9: pure instrumental track (track_type='instrumental')
	// 10: track with clean title but relative_path in an Off Vocal folder
	// 11: track with clean title/path but AI tag vocal='instrumental'
	// 12: track with clean title/path/untagged but EffNet heads_json voice=0.08
	// 13: track with EffNet heads_json voice=0.88 (vocal -> must be kept)
	tracks := []struct {
		id    int
		title string
		kind  string
	}{
		{1, "Hero Song", "regular"},
		{2, "Hero (Instinct)", "regular"},
		{3, "Starlight -Instrumental-", "regular"},
		{4, "Starlight (Off Vocal)", "regular"},
		{5, "Starlight (Inst.)", "regular"},
		{6, "Starlight（カラオケ）", "regular"},
		{7, "Starlight (伴奏)", "regular"},
		{8, "Starlight -Less Vocal-", "regular"},
		{9, "Piano Solo", "instrumental"},
		{10, "Clean Title Path", "regular"},
		{11, "Moonlight Sonata", "regular"},
		{12, "Evening Breeze", "regular"},
		{13, "Morning Chorus", "regular"},
	}
	for _, tr := range tracks {
		addSimilarityTrack(t, s, tr.id, tr.title, tr.kind, true)
	}
	if _, e := s.db.ExecContext(ctx, `UPDATE audio_files SET relative_path='Album/Off Vocal/10 Clean.flac' WHERE track_id=10`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(ctx, `INSERT INTO track_tag_meta(track_id,source_stamp,input_hash,vocab_version,prompt_version,model,vocal,tagged_at) VALUES(11,'s','h',1,1,'m','instrumental','2026-01-01T00:00:00Z')`); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveTrackAudioFeatures(ctx, TrackAudioFeatures{
		TrackID: 12, Fingerprint: "fp12", AnalysisVersion: "v1", Mode: "fast",
		Genre: []byte{1}, MoodTheme: []byte{1}, Instrument: []byte{1}, Embedding: []byte{1, 2, 3, 4},
		HeadsJSON: `{"voice":0.08}`,
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveTrackAudioFeatures(ctx, TrackAudioFeatures{
		TrackID: 13, Fingerprint: "fp13", AnalysisVersion: "v1", Mode: "fast",
		Genre: []byte{1}, MoodTheme: []byte{1}, Instrument: []byte{1}, Embedding: []byte{1, 2, 3, 4},
		HeadsJSON: `{"voice":0.88}`,
	}); e != nil {
		t.Fatal(e)
	}

	items, e := s.SimilarTracks(ctx, 1, 30)
	if e != nil {
		t.Fatal(e)
	}
	gotIDs := make([]int64, 0, len(items))
	for _, it := range items {
		gotIDs = append(gotIDs, it.Track.ID)
	}
	// Only tracks 2 ("Instinct") and 13 (vocal voice=0.88) should survive for vocal seed 1.
	if len(gotIDs) != 2 || (gotIDs[0] != 2 && gotIDs[1] != 2) || (gotIDs[0] != 13 && gotIDs[1] != 13) {
		t.Fatalf("vocal seed expected [2, 13], got %v", gotIDs)
	}

	// Seeding from instrumental track 9: must STILL block all accompaniment / "(instrumental)"
	// version tracks (3,4,5,6,7,8,10), while allowing clean pure instrumentals (11,12) unless HideInstrumental is set.
	instItems, e := s.SimilarTracks(ctx, 9, 30)
	if e != nil {
		t.Fatal(e)
	}
	instSet := map[int64]bool{}
	for _, it := range instItems {
		instSet[it.Track.ID] = true
	}
	for _, blocked := range []int64{3, 4, 5, 6, 7, 8, 10} {
		if instSet[blocked] {
			t.Fatalf("accompaniment track %d leaked into instrumental seed results: %v", blocked, instSet)
		}
	}
	// Even when seeding directly from an "(instrumental)" track (3), other accompaniment versions must NOT be recommended.
	accSeedItems, e := s.SimilarTracks(ctx, 3, 30)
	if e != nil {
		t.Fatal(e)
	}
	for _, it := range accSeedItems {
		for _, blocked := range []int64{4, 5, 6, 7, 8, 10} {
			if it.Track.ID == blocked {
				t.Fatalf("accompaniment track %d leaked when seeding from accompaniment track 3", blocked)
			}
		}
	}
	if !instSet[11] || !instSet[12] {
		t.Fatalf("expected pure instrumental tracks 11 and 12 for instrumental seed, got %v", instSet)
	}
	strictItems, e := s.SimilarTracksWithOptions(ctx, 9, 30, SimilarTracksOptions{HideInstrumental: true})
	if e != nil {
		t.Fatal(e)
	}
	for _, it := range strictItems {
		if it.Track.ID == 11 || it.Track.ID == 12 {
			t.Fatalf("HideInstrumental leaked instrumental track %d", it.Track.ID)
		}
	}
}

func TestDedupeCandidatesInterleavesStructuralWithAcousticOnly(t *testing.T) {
	seed := similarityMeta{track: Track{ID: 1, Title: "Seed"}, primary: map[int64]bool{1: true}}
	cand := func(id int64, artistID int64, score float64, reasons ...string) scoredCandidate {
		return scoredCandidate{
			id:      id,
			score:   score,
			reasons: reasons,
			meta:    &similarityMeta{track: Track{ID: id, Title: string(rune('A' + id))}, primary: map[int64]bool{artistID: true}},
		}
	}
	ranked := []scoredCandidate{
		cand(2, 2, 0.42, "genre", "soundsSimilar"),
		cand(3, 3, 0.41, "soundsSimilar"),
		cand(4, 4, 0.40, "soundsSimilar"),
		cand(5, 5, 0.39, "soundsSimilar"),
		cand(6, 6, 0.38, "soundsSimilar"),
		cand(7, 7, 0.32, "sameWork", "genre"),
		cand(8, 8, 0.30, "sharedCredit", "era"),
		cand(9, 9, 0.10, "coPlaylist"), // below quality floor (< 0.21) -> must not jump ahead
	}
	got := dedupeCandidatesWithOptions(seed, ranked, 6, SimilarTracksOptions{MaxPerArtist: 2})
	want := []int64{2, 3, 7, 4, 5, 8}
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d got=%v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].id != id {
			ids := make([]int64, len(got))
			for j, g := range got {
				ids[j] = g.id
			}
			t.Fatalf("got order %v want %v", ids, want)
		}
	}
}

func TestDedupeCandidatesSeededJitterVariesOrderAndPreservesRelevance(t *testing.T) {
	seed := similarityMeta{track: Track{ID: 1, Title: "Seed"}, primary: map[int64]bool{1: true}}
	cand := func(id int64, score float64, reasons ...string) scoredCandidate {
		return scoredCandidate{
			id:      id,
			score:   score,
			reasons: reasons,
			meta:    &similarityMeta{track: Track{ID: id, Title: string(rune('A' + id))}, primary: map[int64]bool{id: true}},
		}
	}
	ranked := []scoredCandidate{
		cand(2, 0.44, "sameWork"),
		cand(3, 0.43, "soundsSimilar"),
		cand(4, 0.42, "sharedCredit"),
		cand(5, 0.41, "soundsSimilar"),
		cand(6, 0.40, "coPlaylist"),
		cand(7, 0.39, "soundsSimilar"),
		cand(8, 0.38, "sameArtist"),
		cand(9, 0.08, "era"), // low relevance: must never enter top 5 even with max jitter
	}
	a := dedupeCandidatesWithOptions(seed, ranked, 5, SimilarTracksOptions{MaxPerArtist: 2, Seed: 12345})
	b := dedupeCandidatesWithOptions(seed, ranked, 5, SimilarTracksOptions{MaxPerArtist: 2, Seed: 12345})
	if len(a) != 5 || len(b) != 5 {
		t.Fatalf("expected 5 items, got %d / %d", len(a), len(b))
	}
	for i := range a {
		if a[i].id != b[i].id || a[i].score != b[i].score {
			t.Fatalf("same seed must be reproducible: a=%v b=%v", a, b)
		}
		if a[i].id == 9 {
			t.Fatalf("low-relevance candidate 9 leaked into top 5: %v", a)
		}
	}
	// Across several distinct seeds, at least one must differ in selection or order.
	differed := false
	for _, s := range []int64{22222, 33333, 44444, 55555, 66666} {
		other := dedupeCandidatesWithOptions(seed, ranked, 5, SimilarTracksOptions{MaxPerArtist: 2, Seed: s})
		for i := range other {
			if other[i].id == 9 {
				t.Fatalf("low-relevance candidate 9 leaked for seed %d", s)
			}
			if other[i].id != a[i].id {
				differed = true
			}
		}
	}
	if !differed {
		t.Fatal("expected distinct seeds to vary candidate selection or order")
	}
}
