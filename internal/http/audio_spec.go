package httpapi

import (
	"slices"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

// audioSpecLabel renders the full format tag of one track, e.g.
// "FLAC / 24bit / 48kHz". Codec comes from the audio probe and falls back to
// the file container; bit depth / sample rate are omitted when not probed
// (lossy codecs have no bit depth). Empty when nothing is known.
func audioSpecLabel(track storage.Track) string {
	codec := track.Codec
	if codec == "" {
		codec = track.Container
	}
	return joinAudioSpec(
		strings.ToUpper(strings.TrimSpace(codec)),
		bitDepthLabel(track.BitDepth, track.BitDepth),
		sampleRateLabel(track.SampleRate, track.SampleRate),
	)
}

// albumSpecLabel summarizes the format of an album's tracks. Uniform albums
// read like a single track ("FLAC / 24bit / 48kHz"); mixed ones list the
// codecs and show bit depth / sample rate as ranges
// ("FLAC / 16–24bit / 44.1–96kHz"). fallback (the stored container list) is
// used when no track carries format data.
func albumSpecLabel(tracks []storage.Track, fallback string) string {
	var codecs []string
	minDepth, maxDepth, minRate, maxRate := 0, 0, 0, 0
	for _, track := range tracks {
		codec := track.Codec
		if codec == "" {
			codec = track.Container
		}
		if codec = strings.ToUpper(strings.TrimSpace(codec)); codec != "" && !slices.Contains(codecs, codec) {
			codecs = append(codecs, codec)
		}
		if d := track.BitDepth; d > 0 {
			if minDepth == 0 || d < minDepth {
				minDepth = d
			}
			maxDepth = max(maxDepth, d)
		}
		if r := track.SampleRate; r > 0 {
			if minRate == 0 || r < minRate {
				minRate = r
			}
			maxRate = max(maxRate, r)
		}
	}
	label := joinAudioSpec(strings.Join(codecs, "/"), bitDepthLabel(minDepth, maxDepth), sampleRateLabel(minRate, maxRate))
	if label == "" {
		return fallback
	}
	return label
}

func joinAudioSpec(parts ...string) string {
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, " / ")
}

func bitDepthLabel(lo, hi int) string {
	switch {
	case hi <= 0:
		return ""
	case lo == hi:
		return strconv.Itoa(hi) + "bit"
	default:
		return strconv.Itoa(lo) + "–" + strconv.Itoa(hi) + "bit"
	}
}

func sampleRateLabel(lo, hi int) string {
	khz := func(hz int) string { return strconv.FormatFloat(float64(hz)/1000, 'f', -1, 64) }
	switch {
	case hi <= 0:
		return ""
	case lo == hi:
		return khz(hi) + "kHz"
	default:
		return khz(lo) + "–" + khz(hi) + "kHz"
	}
}
