package lyricsonline

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/lyrics"
)

func TestScore(t *testing.T) {
	q := Query{Title: "紅蓮華", Artist: "LiSA", DurationMs: 239000}
	cases := []struct {
		name string
		c    Candidate
		ok   bool
	}{
		{"exact", Candidate{Title: "紅蓮華", Artist: "LiSA", DurationMs: 239500}, true},
		{"version note", Candidate{Title: "紅蓮華 (TV Size)", Artist: "LiSA", DurationMs: 240000}, true},
		{"fullwidth artist with feat", Candidate{Title: "紅蓮華", Artist: "ＬｉＳＡ feat. Someone", DurationMs: 0}, true},
		{"artist romanised, same length", Candidate{Title: "紅蓮華", Artist: "リサ", DurationMs: 239800}, true},
		{"artist romanised, unknown length", Candidate{Title: "紅蓮華", Artist: "リサ"}, false},
		{"other song", Candidate{Title: "炎", Artist: "LiSA", DurationMs: 239000}, false},
		{"length far off", Candidate{Title: "紅蓮華", Artist: "LiSA", DurationMs: 89000}, false},
	}
	for _, c := range cases {
		if _, ok := Score(q, c.c); ok != c.ok {
			t.Errorf("%s: ok = %v", c.name, ok)
		}
	}
	exact, _ := Score(q, cases[0].c)
	noted, _ := Score(q, cases[1].c)
	if !(exact < noted) {
		t.Errorf("exact %.3f should beat noted %.3f", exact, noted)
	}
}

func TestRankPrefersSyncedAmongClose(t *testing.T) {
	yes, no := true, false
	q := Query{Title: "Song", Artist: "Band"}
	_, accepted := Rank(q, []Candidate{
		{ID: "plain", Title: "Song", Artist: "Band", Synced: &no},
		{ID: "synced", Title: "Song (Remaster)", Artist: "Band", Synced: &yes},
		{ID: "bad", Title: "Other", Artist: "X"},
	})
	if len(accepted) != 2 || accepted[0].ID != "synced" {
		t.Fatalf("accepted = %+v", accepted)
	}
}

func neteaseServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") == "" || !strings.Contains(r.Header.Get("User-Agent"), "032music") {
			t.Errorf("missing headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/api/search/get":
			if r.URL.Query().Get("s") != "Song Band" {
				t.Errorf("search term = %q", r.URL.Query().Get("s"))
			}
			_, _ = w.Write([]byte(`{"code":200,"result":{"songs":[
				{"id":11,"name":"Song","duration":200000,"artists":[{"name":"Band"},{"name":"Guest"}],"album":{"name":"A"}},
				{"id":12,"name":"Song","duration":200500,"artists":[{"name":"Band"}],"album":{"name":"B"}}]}}`))
		case "/api/song/lyric":
			switch r.URL.Query().Get("id") {
			case "11":
				_, _ = w.Write([]byte(`{"code":200,"nolyric":true}`))
			case "12":
				_, _ = w.Write([]byte(`{"code":200,"lrc":{"lyric":"[00:01.00]one\n[00:02.00]two"},"tlyric":{"lyric":"[00:01.00]一\n[00:02.00]二"}}`))
			case "13":
				_, _ = w.Write([]byte(`{"code":200,"lrc":{"lyric":"[00:00.00]纯音乐，请欣赏"}}`))
			}
		}
	}))
}

func TestNetEaseProvider(t *testing.T) {
	srv := neteaseServer(t)
	defer srv.Close()
	n := &NetEase{HTTP: srv.Client(), BaseURL: srv.URL}
	cands, err := n.Search(context.Background(), Query{Title: "Song", Artist: "Band"})
	if err != nil || len(cands) != 2 || cands[0].Artist != "Band, Guest" || cands[0].DurationMs != 200000 || cands[0].Synced != nil {
		t.Fatalf("search = %+v %v", cands, err)
	}
	if _, err := n.Get(context.Background(), "11"); !errors.Is(err, ErrNoLyrics) {
		t.Fatalf("nolyric = %v", err)
	}
	if _, err := n.Get(context.Background(), "13"); !errors.Is(err, ErrNoLyrics) {
		t.Fatalf("pure music placeholder = %v", err)
	}
	if _, err := n.Get(context.Background(), "../x"); err == nil {
		t.Fatal("invalid id accepted")
	}

	// Best skips the first match without lyrics and keeps the translation.
	c := NewWithProviders(n)
	l, cand, err := c.Best(context.Background(), Query{Title: "Song", Artist: "Band", DurationMs: 200000})
	if err != nil || l.ID != "12" || cand.ID != "12" || !strings.Contains(l.TranslationLRC, "一") {
		t.Fatalf("best = %+v %+v %v", l, cand, err)
	}
}

func TestLRCLIBProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/search" && r.URL.Query().Get("artist_name") == "Band":
			_, _ = w.Write([]byte(`[
				{"id":1,"trackName":"Song","artistName":"Band","albumName":"A","duration":200.4,"instrumental":false,"plainLyrics":"one\ntwo","syncedLyrics":"[00:01.00]one\n[00:02.00]two"},
				{"id":2,"trackName":"Song","artistName":"Band","duration":200,"instrumental":true,"plainLyrics":null,"syncedLyrics":null},
				{"id":3,"trackName":"Song","artistName":"Band","duration":200,"instrumental":false,"plainLyrics":"plain only","syncedLyrics":null}]`))
		case r.URL.Path == "/api/search":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/get/1":
			_, _ = w.Write([]byte(`{"id":1,"trackName":"Song","artistName":"Band","duration":200,"syncedLyrics":"[00:01.00]one"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	l := &LRCLIB{HTTP: srv.Client(), BaseURL: srv.URL}
	cands, err := l.Search(context.Background(), Query{Title: "Song", Artist: "Band"})
	if err != nil || len(cands) != 2 || !*cands[0].Synced || *cands[1].Synced || cands[0].DurationMs != 200400 {
		t.Fatalf("search = %+v %v", cands, err)
	}
	// Best uses the lyrics from the search response without another request; synced first.
	best, _, err := NewWithProviders(l).Best(context.Background(), Query{Title: "Song", Artist: "Band", DurationMs: 200000})
	if err != nil || best.ID != "1" || !strings.HasPrefix(best.LRC, "[00:01.00]") {
		t.Fatalf("best = %+v %v", best, err)
	}
	if got, err := l.Get(context.Background(), "1"); err != nil || got.Title != "Song" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := l.Get(context.Background(), "404"); !errors.Is(err, ErrNoLyrics) {
		t.Fatalf("missing = %v", err)
	}
	if _, _, err := NewWithProviders(l).Best(context.Background(), Query{Title: "Nothing", Artist: "Nobody"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no match = %v", err)
	}
}

type brokenProvider struct{}

func (brokenProvider) ID() string { return "broken" }
func (brokenProvider) Search(context.Context, Query) ([]Candidate, error) {
	return nil, errors.New("timeout")
}
func (brokenProvider) Get(context.Context, string) (Lyrics, error) {
	return Lyrics{}, errors.New("timeout")
}

func TestSearchToleratesOneFailingSource(t *testing.T) {
	srv := neteaseServer(t)
	defer srv.Close()
	c := NewWithProviders(brokenProvider{}, &NetEase{HTTP: srv.Client(), BaseURL: srv.URL})
	if res, err := c.Search(context.Background(), Query{Title: "Song", Artist: "Band"}); err != nil || len(res) != 2 {
		t.Fatalf("partial failure = %v %v", res, err)
	}
	_, _, err := NewWithProviders(brokenProvider{}).Best(context.Background(), Query{Title: "Song"})
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("all sources failing must be a transient error, got %v", err)
	}
}

func TestAlignTranslation(t *testing.T) {
	t.Run("basic and blank original lines", func(t *testing.T) {
		orig := lyrics.ParseLRC("[00:01.00]one\n[00:02.00]\n[00:03.00]three\n[00:04.00]four")
		out, ok := AlignTranslation(orig.Lines, "[00:01.05]一\n[00:03.00]三")
		if !ok || strings.Join(out, "|") != "一||三|" {
			t.Fatalf("aligned = %q %v", out, ok)
		}
		if _, ok := AlignTranslation(orig.Lines, "no timestamps"); ok {
			t.Fatal("unsynced translation must not align")
		}
		if _, ok := AlignTranslation(orig.Lines, "[00:01.00]   \n[00:03.00]\t"); ok {
			t.Fatal("whitespace-only translation must not align")
		}
	})

	t.Run("1ms clear-line before translated lyric", func(t *testing.T) {
		orig := lyrics.ParseLRC(strings.Join([]string{
			"[00:34.110]第六感的な何か？未だ識らないことばかり",
			"[00:38.523]",
			"[00:38.524]「ねえ、気付いちゃった？気付いちゃったんでしょう？」",
			"[00:43.172]ああ、分かっています 呪いの不可思議！！！",
			"[00:47.805]",
			"[00:47.806]超常現象ランデヴ わたし大言壮語のアイラヴュ",
			"[00:52.484]何回も思い出している もう居ないのにな！",
		}, "\n"))
		tlyric := strings.Join([]string{
			"[by:字幕组]",
			"[00:34.110]像是第六感之类的？怎么都是些没见过的",
			"[00:38.523]",
			"[00:38.524]“呐 你发现了吗？你已经发现了吧？”",
			"[00:43.172]啊啊 我懂啊 诅咒就是这么不可思议！",
			"[00:47.805]",
			"[00:47.806]和灵异相约 我的那句我爱你 听来简直大言不惭",
			"[00:52.484]无数次回想起来 可早就已经不在了啊",
		}, "\n")
		out, ok := AlignTranslation(orig.Lines, tlyric)
		want := strings.Join([]string{
			"像是第六感之类的？怎么都是些没见过的",
			"",
			"“呐 你发现了吗？你已经发现了吧？”",
			"啊啊 我懂啊 诅咒就是这么不可思议！",
			"",
			"和灵异相约 我的那句我爱你 听来简直大言不惭",
			"无数次回想起来 可早就已经不在了啊",
		}, "|")
		if !ok || strings.Join(out, "|") != want {
			t.Fatalf("aligned = %q (ok=%v), want %q", strings.Join(out, "|"), ok, want)
		}
	})

	t.Run("minor drift within 250ms aligns and beyond does not", func(t *testing.T) {
		orig := lyrics.ParseLRC("[00:10.000]first\n[00:20.000]second\n[00:30.000]third")
		out, ok := AlignTranslation(orig.Lines, "[00:10.200]一\n[00:19.800]二\n[00:30.300]三")
		if !ok || strings.Join(out, "|") != "一|二|" {
			t.Fatalf("aligned = %q %v", out, ok)
		}
	})

	t.Run("untranslated line does not steal neighbor translation", func(t *testing.T) {
		orig := lyrics.ParseLRC("[00:01.000]untranslated\n[00:01.150]translated")
		out, ok := AlignTranslation(orig.Lines, "[00:01.150]译文")
		if !ok || strings.Join(out, "|") != "|译文" {
			t.Fatalf("aligned = %q %v", out, ok)
		}
	})

	t.Run("trailing leftover translation does not leak to later untranslated line", func(t *testing.T) {
		orig := lyrics.ParseLRC("[00:01.000]first\n[00:01.200]untranslated")
		out, ok := AlignTranslation(orig.Lines, "[00:01.000]一\n[00:01.040]残留")
		if !ok || strings.Join(out, "|") != "一|" {
			t.Fatalf("aligned = %q %v", out, ok)
		}
	})

	t.Run("exact match beats earlier in-window candidate", func(t *testing.T) {
		orig := lyrics.ParseLRC("[00:10.000]target")
		out, ok := AlignTranslation(orig.Lines, "[00:09.950]早一点\n[00:10.000]精确")
		if !ok || strings.Join(out, "|") != "精确" {
			t.Fatalf("aligned = %q %v", out, ok)
		}
	})

	t.Run("rapid consecutive lines within tolerance match individually", func(t *testing.T) {
		orig := lyrics.ParseLRC("[00:10.000]first\n[00:10.050]second")
		out, ok := AlignTranslation(orig.Lines, "[00:10.000]一\n[00:10.050]二")
		if !ok || strings.Join(out, "|") != "一|二" {
			t.Fatalf("aligned = %q %v", out, ok)
		}
	})

	t.Run("duplicate timestamps align one-to-one without duplicating", func(t *testing.T) {
		orig := lyrics.ParseLRC("[00:10.000]A\n[00:10.000]B\n[00:20.000]C\n[00:20.000]D")
		out, ok := AlignTranslation(orig.Lines, "[00:10.000]译A\n[00:10.000]译B\n[00:20.000]译C")
		if !ok || strings.Join(out, "|") != "译A|译B|译C|" {
			t.Fatalf("aligned = %q %v", out, ok)
		}
	})

	for code, want := range map[string]bool{"zh-Hans": true, "zh": true, "zh-CN": true, "zh-Hant": false, "ja": false} {
		if IsSimplifiedChinese(code) != want {
			t.Errorf("IsSimplifiedChinese(%q) != %v", code, want)
		}
	}
}
