package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ListenAddress  string
	DataDirectory  string
	DatabasePath   string
	MusicDirectory string
	LibraryName    string
	AdminUsername  string
	AdminPassword  string
	APIToken       string
	CookieSecure   bool
	LogLevel       string
}

func Load() (Config, error) {
	dataDirectory := envOrDefault("MUSIC_SERVER_DATA_DIR", "/data")

	cfg := Config{
		ListenAddress:  envOrDefault("MUSIC_SERVER_ADDRESS", ":4533"),
		DataDirectory:  dataDirectory,
		DatabasePath:   envOrDefault("MUSIC_SERVER_DATABASE_PATH", filepath.Join(dataDirectory, "music.db")),
		MusicDirectory: envOrDefault("MUSIC_SERVER_MUSIC_DIR", "/music"),
		LibraryName:    envOrDefault("MUSIC_SERVER_LIBRARY_NAME", "Music"),
		AdminUsername:  envOrDefault("MUSIC_SERVER_ADMIN_USERNAME", "admin"),
		AdminPassword:  os.Getenv("MUSIC_SERVER_ADMIN_PASSWORD"),
		APIToken:       os.Getenv("MUSIC_SERVER_API_TOKEN"),
		CookieSecure:   parseBool(os.Getenv("MUSIC_SERVER_COOKIE_SECURE")),
		LogLevel:       strings.ToLower(envOrDefault("MUSIC_SERVER_LOG_LEVEL", "info")),
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
	if len(c.AdminPassword) < 12 && c.AdminPassword != "admin" {
		problems = append(problems, "MUSIC_SERVER_ADMIN_PASSWORD must contain at least 12 characters (or use admin for local development)")
	}
	if len(c.APIToken) < 24 {
		problems = append(problems, "MUSIC_SERVER_API_TOKEN must contain at least 24 characters")
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
