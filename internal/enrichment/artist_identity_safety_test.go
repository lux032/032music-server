package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestTaggedArtistNameMismatchCannotAutoConfirm(t *testing.T) {
	const mbid = "11111111-1111-4111-8111-111111111111"
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) { return http.DefaultTransport.RoundTrip(r) }))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"`+mbid+`","name":"Wrong Performer","sort-name":"Wrong Performer","aliases":[],"tags":[],"relations":[]}`)
	}))
	defer server.Close()
	manager.musicBrainzBase = server.URL
	s := manager.store
	ctx := context.Background()
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"ACE+"}, AlbumArtists: []string{"ACE+"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {mbid}}}}); err != nil {
		t.Fatal(err)
	}
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Enabled = true
	setting.AutoMatch = true
	if err = s.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("%v %v", artists, err)
	}
	result, err := manager.MatchArtist(ctx, artists[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.AutoMatched {
		t.Fatal("mismatched name auto confirmed")
	}
	candidates, _ := s.ArtistCandidates(ctx, artists[0].ID)
	if len(candidates) != 1 || candidates[0].Score >= 92 || candidates[0].Status != "candidate" {
		t.Fatalf("%+v", candidates)
	}
}
func TestArtistProfileNameMatchesAliases(t *testing.T) {
	if !artistProfileNameMatches("ACE+", storage.ExternalArtistProfile{DisplayName: "Other", Aliases: []string{"ACE+"}}) {
		t.Fatal("alias match rejected")
	}
	if artistProfileNameMatches("ACE+", storage.ExternalArtistProfile{DisplayName: "清田愛未"}) {
		t.Fatal("different name accepted")
	}
}

func TestRejectedHighScoreArtistCannotAutoBind(t *testing.T) {
	const mbid = "11111111-1111-4111-8111-111111111111"
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"` + mbid + `","name":"ACE+","sort-name":"ACE+","aliases":[],"tags":[],"relations":[]}`))}, nil
	}))
	ctx := context.Background()
	s := manager.store
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, _ := s.LibraryByRoot(ctx, "/music")
	if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"ACE+"}, AlbumArtists: []string{"ACE+"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {mbid}}}}); err != nil {
		t.Fatal(err)
	}
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Enabled, setting.AutoMatch = true, true
	if err := s.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	artists, _ := s.ArtistsForMatching(ctx)
	id := artists[0].ID
	if err := s.ReplaceArtistCandidates(ctx, id, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: mbid, MBID: mbid, DisplayName: "ACE+", Score: 100}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := s.ArtistCandidates(ctx, id)
	if err := s.RejectArtistCandidate(ctx, id, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	result, err := manager.MatchArtist(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.AutoMatched {
		t.Fatal("rejected high-score identity auto confirmed")
	}
	if externalID, err := s.ArtistExternalID(ctx, id, "musicbrainz"); err == nil {
		t.Fatalf("rejected identity persisted: %s", externalID)
	}
}

const safetyMBID = "11111111-1111-4111-8111-111111111111"

// All HTTP, including optional biography/image follow-ups, stays in this mock.
func independentArtistManager(t *testing.T, lastFM bool) (*Manager, int64) {
	t.Helper()
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"id":"` + safetyMBID + `","name":"ACE+","sort-name":"ACE+","aliases":[],"tags":[],"relations":[]}`
		if r.URL.Host == "ws.audioscrobbler.com" {
			body = `{"artist":{"name":"ACE+","mbid":"` + safetyMBID + `","url":"https://www.last.fm/music/ACE","bio":{"content":"Last.fm fallback biography"},"image":[{"#text":"https://images.example.org/artist.png","size":"large"}]}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	ctx := context.Background()
	s := manager.store
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"ACE+"}, AlbumArtists: []string{"ACE+"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {safetyMBID}}}}); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"musicbrainz", "lastfm"} {
		setting, err := s.MetadataSourceSetting(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		setting.Enabled = source == "musicbrainz" || lastFM
		setting.AutoMatch = setting.Enabled
		if source == "lastfm" {
			setting.APIKey = "mock-key"
		}
		if err := s.SaveMetadataSourceSetting(ctx, setting); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("%v %v", artists, err)
	}
	return manager, artists[0].ID
}

func TestMatchArtistConfirmedSameCanRefresh(t *testing.T) {
	manager, artist := independentArtistManager(t, false)
	ctx := context.Background()
	if err := manager.store.ReplaceArtistCandidates(ctx, artist, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: safetyMBID, MBID: safetyMBID, DisplayName: "ACE+", Score: 100}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := manager.store.ArtistCandidates(ctx, artist)
	if err := manager.store.ConfirmArtistCandidate(ctx, artist, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := manager.MatchArtist(ctx, artist)
		if err != nil || !result.AutoMatched {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestMatchArtistIndependentSourcesBindSafely(t *testing.T) {
	for _, decision := range []string{"both", "lastfm-rejected", "lastfm-different", "lastfm-owner-conflict", "mb-already-bound"} {
		t.Run(decision, func(t *testing.T) {
			manager, artist := independentArtistManager(t, true)
			ctx := context.Background()
			s := manager.store
			if decision == "lastfm-rejected" {
				if err := s.ReplaceArtistCandidates(ctx, artist, []storage.ArtistCandidate{{Source: "lastfm", ExternalID: safetyMBID, MBID: safetyMBID, DisplayName: "ACE+", Score: 98}}); err != nil {
					t.Fatal(err)
				}
				candidates, _ := s.ArtistCandidates(ctx, artist)
				if err := s.RejectArtistCandidate(ctx, artist, candidates[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			if decision == "lastfm-different" {
				if err := s.UpsertExternalArtistProfile(ctx, artist, storage.ExternalArtistProfile{Source: "lastfm", ExternalID: "manual", DisplayName: "Manual"}); err != nil {
					t.Fatal(err)
				}
			}
			if decision == "lastfm-owner-conflict" {
				if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: libraryForSafetyTest(t, s, ctx), RelativePath: "Other/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Other", Album: "Other", Artists: []string{"Other"}, AlbumArtists: []string{"Other"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
					t.Fatal(err)
				}
				artists, err := s.ArtistsForMatching(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, other := range artists {
					if other.ID != artist {
						if err := s.UpsertExternalArtistProfile(ctx, other.ID, storage.ExternalArtistProfile{Source: "lastfm", ExternalID: safetyMBID, DisplayName: "Other"}); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if decision == "mb-already-bound" {
				if err := s.UpsertExternalArtistProfile(ctx, artist, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "ACE+"}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := manager.MatchArtist(ctx, artist)
			if err != nil || !result.AutoMatched {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if id, err := s.ArtistExternalID(ctx, artist, "musicbrainz"); err != nil || id != safetyMBID {
				t.Fatalf("mb=%s err=%v", id, err)
			}
			id, err := s.ArtistExternalID(ctx, artist, "lastfm")
			if decision == "both" {
				if err != nil || id != safetyMBID {
					t.Fatalf("lastfm=%s err=%v", id, err)
				}
				// MB has no image: the saved Last.fm profile supplies the fallback.
				source, image, err := s.ArtistImageSource(ctx, artist)
				if err != nil || source != "lastfm" || image != "https://images.example.org/artist.png" {
					t.Fatalf("fallback=%s %s %v", source, image, err)
				}
				detail, err := s.ArtistDetail(ctx, artist)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, profile := range detail.Profiles {
					if profile.Source == "lastfm" && profile.Biography == "Last.fm fallback biography" {
						found = true
					}
				}
				if !found {
					t.Fatal("Last.fm biography not available")
				}
			} else if decision == "lastfm-different" {
				if err != nil || id != "manual" {
					t.Fatalf("lastfm=%s err=%v", id, err)
				}
			} else if err == nil {
				t.Fatalf("unexpected lastfm binding %s", id)
			}
			candidates, _ := s.ArtistCandidates(ctx, artist)
			for _, candidate := range candidates {
				if candidate.Source == "musicbrainz" && candidate.Status != "confirmed" {
					t.Fatal(candidate)
				}
				if candidate.Source == "lastfm" {
					expected := "candidate"
					if decision == "both" {
						expected = "confirmed"
					}
					if decision == "lastfm-rejected" {
						expected = "rejected"
					}
					if candidate.Status != expected {
						t.Fatal(candidate)
					}
				}
			}
		})
	}
}

func TestMatchArtistOwnerConflictRemainsReview(t *testing.T) {
	manager, artist := independentArtistManager(t, false)
	ctx := context.Background()
	s := manager.store
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Other/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Other", Album: "Other", Artists: []string{"Other"}, AlbumArtists: []string{"Other"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range artists {
		if owner.ID != artist {
			if err := s.UpsertExternalArtistProfile(ctx, owner.ID, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "Other"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := manager.MatchArtist(ctx, artist)
	if err != nil || result.AutoMatched || result.CandidateCount == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	candidates, err := s.ArtistCandidates(ctx, artist)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "candidate" {
		t.Fatalf("%+v %v", candidates, err)
	}
	// The pending candidate still enters the existing manual conflict workflow.
	var conflict *storage.ArtistExternalIDConflictError
	if err := s.ConfirmArtistCandidate(ctx, artist, candidates[0].ID); !errors.As(err, &conflict) {
		t.Fatalf("expected manual ownership conflict, got %v", err)
	}
}

func libraryForSafetyTest(t *testing.T, s *storage.Store, ctx context.Context) int64 {
	t.Helper()
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	return library.ID
}

func writableSafetyDB(t *testing.T, manager *Manager) *sql.DB {
	t.Helper()
	path := phase4DBPath[manager.store]
	if path == "" {
		path = trackTestDBPath[manager.store]
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMatchArtistLastFMRefreshAndAutomaticFreshSkip(t *testing.T) {
	for _, mode := range []string{"manual-both", "automatic-both", "lastfm-only-expired"} {
		t.Run(mode, func(t *testing.T) {
			manager, artist := independentArtistManager(t, true)
			ctx := context.Background()
			if result, err := manager.MatchArtist(ctx, artist); err != nil || !result.AutoMatched {
				t.Fatalf("%+v %v", result, err)
			}
			db := writableSafetyDB(t, manager)
			query := `UPDATE artist_external_profiles SET fetched_at='2000-01-01T00:00:00Z' WHERE artist_id=?`
			if mode == "lastfm-only-expired" {
				query += ` AND source='lastfm'`
			}
			if _, err := db.ExecContext(ctx, query, artist); err != nil {
				t.Fatal(err)
			}
			// Manual matching refreshes attachments/identity; automatic scanning now
			// skips confirmed identities even when their profile is expired.
			automatic := mode != "manual-both"
			result, err := manager.matchArtist(ctx, artist, automatic)
			if err != nil || automatic && result.Outcome != "skipped" || !automatic && !result.AutoMatched {
				t.Fatalf("%+v %v", result, err)
			}
			var old int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM artist_external_profiles WHERE artist_id=? AND fetched_at='2000-01-01T00:00:00Z'`, artist).Scan(&old); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if automatic {
				expected = 2
				if mode == "lastfm-only-expired" {
					expected = 1
				}
			}
			if old != expected {
				t.Fatalf("old=%d expected=%d", old, expected)
			}
			requests := 0
			manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				return nil, fmt.Errorf("unexpected confirmed request")
			})
			// No fake image cache: confirmed no-image profiles also skip.
			result, err = manager.matchArtist(ctx, artist, true)
			if err != nil || result.Outcome != "skipped" || requests != 0 {
				t.Fatalf("%+v %v requests=%d", result, err, requests)
			}

		})
	}
}

func TestMatchArtistSecondaryFailurePreservesPrimaryAndBatchCounts(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			manager, artist := independentArtistManager(t, true)
			db := writableSafetyDB(t, manager)
			if _, err := db.Exec(`CREATE TRIGGER fail_lastfm BEFORE INSERT ON artist_external_profiles WHEN NEW.source='lastfm' BEGIN SELECT RAISE(ABORT,'secret SQL failure'); END`); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if batch {
				if _, err := manager.StartAll(ctx); err != nil {
					t.Fatal(err)
				}
				manager.Wait()
				runs, err := manager.store.ListArtistMatchRuns(ctx, 1)
				if err != nil || len(runs) != 1 || runs[0].Matched != 1 || runs[0].Failed != 0 || runs[0].Processed != 1 {
					t.Fatalf("%+v %v", runs, err)
				}
			} else {
				result, err := manager.MatchArtist(ctx, artist)
				var partial *ArtistMatchPartialError
				if !result.AutoMatched || !errors.As(err, &partial) || !strings.Contains(partial.Notice(), "待人工确认") {
					t.Fatalf("%+v %v", result, err)
				}
			}
			if id, err := manager.store.ArtistExternalID(ctx, artist, "musicbrainz"); err != nil || id != safetyMBID {
				t.Fatalf("%s %v", id, err)
			}
			candidates, _ := manager.store.ArtistCandidates(ctx, artist)
			for _, candidate := range candidates {
				if candidate.Source == "lastfm" && candidate.Status != "candidate" {
					t.Fatal(candidate)
				}
			}
		})
	}
}

func TestAutomaticCompositeArtistZeroRequestsPreservesCandidates(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	db := writableSafetyDB(t, manager)
	if _, err := db.ExecContext(ctx, `UPDATE artists SET user_display_name='ACE+ & Other' WHERE id=?`, artist); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.ReplaceArtistCandidates(ctx, artist, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "ACE+", Score: 85}}); err != nil {
		t.Fatal(err)
	}
	old, _ := manager.store.ArtistCandidates(ctx, artist)
	requests := 0
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("unexpected external request")
	})
	result, err := manager.matchArtist(ctx, artist, true)
	current, _ := manager.store.ArtistCandidates(ctx, artist)
	if err != nil || result.SkipReason != "skipped_composite" || requests != 0 || len(current) != 1 || current[0].ID != old[0].ID || current[0].Status != "candidate" {
		t.Fatalf("%+v %v requests=%d candidates=%+v", result, err, requests, current)
	}
}

func TestArtistCandidatesFailedSourcePreserved(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	if err := manager.store.ReplaceArtistCandidates(ctx, artist, []storage.ArtistCandidate{{Source: "lastfm", ExternalID: "previous", DisplayName: "Previous", Score: 82}}); err != nil {
		t.Fatal(err)
	}
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "ws.audioscrobbler.com" {
			return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"` + safetyMBID + `","name":"ACE+","aliases":[],"tags":[],"relations":[]}`))}, nil
	})
	if _, err := manager.MatchArtist(ctx, artist); err != nil {
		t.Fatal(err)
	}
	candidates, _ := manager.store.ArtistCandidates(ctx, artist)
	found := false
	for _, candidate := range candidates {
		if candidate.Source == "lastfm" && candidate.ExternalID == "previous" && candidate.Status == "candidate" {
			found = true
		}
	}
	if !found {
		t.Fatal(candidates)
	}
}

func TestAutomaticSourceMissCooldownAndNoEmptyRun(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	db := writableSafetyDB(t, manager)
	// Only Last.fm enabled; explicit not-found is a successful empty snapshot.
	setting, _ := manager.store.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Enabled = false
	if err := manager.store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	requests := 0
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return cannedResponse(200, http.Header{}, `{"error":6,"message":"Artist not found"}`), nil
	})
	result, err := manager.matchArtist(ctx, artist, true)
	if err != nil || result.Outcome != "no_result" || requests != 1 {
		t.Fatalf("%+v %v requests=%d", result, err, requests)
	}
	result, err = manager.matchArtist(ctx, artist, true)
	if err != nil || result.Outcome != "skipped" || requests != 1 {
		t.Fatalf("%+v %v requests=%d", result, err, requests)
	}
	if _, err := manager.StartAll(ctx); !errors.Is(err, ErrNoEligibleArtists) {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE artists SET user_display_name='Changed Name' WHERE id=?`, artist); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.matchArtist(ctx, artist, true); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal(requests)
	}
}

func TestAutomaticCrossSourceCachedEvidenceAfterRateLimit(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	// Name search yields 85: independently agreeing Last.fm is needed for 98.
	db := writableSafetyDB(t, manager)
	if _, err := db.ExecContext(ctx, `DELETE FROM audio_file_tags`); err != nil {
		t.Fatal(err)
	}
	mbRequests, lastRequests := 0, 0
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "ws.audioscrobbler.com" {
			lastRequests++
			if lastRequests == 1 {
				return cannedResponse(429, http.Header{"Retry-After": {"1"}}, `{}`), nil
			}
			return cannedResponse(200, http.Header{}, `{"artist":{"name":"ACE+","mbid":"`+safetyMBID+`","url":"https://last.fm/ACE"}}`), nil
		}
		mbRequests++
		return cannedResponse(200, http.Header{}, `{"artists":[{"id":"`+safetyMBID+`","name":"ACE+","score":85,"aliases":[],"tags":[]} ]}`), nil
	})
	result, err := manager.matchArtist(ctx, artist, true)
	if asRateLimited(err) == nil || result.AutoMatched {
		t.Fatalf("%+v %v", result, err)
	}
	old, _ := manager.store.ArtistCandidates(ctx, artist)
	if len(old) != 1 || old[0].Score >= 92 {
		t.Fatal(old)
	}
	// Transport mocks don't enforce generic cooldown; the second Last.fm request
	// represents the eligible next attempt, not a real sleep/network call.
	manager.blockSourceUntil("lastfm", time.Time{})
	manager.cooldownMu.Lock()
	delete(manager.blockedUntil, "lastfm")
	manager.cooldownMu.Unlock()
	result, err = manager.matchArtist(ctx, artist, true)
	if err != nil || !result.AutoMatched || mbRequests != 1 || lastRequests != 2 {
		t.Fatalf("%+v %v MB=%d LF=%d", result, err, mbRequests, lastRequests)
	}
	for _, source := range []string{"musicbrainz", "lastfm"} {
		id, e := manager.store.ArtistExternalID(ctx, artist, source)
		if e != nil || id != safetyMBID {
			t.Fatalf("%s %s %v", source, id, e)
		}
	}
}

func TestAutomaticFailedSourceNeverCachesMiss(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	setting, _ := manager.store.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Enabled = false
	if err := manager.store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	requests := 0
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return cannedResponse(500, http.Header{}, `{}`), nil
	})
	for i := 0; i < 2; i++ {
		result, err := manager.matchArtist(ctx, artist, true)
		if err == nil || result.Outcome != "failed" {
			t.Fatalf("%+v %v", result, err)
		}
	}
	db := writableSafetyDB(t, manager)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM artist_source_match_state WHERE artist_id=?`, artist).Scan(&count); err != nil || count != 0 || requests != 2 {
		t.Fatalf("count=%d requests=%d err=%v", count, requests, err)
	}
}

func TestAutomaticReviewStableAndCompositeCounters(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	db := writableSafetyDB(t, manager)
	setting, _ := manager.store.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Enabled = false
	if err := manager.store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	requests := 0
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return cannedResponse(200, http.Header{}, `{"artist":{"name":"ACE+","mbid":"`+safetyMBID+`","url":"https://last.fm/ACE"}}`), nil
	})
	result, err := manager.matchArtist(ctx, artist, true)
	if err != nil || result.Outcome != "review" {
		t.Fatalf("%+v %v", result, err)
	}
	old, _ := manager.store.ArtistCandidates(ctx, artist)
	result, err = manager.matchArtist(ctx, artist, true)
	current, _ := manager.store.ArtistCandidates(ctx, artist)
	if err != nil || result.Outcome != "skipped" || requests != 1 || current[0].ID != old[0].ID {
		t.Fatalf("%+v %v %d", result, err, requests)
	}
	// Existing confirmed attachment-bearing composite must hit the early guard.
	if err := manager.store.UpsertExternalArtistProfile(ctx, artist, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "ACE+"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE artists SET user_display_name='ACE+ & Other' WHERE id=?; UPDATE artist_external_profiles SET image_checked_at=NULL WHERE artist_id=?`, artist, artist); err != nil {
		t.Fatal(err)
	}
	_, err = manager.StartAll(ctx)
	if !errors.Is(err, ErrNoEligibleArtists) {
		t.Fatal(err)
	}
	runs, err := manager.store.ListArtistMatchRuns(ctx, 1)
	if err != nil || len(runs) != 0 || requests != 1 {
		t.Fatal(runs, requests, err)
	}
	// A genuine eligible object makes the composite participate as skipped.
	library, _ := manager.store.LibraryByRoot(ctx, "/music")
	if err := manager.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Real/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Real", Album: "Real", Artists: []string{"Real"}, AlbumArtists: []string{"Real"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	runID, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	runs, err = manager.store.ListArtistMatchRuns(ctx, 1)
	if err != nil || runs[0].ID != runID || runs[0].Processed != 2 || runs[0].Skipped != 1 || runs[0].Processed != runs[0].Matched+runs[0].Review+runs[0].Skipped+runs[0].NoResult+runs[0].Failed {
		t.Fatal(runs, err)
	}

}

func TestLastFMQueryProvenanceSurvivesIdentityReset(t *testing.T) {
	for _, change := range []string{"reset", "change-during-query"} {
		t.Run(change, func(t *testing.T) {
			manager, artist := independentArtistManager(t, true)
			ctx := context.Background()
			db := writableSafetyDB(t, manager)
			if _, err := db.Exec(`DELETE FROM audio_file_tags`); err != nil {
				t.Fatal(err)
			}
			if err := manager.store.UpsertExternalArtistProfile(ctx, artist, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "ACE+"}); err != nil {
				t.Fatal(err)
			}
			nameQueries := 0
			manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "ws.audioscrobbler.com" {
					if r.URL.Query().Get("mbid") != "" {
						if change == "change-during-query" {
							if err := manager.store.ResetArtistIdentity(ctx, artist, "musicbrainz", safetyMBID); err != nil {
								t.Fatal(err)
							}
						}
						return cannedResponse(200, http.Header{}, `{"artist":{"name":"ACE+","mbid":"`+safetyMBID+`"}}`), nil
					}
					nameQueries++
					return cannedResponse(200, http.Header{}, `{"error":6,"message":"not found"}`), nil
				}
				return cannedResponse(200, http.Header{}, `{"artists":[{"id":"`+safetyMBID+`","name":"ACE+","score":85}]}`), nil
			})
			if _, err := manager.matchArtist(ctx, artist, true); err != nil {
				t.Fatal(err)
			}
			if change == "reset" {
				if err := manager.store.ResetArtistIdentity(ctx, artist, "musicbrainz", safetyMBID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := manager.matchArtist(ctx, artist, true)
			if err != nil || result.AutoMatched || nameQueries != 1 {
				t.Fatalf("%+v %v independent name queries=%d", result, err, nameQueries)
			}
			if _, err := manager.store.ArtistExternalID(ctx, artist, "musicbrainz"); err == nil {
				t.Fatal("non-independent cached evidence rebound reset identity")
			}
		})
	}
}

func TestRedirectedUnsafeTagCachedEvidenceCannotAutoBind(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	mbRequests, lfRequests := 0, 0
	const redirected = "22222222-2222-4222-8222-222222222222"
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "ws.audioscrobbler.com" {
			lfRequests++
			if lfRequests == 1 {
				return cannedResponse(429, http.Header{}, `{}`), nil
			}
			return cannedResponse(200, http.Header{}, `{"artist":{"name":"ACE+","mbid":"`+redirected+`"}}`), nil
		}
		mbRequests++
		return cannedResponse(200, http.Header{}, `{"id":"`+redirected+`","name":"Wrong Name","aliases":[],"relations":[]}`), nil
	})
	if _, err := manager.matchArtist(ctx, artist, true); asRateLimited(err) == nil {
		t.Fatal(err)
	}
	manager.cooldownMu.Lock()
	delete(manager.blockedUntil, "lastfm")
	manager.cooldownMu.Unlock()
	result, err := manager.matchArtist(ctx, artist, true)
	if err != nil || result.AutoMatched || mbRequests != 1 {
		t.Fatalf("%+v %v requests=%d", result, err, mbRequests)
	}
	candidates, _ := manager.store.ArtistCandidates(ctx, artist)
	for _, c := range candidates {
		if c.Score >= 92 {
			t.Fatal(c)
		}
	}
}

func TestMusicBrainzEmptySnapshotPrunesOnlyPending(t *testing.T) {
	manager, artist := independentArtistManager(t, false)
	ctx := context.Background()
	db := writableSafetyDB(t, manager)
	if _, err := db.Exec(`DELETE FROM audio_file_tags`); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.ReplaceArtistCandidates(ctx, artist, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: "pending", DisplayName: "Pending"}, {Source: "musicbrainz", ExternalID: "rejected", DisplayName: "Rejected"}, {Source: "musicbrainz", ExternalID: "confirmed", DisplayName: "Confirmed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE artist_match_candidates SET status=external_id WHERE external_id IN ('rejected','confirmed')`); err != nil {
		t.Fatal(err)
	}
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(200, http.Header{}, `{"artists":[]}`), nil
	})
	result, err := manager.matchArtist(ctx, artist, true)
	if err != nil || result.Outcome != "no_result" {
		t.Fatalf("%+v %v", result, err)
	}
	candidates, _ := manager.store.ArtistCandidates(ctx, artist)
	if len(candidates) != 2 {
		t.Fatal(candidates)
	}
	for _, c := range candidates {
		if c.Status == "candidate" {
			t.Fatal(c)
		}
	}
}

func TestUnknownLastFMSnapshotFailsClosed(t *testing.T) {
	manager, artist := independentArtistManager(t, true)
	ctx := context.Background()
	db := writableSafetyDB(t, manager)
	if _, err := db.Exec(`DELETE FROM audio_file_tags`); err != nil {
		t.Fatal(err)
	}
	input, _ := manager.store.ArtistForMatching(ctx, artist)
	input, err := manager.store.ArtistMatchQueryContext(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	setting, _ := manager.store.MetadataSourceSetting(ctx, "lastfm")
	// Pre-provenance snapshot has no explicit independence proof.
	snapshot := storage.ArtistSourceSnapshot{Candidates: []storage.ArtistCandidate{{Source: "lastfm", ExternalID: safetyMBID, MBID: safetyMBID, DisplayName: "ACE+", Score: 82}}, Profiles: map[string]storage.ExternalArtistProfile{safetyMBID: {Source: "lastfm", ExternalID: safetyMBID, DisplayName: "ACE+"}}}
	if err := manager.store.SaveArtistSourceCheck(ctx, input, setting, snapshot); err != nil {
		t.Fatal(err)
	}
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "ws.audioscrobbler.com" {
			t.Fatal("unknown fresh snapshot unexpectedly queried")
		}
		return cannedResponse(200, http.Header{}, `{"artists":[{"id":"`+safetyMBID+`","name":"ACE+","score":85}]}`), nil
	})
	result, err := manager.matchArtist(ctx, artist, true)
	if err != nil || result.AutoMatched {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestCompositeNormalizationEvidenceUnique(t *testing.T) {
	c := []storage.ArtistCandidate{{Score: 100}}
	normalizeArtistCandidates(c, false, true)
	normalizeArtistCandidates(c, false, true)
	if c[0].Score != 85 || len(c[0].Evidence) != 1 {
		t.Fatal(c)
	}
}
