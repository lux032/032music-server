// Package jackett implements the small Torznab surface needed for acquisition.
package jackett

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/acquisition/catalog"
	"github.com/lux032/032music-server/internal/acquisition/lanhttp"
	"github.com/lux032/032music-server/internal/acquisition/sanitize"
	"github.com/lux032/032music-server/internal/acquisition/torrent"
	"github.com/lux032/032music-server/internal/sourcegate"
)

// HTTPError contains no response text or URL.
type HTTPError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("Jackett HTTP %d", e.Status) }

var ErrMagnetOnly = lanhttp.ErrMagnetOnly

// APIError carries only a safe Torznab code, never its remote description.
type APIError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Jackett: %s (code %d)", errorMessage(e.Code), e.Code)
}

type Gate interface {
	Wait(context.Context, string) error
	Block(string, time.Time)
}

// Config contains credentials; never use it as a request DTO.
type Config struct {
	BaseURL string
	APIKey  string `json:"-"`
	Indexer string
}

func (Config) String() string   { return "[Jackett config]" }
func (Config) GoString() string { return "[Jackett config]" }

type Client struct {
	config Config
	http   *http.Client
	gate   Gate
	logger *log.Logger
}
type Result = catalog.Result

func DownloadRef(raw string, basePath ...string) (string, bool, error) {
	return catalog.DownloadRef(raw, basePath...)
}

func New(c Config, gate Gate, logger *log.Logger) (*Client, error) {
	h, e := lanhttp.New(c.BaseURL, 30*time.Second)
	if e != nil {
		return nil, e
	}
	if c.Indexer == "" {
		c.Indexer = "jpopsuki"
	}
	return &Client{c, h, gate, logger}, nil
}
func (c *Client) safe(op string, e error) error {
	return sanitize.Error("Jackett "+op, e, c.config.APIKey)
}

func (c *Client) Search(ctx context.Context, q string) ([]Result, error) {
	if c.gate != nil {
		if e := c.gate.Wait(ctx, "jackett"); e != nil {
			return nil, e
		}
	}
	endpoint := strings.TrimRight(c.config.BaseURL, "/") + "/api/v2.0/indexers/" + url.PathEscape(c.config.Indexer) + "/results/torznab/api"
	u, _ := url.Parse(endpoint)
	v := u.Query()
	v.Set("apikey", c.config.APIKey)
	v.Set("t", "search")
	v.Set("q", q)
	u.RawQuery = v.Encode()
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, c.safe("search", e)
	}
	r, e := c.http.Do(req)
	if e != nil {
		return nil, c.safe("search", e)
	}
	defer r.Body.Close()
	b, e := lanhttp.Read(r.Body, 5*1024*1024)
	if e != nil {
		return nil, c.safe("search", e)
	}
	if e = c.responseError(r.StatusCode, r.Header, b); e != nil {
		return nil, e
	}
	if r.StatusCode != 200 {
		return nil, &HTTPError{Status: r.StatusCode, RetryAfter: sourcegate.ParseRetryAfter(r.Header.Get("Retry-After"), time.Now())}
	}
	var rss struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Items []struct {
				Title     string `xml:"title"`
				GUID      string `xml:"guid"`
				Link      string `xml:"link"`
				Size      int64  `xml:"size"`
				Date      string `xml:"pubDate"`
				Category  string `xml:"category"`
				Enclosure struct {
					URL    string `xml:"url,attr"`
					Length int64  `xml:"length,attr"`
				} `xml:"enclosure"`
				Attrs []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:"value,attr"`
				} `xml:"attr"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if e = xml.Unmarshal(b, &rss); e != nil {
		return nil, c.safe("parse search", e)
	}
	out := []Result{}
	for _, item := range rss.Channel.Items {
		link := item.Link
		if link == "" {
			link = item.Enclosure.URL
		}
		if link == "" {
			for _, attr := range item.Attrs {
				if attr.Name == "magneturl" && strings.HasPrefix(attr.Value, "magnet:") {
					link = attr.Value
					break
				}
			}
		}
		base, _ := url.Parse(c.config.BaseURL)
		ref, mag, e := DownloadRef(link, base.Path)
		if e != nil {
			if c.logger != nil {
				c.logger.Printf("Jackett skipped invalid result: %s", sanitize.Text(e.Error(), c.config.APIKey))
			}
			continue
		}
		x := Result{Title: item.Title, GUID: sanitize.Text(item.GUID, c.config.APIKey), Category: item.Category, PublishedAt: item.Date, DownloadRef: ref, MagnetOnly: mag, Size: item.Size}
		if x.Size == 0 {
			x.Size = item.Enclosure.Length
		}
		for _, a := range item.Attrs {
			n, e := strconv.ParseInt(a.Value, 10, 64)
			switch a.Name {
			case "seeders":
				if e == nil {
					x.Seeders = &n
				}
			case "peers":
				if e == nil {
					x.Peers = &n
				}
			case "grabs":
				if e == nil {
					x.Grabs = &n
				}
			case "category":
				if x.Category == "" {
					x.Category = a.Value
				}
			case "downloadvolumefactor":
				if f, e := strconv.ParseFloat(a.Value, 64); e == nil {
					x.DownloadFactor = &f
				}
			case "magneturl":
				if ref == "" && strings.HasPrefix(a.Value, "magnet:") {
					x.MagnetOnly = true
				}
			}
		}
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		var a, b int64
		if out[i].Seeders != nil {
			a = *out[i].Seeders
		}
		if out[j].Seeders != nil {
			b = *out[j].Seeders
		}
		return a > b
	})
	return out, nil
}

// responseError inspects Torznab error XML before interpreting HTTP status.
func (c *Client) responseError(status int, header http.Header, body []byte) error {
	if status == 429 {
		c.block(header.Get("Retry-After"))
	}
	var remote struct {
		XMLName     xml.Name
		Code        int    `xml:"code,attr"`
		Description string `xml:"description,attr"`
	}
	if xml.Unmarshal(body, &remote) == nil && remote.XMLName.Local == "error" {
		if remote.Code == 500 || remote.Code == 501 {
			c.block(header.Get("Retry-After"))
		}
		if c.logger != nil {
			c.logger.Printf("Jackett error %d: %s", remote.Code, sanitize.Text(remote.Description, c.config.APIKey))
		}
		return &APIError{Code: remote.Code, RetryAfter: sourcegate.ParseRetryAfter(header.Get("Retry-After"), time.Now())}
	}
	return nil
}

func (c *Client) block(header string) {
	if c.gate != nil {
		now := time.Now()
		c.gate.Block("jackett", now.Add(sourcegate.ParseRetryAfter(header, now)))
	}
}
func errorMessage(code int) string {
	switch code {
	case 100:
		return "凭据错误"
	case 101:
		return "账号已被封禁"
	case 102:
		return "权限不足"
	case 200, 201:
		return "搜索参数错误"
	case 203:
		return "功能不可用"
	case 500, 501:
		return "请求或下载次数超限，请稍后重试"
	case 910:
		return "API 已禁用"
	default:
		return "索引器请求失败"
	}
}
func (c *Client) Download(ctx context.Context, ref string) ([]byte, error) {
	if c.gate != nil {
		if err := c.gate.Wait(ctx, "jackett"); err != nil {
			return nil, err
		}
	}
	base, _ := url.Parse(c.config.BaseURL)
	ref, mag, e := DownloadRef(ref, base.Path)
	if e != nil {
		return nil, c.safe("download", e)
	}
	if mag {
		return nil, ErrMagnetOnly
	}
	u, _ := url.Parse(strings.TrimRight(c.config.BaseURL, "/") + ref)
	q := u.Query()
	q.Set("jackett_apikey", c.config.APIKey)
	u.RawQuery = q.Encode()
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, c.safe("download", e)
	}
	r, e := c.http.Do(req)
	if e != nil {
		if errors.Is(e, ErrMagnetOnly) {
			return nil, ErrMagnetOnly
		}
		return nil, c.safe("download", e)
	}
	defer r.Body.Close()
	b, e := lanhttp.Read(r.Body, torrent.MaxSize)
	if e != nil {
		return nil, c.safe("download", e)
	}
	if e = c.responseError(r.StatusCode, r.Header, b); e != nil {
		return nil, e
	}
	if r.StatusCode != 200 {
		return nil, &HTTPError{Status: r.StatusCode, RetryAfter: sourcegate.ParseRetryAfter(r.Header.Get("Retry-After"), time.Now())}
	}
	return b, nil
}

var badgePatterns = []struct {
	label   string
	pattern *regexp.Regexp
}{
	{"FLAC", regexp.MustCompile(`(?i)\bflac\b`)}, {"24bit", regexp.MustCompile(`(?i)\b24[ -]?(?:bit|bits)\b`)}, {"MP3 320", regexp.MustCompile(`(?i)\b(?:mp3[\s /-]*)?320(?:\s*kbps)?\b`)}, {"V0", regexp.MustCompile(`(?i)\bv0\b`)}, {"Log", regexp.MustCompile(`(?i)\blog\b`)}, {"Cue", regexp.MustCompile(`(?i)\bcue\b`)},
}

func Badges(title string) []string {
	out := []string{}
	for _, p := range badgePatterns {
		if p.pattern.MatchString(title) {
			out = append(out, p.label)
		}
	}
	return out
}
