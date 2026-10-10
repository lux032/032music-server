package upnp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrDeviceNotFound is returned for an unknown device id.
var ErrDeviceNotFound = errors.New("upnp: device not found")

// Options configures a Manager.
type Options struct {
	// StaticDevices are extra description URLs or bare hosts (assumed Sonos,
	// port 1400) probed besides SSDP — e.g. when the server runs in a
	// container without multicast.
	StaticDevices []string
	Logger        *slog.Logger
	HTTPClient    *http.Client
	// SearchTimeout bounds one SSDP round (default 3s).
	SearchTimeout time.Duration
	// DisableSSDP limits discovery to StaticDevices.
	DisableSSDP bool
	// search replaces SSDP in tests.
	search func(ctx context.Context, timeout time.Duration) []string
}

// Manager discovers renderers and hands out one Renderer per device. Generic
// renderers keep their server-side queue for the life of the process.
type Manager struct {
	opts   Options
	soap   soapClient
	http   *http.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	scanMu    sync.Mutex
	devices   map[string]Device
	renderers map[string]Renderer
	scannedAt time.Time
}

func NewManager(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.SearchTimeout <= 0 {
		opts.SearchTimeout = 3 * time.Second
	}
	if opts.DisableSSDP {
		opts.search = func(context.Context, time.Duration) []string { return nil }
	} else if opts.search == nil {
		opts.search = ssdpSearch
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		opts: opts, soap: newSOAPClient(client), http: client, ctx: ctx, cancel: cancel,
		devices: map[string]Device{}, renderers: map[string]Renderer{},
	}
}

// Close stops every generic renderer monitor.
func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.renderers {
		if g, ok := r.(*genericRenderer); ok {
			g.close()
		}
	}
}

// Devices returns the known devices, rescanning when refresh is set or the
// cache is empty or older than five minutes.
func (m *Manager) Devices(ctx context.Context, refresh bool) []Device {
	m.mu.Lock()
	stale := refresh || len(m.devices) == 0 || time.Since(m.scannedAt) > 5*time.Minute
	m.mu.Unlock()
	if stale {
		m.scan(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Device, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == KindSonos
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Renderer returns the controller for id, scanning once if it is unknown.
func (m *Manager) Renderer(ctx context.Context, id string) (Renderer, error) {
	if r := m.renderer(id); r != nil {
		return r, nil
	}
	m.scan(ctx)
	if r := m.renderer(id); r != nil {
		return r, nil
	}
	return nil, ErrDeviceNotFound
}

func (m *Manager) renderer(id string) Renderer {
	m.mu.Lock()
	defer m.mu.Unlock()
	dev, ok := m.devices[id]
	if !ok {
		return nil
	}
	if r, ok := m.renderers[id]; ok {
		return r
	}
	var r Renderer
	if dev.Kind == KindSonos {
		r = newSonosRenderer(dev, m.soap)
	} else {
		r = newGenericRenderer(m.ctx, dev, m.soap)
	}
	m.renderers[id] = r
	return r
}

func (m *Manager) scan(ctx context.Context) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	locations := m.opts.search(ctx, m.opts.SearchTimeout)
	for _, static := range m.opts.StaticDevices {
		static = strings.TrimSpace(static)
		if static == "" {
			continue
		}
		if !strings.Contains(static, "://") {
			if !strings.Contains(static, ":") {
				static += ":1400"
			}
			static = "http://" + static + "/xml/device_description.xml"
		}
		locations = append(locations, static)
	}

	type found struct {
		location string
		desc     description
	}
	results := make([]found, len(locations))
	var wg sync.WaitGroup
	for i, loc := range locations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			desc, err := fetchDescription(callCtx, m.http, loc)
			if err != nil {
				m.opts.Logger.Debug("upnp description failed", "location", loc, "error", err)
				return
			}
			results[i] = found{loc, desc}
		}()
	}
	wg.Wait()

	devices := map[string]Device{}
	sonosPlayers := map[string]Device{}
	topology := false
	for _, f := range results {
		if f.location == "" {
			continue
		}
		if isSonos(f.desc) {
			d := sonosDevice(f.location, f.desc)
			if d.ID != "" {
				sonosPlayers[d.ID] = d
			}
			continue
		}
		if d, ok := genericDevice(f.location, f.desc); ok {
			devices[d.ID] = d
		}
	}
	if len(sonosPlayers) > 0 {
		var groups []Device
		for _, player := range sonosPlayers {
			callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			g, err := sonosGroups(callCtx, m.soap, player.baseURL, sonosPlayers)
			cancel()
			if err == nil && len(g) > 0 {
				groups = g
				topology = true
				break
			}
		}
		if len(groups) == 0 {
			for _, player := range sonosPlayers {
				groups = append(groups, player)
			}
		}
		for _, g := range groups {
			devices[g.ID] = g
		}
	}
	if len(devices) == 0 && ctx.Err() != nil {
		return // a cancelled scan must not wipe the cache
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, d := range devices {
		if old, ok := m.devices[id]; ok && sameEndpoint(old, d) {
			continue // keep the renderer (and a generic queue) alive
		}
		if r, ok := m.renderers[id]; ok {
			if g, ok := r.(*genericRenderer); ok {
				g.close()
			}
			delete(m.renderers, id)
		}
	}
	// Devices that did not answer this round stay known for a while: a
	// sleeping speaker must not lose its server-side queue.
	for id, d := range devices {
		m.devices[id] = d
	}
	// The Sonos topology is authoritative: regrouped coordinators vanish.
	if topology {
		for id, d := range m.devices {
			if _, ok := devices[id]; !ok && d.Kind == KindSonos {
				delete(m.devices, id)
				delete(m.renderers, id)
			}
		}
	}
	m.scannedAt = time.Now()
}

func sameEndpoint(a, b Device) bool {
	return a.baseURL == b.baseURL && a.avTransportURL == b.avTransportURL && a.Kind == b.Kind
}
