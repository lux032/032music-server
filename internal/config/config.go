package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	// ResetCredentials is MUSIC_SERVER_RESET_CREDENTIALS: "" (off),
	// "password" (admin username + password overrides), "tokens" (API and
	// media token overrides) or "all". While set, every startup deletes the
	// matching admin-page overrides and all admin sessions.
	ResetCredentials string
	// TrustedProxies is MUSIC_SERVER_TRUSTED_PROXIES: comma-separated IPs
	// or CIDRs of reverse proxies whose X-Forwarded-For is believed when
	// rate limiting logins. Empty keeps RemoteAddr as the client address.
	TrustedProxies string
	// WorkPosterBackfill is MUSIC_SERVER_WORK_POSTER_BACKFILL (default on):
	// automatically cache missing work posters ~30s after startup, after scans
	// and after each enrichment run. Tests/e2e disable it so nothing reaches
	// the network.
	WorkPosterBackfill bool
	// WatchInterval is MUSIC_SERVER_WATCH_INTERVAL (default 60s): how often
	// the library directory is polled for changes that trigger an automatic
	// incremental scan. 0 disables the watcher (startup scan still runs).
	WatchInterval time.Duration
	// Warnings 是启动时应告警但不致命的配置问题（如无法识别的开关取值）。
	Warnings []string
}

// ParseTrustedProxies parses MUSIC_SERVER_TRUSTED_PROXIES. A bare IP is
// treated as a single-address prefix.
func ParseTrustedProxies(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			prefix, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", item)
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q", item)
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

// Values accepted by MUSIC_SERVER_RESET_CREDENTIALS.
const (
	ResetCredentialsPassword = "password"
	ResetCredentialsTokens   = "tokens"
	ResetCredentialsAll      = "all"
)

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

	// L5：只有明确的关闭值才关闭；无法识别的值按开启处理并告警。
	posterBackfill, posterBackfillWarning := posterBackfillFromEnv(os.Getenv("MUSIC_SERVER_WORK_POSTER_BACKFILL"))

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
		ResetCredentials:    strings.ToLower(strings.TrimSpace(os.Getenv("MUSIC_SERVER_RESET_CREDENTIALS"))),
		TrustedProxies:      strings.TrimSpace(os.Getenv("MUSIC_SERVER_TRUSTED_PROXIES")),
	}

	cfg.WorkPosterBackfill = posterBackfill
	watchInterval, watchWarning := watchIntervalFromEnv(os.Getenv("MUSIC_SERVER_WATCH_INTERVAL"))
	cfg.WatchInterval = watchInterval
	if watchWarning != "" {
		cfg.Warnings = append(cfg.Warnings, watchWarning)
	}
	if posterBackfillWarning != "" {
		cfg.Warnings = append(cfg.Warnings, posterBackfillWarning)
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
	switch c.ResetCredentials {
	case "", ResetCredentialsPassword, ResetCredentialsTokens, ResetCredentialsAll:
	default:
		problems = append(problems, "MUSIC_SERVER_RESET_CREDENTIALS must be password, tokens, or all")
	}
	if _, err := ParseTrustedProxies(c.TrustedProxies); err != nil {
		problems = append(problems, "MUSIC_SERVER_TRUSTED_PROXIES must be comma-separated IPs or CIDRs: "+err.Error())
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

// posterBackfillFromEnv 解析 MUSIC_SERVER_WORK_POSTER_BACKFILL（L5）：空值与
// 明确的开启值开启；只有明确的关闭值（false/0/off/no，不区分大小写）才关闭；
// 其他无法识别的值按开启处理并返回一条 warning。
func posterBackfillFromEnv(value string) (enabled bool, warning string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "true", "yes", "on":
		return true, ""
	case "0", "false", "no", "off":
		return false, ""
	default:
		return true, fmt.Sprintf("unrecognized MUSIC_SERVER_WORK_POSTER_BACKFILL value %q; work poster backfill stays enabled", value)
	}
}

// Watch interval bounds for MUSIC_SERVER_WATCH_INTERVAL.
const (
	DefaultWatchInterval = 60 * time.Second
	MinWatchInterval     = 10 * time.Second
)

// watchIntervalFromEnv 解析 MUSIC_SERVER_WATCH_INTERVAL：空值取默认 60s；
// 0/false/off/no 关闭监控；纯数字按秒解释，也接受 Go 时长写法（如 5m）。
// 低于下限的值提升到下限，无法识别的值回退默认值；两者都返回 warning。
func watchIntervalFromEnv(value string) (time.Duration, string) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "":
		return DefaultWatchInterval, ""
	case "0", "false", "off", "no":
		return 0, ""
	}
	var interval time.Duration
	if seconds, err := strconv.Atoi(normalized); err == nil {
		interval = time.Duration(seconds) * time.Second
	} else if parsed, err := time.ParseDuration(normalized); err == nil {
		interval = parsed
	} else {
		return DefaultWatchInterval, fmt.Sprintf("unrecognized MUSIC_SERVER_WATCH_INTERVAL value %q; using %s", value, DefaultWatchInterval)
	}
	if interval <= 0 {
		return DefaultWatchInterval, fmt.Sprintf("MUSIC_SERVER_WATCH_INTERVAL value %q is not positive; using %s (use 0 or off to disable)", value, DefaultWatchInterval)
	}
	if interval < MinWatchInterval {
		return MinWatchInterval, fmt.Sprintf("MUSIC_SERVER_WATCH_INTERVAL value %q is below the minimum; using %s", value, MinWatchInterval)
	}
	return interval, ""
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
