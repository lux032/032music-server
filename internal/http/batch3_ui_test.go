package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func setupBatch3HTTPFixture(t *testing.T) (*App, *http.Cookie, string, int64, int64, []int64) {
	t.Helper()
	app, cookie, csrf := csrfSessionApp(t)
	ctx := context.Background()
	if err := app.store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := app.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	// Import Album 1 with 3 tracks
	for i := 1; i <= 3; i++ {
		err = app.store.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    lib.ID,
			RelativePath: "AlbumOne/track" + strconv.Itoa(i) + ".flac",
			FileSize:     100,
			ModifiedAtNS: int64(i),
			Metadata: metadata.AudioMetadata{
				Title:        "Song " + strconv.Itoa(i),
				Album:        "Album One",
				Artists:      []string{"Artist LiSA"},
				AlbumArtists: []string{"Artist LiSA"},
				DiscNumber:   1,
				TrackNumber:  i,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Import Album 2 (compilation / other) with 1 track
	err = app.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "AlbumTwo/track1.flac",
		FileSize:     100,
		ModifiedAtNS: 10,
		Metadata: metadata.AudioMetadata{
			Title:        "Compilation Song",
			Album:        "Greatest Hits",
			Artists:      []string{"Various Artists"},
			AlbumArtists: []string{"Various Artists"},
			DiscNumber:   1,
			TrackNumber:  1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	albums, err := app.store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var albumOneID, albumTwoID int64
	for _, a := range albums {
		if a.Title == "Album One" {
			albumOneID = a.ID
		} else if a.Title == "Greatest Hits" {
			albumTwoID = a.ID
		}
	}
	if albumOneID == 0 || albumTwoID == 0 {
		t.Fatalf("fixture albums missing: %+v", albums)
	}
	tracks, err := app.store.ListTracks(ctx, storage.Filters{AlbumID: albumOneID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 3 {
		t.Fatalf("fixture tracks missing: %+v", tracks)
	}
	trackIDs := make([]int64, len(tracks))
	for i, tr := range tracks {
		trackIDs[i] = tr.ID
	}

	return app, cookie, csrf, albumOneID, albumTwoID, trackIDs
}

// getAdmin issues an authenticated GET and fails on non-200.
func getAdmin(t *testing.T, handler http.Handler, cookie *http.Cookie, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d", path, rec.Code)
	}
	return rec.Body.String()
}

// httpExec 在测试库上执行写 SQL（与 httpCount 同一路径机制）。
func httpExec(t *testing.T, store *storage.Store, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", httpTestDBPath[store])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

var tabBadgeRe = regexp.MustCompile(`review-tab-badge">(\d+)`)

func tabBadgeCounts(t *testing.T, body string) []string {
	t.Helper()
	matches := tabBadgeRe.FindAllStringSubmatch(body, -1)
	counts := make([]string, 0, len(matches))
	for _, m := range matches {
		counts = append(counts, m[1])
	}
	return counts
}

func TestWorkReviewTabsAuthCountsAndGrouping(t *testing.T) {
	app, cookie, _, albumID, albumTwoID, trackIDs := setupBatch3HTTPFixture(t)
	handler := app.Handler()
	ctx := context.Background()

	// 1. Unauthenticated request redirects to login
	req := httptest.NewRequest(http.MethodGet, "/admin/work-review", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/admin/login") {
		t.Fatalf("unauthenticated expected redirect to login, got %d", rec.Code)
	}

	// Setup candidate fixtures: 2 album candidates (different albums), 2 track
	// candidates on the SAME album, 1 work alignment candidate.
	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{
		{
			ExternalID: "412958",
			Title:      "Amore",
			Score:      88,
			Evidence:   []string{"专辑名完全一致", "歌手一致"},
			Tieups:     []storage.BangumiTieup{{SubjectID: 501, Title: "Work Anime A", Type: "anime", Role: "op"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumTwoID, []storage.AlbumSubjectCandidate{
		{ExternalID: "413000", Title: "Beta Hits", Score: 70},
	}); err != nil {
		t.Fatal(err)
	}

	for i, trackID := range trackIDs[:2] {
		if err := app.store.SaveTrackSubjectCandidates(ctx, trackID, []storage.TrackSubjectCandidate{
			{
				ExternalID: "35921" + strconv.Itoa(6+i),
				Title:      "Track Cand " + strconv.Itoa(i+1),
				MatchKind:  "multi_title",
				Evidence:   []string{"歌手一致"},
				Tieups:     []storage.BangumiTieup{{SubjectID: int64(502 + i), Title: "Work Anime B", Type: "anime", Role: "op"}},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Local Work Test", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.store.ReplaceWorkMatchCandidates(ctx, work.ID, []storage.WorkMatchCandidate{
		{
			WorkID:     work.ID,
			Source:     "bangumi",
			ExternalID: "326624",
			Title:      "Local Work Cand",
			Score:      96,
			Type:       "anime",
			Status:     "candidate",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Albums tab: counts [2,2,1], grouping by album with 2 group heads in
	// score order (Amore score 88 first, Beta Hits score 70 second).
	body := getAdmin(t, handler, cookie, "/admin/work-review?tab=albums")
	if !strings.Contains(body, "作品关联审核") || !strings.Contains(body, "专辑候选") {
		t.Fatalf("missing page header: %s", body)
	}
	if got := strings.Join(tabBadgeCounts(t, body), ","); got != "2,2,1,0" {
		t.Fatalf("tab badges = %s, want 2,2,1", got)
	}
	if !strings.Contains(body, "Amore") || !strings.Contains(body, "412958") || !strings.Contains(body, "Work Anime A") {
		t.Fatalf("missing album candidate content: %s", body)
	}
	if n := strings.Count(body, `class="review-group-head"`); n != 2 {
		t.Fatalf("album tab group heads = %d, want 2", n)
	}
	if strings.Index(body, "Amore") > strings.Index(body, "Beta Hits") {
		t.Fatalf("group order wrong: Amore (score 88) must come before Beta Hits (score 70)")
	}
	// Each group card contains only its own album's candidate.
	cards := strings.Split(body, `class="review-group-card"`)
	if len(cards) != 3 { // 前置内容 + 2 张卡片
		t.Fatalf("album tab group cards = %d, want 2", len(cards)-1)
	}
	if !strings.Contains(cards[1], "Amore") || !strings.Contains(cards[1], "412958") || strings.Contains(cards[1], "413000") {
		t.Fatalf("first group card must only contain the Amore candidate")
	}
	if !strings.Contains(cards[2], "Beta Hits") || !strings.Contains(cards[2], "413000") || strings.Contains(cards[2], "412958") {
		t.Fatalf("second group card must only contain the Beta Hits candidate")
	}

	// 3. Tracks tab (group=album): both track candidates belong to Album One,
	// so exactly ONE group head containing both candidates.
	body = getAdmin(t, handler, cookie, "/admin/work-review?tab=tracks")
	if !strings.Contains(body, "多曲名条目") || !strings.Contains(body, "Work Anime B") {
		t.Fatalf("missing track candidate content: %s", body)
	}
	if n := strings.Count(body, `class="review-group-head"`); n != 1 {
		t.Fatalf("tracks tab group heads = %d, want 1 (same album)", n)
	}
	headIdx := strings.Index(body, "Album One")
	if headIdx < 0 || !strings.Contains(body[headIdx:], "359216") || !strings.Contains(body[headIdx:], "359217") {
		t.Fatalf("both track candidates must be under the Album One group")
	}

	// Tracks tab group=work: both candidates tie up to Work Anime B -> one group.
	body = getAdmin(t, handler, cookie, "/admin/work-review?tab=tracks&group=work")
	if n := strings.Count(body, `class="review-group-head"`); n != 1 {
		t.Fatalf("tracks tab group=work heads = %d, want 1", n)
	}
	if !strings.Contains(body, "Work Anime B") {
		t.Fatalf("group=work header missing work title")
	}

	// 4. Works tab: flat list, no grouping control, badge stays 1.
	body = getAdmin(t, handler, cookie, "/admin/work-review?tab=works")
	if !strings.Contains(body, "Local Work Test") || !strings.Contains(body, "326624") {
		t.Fatalf("missing work alignment candidate: %s", body)
	}
	if strings.Contains(body, "group-segmented") {
		t.Fatalf("works tab must not render the grouping control (grouping by album is meaningless there)")
	}

	// 5. Albums tab group=work: candidate with tieup groups under the tieup
	// work title; candidate without tieups falls back to the album title.
	body = getAdmin(t, handler, cookie, "/admin/work-review?tab=albums&group=work")
	if !strings.Contains(body, `class="group-segmented"`) {
		t.Fatalf("missing group segmented control: %s", body)
	}
	if n := strings.Count(body, `class="review-group-head"`); n != 2 {
		t.Fatalf("albums tab group=work heads = %d, want 2", n)
	}
	if strings.Index(body, "Work Anime A") > strings.Index(body, "Greatest Hits") {
		t.Fatalf("group=work order wrong: Amore candidate (with tieup, saved first) must lead")
	}

	// 6. Album filter pushed down: only the filtered album's candidates, and
	// the tab badges show filtered counts (albums=1, tracks=2, works=1).
	body = getAdmin(t, handler, cookie, "/admin/work-review?tab=albums&albumId="+strconv.FormatInt(albumID, 10))
	if !strings.Contains(body, "Amore") || strings.Contains(body, "Beta Hits") {
		t.Fatalf("albumId filter not applied: %s", body)
	}
	if got := strings.Join(tabBadgeCounts(t, body), ","); got != "1,2,1,0" {
		t.Fatalf("filtered tab badges = %s, want 1,2,1", got)
	}
	// L5：带 albumId 过滤时导航角标仍是全局总数（2 专辑 + 2 曲目 + 1 作品 = 5），
	// 只有 Tab 计数缩小。
	if !strings.Contains(body, `nav-badge">5<`) {
		t.Fatalf("nav badge must stay the global total under albumId filter")
	}
}

func TestWorkReviewReturnToRejectsExternalRedirect(t *testing.T) {
	app, cookie, csrf, albumID, _, _ := setupBatch3HTTPFixture(t)
	handler := app.Handler()
	ctx := context.Background()

	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{
		{ExternalID: "777", Title: "Candidate Reject", Score: 80},
	}); err != nil {
		t.Fatal(err)
	}
	cands, err := app.store.AlbumSubjectCandidates(ctx, albumID)
	if err != nil || len(cands) == 0 {
		t.Fatalf("candidates: %v %+v", err, cands)
	}
	candID := cands[0].ID

	// Attempt open redirect via returnTo: https://evil.com
	form := url.Values{
		"csrfToken": {csrf},
		"returnTo":  {"https://evil.com/phish"},
	}
	rec := postForm(handler, cookie, "/admin/enrichment/albums/"+strconv.FormatInt(albumID, 10)+"/subjects/"+strconv.FormatInt(candID, 10)+"/reject", form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reject status=%d", rec.Code)
	}
	location := rec.Header().Get("Location")
	if strings.Contains(location, "evil.com") {
		t.Fatalf("open redirect detected! Location=%q", location)
	}
	if !strings.HasPrefix(location, "/admin/work-review") {
		t.Fatalf("expected fallback to /admin/work-review, got %q", location)
	}

	// Relative safe returnTo must be respected
	form = url.Values{
		"csrfToken": {csrf},
		"returnTo":  {"/admin/albums/" + strconv.FormatInt(albumID, 10)},
	}
	// Add another candidate
	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{
		{ExternalID: "888", Title: "Candidate Safe", Score: 80},
	}); err != nil {
		t.Fatal(err)
	}
	cands, err = app.store.AlbumSubjectCandidates(ctx, albumID)
	if err != nil || len(cands) == 0 {
		t.Fatalf("candidates: %v %+v", err, cands)
	}
	candID = cands[0].ID

	rec = postForm(handler, cookie, "/admin/enrichment/albums/"+strconv.FormatInt(albumID, 10)+"/subjects/"+strconv.FormatInt(candID, 10)+"/reject", form)

	location = rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/admin/albums/"+strconv.FormatInt(albumID, 10)) {
		t.Fatalf("expected returnTo /admin/albums/%d, got %q", albumID, location)
	}
}

// H1 (D44)：作品关联审核区不在 /admin/enrichment，艺术家关系候选区保留在该页。
func TestEnrichmentPageKeepsArtistReviewOnly(t *testing.T) {
	app, cookie, _, _, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()

	artists, err := app.store.ListArtists(ctx, storage.Filters{Limit: 5})
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists: %v %+v", err, artists)
	}
	artistID := artists[0].ID
	if err := app.store.ReplaceArtistRelationCandidates(ctx, artistID, []storage.ArtistRelationCandidate{
		{Source: "musicbrainz", ExternalID: "main", RelatedExternalID: "alias-ext", RelatedName: "Alias LiSA", RelationType: "alias_of", Score: 85},
	}); err != nil {
		t.Fatal(err)
	}

	body := getAdmin(t, app.Handler(), cookie, "/admin/enrichment")

	// 作品关联审核区不在该页
	for _, forbidden := range []string{"专辑 → 作品", "曲目候选", "作品候选", "只接受勾选作品"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("/admin/enrichment must not contain %q", forbidden)
		}
	}
	// 艺术家关系候选区保留在该页（D44），带接受/拒绝表单
	if !strings.Contains(body, "艺术家关系候选") || !strings.Contains(body, "Alias LiSA") {
		t.Fatalf("/admin/enrichment missing artist relation review section: %s", body)
	}
	acceptAction := "/admin/enrichment/artists/" + strconv.FormatInt(artistID, 10) + "/relations/"
	if !strings.Contains(body, acceptAction) || !strings.Contains(body, "确认关系") {
		t.Fatalf("artist relation accept/reject forms missing")
	}
	// 任务记录和去审核入口仍在
	if !strings.Contains(body, "任务记录") || !strings.Contains(body, "/admin/work-review") || !strings.Contains(body, "去审核 →") {
		t.Fatalf("/admin/enrichment missing task list or review link: %s", body)
	}
}

func TestAlbumPageCapsulesDedupeRoleSourceOSTAndMore(t *testing.T) {
	app, cookie, _, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()

	// Create 3 works
	work1, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Work One OP", Type: "anime", Year: 2019})
	if err != nil {
		t.Fatal(err)
	}
	work2, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Work Two OST", Type: "anime", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}
	work3, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Work Three More", Type: "anime", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}

	// Album-level link for work1 (other) and track-level link for work1 on track 1 (op) -> Capsule displays OP
	if err := app.store.AddWorkAlbum(ctx, work1.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkTrack(ctx, work1.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}

	// Album-level link for work2 (ost) with NO track-level links -> Capsule displays OST (R4)
	if err := app.store.AddWorkAlbum(ctx, work2.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}

	// Track-level only link for work3 on track 2 (ed) -> Capsule displays ED
	if err := app.store.AddWorkTrack(ctx, work3.ID, storage.WorkTrackInput{TrackID: trackIDs[1], Role: "ed"}); err != nil {
		t.Fatal(err)
	}

	body := getAdmin(t, app.Handler(), cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))

	// 1. Check deduplication: Work One OP appears as capsule
	if !strings.Contains(body, "Work One OP") {
		t.Fatalf("missing Work One OP capsule")
	}

	// 2. Check Role source: Work One has track-level role OP
	if !strings.Contains(body, ">OP</span>") {
		t.Fatalf("missing OP role badge on capsule")
	}

	// 3. Check OST role on Work Two OST: album-level ost shows OST
	if !strings.Contains(body, "Work Two OST") || !strings.Contains(body, ">OST</span>") {
		t.Fatalf("missing OST role badge on capsule")
	}

	// 4. Check "+1" collapse: with 3 works, first 2 are shown and +1 is present
	if !strings.Contains(body, "+1") {
		t.Fatalf("expected +1 collapse button for 3 works, body: %s", body)
	}
	if strings.Contains(body, "+1 部系列作品") {
		t.Fatalf("forbidden phrasing '+1 部系列作品' found")
	}
}

// H2：soundtrack 专辑类型不能推出 OST；只有专辑级 other 关联时不显示用途。
func TestAlbumPageSoundtrackAlbumNotOST(t *testing.T) {
	app, cookie, _, albumID, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()

	// 把 Album One 标记为 soundtrack 类型
	if err := app.store.UpdateAlbum(ctx, albumID, storage.AlbumEdit{AlbumType: "soundtrack"}); err != nil {
		t.Fatal(err)
	}

	// 专辑级 other 关联（D45：不能因 soundtrack 类型显示 OST；R4：无曲目级用途不显示用途）
	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Soundtrack Anime", Type: "anime", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}

	body := getAdmin(t, app.Handler(), cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))
	if !strings.Contains(body, "Soundtrack Anime") {
		t.Fatalf("missing capsule for album-level linked work")
	}
	if strings.Contains(body, "work-capsule-role") {
		t.Fatalf("soundtrack album with album-level 'other' link must not show any role badge (D45/R4)")
	}
	if strings.Contains(body, ">OST</span>") {
		t.Fatalf("soundtrack album type must not produce OST badge (D45)")
	}

	// 作品页角标：soundtrack 类型 + 专辑级 other → “相关专辑”，不是 OST
	workBody := getAdmin(t, app.Handler(), cookie, "/admin/works/"+strconv.FormatInt(work.ID, 10))
	if !strings.Contains(workBody, "相关专辑") {
		t.Fatalf("work page overlay should be 相关专辑, body: %s", workBody)
	}
	if strings.Contains(workBody, "OST") {
		t.Fatalf("work page overlay must not show OST for soundtrack album type alone (D45)")
	}

	// 专辑级 role='ost' → 胶囊和作品页角标都显示 OST
	ostWork, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Real OST Anime", Type: "anime", Year: 2022})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkAlbum(ctx, ostWork.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}
	body = getAdmin(t, app.Handler(), cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))
	if !strings.Contains(body, "Real OST Anime") || !strings.Contains(body, ">OST</span>") {
		t.Fatalf("album-level ost link must show OST capsule badge")
	}
	ostBody := getAdmin(t, app.Handler(), cookie, "/admin/works/"+strconv.FormatInt(ostWork.ID, 10))
	if !strings.Contains(ostBody, "原声集 OST") {
		t.Fatalf("work page overlay must show 原声集 OST for album-level ost link")
	}
}

// H2/R2：单曲不按曲目数推测，只看 AlbumType=='single'；无曲目级用途时不用专辑级 role 拼用途。
func TestWorkAlbumRelationLabelR4D45(t *testing.T) {
	cases := []struct {
		name string
		aw   storage.AlbumWork
		want string
	}{
		{"soundtrack album type alone is not OST", storage.AlbumWork{AlbumRole: "other", Role: "other", AlbumType: "soundtrack"}, "相关专辑"},
		{"album role ost shows OST", storage.AlbumWork{AlbumRole: "ost", Role: "ost", AlbumType: "album"}, "原声集 OST"},
		{"few tracks but not single", storage.AlbumWork{AlbumRole: "other", Role: "other", AlbumType: "album", TrackCount: 3}, "相关专辑"},
		{"single by type", storage.AlbumWork{AlbumRole: "other", Role: "other", AlbumType: "single", TrackCount: 9}, "相关单曲"},
		{"track op on single", storage.AlbumWork{AlbumRole: "other", Role: "op", AlbumType: "single", TrackRoles: []string{"op"}}, "OP 单曲"},
		{"album-level other never becomes OP 专辑", storage.AlbumWork{AlbumRole: "other", Role: "other", AlbumType: "album"}, "相关专辑"},
	}
	for _, tc := range cases {
		if got := workAlbumRelationLabel(tc.aw); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// H4 (D46)：作品页“关联专辑”卡片下的“曲目用途”区列出曲目级关联并可解除（写抑制）。
func TestWorkPageTrackUsagesAndRemove(t *testing.T) {
	app, cookie, csrf, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	// 单曲专辑整张关联（other），其中一首曲目有 bangumi OP 关联
	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Gurenge Anime", Type: "anime", Year: 2019})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	// 把曲目级来源改成 bangumi，模拟 Bangumi 自动确认写入的关联
	httpExec(t, app.store, `UPDATE work_tracks SET source='bangumi' WHERE work_id=? AND track_id=?`, work.ID, trackIDs[0])

	workPath := "/admin/works/" + strconv.FormatInt(work.ID, 10)
	body := getAdmin(t, handler, cookie, workPath)
	if !strings.Contains(body, "曲目用途") {
		t.Fatalf("work page missing 曲目用途 section: %s", body)
	}
	usageIdx := strings.Index(body, "曲目用途")
	if !strings.Contains(body[usageIdx:], "Song 1") {
		t.Fatalf("曲目用途 missing track title")
	}
	if !strings.Contains(body[usageIdx:], ">OP</span>") {
		t.Fatalf("曲目用途 missing OP role badge")
	}
	if !strings.Contains(body[usageIdx:], "Bangumi") {
		t.Fatalf("曲目用途 missing source label Bangumi")
	}

	// 解除：POST 现有 RemoveWorkTrack 路由
	form := url.Values{
		"csrfToken": {csrf},
		"role":      {"op"},
		"season":    {"0"},
		"sequence":  {"0"},
	}
	rec := postForm(handler, cookie, workPath+"/tracks/"+strconv.FormatInt(trackIDs[0], 10)+"/remove", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("remove status=%d", rec.Code)
	}

	// 解除后写入抑制（R3）
	if n := httpCount(t, app.store, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=?`, trackIDs[0]); n < 1 {
		t.Fatalf("expected suppression row after removing track usage, got %d", n)
	}

	// 解除后作品页不再显示该曲目用途
	body = getAdmin(t, handler, cookie, workPath)
	if strings.Contains(body, `class="work-track-usages"`) {
		t.Fatalf("曲目用途 section should disappear after removing the only track usage")
	}
	// 专辑级关联仍在
	if !strings.Contains(body, "Album One") {
		t.Fatalf("album-level link must survive track usage removal")
	}
}

// M4 (D1)：专辑级关联的 role 只接受 ost / other。
func TestAlbumWorkRoleValidation(t *testing.T) {
	app, cookie, csrf, albumID, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Role Test Anime", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	workID := strconv.FormatInt(work.ID, 10)
	albumIDStr := strconv.FormatInt(albumID, 10)

	// 专辑抽屉添加表单：role=op 必须 400
	rec := postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works", url.Values{
		"csrfToken": {csrf}, "workId": {workID}, "role": {"op"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("album add role=op status=%d, want 400", rec.Code)
	}
	// 作品页添加表单：role=ed 必须 400
	rec = postForm(handler, cookie, "/admin/works/"+workID+"/albums", url.Values{
		"csrfToken": {csrf}, "albumId": {albumIDStr}, "role": {"ed"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("work add role=ed status=%d, want 400", rec.Code)
	}
	// ost / other 都接受
	rec = postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works", url.Values{
		"csrfToken": {csrf}, "workId": {workID}, "role": {"ost"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("album add role=ost status=%d, want 303", rec.Code)
	}
	albums, err := app.store.AlbumsForWork(ctx, work.ID)
	if err != nil || len(albums) != 1 || albums[0].AlbumRole != "ost" {
		t.Fatalf("albums for work: %v %+v", err, albums)
	}

	// 页面上的专辑级表单只提供 other/ost 选项
	body := getAdmin(t, handler, cookie, "/admin/works/"+workID)
	formIdx := strings.Index(body, "手动添加关联专辑")
	if formIdx < 0 {
		t.Fatalf("work page missing manual add album form")
	}
	formSection := body[formIdx:]
	if end := strings.Index(formSection, "</form>"); end > 0 {
		formSection = formSection[:end]
	}
	for _, forbidden := range []string{`value="op"`, `value="ed"`, `value="insert"`, `value="theme"`} {
		if strings.Contains(formSection, forbidden) {
			t.Fatalf("album-level role select must not offer %s (D1)", forbidden)
		}
	}
	albumBody := getAdmin(t, handler, cookie, "/admin/albums/"+albumIDStr)
	drawerIdx := strings.Index(albumBody, `id="album-add-work-form"`)
	if drawerIdx < 0 {
		t.Fatalf("album drawer missing add work form")
	}
	if strings.Contains(albumBody, `value="op"`) && strings.Contains(albumBody[strings.Index(albumBody, "drawer-add-tieup"):], `value="op"`) {
		t.Fatalf("album drawer role select must not offer op (D1)")
	}
}

// M6：/admin/albums/{id}/works 与 /remove 的 CSRF、404 与抑制语义。
func TestAdminAlbumWorksHTTP(t *testing.T) {
	app, cookie, csrf, albumID, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "HTTP Test Anime", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	workID := strconv.FormatInt(work.ID, 10)
	albumIDStr := strconv.FormatInt(albumID, 10)

	// CSRF 错误 → 403
	rec := postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works", url.Values{"workId": {workID}, "role": {"other"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("add without csrf status=%d, want 403", rec.Code)
	}
	// 作品不存在 → 404
	rec = postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works", url.Values{"csrfToken": {csrf}, "workId": {"999999"}, "role": {"other"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("add missing work status=%d, want 404", rec.Code)
	}
	// 专辑不存在 → 404
	rec = postForm(handler, cookie, "/admin/albums/999999/works", url.Values{"csrfToken": {csrf}, "workId": {workID}, "role": {"other"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("add missing album status=%d, want 404", rec.Code)
	}
	// 正常添加
	rec = postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works", url.Values{"csrfToken": {csrf}, "workId": {workID}, "role": {"other"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add status=%d, want 303", rec.Code)
	}
	// 抑制语义（R3）只针对 auto/bangumi 来源的关联：把来源置为 bangumi 再解除
	httpExec(t, app.store, `UPDATE album_works SET source='bangumi', inferred_key='bangumi:412958' WHERE album_id=? AND work_id=?`, albumID, work.ID)

	// 解除：CSRF 错误 → 403
	rec = postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works/"+workID+"/remove", url.Values{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remove without csrf status=%d, want 403", rec.Code)
	}
	// 解除不存在的关联 → 404
	other, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Not Linked Anime", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	rec = postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works/"+strconv.FormatInt(other.ID, 10)+"/remove", url.Values{"csrfToken": {csrf}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("remove missing link status=%d, want 404", rec.Code)
	}
	// 正常解除 → 303，且写入抑制（R3）
	rec = postForm(handler, cookie, "/admin/albums/"+albumIDStr+"/works/"+workID+"/remove", url.Values{"csrfToken": {csrf}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("remove status=%d, want 303", rec.Code)
	}
	if n := httpCount(t, app.store, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=?`, albumID); n < 1 {
		t.Fatalf("expected album_work_suppressions row after remove, got %d", n)
	}
	if n := httpCount(t, app.store, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND work_id=?`, albumID, work.ID); n != 0 {
		t.Fatalf("album_works row still present after remove")
	}
}

// M6：onlySelected 空选择被拒绝；部分选择只写入勾选的作品。
func TestAlbumSubjectOnlySelected(t *testing.T) {
	app, cookie, csrf, albumID, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{
		{
			ExternalID: "412958",
			Title:      "Amore",
			Score:      88,
			Tieups: []storage.BangumiTieup{
				{SubjectID: 501, Title: "Work Anime A", Type: "anime", Role: "op"},
				{SubjectID: 502, Title: "Work Anime B", Type: "anime", Role: "ed"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	cands, err := app.store.AlbumSubjectCandidates(ctx, albumID)
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidates: %v %+v", err, cands)
	}
	candID := strconv.FormatInt(cands[0].ID, 10)
	acceptPath := "/admin/enrichment/albums/" + strconv.FormatInt(albumID, 10) + "/subjects/" + candID + "/accept"

	// 空选择：拒绝，候选仍在，且不写任何 album_works
	rec := postForm(handler, cookie, acceptPath, url.Values{
		"csrfToken": {csrf}, "onlySelected": {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("empty selection status=%d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "%E8%AF%B7%E8%87%B3%E5%B0%91%E9%80%89%E6%8B%A9%E4%B8%80%E9%83%A8%E4%BD%9C%E5%93%81") {
		t.Fatalf("empty selection should redirect with 请至少选择一部作品 notice, got %q", rec.Header().Get("Location"))
	}
	if n := httpCount(t, app.store, `SELECT COUNT(*) FROM album_subject_candidates WHERE id=? AND status='candidate'`, cands[0].ID); n != 1 {
		t.Fatalf("candidate must stay pending after empty selection")
	}
	if n := httpCount(t, app.store, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
		t.Fatalf("empty selection must not write album_works")
	}

	// 部分选择：只写入勾选的作品（501），不写入未勾选的（502）
	rec = postForm(handler, cookie, acceptPath, url.Values{
		"csrfToken": {csrf}, "onlySelected": {"1"}, "workSubjectIds": {"501"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("partial selection status=%d", rec.Code)
	}
	if n := httpCount(t, app.store, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 1 {
		t.Fatalf("partial selection must link exactly 1 work, got %d", n)
	}
	// 确认勾选的是 Work Anime A（subject 501）
	works, err := app.store.ListWorks(ctx, storage.WorkFilters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	foundA, foundB := false, false
	for _, w := range works {
		linked, err := app.store.AlbumsForWork(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range linked {
			if l.AlbumID != albumID {
				continue
			}
			if w.ExternalID == "501" || strings.Contains(w.Title, "Work Anime A") {
				foundA = true
			}
			if w.ExternalID == "502" || strings.Contains(w.Title, "Work Anime B") {
				foundB = true
			}
		}
	}
	if !foundA || foundB {
		t.Fatalf("partial selection: selected work linked=%v, unselected work linked=%v", foundA, foundB)
	}
}

// M6：safeAdminReturnTo 拒绝开放重定向。
func TestSafeAdminReturnTo(t *testing.T) {
	fallback := "/admin/work-review"
	rejected := []string{"//evil.com", `/\evil`, "/%2F%2Fevil", "https://evil.com", "http://evil.com/x", "evil.com", "", "  ", "/admin\r\nLocation: x", "/other-page"}
	for _, raw := range rejected {
		if got := safeAdminReturnTo(raw, fallback); got != fallback {
			t.Fatalf("safeAdminReturnTo(%q) = %q, want fallback", raw, got)
		}
	}
	accepted := []string{"/admin/work-review?tab=albums", "/admin/albums/3", "/admin/works/9#edit"}
	for _, raw := range accepted {
		if got := safeAdminReturnTo(raw, fallback); got != raw {
			t.Fatalf("safeAdminReturnTo(%q) = %q, want passthrough", raw, got)
		}
	}
}

// L1：带 fragment 的 fallback 重定向不能丢 notice。
func TestRedirectWithNoticeFragment(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	redirectWithNotice(rec, req, "/admin/albums/3#edit", "作品关联已添加")
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/admin/albums/3?notice=") || !strings.HasSuffix(location, "#edit") {
		t.Fatalf("notice must precede fragment, got %q", location)
	}
	if !strings.Contains(location, url.QueryEscape("作品关联已添加")) {
		t.Fatalf("notice text missing from %q", location)
	}
}

func TestAlbumPageTrackIconsMatchInspectorSummary(t *testing.T) {
	app, cookie, _, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()

	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Railgun Anime", Type: "anime", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}

	body := getAdmin(t, app.Handler(), cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))
	// Track row has track-tieup-trigger with OP
	if !strings.Contains(body, "class=\"track-tieup-trigger\"") || !strings.Contains(body, "<span>OP</span>") {
		t.Fatalf("missing track tieup icon: %s", body)
	}
	// Right column inspector has Railgun Anime summary
	if !strings.Contains(body, "id=\"inspector-works\"") || !strings.Contains(body, "Railgun Anime") {
		t.Fatalf("missing inspector works summary: %s", body)
	}
}

func TestWorkPageCollectedInExcludesAlbumLevelTracks(t *testing.T) {
	app, cookie, _, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()

	// Album 1 (albumID) has an album-level link to Work
	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Demon Slayer", Type: "anime", Year: 2019})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}

	// Album 2 (compilation) has NO album-level link, but has a track-level link to Work
	albums, err := app.store.ListAlbums(ctx, storage.Filters{Query: "Greatest Hits"})
	if err != nil || len(albums) != 1 {
		t.Fatalf("compilation album missing: %v %+v", err, albums)
	}
	compAlbumID := albums[0].ID
	compTracks, err := app.store.ListTracks(ctx, storage.Filters{AlbumID: compAlbumID})
	if err != nil || len(compTracks) != 1 {
		t.Fatalf("compilation track missing: %v %+v", err, compTracks)
	}
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: compTracks[0].ID, Role: "ed"}); err != nil {
		t.Fatal(err)
	}

	body := getAdmin(t, app.Handler(), cookie, "/admin/works/"+strconv.FormatInt(work.ID, 10))

	// "收录于精选集 / 原创专辑" section must contain Compilation Song from Album 2
	if !strings.Contains(body, "收录于精选集 / 原创专辑") || !strings.Contains(body, "Compilation Song") {
		t.Fatalf("missing compilation track in collected-in section: %s", body)
	}

	// Must NOT contain Song 1 from Album 1 in the collected-in section (Album 1 is already in 关联专辑)
	// Check the collected-in-list block specifically:
	startCollected := strings.Index(body, "collected-in-list")
	if startCollected < 0 {
		t.Fatalf("missing collected-in-list class in body")
	}
	collectedSection := body[startCollected:]
	endCollected := strings.Index(collectedSection, "</section>")
	if endCollected > 0 {
		collectedSection = collectedSection[:endCollected]
	}
	if strings.Contains(collectedSection, "Song 1") {
		t.Fatalf("collected-in section must NOT contain Song 1 (which belongs to album-level tied Album 1)")
	}
}

func TestSeriesTitleConsistentAcrossListAndDetail(t *testing.T) {
	app, store, handler := credentialTestApp(t)
	series, groupedA, _, _ := seriesHTTPFixture(t, store)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// List page
	bodyList := getAdmin(t, handler, cookie, "/admin/works")

	// Detail page
	bodyDetail := getAdmin(t, handler, cookie, "/admin/works/"+strconv.FormatInt(groupedA.ID, 10))

	// Both pages must contain the exact same series title
	if !strings.Contains(bodyList, series.Title) {
		t.Fatalf("list page missing series title %q", series.Title)
	}
	if !strings.Contains(bodyDetail, series.Title) {
		t.Fatalf("detail page missing series title %q", series.Title)
	}

	// Dissolve hint must exist
	if !strings.Contains(bodyDetail, "解散后这些作品将不再参与自动归组") {
		t.Fatalf("detail page missing D39 dissolve hint")
	}
	_ = app
}

func TestAdminWorkOptionsAuthAndSearch(t *testing.T) {
	app, cookie, _, _, _, _ := setupBatch3HTTPFixture(t)
	handler := app.Handler()
	ctx := context.Background()

	// 1. Unauthenticated gets 401 JSON
	req := httptest.NewRequest(http.MethodGet, "/admin/options/works", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d, want 401", rec.Code)
	}

	// 2. Search options
	if _, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "CLANNAD", TranslatedTitle: "小镇家族", Type: "anime", Year: 2007}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "AIR", Type: "anime", Year: 2005}); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/options/works?q=CLANNAD", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("options status=%d", rec.Code)
	}
	var items []optionItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Label, "CLANNAD") {
		t.Fatalf("unexpected options result: %+v", items)
	}
}

// M-a（D47/D48）：专辑级 ost 与曲目级用途合并显示；OST 身份只看专辑级 role。
// 四种组合在专辑页胶囊与作品页角标上的口径必须一致。
func TestAlbumWorkDisplayD47D48(t *testing.T) {
	app, cookie, _, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	mk := func(title string) storage.Work {
		w, err := app.store.CreateWork(ctx, storage.WorkInput{Title: title, Type: "anime", Year: 2021})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	// 组合 1：专辑级 ost + 曲目级 op
	wOstOp := mk("Combo OST+OP")
	if err := app.store.AddWorkAlbum(ctx, wOstOp.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkTrack(ctx, wOstOp.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	// 组合 2：只有专辑级 ost
	wOst := mk("Combo OST only")
	if err := app.store.AddWorkAlbum(ctx, wOst.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}
	// 组合 3：只有曲目级 ost
	wTrackOst := mk("Combo track OST")
	if err := app.store.AddWorkTrack(ctx, wTrackOst.ID, storage.WorkTrackInput{TrackID: trackIDs[1], Role: "ost"}); err != nil {
		t.Fatal(err)
	}
	// 组合 4：只有专辑级 other
	wOther := mk("Combo other only")
	if err := app.store.AddWorkAlbum(ctx, wOther.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}

	// ---- 专辑页胶囊 ----
	body := getAdmin(t, handler, cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))
	if !strings.Contains(body, `work-capsule-role ost">OST · OP</span>`) {
		t.Fatalf("capsule for album-ost + track-op must be highlighted 'OST · OP'")
	}
	if !strings.Contains(body, `work-capsule-role ost">OST</span>`) {
		t.Fatalf("capsule for album-ost only must be highlighted 'OST'")
	}
	if !strings.Contains(body, `work-capsule-role ">OST</span>`) {
		t.Fatalf("capsule for track-ost only must show 'OST' WITHOUT ost highlight (D48)")
	}
	// 组合 4 的胶囊没有用途徽章（角色 span 在标题之后）
	w4idx := strings.Index(body, "Combo other only</span>")
	if w4idx < 0 {
		t.Fatalf("missing capsule for album-other work")
	}
	if strings.Contains(body[w4idx:w4idx+200], "work-capsule-role") {
		t.Fatalf("album-other only capsule must not show a role badge (R4)")
	}

	// ---- 作品页角标（与胶囊同口径；overlay class 只看 AlbumRole）----
	overlay := func(w storage.Work) string {
		return getAdmin(t, handler, cookie, "/admin/works/"+strconv.FormatInt(w.ID, 10))
	}
	if b := overlay(wOstOp); !strings.Contains(b, `role-overlay ost">OST · OP 专辑</span>`) {
		t.Fatalf("overlay for album-ost + track-op must be highlighted 'OST · OP 专辑'")
	}
	if b := overlay(wOst); !strings.Contains(b, `role-overlay ost">原声集 OST</span>`) {
		t.Fatalf("overlay for album-ost only must be highlighted '原声集 OST'")
	}
	// 组合 3：只有曲目级 ost 的作品没有专辑级关联 → 作品页不出现专辑角标，
	// 只在“收录于”曲目行显示该曲目的 OST 用途（D48：不能推出专辑是 OST）。
	if b := overlay(wTrackOst); strings.Contains(b, "role-overlay") || !strings.Contains(b, `role-badge ost">OST</span>`) {
		t.Fatalf("track-ost only: work page must show the track usage badge but no album overlay (D48)")
	}
	if b := overlay(wOther); !strings.Contains(b, `role-overlay ">相关专辑</span>`) {
		t.Fatalf("overlay for album-other only must be '相关专辑' without ost class")
	}
}

// L1：多用途按优先级排序合并（与 TrackWorksForAlbum 的 CASE 顺序一致）。
func TestWorkAlbumUsageLabelMergesAndOrders(t *testing.T) {
	if got := workAlbumUsageLabel("ost", []string{"op"}); got != "OST · OP" {
		t.Fatalf("ost+op = %q, want 'OST · OP'", got)
	}
	if got := workAlbumUsageLabel("", []string{"ed", "op"}); got != "OP · ED" {
		t.Fatalf("unordered track roles must be sorted by priority, got %q", got)
	}
	if got := workAlbumUsageLabel("ost", nil); got != "OST" {
		t.Fatalf("ost only = %q", got)
	}
	if got := workAlbumUsageLabel("other", nil); got != "" {
		t.Fatalf("other only = %q, want empty", got)
	}
	if got := workAlbumUsageLabel("ost", []string{"ost"}); got != "OST" {
		t.Fatalf("album ost + track ost must dedupe, got %q", got)
	}

}

// L4（M1）：导航角标等于待审数量；计数查询失败时页面正常渲染且不显示角标。
func TestNavBadgeReflectsPendingCount(t *testing.T) {
	app, cookie, _, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	// 无待审时：不显示角标
	body := getAdmin(t, handler, cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))
	if strings.Contains(body, `class="nav-badge"`) {
		t.Fatalf("nav badge must be absent when nothing is pending")
	}

	// 两条待审（1 专辑 + 1 曲目）→ 角标显示 2
	if err := app.store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{{ExternalID: "777", Title: "Badge Cand", Score: 80}}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveTrackSubjectCandidates(ctx, trackIDs[0], []storage.TrackSubjectCandidate{{ExternalID: "778", Title: "Badge Track", MatchKind: "exact"}}); err != nil {
		t.Fatal(err)
	}
	body = getAdmin(t, handler, cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))
	if !strings.Contains(body, `nav-badge">2`) {
		t.Fatalf("nav badge must show pending count 2: %s", body)
	}

	// 查询失败（context 已取消）时：chromeFor 返回 0，页面/侧栏仍能渲染且不显示角标
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	chrome := app.chromeFor(cancelled, adminSession{Username: "admin"}, "albums")
	if chrome.PendingReviewCount != 0 {
		t.Fatalf("cancelled ctx count = %d, want 0", chrome.PendingReviewCount)
	}
	var buf strings.Builder
	if err := app.templates.ExecuteTemplate(&buf, "sidebar", chrome); err != nil {
		t.Fatalf("render sidebar with failed count: %v", err)
	}
	if strings.Contains(buf.String(), `class="nav-badge"`) {
		t.Fatalf("sidebar must render without badge when count query failed")
	}
}

// L5：handleRemoveWorkTrack 支持 returnTo（站内），默认回到作品页 #albums 锚点。
func TestRemoveWorkTrackReturnTo(t *testing.T) {
	app, cookie, csrf, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "ReturnTo Anime", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}
	workPath := "/admin/works/" + strconv.FormatInt(work.ID, 10)

	// 默认：回到作品页 #albums，notice 在 fragment 之前
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	rec := postForm(handler, cookie, workPath+"/tracks/"+strconv.FormatInt(trackIDs[0], 10)+"/remove", url.Values{
		"csrfToken": {csrf}, "role": {"op"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("remove status=%d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, workPath+"?notice=") || !strings.HasSuffix(loc, "#albums") {
		t.Fatalf("default returnTo must be %s?notice=...#albums, got %q", workPath, loc)
	}

	// 站内 returnTo 被尊重；站外被拒绝回退默认值
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	rec = postForm(handler, cookie, workPath+"/tracks/"+strconv.FormatInt(trackIDs[0], 10)+"/remove", url.Values{
		"csrfToken": {csrf}, "role": {"op"}, "returnTo": {"/admin/albums/" + strconv.FormatInt(albumID, 10)},
	})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/albums/"+strconv.FormatInt(albumID, 10)+"?notice=") {
		t.Fatalf("站内 returnTo not respected: %q", loc)
	}
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	rec = postForm(handler, cookie, workPath+"/tracks/"+strconv.FormatInt(trackIDs[0], 10)+"/remove", url.Values{
		"csrfToken": {csrf}, "role": {"op"}, "returnTo": {"https://evil.com"},
	})
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "evil.com") || !strings.HasPrefix(loc, workPath) {
		t.Fatalf("external returnTo must be rejected, got %q", loc)
	}
}

// L7/L-b：见 TestWorkReviewStoreErrorPerTab（逐 tab 隔离构造失败场景）。

// M1（D49）：同一首曲目对同一作品的多种用途在所有位置合并显示（OP · ED）。
func TestTrackWorkMultiRoleMergedD49(t *testing.T) {
	app, cookie, _, albumID, _, trackIDs := setupBatch3HTTPFixture(t)
	ctx := context.Background()
	handler := app.Handler()

	work, err := app.store.CreateWork(ctx, storage.WorkInput{Title: "Multi Role Anime", Type: "anime", Year: 2022})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkAlbum(ctx, work.ID, albumID, "other"); err != nil {
		t.Fatal(err)
	}
	// 同一（曲目，作品）：bangumi 来源 op + manual 来源 ed
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackIDs[0], Role: "ed"}); err != nil {
		t.Fatal(err)
	}
	httpExec(t, app.store, `UPDATE work_tracks SET source='bangumi' WHERE work_id=? AND track_id=? AND role='op'`, work.ID, trackIDs[0])

	// 存储层：TrackWorksForAlbum 的 Roles 带全部用途（按优先级排序），代表行来源为 manual
	tws, err := app.store.TrackWorksForAlbum(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	var found *storage.AlbumTrackWorkView
	for i := range tws {
		if tws[i].Work.ID == work.ID && tws[i].TrackID == trackIDs[0] {
			found = &tws[i]
		}
	}
	if found == nil || len(found.Roles) != 2 || found.Roles[0] != "op" || found.Roles[1] != "ed" {
		t.Fatalf("TrackWorksForAlbum Roles = %+v, want [op ed]", found)
	}
	if found.Source != "manual" {
		t.Fatalf("representative source = %q, want manual (manual > bangumi)", found.Source)
	}

	albumBody := getAdmin(t, handler, cookie, "/admin/albums/"+strconv.FormatInt(albumID, 10))
	// 胶囊
	if !strings.Contains(albumBody, `work-capsule-role ">OP · ED</span>`) {
		t.Fatalf("capsule must show merged 'OP · ED'")
	}
	// 曲目行图标
	if !strings.Contains(albumBody, `<span>OP · ED</span>`) {
		t.Fatalf("track row tieup icon must show merged 'OP · ED'")
	}
	// 右栏汇总
	inspectorIdx := strings.Index(albumBody, `id="inspector-works"`)
	if inspectorIdx < 0 || !strings.Contains(albumBody[inspectorIdx:], "OP · ED · Song 1") {
		t.Fatalf("inspector summary must show merged roles: %s", albumBody[inspectorIdx:inspectorIdx+600])
	}

	// 作品页 overlay 角标
	workBody := getAdmin(t, handler, cookie, "/admin/works/"+strconv.FormatInt(work.ID, 10))
	if !strings.Contains(workBody, `role-overlay ">OP · ED 专辑</span>`) {
		t.Fatalf("work page overlay must show merged 'OP · ED 专辑'")
	}
}

// L-a：曲目级 other 不参与合并；专辑级 ost + 曲目级只有 ost → 原声集 OST；"相关专辑"无空格。
func TestWorkAlbumRelationLabelEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		aw   storage.AlbumWork
		want string
	}{
		{"track-level other filtered", storage.AlbumWork{AlbumRole: "other", Role: "other", AlbumType: "album", TrackRoles: []string{"other"}}, "相关专辑"},
		{"track-level other + op keeps op only", storage.AlbumWork{AlbumRole: "other", Role: "op", AlbumType: "single", TrackRoles: []string{"other", "op"}}, "OP 单曲"},
		{"album ost + track ost only", storage.AlbumWork{AlbumRole: "ost", Role: "ost", AlbumType: "album", TrackRoles: []string{"ost"}}, "原声集 OST"},
		{"album ost + track ost and op", storage.AlbumWork{AlbumRole: "ost", Role: "op", AlbumType: "album", TrackRoles: []string{"ost", "op"}}, "OST · OP 专辑"},
		{"album ost no track roles", storage.AlbumWork{AlbumRole: "ost", Role: "ost", AlbumType: "album"}, "原声集 OST"},
	}
	for _, tc := range cases {
		got := workAlbumRelationLabel(tc.aw)
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, "  ") || strings.Contains(got, "相关 ") {
			t.Fatalf("%s: label %q has stray space", tc.name, got)
		}
	}
	if got := workAlbumUsageLabel("", []string{"other"}); got != "" {
		t.Fatalf("track-level other must be filtered, got %q", got)
	}
}

// L-b：逐个 tab 构造只让该 tab 的查询失败的场景（重命名对应表），断言 500 且不显示“暂无待审”。
func TestWorkReviewStoreErrorPerTab(t *testing.T) {
	cases := []struct {
		name  string
		table string
		path  string
	}{
		{"albums tab", "album_subject_candidates", "/admin/work-review?tab=albums"},
		{"albums tab with albumId filter", "album_subject_candidates", "/admin/work-review?tab=albums&albumId=1"},
		{"tracks tab", "track_subject_candidates", "/admin/work-review?tab=tracks"},
		{"tracks tab with albumId filter", "track_subject_candidates", "/admin/work-review?tab=tracks&albumId=1"},
		{"works tab", "work_match_candidates", "/admin/work-review?tab=works"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, cookie, _, _, _, _ := setupBatch3HTTPFixture(t)
			handler := app.Handler()

			// 重命名表，只让该类查询失败
			httpExec(t, app.store, `ALTER TABLE `+tc.table+` RENAME TO `+tc.table+`_gone`)
			defer httpExec(t, app.store, `ALTER TABLE `+tc.table+`_gone RENAME TO `+tc.table)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("GET %s with %s renamed = %d, want 500", tc.path, tc.table, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "暂无待审") {
				t.Fatalf("GET %s must not render 暂无待审 on store error", tc.path)
			}
		})
	}
}

// L-b：enrichmentReviews 本身把查询错误返回出来。
func TestEnrichmentReviewsPropagatesError(t *testing.T) {
	app, _, _, _, _, _ := setupBatch3HTTPFixture(t)
	ctx := context.Background()

	httpExec(t, app.store, `ALTER TABLE work_match_candidates RENAME TO work_match_candidates_gone`)
	defer httpExec(t, app.store, `ALTER TABLE work_match_candidates_gone RENAME TO work_match_candidates`)

	if _, _, _, _, err := app.enrichmentReviews(ctx); err == nil {
		t.Fatalf("enrichmentReviews must return the query error")
	}
}
