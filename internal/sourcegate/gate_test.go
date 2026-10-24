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

func TestSetIntervalPreservesCooldownAndLastRequest(t *testing.T) {
	now := time.Now()
	var waited time.Duration
	g := New(Options{Intervals: map[string]time.Duration{"bangumi": time.Second}, Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) error { waited = d; now = now.Add(d); return nil }})
	if err := g.Wait(context.Background(), "bangumi"); err != nil {
		t.Fatal(err)
	}
	g.SetInterval("bangumi", 2*time.Second)
	if err := g.Wait(context.Background(), "bangumi"); err != nil {
		t.Fatal(err)
	}
	if waited != 2*time.Second {
		t.Fatal(waited)
	}
	g.Block("bangumi", now.Add(time.Minute))
	g.SetInterval("bangumi", 0)
	if err := g.Wait(context.Background(), "bangumi"); err == nil {
		t.Fatal("lost cooldown")
	}
	g.SetInterval("unknown", time.Second)
	if g.Owns("unknown") {
		t.Fatal("registered unknown source")
	}
}

func TestGateIntervalWaitDeadline(t *testing.T) {
	g := New(Options{Intervals: map[string]time.Duration{"bangumi": time.Hour}})
	if err := g.Wait(context.Background(), "bangumi"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := g.Wait(ctx, "bangumi"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait err=%v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("interval wait took %s", elapsed)
	}
}
