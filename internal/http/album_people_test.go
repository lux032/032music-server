package httpapi

import (
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestAlbumPeopleMergesPerformersAndCredits(t *testing.T) {
	tracks := []storage.Track{
		{ID: 1, DiscNumber: 1, DurationMillis: 60000,
			Artists: []storage.Artist{{ID: 10, Name: "Singer", ImageURL: "/img/10"}},
			Credits: []storage.TrackCredit{{Role: "composer", Artists: []storage.ArtistRef{{ID: 20, Name: "Writer"}, {ID: 10, Name: "Singer"}}}}},
		{ID: 2, DiscNumber: 1, DurationMillis: 30000,
			Artists: []storage.Artist{{ID: 10, Name: "Singer"}},
			Credits: []storage.TrackCredit{{Role: "lyricist", Artists: []storage.ArtistRef{{ID: 20, Name: "Writer"}}}}},
		{ID: 3, DiscNumber: 2, DurationMillis: 0,
			Credits: []storage.TrackCredit{{Role: "arranger", Artists: []storage.ArtistRef{{ID: 30, Name: "  "}}}}},
	}
	people := albumPeople(tracks)
	if len(people) != 2 {
		t.Fatalf("want 2 people (blank names skipped), got %+v", people)
	}
	singer, writer := people[0], people[1]
	if singer.ID != 10 || singer.URL != "/admin/artists/10" || singer.Roles != "演唱 · 作曲" || singer.Tracks != 2 || singer.ImageURL != "/img/10" {
		t.Fatalf("performer row wrong: %+v", singer)
	}
	if writer.ID != 20 || writer.URL != "/admin/credits/20?role=composer" || writer.Roles != "作曲 · 作词" || writer.Tracks != 2 || writer.Initial != "W" {
		t.Fatalf("credit row wrong: %+v", writer)
	}
	if got := tracksDuration(tracks); got != 90000 {
		t.Fatalf("tracksDuration = %d", got)
	}
	if got := discSummary(tracks, 1); got != "2 首 · 1:30" {
		t.Fatalf("discSummary(1) = %q", got)
	}
	if got := discSummary(tracks, 2); got != "1 首" {
		t.Fatalf("discSummary(2) = %q", got)
	}
}

func TestNameInitialSkipsPunctuation(t *testing.T) {
	cases := map[string]string{"(K)NoW_NAME": "K", "[ahi:]": "A", "8082Audio": "8", "のみこ": "の", "+": "+", "": ""}
	for in, want := range cases {
		if got := nameInitial(in); got != want {
			t.Errorf("nameInitial(%q) = %q, want %q", in, got, want)
		}
	}
}
