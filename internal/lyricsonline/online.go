// Package lyricsonline searches and downloads lyrics from online sources
// (NetEase Cloud Music, LRCLIB). Requests honour HTTP(S)_PROXY from the
// environment (container settings).
package lyricsonline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/lyrics"
)

// Source ids used in the API and database.
const (
	SourceNetEase = "netease"
	SourceLRCLIB  = "lrclib"
)

// SourceName is the display name of a source id.
func SourceName(id string) string {
	switch id {
	case SourceNetEase:
		return "NetEase"
	case SourceLRCLIB:
		return "LRCLIB"
	}
	return id
}

// ErrNotFound means every source answered and none had matching lyrics.
var ErrNotFound = errors.New("no matching lyrics found online")

// ErrNoLyrics means the requested remote song has no lyrics (or is instrumental).
var ErrNoLyrics = errors.New("remote song has no lyrics")

// Query describes the local track.
type Query struct {
	Title      string
	Artist     string
	Album      string
	DurationMs int64
}

// Candidate is one search result.
type Candidate struct {
	Source     string  `json:"source"`
	ID         string  `json:"id"`
	Title      string  `json:"name"`
	Artist     string  `json:"artist"`
	Album      string  `json:"album,omitempty"`
	DurationMs int64   `json:"durationMs,omitempty"`
	Synced     *bool   `json:"synced"`
	Score      float64 `json:"score"`
	// lrc is set when the search response already carries the lyrics (LRCLIB).
	lrc string
}

// Lyrics is downloaded lyrics of one remote song.
type Lyrics struct {
	Source string
	ID     string
	Title  string
	Artist string
	// LRC is the original lyrics (LRC or plain text).
	LRC string
	// TranslationLRC is the source's own translation (NetEase tlyric), aligned by timestamp.
	TranslationLRC string
}

// Provider is one online source.
type Provider interface {
	ID() string
	Search(ctx context.Context, q Query) ([]Candidate, error)
	Get(ctx context.Context, id string) (Lyrics, error)
}

// Client queries all providers.
type Client struct {
	providers []Provider
}

const userAgent = "032music-server (https://github.com/lux032/032music-server)"

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

// New returns a client for NetEase and LRCLIB.
func New() *Client {
	hc := newHTTPClient()
	return NewWithProviders(&NetEase{HTTP: hc}, &LRCLIB{HTTP: hc})
}

// NewWithProviders is used by tests.
func NewWithProviders(p ...Provider) *Client { return &Client{providers: p} }

func (c *Client) provider(id string) (Provider, bool) {
	for _, p := range c.providers {
		if p.ID() == id {
			return p, true
		}
	}
	return nil, false
}

// ValidSource reports whether id is a configured source.
func (c *Client) ValidSource(id string) bool {
	_, ok := c.provider(id)
	return ok
}

// Search queries every provider concurrently and returns all candidates
// ranked best first. It fails only when every provider failed.
func (c *Client) Search(ctx context.Context, q Query) ([]Candidate, error) {
	all, _, err := c.search(ctx, q)
	return all, err
}

func (c *Client) search(ctx context.Context, q Query) (all, accepted []Candidate, err error) {
	type result struct {
		cands []Candidate
		err   error
	}
	results := make([]result, len(c.providers))
	var wg sync.WaitGroup
	for i, p := range c.providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cands, err := p.Search(ctx, q)
			results[i] = result{cands, err}
		}()
	}
	wg.Wait()
	var merged []Candidate
	var errs []error
	for i, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.providers[i].ID(), r.err))
			continue
		}
		merged = append(merged, r.cands...)
	}
	if len(c.providers) > 0 && len(errs) == len(c.providers) {
		return nil, nil, errors.Join(errs...)
	}
	all, accepted = Rank(q, merged)
	return all, accepted, nil
}

// Get downloads one remote song's lyrics.
func (c *Client) Get(ctx context.Context, source, id string) (Lyrics, error) {
	p, ok := c.provider(source)
	if !ok {
		return Lyrics{}, fmt.Errorf("unknown lyrics source %q", source)
	}
	return p.Get(ctx, id)
}

// maxDownloads bounds how many accepted candidates are tried when the best
// ones turn out to have no lyrics.
const maxDownloads = 3

// Best finds and downloads the best matching lyrics for q. It returns
// ErrNotFound when the sources answered but nothing usable matched; any other
// error is transient (network, rate limit) and should not be cached.
func (c *Client) Best(ctx context.Context, q Query) (Lyrics, Candidate, error) {
	_, accepted, err := c.search(ctx, q)
	if err != nil {
		return Lyrics{}, Candidate{}, err
	}
	var lastErr error
	tries := 0
	for _, cand := range accepted {
		if cand.lrc != "" {
			return Lyrics{Source: cand.Source, ID: cand.ID, Title: cand.Title, Artist: cand.Artist, LRC: cand.lrc}, cand, nil
		}
		if tries == maxDownloads {
			break
		}
		tries++
		l, err := c.Get(ctx, cand.Source, cand.ID)
		if err == nil {
			return l, cand, nil
		}
		if !errors.Is(err, ErrNoLyrics) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return Lyrics{}, Candidate{}, lastErr
	}
	return Lyrics{}, Candidate{}, ErrNotFound
}

// usable reports whether text contains lyrics (not empty, not a "pure music" placeholder).
func usable(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	res := lyrics.ParseLRC(text)
	if len(res.Lines) == 0 {
		return false
	}
	if len(res.Lines) <= 2 {
		joined := ""
		for _, l := range res.Lines {
			joined += l.Text
		}
		if strings.Contains(joined, "纯音乐") || strings.Contains(strings.ToLower(joined), "instrumental") {
			return false
		}
	}
	return true
}
