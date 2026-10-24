// Package lanhttp provides bounded, direct HTTP for configured LAN services.
package lanhttp

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

var ErrMagnetOnly = errors.New("不支持磁力链接")
var ErrResponseTooLarge = errors.New("remote response exceeds limit")

func New(base string, timeout time.Duration) (*http.Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid service base URL")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &http.Client{Transport: t, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme == "magnet" {
			return ErrMagnetOnly
		}
		if len(via) >= 10 || req.URL.Host != u.Host || req.URL.Scheme != u.Scheme || req.URL.User != nil {
			return errors.New("redirect outside configured host")
		}
		return nil
	}}, nil
}

func Read(body io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(body, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, ErrResponseTooLarge
	}
	return b, nil
}
