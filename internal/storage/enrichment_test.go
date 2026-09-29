package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func openEnrichmentTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "enrichment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func seedEnrichmentEntities(t *testing.T, store *Store, ctx context.Context) (int64, int64, int64, int64) {
	t.Helper()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	var libraryID int64
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM libraries WHERE root_path='/music'`).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	artistResult, err := store.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Manual Artist','manual artist','manual artist')`)
	if err != nil {
		t.Fatal(err)
	}
	artistID, _ := artistResult.LastInsertId()
	albumResult, err := store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key,disc_count,user_label) VALUES(?,?,?,?,1,'Manual Label')`, libraryID, "Existing Album", "existing album", "album-key")
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := albumResult.LastInsertId()
	trackResult, err := store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number) VALUES(?,?,?,?,?)`, albumID, "Track", "track", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	trackID, _ := trackResult.LastInsertId()
	workResult, err := store.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type) VALUES('Existing Work','existing work','other')`)
	if err != nil {
		t.Fatal(err)
	}
	workID, _ := workResult.LastInsertId()
	return albumID, trackID, workID, artistID
}

func TestPhase4MigrationPreservesMetadataSourceSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings-upgrade.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	migration012, err := migrationFiles.ReadFile("migrations/012_phase4_enrichment.sql")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "012_phase4_enrichment.sql" {
			continue
		}
		script, readErr := migrationFiles.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = store.db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("apply pre-012 migration %s: %v", entry.Name(), err)
		}
	}
	_, err = store.db.ExecContext(ctx, `UPDATE metadata_source_settings SET enabled=1,priority=7,api_key='secret',language='ja',application_name='Custom',application_version='1.2.3',contact='owner@example.test',auto_match=0,cache_days=77,updated_at='2024-01-01T00:00:00Z' WHERE source='lastfm'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, string(migration012)); err != nil {
		t.Fatalf("apply migration 012: %v", err)
	}
	var setting MetadataSourceSetting
	var updatedAt string
	var enabled, auto int
	err = store.db.QueryRowContext(ctx, `SELECT source,enabled,priority,api_key,language,application_name,application_version,contact,auto_match,cache_days,updated_at FROM metadata_source_settings WHERE source='lastfm'`).Scan(&setting.Source, &enabled, &setting.Priority, &setting.APIKey, &setting.Language, &setting.ApplicationName, &setting.ApplicationVersion, &setting.Contact, &auto, &setting.CacheDays, &updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || setting.Priority != 7 || setting.APIKey != "secret" || setting.Language != "ja" || setting.ApplicationName != "Custom" || setting.ApplicationVersion != "1.2.3" || setting.Contact != "owner@example.test" || auto != 0 || setting.CacheDays != 77 || updatedAt != "2024-01-01T00:00:00Z" {
		t.Fatalf("settings not preserved: %#v enabled=%d auto=%d updatedAt=%q", setting, enabled, auto, updatedAt)
	}
	var oldTables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name IN ('metadata_source_settings_phase4_new','metadata_source_settings_phase4_old')`).Scan(&oldTables); err != nil || oldTables != 0 {
		t.Fatalf("temporary settings tables=%d err=%v", oldTables, err)
	}
}

func TestPhase4MigrationSourcesAndStrictTables(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	settings, err := store.MetadataSourceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, setting := range settings {
		found[setting.Source] = true
	}
	for _, source := range []string{"musicbrainz", "lastfm", "vgmdb", "bangumi"} {
		if !found[source] {
			t.Fatalf("metadata source %q missing: %#v", source, settings)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO metadata_source_settings(source) VALUES('invalid')`); err == nil {
		t.Fatal("metadata source CHECK accepted an invalid source")
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second migrate must be idempotent: %v", err)
	}
	var strict int
	if err := store.db.QueryRowContext(ctx, `SELECT strict FROM pragma_table_list WHERE name='enrichment_runs'`).Scan(&strict); err != nil || strict != 1 {
		t.Fatalf("enrichment_runs strict=%d err=%v", strict, err)
	}
}

func TestEnrichmentRunJSONTagsAreDistinct(t *testing.T) {
	value := EnrichmentRun{ID: 1, Status: "running", Scope: "album", Current: "current", ErrorMessage: "error", TargetID: 2, Force: true, Total: 10, Processed: 9, Succeeded: 8, Skipped: 7, Review: 6, Failed: 5, Stage: "tracks", StageAlbums: 11, StageTracks: 12, StageWorks: 13, CreatedAt: "created", StartedAt: "started", UpdatedAt: "updated", FinishedAt: "finished"}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"id": float64(1), "status": "running", "scope": "album", "current": "current", "errorMessage": "error", "targetId": float64(2), "force": true, "total": float64(10), "processed": float64(9), "succeeded": float64(8), "skipped": float64(7), "review": float64(6), "failed": float64(5), "stage": "tracks", "stageAlbums": float64(11), "stageTracks": float64(12), "stageWorks": float64(13), "createdAt": "created", "startedAt": "started", "updatedAt": "updated", "finishedAt": "finished"}
	if len(got) != len(want) {
		t.Fatalf("JSON keys=%v raw=%s", got, raw)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Fatalf("JSON %s=%v, want %v; raw=%s", key, got[key], expected, raw)
		}
	}
}

func TestEnrichmentRunsAndHTTPResponseCache(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	run, err := store.CreateEnrichmentRun(ctx, "albums", 0, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" || !run.Force || run.Total != 3 || run.StartedAt == "" {
		t.Fatalf("created run = %#v", run)
	}
	if err := store.UpdateEnrichmentRun(ctx, run.ID, EnrichmentRunUpdate{Total: 3, Processed: 2, Succeeded: 1, Review: 1, Current: "Album B"}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishEnrichmentRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	got, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil || got.Status != "completed" || got.Processed != 2 || got.FinishedAt == "" {
		t.Fatalf("finished run=%#v err=%v", got, err)
	}
	list, err := store.ListEnrichmentRuns(ctx, 10, 0)
	if err != nil || len(list) != 1 || list[0].ID != run.ID {
		t.Fatalf("runs=%#v err=%v", list, err)
	}

	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	entry := HTTPResponseCacheEntry{Source: "vgmdb", Key: "album:1", Status: 200, Body: []byte(`{"ok":true}`), ETag: `"v1"`, LastModified: "yesterday", ExpiresAt: expires}
	if err := store.PutHTTPResponseCache(ctx, entry); err != nil {
		t.Fatal(err)
	}
	entry.Body = []byte(`{"ok":false}`)
	entry.ETag = `"v2"`
	if err := store.PutHTTPResponseCache(ctx, entry); err != nil {
		t.Fatal(err)
	}
	cached, err := store.GetHTTPResponseCache(ctx, "vgmdb", "album:1")
	if err != nil || string(cached.Body) != `{"ok":false}` || cached.ETag != `"v2"` || cached.ExpiresAt != expires {
		t.Fatalf("cache=%#v err=%v", cached, err)
	}
}

func TestFailRunningEnrichmentRunsAfterRestart(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	runningA, err := store.CreateEnrichmentRun(ctx, "albums", 0, false, 4)
	if err != nil {
		t.Fatal(err)
	}
	runningB, err := store.CreateEnrichmentRun(ctx, "works", 0, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CreateEnrichmentRun(ctx, "artist", 0, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishEnrichmentRun(ctx, completed.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	affected, err := store.FailRunningEnrichmentRuns(ctx, "server restarted")
	if err != nil || affected != 2 {
		t.Fatalf("affected=%d err=%v", affected, err)
	}
	for _, id := range []int64{runningA.ID, runningB.ID} {
		run, getErr := store.EnrichmentRun(ctx, id)
		if getErr != nil || run.Status != "failed" || run.ErrorMessage != "server restarted" || run.Current != "" || run.FinishedAt == "" {
			t.Fatalf("recovered run=%#v err=%v", run, getErr)
		}
	}
	unchanged, err := store.EnrichmentRun(ctx, completed.ID)
	if err != nil || unchanged.Status != "completed" || unchanged.ErrorMessage != "" {
		t.Fatalf("completed run changed=%#v err=%v", unchanged, err)
	}
	affected, err = store.FailRunningEnrichmentRuns(ctx, "")
	if err != nil || affected != 0 {
		t.Fatalf("second recovery affected=%d err=%v", affected, err)
	}
}

func TestAlbumEnrichmentConservativeFillCreditsAndProvenance(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	albumID, trackID, _, _ := seedEnrichmentEntities(t, store, ctx)
	run, err := store.CreateEnrichmentRun(ctx, "album", albumID, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	profile := ExternalAlbumProfile{Source: "vgmdb", ExternalID: "SVWC-70658", Title: "Remote Album", ReleaseDate: "2024-01-02", CatalogNumber: "SVWC-70658", Label: "Remote Label", DiscCount: 2, Raw: json.RawMessage(`{"source":"vgmdb"}`)}
	if err := store.UpsertExternalAlbumProfile(ctx, albumID, profile); err != nil {
		t.Fatal(err)
	}
	patch := AlbumFieldPatch{Title: profile.Title, ReleaseDate: profile.ReleaseDate, ReleaseYear: 2024, CatalogNumber: profile.CatalogNumber, Label: profile.Label, Country: "JP", DiscCount: 2}
	if err := store.ConservativelyFillAlbum(ctx, albumID, patch, profile.Source, profile.ExternalID, run.ID); err != nil {
		t.Fatal(err)
	}
	var title, label, catalog, country string
	var year, discs int
	if err := store.db.QueryRowContext(ctx, `SELECT title,COALESCE(user_label,label,''),COALESCE(catalog_number,''),COALESCE(country,''),COALESCE(release_year,0),disc_count FROM albums WHERE id=?`, albumID).Scan(&title, &label, &catalog, &country, &year, &discs); err != nil {
		t.Fatal(err)
	}
	if title != "Existing Album" || label != "Manual Label" || catalog != "SVWC-70658" || country != "JP" || year != 2024 || discs != 2 {
		t.Fatalf("conservative album fill title=%q label=%q catalog=%q country=%q year=%d discs=%d", title, label, catalog, country, year, discs)
	}
	credits := []EnrichmentCredit{{TrackID: trackID, Name: "Composer Name", Role: "composer", ExternalID: "credit-1"}, {Name: "Album Performer", Role: "performer", ExternalID: "credit-2"}}
	if err := store.AddEnrichmentCredits(ctx, albumID, credits, "vgmdb", run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AddEnrichmentCredits(ctx, albumID, credits, "vgmdb", run.ID); err != nil {
		t.Fatal(err)
	}
	var trackCredits, albumCredits, provenance int
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artists WHERE track_id=? AND role='composer'`, trackID).Scan(&trackCredits)
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_artists WHERE album_id=?`, albumID).Scan(&albumCredits)
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM enrichment_provenance WHERE source='vgmdb' AND run_id=?`, run.ID).Scan(&provenance)
	if trackCredits != 1 || albumCredits != 1 || provenance < 5 {
		t.Fatalf("trackCredits=%d albumCredits=%d provenance=%d", trackCredits, albumCredits, provenance)
	}
	targets, err := store.AlbumsForEnrichment(ctx, "vgmdb", false, 10)
	if err != nil || len(targets) != 0 {
		t.Fatalf("already enriched album returned: %#v err=%v", targets, err)
	}
}

func TestTrackInvolvedPeopleRolesAndRemoteCreditSurvivesRescan(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, InvolvedPeople: []metadata.InvolvedPerson{{Role: "composed by", Name: "Tag Composer"}, {Role: "words", Name: "Tag Lyricist"}, {Role: "arranged by", Name: "Tag Arranger"}, {Role: "produced by", Name: "Tag Producer"}, {Role: "guitar", Name: "Tag Guitarist"}}}}
	if err := store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	var trackID, albumID int64
	if err := store.db.QueryRowContext(ctx, `SELECT t.id,t.album_id FROM tracks t JOIN audio_files f ON f.track_id=t.id WHERE f.relative_path=?`, input.RelativePath).Scan(&trackID, &albumID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTrackInvolvedPeople(ctx, trackID, input.Metadata.InvolvedPeople, "file_tag", 0); err != nil {
		t.Fatal(err)
	}
	wantRoles := map[string]string{"composed by": "composer", "words": "lyricist", "arranged by": "arranger", "produced by": "producer", "Drums / Percussion": "instrument:drums_percussion"}
	for inputRole, wantRole := range wantRoles {
		if got := NormalizeTrackArtistRole(inputRole); got != wantRole {
			t.Fatalf("NormalizeTrackArtistRole(%q)=%q want %q", inputRole, got, wantRole)
		}
	}
	var structuredRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artist_sources WHERE track_id=? AND source='file_tag' AND role IN ('composer','lyricist','arranger','producer','instrument:guitar')`, trackID).Scan(&structuredRows); err != nil || structuredRows != 5 {
		t.Fatalf("structured involved people rows=%d err=%v", structuredRows, err)
	}
	run, err := store.CreateEnrichmentRun(ctx, "album", albumID, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddEnrichmentCredits(ctx, albumID, []EnrichmentCredit{{TrackID: trackID, Name: "Remote Drummer", Role: "drums", ExternalID: "vgmdb-credit"}}, "vgmdb", run.ID); err != nil {
		t.Fatal(err)
	}
	input.ModifiedAtNS = 2
	input.Metadata.InvolvedPeople = []metadata.InvolvedPerson{{Role: "arranged by", Name: "Tag Arranger"}}
	if err := store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	var remoteImmediatelyAfterScan, remoteSourceRows int
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artists ta JOIN artists a ON a.id=ta.artist_id WHERE ta.track_id=? AND ta.role='instrument:drums' AND a.display_name='Remote Drummer'`, trackID).Scan(&remoteImmediatelyAfterScan)
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artist_sources s JOIN artists a ON a.id=s.artist_id WHERE s.track_id=? AND s.source='vgmdb' AND s.external_id='vgmdb-credit' AND a.display_name='Remote Drummer'`, trackID).Scan(&remoteSourceRows)
	if remoteImmediatelyAfterScan != 1 || remoteSourceRows != 1 {
		t.Fatalf("remote credit was lost during ImportTrack: materialized=%d source=%d", remoteImmediatelyAfterScan, remoteSourceRows)
	}
	if err := store.SaveTrackInvolvedPeople(ctx, trackID, input.Metadata.InvolvedPeople, "file_tag", 0); err != nil {
		t.Fatal(err)
	}
	var remoteRows, oldTagRows, newTagRows int
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artists ta JOIN artists a ON a.id=ta.artist_id WHERE ta.track_id=? AND ta.role='instrument:drums' AND a.display_name='Remote Drummer'`, trackID).Scan(&remoteRows)
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artist_sources s JOIN artists a ON a.id=s.artist_id WHERE s.track_id=? AND s.source='file_tag' AND a.display_name='Tag Guitarist'`, trackID).Scan(&oldTagRows)
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artist_sources s JOIN artists a ON a.id=s.artist_id WHERE s.track_id=? AND s.source='file_tag' AND s.role='arranger' AND a.display_name='Tag Arranger'`, trackID).Scan(&newTagRows)
	if remoteRows != 1 || oldTagRows != 0 || newTagRows != 1 {
		t.Fatalf("remote=%d oldTag=%d newTag=%d", remoteRows, oldTagRows, newTagRows)
	}
}

func TestBangumiCandidateConfirmationAndArtistRelations(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	_, _, workID, artistID := seedEnrichmentEntities(t, store, ctx)
	run, err := store.CreateEnrichmentRun(ctx, "work", workID, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	candidates := []WorkMatchCandidate{{Source: "bangumi", ExternalID: "123", Title: "Remote Work", TranslatedTitle: "Translated", Type: "anime", Year: 2023, PosterURL: "https://example/poster.jpg", Score: 95, Evidence: []string{"exact title"}, Payload: json.RawMessage(`{"id":123}`)}}
	if err := store.ReplaceWorkMatchCandidates(ctx, workID, candidates); err != nil {
		t.Fatal(err)
	}
	list, err := store.WorkMatchCandidates(ctx, workID)
	if err != nil || len(list) != 1 {
		t.Fatalf("work candidates=%#v err=%v", list, err)
	}
	if err := store.SetWorkMatchCandidateStatus(ctx, workID, list[0].ID, "rejected"); err != nil {
		t.Fatal(err)
	}
	rejected, err := store.WorkMatchCandidates(ctx, workID)
	if err != nil || len(rejected) != 1 || rejected[0].Status != "rejected" {
		t.Fatalf("rejected work candidates=%#v err=%v", rejected, err)
	}
	if err := store.SetWorkMatchCandidateStatus(ctx, workID, list[0].ID, "candidate"); err == nil {
		t.Fatal("invalid work candidate status was accepted")
	}
	if err := store.SetWorkMatchCandidateStatus(ctx, workID, 999999, "rejected"); err != sql.ErrNoRows {
		t.Fatalf("missing work candidate error=%v", err)
	}
	if err := store.ReplaceWorkMatchCandidates(ctx, workID, candidates); err != nil {
		t.Fatal(err)
	}
	list, err = store.WorkMatchCandidates(ctx, workID)
	if err != nil || len(list) != 1 {
		t.Fatalf("work candidates after replace=%#v err=%v", list, err)
	}
	if err := store.ConfirmWorkMatchCandidate(ctx, workID, list[0].ID, run.ID); err != nil {
		t.Fatal(err)
	}
	var translated, workType, poster string
	var year int
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(translated_title,''),type,COALESCE(year,0),COALESCE(poster_url,'') FROM works WHERE id=?`, workID).Scan(&translated, &workType, &year, &poster); err != nil {
		t.Fatal(err)
	}
	if translated != "Translated" || workType != "anime" || year != 2023 || poster == "" {
		t.Fatalf("confirmed work translated=%q type=%q year=%d poster=%q", translated, workType, year, poster)
	}
	if targets, err := store.WorksForEnrichment(ctx, "bangumi", false, 10); err != nil || len(targets) != 0 {
		t.Fatalf("already enriched work returned: %#v err=%v", targets, err)
	}

	relations := []ArtistRelationCandidate{{Source: "musicbrainz", ExternalID: "mb-main", RelatedExternalID: "mb-alias", RelatedName: "Alias", RelationType: "alias_of", Score: 90, Evidence: []string{"source relation"}}}
	if err := store.ReplaceArtistRelationCandidates(ctx, artistID, relations); err != nil {
		t.Fatal(err)
	}
	relationList, err := store.ArtistRelationCandidates(ctx, artistID)
	if err != nil || len(relationList) != 1 {
		t.Fatalf("relations=%#v err=%v", relationList, err)
	}
	if err := store.SetArtistRelationCandidateStatus(ctx, artistID, relationList[0].ID, "confirmed"); err != nil {
		t.Fatal(err)
	}
	relationList, _ = store.ArtistRelationCandidates(ctx, artistID)
	if relationList[0].Status != "confirmed" {
		t.Fatalf("relation status=%q", relationList[0].Status)
	}
	if err := store.SetArtistRelationCandidateStatus(ctx, artistID, 999999, "rejected"); err != sql.ErrNoRows {
		t.Fatalf("missing relation error=%v", err)
	}
}

func TestWorksForEnrichmentIncludesBeyondThousandAndExcludesReviewed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "works-enrichment.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var reviewed int64
	for i := 0; i < 1005; i++ {
		work, e := store.CreateWork(ctx, WorkInput{Title: fmt.Sprintf("Enrichment work %d", i), Type: "anime"})
		if e != nil {
			t.Fatal(e)
		}
		attachWorkForEnrichmentTest(t, store, ctx, work.ID)
		if i == 500 {
			reviewed = work.ID
		}
	}
	if err = store.ReplaceWorkMatchCandidates(ctx, reviewed, []WorkMatchCandidate{{Source: "bangumi", ExternalID: "1", Title: "Candidate"}}); err != nil {
		t.Fatal(err)
	}
	works, err := store.WorksForEnrichment(ctx, "bangumi", false, 0)
	if err != nil || len(works) != 1004 {
		t.Fatalf("works=%d err=%v", len(works), err)
	}
	for _, work := range works {
		if work.ID == reviewed {
			t.Fatal("reviewed work retried")
		}
	}
}
