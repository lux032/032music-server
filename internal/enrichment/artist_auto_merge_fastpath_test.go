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

func TestTaggedMBIDLocalProfileFastPathAndRemoteFallback(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "fastpath.db"))
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

	const mbid = "11111111-1111-4111-8111-111111111111"
	var mbRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mbRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "11111111-1111-4111-8111-111111111111",
			"name": "梶浦由記",
			"sort-name": "Kajiura, Yuki",
			"country": "JP",
			"type": "Person",
			"aliases": [
				{"name": "Yuki Kajiura", "locale": "en", "primary": true, "type": "Artist name"},
				{"name": "Kajiura Yuki", "locale": "en", "primary": false, "type": "Artist name"}
			],
			"tags": [],
			"relations": []
		}`))
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(ctx, store, logger, t.TempDir())
	m.client = srv.Client()
	m.musicBrainzBase = srv.URL + "/ws/2"
	m.gate = sourcegate.New(sourcegate.Options{
		Intervals: map[string]time.Duration{"musicbrainz": 0},
		Now:       time.Now,
		Sleep:     func(context.Context, time.Duration) error { return nil },
	})

	importTrack := func(path, artistName, tagMBID string) int64 {
		t.Helper()
		raw := map[string][]string{"ARTIST": {artistName}}
		if tagMBID != "" {
			raw["MUSICBRAINZ_ARTISTID"] = []string{tagMBID}
		}
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    1,
			RelativePath: path,
			FileSize:     100,
			ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title:        path,
				Album:        "Album " + path,
				DiscNumber:   1,
				TrackNumber:  1,
				Container:    "flac",
				Artists:      []string{artistName},
				AlbumArtists: []string{artistName},
				Raw:          raw,
			},
		}); err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		all, err := store.ArtistsForMatching(ctx)
		if err != nil {
			t.Fatalf("ArtistsForMatching: %v", err)
		}
		for _, a := range all {
			if a.Name == artistName {
				return a.ID
			}
		}
		t.Fatalf("artist %q not found", artistName)
		return 0
	}

	// Seed canonical owner 梶浦由記 with locally cached MB profile containing "Yuki Kajiura".
	jpID := importTrack("jp.flac", "梶浦由記", mbid)
	if err = store.UpsertExternalArtistProfile(ctx, jpID, storage.ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		SortName:    "Kajiura, Yuki",
		Country:     "JP",
		Aliases:     []string{"Yuki Kajiura"},
		Raw:         []byte(`{"aliases":["Yuki Kajiura"]}`),
	}); err != nil {
		t.Fatalf("seed jp MB profile: %v", err)
	}

	// 1. New Romanized artist "Yuki Kajiura" with same TaggedMBID hits local cache -> ZERO MB HTTP requests!
	romajiID := importTrack("romaji.flac", "Yuki Kajiura", mbid)
	res, err := m.matchArtistWithMode(ctx, romajiID, true, storage.ArtistScanIncremental)
	if err != nil {
		t.Fatalf("matchArtistWithMode fast-path: %v", err)
	}
	if !res.AutoMatched || res.MergedIntoArtistID != jpID {
		t.Fatalf("expected AutoMatched & MergedIntoArtistID=%d, got %+v", jpID, res)
	}
	if got := mbRequests.Load(); got != 0 {
		t.Fatalf("expected 0 remote MusicBrainz HTTP requests on local cache hit, got %d", got)
	}

	// 2. Artist whose spelling is NOT in local cached aliases ("Kajiura Yuki") falls back to remote MB lookup (1 HTTP call),
	// validates against remote alias, and auto-merges into jpID.
	altID := importTrack("alt.flac", "Kajiura Yuki", mbid)
	resAlt, err := m.matchArtistWithMode(ctx, altID, true, storage.ArtistScanIncremental)
	if err != nil {
		t.Fatalf("matchArtistWithMode fallback alias: %v", err)
	}
	if !resAlt.AutoMatched || resAlt.MergedIntoArtistID != jpID {
		t.Fatalf("expected fallback auto-merge into %d, got %+v", jpID, resAlt)
	}
	if got := mbRequests.Load(); got != 1 {
		t.Fatalf("expected 1 remote MusicBrainz HTTP request on local alias miss, got %d", got)
	}

	// 3. Mis-tagged unrelated artist falls back to remote MB lookup, fails name match, and MUST NOT auto-merge.
	wrongID := importTrack("wrong.flac", "Totally Unrelated Singer", mbid)
	resWrong, err := m.matchArtistWithMode(ctx, wrongID, true, storage.ArtistScanIncremental)
	if err != nil {
		t.Fatalf("matchArtistWithMode mis-tagged: %v", err)
	}
	if resWrong.AutoMatched || resWrong.MergedIntoArtistID != 0 {
		t.Fatalf("expected mis-tagged artist NOT auto-merged, got %+v", resWrong)
	}
	if got := mbRequests.Load(); got != 2 {
		t.Fatalf("expected remote fallback lookup for mis-tagged artist (total 2), got %d", got)
	}
}
