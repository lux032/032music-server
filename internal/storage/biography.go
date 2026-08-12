package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type BiographySettings struct {
	PreferredLanguages string
	SourcePriority     string
	WikipediaEnabled   bool
	EnglishFallback    bool
	CacheDays          int
}

type ArtistBiography struct {
	Source, Language, Biography, PageURL, Status, FetchedAt string
	Selected                                                bool
}

func (s *Store) BiographySettings(ctx context.Context) (BiographySettings, error) {
	var value BiographySettings
	var wikipedia, fallback int
	err := s.db.QueryRowContext(ctx, `SELECT preferred_languages,source_priority,wikipedia_enabled,english_fallback,cache_days FROM artist_biography_settings WHERE id=1`).Scan(&value.PreferredLanguages, &value.SourcePriority, &wikipedia, &fallback, &value.CacheDays)
	value.WikipediaEnabled = wikipedia != 0
	value.EnglishFallback = fallback != 0
	return value, err
}

func (s *Store) SaveBiographySettings(ctx context.Context, value BiographySettings) error {
	value.PreferredLanguages = normalizeCSV(value.PreferredLanguages, []string{"zh", "ja", "en"})
	value.SourcePriority = normalizeSources(value.SourcePriority)
	if value.CacheDays < 1 {
		value.CacheDays = 30
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artist_biography_settings SET preferred_languages=?,source_priority=?,wikipedia_enabled=?,english_fallback=?,cache_days=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, value.PreferredLanguages, value.SourcePriority, boolInt(value.WikipediaEnabled), boolInt(value.EnglishFallback), value.CacheDays)
	return err
}

func (s *Store) ArtistBiographyFresh(ctx context.Context, artistID int64, source, language string, days int) (bool, error) {
	if days < 1 {
		days = 30
	}
	var fresh bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_biographies WHERE artist_id=? AND source=? AND language=? AND fetched_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`, artistID, source, normalizeLanguage(language), fmt.Sprintf("-%d days", days)).Scan(&fresh)
	return fresh, err
}

func (s *Store) UpsertArtistBiography(ctx context.Context, artistID int64, value ArtistBiography) error {
	value.Source = strings.ToLower(strings.TrimSpace(value.Source))
	value.Language = normalizeLanguage(value.Language)
	value.Biography = strings.TrimSpace(value.Biography)
	value.PageURL = strings.TrimSpace(value.PageURL)
	if value.Status == "" {
		value.Status = "found"
	}
	if value.Biography == "" {
		value.Status = "missing"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO artist_biographies(artist_id,source,language,biography,page_url,status) VALUES(?,?,?,?,?,?) ON CONFLICT(artist_id,source,language) DO UPDATE SET biography=excluded.biography,page_url=excluded.page_url,status=excluded.status,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, artistID, value.Source, value.Language, value.Biography, value.PageURL, value.Status)
	return err
}

func (s *Store) SetArtistBiographyPreference(ctx context.Context, artistID int64, source, language string) error {
	source = strings.ToLower(strings.TrimSpace(source))
	language = normalizeLanguage(language)
	if source == "" {
		language = ""
	} else if source != "wikipedia" && source != "lastfm" {
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
	rows, err := s.db.QueryContext(ctx, `SELECT source,language,biography,page_url,status,fetched_at FROM artist_biographies WHERE (artist_id=? OR artist_id IN (SELECT id FROM artists WHERE merged_into_artist_id=?)) AND status='found' AND biography<>'' ORDER BY fetched_at DESC`, artistID, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ArtistBiography
	for rows.Next() {
		var value ArtistBiography
		if err = rows.Scan(&value.Source, &value.Language, &value.Biography, &value.PageURL, &value.Status, &value.FetchedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func selectBiography(values []ArtistBiography, preferredSource, preferredLanguage string, setting BiographySettings) (ArtistBiography, bool) {
	if preferredSource != "" {
		for _, value := range values {
			if value.Source == preferredSource && value.Language == preferredLanguage {
				return value, true
			}
		}
	}
	languages := splitCSV(setting.PreferredLanguages)
	if setting.EnglishFallback && !contains(languages, "en") {
		languages = append(languages, "en")
	}
	sources := splitCSV(setting.SourcePriority)
	for _, language := range languages {
		for _, source := range sources {
			for _, value := range values {
				if value.Language == language && value.Source == source {
					return value, true
				}
			}
		}
	}
	return ArtistBiography{}, false
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
