package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/storage"
)

type namedLyricsSource struct {
	id   string
	fail bool
}

func (s namedLyricsSource) ID() string { return s.id }
func (s namedLyricsSource) Search(context.Context, lyricsonline.Query) ([]lyricsonline.Candidate, error) {
	if s.fail {
		return nil, errors.New("HTTP 503")
	}
	return nil, nil
}
func (namedLyricsSource) Get(context.Context, string) (lyricsonline.Lyrics, error) {
	return lyricsonline.Lyrics{}, lyricsonline.ErrNoLyrics
}

// NetEase failing while LRCLIB has nothing used to be cached as a 12 h miss.
func TestOnlineLyricsIncompleteMissRetriesSoon(t *testing.T) {
	app, store, handler, _, trackID := lyricsTestApp(t, "Rare Song", "")
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(namedLyricsSource{id: lyricsonline.SourceNetEase, fail: true},
		namedLyricsSource{id: lyricsonline.SourceLRCLIB}))
	if _, code := getLyrics(t, handler, trackID); code != http.StatusNotFound {
		t.Fatalf("lookup = %d", code)
	}
	row, err := store.OnlineLyrics(context.Background(), trackID)
	if err != nil || row.Status != storage.OnlineLyricsNotFound {
		t.Fatalf("row = %+v %v", row, err)
	}
	retry, _ := time.Parse(time.RFC3339Nano, row.RetryAfter)
	if d := time.Until(retry); d < 25*time.Minute || d > 35*time.Minute {
		t.Fatalf("incomplete miss retries after %v, want ~30m", d)
	}
}

func TestOnlineLyricsDiagnosePage(t *testing.T) {
	app, store, handler, cookie, trackID := lyricsTestApp(t, "Rare Song", "")
	src := &fakeLyricsSource{none: true}
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(src))
	id := strconv.FormatInt(trackID, 10)

	// Search by title finds the track (single match opens it directly).
	rec := getWithCookie(handler, onlineLyricsPath+"/diagnose?q="+url.QueryEscape("Rare"), cookie)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "曲目 "+id) || !strings.Contains(body, "下次播放时会在线搜索") {
		t.Fatalf("diagnose by title = %d\n%s", rec.Code, body)
	}

	// A remembered miss is explained.
	getLyrics(t, handler, trackID)
	body = getWithCookie(handler, onlineLyricsPath+"/diagnose?q="+id, cookie).Body.String()
	if !strings.Contains(body, "之前在线没找到") {
		t.Fatalf("miss not explained:\n%s", body)
	}

	// "Look up now" reports candidates and saves a match.
	src.none = false
	rec = postSecurity(t, app, handler, cookie, onlineLyricsPath+"/diagnose", url.Values{"q": {id}})
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "找到歌词") || !strings.Contains(body, "Completely Different") ||
		!strings.Contains(body, "使用已缓存的在线歌词") {
		t.Fatalf("lookup now = %d\n%s", rec.Code, body)
	}
	if row, err := store.OnlineLyrics(context.Background(), trackID); err != nil || row.Status != storage.OnlineLyricsFound || row.RemoteID != "2" {
		t.Fatalf("saved row = %+v %v", row, err)
	}

	// A cleared track is diagnosed without being overwritten.
	if err := store.SuppressOnlineLyrics(context.Background(), trackID); err != nil {
		t.Fatal(err)
	}
	body = postSecurity(t, app, handler, cookie, onlineLyricsPath+"/diagnose", url.Values{"q": {id}}).Body.String()
	if !strings.Contains(body, "本次结果没有保存") {
		t.Fatalf("suppressed lookup:\n%s", body)
	}
	if row, _ := store.OnlineLyrics(context.Background(), trackID); row.Status != storage.OnlineLyricsSuppressed {
		t.Fatalf("suppressed row overwritten: %+v", row)
	}

	// Admin only.
	if rec := getWithCookie(handler, onlineLyricsPath+"/diagnose?q="+id, nil); rec.Code == http.StatusOK {
		t.Fatal("diagnose page must require admin")
	}
}
