package upnp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSonos is a minimal in-memory Sonos zone player: it keeps a queue,
// a transport and records every SOAP action it receives.
type fakeSonos struct {
	t        *testing.T
	mu       sync.Mutex
	queue    []string
	metas    []string
	current  string // CurrentURI
	track    int    // 1-based
	state    string
	relTime  string
	actions  []string
	noBatch  bool // reject AddMultipleURIsToQueue (old firmware)
	server   *httptest.Server
	uuid     string
	playMode string
}

func newFakeSonos(t *testing.T) *fakeSonos {
	f := &fakeSonos{t: t, state: "STOPPED", uuid: "RINCON_TEST01400", relTime: "0:00:00"}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeSonos) device() Device {
	return Device{ID: f.uuid, Kind: KindSonos, Queue: true, baseURL: f.server.URL, Host: "127.0.0.1"}
}

func (f *fakeSonos) fault(w http.ResponseWriter, code int) {
	w.WriteHeader(500)
	fmt.Fprintf(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>x</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code)
}

func (f *fakeSonos) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		fmt.Fprintf(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device><deviceType>urn:schemas-upnp-org:device:ZonePlayer:1</deviceType><friendlyName>127.0.0.1 - Sonos One</friendlyName><manufacturer>Sonos, Inc.</manufacturer><modelName>Sonos One</modelName><UDN>uuid:%s</UDN><roomName>Living Room</roomName></device></root>`, f.uuid)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	soapAction := strings.Trim(r.Header.Get("SOAPACTION"), `"`)
	_, action, _ := strings.Cut(soapAction, "#")
	args, err := responseValues(raw, action)
	if err != nil {
		f.t.Errorf("bad request for %s: %v", action, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, action)
	out := map[string]string{}
	switch action {
	case "RemoveAllTracksFromQueue":
		f.queue, f.metas = nil, nil
		f.state = "STOPPED"
	case "AddMultipleURIsToQueue":
		if f.noBatch {
			f.fault(w, 402)
			return
		}
		uris := strings.Fields(args["EnqueuedURIs"])
		if n, _ := strconv.Atoi(args["NumberOfURIs"]); n != len(uris) || n > sonosBatch {
			f.t.Errorf("NumberOfURIs=%s for %d uris", args["NumberOfURIs"], len(uris))
		}
		if strings.Count(args["EnqueuedURIsMetaData"], "<DIDL-Lite") != len(uris) {
			f.t.Errorf("metadata count mismatch")
		}
		f.queue = append(f.queue, uris...)
		out["NumTracksAdded"] = strconv.Itoa(len(uris))
	case "AddURIToQueue":
		desired, _ := strconv.Atoi(args["DesiredFirstTrackNumberEnqueued"])
		uri := args["EnqueuedURI"]
		if desired <= 0 || desired > len(f.queue) {
			f.queue = append(f.queue, uri)
		} else {
			f.queue = append(f.queue[:desired-1], append([]string{uri}, f.queue[desired-1:]...)...)
		}
		f.metas = append(f.metas, args["EnqueuedURIMetaData"])
	case "RemoveTrackFromQueue":
		n, _ := strconv.Atoi(strings.TrimPrefix(args["ObjectID"], "Q:0/"))
		f.queue = append(f.queue[:n-1], f.queue[n:]...)
	case "ReorderTracksInQueue":
		start, _ := strconv.Atoi(args["StartingIndex"])
		before, _ := strconv.Atoi(args["InsertBefore"])
		uri := f.queue[start-1]
		rest := append(append([]string{}, f.queue[:start-1]...), f.queue[start:]...)
		pos := before - 1
		if before > start {
			pos--
		}
		f.queue = append(rest[:pos], append([]string{uri}, rest[pos:]...)...)
	case "SetAVTransportURI":
		f.current = args["CurrentURI"]
		f.track = 1
	case "Seek":
		if !strings.HasPrefix(f.current, "x-rincon-queue:") {
			f.fault(w, 701)
			return
		}
		if args["Unit"] == "TRACK_NR" {
			f.track, _ = strconv.Atoi(args["Target"])
		} else {
			f.relTime = args["Target"]
		}
	case "Play":
		f.state = "PLAYING"
	case "Pause":
		f.state = "PAUSED_PLAYBACK"
	case "Stop":
		f.state = "STOPPED"
	case "Next":
		f.track++
	case "Previous":
		f.track--
	case "SetPlayMode":
		f.playMode = args["NewPlayMode"]
	case "GetTransportInfo":
		out["CurrentTransportState"] = f.state
	case "GetMediaInfo":
		out["CurrentURI"] = f.current
		out["NrTracks"] = strconv.Itoa(len(f.queue))
	case "GetPositionInfo":
		out["Track"] = strconv.Itoa(f.track)
		out["RelTime"] = f.relTime
		out["TrackDuration"] = "0:04:05"
		if f.track >= 1 && f.track <= len(f.queue) {
			out["TrackURI"] = f.queue[f.track-1]
		}
		out["TrackMetaData"] = Item{Title: "Now <&> Playing", URI: out["TrackURI"]}.DIDL()
	case "Browse":
		start, _ := strconv.Atoi(args["StartingIndex"])
		count, _ := strconv.Atoi(args["RequestedCount"])
		end := min(start+count, len(f.queue))
		var b strings.Builder
		b.WriteString(`<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/">`)
		for i := start; i < end; i++ {
			fmt.Fprintf(&b, `<item id="Q:0/%d"><dc:title>T%d</dc:title><res>%s</res></item>`, i+1, i, xmlEscape(f.queue[i]))
		}
		b.WriteString(`</DIDL-Lite>`)
		out["Result"] = b.String()
		out["NumberReturned"] = strconv.Itoa(max(end-start, 0))
		out["TotalMatches"] = strconv.Itoa(len(f.queue))
	case "GetZoneGroupState":
		out["ZoneGroupState"] = fmt.Sprintf(`<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="%s" ID="g1"><ZoneGroupMember UUID="%s" Location="%s/xml/device_description.xml" ZoneName="Living Room"/><ZoneGroupMember UUID="RINCON_SUB" Location="http://10.0.0.9:1400/xml/device_description.xml" ZoneName="Living Room" Invisible="1"/><ZoneGroupMember UUID="RINCON_KITCHEN" Location="http://10.0.0.8:1400/xml/device_description.xml" ZoneName="Kitchen"/></ZoneGroup></ZoneGroups></ZoneGroupState>`, f.uuid, f.uuid, f.server.URL)
	case "GetGroupVolume":
		out["CurrentVolume"] = "23"
	case "SetGroupVolume":
	default:
		f.t.Errorf("unexpected action %s", action)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="x">`, action)
	for k, v := range out {
		fmt.Fprintf(&b, "<%s>%s</%s>", k, xmlEscape(v), k)
	}
	fmt.Fprintf(&b, `</u:%sResponse></s:Body></s:Envelope>`, action)
	_, _ = io.WriteString(w, b.String())
}

func (f *fakeSonos) snapshot() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queue...), append([]string(nil), f.actions...)
}

func items(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{URI: fmt.Sprintf("http://10.0.0.2:4533/api/v1/tracks/%d/stream?mediaToken=t", i+1), Title: fmt.Sprintf("Track %d", i+1), MIME: "audio/flac"}
	}
	return out
}

func TestSonosReplaceBatchesQueueAndStartsAtIndex(t *testing.T) {
	f := newFakeSonos(t)
	s := newSonosRenderer(f.device(), newSOAPClient(nil))
	ctx := context.Background()
	if err := s.Replace(ctx, items(40), 5, 61000, true); err != nil {
		t.Fatal(err)
	}
	queue, actions := f.snapshot()
	if len(queue) != 40 || queue[39] != items(40)[39].URI {
		t.Fatalf("queue = %d items", len(queue))
	}
	batches := strings.Count(strings.Join(actions, ","), "AddMultipleURIsToQueue")
	if batches != 3 {
		t.Fatalf("batches = %d, want 3 (16+16+8)", batches)
	}
	if f.current != "x-rincon-queue:RINCON_TEST01400#0" || f.track != 6 || f.state != "PLAYING" || f.relTime != "0:01:01" {
		t.Fatalf("transport = %q track=%d state=%s rel=%s", f.current, f.track, f.state, f.relTime)
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Index != 5 || st.State != StatePlaying || st.QueueLength != 40 || !st.QueueActive || st.TrackURI != items(40)[5].URI || st.DurationMs != 245000 || st.Title != "Now <&> Playing" {
		t.Fatalf("status = %+v", st)
	}
}

func TestSonosReplaceFallsBackToSingleAdds(t *testing.T) {
	f := newFakeSonos(t)
	f.noBatch = true
	s := newSonosRenderer(f.device(), newSOAPClient(nil))
	if err := s.Replace(context.Background(), items(3), 0, 0, true); err != nil {
		t.Fatal(err)
	}
	queue, actions := f.snapshot()
	if len(queue) != 3 || strings.Count(strings.Join(actions, ","), "AddURIToQueue") != 3 {
		t.Fatalf("queue=%v actions=%v", queue, actions)
	}
	if !strings.Contains(f.metas[0], "<dc:title>Track 1</dc:title>") {
		t.Fatalf("metadata = %s", f.metas[0])
	}
}

func TestSonosInsertRemoveMoveAndPlayIndex(t *testing.T) {
	f := newFakeSonos(t)
	s := newSonosRenderer(f.device(), newSOAPClient(nil))
	ctx := context.Background()
	all := items(6)
	if err := s.Replace(ctx, all[:3], 0, 0, true); err != nil {
		t.Fatal(err)
	}
	// insert two after the first item
	if err := s.Insert(ctx, all[3:5], 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Insert(ctx, all[5:], -1); err != nil {
		t.Fatal(err)
	}
	want := []string{all[0].URI, all[3].URI, all[4].URI, all[1].URI, all[2].URI, all[5].URI}
	queue, _ := f.snapshot()
	if strings.Join(queue, " ") != strings.Join(want, " ") {
		t.Fatalf("after insert queue=%v", queue)
	}
	if err := s.Remove(ctx, 1); err != nil { // drop all[3]
		t.Fatal(err)
	}
	if err := s.Move(ctx, 0, 3); err != nil { // all[0] after all[2]
		t.Fatal(err)
	}
	want = []string{all[4].URI, all[1].URI, all[2].URI, all[0].URI, all[5].URI}
	queue, _ = f.snapshot()
	if strings.Join(queue, " ") != strings.Join(want, " ") {
		t.Fatalf("after move queue=%v", queue)
	}
	if err := s.Move(ctx, 4, 1); err != nil {
		t.Fatal(err)
	}
	want = []string{all[4].URI, all[5].URI, all[1].URI, all[2].URI, all[0].URI}
	queue, _ = f.snapshot()
	if strings.Join(queue, " ") != strings.Join(want, " ") {
		t.Fatalf("after move back queue=%v", queue)
	}
	// The Sonos app switched to a radio station: PlayIndex re-selects the queue.
	f.mu.Lock()
	f.current = "x-sonosapi-stream:radio"
	f.mu.Unlock()
	if err := s.PlayIndex(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if f.current != s.queueURI() || f.track != 4 {
		t.Fatalf("PlayIndex current=%s track=%d", f.current, f.track)
	}
	got, err := s.Queue(ctx)
	if err != nil || len(got) != 5 || got[3].URI != all[2].URI {
		t.Fatalf("Queue() = %v, %v", got, err)
	}
	if err := s.SetPlayMode(ctx, LoopAll, true); err != nil || f.playMode != "SHUFFLE" {
		t.Fatalf("play mode = %s, %v", f.playMode, err)
	}
	if v, err := s.Volume(ctx); err != nil || v != 23 {
		t.Fatalf("volume = %d, %v", v, err)
	}
}

func TestSonosInsertIntoEmptyQueueStartsPlayback(t *testing.T) {
	f := newFakeSonos(t)
	s := newSonosRenderer(f.device(), newSOAPClient(nil))
	if err := s.Insert(context.Background(), items(2), -1); err != nil {
		t.Fatal(err)
	}
	if f.state != "PLAYING" || f.track != 1 || f.current != s.queueURI() {
		t.Fatalf("state=%s track=%d current=%s", f.state, f.track, f.current)
	}
}

func TestManagerDiscoversSonosGroupThroughTopology(t *testing.T) {
	f := newFakeSonos(t)
	m := NewManager(Options{search: func(context.Context, time.Duration) []string {
		return []string{f.server.URL + "/xml/device_description.xml"}
	}})
	defer m.Close()
	devices := m.Devices(context.Background(), true)
	if len(devices) != 1 {
		t.Fatalf("devices = %+v", devices)
	}
	d := devices[0]
	if d.ID != f.uuid || d.Kind != KindSonos || d.Name != "Living Room + Kitchen" || d.MemberCount != 2 || !d.Queue || d.Model != "Sonos One" {
		t.Fatalf("device = %+v", d)
	}
	r, err := m.Renderer(context.Background(), f.uuid)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*sonosRenderer); !ok {
		t.Fatalf("renderer = %T", r)
	}
	if _, err := m.Renderer(context.Background(), "nope"); err != ErrDeviceNotFound {
		t.Fatalf("unknown device err = %v", err)
	}
}

func TestDIDLEscapesMetadata(t *testing.T) {
	doc := Item{URI: "http://h/a?x=1&y=2", Title: `A "B" <C>`, Artist: "D&E", Album: "F", ArtURI: "http://h/art?s=1&t=2", MIME: "audio/flac", DurationMs: 61500, ID: "032-track-1"}.DIDL()
	for _, want := range []string{"&lt;C&gt;", "D&amp;E", `protocolInfo="http-get:*:audio/flac:*"`, `duration="0:01:01.000"`, "http://h/a?x=1&amp;y=2", "<upnp:albumArtURI>http://h/art?s=1&amp;t=2</upnp:albumArtURI>"} {
		if !strings.Contains(doc, want) {
			t.Errorf("DIDL missing %q: %s", want, doc)
		}
	}
	entries := parseDIDL(doc)
	if len(entries) != 1 || entries[0].URI != "http://h/a?x=1&y=2" || entries[0].Title != `A "B" <C>` {
		t.Fatalf("parse = %+v", entries)
	}
	if strings.Contains(doc, " \n") {
		t.Fatal("unexpected whitespace")
	}
}

func TestDurationRoundTrip(t *testing.T) {
	if formatDuration(3723000) != "1:02:03" || parseDuration("1:02:03.500") != 3723500 || parseDuration("NOT_IMPLEMENTED") != 0 {
		t.Fatal("duration conversion")
	}
}

// fakeRenderer is a single-URI DLNA renderer whose track ends on demand.
type fakeRenderer struct {
	mu      sync.Mutex
	uri     string
	state   string
	actions []string
	server  *httptest.Server
}

func newFakeRenderer(t *testing.T) *fakeRenderer {
	f := &fakeRenderer{state: "STOPPED"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_, action, _ := strings.Cut(strings.Trim(r.Header.Get("SOAPACTION"), `"`), "#")
		args, _ := responseValues(raw, action)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.actions = append(f.actions, action)
		out := map[string]string{}
		switch action {
		case "SetAVTransportURI":
			f.uri = args["CurrentURI"]
			f.state = "STOPPED"
		case "Play":
			f.state = "PLAYING"
		case "Stop":
			f.state = "STOPPED"
		case "GetTransportInfo":
			out["CurrentTransportState"] = f.state
		case "GetPositionInfo":
			out["TrackURI"] = f.uri
			out["RelTime"] = "0:00:10"
			out["TrackDuration"] = "0:00:12"
		}
		fmt.Fprintf(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="x">`, action)
		for k, v := range out {
			fmt.Fprintf(w, "<%s>%s</%s>", k, xmlEscape(v), k)
		}
		fmt.Fprintf(w, `</u:%sResponse></s:Body></s:Envelope>`, action)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRenderer) get() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uri, f.state
}

func TestGenericRendererAdvancesAfterTrackEnds(t *testing.T) {
	f := newFakeRenderer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := newGenericRenderer(ctx, Device{ID: "tv", Kind: KindDLNA, avTransportURL: f.server.URL}, newSOAPClient(nil))
	g.pollEvery = 10 * time.Millisecond
	defer g.close()
	all := items(3)
	if err := g.Replace(ctx, all, 0, 0, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.sawPlaying })
	// the renderer reaches the end of track 1 on its own
	f.mu.Lock()
	f.state = "STOPPED"
	f.mu.Unlock()
	waitFor(t, func() bool { uri, state := f.get(); return uri == all[1].URI && state == "PLAYING" })
	st, err := g.Status(ctx)
	if err != nil || st.Index != 1 || st.QueueLength != 3 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	// an explicit Stop must not advance
	if err := g.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if uri, _ := f.get(); uri != all[1].URI {
		t.Fatalf("advanced after Stop to %s", uri)
	}
	// insert before the current item keeps the current index pointing at it
	if err := g.Insert(ctx, items(1), 0); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.Status(ctx); st.Index != 2 {
		t.Fatalf("index after insert = %d", st.Index)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestGenericDeviceFromDescription(t *testing.T) {
	desc := description{Device: descDevice{
		DeviceType: "urn:schemas-upnp-org:device:MediaRenderer:1", FriendlyName: "Living TV", UDN: "uuid:abc-123",
		Services: []descService{
			{ServiceType: "urn:schemas-upnp-org:service:AVTransport:1", ControlURL: "/upnp/control/AVTransport1"},
			{ServiceType: "urn:schemas-upnp-org:service:RenderingControl:1", ControlURL: "upnp/control/RC"},
		},
	}}
	d, ok := genericDevice("http://192.168.1.50:9197/dmr", desc)
	if !ok || d.ID != "abc-123" || d.avTransportURL != "http://192.168.1.50:9197/upnp/control/AVTransport1" || d.renderingControlURL != "http://192.168.1.50:9197/upnp/control/RC" || d.Kind != KindDLNA || d.Queue {
		t.Fatalf("device = %+v", d)
	}
	if isSonos(desc) {
		t.Fatal("not sonos")
	}
}
