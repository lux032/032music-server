package storage

import (
	"context"
	"testing"
)

// TestTrackEffectiveGenreOrder forces creation order and alphabetical order
// to disagree with the positions entered by the user.
func TestTrackEffectiveGenreOrder(t *testing.T) {
	fx := newBrowseFixture(t)
	album := fx.album(t, "Ordering", 2020, "Singer", []string{"Singer"})
	fx.genre(t, "Alpha")
	fx.genre(t, "Beta")
	fx.genre(t, "Zulu")
	id := fx.track(t, "Ordered", album, 1, []string{"Zulu", "Beta", "Alpha"}, nil)
	ctx := context.Background()
	assertGenre := func(source string, got Track, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got.Genres != "Zulu,Beta,Alpha" {
			t.Errorf("%s genres = %q", source, got.Genres)
		}
	}
	list, err := fx.store.ListTracks(ctx, Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list length = %d", len(list))
	}
	assertGenre("list", list[0], nil)
	track, err := fx.store.TrackByID(ctx, id)
	assertGenre("by ID", track, err)
	artistTracks, _, err := fx.store.ArtistTracks(ctx, fx.artists["Singer"])
	if err != nil {
		t.Fatal(err)
	}
	if len(artistTracks) != 1 {
		t.Fatalf("artist tracks length = %d", len(artistTracks))
	}
	assertGenre("artist", artistTracks[0], nil)
	work, err := fx.store.CreateWork(ctx, WorkInput{Title: "Ordering Work", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.store.AddWorkTrack(ctx, work.ID, WorkTrackInput{TrackID: id, Role: "op"}); err != nil {
		t.Fatal(err)
	}
	workTracks, err := fx.store.TracksForWork(ctx, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workTracks) != 1 {
		t.Fatalf("work tracks length = %d", len(workTracks))
	}
	assertGenre("work", workTracks[0].Track, nil)
	// Override order also disagrees with both the raw order and genre IDs.
	for pos, name := range []string{"Beta", "Zulu"} {
		if _, err := fx.store.db.Exec(`INSERT INTO track_genre_overrides(track_id,genre_id,position) VALUES(?,?,?)`, id, fx.genres[name], pos); err != nil {
			t.Fatal(err)
		}
	}
	list, err = fx.store.ListTracks(ctx, Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Genres != "Beta,Zulu" {
		t.Errorf("override list genres = %q", list[0].Genres)
	}
	track, err = fx.store.TrackByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if track.Genres != "Beta,Zulu" {
		t.Errorf("override by ID genres = %q", track.Genres)
	}
	artistTracks, _, err = fx.store.ArtistTracks(ctx, fx.artists["Singer"])
	if err != nil {
		t.Fatal(err)
	}
	if artistTracks[0].Genres != "Beta,Zulu" {
		t.Errorf("override artist genres = %q", artistTracks[0].Genres)
	}
	workTracks, err = fx.store.TracksForWork(ctx, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workTracks[0].Genres != "Beta,Zulu" {
		t.Errorf("override work genres = %q", workTracks[0].Genres)
	}
}
