package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 声学分析的存储层。曲目“需要分析”= 有可用音频文件，且没有特征、或文件指纹/分析版本
// 与已存特征不一致，且没有处于退避期或永久失败的失败记录（同指纹、同版本）。
// 选用的音频文件与流式播放一致：可用文件中 id 最小的一个。

// AudioAnalysisSettings 是管理页的声学分析设置。没有保存过时全部为零值（关闭）。
type AudioAnalysisSettings struct {
	Enabled bool
	Paused  bool
	// WindowStart/WindowEnd 是 UTC+8 当天的分钟数（0–1439，可跨午夜）；均为 nil 表示不限。
	WindowStart, WindowEnd *int
	FirstEnabledAt         string
}

// AudioAnalysisCandidate 是一首待分析曲目。
type AudioAnalysisCandidate struct {
	TrackID      int64
	AudioFileID  int64
	RelativePath string
	Fingerprint  string
	DurationMs   int64
	Channels     int
	Title        string
}

// TrackAudioFeatures 是一首曲目的分析结果。向量均为 float16 小端字节。
type TrackAudioFeatures struct {
	TrackID         int64
	Fingerprint     string
	AnalysisVersion string
	Mode            string
	DurationMs      int64
	Windows         int
	Patches         int
	Genre           []byte
	MoodTheme       []byte
	Instrument      []byte
	Embedding       []byte
	HeadsJSON       string
	MusicnnValence  *float64
	MusicnnArousal  *float64
	Valence         *float64
	Arousal         *float64
	AVSource        string
	AVModelID       *int64
	AnalyzeMs       int64
	AnalyzedAt      string
}

// AudioAVModel 是本地校准的唤醒度/效价线性回归：[valence, arousal] = bias + x·weights。
type AudioAVModel struct {
	ID              int64
	AnalysisVersion string
	Kind            string   // embedding | heads
	Features        []string // kind=heads 时的特征顺序
	Dims            int
	Weights         []byte // float32 小端 [dims][2]
	BiasValence     float64
	BiasArousal     float64
	Samples         int
	MetricsJSON     string
	CreatedAt       string
}

// AudioAVSample 是一个校准样本（full 模式的曲目）。
type AudioAVSample struct {
	TrackID   int64
	Embedding []byte
	HeadsJSON string
	Valence   float64
	Arousal   float64
}

// AudioAVPending 是缺少生效唤醒度/效价、等待用回归模型回填的曲目。
type AudioAVPending struct {
	TrackID   int64
	Embedding []byte
	HeadsJSON string
}

// AudioAnalysisStats 是进度统计（只统计当前分析版本）。
type AudioAnalysisStats struct {
	Eligible          int64 // 有可用音频文件的曲目
	Analyzed          int64 // 特征与当前文件、当前版本一致
	Calibrated        int64 // 其中 full 模式（含 musicnn 唤醒度/效价）
	MissingAV         int64 // 已分析但还没有唤醒度/效价
	RetryPending      int64 // 处于退避期、稍后自动重试
	PermanentFailures int64 // 无法解码/文件不存在，文件变化前不再重试
	LastAnalyzedAt    string
	RecentAvgMs       int64 // 最近 50 首的平均分析耗时
}

// AudioAnalysisFailure 是管理页失败列表的一行。
type AudioAnalysisFailure struct {
	TrackID      int64
	Title        string
	RelativePath string
	Attempts     int
	Permanent    bool
	LastError    string
	NextRetryAt  string
	UpdatedAt    string
}

// AnalysisTimestamp 把时间格式化为与 strftime('%Y-%m-%dT%H:%M:%fZ') 一致、可按字符串比较的形式。
func AnalysisTimestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

const audioCandidateCTE = `WITH cand AS (
	SELECT t.id AS track_id, af.id AS file_id, af.relative_path AS rel,
		af.id || ':' || af.file_size || ':' || af.modified_at_ns AS fp,
		COALESCE(t.duration_ms, 0) AS dur, COALESCE(af.channels, 0) AS ch,
		COALESCE(t.user_title, t.title) AS title
	FROM tracks t
	JOIN audio_files af ON af.id = (SELECT MIN(a2.id) FROM audio_files a2 WHERE a2.track_id = t.id AND a2.status = 'available')
)`

// 失败记录对当前候选生效：同指纹、同版本，且永久失败或仍在退避期。参数：version, now。
const audioFailureBlocks = `EXISTS (SELECT 1 FROM audio_analysis_failures x WHERE x.track_id = c.track_id
	AND x.file_fingerprint = c.fp AND x.analysis_version = ? AND (x.permanent = 1 OR x.next_retry_at > ?))`

// AudioAnalysisSettings 读取设置；没有行时返回默认值（关闭）。
func (s *Store) AudioAnalysisSettings(ctx context.Context) (AudioAnalysisSettings, error) {
	var v AudioAnalysisSettings
	var enabled, paused int
	var start, end sql.NullInt64
	var first sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT enabled, paused, window_start_minute, window_end_minute, first_enabled_at FROM audio_analysis_settings WHERE id = 1`).
		Scan(&enabled, &paused, &start, &end, &first)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Enabled, v.Paused, v.FirstEnabledAt = enabled != 0, paused != 0, first.String
	if start.Valid && end.Valid {
		a, b := int(start.Int64), int(end.Int64)
		v.WindowStart, v.WindowEnd = &a, &b
	}
	return v, nil
}

// SaveAudioAnalysisSettings 写入设置。首次开启时记录 first_enabled_at（之后不再改变）。
func (s *Store) SaveAudioAnalysisSettings(ctx context.Context, v AudioAnalysisSettings) error {
	if (v.WindowStart == nil) != (v.WindowEnd == nil) {
		return errors.New("window start and end must both be set or both be empty")
	}
	var start, end any
	if v.WindowStart != nil {
		if *v.WindowStart < 0 || *v.WindowStart > 1439 || *v.WindowEnd < 0 || *v.WindowEnd > 1439 || *v.WindowStart == *v.WindowEnd {
			return errors.New("invalid analysis window")
		}
		start, end = *v.WindowStart, *v.WindowEnd
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_analysis_settings(id, enabled, paused, window_start_minute, window_end_minute, first_enabled_at)
		VALUES (1, ?, ?, ?, ?, CASE WHEN ? = 1 THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') END)
		ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled, paused = excluded.paused,
			window_start_minute = excluded.window_start_minute, window_end_minute = excluded.window_end_minute,
			first_enabled_at = COALESCE(audio_analysis_settings.first_enabled_at, excluded.first_enabled_at),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		boolInt(v.Enabled), boolInt(v.Paused), start, end, boolInt(v.Enabled))
	return err
}

func scanCandidate(row *sql.Row) (AudioAnalysisCandidate, bool, error) {
	var c AudioAnalysisCandidate
	err := row.Scan(&c.TrackID, &c.AudioFileID, &c.RelativePath, &c.Fingerprint, &c.DurationMs, &c.Channels, &c.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// NextAudioAnalysisCandidate 返回下一首需要（fast 模式）分析的曲目，新入库的优先。
func (s *Store) NextAudioAnalysisCandidate(ctx context.Context, version string, now time.Time) (AudioAnalysisCandidate, bool, error) {
	return scanCandidate(s.db.QueryRowContext(ctx, audioCandidateCTE+`
		SELECT c.track_id, c.file_id, c.rel, c.fp, c.dur, c.ch, c.title FROM cand c
		LEFT JOIN track_audio_features f ON f.track_id = c.track_id
		WHERE (f.track_id IS NULL OR f.file_fingerprint <> c.fp OR f.analysis_version <> ?)
		AND NOT `+audioFailureBlocks+`
		ORDER BY c.track_id DESC LIMIT 1`, version, version, AnalysisTimestamp(now)))
}

// calibrationOrder 是校准样本的确定性伪随机顺序（Knuth 乘法哈希），避免只抽到同一批专辑。
const calibrationOrder = `ORDER BY (c.track_id * 2654435761) % 4294967296, c.track_id`

// NextAudioCalibrationCandidate 返回校准集（按伪随机顺序的前 target 首，不含永久失败）中
// 还没有当前版本 full 模式结果的下一首。
func (s *Store) NextAudioCalibrationCandidate(ctx context.Context, version string, target int, now time.Time) (AudioAnalysisCandidate, bool, error) {
	ts := AnalysisTimestamp(now)
	return scanCandidate(s.db.QueryRowContext(ctx, audioCandidateCTE+`,
	cal AS (
		SELECT c.* FROM cand c
		WHERE NOT EXISTS (SELECT 1 FROM audio_analysis_failures x WHERE x.track_id = c.track_id
			AND x.file_fingerprint = c.fp AND x.analysis_version = ? AND x.permanent = 1)
		`+calibrationOrder+` LIMIT ?
	)
	SELECT c.track_id, c.file_id, c.rel, c.fp, c.dur, c.ch, c.title FROM cal c
	LEFT JOIN track_audio_features f ON f.track_id = c.track_id
	WHERE NOT (f.track_id IS NOT NULL AND f.file_fingerprint = c.fp AND f.analysis_version = ? AND f.mode = 'full')
	AND NOT `+audioFailureBlocks+`
	ORDER BY c.track_id LIMIT 1`, version, target, version, version, ts))
}

// SaveTrackAudioFeatures 写入（覆盖）一首曲目的分析结果，并清除其失败记录。
func (s *Store) SaveTrackAudioFeatures(ctx context.Context, f TrackAudioFeatures) error {
	if f.Mode != "fast" && f.Mode != "full" {
		return fmt.Errorf("invalid analysis mode %q", f.Mode)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO track_audio_features(track_id, file_fingerprint, analysis_version, mode, duration_ms, windows, patches,
		genre, moodtheme, instrument, embedding, heads_json, musicnn_valence, musicnn_arousal, valence, arousal, av_source, av_model_id, analyze_ms, analyzed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULLIF(?, ''),?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(track_id) DO UPDATE SET file_fingerprint = excluded.file_fingerprint, analysis_version = excluded.analysis_version,
			mode = excluded.mode, duration_ms = excluded.duration_ms, windows = excluded.windows, patches = excluded.patches,
			genre = excluded.genre, moodtheme = excluded.moodtheme, instrument = excluded.instrument, embedding = excluded.embedding,
			heads_json = excluded.heads_json, musicnn_valence = excluded.musicnn_valence, musicnn_arousal = excluded.musicnn_arousal,
			valence = excluded.valence, arousal = excluded.arousal, av_source = excluded.av_source, av_model_id = excluded.av_model_id,
			analyze_ms = excluded.analyze_ms, analyzed_at = excluded.analyzed_at`,
		f.TrackID, f.Fingerprint, f.AnalysisVersion, f.Mode, f.DurationMs, f.Windows, f.Patches,
		f.Genre, f.MoodTheme, f.Instrument, f.Embedding, f.HeadsJSON, f.MusicnnValence, f.MusicnnArousal,
		f.Valence, f.Arousal, f.AVSource, f.AVModelID, f.AnalyzeMs); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM audio_analysis_failures WHERE track_id = ?`, f.TrackID); err != nil {
		return err
	}
	return tx.Commit()
}

// TrackAudioFeaturesByID 读取一首曲目的分析结果。
func (s *Store) TrackAudioFeaturesByID(ctx context.Context, trackID int64) (TrackAudioFeatures, bool, error) {
	var f TrackAudioFeatures
	var source sql.NullString
	var modelID sql.NullInt64
	var mv, ma, v, a sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `SELECT track_id, file_fingerprint, analysis_version, mode, duration_ms, windows, patches,
		genre, moodtheme, instrument, embedding, heads_json, musicnn_valence, musicnn_arousal, valence, arousal, av_source, av_model_id, analyze_ms, analyzed_at
		FROM track_audio_features WHERE track_id = ?`, trackID).Scan(&f.TrackID, &f.Fingerprint, &f.AnalysisVersion, &f.Mode, &f.DurationMs,
		&f.Windows, &f.Patches, &f.Genre, &f.MoodTheme, &f.Instrument, &f.Embedding, &f.HeadsJSON, &mv, &ma, &v, &a, &source, &modelID, &f.AnalyzeMs, &f.AnalyzedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	f.MusicnnValence, f.MusicnnArousal, f.Valence, f.Arousal = nullFloat(mv), nullFloat(ma), nullFloat(v), nullFloat(a)
	f.AVSource = source.String
	if modelID.Valid {
		f.AVModelID = &modelID.Int64
	}
	return f, true, nil
}

func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

// audioRetryDelay 是第 n 次（从 1 开始）临时失败后的退避时间。
func audioRetryDelay(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 10 * time.Minute
	case attempts == 2:
		return time.Hour
	case attempts == 3:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// RecordAudioAnalysisFailure 记录一次失败。permanent=true 表示文件本身有问题（无法解码/不存在），
// 在文件指纹或分析版本变化前不再重试；否则按次数指数退避。指纹/版本变化会重置次数。
func (s *Store) RecordAudioAnalysisFailure(ctx context.Context, c AudioAnalysisCandidate, version, message string, permanent bool, now time.Time) error {
	if len(message) > 500 {
		message = message[:500]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	attempts := 0
	var fp, ver string
	err = tx.QueryRowContext(ctx, `SELECT attempts, file_fingerprint, analysis_version FROM audio_analysis_failures WHERE track_id = ?`, c.TrackID).Scan(&attempts, &fp, &ver)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if fp != c.Fingerprint || ver != version {
		attempts = 0
	}
	attempts++
	next := AnalysisTimestamp(now.Add(audioRetryDelay(attempts)))
	if _, err = tx.ExecContext(ctx, `INSERT INTO audio_analysis_failures(track_id, file_fingerprint, analysis_version, attempts, permanent, last_error, next_retry_at)
		VALUES (?,?,?,?,?,?,?) ON CONFLICT(track_id) DO UPDATE SET file_fingerprint = excluded.file_fingerprint,
			analysis_version = excluded.analysis_version, attempts = excluded.attempts, permanent = excluded.permanent,
			last_error = excluded.last_error, next_retry_at = excluded.next_retry_at, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		c.TrackID, c.Fingerprint, version, attempts, boolInt(permanent), message, next); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearAudioAnalysisFailures 删除全部失败记录（管理页“全部重试”）。
func (s *Store) ClearAudioAnalysisFailures(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM audio_analysis_failures`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// AudioAnalysisFailures 列出当前版本、当前文件仍生效的失败（最近的在前）。
func (s *Store) AudioAnalysisFailures(ctx context.Context, version string, limit int) ([]AudioAnalysisFailure, error) {
	rows, err := s.db.QueryContext(ctx, audioCandidateCTE+`
		SELECT c.track_id, c.title, c.rel, x.attempts, x.permanent, x.last_error, x.next_retry_at, x.updated_at
		FROM audio_analysis_failures x JOIN cand c ON c.track_id = x.track_id AND c.fp = x.file_fingerprint
		WHERE x.analysis_version = ? ORDER BY x.updated_at DESC, x.track_id LIMIT ?`, version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioAnalysisFailure
	for rows.Next() {
		var f AudioAnalysisFailure
		var permanent int
		if err := rows.Scan(&f.TrackID, &f.Title, &f.RelativePath, &f.Attempts, &permanent, &f.LastError, &f.NextRetryAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.Permanent = permanent != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// AudioAnalysisStats 统计当前版本的进度。
func (s *Store) AudioAnalysisStats(ctx context.Context, version string, now time.Time) (AudioAnalysisStats, error) {
	var st AudioAnalysisStats
	ts := AnalysisTimestamp(now)
	err := s.db.QueryRowContext(ctx, audioCandidateCTE+`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN f.track_id IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN f.track_id IS NOT NULL AND f.mode = 'full' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN f.track_id IS NOT NULL AND f.arousal IS NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN f.track_id IS NULL AND x.permanent = 0 AND x.next_retry_at > ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN f.track_id IS NULL AND x.permanent = 1 THEN 1 ELSE 0 END), 0)
		FROM cand c
		LEFT JOIN track_audio_features f ON f.track_id = c.track_id AND f.file_fingerprint = c.fp AND f.analysis_version = ?
		LEFT JOIN audio_analysis_failures x ON x.track_id = c.track_id AND x.file_fingerprint = c.fp AND x.analysis_version = ?`,
		ts, version, version).Scan(&st.Eligible, &st.Analyzed, &st.Calibrated, &st.MissingAV, &st.RetryPending, &st.PermanentFailures)
	if err != nil {
		return st, err
	}
	var last sql.NullString
	var avg sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(analyzed_at), AVG(analyze_ms) FROM (SELECT analyzed_at, analyze_ms FROM track_audio_features
		WHERE analysis_version = ? AND mode = 'fast' ORDER BY analyzed_at DESC LIMIT 50)`, version).Scan(&last, &avg); err != nil {
		return st, err
	}
	st.LastAnalyzedAt, st.RecentAvgMs = last.String, int64(avg.Float64)
	return st, nil
}

// AudioCalibrationSamples 返回当前版本的全部校准样本（full 模式且有 musicnn 唤醒度/效价）。
func (s *Store) AudioCalibrationSamples(ctx context.Context, version string) ([]AudioAVSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT track_id, embedding, heads_json, musicnn_valence, musicnn_arousal FROM track_audio_features
		WHERE analysis_version = ? AND mode = 'full' AND musicnn_valence IS NOT NULL AND musicnn_arousal IS NOT NULL ORDER BY track_id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioAVSample
	for rows.Next() {
		var v AudioAVSample
		if err := rows.Scan(&v.TrackID, &v.Embedding, &v.HeadsJSON, &v.Valence, &v.Arousal); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaveAudioAVModel 保存一个新模型并设为该版本唯一生效的模型，返回其 id。
func (s *Store) SaveAudioAVModel(ctx context.Context, m AudioAVModel) (int64, error) {
	if m.Kind != "embedding" && m.Kind != "heads" {
		return 0, fmt.Errorf("invalid av model kind %q", m.Kind)
	}
	if m.Dims <= 0 || len(m.Weights) != m.Dims*2*4 {
		return 0, fmt.Errorf("av model weights size %d does not match dims %d", len(m.Weights), m.Dims)
	}
	features, err := json.Marshal(m.Features)
	if err != nil {
		return 0, err
	}
	if m.Features == nil {
		features = []byte("[]")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE audio_av_models SET active = 0 WHERE analysis_version = ? AND active = 1`, m.AnalysisVersion); err != nil {
		return 0, err
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO audio_av_models(analysis_version, kind, features_json, dims, weights, bias_valence, bias_arousal, samples, metrics_json, active)
		VALUES (?,?,?,?,?,?,?,?,?,1)`, m.AnalysisVersion, m.Kind, string(features), m.Dims, m.Weights, m.BiasValence, m.BiasArousal, m.Samples, m.MetricsJSON)
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	// 旧模型的回归值作废，等待用新模型回填（musicnn 原值保留）。
	if _, err = tx.ExecContext(ctx, `UPDATE track_audio_features SET valence = NULL, arousal = NULL, av_source = NULL, av_model_id = NULL
		WHERE analysis_version = ? AND av_source = 'regression'`, m.AnalysisVersion); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ActiveAudioAVModel 返回该版本生效的模型。
func (s *Store) ActiveAudioAVModel(ctx context.Context, version string) (AudioAVModel, bool, error) {
	var m AudioAVModel
	var features string
	err := s.db.QueryRowContext(ctx, `SELECT id, analysis_version, kind, features_json, dims, weights, bias_valence, bias_arousal, samples, metrics_json, created_at
		FROM audio_av_models WHERE analysis_version = ? AND active = 1`, version).Scan(&m.ID, &m.AnalysisVersion, &m.Kind, &features, &m.Dims,
		&m.Weights, &m.BiasValence, &m.BiasArousal, &m.Samples, &m.MetricsJSON, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal([]byte(features), &m.Features); err != nil {
		return m, false, fmt.Errorf("decode av model features: %w", err)
	}
	return m, true, nil
}

// AudioAVPendingTracks 返回已分析、还没有唤醒度/效价的曲目（等待回归模型回填）。
func (s *Store) AudioAVPendingTracks(ctx context.Context, version string, limit int) ([]AudioAVPending, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT track_id, embedding, heads_json FROM track_audio_features
		WHERE analysis_version = ? AND av_source IS NULL ORDER BY track_id LIMIT ?`, version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioAVPending
	for rows.Next() {
		var p AudioAVPending
		if err := rows.Scan(&p.TrackID, &p.Embedding, &p.HeadsJSON); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AudioAVValue 是一首曲目的回归预测值。
type AudioAVValue struct {
	TrackID          int64
	Valence, Arousal float64
}

// ApplyAudioAVRegression 批量写入回归预测值（只更新仍缺值的行，不覆盖 musicnn 原值）。
func (s *Store) ApplyAudioAVRegression(ctx context.Context, version string, modelID int64, values []AudioAVValue) error {
	if len(values) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE track_audio_features SET valence = ?, arousal = ?, av_source = 'regression', av_model_id = ?
		WHERE track_id = ? AND analysis_version = ? AND av_source IS NULL`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range values {
		if _, err := stmt.ExecContext(ctx, v.Valence, v.Arousal, modelID, v.TrackID, version); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveAudioAnalysisLabels / AudioAnalysisLabels 读写某分析版本的向量类别名（JSON）。
func (s *Store) SaveAudioAnalysisLabels(ctx context.Context, version, labelsJSON string) error {
	if !json.Valid([]byte(labelsJSON)) {
		return errors.New("labels must be valid JSON")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_analysis_labels(analysis_version, labels_json) VALUES (?, ?)
		ON CONFLICT(analysis_version) DO UPDATE SET labels_json = excluded.labels_json, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, version, labelsJSON)
	return err
}

func (s *Store) AudioAnalysisLabels(ctx context.Context, version string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT labels_json FROM audio_analysis_labels WHERE analysis_version = ?`, version).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// LatestAudioAnalysisVersion 返回最近使用的分析版本（用于服务重启后、连上分析服务之前显示统计）。
func (s *Store) LatestAudioAnalysisVersion(ctx context.Context) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT analysis_version FROM (
		SELECT analysis_version, updated_at AS at FROM audio_analysis_labels
		UNION ALL SELECT analysis_version, analyzed_at AS at FROM (SELECT analysis_version, analyzed_at FROM track_audio_features ORDER BY analyzed_at DESC LIMIT 1)
	) ORDER BY at DESC LIMIT 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v.String, err
}
