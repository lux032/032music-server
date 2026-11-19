package httpapi

import "testing"

import "github.com/lux032/032music-server/internal/storage"

func TestArtistLinksDefaultsEmptyJoinPhrase(t *testing.T) {
	feat := " feat. "
	view := artistLinks("detail", []storage.Artist{
		{ID: 1, Name: "A"},
		{ID: 2, Name: "B", JoinPhrase: &feat},
		{ID: 3, Name: "C"},
	}, "", "fallback")
	if view.Mode != "detail" || len(view.Items) != 3 {
		t.Fatalf("view = %+v", view)
	}
	if view.Items[0].JoinPhrase != "" || view.Items[1].JoinPhrase != " feat. " || view.Items[2].JoinPhrase != ", " {
		t.Fatalf("join phrases = %#v %#v %#v", view.Items[0].JoinPhrase, view.Items[1].JoinPhrase, view.Items[2].JoinPhrase)
	}
	refs := artistLinks("compact", []storage.ArtistRef{{ID: 4, Name: "D"}, {ID: 5, Name: "E"}}, "Performed", "Artist")
	if refs.Mode != "compact" || len(refs.Items) != 2 || refs.Items[1].JoinPhrase != ", " || refs.Fallback != "" {
		t.Fatalf("refs = %+v", refs)
	}
	fallback := artistLinks("compact", []storage.Artist{}, "Performed", "Artist")
	if fallback.Fallback != "Performed" || len(fallback.Items) != 0 {
		t.Fatalf("fallback = %+v", fallback)
	}
	secondary := artistLinks("other", []storage.Artist(nil), "", "Artist")
	if secondary.Mode != "compact" || secondary.Fallback != "Artist" {
		t.Fatalf("secondary = %+v", secondary)
	}
}
