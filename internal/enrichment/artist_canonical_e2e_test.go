package enrichment

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
)

func TestArtistCanonicalNamingAutoMergeSearchRescanAndRollbackE2E(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err = store.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	if err = store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{
		Source:    "musicbrainz",
		Enabled:   true,
		AutoMatch: true,
		Priority:  10,
		CacheDays: 30,
		Contact:   "test@example.com",
	}); err != nil {
		t.Fatalf("enable musicbrainz: %v", err)
	}

	const mbid = "99999999-9999-4999-8999-999999999999"
	var mbRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mbRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "99999999-9999-4999-8999-999999999999",
			"name": "梶浦由記",
			"sort-name": "Kajiura, Yuki",
			"country": "JP",
			"type": "Person",
			"aliases": [
				{"name": "Yuki Kajiura", "locale": "en", "primary": true, "type": "Artist name"}
			],
			"tags": [{"name": "anime"}],
			"relations": []
		}`))
	}))
	defer srv.Close()

	m := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	m.client = srv.Client()
	m.musicBrainzBase = srv.URL + "/ws/2"
	m.gate = sourcegate.New(sourcegate.Options{
		Intervals: map[string]time.Duration{"musicbrainz": 0},
		Now:       time.Now,
		Sleep:     func(context.Context, time.Duration) error { return nil },
	})

	// 1. Track 1 arrives with Romanized album_artist "Yuki Kajiura" + MUSICBRAINZ_ALBUMARTISTID.
	if err = store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    1,
		RelativePath: "ost1/01.flac",
		FileSize:     1000,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Opening",
			Album:        "OST 1",
			DiscNumber:   1,
			TrackNumber:  1,
			Container:    "flac",
			Artists:      []string{"Guest Singer"},
			AlbumArtists: []string{"Yuki Kajiura"},
			Raw: map[string][]string{
				"ARTIST":                    {"Guest Singer"},
				"ALBUMARTIST":               {"Yuki Kajiura"},
				"MUSICBRAINZ_ALBUMARTISTID": {mbid},
			},
		},
	}); err != nil {
		t.Fatalf("import track 1: %v", err)
	}

	var romajiID int64
	all, _ := store.ArtistsForMatching(ctx)
	for _, a := range all {
		if a.Name == "Yuki Kajiura" {
			romajiID = a.ID
			if a.TaggedMBID != mbid {
				t.Fatalf("expected album_artist TaggedMBID=%s, got %q", mbid, a.TaggedMBID)
			}
		}
	}
	if romajiID == 0 {
		t.Fatalf("Yuki Kajiura artist not created")
	}

	// Auto-match confirms MB identity (1 MB call) and canonicalizes display_name to 梶浦由記.
	res1, err := m.matchArtistWithMode(ctx, romajiID, true, storage.ArtistScanIncremental)
	if err != nil || !res1.AutoMatched {
		t.Fatalf("first match failed: %+v err=%v", res1, err)
	}
	if mbRequests.Load() != 1 {
		t.Fatalf("expected 1 MB HTTP request for first-time MBID, got %d", mbRequests.Load())
	}
	detail1, err := store.ArtistDetail(ctx, romajiID)
	if err != nil || detail1.Name != "梶浦由記" {
		t.Fatalf("expected canonicalized display_name 梶浦由記, got %q err=%v", detail1.Name, err)
	}

	// Search by original Romanized spelling "Yuki Kajiura" finds 梶浦由記.
	searchRes, err := store.Search(ctx, storage.SearchOptions{Query: "Yuki Kajiura", Limit: 10})
	if err != nil {
		t.Fatalf("search by romanized alias: %v", err)
	}
	foundByRomaji := false
	for _, a := range searchRes.Artists.Items {
		if a.ID == romajiID && a.Name == "梶浦由記" {
			foundByRomaji = true
		}
	}
	if !foundByRomaji {
		t.Fatalf("expected 'Yuki Kajiura' search to resolve canonical 梶浦由記, got %+v", searchRes.Artists.Items)
	}

	// 2. Track 2 arrives tagged with native "梶浦由記" + same MBID -> local fast-path (0 extra MB requests) auto-merges into romajiID!
	if err = store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    1,
		RelativePath: "ost2/01.flac",
		FileSize:     1000,
		ModifiedAtNS: 2,
		Metadata: metadata.AudioMetadata{
			Title:        "Ending",
			Album:        "OST 2",
			DiscNumber:   1,
			TrackNumber:  1,
			Container:    "flac",
			Artists:      []string{"梶浦由記"},
			AlbumArtists: []string{"梶浦由記"},
			Raw: map[string][]string{
				"ARTIST":               {"梶浦由記"},
				"MUSICBRAINZ_ARTISTID": {mbid},
			},
		},
	}); err != nil {
		t.Fatalf("import track 2: %v", err)
	}

	var jpRowID int64
	all, _ = store.ArtistsForMatching(ctx)
	for _, a := range all {
		if a.ID != romajiID && a.Name == "梶浦由記" {
			jpRowID = a.ID
		}
	}
	if jpRowID == 0 {
		t.Fatalf("expected separate raw row for 梶浦由記 before auto-merge")
	}
	res2, err := m.matchArtistWithMode(ctx, jpRowID, true, storage.ArtistScanIncremental)
	if err != nil || !res2.AutoMatched || res2.MergedIntoArtistID != romajiID {
		t.Fatalf("expected track 2 artist auto-merged into %d, got %+v err=%v", romajiID, res2, err)
	}
	if mbRequests.Load() != 1 {
		t.Fatalf("expected 0 extra MB requests via local cached profile fast-path (total still 1), got %d", mbRequests.Load())
	}

	// 3. Track 3 arrives tagged again with "梶浦由記" and "Yuki Kajiura" -> reuses canonical row directly with no new artists.
	if err = store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    1,
		RelativePath: "ost3/01.flac",
		FileSize:     1000,
		ModifiedAtNS: 3,
		Metadata: metadata.AudioMetadata{
			Title:        "Insert",
			Album:        "OST 3",
			DiscNumber:   1,
			TrackNumber:  1,
			Container:    "flac",
			Artists:      []string{"Yuki Kajiura"},
			AlbumArtists: []string{"梶浦由記"},
		},
	}); err != nil {
		t.Fatalf("import track 3: %v", err)
	}
	allAfterRescan, _ := store.ArtistsForMatching(ctx)
	canonicalKajiura := 0
	for _, a := range allAfterRescan {
		if a.Name == "梶浦由記" || a.Name == "Yuki Kajiura" {
			canonicalKajiura++
		}
	}
	if canonicalKajiura != 1 {
		t.Fatalf("expected exactly 1 canonical 梶浦由記 artist after rescan, got %d (%+v)", canonicalKajiura, allAfterRescan)
	}

	// 4. Admin rolls back merge -> subsequent match run MUST NOT re-auto-merge the rolled-back pair.
	ops, err := store.MergeOperations(ctx)
	if err != nil || len(ops) != 1 || ops[0].TriggerSource != storage.MergeTriggerAutoMatch {
		t.Fatalf("expected 1 auto_match merge op, got %+v err=%v", ops, err)
	}
	if err = store.RollbackArtistMerge(ctx, ops[0].ID); err != nil {
		t.Fatalf("rollback merge: %v", err)
	}
	resAfterRollback, err := m.matchArtistWithMode(ctx, jpRowID, true, storage.ArtistScanFull)
	if err != nil {
		t.Fatalf("match after rollback: %v", err)
	}
	if resAfterRollback.AutoMatched || resAfterRollback.MergedIntoArtistID != 0 {
		t.Fatalf("expected rolled-back pair NOT re-auto-merged, got %+v", resAfterRollback)
	}
}
