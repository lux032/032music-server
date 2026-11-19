// Package listenbrainz submits listens, now-playing updates, metadata lookups,
// and recording love/unlove feedback to ListenBrainz (or compatible servers).
package listenbrainz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/appmeta"
	"github.com/lux032/032music-server/internal/metadata"
)

const (
	DefaultServerURL = "https://api.listenbrainz.org"
	// MaxImportBatch caps one listen_type=import call well below ListenBrainz's
	// MAX_LISTENS_PER_REQUEST (1000) and MAX_LISTEN_PAYLOAD_SIZE limits.
	MaxImportBatch = 100
)

// NormalizeServerURL validates and canonicalizes a ListenBrainz server URL.
func NormalizeServerURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return DefaultServerURL, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("ListenBrainz 服务器地址无效：%w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("ListenBrainz 服务器地址必须以 http:// 或 https:// 开头并包含主机名")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// APIError is an HTTP or API-level error returned by ListenBrainz.
type APIError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("ListenBrainz HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("ListenBrainz HTTP %d", e.StatusCode)
}

func (e *APIError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode == http.StatusRequestTimeout || e.StatusCode >= 500
}

func (e *APIError) AuthorizationBroken() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// Unsupported reports endpoints missing on lightweight ListenBrainz-compatible servers.
func (e *APIError) Unsupported() bool {
	return e.StatusCode == http.StatusNotFound || e.StatusCode == http.StatusMethodNotAllowed || e.StatusCode == http.StatusNotImplemented
}

// Client talks to one ListenBrainz-compatible API server.
type Client struct {
	HTTP      *http.Client
	ServerURL string
	UserAgent string
}

func NewClient(serverURL string) *Client {
	normalized, err := NormalizeServerURL(serverURL)
	if err != nil {
		normalized = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	}
	return &Client{
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		ServerURL: normalized,
		UserAgent: "032-Music-Server/dev (" + appmeta.RepoURL + ")",
	}
}

func (c *Client) call(ctx context.Context, method, path, token string, payload any, target any) error {
	base, err := NormalizeServerURL(c.ServerURL)
	if err != nil {
		return err
	}
	var bodyReader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bodyReader)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Token "+strings.TrimSpace(token))
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &errBody)
		msg := strings.TrimSpace(errBody.Error)
		if msg == "" {
			msg = strings.TrimSpace(errBody.Message)
		}
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    msg,
			RetryAfter: parseRetryAfter(resp.Header),
		}
	}
	if target == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode ListenBrainz %s response: %w", path, err)
	}
	return nil
}

func parseRetryAfter(h http.Header) time.Duration {
	for _, key := range []string{"X-RateLimit-Reset-In", "Retry-After"} {
		val := strings.TrimSpace(h.Get(key))
		if val == "" {
			continue
		}
		if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
			if secs > 3600 {
				secs = 3600
			}
			return time.Duration(secs) * time.Second
		}
		if at, err := http.ParseTime(val); err == nil {
			if d := time.Until(at); d > 0 {
				return min(d, time.Hour)
			}
		}
	}
	return 0
}

// ValidateToken verifies the User Token via GET /1/validate-token and returns user_name.
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", errors.New("请填写 ListenBrainz User Token")
	}
	var resp struct {
		Valid    bool   `json:"valid"`
		UserName string `json:"user_name"`
		Message  string `json:"message"`
	}
	if err := c.call(ctx, http.MethodGet, "/1/validate-token", token, nil, &resp); err != nil {
		return "", err
	}
	if !resp.Valid || strings.TrimSpace(resp.UserName) == "" {
		msg := strings.TrimSpace(resp.Message)
		if msg == "" {
			msg = "User Token 无效"
		}
		return "", &APIError{StatusCode: http.StatusUnauthorized, Message: msg}
	}
	return strings.TrimSpace(resp.UserName), nil
}

// Track is one listen or now-playing item submitted to ListenBrainz.
type Track struct {
	Artist          string
	Track           string
	Album           string
	TrackNumber     int
	DurationSeconds int64
	RecordingMBID   string
	ReleaseMBID     string
	ArtistMBIDs     []string
	Timestamp       int64
}

type listenPayloadItem struct {
	ListenedAt    int64         `json:"listened_at,omitempty"`
	TrackMetadata trackMetadata `json:"track_metadata"`
}

type trackMetadata struct {
	ArtistName     string         `json:"artist_name"`
	TrackName      string         `json:"track_name"`
	ReleaseName    string         `json:"release_name,omitempty"`
	AdditionalInfo map[string]any `json:"additional_info,omitempty"`
}

func (t Track) toMetadata() trackMetadata {
	info := map[string]any{
		"submission_client": "032-Music-Server",
	}
	if t.DurationSeconds > 0 {
		info["duration_ms"] = t.DurationSeconds * 1000
	}
	if t.TrackNumber > 0 {
		info["tracknumber"] = t.TrackNumber
	}
	if t.RecordingMBID != "" {
		info["recording_mbid"] = t.RecordingMBID
	}
	if t.ReleaseMBID != "" {
		info["release_mbid"] = t.ReleaseMBID
	}
	if len(t.ArtistMBIDs) > 0 {
		info["artist_mbids"] = t.ArtistMBIDs
	}
	return trackMetadata{
		ArtistName:     t.Artist,
		TrackName:      t.Track,
		ReleaseName:    t.Album,
		AdditionalInfo: info,
	}
}

// SubmitNowPlaying sends listen_type=playing_now with ?return_msid=true and
// returns any recording_msid assigned by MessyBrainz.
func (c *Client) SubmitNowPlaying(ctx context.Context, token string, track Track) (string, error) {
	reqBody := struct {
		ListenType string              `json:"listen_type"`
		Payload    []listenPayloadItem `json:"payload"`
	}{
		ListenType: "playing_now",
		Payload:    []listenPayloadItem{{TrackMetadata: track.toMetadata()}},
	}
	var raw map[string]any
	if err := c.call(ctx, http.MethodPost, "/1/submit-listens?return_msid=true", token, reqBody, &raw); err != nil {
		return "", err
	}
	return extractMSID(raw), nil
}

func extractMSID(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	if v, ok := raw["recording_msid"].(string); ok {
		if id, valid := metadata.NormalizeMusicBrainzID(v); valid {
			return id
		}
	}
	if arr, ok := raw["payload"].([]any); ok && len(arr) > 0 {
		if first, ok := arr[0].(map[string]any); ok {
			if v, ok := first["recording_msid"].(string); ok {
				if id, valid := metadata.NormalizeMusicBrainzID(v); valid {
					return id
				}
			}
		}
	}
	return ""
}

// SubmitListens submits 1 listen as "single" or multiple listens as "import".
func (c *Client) SubmitListens(ctx context.Context, token string, tracks []Track) error {
	if len(tracks) == 0 {
		return nil
	}
	if len(tracks) > MaxImportBatch {
		return fmt.Errorf("at most %d listens per request", MaxImportBatch)
	}
	listenType := "single"
	if len(tracks) > 1 {
		listenType = "import"
	}
	payload := make([]listenPayloadItem, len(tracks))
	for i, track := range tracks {
		payload[i] = listenPayloadItem{
			ListenedAt:    track.Timestamp,
			TrackMetadata: track.toMetadata(),
		}
	}
	reqBody := struct {
		ListenType string              `json:"listen_type"`
		Payload    []listenPayloadItem `json:"payload"`
	}{
		ListenType: listenType,
		Payload:    payload,
	}
	return c.call(ctx, http.MethodPost, "/1/submit-listens", token, reqBody, nil)
}

// LookupMetadata resolves a MusicBrainz recording_mbid via GET /1/metadata/lookup/.
func (c *Client) LookupMetadata(ctx context.Context, token, artist, track, album string) (string, error) {
	query := url.Values{
		"artist_name":    {artist},
		"recording_name": {track},
	}
	if strings.TrimSpace(album) != "" {
		query.Set("release_name", album)
	}
	var resp struct {
		RecordingMBID string `json:"recording_mbid"`
	}
	if err := c.call(ctx, http.MethodGet, "/1/metadata/lookup/?"+query.Encode(), token, nil, &resp); err != nil {
		return "", err
	}
	if id, ok := metadata.NormalizeMusicBrainzID(resp.RecordingMBID); ok {
		return id, nil
	}
	return "", nil
}

// SubmitRecordingFeedback sends score=1 (love) or score=0 (remove feedback) to
// POST /1/feedback/recording-feedback using either recording_mbid or recording_msid.
func (c *Client) SubmitRecordingFeedback(ctx context.Context, token, recordingMBID, recordingMSID string, score int) error {
	payload := map[string]any{"score": score}
	switch {
	case recordingMBID != "":
		payload["recording_mbid"] = recordingMBID
	case recordingMSID != "":
		payload["recording_msid"] = recordingMSID
	default:
		return errors.New("recording_mbid or recording_msid is required")
	}
	return c.call(ctx, http.MethodPost, "/1/feedback/recording-feedback", token, payload, nil)
}
