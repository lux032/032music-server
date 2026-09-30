package storage

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestTrackFocusDimensionsAndCombinations(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Focus", "/focus"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/focus")
	codecs := []string{"flac", "flac", "alac", "opus", "", "mp3"}
	bits := []int{16, 24, 16, 0, 0, 0}
	rates := []int{44100, 96000, 48000, 48000, 0, 44100}
	for i := range codecs {
		year := 2015
		if i >= 3 {
			year = 2020
		}
		kind := "regular"
		if i == 5 {
			kind = "instrumental"
		}
		err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%d.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song%d", i), Album: fmt.Sprintf("Album%d", i), Year: year, Artists: []string{"Singer"}, Composer: "Composer", Lyricist: "Writer", Arranger: "Arranger", TrackType: kind, DiscNumber: 1, TrackNumber: 1}, AudioProps: metadata.AudioProps{Codec: codecs[i], BitDepth: bits[i], SampleRate: rates[i]}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var composer, writer, arranger int64
	for name, id := range map[string]*int64{"Composer": &composer, "Writer": &writer, "Arranger": &arranger} {
		if err := s.db.QueryRowContext(ctx, "SELECT id FROM artists WHERE display_name=?", name).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		focus TrackFocus
		want  int
	}{
		{"decade", TrackFocus{Decades: []int{2010}}, 3}, {"decades OR", TrackFocus{Decades: []int{2010, 2020}}, 6}, {"range", TrackFocus{YearFrom: 2020, YearTo: 2020}, 3},
		{"include", TrackFocus{TrackTypes: []string{"instrumental"}}, 1}, {"exclude", TrackFocus{ExcludeTypes: []string{"instrumental", "off_vocal"}}, 5},
		{"lossless", TrackFocus{Quality: "lossless"}, 3}, {"hires", TrackFocus{Quality: "hires"}, 2}, {"lossy", TrackFocus{Quality: "lossy"}, 2}, {"format", TrackFocus{Formats: []string{"flac", "opus"}}, 3},
		{"credits AND", TrackFocus{Credits: []CreditFilter{{"composer", composer}, {"lyricist", writer}}}, 6}, {"missing credit AND", TrackFocus{Credits: []CreditFilter{{"composer", composer}, {"arranger", writer}}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			list, e := s.ListTracks(ctx, Filters{Focus: tc.focus})
			n, ce := s.CountTracks(ctx, Filters{Focus: tc.focus})
			if e != nil || ce != nil || len(list) != tc.want || n != int64(tc.want) {
				t.Fatalf("list=%d count=%d want=%d errors %v %v", len(list), n, tc.want, e, ce)
			}
		})
	}
	dimensions := []TrackFocus{{Decades: []int{2010}}, {TrackTypes: []string{"regular"}}, {ExcludeTypes: []string{"off_vocal", "instrumental"}}, {Formats: []string{"flac"}}, {Quality: "hires"}, {Credits: []CreditFilter{{"arranger", arranger}}}}
	for i := range dimensions {
		for j := i + 1; j < len(dimensions); j++ {
			f := TrackFocus{Decades: dimensions[i].Decades, TrackTypes: dimensions[i].TrackTypes, ExcludeTypes: dimensions[i].ExcludeTypes, Formats: dimensions[i].Formats, Quality: dimensions[i].Quality, Credits: dimensions[i].Credits}
			g := dimensions[j]
			if g.TrackTypes != nil {
				f.TrackTypes = g.TrackTypes
			}
			if g.ExcludeTypes != nil {
				f.ExcludeTypes = g.ExcludeTypes
			}
			if g.Formats != nil {
				f.Formats = g.Formats
			}
			if g.Quality != "" {
				f.Quality = g.Quality
			}
			if g.Credits != nil {
				f.Credits = g.Credits
			}
			list, e := s.ListTracks(ctx, Filters{Focus: f})
			n, ce := s.CountTracks(ctx, Filters{Focus: f})
			if e != nil || ce != nil || int64(len(list)) != n {
				t.Fatalf("pair %d/%d: %d %d %v %v", i, j, len(list), n, e, ce)
			}
		}
	}
	// Same-row constraint: Game OP plus Anime insert must not match Anime OP.
	tracks, _ := s.ListTracks(ctx, Filters{})
	id := tracks[0].ID
	for _, q := range []string{"INSERT INTO works(title,normalized_title,type) VALUES ('Game','game','game'),('Anime','anime','anime')", fmt.Sprintf("INSERT INTO work_tracks(work_id,track_id,role,source) SELECT id,%d,CASE type WHEN 'game' THEN 'op' ELSE 'insert' END,'manual' FROM works", id)} {
		if _, e := s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct {
		roles, types []string
		want         int
	}{{[]string{"op"}, []string{"anime"}, 0}, {[]string{"op"}, []string{"game"}, 1}, {nil, []string{"anime"}, 1}, {[]string{"insert"}, nil, 1}} {
		f := Filters{Focus: TrackFocus{TieupRoles: tc.roles, WorkTypes: tc.types}}
		list, e := s.ListTracks(ctx, f)
		n, ce := s.CountTracks(ctx, f)
		if e != nil || ce != nil || len(list) != tc.want || n != int64(tc.want) {
			t.Fatalf("tieup list=%d total=%d %v %v", len(list), n, e, ce)
		}
	}
	// A second available file can qualify a previously unprobed track.
	var emptyID int64
	s.db.QueryRowContext(ctx, "SELECT id FROM tracks WHERE title='Song4'").Scan(&emptyID)
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(track_id,library_id,relative_path,file_size,modified_at_ns,status,codec,bit_depth,sample_rate) VALUES (?,?,'second.flac',1,1,'available','flac',24,96000)`, emptyID, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CountTracks(ctx, Filters{Focus: TrackFocus{Quality: "hires"}})
	if err != nil || n != 3 {
		t.Fatalf("multi file %d %v", n, err)
	}
	// Verify index usage on the migrated database and retain raw planner evidence.
	for _, f := range []TrackFocus{{Credits: []CreditFilter{{"composer", composer}}}, {TieupRoles: []string{"op"}, WorkTypes: []string{"anime"}}} {
		where, args := trackWhere(Filters{Focus: f})
		rows, e := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN SELECT t.id FROM tracks t JOIN albums a ON a.id=t.album_id WHERE "+where, args...)
		if e != nil {
			t.Fatal(e)
		}
		plan := []string{}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
				t.Fatal(e)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		t.Log(strings.Join(plan, "\n"))
		if len(f.Credits) > 0 && !strings.Contains(strings.Join(plan, "\n"), "idx_track_artists_role") {
			t.Fatal("credit predicate did not use role index")
		}
	}
}

func TestTrackCreditsMergedIDsAndPrimaryAlbumArtists(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	s.EnsureLibrary(ctx, "Credits", "/credits")
	lib, _ := s.LibraryByRoot(ctx, "/credits")
	if e := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, Composer: "Old", Lyricist: "Writer", Arranger: "Arranger", Producer: "Producer", DiscNumber: 1, TrackNumber: 1}}); e != nil {
		t.Fatal(e)
	}
	var old int64
	s.db.QueryRowContext(ctx, "SELECT id FROM artists WHERE display_name='Old'").Scan(&old)
	result, e := s.db.ExecContext(ctx, "INSERT INTO artists(identity_key,display_name,sort_name) VALUES ('middle','Middle','Middle')")
	if e != nil {
		t.Fatal(e)
	}
	middle, _ := result.LastInsertId()
	result, e = s.db.ExecContext(ctx, "INSERT INTO artists(identity_key,display_name,sort_name) VALUES ('current','Current','Current')")
	if e != nil {
		t.Fatal(e)
	}
	current, _ := result.LastInsertId()
	s.db.ExecContext(ctx, "UPDATE artists SET merged_into_artist_id=? WHERE id=?", middle, old)
	s.db.ExecContext(ctx, "UPDATE artists SET merged_into_artist_id=? WHERE id=?", current, middle)
	all, _ := s.ListTracks(ctx, Filters{})
	_, e = s.db.ExecContext(ctx, "INSERT INTO track_artists(track_id,artist_id,role,position) VALUES (?,?,'composer',9)", all[0].ID, current)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []int64{old, current} {
		list, e := s.ListTracks(ctx, Filters{Focus: TrackFocus{Credits: []CreditFilter{{"composer", id}}}})
		if e != nil || len(list) != 1 {
			t.Fatalf("merge filter %v %v", list, e)
		}
		c := list[0].Credits
		if len(c) != 4 || c[1].Role != "composer" || len(c[1].Artists) != 1 || c[1].Artists[0].ID != current {
			t.Fatalf("credits %#v", c)
		}
	}
	for _, id := range []int64{old, current} {
		roles, err := s.ArtistCreditRoles(ctx, id)
		if err != nil || len(roles) != 1 || roles[0].Role != "composer" || roles[0].Count != 1 {
			t.Fatalf("merged role counts %v %v", roles, err)
		}
	}
	artists, e := s.ArtistsForAlbumTracks(ctx, all[0].AlbumID)
	if e != nil || len(artists[all[0].ID]) != 1 || artists[all[0].ID][0].Name != "Singer" {
		t.Fatalf("primary: %v %v", artists, e)
	}
	options, e := s.ListArtistOptions(ctx, "composer", "", 20)
	if e != nil || len(options) != 1 || options[0].ID != current {
		t.Fatalf("options %v %v", options, e)
	}
}

func TestListTracksEffectiveTrackTypeMatchesFilters(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Types", "/types"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/types")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, TrackType: "instrumental", DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ stored, override, want string }{{"instrumental", "", "instrumental"}, {"", "", "regular"}, {"instrumental", "regular", "regular"}} {
		if _, err := s.db.ExecContext(ctx, "UPDATE tracks SET track_type=?,user_track_type=?", tc.stored, tc.override); err != nil {
			t.Fatal(err)
		}
		f := Filters{Focus: TrackFocus{TrackTypes: []string{tc.want}}}
		items, err := s.ListTracks(ctx, f)
		if err != nil || len(items) != 1 || items[0].TrackType != tc.want {
			t.Fatalf("types %+v %v", items, err)
		}
		count, err := s.CountTracks(ctx, f)
		if err != nil || count != 1 {
			t.Fatalf("count %d %v", count, err)
		}
	}
}
