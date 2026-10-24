package mb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/sourcegate"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrGone = errors.New("MusicBrainz entity gone")

type Credit struct {
	Name   string `json:"name"`
	Artist struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
}
type ReleaseGroup struct {
	Score            int      `json:"score"`
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	PrimaryType      string   `json:"primary-type"`
	FirstReleaseDate string   `json:"first-release-date"`
	Disambiguation   string   `json:"disambiguation"`
	SecondaryTypes   []string `json:"secondary-types"`
	ArtistCredit     []Credit `json:"artist-credit"`
}
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Gate    *sourcegate.Gate
	UA      func(context.Context) string
	Now     func() time.Time
	Logger  *slog.Logger
}

// IsSpecialArtist excludes MusicBrainz special-purpose buckets, not real performers.
func IsSpecialArtist(id string) bool {
	switch strings.ToLower(id) {
	case "89ad4ac3-39f7-470e-963a-56509c546377", "f731ccc4-e22a-43af-a747-64213329e088", "125ec42a-7229-4250-afc5-e057484327fe":
		return true
	}
	return false
}
func (c *Client) get(ctx context.Context, path string, out any) error {
	if e := c.Gate.Wait(ctx, "musicbrainz"); e != nil {
		return e
	}
	base := c.BaseURL
	if base == "" {
		base = "https://musicbrainz.org/ws/2"
	}
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+path, nil)
	if e != nil {
		return errors.New("invalid MusicBrainz request")
	}
	if c.UA != nil {
		req.Header.Set("User-Agent", c.UA(ctx))
	}
	res, e := c.HTTP.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("MusicBrainz request failed")
	}
	defer res.Body.Close()
	if sourcegate.IsRateLimitResponse("musicbrainz", res.StatusCode, res.Header) {
		now := time.Now()
		if c.Now != nil {
			now = c.Now()
		}
		d := sourcegate.ParseRetryAfter(res.Header.Get("Retry-After"), now)
		c.Gate.Block("musicbrainz", now.Add(d))
		return &sourcegate.CooldownError{Source: "musicbrainz", RetryAfter: d}
	}
	if res.StatusCode == 404 {
		return ErrGone
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("MusicBrainz returned HTTP %d", res.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(out); e != nil {
		return errors.New("invalid MusicBrainz response")
	}
	return ctx.Err()
}
func (c *Client) BrowseArtistReleaseGroups(ctx context.Context, id string) ([]ReleaseGroup, error) {
	if IsSpecialArtist(id) {
		return nil, nil
	}
	id, ok := metadata.NormalizeMusicBrainzID(id)
	if !ok {
		return nil, errors.New("invalid artist MBID")
	}
	seen := map[string]bool{}
	out := []ReleaseGroup{}
	offset := 0
	for page := 0; page < 20; page++ {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		var v struct {
			Groups []ReleaseGroup `json:"release-groups"`
			Count  int            `json:"release-group-count"`
		}
		q := url.Values{"artist": {id}, "type": {"album|ep|single"}, "inc": {"artist-credits"}, "limit": {"100"}, "offset": {strconv.Itoa(offset)}, "fmt": {"json"}}
		if e := c.get(ctx, "/release-group?"+q.Encode(), &v); e != nil {
			return nil, e
		}
		for _, rg := range v.Groups {
			key, valid := metadata.NormalizeMusicBrainzID(rg.ID)
			if valid && !seen[key] {
				rg.ID = key
				seen[key] = true
				out = append(out, rg)
			}
		}
		offset += len(v.Groups)
		if len(v.Groups) == 0 || offset >= v.Count {
			return out, nil
		}
	}
	if c.Logger != nil {
		c.Logger.Warn("MusicBrainz browse truncated at 20 pages")
	}
	return out, nil
}
func (c *Client) ReleaseGroup(ctx context.Context, id string) (ReleaseGroup, error) {
	var rg ReleaseGroup
	id, ok := metadata.NormalizeMusicBrainzID(id)
	if !ok {
		return rg, errors.New("invalid release group ID")
	}
	e := c.get(ctx, "/release-group/"+id+"?inc=artist-credits&fmt=json", &rg)
	return rg, e
}

// EscapeLucene quotes reserved query syntax inside a releasegroup phrase.
func EscapeLucene(title string) string {
	return titleWithEscapes(title)
}
func titleWithEscapes(title string) string {
	var b strings.Builder
	for _, r := range title {
		if strings.ContainsRune(`+-&|!(){}[]^"~*?:\/`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
func (c *Client) SearchReleaseGroups(ctx context.Context, title string) ([]ReleaseGroup, error) {
	var v struct {
		Groups []ReleaseGroup `json:"release-groups"`
	}
	q := url.Values{"query": {`releasegroup:"` + EscapeLucene(title) + `"`}, "limit": {"10"}, "fmt": {"json"}}
	if e := c.get(ctx, "/release-group?"+q.Encode(), &v); e != nil {
		return nil, e
	}
	out := []ReleaseGroup{}
	for _, rg := range v.Groups {
		if id, ok := metadata.NormalizeMusicBrainzID(rg.ID); ok {
			rg.ID = id
			out = append(out, rg)
		}
	}
	return out, nil
}
