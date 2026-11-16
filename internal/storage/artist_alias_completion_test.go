package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func aliasFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s, lib := searchTestStore(t)
	searchTestImport(t, s, lib, "a.flac", "Song", "Album", "星街すいせい", "星街すいせい", "")
	a, e := s.ListArtists(context.Background(), Filters{})
	if e != nil || len(a) != 1 {
		t.Fatal(a, e)
	}
	if e = s.UpsertExternalArtistProfile(context.Background(), a[0].ID, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mb-test", DisplayName: "星街すいせい"}); e != nil {
		t.Fatal(e)
	}
	st, _ := s.ArtistAliasSettings(context.Background())
	st.Enabled = true
	if e = s.SaveArtistAliasSettings(context.Background(), st); e != nil {
		t.Fatal(e)
	}
	return s, a[0].ID
}
func aliasClaim(t *testing.T, s *Store) ArtistAliasJob {
	t.Helper()
	ctx := context.Background()
	list, e := s.ArtistAliasSubjects(ctx, false)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	if _, e = s.EnqueueArtistAlias(ctx, list[0], "manual"); e != nil {
		t.Fatal(e)
	}
	j, e := s.ClaimArtistAliasJob(ctx)
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func aliasCandidate(value string) ArtistAliasCandidate {
	return ArtistAliasCandidate{Value: value, OriginalValue: value, Language: "zh", Evidence: []ArtistAliasEvidence{{URL: "https://www.wikidata.org/wiki/Q1", Text: value, Source: "wikidata"}}}
}
func TestArtistAliasDurableBudgetAndIdentityFence(t *testing.T) {
	s, id := aliasFixture(t)
	ctx := context.Background()
	j := aliasClaim(t, s)
	for i := 0; i < 2; i++ {
		if e := s.ReserveArtistAliasCall(ctx, j, "search", 2); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.RecoverArtistAliasJobs(ctx); e != nil {
		t.Fatal(e)
	}
	next, e := s.ClaimArtistAliasJob(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if next.SearchCalls != 2 || next.Epoch <= j.Epoch {
		t.Fatal(next)
	}
	if e = s.ReserveArtistAliasCall(ctx, next, "search", 2); !errors.Is(e, ErrAliasBudget) {
		t.Fatal(e)
	}
	if e = s.SaveArtistAliasCandidate(ctx, j, aliasCandidate("星街彗星"), true); !errors.Is(e, ErrAliasJobState) {
		t.Fatalf("stale worker: %v", e)
	}
	if e = s.UpsertExternalArtistProfile(ctx, id, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mb-other", DisplayName: "Other"}); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveArtistAliasCandidate(ctx, next, aliasCandidate("星街彗星"), true); !errors.Is(e, ErrAliasJobState) {
		t.Fatalf("changed identity: %v", e)
	}
	a, _ := s.ArtistSearchAliases(ctx, id)
	if len(a) != 0 {
		t.Fatal(a)
	}
}
func TestArtistAliasReviewDeletionAndManualPreservation(t *testing.T) {
	s, id := aliasFixture(t)
	ctx := context.Background()
	j := aliasClaim(t, s)
	if e := s.AddArtistSearchAlias(ctx, id, "人工保留"); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveArtistAliasCandidate(ctx, j, aliasCandidate("星街彗星"), false); e != nil {
		t.Fatal(e)
	}
	pending, e := s.ArtistAliasCandidates(ctx, "pending", 0, 100)
	if e != nil || len(pending) != 1 {
		t.Fatal(pending, e)
	}
	a, _ := s.ArtistSearchAliases(ctx, id)
	if len(a) != 1 {
		t.Fatal(a)
	}
	if e = s.ReviewArtistAlias(ctx, pending[0].ID, true, ""); e != nil {
		t.Fatal(e)
	}
	result, e := s.Search(ctx, SearchOptions{Query: "星街彗星"})
	if e != nil || result.Artists.Total != 1 || result.Tracks.Total != 1 {
		t.Fatal(result, e)
	}
	a, _ = s.ArtistSearchAliases(ctx, id)
	var autoID int64
	for _, v := range a {
		if v.Source == "automatic" {
			autoID = v.ID
		}
	}
	if autoID == 0 {
		t.Fatal(a)
	}
	if e = s.DeleteArtistSearchAlias(ctx, id, autoID); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveArtistAliasCandidate(ctx, j, aliasCandidate("星街彗星"), true); e != nil {
		t.Fatal(e)
	}
	a, _ = s.ArtistSearchAliases(ctx, id)
	if len(a) != 1 || a[0].Value != "人工保留" {
		t.Fatal(a)
	}
	if e = s.SaveArtistAliasCandidate(ctx, j, aliasCandidate("不正确名字"), false); e != nil {
		t.Fatal(e)
	}
	pending, _ = s.ArtistAliasCandidates(ctx, "pending", 0, 100)
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	if e = s.ReviewArtistAlias(ctx, pending[0].ID, false, ""); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveArtistAliasCandidate(ctx, j, aliasCandidate("不正确名字"), true); e != nil {
		t.Fatal(e)
	}
	a, _ = s.ArtistSearchAliases(ctx, id)
	if len(a) != 1 {
		t.Fatal(a)
	}
}
func TestArtistAliasMigrationDoesNotQueueExistingArtists(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "old.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.MigrateUpTo(ctx, 71); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Old','Old','old')`); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artist_alias_new_artists`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if _, e = s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('New','New','new')`); e != nil {
		t.Fatal(e)
	}
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artist_alias_new_artists`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	// Source settings retain secrets server-side only and usage migration preserves
	// existing purposes while accepting the new one.
	if e = s.AddAIUsage(ctx, time.Now(), "test-model", "artist_alias", AIUsage{Requests: 1}); e != nil {
		t.Fatal(e)
	}
}

func TestArtistAliasSubjectsLargeLibrary(t *testing.T) {
	s := largeSearchTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, e := s.ArtistAliasSubjects(ctx, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(list) == 0 {
		t.Fatal("no performer candidates")
	}
}

func TestArtistAliasFailedTaskVisibleAheadOfQueuedHistory(t *testing.T) {
	s, _ := aliasFixture(t)
	ctx := context.Background()
	j := aliasClaim(t, s)
	if e := s.FinishArtistAliasJob(ctx, j, "failed", "DeepSeek 网页搜索失败（HTTP 401）"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 5; i++ {
		next := aliasClaim(t, s)
		if e := s.FinishArtistAliasJob(ctx, next, "missing", ""); e != nil {
			t.Fatal(e)
		}
	}
	visible, e := s.ArtistAliasJobs(ctx, 3)
	if e != nil || len(visible) != 3 || visible[0].ID != j.ID || visible[0].Status != "failed" {
		t.Fatal(visible, e)
	}
}
