package enrichment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// F3: Files in testdata are trimmed responses fetched from api.bgm.tv with the
// lux032/032music-server-dev User-Agent and >=300ms between upstream requests.
func fixtureBangumiServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/search/subjects" {
			var req struct {
				Keyword string `json:"keyword"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				http.Error(w, "bad request", 400)
				return
			}
			keyword := stripMusicVersion(req.Keyword)
			var ids []int
			switch keyword {
			case "Amore":
				ids = []int{507031, 660542}
			case "結束バンド":
				ids = []int{406604}
			case "結束バンドLIVE-恒星":
				ids = []int{512098}
			case "人間開花":
				ids = []int{198229}
			case "君の名は。":
				ids = []int{187695}
			case "infinite synthesis 6":
				ids = []int{375293}
			default:
				t.Errorf("unexpected search keyword %q", keyword)
			}
			var hits []json.RawMessage
			for _, id := range ids {
				raw, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("%d.json", id)))
				if err != nil {
					t.Error(err)
					continue
				}
				hits = append(hits, raw)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": hits})
			return
		}
		tail := strings.TrimPrefix(r.URL.Path, "/v0/subjects/")
		parts := strings.Split(tail, "/")
		if len(parts) < 1 {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		suffix := ""
		if len(parts) == 2 {
			suffix = "-" + parts[1]
		}
		raw, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("%d%s.json", id, suffix)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(raw)
	}))
}

func TestF3BangumiFixtureEndToEndMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, title, artist, date   string
		wantOutcome, role, workType string
		tieups                      int
	}{
		{name: "Amore OP", title: "Amore", artist: "ReoNa", date: "2026-07-22", wantOutcome: "succeeded", role: "op", workType: "anime", tieups: 1},
		{name: "Amore version 20 day delta", title: "Amore (初回限定盤)", artist: "ReoNa", date: "2026-07-02", wantOutcome: "review", workType: "anime", tieups: 1},
		{name: "507031 same title different singer", title: "Amore", artist: "IZNA", date: "2024-08-12", wantOutcome: "review", tieups: 0},
		{name: "406604 TV and two movies", title: "結束バンド", artist: "長谷川育美", date: "2022-12-28", wantOutcome: "review", tieups: 3},
		{name: "512098 type six ignored", title: "結束バンドLIVE-恒星", artist: "結束バンド", date: "2023-11-22", wantOutcome: "review", tieups: 1},
		{name: "198229 movie generic", title: "人間開花", artist: "RADWIMPS", date: "2016-11-23", wantOutcome: "review", tieups: 1, workType: "movie"},
		{name: "187695 movie OST", title: "君の名は。", artist: "RADWIMPS", date: "2016-08-24", wantOutcome: "succeeded", role: "ost", workType: "movie", tieups: 1},
		{name: "375293 game opening", title: "infinite synthesis 6", artist: "fripSide", date: "2022-03-23", wantOutcome: "succeeded", role: "op", workType: "game", tieups: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := storage.Open(filepath.Join(t.TempDir(), "fixture.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err = store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
				t.Fatal(err)
			}
			lib, _ := store.LibraryByRoot(ctx, "/music")
			err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: tc.title, Artists: []string{tc.artist}, AlbumArtists: []string{tc.artist}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size", Raw: map[string][]string{"DATE": {tc.date}}}})
			if err != nil {
				t.Fatal(err)
			}
			server := fixtureBangumiServer(t)
			defer server.Close()
			manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
			manager.phaseEndpoints.BangumiAPI = server.URL
			manager.client = server.Client()
			manager.bangumiInterval = 0
			setting, err := store.MetadataSourceSetting(ctx, "bangumi")
			if err != nil {
				t.Fatal(err)
			}
			setting.Enabled = true
			setting.AutoMatch = true
			if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
				t.Fatal(err)
			}
			targets, err := store.AlbumsForBangumiTieup(ctx, true, 0)
			if err != nil || len(targets) != 1 {
				t.Fatalf("targets=%+v err=%v", targets, err)
			}
			outcome, err := manager.enrichBangumiAlbum(ctx, 0, targets[0], false)
			if err != nil || outcome != tc.wantOutcome {
				t.Fatalf("outcome=%q expected=%q err=%v", outcome, tc.wantOutcome, err)
			}
			candidates, err := store.AlbumSubjectCandidates(ctx, targets[0].ID)
			if err != nil || len(candidates) == 0 {
				t.Fatalf("candidates=%+v err=%v", candidates, err)
			}
			best := candidates[0]
			if tc.tieups > 0 && len(best.Tieups) != tc.tieups {
				t.Fatalf("tieups=%+v expected=%d", best.Tieups, tc.tieups)
			}
			if tc.workType != "" {
				if len(best.Tieups) == 0 || best.Tieups[0].Type != tc.workType {
					t.Fatalf("work type: %+v", best.Tieups)
				}
			}
			works, err := store.ListWorks(ctx, storage.WorkFilters{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantOutcome == "succeeded" {
				if len(works) != 1 {
					t.Fatalf("works %+v", works)
				}
				links, err := store.AlbumsForWork(ctx, works[0].ID)
				wantAlbumRole := "other"
				if tc.role == "ost" {
					wantAlbumRole = "ost"
				}
				if err != nil || len(links) != 1 || links[0].Role != wantAlbumRole || links[0].Source != "bangumi" {
					t.Fatalf("links=%+v err=%v", links, err)
				}
				tracks, err := store.TracksForWork(ctx, works[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				// Fixture albums use a placeholder track title, so only the album-level
				// association is expected (including IS6 and OST).
				if len(tracks) != 1 || tracks[0].Source != "album" || tracks[0].Role != wantAlbumRole {
					t.Fatalf("tracks=%+v", tracks)
				}
			} else if len(works) != 0 {
				t.Fatalf("review created works: %+v", works)
			}
		})
	}
}

// F3/H1: real 375293 is a game OP in Bangumi's reverse relation; no
// heuristic may veto this unique, concrete role solely because it is an album.
func TestRVL7AmoreFixtureLinksMainAndVersionsButNotCW(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "amore-fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	tracks := []struct{ title, typ string }{
		{"Amore", "regular"}, {"それは魔法でした", "regular"}, {"心痛", "regular"}, {"結々の唄", "regular"},
		{"Amore (TV ver.)", "tv_size"}, {"Amore (Instrumental)", "instrumental"},
	}
	for i, track := range tracks {
		if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("Amore/%02d.flac", i+1), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: track.title, Album: "Amore", Artists: []string{"ReoNa"}, AlbumArtists: []string{"ReoNa"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: track.typ, Raw: map[string][]string{"DATE": {"2026-07-22"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	server := fixtureBangumiServer(t)
	defer server.Close()
	manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.phaseEndpoints.BangumiAPI, manager.client, manager.bangumiInterval = server.URL, server.Client(), 0
	setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
	setting.Enabled, setting.AutoMatch = true, true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	targets, _ := store.AlbumsForBangumiTieup(ctx, true, 0)
	if outcome, err := manager.enrichBangumiAlbum(ctx, 0, targets[0], false); err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	works, _ := store.ListWorks(ctx, storage.WorkFilters{})
	if len(works) != 1 {
		t.Fatalf("works=%+v", works)
	}
	albums, _ := store.AlbumsForWork(ctx, works[0].ID)
	linked, _ := store.TracksForWork(ctx, works[0].ID)
	opTracks, cwTracks := 0, 0
	for _, track := range linked {
		if track.Source == "bangumi" && track.Role == "op" {
			opTracks++
		}
		if track.Title == "それは魔法でした" || track.Title == "心痛" || track.Title == "結々の唄" {
			if track.Source == "bangumi" {
				cwTracks++
			}
		}
	}
	if len(albums) != 1 || albums[0].Source != "bangumi" || opTracks != 3 || cwTracks != 0 {
		t.Fatalf("albums=%+v opTracks=%d cwTracks=%d linked=%+v", albums, opTracks, cwTracks, linked)
	}
}

func TestF3Bangumi375293GameOPIsAuthoritative(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "269645-subjects.json"))
	if err != nil {
		t.Fatal(err)
	}
	var relations []musicRelation
	if err = json.Unmarshal(data, &relations); err != nil {
		t.Fatal(err)
	}
	for _, relation := range relations {
		if relation.ID == 375293 {
			if relationRole(relation.Relation) != "op" {
				t.Fatalf("reverse relation %q", relation.Relation)
			}
			return
		}
	}
	t.Fatal("375293 absent from game's reverse relations")
}
