package enrichment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestAlbumSearchUsesTwentyFiveAndTrackUsesTwenty(t *testing.T) {
	var limits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limits = append(limits, r.URL.Query().Get("limit"))
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	manager, store, _ := newTrackManager(t, server)
	setting, err := store.MetadataSourceSetting(context.Background(), "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.searchMusicSubjectsQuery(context.Background(), setting, "Album", "Album", true, 25)
	_, _ = manager.searchMusicSubjects(context.Background(), setting, "Track", true)
	if len(limits) != 2 || limits[0] != "25" || limits[1] != "20" {
		t.Fatalf("limits=%v", limits)
	}
	if _, err = store.GetHTTPResponseCache(context.Background(), "bangumi", "music-search:v3:Album:25:0"); err != nil {
		t.Fatalf("v3 cache key missing: %v", err)
	}
	if _, err = store.GetHTTPResponseCache(context.Background(), "bangumi", "music-search:v2:Album:25:0"); err == nil {
		t.Fatal("legacy v2 cache key written")
	}
}

func TestSuppressedTrackHitsBecomeMissNotCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			_, _ = w.Write([]byte(`{"data":[{"id":8,"type":3,"name":"Song","infobox":[{"key":"艺术家","value":"Singer"}]}]}`))
		case "/v0/subjects/8/subjects":
			_, _ = w.Write([]byte(`[{"id":5,"type":2,"name":"Show","platform":"TV"}]`))
		case "/v0/subjects/5/subjects":
			_, _ = w.Write([]byte(`[{"id":8,"type":3,"name":"Song","relation":"片头曲"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager, store, lib := newTrackManager(t, server)
	target := importTrackSong(t, store, lib, "Album", "Song", "Singer", "regular")
	if err := store.SaveAlbumSubjectCandidates(context.Background(), target.AlbumID, []storage.AlbumSubjectCandidate{{ExternalID: "8", Title: "Song"}}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.AlbumSubjectCandidates(context.Background(), target.AlbumID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("album candidates=%v err=%v", candidates, err)
	}
	if err = store.RejectAlbumSubjectCandidate(context.Background(), target.AlbumID, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveTrackSubjectCandidates(context.Background(), target.ID, []storage.TrackSubjectCandidate{{ExternalID: "old", Title: "Old", MatchKind: "exact"}}); err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.enrichBangumiTrack(context.Background(), 0, target, true)
	if err != nil || outcome != "skipped" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := mustCount(t, store, `SELECT COUNT(*) FROM track_subject_candidates WHERE track_id=?`, target.ID); n != 0 {
		t.Fatalf("candidate count=%d", n)
	}
	if n := mustCount(t, store, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=?`, target.ID); n != 1 {
		t.Fatalf("miss count=%d", n)
	}
}

func TestBangumiTypeCorrectionPlatformMapping(t *testing.T) {
	for _, tc := range []struct {
		platform, want string
	}{{"剧场版", "movie"}, {"劇場版", "movie"}, {"TV", "anime"}, {"OVA", "anime"}, {"WEB", "anime"}, {"", "anime"}} {
		if got := bangumiWorkType(2, tc.platform); got != tc.want {
			t.Fatalf("platform=%q got=%q want=%q", tc.platform, got, tc.want)
		}
	}
	if got := bangumiWorkType(4, "PC"); got != "game" {
		t.Fatalf("game=%q", got)
	}
}

func TestBoundWorkMissingDetailIsSkipped(t *testing.T) {
	manager, store, _, id := phase4TestManager(t, http.NotFoundHandler())
	ctx := context.Background()
	if err := store.ReplaceWorkMatchCandidates(ctx, id, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "404", Title: "Original", Type: "anime", Payload: json.RawMessage(`{"id":404}`)}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	if err := store.ConfirmWorkMatchCandidate(ctx, id, candidates[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.enrichBangumiWork(ctx, 7, storage.WorkEnrichmentTarget{ID: id, Title: "Original", Type: "anime", BangumiExternalID: "404", BangumiRaw: json.RawMessage(`{"id":404}`)}, false)
	if err != nil || outcome != "skipped" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := mustCount(t, store, `SELECT COUNT(*) FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type_correction_skipped' AND source='bangumi'`, id); n != 1 {
		t.Fatalf("evidence count=%d", n)
	}
	pending, err := store.WorksForEnrichment(ctx, "bangumi", false, 0, id)
	if err != nil || len(pending) != 0 {
		t.Fatalf("404 work selected again: %+v err=%v", pending, err)
	}
	pending, err = store.WorksForEnrichment(ctx, "bangumi", true, 0, id)
	if err != nil || len(pending) != 1 {
		t.Fatalf("force did not select 404 work: %+v err=%v", pending, err)
	}
}

func TestBoundWorkTypeCorrectionNoOpPreservesTimestampsAndProvenance(t *testing.T) {
	manager, store, _, id := phase4TestManager(t, http.NotFoundHandler())
	ctx := context.Background()
	if err := store.ReplaceWorkMatchCandidates(ctx, id, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "123", Title: "Original", Type: "anime", Payload: json.RawMessage(`{"id":123,"type":2,"platform":"TV"}`)}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	if err := store.ConfirmWorkMatchCandidate(ctx, id, candidates[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	run1, err := store.CreateEnrichmentRun(ctx, "work", id, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CorrectBangumiWorkType(ctx, id, "anime", "movie", "123", run1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CorrectBangumiWorkType(ctx, id, "movie", "anime", "123", run1.ID); err != nil {
		t.Fatal(err)
	}
	db := readOnlyDB(t, store)
	var beforeRunID int64
	var beforeProvenanceUpdated, beforeValue string
	if err = db.QueryRowContext(ctx, `SELECT run_id,updated_at,value_json FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type' AND source='bangumi'`, id).Scan(&beforeRunID, &beforeProvenanceUpdated, &beforeValue); err != nil {
		t.Fatalf("type provenance missing before no-op: %v", err)
	}
	before, _ := store.WorkByID(ctx, id)
	run2, err := store.CreateEnrichmentRun(ctx, "work", id, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.enrichBangumiWork(ctx, run2.ID, storage.WorkEnrichmentTarget{ID: id, Title: "Original", Type: "anime", BangumiExternalID: "123", BangumiRaw: json.RawMessage(`{"id":123,"type":2,"platform":"TV"}`)}, false)
	if err != nil || outcome != "skipped" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	after, _ := store.WorkByID(ctx, id)
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("updated_at changed %q -> %q", before.UpdatedAt, after.UpdatedAt)
	}
	var runID int64
	var provenanceUpdated, value string
	if err = db.QueryRowContext(ctx, `SELECT run_id,updated_at,value_json FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type' AND source='bangumi'`, id).Scan(&runID, &provenanceUpdated, &value); err != nil {
		t.Fatalf("type provenance missing after no-op: %v", err)
	}
	if runID != beforeRunID || provenanceUpdated != beforeProvenanceUpdated || value != beforeValue {
		t.Fatalf("no-op changed provenance: run=%d/%d updated=%q/%q value=%q/%q", runID, beforeRunID, provenanceUpdated, beforeProvenanceUpdated, value, beforeValue)
	}
}

func TestBoundWorkFetchesMissingTypeAndPlatformAndCachesRaw(t *testing.T) {
	var subjectRequests int
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/subjects/123" {
			subjectRequests++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "type": 2, "platform": "剧场版"})
			return
		}
		http.NotFound(w, r)
	}))
	ctx := context.Background()
	if err := store.ReplaceWorkMatchCandidates(ctx, id, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "123", Title: "Original", Type: "anime", Payload: json.RawMessage(`{"id":123,"type":2}`)}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	if err := store.ConfirmWorkMatchCandidate(ctx, id, candidates[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateWork(ctx, id, storage.WorkInput{Title: "Original", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	// UpdateWork leaves the lock unchanged when the type does not change.
	work := storage.WorkEnrichmentTarget{ID: id, Title: "Original", Type: "anime", BangumiExternalID: "123", BangumiProfileType: "anime", BangumiRaw: json.RawMessage(`{"id":123}`)}
	outcome, err := manager.enrichBangumiWork(ctx, 0, work, false)
	if err != nil || outcome != "succeeded" || subjectRequests != 1 {
		t.Fatalf("outcome=%s requests=%d err=%v", outcome, subjectRequests, err)
	}
	got, _ := store.WorkByID(ctx, id)
	if got.Type != "movie" {
		t.Fatalf("type=%s", got.Type)
	}
	pending, err := store.WorksForEnrichment(ctx, "bangumi", false, 0, id)
	if err != nil || len(pending) != 0 {
		t.Fatalf("corrected work selected again: %+v err=%v", pending, err)
	}
}
