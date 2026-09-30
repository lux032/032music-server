package metadata

import (
	"reflect"
	"testing"
)

func TestStructuredArtistNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string][]string
		want []string
	}{
		{"featured", map[string][]string{"ARTIST": {"Aiobahn +81 feat. Mori Calliope"}, "ARTISTS": {"Aiobahn +81", "Mori Calliope"}}, []string{"Aiobahn +81", "Mori Calliope"}},
		{"multiple", map[string][]string{"ARTIST": {"ACE", "CHiCO"}}, []string{"ACE", "CHiCO"}},
		{"comma not heuristic", map[string][]string{"ARTIST": {"ACME, Inc."}}, []string{"ACME, Inc."}},
		{"band", map[string][]string{"ARTIST": {"Simon & Garfunkel"}}, []string{"Simon & Garfunkel"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := applyFallbacks(AudioMetadata{Raw: tc.raw}, "song.flac")
			if !reflect.DeepEqual(result.Artists, tc.want) {
				t.Fatalf("%v want %v", result.Artists, tc.want)
			}
		})
	}
}
