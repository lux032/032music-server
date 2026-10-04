package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCreditDetailPagesAndRedirects(t *testing.T) {
	ctx := context.Background()
	app, s, token := setupTestApp(t)
	if err := s.EnsureLibrary(ctx, "Credits", "/credits"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/credits")
	for i := 0; i < 21; i++ {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%d.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song%d", i), Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, Composer: "Creator", Lyricist: "Creator", DiscNumber: 1, TrackNumber: i + 1}}); err != nil {
			t.Fatal(err)
		}
	}
	options, _ := s.ListArtistOptions(ctx, "all", "", 20)
	var creator, singer int64
	for _, p := range options {
		if p.Name == "Creator" {
			creator = p.ID
		}
		if p.Name == "Singer" {
			singer = p.ID
		}
	}
	db, err := sql.Open("sqlite", httpTestDBPath[s])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, "INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Empty','Empty','empty-credit-detail')")
	if err != nil {
		t.Fatal(err)
	}
	emptyID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	app.sessions.create(login, "admin")
	cookie := login.Result().Cookies()[0]
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		path     string
		code     int
		location string
	}{
		{fmt.Sprintf("/admin/artists/%d", creator), 303, fmt.Sprintf("/admin/credits/%d", creator)},
		{fmt.Sprintf("/admin/artists/%d?notice=saved", creator), 303, fmt.Sprintf("/admin/credits/%d?notice=saved", creator)},
		{fmt.Sprintf("/admin/artists/%d?credit=composer&creditOffset=20", creator), 303, fmt.Sprintf("/admin/credits/%d?offset=20&role=composer", creator)},
		{fmt.Sprintf("/admin/artists/%d?view=profile", creator), 200, ""},
		{fmt.Sprintf("/admin/artists/%d?identityConflict=999&credit=composer", creator), 200, ""},
		{fmt.Sprintf("/admin/artists/%d", singer), 200, ""},
		{fmt.Sprintf("/admin/credits/%d", singer), 303, artistProfilePath(singer)},
		{fmt.Sprintf("/admin/credits/%d?notice=saved", singer), 303, artistProfilePath(singer) + "&notice=saved"},
		{"/admin/credits/999999", 404, ""},
	} {
		w := get(tc.path)
		if w.Code != tc.code || w.Header().Get("Location") != tc.location {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Header().Get("Location"))
		}
	}
	if w := get(fmt.Sprintf("/admin/artists/%d", emptyID)); w.Code != 200 {
		t.Fatal("empty artist redirected", w.Code)
	}
	if w := get(fmt.Sprintf("/admin/credits/%d", emptyID)); w.Code != 303 || w.Header().Get("Location") != artistProfilePath(emptyID) {
		t.Fatal("empty credit fallback", w.Code)
	}
	for _, suffix := range []string{"", "?role=composer", "?role=invalid", "?role=producer"} {
		w := get(fmt.Sprintf("/admin/credits/%d%s", creator, suffix))
		body := w.Body.String()
		if w.Code != 200 || strings.Count(body, "<article data-track-id=") != 20 || !strings.Contains(body, "全部 21") || !strings.Contains(body, "合作歌手") || !strings.Contains(body, "参与的专辑") || !strings.Contains(body, `data-nav="credits"`) {
			t.Fatalf("detail %d %s", w.Code, body)
		}
		if strings.Count(body, `class="album-facts"`) != 1 || !strings.Contains(body, "全部 21 首") || !strings.Contains(body, "作曲 21") || !strings.Contains(body, "作词 21") || !strings.Contains(body, `aria-current="page"`) || strings.Contains(body, ">制作 ") || strings.Contains(body, ">编曲 ") {
			t.Fatal("facts or actual-role tabs incorrect")
		}
		if (suffix == "?role=invalid" || suffix == "?role=producer") && !strings.Contains(body, fmt.Sprintf("credit=any:%d", creator)) {
			t.Fatal("invalid role not all")
		}
	}
	w := get(fmt.Sprintf("/admin/credits/%d?role=composer&offset=20", creator))
	if strings.Count(w.Body.String(), "<article data-track-id=") != 1 || !strings.Contains(w.Body.String(), "上一页") {
		t.Fatal("pagination")
	}
	w = get(fmt.Sprintf("/admin/artists/%d?view=profile", creator))
	if strings.Contains(w.Body.String(), `id="credits"`) || strings.Contains(w.Body.String(), "· 幕后") || !strings.Contains(w.Body.String(), fmt.Sprintf(`/admin/credits/%d`, creator)) {
		t.Fatal("profile credits or facts")
	}
	w = get(fmt.Sprintf("/admin/tracks?credit=any:%d", creator))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "幕后：Creator") {
		t.Fatal("any web filter")
	}
	w = get(fmt.Sprintf("/api/v1/tracks?credit=any:%d", creator))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":21`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = get("/api/v1/tracks?credit=invalid:1"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, role := range []string{"any", "credit"} {
		w := get("/admin/options/artists?role=" + role)
		var options []optionItem
		if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil || w.Code != 200 {
			t.Fatal(w.Code, err)
		}
		want := 3
		if role == "credit" {
			want = 1
		}
		if len(options) != want {
			t.Fatalf("options role=%s: %+v", role, options)
		}
		if role == "credit" && options[0].ID != creator {
			t.Fatal("credit options include performer")
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM track_artists WHERE artist_id=? AND role='lyricist'", creator); err != nil {
		t.Fatal(err)
	}
	w = get(fmt.Sprintf("/admin/credits/%d?role=lyricist", creator))
	if w.Code != 200 || strings.Contains(w.Body.String(), `class="filter-tags"`) || strings.Contains(w.Body.String(), "全部 21") || !strings.Contains(w.Body.String(), fmt.Sprintf("credit=any:%d", creator)) || strings.Count(w.Body.String(), "<article data-track-id=") != 20 {
		t.Fatal("single-role tabs or fallback")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO track_artists(track_id,artist_id,role,position) SELECT track_id,artist_id,'lyricist',position FROM track_artists WHERE artist_id=? AND role='composer'", creator); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeArtists(ctx, creator, singer); err != nil {
		t.Fatal(err)
	}
	w = get(fmt.Sprintf("/admin/credits/%d?role=lyricist&offset=20", creator))
	u, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != 303 || u.Path != fmt.Sprintf("/admin/credits/%d", singer) || u.Query().Get("role") != "lyricist" || u.Query().Get("offset") != "20" {
		t.Fatal(w.Code, u)
	}
	w = get(fmt.Sprintf("/admin/artists/%d?credit=lyricist&creditOffset=20", creator))
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "offset=20&role=lyricist") {
		t.Fatal(w.Code, w.Header())
	}
	w = get(fmt.Sprintf("/admin/artists/%d?identityConflict=999", creator))
	u, _ = url.Parse(w.Header().Get("Location"))
	if w.Code != 303 || u.Path != fmt.Sprintf("/admin/artists/%d", singer) {
		t.Fatal("merged conflict bypassed canonical redirect", w.Code, u)
	}
	w = get(fmt.Sprintf("/admin/artists/%d", singer))
	if w.Code != 200 {
		t.Fatal("mixed redirected")
	}
}

func TestArtistManagementReturnsToProfile(t *testing.T) {
	_, s, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	people, _ := s.ArtistsForMatching(ctx)
	id := people[0].ID
	manager.NoteRateLimited("musicbrainz", 5*time.Minute)
	assertProfile := func(w *httptest.ResponseRecorder) {
		t.Helper()
		u, e := url.Parse(w.Header().Get("Location"))
		if e != nil || w.Code != 303 || u.Query().Get("view") != "profile" {
			t.Fatalf("profile return %d %s", w.Code, w.Header().Get("Location"))
		}
	}
	for _, suffix := range []string{"/match", "/biographies/refresh", "/biography", "", "/identity/reset", "/merge"} {
		form := url.Values{}
		if suffix == "/biography" {
			form.Set("reset", "1")
		}
		assertProfile(postAdminForm(t, handler, cookie, csrf, fmt.Sprintf("/admin/artists/%d%s", id, suffix), form))
	}
	if err := s.ReplaceArtistCandidates(ctx, id, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: "confirm-profile", DisplayName: "Artist", Score: 95}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := s.ArtistCandidates(ctx, id)
	assertProfile(postAdminForm(t, handler, cookie, csrf, fmt.Sprintf("/admin/artists/%d/confirm/%d", id, candidates[0].ID), nil))
	if err := s.ReplaceArtistCandidates(ctx, id, []storage.ArtistCandidate{{Source: "lastfm", ExternalID: "reject-profile", DisplayName: "Artist", Score: 95}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ = s.ArtistCandidates(ctx, id)
	for _, c := range candidates {
		if c.Source == "lastfm" {
			assertProfile(postAdminForm(t, handler, cookie, csrf, fmt.Sprintf("/admin/artists/%d/reject/%d", id, c.ID), nil))
		}
	}
	app, cookie2, csrf2 := csrfSessionApp(t)
	importTestTrack(t, app)
	imageID := firstArtistID(t, app)
	assertProfile(uploadAlbumArtwork2(t, app, cookie2, fmt.Sprintf("/admin/artists/%d/image", imageID), csrf2, "portrait.webp", webpBytes(t)))
	assertProfile(postAdminForm(t, app.Handler(), cookie2, csrf2, fmt.Sprintf("/admin/artists/%d/image/reset", imageID), nil))
}

func TestCreditDetailHidesEmptyStatistics(t *testing.T) {
	app, _, _ := setupTestApp(t)
	w := httptest.NewRecorder()
	app.render(w, 200, "credit.html", creditDetailData{Artist: storage.ArtistDetail{}, Roles: []storage.CreditRoleCount{{Role: "composer", Count: 1}}})
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, `id="credit-collaborators"`) || strings.Contains(body, `id="credit-albums"`) || strings.Contains(body, "暂无合作歌手") {
		t.Fatal("empty statistics rendered")
	}
	if !strings.Contains(body, `aria-label="页面导航"`) || !strings.Contains(body, "#icon-arrow-left") {
		t.Fatal("back button inaccessible")
	}
}

// Medium-1: after a bulk delete shrinks the credit track total below the
// offset carried in the return address, the page must correct to the last
// valid page instead of rendering an empty page with stale pagination.
func TestCreditDetailStaleOffsetFallsBackToLastPage(t *testing.T) {
	ctx := context.Background()
	app, s, _ := setupTestApp(t)
	if err := s.EnsureLibrary(ctx, "Pager", "/pager"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/pager")
	for i := 0; i < 20; i++ {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("a/%d.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("PageSong%d", i), Album: "PagerA", Artists: []string{"PagerSinger"}, AlbumArtists: []string{"PagerSinger"}, Composer: "Pager", DiscNumber: 1, TrackNumber: i + 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "b/1.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "LastPageSong", Album: "PagerB", Artists: []string{"PagerSinger"}, AlbumArtists: []string{"PagerSinger"}, Composer: "Pager", DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	options, _ := s.ListArtistOptions(ctx, "all", "Pager", 20)
	var pager int64
	for _, p := range options {
		if p.Name == "Pager" {
			pager = p.ID
		}
	}
	login := httptest.NewRecorder()
	app.sessions.create(login, "admin")
	cookie := login.Result().Cookies()[0]
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	if w := get(fmt.Sprintf("/admin/credits/%d?offset=20", pager)); strings.Count(w.Body.String(), "<article data-track-id=") != 1 {
		t.Fatal("second page should show the single remaining track before deletion")
	}
	albums, _ := s.ListAlbums(ctx, storage.Filters{Limit: 50})
	for _, album := range albums {
		if album.Title == "PagerB" {
			if _, err := s.DeleteAlbums(ctx, []int64{album.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	w := get(fmt.Sprintf("/admin/credits/%d?offset=20&notice=已从曲库删除", pager))
	body := w.Body.String()
	if w.Code != 200 || strings.Count(body, "<article data-track-id=") != 20 {
		t.Fatalf("stale offset should fall back to the valid last page (status %d, tracks %d)", w.Code, strings.Count(body, "<article data-track-id="))
	}
	if strings.Contains(body, "暂无该角色作品") {
		t.Fatal("corrected page must not render the empty state")
	}
	if !strings.Contains(body, "已从曲库删除") {
		t.Fatal("notice must survive the offset correction")
	}
}
