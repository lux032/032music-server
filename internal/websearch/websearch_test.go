package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func TestTavilyRoundRobinFailoverAndRedaction(t *testing.T) {
	var mu sync.Mutex
	var seenAuth []string
	var failMode = map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seenAuth = append(seenAuth, auth)
		status := failMode[auth]
		mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("raw upstream leak " + auth))
			return
		}
		switch r.URL.Path {
		case "/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"url": "https://artist.example/page", "title": "Artist", "content": "Official artist bio"},
					{"url": "ftp://bad.example/file", "title": "Bad", "content": "ignored"},
				},
			})
		case "/extract":
			var req struct {
				URLs []string `json:"urls"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.URLs) > 0 && strings.Contains(req.URLs[0], "empty") {
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"url": req.URLs[0], "raw_content": "Extracted full artist content"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	reg := NewRegistry()
	reg.SetEndpointsForTest(srv.URL, srv.URL, srv.Client(), clock)

	ws := storage.WebSearchSettings{
		Provider:   storage.WebSearchProviderTavily,
		TavilyKeys: []string{"tvly-secret-key-1111", "tvly-secret-key-2222", "tvly-secret-key-3333"},
	}

	// 1. Healthy round-robin across 3 calls via fresh Client() lookups sharing Registry state.
	for i := 0; i < 3; i++ {
		c, err := reg.Client(ws, storage.ArtistAliasSettings{}, storage.AISettings{})
		if err != nil {
			t.Fatal(err)
		}
		res, err := c.Search(context.Background(), "query")
		if err != nil || len(res) != 1 || res[0].URL != "https://artist.example/page" {
			t.Fatalf("call %d search = (%+v,%v)", i, res, err)
		}
	}
	wantOrder := []string{"Bearer tvly-secret-key-1111", "Bearer tvly-secret-key-2222", "Bearer tvly-secret-key-3333"}
	for i, want := range wantOrder {
		if seenAuth[i] != want {
			t.Fatalf("round-robin[%d] = %q, want %q (all=%v)", i, seenAuth[i], want, seenAuth)
		}
	}

	// 2. Single-page extract empty result must NOT penalize healthy key.
	c, _ := reg.Client(ws, storage.ArtistAliasSettings{}, storage.AISettings{})
	if _, err := c.Fetch(context.Background(), "https://empty.example/page"); err == nil {
		t.Fatal("expected fetch miss error")
	}
	for _, st := range reg.TavilyStatuses(ws, storage.AISettings{}) {
		if st.State != KeyStateHealthy {
			t.Fatalf("extract miss should not penalize key: %+v", st)
		}
	}
	// Unsafe URL must be rejected before hitting server.
	if _, err := c.Fetch(context.Background(), "https://user:pass@secret.example/page"); err == nil {
		t.Fatal("expected unsafe credential URL to be rejected")
	}

	// 3. Key 1 returns 429 (cooldown), Key 2 returns 432 (quota), Key 3 returns 200 -> failover succeeds!
	mu.Lock()
	failMode["Bearer tvly-secret-key-1111"] = http.StatusTooManyRequests
	failMode["Bearer tvly-secret-key-2222"] = 432
	mu.Unlock()
	// Reset cursor by testing from Key 1 (cursor is currently at 1 after 4 calls -> try Search until both fail and Key 3 wins).
	body, err := c.Fetch(context.Background(), "https://artist.example/bio")
	if err != nil || body != "Extracted full artist content" {
		t.Fatalf("failover fetch = (%q, %v)", body, err)
	}
	// Trigger one more search to ensure both Key 1 and Key 2 have been probed and marked.
	if _, err := c.Search(context.Background(), "probe"); err != nil {
		t.Fatalf("second failover search: %v", err)
	}
	statuses := reg.TavilyStatuses(ws, storage.AISettings{})
	if statuses[0].State != KeyStateCooldown || statuses[1].State != KeyStateQuotaExhausted || statuses[2].State != KeyStateHealthy {
		t.Fatalf("unexpected statuses after failover: %+v", statuses)
	}

	// 4. Key 3 returns 401 -> marked invalid, all keys unavailable -> returns 429 because Key 1 is in cooldown (and never leaks secrets).
	mu.Lock()
	failMode["Bearer tvly-secret-key-3333"] = http.StatusUnauthorized
	mu.Unlock()
	_, err = c.Search(context.Background(), "all-down")
	var we *WebError
	if !errors.As(err, &we) || we.Status != http.StatusTooManyRequests {
		t.Fatalf("all-down with cooldown key err = %v, want HTTP 429 WebError", err)
	}
	for _, secret := range ws.TavilyKeys {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked secret: %q", err.Error())
		}
	}
	// Advance clock past 60s cooldown while Key 1 now also returns 432 -> all keys quota/invalid -> returns 432.
	mu.Lock()
	failMode["Bearer tvly-secret-key-1111"] = 432
	mu.Unlock()
	now = now.Add(2 * time.Minute)
	_, err = c.Search(context.Background(), "quota-down")
	if !errors.As(err, &we) || we.Status != 432 {
		t.Fatalf("all-quota err = %v, want HTTP 432 WebError", err)
	}
}

func TestRouterTavilyFallbackToDeepSeek(t *testing.T) {
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(432)
	}))
	defer tavilySrv.Close()

	dsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"url": "https://ds.example/artist", "title": "DS Hit", "content": "From DeepSeek"},
			},
		})
	}))
	defer dsSrv.Close()

	reg := NewRegistry()
	reg.SetEndpointsForTest(tavilySrv.URL, dsSrv.URL, tavilySrv.Client(), nil)
	ws := storage.WebSearchSettings{
		Provider:    storage.WebSearchProviderTavilyThenDeepSeek,
		TavilyKeys:  []string{"tvly-exhausted-0001"},
		DeepSeekKey: "ds-backup-0001",
	}
	c, err := reg.Client(ws, storage.ArtistAliasSettings{}, storage.AISettings{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Search(context.Background(), "artist")
	if err != nil || len(res) != 1 || res[0].Title != "DS Hit" {
		t.Fatalf("fallback search = (%+v, %v)", res, err)
	}
}
