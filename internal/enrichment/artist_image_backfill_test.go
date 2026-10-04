package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// backfillJPEG 是 http.DetectContentType 可识别为 image/jpeg 的最小内容。
const backfillJPEG = "\xff\xd8\xff\xe0\x00\x10JFIF"

// imageHostURL 是 IP 字面量的“公网”图片地址：validatePublicImageURL 跳过
// DNS 解析，请求由测试 transport 应答，绝不触碰真实网络。
const imageHostURL = "http://93.184.216.34/artist.jpg"

// backfillTransport 按 host 分发假应答：musicbrainz.org / open.spotify.com /
// ws.audioscrobbler.com / 93.184.216.34，并统计各类请求数。
type backfillTransport struct {
	mbBody       string
	spotifyThumb string
	lastfmBody   string
	lastfmStatus int
	imageStatus  int
	imageBody    string
	mbRequests   atomic.Int32
	spotifyReqs  atomic.Int32
	lastfmReqs   atomic.Int32
	imageReqs    atomic.Int32
}

func (bt *backfillTransport) roundTrip(r *http.Request) (*http.Response, error) {
	status := http.StatusOK
	body := ""
	switch {
	case r.URL.Host == "93.184.216.34":
		bt.imageReqs.Add(1)
		status = bt.imageStatus
		body = bt.imageBody
	case strings.Contains(r.URL.Host, "musicbrainz.org"):
		bt.mbRequests.Add(1)
		body = bt.mbBody
	case strings.Contains(r.URL.Host, "spotify.com"):
		bt.spotifyReqs.Add(1)
		body = fmt.Sprintf(`{"thumbnail_url":%q}`, bt.spotifyThumb)
	case strings.Contains(r.URL.Host, "audioscrobbler.com"):
		bt.lastfmReqs.Add(1)
		status = bt.lastfmStatus
		body = bt.lastfmBody
	default:
		return nil, fmt.Errorf("unexpected host %s", r.URL.Host)
	}
	header := http.Header{}
	if status == http.StatusTooManyRequests {
		header.Set("Retry-After", "60")
	}
	return cannedResponse(status, header, body), nil
}

func newBackfillTransport() *backfillTransport {
	return &backfillTransport{imageStatus: http.StatusOK, imageBody: backfillJPEG, lastfmStatus: http.StatusOK}
}

// imageBackfillManager 构建一个 HTTP 全部被 transport 拦截的管理器，
// 并导入一位艺术家（可选 tagged MBID）。
func imageBackfillManager(t *testing.T, bt *backfillTransport, artist, taggedMBID string) (*Manager, int64) {
	t.Helper()
	manager := transportManager(t, roundTripFunc(bt.roundTrip))
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string][]string{}
	if taggedMBID != "" {
		raw["MUSICBRAINZ_ARTISTID"] = []string{taggedMBID}
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: artist + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{artist}, AlbumArtists: []string{artist}, DiscNumber: 1, TrackNumber: 1, Raw: raw}}); err != nil {
		t.Fatal(err)
	}
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	return manager, artists[0].ID
}

func enableBackfillSource(t *testing.T, store *storage.Store, source string, enabled bool) {
	t.Helper()
	setting, err := store.MetadataSourceSetting(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = enabled
	setting.AutoMatch = enabled
	if source == "lastfm" {
		setting.APIKey = "mock-key"
	}
	if err = store.SaveMetadataSourceSetting(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
}

func confirmBackfillIdentity(t *testing.T, store *storage.Store, artistID int64, source, externalID, imageURL string) {
	t.Helper()
	if err := store.UpsertExternalArtistProfile(context.Background(), artistID, storage.ExternalArtistProfile{Source: source, ExternalID: externalID, DisplayName: "Confirmed", RemoteImageURL: imageURL}); err != nil {
		t.Fatal(err)
	}
}

func imageBackfillCacheRow(t *testing.T, manager *Manager, artistID int64) (cachePath string, ok bool) {
	t.Helper()
	db := writableSafetyDB(t, manager)
	err := db.QueryRowContext(context.Background(), `SELECT cache_path FROM artist_image_cache WHERE artist_id=?`, artistID).Scan(&cachePath)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return cachePath, true
}

// waitImageBackfillRun 轮询直到补全任务离开 running/queued（真实时钟，
// 供无限流场景使用）。
func waitImageBackfillRun(t *testing.T, store *storage.Store, runID int64) storage.ArtistImageBackfillRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.DurableArtistImageBackfillRun(context.Background(), runID)
		if err == nil && run.Status != "running" && run.Status != "queued" {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("artist image backfill run did not finish")
	return storage.ArtistImageBackfillRun{}
}

// 库存 URL 优先：一次图片 GET 完成缓存，不查询任何来源 API。
func TestArtistImageBackfillCachesInventoryURL(t *testing.T) {
	bt := newBackfillTransport()
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Cached != 1 || finished.Failed != 0 || finished.Total != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.imageReqs.Load() != 1 || bt.mbRequests.Load() != 0 || bt.lastfmReqs.Load() != 0 {
		t.Fatalf("image=%d mb=%d lastfm=%d", bt.imageReqs.Load(), bt.mbRequests.Load(), bt.lastfmReqs.Load())
	}
	cachePath, ok := imageBackfillCacheRow(t, manager, artist)
	if !ok {
		t.Fatal("cache row missing")
	}
	if _, err = os.Stat(cachePath); err != nil {
		t.Fatalf("cached file missing: %v", err)
	}
	// 幂等：缓存有效后再启动没有候选。
	if _, err = manager.StartArtistImageBackfill(context.Background()); !errors.Is(err, ErrNoArtistImageBackfillCandidates) {
		t.Fatalf("second start must have no candidates: %v", err)
	}
}

// 库存 URL 为空：只向已确认身份对应的启用来源（Last.fm）查询资料取 URL，
// 身份不变，随后下载缓存。
func TestArtistImageBackfillEmptyURLQueriesLastFM(t *testing.T) {
	bt := newBackfillTransport()
	bt.lastfmBody = `{"artist":{"name":"Backfill Artist","mbid":"mbid-lf-1","url":"https://www.last.fm/music/x","bio":{"content":""},"image":[{"#text":"` + imageHostURL + `","size":"large"}]}}`
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-lf-1", "")
	enableBackfillSource(t, manager.store, "lastfm", true)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Cached != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.lastfmReqs.Load() != 1 || bt.imageReqs.Load() != 1 || bt.mbRequests.Load() != 0 {
		t.Fatalf("lastfm=%d image=%d mb=%d", bt.lastfmReqs.Load(), bt.imageReqs.Load(), bt.mbRequests.Load())
	}
	// 身份未被重写，URL 已由来源资料补上。
	externalID, err := manager.store.ArtistExternalID(context.Background(), artist, "lastfm")
	if err != nil || externalID != "mbid-lf-1" {
		t.Fatalf("identity rewritten: %q %v", externalID, err)
	}
	_, _, remoteURL, err := manager.store.ArtistImageProfileSource(context.Background(), artist)
	if err != nil || remoteURL != imageHostURL {
		t.Fatalf("profile url=%q err=%v", remoteURL, err)
	}
}

// 空 URL 且已确认身份的来源被禁用：不查询、不计失败，outcome=no_url。
func TestArtistImageBackfillNoURLWhenSourceDisabled(t *testing.T) {
	bt := newBackfillTransport()
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-lf-1", "")
	enableBackfillSource(t, manager.store, "lastfm", false)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.NoURL != 1 || finished.Failed != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.imageReqs.Load()+bt.mbRequests.Load()+bt.lastfmReqs.Load() != 0 {
		t.Fatal("disabled sources must not be queried")
	}
}

// 空 URL 时 MusicBrainz 路径：按已绑定 MBID 查档，经 Spotify 关系取得图片
// 地址后下载缓存。
func TestArtistImageBackfillEmptyURLQueriesMusicBrainz(t *testing.T) {
	bt := newBackfillTransport()
	bt.spotifyThumb = imageHostURL
	bt.mbBody = `{"id":"mbid-mb-1","name":"Backfill Artist","sort-name":"Backfill Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/xyz"}}]}`
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "musicbrainz", "mbid-mb-1", "")
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Cached != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.mbRequests.Load() != 1 || bt.spotifyReqs.Load() != 1 || bt.imageReqs.Load() != 1 {
		t.Fatalf("mb=%d spotify=%d image=%d", bt.mbRequests.Load(), bt.spotifyReqs.Load(), bt.imageReqs.Load())
	}
}

// 来源返回的身份与已确认身份不一致（漂移）：资料不落库、身份不改写、
// 图片不采用，outcome=no_url。
func TestArtistImageBackfillIdentityDriftRefused(t *testing.T) {
	bt := newBackfillTransport()
	bt.lastfmBody = `{"artist":{"name":"Someone Else","mbid":"mbid-DIFFERENT","url":"https://www.last.fm/music/y","bio":{"content":""},"image":[{"#text":"` + imageHostURL + `","size":"large"}]}}`
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-lf-1", "")
	enableBackfillSource(t, manager.store, "lastfm", true)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.NoURL != 1 || finished.Cached != 0 {
		t.Fatalf("run=%+v", finished)
	}
	externalID, err := manager.store.ArtistExternalID(context.Background(), artist, "lastfm")
	if err != nil || externalID != "mbid-lf-1" {
		t.Fatalf("identity rewritten by drifted profile: %q %v", externalID, err)
	}
	if _, _, remoteURL, err := manager.store.ArtistImageProfileSource(context.Background(), artist); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("drifted profile url must not persist: %q %v", remoteURL, err)
	}
}

// 候选口径与“行在文件丢”自愈：自定义头像/有效缓存/无身份者不进任务；
// 缓存行在但磁盘文件丢失者重新补全。
func TestArtistImageBackfillCandidatesAndMissingFileRepair(t *testing.T) {
	bt := newBackfillTransport()
	manager := transportManager(t, roundTripFunc(bt.roundTrip))
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	importOne := func(name string) int64 {
		if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album-" + name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
		artists, _ := store.ArtistsForMatching(ctx)
		for _, a := range artists {
			if a.Name == name {
				return a.ID
			}
		}
		t.Fatalf("artist %s missing", name)
		return 0
	}
	plain := importOne("Plain")     // 有身份无缓存 → 候选
	customized := importOne("Cust") // 有身份有自定义 → 排除
	cached := importOne("Cached")   // 有身份有有效缓存 → 排除
	broken := importOne("Broken")   // 有身份缓存行在但文件丢 → 候选（自愈）
	faceless := importOne("NoID")   // 无身份 → 排除
	confirmBackfillIdentity(t, store, plain, "lastfm", "lf-plain", imageHostURL)
	confirmBackfillIdentity(t, store, customized, "lastfm", "lf-cust", imageHostURL)
	confirmBackfillIdentity(t, store, cached, "lastfm", "lf-cached", imageHostURL)
	confirmBackfillIdentity(t, store, broken, "lastfm", "lf-broken", imageHostURL)

	if _, err = store.SaveCustomArtistImage(ctx, customized, storage.CustomImageInput{Hash: "h-custom", MIMEType: "image/png", FileName: "cust.png", ByteSize: 10}); err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(t.TempDir(), "valid.jpg")
	if err = os.WriteFile(validPath, []byte(backfillJPEG), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveArtistImage(ctx, storage.ArtistImageInput{ArtistID: cached, ByteSize: 10, Source: "lastfm", RemoteURL: imageHostURL, Hash: "h-valid", MIMEType: "image/jpeg", CachePath: validPath}); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveArtistImage(ctx, storage.ArtistImageInput{ArtistID: broken, ByteSize: 10, Source: "lastfm", RemoteURL: imageHostURL, Hash: "h-broken", MIMEType: "image/jpeg", CachePath: filepath.Join(t.TempDir(), "gone.jpg")}); err != nil {
		t.Fatal(err)
	}

	cachedCount, total, err := manager.ArtistImageBackfillStats(ctx)
	if err != nil || total != 3 || cachedCount != 1 {
		t.Fatalf("stats cached=%d total=%d err=%v (want plain/cached/broken, only cached valid)", cachedCount, total, err)
	}
	runID, err := manager.StartArtistImageBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Total != 2 || finished.Cached != 2 {
		t.Fatalf("run=%+v (want plain+broken only)", finished)
	}
	for _, id := range []int64{plain, broken} {
		if path, ok := imageBackfillCacheRow(t, manager, id); !ok {
			t.Fatalf("artist %d not cached", id)
		} else if _, err = os.Stat(path); err != nil {
			t.Fatalf("artist %d cache file missing: %v", id, err)
		}
	}
	if _, ok := imageBackfillCacheRow(t, manager, customized); ok {
		t.Fatal("custom image artist must not gain automatic cache")
	}
	if _, ok := imageBackfillCacheRow(t, manager, faceless); ok {
		t.Fatal("identity-less artist must not gain cache")
	}
	if bt.imageReqs.Load() != 2 {
		t.Fatalf("image requests=%d, want 2", bt.imageReqs.Load())
	}
}

// 限流固定阶梯 30s/1min/3min、waiting_until 持久化、自动恢复最多 3 轮后
// 停在 rate_limit_exhausted，人工继续仍然可用。fake clock，无真实等待。
func TestArtistImageBackfillRateLimitAutoResumeExhausted(t *testing.T) {
	bt := newBackfillTransport()
	bt.imageStatus = http.StatusTooManyRequests
	bt.imageBody = ""
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	artistFakeClock(manager)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, err := manager.store.DurableArtistImageBackfillRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// 4 轮 × 3 次限流响应 = 12 次请求；3 轮自动恢复用尽后停在终态。
	if state.Status != "paused" || state.PauseReason != "rate_limit_exhausted" || state.AutoResumeCount != 3 || bt.imageReqs.Load() != 12 {
		t.Fatalf("run=%+v imageReqs=%d", state, bt.imageReqs.Load())
	}
	if state.WaitingUntil == "" || state.WaitTotalMS == 0 {
		t.Fatalf("waiting deadline/total must persist: %+v", state)
	}
	if storage.ArtistImageBackfillAutoResumeEligible(state) {
		t.Fatal("exhausted run must not auto resume again")
	}
	// 断点保留：item 仍未完成，人工继续后接着重试（3 次后再停）。
	if err = manager.ResumeArtistImageBackfill(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, _ = manager.store.DurableArtistImageBackfillRun(context.Background(), runID)
	if state.Status != "paused" || state.PauseReason != "rate_limit_exhausted" || bt.imageReqs.Load() != 15 || state.Processed != 0 {
		t.Fatalf("run=%+v imageReqs=%d", state, bt.imageReqs.Load())
	}
}

// 限流后成功恢复：429 的共享冷却（60s，长于固定阶梯 30s）取胜，一轮
// 等待后下载成功，任务完成且等待已持久化。fake clock，无真实等待。
func TestArtistImageBackfillRateLimitThenSuccess(t *testing.T) {
	bt := newBackfillTransport()
	var imageCalls atomic.Int32
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	base := manager.client.Transport
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "93.184.216.34" && imageCalls.Add(1) == 1 {
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"60"}}, ""), nil
		}
		return base.RoundTrip(r)
	})
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	artistFakeClock(manager)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, err := manager.store.DurableArtistImageBackfillRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" || state.Cached != 1 || imageCalls.Load() != 2 || state.WaitTotalMS != 60000 {
		t.Fatalf("run=%+v imageCalls=%d", state, imageCalls.Load())
	}
}

// 暂停/继续/停止：等待中的 worker 可被打断，断点保留，取消为终态。
func TestArtistImageBackfillPauseResumeCancel(t *testing.T) {
	bt := newBackfillTransport()
	bt.imageStatus = http.StatusTooManyRequests
	bt.imageBody = ""
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	var entered atomic.Int32
	release := make(chan struct{})
	manager.sleep = func(ctx context.Context, d time.Duration) error {
		entered.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if entered.Load() == 0 {
		t.Fatal("worker never entered rate-limit wait")
	}
	if err = manager.PauseArtistImageBackfill(runID); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, _ := manager.store.DurableArtistImageBackfillRun(context.Background(), runID)
	if state.Status != "paused" || state.PauseReason != "manual" || state.Processed != 0 {
		t.Fatalf("run=%+v", state)
	}
	if err = manager.ResumeArtistImageBackfill(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for entered.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if entered.Load() < 2 {
		t.Fatal("resumed worker never re-entered wait")
	}
	if err = manager.CancelArtistImageBackfill(runID); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	close(release)
	state, _ = manager.store.DurableArtistImageBackfillRun(context.Background(), runID)
	if state.Status != "cancelled" || state.Processed != 0 {
		t.Fatalf("run=%+v", state)
	}
	if err = manager.ResumeArtistImageBackfill(context.Background(), runID); err == nil {
		t.Fatal("cancelled run must not resume")
	}
}

// 重复启动与重复点击：活动/暂停任务存在时启动被拒绝；取消后可开新任务。
func TestArtistImageBackfillDuplicateStart(t *testing.T) {
	bt := newBackfillTransport()
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.StartArtistImageBackfill(context.Background()); err == nil {
		t.Fatal("duplicate start must be rejected")
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" {
		t.Fatalf("run=%+v", finished)
	}
	// 完成后缓存有效 → 没有候选，而不是误判为“任务冲突”。
	if _, err = manager.StartArtistImageBackfill(context.Background()); !errors.Is(err, ErrNoArtistImageBackfillCandidates) {
		t.Fatalf("post-completion start: %v", err)
	}
	// 缓存行在但磁盘文件丢失 → 再次成为候选；暂停中的任务阻止新任务。
	if err = manager.store.SaveArtistImage(context.Background(), storage.ArtistImageInput{ArtistID: artist, ByteSize: 10, Source: "lastfm", RemoteURL: imageHostURL, Hash: "h-gone", MIMEType: "image/jpeg", CachePath: filepath.Join(t.TempDir(), "gone.jpg")}); err != nil {
		t.Fatal(err)
	}
	run2, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.PauseArtistImageBackfill(run2); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	if _, err = manager.StartArtistImageBackfill(context.Background()); !errors.Is(err, storage.ErrArtistImageBackfillState) {
		t.Fatalf("paused run must block new start: %v", err)
	}
	if err = manager.CancelArtistImageBackfill(run2); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	run3, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitImageBackfillRun(t, manager.store, run3)
	manager.Wait()
}

// 重启恢复：in_progress 断点在重启后被暂停保留，继续后精确完成一次。
func TestArtistImageBackfillRestartRecovery(t *testing.T) {
	bt := newBackfillTransport()
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	ctx := context.Background()
	runID, err := manager.store.CreateArtistImageBackfillRun(ctx, []storage.ArtistImageBackfillCandidate{{ID: artist, Name: "Backfill Artist"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.store.ClaimArtistImageBackfillItem(ctx, runID); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃：新管理器走 New 的启动恢复。
	restarted := New(ctx, manager.store, manager.logger, filepath.Dir(manager.imageDirectory))
	restarted.client = manager.client
	state, _ := manager.store.DurableArtistImageBackfillRun(ctx, runID)
	if state.Status != "paused" || state.PauseReason != "server_restart" {
		t.Fatalf("run=%+v", state)
	}
	if _, err = restarted.StartArtistImageBackfill(ctx); !errors.Is(err, storage.ErrArtistImageBackfillState) {
		t.Fatalf("paused run must block new start after restart: %v", err)
	}
	if err = restarted.ResumeArtistImageBackfill(ctx, runID); err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	restarted.Wait()
	manager.Wait()
	if finished.Status != "completed" || finished.Cached != 1 || finished.Processed != 1 || bt.imageReqs.Load() != 1 {
		t.Fatalf("run=%+v imageReqs=%d", finished, bt.imageReqs.Load())
	}
}

// 并发自定义头像：下载前复查通过、写入前用户设置了自定义头像，guarded
// 写入必须拒绝，不产生自动缓存行。
func TestArtistImageBackfillConcurrentCustomImageRefused(t *testing.T) {
	bt := newBackfillTransport()
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	manager.SetArtistImageBackfillTestHook(func(artistID int64) {
		if _, err := manager.store.SaveCustomArtistImage(context.Background(), artistID, storage.CustomImageInput{Hash: "h-race", MIMEType: "image/png", FileName: "race.png", ByteSize: 10}); err != nil {
			t.Errorf("seed concurrent custom image: %v", err)
		}
	})
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Cached != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if _, ok := imageBackfillCacheRow(t, manager, artist); ok {
		t.Fatal("guarded save must not write over a concurrent custom image")
	}
}

// 与身份匹配 run 并行：两类任务互不阻塞（共享来源带宽由原语保证），
// 各自完成且结果正确。
func TestArtistImageBackfillConcurrentWithMatchRun(t *testing.T) {
	bt := newBackfillTransport()
	const mbid = "44444444-4444-4444-8444-444444444444"
	bt.mbBody = `{"id":"` + mbid + `","name":"Match Artist","sort-name":"Match Artist","aliases":[],"tags":[],"relations":[]}`
	manager := transportManager(t, roundTripFunc(bt.roundTrip))
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	importOne := func(name string, raw map[string][]string) int64 {
		if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album-" + name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1, Raw: raw}}); err != nil {
			t.Fatal(err)
		}
		artists, _ := store.ArtistsForMatching(ctx)
		for _, a := range artists {
			if a.Name == name {
				return a.ID
			}
		}
		t.Fatalf("artist %s missing", name)
		return 0
	}
	matchArtist := importOne("Match Artist", map[string][]string{"MUSICBRAINZ_ARTISTID": {mbid}})
	_ = matchArtist
	imageArtist := importOne("Image Artist", nil)
	confirmBackfillIdentity(t, store, imageArtist, "lastfm", "lf-img", imageHostURL)
	enableBackfillSource(t, store, "musicbrainz", true)

	matchRun, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	imageRun, err := manager.StartArtistImageBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	matchFinished := waitArtistMatchRun(t, store, matchRun)
	imageFinished := waitImageBackfillRun(t, store, imageRun)
	manager.Wait()
	if matchFinished.Status != "completed" || matchFinished.Matched != 1 {
		t.Fatalf("match run=%+v", matchFinished)
	}
	if imageFinished.Status != "completed" || imageFinished.Cached != 1 {
		t.Fatalf("image run=%+v", imageFinished)
	}
}

// H1 回归：批量自动匹配绑定身份成功后下载缓存头像。
func TestArtistAutoMatchCachesImageAfterBind(t *testing.T) {
	bt := newBackfillTransport()
	const mbid = "55555555-5555-4555-8555-555555555555"
	bt.spotifyThumb = imageHostURL
	bt.mbBody = `{"id":"` + mbid + `","name":"Auto Artist","sort-name":"Auto Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/xyz"}}]}`
	manager, artist := imageBackfillManager(t, bt, "Auto Artist", mbid)
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	runID, err := manager.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitArtistMatchRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Matched != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if _, err = manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); err != nil {
		t.Fatalf("identity not bound: %v", err)
	}
	cachePath, ok := imageBackfillCacheRow(t, manager, artist)
	if !ok {
		t.Fatal("auto match must cache the bound identity image")
	}
	if _, err = os.Stat(cachePath); err != nil {
		t.Fatalf("cached file missing: %v", err)
	}
	if bt.imageReqs.Load() != 1 {
		t.Fatalf("image requests=%d, want 1", bt.imageReqs.Load())
	}
}

// H3 回归：图片 CDN 429 不进入身份匹配 run 的限流等待通道、不消耗等待
// 预算、不暂停任务，身份保留。
func TestArtistAutoMatchImage429DoesNotTouchRun(t *testing.T) {
	bt := newBackfillTransport()
	const mbid = "66666666-6666-4666-8666-666666666666"
	bt.spotifyThumb = imageHostURL
	bt.imageStatus = http.StatusTooManyRequests
	bt.imageBody = ""
	bt.mbBody = `{"id":"` + mbid + `","name":"Auto Artist","sort-name":"Auto Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/xyz"}}]}`
	manager, artist := imageBackfillManager(t, bt, "Auto Artist", mbid)
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	runID, err := manager.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitArtistMatchRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Matched != 1 || finished.Processed != 1 {
		t.Fatalf("run=%+v", finished)
	}
	durable, err := manager.store.DurableArtistRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if durable.WaitTotalMS != 0 || durable.WaitingUntil != "" || durable.PauseReason != "" {
		t.Fatalf("image 429 leaked into the identity run wait channel: %+v", durable)
	}
	if _, err = manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); err != nil {
		t.Fatalf("identity must survive image 429: %v", err)
	}
	if _, ok := imageBackfillCacheRow(t, manager, artist); ok {
		t.Fatal("429 image must not produce a cache row")
	}
}

// H3 回归：图片下载普通失败同样不影响匹配结果与身份。
func TestArtistAutoMatchImageFailureKeepsIdentity(t *testing.T) {
	bt := newBackfillTransport()
	const mbid = "77777777-7777-4777-8777-777777777777"
	bt.spotifyThumb = imageHostURL
	bt.imageStatus = http.StatusInternalServerError
	bt.imageBody = ""
	bt.mbBody = `{"id":"` + mbid + `","name":"Auto Artist","sort-name":"Auto Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/xyz"}}]}`
	manager, artist := imageBackfillManager(t, bt, "Auto Artist", mbid)
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	runID, err := manager.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitArtistMatchRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Matched != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if _, err = manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); err != nil {
		t.Fatalf("identity must survive image failure: %v", err)
	}
}

// backfillLogCapture 是测试用 slog.Handler：收集级别、消息与属性文本，
// 用于断言“没有 ERROR 日志”与“api_key 不泄漏”。
type backfillLogCapture struct {
	mu      sync.Mutex
	entries []string
	levels  []slog.Level
}

func (h *backfillLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *backfillLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	text := r.Message
	r.Attrs(func(a slog.Attr) bool {
		text += " " + a.Key + "=" + a.Value.String()
		return true
	})
	h.entries = append(h.entries, text)
	h.levels = append(h.levels, r.Level)
	return nil
}
func (h *backfillLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *backfillLogCapture) WithGroup(string) slog.Handler      { return h }

func (h *backfillLogCapture) errorCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, level := range h.levels {
		if level >= slog.LevelError {
			count++
		}
	}
	return count
}

func (h *backfillLogCapture) contains(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, entry := range h.entries {
		if strings.Contains(entry, sub) {
			return true
		}
	}
	return false
}

// P1-1 场景 1：网络查询窗口内身份被并发解除（reset），guarded 资料刷新必须
// 拒绝，身份不得复活，item 记 skipped，不下载图片。
func TestArtistImageBackfillIdentityResetMidQuery(t *testing.T) {
	const mbid = "mbid-reset-1"
	bt := newBackfillTransport()
	bt.spotifyThumb = imageHostURL
	bt.mbBody = `{"id":"` + mbid + `","name":"Backfill Artist","sort-name":"Backfill Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/xyz"}}]}`
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "musicbrainz", mbid, "")
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	base := manager.client.Transport
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "musicbrainz.org") {
			if err := manager.store.ResetArtistIdentity(context.Background(), artist, "musicbrainz", mbid); err != nil {
				t.Errorf("reset identity mid query: %v", err)
			}
		}
		return base.RoundTrip(r)
	})
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Cached != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if _, err = manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reset identity must not be resurrected, err=%v", err)
	}
	if bt.imageReqs.Load() != 0 {
		t.Fatalf("image requests=%d, want 0", bt.imageReqs.Load())
	}
}

// P1-1 场景 2：网络查询窗口内身份被并发重新确认为另一个 external_id，
// guarded 刷新必须拒绝，新身份不得被旧响应覆盖回来。
func TestArtistImageBackfillIdentityReconfirmMidQuery(t *testing.T) {
	const mbid = "mbid-old-1"
	bt := newBackfillTransport()
	bt.spotifyThumb = imageHostURL
	bt.mbBody = `{"id":"` + mbid + `","name":"Backfill Artist","sort-name":"Backfill Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/xyz"}}]}`
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "musicbrainz", mbid, "")
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	base := manager.client.Transport
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "musicbrainz.org") {
			// 模拟并发重新确认：profile 行的 external_id 变为新身份。
			if err := manager.store.UpsertExternalArtistProfile(context.Background(), artist, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-NEW", DisplayName: "Reconfirmed"}); err != nil {
				t.Errorf("reconfirm mid query: %v", err)
			}
		}
		return base.RoundTrip(r)
	})
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Cached != 0 {
		t.Fatalf("run=%+v", finished)
	}
	externalID, err := manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz")
	if err != nil || externalID != "mbid-NEW" {
		t.Fatalf("reconfirmed identity must survive: %q %v", externalID, err)
	}
	// 旧响应的图片地址不得写到新身份的资料行上。
	if _, _, remoteURL, err := manager.store.ArtistImageProfileSource(context.Background(), artist); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale response must not land on new identity: %q %v", remoteURL, err)
	}
}

// P1-1 场景 3：网络查询窗口内任务被暂停，guarded 刷新因 checkpoint 失效
// 拒绝写入；run 保持人工暂停，身份与资料不变。
func TestArtistImageBackfillPauseMidQuery(t *testing.T) {
	const mbid = "mbid-pause-1"
	bt := newBackfillTransport()
	bt.spotifyThumb = imageHostURL
	bt.mbBody = `{"id":"` + mbid + `","name":"Backfill Artist","sort-name":"Backfill Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/xyz"}}]}`
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "musicbrainz", mbid, "")
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	base := manager.client.Transport
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "musicbrainz.org") {
			run, runErr := manager.store.UnfinishedArtistImageBackfillRun(context.Background())
			if runErr == nil {
				if pauseErr := manager.PauseArtistImageBackfill(run.ID); pauseErr != nil {
					t.Errorf("pause mid query: %v", pauseErr)
				}
			}
		}
		return base.RoundTrip(r)
	})
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, err := manager.store.DurableArtistImageBackfillRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "paused" || state.PauseReason != "manual" {
		t.Fatalf("run=%+v", state)
	}
	externalID, err := manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz")
	if err != nil || externalID != mbid {
		t.Fatalf("identity changed after pause: %q %v", externalID, err)
	}
	if _, _, remoteURL, err := manager.store.ArtistImageProfileSource(context.Background(), artist); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("profile written after pause: %q %v", remoteURL, err)
	}
	if _, ok := imageBackfillCacheRow(t, manager, artist); ok {
		t.Fatal("cache written after pause")
	}
}

// P2-1：完成项时撞上状态冲突（人工暂停抢先落地）是预期竞态，worker 安静
// 退出——不记 ERROR、不覆盖暂停原因、不产生缓存行；恢复后断点重算。
func TestArtistImageBackfillCompleteConflictQuietExit(t *testing.T) {
	bt := newBackfillTransport()
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-1", imageHostURL)
	logs := &backfillLogCapture{}
	manager.logger = slog.New(logs)
	var hookOnce sync.Once
	manager.SetArtistImageBackfillTestHook(func(artistID int64) {
		hookOnce.Do(func() {
			run, err := manager.store.UnfinishedArtistImageBackfillRun(context.Background())
			if err != nil {
				t.Errorf("lookup run: %v", err)
				return
			}
			// 只改库不取消 worker ctx：下载继续，guarded 写入与完成都撞冲突。
			if err := manager.store.TransitionArtistImageBackfillRun(context.Background(), run.ID, "pause", "manual"); err != nil {
				t.Errorf("pause mid item: %v", err)
			}
		})
	})
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, err := manager.store.DurableArtistImageBackfillRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "paused" || state.PauseReason != "manual" {
		t.Fatalf("pause reason must stay manual: %+v", state)
	}
	if logs.errorCount() != 0 {
		t.Fatalf("state conflict must exit quietly, logs=%v", logs.entries)
	}
	if _, ok := imageBackfillCacheRow(t, manager, artist); ok {
		t.Fatal("cache write must have been refused")
	}
	// 恢复后断点保留：同一 artist 重算并成功缓存。
	if err = manager.ResumeArtistImageBackfill(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Cached != 1 {
		t.Fatalf("run=%+v", finished)
	}
}

// P2-5：正常关闭（baseCtx 取消）以 "shutdown" 暂停且需手动继续；重启恢复
// 不得把它误标为 "server_restart"。
func TestArtistImageBackfillGracefulShutdownReason(t *testing.T) {
	bt := newBackfillTransport()
	bt.imageStatus = http.StatusTooManyRequests
	bt.imageBody = ""
	baseCtx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(filepath.Join(t.TempDir(), "shutdown.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	manager := New(baseCtx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.client = &http.Client{Transport: roundTripFunc(bt.roundTrip)}
	if err = store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Backfill Artist"}, AlbumArtists: []string{"Backfill Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	confirmBackfillIdentity(t, store, artists[0].ID, "lastfm", "mbid-1", imageHostURL)
	entered := make(chan struct{})
	var once sync.Once
	manager.sleep = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return ctx.Err()
	}
	runID, err := manager.StartArtistImageBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never entered rate-limit wait")
	}
	cancel()
	manager.Wait()
	state, err := store.DurableArtistImageBackfillRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "paused" || state.PauseReason != "shutdown" {
		t.Fatalf("graceful shutdown must pause as shutdown: %+v", state)
	}
	// 重启恢复不得改写关闭原因（server_restart 只留给崩溃/掉电的中断）。
	restarted := New(context.Background(), store, manager.logger, t.TempDir())
	defer restarted.Wait()
	state, _ = store.DurableArtistImageBackfillRun(ctx, runID)
	if state.PauseReason != "shutdown" {
		t.Fatalf("restart recovery must preserve shutdown reason: %+v", state)
	}
}

// P2-4：RedactSourceError 剥离 *url.Error 中含 api_key 的请求 URL。
func TestRedactSourceError(t *testing.T) {
	inner := errors.New("connection reset")
	urlErr := &url.Error{Op: "Get", URL: "https://ws.audioscrobbler.com/2.0/?api_key=SECRET", Err: inner}
	if got := RedactSourceError(urlErr); got != inner {
		t.Fatalf("redacted=%v", got)
	}
	if got := RedactSourceError(fmt.Errorf("lastfm: %w", urlErr)); got != inner {
		t.Fatalf("wrapped redacted=%v", got)
	}
	plain := errors.New("plain")
	if got := RedactSourceError(plain); got != plain {
		t.Fatalf("plain=%v", got)
	}
	if RedactSourceError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

// P2-4 集成：Last.fm 网络层错误的日志不得含 api_key/密钥值。
func TestArtistImageBackfillLastFMNetworkErrorLogRedacted(t *testing.T) {
	bt := newBackfillTransport()
	manager, artist := imageBackfillManager(t, bt, "Backfill Artist", "")
	confirmBackfillIdentity(t, manager.store, artist, "lastfm", "mbid-lf-1", "")
	enableBackfillSource(t, manager.store, "lastfm", true)
	logs := &backfillLogCapture{}
	manager.logger = slog.New(logs)
	base := manager.client.Transport
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "audioscrobbler.com") {
			return nil, errors.New("connection reset by peer")
		}
		return base.RoundTrip(r)
	})
	runID, err := manager.StartArtistImageBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitImageBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.NoURL != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if !logs.contains("lastfm lookup for image backfill") {
		t.Fatalf("expected redacted warn log, logs=%v", logs.entries)
	}
	for _, entry := range logs.entries {
		if strings.Contains(entry, "mock-key") || strings.Contains(entry, "api_key") {
			t.Fatalf("api key leaked into logs: %q", entry)
		}
	}
}
