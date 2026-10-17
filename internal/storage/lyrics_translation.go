package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// LyricsTranslationSettings 是歌词翻译的开关与模型。接口地址与 API Key 复用 AISettings。
type LyricsTranslationSettings struct {
	Enabled       bool
	Model         string // 空表示使用 AI 打标的主模型
	FallbackModel string // 空表示不回退；不继承打标后备模型
}

// EffectiveTranslationModel 返回实际使用的模型：翻译模型优先，否则打标主模型。
func EffectiveTranslationModel(ai AISettings, tr LyricsTranslationSettings) string {
	if m := strings.TrimSpace(tr.Model); m != "" {
		return m
	}
	return strings.TrimSpace(ai.Model)
}

// TranslationConfigured 表示接口地址、API Key 和（翻译或打标）模型都已填写。
func TranslationConfigured(ai AISettings, tr LyricsTranslationSettings) bool {
	return strings.TrimSpace(ai.BaseURL) != "" && ai.APIKey != "" && EffectiveTranslationModel(ai, tr) != ""
}

func (s *Store) LyricsTranslationSettings(ctx context.Context) (LyricsTranslationSettings, error) {
	var v LyricsTranslationSettings
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT translation_enabled, translation_model, translation_fallback_model FROM ai_settings WHERE id = 1`).Scan(&enabled, &v.Model, &v.FallbackModel)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	v.Enabled = enabled != 0
	return v, err
}

// SaveLyricsTranslationSettings 只写翻译设置，不影响 AI 打标设置。
func (s *Store) SaveLyricsTranslationSettings(ctx context.Context, v LyricsTranslationSettings) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_settings(id, translation_enabled, translation_model, translation_fallback_model) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET translation_enabled = excluded.translation_enabled, translation_model = excluded.translation_model, translation_fallback_model = excluded.translation_fallback_model,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, boolInt(v.Enabled), strings.TrimSpace(v.Model), strings.TrimSpace(v.FallbackModel))
	return err
}

// LyricsSourceHash 是原文逐行内容的哈希，用作缓存键的一部分。
func LyricsSourceHash(lines []string) string {
	raw, _ := json.Marshal(lines)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// LyricsTranslation 是一条缓存的翻译。
type LyricsTranslation struct {
	ID               int64
	TrackID          int64
	Title, Artist    string
	TargetLanguage   string
	SourceHash       string
	SourceLines      []string
	TranslatedLines  []string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	CreatedAt        string
	UpdatedAt        string
}

const lyricsTranslationColumns = `lt.id, lt.track_id, COALESCE(t.user_title, t.title), ` + trackArtistSQL + `, lt.target_language, lt.source_hash,
	lt.source_lines, lt.translated_lines, lt.model, lt.prompt_tokens, lt.completion_tokens, lt.created_at, lt.updated_at
	FROM lyrics_translations lt JOIN tracks t ON t.id = lt.track_id JOIN albums a ON a.id = t.album_id`

func scanLyricsTranslation(row interface{ Scan(...any) error }) (LyricsTranslation, error) {
	var v LyricsTranslation
	var src, dst string
	if err := row.Scan(&v.ID, &v.TrackID, &v.Title, &v.Artist, &v.TargetLanguage, &v.SourceHash, &src, &dst, &v.Model,
		&v.PromptTokens, &v.CompletionTokens, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(src), &v.SourceLines); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(dst), &v.TranslatedLines); err != nil {
		return v, err
	}
	return v, nil
}

// CachedLyricsTranslation 按缓存键查找；没有时返回 sql.ErrNoRows。
func (s *Store) CachedLyricsTranslation(ctx context.Context, trackID int64, language, sourceHash string) (LyricsTranslation, error) {
	return scanLyricsTranslation(s.db.QueryRowContext(ctx, `SELECT `+lyricsTranslationColumns+`
		WHERE lt.track_id = ? AND lt.target_language = ? AND lt.source_hash = ?`, trackID, language, sourceHash))
}

// LyricsTranslationByID 返回一条翻译；没有时返回 sql.ErrNoRows。
func (s *Store) LyricsTranslationByID(ctx context.Context, id int64) (LyricsTranslation, error) {
	return scanLyricsTranslation(s.db.QueryRowContext(ctx, `SELECT `+lyricsTranslationColumns+` WHERE lt.id = ?`, id))
}

// SaveLyricsTranslation 写入或覆盖（同一缓存键）一条翻译，返回其 id。
// 曲目不存在时返回 sql.ErrNoRows。
func (s *Store) SaveLyricsTranslation(ctx context.Context, v LyricsTranslation) (int64, error) {
	if len(v.SourceLines) != len(v.TranslatedLines) {
		return 0, errors.New("source and translated lines differ in length")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, v.TrackID).Scan(&exists); err != nil {
		return 0, err
	}
	src, _ := json.Marshal(v.SourceLines)
	dst, _ := json.Marshal(v.TranslatedLines)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO lyrics_translations(track_id, target_language, source_hash, source_lines, translated_lines, model, prompt_tokens, completion_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(track_id, target_language, source_hash) DO UPDATE SET translated_lines = excluded.translated_lines, model = excluded.model,
			prompt_tokens = excluded.prompt_tokens, completion_tokens = excluded.completion_tokens,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		v.TrackID, v.TargetLanguage, v.SourceHash, string(src), string(dst), v.Model, v.PromptTokens, v.CompletionTokens); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM lyrics_translations WHERE track_id = ? AND target_language = ? AND source_hash = ?`,
		v.TrackID, v.TargetLanguage, v.SourceHash).Scan(&id)
	return id, err
}

// LyricsTranslationFilter 是管理页的筛选条件。Query 匹配曲名或艺人。
type LyricsTranslationFilter struct {
	Query    string
	Language string
	Limit    int
	Offset   int
}

// ListLyricsTranslations 按更新时间倒序列出翻译，并返回符合条件的总数。
func (s *Store) ListLyricsTranslations(ctx context.Context, f LyricsTranslationFilter) ([]LyricsTranslation, int64, error) {
	where := []string{"1 = 1"}
	var args []any
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, `(COALESCE(t.user_title, t.title) LIKE '%'||?||'%' OR `+trackArtistSQL+` LIKE '%'||?||'%')`)
		args = append(args, q, q)
	}
	if f.Language != "" {
		where = append(where, "lt.target_language = ?")
		args = append(args, f.Language)
	}
	cond := strings.Join(where, " AND ")
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lyrics_translations lt JOIN tracks t ON t.id = lt.track_id JOIN albums a ON a.id = t.album_id WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+lyricsTranslationColumns+` WHERE `+cond+` ORDER BY lt.updated_at DESC, lt.id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LyricsTranslation
	for rows.Next() {
		v, err := scanLyricsTranslation(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// LyricsTranslationLanguages 列出已有翻译的目标语言（供筛选）。
func (s *Store) LyricsTranslationLanguages(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT target_language FROM lyrics_translations ORDER BY target_language`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteLyricsTranslations 删除指定的翻译，返回删除条数。
func (s *Store) DeleteLyricsTranslations(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM lyrics_translations WHERE id IN (`+sqlPlaceholders(len(ids))+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
