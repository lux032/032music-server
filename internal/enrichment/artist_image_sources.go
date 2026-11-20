package enrichment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

var (
	reDeezerArtistPath = regexp.MustCompile(`(?i)/(?:[a-z]{2}/)?artist/(\d+)`)
)

// wikipediaArtistImage fetches the lead image of an artist's Wikipedia article
// via MediaWiki prop=pageimages when Wikidata lacks a P18 claim.
func (m *Manager) wikipediaArtistImage(ctx context.Context, wikidataResource string) (string, error) {
	sitelinks, err := m.wikidataSitelinks(ctx, wikidataResource)
	if err != nil {
		return "", err
	}
	for _, lang := range []string{"ja", "en", "zh"} {
		title := strings.TrimSpace(sitelinks[lang])
		if title == "" {
			continue
		}
		imgURL, imgErr := m.wikipediaPageImage(ctx, lang, title)
		if imgErr != nil {
			if asRateLimited(imgErr) != nil {
				return "", imgErr
			}
			continue
		}
		if imgURL != "" {
			return imgURL, nil
		}
	}
	return "", nil
}

func (m *Manager) wikipediaPageImage(ctx context.Context, language, title string) (string, error) {
	query := url.Values{
		"action":        {"query"},
		"prop":          {"pageimages"},
		"piprop":        {"thumbnail|original"},
		"pithumbsize":   {"800"},
		"redirects":     {"1"},
		"format":        {"json"},
		"formatversion": {"2"},
		"titles":        {title},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+language+".wikipedia.org/w/api.php?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var resp struct {
		Query struct {
			Pages []struct {
				Thumbnail struct {
					Source string `json:"source"`
					Width  int    `json:"width"`
					Height int    `json:"height"`
				} `json:"thumbnail"`
				Original struct {
					Source string `json:"source"`
					Width  int    `json:"width"`
					Height int    `json:"height"`
				} `json:"original"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err = m.doSourceJSON(m.client, req, "wikipedia", &resp); err != nil {
		return "", err
	}
	if len(resp.Query.Pages) == 0 {
		return "", nil
	}
	page := resp.Query.Pages[0]
	if src := strings.TrimSpace(page.Thumbnail.Source); src != "" && !strings.HasSuffix(strings.ToLower(src), ".svg") {
		return src, nil
	}
	if src := strings.TrimSpace(page.Original.Source); src != "" && !strings.HasSuffix(strings.ToLower(src), ".svg") {
		return src, nil
	}
	return "", nil
}

// extractDeezerArtistID extracts the numeric artist ID from a Deezer artist URL.
func extractDeezerArtistID(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "deezer.com" && !strings.HasSuffix(host, ".deezer.com") {
		return ""
	}
	m := reDeezerArtistPath.FindStringSubmatch(u.Path)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// deezerArtistImageByID fetches picture_xl (1000x1000) for a confirmed Deezer
// artist ID using Deezer's free public API (no API key required).
func (m *Manager) deezerArtistImageByID(ctx context.Context, deezerID string) (string, error) {
	deezerID = strings.TrimSpace(deezerID)
	if deezerID == "" {
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deezer.com/artist/"+url.PathEscape(deezerID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var resp struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		PictureXL  string `json:"picture_xl"`
		PictureBig string `json:"picture_big"`
	}
	if err = m.doSourceJSON(m.client, req, "deezer", &resp); err != nil {
		return "", err
	}
	return pickValidDeezerPicture(resp.PictureXL, resp.PictureBig), nil
}

func pickValidDeezerPicture(candidates ...string) string {
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		lower := strings.ToLower(raw)
		// Deezer uses empty-hash /artist//1000x1000... paths for silhouette placeholders.
		if strings.Contains(lower, "/images/artist//") || strings.Contains(lower, "/artist//") {
			continue
		}
		return raw
	}
	return ""
}

// deezerSearchArtistImage searches Deezer's public artist API using the artist
// anchor and verifies exact normalized name/alias match (plus optional album
// cross-check when multiple same-name artists exist).
func (m *Manager) deezerSearchArtistImage(ctx context.Context, anchor storage.ArtistBiographyAnchor) (string, string, error) {
	queries := buildArtistSearchNames(anchor)
	if len(queries) == 0 {
		return "", "", nil
	}
	nameSet := normalizedAnchorNameSet(anchor)
	albumSet := normalizedStringSet(anchor.KnownAlbums)

	for _, qName := range queries {
		q := url.Values{"q": {qName}, "limit": {"5"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deezer.com/search/artist?"+q.Encode(), nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
		var resp struct {
			Data []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Link       string `json:"link"`
				NBAlbum    int    `json:"nb_album"`
				NBFan      int    `json:"nb_fan"`
				PictureXL  string `json:"picture_xl"`
				PictureBig string `json:"picture_big"`
			} `json:"data"`
		}
		if err = m.doSourceJSON(m.client, req, "deezer", &resp); err != nil {
			if asRateLimited(err) != nil {
				return "", "", err
			}
			continue
		}
		type hit struct {
			id      int64
			imgURL  string
			pageURL string
			nbFan   int
		}
		var matches []hit
		for _, item := range resp.Data {
			if !nameSet[normalize(item.Name)] {
				continue
			}
			img := pickValidDeezerPicture(item.PictureXL, item.PictureBig)
			matches = append(matches, hit{id: item.ID, imgURL: img, pageURL: strings.TrimSpace(item.Link), nbFan: item.NBFan})
		}
		if len(matches) == 0 {
			continue
		}
		if len(matches) == 1 && len(albumSet) == 0 {
			return matches[0].imgURL, matches[0].pageURL, nil
		}
		// When albums are known or multiple homonymous artists match, verify album overlap.
		if len(albumSet) > 0 {
			for _, h := range matches {
				overlap, checkErr := m.deezerArtistHasAlbumOverlap(ctx, h.id, albumSet)
				if checkErr != nil && asRateLimited(checkErr) != nil {
					return "", "", checkErr
				}
				if overlap && h.imgURL != "" {
					return h.imgURL, h.pageURL, nil
				}
			}
		}
	}
	return "", "", nil
}

func (m *Manager) deezerArtistHasAlbumOverlap(ctx context.Context, deezerID int64, albumSet map[string]bool) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.deezer.com/artist/%d/albums?limit=15", deezerID), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var resp struct {
		Data []struct {
			Title string `json:"title"`
		} `json:"data"`
	}
	if err = m.doSourceJSON(m.client, req, "deezer", &resp); err != nil {
		return false, err
	}
	for _, a := range resp.Data {
		normTitle := normalize(a.Title)
		if normTitle == "" {
			continue
		}
		if albumSet[normTitle] {
			return true, nil
		}
		for known := range albumSet {
			if len([]rune(known)) >= 4 && len([]rune(normTitle)) >= 4 && (strings.Contains(normTitle, known) || strings.Contains(known, normTitle)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// bangumiPersonImage searches Bangumi API v0 (/v0/search/persons) for an
// artist, voice actor, or composer and returns their portrait image URL.
func (m *Manager) bangumiPersonImage(ctx context.Context, anchor storage.ArtistBiographyAnchor) (string, string, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		return "", "", err
	}
	if !setting.Enabled {
		return "", "", nil
	}
	queries := buildArtistSearchNames(anchor)
	if len(queries) == 0 {
		return "", "", nil
	}
	nameSet := normalizedAnchorNameSet(anchor)
	base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}

	for _, qName := range queries {
		if waitErr := m.waitBangumiRateLimit(ctx); waitErr != nil {
			return "", "", waitErr
		}
		payload, _ := json.Marshal(map[string]any{
			"keyword": qName,
		})
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v0/search/persons?limit=5", bytes.NewReader(payload))
		if reqErr != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent(setting))
		if token := strings.TrimSpace(setting.APIKey); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		var resp struct {
			Data []struct {
				ID     int64    `json:"id"`
				Name   string   `json:"name"`
				Career []string `json:"career"`
				Images struct {
					Large  string `json:"large"`
					Medium string `json:"medium"`
					Common string `json:"common"`
				} `json:"images"`
				Infobox []struct {
					Key   string `json:"key"`
					Value any    `json:"value"`
				} `json:"infobox"`
			} `json:"data"`
		}
		if doErr := m.doSourceJSON(m.client, req, "bangumi", &resp); doErr != nil {
			if asRateLimited(doErr) != nil {
				return "", "", doErr
			}
			continue
		}
		var matchedID int64
		var matchedImage string
		for _, p := range resp.Data {
			if p.ID <= 0 || !hasBangumiMusicCareer(p.Career) {
				continue
			}
			matched := nameSet[normalize(p.Name)]
			if !matched {
				for _, box := range p.Infobox {
					if box.Key != "别名" && box.Key != "简体中文名" && box.Key != "纯假名" && box.Key != "罗马字" {
						continue
					}
					for _, alias := range flattenBangumiInfoboxValue(box.Value) {
						if nameSet[normalize(alias)] {
							matched = true
							break
						}
					}
					if matched {
						break
					}
				}
			}
			if !matched {
				continue
			}
			if matchedID != 0 && matchedID != p.ID {
				matchedID = 0
				break
			}
			matchedID = p.ID
			matchedImage = firstNonEmpty(p.Images.Large, p.Images.Medium, p.Images.Common)
		}
		if matchedID != 0 && matchedImage != "" && !strings.Contains(strings.ToLower(matchedImage), "no_icon") {
			return matchedImage, fmt.Sprintf("https://bgm.tv/person/%d", matchedID), nil
		}
	}
	return "", "", nil
}

func hasBangumiMusicCareer(careers []string) bool {
	for _, career := range careers {
		switch career {
		case "artist", "seiyu", "producer":
			return true
		}
	}
	return false
}

func flattenBangumiInfoboxValue(v any) []string {
	var out []string
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) != "" {
			out = append(out, strings.TrimSpace(x))
		}
	case []any:
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				if s, ok := m["v"].(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			} else if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

// fanartArtistImage fetches the highest-liked 1000x1000 artistthumb from
// Fanart.tv v3 using a confirmed MusicBrainz ID.
func (m *Manager) fanartArtistImage(ctx context.Context, mbid, apiKey string) (string, error) {
	mbid = strings.TrimSpace(mbid)
	apiKey = strings.TrimSpace(apiKey)
	if mbid == "" || apiKey == "" {
		return "", nil
	}
	endpoint := "https://webservice.fanart.tv/v3/music/" + url.PathEscape(mbid) + "?api_key=" + url.QueryEscape(apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var resp struct {
		ArtistThumb []struct {
			URL   string `json:"url"`
			Likes string `json:"likes"`
		} `json:"artistthumb"`
	}
	if err = m.doSourceJSON(m.client, req, "fanart", &resp); err != nil {
		if asRateLimited(err) != nil {
			return "", err
		}
		return "", errors.New("fanart lookup failed")
	}
	if len(resp.ArtistThumb) == 0 {
		return "", nil
	}
	sort.SliceStable(resp.ArtistThumb, func(i, j int) bool {
		li, _ := strconv.Atoi(resp.ArtistThumb[i].Likes)
		lj, _ := strconv.Atoi(resp.ArtistThumb[j].Likes)
		return li > lj
	})
	return strings.TrimSpace(resp.ArtistThumb[0].URL), nil
}

func buildArtistSearchNames(anchor storage.ArtistBiographyAnchor) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		key := normalize(s)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	add(anchor.Name)
	for _, a := range anchor.Aliases {
		if len(out) >= 3 {
			break
		}
		add(a)
	}
	return out
}

func normalizedAnchorNameSet(anchor storage.ArtistBiographyAnchor) map[string]bool {
	set := map[string]bool{}
	if k := normalize(anchor.Name); k != "" {
		set[k] = true
	}
	for _, a := range anchor.Aliases {
		if k := normalize(a); k != "" {
			set[k] = true
		}
	}
	return set
}

func normalizedStringSet(items []string) map[string]bool {
	set := map[string]bool{}
	for _, item := range items {
		if k := normalize(item); k != "" {
			set[k] = true
		}
	}
	return set
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
