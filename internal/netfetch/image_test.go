package netfetch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestDialRejectsPrivate(t *testing.T) {
	for _, s := range []string{"127.0.0.1:80", "10.0.0.1:80", "169.254.1.1:80", "100.64.0.1:80", "[::ffff:8.8.8.8]:80", "[::]:80", "224.0.0.1:80"} {
		if e := rejectNonPublic("tcp", s, nil); e == nil {
			t.Errorf("allowed %s", s)
		}
	}
	c := NewPublicClient(time.Second)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://127.0.0.1:1", nil)
	if _, e := c.Do(req); e == nil {
		t.Fatal("dial allowed private")
	}
	if e := rejectNonPublic("tcp", "93.184.216.34:80", nil); e != nil {
		t.Fatal(e)
	}
}

type imageTransport func(*http.Request) (*http.Response, error)

func (f imageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDownloadPublicImageAndCache(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\nrest")
	client := &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("User-Agent") != "test-UA" {
			t.Error("UA")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	img, e := DownloadImage(context.Background(), client, nil, "coverartarchive", "http://93.184.216.34/front", "test-UA")
	if e != nil || img.Ext != ".png" {
		t.Fatal(img, e)
	}
	path, e := WriteCacheFile(t.TempDir(), "cover.png", img.Data)
	if e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = ValidatePublicURL(ctx, "https://unresolvable.invalid/image"); e == nil {
		t.Fatal("cancelled DNS")
	}
}

func TestImageReservedPrefixes(t *testing.T) {
	for _, ip := range []string{"0.1.2.3", "192.0.0.8", "198.18.1.1", "240.0.0.1", "255.255.255.255", "64:ff9b::a00:1", "2002:a00:1::", "::a00:1"} {
		if rejectIP(ip) == nil {
			t.Error("allowed", ip)
		}
	}
}

func TestImageConfiguredProxyMayBePrivate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("\x89PNG\r\n\x1a\nrest")) }))
	defer server.Close()
	client := NewPublicClient(time.Second)
	transport := client.Transport.(*publicTransport)
	proxyURL, _ := url.Parse(server.URL)
	transport.transport.Proxy = http.ProxyURL(proxyURL)
	image, err := DownloadImage(context.Background(), client, nil, "coverartarchive", "http://93.184.216.34/front", "test")
	if err != nil || image.Ext != ".png" {
		t.Fatal(image, err)
	}
}

func TestImageDefaultPublicTransportReused(t *testing.T) {
	first := defaultPublicTransport()
	if first == nil || first != defaultPublicTransport() {
		t.Fatal("default public transport is not reused")
	}
	if _, ok := first.(*publicTransport); !ok {
		t.Fatal("default transport lost public/proxy handling")
	}
}
