package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/upnp"
)

// Cast: the server drives UPnP renderers on the LAN so the web panel (which
// cannot multicast) can push whole albums/playlists. Sonos gets its native
// play queue — the speaker advances on its own, no client has to stay
// online; plain DLNA renderers get a server-side queue (see upnp package).

const castMaxTracks = 5000

// castRenderer is the slice of upnp.Manager the handlers use (tests fake it).
type castRenderers interface {
	Devices(ctx context.Context, refresh bool) []upnp.Device
	Renderer(ctx context.Context, id string) (upnp.Renderer, error)
}

type castState struct {
	locks sync.Map // device id -> *sync.Mutex: serializes queue edits per device
	warm  sync.Map // device id -> context.CancelFunc of the running warm-up
}

func (a *App) castLock(id string) *sync.Mutex {
	mu, _ := a.castState.locks.LoadOrStore(id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// SetCastManager attaches the renderer manager (nil disables casting).
func (a *App) SetCastManager(m castRenderers) { a.cast = m }

func (a *App) castRenderer(w http.ResponseWriter, r *http.Request) (upnp.Renderer, bool) {
	if a.cast == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "cast_unavailable", "Casting is disabled on this server.")
		return nil, false
	}
	renderer, err := a.cast.Renderer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "device_not_found", "Renderer not found on the network.")
		return nil, false
	}
	return renderer, true
}

func (a *App) writeCastError(w http.ResponseWriter, err error) {
	if errors.Is(err, upnp.ErrUnsupported) {
		writeAPIError(w, http.StatusConflict, "unsupported", "The renderer cannot perform this action.")
		return
	}
	a.logger.Warn("cast command failed", "error", err)
	writeAPIError(w, http.StatusBadGateway, "renderer_error", err.Error())
}

func (a *App) handleCastDevices(w http.ResponseWriter, r *http.Request) {
	if a.cast == nil {
		writeJSON(w, http.StatusOK, map[string]any{"devices": []upnp.Device{}, "available": false})
		return
	}
	devices := a.cast.Devices(r.Context(), r.URL.Query().Get("refresh") == "1")
	if devices == nil {
		devices = []upnp.Device{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices, "available": true})
}

type castStatusResponse struct {
	upnp.Status
	TrackID int64 `json:"trackId"`
	Volume  *int  `json:"volume,omitempty"`
}

func (a *App) castStatus(ctx context.Context, renderer upnp.Renderer, withVolume bool) (castStatusResponse, error) {
	st, err := renderer.Status(ctx)
	if err != nil {
		return castStatusResponse{}, err
	}
	out := castStatusResponse{Status: st, TrackID: castTrackID(st.TrackURI)}
	if withVolume {
		if v, err := renderer.Volume(ctx); err == nil {
			out.Volume = &v
		}
	}
	return out, nil
}

func (a *App) handleCastStatus(w http.ResponseWriter, r *http.Request) {
	renderer, ok := a.castRenderer(w, r)
	if !ok {
		return
	}
	st, err := a.castStatus(r.Context(), renderer, r.URL.Query().Get("volume") == "1")
	if err != nil {
		a.writeCastError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type castQueueTrack struct {
	TrackID    int64  `json:"trackId"`
	Title      string `json:"title"`
	Artist     string `json:"artist,omitempty"`
	Album      string `json:"album,omitempty"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Container  string `json:"container,omitempty"`
}

// handleCastQueue lists the renderer's queue with library metadata so a
// client can adopt a queue another client (or the Sonos app) built.
func (a *App) handleCastQueue(w http.ResponseWriter, r *http.Request) {
	renderer, ok := a.castRenderer(w, r)
	if !ok {
		return
	}
	entries, err := renderer.Queue(r.Context())
	if err != nil {
		a.writeCastError(w, err)
		return
	}
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		if id := castTrackID(e.URI); id > 0 {
			ids = append(ids, id)
		}
	}
	known := map[int64]storage.Track{}
	for start := 0; start < len(ids); start += 500 {
		tracks, err := a.store.TracksByIDs(r.Context(), uniqueIDs(ids[start:min(start+500, len(ids))]))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusInternalServerError, "query_failed", "Track lookup failed.")
			return
		}
		for _, t := range tracks {
			known[t.ID] = t
		}
	}
	items := make([]castQueueTrack, 0, len(entries))
	for _, e := range entries {
		item := castQueueTrack{TrackID: castTrackID(e.URI), Title: e.Title}
		if t, ok := known[item.TrackID]; ok {
			item = castQueueTrack{TrackID: t.ID, Title: t.Title, Artist: t.Artist, Album: t.Album, ArtworkURL: t.ArtworkURL, DurationMs: t.DurationMillis, Container: t.Container}
		} else {
			item.TrackID = 0 // foreign item (radio, Sonos app, a missing track)
		}
		items = append(items, item)
	}
	st, err := a.castStatus(r.Context(), renderer, false)
	if err != nil {
		a.writeCastError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "status": st})
}

type castQueueRequest struct {
	// Mode is replace (default), append or insert.
	Mode       string  `json:"mode"`
	TrackIDs   []int64 `json:"trackIds"`
	StartIndex int     `json:"startIndex"`
	PositionMs int64   `json:"positionMs"`
	// InsertAt is the 0-based queue position for mode insert.
	InsertAt int   `json:"insertAt"`
	Play     *bool `json:"play"`
}

// castCommandContext detaches a queue edit from the HTTP request: a browser
// navigating away mid-replace must not leave a half-built speaker queue.
func castCommandContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
}

func (a *App) handleCastLoadQueue(w http.ResponseWriter, r *http.Request) {
	renderer, ok := a.castRenderer(w, r)
	if !ok {
		return
	}
	var body castQueueRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON body.")
		return
	}
	if body.Mode == "" {
		body.Mode = "replace"
	}
	if body.Mode != "replace" && body.Mode != "append" && body.Mode != "insert" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "mode must be replace, append or insert.")
		return
	}
	if len(body.TrackIDs) == 0 && body.Mode != "replace" || len(body.TrackIDs) > castMaxTracks {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "trackIds must hold 1-5000 ids.")
		return
	}
	ctx, cancel := castCommandContext(r)
	defer cancel()
	items, warm, err := a.castItems(ctx, r, renderer.Device(), body.TrackIDs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "One or more tracks were not found.")
		} else {
			writeAPIError(w, http.StatusInternalServerError, "query_failed", "Track lookup failed.")
		}
		return
	}
	mu := a.castLock(renderer.Device().ID)
	mu.Lock()
	defer mu.Unlock()
	switch body.Mode {
	case "replace":
		start := body.StartIndex
		if start < 0 || start >= len(items) {
			start = 0
		}
		// The first track must be ready before the speaker asks for it; if
		// a hi-res cache cannot be built in time it plays as live MP3.
		if len(items) > 0 && warm[start] {
			if !a.warmCastTrack(ctx, body.TrackIDs[start], 45*time.Second) {
				items[start] = a.castLiveItem(items[start])
				warm[start] = false
			}
		}
		play := body.Play == nil || *body.Play
		err = renderer.Replace(ctx, items, start, max(body.PositionMs, 0), play)
		a.startCastWarmup(renderer.Device().ID, body.TrackIDs, warm, start, true)
	default:
		at := -1
		if body.Mode == "insert" {
			at = body.InsertAt
		}
		err = renderer.Insert(ctx, items, at)
		a.startCastWarmup(renderer.Device().ID, body.TrackIDs, warm, 0, false)
	}
	if err != nil {
		a.writeCastError(w, err)
		return
	}
	st, err := a.castStatus(ctx, renderer, false)
	if err != nil {
		a.writeCastError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *App) handleCastRemove(w http.ResponseWriter, r *http.Request) {
	a.castEdit(w, r, func(ctx context.Context, renderer upnp.Renderer, body castControlRequest) error {
		return renderer.Remove(ctx, body.Index)
	})
}

func (a *App) handleCastMove(w http.ResponseWriter, r *http.Request) {
	a.castEdit(w, r, func(ctx context.Context, renderer upnp.Renderer, body castControlRequest) error {
		return renderer.Move(ctx, body.From, body.To)
	})
}

type castControlRequest struct {
	Action     string `json:"action"`
	PositionMs int64  `json:"positionMs"`
	Index      int    `json:"index"`
	From       int    `json:"from"`
	To         int    `json:"to"`
	Volume     int    `json:"volume"`
	Loop       string `json:"loop"`
	Shuffle    bool   `json:"shuffle"`
}

func (a *App) handleCastControl(w http.ResponseWriter, r *http.Request) {
	a.castEdit(w, r, func(ctx context.Context, renderer upnp.Renderer, body castControlRequest) error {
		switch body.Action {
		case "play":
			return renderer.Play(ctx)
		case "pause":
			return renderer.Pause(ctx)
		case "stop":
			return renderer.Stop(ctx)
		case "next":
			return renderer.Next(ctx)
		case "previous":
			return renderer.Previous(ctx)
		case "seek":
			return renderer.Seek(ctx, max(body.PositionMs, 0))
		case "playIndex":
			return renderer.PlayIndex(ctx, body.Index)
		case "volume":
			return renderer.SetVolume(ctx, body.Volume)
		case "playMode":
			return renderer.SetPlayMode(ctx, body.Loop, body.Shuffle)
		}
		return errCastBadAction
	})
}

var errCastBadAction = errors.New("unknown action")

func (a *App) castEdit(w http.ResponseWriter, r *http.Request, run func(context.Context, upnp.Renderer, castControlRequest) error) {
	renderer, ok := a.castRenderer(w, r)
	if !ok {
		return
	}
	var body castControlRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON body.")
		return
	}
	ctx, cancel := castCommandContext(r)
	defer cancel()
	mu := a.castLock(renderer.Device().ID)
	mu.Lock()
	err := run(ctx, renderer, body)
	mu.Unlock()
	if errors.Is(err, errCastBadAction) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Unknown action.")
		return
	}
	if err != nil {
		a.writeCastError(w, err)
		return
	}
	st, err := a.castStatus(ctx, renderer, body.Action == "volume")
	if err != nil {
		a.writeCastError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ---------------------------------------------------------------- media

var castTrackPattern = regexp.MustCompile(`/api/v1/tracks/(\d+)/(?:stream|transcode\.[a-z0-9]+)`)

// castTrackID extracts the library track id from a queued media URI (Sonos
// may report it percent-decoded or as x-rincon-mp3radio://); 0 if foreign.
func castTrackID(uri string) int64 {
	m := castTrackPattern.FindStringSubmatch(uri)
	if m == nil {
		return 0
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// sonosCodecs are the codecs Sonos (and most DLNA renderers) decode
// natively; anything else goes through the live MP3 transcoder.
var sonosCodecs = map[string]bool{"mp3": true, "aac": true, "flac": true, "alac": true, "vorbis": true, "wav": true, "pcm_s16le": true, "pcm_s24le": true, "pcm_s16be": true, "pcm_s24be": true}

// castItems resolves track ids into renderer items. warm[i] marks items
// served from the FLAC 48 kHz cache, which has to be built before use.
func (a *App) castItems(ctx context.Context, r *http.Request, dev upnp.Device, ids []int64) ([]upnp.Item, []bool, error) {
	byID := map[int64]storage.Track{}
	unique := uniqueIDs(ids)
	for start := 0; start < len(unique); start += 500 {
		tracks, err := a.store.TracksByIDs(ctx, unique[start:min(start+500, len(unique))])
		if err != nil {
			return nil, nil, err
		}
		for _, t := range tracks {
			byID[t.ID] = t
		}
	}
	base := a.castBaseURL(r, dev)
	token := url.QueryEscape(a.currentCredentials().mediaToken)
	items := make([]upnp.Item, len(ids))
	warm := make([]bool, len(ids))
	for i, id := range ids {
		t := byID[id]
		codec := strings.ToLower(t.Codec)
		lossless := codec == "flac" || codec == "alac" || codec == "wav" || strings.HasPrefix(codec, "pcm_")
		path, mime := "/stream", t.MIMEType
		switch {
		case lossless && t.SampleRate > 48000 && a.transcoder.available["flac"]:
			// Sonos plays at most 24-bit/48 kHz.
			path, mime, warm[i] = "/transcode.flac", "audio/flac", true
		case codec != "" && !sonosCodecs[codec] && a.transcoder.available["mp3"]:
			path, mime = "/transcode.mp3", "audio/mpeg"
		}
		uri := base + "/api/v1/tracks/" + strconv.FormatInt(id, 10) + path + "?mediaToken=" + token
		if warm[i] {
			uri += "&maxSampleRate=48000"
		} else if path == "/transcode.mp3" {
			uri += "&bitrate=320"
		}
		item := upnp.Item{URI: uri, MIME: mime, Title: t.Title, Artist: t.Artist, Album: t.Album, DurationMs: t.DurationMillis, ID: "032-track-" + strconv.FormatInt(id, 10)}
		if t.ArtworkURL != "" {
			item.ArtURI = base + thumbURL(t.ArtworkURL, 512) + "&mediaToken=" + token
		}
		items[i] = item
	}
	return items, warm, nil
}

// castLiveItem rewrites a FLAC-cache item to the live MP3 transcode.
func (a *App) castLiveItem(item upnp.Item) upnp.Item {
	if !a.transcoder.available["mp3"] {
		return item
	}
	base, query, _ := strings.Cut(item.URI, "?")
	base = strings.TrimSuffix(base, "/transcode.flac") + "/transcode.mp3"
	query = strings.Replace(query, "maxSampleRate=48000", "bitrate=320", 1)
	item.URI, item.MIME = base+"?"+query, "audio/mpeg"
	return item
}

// castBaseURL is the origin renderers fetch media from. Order: the
// configured MUSIC_SERVER_CAST_BASE_URL; the browser's own host when it is
// a private IP on the speaker's network (works behind Docker port
// mapping); otherwise the local interface address towards the speaker.
func (a *App) castBaseURL(r *http.Request, dev upnp.Device) string {
	if a.config.CastBaseURL != "" {
		return a.config.CastBaseURL
	}
	deviceIP := net.ParseIP(dev.Host).To4()
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if ip := net.ParseIP(host).To4(); ip != nil && ip.IsPrivate() && deviceIP != nil && ip[0] == deviceIP[0] && ip[1] == deviceIP[1] {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	port := "4533"
	if _, p, err := net.SplitHostPort(a.config.ListenAddress); err == nil && p != "" {
		port = p
	}
	if dev.Host != "" {
		if conn, err := net.Dial("udp4", net.JoinHostPort(dev.Host, "1400")); err == nil {
			local := conn.LocalAddr().(*net.UDPAddr).IP.String()
			conn.Close()
			return "http://" + net.JoinHostPort(local, port)
		}
	}
	return "http://" + r.Host
}

// ------------------------------------------------------------ FLAC warm-up

type discardResponse struct {
	header http.Header
	status int
}

func (d *discardResponse) Header() http.Header         { return d.header }
func (d *discardResponse) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardResponse) WriteHeader(status int) {
	if d.status == 0 {
		d.status = status
	}
}

// warmCastTrack builds the 48 kHz FLAC cache entry for a track through the
// regular transcode handler, retrying while the cache workers are busy.
func (a *App) warmCastTrack(ctx context.Context, id int64, budget time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	stop := context.AfterFunc(a.transcoder.ctx, cancel)
	defer stop()
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/tracks/"+strconv.FormatInt(id, 10)+"/transcode.flac?maxSampleRate=48000", nil)
		if err != nil {
			return false
		}
		req.SetPathValue("id", strconv.FormatInt(id, 10))
		req.Header.Set("Range", "bytes=0-0")
		resp := &discardResponse{header: http.Header{}}
		a.handleTranscode(resp, req, "flac")
		switch {
		case resp.status == 0 || resp.status == http.StatusOK || resp.status == http.StatusPartialContent:
			return ctx.Err() == nil
		case resp.status == http.StatusServiceUnavailable && resp.header.Get("Retry-After") != "":
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		default:
			return false
		}
	}
	return false
}

// startCastWarmup pre-builds the FLAC cache for the queued hi-res tracks in
// play order (starting after start), in the background, so the speaker
// finds them ready even with no client online. A replace cancels the
// previous warm-up of the same device.
func (a *App) startCastWarmup(deviceID string, ids []int64, warm []bool, start int, replace bool) {
	var order []int64
	for k := 1; k <= len(ids); k++ {
		i := (start + k) % len(ids)
		if warm[i] {
			order = append(order, ids[i])
		}
	}
	if len(order) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(a.transcoder.ctx)
	if replace {
		if old, ok := a.castState.warm.Swap(deviceID, cancel); ok {
			old.(context.CancelFunc)()
		}
	}
	go func() {
		defer cancel()
		seen := map[int64]bool{}
		for _, id := range order {
			if seen[id] || ctx.Err() != nil {
				continue
			}
			seen[id] = true
			a.warmCastTrack(ctx, id, 10*time.Minute)
		}
	}()
}
