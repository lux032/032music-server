package storage

import (
	"testing"
)

// 跨批次整体审查（M1/D68、D69、L2、L3、L4）的回归测试。

// M1/D68：手动放进系列的本地 auto 作品失去引用后，CleanupAutoWorks 不得
// 删除它；它应出现在无引用受保护列表里，系列、名字、成员保持不变。
func TestCleanupAutoWorksProtectsManualSeriesMember(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, e := s.ListWorks(ctx, WorkFilters{})
	if e != nil || len(works) != 1 {
		t.Fatalf("works=%+v %v", works, e)
	}
	workID := works[0].ID
	var origin string
	if e = s.db.QueryRowContext(ctx, `SELECT origin FROM works WHERE id=?`, workID).Scan(&origin); e != nil {
		t.Fatal(e)
	}
	if origin != "auto" {
		t.Fatalf("fixture work origin=%q, want auto", origin)
	}
	// 用户手动把这部 auto 作品放进一个（改名的）系列。
	seriesID, e := s.CreateWorkSeries(ctx, "我的空洞骑士", []int64{workID})
	if e != nil {
		t.Fatal(e)
	}
	// 删掉引用它的专辑，再跑重算/清理。
	if _, e = s.DeleteAlbums(ctx, []int64{album}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RefreshAlbumWorks(ctx, false); e != nil {
		t.Fatal(e)
	}
	var stats RefreshStats
	if e = s.CleanupAutoWorks(ctx, &stats); e != nil {
		t.Fatal(e)
	}
	if stats.WorksDeleted != 0 {
		t.Fatalf("protected work deleted: stats=%+v", stats)
	}
	if _, e = s.WorkByID(ctx, workID); e != nil {
		t.Fatalf("manual series member work must survive: %v", e)
	}
	series, members, e := s.SeriesForWork(ctx, workID)
	if e != nil {
		t.Fatalf("series membership lost: %v", e)
	}
	if series.ID != seriesID || series.Title != "我的空洞骑士" || series.TitleSource != "manual" {
		t.Fatalf("series changed: %+v", series)
	}
	if len(members) != 1 || members[0].Work.ID != workID || members[0].Source != "manual" {
		t.Fatalf("members changed: %+v", members)
	}
	// 无引用受保护列表（管理页“无引用作品”）自然包含这类作品。
	unreferenced, total, e := s.UnreferencedProtectedWorks(ctx)
	if e != nil || total != 1 || len(unreferenced) != 1 || unreferenced[0].ID != workID {
		t.Fatalf("unreferenced=%+v total=%d %v", unreferenced, total, e)
	}
}

// M1/D68：被拆出/解散而留下系列锁的 auto 作品同样受保护。
func TestCleanupAutoWorksProtectsSeriesLocked(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Hollow Knight Original Soundtrack", "Track")
	if _, e := s.RefreshAlbumWorks(ctx, true); e != nil {
		t.Fatal(e)
	}
	works, e := s.ListWorks(ctx, WorkFilters{})
	if e != nil || len(works) != 1 {
		t.Fatalf("works=%+v %v", works, e)
	}
	workID := works[0].ID
	if _, e = s.db.ExecContext(ctx, `INSERT INTO work_series_locks(work_id) VALUES(?)`, workID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DeleteAlbums(ctx, []int64{album}); e != nil {
		t.Fatal(e)
	}
	var stats RefreshStats
	if e = s.CleanupAutoWorks(ctx, &stats); e != nil {
		t.Fatal(e)
	}
	if stats.WorksDeleted != 0 || stats.ProtectedUnreferenced != 1 {
		t.Fatalf("locked work cleanup stats=%+v", stats)
	}
	if _, e = s.WorkByID(ctx, workID); e != nil {
		t.Fatalf("series-locked work must survive: %v", e)
	}
	// 对照：没有锁、没有系列、没有人工痕迹的 auto 作品照旧被清理。
	plain, e := s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Plain Auto','plain auto','anime','auto')`)
	if e != nil {
		t.Fatal(e)
	}
	plainID, _ := plain.LastInsertId()
	stats = RefreshStats{}
	if e = s.CleanupAutoWorks(ctx, &stats); e != nil {
		t.Fatal(e)
	}
	if stats.WorksDeleted != 1 {
		t.Fatalf("plain auto work must be cleaned: stats=%+v", stats)
	}
	if _, e = s.WorkByID(ctx, plainID); e == nil {
		t.Fatalf("plain auto work %d must be deleted", plainID)
	}
	if _, e = s.WorkByID(ctx, workID); e != nil {
		t.Fatalf("locked work deleted alongside: %v", e)
	}
}

// D69：改名的系列移出到只剩 1 个成员时保留；0 个成员时仍删除。
func TestRenamedSeriesSurvivesOneMemberButNotZero(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Keep A", 2019)
	b := mustWork(t, store, ctx, "Keep B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.RenameWorkSeries(ctx, series.ID, "改名系列"); err != nil {
		t.Fatal(err)
	}
	// 移出 B：只剩 1 个成员的改名系列保留，名字与成员不变。
	if err := store.DetachWorkFromSeries(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	fresh := onlySeries(t, store, ctx)
	if fresh.ID != series.ID || fresh.Title != "改名系列" || fresh.TitleSource != "manual" {
		t.Fatalf("renamed series not preserved: %+v", fresh)
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 1 || members[a.ID] != "auto" {
		t.Fatalf("members=%+v", members)
	}
	// 再移出 A：0 个成员时仍然删除。
	if err := store.DetachWorkFromSeries(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("empty series left=%+v", all)
	}
}

// D69：auto 名字的系列保持原行为，少于 2 个成员就删。
func TestAutoNamedSeriesStillDeletedAtOneMember(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Auto A", 2019)
	b := mustWork(t, store, ctx, "Auto B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DetachWorkFromSeries(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("auto-named single-member series left=%+v", all)
	}
}

// D69：作品被删除导致改名系列只剩 1 个成员时同样保留。
func TestRenamedSeriesSurvivesWorkDeletion(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Del A", 2019)
	b := mustWork(t, store, ctx, "Del B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.RenameWorkSeries(ctx, series.ID, "删除场景改名"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWork(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	fresh := onlySeries(t, store, ctx)
	if fresh.ID != series.ID || fresh.Title != "删除场景改名" {
		t.Fatalf("renamed series deleted with its member: %+v", fresh)
	}
	if members := seriesIDs(t, store, ctx, series.ID); len(members) != 1 || members[a.ID] != "auto" {
		t.Fatalf("members=%+v", members)
	}
}

// D69 + D59：只剩 1 个 auto 成员（未锁定）的改名系列仍可被组件认领并补回成员。
func TestRenamedSingleMemberSeriesClaimedAndRefilled(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Claim A", 2019)
	b := mustWork(t, store, ctx, "Claim B", 2021)
	c := mustWork(t, store, ctx, "Claim C", 2022)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.RenameWorkSeries(ctx, series.ID, "认领系列"); err != nil {
		t.Fatal(err)
	}
	// 移出 B（B 被锁定），改名系列只剩 auto 成员 A。
	if err := store.DetachWorkFromSeries(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	// Bangumi 现在把 A 与 C 连成一部：组件 {A,C} 认领该系列并补回成员，
	// 名字保持用户所改。
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, c.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MembersAdded != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	fresh := onlySeries(t, store, ctx)
	if fresh.ID != series.ID || fresh.Title != "认领系列" {
		t.Fatalf("claimed series changed: %+v", fresh)
	}
	members := seriesIDs(t, store, ctx, series.ID)
	if len(members) != 2 || members[a.ID] != "auto" || members[c.ID] != "auto" {
		t.Fatalf("members=%+v", members)
	}
}

// L2：分量里含有已删除的作品时，ApplyAutoSeries 跳过它，其余部分正常应用。
func TestApplyAutoSeriesSkipsDeletedComponentWorks(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Live A", 2019)
	b := mustWork(t, store, ctx, "Live B", 2021)
	gone := mustWork(t, store, ctx, "Deleted C", 2022)
	if err := store.DeleteWork(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	// 第 3 阶段（新建系列）：含已删除作品的分量不再因外键冲突整批回滚。
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID, gone.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SeriesCreated != 1 || stats.MembersAdded != 2 {
		t.Fatalf("stats=%+v", stats)
	}
	series := onlySeries(t, store, ctx)
	if members := seriesIDs(t, store, ctx, series.ID); len(members) != 2 {
		t.Fatalf("members=%+v", members)
	}
	// 第 2 阶段（向既有系列补成员）：同理跳过已删除作品。
	d := mustWork(t, store, ctx, "Live D", 2023)
	gone2 := mustWork(t, store, ctx, "Deleted E", 2024)
	if err := store.DeleteWork(ctx, gone2.ID); err != nil {
		t.Fatal(err)
	}
	stats, err = store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, gone2.ID, d.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MembersAdded != 1 {
		t.Fatalf("phase-2 stats=%+v", stats)
	}
	if members := seriesIDs(t, store, ctx, series.ID); len(members) != 3 || members[d.ID] != "auto" {
		t.Fatalf("phase-2 members=%+v", members)
	}
}

// L2：ReplaceSeriesSuggestions 过滤分量里已删除的作品，其余建议正常写入。
func TestReplaceSeriesSuggestionsSkipsDeletedWorks(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Sug Live A", 2019)
	b := mustWork(t, store, ctx, "Sug Live B", 2020)
	gone := mustWork(t, store, ctx, "Sug Deleted", 2021)
	if err := store.DeleteWork(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	inputs := []SeriesSuggestionInput{
		{WorkA: a.ID, WorkB: gone.ID, SubjectA: 11, SubjectB: 99, RelationAB: "游戏", RelationBA: "动画", Kind: "cross"},
		{WorkA: a.ID, WorkB: b.ID, SubjectA: 11, SubjectB: 22, RelationAB: "游戏", RelationBA: "动画", Kind: "cross"},
	}
	if err := store.ReplaceSeriesSuggestions(ctx, 1, inputs, []int64{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	suggestions, err := store.PendingSeriesSuggestions(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(suggestions) != 1 || suggestions[0].WorkB.ID != b.ID {
		t.Fatalf("suggestions=%+v, want only the valid pair", suggestions)
	}
}

// L3：目标专辑已有 confirmed Bangumi 候选或 manual/bangumi 关联时，从源专辑
// 搬来的待审候选被丢弃；已审核的候选仍然搬运。
func TestMergeAlbumsDropsPendingCandidatesWhenTargetDecided(t *testing.T) {
	f := newAlbumMergeFixture(t)
	ctx := f.ctx
	f.importFile(t, "A/01.flac", "Target Album", "Song", 1, 1)
	f.importFile(t, "B/01.flac", "Source Album", "Song", 1, 1)
	targetID := f.albumID(t, "Target Album")
	sourceID := f.albumID(t, "Source Album")
	// 目标已有 confirmed 候选；源有待审候选与已拒绝候选。
	if err := f.store.SaveAlbumSubjectCandidates(ctx, targetID, []AlbumSubjectCandidate{{ExternalID: "E1", Title: "Decided", Score: 90}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(ctx, `UPDATE album_subject_candidates SET status='confirmed' WHERE album_id=? AND external_id='E1'`, targetID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveAlbumSubjectCandidates(ctx, sourceID, []AlbumSubjectCandidate{{ExternalID: "E2", Title: "Pending", Score: 80}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveAlbumSubjectCandidates(ctx, sourceID, []AlbumSubjectCandidate{{ExternalID: "E3", Title: "Reviewed", Score: 70}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(ctx, `UPDATE album_subject_candidates SET status='rejected' WHERE album_id=? AND external_id='E3'`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MergeAlbums(ctx, targetID, []int64{sourceID}); err != nil {
		t.Fatal(err)
	}
	cands, err := f.store.AlbumSubjectCandidates(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	byExternal := map[string]string{}
	for _, c := range cands {
		byExternal[c.ExternalID] = c.Status
	}
	if byExternal["E1"] != "confirmed" {
		t.Fatalf("target confirmed candidate lost: %+v", byExternal)
	}
	if _, moved := byExternal["E2"]; moved {
		t.Fatalf("pending candidate moved onto a decided target: %+v", byExternal)
	}
	if byExternal["E3"] != "rejected" {
		t.Fatalf("reviewed candidate must still move: %+v", byExternal)
	}
}

// L3 对照：目标专辑有 manual 专辑关联时同样丢弃待审候选；目标干净时待审候选照常搬运。
func TestMergeAlbumsPendingCandidatesMoveRules(t *testing.T) {
	f := newAlbumMergeFixture(t)
	ctx := f.ctx
	f.importFile(t, "A/01.flac", "Linked Target", "Song", 1, 1)
	f.importFile(t, "B/01.flac", "Linked Source", "Song", 1, 1)
	f.importFile(t, "C/01.flac", "Clean Target", "Song", 1, 1)
	f.importFile(t, "D/01.flac", "Clean Source", "Song", 1, 1)
	linkedTarget := f.albumID(t, "Linked Target")
	linkedSource := f.albumID(t, "Linked Source")
	cleanTarget := f.albumID(t, "Clean Target")
	cleanSource := f.albumID(t, "Clean Source")
	work, err := f.store.CreateWork(ctx, WorkInput{Title: "Manual Show", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.AddWorkAlbum(ctx, work.ID, linkedTarget, ""); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveAlbumSubjectCandidates(ctx, linkedSource, []AlbumSubjectCandidate{{ExternalID: "P1", Title: "Pending One", Score: 80}}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveAlbumSubjectCandidates(ctx, cleanSource, []AlbumSubjectCandidate{{ExternalID: "P2", Title: "Pending Two", Score: 80}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.MergeAlbums(ctx, linkedTarget, []int64{linkedSource}); err != nil {
		t.Fatal(err)
	}
	cands, err := f.store.AlbumSubjectCandidates(ctx, linkedTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("pending candidate moved onto a manually linked target: %+v", cands)
	}
	if _, err = f.store.MergeAlbums(ctx, cleanTarget, []int64{cleanSource}); err != nil {
		t.Fatal(err)
	}
	cands, err = f.store.AlbumSubjectCandidates(ctx, cleanTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].ExternalID != "P2" || cands[0].Status != "candidate" {
		t.Fatalf("pending candidate must move onto a clean target: %+v", cands)
	}
}

// L4：SetSuppressedTrackBangumiMiss 在事务里复核抑制状态；抑制已解除时什么
// 都不写（不写 miss、不动候选）。
func TestSetSuppressedTrackBangumiMissRechecksSuppression(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, "Album", "Track")
	var trackID int64
	if e := s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE album_id=?`, album).Scan(&trackID); e != nil {
		t.Fatal(e)
	}
	target := TrackBangumiTarget{ID: trackID, AlbumID: album, Title: "Track", Fingerprint: "fp"}
	links := []BangumiSuppressedLink{{Music: "555", Tie: BangumiTieup{SubjectID: 777, Role: "op"}}}
	if e := s.SaveTrackSubjectCandidates(ctx, trackID, []TrackSubjectCandidate{{ExternalID: "555", Title: "Stale Cand", MatchKind: "exact"}}); e != nil {
		t.Fatal(e)
	}
	// 抑制存在：miss 写入，陈旧候选被清掉。
	if _, e := s.db.ExecContext(ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,'bangumi:555')`, trackID); e != nil {
		t.Fatal(e)
	}
	if e := s.SetSuppressedTrackBangumiMiss(ctx, target, links); e != nil {
		t.Fatal(e)
	}
	var misses, staleCands int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=? AND source='bangumi'`, trackID).Scan(&misses); e != nil || misses != 1 {
		t.Fatalf("misses=%d %v", misses, e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_subject_candidates WHERE track_id=? AND status='candidate'`, trackID).Scan(&staleCands); e != nil || staleCands != 0 {
		t.Fatalf("stale candidates=%d %v", staleCands, e)
	}
	// 判定后抑制被解除：什么都不写（miss 删除后不再重建，候选也不再被清）。
	if _, e := s.db.ExecContext(ctx, `DELETE FROM track_work_suppressions WHERE track_id=?`, trackID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(ctx, `DELETE FROM track_enrichment_misses WHERE track_id=?`, trackID); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveTrackSubjectCandidates(ctx, trackID, []TrackSubjectCandidate{{ExternalID: "555", Title: "New Cand", MatchKind: "exact"}}); e != nil {
		t.Fatal(e)
	}
	if e := s.SetSuppressedTrackBangumiMiss(ctx, target, links); e != nil {
		t.Fatal(e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=?`, trackID).Scan(&misses); e != nil || misses != 0 {
		t.Fatalf("miss written after suppression lifted: misses=%d %v", misses, e)
	}
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_subject_candidates WHERE track_id=? AND status='candidate'`, trackID).Scan(&staleCands); e != nil || staleCands != 1 {
		t.Fatalf("candidates touched after suppression lifted: %d %v", staleCands, e)
	}
}

// H1（D69 方案 A）：改名系列拆出后只剩 1 个 auto 成员时，下一轮归组候选 <2
// 也保留这个成员（候选 ∩ 现有 auto 成员），系列、名字、成员都不变；B 被
// 删除后用单例分量再跑结果相同。
func TestRenamedSeriesKeepsAutoMemberWhenCandidatesRunThin(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Hold A", 2019)
	b := mustWork(t, store, ctx, "Hold B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	series := onlySeries(t, store, ctx)
	if err := store.RenameWorkSeries(ctx, series.ID, "保留系列"); err != nil {
		t.Fatal(err)
	}
	if err := store.DetachWorkFromSeries(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	// 下一轮归组：分量 {A,B}，B 被锁定，候选只剩 {A}。
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MembersRemoved != 0 {
		t.Fatalf("member removed: stats=%+v", stats)
	}
	fresh := onlySeries(t, store, ctx)
	if fresh.ID != series.ID || fresh.Title != "保留系列" {
		t.Fatalf("renamed series lost: %+v", fresh)
	}
	if members := seriesIDs(t, store, ctx, series.ID); len(members) != 1 || members[a.ID] != "auto" {
		t.Fatalf("members=%+v", members)
	}
	// B 被删除后用 {A} 再跑一轮，结果相同。
	if err := store.DeleteWork(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	stats, err = store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MembersRemoved != 0 {
		t.Fatalf("member removed after delete: stats=%+v", stats)
	}
	fresh = onlySeries(t, store, ctx)
	if fresh.ID != series.ID || fresh.Title != "保留系列" {
		t.Fatalf("renamed series lost after delete: %+v", fresh)
	}
	if members := seriesIDs(t, store, ctx, series.ID); len(members) != 1 || members[a.ID] != "auto" {
		t.Fatalf("members after delete=%+v", members)
	}
}

// H1 对照：auto 名字的系列在同样场景下照旧被删除，也不会被单候选分量复活；
// 两个单例分量让既有 auto 系列正常退化删除（H1 不改变 auto 名字系列行为）。
func TestAutoNamedSeriesDropsMemberWhenCandidatesRunThin(t *testing.T) {
	store, ctx := newSeriesStore(t)
	a := mustWork(t, store, ctx, "Thin A", 2019)
	b := mustWork(t, store, ctx, "Thin B", 2021)
	if _, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	// 场景一：两个单例分量——auto 名字系列照旧退化删除。
	stats, err := store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID}, {b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MembersRemoved != 2 {
		t.Fatalf("auto-named series must lose both members: stats=%+v", stats)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("auto-named series left=%+v", all)
	}
	// 场景二：拆出 B（auto 名字系列当即删除），再用 {A,B} 跑一轮不复活。
	if _, err = store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	if err = store.DetachWorkFromSeries(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("auto-named series should be gone after detach: %+v", all)
	}
	if _, err = store.ApplyAutoSeries(ctx, 0, [][]int64{{a.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.ListSeries(ctx); len(all) != 0 {
		t.Fatalf("auto-named series revived by single-candidate component: %+v", all)
	}
}

// Low-1：合并收尾统一删除目标上的待审候选，两种合并顺序结果一致。
func TestMergeAlbumsFinalCleanupOrderIndependent(t *testing.T) {
	run := func(t *testing.T, order []string) map[string]string {
		f := newAlbumMergeFixture(t)
		ctx := f.ctx
		f.importFile(t, "T/01.flac", "Order Target", "Song", 1, 1)
		f.importFile(t, "S1/01.flac", "Decided Source", "Song", 1, 1)
		f.importFile(t, "S2/01.flac", "Pending Source", "Song", 1, 1)
		targetID := f.albumID(t, "Order Target")
		decidedID := f.albumID(t, "Decided Source")
		pendingID := f.albumID(t, "Pending Source")
		// 源 1 带 confirmed 候选，源 2 带待审候选。
		if err := f.store.SaveAlbumSubjectCandidates(ctx, decidedID, []AlbumSubjectCandidate{{ExternalID: "C1", Title: "Decided", Score: 90}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.db.ExecContext(ctx, `UPDATE album_subject_candidates SET status='confirmed' WHERE album_id=? AND external_id='C1'`, decidedID); err != nil {
			t.Fatal(err)
		}
		if err := f.store.SaveAlbumSubjectCandidates(ctx, pendingID, []AlbumSubjectCandidate{{ExternalID: "P1", Title: "Pending", Score: 80}}); err != nil {
			t.Fatal(err)
		}
		var sources []int64
		for _, name := range order {
			if name == "decided" {
				sources = append(sources, decidedID)
			} else {
				sources = append(sources, pendingID)
			}
		}
		if _, err := f.store.MergeAlbums(ctx, targetID, sources); err != nil {
			t.Fatal(err)
		}
		cands, err := f.store.AlbumSubjectCandidates(ctx, targetID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, c := range cands {
			out[c.ExternalID] = c.Status
		}
		return out
	}
	first := run(t, []string{"decided", "pending"})
	second := run(t, []string{"pending", "decided"})
	for name, got := range map[string]map[string]string{"decided-first": first, "pending-first": second} {
		if got["C1"] != "confirmed" {
			t.Fatalf("%s: confirmed candidate lost: %+v", name, got)
		}
		if _, exists := got["P1"]; exists {
			t.Fatalf("%s: pending candidate survived the final cleanup: %+v", name, got)
		}
	}
}
