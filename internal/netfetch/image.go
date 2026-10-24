package netfetch

import (
	"context"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/sourcegate"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Image struct {
	Data      []byte
	MIME, Ext string
}

var ErrNotFound = errors.New("image not found")

func rejectNonPublic(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	return rejectIP(host)
}

var nonPublicPrefixes = func() []netip.Prefix {
	out := []netip.Prefix{}
	for _, raw := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::/96", "::ffff:0:0/96", "64:ff9b::/96", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"} {
		out = append(out, netip.MustParsePrefix(raw))
	}
	return out
}()

func rejectIP(host string) error {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("invalid image address")
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return errors.New("image address is not public")
		}
	}
	return nil
}
func NewPublicClient(timeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	// Direct connections check the actual destination IP. An explicitly configured
	// proxy may itself be private; proxied requests rely on URL/DNS validation at
	// the request and every redirect, and on the administrator's trusted proxy.
	t.Proxy = http.ProxyFromEnvironment
	direct := (&net.Dialer{Timeout: 30 * time.Second, Control: rejectNonPublic}).DialContext
	proxy := (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	type proxyKey struct{}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if yes, _ := ctx.Value(proxyKey{}).(bool); yes {
			return proxy(ctx, network, address)
		}
		return direct(ctx, network, address)
	}
	return &http.Client{Timeout: timeout, Transport: &publicTransport{transport: t, proxyKey: proxyKey{}}}
}

// Reuse the default image connection pool across callers with nil Transport.
var defaultPublicTransport = sync.OnceValue(func() http.RoundTripper {
	return NewPublicClient(0).Transport
})

type publicTransport struct {
	transport *http.Transport
	proxyKey  any
}

func (t *publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p, e := t.transport.Proxy(r)
	if e != nil {
		return nil, e
	}
	if p != nil {
		r = r.Clone(context.WithValue(r.Context(), t.proxyKey, true))
	}
	return t.transport.RoundTrip(r)
}

// ImageRateLimitError preserves the HTTP response status for legacy callers.
type ImageRateLimitError struct {
	*sourcegate.CooldownError
	StatusCode int
}

func ValidatePublicURL(ctx context.Context, raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("invalid image URL")
	}
	if _, e = netip.ParseAddr(u.Hostname()); e == nil {
		return rejectIP(u.Hostname())
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if e != nil {
		return errors.New("resolve image host failed")
	}
	if len(ips) == 0 {
		return errors.New("image host has no address")
	}
	for _, ip := range ips {
		if e = rejectIP(ip.IP.String()); e != nil {
			return e
		}
	}
	return nil
}
func DownloadImage(ctx context.Context, client *http.Client, gate *sourcegate.Gate, source, raw, ua string) (Image, error) {
	var zero Image
	if e := ValidatePublicURL(ctx, raw); e != nil {
		return zero, e
	}
	if gate != nil {
		if e := gate.Wait(ctx, source); e != nil {
			return zero, e
		}
	}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return zero, errors.New("invalid image request")
	}
	req.Header.Set("User-Agent", ua)
	c := *client
	if c.Transport == nil {
		c.Transport = defaultPublicTransport()
	}
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return ValidatePublicURL(r.Context(), r.URL.String())
	}
	res, e := c.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, errors.New("image request failed")
	}
	defer res.Body.Close()
	if sourcegate.IsRateLimitResponse(source, res.StatusCode, res.Header) {
		d := sourcegate.ParseRetryAfter(res.Header.Get("Retry-After"), time.Now())
		if gate != nil {
			gate.Block(source, time.Now().Add(d))
		}
		return zero, &ImageRateLimitError{CooldownError: &sourcegate.CooldownError{Source: source, RetryAfter: d}, StatusCode: res.StatusCode}
	}
	if res.StatusCode == 404 {
		return zero, ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return zero, fmt.Errorf("image returned HTTP %d", res.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, (10<<20)+1))
	if e != nil {
		return zero, errors.New("read image failed")
	}
	if len(data) == 0 || len(data) > 10<<20 {
		return zero, errors.New("image is empty or exceeds 10 MiB")
	}
	mime := http.DetectContentType(data)
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}[mime]
	if ext == "" {
		return zero, errors.New("unsupported image type")
	}
	return Image{data, mime, ext}, nil
}
func WriteCacheFile(directory, name string, data []byte) (string, error) {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	cachePath := filepath.Join(directory, name)
	if _, statErr := os.Stat(cachePath); statErr == nil {
		return cachePath, nil
	}
	temporary, err := os.CreateTemp(directory, "download-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Chmod(0o640)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return cachePath, os.Rename(temporaryPath, cachePath)
}

func (e *ImageRateLimitError) Unwrap() error { return e.CooldownError }
