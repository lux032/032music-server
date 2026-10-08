package storage

import (
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"strings"
	"testing"
)

func TestPerformerBrowseAndCreditDirectory(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Performers", "/performers"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/performers")
	for i, composer := range []string{"Composer", "Singer", "Composer"} {
		if err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%d.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song%d", i), Album: fmt.Sprintf("Album%d", i), Artists: []string{"Singer"}, AlbumArtists: []string{"Album Singer"}, Composer: composer, Lyricist: "Composer", Arranger: "Composer", Producer: "Composer", DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := s.ListArtists(ctx, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, a := range artists {
		ids[a.Name] = a.ID
	}
	for _, tc := range []struct {
		name string
		f    Filters
		want int64
	}{
		{"legacy artist", Filters{ArtistID: ids["Composer"]}, 3},
		{"credit artist", Filters{ArtistID: ids["Composer"], PerformerOnly: true}, 0},
		{"legacy query", Filters{Query: "Composer"}, 3},
		{"credit query", Filters{Query: "Composer", PerformerOnly: true}, 0},
		{"primary query", Filters{Query: "Singer", PerformerOnly: true}, 3},
		{"album artist", Filters{ArtistID: ids["Album Singer"], PerformerOnly: true}, 3},
		{"mixed singer", Filters{ArtistID: ids["Singer"], PerformerOnly: true}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, e := s.CountTracks(ctx, tc.f)
			list, le := s.ListTracks(ctx, tc.f)
			if e != nil || le != nil || n != tc.want || int64(len(list)) != n {
				t.Fatalf("count=%d list=%d err=%v/%v", n, len(list), e, le)
			}
		})
	}
	for _, flag := range []bool{false, true} {
		n, e := s.CountAlbums(ctx, Filters{ArtistID: ids["Composer"], PerformerOnly: flag})
		want := int64(3)
		if flag {
			want = 0
		}
		if e != nil || n != want {
			t.Fatalf("album count %d %v", n, e)
		}
	}
	list, err := s.ListArtists(ctx, Filters{ArtistRole: "track", PerformerOnly: true})
	if err != nil || len(list) != 1 || list[0].ID != ids["Singer"] {
		t.Fatalf("performers=%+v err=%v", list, err)
	}
	if list[0].TrackCount != 3 {
		t.Fatalf("performer card count=%d", list[0].TrackCount)
	}
	for _, name := range []string{"Singer", "Album Singer", "Composer"} {
		d, e := s.ArtistDetail(ctx, ids[name])
		_, total, te := s.ArtistTracks(ctx, ids[name], ArtistTrackQuery{Limit: 100})
		n, ce := s.CountTracks(ctx, Filters{ArtistID: ids[name], PerformerOnly: true})
		if e != nil || te != nil || ce != nil || d.PerformedTrackCount != total || n != total {
			t.Fatalf("%s detail=%d total=%d count=%d errors=%v/%v/%v", name, d.PerformedTrackCount, total, n, e, te, ce)
		}
	}
	opts, err := s.ListArtistOptions(ctx, "performer", "", 20)
	if err != nil || len(opts) != 2 {
		t.Fatalf("options %+v %v", opts, err)
	}
	for _, a := range opts {
		if a.ID == ids["Composer"] {
			t.Fatal("composer in performer options")
		}
	}
	// Historical credit IDs survive a three-level merge chain and deduplicate at its root.
	for _, name := range []string{"Old Composer", "Older Composer"} {
		if _, err = s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES (?,?,?)`, name, name, name); err != nil {
			t.Fatal(err)
		}
	}
	var old, older int64
	s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE display_name='Old Composer'`).Scan(&old)
	s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE display_name='Older Composer'`).Scan(&older)
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=? WHERE id=?`, ids["Composer"], old); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=? WHERE id=?`, old, older); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role,position) SELECT id,?,'composer',9 FROM tracks`, older); err != nil {
		t.Fatal(err)
	}
	roles, e := s.ArtistCreditRoles(ctx, ids["Composer"])
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range roles {
		items, e := s.ListCreditArtists(ctx, CreditArtistFilters{Role: r.Role, Query: "Composer"})
		if e != nil || len(items) != 1 || items[0].ID != ids["Composer"] || items[0].TrackCount != r.Count {
			t.Fatalf("role=%s items=%+v count=%d err=%v", r.Role, items, r.Count, e)
		}
	}
	items, e := s.ListCreditArtists(ctx, CreditArtistFilters{Role: "all", Sort: "tracks", Limit: 1})
	if e != nil || len(items) != 1 || items[0].ID != ids["Composer"] || items[0].TrackCount != 3 {
		t.Fatalf("all=%+v err=%v", items, e)
	}
	n, e := s.CountCreditArtists(ctx, CreditArtistFilters{})
	if e != nil || n != 2 {
		t.Fatalf("count=%d %v", n, e)
	}
	items, e = s.ListCreditArtists(ctx, CreditArtistFilters{Sort: "tracks", Limit: 1, Offset: 1})
	if e != nil || len(items) != 1 || items[0].ID != ids["Singer"] {
		t.Fatalf("page=%+v %v", items, e)
	}
	nameItems, nameErr := s.ListCreditArtists(ctx, CreditArtistFilters{Sort: "name"})
	if nameErr != nil || len(nameItems) != 2 || nameItems[0].Name != "Composer" || nameItems[1].Name != "Singer" {
		t.Fatalf("name sort=%+v %v", nameItems, nameErr)
	}
	items, e = s.ListCreditArtists(ctx, CreditArtistFilters{Index: "C", Query: "Composer"})
	if e != nil || len(items) != 1 {
		t.Fatalf("index=%+v %v", items, e)
	}
	// Log real planner evidence, using the test's temporary database (no repository artifacts).
	for _, query := range []string{
		`SELECT track_id FROM track_artists WHERE role='primary' AND artist_id=1 UNION SELECT t.id FROM album_artists aa JOIN tracks t ON t.album_id=aa.album_id WHERE aa.artist_id=1`,
		creditArtistChain + `SELECT ar.id FROM credits JOIN artists ar ON ar.id=credits.root`,
	} {
		args := []any{}
		if strings.HasPrefix(query, "WITH") {
			args = []any{"all", "all"}
		}
		rows, e := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
				t.Fatal(e)
			}
			t.Log(detail)
		}
		rows.Close()
	}
}
