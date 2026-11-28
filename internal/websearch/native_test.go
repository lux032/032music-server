package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/netfetch"
	"github.com/lux032/032music-server/internal/storage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNormalizeResponsesURLAndCanonicalize(t *testing.T) {
	cases := map[string]string{
		"https://cli.example.com/v1":                        "https://cli.example.com/v1/responses",
		"https://cli.example.com/v1/":                       "https://cli.example.com/v1/responses",
		"https://cli.example.com/v1/chat/completions":       "https://cli.example.com/v1/responses",
		"https://cli.example.com/v1/responses":              "https://cli.example.com/v1/responses",
		"https://cli.example.com/v1/responses/":             "https://cli.example.com/v1/responses",
		"https://gw.example.com/v1?api-version=2025-04-01":  "https://gw.example.com/v1/responses?api-version=2025-04-01",
		"https://gw.example.com/v1/chat/completions?foo=1":  "https://gw.example.com/v1/responses?foo=1",
		"https://gw.example.com/v1#frag":                    "",
		"https://user:pass@gw.example.com/v1":               "",
	}
	for in, want := range cases {
		if got := normalizeResponsesURL(in); got != want {
			t.Errorf("normalizeResponsesURL(%q) = %q, want %q", in, got, want)
		}
	}

	u1 := canonicalizeURL("https://www.Example.COM/artist/yoasobi/?utm_source=openai&b=2&a=1#bio")
	u2 := canonicalizeURL("http://example.com/artist/yoasobi?a=1&b=2&fbclid=xyz")
	if u1 == "" || u1 != u2 {
		t.Fatalf("canonical URL mismatch: %q vs %q", u1, u2)
	}
	// stripTrackingParams must preserve exact ordering & raw encoding of surviving signed query params.
	signed := "https://cdn.example.com/page?sig=AbC%2B123%3D&utm_source=openai&exp=9999#top"
	wantClean := "https://cdn.example.com/page?sig=AbC%2B123%3D&exp=9999"
	if clean := stripTrackingParams(signed); clean != wantClean {
		t.Fatalf("stripTrackingParams = %q, want %q", clean, wantClean)
	}
}

func TestNativeSearchSourcePoolFilteringUsageAndRateLimit(t *testing.T) {
	var seenPath, seenAuth string
	var seenInclude, seenSchema bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if inc, ok := req["include"].([]any); ok && len(inc) > 0 && inc[0] == "web_search_call.action.sources" {
			seenInclude = true
		}
		if textObj, ok := req["text"].(map[string]any); ok {
			if fmtObj, ok := textObj["format"].(map[string]any); ok && fmtObj["type"] == "json_schema" {
				seenSchema = true
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{
				// Incomplete call must be ignored.
				map[string]any{
					"type":   "web_search_call",
					"status": "failed",
					"action": map[string]any{
						"type":    "search",
						"sources": []any{"https://failed.example.com/nope"},
					},
				},
				// Reasoning block text must not corrupt JSON parsing.
				map[string]any{
					"type":    "reasoning",
					"content": []any{map[string]any{"type": "reasoning_text", "text": "thinking {not valid json"}},
				},
				// Completed call with real sources (including utm_source=openai).
				map[string]any{
					"type":   "web_search_call",
					"status": "completed",
					"action": map[string]any{
						"type": "search",
						"sources": []any{
							map[string]any{"type": "url", "url": "https://www.musicbrainz.org/artist/df6c619f/?utm_source=openai", "title": "YOASOBI — MusicBrainz"},
							"https://zh.wikipedia.org/wiki/YOASOBI?utm_medium=test",
						},
					},
				},
				// Strict JSON output with empty annotations (matches live cli.032.im behavior).
				map[string]any{
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []any{
						map[string]any{
							"type":        "output_text",
							"annotations": []any{},
							"text": `{"results":[` +
								`{"url":"https://musicbrainz.org/artist/df6c619f","title":"","content":"YOASOBI Japanese musical duo"},` +
								`{"url":"https://hallucinated.example.com/fake","title":"Fake","content":"Should be dropped"},` +
								`{"url":"https://failed.example.com/nope","title":"Failed","content":"Should also be dropped"},` +
								`{"url":"https://zh.wikipedia.org/wiki/YOASOBI?utm_source=openai","title":"YOASOBI","content":"日本双人音乐组合"}` +
								`]}`,
						},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  8900,
				"output_tokens": 220,
				"input_tokens_details": map[string]any{
					"cached_tokens": 4096,
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": 35,
				},
			},
		})
	}))
	defer srv.Close()

	var rateCalls int32
	var recordedModel, recordedPurpose string
	var recordedUsage storage.AIUsage
	reg := NewRegistry()
	reg.SetNativeEndpointsForTest(srv.URL+"/v1/chat/completions", srv.Client(), nil)
	reg.SetRateWait(func(_ context.Context, _ int) error {
		atomic.AddInt32(&rateCalls, 1)
		return nil
	})
	reg.SetUsageRecorder(func(_ context.Context, model, purpose string, usage storage.AIUsage) {
		recordedModel = model
		recordedPurpose = purpose
		recordedUsage = usage
	})

	ws := storage.WebSearchSettings{Provider: storage.WebSearchProviderNativeThenSearch}
	ai := storage.AISettings{BaseURL: srv.URL + "/v1", APIKey: "sk-native-secret", Model: "gpt-6-luna", RequestsPerMinute: 45}
	client, err := reg.Client(ws, storage.ArtistAliasSettings{}, ai)
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.Search(context.Background(), "YOASOBI")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if seenPath != "/v1/responses" || seenAuth != "Bearer sk-native-secret" || !seenInclude || !seenSchema {
		t.Fatalf("unexpected request: path=%q auth=%q include=%v schema=%v", seenPath, seenAuth, seenInclude, seenSchema)
	}
	if len(results) != 2 {
		t.Fatalf("filtered results = %+v, want 2 verified entries", results)
	}
	// Must return matched server tool URL (with tracking stripped), not unverified rewrite.
	if results[0].URL != "https://www.musicbrainz.org/artist/df6c619f/" || results[0].Title != "YOASOBI — MusicBrainz" {
		t.Fatalf("result[0] = %+v", results[0])
	}
	if results[1].URL != "https://zh.wikipedia.org/wiki/YOASOBI" {
		t.Fatalf("result[1] tracking param not stripped: %+v", results[1])
	}
	if atomic.LoadInt32(&rateCalls) != 1 {
		t.Fatalf("rateCalls = %d, want 1", rateCalls)
	}
	if recordedModel != "gpt-6-luna" || recordedPurpose != "web_search_native" ||
		recordedUsage.PromptTokens != 8900 || recordedUsage.CompletionTokens != 220 ||
		recordedUsage.CachedTokens != 4096 || recordedUsage.ReasoningTokens != 35 {
		t.Fatalf("recorded usage = (%q, %q, %+v)", recordedModel, recordedPurpose, recordedUsage)
	}
}

func TestNativeSearchTargeted400FallbackAndStrictVerification(t *testing.T) {
	t.Run("strips all three extensions sequentially and verifies via url_citation", func(t *testing.T) {
		var attempts int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if _, hasInc := req["include"]; hasInc {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"param": "include", "message": "Unknown parameter: 'include'."},
				})
				return
			}
			tools, _ := req["tools"].([]any)
			if len(tools) > 0 {
				if t0, _ := tools[0].(map[string]any); t0["search_context_size"] != nil {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{"param": "tools[0].search_context_size", "message": "Unsupported search_context_size"},
					})
					return
				}
			}
			if _, hasText := req["text"]; hasText {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"param": "text.format", "message": "json_schema unsupported"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output": []any{
					map[string]any{"type": "web_search_call", "status": "completed"},
					map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"annotations": []any{
									map[string]any{
										"type":         "url_citation",
										"url_citation": map[string]any{"url": "https://artist.example/ok", "title": "Artist OK"},
									},
								},
								"text": `{"results":[{"url":"https://artist.example/ok","title":"OK","content":"Verified after 4-step fallback"}]}`,
							},
						},
					},
				},
			})
		}))
		defer srv.Close()

		nc := &NativeClient{HTTP: srv.Client(), ResponsesURL: srv.URL + "/v1/responses", APIKey: "sk-1", Model: "gpt-6-luna"}
		res, err := nc.Search(context.Background(), "artist")
		if err != nil || len(res) != 1 || res[0].URL != "https://artist.example/ok" {
			t.Fatalf("400 fallback search = (%+v, %v)", res, err)
		}
		if attempts != 4 {
			t.Fatalf("attempts = %d, want 4", attempts)
		}
	})

	t.Run("removing include with empty source pool returns zero results instead of unverified URLs", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if _, hasInc := req["include"]; hasInc {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"param": "include", "message": "Unsupported include"}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output": []any{
					map[string]any{"type": "web_search_call", "status": "completed"},
					map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"text": `{"results":[{"url":"https://unverified.example/guess","title":"Guess","content":"Unverified"}]}`,
							},
						},
					},
				},
			})
		}))
		defer srv.Close()

		nc := &NativeClient{HTTP: srv.Client(), ResponsesURL: srv.URL + "/v1/responses", APIKey: "sk-1", Model: "gpt-6-luna"}
		res, err := nc.Search(context.Background(), "artist")
		if err != nil || len(res) != 0 {
			t.Fatalf("expected 0 results when source pool is empty after dropping include, got (%+v, %v)", res, err)
		}
	})

	t.Run("does not retry model_not_found even when message mentions text", func(t *testing.T) {
		var count int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&count, 1)
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "model_not_found", "param": "model", "message": "secret sk-123456 text.format model_not_found"},
			})
		}))
		defer srv.Close()

		nc := &NativeClient{HTTP: srv.Client(), ResponsesURL: srv.URL + "/v1/responses", APIKey: "sk-123456", Model: "bad-model"}
		_, err := nc.Search(context.Background(), "artist")
		var we *WebError
		if !errors.As(err, &we) || we.Status != http.StatusBadRequest {
			t.Fatalf("err = %v, want HTTP 400 WebError", err)
		}
		if strings.Contains(err.Error(), "sk-123456") {
			t.Fatalf("error leaked secret: %q", err.Error())
		}
		if atomic.LoadInt32(&count) != 1 {
			t.Fatalf("count = %d, want 1 (no retry on non-extension error)", count)
		}
	})

	t.Run("rejects fake 200 response with no web_search_call", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output": []any{
					map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []any{
							map[string]any{
								"type": "output_text",
								"annotations": []any{
									map[string]any{"type": "file_citation", "url": "https://memory.example/madeup"},
								},
								"text": `{"results":[{"url":"https://memory.example/madeup","title":"Memory","content":"Made up"}]}`,
							},
						},
					},
				},
			})
		}))
		defer srv.Close()

		nc := &NativeClient{HTTP: srv.Client(), ResponsesURL: srv.URL + "/v1/responses", APIKey: "sk-1", Model: "gpt-6-luna"}
		if _, err := nc.Search(context.Background(), "artist"); err == nil {
			t.Fatal("expected fake 200 without web_search_call to fail")
		}
	})

	t.Run("records usage even if output fails to decode and classifies 429 from headers", func(t *testing.T) {
		var recorded storage.AIUsage
		badOutSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"output":"not-an-array","usage":{"input_tokens":500,"output_tokens":20}}`)
		}))
		defer badOutSrv.Close()
		nc := &NativeClient{
			HTTP: badOutSrv.Client(), ResponsesURL: badOutSrv.URL + "/v1/responses", APIKey: "sk-1", Model: "gpt-6-luna",
			UsageRecorder: func(_ context.Context, _ string, _ string, u storage.AIUsage) { recorded = u },
		}
		if _, err := nc.Search(context.Background(), "artist"); err == nil {
			t.Fatal("expected invalid output error")
		}
		if recorded.PromptTokens != 500 || recorded.CompletionTokens != 20 {
			t.Fatalf("usage lost on output decode failure: %+v", recorded)
		}

		rlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, strings.Repeat("x", 5<<20))
		}))
		defer rlSrv.Close()
		nc2 := &NativeClient{HTTP: rlSrv.Client(), ResponsesURL: rlSrv.URL + "/v1/responses", APIKey: "sk-1", Model: "gpt-6-luna"}
		_, err := nc2.Search(context.Background(), "artist")
		var we *WebError
		if !errors.As(err, &we) || we.Status != http.StatusTooManyRequests || we.RetryAfter != 45*time.Second {
			t.Fatalf("429 header classification = %v, want 429 with 45s RetryAfter", err)
		}
	})
}

func TestNativeFetchRequiresExactTargetMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{
				map[string]any{
					"type":   "web_search_call",
					"status": "completed",
					"action": map[string]any{"type": "open_page", "url": "http://93.184.216.34/other-page"},
				},
				map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "output_text", "text": strings.Repeat("Wrong page content returned by model. ", 4)},
					},
				},
			},
		})
	}))
	defer srv.Close()

	nc := &NativeClient{HTTP: srv.Client(), ResponsesURL: srv.URL + "/v1/responses", APIKey: "sk-1", Model: "gpt-6-luna"}
	if _, err := nc.Fetch(context.Background(), "http://93.184.216.34/target-page"); err == nil {
		t.Fatal("expected NativeClient.Fetch to reject open_page targeting a different path on the same host")
	}
}

func TestRouterNativeThenSearchFallbackZeroResultsCooldownAndIsolation(t *testing.T) {
	var mu sync.Mutex
	nativeMode := "empty"
	var nativeHits, tavilyHits, dsHits int

	nativeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mode := nativeMode
		nativeHits++
		mu.Unlock()
		switch mode {
		case "empty":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output": []any{
					map[string]any{"type": "web_search_call", "status": "completed", "action": map[string]any{"type": "search", "sources": []any{"https://other.example/x"}}},
					map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": `{"results":[]}`}}},
				},
			})
		case "404":
			w.WriteHeader(http.StatusNotFound)
		case "ok":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output": []any{
					map[string]any{"type": "web_search_call", "status": "completed", "action": map[string]any{"type": "search", "sources": []any{"https://musicbrainz.org/artist/1"}}},
					map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": `{"results":[{"url":"https://musicbrainz.org/artist/1","title":"MB","content":"Native hit"}]}`}}},
				},
			})
		}
	}))
	defer nativeSrv.Close()

	tavilyStatus := http.StatusOK
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tavilyHits++
		st := tavilyStatus
		mu.Unlock()
		if st != http.StatusOK {
			w.WriteHeader(st)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"url": "https://tavily.example/hit", "title": "Tavily Hit", "content": "From Tavily"}},
		})
	}))
	defer tavilySrv.Close()

	dsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dsHits++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"url": "https://ds.example/hit", "title": "DS Hit", "content": "From DS"}},
		})
	}))
	defer dsSrv.Close()

	now := time.Date(2026, 11, 28, 5, 0, 0, 0, time.UTC)
	reg := NewRegistry()
	reg.SetEndpointsForTest(tavilySrv.URL, dsSrv.URL, tavilySrv.Client(), func() time.Time { return now })
	reg.SetNativeEndpointsForTest(nativeSrv.URL+"/v1", nativeSrv.Client(), nil)

	ws := storage.WebSearchSettings{
		Provider:    storage.WebSearchProviderNativeThenSearch,
		TavilyKeys:  []string{"tvly-fb-1111"},
		DeepSeekKey: "ds-fb-2222",
	}
	ai := storage.AISettings{BaseURL: nativeSrv.URL + "/v1", APIKey: "sk-live", Model: "gpt-6-luna"}

	// 1. Native returns 0 results -> automatically falls back to Tavily.
	c, err := reg.Client(ws, storage.ArtistAliasSettings{}, ai)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Search(context.Background(), "obscure artist")
	if err != nil || len(res) != 1 || res[0].Title != "Tavily Hit" {
		t.Fatalf("zero-result fallback = (%+v, %v)", res, err)
	}

	// 2. Native returns 404 (unsupported) AND Tavily returns 432 -> falls back to DeepSeek,
	//    and puts Native into 10-minute unsupported cooldown.
	mu.Lock()
	nativeMode = "404"
	tavilyStatus = 432
	mu.Unlock()
	res, err = c.Search(context.Background(), "third-tier")
	if err != nil || len(res) != 1 || res[0].Title != "DS Hit" {
		t.Fatalf("multi-tier fallback = (%+v, %v)", res, err)
	}
	beforeCooldownHits := nativeHits

	// 3. Subsequent search within 10-minute cooldown skips Native completely.
	res, err = c.Search(context.Background(), "skip-native")
	if err != nil || len(res) != 1 || res[0].Title != "DS Hit" {
		t.Fatalf("cooldown skip search = (%+v, %v)", res, err)
	}
	if nativeHits != beforeCooldownHits {
		t.Fatalf("expected native to be skipped during unsupported cooldown, hits went %d -> %d", beforeCooldownHits, nativeHits)
	}

	// 4. Stale generation cannot reapply cooldown after ResetNativeCooldown.
	staleRouter := c.(*RouterClient)
	reg.ResetNativeCooldown()
	reg.markNativeUnsupported(staleRouter.backends[0].nativeFP, staleRouter.cooldownGen)
	if reg.isNativeUnsupported(staleRouter.backends[0].nativeFP) {
		t.Fatal("stale generation reapplied unsupported cooldown after reset")
	}

	// 5. TestConnection clears cooldown and succeeds on native without polluting production cooldown.
	mu.Lock()
	nativeMode = "ok"
	mu.Unlock()
	summary, err := reg.TestConnection(context.Background(), ws, storage.ArtistAliasSettings{}, ai)
	if err != nil || !strings.Contains(summary, "模型原生搜索正常") || !strings.Contains(summary, "gpt-6-luna") {
		t.Fatalf("TestConnection after reset = (%q, %v)", summary, err)
	}
	if nativeHits <= beforeCooldownHits {
		t.Fatal("expected TestConnection to probe native after resetting cooldown")
	}
}

func TestRouterNativeLayeredFetchAndSSRFStop(t *testing.T) {
	directMode := "ok"
	directClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch directMode {
		case "blocked":
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"Content-Type": {"text/html"}},
				Body:       io.NopCloser(strings.NewReader("Access Denied")),
				Request:    r,
			}, nil
		case "ssrf_redirect":
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": {"http://169.254.169.254/latest/meta-data/"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		default:
			htmlBody := `<html><head><title>Artist Official</title></head><body><p>` +
				strings.Repeat("Official verbatim biography text for direct fetch verification. ", 4) +
				`</p></body></html>`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/html; charset=utf-8"}},
				Body:       io.NopCloser(strings.NewReader(htmlBody)),
				Request:    r,
			}, nil
		}
	})}

	var tavilyExtractHits, openPageHits int32
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tavilyExtractHits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"url": "http://93.184.216.34/artist", "raw_content": "Extracted by Tavily fallback"}},
		})
	}))
	defer tavilySrv.Close()

	nativeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&openPageHits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{
				map[string]any{
					"type":   "web_search_call",
					"status": "completed",
					"action": map[string]any{"type": "open_page", "url": "http://93.184.216.34/artist"},
				},
				map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{
							"type": "output_text",
							"text": "Opened by native open_page fallback with sufficient verified artist profile details.",
						},
					},
				},
			},
		})
	}))
	defer nativeSrv.Close()

	reg := NewRegistry()
	reg.SetEndpointsForTest(tavilySrv.URL, tavilySrv.URL, tavilySrv.Client(), nil)
	reg.SetNativeEndpointsForTest(nativeSrv.URL+"/v1", nativeSrv.Client(), directClient)

	ai := storage.AISettings{BaseURL: nativeSrv.URL + "/v1", APIKey: "sk-live", Model: "gpt-6-luna"}

	// SSRF guard: literal private IP must be rejected immediately without hitting any extractor.
	wsWithTavily := storage.WebSearchSettings{
		Provider:   storage.WebSearchProviderNativeThenSearch,
		TavilyKeys: []string{"tvly-1111"},
	}
	c, err := reg.Client(wsWithTavily, storage.ArtistAliasSettings{}, ai)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(context.Background(), "http://127.0.0.1/secret"); !errors.Is(err, netfetch.ErrBlockedDestination) {
		t.Fatalf("expected ErrBlockedDestination for 127.0.0.1, got %v", err)
	}
	// SSRF redirect must also stop the fallback chain immediately without hitting Tavily or open_page.
	directMode = "ssrf_redirect"
	if _, err := c.Fetch(context.Background(), "http://93.184.216.34/redirect"); !errors.Is(err, netfetch.ErrBlockedDestination) {
		t.Fatalf("expected ErrBlockedDestination for private redirect, got %v", err)
	}
	if atomic.LoadInt32(&tavilyExtractHits) != 0 || atomic.LoadInt32(&openPageHits) != 0 {
		t.Fatalf("blocked SSRF target leaked to external extractors: tavily=%d openPage=%d", tavilyExtractHits, openPageHits)
	}

	// Layer 1: Direct SSRF-safe public HTTP fetch succeeds with 0 Tavily / 0 open_page calls.
	directMode = "ok"
	body, err := c.Fetch(context.Background(), "http://93.184.216.34/artist")
	if err != nil || !strings.Contains(body, "Official verbatim biography") {
		t.Fatalf("Layer 1 direct fetch = (%q, %v)", body, err)
	}
	if atomic.LoadInt32(&tavilyExtractHits) != 0 || atomic.LoadInt32(&openPageHits) != 0 {
		t.Fatalf("expected zero fallback calls on direct hit, got tavily=%d openPage=%d", tavilyExtractHits, openPageHits)
	}

	// Layer 2: Direct HTTP returns 403 -> falls back to Tavily /extract first (not open_page).
	directMode = "blocked"
	body, err = c.Fetch(context.Background(), "http://93.184.216.34/artist")
	if err != nil || body != "Extracted by Tavily fallback" {
		t.Fatalf("Layer 2 tavily extract fallback = (%q, %v)", body, err)
	}
	if atomic.LoadInt32(&tavilyExtractHits) != 1 || atomic.LoadInt32(&openPageHits) != 0 {
		t.Fatalf("expected tavily=1 openPage=0, got tavily=%d openPage=%d", tavilyExtractHits, openPageHits)
	}

	// Layer 3: Direct HTTP blocked AND no Tavily/DeepSeek configured -> falls back to native open_page.
	wsNativeOnly := storage.WebSearchSettings{Provider: storage.WebSearchProviderNativeThenSearch}
	cNativeOnly, err := reg.Client(wsNativeOnly, storage.ArtistAliasSettings{}, ai)
	if err != nil {
		t.Fatal(err)
	}
	body, err = cNativeOnly.Fetch(context.Background(), "http://93.184.216.34/artist")
	if err != nil || !strings.Contains(body, "Opened by native open_page fallback") {
		t.Fatalf("Layer 3 native open_page fallback = (%q, %v)", body, err)
	}
	if atomic.LoadInt32(&openPageHits) != 1 {
		t.Fatalf("expected openPageHits=1, got %d", openPageHits)
	}
}
