package lyricsonline

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrimaryArtist(t *testing.T) {
	for in, want := range map[string]string{
		"音乃瀬奏":                "音乃瀬奏",
		"音乃瀬奏, 一条莉々華":         "音乃瀬奏",
		"Band feat. Guest":    "Band",
		" / Solo":             "Solo",
		"":                    "",
		"Simon & Garfunkel":   "Simon",
		"YOASOBI、Ayase、ikura": "YOASOBI",
	} {
		if got := PrimaryArtist(in); got != want {
			t.Errorf("PrimaryArtist(%q) = %q, want %q", in, got, want)
		}
	}
}

// Searching NetEase with every artist often returns nothing: only the first
// artist is used, then the title alone.
func TestNetEaseSearchUsesPrimaryArtistThenTitle(t *testing.T) {
	var terms []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term := r.URL.Query().Get("s")
		terms = append(terms, term)
		if term == "Song" {
			_, _ = w.Write([]byte(`{"code":200,"result":{"songs":[{"id":5,"name":"Song","duration":200000,"artists":[{"name":"Other Name"}]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"result":{}}`))
	}))
	defer srv.Close()
	n := &NetEase{HTTP: srv.Client(), BaseURL: srv.URL}
	res, err := n.Search(context.Background(), Query{Title: "Song", Artist: "Band, Guest"})
	if err != nil || len(res) != 1 {
		t.Fatalf("search = %v %v", res, err)
	}
	if strings.Join(terms, "|") != "Song Band|Song" {
		t.Fatalf("terms = %q", terms)
	}
}

type emptyProvider struct{ id string }

func (p emptyProvider) ID() string                                       { return p.id }
func (emptyProvider) Search(context.Context, Query) ([]Candidate, error) { return nil, nil }
func (emptyProvider) Get(context.Context, string) (Lyrics, error)        { return Lyrics{}, ErrNoLyrics }

func TestLookupReportsAndFlagsIncompleteMisses(t *testing.T) {
	// One source failed and the other had nothing: not a reliable miss.
	_, _, report, err := NewWithProviders(brokenProvider{}, emptyProvider{SourceLRCLIB}).Lookup(context.Background(), Query{Title: "Song"})
	if !errors.Is(err, ErrIncomplete) || errors.Is(err, ErrNotFound) {
		t.Fatalf("partial failure = %v", err)
	}
	if len(report.Providers) != 2 || report.Providers[0].Err == nil || len(report.Failed()) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if s := report.Summary(); !strings.Contains(s, "broken=error(timeout)") || !strings.Contains(s, "lrclib=0") {
		t.Fatalf("summary = %q", s)
	}

	// Every source answered: a real miss.
	if _, _, _, err := NewWithProviders(emptyProvider{SourceNetEase}, emptyProvider{SourceLRCLIB}).Lookup(context.Background(), Query{Title: "Song"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clean miss = %v", err)
	}

	// Candidates carry Accepted, and downloads are recorded.
	srv := neteaseServer(t)
	defer srv.Close()
	l, _, report, err := NewWithProviders(&NetEase{HTTP: srv.Client(), BaseURL: srv.URL}).Lookup(context.Background(),
		Query{Title: "Song", Artist: "Band", DurationMs: 200000})
	if err != nil || l.ID != "12" {
		t.Fatalf("lookup = %+v %v", l, err)
	}
	if len(report.Candidates) != 2 || !report.Candidates[0].Accepted || len(report.Downloads) != 2 ||
		!strings.Contains(report.Downloads[0], "netease:11 -> ") || report.Downloads[1] != "netease:12 -> ok" {
		t.Fatalf("report = %+v", report)
	}
}
