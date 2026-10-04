package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// bioTransport 按 host 分发假应答：musicbrainz.org / www.wikidata.org /
// *.wikipedia.org / ws.audioscrobbler.com，并统计各类请求数；onRequest 可在
// 特定请求前注入并发操作（解除/改认身份、暂停任务），绝不触碰真实网络。
type bioTransport struct {
	mbBody       string
	wikidataBody string
	wikiExtract  string
	wikiStatus   atomic.Int32
	lastfmBody   string
	lastfmStatus atomic.Int32
	mbReqs       atomic.Int32
	wikidataReqs atomic.Int32
	wikiReqs     atomic.Int32
	lastfmReqs   atomic.Int32
	onRequest    func(r *http.Request)
}

func newBioTransport() *bioTransport {
	bt := &bioTransport{}
	bt.wikiStatus.Store(http.StatusOK)
	bt.lastfmStatus.Store(http.StatusOK)
	return bt
}

func (bt *bioTransport) roundTrip(r *http.Request) (*http.Response, error) {
	if bt.onRequest != nil {
		bt.onRequest(r)
	}
	host := r.URL.Host
	switch {
	case strings.Contains(host, "musicbrainz.org"):
		bt.mbReqs.Add(1)
		return cannedResponse(http.StatusOK, http.Header{}, bt.mbBody), nil
	case strings.Contains(host, "wikidata.org"):
		bt.wikidataReqs.Add(1)
		return cannedResponse(http.StatusOK, http.Header{}, bt.wikidataBody), nil
	case strings.Contains(host, "wikipedia.org"):
		bt.wikiReqs.Add(1)
		status := int(bt.wikiStatus.Load())
		header := http.Header{}
		if status == http.StatusTooManyRequests {
			header.Set("Retry-After", "60")
		}
		body := fmt.Sprintf(`{"extract":%q,"content_urls":{"desktop":{"page":"https://example.test/page"}}}`, bt.wikiExtract)
		return cannedResponse(status, header, body), nil
	case strings.Contains(host, "audioscrobbler.com"):
		bt.lastfmReqs.Add(1)
		return cannedResponse(int(bt.lastfmStatus.Load()), http.Header{}, bt.lastfmBody), nil
	default:
		return nil, fmt.Errorf("unexpected host %s", host)
	}
}

const bioMBBody = `{"id":"mbid-bio-1","name":"Bio Artist","sort-name":"Bio Artist","aliases":[],"tags":[],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q1"}}]}`

const bioWikidataBody = `{"entities":{"Q1":{"sitelinks":{"zhwiki":{"title":"歌手"},"jawiki":{"title":"かしゅ"},"enwiki":{"title":"Singer"}}}}}`

const bioLastFMBody = `{"artist":{"name":"Bio Artist","mbid":"mbid-bio-1","url":"https://www.last.fm/music/bio","bio":{"content":"LastFM 简介 <a href=\"https://www.last.fm/music/bio/+wiki\">Read more</a>"},"image":[],"tags":{"tag":[]}}}`

// importMetadata 构造导入一位歌手（专辑+演唱关系）的最小元数据。
func importMetadata(artist string) metadata.AudioMetadata {
	return metadata.AudioMetadata{Title: "Song", Album: "Album-" + artist, Artists: []string{artist}, AlbumArtists: []string{artist}, DiscNumber: 1, TrackNumber: 1}
}

// bioAutoMatchManager 与 imageBackfillManager 同构，但使用 bioTransport：
// 导入一位带 tagged MBID 的歌手，供自动匹配 run 测试。
func bioAutoMatchManager(t *testing.T, bt *bioTransport, artist, taggedMBID string) (*Manager, int64) {
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
	meta := importMetadata(artist)
	meta.Raw = raw
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: artist + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	return manager, artists[0].ID
}

// bioBackfillManager 构建 HTTP 全部被拦截的管理器并导入一位歌手。
func bioBackfillManager(t *testing.T, bt *bioTransport, artist string) (*Manager, int64) {
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
	importTrack := func(name string) {
		t.Helper()
		if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: importMetadata(name)}); err != nil {
			t.Fatal(err)
		}
	}
	importTrack(artist)
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range artists {
		if a.Name == artist {
			return manager, a.ID
		}
	}
	t.Fatalf("artist %q not found", artist)
	return nil, 0
}

func confirmBioIdentity(t *testing.T, store *storage.Store, artistID int64, mbid string) {
	t.Helper()
	if err := store.UpsertExternalArtistProfile(context.Background(), artistID, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: mbid, DisplayName: "Bio Artist"}); err != nil {
		t.Fatal(err)
	}
}

func waitBioBackfillRun(t *testing.T, store *storage.Store, runID int64) storage.ArtistBiographyBackfillRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.DurableArtistBiographyBackfillRun(context.Background(), runID)
		if err == nil && run.Status != "running" && run.Status != "queued" {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("artist biography backfill run did not finish")
	return storage.ArtistBiographyBackfillRun{}
}

func bioRowCount(t *testing.T, manager *Manager, artistID int64) int {
	t.Helper()
	db := writableSafetyDB(t, manager)
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM artist_biographies WHERE artist_id=?`, artistID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// 主流程：已确认 MBID 的缺简介歌手，一次补全写入 Wikipedia×3 语言与
// Last.fm×3 语言版本行，outcome=filled。
func TestArtistBiographyBackfillFillsSources(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "维基百科简介"
	bt.lastfmBody = bioLastFMBody
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	enableBackfillSource(t, manager.store, "lastfm", true)
	runID, err := manager.StartArtistBiographyBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Filled != 1 || finished.Failed != 0 || finished.Total != 1 {
		t.Fatalf("run=%+v", finished)
	}
	// 3 语言 × (wikipedia + lastfm) = 6 行。
	if got := bioRowCount(t, manager, artist); got != 6 {
		t.Fatalf("bio rows=%d, want 6", got)
	}
	if bt.mbReqs.Load() != 1 || bt.wikidataReqs.Load() != 1 || bt.wikiReqs.Load() != 3 || bt.lastfmReqs.Load() != 3 {
		t.Fatalf("mb=%d wikidata=%d wiki=%d lastfm=%d", bt.mbReqs.Load(), bt.wikidataReqs.Load(), bt.wikiReqs.Load(), bt.lastfmReqs.Load())
	}
	// 幂等：补齐后再启动没有候选。
	if _, err = manager.StartArtistBiographyBackfill(context.Background()); !errors.Is(err, ErrNoArtistBiographyBackfillCandidates) {
		t.Fatalf("second start must have no candidates: %v", err)
	}
}

// T2：无已确认 MBID（含合并继承）→ skipped_no_mbid 计数，零网络请求，
// 绝不匹配身份。
func TestArtistBiographyBackfillNoMBIDSkipped(t *testing.T) {
	bt := newBioTransport()
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	runID, err := manager.StartArtistBiographyBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Filled != 0 || finished.Failed != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.mbReqs.Load()+bt.wikidataReqs.Load()+bt.wikiReqs.Load()+bt.lastfmReqs.Load() != 0 {
		t.Fatal("no-MBID item must not touch the network")
	}
	var outcome string
	db := writableSafetyDB(t, manager)
	if err = db.QueryRowContext(context.Background(), `SELECT outcome FROM artist_biography_backfill_items WHERE run_id=? AND artist_id=?`, runID, artist).Scan(&outcome); err != nil || outcome != "skipped_no_mbid" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	if _, err = manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("backfill must never bind identity: %v", err)
	}
}

// T6：fresh missing 行在 cache_days 窗口内复查 → skipped_fresh，不反复打源。
func TestArtistBiographyBackfillFreshMissingSkipped(t *testing.T) {
	bt := newBioTransport()
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	ctx := context.Background()
	// Last.fm 默认禁用；Wikipedia 全语言 fresh missing。
	for _, language := range []string{"zh", "ja", "en"} {
		if err := manager.store.UpsertArtistBiography(ctx, artist, storage.ArtistBiography{Source: "wikipedia", Language: language, Status: "missing"}); err != nil {
			t.Fatal(err)
		}
	}
	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Filled != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.mbReqs.Load()+bt.wikidataReqs.Load()+bt.wikiReqs.Load()+bt.lastfmReqs.Load() != 0 {
		t.Fatal("fresh missing must not re-hit sources within cache_days")
	}
	db := writableSafetyDB(t, manager)
	var outcome string
	if err = db.QueryRowContext(ctx, `SELECT outcome FROM artist_biography_backfill_items WHERE run_id=?`, runID).Scan(&outcome); err != nil || outcome != "skipped_fresh" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
}

// T8：所有简介来源禁用/无密钥 → skipped_source_disabled（非 failed），零网络。
func TestArtistBiographyBackfillSourcesDisabled(t *testing.T) {
	bt := newBioTransport()
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	ctx := context.Background()
	settings, err := manager.store.BiographySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.WikipediaEnabled = false
	if err = manager.store.SaveBiographySettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	// Last.fm 保持默认禁用。
	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Failed != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.mbReqs.Load()+bt.wikidataReqs.Load()+bt.wikiReqs.Load()+bt.lastfmReqs.Load() != 0 {
		t.Fatal("disabled sources must not be queried")
	}
	db := writableSafetyDB(t, manager)
	var outcome string
	if err = db.QueryRowContext(ctx, `SELECT outcome FROM artist_biography_backfill_items WHERE run_id=?`, runID).Scan(&outcome); err != nil || outcome != "skipped_source_disabled" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
}

// T11（分叉 C1）：身份确认在被合并来源行上 → 沿合并链读取 MBID，简介写
// 规范（未合并）歌手行，outcome=filled。
func TestArtistBiographyBackfillMergedIdentitySource(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "合并继承简介"
	manager, canonical := bioBackfillManager(t, bt, "Bio Artist")
	store := manager.store
	ctx := context.Background()
	// 第二位歌手持有已确认身份，随后并入规范歌手。
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Source/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: importMetadata("Source Artist")}); err != nil {
		t.Fatal(err)
	}
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var source int64
	for _, a := range artists {
		if a.Name == "Source Artist" {
			source = a.ID
		}
	}
	if source == 0 {
		t.Fatal("source artist missing")
	}
	confirmBioIdentity(t, store, source, "mbid-bio-1")
	if _, err = store.MergeArtists(ctx, source, canonical); err != nil {
		t.Fatal(err)
	}
	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Filled != 1 {
		t.Fatalf("run=%+v", finished)
	}
	// 简介写在规范歌手行上（3 语言 wikipedia）。
	if got := bioRowCount(t, manager, canonical); got != 3 {
		t.Fatalf("canonical bio rows=%d, want 3", got)
	}
	if got := bioRowCount(t, manager, source); got != 0 {
		t.Fatalf("merged source must not receive bio rows, got %d", got)
	}
	// item error 字段注明身份属主（分叉 C1 审计线索）。
	db := writableSafetyDB(t, manager)
	var outcome, note string
	if err = db.QueryRowContext(ctx, `SELECT outcome,error FROM artist_biography_backfill_items WHERE run_id=?`, runID).Scan(&outcome, &note); err != nil {
		t.Fatal(err)
	}
	if outcome != "filled" || !strings.Contains(note, "身份来自合并来源 #") {
		t.Fatalf("outcome=%q note=%q", outcome, note)
	}
}

// T3 场景 1：网络窗口内身份被并发解除（reset）→ guarded 拒绝，身份不复活，
// item 记 skipped_state，无简介行残留。
func TestArtistBiographyBackfillIdentityResetMidFetch(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "迟到简介"
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	var once sync.Once
	bt.onRequest = func(r *http.Request) {
		if strings.Contains(r.URL.Host, "musicbrainz.org") {
			once.Do(func() {
				if err := manager.store.ResetArtistIdentity(context.Background(), artist, "musicbrainz", "mbid-bio-1"); err != nil {
					t.Errorf("reset identity mid fetch: %v", err)
				}
			})
		}
	}
	runID, err := manager.StartArtistBiographyBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Filled != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if got := bioRowCount(t, manager, artist); got != 0 {
		t.Fatalf("late biography must not be written after reset, rows=%d", got)
	}
	if _, err = manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reset identity must not be resurrected: %v", err)
	}
}

// T3 场景 2：网络窗口内身份被并发改认为另一个 MBID → guarded 拒绝，新身份
// 不被旧响应覆盖。
func TestArtistBiographyBackfillRebindMidFetch(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "旧身份简介"
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	var once sync.Once
	bt.onRequest = func(r *http.Request) {
		if strings.Contains(r.URL.Host, "musicbrainz.org") {
			once.Do(func() {
				if err := manager.store.UpsertExternalArtistProfile(context.Background(), artist, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-NEW", DisplayName: "Reconfirmed"}); err != nil {
					t.Errorf("rebind mid fetch: %v", err)
				}
			})
		}
	}
	runID, err := manager.StartArtistBiographyBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Filled != 0 {
		t.Fatalf("run=%+v", finished)
	}
	externalID, err := manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz")
	if err != nil || externalID != "mbid-NEW" {
		t.Fatalf("rebound identity must survive: %q %v", externalID, err)
	}
	if got := bioRowCount(t, manager, artist); got != 0 {
		t.Fatalf("stale biography must not land on the new identity, rows=%d", got)
	}
}

// T4：网络窗口前任务被暂停（DB 先落地）→ guarded 写入与完成都撞冲突，
// worker 安静退出（无 ERROR），run 保持人工暂停；恢复后断点重算并补全。
func TestArtistBiographyBackfillPauseMidItemQuietExit(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "暂停窗口简介"
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	logs := &backfillLogCapture{}
	manager.logger = slog.New(logs)
	var once sync.Once
	manager.SetArtistBiographyBackfillTestHook(func(artistID int64) {
		once.Do(func() {
			run, err := manager.store.UnfinishedArtistBiographyBackfillRun(context.Background())
			if err != nil {
				t.Errorf("lookup run: %v", err)
				return
			}
			// 只改库不取消 worker ctx：抓取继续，guarded 写入与完成都撞冲突。
			if err = manager.store.TransitionArtistBiographyBackfillRun(context.Background(), run.ID, "pause", "manual"); err != nil {
				t.Errorf("pause mid item: %v", err)
			}
		})
	})
	runID, err := manager.StartArtistBiographyBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, err := manager.store.DurableArtistBiographyBackfillRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "paused" || state.PauseReason != "manual" {
		t.Fatalf("pause reason must stay manual: %+v", state)
	}
	if logs.errorCount() != 0 {
		t.Fatalf("state conflict must exit quietly, logs=%v", logs.entries)
	}
	if got := bioRowCount(t, manager, artist); got != 0 {
		t.Fatalf("paused run must not write biographies, rows=%d", got)
	}
	// 恢复后断点保留：同一歌手重算并成功补全。
	if err = manager.ResumeArtistBiographyBackfill(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Filled != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if got := bioRowCount(t, manager, artist); got != 3 {
		t.Fatalf("bio rows=%d, want 3", got)
	}
}

// T5：Wikipedia 429 固定阶梯 + 共享冷却取较长（60s>30s）；连续 3 次即
// 暂停并自动恢复，3 轮用尽后 rate_limit_exhausted；人工继续可再试。
func TestArtistBiographyBackfillRateLimitAutoResumeExhausted(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiStatus.Store(http.StatusTooManyRequests)
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	artistFakeClock(manager)
	runID, err := manager.StartArtistBiographyBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, err := manager.store.DurableArtistBiographyBackfillRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// 4 轮 × 3 次限流响应 = 12 次 Wikipedia 请求；3 轮自动恢复用尽后停在终态。
	if state.Status != "paused" || state.PauseReason != "rate_limit_exhausted" || state.AutoResumeCount != 3 || bt.wikiReqs.Load() != 12 {
		t.Fatalf("run=%+v wikiReqs=%d", state, bt.wikiReqs.Load())
	}
	if state.WaitingUntil == "" || state.WaitTotalMS == 0 {
		t.Fatalf("waiting deadline/total must persist: %+v", state)
	}
	if storage.ArtistBiographyBackfillAutoResumeEligible(state) {
		t.Fatal("exhausted run must not auto resume again")
	}
	// 断点保留：item 仍未完成，人工继续后接着重试（3 次后再停）。
	if err = manager.ResumeArtistBiographyBackfill(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	state, _ = manager.store.DurableArtistBiographyBackfillRun(context.Background(), runID)
	if state.Status != "paused" || state.PauseReason != "rate_limit_exhausted" || bt.wikiReqs.Load() != 15 || state.Processed != 0 {
		t.Fatalf("run=%+v wikiReqs=%d", state, bt.wikiReqs.Load())
	}
}

// T10：限流暂停的任务在重启后由 ScanAutoResumeRuns 拾起，到点自动继续并
// 完成补全（waiting_until 已过时立即触发）。
func TestArtistBiographyBackfillRestartAutoResume(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "重启续跑简介"
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	ctx := context.Background()
	runID, err := manager.store.CreateArtistBiographyBackfillRun(ctx, []storage.ArtistBiographyBackfillCandidate{{ID: artist, Name: "Bio Artist"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := manager.store.ClaimArtistBiographyBackfillItem(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	cp := storage.ArtistBiographyBackfillCheckpoint{RunID: runID, ItemID: item.ID, ClaimToken: item.ClaimToken}
	deadline := time.Now().Add(-time.Minute).UTC()
	for i := 0; i < 3; i++ {
		if err = manager.store.RecordArtistBiographyBackfillWait(ctx, cp, "wikipedia", deadline, 0, true); err != nil && i < 2 {
			t.Fatal(err)
		}
	}
	manager.Wait()
	// 重启：running→paused 恢复不触碰已暂停任务；ScanAutoResumeRuns 拾起。
	restarted := New(context.Background(), manager.store, manager.logger, t.TempDir())
	defer restarted.Wait()
	restarted.client = manager.client
	run, err := manager.store.DurableArtistBiographyBackfillRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "paused" || run.PauseReason != "rate_limit_count" {
		t.Fatalf("run=%+v", run)
	}
	restarted.ScanAutoResumeRuns()
	// 等待自动恢复定时器触发并跑完（到点已过的截止时间立即触发）。
	poll := time.Now().Add(10 * time.Second)
	for time.Now().Before(poll) {
		finished, e := manager.store.DurableArtistBiographyBackfillRun(ctx, runID)
		if e == nil && (finished.Status == "completed" || finished.Status == "failed" || finished.Status == "cancelled") {
			if finished.Status != "completed" || finished.Filled != 1 || finished.AutoResumeCount != 1 {
				t.Fatalf("run=%+v", finished)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("auto-resumed biography backfill run did not complete")
}

// T9：Last.fm 网络错误的日志与 item 错误文本严格脱敏（不含 api_key）。
func TestArtistBiographyBackfillLastFMErrorRedacted(t *testing.T) {
	bt := newBioTransport()
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	enableBackfillSource(t, manager.store, "lastfm", true)
	// 只留 Last.fm 来源，让失败走到 item 终态。
	settings, err := manager.store.BiographySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.WikipediaEnabled = false
	if err = manager.store.SaveBiographySettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	logs := &backfillLogCapture{}
	manager.logger = slog.New(logs)
	base := manager.client.Transport
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "audioscrobbler.com") {
			return nil, errors.New("connection reset by peer")
		}
		return base.RoundTrip(r)
	})
	runID, err := manager.StartArtistBiographyBackfill(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Failed != 1 {
		t.Fatalf("run=%+v", finished)
	}
	for _, entry := range logs.entries {
		if strings.Contains(entry, "mock-key") || strings.Contains(entry, "api_key") {
			t.Fatalf("api key leaked into logs: %q", entry)
		}
	}
	db := writableSafetyDB(t, manager)
	var itemErr string
	if err = db.QueryRowContext(context.Background(), `SELECT error FROM artist_biography_backfill_items WHERE run_id=?`, runID).Scan(&itemErr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(itemErr, "mock-key") || strings.Contains(itemErr, "api_key") {
		t.Fatalf("api key leaked into item error: %q", itemErr)
	}
}

// T1（分叉 1A）：批量自动匹配绑定身份成功后 best-effort 补简介，版本行
// 落库；简介失败不影响匹配。
func TestArtistAutoMatchFillsBiographyAfterBind(t *testing.T) {
	bt := newBioTransport()
	const mbid = "88888888-8888-4888-8888-888888888888"
	bt.mbBody = `{"id":"` + mbid + `","name":"Auto Bio Artist","sort-name":"Auto Bio Artist","aliases":[],"tags":[],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q1"}}]}`
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "自动匹配后的简介"
	manager, artist := bioAutoMatchManager(t, bt, "Auto Bio Artist", mbid)
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
	if got := bioRowCount(t, manager, artist); got != 3 {
		t.Fatalf("bio rows=%d, want 3 (wikipedia zh/ja/en)", got)
	}
	if bt.wikiReqs.Load() != 3 {
		t.Fatalf("wiki requests=%d, want 3", bt.wikiReqs.Load())
	}
}

// 1A/H3：简介来源 429 不进入身份匹配 run 的限流等待通道、不消耗等待预算、
// 不暂停任务，身份保留。
func TestArtistAutoMatchBiography429DoesNotTouchRun(t *testing.T) {
	bt := newBioTransport()
	const mbid = "99999999-9999-4999-8999-999999999999"
	bt.mbBody = `{"id":"` + mbid + `","name":"Auto Bio Artist","sort-name":"Auto Bio Artist","aliases":[],"tags":[],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q1"}}]}`
	bt.wikidataBody = bioWikidataBody
	bt.wikiStatus.Store(http.StatusTooManyRequests)
	manager, artist := bioAutoMatchManager(t, bt, "Auto Bio Artist", mbid)
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
		t.Fatalf("biography 429 leaked into the identity run wait channel: %+v", durable)
	}
	if _, err = manager.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); err != nil {
		t.Fatalf("identity must survive biography 429: %v", err)
	}
	if got := bioRowCount(t, manager, artist); got != 0 {
		t.Fatalf("429 biography must not produce rows, got %d", got)
	}
}

// 1A 冷却短路：相关来源在共享冷却期时自动匹配后的简介补全直接跳过，
// 不打任何简介请求；匹配结果不受影响。
func TestArtistAutoMatchBiographyCooldownShortCircuit(t *testing.T) {
	bt := newBioTransport()
	const mbid = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	bt.mbBody = `{"id":"` + mbid + `","name":"Auto Bio Artist","sort-name":"Auto Bio Artist","aliases":[],"tags":[],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q1"}}]}`
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "不应被抓取"
	manager, artist := bioAutoMatchManager(t, bt, "Auto Bio Artist", mbid)
	enableBackfillSource(t, manager.store, "musicbrainz", true)
	manager.NoteRateLimited("wikipedia", 5*time.Minute)
	runID, err := manager.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitArtistMatchRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Matched != 1 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.wikiReqs.Load() != 0 {
		t.Fatalf("cooldown must short-circuit biography refresh, wiki=%d", bt.wikiReqs.Load())
	}
	if got := bioRowCount(t, manager, artist); got != 0 {
		t.Fatalf("cooldown must skip biography refresh, rows=%d", got)
	}
}

// bioArtistID 按名字查艺术家 ID。
func bioArtistID(t *testing.T, store *storage.Store, name string) int64 {
	t.Helper()
	artists, err := store.ArtistsForMatching(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range artists {
		if a.Name == name {
			return a.ID
		}
	}
	t.Fatalf("artist %q not found", name)
	return 0
}

// bioImportArtist 导入一位歌手（专辑+演唱关系）并返回其 ID。
func bioImportArtist(t *testing.T, store *storage.Store, name string) int64 {
	t.Helper()
	ctx := context.Background()
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: importMetadata(name)}); err != nil {
		t.Fatal(err)
	}
	return bioArtistID(t, store, name)
}

// P1-1：自动匹配绑定成功后，纯幕后（仅作曲）人员绝不触发任何简介请求、
// 不落简介行；歌手（同一 run 内）照常补全。绑定走已保存的双来源快照
// （同一 MBID 互证 score 98），匹配本身零网络；简介补全走假源。
func TestArtistAutoMatchBiographySkipsCreditOnlyArtist(t *testing.T) {
	const pfMBID = "11111111-1111-4111-8111-111111111111"
	const bkMBID = "22222222-2222-4222-8222-222222222222"
	var mbReqs, wikidataReqs, wikiReqs, lastfmReqs atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		host := r.URL.Host
		switch {
		case strings.Contains(host, "musicbrainz.org"):
			mbReqs.Add(1)
			return cannedResponse(http.StatusOK, http.Header{}, `{"id":"`+pfMBID+`","name":"Stage Artist","sort-name":"Stage Artist","aliases":[],"tags":[],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q1"}}]}`), nil
		case strings.Contains(host, "wikidata.org"):
			wikidataReqs.Add(1)
			return cannedResponse(http.StatusOK, http.Header{}, bioWikidataBody), nil
		case strings.Contains(host, "wikipedia.org"):
			wikiReqs.Add(1)
			return cannedResponse(http.StatusOK, http.Header{}, `{"extract":"舞台歌手简介","content_urls":{"desktop":{"page":""}}}`), nil
		case strings.Contains(host, "audioscrobbler.com"):
			lastfmReqs.Add(1)
			return cannedResponse(http.StatusOK, http.Header{}, `{"artist":{"name":"Stage Artist","mbid":"`+pfMBID+`","url":"https://www.last.fm/music/stage","bio":{"content":"LastFM 舞台简介"},"image":[],"tags":{"tag":[]}}}`), nil
		default:
			return nil, fmt.Errorf("unexpected host %s", host)
		}
	})
	manager := transportManager(t, transport)
	artistFakeClock(manager)
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	// 一首歌：Stage Artist 演唱（歌手），Backstage Person 仅作曲（纯幕后）。
	meta := importMetadata("Stage Artist")
	meta.Composer = "Backstage Person"
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Stage Artist/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	performer := bioArtistID(t, store, "Stage Artist")
	backstage := bioArtistID(t, store, "Backstage Person")
	enableBackfillSource(t, store, "musicbrainz", true)
	enableBackfillSource(t, store, "lastfm", true)
	pfInput, err := store.ArtistForMatching(ctx, performer)
	if err != nil {
		t.Fatal(err)
	}
	bkInput, err := store.ArtistForMatching(ctx, backstage)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: pfInput}, {Artist: bkInput}})
	if err != nil {
		t.Fatal(err)
	}
	// 为两个 item 预存双来源一致快照（同一 MBID 互证 → score 98 → 自动绑定）。
	for _, tc := range []struct {
		input storage.ArtistMatchInput
		mbid  string
	}{{pfInput, pfMBID}, {bkInput, bkMBID}} {
		item, e := store.ClaimArtistRunItem(ctx, run)
		if e != nil {
			t.Fatal(e)
		}
		cp := storage.ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
		// 快照键必须与 matchArtist 运行时重算的输入一致（绑定前 LastFMQueryMBID
		// 为空）：走 ArtistMatchQueryContext 正式捕获。
		input, e := store.ArtistMatchQueryContext(ctx, tc.input)
		if e != nil {
			t.Fatal(e)
		}
		for _, source := range []string{"musicbrainz", "lastfm"} {
			setting, e := store.MetadataSourceSetting(ctx, source)
			if e != nil {
				t.Fatal(e)
			}
			independent := true
			snapshot := storage.ArtistSourceSnapshot{
				Candidates: []storage.ArtistCandidate{{Source: source, ExternalID: tc.mbid, MBID: tc.mbid, DisplayName: tc.input.Name, Score: 85}},
				Profiles:   map[string]storage.ExternalArtistProfile{tc.mbid: {Source: source, ExternalID: tc.mbid, DisplayName: tc.input.Name}},
			}
			if source == "lastfm" {
				snapshot.Independent = &independent
			}
			if e = store.SaveArtistRunSourceCheck(ctx, input, setting, snapshot, cp); e != nil {
				t.Fatal(e)
			}
		}
	}
	restarted := New(ctx, store, manager.logger, t.TempDir())
	defer restarted.Wait()
	restarted.client = manager.client
	artistFakeClock(restarted)
	if err = restarted.ResumeArtistMatching(ctx, run); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	manager.Wait()
	finished, err := store.DurableArtistRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" || finished.Matched != 2 {
		t.Fatalf("run=%+v", finished)
	}
	// 两位都已绑定身份。
	for id, mbid := range map[int64]string{performer: pfMBID, backstage: bkMBID} {
		if externalID, e := store.ArtistExternalID(ctx, id, "musicbrainz"); e != nil || externalID != mbid {
			t.Fatalf("artist %d identity: %q %v", id, externalID, e)
		}
	}
	// 纯幕后：零简介网络请求、零简介行；歌手：3 语言 × (wikipedia+lastfm)。
	if wikiReqs.Load() != 3 || lastfmReqs.Load() != 3 || wikidataReqs.Load() != 1 || mbReqs.Load() != 1 {
		t.Fatalf("wiki=%d lastfm=%d wikidata=%d mb=%d (want 3/3/1/1, performer only)", wikiReqs.Load(), lastfmReqs.Load(), wikidataReqs.Load(), mbReqs.Load())
	}
	if got := bioRowCount(t, restarted, backstage); got != 0 {
		t.Fatalf("credit-only artist must not receive bio rows, got %d", got)
	}
	if got := bioRowCount(t, restarted, performer); got != 6 {
		t.Fatalf("performer bio rows=%d, want 6", got)
	}
}

// P1-2：合并来源继承身份时，Last.fm 简介请求必须锚定身份快照的 MBID
// （带 mbid 参数、不带 artist 参数），绝不按名字 autocorrect。
func TestArtistBiographyBackfillMergedIdentityLastFMUsesMBID(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "合并继承简介"
	bt.lastfmBody = bioLastFMBody
	manager, canonical := bioBackfillManager(t, bt, "Bio Artist")
	store := manager.store
	ctx := context.Background()
	source := bioImportArtist(t, store, "Source Artist")
	confirmBioIdentity(t, store, source, "mbid-bio-1")
	if _, err := store.MergeArtists(ctx, source, canonical); err != nil {
		t.Fatal(err)
	}
	enableBackfillSource(t, store, "lastfm", true)
	var mu sync.Mutex
	var lastfmQueries []string
	bt.onRequest = func(r *http.Request) {
		if strings.Contains(r.URL.Host, "audioscrobbler.com") {
			mu.Lock()
			lastfmQueries = append(lastfmQueries, r.URL.RawQuery)
			mu.Unlock()
		}
	}
	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Filled != 1 {
		t.Fatalf("run=%+v", finished)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lastfmQueries) != 3 {
		t.Fatalf("lastfm requests=%d, want 3", len(lastfmQueries))
	}
	for _, q := range lastfmQueries {
		values, err := url.ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("mbid") != "mbid-bio-1" || values.Get("artist") != "" {
			t.Fatalf("lastfm query must anchor the snapshot MBID without artist name: %s", q)
		}
	}
	// 3 语言 × (wikipedia + lastfm) = 6 行。
	if got := bioRowCount(t, manager, canonical); got != 6 {
		t.Fatalf("canonical bio rows=%d, want 6", got)
	}
}

// P2-3：以继承身份抓取期间，规范歌手本行被确认了优先级更高的本行身份，
// 守卫在事务内重解生效身份并拒绝迟到的旧身份简介。
func TestArtistBiographyBackfillOwnIdentityConfirmedMidFetch(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "旧身份简介"
	manager, canonical := bioBackfillManager(t, bt, "Bio Artist")
	store := manager.store
	ctx := context.Background()
	source := bioImportArtist(t, store, "Source Artist")
	confirmBioIdentity(t, store, source, "mbid-bio-1")
	if _, err := store.MergeArtists(ctx, source, canonical); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	bt.onRequest = func(r *http.Request) {
		if strings.Contains(r.URL.Host, "musicbrainz.org") {
			once.Do(func() {
				// 并发在规范歌手本行确认另一个身份（本行优先级高于继承）。
				if err := store.UpsertExternalArtistProfile(context.Background(), canonical, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-own-NEW", DisplayName: "Bio Artist"}); err != nil {
					t.Errorf("confirm own identity mid fetch: %v", err)
				}
			})
		}
	}
	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Skipped != 1 || finished.Filled != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if got := bioRowCount(t, manager, canonical); got != 0 {
		t.Fatalf("stale inherited-identity biography must be refused, rows=%d", got)
	}
	externalID, err := store.ArtistExternalID(ctx, canonical, "musicbrainz")
	if err != nil || externalID != "mbid-own-NEW" {
		t.Fatalf("own identity must survive: %q %v", externalID, err)
	}
}

// P2-1：部分语言已落 found 后遭遇 429，等待重试应继续补剩余语言并以
// filled 结算，不得误判 skipped_state、不得遗漏剩余语言。
func TestArtistBiographyBackfillPartialThen429Completes(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = bioMBBody
	bt.wikidataBody = bioWikidataBody
	bt.wikiExtract = "分段简介"
	var wikiCalls atomic.Int32
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "wikipedia.org") && wikiCalls.Add(1) == 2 {
			// 第 2 个语言（ja）首次请求限流一次。
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"60"}}, ""), nil
		}
		return bt.roundTrip(r)
	}))
	artistFakeClock(manager)
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	artist := bioImportArtist(t, store, "Bio Artist")
	confirmBioIdentity(t, store, artist, "mbid-bio-1")
	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	finished, err := store.DurableArtistBiographyBackfillRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" || finished.Filled != 1 || finished.Skipped != 0 || finished.Failed != 0 {
		t.Fatalf("run=%+v", finished)
	}
	// zh 落行后 ja 429（2 次请求）；重试重新拉取 3 语言（3 次），共 5 次。
	if wikiCalls.Load() != 5 {
		t.Fatalf("wiki requests=%d, want 5", wikiCalls.Load())
	}
	if got := bioRowCount(t, manager, artist); got != 3 {
		t.Fatalf("bio rows=%d, want 3 (remaining languages must be filled)", got)
	}
	db := writableSafetyDB(t, manager)
	var outcome string
	if err = db.QueryRowContext(ctx, `SELECT outcome FROM artist_biography_backfill_items WHERE run_id=?`, runID).Scan(&outcome); err != nil || outcome != "filled" {
		t.Fatalf("outcome=%q err=%v (must not be skipped_state)", outcome, err)
	}
}

// P1-A：Last.fm 明确“未收录”（error 6）逐语言写 guarded missing 行，
// outcome=missing 而非 failed；第二次运行 fresh 全部跳过、Last.fm 请求
// 零增量，重复点击不会把 missing 误计 failed。
func TestArtistBiographyBackfillLastFMNotFoundWritesMissing(t *testing.T) {
	bt := newBioTransport()
	// MB 上没有 wikidata 关系：Wikipedia 路径同样落 missing。
	bt.mbBody = `{"id":"mbid-bio-1","name":"Bio Artist","sort-name":"Bio Artist","aliases":[],"tags":[],"relations":[]}`
	bt.lastfmBody = `{"error":6,"message":"The artist you supplied could not be found"}`
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	enableBackfillSource(t, manager.store, "lastfm", true)
	ctx := context.Background()

	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Status != "completed" || finished.Missing != 1 || finished.Failed != 0 || finished.Filled != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if bt.lastfmReqs.Load() != 3 || bt.mbReqs.Load() != 1 {
		t.Fatalf("lastfm=%d mb=%d", bt.lastfmReqs.Load(), bt.mbReqs.Load())
	}
	db := writableSafetyDB(t, manager)
	var missingRows, foundRows int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM artist_biographies WHERE artist_id=? AND status='missing'`, artist).Scan(&missingRows); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM artist_biographies WHERE artist_id=? AND status='found'`, artist).Scan(&foundRows); err != nil {
		t.Fatal(err)
	}
	// 3 语言 × (wikipedia + lastfm) = 6 行 missing，无 found。
	if missingRows != 6 || foundRows != 0 {
		t.Fatalf("missing=%d found=%d, want 6/0", missingRows, foundRows)
	}

	// 第二次运行（重复点击）：fresh missing 全部跳过，Last.fm 请求零增量，
	// 不计 failed。
	runID2, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished2 := waitBioBackfillRun(t, manager.store, runID2)
	manager.Wait()
	if finished2.Status != "completed" || finished2.Skipped != 1 || finished2.Failed != 0 {
		t.Fatalf("run2=%+v", finished2)
	}
	if bt.lastfmReqs.Load() != 3 || bt.mbReqs.Load() != 1 || bt.wikiReqs.Load() != 0 {
		t.Fatalf("fresh missing must not re-hit sources: lastfm=%d mb=%d wiki=%d", bt.lastfmReqs.Load(), bt.mbReqs.Load(), bt.wikiReqs.Load())
	}
	var outcome string
	if err = db.QueryRowContext(ctx, `SELECT outcome FROM artist_biography_backfill_items WHERE run_id=?`, runID2).Scan(&outcome); err != nil || outcome != "skipped_fresh" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
}

// P1-A 反例：Last.fm HTTP 404（Last.fm 不以 404 表达“未收录”，generic
// 错误）绝不误记 missing：不写 lastfm 行、下次运行仍重试该来源；仅当所有
// 启用来源都未解决时才计 failed（既有结算语义）。
func TestArtistBiographyBackfillLastFM404NotMissing(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = `{"id":"mbid-bio-1","name":"Bio Artist","sort-name":"Bio Artist","aliases":[],"tags":[],"relations":[]}`
	bt.lastfmStatus.Store(http.StatusNotFound)
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	enableBackfillSource(t, manager.store, "lastfm", true)
	ctx := context.Background()
	runID, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	// Wikipedia 已落 missing（resolved），lastfm 瞬时失败不写任何行。
	if finished.Status != "completed" || finished.Missing != 1 || finished.Failed != 0 {
		t.Fatalf("run=%+v", finished)
	}
	db := writableSafetyDB(t, manager)
	var lastfmRows int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM artist_biographies WHERE artist_id=? AND source='lastfm'`, artist).Scan(&lastfmRows); err != nil {
		t.Fatal(err)
	}
	if lastfmRows != 0 {
		t.Fatalf("HTTP 404 must not produce missing rows, got %d", lastfmRows)
	}
	// 第二次运行：lastfm 无行不新鲜 → 重试（请求增量 3），全部来源未解决
	// → failed，证明 404 未被当作“已检查无简介”缓存。
	runID2, err := manager.StartArtistBiographyBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished2 := waitBioBackfillRun(t, manager.store, runID2)
	manager.Wait()
	if finished2.Status != "completed" || finished2.Failed != 1 || bt.lastfmReqs.Load() != 6 {
		t.Fatalf("run2=%+v lastfm=%d", finished2, bt.lastfmReqs.Load())
	}
}

// P2-6：语言配置为空（绕过规范化直写 DB）返回专用错误而非 sql.ErrNoRows。
func TestRefreshArtistBiographiesNoLanguages(t *testing.T) {
	bt := newBioTransport()
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	db := writableSafetyDB(t, manager)
	if _, err := db.ExecContext(context.Background(), `UPDATE artist_biography_settings SET preferred_languages='',english_fallback=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshArtistBiographies(context.Background(), artist, true); !errors.Is(err, ErrNoBiographyLanguages) {
		t.Fatalf("err=%v, want ErrNoBiographyLanguages", err)
	}
	if bt.mbReqs.Load()+bt.wikidataReqs.Load()+bt.wikiReqs.Load()+bt.lastfmReqs.Load() != 0 {
		t.Fatal("empty language list must not touch the network")
	}
}
