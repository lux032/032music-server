package mb

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lux032/032music-server/internal/sourcegate"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const artistID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestBrowsePaginationDedup(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("limit") != "100" {
			t.Error(r.URL)
		}
		ids := []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"}
		if calls > 1 {
			ids = []string{ids[1], "33333333-3333-3333-3333-333333333333"}
		}
		groups := []ReleaseGroup{}
		for _, id := range ids {
			groups = append(groups, ReleaseGroup{ID: id})
		}
		json.NewEncoder(w).Encode(map[string]any{"release-groups": groups, "release-group-count": 4})
	}))
	defer s.Close()
	c := Client{BaseURL: s.URL, HTTP: s.Client(), Gate: sourcegate.New(sourcegate.Options{})}
	v, e := c.BrowseArtistReleaseGroups(context.Background(), artistID)
	if e != nil || len(v) != 3 || calls != 2 {
		t.Fatal(len(v), calls, e)
	}
	_, e = c.BrowseArtistReleaseGroups(context.Background(), "89ad4ac3-39f7-470e-963a-56509c546377")
	if e != nil || calls != 2 {
		t.Fatal("special artist requested")
	}
}
func TestBrowseCooldownGoneCancelAndCap(t *testing.T) {
	for _, status := range []int{429, 503, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer s.Close()
			g := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0}})
			c := Client{BaseURL: s.URL, HTTP: s.Client(), Gate: g}
			_, e := c.BrowseArtistReleaseGroups(context.Background(), artistID)
			if status == 404 {
				if !errors.Is(e, ErrGone) {
					t.Fatal(e)
				}
			} else {
				var cd *sourcegate.CooldownError
				if !errors.As(e, &cd) || cd.RetryAfter != time.Minute || g.BlockedUntil("musicbrainz").IsZero() {
					t.Fatal(e)
				}
			}
		})
	}
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"release-groups": []ReleaseGroup{{ID: artistID}}, "release-group-count": 10000})
	}))
	defer s.Close()
	c := Client{BaseURL: s.URL, HTTP: s.Client(), Gate: sourcegate.New(sourcegate.Options{})}
	_, e := c.BrowseArtistReleaseGroups(context.Background(), artistID)
	if e != nil || n.Load() != 20 {
		t.Fatal(e, n.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.BrowseArtistReleaseGroups(ctx, artistID); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestBrowseCancellationDuringRequest(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	c := Client{BaseURL: server.URL, HTTP: server.Client(), Gate: sourcegate.New(sourcegate.Options{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := c.BrowseArtistReleaseGroups(ctx, artistID); done <- e }()
	<-entered
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
