package storage

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

// 候选口径（H2/M4）：未合并歌手（专辑歌手或曲目 primary）、本行无人工
// 简介、本人与合并来源均无 found 版本行；纯幕后不纳入，歌手兼幕后纳入；
// 无 MBID 者仍是 SQL 候选（逐项计 skipped_no_mbid）。
func TestArtistBiographyBackfillCandidates(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	f.importArtistTrack(t, "C/01.flac", "Gamma")
	f.importArtistTrack(t, "D/01.flac", "Delta")
	// Epsilon 纯幕后（Eta 演唱、Epsilon 仅作曲）；Zeta 歌手兼幕后（演唱+作曲）。
	importWithComposer := func(path, performer, composer string) {
		t.Helper()
		input := ImportInput{LibraryID: f.library, RelativePath: path, FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album-" + performer, Artists: []string{performer}, AlbumArtists: []string{performer}, Composer: composer, DiscNumber: 1, TrackNumber: 1}}
		if err := f.store.ImportTrack(f.ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	importWithComposer("E/01.flac", "Eta", "Epsilon")
	importWithComposer("Z/01.flac", "Zeta", "Zeta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	gamma := f.artistID(t, "Gamma")
	delta := f.artistID(t, "Delta")
	epsilon := f.artistID(t, "Epsilon")
	zeta := f.artistID(t, "Zeta")

	// Beta：本行有人工简介 → 排除。
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE artists SET user_biography='人工简介' WHERE id=?`, beta); err != nil {
		t.Fatal(err)
	}
	// Gamma：本行已有 found 版本行 → 排除。
	if err := f.store.UpsertArtistBiography(f.ctx, gamma, ArtistBiography{Source: "wikipedia", Language: "zh", Biography: "已有"}); err != nil {
		t.Fatal(err)
	}
	// Delta：本人无 found 行，但合并来源 Alpha 有 found 行（继承口径）→ 排除；
	// Alpha 已有 found 行，本身也不再是候选。
	if _, err := f.store.MergeArtists(f.ctx, alpha, delta); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertArtistBiography(f.ctx, alpha, ArtistBiography{Source: "lastfm", Language: "en", Biography: "inherited"}); err != nil {
		t.Fatal(err)
	}

	candidates, err := f.store.ArtistBiographyBackfillCandidates(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, c := range candidates {
		ids[c.ID] = true
	}
	// 候选：Zeta（歌手兼幕后）与 Eta（演唱者，无简介）。
	if !ids[zeta] || !ids[f.artistID(t, "Eta")] {
		t.Fatalf("zeta/eta 必须是候选: %v", ids)
	}
	for _, excluded := range []int64{alpha, beta, gamma, delta, epsilon} {
		if ids[excluded] {
			t.Fatalf("artist %d 不应是候选: %v", excluded, ids)
		}
	}
	total, err := f.store.ArtistBiographyPerformerCount(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 歌手总数：Beta/Gamma/Delta/Zeta/Eta（Alpha 已并入 Delta 不计；
	// Epsilon 纯幕后不计）。
	if total != 5 {
		t.Fatalf("performer total=%d, want 5", total)
	}
	if _, err = f.store.ArtistBiographyBackfillSubject(f.ctx, zeta); err != nil {
		t.Fatalf("zeta subject: %v", err)
	}
	for _, excluded := range []int64{alpha, beta, gamma, delta, epsilon} {
		if _, err = f.store.ArtistBiographyBackfillSubject(f.ctx, excluded); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("subject %d must be ineligible, got %v", excluded, err)
		}
	}
}

// 身份快照（分叉 C1）：本行优先；沿合并链（含多跳）读取来源行上的已确认
// MBID；链上没有身份时返回 sql.ErrNoRows。
func TestArtistBiographyIdentityMergeChain(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	f.importArtistTrack(t, "C/01.flac", "Gamma")
	f.importArtistTrack(t, "D/01.flac", "Delta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	gamma := f.artistID(t, "Gamma")
	delta := f.artistID(t, "Delta")

	// 身份确认在 Gamma（将并入 Beta，Beta 再并入 Alpha）上：多跳继承。
	f.confirmIdentity(t, gamma, "musicbrainz", "mbid-gamma", "")
	if _, err := f.store.MergeArtists(f.ctx, gamma, beta); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MergeArtists(f.ctx, beta, alpha); err != nil {
		t.Fatal(err)
	}
	identity, err := f.store.ArtistBiographyIdentity(f.ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if identity.MBID != "mbid-gamma" || identity.OwnerID != gamma {
		t.Fatalf("identity=%+v, want owner=%d mbid-gamma", identity, gamma)
	}
	// 本行身份优先于合并来源身份。
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-alpha", "")
	identity, err = f.store.ArtistBiographyIdentity(f.ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if identity.MBID != "mbid-alpha" || identity.OwnerID != alpha {
		t.Fatalf("identity=%+v, want own row first", identity)
	}
	// 无身份链 → ErrNoRows。
	if _, err = f.store.ArtistBiographyIdentity(f.ctx, delta); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("delta identity: %v", err)
	}
}

// P2-1 放宽复查：已有 found 行的歌手全口径复查失败但放宽复查通过（限流
// 重试续补剩余语言）；人工简介/纯幕后/已合并仍双双拒绝。
func TestArtistBiographyBackfillRetrySubject(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	alpha := f.artistID(t, "Alpha")
	f.store.ImportTrack(f.ctx, ImportInput{LibraryID: f.library, RelativePath: "E/01.flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album-Eta", Artists: []string{"Eta"}, AlbumArtists: []string{"Eta"}, Composer: "Epsilon", DiscNumber: 1, TrackNumber: 1}})
	epsilon := f.artistID(t, "Epsilon")

	// Alpha 写入一条 found 行：全口径 subject 拒绝，放宽 subject 放行。
	if err := f.store.UpsertArtistBiography(f.ctx, alpha, ArtistBiography{Source: "wikipedia", Language: "zh", Biography: "已有"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ArtistBiographyBackfillSubject(f.ctx, alpha); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("full subject must reject artist with found rows: %v", err)
	}
	if _, err := f.store.ArtistBiographyBackfillRetrySubject(f.ctx, alpha); err != nil {
		t.Fatalf("retry subject must allow partial-filled artist: %v", err)
	}
	// 人工简介：放宽复查同样拒绝。
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE artists SET user_biography='人工简介' WHERE id=?`, alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ArtistBiographyBackfillRetrySubject(f.ctx, alpha); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retry subject must reject user_biography: %v", err)
	}
	// 纯幕后：双双拒绝。
	if _, err := f.store.ArtistBiographyBackfillRetrySubject(f.ctx, epsilon); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retry subject must reject credit-only artist: %v", err)
	}
}

// P2-3：守卫在事务内重解生效身份——以继承身份（owner=X）快照写入期间，
// 规范歌手本行出现优先级更高的本行身份后，旧快照写入被拒；新快照放行。
func TestUpsertArtistBiographyGuardedResolutionPrecedence(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	f.confirmIdentity(t, beta, "musicbrainz", "mbid-beta", "")
	if _, err := f.store.MergeArtists(f.ctx, beta, alpha); err != nil {
		t.Fatal(err)
	}
	bio := ArtistBiography{Source: "wikipedia", Language: "zh", Biography: "继承身份简介"}
	if err := f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, beta, "mbid-beta", bio, nil); err != nil {
		t.Fatal(err)
	}
	// 本行确认新身份后：旧快照 (beta, mbid-beta) 拒绝，新快照 (alpha, mbid-alpha) 放行。
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-alpha", "")
	if err := f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, beta, "mbid-beta", bio, nil); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("stale inherited snapshot must be refused: %v", err)
	}
	if err := f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", ArtistBiography{Source: "wikipedia", Language: "en", Biography: "本行身份简介"}, nil); err != nil {
		t.Fatalf("fresh own-row snapshot must pass: %v", err)
	}
}

func bioBackfillCheckpoint(t *testing.T, f backfillFixture, artistID int64) (int64, ArtistBiographyBackfillCheckpoint) {
	t.Helper()
	runID, err := f.store.CreateArtistBiographyBackfillRun(f.ctx, []ArtistBiographyBackfillCandidate{{ID: artistID, Name: "Artist"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.store.ClaimArtistBiographyBackfillItem(f.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return runID, ArtistBiographyBackfillCheckpoint{RunID: runID, ItemID: item.ID, ClaimToken: item.ClaimToken}
}

// H1 事务围栏：网络窗口后的写必须在同一事务内校验 checkpoint、规范歌手
// 未合并、身份属主仍持有快照 MBID 且仍沿合并链归属规范歌手。
func TestUpsertArtistBiographyGuarded(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-alpha", "")
	runID, cp := bioBackfillCheckpoint(t, f, alpha)

	// 正常路径：checkpoint 有效 + 身份未变 → 写入规范歌手行。
	bio := ArtistBiography{Source: "wikipedia", Language: "zh", Biography: "简介"}
	if err := f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", bio, &cp); err != nil {
		t.Fatal(err)
	}
	values, err := f.store.ArtistBiographies(f.ctx, alpha)
	if err != nil || len(values) != 1 || values[0].Biography != "简介" {
		t.Fatalf("biographies=%+v err=%v", values, err)
	}

	// 空简介自动落 missing 行。
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", ArtistBiography{Source: "wikipedia", Language: "en"}, &cp); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = f.store.db.QueryRowContext(f.ctx, `SELECT status FROM artist_biographies WHERE artist_id=? AND source='wikipedia' AND language='en'`, alpha).Scan(&status); err != nil || status != "missing" {
		t.Fatalf("status=%q err=%v", status, err)
	}

	// checkpoint 失配（claim token 过期）→ 拒绝。
	stale := cp
	stale.ClaimToken++
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", bio, &stale); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("stale token: %v", err)
	}
	// 任务暂停后（checkpoint 失效）→ 拒绝。
	if err = f.store.TransitionArtistBiographyBackfillRun(f.ctx, runID, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", bio, &cp); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("paused run: %v", err)
	}
	// 无 checkpoint（交互路径）仍校验身份。
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", bio, nil); err != nil {
		t.Fatalf("nil checkpoint with intact identity: %v", err)
	}

	// 身份被改认为其他 MBID → 拒绝（旧简介不写新身份）。
	f.confirmIdentity(t, alpha, "musicbrainz", "mbid-NEW", "")
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", bio, nil); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("identity drift: %v", err)
	}
	// 身份被解除 → 拒绝（不复活）。
	if err = f.store.ResetArtistIdentity(f.ctx, alpha, "musicbrainz", "mbid-NEW"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-NEW", bio, nil); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("identity reset: %v", err)
	}

	// 继承身份（owner 为合并来源）写入规范歌手行。
	f.confirmIdentity(t, beta, "musicbrainz", "mbid-beta", "")
	if _, err = f.store.MergeArtists(f.ctx, beta, alpha); err != nil {
		t.Fatal(err)
	}
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, beta, "mbid-beta", bio, nil); err != nil {
		t.Fatalf("inherited identity write: %v", err)
	}
	var count int
	if err = f.store.db.QueryRowContext(f.ctx, `SELECT count(*) FROM artist_biographies WHERE artist_id=? AND source='wikipedia' AND language='zh' AND biography='简介'`, alpha).Scan(&count); err != nil || count != 1 {
		t.Fatalf("canonical row count=%d err=%v", count, err)
	}

	// 合并被回滚（owner 脱离规范歌手链）→ 拒绝。
	operations, err := f.store.MergeOperations(f.ctx)
	if err != nil || len(operations) == 0 {
		t.Fatalf("operations=%v err=%v", operations, err)
	}
	if err = f.store.RollbackArtistMerge(f.ctx, operations[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, beta, "mbid-beta", bio, nil); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("owner detached: %v", err)
	}
	// 规范歌手被合并 → 拒绝。
	if _, err = f.store.MergeArtists(f.ctx, alpha, beta); err != nil {
		t.Fatal(err)
	}
	if err = f.store.UpsertArtistBiographyGuarded(f.ctx, alpha, alpha, "mbid-alpha", bio, nil); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("canonical merged: %v", err)
	}
}

// run 状态机：claim/complete 计数（含全部 skipped_* 归类）、幂等重放、
// resume 递增 token、恢复与限流等待预算。
func TestArtistBiographyBackfillRunStateMachine(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "Alpha")
	f.importArtistTrack(t, "B/01.flac", "Beta")
	f.importArtistTrack(t, "C/01.flac", "Gamma")
	f.importArtistTrack(t, "D/01.flac", "Delta")
	alpha := f.artistID(t, "Alpha")
	beta := f.artistID(t, "Beta")
	gamma := f.artistID(t, "Gamma")
	delta := f.artistID(t, "Delta")

	runID, err := f.store.CreateArtistBiographyBackfillRun(f.ctx, []ArtistBiographyBackfillCandidate{{ID: alpha, Name: "Alpha"}, {ID: beta, Name: "Beta"}, {ID: gamma, Name: "Gamma"}, {ID: delta, Name: "Delta"}})
	if err != nil {
		t.Fatal(err)
	}
	// 活动任务存在时禁止并发创建。
	if _, err = f.store.CreateArtistBiographyBackfillRun(f.ctx, []ArtistBiographyBackfillCandidate{{ID: alpha, Name: "Alpha"}}); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("duplicate run: %v", err)
	}
	outcomes := []string{"filled", "missing", "skipped_no_mbid", "skipped_fresh"}
	for _, outcome := range outcomes {
		item, e := f.store.ClaimArtistBiographyBackfillItem(f.ctx, runID)
		if e != nil {
			t.Fatal(e)
		}
		cp := ArtistBiographyBackfillCheckpoint{RunID: runID, ItemID: item.ID, ClaimToken: item.ClaimToken}
		if e = f.store.CompleteArtistBiographyBackfillItem(f.ctx, cp, outcome, ""); e != nil {
			t.Fatalf("complete %s: %v", outcome, e)
		}
		// 幂等：重放完成不报错、计数不错位。
		if e = f.store.CompleteArtistBiographyBackfillItem(f.ctx, cp, outcome, ""); e != nil {
			t.Fatalf("replay %s: %v", outcome, e)
		}
	}
	if _, err = f.store.ClaimArtistBiographyBackfillItem(f.ctx, runID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no pending: %v", err)
	}
	if err = f.store.TransitionArtistBiographyBackfillRun(f.ctx, runID, "complete", ""); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.DurableArtistBiographyBackfillRun(f.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.Processed != 4 || run.Filled != 1 || run.Missing != 1 || run.Skipped != 2 || run.Failed != 0 {
		t.Fatalf("run=%+v", run)
	}
	if _, err = f.store.UnfinishedArtistBiographyBackfillRun(f.ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unfinished after complete: %v", err)
	}

	// 暂停/恢复：in_progress 项回炉且 claim_token 递增，旧 worker 写入被拒。
	runID2, err := f.store.CreateArtistBiographyBackfillRun(f.ctx, []ArtistBiographyBackfillCandidate{{ID: alpha, Name: "Alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.store.ClaimArtistBiographyBackfillItem(f.ctx, runID2)
	if err != nil {
		t.Fatal(err)
	}
	cp := ArtistBiographyBackfillCheckpoint{RunID: runID2, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = f.store.TransitionArtistBiographyBackfillRun(f.ctx, runID2, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.TransitionArtistBiographyBackfillRun(f.ctx, runID2, "resume", ""); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CompleteArtistBiographyBackfillItem(f.ctx, cp, "filled", ""); !errors.Is(err, ErrArtistBiographyBackfillState) {
		t.Fatalf("old worker token must be refused: %v", err)
	}
	reclaimed, err := f.store.ClaimArtistBiographyBackfillItem(f.ctx, runID2)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.ClaimToken <= cp.ClaimToken {
		t.Fatalf("token must increase: %d -> %d", cp.ClaimToken, reclaimed.ClaimToken)
	}

	// 限流等待预算：连续 3 次即暂停并给自动恢复资格；轮次用尽后标记
	// rate_limit_exhausted 且不再具备自动恢复资格。
	cp2 := ArtistBiographyBackfillCheckpoint{RunID: runID2, ItemID: reclaimed.ID, ClaimToken: reclaimed.ClaimToken}
	deadline := time.Now().Add(time.Minute).UTC()
	for i := 0; i < 3; i++ {
		err = f.store.RecordArtistBiographyBackfillWait(f.ctx, cp2, "wikipedia", deadline, 0, true)
		if i < 2 && err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(err, ErrArtistBiographyBackfillBudget) {
		t.Fatalf("third response must exhaust budget: %v", err)
	}
	run, _ = f.store.DurableArtistBiographyBackfillRun(f.ctx, runID2)
	if run.Status != "paused" || run.PauseReason != "rate_limit_count" || !ArtistBiographyBackfillAutoResumeEligible(run) {
		t.Fatalf("run=%+v", run)
	}
	awaiting, err := f.store.ArtistBiographyBackfillRunsAwaitingAutoResume(f.ctx)
	if err != nil || len(awaiting) != 1 || awaiting[0].ID != runID2 {
		t.Fatalf("awaiting=%+v err=%v", awaiting, err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE artist_biography_backfill_runs SET auto_resume_count=? WHERE id=?`, MaxArtistBiographyBackfillAutoResumeRounds, runID2); err != nil {
		t.Fatal(err)
	}
	run, _ = f.store.DurableArtistBiographyBackfillRun(f.ctx, runID2)
	if ArtistBiographyBackfillAutoResumeEligible(run) {
		t.Fatalf("exhausted run must not auto resume: %+v", run)
	}

	// 启动恢复：无等待截止时间的 running 任务 → paused(server_restart)。
	if err = f.store.TransitionArtistBiographyBackfillRun(f.ctx, runID2, "cancel", "manual"); err != nil {
		t.Fatal(err)
	}
	runID3, err := f.store.CreateArtistBiographyBackfillRun(f.ctx, []ArtistBiographyBackfillCandidate{{ID: beta, Name: "Beta"}})
	if err != nil {
		t.Fatal(err)
	}
	if recovered, e := f.store.RecoverArtistBiographyBackfillRuns(f.ctx); e != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, e)
	}
	run, _ = f.store.DurableArtistBiographyBackfillRun(f.ctx, runID3)
	if run.Status != "paused" || run.PauseReason != "server_restart" {
		t.Fatalf("run=%+v", run)
	}
}
