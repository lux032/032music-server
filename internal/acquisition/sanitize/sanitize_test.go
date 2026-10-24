package sanitize

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCredentialsNeverEscape(t *testing.T) {
	raw := "http://user:secret@example.test/dl/?apikey=KEY&jackett_apikey=KEY&passkey=PASS&file=test"
	got := Text("remote "+raw+" password=secret", "KEY", "secret")
	for _, s := range []string{"KEY", "secret", "PASS", "user:"} {
		if strings.Contains(got, s) {
			t.Fatalf("leaked %s: %s", s, got)
		}
	}
	err := Error("request", &url.Error{Op: "Get", URL: raw, Err: errors.New("network unavailable")})
	if strings.Contains(err.Error(), "http") || strings.Contains(err.Error(), "KEY") {
		t.Fatal(err)
	}
}

func TestGazelleMagnetAndErrorSemantics(t *testing.T) {
	raw := "http://u:p@host/torrents.php?authkey=AUTH&torrent_pass=TORRENT&unknown=UNKNOWN#FRAGMENT magnet:?xt=hash&tr=TRACKER authkey=BARE torrent_pass=BARE2"
	text := Text(raw)
	for _, secret := range []string{"AUTH", "TORRENT", "UNKNOWN", "FRAGMENT", "TRACKER", "BARE", "xt=", "tr="} {
		if strings.Contains(text, secret) {
			t.Fatal(text)
		}
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		err := Error("request", &url.Error{URL: raw, Err: sentinel})
		if !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			t.Fatal("unsafe chain exposed")
		}
		var timeout interface{ Timeout() bool }
		if !errors.As(err, &timeout) || timeout.Timeout() != (sentinel == context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	err := Error("request", &net.DNSError{Err: "timeout", IsTimeout: true})
	var timeout interface{ Timeout() bool }
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatal(err)
	}
}

func TestTextTruncationUTF8(t *testing.T) {
	text := Text(strings.Repeat("音", 200))
	if !utf8.ValidString(text) || len(text) > 512 {
		t.Fatal("invalid UTF-8 truncation")
	}
}
