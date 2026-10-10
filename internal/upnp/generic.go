package upnp

import (
	"context"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"
)

// genericRenderer gives a plain DLNA MediaRenderer (one URI at a time) a
// server-side queue: a monitor goroutine notices the end of a track and
// sets the next URI, so playback still continues without any client.
type genericRenderer struct {
	dev  Device
	soap soapClient
	// pollEvery is shortened in tests.
	pollEvery time.Duration

	mu      sync.Mutex
	items   []Item
	index   int
	loop    string
	shuffle bool
	// stopped is set by an explicit Stop so the monitor does not advance.
	stopped bool
	// sawPlaying/lastPos/lastDur are the monitor's view of the current track.
	sawPlaying bool
	lastPos    int64
	lastDur    int64
	monitor    context.CancelFunc
	parent     context.Context
}

func newGenericRenderer(parent context.Context, dev Device, soap soapClient) *genericRenderer {
	return &genericRenderer{dev: dev, soap: soap, parent: parent, pollEvery: 2 * time.Second, loop: LoopOff}
}

func (g *genericRenderer) Device() Device { return g.dev }

func (g *genericRenderer) av(ctx context.Context, action string, args ...arg) (map[string]string, error) {
	return g.soap.call(ctx, g.dev.avTransportURL, svcAVTransport, action, append([]arg{{"InstanceID", "0"}}, args...)...)
}

// load sets items[index] as the transport URI and starts it.
func (g *genericRenderer) load(ctx context.Context, index int, positionMs int64, play bool) error {
	g.mu.Lock()
	if index < 0 || index >= len(g.items) {
		g.mu.Unlock()
		return ErrUnsupported
	}
	it := g.items[index]
	g.index = index
	g.stopped = !play
	g.sawPlaying = false
	g.lastPos, g.lastDur = 0, it.DurationMs
	g.mu.Unlock()
	if _, err := g.av(ctx, "SetAVTransportURI", arg{"CurrentURI", it.URI}, arg{"CurrentURIMetaData", it.DIDL()}); err != nil {
		return err
	}
	if !play {
		return nil
	}
	if err := g.Play(ctx); err != nil {
		return err
	}
	if positionMs > 0 {
		var err error
		for attempt := 0; attempt < 6; attempt++ {
			if err = g.Seek(ctx, positionMs); err == nil || ctx.Err() != nil {
				break
			}
			time.Sleep(400 * time.Millisecond)
		}
		return err
	}
	return nil
}

func (g *genericRenderer) ensureMonitor() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.monitor != nil {
		return
	}
	ctx, cancel := context.WithCancel(g.parent)
	g.monitor = cancel
	go g.watch(ctx)
}

func (g *genericRenderer) watch(ctx context.Context) {
	ticker := time.NewTicker(g.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		g.tick(ctx)
	}
}

// tick advances to the next track when the renderer stopped on its own at
// the end of the current one.
func (g *genericRenderer) tick(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := g.av(callCtx, "GetTransportInfo")
	if err != nil {
		return
	}
	state := transportState(info["CurrentTransportState"])
	if state == StatePlaying || state == StatePaused {
		if pos, err := g.av(callCtx, "GetPositionInfo"); err == nil {
			g.mu.Lock()
			g.lastPos = parseDuration(pos["RelTime"])
			if d := parseDuration(pos["TrackDuration"]); d > 0 {
				g.lastDur = d
			}
			g.mu.Unlock()
		}
	}
	g.mu.Lock()
	if state == StatePlaying {
		g.sawPlaying = true
	}
	ended := state == StateStopped && g.sawPlaying && !g.stopped &&
		(g.lastDur <= 0 || g.lastPos >= g.lastDur-5000)
	next := -1
	if ended {
		next = g.nextIndexLocked(true)
		g.sawPlaying = false
	}
	g.mu.Unlock()
	if next >= 0 {
		_ = g.load(callCtx, next, 0, true)
	}
}

// nextIndexLocked picks the following index; natural ends honour repeat-one.
func (g *genericRenderer) nextIndexLocked(natural bool) int {
	n := len(g.items)
	if n == 0 {
		return -1
	}
	if natural && g.loop == LoopOne {
		return g.index
	}
	if g.shuffle && n > 1 {
		next := rand.IntN(n - 1)
		if next >= g.index {
			next++
		}
		return next
	}
	if g.index+1 < n {
		return g.index + 1
	}
	if g.loop == LoopAll || (!natural && g.loop == LoopOne) {
		return 0
	}
	return -1
}

func (g *genericRenderer) Replace(ctx context.Context, items []Item, start int, positionMs int64, play bool) error {
	g.mu.Lock()
	g.items = append([]Item(nil), items...)
	if start < 0 || start >= len(items) {
		start = 0
	}
	g.mu.Unlock()
	if len(items) == 0 {
		return g.Stop(ctx)
	}
	g.ensureMonitor()
	return g.load(ctx, start, positionMs, play)
}

func (g *genericRenderer) Insert(ctx context.Context, items []Item, at int) error {
	g.mu.Lock()
	wasEmpty := len(g.items) == 0
	if at < 0 || at > len(g.items) {
		at = len(g.items)
	}
	merged := make([]Item, 0, len(g.items)+len(items))
	merged = append(merged, g.items[:at]...)
	merged = append(merged, items...)
	merged = append(merged, g.items[at:]...)
	g.items = merged
	if !wasEmpty && at <= g.index {
		g.index += len(items)
	}
	g.mu.Unlock()
	if wasEmpty {
		g.ensureMonitor()
		return g.load(ctx, 0, 0, true)
	}
	return nil
}

func (g *genericRenderer) Remove(ctx context.Context, index int) error {
	g.mu.Lock()
	if index < 0 || index >= len(g.items) {
		g.mu.Unlock()
		return nil
	}
	g.items = append(g.items[:index:index], g.items[index+1:]...)
	current := index == g.index
	if index < g.index {
		g.index--
	}
	n := len(g.items)
	g.mu.Unlock()
	if !current {
		return nil
	}
	if index < n {
		return g.load(ctx, index, 0, true)
	}
	return g.Stop(ctx)
}

func (g *genericRenderer) Move(_ context.Context, from, to int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.items)
	if from < 0 || from >= n || to < 0 || to >= n || from == to {
		return nil
	}
	it := g.items[from]
	g.items = append(g.items[:from:from], g.items[from+1:]...)
	g.items = append(g.items[:to], append([]Item{it}, g.items[to:]...)...)
	switch {
	case g.index == from:
		g.index = to
	case from < g.index && to >= g.index:
		g.index--
	case from > g.index && to <= g.index:
		g.index++
	}
	return nil
}

func (g *genericRenderer) PlayIndex(ctx context.Context, index int) error {
	g.ensureMonitor()
	return g.load(ctx, index, 0, true)
}

func (g *genericRenderer) Play(ctx context.Context) error {
	g.mu.Lock()
	g.stopped = false
	g.mu.Unlock()
	_, err := g.av(ctx, "Play", arg{"Speed", "1"})
	return err
}

func (g *genericRenderer) Pause(ctx context.Context) error {
	_, err := g.av(ctx, "Pause")
	return err
}

func (g *genericRenderer) Stop(ctx context.Context) error {
	g.mu.Lock()
	g.stopped = true
	g.mu.Unlock()
	_, err := g.av(ctx, "Stop")
	return err
}

func (g *genericRenderer) Next(ctx context.Context) error {
	g.mu.Lock()
	next := g.nextIndexLocked(false)
	g.mu.Unlock()
	if next < 0 {
		return ErrUnsupported
	}
	return g.load(ctx, next, 0, true)
}

func (g *genericRenderer) Previous(ctx context.Context) error {
	g.mu.Lock()
	prev := g.index - 1
	if prev < 0 {
		prev = 0
		if g.loop == LoopAll && len(g.items) > 0 {
			prev = len(g.items) - 1
		}
	}
	g.mu.Unlock()
	return g.load(ctx, prev, 0, true)
}

func (g *genericRenderer) Seek(ctx context.Context, positionMs int64) error {
	_, err := g.av(ctx, "Seek", arg{"Unit", "REL_TIME"}, arg{"Target", formatDuration(positionMs)})
	return err
}

func (g *genericRenderer) SetPlayMode(_ context.Context, loop string, shuffle bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if loop != LoopAll && loop != LoopOne {
		loop = LoopOff
	}
	g.loop, g.shuffle = loop, shuffle
	return nil
}

func (g *genericRenderer) Status(ctx context.Context) (Status, error) {
	info, err := g.av(ctx, "GetTransportInfo")
	if err != nil {
		return Status{}, err
	}
	pos, err := g.av(ctx, "GetPositionInfo")
	if err != nil {
		return Status{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := Status{
		State:       transportState(info["CurrentTransportState"]),
		Index:       -1,
		TrackURI:    pos["TrackURI"],
		PositionMs:  parseDuration(pos["RelTime"]),
		DurationMs:  parseDuration(pos["TrackDuration"]),
		QueueLength: len(g.items),
		QueueActive: len(g.items) > 0,
	}
	if st.QueueActive {
		st.Index = g.index
		if st.DurationMs == 0 {
			st.DurationMs = g.items[g.index].DurationMs
		}
		st.Title = g.items[g.index].Title
		if st.TrackURI == "" {
			st.TrackURI = g.items[g.index].URI
		}
	}
	return st, nil
}

func (g *genericRenderer) Queue(context.Context) ([]Item, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Item(nil), g.items...), nil
}

func (g *genericRenderer) Volume(ctx context.Context) (int, error) {
	if g.dev.renderingControlURL == "" {
		return 0, ErrUnsupported
	}
	res, err := g.soap.call(ctx, g.dev.renderingControlURL, svcRenderingControl, "GetVolume", arg{"InstanceID", "0"}, arg{"Channel", "Master"})
	if err != nil {
		return 0, err
	}
	v, _ := strconv.Atoi(res["CurrentVolume"])
	return v, nil
}

func (g *genericRenderer) SetVolume(ctx context.Context, volume int) error {
	if g.dev.renderingControlURL == "" {
		return ErrUnsupported
	}
	_, err := g.soap.call(ctx, g.dev.renderingControlURL, svcRenderingControl, "SetVolume", arg{"InstanceID", "0"}, arg{"Channel", "Master"}, arg{"DesiredVolume", itoa(clampVolume(volume))})
	return err
}

func (g *genericRenderer) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.monitor != nil {
		g.monitor()
		g.monitor = nil
	}
}
