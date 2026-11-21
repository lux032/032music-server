package enrichment

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestStartAutoPhaseQueuesRerunWhileRunning(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	entered, release := make(chan struct{}), make(chan struct{})
	blocked := false
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		mu.Lock()
		seen = append(seen, body)
		shouldBlock := !blocked && strings.Contains(body, "FirstTrack")
		if shouldBlock {
			blocked = true
		}
		mu.Unlock()
		if shouldBlock {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	library := mustLibrary(t, s)
	if err := s.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    library,
		RelativePath: "Album1/01.flac",
		FileSize:     1,
		ModifiedAtNS: 1,
		Metadata:     metadata.AudioMetadata{Title: "FirstTrack", Album: "Anime OST 1", Artists: []string{"Singer1"}, AlbumArtists: []string{"Singer1"}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartRun(ctx, RunRequest{Scope: "all"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not reach tracks stage")
	}
	// New album arrives while run #1 is already in the tracks stage (both albums and tracks snapshots frozen).
	if err := s.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    library,
		RelativePath: "Album2/01.flac",
		FileSize:     1,
		ModifiedAtNS: 1,
		Metadata:     metadata.AudioMetadata{Title: "SecondTrack", Album: "Anime OST 2", Artists: []string{"Singer2"}, AlbumArtists: []string{"Singer2"}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size"},
	}); err != nil {
		t.Fatal(err)
	}
	m.StartAuto(ctx)
	close(release)
	m.Wait()

	runs, err := s.ListEnrichmentRuns(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 enrichment runs (original + queued auto rerun), got %d: %+v", len(runs), runs)
	}
	for _, r := range runs {
		if r.Status != "completed" {
			t.Fatalf("expected completed run, got %+v", r)
		}
	}
	mu.Lock()
	joined := strings.Join(seen, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "SecondTrack") {
		t.Fatalf("queued rerun did not enrich newly imported track; requests=%s", joined)
	}
}

func TestStartAutoPhaseCancelledRunClearsQueuedRerun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	library := mustLibrary(t, s)
	if err := s.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    library,
		RelativePath: "Album1/01.flac",
		FileSize:     1,
		ModifiedAtNS: 1,
		Metadata:     metadata.AudioMetadata{Title: "FirstTrack", Album: "Anime OST 1", Artists: []string{"Singer1"}, AlbumArtists: []string{"Singer1"}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size"},
	}); err != nil {
		t.Fatal(err)
	}
	run, err := m.StartRun(ctx, RunRequest{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	m.StartAuto(ctx)
	if err = m.CancelRun(run.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	m.Wait()

	runs, err := s.ListEnrichmentRuns(ctx, 10, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != "cancelled" {
		t.Fatalf("cancelled run must not trigger queued rerun; runs=%+v err=%v", runs, err)
	}
}

func TestStartAutoPhasePausedThenResumedRunsQueuedRerun(t *testing.T) {
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	run, err := s.CreateDurableEnrichmentRun(ctx, "all", 0, false, phaseStages(RunRequest{Scope: "all"}))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "pause", "server_restart"); err != nil {
		t.Fatal(err)
	}
	m.StartAuto(ctx)
	// Paused run stays paused until resumed.
	runs, err := s.ListEnrichmentRuns(ctx, 10, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != "paused" {
		t.Fatalf("expected single paused run, got %+v err=%v", runs, err)
	}
	if err = m.ResumeRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	runs, err = s.ListEnrichmentRuns(ctx, 10, 0)
	if err != nil || len(runs) != 2 {
		t.Fatalf("expected follow-up run after resumed run completed, got %+v err=%v", runs, err)
	}
	for _, r := range runs {
		if r.Status != "completed" {
			t.Fatalf("expected completed run, got %+v", r)
		}
	}
}

func TestStartAutoArtistQueuesRerunWhileRunning(t *testing.T) {
	m, firstArtist := independentArtistManager(t, false)
	artistFakeClock(m)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	blocked := false
	secondMBID := "22222222-2222-4222-8222-222222222222"
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		urlStr := r.URL.String()
		mu.Lock()
		shouldBlock := !blocked && strings.Contains(urlStr, safetyMBID)
		if shouldBlock {
			blocked = true
		}
		mu.Unlock()
		if shouldBlock {
			close(entered)
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-release:
			}
		}
		if strings.Contains(urlStr, secondMBID) {
			return cannedResponse(200, http.Header{}, `{"id":"`+secondMBID+`","name":"NewSinger","relations":[]}`), nil
		}
		return cannedResponse(200, http.Header{}, `{"id":"`+safetyMBID+`","name":"ACE+","relations":[]}`), nil
	})

	if _, err := m.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first artist run did not reach HTTP request")
	}

	// Import a new album with a new artist while artist run #1 is in flight.
	library, err := m.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    library.ID,
		RelativePath: "NewAlbum/01.flac",
		FileSize:     1,
		ModifiedAtNS: 1,
		Metadata:     metadata.AudioMetadata{Title: "NewSong", Album: "NewAlbum", Artists: []string{"NewSinger"}, AlbumArtists: []string{"NewSinger"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {secondMBID}}},
	}); err != nil {
		t.Fatal(err)
	}
	m.StartAuto(ctx)
	close(release)
	m.Wait()

	if _, err = m.store.ArtistExternalID(ctx, firstArtist, "musicbrainz"); err != nil {
		t.Fatalf("first artist not matched: %v", err)
	}
	artists, err := m.store.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var secondID int64
	for _, a := range artists {
		if a.Name == "NewSinger" {
			secondID = a.ID
		}
	}
	if secondID == 0 {
		t.Fatal("second artist not found")
	}
	gotMBID, err := m.store.ArtistExternalID(ctx, secondID, "musicbrainz")
	if err != nil || gotMBID != secondMBID {
		t.Fatalf("newly imported artist was not matched by queued rerun: mbid=%q err=%v", gotMBID, err)
	}
}

func TestStartAutoArtistCancelledRunClearsQueuedRerun(t *testing.T) {
	m, _ := independentArtistManager(t, false)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-release:
		}
		return cannedResponse(200, http.Header{}, `{"id":"`+safetyMBID+`","name":"ACE+","relations":[]}`), nil
	})

	runID, err := m.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("artist run did not start")
	}
	m.StartAuto(ctx)
	if err = m.CancelArtistMatching(runID); err != nil {
		t.Fatal(err)
	}
	close(release)
	m.Wait()

	runs, _, err := m.store.ListDurableArtistRuns(ctx, 10, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != "cancelled" {
		t.Fatalf("cancelled artist run must not trigger queued rerun; runs=%+v err=%v", runs, err)
	}
}

func TestStartAutoWorkPosterBackfillRerunsWhenAlreadyRunning(t *testing.T) {
	store := batch8Store(t)
	ctx := context.Background()
	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "First", Type: "anime", PosterURL: "http://127.0.0.1/first.jpg"}); err != nil {
		t.Fatal(err)
	}
	manager := batch8Manager(t, store)
	manager.SetPosterBackfillEnabled(true)

	entered := make(chan struct{})
	blocked := make(chan struct{})
	passes := 0
	manager.testPosterBackfillHook = func() {
		passes++
		if passes == 1 {
			close(entered)
			<-blocked
		}
	}
	if !manager.StartWorkPosterBackfill() {
		t.Fatal("first poster backfill did not start")
	}
	<-entered
	// New work arrives while backfill is running, and StartAuto requests another pass.
	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "Second", Type: "anime", PosterURL: "http://127.0.0.1/second.jpg"}); err != nil {
		t.Fatal(err)
	}
	manager.requestAutoWorkPosterBackfill()
	close(blocked)
	manager.Wait()

	result := manager.LastPosterBackfill()
	if result == nil || passes != 2 || result.Failed != 2 {
		t.Fatalf("expected 2 passes covering both works; passes=%d result=%+v", passes, result)
	}
}
