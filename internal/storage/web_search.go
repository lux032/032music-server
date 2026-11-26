package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	WebSearchProviderTavily             = "tavily"
	WebSearchProviderDeepSeek           = "deepseek"
	WebSearchProviderTavilyThenDeepSeek = "tavily_then_deepseek"
)

// WebSearchSettings holds global web search provider routing and credentials
// shared across artist image discovery/audit, biographies, Chinese aliases,
// and AI review web verification.
type WebSearchSettings struct {
	Provider    string   `json:"provider"`
	TavilyKeys  []string `json:"-"`
	DeepSeekKey string   `json:"-"`
	UpdatedAt   string   `json:"updatedAt,omitempty"`
}

func NormalizeWebSearchProvider(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WebSearchProviderTavily:
		return WebSearchProviderTavily
	case WebSearchProviderDeepSeek:
		return WebSearchProviderDeepSeek
	default:
		return WebSearchProviderTavilyThenDeepSeek
	}
}

// ParseAPIKeyList splits multiline, comma-, or semicolon-separated API keys,
// trims whitespace, and preserves first-seen order without duplicates.
func ParseAPIKeyList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	return DedupeAPIKeys(parts)
}

// DedupeAPIKeys trims and deduplicates API keys preserving order.
func DedupeAPIKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, exists := seen[k]; exists {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// MaskAPIKey returns a safe display mask that never exposes full credentials.
func MaskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	r := []rune(key)
	if len(r) == 0 {
		return ""
	}
	if len(r) <= 8 {
		return "****"
	}
	prefix := string(r[:4])
	if strings.HasPrefix(strings.ToLower(key), "tvly-") && len(r) > 10 {
		prefix = string(r[:5])
	}
	suffix := string(r[len(r)-4:])
	return prefix + "****" + suffix
}

// EffectiveDeepSeekKey resolves the DeepSeek web search API key from global
// web_search_settings, falling back to legacy artist_alias_settings and then
// global ai_settings when BaseURL targets official https://api.deepseek.com.
func (v WebSearchSettings) EffectiveDeepSeekKey(alias ArtistAliasSettings, ai AISettings) string {
	if k := strings.TrimSpace(v.DeepSeekKey); k != "" {
		return k
	}
	if k := strings.TrimSpace(alias.DeepSeekKey); k != "" {
		return k
	}
	u, _ := url.Parse(strings.TrimSpace(ai.BaseURL))
	if u != nil && u.Hostname() == "api.deepseek.com" && u.Scheme == "https" {
		return strings.TrimSpace(ai.APIKey)
	}
	return ""
}

// Configured reports whether at least one usable search provider credential is
// available under the selected routing policy.
func (v WebSearchSettings) Configured(alias ArtistAliasSettings, ai AISettings) bool {
	hasTavily := len(DedupeAPIKeys(v.TavilyKeys)) > 0
	hasDeepSeek := v.EffectiveDeepSeekKey(alias, ai) != ""
	switch NormalizeWebSearchProvider(v.Provider) {
	case WebSearchProviderTavily:
		return hasTavily
	case WebSearchProviderDeepSeek:
		return hasDeepSeek
	default:
		return hasTavily || hasDeepSeek
	}
}

func (s *Store) WebSearchSettings(ctx context.Context) (WebSearchSettings, error) {
	v := WebSearchSettings{Provider: WebSearchProviderTavilyThenDeepSeek}
	var rawKeys string
	err := s.db.QueryRowContext(ctx, `SELECT provider, tavily_keys_json, deepseek_key, updated_at FROM web_search_settings WHERE id=1`).
		Scan(&v.Provider, &rawKeys, &v.DeepSeekKey, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Provider = NormalizeWebSearchProvider(v.Provider)
	v.DeepSeekKey = strings.TrimSpace(v.DeepSeekKey)
	var keys []string
	if strings.TrimSpace(rawKeys) != "" {
		if uErr := json.Unmarshal([]byte(rawKeys), &keys); uErr != nil {
			return v, fmt.Errorf("parse tavily_keys_json: %w", uErr)
		}
	}
	v.TavilyKeys = DedupeAPIKeys(keys)
	return v, nil
}

func (s *Store) SaveWebSearchSettings(ctx context.Context, v WebSearchSettings) error {
	v.Provider = NormalizeWebSearchProvider(v.Provider)
	v.TavilyKeys = DedupeAPIKeys(v.TavilyKeys)
	v.DeepSeekKey = strings.TrimSpace(v.DeepSeekKey)
	if v.TavilyKeys == nil {
		v.TavilyKeys = []string{}
	}
	rawKeys, err := json.Marshal(v.TavilyKeys)
	if err != nil {
		return err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO web_search_settings(id, provider, tavily_keys_json, deepseek_key, updated_at)
		VALUES(1, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(id) DO UPDATE SET
			provider=excluded.provider,
			tavily_keys_json=excluded.tavily_keys_json,
			deepseek_key=excluded.deepseek_key,
			updated_at=excluded.updated_at`,
		v.Provider, string(rawKeys), v.DeepSeekKey); err != nil {
		return err
	}
	// Keep legacy artist_alias_settings.deepseek_key in sync for backward compatibility.
	if _, err = tx.ExecContext(ctx, `UPDATE artist_alias_settings SET deepseek_key=? WHERE id=1`, v.DeepSeekKey); err != nil {
		return err
	}
	return tx.Commit()
}
