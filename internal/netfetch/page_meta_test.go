package netfetch

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestParsePageImageMeta(t *testing.T) {
	rawHTML := `<!doctype html>
<html>
<head>
  <title>Test Artist Official</title>
  <meta property="og:title" content="Test Artist Official Profile">
  <meta property="og:description" content="Japanese anisong vocalist and composer.">
  <meta property="og:image" content="/assets/portrait.jpg">
  <meta name="twitter:image" content="https://cdn.example.com/artist/logo-favicon.png">
  <script type="application/ld+json">
  {"@type":"MusicGroup","name":"Test Artist","image":"https://cdn.example.com/artist/main.webp"}
  </script>
</head>
<body>
  <img src="/assets/live-photo.jpg" alt="Test Artist live portrait">
  <img src="/assets/site-logo.png" alt="Site Logo">
</body>
</html>`

	meta := ParsePageImageMeta("https://artist.example.com/profile", "https://artist.example.com/profile", rawHTML)
	if meta.Title != "Test Artist Official Profile" {
		t.Fatalf("unexpected title: %q", meta.Title)
	}
	if meta.Description != "Japanese anisong vocalist and composer." {
		t.Fatalf("unexpected description: %q", meta.Description)
	}
	if len(meta.Images) != 3 {
		t.Fatalf("expected 3 non-junk images, got %d: %+v", len(meta.Images), meta.Images)
	}
	if meta.Images[0].URL != "https://artist.example.com/assets/portrait.jpg" || meta.Images[0].Kind != "og:image" {
		t.Fatalf("unexpected first image: %+v", meta.Images[0])
	}
	if meta.Images[1].URL != "https://cdn.example.com/artist/main.webp" || meta.Images[1].Kind != "jsonld" {
		t.Fatalf("unexpected second image: %+v", meta.Images[1])
	}
	if meta.Images[2].URL != "https://artist.example.com/assets/live-photo.jpg" || meta.Images[2].Kind != "img" {
		t.Fatalf("unexpected third image: %+v", meta.Images[2])
	}
}

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 120, G: 80, B: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInspectImageQuality(t *testing.T) {
	if w, h, ok, reason := InspectImageQuality(makeTestPNG(t, 400, 400)); !ok || w != 400 || h != 400 {
		t.Fatalf("expected 400x400 valid, got ok=%v %dx%d reason=%s", ok, w, h, reason)
	}
	if _, _, ok, _ := InspectImageQuality(makeTestPNG(t, 64, 64)); ok {
		t.Fatal("expected 64x64 icon to be rejected")
	}
	if _, _, ok, _ := InspectImageQuality(makeTestPNG(t, 900, 200)); ok {
		t.Fatal("expected 900x200 wide banner to be rejected")
	}
}
