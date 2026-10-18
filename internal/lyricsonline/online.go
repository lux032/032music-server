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

// ErrIncomplete means nothing matched but some source failed (network, rate
// limit): the miss may be wrong, so it should only be remembered briefly.
var ErrIncomplete = errors.New("online lyrics lookup incomplete")

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
	// Accepted: good enough to be used automatically.
	Accepted bool `json:"-"`
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

// ProviderReport is what one source answered during a lookup.
type ProviderReport struct {
	Source    string
	Results   int
	Err       error
	ElapsedMs int64
}

// Report explains one lookup (for logs and the admin diagnosis page).
type Report struct {
	Providers  []ProviderReport
	Candidates []Candidate // every result, best first; Accepted marks usable ones
	Downloads  []string    // "source:id -> outcome", in the order tried
}

// Failed lists the sources that failed ("netease: HTTP 503").
func (r Report) Failed() []string {
	var out []string
	for _, p := range r.Providers {
		if p.Err != nil {
			out = append(out, p.Source+": "+p.Err.Error())
		}
	}
	return out
}

// Summary is a one-line description for logs.
func (r Report) Summary() string {
	parts := make([]string, 0, len(r.Providers)+4)
	for _, p := range r.Providers {
		if p.Err != nil {
			parts = append(parts, fmt.Sprintf("%s=error(%v) %dms", p.Source, p.Err, p.ElapsedMs))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%d %dms", p.Source, p.Results, p.ElapsedMs))
		}
	}
	for i, c := range r.Candidates {
		if i == 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("#%d %s:%s %q/%q %dms score=%.2f ok=%v", i+1, c.Source, c.ID, c.Title, c.Artist, c.DurationMs, c.Score, c.Accepted))
	}
	if len(r.Downloads) > 0 {
		parts = append(parts, "downloads: "+strings.Join(r.Downloads, "; "))
	}
	return strings.Join(parts, " | ")
}

// Search queries every provider concurrently and returns all candidates
// ranked best first. It fails only when every provider failed.
func (c *Client) Search(ctx context.Context, q Query) ([]Candidate, error) {
	all, _, _, err := c.search(ctx, q)
	return all, err
}

func (c *Client) search(ctx context.Context, q Query) (all, accepted []Candidate, report Report, err error) {
	type result struct {
		cands   []Candidate
		err     error
		elapsed time.Duration
	}
	results := make([]result, len(c.providers))
	var wg sync.WaitGroup
	for i, p := range c.providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			cands, err := p.Search(ctx, q)
			results[i] = result{cands, err, time.Since(start)}
		}()
	}
	wg.Wait()
	var merged []Candidate
	var errs []error
	for i, r := range results {
		id := c.providers[i].ID()
		report.Providers = append(report.Providers, ProviderReport{Source: id, Results: len(r.cands), Err: r.err, ElapsedMs: r.elapsed.Milliseconds()})
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, r.err))
			continue
		}
		merged = append(merged, r.cands...)
	}
	if len(c.providers) > 0 && len(errs) == len(c.providers) {
		return nil, nil, report, errors.Join(errs...)
	}
	all, accepted = Rank(q, merged)
	report.Candidates = all
	return all, accepted, report, nil
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
	l, cand, _, err := c.Lookup(ctx, q)
	return l, cand, err
}

// Lookup is Best plus a report of what every source answered. Accepted
// candidates are tried best first; lyrics without timestamps are kept only as
// a fallback while later candidates (within maxDownloads) may still be synced.
// Errors:
// ErrNotFound (cacheable miss), ErrIncomplete wrapping the source errors when
// some source failed and nothing matched, or the error of a failed download.
func (c *Client) Lookup(ctx context.Context, q Query) (Lyrics, Candidate, Report, error) {
	_, accepted, report, err := c.search(ctx, q)
	if err != nil {
		return Lyrics{}, Candidate{}, report, err
	}
	var lastErr error
	var fallback *Lyrics
	var fallbackCand Candidate
	tries := 0
	for _, cand := range accepted {
		key := cand.Source + ":" + cand.ID
		var l Lyrics
		if cand.lrc != "" {
			l = Lyrics{Source: cand.Source, ID: cand.ID, Title: cand.Title, Artist: cand.Artist, LRC: cand.lrc}
			report.Downloads = append(report.Downloads, key+" -> lyrics in search result")
		} else {
			if tries == maxDownloads {
				continue // later candidates may still carry lyrics in the search result
			}
			tries++
			var err error
			if l, err = c.Get(ctx, cand.Source, cand.ID); err != nil {
				report.Downloads = append(report.Downloads, key+" -> "+err.Error())
				if !errors.Is(err, ErrNoLyrics) {
					lastErr = err
				}
				continue
			}
			report.Downloads = append(report.Downloads, key+" -> ok")
		}
		synced := lyrics.ParseLRC(l.LRC).Synced
		cand.Synced = &synced
		if synced {
			return l, cand, report, nil
		}
		report.Downloads[len(report.Downloads)-1] += " (no timestamps, kept as fallback)"
		if fallback == nil {
			fallback, fallbackCand = &l, cand
		}
	}
	if fallback != nil {
		return *fallback, fallbackCand, report, nil
	}
	if lastErr != nil {
		return Lyrics{}, Candidate{}, report, lastErr
	}
	if failed := report.Failed(); len(failed) > 0 {
		return Lyrics{}, Candidate{}, report, fmt.Errorf("%w (%s)", ErrIncomplete, strings.Join(failed, "; "))
	}
	return Lyrics{}, Candidate{}, report, ErrNotFound
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
