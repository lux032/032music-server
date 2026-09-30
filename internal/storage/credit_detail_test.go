package storage

import (
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"strings"
	"testing"
)

func TestCreditDetailStatistics(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Credits", "/credits"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/credits")
	for i := 0; i < 65; i++ {
		singer := "Singer B"
		if i < 3 {
			singer = "Singer A"
		}
		if i == 4 {
			singer = "Creator"
		}
		err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%d.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song%d", i), Album: fmt.Sprintf("Album%02d", i), Year: 1900 + i, Artists: []string{singer}, AlbumArtists: []string{singer}, Composer: "Creator", Lyricist: "Creator", TrackNumber: 1, DiscNumber: 1}})
		if err != nil {
			t.Fatal(err)
		}
	}
	id := func(name string) int64 {
		var n int64
		if err := s.db.QueryRowContext(ctx, "SELECT id FROM artists WHERE display_name=?", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	creator, a, b := id("Creator"), id("Singer A"), id("Singer B")
	// One fallback track; a historical performer ID resolves to Singer A.
	var first int64
	s.db.QueryRowContext(ctx, "SELECT id FROM tracks ORDER BY id LIMIT 1").Scan(&first)
	if _, err := s.db.ExecContext(ctx, "DELETE FROM track_artists WHERE track_id=? AND role='primary'", first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO artists(display_name,sort_name,identity_key,merged_into_artist_id) VALUES('Historical A','Historical A','historical-a',?)", a); err != nil {
		t.Fatal(err)
	}
	old := id("Historical A")
	if _, err := s.db.ExecContext(ctx, "INSERT INTO track_artists(track_id,artist_id,role,position) SELECT track_id,?,'primary',99 FROM track_artists WHERE artist_id=? AND role='primary'", old, a); err != nil {
		t.Fatal(err)
	}
	people, err := s.CreditCollaborators(ctx, creator, 50)
	if err != nil || len(people) != 2 || people[0].ID != b || people[0].TrackCount != 61 || people[1].ID != a || people[1].TrackCount != 3 {
		t.Fatalf("people %+v %v", people, err)
	}
	n, err := s.CreditSelfPerformedCount(ctx, creator)
	if err != nil || n != 1 {
		t.Fatalf("self %d %v", n, err)
	}
	albums, total, err := s.CreditAlbums(ctx, creator, 100)
	if err != nil || total != 65 || len(albums) != 60 || albums[0].Year != 1964 || albums[59].Year != 1905 {
		t.Fatalf("albums %d %d %v", len(albums), total, err)
	}
	small, total, err := s.CreditAlbums(ctx, creator, 2)
	if err != nil || len(small) != 2 || total != 65 {
		t.Fatal(len(small), total, err)
	}
	any := Filters{Focus: TrackFocus{Credits: []CreditFilter{{Role: "any", ArtistID: creator}}}}
	count, err := s.CountTracks(ctx, any)
	if err != nil || count != 65 {
		t.Fatal(count, err)
	}
	// Explain the actual shared CTE. Verify all three existing indexes are used.
	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+creditDetailTracks+creditDetailPerformers+`SELECT COUNT(DISTINCT track_id) FROM resolved WHERE next IS NULL AND id=?`, creator, creator)
	if err != nil {
		t.Fatal(err)
	}
	plan := ""
	for rows.Next() {
		var x, y, z int
		var text string
		if err := rows.Scan(&x, &y, &z, &text); err != nil {
			t.Fatal(err)
		}
		plan += text + "\n"
	}
	rows.Close()
	t.Log(plan)
	for _, index := range []string{"idx_track_artists_role", "idx_track_artists_track_role", "idx_artists_merged_into"} {
		if !strings.Contains(plan, index) {
			t.Fatalf("missing %s: %s", index, plan)
		}
	}
}
