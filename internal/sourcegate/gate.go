package sourcegate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrRateLimited = errors.New("remote source rate limited")

type CooldownError struct {
	Source     string
	RetryAfter time.Duration
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("%s rate limited, retry after %s", e.Source, e.RetryAfter)
}
func (e *CooldownError) Unwrap() error { return ErrRateLimited }

type Options struct {
	Intervals map[string]time.Duration
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
}
type state struct {
	mu            sync.Mutex
	last, blocked time.Time
	interval      time.Duration
}
type Gate struct {
	states map[string]*state
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
}

func New(o Options) *Gate {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = func(c context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-c.Done():
				return c.Err()
			case <-t.C:
				return nil
			}
		}
	}
	g := &Gate{states: map[string]*state{}, now: o.Now, sleep: o.Sleep}
	for k, v := range o.Intervals {
		g.states[k] = &state{interval: v}
	}
	return g
}
func (g *Gate) Owns(s string) bool {
	if g == nil {
		return false
	}
	_, ok := g.states[s]
	return ok
}
func (g *Gate) Wait(c context.Context, s string) error {
	if err := c.Err(); err != nil {
		return err
	}
	if !g.Owns(s) {
		return nil
	}
	v := g.states[s]
	v.mu.Lock()
	defer v.mu.Unlock()
	if d := v.blocked.Sub(g.now()); d > 0 {
		return &CooldownError{s, d}
	}
	if d := v.last.Add(v.interval).Sub(g.now()); d > 0 {
		if err := g.sleep(c, d); err != nil {
			return err
		}
	}
	if err := c.Err(); err != nil {
		return err
	}
	v.last = g.now()
	return nil
}
func (g *Gate) Block(s string, t time.Time) {
	if !g.Owns(s) {
		return
	}
	v := g.states[s]
	v.mu.Lock()
	defer v.mu.Unlock()
	if t.After(v.blocked) {
		v.blocked = t
	}
}
func (g *Gate) BlockedUntil(s string) time.Time {
	if !g.Owns(s) {
		return time.Time{}
	}
	v := g.states[s]
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.blocked
}

const (
	defaultRateLimitBackoff = time.Minute
	maxRateLimitBackoff     = 10 * time.Minute
)

func ParseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	delay := defaultRateLimitBackoff
	if header != "" {
		if seconds, err := strconv.Atoi(header); err == nil {
			if seconds < 0 {
				seconds = 0
			}
			// Cap before multiplying: seconds*time.Second overflows int64 for
			// huge (but parseable) values.
			if seconds > int(maxRateLimitBackoff/time.Second) {
				return maxRateLimitBackoff
			}
			delay = time.Duration(seconds) * time.Second
		} else if errors.Is(err, strconv.ErrRange) {
			// A number beyond int64 is far beyond the cap.
			return maxRateLimitBackoff
		} else if at, err := http.ParseTime(header); err == nil {
			delay = at.Sub(now)
			if delay < 0 {
				delay = 0
			}
		}
	}
	if delay > maxRateLimitBackoff {
		delay = maxRateLimitBackoff
	}
	return delay
}

func IsRateLimitResponse(source string, status int, h http.Header) bool {
	return status == 429 || status == 503 && (source == "musicbrainz" || h.Get("Retry-After") != "")
}
