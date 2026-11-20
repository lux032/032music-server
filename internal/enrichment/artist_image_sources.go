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

	"github.com/lux032/032music-server/internal/netfetch"
	"github.com/lux032/032music-server/internal/storage"
	"golang.org/x/text/unicode/norm"
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
		if !validStructuredArtistImageURL(raw) {
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

// deezerSearchArtistImage prefers picture_xl and requires album evidence whenever
// the artist's local discography is known. Fan counts are not identity evidence.
func (m *Manager) deezerSearchArtistImage(ctx context.Context, anchor storage.ArtistBiographyAnchor) (string, string, error) {
	names := normalizedAnchorNameSet(anchor)
	albums := normalizedStringSet(anchor.KnownAlbums)
	albumMatches := map[int64]bool{}
	var lookupErr error
	type hit struct {
		imageURL string
		pageURL  string
	}

	for _, qName := range buildArtistSearchNames(anchor) {
		q := url.Values{"q": {qName}, "limit": {"10"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deezer.com/search/artist?"+q.Encode(), nil)
		if err != nil {
			return "", "", err
		}
		req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
		var resp struct {
			Data []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Link       string `json:"link"`
				PictureXL  string `json:"picture_xl"`
				PictureBig string `json:"picture_big"`
			} `json:"data"`
		}
		if err := m.doSourceJSON(m.client, req, "deezer", &resp); err != nil {
			if asRateLimited(err) != nil || ctx.Err() != nil {
				return "", "", err
			}
			lookupErr = err
			continue
		}

		matches := map[int64]hit{}
		var matchIDs []int64
		for _, item := range resp.Data {
			if item.ID <= 0 || !names[compactArtistName(item.Name)] {
				continue
			}
			img := pickValidDeezerPicture(item.PictureXL, item.PictureBig)
			prior, exists := matches[item.ID]
			if !exists {
				matchIDs = append(matchIDs, item.ID)
			}
			if prior.imageURL == "" {
				matches[item.ID] = hit{imageURL: img, pageURL: strings.TrimSpace(item.Link)}
			}
		}
		if len(albums) == 0 && len(matches) > 1 {
			return "", "", nil
		}

		var selected hit
		var confirmed int
		var albumLookupErr error
		for _, id := range matchIDs {
			if len(albums) > 0 {
				overlap, checked := albumMatches[id]
				if !checked {
					overlap, err = m.deezerArtistHasAlbumOverlap(ctx, id, albums)
					if err != nil {
						if asRateLimited(err) != nil || ctx.Err() != nil {
							return "", "", err
						}
						albumLookupErr = err
						continue
					}
					albumMatches[id] = overlap
				}
				if !overlap {
					continue
				}
			}
			confirmed++
			selected = matches[id]
		}
		if confirmed > 1 {
			return "", "", nil
		}
		if albumLookupErr != nil {
			// An unchecked homonym might also match the local discography.
			if confirmed == 1 {
				return "", "", albumLookupErr
			}
			lookupErr = albumLookupErr
		}
		if confirmed == 1 && selected.imageURL != "" {
			return selected.imageURL, selected.pageURL, nil
		}
	}
	return "", "", lookupErr
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
	for _, album := range resp.Data {
		if albumSet[normalize(norm.NFKC.String(album.Title))] {
			return true, nil
		}
	}
	return false, nil
}

type bangumiPersonInfoboxEntry struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
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

	var lookupErr error
	for _, qName := range queries {
		if waitErr := m.waitBangumiRateLimit(ctx); waitErr != nil {
			return "", "", waitErr
		}
		// Search by keyword and check careers locally because the API combines career filters with AND.
		payload, _ := json.Marshal(map[string]string{"keyword": qName})
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
					Grid   string `json:"grid"`
				} `json:"images"`
				Infobox []bangumiPersonInfoboxEntry `json:"infobox"`
			} `json:"data"`
		}
		if doErr := m.doSourceJSON(m.client, req, "bangumi", &resp); doErr != nil {
			if asRateLimited(doErr) != nil || ctx.Err() != nil {
				return "", "", doErr
			}
			lookupErr = doErr
			continue
		}
		var matchedID int64
		var matchedImage string
		for _, p := range resp.Data {
			if p.ID <= 0 || !hasBangumiMusicCareer(p.Career) {
				continue
			}
			var imgURL string
			for _, candidate := range []string{p.Images.Large, p.Images.Medium, p.Images.Common, p.Images.Grid} {
				candidate = strings.TrimSpace(candidate)
				if validStructuredArtistImageURL(candidate) {
					imgURL = candidate
					break
				}
			}
			if imgURL == "" {
				continue
			}
			if !nameSet[compactArtistName(p.Name)] {
				infobox := p.Infobox
				if len(infobox) == 0 {
					var detailErr error
					infobox, detailErr = m.bangumiPersonInfobox(ctx, base, p.ID)
					if detailErr != nil {
						if asRateLimited(detailErr) != nil || ctx.Err() != nil {
							return "", "", detailErr
						}
						lookupErr = detailErr
						continue
					}
				}
				if !bangumiInfoboxMatchesName(infobox, nameSet) {
					continue
				}
			}
			if matchedID != 0 && matchedID != p.ID {
				// Both candidates have plausible identities and usable portraits.
				return "", "", nil
			}
			matchedID, matchedImage = p.ID, imgURL
		}
		if matchedID != 0 {
			return matchedImage, fmt.Sprintf("https://bgm.tv/person/%d", matchedID), nil
		}
	}
	return "", "", lookupErr
}

func validStructuredArtistImageURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.Hostname() != "" && u.User == nil && !netfetch.IsLikelyPlaceholderImageURL(u)
}

// The search response can omit infobox fields, which are available on the person detail endpoint.
func (m *Manager) bangumiPersonInfobox(ctx context.Context, base string, id int64) ([]bangumiPersonInfoboxEntry, error) {
	if err := m.waitBangumiRateLimit(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v0/persons/%d", base, id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var resp struct {
		Infobox []bangumiPersonInfoboxEntry `json:"infobox"`
	}
	if err := m.doSourceJSON(m.client, req, "bangumi", &resp); err != nil {
		return nil, err
	}
	return resp.Infobox, nil
}

func bangumiInfoboxMatchesName(infobox []bangumiPersonInfoboxEntry, names map[string]bool) bool {
	for _, entry := range infobox {
		switch entry.Key {
		case "别名", "简体中文名", "纯假名", "罗马字", "日文名", "英文名", "艺名", "本名":
			for _, alias := range flattenBangumiInfoboxValue(entry.Value) {
				if names[compactArtistName(alias)] {
					return true
				}
			}
		}
	}
	return false
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

// Only strip trailing annotations. Splitting on slashes or stripping internal
// parentheses could turn a group or multiple credits into an unrelated artist.
var artistNameAnnotation = regexp.MustCompile(`(?:\s*\([^()]*\))+$`)

func stripArtistNameAnnotation(name string) string {
	return strings.TrimSpace(artistNameAnnotation.ReplaceAllString(name, ""))
}

func buildArtistSearchNames(anchor storage.ArtistBiographyAnchor) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		key := normalize(name)
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, name)
		}
	}
	primaryNames := map[string]bool{}
	for _, name := range append([]string{anchor.Name}, anchor.Aliases...) {
		name = strings.TrimSpace(norm.NFKC.String(name))
		key := normalize(name)
		if key == "" || primaryNames[key] {
			continue
		}
		primaryNames[key] = true
		add(name)
		add(stripArtistNameAnnotation(name))
		if len(primaryNames) == 3 {
			break
		}
	}
	return out
}

var artistNameSeparators = strings.NewReplacer(" ", "", "・", "", "·", "")

func compactArtistName(name string) string {
	return artistNameSeparators.Replace(normalize(norm.NFKC.String(name)))
}

func normalizedAnchorNameSet(anchor storage.ArtistBiographyAnchor) map[string]bool {
	set := map[string]bool{}
	for _, name := range append([]string{anchor.Name}, anchor.Aliases...) {
		name = strings.TrimSpace(norm.NFKC.String(name))
		for _, variant := range []string{name, stripArtistNameAnnotation(name)} {
			if key := compactArtistName(variant); key != "" {
				set[key] = true
			}
		}
	}
	return set
}

func normalizedStringSet(items []string) map[string]bool {
	set := map[string]bool{}
	for _, item := range items {
		if k := normalize(norm.NFKC.String(item)); k != "" {
			set[k] = true
		}
	}
	return set
}
