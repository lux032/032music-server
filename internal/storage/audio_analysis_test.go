package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

const testAnalysisVersion = "test-v1"

func audioAnalysisFixture(t *testing.T, tracks int) (*Store, context.Context, []int64) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "analysis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	var ids []int64
	for i := 0; i < tracks; i++ {
		rel := fmt.Sprintf("Album %d/%02d.flac", i, i+1)
		if err = s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: rel, FileSize: int64(1000 + i), ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song %d", i), Album: fmt.Sprintf("Album %d", i), Artists: []string{"A"}, AlbumArtists: []string{"A"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err = s.db.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE relative_path = ?`, rel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return s, ctx, ids
}

func testFeatures(c AudioAnalysisCandidate, mode string) TrackAudioFeatures {
	f := TrackAudioFeatures{TrackID: c.TrackID, Fingerprint: c.Fingerprint, AnalysisVersion: testAnalysisVersion, Mode: mode,
		DurationMs: 1000, Windows: 1, Patches: 9, Genre: make([]byte, 800), MoodTheme: make([]byte, 112), Instrument: make([]byte, 80),
		Embedding: make([]byte, 2560), HeadsJSON: `{"voice":0.5}`, AnalyzeMs: 1200}
	if mode == "full" {
		v, a := 5.0, 6.0
		f.MusicnnValence, f.MusicnnArousal, f.Valence, f.Arousal, f.AVSource = &v, &a, &v, &a, "musicnn"
	}
	return f
}

func mustNext(t *testing.T, s *Store, ctx context.Context, now time.Time) (AudioAnalysisCandidate, bool) {
	t.Helper()
	c, ok, err := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now)
	if err != nil {
		t.Fatal(err)
	}
	return c, ok
}

func TestAudioAnalysisSettingsDefaultsAndFirstEnabled(t *testing.T) {
	s, ctx, _ := audioAnalysisFixture(t, 0)
	v, err := s.AudioAnalysisSettings(ctx)
	if err != nil || v.Enabled || v.Paused || v.WindowStart != nil || v.FirstEnabledAt != "" {
		t.Fatalf("defaults = %+v err=%v, want disabled", v, err)
	}
	if err = s.SaveAudioAnalysisSettings(ctx, AudioAnalysisSettings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.AudioAnalysisSettings(ctx); v.FirstEnabledAt != "" {
		t.Fatal("first_enabled_at must stay empty until enabled")
	}
	start, end := 60, 420
	if err = s.SaveAudioAnalysisSettings(ctx, AudioAnalysisSettings{Enabled: true, WindowStart: &start, WindowEnd: &end}); err != nil {
		t.Fatal(err)
	}
	v, _ = s.AudioAnalysisSettings(ctx)
	first := v.FirstEnabledAt
	if !v.Enabled || first == "" || *v.WindowStart != 60 || *v.WindowEnd != 420 {
		t.Fatalf("saved = %+v", v)
	}
	time.Sleep(5 * time.Millisecond)
	_ = s.SaveAudioAnalysisSettings(ctx, AudioAnalysisSettings{Enabled: false})
	_ = s.SaveAudioAnalysisSettings(ctx, AudioAnalysisSettings{Enabled: true, Paused: true})
	if v, _ = s.AudioAnalysisSettings(ctx); v.FirstEnabledAt != first || !v.Paused || v.WindowStart != nil {
		t.Fatalf("first_enabled_at must be kept, got %+v (first %s)", v, first)
	}
	same := 30
	for _, bad := range []AudioAnalysisSettings{{WindowStart: &start}, {WindowStart: &same, WindowEnd: &same}, {WindowStart: &[]int{-1}[0], WindowEnd: &end}} {
		if err := s.SaveAudioAnalysisSettings(ctx, bad); err == nil {
			t.Fatalf("invalid window accepted: %+v", bad)
		}
	}
}

func TestAudioAnalysisCandidatesFollowFileAndVersion(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 3)
	now := time.Now()
	c, ok := mustNext(t, s, ctx, now)
	if !ok || c.TrackID != ids[2] || c.RelativePath != "Album 2/03.flac" || c.Title != "Song 2" {
		t.Fatalf("first candidate = %+v ok=%v, want newest track", c, ok)
	}
	if c.Fingerprint != fmt.Sprintf("%d:1002:1", c.AudioFileID) {
		t.Fatalf("fingerprint = %q", c.Fingerprint)
	}
	for i := 0; i < 3; i++ {
		c, ok = mustNext(t, s, ctx, now)
		if !ok {
			t.Fatalf("expected candidate %d", i)
		}
		if err := s.SaveTrackAudioFeatures(ctx, testFeatures(c, "fast")); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok = mustNext(t, s, ctx, now); ok {
		t.Fatal("everything analysed; no candidate expected")
	}
	// 文件变化（修改时间）→ 重新分析
	if _, err := s.db.ExecContext(ctx, `UPDATE audio_files SET modified_at_ns = 2 WHERE track_id = ?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	if c, ok = mustNext(t, s, ctx, now); !ok || c.TrackID != ids[1] {
		t.Fatalf("changed file must be re-analysed, got %+v ok=%v", c, ok)
	}
	// 分析版本变化 → 全部重新分析
	if c, ok, _ = s.NextAudioAnalysisCandidate(ctx, "test-v2", now); !ok || c.TrackID != ids[2] {
		t.Fatalf("new version must re-analyse, got %+v", c)
	}
	// 文件缺失 → 不再是候选；删除曲目 → 特征级联删除
	if _, err := s.db.ExecContext(ctx, `UPDATE audio_files SET status = 'missing' WHERE track_id = ?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, ok = mustNext(t, s, ctx, now); ok {
		t.Fatal("track without available file must not be a candidate")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tracks WHERE id = ?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.TrackAudioFeaturesByID(ctx, ids[0]); found {
		t.Fatal("features must be deleted with the track")
	}
}

func TestAudioAnalysisFailureBackoffAndPermanent(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 2)
	now := time.Now()
	c, _ := mustNext(t, s, ctx, now) // ids[1]
	if err := s.RecordAudioAnalysisFailure(ctx, c, testAnalysisVersion, "timeout", false, now); err != nil {
		t.Fatal(err)
	}
	if next, _ := mustNext(t, s, ctx, now); next.TrackID != ids[0] {
		t.Fatalf("failed track must back off, got %d", next.TrackID)
	}
	if next, _ := mustNext(t, s, ctx, now.Add(11*time.Minute)); next.TrackID != ids[1] {
		t.Fatal("track must be retried after the first backoff (10 min)")
	}
	_ = s.RecordAudioAnalysisFailure(ctx, c, testAnalysisVersion, "timeout", false, now)
	failures, err := s.AudioAnalysisFailures(ctx, testAnalysisVersion, 10)
	if err != nil || len(failures) != 1 || failures[0].Attempts != 2 || failures[0].Permanent {
		t.Fatalf("failures = %+v err=%v", failures, err)
	}
	if next, _ := mustNext(t, s, ctx, now.Add(30*time.Minute)); next.TrackID != ids[0] {
		t.Fatal("second backoff is one hour")
	}
	stats, err := s.AudioAnalysisStats(ctx, testAnalysisVersion, now)
	if err != nil || stats.Eligible != 2 || stats.RetryPending != 1 || stats.PermanentFailures != 0 {
		t.Fatalf("stats = %+v err=%v", stats, err)
	}
	// 永久失败：时间过去也不重试；文件变化后重置
	other, _ := mustNext(t, s, ctx, now)
	_ = s.RecordAudioAnalysisFailure(ctx, other, testAnalysisVersion, "decode_failed", true, now)
	if _, ok := mustNext(t, s, ctx, now.Add(48*time.Hour)); ok {
		// ids[1] 的退避已过期会重新出现；ids[0] 是永久失败不应出现
		next, _ := mustNext(t, s, ctx, now.Add(48*time.Hour))
		if next.TrackID == ids[0] {
			t.Fatal("permanent failure must not be retried")
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audio_files SET file_size = 9 WHERE track_id = ?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < 2; i++ {
		next, ok := mustNext(t, s, ctx, now)
		if ok && next.TrackID == ids[0] {
			found = true
			_ = s.RecordAudioAnalysisFailure(ctx, next, testAnalysisVersion, "timeout", false, now)
			break
		}
	}
	if !found {
		t.Fatal("changed file must clear the permanent failure")
	}
	failures, _ = s.AudioAnalysisFailures(ctx, testAnalysisVersion, 10)
	for _, f := range failures {
		if f.TrackID == ids[0] && f.Attempts != 1 {
			t.Fatalf("attempts must reset after a fingerprint change, got %d", f.Attempts)
		}
	}
	if n, err := s.ClearAudioAnalysisFailures(ctx); err != nil || n != 2 {
		t.Fatalf("cleared = %d err=%v", n, err)
	}
}

func TestAudioCalibrationCandidatesAndStats(t *testing.T) {
	s, ctx, _ := audioAnalysisFixture(t, 6)
	now := time.Now()
	seen := map[int64]bool{}
	for i := 0; i < 3; i++ {
		c, ok, err := s.NextAudioCalibrationCandidate(ctx, testAnalysisVersion, 3, now)
		if err != nil || !ok {
			t.Fatalf("calibration candidate %d: ok=%v err=%v", i, ok, err)
		}
		seen[c.TrackID] = true
		// fast 结果不计入校准：同一首仍是校准候选
		_ = s.SaveTrackAudioFeatures(ctx, testFeatures(c, "fast"))
		again, _, _ := s.NextAudioCalibrationCandidate(ctx, testAnalysisVersion, 3, now)
		if again.TrackID != c.TrackID {
			t.Fatal("fast-mode result must not count as calibrated")
		}
		if err := s.SaveTrackAudioFeatures(ctx, testFeatures(c, "full")); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("calibration set size = %d, want 3", len(seen))
	}
	if _, ok, _ := s.NextAudioCalibrationCandidate(ctx, testAnalysisVersion, 3, now); ok {
		t.Fatal("calibration set of 3 is complete")
	}
	samples, err := s.AudioCalibrationSamples(ctx, testAnalysisVersion)
	if err != nil || len(samples) != 3 || samples[0].Arousal != 6 {
		t.Fatalf("samples = %d err=%v", len(samples), err)
	}
	stats, err := s.AudioAnalysisStats(ctx, testAnalysisVersion, now)
	if err != nil || stats.Eligible != 6 || stats.Analyzed != 3 || stats.Calibrated != 3 || stats.MissingAV != 0 {
		t.Fatalf("stats = %+v err=%v", stats, err)
	}
}

func TestAudioAVModelLifecycle(t *testing.T) {
	s, ctx, _ := audioAnalysisFixture(t, 3)
	now := time.Now()
	var fast []int64
	for i := 0; i < 3; i++ {
		c, _ := mustNext(t, s, ctx, now)
		mode := "fast"
		if i == 0 {
			mode = "full"
		} else {
			fast = append(fast, c.TrackID)
		}
		_ = s.SaveTrackAudioFeatures(ctx, testFeatures(c, mode))
	}
	if _, found, _ := s.ActiveAudioAVModel(ctx, testAnalysisVersion); found {
		t.Fatal("no model yet")
	}
	pending, err := s.AudioAVPendingTracks(ctx, testAnalysisVersion, 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %d err=%v", len(pending), err)
	}
	if _, err := s.SaveAudioAVModel(ctx, AudioAVModel{AnalysisVersion: testAnalysisVersion, Kind: "embedding", Dims: 2, Weights: make([]byte, 8)}); err == nil {
		t.Fatal("weights size mismatch must be rejected")
	}
	m := AudioAVModel{AnalysisVersion: testAnalysisVersion, Kind: "heads", Features: []string{"voice"}, Dims: 1, Weights: make([]byte, 8), BiasValence: 4, BiasArousal: 5, Samples: 40, MetricsJSON: "{}"}
	id1, err := s.SaveAudioAVModel(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyAudioAVRegression(ctx, testAnalysisVersion, id1, []AudioAVValue{{fast[0], 4, 5}, {fast[1], 4.5, 5.5}}); err != nil {
		t.Fatal(err)
	}
	if pending, _ = s.AudioAVPendingTracks(ctx, testAnalysisVersion, 10); len(pending) != 0 {
		t.Fatal("all tracks have AV now")
	}
	f, _, _ := s.TrackAudioFeaturesByID(ctx, fast[1])
	if f.AVSource != "regression" || *f.Arousal != 5.5 || *f.AVModelID != id1 {
		t.Fatalf("regression values not stored: %+v", f)
	}
	// 新模型：旧回归值作废、musicnn 原值保留，旧模型失效
	id2, err := s.SaveAudioAVModel(ctx, m)
	if err != nil || id2 == id1 {
		t.Fatalf("second model id=%d err=%v", id2, err)
	}
	active, found, _ := s.ActiveAudioAVModel(ctx, testAnalysisVersion)
	if !found || active.ID != id2 || active.Features[0] != "voice" {
		t.Fatalf("active = %+v", active)
	}
	if pending, _ = s.AudioAVPendingTracks(ctx, testAnalysisVersion, 10); len(pending) != 2 {
		t.Fatalf("regression values must be invalidated, pending=%d", len(pending))
	}
	stats, _ := s.AudioAnalysisStats(ctx, testAnalysisVersion, now)
	if stats.MissingAV != 2 || stats.Calibrated != 1 || stats.RecentAvgMs != 1200 {
		t.Fatalf("stats = %+v", stats)
	}
	if err := s.SaveAudioAnalysisLabels(ctx, testAnalysisVersion, `{"genre":["x"]}`); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.AudioAnalysisLabels(ctx, testAnalysisVersion); err != nil || !ok || v != `{"genre":["x"]}` {
		t.Fatalf("labels = %q ok=%v err=%v", v, ok, err)
	}
	if err := s.SaveAudioAnalysisLabels(ctx, testAnalysisVersion, `{`); err == nil {
		t.Fatal("invalid labels JSON must be rejected")
	}
}
