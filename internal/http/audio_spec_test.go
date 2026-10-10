package httpapi

import (
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func specTrack(codec, container string, depth, rate int) storage.Track {
	t := storage.Track{Container: container}
	t.Codec, t.BitDepth, t.SampleRate = codec, depth, rate
	return t
}

func TestAudioSpecLabel(t *testing.T) {
	cases := []struct {
		track storage.Track
		want  string
	}{
		{specTrack("flac", "flac", 24, 48000), "FLAC / 24bit / 48kHz"},
		{specTrack("flac", "flac", 16, 44100), "FLAC / 16bit / 44.1kHz"},
		{specTrack("alac", "m4a", 24, 96000), "ALAC / 24bit / 96kHz"},
		{specTrack("mp3", "mp3", 0, 44100), "MP3 / 44.1kHz"},
		{specTrack("", "flac", 0, 0), "FLAC"},
		{specTrack("", "", 0, 0), ""},
	}
	for _, c := range cases {
		if got := audioSpecLabel(c.track); got != c.want {
			t.Errorf("audioSpecLabel(%+v) = %q, want %q", c.track.TrackExtras, got, c.want)
		}
	}
}

func TestAlbumSpecLabel(t *testing.T) {
	uniform := []storage.Track{specTrack("flac", "flac", 24, 48000), specTrack("flac", "flac", 24, 48000)}
	if got := albumSpecLabel(uniform, "FLAC"); got != "FLAC / 24bit / 48kHz" {
		t.Errorf("uniform = %q", got)
	}
	mixed := []storage.Track{specTrack("flac", "flac", 16, 44100), specTrack("flac", "flac", 24, 96000), specTrack("mp3", "mp3", 0, 44100)}
	if got := albumSpecLabel(mixed, ""); got != "FLAC/MP3 / 16–24bit / 44.1–96kHz" {
		t.Errorf("mixed = %q", got)
	}
	if got := albumSpecLabel(nil, "FLAC,MP3"); got != "FLAC,MP3" {
		t.Errorf("fallback = %q", got)
	}
}
