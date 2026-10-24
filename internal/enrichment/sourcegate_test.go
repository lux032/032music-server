package enrichment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/discovery/mb"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
)

func TestMusicBrainz503WithoutRetryAfterAndSharedGate(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "enrichment-to-discovery", true: "discovery-to-enrichment"}[reverse], func(t *testing.T) {
			m := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) { return cannedResponse(503, http.Header{}, ""), nil }))
			g := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0, "bangumi": 0}})
			m.SetSourceGate(g)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
			defer s.Close()
			c := mb.Client{BaseURL: s.URL, HTTP: s.Client(), Gate: g}
			if reverse {
				_, e := c.BrowseArtistReleaseGroups(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
				if !errors.Is(e, sourcegate.ErrRateLimited) {
					t.Fatal(e)
				}
				if e = m.waitMBRateLimit(context.Background()); asRateLimited(e) == nil {
					t.Fatal(e)
				}
			} else {
				e := m.mbRequest(context.Background(), "http://example.com", storage.MetadataSourceSetting{}, &struct{}{})
				r := asRateLimited(e)
				if r == nil || r.RetryAfter != time.Minute {
					t.Fatal(e)
				}
				_, e = c.BrowseArtistReleaseGroups(context.Background(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
				if !errors.Is(e, sourcegate.ErrRateLimited) {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestCachedMusicBrainz503WithoutRetryAfter(t *testing.T) {
	m := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) { return cannedResponse(503, http.Header{}, ""), nil }))
	_, err := m.cachedJSONAttempt(context.Background(), "musicbrainz", "test503", "http://example.com/release-group", storage.MetadataSourceSetting{}, http.MethodGet, nil, &struct{}{})
	limited := asRateLimited(err)
	if limited == nil || limited.RetryAfter != time.Minute {
		t.Fatal(err)
	}
	if err = m.waitMBRateLimit(context.Background()); asRateLimited(err) == nil {
		t.Fatal("cached MB response failed to block shared gate", err)
	}
}

func TestImageRateLimitPreservesStatusAndLabel(t *testing.T) {
	m := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(503, http.Header{"Retry-After": {"60"}}, ""), nil
	}))
	_, _, _, err := m.downloadPublicImage(context.Background(), "http://93.184.216.34/front", "work poster")
	limited := asRateLimited(err)
	if limited == nil || limited.StatusCode != 503 || limited.Source != "work poster" || !strings.Contains(err.Error(), "work poster") {
		t.Fatal(err)
	}
}
