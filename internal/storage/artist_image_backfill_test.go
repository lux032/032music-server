package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

// backfillFixture 导入曲目产生艺术家，并提供确认身份/自定义头像的快捷方式。
type backfillFixture struct {
	albumMergeFixture
}

func newBackfillFixture(t *testing.T) backfillFixture {
	t.Helper()
	return backfillFixture{newAlbumMergeFixture(t)}
}

func (f backfillFixture) importArtistTrack(t *testing.T, path, artist string) {
	t.Helper()
	input := ImportInput{LibraryID: f.library, RelativePath: path, FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album-" + artist, Artists: []string{artist}, AlbumArtists: []string{artist}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
}

func (f backfillFixture) artistID(t *testing.T, name string) int64 {
	t.Helper()
	artists, err := f.store.ArtistsForMatching(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, artist := range artists {
		if artist.Name == name {
			return artist.ID
		}
	}
	t.Fatalf("artist %q not found", name)
	return 0
}

func (f backfillFixture) confirmIdentity(t *testing.T, artistID int64, source, externalID, imageURL string) {
	t.Helper()
	profile := ExternalArtistProfile{Source: source, ExternalID: externalID, DisplayName: fmt.Sprintf("Artist %d", artistID), RemoteImageURL: imageURL}
	if err := f.store.UpsertExternalArtistProfile(f.ctx, artistID, profile); err != nil {
		t.Fatal(err)
	}
}

func candidateIDs(list []ArtistImageBackfillCandidate) map[int64]bool {
	ids := map[int64]bool{}
	for _, c := range list {
		ids[c.ID] = true
	}
	return ids
}

// D50 候选口径：仅未合并且持有已确认身份、本人与合并来源均无自定义头像者。
func TestArtistImageBackfillCandidatesD50(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	f.importArtistTrack(t, "C/01.flac", "Gamma")
	f.importArtistTrack(t, "D/01.flac", "Delta")
	f.importArtistTrack(t, "E/01.flac", "Epsilon")
	f.importArtistTrack(t, "F/01.flac", "Zeta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	gamma := f.artistID(t, "Gamma")
	delta := f.artistID(t, "Delta")
	epsilon := f.artistID(t, "Epsilon")
	zeta := f.artistID(t, "Zeta")

	// Alpha：确认身份、无自定义 → 候选。
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-alpha", "")
	// Beta：本人自定义头像 → 排除。
	f.confirmIdentity(t, beta, "musicbrainz", "mbid-beta", "")
	if _, err := f.store.SaveCustomArtistImage(f.ctx, beta, f.customImage("beta.png")); err != nil {
		t.Fatal(err)
	}
	// Gamma：有身份但已并入 Alpha → 排除。
	f.confirmIdentity(t, gamma, "musicbrainz", "mbid-gamma", "")
	if _, err := f.store.MergeArtists(f.ctx, gamma, alpha); err != nil {
		t.Fatal(err)
	}
	// Delta：本人无自定义，但合并来源 Epsilon 有自定义（继承）→ 排除。
	f.confirmIdentity(t, delta, "musicbrainz", "mbid-delta", "")
	f.confirmIdentity(t, epsilon, "musicbrainz", "mbid-epsilon", "")
	if _, err := f.store.MergeArtists(f.ctx, epsilon, delta); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveCustomArtistImage(f.ctx, epsilon, f.customImage("epsilon.png")); err != nil {
		t.Fatal(err)
	}
	// Zeta：无任何已确认身份 → 排除。

	candidates, err := f.store.ArtistImageBackfillCandidates(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := candidateIDs(candidates)
	if !ids[alpha] {
		t.Fatalf("alpha must be a candidate: %v", ids)
	}
	for _, excluded := range []int64{beta, gamma, delta, epsilon, zeta} {
		if ids[excluded] {
			t.Fatalf("artist %d must not be a candidate: %v", excluded, ids)
		}
	}

	if _, err = f.store.ArtistImageBackfillSubject(f.ctx, alpha); err != nil {
		t.Fatalf("alpha subject: %v", err)
	}
	for _, excluded := range []int64{beta, gamma, delta, zeta} {
		if _, err = f.store.ArtistImageBackfillSubject(f.ctx, excluded); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("subject %d must be ineligible, got %v", excluded, err)
		}
	}
}

// 任务生命周期：创建互斥、claim token 围栏、幂等完成、暂停/继续/停止/完成。
func TestArtistImageBackfillRunLifecycle(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	ctx := f.ctx

	if _, err := f.store.CreateArtistImageBackfillRun(ctx, nil); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("empty run must be rejected: %v", err)
	}
	items := []ArtistImageBackfillCandidate{{ID: alpha, Name: "Alpha"}, {ID: beta, Name: "Beta"}}
	run, err := f.store.CreateArtistImageBackfillRun(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CreateArtistImageBackfillRun(ctx, items); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("second active run must be rejected: %v", err)
	}

	item, err := f.store.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil || item.ArtistID != alpha || item.ClaimToken != 1 || item.Status != "in_progress" {
		t.Fatalf("claim=%+v err=%v", item, err)
	}
	stale := ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken + 1}
	if err = f.store.CompleteArtistImageBackfillItem(ctx, stale, "cached", ""); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("stale token must be rejected: %v", err)
	}
	checkpoint := ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = f.store.CompleteArtistImageBackfillItem(ctx, checkpoint, "cached", ""); err != nil {
		t.Fatal(err)
	}
	// 幂等：重复完成同一项直接成功（断点重放安全）。
	if err = f.store.CompleteArtistImageBackfillItem(ctx, checkpoint, "cached", ""); err != nil {
		t.Fatalf("idempotent complete: %v", err)
	}
	state, err := f.store.DurableArtistImageBackfillRun(ctx, run)
	if err != nil || state.Processed != 1 || state.Cached != 1 {
		t.Fatalf("run=%+v err=%v", state, err)
	}

	item2, err := f.store.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil || item2.ArtistID != beta {
		t.Fatalf("claim2=%+v err=%v", item2, err)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ClaimArtistImageBackfillItem(ctx, run); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("paused claim must fail: %v", err)
	}
	oldToken := item2.ClaimToken
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run, "resume", ""); err != nil {
		t.Fatal(err)
	}
	// resume 后旧 token 失效（纪元围栏），重新 claim 得到新 token。
	if err = f.store.CompleteArtistImageBackfillItem(ctx, ArtistImageBackfillCheckpoint{RunID: run, ItemID: item2.ID, ClaimToken: oldToken}, "failed", "x"); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("pre-resume token must be fenced: %v", err)
	}
	item2, err = f.store.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil || item2.ClaimToken == oldToken {
		t.Fatalf("re-claim=%+v err=%v", item2, err)
	}
	if err = f.store.CompleteArtistImageBackfillItem(ctx, ArtistImageBackfillCheckpoint{RunID: run, ItemID: item2.ID, ClaimToken: item2.ClaimToken}, "failed", "download failed"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ClaimArtistImageBackfillItem(ctx, run); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("drained claim must be sql.ErrNoRows: %v", err)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run, "complete", ""); err != nil {
		t.Fatal(err)
	}
	state, _ = f.store.DurableArtistImageBackfillRun(ctx, run)
	if state.Status != "completed" || state.Processed != 2 || state.Failed != 1 {
		t.Fatalf("run=%+v", state)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run, "pause", "manual"); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("completed run must not pause: %v", err)
	}

	// 新一轮：暂停中的任务可以取消；取消是终态，不能再继续。
	run2, err := f.store.CreateArtistImageBackfillRun(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run2, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run2, "cancel", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run2, "resume", ""); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("cancelled run must not resume: %v", err)
	}
}

// 限流等待：较长截止时间取胜、连续 3 次与 30 分钟窗口、自动恢复轮次与用尽。
func TestArtistImageBackfillWaitBudget(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	alpha := f.artistID(t, "Alpha")
	ctx := f.ctx
	now := time.Now().UTC().Truncate(time.Millisecond)

	newClaimedRun := func(t *testing.T) (int64, ArtistImageBackfillCheckpoint) {
		t.Helper()
		run, err := f.store.CreateArtistImageBackfillRun(ctx, []ArtistImageBackfillCandidate{{ID: alpha, Name: "Alpha"}})
		if err != nil {
			t.Fatal(err)
		}
		item, err := f.store.ClaimArtistImageBackfillItem(ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		return run, ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	}

	// 连续 3 次限流 → 暂停 rate_limit_count；较长 waiting_until 取胜。
	run, cp := newClaimedRun(t)
	if err := f.store.RecordArtistImageBackfillWait(ctx, cp, "artist image", now.Add(30*time.Second), 0, true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordArtistImageBackfillWait(ctx, cp, "musicbrainz", now.Add(10*time.Second), 0, true); err != nil {
		t.Fatal(err)
	}
	state, _ := f.store.DurableArtistImageBackfillRun(ctx, run)
	if state.WaitSource != "artist image" {
		t.Fatalf("longer deadline source must win: %+v", state)
	}
	if parsed, e := time.Parse(time.RFC3339Nano, state.WaitingUntil); e != nil || !parsed.Equal(now.Add(30*time.Second)) {
		t.Fatalf("waiting_until=%q parse=%v", state.WaitingUntil, e)
	}
	if err := f.store.RecordArtistImageBackfillWait(ctx, cp, "artist image", now.Add(3*time.Minute), 0, true); !errors.Is(err, ErrArtistImageBackfillBudget) {
		t.Fatalf("third rate limit must exhaust budget: %v", err)
	}
	state, _ = f.store.DurableArtistImageBackfillRun(ctx, run)
	if state.Status != "paused" || state.PauseReason != "rate_limit_count" {
		t.Fatalf("run=%+v", state)
	}
	if !ArtistImageBackfillAutoResumeEligible(state) {
		t.Fatalf("rate-limit pause must be auto-resumable: %+v", state)
	}
	// 旧 worker 的 response=true 等待在暂停后被拒绝。
	if err := f.store.RecordArtistImageBackfillWait(ctx, cp, "artist image", now.Add(time.Minute), 0, true); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("stale response wait must be rejected: %v", err)
	}
	// auto_resume 复位 item 与预算基线，轮次 +1。
	if err := f.store.TransitionArtistImageBackfillRun(ctx, run, "auto_resume", ""); err != nil {
		t.Fatal(err)
	}
	state, _ = f.store.DurableArtistImageBackfillRun(ctx, run)
	if state.Status != "running" || state.AutoResumeCount != 1 {
		t.Fatalf("run=%+v", state)
	}
	if err := f.store.TransitionArtistImageBackfillRun(ctx, run, "cancel", "manual"); err != nil {
		t.Fatal(err)
	}

	// 30 分钟窗口预算：累计等待超过 30 分钟 → rate_limit_wait_budget。
	run2, cp2 := newClaimedRun(t)
	if err := f.store.RecordArtistImageBackfillWait(ctx, cp2, "artist image", now.Add(31*time.Minute), 31*time.Minute, false); !errors.Is(err, ErrArtistImageBackfillBudget) {
		t.Fatalf("wait budget must trip: %v", err)
	}
	state, _ = f.store.DurableArtistImageBackfillRun(ctx, run2)
	if state.PauseReason != "rate_limit_wait_budget" {
		t.Fatalf("run=%+v", state)
	}

	// 自动恢复轮次用尽 → rate_limit_exhausted，且不再允许 auto_resume。
	if _, err := f.store.db.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET auto_resume_count=? WHERE id=?`, MaxArtistImageBackfillAutoResumeRounds, run2); err != nil {
		t.Fatal(err)
	}
	if err := f.store.TransitionArtistImageBackfillRun(ctx, run2, "resume", ""); err != nil {
		t.Fatal(err)
	}
	item, err := f.store.ClaimArtistImageBackfillItem(ctx, run2)
	if err != nil {
		t.Fatal(err)
	}
	cp3 := ArtistImageBackfillCheckpoint{RunID: run2, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = f.store.RecordArtistImageBackfillWait(ctx, cp3, "artist image", now.Add(31*time.Minute), 31*time.Minute, true); !errors.Is(err, ErrArtistImageBackfillBudget) {
		t.Fatalf("exhausted budget wait: %v", err)
	}
	state, _ = f.store.DurableArtistImageBackfillRun(ctx, run2)
	if state.PauseReason != "rate_limit_exhausted" || ArtistImageBackfillAutoResumeEligible(state) {
		t.Fatalf("run=%+v", state)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run2, "auto_resume", ""); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("exhausted run must not auto resume: %v", err)
	}
	// 终态仍可人工继续。
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run2, "resume", ""); err != nil {
		t.Fatalf("manual resume of exhausted run: %v", err)
	}
}

// 启动恢复：中断任务暂停为 server_restart，轮次用尽者标记终态原因；
// 带限流截止时间的恢复任务进入自动恢复扫描。
func TestArtistImageBackfillRecover(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	alpha := f.artistID(t, "Alpha")
	ctx := f.ctx
	now := time.Now().UTC().Truncate(time.Millisecond)

	run, err := f.store.CreateArtistImageBackfillRun(ctx, []ArtistImageBackfillCandidate{{ID: alpha, Name: "Alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.store.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	cp := ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = f.store.RecordArtistImageBackfillWait(ctx, cp, "artist image", now.Add(time.Minute), 0, true); err != nil {
		t.Fatal(err)
	}
	n, err := f.store.RecoverArtistImageBackfillRuns(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered=%d err=%v", n, err)
	}
	state, _ := f.store.DurableArtistImageBackfillRun(ctx, run)
	if state.Status != "paused" || state.PauseReason != "server_restart" || state.WaitingUntil == "" {
		t.Fatalf("run=%+v", state)
	}
	awaiting, err := f.store.ArtistImageBackfillRunsAwaitingAutoResume(ctx)
	if err != nil || len(awaiting) != 1 || awaiting[0].ID != run {
		t.Fatalf("awaiting=%+v err=%v", awaiting, err)
	}

	// 轮次已用尽的中断任务：恢复时标记 rate_limit_exhausted，不再自动继续。
	if _, err = f.store.db.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status='running',pause_reason='',auto_resume_count=? WHERE id=?`, MaxArtistImageBackfillAutoResumeRounds, run); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RecoverArtistImageBackfillRuns(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ = f.store.DurableArtistImageBackfillRun(ctx, run)
	if state.PauseReason != "rate_limit_exhausted" {
		t.Fatalf("run=%+v", state)
	}
	awaiting, _ = f.store.ArtistImageBackfillRunsAwaitingAutoResume(ctx)
	if len(awaiting) != 0 {
		t.Fatalf("exhausted run must not await auto resume: %+v", awaiting)
	}
}

// guarded 写入：状态/URL/身份在提交前再次验证，任何漂移都拒绝。
func TestSaveArtistImageGuarded(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	ctx := f.ctx
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-alpha", "https://cdn.example/a.jpg")

	image := ArtistImageInput{ArtistID: alpha, ByteSize: 10, Source: "musicbrainz", RemoteURL: "https://cdn.example/a.jpg", Hash: "hash-1", MIMEType: "image/jpeg", CachePath: "/tmp/a.jpg"}
	if err := f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-alpha", "https://cdn.example/a.jpg", nil); err != nil {
		t.Fatalf("clean guarded save: %v", err)
	}

	// URL 漂移 → 拒绝。
	if err := f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-alpha", "https://cdn.example/other.jpg", nil); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("url drift must be rejected: %v", err)
	}
	// 身份漂移 → 拒绝。
	if err := f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-other", "https://cdn.example/a.jpg", nil); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("identity drift must be rejected: %v", err)
	}
	// 资料被解除（行消失）→ 拒绝。
	if err := f.store.SaveArtistImageGuarded(ctx, image, "lastfm", "x", "https://cdn.example/a.jpg", nil); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("missing profile must be rejected: %v", err)
	}
	// 并发新增本人自定义头像 → 拒绝。
	if _, err := f.store.SaveCustomArtistImage(ctx, alpha, f.customImage("alpha.png")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-alpha", "https://cdn.example/a.jpg", nil); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("concurrent custom image must be rejected: %v", err)
	}
	if _, err := f.store.ResetCustomArtistImage(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	// 合并来源的自定义头像（继承）→ 拒绝。
	f.confirmIdentity(t, beta, "musicbrainz", "mbid-beta", "")
	if _, err := f.store.MergeArtists(ctx, beta, alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveCustomArtistImage(ctx, beta, f.customImage("beta.png")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-alpha", "https://cdn.example/a.jpg", nil); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("inherited custom image must be rejected: %v", err)
	}
	if _, err := f.store.ResetCustomArtistImage(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	// 艺术家本人被合并 → 拒绝。
	if err := f.store.SaveArtistImageGuarded(ctx, ArtistImageInput{ArtistID: beta, ByteSize: 10, Source: "musicbrainz", RemoteURL: "u", Hash: "h", MIMEType: "image/jpeg", CachePath: "/tmp/b.jpg"}, "musicbrainz", "mbid-beta", "u", nil); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("merged artist must be rejected: %v", err)
	}

	// checkpoint 纪元围栏：token 失配拒绝，匹配则写入。
	if err := f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-alpha", "https://cdn.example/a.jpg", nil); err != nil {
		t.Fatalf("guarded save after cleanup: %v", err)
	}
	run, err := f.store.CreateArtistImageBackfillRun(ctx, []ArtistImageBackfillCandidate{{ID: alpha, Name: "Alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.store.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	image.Hash = "hash-2"
	stale := &ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken + 9}
	if err = f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-alpha", "https://cdn.example/a.jpg", stale); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("stale checkpoint must be rejected: %v", err)
	}
	good := &ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = f.store.SaveArtistImageGuarded(ctx, image, "musicbrainz", "mbid-alpha", "https://cdn.example/a.jpg", good); err != nil {
		t.Fatalf("checkpointed guarded save: %v", err)
	}
	var hash string
	if err = f.store.db.QueryRowContext(ctx, `SELECT content_hash FROM artist_image_cache WHERE artist_id=?`, alpha).Scan(&hash); err != nil || hash != "hash-2" {
		t.Fatalf("cache row hash=%q err=%v", hash, err)
	}
}

// P1-1：guarded 资料刷新——单事务内 checkpoint + 身份等于预期才 UPDATE，
// 绝不 INSERT、绝不写 external_id；身份解除/改认/claim 过期一律拒绝。
func TestRefreshArtistProfileGuarded(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	alpha := f.artistID(t, "Alpha")
	ctx := f.ctx
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-alpha", "")

	run, err := f.store.CreateArtistImageBackfillRun(ctx, []ArtistImageBackfillCandidate{{ID: alpha, Name: "Alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.store.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	good := &ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}

	refreshed := ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-alpha", DisplayName: "Alpha New", RemoteImageURL: "https://cdn.example/a.jpg"}
	if err = f.store.RefreshArtistProfileGuarded(ctx, alpha, refreshed, "mbid-alpha", good); err != nil {
		t.Fatalf("clean guarded refresh: %v", err)
	}
	var externalID, remoteURL, displayName string
	if err = f.store.db.QueryRowContext(ctx, `SELECT external_id,COALESCE(remote_image_url,''),display_name FROM artist_external_profiles WHERE artist_id=? AND source='musicbrainz'`, alpha).Scan(&externalID, &remoteURL, &displayName); err != nil {
		t.Fatal(err)
	}
	if externalID != "mbid-alpha" || remoteURL != "https://cdn.example/a.jpg" || displayName != "Alpha New" {
		t.Fatalf("profile=(%q,%q,%q)", externalID, remoteURL, displayName)
	}

	// 响应身份与预期不一致（漂移预检）→ 拒绝。
	if err = f.store.RefreshArtistProfileGuarded(ctx, alpha, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-other"}, "mbid-alpha", good); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("drifted profile must be rejected: %v", err)
	}
	// 预期身份为空 → 拒绝。
	if err = f.store.RefreshArtistProfileGuarded(ctx, alpha, refreshed, "", good); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("empty expected identity must be rejected: %v", err)
	}
	// 预期身份与库存不一致（并发重新确认为其他身份）→ 拒绝且库存不动。
	if err = f.store.RefreshArtistProfileGuarded(ctx, alpha, refreshed, "mbid-stale", good); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("stale expected identity must be rejected: %v", err)
	}
	// 过期 claim token → 拒绝。
	stale := &ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken + 9}
	if err = f.store.RefreshArtistProfileGuarded(ctx, alpha, refreshed, "mbid-alpha", stale); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("stale checkpoint must be rejected: %v", err)
	}
	// 任务暂停（run 不再 running）→ 拒绝。
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.RefreshArtistProfileGuarded(ctx, alpha, refreshed, "mbid-alpha", good); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("paused run must be rejected: %v", err)
	}

	// 身份被解除（行删除）→ 拒绝且绝不 INSERT 复活。
	if _, err = f.store.db.ExecContext(ctx, `DELETE FROM artist_external_profiles WHERE artist_id=? AND source='musicbrainz'`, alpha); err != nil {
		t.Fatal(err)
	}
	if err = f.store.TransitionArtistImageBackfillRun(ctx, run, "resume", ""); err != nil {
		t.Fatal(err)
	}
	item, err = f.store.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	good = &ArtistImageBackfillCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = f.store.RefreshArtistProfileGuarded(ctx, alpha, refreshed, "mbid-alpha", good); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("deleted identity must not resurrect: %v", err)
	}
	var rows int
	if err = f.store.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_external_profiles WHERE artist_id=?`, alpha).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("identity resurrected: rows=%d err=%v", rows, err)
	}
}

// P2-3：批量扫描一次列出 D50 候选与有效缓存路径（本人优先、继承最新）。
func TestArtistImageBackfillCacheScan(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	f.importArtistTrack(t, "C/01.flac", "Gamma")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	gamma := f.artistID(t, "Gamma")
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-alpha", "")
	f.confirmIdentity(t, beta, "musicbrainz", "mbid-beta", "")
	f.confirmIdentity(t, gamma, "musicbrainz", "mbid-gamma", "")
	// Alpha 本人有缓存行；Gamma 并入 Beta 且 Gamma 有缓存行（Beta 继承）。
	if err := f.store.SaveArtistImage(f.ctx, ArtistImageInput{ArtistID: alpha, ByteSize: 10, Source: "musicbrainz", RemoteURL: "u1", Hash: "h1", MIMEType: "image/jpeg", CachePath: "/cache/alpha.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveArtistImage(f.ctx, ArtistImageInput{ArtistID: gamma, ByteSize: 10, Source: "musicbrainz", RemoteURL: "u2", Hash: "h2", MIMEType: "image/jpeg", CachePath: "/cache/gamma.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MergeArtists(f.ctx, gamma, beta); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.ArtistImageBackfillCacheScan(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[int64]string{}
	for _, row := range rows {
		paths[row.Candidate.ID] = row.CachePath
	}
	// Gamma 已合并不是候选；Alpha 本人路径；Beta 继承 Gamma 路径。
	if len(rows) != 2 || paths[alpha] != "/cache/alpha.jpg" || paths[beta] != "/cache/gamma.jpg" {
		t.Fatalf("scan=%+v", rows)
	}
}
