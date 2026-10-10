package upnp

import (
	"context"
	"errors"
)

// Device kinds.
const (
	KindSonos = "sonos"
	KindDLNA  = "dlna"
)

// Device is one controllable output. For Sonos it is a zone group, always
// addressed through its coordinator.
type Device struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Model        string `json:"model,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Host         string `json:"host"`
	MemberCount  int    `json:"memberCount"`
	// Queue reports a device-side play queue (Sonos). Generic renderers get
	// a server-side queue instead.
	Queue bool `json:"queue"`

	baseURL             string // http://host:port
	avTransportURL      string
	renderingControlURL string
}

// Transport states reported by Status.
const (
	StatePlaying   = "playing"
	StatePaused    = "paused"
	StateStopped   = "stopped"
	StateBuffering = "buffering"
)

// Status is a renderer snapshot. Index is the 0-based queue position of the
// current track, -1 when the renderer is not playing our queue.
type Status struct {
	State       string `json:"state"`
	Index       int    `json:"index"`
	TrackURI    string `json:"trackUri,omitempty"`
	Title       string `json:"title,omitempty"`
	PositionMs  int64  `json:"positionMs"`
	DurationMs  int64  `json:"durationMs"`
	QueueLength int    `json:"queueLength"`
	QueueActive bool   `json:"queueActive"`
}

// Play modes (loop) understood by SetPlayMode.
const (
	LoopOff = "off"
	LoopAll = "all"
	LoopOne = "one"
)

// ErrUnsupported is returned for actions a renderer cannot perform.
var ErrUnsupported = errors.New("upnp: action not supported by this renderer")

// Renderer is the queue-oriented control surface shared by the Sonos and
// the generic implementations. Indices are 0-based.
type Renderer interface {
	Device() Device
	// Replace swaps the whole queue and starts items[start] at positionMs.
	Replace(ctx context.Context, items []Item, start int, positionMs int64, play bool) error
	// Insert adds items before index at (at < 0 or ≥ length appends).
	Insert(ctx context.Context, items []Item, at int) error
	Remove(ctx context.Context, index int) error
	Move(ctx context.Context, from, to int) error
	PlayIndex(ctx context.Context, index int) error
	Play(ctx context.Context) error
	Pause(ctx context.Context) error
	Stop(ctx context.Context) error
	Next(ctx context.Context) error
	Previous(ctx context.Context) error
	Seek(ctx context.Context, positionMs int64) error
	SetPlayMode(ctx context.Context, loop string, shuffle bool) error
	Status(ctx context.Context) (Status, error)
	// Queue lists the renderer's current queue as (title, URI) entries.
	Queue(ctx context.Context) ([]Item, error)
	Volume(ctx context.Context) (int, error)
	SetVolume(ctx context.Context, volume int) error
}

func transportState(raw string) string {
	switch raw {
	case "PLAYING":
		return StatePlaying
	case "PAUSED_PLAYBACK", "PAUSED_RECORDING":
		return StatePaused
	case "TRANSITIONING":
		return StateBuffering
	default:
		return StateStopped
	}
}

func clampVolume(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
