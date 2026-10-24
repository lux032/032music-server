// Package sanitize keeps remote credentials out of errors and diagnostic logs.
package sanitize

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var urls = regexp.MustCompile(`(?i)(?:https?://|magnet:\?)[^\s<>"']+`)
var credentials = regexp.MustCompile(`(?i)(apikey|jackett_apikey|passkey|password|authkey|torrent_pass|token|key|sid)=([^&\s<>"']*)`)

func Text(s string, secrets ...string) string {
	s = urls.ReplaceAllStringFunc(s, func(raw string) string {
		if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
			return "[magnet]"
		}
		u, e := url.Parse(raw)
		if e != nil {
			return "[redacted URL]"
		}
		u.User = nil
		u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
		u.ForceQuery = false

		return u.String()
	})
	s = credentials.ReplaceAllString(s, "$1=[redacted]")
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
			s = strings.ReplaceAll(s, url.QueryEscape(secret), "[redacted]")
		}
	}
	if len(s) > 512 {
		s = s[:512]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
	}
	return s
}

// Error deliberately drops url.Error's URL and does not unwrap remote errors.
func Error(operation string, err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var sentinel error
	if errors.Is(err, context.Canceled) {
		sentinel = context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		sentinel = context.DeadlineExceeded
	}
	var timeout net.Error
	timedOut := errors.As(err, &timeout) && timeout.Timeout()
	return &safeError{operation + ": " + Text(err.Error(), secrets...), sentinel, timedOut, errors.As(err, &timeout)}
}

type safeError struct {
	message  string
	sentinel error
	timeout  bool
	network  bool
}

func (e *safeError) Error() string { return e.message }
func (e *safeError) Unwrap() error { return e.sentinel }
func (e *safeError) Timeout() bool { return e.timeout }

// NetworkFailure distinguishes transport errors from sanitized validation errors.
func (e *safeError) NetworkFailure() bool { return e.network }
