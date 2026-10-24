package jackett

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/sourcegate"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchRemoteErrorsAndCooldown(t *testing.T) {
	for _, tc := range []struct {
		status, code int
		blocked      bool
	}{{200, 100, false}, {401, 100, false}, {200, 900, false}, {429, 0, true}, {200, 500, true}, {401, 501, true}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("apikey") != "SECRET" || r.URL.Query().Get("t") != "search" || r.URL.Query().Get("q") != "artist title" {
					t.Error(r.URL.Path)
				}
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(tc.status)
				if tc.code != 0 {
					fmt.Fprintf(w, `<error code="%d" description="bad http://user:password@host/path?apikey=SECRET&amp;passkey=PASS&amp;authkey=AUTH&amp;torrent_pass=TORRENT magnet:?xt=hash&amp;tr=TRACKER"/>`, tc.code)
				} else {
					w.Write([]byte("rate limited"))
				}
			}))
			defer s.Close()
			gate := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"jackett": 0}})
			var logs bytes.Buffer
			c, _ := New(Config{s.URL, "SECRET", "jpopsuki"}, gate, log.New(&logs, "", 0))
			out, e := c.Search(context.Background(), "artist title")
			if e == nil || out != nil {
				t.Fatalf("%v %v", out, e)
			}
			for _, secret := range []string{"SECRET", "password", "PASS", "AUTH", "TORRENT", "TRACKER"} {
				if strings.Contains(logs.String()+e.Error(), secret) {
					t.Fatalf("leak %s: %s %s", secret, &logs, e)
				}
			}
			if tc.code != 0 {
				var remote *APIError
				if !errors.As(e, &remote) || remote.Code != tc.code {
					t.Fatalf("missing Torznab code: %v", e)
				}
			}
			if tc.blocked && time.Until(gate.BlockedUntil("jackett")) < time.Minute {
				t.Fatal("not blocked")
			}
		})
	}
}
func TestSearchEmptyAndResults(t *testing.T) {
	response := `<rss><channel></channel></rss>`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
	defer s.Close()
	c, _ := New(Config{s.URL, "KEY", "jpopsuki"}, nil, nil)
	out, e := c.Search(context.Background(), "q")
	if e != nil || out == nil || len(out) != 0 {
		t.Fatalf("%v %v", out, e)
	}
	response = `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>A FLAC 24bit Log Cue</title><guid>one</guid><link>http://wrong-host/dl/jpopsuki/?jackett_apikey=OLD&amp;path=abc</link><size>123</size><category>3000</category><pubDate>today</pubDate><torznab:attr name="seeders" value="5"/><torznab:attr name="peers" value="2"/><torznab:attr name="grabs" value="9"/><torznab:attr name="downloadvolumefactor" value="0"/></item><item><title>magnet</title><torznab:attr name="magneturl" value="magnet:?xt=urn:btih:test"/></item></channel></rss>`
	out, e = c.Search(context.Background(), "q")
	if e != nil || len(out) != 2 {
		t.Fatalf("%v %v", out, e)
	}
	r := out[0]
	if *r.Seeders != 5 || *r.Peers != 2 || *r.Grabs != 9 || *r.DownloadFactor != 0 || r.Size != 123 || r.DownloadRef != "/dl/jpopsuki/?path=abc" || !out[1].MagnetOnly {
		t.Fatalf("%+v", out)
	}
	if len(Badges(r.Title)) != 4 {
		t.Fatal(Badges(r.Title))
	}
	response = "broken"
	if _, e = c.Search(context.Background(), "q"); e == nil {
		t.Fatal("malformed feed accepted")
	}
}
func TestDownloadRewriteAndMagnet(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("jackett_apikey") != "CURRENT" || r.URL.Query().Get("path") != "abc" {
			t.Error("not rewritten")
		}
		if r.URL.Query().Get("file") == "mag" {
			http.Redirect(w, r, "magnet:?xt=urn:btih:test", 302)
			return
		}
		w.Write([]byte("torrent"))
	}))
	defer s.Close()
	c, _ := New(Config{s.URL, "CURRENT", "jpopsuki"}, nil, nil)
	ref, _, e := DownloadRef("http://wrong/dl/jpopsuki/?jackett_apikey=OLD&path=abc")
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Download(context.Background(), ref)
	if e != nil || string(b) != "torrent" {
		t.Fatalf("%s %v", b, e)
	}
	if _, e = c.Download(context.Background(), "/dl/jpopsuki/?path=abc&file=mag"); !errors.Is(e, ErrMagnetOnly) {
		t.Fatal(e)
	}
	for _, bad := range []string{"http://host/api/test", "/not-dl/", "/dl/../evil", "file:///dl/test"} {
		if _, _, e = DownloadRef(bad); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
func TestDownloadLimitAndNetworkRedaction(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", 5*1024*1024+1)) }))
	c, _ := New(Config{s.URL, "SECRET", "jpopsuki"}, nil, nil)
	if _, e := c.Download(context.Background(), "/dl/x"); e == nil {
		t.Fatal("oversize accepted")
	}
	s.Close()
	_, e := c.Search(context.Background(), "x")
	if e == nil || strings.Contains(e.Error(), "SECRET") || strings.Contains(e.Error(), s.URL) {
		t.Fatal(e)
	}
}

func TestDownloadTorznabErrorBeforeStatus(t *testing.T) {
	for _, status := range []int{200, 401, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(status)
				fmt.Fprint(w, `<error code="501" description="http://user:password@host/?jackett_apikey=SECRET&amp;passkey=PASS&amp;authkey=AUTH&amp;torrent_pass=TORRENT magnet:?xt=hash&amp;tr=TRACKER"/>`)
			}))
			defer s.Close()
			var logs bytes.Buffer
			gate := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"jackett": 0}})
			c, _ := New(Config{s.URL, "SECRET", "jpopsuki"}, gate, log.New(&logs, "", 0))
			_, err := c.Download(context.Background(), "/dl/test")
			if err == nil || !strings.Contains(err.Error(), "code 501") || time.Until(gate.BlockedUntil("jackett")) < time.Minute {
				t.Fatal(err)
			}
			for _, secret := range []string{"SECRET", "password", "PASS", "AUTH", "TORRENT", "TRACKER"} {
				if strings.Contains(logs.String()+err.Error(), secret) {
					t.Fatalf("leaked %s", secret)
				}
			}
		})
	}
}

func TestGateDownloadAndMixedResults(t *testing.T) {
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.URL.Path, "/dl/") {
			fmt.Fprint(w, "torrent")
			return
		}
		fmt.Fprint(w, `<rss><channel><item><title>missing</title></item><item><title>bad</title><link>https://other/evil?authkey=AUTH&amp;torrent_pass=PASS</link></item><item><title>good</title><link>https://other/jackett/dl/x?jackett_apikey=OLD</link></item></channel></rss>`)
	}))
	defer s.Close()
	gate := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"jackett": 0}})
	var logs bytes.Buffer
	c, _ := New(Config{s.URL + "/jackett", "SECRET", "jpopsuki"}, gate, log.New(&logs, "", 0))
	results, err := c.Search(context.Background(), "q")
	if err != nil || len(results) != 1 || results[0].DownloadRef != "/dl/x" {
		t.Fatal(results, err)
	}
	if _, err = c.Download(context.Background(), "/jackett/dl/x"); err != nil {
		t.Fatal(err)
	}
	gate.Block("jackett", time.Now().Add(time.Minute))
	before := calls
	for _, download := range []bool{false, true} {
		if download {
			_, err = c.Download(context.Background(), "/dl/x")
		} else {
			_, err = c.Search(context.Background(), "q")
		}
		var cooldown *sourcegate.CooldownError
		if !errors.As(err, &cooldown) || !errors.Is(err, sourcegate.ErrRateLimited) {
			t.Fatal(err)
		}
	}
	if calls != before {
		t.Fatal("cooldown ignored")
	}
	if strings.Contains(logs.String(), "AUTH") || strings.Contains(logs.String(), "PASS") {
		t.Fatal(logs.String())
	}
}

func TestConfigFormatting(t *testing.T) {
	c := Config{BaseURL: "http://host", APIKey: "SECRET", Indexer: "jpopsuki"}
	b, _ := json.Marshal(c)
	output := string(b) + fmt.Sprintf("%v %#v", c, c)
	for _, secret := range []string{"SECRET", "PASSWORD"} {
		if strings.Contains(output, secret) {
			t.Fatal(output)
		}
	}
}

func TestSearchPlainHTTPError(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(status)
				fmt.Fprint(w, "not XML SECRET")
			}))
			defer server.Close()
			client, err := New(Config{server.URL, "SECRET", "jpopsuki"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Search(context.Background(), "album")
			var remote *HTTPError
			if !errors.As(err, &remote) || remote.Status != status || remote.RetryAfter != 120*time.Second {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("remote body disclosed")
			}
		})
	}
}
