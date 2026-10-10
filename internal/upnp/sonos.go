package upnp

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// sonosBatch is the AddMultipleURIsToQueue chunk size Sonos accepts.
const sonosBatch = 16

// sonosRenderer drives a Sonos zone group through its coordinator. All
// queue state lives on the speaker, so the renderer itself is stateless and
// playback survives server restarts and offline clients.
type sonosRenderer struct {
	dev  Device
	soap soapClient
	uuid string // RINCON_xxx of the coordinator
	// sleep is replaced in tests.
	sleep func(time.Duration)
}

func newSonosRenderer(dev Device, soap soapClient) *sonosRenderer {
	return &sonosRenderer{dev: dev, soap: soap, uuid: dev.ID, sleep: time.Sleep}
}

func (s *sonosRenderer) Device() Device { return s.dev }

func (s *sonosRenderer) av(ctx context.Context, action string, args ...arg) (map[string]string, error) {
	return s.soap.call(ctx, s.dev.baseURL+"/MediaRenderer/AVTransport/Control", svcAVTransport, action,
		append([]arg{{"InstanceID", "0"}}, args...)...)
}

func (s *sonosRenderer) queueURI() string { return "x-rincon-queue:" + s.uuid + "#0" }

func (s *sonosRenderer) Replace(ctx context.Context, items []Item, start int, positionMs int64, play bool) error {
	if _, err := s.av(ctx, "RemoveAllTracksFromQueue"); err != nil {
		return err
	}
	if err := s.appendItems(ctx, items); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	if start < 0 || start >= len(items) {
		start = 0
	}
	if err := s.useQueue(ctx); err != nil {
		return err
	}
	if _, err := s.av(ctx, "Seek", arg{"Unit", "TRACK_NR"}, arg{"Target", itoa(start + 1)}); err != nil {
		return err
	}
	if !play {
		return nil
	}
	if err := s.Play(ctx); err != nil {
		return err
	}
	if positionMs > 0 {
		return s.retryTransition(ctx, func() error { return s.Seek(ctx, positionMs) })
	}
	return nil
}

// appendItems enqueues at the end, 16 per AddMultipleURIsToQueue call. A
// firmware that rejects the batch call falls back to one AddURIToQueue per
// item for the rest of the list.
func (s *sonosRenderer) appendItems(ctx context.Context, items []Item) error {
	for i := 0; i < len(items); i += sonosBatch {
		chunk := items[i:min(i+sonosBatch, len(items))]
		uris := make([]string, len(chunk))
		metas := make([]string, len(chunk))
		for k, it := range chunk {
			uris[k] = it.URI
			metas[k] = it.DIDL()
		}
		_, err := s.av(ctx, "AddMultipleURIsToQueue",
			arg{"UpdateID", "0"},
			arg{"NumberOfURIs", itoa(len(chunk))},
			arg{"EnqueuedURIs", strings.Join(uris, " ")},
			arg{"EnqueuedURIsMetaData", strings.Join(metas, " ")},
			arg{"ContainerURI", ""},
			arg{"ContainerMetaData", ""},
			arg{"DesiredFirstTrackNumberEnqueued", "0"},
			arg{"EnqueueAsNext", "0"},
		)
		if err == nil {
			continue
		}
		if ctx.Err() != nil || UPnPCode(err) == 0 {
			return err
		}
		for _, it := range items[i:] {
			if err := s.addOne(ctx, it, 0); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

// addOne enqueues it at the 1-based position desired (0 = end).
func (s *sonosRenderer) addOne(ctx context.Context, it Item, desired int) error {
	_, err := s.av(ctx, "AddURIToQueue",
		arg{"EnqueuedURI", it.URI},
		arg{"EnqueuedURIMetaData", it.DIDL()},
		arg{"DesiredFirstTrackNumberEnqueued", itoa(desired)},
		arg{"EnqueueAsNext", "0"},
	)
	return err
}

func (s *sonosRenderer) Insert(ctx context.Context, items []Item, at int) error {
	length, active, err := s.queueInfo(ctx)
	if err != nil {
		return err
	}
	if at < 0 || at >= length {
		err = s.appendItems(ctx, items)
	} else {
		for k, it := range items {
			if err = s.addOne(ctx, it, at+1+k); err != nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	// Queueing onto an idle speaker should start it, like the web player.
	if length == 0 || !active {
		state, _ := s.av(ctx, "GetTransportInfo")
		if transportState(state["CurrentTransportState"]) == StateStopped {
			start := length // first inserted item
			if at >= 0 && at < length {
				start = at
			}
			return s.PlayIndex(ctx, start)
		}
	}
	return nil
}

func (s *sonosRenderer) Remove(ctx context.Context, index int) error {
	_, err := s.av(ctx, "RemoveTrackFromQueue", arg{"ObjectID", "Q:0/" + itoa(index+1)}, arg{"UpdateID", "0"})
	return err
}

func (s *sonosRenderer) Move(ctx context.Context, from, to int) error {
	if from == to {
		return nil
	}
	// InsertBefore counts positions in the queue before the move.
	insertBefore := to + 1
	if to > from {
		insertBefore = to + 2
	}
	_, err := s.av(ctx, "ReorderTracksInQueue",
		arg{"StartingIndex", itoa(from + 1)},
		arg{"NumberOfTracks", "1"},
		arg{"InsertBefore", itoa(insertBefore)},
		arg{"UpdateID", "0"},
	)
	return err
}

// useQueue points the transport at the speaker's queue (it may be playing
// a radio station or line-in from the Sonos app).
func (s *sonosRenderer) useQueue(ctx context.Context) error {
	_, err := s.av(ctx, "SetAVTransportURI", arg{"CurrentURI", s.queueURI()}, arg{"CurrentURIMetaData", ""})
	return err
}

func (s *sonosRenderer) queueInfo(ctx context.Context) (length int, active bool, err error) {
	media, err := s.av(ctx, "GetMediaInfo")
	if err != nil {
		return 0, false, err
	}
	active = strings.HasPrefix(media["CurrentURI"], "x-rincon-queue:")
	if active {
		length, _ = strconv.Atoi(media["NrTracks"])
		return length, true, nil
	}
	// NrTracks describes the current source, not the queue: count via Browse.
	res, err := s.browseQueue(ctx, 0, 1)
	if err != nil {
		return 0, false, err
	}
	length, _ = strconv.Atoi(res["TotalMatches"])
	return length, false, nil
}

func (s *sonosRenderer) PlayIndex(ctx context.Context, index int) error {
	_, active, err := s.queueInfo(ctx)
	if err != nil {
		return err
	}
	if !active {
		if err := s.useQueue(ctx); err != nil {
			return err
		}
	}
	if err := s.retryTransition(ctx, func() error {
		_, err := s.av(ctx, "Seek", arg{"Unit", "TRACK_NR"}, arg{"Target", itoa(index + 1)})
		return err
	}); err != nil {
		return err
	}
	return s.Play(ctx)
}

// retryTransition retries an action that Sonos refuses with 701 ("transition
// not available") while it is still TRANSITIONING after Play/SetURI.
func (s *sonosRenderer) retryTransition(ctx context.Context, action func() error) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = action(); err == nil || UPnPCode(err) != 701 || ctx.Err() != nil {
			return err
		}
		s.sleep(300 * time.Millisecond)
	}
	return err
}

func (s *sonosRenderer) Play(ctx context.Context) error {
	_, err := s.av(ctx, "Play", arg{"Speed", "1"})
	return err
}

func (s *sonosRenderer) Pause(ctx context.Context) error {
	_, err := s.av(ctx, "Pause")
	return err
}

func (s *sonosRenderer) Stop(ctx context.Context) error {
	_, err := s.av(ctx, "Stop")
	return err
}

func (s *sonosRenderer) Next(ctx context.Context) error {
	_, err := s.av(ctx, "Next")
	return err
}

func (s *sonosRenderer) Previous(ctx context.Context) error {
	_, err := s.av(ctx, "Previous")
	return err
}

func (s *sonosRenderer) Seek(ctx context.Context, positionMs int64) error {
	_, err := s.av(ctx, "Seek", arg{"Unit", "REL_TIME"}, arg{"Target", formatDuration(positionMs)})
	return err
}

func (s *sonosRenderer) SetPlayMode(ctx context.Context, loop string, shuffle bool) error {
	mode := "NORMAL"
	switch {
	case shuffle && loop == LoopAll:
		mode = "SHUFFLE"
	case shuffle && loop == LoopOne:
		mode = "SHUFFLE_REPEAT_ONE"
	case shuffle:
		mode = "SHUFFLE_NOREPEAT"
	case loop == LoopAll:
		mode = "REPEAT_ALL"
	case loop == LoopOne:
		mode = "REPEAT_ONE"
	}
	_, err := s.av(ctx, "SetPlayMode", arg{"NewPlayMode", mode})
	return err
}

func (s *sonosRenderer) Status(ctx context.Context) (Status, error) {
	transport, err := s.av(ctx, "GetTransportInfo")
	if err != nil {
		return Status{}, err
	}
	position, err := s.av(ctx, "GetPositionInfo")
	if err != nil {
		return Status{}, err
	}
	media, err := s.av(ctx, "GetMediaInfo")
	if err != nil {
		return Status{}, err
	}
	st := Status{
		State:      transportState(transport["CurrentTransportState"]),
		Index:      -1,
		TrackURI:   position["TrackURI"],
		PositionMs: parseDuration(position["RelTime"]),
		DurationMs: parseDuration(position["TrackDuration"]),
	}
	if entries := parseDIDL(position["TrackMetaData"]); len(entries) > 0 {
		st.Title = entries[0].Title
	}
	st.QueueActive = strings.HasPrefix(media["CurrentURI"], "x-rincon-queue:")
	if st.QueueActive {
		st.QueueLength, _ = strconv.Atoi(media["NrTracks"])
		if n, convErr := strconv.Atoi(position["Track"]); convErr == nil && n > 0 {
			st.Index = n - 1
		}
	}
	return st, nil
}

func (s *sonosRenderer) browseQueue(ctx context.Context, start, count int) (map[string]string, error) {
	return s.soap.call(ctx, s.dev.baseURL+"/MediaServer/ContentDirectory/Control", svcContentDirectory, "Browse",
		arg{"ObjectID", "Q:0"},
		arg{"BrowseFlag", "BrowseDirectChildren"},
		arg{"Filter", "dc:title,res"},
		arg{"StartingIndex", itoa(start)},
		arg{"RequestedCount", itoa(count)},
		arg{"SortCriteria", ""},
	)
}

func (s *sonosRenderer) Queue(ctx context.Context) ([]Item, error) {
	var items []Item
	for start := 0; start < 10000; {
		res, err := s.browseQueue(ctx, start, 100)
		if err != nil {
			return nil, err
		}
		entries := parseDIDL(res["Result"])
		for _, e := range entries {
			items = append(items, Item{Title: e.Title, URI: e.URI})
		}
		total, _ := strconv.Atoi(res["TotalMatches"])
		returned, _ := strconv.Atoi(res["NumberReturned"])
		start += returned
		if returned == 0 || start >= total {
			break
		}
	}
	return items, nil
}

func (s *sonosRenderer) Volume(ctx context.Context) (int, error) {
	res, err := s.soap.call(ctx, s.dev.baseURL+"/MediaRenderer/GroupRenderingControl/Control", svcGroupRendering, "GetGroupVolume", arg{"InstanceID", "0"})
	if err != nil {
		res, err = s.soap.call(ctx, s.dev.baseURL+"/MediaRenderer/RenderingControl/Control", svcRenderingControl, "GetVolume", arg{"InstanceID", "0"}, arg{"Channel", "Master"})
		if err != nil {
			return 0, err
		}
	}
	v, _ := strconv.Atoi(res["CurrentVolume"])
	return v, nil
}

func (s *sonosRenderer) SetVolume(ctx context.Context, volume int) error {
	v := itoa(clampVolume(volume))
	_, err := s.soap.call(ctx, s.dev.baseURL+"/MediaRenderer/GroupRenderingControl/Control", svcGroupRendering, "SetGroupVolume", arg{"InstanceID", "0"}, arg{"DesiredVolume", v})
	if err != nil {
		_, err = s.soap.call(ctx, s.dev.baseURL+"/MediaRenderer/RenderingControl/Control", svcRenderingControl, "SetVolume", arg{"InstanceID", "0"}, arg{"Channel", "Master"}, arg{"DesiredVolume", v})
	}
	return err
}
