package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestTaggingStampSetBasedByteParityAndPlans(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "tagging_parity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	// 1) 多主唱 + 多专辑歌手 + 流派 + 歌词 + 词曲编
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "Multi/01.flac", FileSize: 1234, ModifiedAtNS: 100, HasExternalLRC: true,
		Metadata: metadata.AudioMetadata{
			Title: "Multi Track", Album: "Multi Album",
			Artists: []string{"Beta", "Alpha"}, AlbumArtists: []string{"Zeta", "Gamma"},
			Composer: "CompA", Lyricist: "LyrA", Arranger: "ArrA",
			Genres: []string{"Rock", "J-Pop"}, Lyrics: "line1\nline2",
			DurationMillis: 215400, Year: 2024, DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 2) 同专辑第二首，测试专辑曲目计数 + 流派人工覆盖 + 作品关联 + 声学特征
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "Multi/02.flac", FileSize: 2345, ModifiedAtNS: 200, HasExternalLRC: false,
		Metadata: metadata.AudioMetadata{
			Title: "Second Track", Album: "Multi Album",
			Artists: []string{"Alpha"}, AlbumArtists: []string{"Zeta", "Gamma"},
			Genres: []string{"Pop"}, DurationMillis: 180000, Year: 2024, DiscNumber: 1, TrackNumber: 2,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 3) 独立专辑，清空 albums.performed_by 验证 taggingAlbumArtistSQL 回落到 album_artists PK 拼接顺序
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "Solo/01.flac", FileSize: 3456, ModifiedAtNS: 300,
		Metadata: metadata.AudioMetadata{
			Title: "Null PerformedBy Track", Album: "Null PerformedBy Album",
			Artists: []string{"Gamma", "Alpha"}, AlbumArtists: []string{"Zeta", "Alpha"},
			DurationMillis: 195000, DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// 4) 无 track_artists、无 album_artists 且 performed_by 为 NULL -> 'Unknown Artist' 与 ''
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "Anon/01.flac", FileSize: 4567, ModifiedAtNS: 400,
		Metadata: metadata.AudioMetadata{
			Title: "Anonymous Track", Album: "Anonymous Album",
			DurationMillis: 90000, DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}

	var trackIDs []int64
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tracks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		trackIDs = append(trackIDs, id)
	}
	rows.Close()
	if len(trackIDs) != 4 {
		t.Fatalf("tracks = %d, want 4", len(trackIDs))
	}

	// 清空第 3 张专辑的 performed_by / user_performed_by，强制命中 album_artists fallback
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET performed_by = NULL, user_performed_by = NULL WHERE title IN ('Null PerformedBy Album', 'Anonymous Album')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM track_artists WHERE track_id = ?`, trackIDs[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM album_artists WHERE album_id = (SELECT album_id FROM tracks WHERE id = ?)`, trackIDs[3]); err != nil {
		t.Fatal(err)
	}
	// 为第 2 首添加流派覆盖、作品关联、专辑作品关联及声学特征
	if err := s.UpdateTrack(ctx, trackIDs[1], "Second Track Edited", 1, 2, "UserComp", "", []string{"Anime", "Electronic"}); err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateWork(ctx, WorkInput{Title: "Test Work", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkTrack(ctx, work.ID, WorkTrackInput{TrackID: trackIDs[1], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	var multiAlbumID int64
	if err := s.db.QueryRowContext(ctx, `SELECT album_id FROM tracks WHERE id = ?`, trackIDs[1]).Scan(&multiAlbumID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO album_works(album_id, work_id, role, source) VALUES(?, ?, 'other', 'manual')`, multiAlbumID, work.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTrackAudioFeatures(ctx, TrackAudioFeatures{
		TrackID: trackIDs[1], Fingerprint: "fp-2", AnalysisVersion: "v1", Mode: "full",
		DurationMs: 180000, Windows: 1, Patches: 9, Genre: make([]byte, 8), MoodTheme: make([]byte, 8),
		Instrument: make([]byte, 8), Embedding: make([]byte, 8), HeadsJSON: `{}`,
		MusicnnValence: ptrFloat64(4.25), MusicnnArousal: ptrFloat64(6.75),
		Valence: ptrFloat64(4.25), Arousal: ptrFloat64(6.75), AVSource: "musicnn",
	}); err != nil {
		t.Fatal(err)
	}

	// 参考公式（原 taggingStampSQL）逐曲计算
	refStamps := map[int64]string{}
	for _, id := range trackIDs {
		var ref string
		if err := s.db.QueryRowContext(ctx, `SELECT `+taggingStampSQL+`
			FROM tracks t JOIN albums a ON a.id = t.album_id
			LEFT JOIN track_audio_features f ON f.track_id = t.id WHERE t.id = ?`, id).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		refStamps[id] = ref
	}

	// 1) TrackTaggingStamp 逐字节一致
	for _, id := range trackIDs {
		got, ok, err := s.TrackTaggingStamp(ctx, id)
		if err != nil || !ok || got != refStamps[id] {
			t.Fatalf("TrackTaggingStamp(%d) = (%q,%v,%v), want %q", id, got, ok, err, refStamps[id])
		}
	}

	// 2) taggingBaseSetSQL 集合 CTE 逐字节一致
	setRows, err := s.db.QueryContext(ctx, taggingBaseSetSQL+` SELECT track_id, stamp FROM base`, 0)
	if err != nil {
		t.Fatal(err)
	}
	setCount := 0
	for setRows.Next() {
		var id int64
		var got string
		if err := setRows.Scan(&id, &got); err != nil {
			t.Fatal(err)
		}
		setCount++
		if got != refStamps[id] {
			t.Fatalf("taggingBaseSetSQL stamp mismatch for track %d:\n got: %q\nwant: %q", id, got, refStamps[id])
		}
	}
	setRows.Close()
	if setCount != len(trackIDs) {
		t.Fatalf("set base rows = %d, want %d", setCount, len(trackIDs))
	}

	// 3) Phase 1 + Phase 2 混合优先级与排序验证
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	// 打标 track 0 (旧 stamp)、track 1 (最新)、track 2 (旧版本)；track 3 保持未打标
	markTagged(t, s, trackIDs[0], "outdated-stamp")
	markTagged(t, s, trackIDs[1], refStamps[trackIDs[1]])
	if err := s.SaveTrackTagResult(ctx, TrackTagMeta{
		TrackID: trackIDs[2], SourceStamp: refStamps[trackIDs[2]], InputHash: "h",
		VocabVersion: 0, PromptVersion: 1, Model: "m",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	cands, err := s.NextTaggingCandidates(ctx, false, 1, 1, now, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 3 {
		t.Fatalf("candidates = %+v, want 3 (fresh + version stale + stamp stale)", cands)
	}
	if !cands[0].Fresh || cands[0].TrackID != trackIDs[3] || cands[0].Stamp != refStamps[trackIDs[3]] {
		t.Fatalf("first candidate must be fresh track 3: %+v", cands[0])
	}
	if cands[1].Fresh || cands[2].Fresh {
		t.Fatalf("remaining candidates must be non-fresh: %+v", cands[1:])
	}
	// 全量版本升级快速路径（vocab=2，所有已打标曲目版本不匹配）
	markTagged(t, s, trackIDs[3], refStamps[trackIDs[3]])
	vBump, err := s.NextTaggingCandidates(ctx, false, 2, 1, now, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(vBump) != 2 || vBump[0].Fresh || vBump[1].Fresh {
		t.Fatalf("version bump fast path = %+v", vBump)
	}
	for _, c := range vBump {
		if c.Stamp != refStamps[c.TrackID] {
			t.Fatalf("version bump stamp mismatch for %d", c.TrackID)
		}
	}

	// 4) EXPLAIN QUERY PLAN 断言：Phase 1 与全量版本升级快速路径均命中 idx_tracks_added_id 且无排序
	ts := AnalysisTimestamp(now)
	phase1Plan := explainQueryPlan(t, s, buildFreshTaggingCandidatesQuery(""), ts, 0, 8)
	if !strings.Contains(phase1Plan, "idx_tracks_added_id") {
		t.Fatalf("Phase 1 NextTaggingCandidates did not use idx_tracks_added_id:\n%s", phase1Plan)
	}
	versionFastPlan := explainQueryPlan(t, s, buildAllVersionStaleTaggingCandidatesQuery(""), ts, 0, 8)
	if !strings.Contains(versionFastPlan, "idx_tracks_added_id") {
		t.Fatalf("Version-bump fast path did not use idx_tracks_added_id:\n%s", versionFastPlan)
	}
	singleStampPlan := explainQueryPlan(t, s, `SELECT `+taggingStampSQL+`
		FROM tracks t JOIN albums a ON a.id = t.album_id
		LEFT JOIN track_audio_features f ON f.track_id = t.id
		WHERE t.id = ? AND EXISTS (SELECT 1 FROM audio_files af WHERE af.track_id = t.id AND af.status = 'available')`, trackIDs[0])
	if !strings.Contains(singleStampPlan, "SEARCH t USING INTEGER PRIMARY KEY") {
		t.Fatalf("TrackTaggingStamp did not use primary key seek:\n%s", singleStampPlan)
	}
}

func TestTaggingRecentResultsBatchHydration(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 3)
	for i, id := range ids {
		if err := s.SaveTrackTagResult(ctx, TrackTagMeta{
			TrackID: id, SourceStamp: fmt.Sprintf("s%d", i), InputHash: "h",
			VocabVersion: 1, PromptVersion: 1, Model: "m",
		}, []TrackTag{
			{Kind: "mood", Tag: "cheerful", Weight: 0.7, Confidence: 0.9, Source: "llm"},
			{Kind: "style", Tag: "pop", Weight: 1.0, Confidence: 0.8, Source: "llm"},
		}, []TrackTag{
			{Kind: "style", Tag: "rock", Weight: 0.5, Confidence: 0.5, Source: "audio"},
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// 对 ids[1] 添加人工修正 style，应覆盖 llm/audio style 并保留 llm mood
	if err := s.SetUserTrackTags(ctx, ids[1], "style", []TrackTag{{Tag: "jazz", Weight: 1}}); err != nil {
		t.Fatal(err)
	}
	recent, err := s.RecentTagResults(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 {
		t.Fatalf("recent = %d, want 3", len(recent))
	}
	for _, r := range recent {
		single, err := s.TrackTags(ctx, r.TrackID)
		if err != nil {
			t.Fatal(err)
		}
		want := EffectiveTags(single)
		if !reflect.DeepEqual(r.Tags, want) {
			t.Fatalf("track %d recent tags = %+v, want %+v", r.TrackID, r.Tags, want)
		}
	}
}

func TestTaggingConcurrentWriteLockContention(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 16)
	now := time.Date(2026, 11, 25, 1, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	errCh := make(chan error, 64)

	// 4 个并发打标写入 + 失败记录协程
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < len(ids); i += 4 {
				id := ids[i]
				if _, _, err := s.RecordTaggingFailure(ctx, id, "s0", "temp err", now, 4); err != nil {
					errCh <- fmt.Errorf("record failure %d: %w", id, err)
					return
				}
				if err := s.SaveTrackTagResult(ctx, TrackTagMeta{
					TrackID: id, SourceStamp: "s1", InputHash: "h1",
					VocabVersion: 1, PromptVersion: 1, Model: "m",
				}, []TrackTag{{Kind: "mood", Tag: "epic", Weight: 1, Confidence: 0.9, Source: "llm"}}, nil); err != nil {
					errCh <- fmt.Errorf("save tag %d: %w", id, err)
					return
				}
			}
		}(w)
	}
	// 并发播放事件写入
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 16; i++ {
			if _, err := s.RecordPlaybackEvent(ctx, PlaybackEventInput{
				ClientID: "client-001", ClientKind: "web", SessionID: fmt.Sprintf("session-%04d", i), Seq: 1,
				Type: "start", State: "playing", TrackID: ids[i%len(ids)], PositionMillis: 0, DurationMillis: 180000,
			}); err != nil {
				errCh <- fmt.Errorf("playback %d: %w", i, err)
				return
			}
		}
	}()
	// 并发抽样任务创建
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			if _, err := s.CreateAISampleRun(ctx, 4, "m1", "m2", int64(i+1)); err != nil {
				errCh <- fmt.Errorf("sample run %d: %w", i, err)
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

func ptrFloat64(v float64) *float64 { return &v }
