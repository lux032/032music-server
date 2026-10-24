package metadata

import (
	"reflect"
	"testing"
)

func TestMusicBrainzFileIDs(t *testing.T) {
	const a = "12345678-1234-1234-1234-123456789abc"
	const b = "87654321-1234-1234-1234-123456789abc"
	for _, key := range []string{"MUSICBRAINZ_RELEASEGROUPID", " musicbrainz release group id ", "MusicBrainz_ReleaseGroupId"} {
		groups, releases := MusicBrainzFileIDs(map[string][]string{key: {" ", "bad", " 12345678-1234-1234-1234-123456789ABC ", a, b}, "musicbrainz album id": {a, b}, "MUSICBRAINZ_ALBUMID": {a}})
		if !reflect.DeepEqual(groups, []string{a, b}) || !reflect.DeepEqual(releases, []string{a, b}) {
			t.Fatalf("%s: %v %v", key, groups, releases)
		}
	}
	groups, _ := MusicBrainzFileIDs(map[string][]string{"RELEASEGROUPID": {a}, "MUSICBRAINZ_RELEASE_GROUP_ID": {a}})
	if len(groups) != 0 {
		t.Fatal("guessed aliases accepted")
	}
}
