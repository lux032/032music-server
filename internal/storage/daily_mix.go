package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Daily mixes (docs/daily-mix-design.md §4–9): settings, the per-track features the
// generator needs, persisted mixes, theme statistics and explicit feedback. The
// generation logic lives in internal/dailymix.

// ---------------------------------------------------------------- settings

// DailyMixSettings is the single settings row.
type DailyMixSettings struct {
	Enabled         bool   `json:"enabled"`
	MixCount        int    `json:"mixCount"`
	TrackCount      int    `json:"trackCount"`
	ExplorePercent  int    `json:"explorePercent"`
	PregenerateHour int    `json:"pregenerateHour"`
	RegenerateLimit int    `json:"regenerateLimit"`
	AllowSad        bool   `json:"allowSad"`
	LLMTitles       bool   `json:"llmTitles"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

// DefaultDailyMixSettings: enabled, 5 mixes × 25 tracks, 30% exploration, generated
// at 05:00 (UTC+8), 3 replacements a day, no sad tracks, model-written titles.
func DefaultDailyMixSettings() DailyMixSettings {
	return DailyMixSettings{Enabled: true, MixCount: 5, TrackCount: 25, ExplorePercent: 30, PregenerateHour: 5, RegenerateLimit: 3, LLMTitles: true}
}

// Validate checks the ranges enforced by the schema.
func (v DailyMixSettings) Validate() error {
	switch {
	case v.MixCount < 1 || v.MixCount > 8:
		return errors.New("歌单数量应为 1–8")
	case v.TrackCount < 10 || v.TrackCount > 60:
		return errors.New("每个歌单的曲数应为 10–60")
	case v.ExplorePercent < 20 || v.ExplorePercent > 60:
		return errors.New("探索比例应为 20%–60%")
	case v.PregenerateHour < 0 || v.PregenerateHour > 23:
		return errors.New("生成时刻应为 0–23 点")
	case v.RegenerateLimit < 0 || v.RegenerateLimit > 10:
		return errors.New("每天换一个的次数应为 0–10")
	}
	return nil
}

func (s *Store) DailyMixSettings(ctx context.Context) (DailyMixSettings, error) {
	v := DefaultDailyMixSettings()
	err := s.db.QueryRowContext(ctx, `SELECT enabled, mix_count, track_count, explore_percent, pregenerate_hour, regenerate_limit, allow_sad, llm_titles, updated_at
		FROM daily_mix_settings WHERE id = 1`).Scan(&v.Enabled, &v.MixCount, &v.TrackCount, &v.ExplorePercent, &v.PregenerateHour, &v.RegenerateLimit, &v.AllowSad, &v.LLMTitles, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	return v, err
}

func (s *Store) SaveDailyMixSettings(ctx context.Context, v DailyMixSettings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO daily_mix_settings(id, enabled, mix_count, track_count, explore_percent, pregenerate_hour, regenerate_limit, allow_sad, llm_titles, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled, mix_count = excluded.mix_count, track_count = excluded.track_count,
			explore_percent = excluded.explore_percent, pregenerate_hour = excluded.pregenerate_hour, regenerate_limit = excluded.regenerate_limit,
			allow_sad = excluded.allow_sad, llm_titles = excluded.llm_titles, updated_at = excluded.updated_at`,
		boolInt(v.Enabled), v.MixCount, v.TrackCount, v.ExplorePercent, v.PregenerateHour, v.RegenerateLimit, boolInt(v.AllowSad), boolInt(v.LLMTitles))
	return err
}

// ---------------------------------------------------------------- generator input

// RecTag is one effective tag score (weight × confidence of the winning source).
type RecTag struct {
	Weight     float64
	Confidence float64
	Source     string
}

// Score is the hit strength used by the generator.
func (t RecTag) Score() float64 { return t.Weight * t.Confidence }

// RecTrack is everything the generator knows about one playable track.
type RecTrack struct {
	ID          int64
	AlbumID     int64
	Title       string
	TitleKey    string // normalised title + primary artists, for de-duplication
	ArtistIDs   []int64
	ArtistNames []string
	Composers   []int64
	Works       []int64
	TrackType   string
	DurationMs  int64
	Favorite    bool
	InPlaylist  bool
	PlayCount   int64
	SkipCount   int64
	LastPlayed  time.Time // zero when never played
	AddedAt     time.Time
	Energy      int // 1–5; 0 when unknown
	Vocal       string
	Arousal     *float64
	Moods       map[string]RecTag
	Styles      map[string]RecTag
	TagSource   string // "user", "llm", "audio" or "" (best source among the effective tags)
	Blocked     bool
}

// NonMain reports instrumentals, TV sizes, drama tracks etc.
func (t *RecTrack) NonMain() bool { return nonMain(t.TrackType) }

// RecLibrary loads every playable track (available audio file, positive duration).
func (s *Store) RecLibrary(ctx context.Context) ([]RecTrack, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.album_id, COALESCE(t.user_title, t.title),
		COALESCE(NULLIF(t.user_track_type, ''), NULLIF(t.track_type, ''), 'regular'), COALESCE(t.duration_ms, 0), t.is_favorite, t.added_at,
		COALESCE(pp.play_count, 0), COALESCE(pp.skip_count, 0), COALESCE(pp.last_played_at, ''),
		EXISTS (SELECT 1 FROM playlist_items pi WHERE pi.track_id = t.id),
		COALESCE(m.energy, 0), COALESCE(m.vocal, ''), f.arousal,
		EXISTS (SELECT 1 FROM recommendation_track_blocks b WHERE b.track_id = t.id)
		FROM tracks t
		LEFT JOIN playback_progress pp ON pp.track_id = t.id
		LEFT JOIN track_tag_meta m ON m.track_id = t.id
		LEFT JOIN track_audio_features f ON f.track_id = t.id
		WHERE COALESCE(t.duration_ms, 0) > 0 AND EXISTS (SELECT 1 FROM audio_files af WHERE af.track_id = t.id AND af.status = 'available')
		ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	var out []RecTrack
	byID := map[int64]int{}
	for rows.Next() {
		var t RecTrack
		var added, played string
		var arousal sql.NullFloat64
		if err = rows.Scan(&t.ID, &t.AlbumID, &t.Title, &t.TrackType, &t.DurationMs, &t.Favorite, &added, &t.PlayCount, &t.SkipCount, &played,
			&t.InPlaylist, &t.Energy, &t.Vocal, &arousal, &t.Blocked); err != nil {
			rows.Close()
			return nil, err
		}
		t.AddedAt = parseStoredTime(added)
		t.LastPlayed = parseStoredTime(played)
		t.Arousal = nullFloat(arousal)
		byID[t.ID] = len(out)
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Primary artists (canonical ids and names).
	if err = s.recPairs(ctx, `SELECT ta.track_id, COALESCE(ar.merged_into_artist_id, ar.id),
		COALESCE((SELECT COALESCE(c.user_display_name, c.display_name) FROM artists c WHERE c.id = COALESCE(ar.merged_into_artist_id, ar.id)), '')
		FROM track_artists ta JOIN artists ar ON ar.id = ta.artist_id WHERE ta.role = 'primary' ORDER BY ta.track_id, ta.position`,
		func(id, ref int64, name string) {
			if i, ok := byID[id]; ok && !containsID(out[i].ArtistIDs, ref) {
				out[i].ArtistIDs = append(out[i].ArtistIDs, ref)
				out[i].ArtistNames = append(out[i].ArtistNames, name)
			}
		}); err != nil {
		return nil, err
	}
	if err = s.recPairs(ctx, `SELECT ta.track_id, COALESCE(ar.merged_into_artist_id, ar.id), '' FROM track_artists ta JOIN artists ar ON ar.id = ta.artist_id WHERE ta.role = 'composer'`,
		func(id, ref int64, _ string) {
			if i, ok := byID[id]; ok && !containsID(out[i].Composers, ref) {
				out[i].Composers = append(out[i].Composers, ref)
			}
		}); err != nil {
		return nil, err
	}
	if err = s.recPairs(ctx, `SELECT track_id, work_id, '' FROM work_tracks UNION SELECT t.id, aw.work_id, '' FROM tracks t JOIN album_works aw ON aw.album_id = t.album_id`,
		func(id, ref int64, _ string) {
			if i, ok := byID[id]; ok && !containsID(out[i].Works, ref) {
				out[i].Works = append(out[i].Works, ref)
			}
		}); err != nil {
		return nil, err
	}
	// Effective tags: user > llm > audio, per kind.
	tagRows, err := s.db.QueryContext(ctx, `SELECT track_id, kind, tag, weight, confidence, source FROM track_tags ORDER BY track_id`)
	if err != nil {
		return nil, err
	}
	defer tagRows.Close()
	var cur int64 = -1
	var pending []TrackTag
	flush := func() {
		if i, ok := byID[cur]; ok && len(pending) > 0 {
			t := &out[i]
			rank := map[string]int{"user": 0, "llm": 1, "audio": 2}
			best := 9
			for _, tg := range EffectiveTags(pending) {
				if tg.Kind == "mood" {
					if t.Moods == nil {
						t.Moods = map[string]RecTag{}
					}
					t.Moods[tg.Tag] = RecTag{tg.Weight, tg.Confidence, tg.Source}
				} else {
					if t.Styles == nil {
						t.Styles = map[string]RecTag{}
					}
					t.Styles[tg.Tag] = RecTag{tg.Weight, tg.Confidence, tg.Source}
				}
				if r := rank[tg.Source]; r < best {
					best = r
					t.TagSource = tg.Source
				}
			}
		}
		pending = pending[:0]
	}
	for tagRows.Next() {
		var id int64
		var tg TrackTag
		if err = tagRows.Scan(&id, &tg.Kind, &tg.Tag, &tg.Weight, &tg.Confidence, &tg.Source); err != nil {
			return nil, err
		}
		if id != cur {
			flush()
			cur = id
		}
		pending = append(pending, tg)
	}
	flush()
	if err = tagRows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		t := &out[i]
		t.TitleKey = normalizedTitle(t.Title) + "\x00" + fmt.Sprint(t.ArtistIDs)
	}
	return out, nil
}

func (s *Store) recPairs(ctx context.Context, q string, fn func(id, ref int64, name string)) error {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, ref int64
		var name string
		if err := rows.Scan(&id, &ref, &name); err != nil {
			return err
		}
		fn(id, ref, name)
	}
	return rows.Err()
}

func containsID(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

func parseStoredTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000Z", time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// RecentPlays returns, for sessions counted since the given time, the number of
// counted plays per track and the latest start (taste profile input).
func (s *Store) RecentPlays(ctx context.Context, since time.Time) (map[int64][]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT track_id, counted_at FROM playback_sessions WHERE counted_at IS NOT NULL AND counted_at >= ?`, AnalysisTimestamp(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]time.Time{}
	for rows.Next() {
		var id int64
		var at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = append(out[id], parseStoredTime(at))
	}
	return out, rows.Err()
}

// WorkTitles / ArtistNames resolve display names for theme titles.
func (s *Store) WorkTitles(ctx context.Context, ids []int64) (map[int64]WorkLabel, error) {
	out := map[int64]WorkLabel{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, COALESCE(translated_title, ''), type FROM works WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w WorkLabel
		if err := rows.Scan(&w.ID, &w.Title, &w.TranslatedTitle, &w.Type); err != nil {
			return nil, err
		}
		out[w.ID] = w
	}
	return out, rows.Err()
}

// WorkLabel is the display data of a work.
type WorkLabel struct {
	ID              int64
	Title           string
	TranslatedTitle string
	Type            string
}

func (s *Store) ArtistDisplayNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(user_display_name, display_name) FROM artists WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- mixes

// DailyMix is one stored mix.
type DailyMix struct {
	ID               int64   `json:"id"`
	Day              string  `json:"day"`
	Slot             int     `json:"slot"`
	Generation       int     `json:"generation"`
	Status           string  `json:"status"`
	ThemeType        string  `json:"themeType"`
	ThemeKey         string  `json:"themeKey"`
	Title            string  `json:"title"`
	Description      string  `json:"description"`
	TitleSource      string  `json:"titleSource"`
	PlanJSON         string  `json:"-"`
	GeneratorVersion int     `json:"generatorVersion"`
	DurationMs       int64   `json:"durationMillis"`
	TrackCount       int     `json:"trackCount"`
	Rating           int     `json:"rating"`
	RatingReasons    string  `json:"-"`
	RatedAt          string  `json:"ratedAt,omitempty"`
	RatingAlpha      float64 `json:"-"`
	RatingBeta       float64 `json:"-"`
	SavedPlaylistID  int64   `json:"savedPlaylistId,omitempty"`
	CreatedAt        string  `json:"createdAt"`
}

// DailyMixItem is one track of a mix.
type DailyMixItem struct {
	Position   int    `json:"position"`
	TrackID    int64  `json:"trackId"`
	Bucket     string `json:"bucket"`
	ReasonJSON string `json:"-"`
}

// NewDailyMix is a generated mix to persist.
type NewDailyMix struct {
	Slot             int
	ThemeType        string
	ThemeKey         string
	Title            string
	Description      string
	TitleSource      string
	PlanJSON         string
	GeneratorVersion int
	Items            []DailyMixItem
	DurationMs       int64
	// CooldownDays is the theme cooldown applied from this day.
	CooldownDays int
}

const dailyMixColumns = `m.id, m.day, m.slot, m.generation, m.status, m.theme_type, m.theme_key, m.title, m.description, m.title_source, m.plan_json,
	m.generator_version, m.duration_ms, (SELECT COUNT(*) FROM daily_mix_items i WHERE i.mix_id = m.id), m.rating, m.rating_reasons, COALESCE(m.rated_at, ''),
	m.rating_alpha, m.rating_beta, COALESCE(m.saved_playlist_id, 0), m.created_at`

func scanDailyMix(row interface{ Scan(...any) error }) (DailyMix, error) {
	var m DailyMix
	err := row.Scan(&m.ID, &m.Day, &m.Slot, &m.Generation, &m.Status, &m.ThemeType, &m.ThemeKey, &m.Title, &m.Description, &m.TitleSource, &m.PlanJSON,
		&m.GeneratorVersion, &m.DurationMs, &m.TrackCount, &m.Rating, &m.RatingReasons, &m.RatedAt, &m.RatingAlpha, &m.RatingBeta, &m.SavedPlaylistID, &m.CreatedAt)
	return m, err
}

// DayAfter returns the YYYY-MM-DD day n days after day.
func DayAfter(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

func insertDailyMix(ctx context.Context, tx *sql.Tx, day string, generation int, m NewDailyMix) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO daily_mixes(day, slot, generation, theme_type, theme_key, title, description, title_source, plan_json, generator_version, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, day, m.Slot, generation, m.ThemeType, m.ThemeKey, m.Title, m.Description, m.TitleSource, m.PlanJSON, m.GeneratorVersion, m.DurationMs)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, it := range m.Items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO daily_mix_items(mix_id, position, track_id, bucket, reason_json) VALUES (?, ?, ?, ?, ?)`,
			id, it.Position, it.TrackID, it.Bucket, it.ReasonJSON); err != nil {
			return 0, err
		}
	}
	cooldown := sql.NullString{}
	if m.CooldownDays > 0 {
		cooldown = sql.NullString{String: DayAfter(day, m.CooldownDays), Valid: true}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO daily_mix_theme_stats(theme_type, theme_key, uses, last_used_day, cooldown_until) VALUES (?, ?, 1, ?, ?)
		ON CONFLICT(theme_type, theme_key) DO UPDATE SET uses = uses + 1, last_used_day = excluded.last_used_day,
			cooldown_until = CASE WHEN cooldown_until IS NOT NULL AND cooldown_until > COALESCE(excluded.cooldown_until, '') THEN cooldown_until ELSE excluded.cooldown_until END,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, m.ThemeType, m.ThemeKey, day, cooldown)
	return id, err
}

// ErrDailyMixesExist is returned when the day already has active mixes.
var ErrDailyMixesExist = errors.New("daily mixes already generated for this day")

// SaveDailyMixes stores a day's mixes. Fails with ErrDailyMixesExist when the day
// already has active mixes (two generators raced).
func (s *Store) SaveDailyMixes(ctx context.Context, day string, mixes []NewDailyMix) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_mixes WHERE day = ? AND status = 'active'`, day).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrDailyMixesExist
	}
	for _, m := range mixes {
		var gen int
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation) + 1, 0) FROM daily_mixes WHERE day = ? AND slot = ?`, day, m.Slot).Scan(&gen); err != nil {
			return err
		}
		if _, err = insertDailyMix(ctx, tx, day, gen, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReplaceDailyMix marks the active mix replaced and stores its successor in the same slot.
func (s *Store) ReplaceDailyMix(ctx context.Context, oldID int64, m NewDailyMix) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var day string
	var slot, gen int
	if err = tx.QueryRowContext(ctx, `SELECT day, slot, generation FROM daily_mixes WHERE id = ? AND status = 'active'`, oldID).Scan(&day, &slot, &gen); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE daily_mixes SET status = 'replaced' WHERE id = ?`, oldID); err != nil {
		return 0, err
	}
	var maxGen int
	if err = tx.QueryRowContext(ctx, `SELECT MAX(generation) FROM daily_mixes WHERE day = ? AND slot = ?`, day, slot).Scan(&maxGen); err != nil {
		return 0, err
	}
	m.Slot = slot
	id, err := insertDailyMix(ctx, tx, day, maxGen+1, m)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DeleteDailyMixes removes every mix of a day (admin “regenerate today”).
func (s *Store) DeleteDailyMixes(ctx context.Context, day string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM daily_mixes WHERE day = ?`, day)
	return err
}

// DailyMixes returns the active mixes of a day, by slot.
func (s *Store) DailyMixes(ctx context.Context, day string) ([]DailyMix, error) {
	return s.queryDailyMixes(ctx, `SELECT `+dailyMixColumns+` FROM daily_mixes m WHERE m.day = ? AND m.status = 'active' ORDER BY m.slot`, day)
}

// DailyMixHistory returns the mixes (active and replaced) of days in [fromDay, toDay].
func (s *Store) DailyMixHistory(ctx context.Context, fromDay, toDay string) ([]DailyMix, error) {
	return s.queryDailyMixes(ctx, `SELECT `+dailyMixColumns+` FROM daily_mixes m WHERE m.day >= ? AND m.day <= ? ORDER BY m.day DESC, m.slot, m.generation`, fromDay, toDay)
}

func (s *Store) queryDailyMixes(ctx context.Context, q string, args ...any) ([]DailyMix, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DailyMix{}
	for rows.Next() {
		m, err := scanDailyMix(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DailyMixByID returns one mix (any status).
func (s *Store) DailyMixByID(ctx context.Context, id int64) (DailyMix, error) {
	return scanDailyMix(s.db.QueryRowContext(ctx, `SELECT `+dailyMixColumns+` FROM daily_mixes m WHERE m.id = ?`, id))
}

// DailyMixItems returns the tracks of a mix in order.
func (s *Store) DailyMixItems(ctx context.Context, mixID int64) ([]DailyMixItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT position, track_id, bucket, reason_json FROM daily_mix_items WHERE mix_id = ? ORDER BY position`, mixID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyMixItem
	for rows.Next() {
		var it DailyMixItem
		if err := rows.Scan(&it.Position, &it.TrackID, &it.Bucket, &it.ReasonJSON); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ReplacementsOn counts mixes replaced on a day (the daily “换一个” budget).
func (s *Store) ReplacementsOn(ctx context.Context, day string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daily_mixes WHERE day = ? AND status = 'replaced'`, day).Scan(&n)
	return n, err
}

// RecentMixTracks returns, per track, the latest day it appeared in any mix on or
// after sinceDay (excluding excludeDay, the day being generated).
func (s *Store) RecentMixTracks(ctx context.Context, sinceDay, excludeDay string) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.track_id, MAX(m.day) FROM daily_mix_items i JOIN daily_mixes m ON m.id = i.mix_id
		WHERE m.day >= ? AND m.day <> ? GROUP BY i.track_id`, sinceDay, excludeDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var day string
		if err := rows.Scan(&id, &day); err != nil {
			return nil, err
		}
		out[id] = day
	}
	return out, rows.Err()
}

// ThemeUse is a theme used on a day.
type ThemeUse struct {
	Day       string
	ThemeType string
	ThemeKey  string
	Title     string
	PlanJSON  string
}

// RecentThemes lists the themes of active and replaced mixes on or after sinceDay.
func (s *Store) RecentThemes(ctx context.Context, sinceDay string) ([]ThemeUse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT day, theme_type, theme_key, title, plan_json FROM daily_mixes WHERE day >= ? ORDER BY day DESC, slot`, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThemeUse
	for rows.Next() {
		var u ThemeUse
		if err := rows.Scan(&u.Day, &u.ThemeType, &u.ThemeKey, &u.Title, &u.PlanJSON); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ThemeStat is the bandit state of a theme.
type ThemeStat struct {
	Alpha, Beta   float64
	Uses          int
	LastUsedDay   string
	CooldownUntil string
}

// ThemeStats returns all theme stats keyed by "type:key".
func (s *Store) ThemeStats(ctx context.Context) (map[string]ThemeStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT theme_type, theme_key, alpha, beta, uses, COALESCE(last_used_day, ''), COALESCE(cooldown_until, '') FROM daily_mix_theme_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ThemeStat{}
	for rows.Next() {
		var typ, key string
		var st ThemeStat
		if err := rows.Scan(&typ, &key, &st.Alpha, &st.Beta, &st.Uses, &st.LastUsedDay, &st.CooldownUntil); err != nil {
			return nil, err
		}
		out[typ+":"+key] = st
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- feedback

// DailyMixRatingReasons are the accepted 👎 reasons.
var DailyMixRatingReasons = []string{"repetitive", "theme", "tracks", "flow", "mismatch"}

// Rating effects on the theme's Beta distribution (design §6).
const (
	ratingLikeAlpha      = 1.0
	ratingDislikeBeta    = 1.0
	ratingThemeBeta      = 2.0 // “不喜欢主题” on top of the dislike
	ratingThemeCooldown  = 45  // days
	ratingRepeatCooldown = 14  // “太重复”
	savedAlpha           = 0.5
)

// RateDailyMix sets (or clears, value 0) the rating of a mix. A previous rating's
// effect on the theme statistics is reverted first, so changing a rating is exact.
func (s *Store) RateDailyMix(ctx context.Context, id int64, value int, reasons []string, now time.Time) (DailyMix, error) {
	if value < -1 || value > 1 {
		return DailyMix{}, errors.New("rating must be -1, 0 or 1")
	}
	allowed := map[string]bool{}
	for _, r := range DailyMixRatingReasons {
		allowed[r] = true
	}
	var clean []string
	seen := map[string]bool{}
	for _, r := range reasons {
		if !allowed[r] {
			return DailyMix{}, fmt.Errorf("unknown reason %q", r)
		}
		if !seen[r] {
			seen[r] = true
			clean = append(clean, r)
		}
	}
	if value != -1 && len(clean) > 0 {
		return DailyMix{}, errors.New("reasons are only accepted with a dislike")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DailyMix{}, err
	}
	defer tx.Rollback()
	m, err := scanDailyMix(tx.QueryRowContext(ctx, `SELECT `+dailyMixColumns+` FROM daily_mixes m WHERE m.id = ?`, id))
	if err != nil {
		return DailyMix{}, err
	}
	var da, db float64
	cooldown := 0
	switch value {
	case 1:
		da = ratingLikeAlpha
	case -1:
		db = ratingDislikeBeta
		if seen["theme"] {
			db += ratingThemeBeta
			cooldown = ratingThemeCooldown
		} else if seen["repetitive"] {
			cooldown = ratingRepeatCooldown
		}
	}
	// Revert the previous effect, apply the new one (alpha/beta stay ≥ the prior 1).
	if _, err = tx.ExecContext(ctx, `INSERT INTO daily_mix_theme_stats(theme_type, theme_key) VALUES (?, ?) ON CONFLICT DO NOTHING`, m.ThemeType, m.ThemeKey); err != nil {
		return DailyMix{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE daily_mix_theme_stats SET alpha = MAX(1, alpha - ? + ?), beta = MAX(1, beta - ? + ?), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE theme_type = ? AND theme_key = ?`, m.RatingAlpha, da, m.RatingBeta, db, m.ThemeType, m.ThemeKey); err != nil {
		return DailyMix{}, err
	}
	if cooldown > 0 {
		until := DayAfter(m.Day, cooldown)
		if _, err = tx.ExecContext(ctx, `UPDATE daily_mix_theme_stats SET cooldown_until = ? WHERE theme_type = ? AND theme_key = ? AND COALESCE(cooldown_until, '') < ?`,
			until, m.ThemeType, m.ThemeKey, until); err != nil {
			return DailyMix{}, err
		}
	}
	ratedAt := sql.NullString{}
	if value != 0 {
		ratedAt = sql.NullString{String: AnalysisTimestamp(now), Valid: true}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE daily_mixes SET rating = ?, rating_reasons = ?, rated_at = ?, rating_alpha = ?, rating_beta = ? WHERE id = ?`,
		value, strings.Join(clean, ","), ratedAt, da, db, id); err != nil {
		return DailyMix{}, err
	}
	if err = tx.Commit(); err != nil {
		return DailyMix{}, err
	}
	return s.DailyMixByID(ctx, id)
}

// SaveDailyMixAsPlaylist copies the mix into a new ordinary playlist (strong positive
// feedback, counted once per mix).
func (s *Store) SaveDailyMixAsPlaylist(ctx context.Context, id int64, name, description string) (Playlist, error) {
	m, err := s.DailyMixByID(ctx, id)
	if err != nil {
		return Playlist{}, err
	}
	items, err := s.DailyMixItems(ctx, id)
	if err != nil {
		return Playlist{}, err
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.TrackID
	}
	p, _, err := s.CreatePlaylistSkippingInvalid(ctx, name, description, ids)
	if err != nil {
		return Playlist{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE daily_mixes SET saved_playlist_id = ? WHERE id = ?`, p.ID, id); err != nil {
		return p, err
	}
	if m.SavedPlaylistID == 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO daily_mix_theme_stats(theme_type, theme_key, alpha) VALUES (?, ?, ?)
			ON CONFLICT(theme_type, theme_key) DO UPDATE SET alpha = alpha + ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
			m.ThemeType, m.ThemeKey, 1+savedAlpha, savedAlpha); err != nil {
			return p, err
		}
	}
	return p, tx.Commit()
}

// SetRecommendationBlock adds or removes a track from “不再推荐”.
func (s *Store) SetRecommendationBlock(ctx context.Context, trackID int64, blocked bool) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE id = ?)`, trackID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return sql.ErrNoRows
	}
	var err error
	if blocked {
		_, err = s.db.ExecContext(ctx, `INSERT INTO recommendation_track_blocks(track_id) VALUES (?) ON CONFLICT DO NOTHING`, trackID)
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM recommendation_track_blocks WHERE track_id = ?`, trackID)
	}
	return err
}

// RecommendationBlocks reports which of the given tracks are blocked.
func (s *Store) RecommendationBlocks(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx, `SELECT track_id FROM recommendation_track_blocks WHERE track_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// CountRecommendationBlocks returns how many tracks are blocked.
func (s *Store) CountRecommendationBlocks(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recommendation_track_blocks`).Scan(&n)
	return n, err
}

// AlbumArtworkURLs returns the primary artwork URL of each album (” when none).
func (s *Store) AlbumArtworkURLs(ctx context.Context, albumIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(albumIDs) == 0 {
		return out, nil
	}
	in, args := inClause(albumIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, COALESCE((SELECT '/api/v1/artwork/' || aw.id FROM artworks aw WHERE aw.album_id = a.id
		ORDER BY (aw.source_type = 'custom') DESC, aw.is_primary DESC, aw.id LIMIT 1), '') FROM albums a WHERE a.id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			return nil, err
		}
		out[id] = url
	}
	return out, rows.Err()
}

// TrackTagsByIDs returns the tags (all sources) of several tracks.
func (s *Store) TrackTagsByIDs(ctx context.Context, ids []int64) (map[int64][]TrackTag, error) {
	out := map[int64][]TrackTag{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx, `SELECT track_id, kind, tag, weight, confidence, source FROM track_tags WHERE track_id IN (`+in+`) ORDER BY track_id, kind, weight DESC, tag`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t TrackTag
		if err := rows.Scan(&id, &t.Kind, &t.Tag, &t.Weight, &t.Confidence, &t.Source); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}
