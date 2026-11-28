package netfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

const (
	maxPageTextBytes  = 2 << 20
	minPageTextRunes  = 150
	maxPageTextRunes  = 60000
	defaultPageTextUA = "032music-server/1.0 (+https://github.com/lux032/032music-server)"
)

var (
	reHTMLComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	reStripHead     = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head>`)
	reStripScript   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	reStripStyle    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	reStripNoscript = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`)
	reStripSVG      = regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg>`)
	reStripTemplate = regexp.MustCompile(`(?is)<template\b[^>]*>.*?</template>`)
	reStripIframe   = regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</iframe>`)
	reBlockBreak    = regexp.MustCompile(`(?i)</?(?:p|div|br|li|tr|td|th|h[1-6]|section|article|header|footer|main|aside|nav|blockquote|pre|dd|dt|hr)\b[^>]*>`)
	reAnyHTMLTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reMetaCharset   = regexp.MustCompile(`(?i)<meta\b[^>]*charset\s*=\s*["']?\s*([a-zA-Z0-9._-]+)`)
	reInlineSpaces  = regexp.MustCompile(`[ \t\f\r]+`)
)

// FetchPageText downloads up to 2 MiB of a public web page using the SSRF-safe
// public transport with per-hop redirect validation, decodes its charset, strips
// non-content HTML markup, and rejects anti-bot/JS-empty shell responses.
// Destination-policy rejections preserve ErrBlockedDestination so callers never
// forward blocked private/internal URLs to external extractors.
func FetchPageText(ctx context.Context, client *http.Client, rawURL, ua string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := ValidatePublicURL(ctx, rawURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errors.New("invalid page request")
	}
	if strings.TrimSpace(ua) == "" {
		ua = defaultPageTextUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.1")

	var c http.Client
	if client != nil {
		c = *client
	}
	if c.Transport == nil {
		c.Transport = defaultPublicTransport()
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Second
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
			return "", ctx.Err()
		}
		if errors.Is(err, ErrBlockedDestination) {
			return "", err
		}
		return "", errors.New("page request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("page returned HTTP %d", resp.StatusCode)
	}

	rawCT := strings.TrimSpace(resp.Header.Get("Content-Type"))
	var mediaType string
	var ctParams map[string]string
	if rawCT != "" {
		mt, params, parseErr := mime.ParseMediaType(rawCT)
		if parseErr != nil {
			return "", errors.New("page has invalid content type")
		}
		mediaType = strings.ToLower(strings.TrimSpace(mt))
		ctParams = params
		if !isSupportedPageMediaType(mediaType) {
			return "", errors.New("page is not text or HTML")
		}
	}

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxPageTextBytes+1))
	if err != nil {
		return "", errors.New("read page failed")
	}
	if len(rawBody) > maxPageTextBytes {
		return "", errors.New("page exceeds 2 MiB limit")
	}
	if len(rawBody) == 0 {
		return "", errors.New("page content too short or empty")
	}

	if mediaType == "" {
		sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(rawBody))
		mediaType = strings.ToLower(strings.TrimSpace(sniffed))
		if !isSupportedPageMediaType(mediaType) {
			return "", errors.New("page is not text or HTML")
		}
	}

	decoded, err := decodePageBytes(rawBody, ctParams["charset"], mediaType != "text/plain")
	if err != nil {
		return "", err
	}
	if mediaType == "text/plain" {
		text := normalizeExtractedText(decoded)
		return validateExtractedPageText("", text)
	}
	return ExtractPageText(decoded)
}

func isSupportedPageMediaType(mediaType string) bool {
	switch mediaType {
	case "text/html", "application/xhtml+xml", "text/plain", "text/xml", "application/xml":
		return true
	default:
		return false
	}
}

// ExtractPageText converts raw HTML into cleaned plain text while removing
// head/scripts/styles/noscript/svg blocks, preserving inline text boundaries
// (no spurious spaces inserted for inline tags like <span>/<b>), and applying
// the minimum-length and anti-bot checks strictly to visible body text.
func ExtractPageText(rawHTML string) (string, error) {
	var title string
	if m := reHTMLTitle.FindStringSubmatch(rawHTML); len(m) > 1 {
		title = cleanMetaText(m[1], 300)
	}
	var description string
	for _, tag := range reMetaTag.FindAllString(rawHTML, 80) {
		attrs := parseHTMLAttrs(tag)
		content := strings.TrimSpace(attrs["content"])
		if content == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(attrs["property"]))
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(attrs["name"]))
		}
		if key == "og:description" {
			description = cleanMetaText(content, 500)
			break
		}
		if key == "description" && description == "" {
			description = cleanMetaText(content, 500)
		}
	}

	cleaned := reHTMLComment.ReplaceAllString(rawHTML, "")
	cleaned = reStripHead.ReplaceAllString(cleaned, "")
	cleaned = reStripScript.ReplaceAllString(cleaned, "")
	cleaned = reStripStyle.ReplaceAllString(cleaned, "")
	cleaned = reStripNoscript.ReplaceAllString(cleaned, "")
	cleaned = reStripSVG.ReplaceAllString(cleaned, "")
	cleaned = reStripTemplate.ReplaceAllString(cleaned, "")
	cleaned = reStripIframe.ReplaceAllString(cleaned, "")
	cleaned = reBlockBreak.ReplaceAllString(cleaned, "\n")
	// Remove inline markup without injecting whitespace so verbatim names/quotes
	// split across <span>/<b>/<ruby> remain intact.
	cleaned = reAnyHTMLTag.ReplaceAllString(cleaned, "")
	cleaned = html.UnescapeString(cleaned)
	body := normalizeExtractedText(cleaned)

	// Validate visible body content BEFORE prepending <title>/<meta> so a
	// metadata-heavy empty SPA shell cannot bypass the empty-shell threshold.
	if _, err := validateExtractedPageText(title, body); err != nil {
		return "", err
	}

	var parts []string
	if title != "" && !strings.Contains(body, title) {
		parts = append(parts, title)
	}
	if description != "" && !strings.Contains(body, description) {
		parts = append(parts, description)
	}
	parts = append(parts, body)
	full := strings.Join(parts, "\n")
	if runes := []rune(full); len(runes) > maxPageTextRunes {
		full = strings.TrimSpace(string(runes[:maxPageTextRunes]))
	}
	return full, nil
}

func validateExtractedPageText(title, text string) (string, error) {
	text = strings.TrimSpace(text)
	if isAntiBotShell(title, text) {
		return "", errors.New("page blocked by anti-bot challenge")
	}
	if utf8.RuneCountInString(text) < minPageTextRunes {
		return "", errors.New("page content too short or empty")
	}
	if runes := []rune(text); len(runes) > maxPageTextRunes {
		text = strings.TrimSpace(string(runes[:maxPageTextRunes]))
	}
	return text, nil
}

func isAntiBotShell(title, text string) bool {
	if utf8.RuneCountInString(text) > 1500 {
		return false
	}
	combined := strings.ToLower(title + "\n" + text)
	for _, marker := range []string{
		"just a moment...",
		"attention required! | cloudflare",
		"checking your browser before accessing",
		"enable javascript and cookies to continue",
		"verify you are human",
		"cf-browser-verification",
		"please enable javascript to view",
		"access to this page has been denied",
	} {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	return false
}

func normalizeExtractedText(raw string) string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(reInlineSpaces.ReplaceAllString(line, " "))
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func decodePageBytes(raw []byte, headerCharset string, inspectMeta bool) (string, error) {
	charset := strings.TrimSpace(headerCharset)
	if charset == "" {
		switch {
		case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
			charset = "utf-8"
		case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
			charset = "utf-16le"
		case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
			charset = "utf-16be"
		}
	}
	if charset == "" && inspectMeta {
		head := raw
		if len(head) > 4096 {
			head = head[:4096]
		}
		if m := reMetaCharset.FindSubmatch(head); len(m) > 1 {
			charset = strings.TrimSpace(string(m[1]))
		}
	}
	lower := strings.ToLower(charset)
	if lower == "" || lower == "utf-8" || lower == "utf8" {
		trimmed := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
		if !utf8.Valid(trimmed) {
			return "", errors.New("page is not valid UTF-8")
		}
		return string(trimmed), nil
	}
	enc, err := htmlindex.Get(lower)
	if err != nil {
		return "", fmt.Errorf("unsupported page charset %q", charset)
	}
	decoded, err := enc.NewDecoder().Bytes(raw)
	if err != nil {
		return "", errors.New("page charset decode failed")
	}
	return string(bytes.TrimPrefix(decoded, []byte{0xEF, 0xBB, 0xBF})), nil
}
