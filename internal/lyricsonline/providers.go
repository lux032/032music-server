package lyricsonline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/lyrics"
)

func getJSON(ctx context.Context, hc *http.Client, rawURL string, headers map[string]string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("invalid JSON: %w", err)
	}
	return resp.StatusCode, nil
}

// ------------------------------------------------------------------ NetEase

// NetEase uses the public web API of NetEase Cloud Music.
type NetEase struct {
	HTTP    *http.Client
	BaseURL string // default https://music.163.com
}

func (n *NetEase) ID() string { return SourceNetEase }

func (n *NetEase) base() string {
	if n.BaseURL != "" {
		return strings.TrimRight(n.BaseURL, "/")
	}
	return "https://music.163.com"
}

var neteaseHeaders = map[string]string{"Referer": "https://music.163.com/"}

func (n *NetEase) Search(ctx context.Context, q Query) ([]Candidate, error) {
	term := strings.TrimSpace(q.Title + " " + q.Artist)
	if term == "" {
		return nil, nil
	}
	var resp struct {
		Code   int `json:"code"`
		Result struct {
			Songs []struct {
				ID       int64  `json:"id"`
				Name     string `json:"name"`
				Duration int64  `json:"duration"`
				Artists  []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Album struct {
					Name string `json:"name"`
				} `json:"album"`
			} `json:"songs"`
		} `json:"result"`
	}
	u := n.base() + "/api/search/get?" + url.Values{"s": {term}, "type": {"1"}, "limit": {"10"}, "offset": {"0"}}.Encode()
	if _, err := getJSON(ctx, n.HTTP, u, neteaseHeaders, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 && resp.Code != 200 {
		return nil, fmt.Errorf("search code %d", resp.Code)
	}
	out := make([]Candidate, 0, len(resp.Result.Songs))
	for _, s := range resp.Result.Songs {
		names := make([]string, 0, len(s.Artists))
		for _, a := range s.Artists {
			names = append(names, a.Name)
		}
		out = append(out, Candidate{Source: SourceNetEase, ID: strconv.FormatInt(s.ID, 10), Title: s.Name,
			Artist: strings.Join(names, ", "), Album: s.Album.Name, DurationMs: s.Duration})
	}
	return out, nil
}

func (n *NetEase) Get(ctx context.Context, id string) (Lyrics, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return Lyrics{}, fmt.Errorf("invalid NetEase id %q", id)
	}
	var resp struct {
		Code        int  `json:"code"`
		NoLyric     bool `json:"nolyric"`
		Uncollected bool `json:"uncollected"`
		Lrc         struct {
			Lyric string `json:"lyric"`
		} `json:"lrc"`
		Tlyric struct {
			Lyric string `json:"lyric"`
		} `json:"tlyric"`
	}
	u := n.base() + "/api/song/lyric?" + url.Values{"id": {id}, "lv": {"-1"}, "kv": {"-1"}, "tv": {"-1"}}.Encode()
	if _, err := getJSON(ctx, n.HTTP, u, neteaseHeaders, &resp); err != nil {
		return Lyrics{}, err
	}
	if resp.Code != 0 && resp.Code != 200 {
		return Lyrics{}, fmt.Errorf("lyric code %d", resp.Code)
	}
	if resp.NoLyric || resp.Uncollected || !usable(resp.Lrc.Lyric) {
		return Lyrics{}, ErrNoLyrics
	}
	l := Lyrics{Source: SourceNetEase, ID: id, LRC: resp.Lrc.Lyric}
	if usable(resp.Tlyric.Lyric) {
		l.TranslationLRC = resp.Tlyric.Lyric
	}
	return l, nil
}

// ------------------------------------------------------------------ LRCLIB

// LRCLIB uses https://lrclib.net (search results already carry the lyrics).
type LRCLIB struct {
	HTTP    *http.Client
	BaseURL string // default https://lrclib.net
}

func (l *LRCLIB) ID() string { return SourceLRCLIB }

func (l *LRCLIB) base() string {
	if l.BaseURL != "" {
		return strings.TrimRight(l.BaseURL, "/")
	}
	return "https://lrclib.net"
}

type lrclibTrack struct {
	ID           int64   `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  *string `json:"plainLyrics"`
	SyncedLyrics *string `json:"syncedLyrics"`
}

func (t lrclibTrack) text() string {
	if t.Instrumental {
		return ""
	}
	if t.SyncedLyrics != nil && usable(*t.SyncedLyrics) {
		return *t.SyncedLyrics
	}
	if t.PlainLyrics != nil && usable(*t.PlainLyrics) {
		return *t.PlainLyrics
	}
	return ""
}

func (l *LRCLIB) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if strings.TrimSpace(q.Title) == "" {
		return nil, nil
	}
	params := url.Values{"track_name": {q.Title}}
	if q.Artist != "" {
		params.Set("artist_name", q.Artist)
	}
	var tracks []lrclibTrack
	if _, err := getJSON(ctx, l.HTTP, l.base()+"/api/search?"+params.Encode(), nil, &tracks); err != nil {
		return nil, err
	}
	if len(tracks) == 0 && q.Artist != "" {
		// Artist spelled differently: fall back to a free-text title search.
		if _, err := getJSON(ctx, l.HTTP, l.base()+"/api/search?"+url.Values{"q": {q.Title}}.Encode(), nil, &tracks); err != nil {
			return nil, err
		}
	}
	out := make([]Candidate, 0, len(tracks))
	for _, t := range tracks {
		text := t.text()
		if text == "" {
			continue
		}
		synced := lyrics.ParseLRC(text).Synced
		out = append(out, Candidate{Source: SourceLRCLIB, ID: strconv.FormatInt(t.ID, 10), Title: t.TrackName, Artist: t.ArtistName,
			Album: t.AlbumName, DurationMs: int64(t.Duration * 1000), Synced: &synced, lrc: text})
	}
	return out, nil
}

func (l *LRCLIB) Get(ctx context.Context, id string) (Lyrics, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return Lyrics{}, fmt.Errorf("invalid LRCLIB id %q", id)
	}
	var t lrclibTrack
	status, err := getJSON(ctx, l.HTTP, l.base()+"/api/get/"+id, nil, &t)
	if err != nil {
		return Lyrics{}, err
	}
	if status == http.StatusNotFound {
		return Lyrics{}, ErrNoLyrics
	}
	text := t.text()
	if text == "" {
		return Lyrics{}, ErrNoLyrics
	}
	return Lyrics{Source: SourceLRCLIB, ID: id, Title: t.TrackName, Artist: t.ArtistName, LRC: text}, nil
}
