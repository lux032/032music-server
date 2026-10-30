package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"
)

// Admin two-factor authentication uses RFC 6238 TOTP with the parameters
// every authenticator app supports: HMAC-SHA1, 6 digits, 30 s steps.
const (
	totpIssuer     = "032 Music Server"
	totpDigits     = 6
	totpPeriod     = 30
	totpSecretSize = 20 // bytes (160 bits, RFC 4226 recommendation)
	// totpSkew accepts codes from this many steps before/after now to
	// tolerate clock drift between the server and the phone.
	totpSkew = 1
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// generateTOTPSecret returns a fresh random secret in unpadded base32.
func generateTOTPSecret() (string, error) {
	buffer := make([]byte, totpSecretSize)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return totpEncoding.EncodeToString(buffer), nil
}

// decodeTOTPSecret accepts the stored (canonical) form only: upper-case
// unpadded base32 of at least 16 bytes.
func decodeTOTPSecret(secret string) ([]byte, bool) {
	key, err := totpEncoding.DecodeString(secret)
	if err != nil || len(key) < 16 || len(key) > 64 {
		return nil, false
	}
	return key, true
}

func validTOTPSecret(secret string) bool {
	_, ok := decodeTOTPSecret(secret)
	return ok
}

// totpCodeAt computes the code for one time step (RFC 4226 HOTP).
func totpCodeAt(key []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000)
}

func totpStep(now time.Time) int64 { return now.Unix() / totpPeriod }

// normalizeTOTPCode strips the spaces and dashes people type when copying
// a code; it returns "" when the rest is not exactly six digits.
func normalizeTOTPCode(raw string) string {
	code := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\u00a0' {
			return -1
		}
		return r
	}, raw)
	if len(code) != totpDigits {
		return ""
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return ""
		}
	}
	return code
}

// matchTOTP returns the time step the code is valid for (within ±totpSkew
// of now), comparing in constant time.
func matchTOTP(secret, rawCode string, now time.Time) (int64, bool) {
	key, ok := decodeTOTPSecret(secret)
	code := normalizeTOTPCode(rawCode)
	if !ok || code == "" {
		return 0, false
	}
	current := totpStep(now)
	var matched int64
	found := 0
	for step := current - totpSkew; step <= current+totpSkew; step++ {
		if subtle.ConstantTimeCompare([]byte(totpCodeAt(key, step)), []byte(code)) == 1 {
			matched, found = step, 1
		}
	}
	return matched, found == 1
}

// totpReplayGuard rejects reusing a code: a step accepted once (and every
// earlier step) can never be accepted again for the same secret. State is
// in memory; after a restart a code could be reused only within its own
// ~90 s validity window.
type totpReplayGuard struct {
	mu     sync.Mutex
	secret string
	last   int64
}

// verify checks code against secret and atomically consumes its step.
func (g *totpReplayGuard) verify(secret, code string, now time.Time) bool {
	step, ok := matchTOTP(secret, code, now)
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.secret != secret {
		g.secret, g.last = secret, 0
	}
	if step <= g.last {
		return false
	}
	g.last = step
	return true
}

// totpURI is the otpauth:// provisioning URI understood by authenticator
// apps (Key Uri Format).
func totpURI(account, secret string) string {
	label := url.PathEscape(totpIssuer + ":" + account)
	values := url.Values{}
	values.Set("secret", secret)
	values.Set("issuer", totpIssuer)
	values.Set("algorithm", "SHA1")
	values.Set("digits", fmt.Sprint(totpDigits))
	values.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + values.Encode()
}

// totpQRDataURI renders the provisioning URI as a PNG data URI ("" on
// failure; the page then only offers the manual key).
func totpQRDataURI(uri string) string {
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		return ""
	}
	code.Scale = 6
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
}

// groupTOTPSecret splits the key into groups of four for manual entry.
func groupTOTPSecret(secret string) string {
	var b strings.Builder
	for i := 0; i < len(secret); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(secret[i:min(i+4, len(secret))])
	}
	return b.String()
}
