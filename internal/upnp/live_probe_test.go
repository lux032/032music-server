package upnp

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// Opt-in probe against a real Sonos: adds to the (inactive) queue, reads it
// back and clears it. Never touches the transport. UPNP_LIVE_SONOS=http://ip:1400
func TestLiveSonosQueueBatch(t *testing.T) {
	base := os.Getenv("UPNP_LIVE_SONOS")
	if base == "" {
		t.Skip("set UPNP_LIVE_SONOS")
	}
	ctx := context.Background()
	s := newSonosRenderer(Device{ID: os.Getenv("UPNP_LIVE_UUID"), baseURL: base, Kind: KindSonos}, newSOAPClient(nil))
	before, _ := s.av(ctx, "GetMediaInfo")
	list := make([]Item, 20)
	for i := range list {
		list[i] = Item{URI: fmt.Sprintf("http://192.168.50.10:4533/api/v1/tracks/%d/stream?mediaToken=probe", i+1), Title: fmt.Sprintf("Probe & <%d>", i+1), Artist: "032", Album: "Probe", MIME: "audio/flac", DurationMs: 61000, ID: fmt.Sprintf("032-track-%d", i+1)}
	}
	if err := s.appendItems(ctx, list); err != nil {
		t.Fatal(err)
	}
	got, err := s.Queue(ctx)
	_, clearErr := s.av(ctx, "RemoveAllTracksFromQueue")
	if err != nil || clearErr != nil {
		t.Fatal(err, clearErr)
	}
	after, _ := s.av(ctx, "GetMediaInfo")
	t.Logf("queue read back: %d entries; first=%+v last=%+v", len(got), got[0], got[len(got)-1])
	t.Logf("transport before=%s after=%s", before["CurrentURI"], after["CurrentURI"])
	if len(got) != 20 || got[19].URI != list[19].URI || got[0].Title != "Probe & <1>" {
		t.Fatalf("unexpected queue %+v", got)
	}
	if before["CurrentURI"] != after["CurrentURI"] {
		t.Fatal("transport changed")
	}
	rest, _ := s.Queue(ctx)
	if len(rest) != 0 {
		t.Fatalf("queue not cleared: %d", len(rest))
	}
}
