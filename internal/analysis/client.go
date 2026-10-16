// Package analysis drives the acoustic analysis sidecar (analyzer/): it picks tracks that
// need analysis, calls the sidecar over the internal network, stores the results and
// calibrates the arousal/valence regression locally. See docs/daily-mix-design.md §12–13.
package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Health is the sidecar's /health payload.
type Health struct {
	Status            string `json:"status"` // loading | ready | error
	AnalysisVersion   string `json:"analysisVersion"`
	Backend           string `json:"backend"`
	FullModeAvailable bool   `json:"fullModeAvailable"`
	Threads           int    `json:"threads"`
	Error             string `json:"error"`
}

// AnalyzeRequest asks the sidecar to analyse one file (path relative to the music dir).
type AnalyzeRequest struct {
	Path       string `json:"path"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	Mode       string `json:"mode"` // fast | full
}

// AnalyzeResult is the sidecar's analysis output; vectors are base64 float16 LE.
type AnalyzeResult struct {
	AnalysisVersion string             `json:"analysisVersion"`
	Mode            string             `json:"mode"`
	DurationMs      int64              `json:"durationMs"`
	Windows         int                `json:"windows"`
	Patches         int                `json:"patches"`
	Genre           string             `json:"genre"`
	MoodTheme       string             `json:"moodtheme"`
	Instrument      string             `json:"instrument"`
	Embedding       string             `json:"embedding"`
	Heads           map[string]float64 `json:"heads"`
	Valence         *float64           `json:"valence"`
	Arousal         *float64           `json:"arousal"`
}

// FitSample is one calibration sample sent to /v1/av/fit.
type FitSample struct {
	Embedding string             `json:"embedding"`
	Heads     map[string]float64 `json:"heads"`
	Valence   float64            `json:"valence"`
	Arousal   float64            `json:"arousal"`
}

// FitResult is the fitted linear model: [valence, arousal] = bias + x·weights.
type FitResult struct {
	AnalysisVersion string          `json:"analysisVersion"`
	Kind            string          `json:"kind"`
	Features        []string        `json:"features"`
	Dims            int             `json:"dims"`
	Weights         string          `json:"weights"` // base64 float32 LE [dims][2]
	Bias            [2]float64      `json:"bias"`
	Samples         int             `json:"samples"`
	Metrics         json.RawMessage `json:"metrics"`
}

// Client is the sidecar API; an interface so the service can be tested without HTTP.
type Client interface {
	Health(ctx context.Context) (Health, error)
	Labels(ctx context.Context) (json.RawMessage, error)
	Analyze(ctx context.Context, req AnalyzeRequest) (AnalyzeResult, error)
	Fit(ctx context.Context, samples []FitSample) (FitResult, error)
}

// APIError is a non-2xx sidecar response.
type APIError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("analyzer %d %s: %s", e.Status, e.Code, e.Message)
}

// Permanent reports whether the error is a property of the file itself, so retrying
// the same file version cannot succeed.
func (e *APIError) Permanent() bool {
	switch e.Code {
	case "file_not_found", "decode_failed", "invalid_path":
		return true
	}
	return false
}

// HTTPClient talks to the sidecar over HTTP.
type HTTPClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewHTTPClient returns a client; per-request timeouts are applied by context.
// The sidecar is a LAN/compose-internal service: HTTP(S)_PROXY set for metadata
// providers must not apply (the proxy cannot resolve compose service names and
// would see the bearer token), and redirects are never followed.
func NewHTTPClient(baseURL, token string) *HTTPClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &HTTPClient{BaseURL: baseURL, Token: token, HTTP: &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, timeout time.Duration, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		apiErr := &APIError{Status: resp.StatusCode, Code: "http_error", Message: http.StatusText(resp.StatusCode)}
		var payload struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Error.Code != "" {
			apiErr.Code, apiErr.Message = payload.Error.Code, payload.Error.Message
		}
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			apiErr.RetryAfter = time.Duration(seconds) * time.Second
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode analyzer response: %w", err)
	}
	return nil
}

func (c *HTTPClient) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.do(ctx, http.MethodGet, "/health", 10*time.Second, nil, &h)
	return h, err
}

func (c *HTTPClient) Labels(ctx context.Context) (json.RawMessage, error) {
	var payload struct {
		Labels json.RawMessage `json:"labels"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/labels", 30*time.Second, nil, &payload); err != nil {
		return nil, err
	}
	if len(payload.Labels) == 0 {
		return nil, errors.New("analyzer returned no labels")
	}
	return payload.Labels, nil
}

// Analyze allows up to 10 minutes: a long file on a slow NAS in full mode can take a while.
func (c *HTTPClient) Analyze(ctx context.Context, req AnalyzeRequest) (AnalyzeResult, error) {
	var r AnalyzeResult
	err := c.do(ctx, http.MethodPost, "/v1/analyze", 10*time.Minute, req, &r)
	return r, err
}

func (c *HTTPClient) Fit(ctx context.Context, samples []FitSample) (FitResult, error) {
	var r FitResult
	err := c.do(ctx, http.MethodPost, "/v1/av/fit", 5*time.Minute, map[string]any{"samples": samples, "folds": 5}, &r)
	return r, err
}
