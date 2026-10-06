package config

import (
	"strings"
	"testing"
	"time"
)

func TestWatchIntervalFromEnv(t *testing.T) {
	cases := []struct {
		value   string
		want    time.Duration
		warning bool
	}{
		{"", DefaultWatchInterval, false},
		{"0", 0, false},
		{"off", 0, false},
		{"FALSE", 0, false},
		{"30", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"1s", MinWatchInterval, true},
		{"-5", DefaultWatchInterval, true},
		{"soon", DefaultWatchInterval, true},
	}
	for _, c := range cases {
		got, warning := watchIntervalFromEnv(c.value)
		if got != c.want || (warning != "") != c.warning {
			t.Fatalf("%q: got %s warning=%q, want %s warning=%v", c.value, got, warning, c.want, c.warning)
		}
	}
}

func TestConfigValidateResetCredentials(t *testing.T) {
	base := Config{ListenAddress: ":1", DataDirectory: "d", DatabasePath: "d/db", MusicDirectory: "m", LibraryName: "M", AdminUsername: "admin", AdminPassword: "initial-password-123", APIToken: "env-api-token-at-least-24-characters", MediaToken: "env-media-token-at-least-24-characters", LogLevel: "info"}
	for _, mode := range []string{"", "password", "tokens", "all"} {
		cfg := base
		cfg.ResetCredentials = mode
		if err := cfg.Validate(); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	cfg := base
	cfg.ResetCredentials = "everything"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "MUSIC_SERVER_RESET_CREDENTIALS") {
		t.Fatalf("invalid mode error = %v", err)
	}
}

func TestConfigValidateTrustedProxies(t *testing.T) {
	base := Config{ListenAddress: ":1", DataDirectory: "d", DatabasePath: "d/db", MusicDirectory: "m", LibraryName: "M", AdminUsername: "admin", AdminPassword: "initial-password-123", APIToken: "env-api-token-at-least-24-characters", MediaToken: "env-media-token-at-least-24-characters", LogLevel: "info"}
	for _, value := range []string{"", "10.0.0.0/8", "127.0.0.1, fd00::/8"} {
		cfg := base
		cfg.TrustedProxies = value
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
	}
	cfg := base
	cfg.TrustedProxies = "10.0.0.0/8, proxy.local"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "MUSIC_SERVER_TRUSTED_PROXIES") {
		t.Fatalf("invalid trusted proxies error = %v", err)
	}
}

// 批次 8.5 L5：MUSIC_SERVER_WORK_POSTER_BACKFILL 只有明确的关闭值才关闭；
// 无法识别的值按开启处理并带 warning。
func TestPosterBackfillFromEnv(t *testing.T) {
	for _, value := range []string{"", "true", "1", "ON", "yes"} {
		enabled, warning := posterBackfillFromEnv(value)
		if !enabled || warning != "" {
			t.Fatalf("%q: enabled=%v warning=%q, want on without warning", value, enabled, warning)
		}
	}
	for _, value := range []string{"false", "0", "off", "No", "OFF"} {
		enabled, warning := posterBackfillFromEnv(value)
		if enabled || warning != "" {
			t.Fatalf("%q: enabled=%v warning=%q, want off without warning", value, enabled, warning)
		}
	}
	enabled, warning := posterBackfillFromEnv("maybe")
	if !enabled || warning == "" {
		t.Fatalf("unrecognized: enabled=%v warning=%q, want on with warning", enabled, warning)
	}
}

func TestLoadPosterBackfillWarning(t *testing.T) {
	t.Setenv("MUSIC_SERVER_DATA_DIR", t.TempDir())
	t.Setenv("MUSIC_SERVER_MUSIC_DIR", t.TempDir())
	t.Setenv("MUSIC_SERVER_ADMIN_PASSWORD", "test-password-123")
	t.Setenv("MUSIC_SERVER_API_TOKEN", "test-api-token-00000000000000")
	t.Setenv("MUSIC_SERVER_MEDIA_TOKEN", "test-media-token-000000000000")
	t.Setenv("MUSIC_SERVER_WORK_POSTER_BACKFILL", "maybe")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WorkPosterBackfill || len(cfg.Warnings) != 1 {
		t.Fatalf("backfill=%v warnings=%v", cfg.WorkPosterBackfill, cfg.Warnings)
	}
}
