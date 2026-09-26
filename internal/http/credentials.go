package httpapi

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

// Credential sources reported by CredentialSources.
const (
	CredentialSourceEnv      = "env"
	CredentialSourceOverride = "override"
)

const (
	passwordHashScheme     = "pbkdf2-sha256"
	passwordHashIterations = 600000
	passwordSaltBytes      = 16
	passwordKeyBytes       = 32
	// passwordHashMaxIterations rejects absurd parameters read back from
	// the database so a tampered row cannot pin the CPU.
	passwordHashMaxIterations = 2_000_000
	passwordSaltMaxBytes      = 64

	maxAdminUsernameLength = 64
	minAdminPasswordLength = 12
	minCredentialTokenLen  = 24
	maxCredentialTokenLen  = 512
)

var (
	errCredentialTokensEqual = errors.New("api token and media token must differ")
	errPasswordHashFormat    = errors.New("invalid password hash format")
	// errCredentialsChanged: the credentials a sensitive operation verified
	// the current password against were replaced before it could save.
	errCredentialsChanged = errors.New("credentials changed during the operation")
	errCredentialInvalid  = errors.New("credential override value is invalid")
)

// CredentialSources reports, per credential, whether the effective value
// comes from the environment ("env") or an admin-page override
// ("override").
type CredentialSources struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	APIToken   string `json:"apiToken"`
	MediaToken string `json:"mediaToken"`
}

// credentials is an immutable snapshot of the effective login and token
// credentials. It is swapped atomically; never mutate a published value.
type credentials struct {
	username string
	// envPassword is the plaintext environment password, used only when
	// passwordHash is empty (no override).
	envPassword  string
	passwordHash string
	apiTokenHash [sha256.Size]byte
	mediaToken   string
	sources      CredentialSources
}

func (c *credentials) apiTokenMatches(token string) bool {
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], c.apiTokenHash[:]) == 1
}

func (c *credentials) mediaTokenMatches(token string) bool {
	return secureEqual(token, c.mediaToken)
}

// credentialOverrideEnv maps each override key to the environment variable
// it supersedes (used in startup warnings).
var credentialOverrideEnv = map[string]string{
	storage.CredentialAdminUsername:     "MUSIC_SERVER_ADMIN_USERNAME",
	storage.CredentialAdminPasswordHash: "MUSIC_SERVER_ADMIN_PASSWORD",
	storage.CredentialAPITokenHash:      "MUSIC_SERVER_API_TOKEN",
	storage.CredentialMediaToken:        "MUSIC_SERVER_MEDIA_TOKEN",
}

// buildCredentials layers overrides on top of the environment values.
// Overrides that fail validation (a corrupt or hand-edited row) are skipped
// so the environment value stays effective; their keys are returned in
// ignored so the caller can warn or refuse.
func buildCredentials(cfg config.Config, overrides map[string]string) (creds *credentials, ignored []string) {
	creds = &credentials{
		username:     cfg.AdminUsername,
		envPassword:  cfg.AdminPassword,
		apiTokenHash: sha256.Sum256([]byte(cfg.APIToken)),
		mediaToken:   cfg.MediaToken,
		sources: CredentialSources{
			Username:   CredentialSourceEnv,
			Password:   CredentialSourceEnv,
			APIToken:   CredentialSourceEnv,
			MediaToken: CredentialSourceEnv,
		},
	}
	for key, value := range overrides {
		switch key {
		case storage.CredentialAdminUsername:
			if username, problem := normalizeAdminUsername(value); problem != "" || username != value {
				ignored = append(ignored, key)
				continue
			}
			creds.username = value
			creds.sources.Username = CredentialSourceOverride
		case storage.CredentialAdminPasswordHash:
			if _, _, _, err := parsePasswordHash(value); err != nil {
				ignored = append(ignored, key)
				continue
			}
			creds.passwordHash = value
			creds.envPassword = ""
			creds.sources.Password = CredentialSourceOverride
		case storage.CredentialAPITokenHash:
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != sha256.Size {
				ignored = append(ignored, key)
				continue
			}
			copy(creds.apiTokenHash[:], decoded)
			creds.sources.APIToken = CredentialSourceOverride
		case storage.CredentialMediaToken:
			if tokenProblem(value) != "" {
				ignored = append(ignored, key)
				continue
			}
			creds.mediaToken = value
			creds.sources.MediaToken = CredentialSourceOverride
		default:
			ignored = append(ignored, key)
		}
	}
	sort.Strings(ignored)
	return creds, ignored
}

// credentialStore publishes the effective credentials. Readers use the
// atomic pointer without locking; writers are serialized by mu and always
// persist to the database first, publishing the new snapshot only after
// the transaction committed.
type credentialStore struct {
	current   atomic.Pointer[credentials]
	mu        sync.Mutex
	env       config.Config
	store     *storage.Store
	overrides map[string]string
}

func (s *credentialStore) load() *credentials {
	return s.current.Load()
}

// resetCredentialKeys maps MUSIC_SERVER_RESET_CREDENTIALS to override keys.
func resetCredentialKeys(mode string) []string {
	switch mode {
	case config.ResetCredentialsPassword:
		return []string{storage.CredentialAdminUsername, storage.CredentialAdminPasswordHash}
	case config.ResetCredentialsTokens:
		return []string{storage.CredentialAPITokenHash, storage.CredentialMediaToken}
	case config.ResetCredentialsAll:
		return []string{storage.CredentialAdminUsername, storage.CredentialAdminPasswordHash, storage.CredentialAPITokenHash, storage.CredentialMediaToken}
	default:
		return nil
	}
}

// newCredentialStore applies MUSIC_SERVER_RESET_CREDENTIALS, loads the
// remaining overrides and warns about every environment variable that an
// override currently supersedes.
func newCredentialStore(ctx context.Context, cfg config.Config, store *storage.Store, logger *slog.Logger) (*credentialStore, error) {
	s := &credentialStore{env: cfg, store: store, overrides: map[string]string{}}
	if store != nil {
		if keys := resetCredentialKeys(cfg.ResetCredentials); keys != nil {
			if err := store.ResetCredentialOverrides(ctx, keys); err != nil {
				return nil, err
			}
			logger.Warn("MUSIC_SERVER_RESET_CREDENTIALS is set: cleared admin-page credential overrides and all admin sessions; this repeats on every startup until the variable is removed", "mode", cfg.ResetCredentials, "items", strings.Join(keys, ","))
		}
		overrides, err := store.CredentialOverrides(ctx)
		if err != nil {
			return nil, err
		}
		s.overrides = overrides
	}
	creds, ignored := buildCredentials(cfg, s.overrides)
	for _, key := range ignored {
		logger.Warn("stored credential override is invalid and was ignored; the environment variable is used instead", "item", key, "env", credentialOverrideEnv[key])
		// Dropped from the in-memory set so later updates are not refused;
		// the row is overwritten or removed by the next change or reset.
		delete(s.overrides, key)
	}
	for _, key := range []string{storage.CredentialAdminUsername, storage.CredentialAdminPasswordHash, storage.CredentialAPITokenHash, storage.CredentialMediaToken} {
		if _, ok := s.overrides[key]; ok {
			logger.Warn("credential is overridden from the admin page; the environment variable is ignored", "item", key, "env", credentialOverrideEnv[key])
		}
	}
	s.current.Store(creds)
	return s, nil
}

// prepareLocked computes the snapshot resulting from set/remove; callers
// hold s.mu. expected, when non-nil, must still be the published snapshot:
// a sensitive operation verified the current password against it, so a
// concurrent change in between invalidates that verification.
func (s *credentialStore) prepareLocked(expected *credentials, set map[string]string, remove []string) (map[string]string, *credentials, error) {
	if expected != nil && s.current.Load() != expected {
		return nil, nil, errCredentialsChanged
	}
	next := maps.Clone(s.overrides)
	maps.Copy(next, set)
	for _, key := range remove {
		delete(next, key)
	}
	creds, ignored := buildCredentials(s.env, next)
	if len(ignored) > 0 {
		return nil, nil, errCredentialInvalid
	}
	// The shareable media token must never double as the full-access API
	// token. The DevMode environment fallback (both from env) is exempt.
	if (creds.sources.APIToken == CredentialSourceOverride || creds.sources.MediaToken == CredentialSourceOverride) && creds.apiTokenMatches(creds.mediaToken) {
		return nil, nil, errCredentialTokensEqual
	}
	return next, creds, nil
}

// update persists a token change in one transaction and then publishes the
// resulting snapshot. On any error the published credentials are
// unchanged. Admin sessions are not touched.
func (s *credentialStore) update(ctx context.Context, expected *credentials, set map[string]string, remove []string) (*credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, creds, err := s.prepareLocked(expected, set, remove)
	if err != nil {
		return nil, err
	}
	if err := s.store.UpdateCredentialOverrides(ctx, set, remove, nil); err != nil {
		return nil, err
	}
	s.overrides = next
	s.current.Store(creds)
	return creds, nil
}

// updateLogin persists a username/password change together with replacing
// every admin session by a freshly rotated current session (one database
// transaction), then publishes the new credentials and revokes the cached
// sessions (see sessionManager.replaceAll). On failure neither the
// credentials nor any session change.
func (s *credentialStore) updateLogin(ctx context.Context, sessions *sessionManager, w http.ResponseWriter, expected *credentials, set map[string]string, remove []string) (*credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, creds, err := s.prepareLocked(expected, set, remove)
	if err != nil {
		return nil, err
	}
	err = sessions.replaceAll(w, creds.username, func(keep storage.AdminSession) error {
		// Bounded: this transaction runs while both the credential and the
		// session locks are held.
		txCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return s.store.UpdateCredentialOverrides(txCtx, set, remove, &keep)
	}, func() {
		s.overrides = next
		s.current.Store(creds)
	})
	if err != nil {
		return nil, err
	}
	return creds, nil
}

// currentCredentials returns the effective credentials. Apps built without
// NewApp (unit tests) fall back to the environment configuration.
func (a *App) currentCredentials() *credentials {
	if a.credentials != nil {
		return a.credentials.load()
	}
	creds, _ := buildCredentials(a.config, nil)
	return creds
}

// CredentialSources reports where each credential currently comes from.
func (a *App) CredentialSources() CredentialSources {
	return a.currentCredentials().sources
}

// checkAdminPassword verifies password against the effective admin
// password. PBKDF2 work is bounded globally (passwordHashSlots, waiting at
// most passwordHashWait) and per client IP (one computation at a time, no
// queueing); both limits surface as errPasswordBusy.
func (a *App) checkAdminPassword(r *http.Request, creds *credentials, password string) (bool, error) {
	if creds.passwordHash == "" {
		return secureEqual(password, creds.envPassword), nil
	}
	end, ok := a.loginLimiter.beginHash(r)
	if !ok {
		return false, errPasswordBusy
	}
	defer end()
	return verifyPasswordHash(r.Context(), creds.passwordHash, password)
}

// hashPassword derives the self-describing encoding
// pbkdf2-sha256$<iterations>$<base64 salt>$<base64 hash>.
func hashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	release, err := acquirePasswordHashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, passwordKeyBytes)
	if err != nil {
		return "", err
	}
	return passwordHashScheme + "$" + strconv.Itoa(passwordHashIterations) + "$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func parsePasswordHash(encoded string) (iterations int, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordHashScheme {
		return 0, nil, nil, errPasswordHashFormat
	}
	iterations, err = strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > passwordHashMaxIterations {
		return 0, nil, nil, errPasswordHashFormat
	}
	salt, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(parts[2], "="))
	if err != nil || len(salt) < 8 || len(salt) > passwordSaltMaxBytes {
		return 0, nil, nil, errPasswordHashFormat
	}
	key, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(parts[3], "="))
	if err != nil || len(key) < 16 || len(key) > 64 {
		return 0, nil, nil, errPasswordHashFormat
	}
	return iterations, salt, key, nil
}

// verifyPasswordHash re-derives the key with the parameters stored in the
// encoding (so iterations can be tuned later) and compares in constant time.
func verifyPasswordHash(ctx context.Context, encoded, password string) (bool, error) {
	iterations, salt, want, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	release, err := acquirePasswordHashSlot(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func hashAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// normalizeAdminUsername applies the username rules and returns a security
// notice code (see securityNotices) when invalid.
func normalizeAdminUsername(raw string) (string, string) {
	username := strings.TrimSpace(raw)
	switch {
	case username == "":
		return "", "username_empty"
	case utf8.RuneCountInString(username) > maxAdminUsernameLength:
		return "", "username_too_long"
	case hasControlCharacter(username):
		return "", "username_invalid"
	}
	return username, ""
}

// validateNewAdminPassword mirrors config.Validate: at least 12 characters
// outside DevMode, non-empty in DevMode. It returns a notice code.
func validateNewAdminPassword(password string, devMode bool) string {
	if password == "" {
		return "password_empty"
	}
	if !devMode && len(password) < minAdminPasswordLength {
		return "password_too_short"
	}
	return ""
}

// validTokenByte reports whether b is URL-safe printable ASCII (RFC 3986
// unreserved: A-Z a-z 0-9 - _ . ~), so tokens can be pasted into media
// URLs and headers without escaping.
func validTokenByte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.' || b == '~'
}

// tokenProblem returns a notice code when token is not an acceptable
// credential token, or "" when it is.
func tokenProblem(token string) string {
	switch {
	case len(token) < minCredentialTokenLen:
		return "token_too_short"
	case len(token) > maxCredentialTokenLen:
		return "token_too_long"
	}
	for i := 0; i < len(token); i++ {
		if !validTokenByte(token[i]) {
			return "token_invalid"
		}
	}
	return ""
}

// chooseCredentialToken returns the trimmed custom token when one was
// submitted, or a freshly generated 32-byte token otherwise (base64url
// without padding, which only uses A-Z a-z 0-9 - _). problem is a notice
// code.
func chooseCredentialToken(custom string) (token string, generated bool, problem string) {
	custom = strings.TrimSpace(custom)
	if custom == "" {
		token, err := randomToken()
		if err != nil {
			return "", false, "save_failed"
		}
		return token, true, ""
	}
	if problem := tokenProblem(custom); problem != "" {
		return "", false, problem
	}
	return custom, false, ""
}
