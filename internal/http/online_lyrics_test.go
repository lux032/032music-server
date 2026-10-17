package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/storage"
)

// fakeLyricsSource is a NetEase-like provider with scripted behaviour.
type fakeLyricsSource struct {
	mu       sync.Mutex
	searches int
	gets     int
	fail     bool
	none     bool
}

func (f *fakeLyricsSource) ID() string { return lyricsonline.SourceNetEase }

func (f *fakeLyricsSource) Search(_ context.Context, q lyricsonline.Query) ([]lyricsonline.Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches++
	if f.fail {
		return nil, errors.New("connection refused")
	}
	if f.none {
		return nil, nil
	}
	return []lyricsonline.Candidate{
		{Source: lyricsonline.SourceNetEase, ID: "1", Title: "Completely Different", Artist: "Nobody", DurationMs: 200000},
		{Source: lyricsonline.SourceNetEase, ID: "2", Title: q.Title + " (TV size)", Artist: "Band", DurationMs: 201000},
	}, nil
}

func (f *fakeLyricsSource) Get(_ context.Context, id string) (lyricsonline.Lyrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.fail {
		return lyricsonline.Lyrics{}, errors.New("connection refused")
	}
	switch id {
	case "2":
		return lyricsonline.Lyrics{Source: lyricsonline.SourceNetEase, ID: id,
			LRC:            "[00:01.00]online one\n[00:02.00]\n[00:03.00]online two",
			TranslationLRC: "[00:01.00]在线一\n[00:03.00]在线二"}, nil
	case "9":
		return lyricsonline.Lyrics{Source: lyricsonline.SourceNetEase, ID: id, Title: "Picked", Artist: "Chooser", LRC: "[00:05.00]picked line"}, nil
	}
	return lyricsonline.Lyrics{}, lyricsonline.ErrNoLyrics
}

func (f *fakeLyricsSource) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searches, f.gets
}

func getLyrics(t *testing.T, handler http.Handler, trackID int64) (apiLyricsResponse, int) {
	t.Helper()
	rec := apiRequest(handler, http.MethodGet, "/api/v1/tracks/"+strconv.FormatInt(trackID, 10)+"/lyrics", "")
	var body apiLyricsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return body, rec.Code
}

func TestOnlineLyricsLookupCacheAndTranslation(t *testing.T) {
	app, store, handler, cookie, trackID := lyricsTestApp(t, "夏の歌", "")
	ctx := context.Background()

	// Without the online client only local lyrics exist.
	if _, code := getLyrics(t, handler, trackID); code != http.StatusNotFound {
		t.Fatalf("no online client: %d", code)
	}
	if capabilityFeature(t, handler, "lyricsOnline") {
		t.Fatal("lyricsOnline capability without client")
	}

	src := &fakeLyricsSource{}
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(src))
	if !capabilityFeature(t, handler, "lyricsOnline") {
		t.Fatal("lyricsOnline capability")
	}

	// First request matches the right candidate (title with a version note, same length), caches it.
	got, code := getLyrics(t, handler, trackID)
	if code != http.StatusOK || got.Source != "netease" || got.SourceName != "NetEase" || !got.Synced || len(got.Lines) != 3 || got.Lines[0].Text != "online one" || got.Manual {
		t.Fatalf("online lyrics = %d %+v", code, got)
	}
	if got.RemoteTitle != "夏の歌 (TV size)" {
		t.Fatalf("remote title = %q", got.RemoteTitle)
	}
	searches, gets := src.counts()
	if _, code = getLyrics(t, handler, trackID); code != http.StatusOK {
		t.Fatal("cached request")
	}
	if s2, g2 := src.counts(); s2 != searches || g2 != gets {
		t.Fatal("second request must be served from the cache")
	}

	// NetEase's own translation is used for Simplified Chinese, aligned by timestamp, without AI.
	tr, code, body := translateRequest(handler, trackID, "zh-Hans")
	if code != http.StatusOK || tr.Source != "netease" || strings.Join(tr.Lines, "|") != "在线一||在线二" {
		t.Fatalf("netease translation: %d %s", code, body)
	}
	// Other languages still need the AI switch.
	if _, code, _ := translateRequest(handler, trackID, "en"); code != http.StatusConflict {
		t.Fatalf("english without AI = %d", code)
	}

	// The .lrc text endpoint returns the online LRC too.
	if rec := getWithCookie(handler, "/api/v1/tracks/"+strconv.FormatInt(trackID, 10)+"/lyrics.lrc", cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "[00:01.00]online one") {
		t.Fatalf("lrc text = %d %q", rec.Code, rec.Body.String())
	}

	// Search + preview + manual bind (applies to every client) + clear.
	rec := apiRequest(handler, http.MethodGet, "/api/v1/lyrics/search?"+url.Values{"name": {"夏の歌"}, "artist": {"Band"}, "durationMs": {"200000"}}.Encode(), "")
	var search struct {
		Results []lyricsonline.Candidate `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &search); err != nil || rec.Code != http.StatusOK || len(search.Results) != 2 || search.Results[0].ID != "2" {
		t.Fatalf("search = %d %s", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(handler, http.MethodGet, "/api/v1/lyrics/remote/netease/9", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "picked line") {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(handler, http.MethodGet, "/api/v1/lyrics/remote/genius/9", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown source = %d", rec.Code)
	}
	path := "/api/v1/tracks/" + strconv.FormatInt(trackID, 10) + "/lyrics/source"
	if rec := apiRequest(handler, http.MethodPut, path, `{"source":"netease","id":"404"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("bind song without lyrics = %d", rec.Code)
	}
	if rec := apiRequest(handler, http.MethodPut, path, `{"source":"netease","id":"9"}`); rec.Code != http.StatusOK {
		t.Fatalf("bind = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ = getLyrics(t, handler, trackID); !got.Manual || got.Lines[0].Text != "picked line" || got.RemoteTitle != "Picked" {
		t.Fatalf("after bind = %+v", got)
	}
	// The old AI/NetEase translation does not apply to the new lyrics.
	if _, code, _ := translateRequest(handler, trackID, "zh-Hans"); code != http.StatusConflict {
		t.Fatalf("translation after rebind = %d", code)
	}
	if rec := apiRequest(handler, http.MethodDelete, path, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d", rec.Code)
	}
	searches, _ = src.counts()
	if _, code = getLyrics(t, handler, trackID); code != http.StatusNotFound {
		t.Fatalf("cleared track = %d", code)
	}
	if s2, _ := src.counts(); s2 != searches {
		t.Fatal("cleared track must not be looked up again")
	}

	// Admin page lists it; deleting the row re-enables automatic matching.
	page := getWithCookie(handler, onlineLyricsPath, cookie).Body.String()
	if !strings.Contains(page, "已清除") || !strings.Contains(page, "夏の歌") {
		t.Fatal("admin page missing row")
	}
	if dash := getWithCookie(handler, "/admin", cookie).Body.String(); !strings.Contains(dash, `href="/admin/online-lyrics"`) {
		t.Fatal("dashboard card missing")
	}
	postSecurity(t, app, handler, cookie, onlineLyricsPath+"/delete", url.Values{"id": {strconv.FormatInt(trackID, 10)}})
	if got, code = getLyrics(t, handler, trackID); code != http.StatusOK || got.Manual || got.Lines[0].Text != "online one" {
		t.Fatalf("after admin delete = %d %+v", code, got)
	}
	if _, err := store.OnlineLyrics(ctx, trackID); err != nil {
		t.Fatal("re-matched lyrics must be cached again")
	}
}

func TestOnlineLyricsMissesAndFailures(t *testing.T) {
	app, store, handler, cookie, trackID := lyricsTestApp(t, "Rare Song", "")
	ctx := context.Background()
	src := &fakeLyricsSource{fail: true}
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(src))

	// Network failures are not cached.
	if _, code := getLyrics(t, handler, trackID); code != http.StatusNotFound {
		t.Fatalf("failure = %d", code)
	}
	if _, err := store.OnlineLyrics(ctx, trackID); err == nil {
		t.Fatal("a network failure must not be cached")
	}

	// A real miss is remembered for 12 hours.
	src.fail, src.none = false, true
	if _, code := getLyrics(t, handler, trackID); code != http.StatusNotFound {
		t.Fatal("miss")
	}
	row, err := store.OnlineLyrics(ctx, trackID)
	if err != nil || row.Status != storage.OnlineLyricsNotFound {
		t.Fatalf("miss row = %+v %v", row, err)
	}
	retry, _ := time.Parse(time.RFC3339Nano, row.RetryAfter)
	if d := time.Until(retry); d < 11*time.Hour || d > 13*time.Hour {
		t.Fatalf("retry after %v", d)
	}
	searches, _ := src.counts()
	getLyrics(t, handler, trackID)
	if s2, _ := src.counts(); s2 != searches {
		t.Fatal("a remembered miss must not search again")
	}
	// "Retry all misses" in the admin page clears them.
	src.none = false
	postSecurity(t, app, handler, cookie, onlineLyricsPath+"/clear-misses", url.Values{})
	if got, code := getLyrics(t, handler, trackID); code != http.StatusOK || got.Source != "netease" {
		t.Fatalf("after clearing misses = %d %+v", code, got)
	}
}

func TestLocalLyricsWinOverOnlineUnlessManual(t *testing.T) {
	app, _, handler, _, trackID := lyricsTestApp(t, "Local Song", "[00:01.00]local line")
	src := &fakeLyricsSource{}
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(src))

	got, code := getLyrics(t, handler, trackID)
	if code != http.StatusOK || got.Source != "embedded" || got.Lines[0].Text != "local line" {
		t.Fatalf("local = %d %+v", code, got)
	}
	if s, g := src.counts(); s != 0 || g != 0 {
		t.Fatal("local lyrics must not trigger an online lookup")
	}
	path := "/api/v1/tracks/" + strconv.FormatInt(trackID, 10) + "/lyrics/source"
	apiRequest(handler, http.MethodPut, path, `{"source":"netease","id":"9"}`)
	if got, _ = getLyrics(t, handler, trackID); !got.Manual || got.Lines[0].Text != "picked line" {
		t.Fatalf("manual must override local: %+v", got)
	}
	// Clearing the manual choice falls back to the local lyrics.
	apiRequest(handler, http.MethodDelete, path, "")
	if got, _ = getLyrics(t, handler, trackID); got.Source != "embedded" {
		t.Fatalf("after clear = %+v", got)
	}
}
