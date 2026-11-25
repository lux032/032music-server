package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

func TestAdminAlbumRulesPageAndDeleteFlow(t *testing.T) {
	ctx := context.Background()
	store := credentialTestStore(t)
	cfg := credentialTestConfig(t)
	musicRoot := t.TempDir()
	cfg.MusicDirectory = musicRoot

	if err := store.EnsureLibrary(ctx, "Default", musicRoot); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, musicRoot)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanMgr := scanner.New(ctx, store, logger, library, t.TempDir())
	app, err := NewApp(cfg, store, scanMgr, nil, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()

	// 未登录访问页面与写接口均应被拒绝。
	unauthGet := httptest.NewRecorder()
	handler.ServeHTTP(unauthGet, httptest.NewRequest(http.MethodGet, albumRulesPath, nil))
	if unauthGet.Code != http.StatusSeeOther && unauthGet.Code != http.StatusFound {
		t.Fatalf("unauthenticated GET %s = %d, want redirect", albumRulesPath, unauthGet.Code)
	}
	unauthPost := httptest.NewRecorder()
	handler.ServeHTTP(unauthPost, httptest.NewRequest(http.MethodPost, albumRulesPath+"/delete", strings.NewReader("rule=1:foo")))
	if unauthPost.Code != http.StatusSeeOther && unauthPost.Code != http.StatusFound && unauthPost.Code != http.StatusUnauthorized && unauthPost.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated POST %s/delete = %d, want rejected", albumRulesPath, unauthPost.Code)
	}

	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// 无规则时展示空状态。
	emptyRec := getWithCookie(handler, albumRulesPath, cookie)
	if emptyRec.Code != http.StatusOK || !strings.Contains(emptyRec.Body.String(), "没有屏蔽规则") || !strings.Contains(emptyRec.Body.String(), "没有合并规则") {
		t.Fatalf("empty rules page status=%d", emptyRec.Code)
	}

	// 导入四张专辑，制造 2 条 delete 规则和 1 条 merge 规则。
	seedTracks := []storage.ImportInput{
		{
			LibraryID:    library.ID,
			RelativePath: "ナナヲアカリ/ぴ/01.flac",
			FileSize:     100,
			ModifiedAtNS: 100,
			Metadata: metadata.AudioMetadata{
				Title:        "ぴ",
				Album:        "ぴ",
				Artists:      []string{"ナナヲアカリ"},
				AlbumArtists: []string{"ナナヲアカリ"},
				Year:         2026,
				DiscNumber:   1,
				TrackNumber:  1,
			},
		},
		{
			LibraryID:    library.ID,
			RelativePath: "Other/BlockedTwo/01.flac",
			FileSize:     100,
			ModifiedAtNS: 100,
			Metadata: metadata.AudioMetadata{
				Title:        "Two Song",
				Album:        "BlockedTwo",
				Artists:      []string{"Other"},
				AlbumArtists: []string{"Other"},
				Year:         2025,
				DiscNumber:   1,
				TrackNumber:  1,
			},
		},
		{
			LibraryID:    library.ID,
			RelativePath: "Main/01.flac",
			FileSize:     100,
			ModifiedAtNS: 100,
			Metadata: metadata.AudioMetadata{
				Title:        "Main Song",
				Album:        "MainAlbum",
				Artists:      []string{"ArtistA"},
				AlbumArtists: []string{"ArtistA"},
				Year:         2024,
				DiscNumber:   1,
				TrackNumber:  1,
			},
		},
		{
			LibraryID:    library.ID,
			RelativePath: "Merged/01.flac",
			FileSize:     100,
			ModifiedAtNS: 100,
			Metadata: metadata.AudioMetadata{
				Title:        "Merged Song",
				Album:        "MergedSource",
				Artists:      []string{"ArtistA"},
				AlbumArtists: []string{"ArtistA"},
				Year:         2024,
				DiscNumber:   1,
				TrackNumber:  2,
			},
		},
	}
	for _, in := range seedTracks {
		if err := store.ImportTrack(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	albumIDs := map[string]int64{}
	for _, al := range albums {
		albumIDs[al.Title] = al.ID
	}
	if _, err := store.DeleteAlbums(ctx, []int64{albumIDs["ぴ"], albumIDs["BlockedTwo"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MergeAlbums(ctx, albumIDs["MainAlbum"], []int64{albumIDs["MergedSource"]}); err != nil {
		t.Fatal(err)
	}

	rules, err := store.ListAlbumKeyRules(ctx)
	if err != nil || len(rules) != 3 {
		t.Fatalf("rules = %+v, err = %v; want 3 rules", rules, err)
	}
	ruleValueByTitle := map[string]string{}
	for _, r := range rules {
		ruleValueByTitle[r.SourceTitle] = strconv.FormatInt(r.LibraryID, 10) + ":" + r.GroupingKey
	}

	// 页面展示 delete 与 merge 规则，以及合并目标专辑链接。
	pageRec := getWithCookie(handler, albumRulesPath, cookie)
	pageBody := pageRec.Body.String()
	if pageRec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", albumRulesPath, pageRec.Code)
	}
	targetLink := `/admin/albums/` + strconv.FormatInt(albumIDs["MainAlbum"], 10)
	for _, want := range []string{
		"ナナヲアカリ/ぴ|ぴ|2026",
		"BlockedTwo",
		"MergedSource",
		"MainAlbum",
		targetLink,
		`data-nav="rules"`,
	} {
		if !strings.Contains(pageBody, want) {
			t.Fatalf("rules page missing %q", want)
		}
	}

	// 非法 CSRF 令牌返回 403 且规则未被修改。
	badCSRF := postSecurity(t, app, handler, cookie, albumRulesPath+"/delete", url.Values{
		"csrfToken": {"forged"},
		"rule":      {ruleValueByTitle["ぴ"]},
	})
	if badCSRF.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF status = %d, want 403", badCSRF.Code)
	}

	// 未选择有效规则时重定向提示，不删除任何规则。
	emptySel := postSecurity(t, app, handler, cookie, albumRulesPath+"/delete", url.Values{
		"rule": {"invalid-without-colon"},
	})
	if emptySel.Code != http.StatusSeeOther {
		t.Fatalf("invalid selection status = %d", emptySel.Code)
	}
	if delCount, mergeCount, _ := store.CountAlbumKeyRules(ctx); delCount != 2 || mergeCount != 1 {
		t.Fatalf("counts after rejected requests = (%d, %d), want (2, 1)", delCount, mergeCount)
	}

	// 删除前再次导入 ぴ 应被规则静默跳过。
	blockedRes, err := store.ImportTrackBatchDetailed(ctx, []storage.ImportInput{seedTracks[0]})
	if err != nil || len(blockedRes) != 1 || !blockedRes[0].RuleSkipped {
		t.Fatalf("blocked import result = %+v, err = %v; want RuleSkipped=true", blockedRes, err)
	}

	// 解除 ぴ 的删除规则并附带 rescan=1 立即触发增量扫描。
	unblockRec := postSecurity(t, app, handler, cookie, albumRulesPath+"/delete", url.Values{
		"rule":   {ruleValueByTitle["ぴ"]},
		"rescan": {"1"},
	})
	if unblockRec.Code != http.StatusSeeOther || !strings.Contains(unblockRec.Header().Get("Location"), url.QueryEscape("已解除 1 条规则，并已启动增量扫描")) {
		t.Fatalf("unblock with rescan: status=%d location=%q", unblockRec.Code, unblockRec.Header().Get("Location"))
	}
	// 等待后台增量扫描完成，避免与测试清理并发冲突。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, jobErr := store.LatestScanJob(ctx)
		if jobErr == nil && job.ID > 0 && job.Status != "queued" && job.Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 解除规则后可正常重新入库 ぴ。
	unblockedRes, err := store.ImportTrackBatchDetailed(ctx, []storage.ImportInput{seedTracks[0]})
	if err != nil || len(unblockedRes) != 1 || unblockedRes[0].RuleSkipped || unblockedRes[0].Err != nil {
		t.Fatalf("unblocked import result = %+v, err = %v; want normal import", unblockedRes, err)
	}
	albumsAfterUnblock, err := store.ListAlbums(ctx, storage.Filters{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	foundPi := false
	for _, al := range albumsAfterUnblock {
		if al.Title == "ぴ" {
			foundPi = true
		}
	}
	if !foundPi {
		t.Fatal("album ぴ must be re-imported after deleting its delete rule")
	}

	// 批量解除剩余 1 条 delete 规则与 1 条 merge 规则。
	batchRec := postSecurity(t, app, handler, cookie, albumRulesPath+"/delete", url.Values{
		"rule": {ruleValueByTitle["BlockedTwo"], ruleValueByTitle["MergedSource"]},
	})
	if batchRec.Code != http.StatusSeeOther || !strings.Contains(batchRec.Header().Get("Location"), url.QueryEscape("已解除 2 条规则")) {
		t.Fatalf("batch delete: status=%d location=%q", batchRec.Code, batchRec.Header().Get("Location"))
	}
	if delCount, mergeCount, err := store.CountAlbumKeyRules(ctx); err != nil || delCount != 0 || mergeCount != 0 {
		t.Fatalf("counts after batch delete = (%d, %d), err = %v; want (0, 0)", delCount, mergeCount, err)
	}

	// 解除 merge 规则后，已合并到 MainAlbum 的曲目必须完整保留。
	mainTracks, err := store.ListTracks(ctx, storage.Filters{AlbumID: albumIDs["MainAlbum"], Limit: 50})
	if err != nil || len(mainTracks) != 2 {
		t.Fatalf("MainAlbum tracks after removing merge rule = %d, err = %v; want 2", len(mainTracks), err)
	}
}

func TestDashboardAndAdminStatusShowRuleSkippedAndRulesLink(t *testing.T) {
	ctx := context.Background()
	app, store, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	if err := store.EnsureLibrary(ctx, "Default", "/music-dashboard"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music-dashboard")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path  string
		album string
	}{
		{"Del/01.flac", "DelAlbum"},
		{"Main/01.flac", "MainAlbum"},
		{"Sub/01.flac", "SubAlbum"},
	} {
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    library.ID,
			RelativePath: item.path,
			FileSize:     10,
			ModifiedAtNS: 10,
			Metadata: metadata.AudioMetadata{
				Title:        item.album + " track",
				Album:        item.album,
				Artists:      []string{"Singer"},
				AlbumArtists: []string{"Singer"},
				DiscNumber:   1,
				TrackNumber:  1,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, al := range albums {
		ids[al.Title] = al.ID
	}
	if _, err := store.DeleteAlbums(ctx, []int64{ids["DelAlbum"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MergeAlbums(ctx, ids["MainAlbum"], []int64{ids["SubAlbum"]}); err != nil {
		t.Fatal(err)
	}

	// 构造一条包含 ruleSkippedFiles = 6 的扫描记录。
	jobID, err := store.CreateScanJob(ctx, library.ID, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartScanJob(ctx, jobID, 100); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateScanJobProgress(ctx, jobID, 0, 100, 6, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScanJob(ctx, jobID, "completed", 0, ""); err != nil {
		t.Fatal(err)
	}

	dashRec := getWithCookie(handler, "/admin", cookie)
	if dashRec.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d", dashRec.Code)
	}
	dashBody := dashRec.Body.String()
	for _, want := range []string{
		`id="scan-rule-skipped" class="scan-rule-notice">`,
		`<strong id="scan-rule-skipped-count">6</strong>`,
		`href="/admin/library/rules"`,
		`id="library-rules"`,
		`屏蔽 1 · 合并 1`,
		`共 2 条规则`,
		`data-nav="rules"`,
	} {
		if !strings.Contains(dashBody, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}

	// 验证 /admin/status JSON 包含 ruleSkippedFiles。
	statusRec := getWithCookie(handler, "/admin/status", cookie)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /admin/status = %d", statusRec.Code)
	}
	var statusPayload struct {
		Scan struct {
			DiscoveredFiles  int64 `json:"discoveredFiles"`
			ProcessedFiles   int64 `json:"processedFiles"`
			SkippedFiles     int64 `json:"skippedFiles"`
			RuleSkippedFiles int64 `json:"ruleSkippedFiles"`
		} `json:"scan"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("unmarshal /admin/status: %v", err)
	}
	if statusPayload.Scan.RuleSkippedFiles != 6 || statusPayload.Scan.ProcessedFiles != 0 || statusPayload.Scan.SkippedFiles != 100 {
		t.Fatalf("status scan = %+v, want ruleSkippedFiles=6 processed=0 skipped=100", statusPayload.Scan)
	}

	// 新一次扫描 ruleSkippedFiles == 0 时，控制台提示应保持 hidden。
	jobID2, err := store.CreateScanJob(ctx, library.ID, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartScanJob(ctx, jobID2, 100); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateScanJobProgress(ctx, jobID2, 0, 100, 0, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScanJob(ctx, jobID2, "completed", 0, ""); err != nil {
		t.Fatal(err)
	}
	dashBody2 := getWithCookie(handler, "/admin", cookie).Body.String()
	if !strings.Contains(dashBody2, `id="scan-rule-skipped" class="scan-rule-notice" hidden>`) {
		t.Fatal("dashboard must hide #scan-rule-skipped when ruleSkippedFiles == 0")
	}

	statusRec2 := getWithCookie(handler, "/admin/status", cookie)
	if err := json.Unmarshal(statusRec2.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("unmarshal /admin/status (2): %v", err)
	}
	if statusPayload.Scan.RuleSkippedFiles != 0 {
		t.Fatalf("status ruleSkippedFiles = %d, want 0", statusPayload.Scan.RuleSkippedFiles)
	}

	_ = app
}
