package storage

import (
	"encoding/json"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestArtistCanonicalDisplayNameRules(t *testing.T) {
	for _, tc := range []struct {
		name     string
		local    string
		profile  ExternalArtistProfile
		wantName string
		wantOK   bool
	}{
		{
			name:     "japanese romanized to mb kanji main name",
			local:    "Yuki Kajiura",
			profile:  ExternalArtistProfile{Source: "musicbrainz", DisplayName: "梶浦由記", Country: "JP"},
			wantName: "梶浦由記",
			wantOK:   true,
		},
		{
			name:     "chinese romanized to mb han main name",
			local:    "Jay Chou",
			profile:  ExternalArtistProfile{Source: "musicbrainz", DisplayName: "周杰倫", Country: "TW"},
			wantName: "周杰倫",
			wantOK:   true,
		},
		{
			name:     "korean romanized to mb hangul main name",
			local:    "IU",
			profile:  ExternalArtistProfile{Source: "musicbrainz", DisplayName: "아이유", Country: "KR"},
			wantName: "아이유",
			wantOK:   true,
		},
		{
			name:  "jp artist latin mb main name falls back to primary ja artist alias",
			local: "Kenshi Yonezu",
			profile: ExternalArtistProfile{
				Source:      "musicbrainz",
				DisplayName: "Kenshi Yonezu",
				Country:     "JP",
				StructuredAliases: []StructuredArtistAlias{
					{Name: "米津玄師", Locale: "ja", Primary: true, Type: "Artist name", Ended: false},
				},
			},
			wantName: "米津玄師",
			wantOK:   true,
		},
		{
			name:  "official latin stage name LiSA ignores japanese legal name alias",
			local: "LiSA",
			profile: ExternalArtistProfile{
				Source:      "musicbrainz",
				DisplayName: "LiSA",
				Country:     "JP",
				StructuredAliases: []StructuredArtistAlias{
					{Name: "織部里沙", Locale: "ja", Primary: true, Type: "Legal name", Ended: false},
					{Name: "リサ", Locale: "ja", Primary: true, Type: "Search hint", Ended: false},
				},
			},
			wantOK: false,
		},
		{
			name:    "official latin stage name YOASOBI stays latin",
			local:   "YOASOBI",
			profile: ExternalArtistProfile{Source: "musicbrainz", DisplayName: "YOASOBI", Country: "JP"},
			wantOK:  false,
		},
		{
			name:    "western artist stays latin",
			local:   "Taylor Swift",
			profile: ExternalArtistProfile{Source: "musicbrainz", DisplayName: "Taylor Swift", Country: "US"},
			wantOK:  false,
		},
		{
			name:    "already native local name untouched",
			local:   "梶浦由記",
			profile: ExternalArtistProfile{Source: "musicbrainz", DisplayName: "梶浦由記", Country: "JP"},
			wantOK:  false,
		},
		{
			name:    "ended primary ja alias ignored",
			local:   "Old Alias",
			profile: ExternalArtistProfile{Source: "musicbrainz", DisplayName: "Old Alias", Country: "JP", StructuredAliases: []StructuredArtistAlias{{Name: "旧名義", Locale: "ja", Primary: true, Type: "Artist name", Ended: true}}},
			wantOK:  false,
		},
		{
			name:    "lastfm source ignored",
			local:   "Yuki Kajiura",
			profile: ExternalArtistProfile{Source: "lastfm", DisplayName: "梶浦由記", Country: "JP"},
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveCanonicalNativeName(tc.local, tc.profile)
			if ok != tc.wantOK || got != tc.wantName {
				t.Fatalf("ResolveCanonicalNativeName(%q) = (%q, %v), want (%q, %v)", tc.local, got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

func TestArtistCanonicalDisplayNameLifecycleSearchRescanAndStartupBackfill(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	var libID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM libraries WHERE root_path='/music'`).Scan(&libID); err != nil {
		t.Fatalf("library id: %v", err)
	}

	// Import a Romanized track for Yuki Kajiura and one for Hiroyuki Sawano (manual override).
	for i, name := range []string{"Yuki Kajiura", "Hiroyuki Sawano", "Kenshi Yonezu"} {
		if err := s.ImportTrack(ctx, ImportInput{
			LibraryID:    libID,
			RelativePath: name + ".flac",
			FileSize:     100,
			ModifiedAtNS: int64(i + 1),
			Metadata: metadata.AudioMetadata{
				Title:        "Song " + name,
				Album:        "Album " + name,
				DiscNumber:   1,
				TrackNumber:  i + 1,
				Artists:      []string{name},
				AlbumArtists: []string{name},
			},
		}); err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
	}

	idByKey := func(key string) int64 {
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key=?`, key).Scan(&id); err != nil {
			t.Fatalf("lookup artist %q: %v", key, err)
		}
		return id
	}
	yukiID := idByKey("yuki kajiura")
	sawanoID := idByKey("hiroyuki sawano")
	yonezuID := idByKey("kenshi yonezu")

	// 1. Set manual user_display_name on Hiroyuki Sawano -> must never be overridden.
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET user_display_name='泽野弘之(自定义)' WHERE id=?`, sawanoID); err != nil {
		t.Fatalf("set user_display_name: %v", err)
	}

	// 2. Confirm MB profile for Yuki Kajiura -> automatically renames display_name, keeps identity_key, indexes alias.
	if err := s.UpsertExternalArtistProfile(ctx, yukiID, ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  "11111111-1111-4111-8111-111111111111",
		DisplayName: "梶浦由記",
		Country:     "JP",
		Aliases:     []string{"Yuki Kajiura"},
	}); err != nil {
		t.Fatalf("upsert yuki profile: %v", err)
	}

	detail, err := s.ArtistDetail(ctx, yukiID)
	if err != nil {
		t.Fatalf("ArtistDetail yuki: %v", err)
	}
	if detail.Name != "梶浦由記" {
		t.Fatalf("expected canonical name 梶浦由記, got %q", detail.Name)
	}
	var identityKey, origName string
	if err = s.db.QueryRowContext(ctx, `SELECT identity_key, COALESCE(original_display_name,'') FROM artists WHERE id=?`, yukiID).Scan(&identityKey, &origName); err != nil {
		t.Fatalf("query artist columns: %v", err)
	}
	if identityKey != "yuki kajiura" || origName != "Yuki Kajiura" {
		t.Fatalf("expected identity_key='yuki kajiura' original='Yuki Kajiura', got %q / %q", identityKey, origName)
	}

	// Unified search still finds the artist by original Romanized name.
	res, err := s.Search(ctx, SearchOptions{Query: "Yuki Kajiura", Limit: 10})
	if err != nil {
		t.Fatalf("UnifiedSearch: %v", err)
	}
	found := false
	for _, a := range res.Artists.Items {
		if a.ID == yukiID && a.Name == "梶浦由記" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unified search for 'Yuki Kajiura' to find canonical artist 梶浦由記, got %+v", res.Artists.Items)
	}

	// Rescanning another track tagged "Yuki Kajiura" reuses the exact same artist row (no duplicate!).
	if err = s.ImportTrack(ctx, ImportInput{
		LibraryID:    libID,
		RelativePath: "Yuki Kajiura 2.flac",
		FileSize:     120,
		ModifiedAtNS: 10,
		Metadata: metadata.AudioMetadata{
			Title:        "Song 2",
			Album:        "Album 2",
			DiscNumber:   1,
			TrackNumber:  1,
			Artists:      []string{"Yuki Kajiura"},
			AlbumArtists: []string{"Yuki Kajiura"},
		},
	}); err != nil {
		t.Fatalf("import second yuki track: %v", err)
	}
	var yukiRows int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE merged_into_artist_id IS NULL`).Scan(&yukiRows); err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if yukiRows != 3 {
		t.Fatalf("expected still 3 canonical artists after rescan, got %d", yukiRows)
	}

	// 3. ResetArtistIdentity restores original_display_name and removes original_tag alias.
	if err = s.ResetArtistIdentity(ctx, yukiID, "musicbrainz", "11111111-1111-4111-8111-111111111111"); err != nil {
		t.Fatalf("ResetArtistIdentity: %v", err)
	}
	detail, err = s.ArtistDetail(ctx, yukiID)
	if err != nil {
		t.Fatalf("ArtistDetail after reset: %v", err)
	}
	if detail.Name != "Yuki Kajiura" {
		t.Fatalf("expected restored name 'Yuki Kajiura', got %q", detail.Name)
	}
	var origTagCount int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artist_names WHERE artist_id=? AND name_type='original_tag'`, yukiID).Scan(&origTagCount); err != nil || origTagCount != 0 {
		t.Fatalf("expected original_tag alias removed, got count=%d err=%v", origTagCount, err)
	}

	// 4. Manual user_display_name wins over canonical rename.
	if err = s.UpsertExternalArtistProfile(ctx, sawanoID, ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  "22222222-2222-4222-8222-222222222222",
		DisplayName: "澤野弘之",
		Country:     "JP",
	}); err != nil {
		t.Fatalf("upsert sawano profile: %v", err)
	}
	sawanoDetail, err := s.ArtistDetail(ctx, sawanoID)
	if err != nil || sawanoDetail.Name != "泽野弘之(自定义)" {
		t.Fatalf("expected user_display_name preserved, got %q err=%v", sawanoDetail.Name, err)
	}

	// 5. Disable toggle -> new confirmation does NOT rename; re-enable + startup backfill converges offline (including JP structured alias).
	if err = s.SaveArtistIdentitySettings(ctx, ArtistIdentitySettings{CanonicalNativeNameEnabled: false}); err != nil {
		t.Fatalf("disable canonical naming: %v", err)
	}
	rawJSON, _ := json.Marshal(map[string]any{
		"structuredAliases": []StructuredArtistAlias{
			{Name: "米津玄師", Locale: "ja", Primary: true, Type: "Artist name", Ended: false},
		},
	})
	if err = s.UpsertExternalArtistProfile(ctx, yonezuID, ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  "33333333-3333-4333-8333-333333333333",
		DisplayName: "Kenshi Yonezu",
		Country:     "JP",
		Raw:         rawJSON,
	}); err != nil {
		t.Fatalf("upsert yonezu profile while disabled: %v", err)
	}
	yonezuDetail, _ := s.ArtistDetail(ctx, yonezuID)
	if yonezuDetail.Name != "Kenshi Yonezu" {
		t.Fatalf("expected name unchanged while setting disabled, got %q", yonezuDetail.Name)
	}

	// Re-enable and trigger startup offline backfill.
	if err = s.SaveArtistIdentitySettings(ctx, ArtistIdentitySettings{CanonicalNativeNameEnabled: true}); err != nil {
		t.Fatalf("re-enable canonical naming: %v", err)
	}
	updated, err := s.ConvergeCanonicalArtistNames(ctx)
	if err != nil {
		t.Fatalf("ConvergeCanonicalArtistNames: %v", err)
	}
	if updated != 1 {
		t.Fatalf("expected 1 artist converged offline, got %d", updated)
	}
	yonezuDetail, _ = s.ArtistDetail(ctx, yonezuID)
	if yonezuDetail.Name != "米津玄師" {
		t.Fatalf("expected offline startup convergence to 米津玄師, got %q", yonezuDetail.Name)
	}
	// Repeat convergence is idempotent (0 changes).
	updatedAgain, err := s.ConvergeCanonicalArtistNames(ctx)
	if err != nil || updatedAgain != 0 {
		t.Fatalf("expected idempotent 0 on repeat backfill, got %d err=%v", updatedAgain, err)
	}
}
