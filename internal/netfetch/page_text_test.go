package netfetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
)

func TestExtractPageTextCleansMarkupAndPreservesInlineQuotes(t *testing.T) {
	rawHTML := `<!doctype html>
<html>
<head>
  <title>YOASOBI Official Profile</title>
  <meta property="og:description" content="Japanese musical duo consisting of Ayase and ikura.">
  <style>.hidden { display: none; }</style>
  <script>window.__SECRET__ = "do-not-include-script";</script>
</head>
<body>
  <!-- HTML comment ignored -->
  <noscript>Please turn on JS</noscript>
  <svg><text>SVG ICON TEXT</text></svg>
  <main>
    <h1>YOASOBI</h1>
    <p><span>米津</span><b>玄師</b> and YOASOBI are prominent Japanese music acts; YOASOBI was formed in 2019 by Sony Music Entertainment Japan, composed of Vocaloid producer Ayase and singer-songwriter ikura (Lilas Ikuta).</p>
    <p>Their debut single &quot;Yoru ni Kakeru&quot; topped the Billboard Japan Hot 100 and launched their signature project turning novels into music.</p>
  </main>
</body>
</html>`

	text, err := ExtractPageText(rawHTML)
	if err != nil {
		t.Fatalf("ExtractPageText error: %v", err)
	}
	for _, want := range []string{
		"YOASOBI Official Profile",
		"米津玄師",
		"Ayase and ikura",
		`debut single "Yoru ni Kakeru"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("extracted text missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"do-not-include-script", ".hidden", "SVG ICON TEXT", "HTML comment ignored", "Please turn on JS"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("extracted text leaked stripped element %q:\n%s", forbidden, text)
		}
	}
}

func TestExtractPageTextRejectsAntiBotAndMetadataOnlySPAShell(t *testing.T) {
	cfHTML := `<html><head><title>Just a moment...</title></head><body><div>Checking your browser before accessing example.com. Enable JavaScript and cookies to continue. ` + strings.Repeat("cloudflare ", 20) + `</div></body></html>`
	if _, err := ExtractPageText(cfHTML); err == nil {
		t.Fatal("expected Cloudflare anti-bot shell to be rejected")
	}

	// Metadata-heavy SPA shell: long og:description (>200 chars), empty body -> MUST reject.
	metaHeavySPA := `<html><head><title>Artist Page</title><meta property="og:description" content="` +
		strings.Repeat("Long SEO metadata description text that must not count as body content. ", 5) +
		`"></head><body><div id="root"></div><script>boot()</script></body></html>`
	if _, err := ExtractPageText(metaHeavySPA); err == nil {
		t.Fatal("expected metadata-heavy empty SPA shell to be rejected")
	}
}

func TestFetchPageTextPublicCharsetMediaTypeLimitsAndSSRF(t *testing.T) {
	// 1. Reject private IP directly with ErrBlockedDestination.
	if _, err := FetchPageText(context.Background(), nil, "http://127.0.0.1/secret", ""); !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("expected ErrBlockedDestination for loopback, got %v", err)
	}
	if _, err := FetchPageText(context.Background(), nil, "https://user:pass@93.184.216.34/page", ""); !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("expected ErrBlockedDestination for credential URL, got %v", err)
	}

	// 2. Reject redirect pointing to private IP and preserve ErrBlockedDestination.
	redirClient := &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": {"http://169.254.169.254/latest/meta-data/"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})}
	if _, err := FetchPageText(context.Background(), redirClient, "http://93.184.216.34/redirect", ""); !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("expected ErrBlockedDestination for private redirect, got %v", err)
	}

	// 3. Reject binary content when Content-Type header is missing (sniffing guard).
	binClient := &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00, 0xFF}, 200)...))),
			Request:    r,
		}, nil
	})}
	if _, err := FetchPageText(context.Background(), binClient, "http://93.184.216.34/bin", ""); err == nil {
		t.Fatal("expected sniffed binary body to be rejected")
	}

	// 4. Reject HTTP 403 anti-bot status (must NOT be ErrBlockedDestination so fallback can run).
	forbidClient := &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Body:       io.NopCloser(strings.NewReader("Forbidden")),
			Request:    r,
		}, nil
	})}
	if _, err := FetchPageText(context.Background(), forbidClient, "http://93.184.216.34/blocked", ""); err == nil || errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("expected non-policy HTTP 403 error, got %v", err)
	}

	// 5. Reject oversized > 2 MiB response body.
	bigClient := &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/html; charset=utf-8"}},
			Body:       io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("a"), maxPageTextBytes+10))),
			Request:    r,
		}, nil
	})}
	if _, err := FetchPageText(context.Background(), bigClient, "http://93.184.216.34/huge", ""); err == nil {
		t.Fatal("expected >2 MiB body to fail")
	}

	// 6. Decode Shift_JIS Japanese artist page cleanly.
	jaHTML := `<html><head><meta charset="Shift_JIS"><title>米津玄師 公式サイト</title></head><body><p>米津玄師（よねづ けんし）は日本のシンガーソングライター・音楽プロデューサー。代表作に「Lemon」「KICK BACK」「地球儀」などがあり、ボカロP「ハチ」名義でも活動し、国内外で高い評価を獲得している。公式サイトではディスコグラフィやライブ情報を公開中。さらに最新ツアー日程や映画主題歌の制作背景についても詳しく紹介している。</p></body></html>`
	sjisBytes, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(jaHTML))
	if err != nil {
		t.Fatal(err)
	}
	sjisClient := &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Body:       io.NopCloser(bytes.NewReader(sjisBytes)),
			Request:    r,
		}, nil
	})}
	got, err := FetchPageText(context.Background(), sjisClient, "http://93.184.216.34/jp", "test-ua")
	if err != nil {
		t.Fatalf("FetchPageText Shift_JIS error: %v", err)
	}
	if !strings.Contains(got, "米津玄師") || !strings.Contains(got, "シンガーソングライター") {
		t.Fatalf("decoded Shift_JIS text missing expected content: %q", got)
	}
}
