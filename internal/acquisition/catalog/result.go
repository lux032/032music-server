package catalog

import (
	"errors"
	"net/url"
	"strings"
)

type Result struct {
	Title, GUID, Category, PublishedAt, DownloadRef string
	Size                                            int64
	Seeders, Peers, Grabs                           *int64
	DownloadFactor                                  *float64
	MagnetOnly                                      bool
}

// DownloadRef retains only a Jackett /dl/ path and noncredential query.
func DownloadRef(raw string, basePath ...string) (string, bool, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return "", false, errors.New("invalid download link")
	}
	if u.Scheme == "magnet" {
		return "", true, nil
	}
	for _, base := range basePath {
		prefix := strings.TrimRight(base, "/")
		if prefix != "" && strings.HasPrefix(u.Path, prefix+"/dl/") {
			u.Path = strings.TrimPrefix(u.Path, prefix)
		}
	}
	if (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") || !strings.HasPrefix(u.Path, "/dl/") || strings.Contains(u.Path, "..") {
		return "", false, errors.New("download link is not a /dl/ path")
	}
	q := u.Query()
	for k := range q {
		switch strings.ToLower(k) {
		case "apikey", "jackett_apikey", "passkey":
			q.Del(k)
		}
	}
	return (&url.URL{Path: u.Path, RawQuery: q.Encode()}).String(), false, nil
}
