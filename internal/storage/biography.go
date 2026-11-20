package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type BiographySettings struct {
	PreferredLanguages   string
	SourcePriority       string
	WikipediaEnabled     bool
	EnglishFallback      bool
	CacheDays            int
	TargetLanguage       string
	AITranslateEnabled   bool
	AIWebFallbackEnabled bool
	AIModel              string
}

type ArtistBiography struct {
	Source, Language, Biography, PageURL, Status, FetchedAt string
	OriginSource, OriginLanguage, Model                     string
	Selected                                                bool
}

// ArtistBiographyAnchor carries confirmed library metadata used to anchor AI
// web-search biography summarization and prevent homonymous artist conflation.
type ArtistBiographyAnchor struct {
	Name        string
	Aliases     []string
	Country     string
	ArtistType  string
	KnownAlbums []string
	KnownTracks []string
}

func (s *Store) BiographySettings(ctx context.Context) (BiographySettings, error) {
	var value BiographySettings
	var wikipedia, fallback, aiTranslate, aiWeb int
	err := s.db.QueryRowContext(ctx, `SELECT preferred_languages,source_priority,wikipedia_enabled,english_fallback,cache_days,target_language,ai_translate_enabled,ai_web_fallback_enabled,ai_model FROM artist_biography_settings WHERE id=1`).Scan(
		&value.PreferredLanguages,
		&value.SourcePriority,
		&wikipedia,
		&fallback,
		&value.CacheDays,
		&value.TargetLanguage,
		&aiTranslate,
		&aiWeb,
		&value.AIModel,
	)
	value.WikipediaEnabled = wikipedia != 0
	value.EnglishFallback = fallback != 0
	value.AITranslateEnabled = aiTranslate != 0
	value.AIWebFallbackEnabled = aiWeb != 0
	return value, err
}

func (s *Store) SaveBiographySettings(ctx context.Context, value BiographySettings) error {
	value.PreferredLanguages = normalizeCSV(value.PreferredLanguages, []string{"zh", "ja", "en"})
	value.SourcePriority = normalizeSources(value.SourcePriority)
	value.TargetLanguage = normalizeTargetLanguage(value.TargetLanguage)
	value.AIModel = strings.TrimSpace(value.AIModel)
	if value.CacheDays < 1 {
		value.CacheDays = 30
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artist_biography_settings SET preferred_languages=?,source_priority=?,wikipedia_enabled=?,english_fallback=?,cache_days=?,target_language=?,ai_translate_enabled=?,ai_web_fallback_enabled=?,ai_model=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`,
		value.PreferredLanguages,
		value.SourcePriority,
		boolInt(value.WikipediaEnabled),
		boolInt(value.EnglishFallback),
		value.CacheDays,
		value.TargetLanguage,
		boolInt(value.AITranslateEnabled),
		boolInt(value.AIWebFallbackEnabled),
		value.AIModel,
	)
	return err
}

func (s *Store) ArtistBiographyFresh(ctx context.Context, artistID int64, source, language string, days int) (bool, error) {
	if days < 1 {
		days = 30
	}
	var fresh bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_biographies WHERE artist_id=? AND source=? AND language=? AND fetched_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`, artistID, source, normalizeBiographyLanguage(source, language), fmt.Sprintf("-%d days", days)).Scan(&fresh)
	return fresh, err
}

func (s *Store) UpsertArtistBiography(ctx context.Context, artistID int64, value ArtistBiography) error {
	value.Source = strings.ToLower(strings.TrimSpace(value.Source))
	value.Language = normalizeBiographyLanguage(value.Source, value.Language)
	value.Biography = strings.TrimSpace(value.Biography)
	value.PageURL = strings.TrimSpace(value.PageURL)
	value.OriginSource = strings.ToLower(strings.TrimSpace(value.OriginSource))
	value.OriginLanguage = normalizeLanguage(value.OriginLanguage)
	value.Model = strings.TrimSpace(value.Model)
	if value.Status == "" {
		value.Status = "found"
	}
	if value.Biography == "" {
		value.Status = "missing"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO artist_biographies(artist_id,source,language,biography,page_url,origin_source,origin_language,model,status) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(artist_id,source,language) DO UPDATE SET biography=excluded.biography,page_url=excluded.page_url,origin_source=excluded.origin_source,origin_language=excluded.origin_language,model=excluded.model,status=excluded.status,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		artistID, value.Source, value.Language, value.Biography, value.PageURL, value.OriginSource, value.OriginLanguage, value.Model, value.Status)
	return err
}

func (s *Store) SetArtistBiographyPreference(ctx context.Context, artistID int64, source, language string) error {
	source = strings.ToLower(strings.TrimSpace(source))
	language = normalizeBiographyLanguage(source, language)
	if source == "" {
		language = ""
	} else if source != "wikipedia" && source != "lastfm" && source != "ai_translated" && source != "ai_web" {
		return errors.New("unsupported biography source")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE artists SET preferred_biography_source=NULLIF(?,''),preferred_biography_language=NULLIF(?,''),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, source, language, artistID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) ArtistBiographies(ctx context.Context, artistID int64) ([]ArtistBiography, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source,language,biography,page_url,origin_source,origin_language,model,status,fetched_at FROM artist_biographies WHERE (artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)) AND status='found' AND biography<>'' ORDER BY fetched_at DESC`, artistID, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ArtistBiography
	for rows.Next() {
		var value ArtistBiography
		if err = rows.Scan(&value.Source, &value.Language, &value.Biography, &value.PageURL, &value.OriginSource, &value.OriginLanguage, &value.Model, &value.Status, &value.FetchedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ArtistBiographyAnchor loads confirmed identity context (name, aliases,
// country, artist type, representative albums and tracks) for an artist and
// any merged-in source artists.
func (s *Store) ArtistBiographyAnchor(ctx context.Context, artistID int64) (ArtistBiographyAnchor, error) {
	var out ArtistBiographyAnchor
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name),COALESCE(country,''),COALESCE(artist_type,'') FROM artists WHERE id=?`, artistID).Scan(&out.Name, &out.Country, &out.ArtistType); err != nil {
		return out, err
	}
	aliasRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT value FROM artist_names WHERE artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?) ORDER BY value LIMIT 12`, artistID, artistID)
	if err == nil {
		for aliasRows.Next() {
			var v string
			if aliasRows.Scan(&v) == nil && strings.TrimSpace(v) != "" && !strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(out.Name)) {
				out.Aliases = append(out.Aliases, strings.TrimSpace(v))
			}
		}
		aliasRows.Close()
	}
	albumRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT COALESCE(al.user_title,al.title) FROM albums al JOIN album_artists aa ON aa.album_id=al.id WHERE (aa.artist_id=? OR aa.artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)) AND `+albumVisibleSQL("al.id")+` ORDER BY COALESCE(al.user_release_year,al.release_year) DESC, COALESCE(al.user_title,al.title) ASC LIMIT 6`, artistID, artistID)
	if err == nil {
		for albumRows.Next() {
			var v string
			if albumRows.Scan(&v) == nil && strings.TrimSpace(v) != "" {
				out.KnownAlbums = append(out.KnownAlbums, strings.TrimSpace(v))
			}
		}
		albumRows.Close()
	}
	trackRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT t.title FROM tracks t JOIN track_artists ta ON ta.track_id=t.id WHERE (ta.artist_id=? OR ta.artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)) AND ta.role IN ('primary','featured') AND `+trackVisibleSQL("t.id")+` ORDER BY t.title ASC LIMIT 8`, artistID, artistID)
	if err == nil {
		for trackRows.Next() {
			var v string
			if trackRows.Scan(&v) == nil && strings.TrimSpace(v) != "" {
				out.KnownTracks = append(out.KnownTracks, strings.TrimSpace(v))
			}
		}
		trackRows.Close()
	}
	return out, nil
}

func selectBiography(values []ArtistBiography, preferredSource, preferredLanguage string, setting BiographySettings) (ArtistBiography, bool) {
	if preferredSource != "" {
		for _, value := range values {
			if value.Source == preferredSource && value.Language == preferredLanguage {
				return value, true
			}
		}
	}
	sources := splitCSV(setting.SourcePriority)
	if setting.AITranslateEnabled {
		targetLang := normalizeLanguage(setting.TargetLanguage)
		if targetLang == "" {
			if langs := splitCSV(setting.PreferredLanguages); len(langs) > 0 {
				targetLang = langs[0]
			} else {
				targetLang = "zh"
			}
		}
		for _, value := range values {
			if value.Source == "ai_translated" && value.Language == normalizeTargetLanguage(setting.TargetLanguage) {
				return value, true
			}
		}
		for _, source := range sources {
			for _, value := range values {
				if value.Source == source && value.Language == targetLang {
					return value, true
				}
			}
		}
	}
	languages := splitCSV(setting.PreferredLanguages)
	if setting.EnglishFallback && !contains(languages, "en") {
		languages = append(languages, "en")
	}
	for _, language := range languages {
		for _, source := range sources {
			for _, value := range values {
				if value.Language == language && value.Source == source {
					return value, true
				}
			}
		}
	}
	targetLang := normalizeTargetLanguage(setting.TargetLanguage)
	for _, value := range values {
		if value.Source == "ai_web" && value.Language == targetLang {
			return value, true
		}
	}
	return ArtistBiography{}, false
}

func normalizeBiographyLanguage(source, value string) string {
	if source == "ai_translated" || source == "ai_web" {
		return normalizeTargetLanguage(value)
	}
	return normalizeLanguage(value)
}

func normalizeTargetLanguage(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "zh-Hans"
	}
	switch strings.ToLower(value) {
	case "zh", "zh-cn", "zh_cn", "zh-hans", "zh_hans":
		return "zh-Hans"
	case "zh-tw", "zh_tw", "zh-hk", "zh_hk", "zh-hant", "zh_hant":
		return "zh-Hant"
	case "jp":
		return "ja"
	default:
		return strings.ReplaceAll(value, "_", "-")
	}
}

func normalizeCSV(value string, defaults []string) string {
	items := splitCSV(value)
	if len(items) == 0 {
		items = defaults
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = normalizeLanguage(item)
		if item != "" && !contains(result, item) {
			result = append(result, item)
		}
	}
	return strings.Join(result, ",")
}

func normalizeSources(value string) string {
	result := make([]string, 0, 2)
	for _, source := range splitCSV(value) {
		if (source == "wikipedia" || source == "lastfm") && !contains(result, source) {
			result = append(result, source)
		}
	}
	for _, source := range []string{"wikipedia", "lastfm"} {
		if !contains(result, source) {
			result = append(result, source)
		}
	}
	return strings.Join(result, ",")
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "jp" {
		return "ja"
	}
	if index := strings.IndexAny(value, "-_"); index > 0 {
		value = value[:index]
	}
	return value
}

func splitCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" && !contains(result, item) {
			result = append(result, item)
		}
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
