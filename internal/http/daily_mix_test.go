package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/dailymix"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func dailyMixTestApp(t *testing.T) (*App, *storage.Store, http.Handler, *http.Cookie) {
	t.Helper()
	store := credentialTestStore(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	moods := []string{"healing", "hotblooded", "cheerful", "relaxed", "epic"}
	styles := []string{"pop", "rock", "piano"}
	for i := 0; i < 360; i++ {
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("A%03d/%02d.flac", i/4, i%4), FileSize: int64(1000 + i), ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song %d", i), Album: fmt.Sprintf("Album %d", i/4), Artists: []string{fmt.Sprintf("Artist %d", i%45)},
				AlbumArtists: []string{fmt.Sprintf("Artist %d", i%45)}, DiscNumber: 1, TrackNumber: i%4 + 1, DurationMillis: int64(190_000 + (i%4)*20_000)}}); err != nil {
			t.Fatal(err)
		}
	}
	tracks, err := store.RecLibrary(ctx)
	if err != nil || len(tracks) != 360 {
		t.Fatalf("library = %d %v", len(tracks), err)
	}
	for i, tr := range tracks {
		meta := storage.TrackTagMeta{TrackID: tr.ID, SourceStamp: "s", InputHash: "h", VocabVersion: 1, PromptVersion: 1, Model: "m", Energy: i%5 + 1}
		if err := store.SaveTrackTagResult(ctx, meta, []storage.TrackTag{
			{Kind: "mood", Tag: moods[i%5], Weight: 1, Confidence: 0.9, Source: "llm"},
			{Kind: "style", Tag: styles[(i/5)%3], Weight: 1, Confidence: 0.8, Source: "llm"}}, nil); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			_ = store.SetTrackFavorite(ctx, tr.ID, true)
		}
	}
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, dailymix.Zone)
	app.SetDailyMixes(dailymix.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), dailymix.Options{Now: func() time.Time { return now }}))
	handler := app.Handler()
	return app, store, handler, mustLogin(t, handler, "admin", testAdminPassword)
}

type mixListBody struct {
	Status           string         `json:"status"`
	Day              string         `json:"day"`
	Items            []dailyMixJSON `json:"items"`
	ReplacementsLeft int            `json:"replacementsLeft"`
}

func TestDailyMixAPI(t *testing.T) {
	_, store, handler, _ := dailyMixTestApp(t)
	ctx := context.Background()
	if !capabilityFeature(t, handler, "dailyMixes") {
		t.Fatal("dailyMixes capability (enabled by default)")
	}
	rec := apiRequest(handler, http.MethodGet, "/api/v1/daily-mixes", "")
	var list mixListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || list.Status != "ready" || list.Day != "2026-10-16" || len(list.Items) != 5 || list.ReplacementsLeft != 3 {
		t.Fatalf("list %d %s", rec.Code, rec.Body.String())
	}
	first := list.Items[0]
	if first.Title == "" || first.TrackCount < dailymix.MinTracks || first.ThemeTypeLabel == "" || first.CoverURLs == nil || first.RatingReasons == nil {
		t.Fatalf("mix = %+v", first)
	}
	id := strconv.FormatInt(first.ID, 10)
	rec = apiRequest(handler, http.MethodGet, "/api/v1/daily-mixes/"+id, "")
	var detail struct {
		Mix    dailyMixJSON        `json:"mix"`
		Tracks []dailyMixTrackJSON `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || len(detail.Tracks) != first.TrackCount || detail.Tracks[0].Track.Title == "" || detail.Tracks[0].Bucket == "" {
		t.Fatalf("detail %d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	if strings.Contains(rec.Body.String(), "reason") || strings.Contains(rec.Body.String(), "plan") {
		t.Fatal("internal explanations stay on the admin page")
	}
	if rec = apiRequest(handler, http.MethodGet, "/api/v1/daily-mixes/999999", ""); rec.Code != 404 {
		t.Fatalf("missing = %d", rec.Code)
	}

	// Rating.
	for _, bad := range []string{`{"value":2}`, `{"value":1,"reasons":["theme"]}`, `{"value":-1,"reasons":["x"]}`, `{}`, `{"value":1,"x":1}`} {
		if rec = apiRequest(handler, http.MethodPut, "/api/v1/daily-mixes/"+id+"/rating", bad); rec.Code != 400 {
			t.Fatalf("%s → %d", bad, rec.Code)
		}
	}
	rec = apiRequest(handler, http.MethodPut, "/api/v1/daily-mixes/"+id+"/rating", `{"value":-1,"reasons":["flow","repetitive"]}`)
	var rated dailyMixJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &rated); err != nil || rated.Rating != -1 || strings.Join(rated.RatingReasons, ",") != "flow,repetitive" {
		t.Fatalf("rated %s", rec.Body.String())
	}

	// Save as playlist.
	rec = apiRequest(handler, http.MethodPost, "/api/v1/daily-mixes/"+id+"/save", "")
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), first.Title) {
		t.Fatalf("save %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(handler, http.MethodPost, "/api/v1/daily-mixes/"+id+"/save", `{"name":"我的歌单"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), "我的歌单") {
		t.Fatalf("save named %d", rec.Code)
	}

	// Replace (limit 3), old one becomes inactive.
	rec = apiRequest(handler, http.MethodPost, "/api/v1/daily-mixes/"+id+"/regenerate", "")
	var next dailyMixJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &next); err != nil || rec.Code != 200 || next.ID == first.ID || next.Slot != first.Slot {
		t.Fatalf("regenerate %d %s", rec.Code, rec.Body.String())
	}
	if rec = apiRequest(handler, http.MethodPost, "/api/v1/daily-mixes/"+id+"/regenerate", ""); rec.Code != 409 {
		t.Fatalf("old mix = %d", rec.Code)
	}
	cur := strconv.FormatInt(next.ID, 10)
	for i := 0; i < 2; i++ {
		rec = apiRequest(handler, http.MethodPost, "/api/v1/daily-mixes/"+cur+"/regenerate", "")
		if rec.Code != 200 {
			t.Fatalf("replace %d = %d %s", i, rec.Code, rec.Body.String())
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &next)
		cur = strconv.FormatInt(next.ID, 10)
	}
	if rec = apiRequest(handler, http.MethodPost, "/api/v1/daily-mixes/"+cur+"/regenerate", ""); rec.Code != 429 {
		t.Fatalf("limit = %d", rec.Code)
	}

	// Recommendation block.
	trackID := strconv.FormatInt(detail.Tracks[0].Track.ID, 10)
	if rec = apiRequest(handler, http.MethodPut, "/api/v1/tracks/"+trackID+"/recommendation-block", ""); rec.Code != 204 {
		t.Fatalf("block = %d", rec.Code)
	}
	if blocked, _ := store.RecommendationBlocks(ctx, []int64{detail.Tracks[0].Track.ID}); !blocked[detail.Tracks[0].Track.ID] {
		t.Fatal("not blocked")
	}
	if rec = apiRequest(handler, http.MethodGet, "/api/v1/daily-mixes/"+id, ""); !strings.Contains(rec.Body.String(), `"blocked":true`) {
		t.Fatal("blocked flag in detail")
	}
	if rec = apiRequest(handler, http.MethodDelete, "/api/v1/tracks/"+trackID+"/recommendation-block", ""); rec.Code != 204 {
		t.Fatalf("unblock = %d", rec.Code)
	}
	if rec = apiRequest(handler, http.MethodPut, "/api/v1/tracks/999999/recommendation-block", ""); rec.Code != 404 {
		t.Fatalf("unknown track = %d", rec.Code)
	}

	// Settings.
	if rec = apiRequest(handler, http.MethodGet, "/api/v1/daily-mixes/settings", ""); !strings.Contains(rec.Body.String(), `"mixCount":5`) {
		t.Fatalf("settings %s", rec.Body.String())
	}
	if rec = apiRequest(handler, http.MethodPatch, "/api/v1/daily-mixes/settings", `{"mixCount":9}`); rec.Code != 400 {
		t.Fatalf("invalid patch = %d", rec.Code)
	}
	if rec = apiRequest(handler, http.MethodPatch, "/api/v1/daily-mixes/settings", `{"enabled":false}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("patch %d %s", rec.Code, rec.Body.String())
	}
	rec = apiRequest(handler, http.MethodGet, "/api/v1/daily-mixes", "")
	if !strings.Contains(rec.Body.String(), `"status":"disabled"`) || capabilityFeature(t, handler, "dailyMixes") {
		t.Fatalf("disabled %s", rec.Body.String())
	}
}

func TestDailyMixAdminPage(t *testing.T) {
	app, store, handler, cookie := dailyMixTestApp(t)
	ctx := context.Background()
	page := getWithCookie(handler, "/admin/daily-mixes", cookie).Body.String()
	for _, want := range []string{"每日推荐", "还没有生成", `name="mix_count"`, `<option value="5" selected>5 个</option>`, "重新生成今天的歌单"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	rec := postSecurity(t, app, handler, cookie, "/admin/daily-mixes/regenerate", nil)
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); !strings.Contains(loc, "已重新生成") {
		t.Fatalf("regenerate = %s", loc)
	}
	mixes, _ := store.DailyMixes(ctx, "2026-10-16")
	if len(mixes) != 5 {
		t.Fatalf("mixes = %d", len(mixes))
	}
	page = getWithCookie(handler, "/admin/daily-mixes", cookie).Body.String()
	for _, want := range []string{mixes[0].Title, "曲目与入选原因", "标签命中", "不再推荐", "候选主题", "可播放曲目 360 首", "模板标题"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if dash := getWithCookie(handler, "/admin", cookie).Body.String(); !strings.Contains(dash, `href="/admin/daily-mixes"`) || !strings.Contains(dash, "今天 5 个") {
		t.Fatal("dashboard card")
	}
	// Admin replace does not use the daily budget.
	rec = postSecurity(t, app, handler, cookie, "/admin/daily-mixes/"+strconv.FormatInt(mixes[1].ID, 10)+"/replace", nil)
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); !strings.Contains(loc, "已换成") || !strings.Contains(loc, "#mix-") {
		t.Fatalf("replace = %s", loc)
	}
	if n, _ := store.ReplacementsOn(ctx, "2026-10-16"); n != 1 {
		t.Fatalf("replacements = %d", n)
	}
	items, _ := store.DailyMixItems(ctx, mixes[0].ID)
	rec = postSecurity(t, app, handler, cookie, "/admin/tracks/"+strconv.FormatInt(items[0].TrackID, 10)+"/recommendation-block",
		url.Values{"blocked": {"1"}, "return": {"/admin/daily-mixes#mix-1"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/daily-mixes?notice=") || !strings.HasSuffix(loc, "#mix-1") {
		t.Fatalf("block redirect = %s", loc)
	}
	if n, _ := store.CountRecommendationBlocks(ctx); n != 1 {
		t.Fatal("block")
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/tracks/1/recommendation-block", url.Values{"blocked": {"0"}, "return": {"https://evil.test/"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/daily-mixes?") {
		t.Fatalf("open redirect: %s", loc)
	}
	// Settings form.
	rec = postSecurity(t, app, handler, cookie, "/admin/daily-mixes/settings", url.Values{"enabled": {"on"}, "mix_count": {"3"}, "track_count": {"30"},
		"explore_percent": {"40"}, "pregenerate_hour": {"6"}, "regenerate_limit": {"2"}})
	set, _ := store.DailyMixSettings(ctx)
	if set.MixCount != 3 || set.TrackCount != 30 || set.ExplorePercent != 40 || set.PregenerateHour != 6 || set.LLMTitles || set.AllowSad {
		t.Fatalf("settings = %+v (%s)", set, rec.Header().Get("Location"))
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/daily-mixes/settings", url.Values{"mix_count": {"0"}})
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); !strings.Contains(loc, "歌单数量应为 1–8") {
		t.Fatalf("invalid = %s", loc)
	}
	// AI usage purpose label exists for mix calls.
	if aiPurposeLabels["mix"] == "" {
		t.Fatal("purpose label")
	}
}
