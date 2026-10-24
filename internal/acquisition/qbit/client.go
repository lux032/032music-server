// Package qbit implements bounded qBittorrent WebUI API v2 calls.
package qbit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/acquisition/lanhttp"
	"github.com/lux032/032music-server/internal/acquisition/sanitize"
)

var ErrAuth = errors.New("qBittorrent 认证失败，请修改连接设置后重试")

// Config contains credentials; never use it as a request DTO.
type Config struct {
	BaseURL, Username string
	Password          string `json:"-"`
}

func (Config) String() string   { return "[qB config]" }
func (Config) GoString() string { return "[qB config]" }

type Client struct {
	config               Config
	http                 *http.Client
	mu                   sync.Mutex
	loggedIn, authFailed bool
}
type AddOutcome string

const (
	AddOK       AddOutcome = "ok"
	AddRejected AddOutcome = "rejected"
	AddUnknown  AddOutcome = "unknown"
)

type Torrent struct {
	Hash         string  `json:"hash"`
	InfoHashV1   string  `json:"infohash_v1"`
	InfoHashV2   string  `json:"infohash_v2"`
	Progress     float64 `json:"progress"`
	AmountLeft   int64   `json:"amount_left"`
	Size         int64   `json:"size"`
	State        string  `json:"state"`
	Tags         string  `json:"tags"`
	Category     string  `json:"category"`
	CompletionOn int64   `json:"completion_on"`
}
type Category struct {
	Name     string `json:"name"`
	SavePath string `json:"savePath"`
}

func New(c Config) (*Client, error) {
	h, e := lanhttp.New(c.BaseURL, 30*time.Second)
	if e != nil {
		return nil, e
	}
	h.Jar, _ = cookiejar.New(nil)
	return &Client{config: c, http: h}, nil
}
func (c *Client) safe(op string, e error) error {
	return sanitize.Error("qBittorrent "+op, e, c.config.Username, c.config.Password)
}
func (c *Client) request(ctx context.Context, method, path, contentType string, b []byte) (int, []byte, error) {
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.config.BaseURL, "/")+path, bytes.NewReader(b))
	if e != nil {
		return 0, nil, c.safe("request", e)
	}
	req.Header.Set("Referer", strings.TrimRight(c.config.BaseURL, "/")+"/")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	r, e := c.http.Do(req)
	if e != nil {
		return 0, nil, c.safe("request", e)
	}
	defer r.Body.Close()
	data, e := lanhttp.Read(r.Body, 5*1024*1024)
	return r.StatusCode, data, c.safe("response", e)
}

// Login latches authentication failure. A new client after a settings change is
// required to retry, avoiding WebUI's IP-ban loop.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.login(ctx)
}
func (c *Client) login(ctx context.Context) error {
	if c.authFailed {
		return ErrAuth
	}
	if c.loggedIn {
		return nil
	}
	form := url.Values{"username": {c.config.Username}, "password": {c.config.Password}}
	status, b, e := c.request(ctx, "POST", "/api/v2/auth/login", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if e != nil {
		return e
	}
	if status == 403 || status == 200 && strings.TrimSpace(string(b)) == "Fails." {
		c.authFailed = true
		return ErrAuth
	}
	if status != 200 || strings.TrimSpace(string(b)) != "Ok." {
		return fmt.Errorf("qBittorrent login HTTP %d: unexpected response", status)
	}
	status, _, e = c.request(ctx, "GET", "/api/v2/app/version", "", nil)
	if e != nil {
		return e
	}
	if status == 403 {
		c.authFailed = true
		return ErrAuth
	}
	if status != 200 {
		return fmt.Errorf("qBittorrent version HTTP %d", status)
	}
	c.loggedIn = true
	return nil
}
func (c *Client) api(ctx context.Context, method, path, ct string, b []byte) (int, []byte, error) {
	if e := c.Login(ctx); e != nil {
		return 0, nil, e
	}
	status, data, e := c.request(ctx, method, path, ct, b)
	if e != nil || status != 403 {
		return status, data, e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loggedIn = false
	if e = c.login(ctx); e != nil {
		return 0, nil, e
	}
	status, data, e = c.request(ctx, method, path, ct, b)
	if status == 403 {
		c.authFailed = true
		c.loggedIn = false
		return status, nil, ErrAuth
	}
	return status, data, e
}

// AddTorrent reports only the add response. Callers must reconcile via Info,
// even on AddRejected or an error; a timeout is an unknown submission outcome.
func (c *Client) AddTorrent(ctx context.Context, torrent []byte, publicID, category string) (AddOutcome, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, e := w.CreateFormFile("torrents", "acquisition.torrent")
	if e != nil {
		return AddUnknown, c.safe("multipart", e)
	}
	if _, e = part.Write(torrent); e != nil {
		return AddUnknown, c.safe("multipart", e)
	}
	if e = w.WriteField("tags", Tag(publicID)); e != nil {
		return AddUnknown, c.safe("multipart", e)
	}
	if e = w.WriteField("category", category); e != nil {
		return AddUnknown, c.safe("multipart", e)
	}
	if e = w.Close(); e != nil {
		return AddUnknown, c.safe("multipart", e)
	}
	status, data, e := c.api(ctx, "POST", "/api/v2/torrents/add", w.FormDataContentType(), b.Bytes())
	if e != nil {
		return AddUnknown, e
	}
	if status == 200 && strings.TrimSpace(string(data)) == "Ok." {
		return AddOK, nil
	}
	if status >= 400 && status < 500 || status == 200 && strings.TrimSpace(string(data)) == "Fails." {
		return AddRejected, nil
	}
	return AddUnknown, nil
}
func Tag(publicID string) string { return "032acq-" + publicID }
func Complete(t Torrent) bool {
	return !strings.HasPrefix(strings.ToLower(t.State), "checking") && (t.Progress >= 1 || t.AmountLeft == 0 && t.Size > 0)
}
func (c *Client) Info(ctx context.Context, hashes, tag string) ([]Torrent, error) {
	q := url.Values{}
	if hashes != "" {
		q.Set("hashes", hashes)
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	status, b, e := c.api(ctx, "GET", "/api/v2/torrents/info?"+q.Encode(), "", nil)
	if e != nil {
		return nil, e
	}
	if status != 200 {
		return nil, fmt.Errorf("qBittorrent info HTTP %d", status)
	}
	var out []Torrent
	e = json.Unmarshal(b, &out)
	return out, c.safe("info", e)
}
func (c *Client) AddTags(ctx context.Context, hashes, tags string) error {
	f := url.Values{"hashes": {hashes}, "tags": {tags}}
	status, _, e := c.api(ctx, "POST", "/api/v2/torrents/addTags", "application/x-www-form-urlencoded", []byte(f.Encode()))
	if e != nil {
		return e
	}
	if status != 200 {
		return fmt.Errorf("qBittorrent addTags HTTP %d", status)
	}
	return nil
}
func (c *Client) Categories(ctx context.Context) (map[string]Category, error) {
	status, b, e := c.api(ctx, "GET", "/api/v2/torrents/categories", "", nil)
	if e != nil {
		return nil, e
	}
	if status != 200 {
		return nil, fmt.Errorf("qBittorrent categories HTTP %d", status)
	}
	out := map[string]Category{}
	e = json.Unmarshal(b, &out)
	return out, c.safe("categories", e)
}

// Probe checks current reachability even when authentication is cached.
func (c *Client) Probe(ctx context.Context) error {
	status, _, err := c.api(ctx, "GET", "/api/v2/app/version", "", nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("qBittorrent version HTTP %d", status)
	}
	return nil
}
