package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// seedHomeLibrary imports one album with three tracks and returns their ids.
func seedHomeLibrary(t *testing.T, app *App, store *storage.Store) (int64, []int64) {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 3)
	for i, title := range []string{"OP Song", "ED Song", "BGM"} {
		input := storage.ImportInput{
			LibraryID: library.ID, RelativePath: "Artist/Album/" + title + ".flac",
			FileSize: 1024, ModifiedAtNS: int64(i + 1),
			Metadata: metadata.AudioMetadata{Title: title, Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, Year: 2024},
		}
		if err := store.ImportTrack(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Query: "Album"})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums = %v, %v", albums, err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{AlbumID: albums[0].ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range tracks {
		ids = append(ids, track.ID)
	}
	return albums[0].ID, ids
}

func loginCookie(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	return login.Result().Cookies()[0]
}

func getHome(t *testing.T, app *App, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHomePageEmptyLibrary(t *testing.T) {
	app, _, _ := setupTestApp(t)
	cookie := loginCookie(t, app)
	rec := getHome(t, app, cookie, "/admin/home")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "曲库还是空的") || !strings.Contains(body, "前往控制台扫描") {
		t.Fatalf("empty state missing: %s", body[:min(len(body), 400)])
	}
	if strings.Contains(body, "h-hero") || strings.Contains(body, "接着听") {
		t.Fatal("empty library must not render hero or resume sections")
	}
}

func TestHomePageRequiresAuth(t *testing.T) {
	app, _, _ := setupTestApp(t)
	rec := getHome(t, app, nil, "/admin/home")
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHomePageRendering(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	albumID, tracks := seedHomeLibrary(t, app, store)
	cookie := loginCookie(t, app)

	// Work link: OP role on the first track.
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "Anime A", Type: "anime", Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: tracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorkAlbum(ctx, work.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}
	// Unfinished playback on the second track.
	if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{ClientID: "test", ClientKind: "web", SessionID: "session-home-test-1", Seq: 1, Type: "start", TrackID: tracks[1], State: "playing", DurationMillis: 240000}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{ClientID: "test", ClientKind: "web", SessionID: "session-home-test-1", Seq: 2, Type: "pause", TrackID: tracks[1], PositionMillis: 64000, DurationMillis: 240000}); err != nil {
		t.Fatal(err)
	}

	rec := getHome(t, app, cookie, "/admin/home")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"主页", "最新入库", "最近在听", "随机重温", "作品聚焦", "接着听", "最近添加", "按作品漫游", "主题曲与插曲", "编年", "Anime A", "OP Song"} {
		if !strings.Contains(body, want) {
			t.Errorf("home page missing %q", want)
		}
	}
	// Resume row carries the breakpoint for the player to seek to.
	if !strings.Contains(body, `data-resume-ms="64000"`) {
		t.Error("resume position missing")
	}
	// 从 x:xx 继续 button.
	if !strings.Contains(body, "从 1:04 继续") {
		t.Error("resume label missing")
	}
	// Chronicle bar for 2024 and footer review entry.
	if !strings.Contains(body, `data-year="2024"`) || !strings.Contains(body, "作品关联待审核") {
		t.Error("chronicle/footer missing")
	}
	// Work card links and role badge.
	if !strings.Contains(body, `data-play-work=`) || !strings.Contains(body, "h-role-op") {
		t.Error("works rail / role rows missing")
	}
}

func TestHomePageNoPlaybackHistory(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedHomeLibrary(t, app, store)
	cookie := loginCookie(t, app)
	rec := getHome(t, app, cookie, "/admin/home")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "最近在听") {
		t.Error("listening tab must be hidden without playback history")
	}
	if !strings.Contains(body, "还没有未听完的歌曲") {
		t.Error("resume empty state missing")
	}
}

func TestHomeRandomSpotlightFragment(t *testing.T) {
	app, store, _ := setupTestApp(t)
	albumID, _ := seedHomeLibrary(t, app, store)
	cookie := loginCookie(t, app)

	rec := getHome(t, app, cookie, "/admin/home/spotlight/random")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "h-hero-inner") || !strings.Contains(body, "换一张") || !strings.Contains(body, "Album") {
		t.Fatalf("fragment unexpected: %s", body[:min(len(body), 300)])
	}
	// Excluding the only album falls back to returning it anyway.
	rec = getHome(t, app, cookie, "/admin/home/spotlight/random?exclude="+itoa(albumID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Album") {
		t.Fatalf("exclude fallback: %d", rec.Code)
	}
	rec = getHome(t, app, nil, "/admin/home/spotlight/random")
	if rec.Code == http.StatusOK {
		t.Fatal("fragment must require auth")
	}
}

func TestHomeChronicleFragment(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedHomeLibrary(t, app, store)
	cookie := loginCookie(t, app)

	rec := getHome(t, app, cookie, "/admin/home/chronicle?year=2024")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "2024 年") {
		t.Fatalf("chronicle: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/admin/albums?year=2024") {
		t.Fatal("chronicle result missing link to filtered album page")
	}
	if rec := getHome(t, app, cookie, "/admin/home/chronicle?year=abc"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad year status = %d", rec.Code)
	}
	if rec := getHome(t, app, cookie, "/admin/home/chronicle?year=1999"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "1999 年没有专辑") {
		t.Fatalf("empty year: %d %s", rec.Code, rec.Body.String())
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
