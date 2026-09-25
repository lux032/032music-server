package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestTrackExtrasOverrideAndArtistRoles(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: lib.ID, RelativePath: "track.flac", FileSize: 123, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, Composer: "Composer", Lyricist: "Writer", DiscNumber: 1, TrackNumber: 1, DurationMillis: 1000, Lyrics: "[00:01]lyrics"}, AudioProps: metadata.AudioProps{Codec: "flac", SampleRate: 96000, BitDepth: 24}}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	list, err := s.SyncTracks(ctx, SyncTracksParams{})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("sync: %+v %v", list, err)
	}
	id := list.Items[0].ID
	for _, get := range []func(context.Context) (string, error){func(ctx context.Context) (string, error) { v, e := s.TrackByID(ctx, id); return v.Artist, e }, func(ctx context.Context) (string, error) {
		v, e := s.SyncTracks(ctx, SyncTracksParams{})
		if e != nil {
			return "", e
		}
		return v.Items[0].Artist, nil
	}} {
		v, e := get(ctx)
		if e != nil || v != "Singer" {
			t.Fatalf("artist %q %v", v, e)
		}
	}
	raw, _ := json.Marshal(list.Items[0])
	for _, key := range []string{`"artist"`, `"title"`, `"album"`, `"streamUrl"`, `"codec"`, `"lyricsUrl"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("missing %s: %s", key, raw)
		}
	}
	for _, key := range []string{`"viewCount"`, `"lastViewedAt"`, `"skipCount"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("unexpected %s: %s", key, raw)
		}
	}
	full, err := s.TrackByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	fullJSON, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(fullJSON, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "albumId", "title", "album", "artist", "genres", "composer", "lyricist", "arranger", "trackType", "container", "mimeType", "relativePath", "artworkUrl", "year", "discNumber", "trackNumber", "fileSize", "durationMillis", "streamUrl", "addedAt", "updatedAt", "positionMillis", "playCount", "isFavorite", "codec", "sampleRate", "bitDepth", "lyricsUrl"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("legacy/new track key %s absent from %s", key, fullJSON)
		}
	}
	for _, key := range []string{"viewCount", "lastViewedAt", "skipCount"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("unplayed track includes %s: %s", key, fullJSON)
		}
	}
	if err = s.UpdateTrack(ctx, id, "", 2, 9, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	track, err := s.TrackByID(ctx, id)
	if err != nil || track.DiscNumber != 2 || track.TrackNumber != 9 {
		t.Fatalf("rescan override: %+v %v", track, err)
	}
	needed, err := s.AudioProbeNeeded(ctx, lib.ID, "track.flac")
	if err != nil || needed {
		t.Fatalf("probe already imported: %v %v", needed, err)
	}
	if err = s.Scrobble(ctx, id, 100, 1000); err != nil {
		t.Fatal(err)
	}
	track, err = s.TrackByID(ctx, id)
	if err != nil || track.ViewCount == nil || track.LastViewedAt == nil || *track.LastViewedAt < 1600000000 {
		t.Fatalf("playback extras: %+v %v", track, err)
	}
}

func TestAudioProbeVersionAndLyricsFlag(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, DurationMillis: 1000, DiscNumber: 1}, HasExternalLRC: true}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = s.db.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE relative_path=?`, input.RelativePath).Scan(&id); err != nil {
		t.Fatal(err)
	}
	track, err := s.TrackByID(ctx, id)
	if err != nil || track.LyricsURL == "" {
		t.Fatalf("external lyrics: %+v %v", track, err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE audio_files SET audio_probe_version=0 WHERE relative_path=?`, input.RelativePath); err != nil {
		t.Fatal(err)
	}
	needed, err := s.AudioProbeNeeded(ctx, lib.ID, input.RelativePath)
	if err != nil || !needed {
		t.Fatalf("legacy probe: %v %v", needed, err)
	}
	if err = s.UpdateAudioProbe(ctx, lib.ID, input.RelativePath, metadata.AudioProps{Codec: "flac", SampleRate: 44100}, false); err != nil {
		t.Fatal(err)
	}
	needed, err = s.AudioProbeNeeded(ctx, lib.ID, input.RelativePath)
	if err != nil || needed {
		t.Fatalf("one-time probe: %v %v", needed, err)
	}
	track, err = s.TrackByID(ctx, id)
	if err != nil || track.LyricsURL != "" || track.Codec != "flac" {
		t.Fatalf("updated probe: %+v %v", track, err)
	}
}

func TestUpdateTrackMatchingOriginalClearsOverrides(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 10, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", DiscNumber: 1, TrackNumber: 3, DurationMillis: 1000}}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	v, err := s.SyncTracks(ctx, SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := v.Items[0].ID
	if err = s.UpdateTrack(ctx, id, "", 2, 4, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateTrack(ctx, id, "", 1, 3, "", "", nil); err != nil {
		t.Fatal(err)
	}
	var disc, number sql.NullInt64
	if err = s.db.QueryRowContext(ctx, `SELECT user_disc_number,user_track_number FROM tracks WHERE id=?`, id).Scan(&disc, &number); err != nil {
		t.Fatal(err)
	}
	if disc.Valid || number.Valid {
		t.Fatalf("matching original should clear overrides: %v %v", disc, number)
	}
	input.Metadata.DiscNumber = 3
	input.Metadata.TrackNumber = 8
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	track, err := s.TrackByID(ctx, id)
	if err != nil || track.DiscNumber != 3 || track.TrackNumber != 8 {
		t.Fatalf("tag correction ignored: %+v %v", track, err)
	}
}
