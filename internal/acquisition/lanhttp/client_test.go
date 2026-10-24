package lanhttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectBoundedClient(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer s.Close()
	c, e := New(s.URL, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if c.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("proxy enabled")
	}
	r, e := c.Get(s.URL)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if _, e = Read(strings.NewReader("1234"), 3); !errors.Is(e, ErrResponseTooLarge) {
		t.Fatal(e)
	}
}
func TestRedirectPolicy(t *testing.T) {
	for _, target := range []string{"magnet:?xt=urn:btih:test", "http://example.org/"} {
		t.Run(target, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, 302) }))
			defer s.Close()
			c, _ := New(s.URL, time.Second)
			r, e := c.Get(s.URL)
			if r != nil {
				r.Body.Close()
			}
			if e == nil {
				t.Fatal("followed redirect")
			}
			if strings.HasPrefix(target, "magnet:") && !errors.Is(e, ErrMagnetOnly) {
				t.Fatal(e)
			}
		})
	}
}
