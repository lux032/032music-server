package config

import (
	"strings"
	"testing"
)

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
