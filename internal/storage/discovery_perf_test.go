package storage

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiscoverySignalsIndexPlansLazyListAndWriteLocks(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "DiscArtist")
	artistID := f.artistID(t, "DiscArtist")
	mbid := "11111111-1111-1111-1111-111111111111"
	f.confirmIdentity(t, artistID, "musicbrainz", mbid, "")

	var trackID, albumID int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT id, album_id FROM tracks LIMIT 1`).Scan(&trackID, &albumID); err != nil {
		t.Fatal(err)
	}
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "DiscWork", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddWorkTrack(f.ctx, work.ID, WorkTrackInput{TrackID: trackID, Role: "op"}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE release_group_rebuild SET status='ready' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	// 插入两条播放会话：一条 10 天前（应计入 90 天衰减分），一条 120 天前（应排除）
	recentAt := time.Now().UTC().Add(-10 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	oldAt := time.Now().UTC().Add(-120 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	for _, s := range []struct {
		sid, at string
	}{
		{"disc-sess-recent-001", recentAt},
		{"disc-sess-old-000001", oldAt},
	} {
		if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO playback_sessions(session_id, chain_id, client_id, client_kind, track_id, state, seq, duration_ms, started_at, last_heartbeat_at, ended_at, counted_at) VALUES(?, ?, 'c1', 'web', ?, 'ended', 1, 180000, ?, ?, ?, ?)`,
			s.sid, s.sid, trackID, s.at, s.at, s.at, s.at); err != nil {
			t.Fatal(err)
		}
	}

	// 1) EXPLAIN QUERY PLAN: 90 天播放信号查询均命中 idx_playback_sessions_counted，ListDiscovery 主查询无相关子查询
	artistPlan := explainQueryPlan(t, f.store, discoveryArtistPlays90dSQL)
	if !strings.Contains(artistPlan, "idx_playback_sessions_counted") {
		t.Fatalf("DiscoveryArtistSignals 90d query did not use idx_playback_sessions_counted:\n%s", artistPlan)
	}
	workPlan := explainQueryPlan(t, f.store, discoveryWorkPlays90dSQL)
	if !strings.Contains(workPlan, "idx_playback_sessions_counted") {
		t.Fatalf("DiscoveryWorkSignals 90d query did not use idx_playback_sessions_counted:\n%s", workPlan)
	}
	listPlan := explainQueryPlan(t, f.store, listDiscoveryCandidatesSQL)
	if strings.Contains(listPlan, "CORRELATED SCALAR SUBQUERY") {
		t.Fatalf("ListDiscovery main query still contains correlated subquery:\n%s", listPlan)
	}

	// 2) 90 天边界计算正确：仅计入 10 天前会话，不计入 120 天前会话
	artistSignals, err := f.store.DiscoveryArtistSignals(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := artistSignals[artistID]; s.Plays90d < 0.7 || s.Plays90d > 0.9 {
		t.Fatalf("artist Plays90d = %v, want ~0.79 (only 10-day-old play)", s.Plays90d)
	}
	workSignals, err := f.store.DiscoveryWorkSignals(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := workSignals[work.ID]; s.Plays90d < 0.7 || s.Plays90d > 0.9 {
		t.Fatalf("work Plays90d = %v, want ~0.79 (only 10-day-old play)", s.Plays90d)
	}

	// 3) 写入发现候选、验证分页惰性 JSON 反序列化、Ownership 去重及全标签页计数
	now := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)
	seeds := []DiscoverySeed{{ArtistID: artistID, Artist: "DiscArtist", MBID: mbid}}
	gen, err := f.store.BeginDiscoveryRefresh(f.ctx, seeds, true, now)
	if err != nil {
		t.Fatal(err)
	}
	rg1 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1"
	rg2 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2"
	rg3 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa3"
	rg4 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa4"
	rg5 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa5"
	groups := []DiscoveryReleaseGroup{
		{RGID: rg1, Title: "Album One", PrimaryType: "Album", SecondaryTypes: []string{"Soundtrack"}, Kind: "OST", FirstReleaseDate: "2025-05-01", ArtistCredit: "DiscArtist"},
		{RGID: rg2, Title: "Album Two", PrimaryType: "Album", SecondaryTypes: []string{}, Kind: "Album", FirstReleaseDate: "2024-03-01", ArtistCredit: "DiscArtist"},
		{RGID: rg3, Title: "Album Three", PrimaryType: "Single", SecondaryTypes: []string{}, Kind: "Single", FirstReleaseDate: "2023-01-01", ArtistCredit: "DiscArtist"},
		{RGID: rg4, Title: "Album Four Mapping", PrimaryType: "Album", SecondaryTypes: []string{"Compilation"}, Kind: "OST", FirstReleaseDate: "2022-07-01", ArtistCredit: "DiscArtist"},
		{RGID: rg5, Title: "Album Five Acquiring", PrimaryType: "Album", SecondaryTypes: []string{}, Kind: "Album", FirstReleaseDate: "2021-01-01", ArtistCredit: "DiscArtist"},
	}
	if err := f.store.SaveDiscoverySeed(f.ctx, gen, seeds[0], groups, "done", "", now); err != nil {
		t.Fatal(err)
	}
	// 注入一条 suggested 映射记录与一条进行中抓取任务，覆盖全部 5 个标签页
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO discovery_bangumi_music(subject_id,name,name_cn,date,artists_json,status,search_status,fetched_at) VALUES(9001,'OST JP','OST 中','2022-07-01','["DiscArtist"]','ok','hits',?)`, now.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO discovery_work_music(work_id,subject_id,relation,ordinal) VALUES(?,9001,'原声集',0)`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO discovery_bangumi_mappings(subject_id,rgid,status,score,evidence_json) VALUES(9001,?,'suggested',88,'["title_strict","date_exact"]')`, rg4); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateAcquisitionTask(f.ctx, AcquisitionTask{
		IdempotencyKey: "disc-perf-acq-rg5",
		RGID:           rg5,
		Title:          "Album Five Acquiring",
		Artist:         "DiscArtist",
		Kind:           "Album",
		TorrentTitle:   "Album Five [FLAC]",
		TorrentSize:    1024,
	}); err != nil {
		t.Fatal(err)
	}
	cands := []DiscoveryCandidate{
		{DiscoveryReleaseGroup: groups[0], SeedKind: "artist", SeedArtistID: artistID, Score: 10, Lane: "exploit", Rank: 1, Reasons: []DiscoveryReason{{Code: "favorite_artist", Artist: "DiscArtist"}}},
		{DiscoveryReleaseGroup: groups[1], SeedKind: "artist", SeedArtistID: artistID, Score: 8, Lane: "exploit", Rank: 2, Reasons: []DiscoveryReason{{Code: "owned_albums", Artist: "DiscArtist", N: 1}}},
		{DiscoveryReleaseGroup: groups[2], SeedKind: "artist", SeedArtistID: artistID, Score: 6, Lane: "explore", Rank: 3, Reasons: []DiscoveryReason{{Code: "explore", Artist: "DiscArtist"}}},
	}
	if err := f.store.SaveDiscoveryCandidates(f.ctx, gen, seeds, cands, []DiscoveryMatch{{RGID: rg2, AlbumID: albumID, Reason: "title_artist"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DiscoveryIgnore(f.ctx, rg3, true); err != nil {
		t.Fatal(err)
	}

	// Ownership 重复输入应去重且返回正确结果
	own, err := f.store.DiscoveryOwnership(f.ctx, []string{rg1, rg1, rg2, rg3, rg3, rg5})
	if err != nil || len(own) != 4 || !own[rg3].Ignored || len(own[rg2].Possible) != 1 || !own[rg5].Acquiring {
		t.Fatalf("DiscoveryOwnership = %+v err=%v", own, err)
	}

	// 校验 recommended / possible / ignored / mapping / acquiring 全标签页惰性解码与计数
	recItems, counts, err := f.store.ListDiscovery(f.ctx, "recommended", "all", 10, 0)
	if err != nil || counts.Recommended != 1 || counts.Possible != 1 || counts.Ignored != 1 || counts.Mapping != 1 || counts.Acquiring != 1 {
		t.Fatalf("recommended counts = %+v err=%v", counts, err)
	}
	if len(recItems) != 1 || recItems[0].RGID != rg1 || len(recItems[0].SecondaryTypes) != 1 || recItems[0].SecondaryTypes[0] != "Soundtrack" || len(recItems[0].Reasons) != 1 || recItems[0].Year != 2025 {
		t.Fatalf("recommended item = %+v", recItems)
	}
	possItems, _, err := f.store.ListDiscovery(f.ctx, "possible", "all", 10, 0)
	if err != nil || len(possItems) != 1 || possItems[0].RGID != rg2 || len(possItems[0].Possible) != 1 {
		t.Fatalf("possible items = %+v err=%v", possItems, err)
	}
	ignItems, _, err := f.store.ListDiscovery(f.ctx, "ignored", "all", 10, 0)
	if err != nil || len(ignItems) != 1 || ignItems[0].RGID != rg3 {
		t.Fatalf("ignored items = %+v err=%v", ignItems, err)
	}
	mapItems, mapCounts, err := f.store.ListDiscovery(f.ctx, "mapping", "all", 10, 0)
	if err != nil || mapCounts.Mapping != 1 || len(mapItems) != 1 || mapItems[0].RGID != rg4 || mapItems[0].SubjectNameCN != "OST 中" || len(mapItems[0].SecondaryTypes) != 1 || mapItems[0].SecondaryTypes[0] != "Compilation" || len(mapItems[0].MappingEvidence) != 2 {
		t.Fatalf("mapping items = %+v counts=%+v err=%v", mapItems, mapCounts, err)
	}
	acqItems, acqCounts, err := f.store.ListDiscovery(f.ctx, "acquiring", "all", 10, 0)
	if err != nil || acqCounts.Acquiring != 1 || len(acqItems) != 1 || acqItems[0].RGID != rg5 {
		t.Fatalf("acquiring items = %+v counts=%+v err=%v", acqItems, acqCounts, err)
	}

	// 4) Discovery 写事务与播放落库并发无快照锁冲突
	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			if err := f.store.UpdateDiscoveryCover(f.ctx, gen, seeds, rg1, "cached", "jpg", now); err != nil {
				errCh <- fmt.Errorf("update cover %d: %w", i, err)
				return
			}
			if err := f.store.DiscoveryMessage(f.ctx, gen, fmt.Sprintf("msg-%d", i)); err != nil {
				errCh <- fmt.Errorf("message %d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			if _, err := f.store.RecordPlaybackEvent(f.ctx, PlaybackEventInput{
				ClientID: "disc-cli", ClientKind: "web", SessionID: fmt.Sprintf("disc-play-%04d", i), Seq: 1,
				Type: "start", State: "playing", TrackID: trackID, PositionMillis: 0, DurationMillis: 180000,
			}); err != nil {
				errCh <- fmt.Errorf("playback %d: %w", i, err)
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
