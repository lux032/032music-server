// Package lastfm submits plays ("scrobbles") and now-playing updates to the
// Last.fm Scrobbling API 2.0 on behalf of one authorised user.
package lastfm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultEndpoint = "https://ws.audioscrobbler.com/2.0/"
	AuthPageURL     = "https://www.last.fm/api/auth/"
	// MaxBatch is the Last.fm limit of scrobbles per track.scrobble call.
	MaxBatch = 50
)

// Error codes documented by the Last.fm API that the service reacts to.
const (
	ErrInvalidService       = 2
	ErrAuthFailed           = 4
	ErrInvalidParameters    = 6
	ErrOperationFailed      = 8
	ErrInvalidSession       = 9
	ErrInvalidAPIKey        = 10
	ErrServiceOffline       = 11
	ErrInvalidSignature     = 13
	ErrTokenNotAuthorized   = 14
	ErrTokenExpired         = 15
	ErrTemporary            = 16
	ErrSuspendedAPIKey      = 26
	ErrRateLimitExceeded    = 29
	errorCodeUnknownFailure = 0
)

// APIError is an error response returned by Last.fm.
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Last.fm error %d: %s", e.Code, e.Message)
}

// Temporary reports whether retrying the same request later may succeed.
func (e *APIError) Temporary() bool {
	switch e.Code {
	case ErrOperationFailed, ErrServiceOffline, ErrTemporary, ErrRateLimitExceeded, errorCodeUnknownFailure:
		return true
	}
	return false
}

// AuthorizationBroken reports errors that no retry can fix until the admin
// reconnects the account or fixes the API key / secret.
func (e *APIError) AuthorizationBroken() bool {
	switch e.Code {
	case ErrAuthFailed, ErrInvalidSession, ErrInvalidAPIKey, ErrInvalidSignature, ErrSuspendedAPIKey:
		return true
	}
	return false
}

// Client calls the Last.fm API with one application key and secret.
type Client struct {
	HTTP      *http.Client
	Endpoint  string
	APIKey    string
	APISecret string
	UserAgent string
}

func NewClient(apiKey, apiSecret string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, Endpoint: DefaultEndpoint, APIKey: apiKey, APISecret: apiSecret, UserAgent: "032-Music-Server/dev (self-hosted scrobbler)"}
}

// Sign computes api_sig: md5 over the alphabetically sorted name+value pairs
// followed by the shared secret. format and callback are never signed.
func Sign(params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		if key == "format" || key == "callback" || key == "api_sig" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString(params.Get(key))
	}
	builder.WriteString(secret)
	sum := md5.Sum([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

// AuthURL is the page where the user grants this application access to the
// request token obtained from auth.getToken (the Last.fm "desktop" flow).
// No callback is involved: once the user has approved, the server exchanges
// the same token for a session, so the flow neither depends on a reachable
// callback address nor on the admin page being allowed to redirect a form
// submission to another origin.
func AuthURL(apiKey, token string) string {
	query := url.Values{"api_key": {apiKey}, "token": {token}}
	return AuthPageURL + "?" + query.Encode()
}

type apiErrorBody struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
}

func (c *Client) call(ctx context.Context, method string, params url.Values, target any) error {
	if c.APIKey == "" || c.APISecret == "" {
		return errors.New("Last.fm API key and shared secret are required")
	}
	params.Set("method", method)
	params.Set("api_key", c.APIKey)
	params.Set("api_sig", Sign(params, c.APISecret))
	params.Set("format", "json")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", c.UserAgent)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	var apiErr apiErrorBody
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != 0 {
		return &APIError{Code: apiErr.Error, Message: apiErr.Message}
	}
	if response.StatusCode >= 500 {
		return &APIError{Code: errorCodeUnknownFailure, Message: fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	if response.StatusCode != http.StatusOK {
		return &APIError{Code: ErrInvalidParameters, Message: fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode Last.fm %s response: %w", method, err)
	}
	return nil
}

// GetToken requests an unauthorised token, valid for 60 minutes, that the
// user then approves on AuthURL.
func (c *Client) GetToken(ctx context.Context) (string, error) {
	var response struct {
		Token string `json:"token"`
	}
	if err := c.call(ctx, "auth.getToken", url.Values{}, &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", errors.New("Last.fm returned no token")
	}
	return response.Token, nil
}

// GetSession exchanges an authorised token for a permanent session key.
func (c *Client) GetSession(ctx context.Context, token string) (username, sessionKey string, err error) {
	var response struct {
		Session struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"session"`
	}
	if err = c.call(ctx, "auth.getSession", url.Values{"token": {token}}, &response); err != nil {
		return "", "", err
	}
	if response.Session.Key == "" {
		return "", "", errors.New("Last.fm returned no session key")
	}
	return response.Session.Name, response.Session.Key, nil
}

// Track is one play submitted to Last.fm.
type Track struct {
	Artist          string
	Track           string
	Album           string
	AlbumArtist     string
	TrackNumber     int
	DurationSeconds int64
	// Timestamp is the UNIX time the track started playing.
	Timestamp int64
}

func (t Track) addTo(params url.Values, suffix string) {
	params.Set("artist"+suffix, t.Artist)
	params.Set("track"+suffix, t.Track)
	if t.Album != "" {
		params.Set("album"+suffix, t.Album)
	}
	if t.AlbumArtist != "" {
		params.Set("albumArtist"+suffix, t.AlbumArtist)
	}
	if t.TrackNumber > 0 {
		params.Set("trackNumber"+suffix, strconv.Itoa(t.TrackNumber))
	}
	if t.DurationSeconds > 0 {
		params.Set("duration"+suffix, strconv.FormatInt(t.DurationSeconds, 10))
	}
}

func (c *Client) UpdateNowPlaying(ctx context.Context, sessionKey string, track Track) error {
	params := url.Values{"sk": {sessionKey}}
	track.addTo(params, "")
	return c.call(ctx, "track.updateNowPlaying", params, nil)
}

// ScrobbleResult is the per-item outcome of a batch, in submission order.
// Ignored items were received but rejected by Last.fm's filters (for example
// a timestamp that is too old); resubmitting them would not help.
type ScrobbleResult struct {
	Accepted      bool
	IgnoredCode   int
	IgnoredReason string
}

// Scrobble submits up to MaxBatch plays in one request.
func (c *Client) Scrobble(ctx context.Context, sessionKey string, tracks []Track) ([]ScrobbleResult, error) {
	if len(tracks) == 0 {
		return nil, nil
	}
	if len(tracks) > MaxBatch {
		return nil, fmt.Errorf("at most %d scrobbles per request", MaxBatch)
	}
	params := url.Values{"sk": {sessionKey}}
	for i, track := range tracks {
		suffix := "[" + strconv.Itoa(i) + "]"
		track.addTo(params, suffix)
		params.Set("timestamp"+suffix, strconv.FormatInt(track.Timestamp, 10))
	}
	var raw struct {
		Scrobbles struct {
			Attr struct {
				Accepted int `json:"accepted"`
				Ignored  int `json:"ignored"`
			} `json:"@attr"`
			Scrobble json.RawMessage `json:"scrobble"`
		} `json:"scrobbles"`
	}
	if err := c.call(ctx, "track.scrobble", params, &raw); err != nil {
		return nil, err
	}
	items, err := decodeScrobbleItems(raw.Scrobbles.Scrobble)
	if err != nil {
		return nil, err
	}
	results := make([]ScrobbleResult, len(tracks))
	if len(items) != len(tracks) {
		// Without a per-item breakdown fall back to the batch counters: if
		// nothing was ignored every item was accepted.
		accepted := raw.Scrobbles.Attr.Ignored == 0
		for i := range results {
			results[i] = ScrobbleResult{Accepted: accepted, IgnoredReason: "Last.fm reported ignored scrobbles"}
			if accepted {
				results[i].IgnoredReason = ""
			}
		}
		return results, nil
	}
	for i, item := range items {
		code, _ := strconv.Atoi(strings.Trim(strings.TrimSpace(string(item.IgnoredMessage.Code)), `"`))
		results[i] = ScrobbleResult{Accepted: code == 0, IgnoredCode: code, IgnoredReason: item.IgnoredMessage.Text}
	}
	return results, nil
}

type scrobbleItem struct {
	IgnoredMessage struct {
		Code json.RawMessage `json:"code"` // "0" or 0 depending on the endpoint version
		Text string          `json:"#text"`
	} `json:"ignoredMessage"`
}

// decodeScrobbleItems handles Last.fm's JSON quirk: a single scrobble is an
// object, several are an array.
func decodeScrobbleItems(raw json.RawMessage) ([]scrobbleItem, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []scrobbleItem
		err := json.Unmarshal(raw, &items)
		return items, err
	}
	var item scrobbleItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	return []scrobbleItem{item}, nil
}
