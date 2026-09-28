package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// trackFixtureServer serves the trimmed api.bgm.tv responses captured for the
// track-level lookup. Search pages are keyed by the processed keyword so the
// test fails if a leading hyphen is still sent (H1).
func trackFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	pages := map[string][]string{
		"legendary future":            {"search-legendary-future.json"},
		"Leap of faith":               {"search-leap-of-faith.json"},
		"紅蓮華":                         {"search-guren.json"},
		"only my railgun":             {"search-railgun.json"},
		"only my railgun version2020": {"search-railgun-2020.json"},
		"カタオモイ":                       {"search-kataomoi.json"},
		"星座になれたら":                     {"search-seiza.json"},
		"amore":                       {"search-amore.json"},
		"Amore":                       {"search-amore.json"},
		"朝が来る":                        {"search-asa.json"},
		"赤い罠(who loves it?)":          {"search-akawan.json"},
		"ADAMAS":                      {"search-adamas.json"},
		"残響散歌":                        {"search-zankyo.json"},
		"hello":                       {"search-empty.json"},
	}
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
			if strings.Contains(req.Keyword, "-") {
				t.Errorf("search keyword still contains a hyphen: %q", req.Keyword)
			}
			files := pages[req.Keyword]
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			page := offset / 20
			if page >= len(files) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
				return
			}
			raw, err := os.ReadFile(filepath.Join("testdata", files[page]))
			if err != nil {
				t.Error(err)
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(raw)
			return
		}
		tail := strings.TrimPrefix(r.URL.Path, "/v0/subjects/")
		parts := strings.Split(tail, "/")
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

var trackTestDBPath = map[*storage.Store]string{}

func newTrackManager(t *testing.T, server *httptest.Server) (*Manager, *storage.Store, int64) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "tracks.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	trackTestDBPath[store] = dbPath
	t.Cleanup(func() {
		delete(trackTestDBPath, store)
		store.Close()
	})
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.phaseEndpoints.BangumiAPI = server.URL
	manager.client = server.Client()
	manager.bangumiInterval = 0
	setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
	setting.Enabled = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	return manager, store, lib.ID
}

func importTrackSong(t *testing.T, store *storage.Store, lib int64, album, title, artist, trackType string) storage.TrackBangumiTarget {
	t.Helper()
	ctx := context.Background()
	path := fmt.Sprintf("%s/%s.flac", album, title)
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib, RelativePath: path, FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: album, Artists: []string{artist}, AlbumArtists: []string{"Various"}, DiscNumber: 1, TrackNumber: 1, TrackType: trackType}}); err != nil {
		t.Fatal(err)
	}
	targets, err := store.TracksForBangumiTieup(ctx, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.Title == title && target.AlbumTitle == album {
			return target
		}
	}
	t.Fatalf("track %q not eligible", title)
	return storage.TrackBangumiTarget{}
}

func TestTrackBangumiFixtures(t *testing.T) {
	server := trackFixtureServer(t)
	defer server.Close()
	for _, tc := range []struct {
		title, artist, trackType string
		want                     string
		work                     int64
		role                     string
		kind                     string
	}{
		{title: "legendary future", artist: "fripSide", want: "succeeded", work: 305621, role: "op"},
		{title: "Leap of faith", artist: "fripSide", want: "succeeded", work: 326870, role: "op"},
		{title: "紅蓮華", artist: "LiSA", want: "succeeded", work: 245665, role: "op"},
		{title: "紅蓮華 (TV Size)", artist: "LiSA", trackType: "tv_size", want: "succeeded", work: 245665, role: "op", kind: "version_base"},
		{title: "only my railgun", artist: "fripSide", want: "succeeded", work: 2585, role: "op"},
		{title: "星座になれたら", artist: "長谷川育美", want: "succeeded", work: 328609, role: "insert"},
		{title: "Amore (TV ver.)", artist: "ReoNa", trackType: "tv_size", want: "succeeded", work: 541285, role: "op", kind: "version_base"},
		{title: "Amore (Instrumental)", artist: "ReoNa", trackType: "instrumental", want: "succeeded", work: 541285, role: "op", kind: "version_base"},
		// 303221 has no work relation, so the re-recording must not fall through to 2585.
		{title: "only my railgun -version2020-", artist: "fripSide", want: "miss"},
		// The only catalogue hit is the FIRST TAKE recording, which is a different entry.
		{title: "カタオモイ", artist: "Aimer", want: "miss"},
		// Known limit: 残響散歌 has no multi-title entry under this keyword, so it is a miss.
		{title: "残響散歌", artist: "Aimer", want: "miss"},
		// 507031 is the only exact title and its singer is IZNA, so ReoNa's entry is discarded (D8).
		{title: "amore", artist: "IZNA", want: "miss"},
		{title: "Hello", artist: "Someone Else", want: "miss"},
		{title: "朝が来る", artist: "Aimer", want: "review", kind: "multi_title"},
		{title: "赤い罠(who loves it?)", artist: "LiSA", want: "review", kind: "multi_title"},
		{title: "ADAMAS", artist: "LiSA", want: "review", kind: "multi_title"},
	} {
		t.Run(tc.title+"/"+tc.artist, func(t *testing.T) {
			manager, store, lib := newTrackManager(t, server)
			ctx := context.Background()
			target := importTrackSong(t, store, lib, "Album "+tc.title, tc.title, tc.artist, tc.trackType)
			outcome, err := manager.enrichBangumiTrack(ctx, 0, target, false)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.want {
			case "miss":
				if outcome != "skipped" {
					t.Fatalf("outcome=%s", outcome)
				}
				if n := mustCount(t, store, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=?`, target.ID); n != 1 {
					t.Fatalf("miss=%d", n)
				}
				if n := mustCount(t, store, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='bangumi'`, target.ID); n != 0 {
					t.Fatalf("wrote %d links", n)
				}
			case "succeeded":
				if outcome != "succeeded" {
					t.Fatalf("outcome=%s", outcome)
				}
				role, external, _, err := store.TrackBangumiLink(ctx, target.ID)
				if err != nil || role != tc.role || external != strconv.FormatInt(tc.work, 10) {
					t.Fatalf("role=%s external=%s err=%v", role, external, err)
				}
				if tc.kind != "" {
					candidates, err := store.TrackSubjectCandidates(ctx, target.ID)
					if err != nil || len(candidates) != 1 || candidates[0].MatchKind != tc.kind || candidates[0].Status != "confirmed" {
						t.Fatalf("candidates=%+v err=%v", candidates, err)
					}
				}
			case "review":
				if outcome != "review" {
					t.Fatalf("outcome=%s", outcome)
				}
				if n := mustCount(t, store, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='bangumi'`, target.ID); n != 0 {
					t.Fatalf("auto wrote %d", n)
				}
				candidates, err := store.TrackSubjectCandidates(ctx, target.ID)
				if err != nil || len(candidates) == 0 || candidates[0].MatchKind != tc.kind || candidates[0].Status != "candidate" {
					t.Fatalf("candidates=%+v err=%v", candidates, err)
				}
				wantID := ""
				switch tc.title {
				case "朝が来る":
					wantID = "350774"
				case "赤い罠(who loves it?)", "ADAMAS":
					wantID = "262860"
				}
				if wantID != "" && candidates[0].ExternalID != wantID {
					t.Fatalf("external=%s want %s", candidates[0].ExternalID, wantID)
				}
			}
		})
	}
}

// B1 consistency: a multi-title entry that registers no work at all is a real
// miss, not a review candidate carrying an empty tieup list.
func TestTrackBangumiMultiTitleWithoutWorksIsMiss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			_, _ = io.WriteString(w, `{"data":[{"id":9001,"type":3,"name":"Alpha Song / Beta Song","infobox":[{"key":"艺术家","value":"Singer"}]}]}`)
		case "/v0/subjects/9001/subjects":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager, store, lib := newTrackManager(t, server)
	ctx := context.Background()
	target := importTrackSong(t, store, lib, "Alpha Album", "Alpha Song", "Singer", "")
	outcome, err := manager.enrichBangumiTrack(ctx, 0, target, false)
	if err != nil || outcome != "skipped" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := mustCount(t, store, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=?`, target.ID); n != 1 {
		t.Fatalf("miss=%d", n)
	}
	if n := mustCount(t, store, `SELECT COUNT(*) FROM track_subject_candidates WHERE track_id=?`, target.ID); n != 0 {
		t.Fatalf("candidates=%d", n)
	}
}

// readOnlyDB opens a read-only connection to the test store's database file
// so assertions cannot mutate state through the store's write connection. The
// path is registered by newTrackManager and phase4TestManager.
func readOnlyDB(t *testing.T, store *storage.Store) *sql.DB {
	t.Helper()
	path := trackTestDBPath[store]
	if path == "" {
		path = phase4DBPath[store]
	}
	if path == "" {
		t.Fatal("test store has no registered database path")
	}
	databasePath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		databasePath = "/" + databasePath
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustCount(t *testing.T, store *storage.Store, query string, args ...any) int {
	t.Helper()
	db := readOnlyDB(t, store)
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBangumiSearchKeywordStripsHyphen(t *testing.T) {
	if got := bangumiSearchKeyword("only my railgun -version2020-"); got != "only my railgun version2020" {
		t.Fatalf("keyword=%q", got)
	}
}

func TestSearchKeywordReachesRequestBody(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Keyword string `json:"keyword"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Keyword
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	manager, store, _ := newTrackManager(t, server)
	setting, _ := store.MetadataSourceSetting(context.Background(), "bangumi")
	_, err := manager.searchMusicSubjects(context.Background(), setting, "only my railgun -version2020-", true)
	if err != nil && !strings.Contains(err.Error(), "no rows") && err.Error() != "sql: no rows in result set" {
		if !isNoRows(err) {
			t.Fatal(err)
		}
	}
	if got != "only my railgun version2020" {
		t.Fatalf("body keyword=%q", got)
	}
}

func isNoRows(err error) bool {
	return err != nil && (err.Error() == "sql: no rows in result set" || strings.Contains(err.Error(), "no rows"))
}
