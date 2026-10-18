package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AISettings 是 AI（OpenAI 兼容接口）配置与打标开关。APIKey / ProxyURL 只在服务端使用，不回显到页面。
type AISettings struct {
	BaseURL           string
	APIKey            string
	Model             string
	FallbackModel     string
	ReviewModel       string
	ReviewEnabled     bool
	ProxyURL          string
	ReasoningEffort   string // "" 表示不发送该参数
	TaggingEnabled    bool
	TaggingPaused     bool
	IncludeLyrics     bool
	RequestsPerMinute int
	Concurrency       int
	FirstEnabledAt    string
	UpdatedAt         string
}

// DefaultAISettings 是没有设置行时的默认值：打标关闭、发送歌词片段、60 次/分钟、并发 4。
func DefaultAISettings() AISettings {
	return AISettings{IncludeLyrics: true, RequestsPerMinute: 60, Concurrency: 4}
}

// Configured 表示接口地址、模型和 API Key 都已填写。
func (v AISettings) Configured() bool {
	return strings.TrimSpace(v.BaseURL) != "" && strings.TrimSpace(v.Model) != "" && v.APIKey != ""
}

// ReviewActive 表示启用了复核且填写了复核模型。
func (v AISettings) ReviewActive() bool {
	return v.ReviewEnabled && strings.TrimSpace(v.ReviewModel) != ""
}

func (s *Store) AISettings(ctx context.Context) (AISettings, error) {
	v := DefaultAISettings()
	var reviewEnabled, enabled, paused, lyrics int
	var first sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT base_url, api_key, model, fallback_model, review_model, review_enabled, proxy_url, reasoning_effort,
		tagging_enabled, tagging_paused, include_lyrics, requests_per_minute, concurrency, first_enabled_at, updated_at FROM ai_settings WHERE id = 1`).
		Scan(&v.BaseURL, &v.APIKey, &v.Model, &v.FallbackModel, &v.ReviewModel, &reviewEnabled, &v.ProxyURL, &v.ReasoningEffort,
			&enabled, &paused, &lyrics, &v.RequestsPerMinute, &v.Concurrency, &first, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.ReviewEnabled, v.TaggingEnabled, v.TaggingPaused, v.IncludeLyrics = reviewEnabled != 0, enabled != 0, paused != 0, lyrics != 0
	v.FirstEnabledAt = first.String
	return v, nil
}

// SaveAISettings 整行写入。首次开启打标时记录 first_enabled_at（之后不变）。
func (s *Store) SaveAISettings(ctx context.Context, v AISettings) error {
	switch v.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high":
	default:
		return fmt.Errorf("invalid reasoning effort %q", v.ReasoningEffort)
	}
	if v.RequestsPerMinute < 1 || v.RequestsPerMinute > 10000 || v.Concurrency < 1 || v.Concurrency > 16 {
		return errors.New("invalid rate limit or concurrency")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_settings(id, base_url, api_key, model, fallback_model, review_model, review_enabled, proxy_url, reasoning_effort,
		tagging_enabled, tagging_paused, include_lyrics, requests_per_minute, concurrency, first_enabled_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? = 1 THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') END)
		ON CONFLICT(id) DO UPDATE SET base_url = excluded.base_url, api_key = excluded.api_key, model = excluded.model,
			fallback_model = excluded.fallback_model, review_model = excluded.review_model, review_enabled = excluded.review_enabled, proxy_url = excluded.proxy_url,
			reasoning_effort = excluded.reasoning_effort, tagging_enabled = excluded.tagging_enabled, tagging_paused = excluded.tagging_paused,
			include_lyrics = excluded.include_lyrics, requests_per_minute = excluded.requests_per_minute, concurrency = excluded.concurrency,
			first_enabled_at = COALESCE(ai_settings.first_enabled_at, excluded.first_enabled_at),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		strings.TrimSpace(v.BaseURL), v.APIKey, strings.TrimSpace(v.Model), strings.TrimSpace(v.FallbackModel), strings.TrimSpace(v.ReviewModel), boolInt(v.ReviewEnabled),
		strings.TrimSpace(v.ProxyURL), v.ReasoningEffort, boolInt(v.TaggingEnabled), boolInt(v.TaggingPaused), boolInt(v.IncludeLyrics),
		v.RequestsPerMinute, v.Concurrency, boolInt(v.TaggingEnabled))
	return err
}

// ---------------------------------------------------------------- candidates

// taggingAlbumArtistSQL / taggingGenresSQL 同时用于打标输入（TaggingTrackInfo）与 stamp，保证两者一致。
const taggingAlbumArtistSQL = `COALESCE(a.user_performed_by, a.performed_by, (SELECT GROUP_CONCAT(COALESCE(ar.user_display_name, ar.display_name), ', ') FROM album_artists aa JOIN artists ar ON ar.id = aa.artist_id WHERE aa.album_id = a.id), '')`

const taggingGenresSQL = `COALESCE((SELECT GROUP_CONCAT(gx.name, ',' ORDER BY ox.position, gx.id) FROM track_genre_overrides ox JOIN genres gx ON gx.id = ox.genre_id WHERE ox.track_id = t.id),
			(SELECT GROUP_CONCAT(gx.name, ',' ORDER BY rx.position, gx.id) FROM track_genres rx JOIN genres gx ON gx.id = rx.genre_id WHERE rx.track_id = t.id), '')`

// taggingStampSQL 只由会进入打标输入的内容组成（标题、艺术家、专辑、年份、时长、类型、流派、
// 制作人员、音频文件指纹（内嵌歌词随文件变化）、是否有外置 .lrc、声学特征、作品关联）。
// 外置 .lrc 只记录有无：只修改已有 .lrc 的内容不会触发（数据库不保存其修改时间）。不使用 tracks/albums 的
// updated_at：收藏、更换封面等与歌曲内容无关的操作也会刷新它们，不应触发重新打标。
// 修改此表达式时需要新增迁移，把已有 track_tag_meta / ai_tagging_failures 的旧 stamp 换算过来。
const taggingStampSQL = `COALESCE(t.user_title, t.title) || '|' || ` + trackArtistSQL + ` || '|' ||
		COALESCE(a.user_title, a.title) || '|' || ` + taggingAlbumArtistSQL + ` || '|' || COALESCE(a.user_release_year, a.release_year, 0) || '|' ||
		(SELECT COUNT(*) FROM tracks t2 WHERE t2.album_id = a.id) || '|' ||
		(COALESCE(t.duration_ms, 0) / 1000) || '|' || ` + effectiveTrackTypeSQL + ` || '|' || ` + taggingGenresSQL + ` || '|' ||
		COALESCE(t.user_composer, t.composer, '') || '|' || COALESCE(t.lyricist, '') || '|' || COALESCE(t.arranger, '') || '|' || LENGTH(COALESCE(t.lyrics, '')) || '|' ||
		(SELECT COALESCE(MAX(af.modified_at_ns), 0) || ':' || COALESCE(SUM(af.file_size), 0) || ':' || COALESCE(MAX(af.has_external_lrc), 0)
			FROM audio_files af WHERE af.track_id = t.id AND af.status = 'available') || '|' ||
		COALESCE(f.analysis_version || ':' || f.file_fingerprint || ':' || COALESCE(printf('%.2f/%.2f', f.arousal, f.valence), '-'), 'noaudio') || '|' ||
		(SELECT COUNT(*) || ':' || COALESCE(MAX(wt.created_at), '') FROM work_tracks wt WHERE wt.track_id = t.id) || '|' ||
		(SELECT COUNT(*) || ':' || COALESCE(MAX(aw.updated_at), '') FROM album_works aw WHERE aw.album_id = t.album_id)`

// taggingBase 列出可打标的曲目（有可用音频文件）及其 source_stamp（见 taggingStampSQL）；
// stamp 变化即需要重新检查（输入哈希不变时只更新 stamp，不调用模型）。
// ready：声学分析开启时，需要已有声学特征（且该版本有回归模型时唤醒度已回填），或声学分析已永久失败；
// 声学分析未开启时直接 ready（只用元数据和歌词）。参数：audioActive(0/1)。
const taggingBase = `WITH base AS (
	SELECT t.id AS track_id, t.added_at AS added_at,
		` + taggingStampSQL + ` AS stamp,
		CASE WHEN ? = 0
			OR (f.track_id IS NOT NULL AND NOT (f.arousal IS NULL AND EXISTS (SELECT 1 FROM audio_av_models m WHERE m.analysis_version = f.analysis_version AND m.active = 1)))
			OR EXISTS (SELECT 1 FROM audio_analysis_failures x WHERE x.track_id = t.id AND x.permanent = 1)
		THEN 1 ELSE 0 END AS ready
	FROM tracks t
	JOIN albums a ON a.id = t.album_id
	LEFT JOIN track_audio_features f ON f.track_id = t.id
	WHERE EXISTS (SELECT 1 FROM audio_files af WHERE af.track_id = t.id AND af.status = 'available')
)`

// TaggingCandidate 是一首待打标曲目。Fresh 表示从未打过标。
type TaggingCandidate struct {
	TrackID int64
	Stamp   string
	Fresh   bool
}

// NextTaggingCandidates 返回最多 limit 首需要（重新）打标的曲目：从未打标的优先（新入库的在前），
// 其次是 stamp 或词表/提示词版本变化的。exclude 为正在处理中的曲目。
func (s *Store) NextTaggingCandidates(ctx context.Context, audioActive bool, vocab, prompt int, now time.Time, exclude []int64, limit int) ([]TaggingCandidate, error) {
	args := []any{boolInt(audioActive), vocab, prompt, AnalysisTimestamp(now)}
	excl := ""
	if len(exclude) > 0 {
		excl = " AND b.track_id NOT IN (" + sqlPlaceholders(len(exclude)) + ")"
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, taggingBase+`
		SELECT b.track_id, b.stamp, m.track_id IS NULL FROM base b
		LEFT JOIN track_tag_meta m ON m.track_id = b.track_id
		WHERE b.ready = 1
		AND (m.track_id IS NULL OR m.source_stamp <> b.stamp OR m.vocab_version <> ? OR m.prompt_version <> ?)
		AND NOT EXISTS (SELECT 1 FROM ai_tagging_failures x WHERE x.track_id = b.track_id AND x.source_stamp = b.stamp
			AND (x.permanent = 1 OR x.next_retry_at > ?))`+excl+`
		ORDER BY (m.track_id IS NULL) DESC, b.added_at DESC, b.track_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaggingCandidate
	for rows.Next() {
		var c TaggingCandidate
		var fresh int
		if err := rows.Scan(&c.TrackID, &c.Stamp, &fresh); err != nil {
			return nil, err
		}
		c.Fresh = fresh != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// NextReviewCandidates 返回需要复核但还没复核过的曲目。
func (s *Store) NextReviewCandidates(ctx context.Context, now time.Time, exclude []int64, limit int) ([]TaggingCandidate, error) {
	args := []any{AnalysisTimestamp(now)}
	excl := ""
	if len(exclude) > 0 {
		excl = " AND m.track_id NOT IN (" + sqlPlaceholders(len(exclude)) + ")"
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT m.track_id, m.source_stamp FROM track_tag_meta m
		WHERE m.needs_review = 1 AND m.reviewed_by = ''
		AND NOT EXISTS (SELECT 1 FROM ai_tagging_failures x WHERE x.track_id = m.track_id AND (x.permanent = 1 OR x.next_retry_at > ?))`+excl+` ORDER BY m.track_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaggingCandidate
	for rows.Next() {
		var c TaggingCandidate
		if err := rows.Scan(&c.TrackID, &c.Stamp); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TrackTaggingStamp 返回一首曲目当前的 source_stamp。
func (s *Store) TrackTaggingStamp(ctx context.Context, trackID int64) (string, bool, error) {
	var stamp string
	err := s.db.QueryRowContext(ctx, taggingBase+` SELECT stamp FROM base WHERE track_id = ?`, 0, trackID).Scan(&stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return stamp, err == nil, err
}

// ---------------------------------------------------------------- input

// TaggingWork 是曲目（或其专辑）关联的作品。
type TaggingWork struct {
	Title string
	Type  string
	Role  string
}

// TaggingTrackInfo 是打标所需的元数据（已应用人工覆盖）。
type TaggingTrackInfo struct {
	TrackID     int64
	AlbumID     int64
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Year        int
	DurationMs  int64
	TrackType   string
	Genres      []string
	Composer    string
	Lyricist    string
	Arranger    string
	Lyrics      string // 内嵌歌词（外置 .lrc 由调用方读取）
	AlbumTracks int
	Works       []TaggingWork
}

func (s *Store) TaggingTrackInfo(ctx context.Context, trackID int64) (TaggingTrackInfo, error) {
	var v TaggingTrackInfo
	var genres string
	var year sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT t.id, a.id, COALESCE(t.user_title, t.title), `+trackArtistSQL+`, COALESCE(a.user_title, a.title),
		`+taggingAlbumArtistSQL+`,
		COALESCE(a.user_release_year, a.release_year), COALESCE(t.duration_ms, 0), `+effectiveTrackTypeSQL+`,
		`+taggingGenresSQL+`,
		COALESCE(t.user_composer, t.composer, ''), COALESCE(t.lyricist, ''), COALESCE(t.arranger, ''), COALESCE(t.lyrics, ''),
		(SELECT COUNT(*) FROM tracks t2 WHERE t2.album_id = a.id)
		FROM tracks t JOIN albums a ON a.id = t.album_id WHERE t.id = ?`, trackID).
		Scan(&v.TrackID, &v.AlbumID, &v.Title, &v.Artist, &v.Album, &v.AlbumArtist, &year, &v.DurationMs, &v.TrackType, &genres,
			&v.Composer, &v.Lyricist, &v.Arranger, &v.Lyrics, &v.AlbumTracks)
	if err != nil {
		return v, err
	}
	v.Year = int(year.Int64)
	for _, g := range strings.Split(genres, ",") {
		if g = strings.TrimSpace(g); g != "" {
			v.Genres = append(v.Genres, g)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.title, w.type, wt.role FROM work_tracks wt JOIN works w ON w.id = wt.work_id WHERE wt.track_id = ?
		UNION ALL SELECT w.title, w.type, aw.role FROM album_works aw JOIN works w ON w.id = aw.work_id
			WHERE aw.album_id = ? AND NOT EXISTS (SELECT 1 FROM work_tracks wt2 WHERE wt2.track_id = ?)
		LIMIT 5`, trackID, v.AlbumID, trackID)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var w TaggingWork
		if err := rows.Scan(&w.Title, &w.Type, &w.Role); err != nil {
			return v, err
		}
		v.Works = append(v.Works, w)
	}
	return v, rows.Err()
}

// AlbumTrackFeatures 是专辑内曲目的声学特征（用于专辑声学摘要）。
type AlbumTrackFeatures struct {
	TrackID    int64
	Version    string
	Genre      []byte
	Instrument []byte
	HeadsJSON  string
	Arousal    *float64
}

func (s *Store) AlbumAudioFeatures(ctx context.Context, albumID int64, limit int) ([]AlbumTrackFeatures, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.track_id, f.analysis_version, f.genre, f.instrument, f.heads_json, f.arousal
		FROM track_audio_features f JOIN tracks t ON t.id = f.track_id WHERE t.album_id = ? ORDER BY t.id LIMIT ?`, albumID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlbumTrackFeatures
	for rows.Next() {
		var v AlbumTrackFeatures
		var a sql.NullFloat64
		if err := rows.Scan(&v.TrackID, &v.Version, &v.Genre, &v.Instrument, &v.HeadsJSON, &a); err != nil {
			return nil, err
		}
		if a.Valid {
			x := a.Float64
			v.Arousal = &x
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- results

// TrackTag 是一条标签。
type TrackTag struct {
	Kind       string  `json:"kind"`
	Tag        string  `json:"tag"`
	Weight     float64 `json:"weight"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
}

// TrackTagMeta 是一首曲目的打标状态。
type TrackTagMeta struct {
	TrackID          int64
	SourceStamp      string
	InputHash        string
	VocabVersion     int
	PromptVersion    int
	Model            string
	Energy           int
	Vocal            string
	Idol             string
	UsedAudio        bool
	UsedLyrics       bool
	NeedsReview      bool
	ReviewReasons    string
	ReviewedBy       string
	PromptTokens     int64
	CompletionTokens int64
	ResultJSON       string
	TaggedAt         string
}

func (s *Store) TrackTagMeta(ctx context.Context, trackID int64) (TrackTagMeta, bool, error) {
	var m TrackTagMeta
	var energy sql.NullInt64
	var vocal, idol sql.NullString
	var audio, lyrics, review int
	err := s.db.QueryRowContext(ctx, `SELECT track_id, source_stamp, input_hash, vocab_version, prompt_version, model, energy, vocal, idol,
		used_audio, used_lyrics, needs_review, review_reasons, reviewed_by, prompt_tokens, completion_tokens, result_json, tagged_at
		FROM track_tag_meta WHERE track_id = ?`, trackID).Scan(&m.TrackID, &m.SourceStamp, &m.InputHash, &m.VocabVersion, &m.PromptVersion,
		&m.Model, &energy, &vocal, &idol, &audio, &lyrics, &review, &m.ReviewReasons, &m.ReviewedBy, &m.PromptTokens, &m.CompletionTokens, &m.ResultJSON, &m.TaggedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m.Energy, m.Vocal, m.Idol = int(energy.Int64), vocal.String, idol.String
	m.UsedAudio, m.UsedLyrics, m.NeedsReview = audio != 0, lyrics != 0, review != 0
	return m, true, nil
}

// SaveTrackTagResult 原子写入一首曲目的打标结果：替换 llm/audio 来源的标签（保留 user），更新状态并清除失败记录。
func (s *Store) SaveTrackTagResult(ctx context.Context, m TrackTagMeta, llm, audio []TrackTag) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_tags WHERE track_id = ? AND source IN ('llm', 'audio')`, m.TrackID); err != nil {
		return err
	}
	for _, group := range [][]TrackTag{llm, audio} {
		for _, t := range group {
			if _, err = tx.ExecContext(ctx, `INSERT INTO track_tags(track_id, kind, tag, weight, confidence, source) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(track_id, kind, tag, source) DO UPDATE SET weight = excluded.weight, confidence = excluded.confidence`,
				m.TrackID, t.Kind, t.Tag, clamp01(t.Weight), clamp01(t.Confidence), t.Source); err != nil {
				return err
			}
		}
	}
	var energy, vocal, idol any
	if m.Energy >= 1 && m.Energy <= 5 {
		energy = m.Energy
	}
	if m.Vocal != "" {
		vocal = m.Vocal
	}
	if m.Idol != "" {
		idol = m.Idol
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO track_tag_meta(track_id, source_stamp, input_hash, vocab_version, prompt_version, model, energy, vocal, idol,
		used_audio, used_lyrics, needs_review, review_reasons, reviewed_by, prompt_tokens, completion_tokens, result_json, tagged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(track_id) DO UPDATE SET source_stamp = excluded.source_stamp, input_hash = excluded.input_hash,
			vocab_version = excluded.vocab_version, prompt_version = excluded.prompt_version, model = excluded.model, energy = excluded.energy,
			vocal = excluded.vocal, idol = excluded.idol, used_audio = excluded.used_audio, used_lyrics = excluded.used_lyrics,
			needs_review = excluded.needs_review, review_reasons = excluded.review_reasons, reviewed_by = excluded.reviewed_by,
			prompt_tokens = excluded.prompt_tokens, completion_tokens = excluded.completion_tokens, result_json = excluded.result_json,
			tagged_at = excluded.tagged_at`,
		m.TrackID, m.SourceStamp, m.InputHash, m.VocabVersion, m.PromptVersion, m.Model, energy, vocal, idol,
		boolInt(m.UsedAudio), boolInt(m.UsedLyrics), boolInt(m.NeedsReview), m.ReviewReasons, m.ReviewedBy,
		m.PromptTokens, m.CompletionTokens, m.ResultJSON); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM ai_tagging_failures WHERE track_id = ?`, m.TrackID); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchTrackTagStamp 在输入内容未变时只更新 stamp（不调用模型）。
func (s *Store) TouchTrackTagStamp(ctx context.Context, trackID int64, stamp string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE track_tag_meta SET source_stamp = ? WHERE track_id = ?`, stamp, trackID)
	return err
}

// ResetTrackTagging 删除一首曲目的打标状态与失败记录，使其重新排队（保留人工修正）。
func (s *Store) ResetTrackTagging(ctx context.Context, trackID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM track_tag_meta WHERE track_id = ?`, `DELETE FROM ai_tagging_failures WHERE track_id = ?`} {
		if _, err = tx.ExecContext(ctx, q, trackID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TrackTags 返回一首曲目所有来源的标签。
func (s *Store) TrackTags(ctx context.Context, trackID int64) ([]TrackTag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, tag, weight, confidence, source FROM track_tags WHERE track_id = ?
		ORDER BY CASE source WHEN 'user' THEN 0 WHEN 'llm' THEN 1 ELSE 2 END, kind, weight DESC, confidence DESC, tag`, trackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrackTag{}
	for rows.Next() {
		var t TrackTag
		if err := rows.Scan(&t.Kind, &t.Tag, &t.Weight, &t.Confidence, &t.Source); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetUserTrackTags 替换某一类（mood/style）的人工修正；tags 为空表示清除修正、恢复使用自动结果。
func (s *Store) SetUserTrackTags(ctx context.Context, trackID int64, kind string, tags []TrackTag) error {
	if kind != "mood" && kind != "style" {
		return fmt.Errorf("invalid tag kind %q", kind)
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id = ?`, trackID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_tags WHERE track_id = ? AND kind = ? AND source = 'user'`, trackID, kind); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err = tx.ExecContext(ctx, `INSERT INTO track_tags(track_id, kind, tag, weight, confidence, source) VALUES (?, ?, ?, ?, 1, 'user')
			ON CONFLICT(track_id, kind, tag, source) DO UPDATE SET weight = excluded.weight`, trackID, kind, t.Tag, clamp01(t.Weight)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- failures

// AITaggingFailure 是一条打标失败记录。
type AITaggingFailure struct {
	TrackID     int64
	Title       string
	Artist      string
	Attempts    int
	Permanent   bool
	LastError   string
	NextRetryAt string
	UpdatedAt   string
}

// RecordTaggingFailure 记录失败并计算下次重试时间（10 分钟、1 小时、6 小时、24 小时）；
// 达到 permanentAfter 次后标记为永久失败（stamp 变化前不再重试）。
func (s *Store) RecordTaggingFailure(ctx context.Context, trackID int64, stamp, message string, now time.Time, permanentAfter int) (int, bool, error) {
	attempts := 0
	var prevStamp string
	err := s.db.QueryRowContext(ctx, `SELECT attempts, source_stamp FROM ai_tagging_failures WHERE track_id = ?`, trackID).Scan(&attempts, &prevStamp)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if prevStamp != stamp {
		attempts = 0
	}
	attempts++
	permanent := permanentAfter > 0 && attempts >= permanentAfter
	delays := []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}
	next := now.Add(delays[min(attempts-1, len(delays)-1)])
	if r := []rune(message); len(r) > 500 {
		message = string(r[:500])
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO ai_tagging_failures(track_id, source_stamp, attempts, permanent, last_error, next_retry_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(track_id) DO UPDATE SET source_stamp = excluded.source_stamp, attempts = excluded.attempts, permanent = excluded.permanent,
			last_error = excluded.last_error, next_retry_at = excluded.next_retry_at, updated_at = excluded.updated_at`,
		trackID, stamp, attempts, boolInt(permanent), message, AnalysisTimestamp(next), AnalysisTimestamp(now))
	return attempts, permanent, err
}

// ClearTaggingFailures 删除全部失败记录（“全部重试”）。
func (s *Store) ClearTaggingFailures(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ai_tagging_failures`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) TaggingFailures(ctx context.Context, limit int) ([]AITaggingFailure, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT x.track_id, COALESCE(t.user_title, t.title), `+trackArtistSQL+`, x.attempts, x.permanent, x.last_error, x.next_retry_at, x.updated_at
		FROM ai_tagging_failures x JOIN tracks t ON t.id = x.track_id JOIN albums a ON a.id = t.album_id
		ORDER BY x.permanent DESC, x.updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AITaggingFailure
	for rows.Next() {
		var f AITaggingFailure
		var permanent int
		if err := rows.Scan(&f.TrackID, &f.Title, &f.Artist, &f.Attempts, &permanent, &f.LastError, &f.NextRetryAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.Permanent = permanent != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- usage & stats

// AIUsage 是一次或多次调用的用量。
type AIUsage struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	CachedTokens     int64 `json:"cachedTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
}

// Add 累加。
func (u *AIUsage) Add(o AIUsage) {
	u.Requests += o.Requests
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	u.CachedTokens += o.CachedTokens
	u.ReasoningTokens += o.ReasoningTokens
}

// AddAIUsage 累加到当天（UTC）该模型、用途的用量。
func (s *Store) AddAIUsage(ctx context.Context, now time.Time, model, purpose string, u AIUsage) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_usage(day, model, purpose, requests, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(day, model, purpose) DO UPDATE SET requests = requests + excluded.requests, prompt_tokens = prompt_tokens + excluded.prompt_tokens,
			completion_tokens = completion_tokens + excluded.completion_tokens, cached_tokens = cached_tokens + excluded.cached_tokens,
			reasoning_tokens = reasoning_tokens + excluded.reasoning_tokens`,
		now.UTC().Format("2006-01-02"), model, purpose, u.Requests, u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.ReasoningTokens)
	return err
}

// AIUsageRow 是按模型和用途汇总的用量。
type AIUsageRow struct {
	Model   string  `json:"model"`
	Purpose string  `json:"purpose"`
	Usage   AIUsage `json:"usage"`
}

// AIUsageTotals 返回 since（UTC 日期，含）以来的用量，按模型和用途汇总；since 为空表示全部。
func (s *Store) AIUsageTotals(ctx context.Context, since string) ([]AIUsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, purpose, SUM(requests), SUM(prompt_tokens), SUM(completion_tokens), SUM(cached_tokens), SUM(reasoning_tokens)
		FROM ai_usage WHERE day >= ? GROUP BY model, purpose ORDER BY model, purpose`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AIUsageRow
	for rows.Next() {
		var r AIUsageRow
		if err := rows.Scan(&r.Model, &r.Purpose, &r.Usage.Requests, &r.Usage.PromptTokens, &r.Usage.CompletionTokens, &r.Usage.CachedTokens, &r.Usage.ReasoningTokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TaggingStats 是打标进度。
type TaggingStats struct {
	Eligible      int64   `json:"eligible"`      // 有可用音频文件的曲目
	Tagged        int64   `json:"tagged"`        // 打标结果为最新
	Stale         int64   `json:"stale"`         // 打过标，但输入已变化，等待重新打标
	WaitingAudio  int64   `json:"waitingAudio"`  // 未打标且在等待声学分析
	NeedsReview   int64   `json:"needsReview"`   // 被判定为需要复核（含已复核）
	Reviewed      int64   `json:"reviewed"`      // 已由复核模型重新打标
	UserCorrected int64   `json:"userCorrected"` // 有人工修正
	RetryPending  int64   `json:"retryPending"`  // 退避中
	Permanent     int64   `json:"permanent"`     // 多次失败，暂停重试
	AvgPrompt     float64 `json:"avgPromptTokens"`
	AvgCompletion float64 `json:"avgCompletionTokens"`
	LastTaggedAt  string  `json:"lastTaggedAt,omitempty"`
}

func (s *Store) TaggingStats(ctx context.Context, audioActive bool, vocab, prompt int, now time.Time) (TaggingStats, error) {
	var st TaggingStats
	ts := AnalysisTimestamp(now)
	err := s.db.QueryRowContext(ctx, taggingBase+`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN m.track_id IS NOT NULL AND m.source_stamp = b.stamp AND m.vocab_version = ? AND m.prompt_version = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN m.track_id IS NOT NULL AND NOT (m.source_stamp = b.stamp AND m.vocab_version = ? AND m.prompt_version = ?) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN b.ready = 0 AND m.track_id IS NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN x.track_id IS NOT NULL AND x.source_stamp = b.stamp AND x.permanent = 0 AND x.next_retry_at > ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN x.track_id IS NOT NULL AND x.source_stamp = b.stamp AND x.permanent = 1 THEN 1 ELSE 0 END), 0)
		FROM base b
		LEFT JOIN track_tag_meta m ON m.track_id = b.track_id
		LEFT JOIN ai_tagging_failures x ON x.track_id = b.track_id`,
		boolInt(audioActive), vocab, prompt, vocab, prompt, ts).Scan(&st.Eligible, &st.Tagged, &st.Stale, &st.WaitingAudio, &st.RetryPending, &st.Permanent)
	if err != nil {
		return st, err
	}
	var last sql.NullString
	var avgP, avgC sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM track_tag_meta WHERE needs_review = 1),
		(SELECT COUNT(*) FROM track_tag_meta WHERE reviewed_by <> ''),
		(SELECT COUNT(DISTINCT track_id) FROM track_tags WHERE source = 'user'),
		(SELECT MAX(tagged_at) FROM track_tag_meta)`).Scan(&st.NeedsReview, &st.Reviewed, &st.UserCorrected, &last); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT AVG(prompt_tokens), AVG(completion_tokens) FROM (SELECT prompt_tokens, completion_tokens FROM track_tag_meta
		WHERE prompt_tokens > 0 ORDER BY tagged_at DESC LIMIT 200)`).Scan(&avgP, &avgC); err != nil {
		return st, err
	}
	st.AvgPrompt, st.AvgCompletion, st.LastTaggedAt = avgP.Float64, avgC.Float64, last.String
	return st, nil
}

// RecentTagResult 是最近打标的一首曲目（供抽查）。
type RecentTagResult struct {
	TrackID     int64
	Title       string
	Artist      string
	Model       string
	ReviewedBy  string
	NeedsReview bool
	TaggedAt    string
	Tags        []TrackTag // 生效标签（user 优先于 llm）
}

func (s *Store) RecentTagResults(ctx context.Context, limit int) ([]RecentTagResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.track_id, COALESCE(t.user_title, t.title), `+trackArtistSQL+`, m.model, m.reviewed_by, m.needs_review, m.tagged_at
		FROM track_tag_meta m JOIN tracks t ON t.id = m.track_id JOIN albums a ON a.id = t.album_id ORDER BY m.tagged_at DESC, m.track_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var out []RecentTagResult
	for rows.Next() {
		var r RecentTagResult
		var review int
		if err := rows.Scan(&r.TrackID, &r.Title, &r.Artist, &r.Model, &r.ReviewedBy, &review, &r.TaggedAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.NeedsReview = review != 0
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		tags, err := s.TrackTags(ctx, out[i].TrackID)
		if err != nil {
			return nil, err
		}
		out[i].Tags = EffectiveTags(tags)
	}
	return out, nil
}

// EffectiveTags 按类别取生效来源：有 user 用 user，否则 llm，否则 audio。
func EffectiveTags(all []TrackTag) []TrackTag {
	rank := map[string]int{"user": 0, "llm": 1, "audio": 2}
	best := map[string]int{"mood": 9, "style": 9}
	for _, t := range all {
		if r, ok := rank[t.Source]; ok && r < best[t.Kind] {
			best[t.Kind] = r
		}
	}
	out := []TrackTag{}
	for _, t := range all {
		if r, ok := rank[t.Source]; ok && r == best[t.Kind] {
			out = append(out, t)
		}
	}
	return out
}

// ---------------------------------------------------------------- sample comparison

// AISampleRun 是一次抽样对比。
type AISampleRun struct {
	ID           int64  `json:"id"`
	Size         int    `json:"size"`
	PrimaryModel string `json:"primaryModel"`
	ReviewModel  string `json:"reviewModel"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	CreatedAt    string `json:"createdAt"`
	FinishedAt   string `json:"finishedAt,omitempty"`
	Done         int    `json:"done"`
}

// AISampleItem 是抽样中的一首曲目及两个模型的结果（JSON）。
type AISampleItem struct {
	TrackID     int64
	Title       string
	Artist      string
	PrimaryJSON string
	ReviewJSON  string
	Error       string
}

// CreateAISampleRun 新建抽样（删除旧的抽样），按伪随机顺序选出 size 首曲目（有声学特征的优先）。
func (s *Store) CreateAISampleRun(ctx context.Context, size int, primary, review string, seed int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM ai_sample_runs`); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO ai_sample_runs(size, primary_model, review_model, status) VALUES (?, ?, ?, 'pending')`, size, primary, review)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_sample_items(run_id, track_id)
		SELECT ?, t.id FROM tracks t
		WHERE EXISTS (SELECT 1 FROM audio_files af WHERE af.track_id = t.id AND af.status = 'available')
		ORDER BY EXISTS (SELECT 1 FROM track_audio_features f WHERE f.track_id = t.id) DESC, ((t.id + ?) * 2654435761) % 4294967296, t.id LIMIT ?`,
		id, seed%1000003, size); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) LatestAISampleRun(ctx context.Context) (AISampleRun, bool, error) {
	var r AISampleRun
	var finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, size, primary_model, review_model, status, error, created_at, finished_at,
		(SELECT COUNT(*) FROM ai_sample_items i WHERE i.run_id = r.id AND (i.primary_json IS NOT NULL OR i.error <> ''))
		FROM ai_sample_runs r ORDER BY id DESC LIMIT 1`).Scan(&r.ID, &r.Size, &r.PrimaryModel, &r.ReviewModel, &r.Status, &r.Error, &r.CreatedAt, &finished, &r.Done)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	r.FinishedAt = finished.String
	return r, err == nil, err
}

// PendingAISampleItems 返回抽样中尚未处理的曲目。
func (s *Store) PendingAISampleItems(ctx context.Context, runID int64, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT track_id FROM ai_sample_items WHERE run_id = ? AND primary_json IS NULL AND error = '' ORDER BY track_id LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) SaveAISampleItem(ctx context.Context, runID, trackID int64, primaryJSON, reviewJSON, message string) error {
	var p, r any
	if primaryJSON != "" {
		p = primaryJSON
	}
	if reviewJSON != "" {
		r = reviewJSON
	}
	if p == nil && message == "" {
		message = "no result"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE ai_sample_items SET primary_json = ?, review_json = ?, error = ? WHERE run_id = ? AND track_id = ?`, p, r, message, runID, trackID)
	return err
}

func (s *Store) SetAISampleRunStatus(ctx context.Context, runID int64, status, message string, now time.Time) error {
	finished := ""
	if status == "done" || status == "cancelled" || status == "failed" {
		finished = AnalysisTimestamp(now)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE ai_sample_runs SET status = ?, error = ?, finished_at = NULLIF(?, '') WHERE id = ?`, status, message, finished, runID)
	return err
}

func (s *Store) AISampleItems(ctx context.Context, runID int64) ([]AISampleItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.track_id, COALESCE(t.user_title, t.title), `+trackArtistSQL+`, COALESCE(i.primary_json, ''), COALESCE(i.review_json, ''), i.error
		FROM ai_sample_items i JOIN tracks t ON t.id = i.track_id JOIN albums a ON a.id = t.album_id WHERE i.run_id = ? ORDER BY i.track_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AISampleItem
	for rows.Next() {
		var v AISampleItem
		if err := rows.Scan(&v.TrackID, &v.Title, &v.Artist, &v.PrimaryJSON, &v.ReviewJSON, &v.Error); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- helpers

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// FormatTokenCount 把 token 数格式化为易读形式（如 1.2 万、3.50 亿）。
func FormatTokenCount(n int64) string {
	if n >= 100000000 {
		return strconv.FormatFloat(float64(n)/100000000, 'f', 2, 64) + " 亿"
	}
	if n >= 10000 {
		return strconv.FormatFloat(float64(n)/10000, 'f', 1, 64) + " 万"
	}
	return strconv.FormatInt(n, 10)
}

// ---------------------------------------------------------------- stamp upgrade

// legacyTaggingStampV1SQL 是迁移 055 之前的 stamp（基于 tracks/albums.updated_at）。仅用于一次性换算。
const legacyTaggingStampV1SQL = `t.updated_at || '|' || a.updated_at || '|' ||
		COALESCE(f.analysis_version || ':' || f.file_fingerprint || ':' || COALESCE(printf('%.2f/%.2f', f.arousal, f.valence), '-'), 'noaudio') || '|' ||
		(SELECT COUNT(*) || ':' || COALESCE(MAX(wt.created_at), '') FROM work_tracks wt WHERE wt.track_id = t.id) || '|' ||
		(SELECT COUNT(*) || ':' || COALESCE(MAX(aw.updated_at), '') FROM album_works aw WHERE aw.album_id = t.album_id)`

// upgradeTaggingStampsV2 把仍与旧 stamp 一致（即打标结果为最新）的记录换算为新 stamp，避免更换 stamp
// 公式后全库重新排队；旧 stamp 已过期的记录保持不变，照常重新检查。
func upgradeTaggingStampsV2(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"track_tag_meta", "ai_tagging_failures"} {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET source_stamp = s.new_stamp FROM (
			SELECT t.id AS track_id, `+legacyTaggingStampV1SQL+` AS old_stamp, `+taggingStampSQL+` AS new_stamp
			FROM `+table+` x JOIN tracks t ON t.id = x.track_id JOIN albums a ON a.id = t.album_id
			LEFT JOIN track_audio_features f ON f.track_id = t.id) s
			WHERE s.track_id = `+table+`.track_id AND s.old_stamp = `+table+`.source_stamp AND s.new_stamp IS NOT NULL`); err != nil {
			return fmt.Errorf("upgrade %s stamps: %w", table, err)
		}
	}
	return nil
}
