package netfetch

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	maxPageMetaBytes = 256 << 10
	minAvatarPixels  = 100
	minAvatarAspect  = 0.35
	maxAvatarAspect  = 2.80
)

// PageImageCandidate represents a single image URL extracted from a public web
// page's HTML metadata (OpenGraph, Twitter Card, JSON-LD, or prominent <img>).
type PageImageCandidate struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
	Alt  string `json:"alt,omitempty"`
}

// PageImageMeta holds SSRF-safely fetched page title, description, and candidate
// image URLs extracted directly from HTML DOM metadata.
type PageImageMeta struct {
	PageURL     string               `json:"pageUrl"`
	FinalURL    string               `json:"finalUrl"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Images      []PageImageCandidate `json:"images"`
}

var (
	reHTMLTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reMetaTag     = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	reImgTag      = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	reJSONLDScript = regexp.MustCompile(`(?is)<script\b[^>]*type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	reHTMLAttr    = regexp.MustCompile(`(?is)([a-zA-Z0-9:_-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+))`)
	reWhitespace  = regexp.MustCompile(`\s+`)
)

// FetchPageImageMeta downloads up to 256 KiB of a public HTML page using the
// same SSRF-safe public transport and redirect checks as DownloadImage, then
// extracts og:image, twitter:image, JSON-LD image fields, and page title/summary.
func FetchPageImageMeta(ctx context.Context, client *http.Client, rawURL, ua string) (PageImageMeta, error) {
	var out PageImageMeta
	rawURL = strings.TrimSpace(rawURL)
	if err := ValidatePublicURL(ctx, rawURL); err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return out, errors.New("invalid page request")
	}
	if strings.TrimSpace(ua) != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")

	var c http.Client
	if client != nil {
		c = *client
	}
	if c.Transport == nil {
		c.Transport = defaultPublicTransport()
	}
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 6 {
			return errors.New("too many redirects")
		}
		return ValidatePublicURL(r.Context(), r.URL.String())
	}

	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("page request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("page returned HTTP %d", resp.StatusCode)
	}
	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") && !strings.Contains(ct, "text/plain") {
		return out, errors.New("page is not HTML")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageMetaBytes))
	if err != nil {
		return out, errors.New("read page failed")
	}
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return ParsePageImageMeta(rawURL, finalURL, string(body)), nil
}

// ParsePageImageMeta extracts page metadata and candidate image URLs from raw HTML.
func ParsePageImageMeta(pageURL, finalURL, rawHTML string) PageImageMeta {
	out := PageImageMeta{PageURL: pageURL, FinalURL: finalURL}
	base, _ := url.Parse(finalURL)
	if base == nil {
		base, _ = url.Parse(pageURL)
	}

	if m := reHTMLTitle.FindStringSubmatch(rawHTML); len(m) > 1 {
		out.Title = cleanMetaText(m[1], 200)
	}

	seen := map[string]bool{}
	addImage := func(rawImg, kind, alt string) {
		if len(out.Images) >= 8 {
			return
		}
		resolved := resolveCandidateImageURL(base, rawImg)
		if resolved == "" || seen[resolved] || isLikelyJunkImageURL(resolved, alt) {
			return
		}
		seen[resolved] = true
		out.Images = append(out.Images, PageImageCandidate{
			URL:  resolved,
			Kind: kind,
			Alt:  cleanMetaText(alt, 120),
		})
	}

	var ogTitle, ogDesc, metaDesc string
	for _, tag := range reMetaTag.FindAllString(rawHTML, 80) {
		attrs := parseHTMLAttrs(tag)
		content := strings.TrimSpace(attrs["content"])
		if content == "" {
			continue
		}
		prop := strings.ToLower(strings.TrimSpace(attrs["property"]))
		name := strings.ToLower(strings.TrimSpace(attrs["name"]))
		key := prop
		if key == "" {
			key = name
		}
		switch key {
		case "og:title":
			if ogTitle == "" {
				ogTitle = cleanMetaText(content, 200)
			}
		case "og:description":
			if ogDesc == "" {
				ogDesc = cleanMetaText(content, 300)
			}
		case "description":
			if metaDesc == "" {
				metaDesc = cleanMetaText(content, 300)
			}
		case "og:image", "og:image:secure_url":
			addImage(content, "og:image", "")
		case "twitter:image", "twitter:image:src":
			addImage(content, "twitter:image", "")
		}
	}
	if ogTitle != "" && len([]rune(ogTitle)) >= len([]rune(out.Title))/2 {
		out.Title = ogTitle
	}
	if ogDesc != "" {
		out.Description = ogDesc
	} else {
		out.Description = metaDesc
	}

	for _, match := range reJSONLDScript.FindAllStringSubmatch(rawHTML, 8) {
		if len(match) < 2 {
			continue
		}
		var doc any
		if json.Unmarshal([]byte(strings.TrimSpace(match[1])), &doc) != nil {
			continue
		}
		for _, imgURL := range extractJSONLDImages(doc) {
			addImage(imgURL, "jsonld", "")
		}
	}

	if len(out.Images) < 4 {
		for _, tag := range reImgTag.FindAllString(rawHTML, 40) {
			if len(out.Images) >= 6 {
				break
			}
			attrs := parseHTMLAttrs(tag)
			src := strings.TrimSpace(attrs["src"])
			if src == "" {
				src = strings.TrimSpace(attrs["data-src"])
			}
			alt := strings.TrimSpace(attrs["alt"])
			if src == "" {
				continue
			}
			addImage(src, "img", alt)
		}
	}
	return out
}

// InspectImageQuality checks pixel dimensions and aspect ratio of JPEG, PNG,
// GIF, or WebP bytes to reject tiny icons, 1x1 pixels, or extreme banners.
func InspectImageQuality(data []byte) (width, height int, ok bool, reason string) {
	if len(data) == 0 {
		return 0, 0, false, "empty image"
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		width, height = cfg.Width, cfg.Height
	} else if w, h, webpOK := decodeWebPDimensions(data); webpOK {
		width, height = w, h
	} else {
		return 0, 0, false, "unrecognized image dimensions"
	}
	if width < minAvatarPixels || height < minAvatarPixels {
		return width, height, false, fmt.Sprintf("image too small (%dx%d)", width, height)
	}
	aspect := float64(width) / float64(height)
	if aspect < minAvatarAspect || aspect > maxAvatarAspect {
		return width, height, false, fmt.Sprintf("extreme aspect ratio (%dx%d)", width, height)
	}
	return width, height, true, ""
}

// decodeWebPDimensions parses VP8, VP8L, and VP8X WebP headers without external deps.
func decodeWebPDimensions(data []byte) (int, int, bool) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	chunk := string(data[12:16])
	switch chunk {
	case "VP8X":
		if len(data) < 30 {
			return 0, 0, false
		}
		w := 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		h := 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
		return w, h, w > 0 && h > 0
	case "VP8 ":
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		w := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
		return w, h, w > 0 && h > 0
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, false
		}
		b0, b1, b2, b3 := uint32(data[21]), uint32(data[22]), uint32(data[23]), uint32(data[24])
		bits := b0 | (b1 << 8) | (b2 << 16) | (b3 << 24)
		w := int((bits & 0x3fff) + 1)
		h := int(((bits >> 14) & 0x3fff) + 1)
		return w, h, w > 0 && h > 0
	default:
		return 0, 0, false
	}
}

func parseHTMLAttrs(tag string) map[string]string {
	out := map[string]string{}
	for _, m := range reHTMLAttr.FindAllStringSubmatch(tag, -1) {
		key := strings.ToLower(strings.TrimSpace(m[1]))
		val := m[2]
		if val == "" {
			val = m[3]
		}
		if val == "" {
			val = m[4]
		}
		out[key] = html.UnescapeString(strings.TrimSpace(val))
	}
	return out
}

func cleanMetaText(raw string, maxRunes int) string {
	s := strings.TrimSpace(reWhitespace.ReplaceAllString(html.UnescapeString(raw), " "))
	r := []rune(s)
	if len(r) > maxRunes {
		return strings.TrimSpace(string(r[:maxRunes]))
	}
	return s
}

func resolveCandidateImageURL(base *url.URL, raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" || strings.HasPrefix(strings.ToLower(raw), "data:") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.String()
}

// IsLikelyPlaceholderImageURL checks recognizable icon and placeholder names in
// an image URL's path. Image dimensions are checked separately after download.
func IsLikelyPlaceholderImageURL(u *url.URL) bool {
	return isLikelyJunkImageURL(u.Path, "")
}

func isLikelyJunkImageURL(rawURL, alt string) bool {
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, ".svg") || strings.Contains(lower, ".ico") {
		return true
	}
	for _, bad := range []string{
		"favicon", "apple-touch-icon", "sprite", "spacer", "1x1", "pixel",
		"placeholder", "default_avatar", "no_image", "noimage", "no_icon", "blank.",
		"2a96cbd8b46e442fc41c2b86b821562f",
	} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	lowerAlt := strings.ToLower(alt)
	if strings.Contains(lowerAlt, "logo") || strings.Contains(lowerAlt, "icon") {
		return true
	}
	return false
}

func extractJSONLDImages(v any) []string {
	var out []string
	var walk func(node any)
	walk = func(node any) {
		switch x := node.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if img, ok := x["image"]; ok {
				switch iv := img.(type) {
				case string:
					if strings.TrimSpace(iv) != "" {
						out = append(out, strings.TrimSpace(iv))
					}
				case []any:
					for _, elem := range iv {
						if s, ok := elem.(string); ok && strings.TrimSpace(s) != "" {
							out = append(out, strings.TrimSpace(s))
						} else if m, ok := elem.(map[string]any); ok {
							if u, ok := m["url"].(string); ok && strings.TrimSpace(u) != "" {
								out = append(out, strings.TrimSpace(u))
							}
						}
					}
				case map[string]any:
					if u, ok := iv["url"].(string); ok && strings.TrimSpace(u) != "" {
						out = append(out, strings.TrimSpace(u))
					}
				}
			}
			if graph, ok := x["@graph"]; ok {
				walk(graph)
			}
		}
	}
	walk(v)
	return out
}
