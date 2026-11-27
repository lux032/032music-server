package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/lux032/032music-server/internal/metadata"
)

const canonicalOriginalTagNameType = "original_tag"

// ResolveCanonicalNativeName decides whether a pure-Latin local artist name
// should be normalized to a native East Asian display name from a confirmed
// MusicBrainz profile (Strategy 2A).
//
// Rules:
//  1. Only applies to MusicBrainz profiles when localName is pure Latin
//     (contains Latin letters and no non-Latin script letters).
//  2. Prefers the MusicBrainz main display_name whenever it contains native
//     East Asian script (Han, Hiragana, Katakana, or Hangul).
//  3. For JP artists whose MB main name is Latin, falls back to an active
//     primary Japanese official artist-name alias (locale ja/ja_JP/ja-JP,
//     primary=true, ended=false, excluding Legal name and Search hint).
//  4. Official Latin stage names (YOASOBI, LiSA, Aimer) and Western artists
//     (Taylor Swift) remain untouched.
func ResolveCanonicalNativeName(localName string, profile ExternalArtistProfile) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(profile.Source), "musicbrainz") {
		return "", false
	}
	trimmedLocal := strings.TrimSpace(localName)
	if !isPureLatinArtistName(trimmedLocal) {
		return "", false
	}

	mbMain := strings.TrimSpace(profile.DisplayName)
	if mbMain != "" && hasNativeEastAsianScript(mbMain) && mbMain != trimmedLocal {
		return mbMain, true
	}

	if strings.EqualFold(strings.TrimSpace(profile.Country), "JP") {
		structured := profile.StructuredAliases
		if len(structured) == 0 {
			structured = parseStructuredAliasesFromRaw(profile.Raw)
		}
		for _, alias := range structured {
			if alias.Ended || !alias.Primary || !isJapaneseLocale(alias.Locale) || isExcludedCanonicalAliasType(alias.Type) {
				continue
			}
			candidate := strings.TrimSpace(alias.Name)
			if candidate != "" && hasNativeEastAsianScript(candidate) && candidate != trimmedLocal {
				return candidate, true
			}
		}
	}
	return "", false
}

func parseStructuredAliasesFromRaw(raw json.RawMessage) []StructuredArtistAlias {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		StructuredAliases []StructuredArtistAlias `json:"structuredAliases"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	return payload.StructuredAliases
}

func isPureLatinArtistName(s string) bool {
	if s == "" {
		return false
	}
	hasLatin := false
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) {
			hasLatin = true
			continue
		}
		if unicode.IsLetter(r) {
			return false
		}
	}
	return hasLatin
}

func hasNativeEastAsianScript(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

func isJapaneseLocale(locale string) bool {
	lower := strings.ToLower(strings.TrimSpace(locale))
	return lower == "ja" || strings.HasPrefix(lower, "ja_") || strings.HasPrefix(lower, "ja-")
}

func isExcludedCanonicalAliasType(aliasType string) bool {
	switch strings.ToLower(strings.TrimSpace(aliasType)) {
	case "legal name", "search hint":
		return true
	default:
		return false
	}
}

func effectiveMusicBrainzProfileTx(ctx context.Context, q queryRower, canonicalID int64) (ExternalArtistProfile, bool, error) {
	var p ExternalArtistProfile
	var aliases, tags, raw string
	err := q.QueryRowContext(ctx, `WITH RECURSIVE src(id) AS (
		SELECT ?
		UNION
		SELECT a.id FROM artists a JOIN src s ON a.merged_into_artist_id=s.id
	)
	SELECT p.source,p.external_id,p.display_name,COALESCE(p.sort_name,''),COALESCE(p.page_url,''),COALESCE(p.remote_image_url,''),COALESCE(p.biography,''),COALESCE(p.country,''),COALESCE(p.artist_type,''),COALESCE(p.disambiguation,''),p.aliases_json,p.tags_json,p.raw_json,p.fetched_at
	FROM artist_external_profiles p
	WHERE p.source='musicbrainz' AND p.artist_id IN (SELECT id FROM src)
	ORDER BY (p.artist_id=?) DESC,p.artist_id ASC
	LIMIT 1`, canonicalID, canonicalID).
		Scan(&p.Source, &p.ExternalID, &p.DisplayName, &p.SortName, &p.PageURL, &p.RemoteImageURL, &p.Biography, &p.Country, &p.ArtistType, &p.Disambiguation, &aliases, &tags, &raw, &p.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ExternalArtistProfile{}, false, nil
	}
	if err != nil {
		return ExternalArtistProfile{}, false, err
	}
	_ = json.Unmarshal([]byte(aliases), &p.Aliases)
	_ = json.Unmarshal([]byte(tags), &p.Tags)
	p.Raw = json.RawMessage(raw)
	p.StructuredAliases = parseStructuredAliasesFromRaw(p.Raw)
	return p, true, nil
}

// applyCanonicalDisplayNameTx normalizes a canonical artist's display_name to
// its MusicBrainz native name when enabled, preserving identity_key and
// user_display_name, backing up original_display_name, and indexing the
// original Latin tag name in artist_names for search.
func applyCanonicalDisplayNameTx(ctx context.Context, tx *sql.Tx, artistID int64) (bool, error) {
	enabled, err := canonicalNativeNameEnabledTx(ctx, tx)
	if err != nil || !enabled {
		return false, err
	}
	canonicalID, err := canonicalArtistID(ctx, tx, artistID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var displayName, userDisplayName, originalDisplayName string
	err = tx.QueryRowContext(ctx, `SELECT display_name,COALESCE(user_display_name,''),COALESCE(original_display_name,'') FROM artists WHERE id=? AND merged_into_artist_id IS NULL`, canonicalID).
		Scan(&displayName, &userDisplayName, &originalDisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(userDisplayName) != "" {
		return false, nil
	}
	profile, ok, err := effectiveMusicBrainzProfileTx(ctx, tx, canonicalID)
	if err != nil || !ok {
		return false, err
	}
	canonicalName, shouldRename := ResolveCanonicalNativeName(displayName, profile)
	if !shouldRename {
		return false, nil
	}
	orig := originalDisplayName
	if orig == "" {
		orig = displayName
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artists SET original_display_name=COALESCE(original_display_name,display_name),display_name=?,sort_name=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, canonicalName, metadata.Normalize(canonicalName), canonicalID); err != nil {
		return false, err
	}
	if strings.TrimSpace(orig) != "" && orig != canonicalName {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artist_names(artist_id,value,name_type) VALUES(?,?,?)`, canonicalID, orig, canonicalOriginalTagNameType); err != nil {
			return false, err
		}
	}
	return true, nil
}

// restoreCanonicalDisplayNameTx reverts an automatically canonicalized
// display_name back to original_display_name when the artist no longer holds
// any effective MusicBrainz identity (e.g. after ResetArtistIdentity or AI revert).
func restoreCanonicalDisplayNameTx(ctx context.Context, tx *sql.Tx, artistID int64) (bool, error) {
	canonicalID, err := canonicalArtistID(ctx, tx, artistID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if _, hasMB, err := effectiveMusicBrainzProfileTx(ctx, tx, canonicalID); err != nil || hasMB {
		return false, err
	}
	var originalDisplayName string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(original_display_name,'') FROM artists WHERE id=?`, canonicalID).Scan(&originalDisplayName)
	if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(originalDisplayName) == "" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artists SET display_name=original_display_name,sort_name=?,original_display_name=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, metadata.Normalize(originalDisplayName), canonicalID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artist_names WHERE artist_id=? AND name_type=?`, canonicalID, canonicalOriginalTagNameType); err != nil {
		return false, err
	}
	return true, nil
}

// ConvergeCanonicalArtistNames silently normalizes existing confirmed
// MusicBrainz artists in the local database (zero external network requests).
func (s *Store) ConvergeCanonicalArtistNames(ctx context.Context) (int, error) {
	settings, err := s.ArtistIdentitySettings(ctx)
	if err != nil || !settings.CanonicalNativeNameEnabled {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT a.id
		FROM artists a
		WHERE a.merged_into_artist_id IS NULL
		  AND COALESCE(TRIM(a.user_display_name), '') = ''
		  AND (
		      EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=a.id AND p.source='musicbrainz')
		      OR EXISTS(
		          SELECT 1 FROM artists child
		          JOIN artist_external_profiles p ON p.artist_id=child.id AND p.source='musicbrainz'
		          WHERE child.merged_into_artist_id=a.id
		      )
		  )
		ORDER BY a.id`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}

	var updated int
	err = withBusyRetry(ctx, func() error {
		updated = 0
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, id := range ids {
			changed, err := applyCanonicalDisplayNameTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if changed {
				updated++
			}
		}
		return tx.Commit()
	})
	return updated, err
}
