package storage

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAudioAnalysisQueryPlansAndCalibrationParity(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 20)
	now := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)
	ts := AnalysisTimestamp(now)

	// 1) EXPLAIN QUERY PLAN: NextAudioAnalysisCandidate 按 tracks 主键逆序且无临时 B-tree 排序
	fastPlan := explainQueryPlan(t, s, nextAudioAnalysisCandidateQuery, testAnalysisVersion, testAnalysisVersion, ts)
	if strings.Contains(fastPlan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("NextAudioAnalysisCandidate still uses temp B-tree sort:\n%s", fastPlan)
	}
	if !strings.Contains(fastPlan, "SCAN t") {
		t.Fatalf("NextAudioAnalysisCandidate did not drive from tracks PK:\n%s", fastPlan)
	}

	// 2) EXPLAIN QUERY PLAN: NextAudioCalibrationCandidate 命中覆盖索引 idx_audio_files_available_track
	calPlan := explainQueryPlan(t, s, nextAudioCalibrationCandidateQuery, testAnalysisVersion, 8, testAnalysisVersion, testAnalysisVersion, ts)
	if !strings.Contains(calPlan, "idx_audio_files_available_track") {
		t.Fatalf("NextAudioCalibrationCandidate did not use idx_audio_files_available_track:\n%s", calPlan)
	}

	// 3) EXPLAIN QUERY PLAN: AudioAnalysisFailures 直接以 audio_analysis_failures 驱动，不扫描 tracks
	failPlan := explainQueryPlan(t, s, audioAnalysisFailuresQuery, testAnalysisVersion, 20)
	if strings.Contains(failPlan, "SCAN t") {
		t.Fatalf("AudioAnalysisFailures still scans tracks:\n%s", failPlan)
	}

	// 4) AudioEligibleTrackCount 准确计数且排除缺失文件
	eligible, err := s.AudioEligibleTrackCount(ctx)
	if err != nil || eligible != 20 {
		t.Fatalf("eligible = %d err=%v, want 20", eligible, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audio_files SET status = 'missing' WHERE track_id = ?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	eligible, err = s.AudioEligibleTrackCount(ctx)
	if err != nil || eligible != 19 {
		t.Fatalf("eligible after missing = %d err=%v, want 19", eligible, err)
	}

	// 5) 校准集确定性 Knuth 哈希顺序与永久失败替换语义完全一致
	// 对按 Knuth 哈希排在最前的一首标记永久失败，验证它被排除并由下一首递补
	availIDs := ids[1:] // ids[0] 已 missing
	type hashItem struct {
		id   int64
		hash uint64
	}
	ranked := make([]hashItem, len(availIDs))
	for i, id := range availIDs {
		ranked[i] = hashItem{id: id, hash: (uint64(id) * 2654435761) % 4294967296}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].hash != ranked[j].hash {
			return ranked[i].hash < ranked[j].hash
		}
		return ranked[i].id < ranked[j].id
	})
	// 将 ranked[0] 标为永久失败
	permID := ranked[0].id
	var permFileID int64
	var permSize, permMod int64
	if err := s.db.QueryRowContext(ctx, `SELECT id, file_size, modified_at_ns FROM audio_files WHERE track_id = ? AND status = 'available' ORDER BY id LIMIT 1`, permID).Scan(&permFileID, &permSize, &permMod); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAudioAnalysisFailure(ctx, AudioAnalysisCandidate{
		TrackID: permID, AudioFileID: permFileID, Fingerprint: fmt.Sprintf("%d:%d:%d", permFileID, permSize, permMod),
	}, testAnalysisVersion, "decode_failed", true, now); err != nil {
		t.Fatal(err)
	}

	const target = 6
	wantSlice := make([]int64, 0, target)
	for i := 1; i <= target; i++ {
		wantSlice = append(wantSlice, ranked[i].id)
	}
	sort.Slice(wantSlice, func(i, j int) bool { return wantSlice[i] < wantSlice[j] })

	var gotSlice []int64
	for i := 0; i < target; i++ {
		c, ok, err := s.NextAudioCalibrationCandidate(ctx, testAnalysisVersion, target, now)
		if err != nil || !ok {
			t.Fatalf("calibration step %d: ok=%v err=%v", i, ok, err)
		}
		gotSlice = append(gotSlice, c.TrackID)
		if err := s.SaveTrackAudioFeatures(ctx, testFeatures(c, "full")); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.NextAudioCalibrationCandidate(ctx, testAnalysisVersion, target, now); err != nil || ok {
		t.Fatalf("expected calibration complete, got ok=%v err=%v", ok, err)
	}
	for i := range wantSlice {
		if gotSlice[i] != wantSlice[i] {
			t.Fatalf("calibration order = %v, want %v", gotSlice, wantSlice)
		}
	}
}

func TestAudioAnalysisConcurrentWriteLocks(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 12)
	now := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	errCh := make(chan error, 64)

	// 并发保存声学特征 + 回归模型 + 播放事件
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i, id := range ids {
			mode := "fast"
			if i%3 == 0 {
				mode = "full"
			}
			c := AudioAnalysisCandidate{TrackID: id, Fingerprint: fmt.Sprintf("fp-%d", id)}
			if err := s.SaveTrackAudioFeatures(ctx, testFeatures(c, mode)); err != nil {
				errCh <- fmt.Errorf("save features %d: %w", id, err)
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		m := AudioAVModel{
			AnalysisVersion: testAnalysisVersion, Kind: "heads", Features: []string{"voice"},
			Dims: 1, Weights: make([]byte, 8), BiasValence: 4, BiasArousal: 5, Samples: 40, MetricsJSON: "{}",
		}
		for i := 0; i < 4; i++ {
			if _, err := s.SaveAudioAVModel(ctx, m); err != nil {
				errCh <- fmt.Errorf("save model %d: %w", i, err)
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 12; i++ {
			if _, err := s.RecordPlaybackEvent(ctx, PlaybackEventInput{
				ClientID: "client-002", ClientKind: "web", SessionID: fmt.Sprintf("audio-sess-%04d", i), Seq: 1,
				Type: "start", State: "playing", TrackID: ids[i%len(ids)], PositionMillis: 0, DurationMillis: 180000,
			}); err != nil {
				errCh <- fmt.Errorf("playback %d: %w", i, err)
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 6; i++ {
			if err := s.RecordAudioAnalysisFailure(ctx, AudioAnalysisCandidate{TrackID: ids[i], Fingerprint: "fp-err"}, testAnalysisVersion, "err", false, now); err != nil {
				errCh <- fmt.Errorf("record failure %d: %w", i, err)
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
