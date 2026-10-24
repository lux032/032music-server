package sourcegate

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestGateSpacingCooldownCancel(t *testing.T) {
	now := time.Unix(1000, 0)
	wait := time.Duration(0)
	g := New(Options{Intervals: map[string]time.Duration{"musicbrainz": time.Second}, Now: func() time.Time { return now }, Sleep: func(c context.Context, d time.Duration) error { wait = d; now = now.Add(d); return c.Err() }})
	c := context.Background()
	if e := g.Wait(c, "musicbrainz"); e != nil {
		t.Fatal(e)
	}
	if e := g.Wait(c, "musicbrainz"); e != nil || wait != time.Second {
		t.Fatal(e, wait)
	}
	g.Block("musicbrainz", now.Add(time.Minute))
	g.Block("musicbrainz", now)
	if e := g.Wait(c, "musicbrainz"); !errors.Is(e, ErrRateLimited) {
		t.Fatal(e)
	}
	cx, cancel := context.WithCancel(c)
	cancel()
	if !errors.Is(g.Wait(cx, "musicbrainz"), context.Canceled) {
		t.Fatal("cancel")
	}
	if !IsRateLimitResponse("musicbrainz", 503, http.Header{}) || IsRateLimitResponse("bangumi", 503, http.Header{}) {
		t.Fatal("source policy")
	}
	if ParseRetryAfter("", now) != time.Minute {
		t.Fatal("default")
	}
}
