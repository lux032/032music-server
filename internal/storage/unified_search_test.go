package storage

import (
	"context"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"path/filepath"
	"testing"
)

func searchTestStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	return s, lib.ID
}

func searchTestImport(t *testing.T, s *Store, lib int64, path, title, album, artist, albumArtist, composer string) {
	t.Helper()
	err := s.ImportTrack(context.Background(), ImportInput{LibraryID: lib, RelativePath: path, FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: album, Artists: []string{artist}, AlbumArtists: []string{albumArtist}, Composer: composer, DiscNumber: 1, TrackNumber: 1}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnifiedSearchAliasesRelationshipsAndRanking(t *testing.T) {
	s, lib := searchTestStore(t)
	ctx := context.Background()
	searchTestImport(t, s, lib, "solo.flac", "Stellar Stellar", "ZZ Solo", "星街すいせい", "星街すいせい", "")
	searchTestImport(t, s, lib, "guest.flac", "Guest Duet", "AA Compilation", "星街すいせい", "Various Artists", "")
	searchTestImport(t, s, lib, "other.flac", "Other Song", "AA Compilation", "Other Singer", "Various Artists", "")
	searchTestImport(t, s, lib, "composed.flac", "Composed Only", "Composed Album", "Other Singer", "Other Singer", "星街すいせい")
	searchTestImport(t, s, lib, "title.flac", "星街彗星への手紙", "星街彗星 tribute", "Other Singer", "Other Singer", "")
	artists, err := s.ListArtists(ctx, Filters{Query: "星街すいせい"})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%+v %v", artists, err)
	}
	id := artists[0].ID
	if err = s.UpsertExternalArtistProfile(ctx, id, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "suisei", DisplayName: "星街すいせい", Aliases: []string{"星街彗星", "Hoshimachi Suisei"}}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Search(ctx, SearchOptions{Query: "星街彗星"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Artists.Total != 1 || result.Composers.Total != 1 || result.Tracks.Total != 3 || result.Albums.Total != 3 {
		t.Fatalf("unexpected groups: %+v", result)
	}
	if result.BestMatch == nil || result.BestMatch.Type != "artists" || result.BestMatch.ID != id {
		t.Fatalf("best=%+v", result.BestMatch)
	}
	if result.Albums.Items[0].Title != "星街彗星 tribute" || result.Albums.Items[1].Title != "ZZ Solo" {
		t.Fatalf("order=%+v", result.Albums.Items)
	}
	for _, v := range result.Tracks.Items {
		if v.Title == "Other Song" || v.Title == "Composed Only" {
			t.Fatalf("unrelated track %+v", v)
		}
	}
	matched, err := s.ListArtists(ctx, Filters{Query: "星街彗星"})
	if err != nil || len(matched) != 1 {
		t.Fatalf("browse aliases=%+v %v", matched, err)
	}
	composers, err := s.ListCreditArtists(ctx, CreditArtistFilters{Role: "composer", Query: "星街彗星"})
	if err != nil || len(composers) != 1 {
		t.Fatalf("composer aliases=%+v %v", composers, err)
	}
	result, err = s.Search(ctx, SearchOptions{Query: "Stellar Stellar"})
	if err != nil || result.BestMatch == nil || result.BestMatch.Type != "tracks" {
		t.Fatalf("track best=%+v err=%v", result.BestMatch, err)
	}
	result, err = s.Search(ctx, SearchOptions{Query: "ZZ Solo"})
	if err != nil || result.BestMatch == nil || result.BestMatch.Type != "albums" {
		t.Fatalf("album best=%+v err=%v", result.BestMatch, err)
	}
}

func TestUnifiedSearchPagingDoesNotTruncateArtistExpansion(t *testing.T) {
	s, lib := searchTestStore(t)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		searchTestImport(t, s, lib, fmt.Sprintf("%d.flac", i), fmt.Sprintf("Song %d", i), fmt.Sprintf("Album %d", i), fmt.Sprintf("Shared Singer %d", i), fmt.Sprintf("Shared Singer %d", i), "")
	}
	preview, err := s.Search(ctx, SearchOptions{Query: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Artists.Items) != 5 || preview.Artists.Total != 8 || preview.Albums.Total != 8 || preview.Tracks.Total != 8 {
		t.Fatalf("truncated expansion=%+v", preview)
	}
	seen := map[int64]bool{}
	for offset := 0; offset < 8; offset += 3 {
		result, err := s.Search(ctx, SearchOptions{Query: "Shared", Category: "albums", Limit: 3, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if result.Albums.Total != 8 || result.Albums.Offset != offset {
			t.Fatalf("page=%+v", result.Albums)
		}
		for _, v := range result.Albums.Items {
			if seen[v.ID] {
				t.Fatalf("duplicate across pages %d", v.ID)
			}
			seen[v.ID] = true
		}
	}
	if len(seen) != 8 {
		t.Fatal(len(seen))
	}
	beyond, err := s.Search(ctx, SearchOptions{Query: "Shared", Category: "albums", Limit: 3, Offset: 100})
	if err != nil || beyond.Albums.Total != 8 || len(beyond.Albums.Items) != 0 {
		t.Fatalf("beyond-end page=%+v err=%v", beyond.Albums, err)
	}

	// The preview must not choose arbitrarily between equal artist matches.
	if preview.BestMatch != nil {
		t.Fatalf("ambiguous artist best=%+v", preview.BestMatch)
	}
}

func TestSearchAliasPersistenceMergeAndLiteralMatching(t *testing.T) {
	s, lib := searchTestStore(t)
	ctx := context.Background()
	searchTestImport(t, s, lib, "source.flac", "One", "One Album", "Source", "Source", "")
	searchTestImport(t, s, lib, "target.flac", "Two", "Two Album", "Target", "Target", "")
	a, _ := s.ListArtists(ctx, Filters{Query: "Source"})
	source := a[0].ID
	a, _ = s.ListArtists(ctx, Filters{Query: "Target"})
	target := a[0].ID
	if err := s.AddArtistSearchAlias(ctx, source, "  星街彗星  "); err != nil {
		t.Fatal(err)
	}
	if err := s.AddArtistSearchAlias(ctx, source, `100%_\`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"星街彗星", "星街", `%_\`} {
		r, err := s.Search(ctx, SearchOptions{Query: q})
		if err != nil || r.Artists.Total != 1 {
			t.Fatalf("q=%q result=%+v err=%v", q, r, err)
		}
	}
	if err := s.UpsertExternalArtistProfile(ctx, source, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "source", DisplayName: "Source", Aliases: []string{"External Alias"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertExternalArtistProfile(ctx, source, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "source", DisplayName: "Source"}); err != nil {
		t.Fatal(err)
	}
	operation, err := s.MergeArtists(ctx, source, target)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Search(ctx, SearchOptions{Query: "星街彗星"})
	if err != nil || r.Artists.Total != 1 || r.Artists.Items[0].ID != target {
		t.Fatalf("merged search=%+v err=%v", r, err)
	}
	aliases, err := s.ArtistSearchAliases(ctx, target)
	if err != nil || len(aliases) != 2 {
		t.Fatalf("aliases=%+v %v", aliases, err)
	}
	if err := s.RollbackArtistMerge(ctx, operation); err != nil {
		t.Fatal(err)
	}
	r, err = s.Search(ctx, SearchOptions{Query: "星街彗星"})
	if err != nil || r.Artists.Items[0].ID != source {
		t.Fatalf("rollback=%+v %v", r, err)
	}
	if err := s.DeleteArtistSearchAlias(ctx, target, aliases[0].ID); err == nil {
		t.Fatal("deleted another artist's alias")
	}
	// Search-only aliases must not enter the identity matching input.
	detail, err := s.ArtistDetail(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range detail.Aliases {
		if alias == "星街彗星" {
			t.Fatal("search alias leaked into identity names")
		}
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE audio_files SET status='missing'`); err != nil {
		t.Fatal(err)
	}
	r, err = s.Search(ctx, SearchOptions{Query: "星街彗星"})
	if err != nil || r.Artists.Total != 0 || r.Albums.Total != 0 || r.Tracks.Total != 0 {
		t.Fatalf("missing content visible: %+v %v", r, err)
	}
}

func TestUnifiedSearchBoundProfilesOnlyAndComposerRoles(t *testing.T) {
	s, lib := searchTestStore(t)
	ctx := context.Background()
	searchTestImport(t, s, lib, "vocal.flac", "Vocal", "Solo", "Singer", "Singer", "")
	searchTestImport(t, s, lib, "credit.flac", "Composed Song", "Credit Album", "Other", "Other", "Composer")
	artists, err := s.ListArtists(ctx, Filters{Query: "Singer"})
	if err != nil {
		t.Fatal(err)
	}
	singer := artists[0].ID
	composers, err := s.ListArtists(ctx, Filters{Query: "Composer"})
	if err != nil {
		t.Fatal(err)
	}
	composer := composers[0].ID
	// A pending candidate's translated name is never evidence for local search.
	if err = s.AddArtistCandidates(ctx, singer, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "candidate", DisplayName: "尚未确认", Score: 80}}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Search(ctx, SearchOptions{Query: "尚未确认"})
	if err != nil || result.Artists.Total != 0 {
		t.Fatalf("candidate leaked: %+v %v", result, err)
	}
	if err = s.UpsertExternalArtistProfile(ctx, singer, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "bound", DisplayName: "Singer", Aliases: []string{"已绑定译名"}}); err != nil {
		t.Fatal(err)
	}
	// Inherited external profiles participate without copying aliases into identity tables.
	operation, err := s.MergeArtists(ctx, singer, composer)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.Search(ctx, SearchOptions{Query: "已绑定译名"})
	if err != nil || result.Artists.Total != 1 || result.Artists.Items[0].ID != composer || result.Composers.Total != 1 {
		t.Fatalf("inherited profile: %+v %v", result, err)
	}
	if err = s.RollbackArtistMerge(ctx, operation); err != nil {
		t.Fatal(err)
	}
	result, err = s.Search(ctx, SearchOptions{Query: "Composer"})
	if err != nil || result.Artists.Total != 0 || result.Composers.Total != 1 || result.Tracks.Total != 0 || result.Albums.Total != 0 {
		t.Fatalf("composer-only result: %+v %v", result, err)
	}
	if result.BestMatch == nil || result.BestMatch.Type != "composers" {
		t.Fatalf("composer best: %+v", result.BestMatch)
	}
}
