package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddress      string
	DataDirectory      string
	DatabasePath       string
	MusicDirectory     string
	LibraryName        string
	AdminUsername      string
	AdminPassword      string
	APIToken           string
	MediaToken         string
	CookieSecure       bool
	LogLevel           string
	FFmpegPath         string
	TranscodeLiveMax   int
	TranscodeCacheJobs int
	TranscodeCacheMB   int
	ThumbCacheMB       int
	DevMode            bool
	// MediaTokenGenerated is true when no MEDIA_TOKEN was configured and a
	// random one was generated for this process. Media URLs change on every
	// restart in that state; main should log a loud warning.
	MediaTokenGenerated bool
}

func Load() (Config, error) {
	dataDirectory := envOrDefault("MUSIC_SERVER_DATA_DIR", "/data")
	apiToken := os.Getenv("MUSIC_SERVER_API_TOKEN")
	devMode := parseBool(os.Getenv("MUSIC_SERVER_DEV_MODE"))

	mediaToken := strings.TrimSpace(os.Getenv("MUSIC_SERVER_MEDIA_TOKEN"))
	mediaTokenGenerated := false
	if mediaToken == "" {
		if devMode {
			// Local development convenience: fall back to the API token.
			mediaToken = apiToken
		} else {
			// Never silently equate the shareable read-only media token with
			// the full-access API token. Generate a per-boot random token so
			// a leaked media URL can never escalate to full API access.
			mediaToken = randomToken()
			mediaTokenGenerated = true
		}
	}

	cfg := Config{
		ListenAddress:       envOrDefault("MUSIC_SERVER_ADDRESS", ":4533"),
		DataDirectory:       dataDirectory,
		DatabasePath:        envOrDefault("MUSIC_SERVER_DATABASE_PATH", filepath.Join(dataDirectory, "music.db")),
		MusicDirectory:      envOrDefault("MUSIC_SERVER_MUSIC_DIR", "/music"),
		LibraryName:         envOrDefault("MUSIC_SERVER_LIBRARY_NAME", "Music"),
		AdminUsername:       envOrDefault("MUSIC_SERVER_ADMIN_USERNAME", "admin"),
		AdminPassword:       os.Getenv("MUSIC_SERVER_ADMIN_PASSWORD"),
		APIToken:            apiToken,
		MediaToken:          mediaToken,
		CookieSecure:        parseBool(os.Getenv("MUSIC_SERVER_COOKIE_SECURE")),
		FFmpegPath:          os.Getenv("MUSIC_SERVER_FFMPEG_PATH"),
		TranscodeLiveMax:    positiveEnv("MUSIC_SERVER_TRANSCODE_LIVE_MAX", 4),
		TranscodeCacheJobs:  positiveEnv("MUSIC_SERVER_TRANSCODE_CACHE_JOBS", 2),
		TranscodeCacheMB:    positiveEnv("MUSIC_SERVER_TRANSCODE_CACHE_MB", 4096),
		ThumbCacheMB:        positiveEnv("MUSIC_SERVER_THUMB_CACHE_MB", 512),
		LogLevel:            strings.ToLower(envOrDefault("MUSIC_SERVER_LOG_LEVEL", "info")),
		DevMode:             devMode,
		MediaTokenGenerated: mediaTokenGenerated,
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(cfg.DataDirectory, 0o750); err != nil {
		return Config{}, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o750); err != nil {
		return Config{}, fmt.Errorf("create database directory: %w", err)
	}

	return cfg, nil
}

func (c Config) Validate() error {
	var problems []string
	if strings.TrimSpace(c.ListenAddress) == "" {
		problems = append(problems, "MUSIC_SERVER_ADDRESS must not be empty")
	}
	if strings.TrimSpace(c.DataDirectory) == "" {
		problems = append(problems, "MUSIC_SERVER_DATA_DIR must not be empty")
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		problems = append(problems, "MUSIC_SERVER_DATABASE_PATH must not be empty")
	}
	if strings.TrimSpace(c.MusicDirectory) == "" {
		problems = append(problems, "MUSIC_SERVER_MUSIC_DIR must not be empty")
	}
	if strings.TrimSpace(c.LibraryName) == "" {
		problems = append(problems, "MUSIC_SERVER_LIBRARY_NAME must not be empty")
	}
	if strings.TrimSpace(c.AdminUsername) == "" {
		problems = append(problems, "MUSIC_SERVER_ADMIN_USERNAME must not be empty")
	}
	if len(c.AdminPassword) < 12 && !(c.DevMode && c.AdminPassword != "") {
		problems = append(problems, "MUSIC_SERVER_ADMIN_PASSWORD must contain at least 12 characters (shorter passwords require MUSIC_SERVER_DEV_MODE=1)")
	}
	if len(c.APIToken) < 24 {
		problems = append(problems, "MUSIC_SERVER_API_TOKEN must contain at least 24 characters")
	}
	if len(c.MediaToken) < 24 {
		problems = append(problems, "MUSIC_SERVER_MEDIA_TOKEN must contain at least 24 characters")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, "MUSIC_SERVER_LOG_LEVEL must be debug, info, warn, or error")
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func randomToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is a fatal environment problem; there is no
		// safe fallback for a token.
		panic(fmt.Sprintf("generate random media token: %v", err))
	}
	return hex.EncodeToString(buf)
}

func positiveEnv(name string, fallback int) int {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
