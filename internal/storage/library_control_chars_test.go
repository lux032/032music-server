package storage

import (
	"context"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

// D-9/D62: raw tag values may carry NUL separators and C0 control bytes.
func TestRawFirstCleansControlCharacters(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"leading NULs", "\x00\x00\x00\x00ARCD0012", "ARCD0012"},
		{"trailing NUL", "ARCD0012\x00", "ARCD0012"},
		{"only NULs", "\x00\x00\x00", ""},
		{"NUL inside japanese", "前\x00後", "前"},
		{"tab folds to space", "ARCD\t0012", "ARCD 0012"},
		{"other C0 stripped", "\x01\x02Label\x1f", "Label"},
		{"plain", "SVWC-70658", "SVWC-70658"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rawFirst(map[string][]string{"CATALOGNUMBER": {tc.value}}, "CATALOGNUMBER"); got != tc.want {
				t.Fatalf("rawFirst(%q)=%q, want %q", tc.value, got, tc.want)
			}
		})
	}
	// A first value that cleans to empty falls through to the next value of
	// the same key; the next key is only consulted when the first key has no
	// values at all.
	if got := rawFirst(map[string][]string{"LABEL": {"\x00", "Fallback"}}, "LABEL", "PUBLISHER"); got != "Fallback" {
		t.Fatalf("multi-value fallback=%q", got)
	}
	if got := rawFirst(map[string][]string{"LABEL": {"\x00"}, "PUBLISHER": {"Publisher"}}, "LABEL", "PUBLISHER"); got != "" {
		t.Fatalf("key order changed: %q", got)
	}
	if got := rawFirst(map[string][]string{"PUBLISHER": {"\x00Publisher\x00"}}, "LABEL", "PUBLISHER"); got != "Publisher" {
		t.Fatalf("second key=%q", got)
	}
}

// Tag-derived album fields are cleaned on import, and a rescan with a
// missing tag replaces a known-dirty stored value instead of keeping it.
func TestImportTrackCleansAndRepairsDirtyAlbumTags(t *testing.T) {
	f := newAlbumMergeFixture(t)
	ctx := f.ctx
	input := ImportInput{LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{
		"CATALOGNUMBER": {"\x00\x00\x00\x00ARCD0012"},
		"LABEL":         {"\x00My Label\tX"},
		"VERSION":       {"限定版\x00"},
	}}}
	if err := f.store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, "Album")
	var label, catalog, version string
	if err := f.store.db.QueryRowContext(ctx, `SELECT label,catalog_number,version FROM albums WHERE id=?`, albumID).Scan(&label, &catalog, &version); err != nil {
		t.Fatal(err)
	}
	if label != "My Label X" || catalog != "ARCD0012" || version != "限定版" {
		t.Fatalf("label=%q catalog=%q version=%q", label, catalog, version)
	}
	// Rescan without tags: clean stored values survive (tag-missing keeps the
	// old value).
	input.ModifiedAtNS = 2
	input.Metadata.Raw = nil
	if err := f.store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRowContext(ctx, `SELECT label,catalog_number FROM albums WHERE id=?`, albumID).Scan(&label, &catalog); err != nil {
		t.Fatal(err)
	}
	if label != "My Label X" || catalog != "ARCD0012" {
		t.Fatalf("clean values overwritten: label=%q catalog=%q", label, catalog)
	}
	// A stored value still carrying control characters is known-dirty: the
	// rescan's empty value replaces it with NULL instead of keeping it.
	if _, err := f.store.db.ExecContext(ctx, `UPDATE albums SET catalog_number=CAST(x'000044495259' AS TEXT) WHERE id=?`, albumID); err != nil {
		t.Fatal(err)
	}
	input.ModifiedAtNS = 3
	if err := f.store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRowContext(ctx, `SELECT label,catalog_number FROM albums WHERE id=?`, albumID).Scan(&label, &catalog); err == nil {
		t.Fatalf("dirty catalog survived as %q", catalog)
	}
	if label != "My Label X" {
		t.Fatalf("clean label lost: %q", label)
	}
}

// The Migrate-time repair cleans rows imported before the reader-side fix,
// is idempotent, and never touches user_* columns.
func TestMigrateCleansAlbumTagControlCharsIdempotent(t *testing.T) {
	f := newAlbumMergeFixture(t)
	ctx := context.Background()
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	albumID := f.albumID(t, "Album")
	if _, err := f.store.db.ExecContext(ctx, `UPDATE albums SET label=CAST(x'00004C6162656C' AS TEXT),catalog_number=CAST(x'000000004152434430303132' AS TEXT),version='v1'||char(9)||'x',country=CAST(x'00' AS TEXT),user_title='用户'||CAST(x'00' AS TEXT)||'标题',updated_at='2001-01-01T00:00:00.000Z' WHERE id=?`, albumID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var label, version, userTitle string
	var catalog, country *string
	if err := f.store.db.QueryRowContext(ctx, `SELECT label,catalog_number,version,country,user_title FROM albums WHERE id=?`, albumID).Scan(&label, &catalog, &version, &country, &userTitle); err != nil {
		t.Fatal(err)
	}
	if label != "Label" || catalog == nil || *catalog != "ARCD0012" || version != "v1 x" || country != nil {
		t.Fatalf("label=%q catalog=%v version=%q country=%v", label, catalog, version, country)
	}
	if userTitle != "用户\x00标题" {
		t.Fatalf("user_title must stay untouched, got %q", userTitle)
	}
	// Second run is a no-op: nothing matches the dirty predicate, so even
	// updated_at stays put.
	if _, err := f.store.db.ExecContext(ctx, `UPDATE albums SET updated_at='2002-02-02T00:00:00.000Z' WHERE id=?`, albumID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var updated string
	if err := f.store.db.QueryRowContext(ctx, `SELECT updated_at FROM albums WHERE id=?`, albumID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != "2002-02-02T00:00:00.000Z" {
		t.Fatalf("second migrate rewrote the row: %s", updated)
	}
}

// D-19: the reader-side rawFirst closure (lyricist/arranger/producer/sort
// names) cleaned nothing before this batch, so track-level tag-derived
// columns and the tag-derived artist reading name may hold NUL/C0 bytes.
// Migrate repairs them idempotently; user_* columns stay untouched.
func TestMigrateCleansTrackTagControlCharsIdempotent(t *testing.T) {
	f := newAlbumMergeFixture(t)
	ctx := context.Background()
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	var trackID, artistID int64
	if err := f.store.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE title='Song'`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE display_name='Singer'`).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(ctx, `UPDATE tracks SET lyricist=CAST(x'0000E4BD9CE8AF8D' AS TEXT),arranger='arr'||char(9)||'x',reading_title=CAST(x'00' AS TEXT),user_title='用户'||CAST(x'00' AS TEXT)||'曲',updated_at='2001-01-01T00:00:00.000Z' WHERE id=?`, trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(ctx, `UPDATE artists SET reading_name=CAST(x'000073696E676572' AS TEXT),updated_at='2001-01-01T00:00:00.000Z' WHERE id=?`, artistID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var lyricist, arranger, userTitle string
	var readingTitle *string
	if err := f.store.db.QueryRowContext(ctx, `SELECT lyricist,arranger,reading_title,user_title FROM tracks WHERE id=?`, trackID).Scan(&lyricist, &arranger, &readingTitle, &userTitle); err != nil {
		t.Fatal(err)
	}
	if lyricist != "作词" || arranger != "arr x" || readingTitle != nil {
		t.Fatalf("lyricist=%q arranger=%q reading_title=%v", lyricist, arranger, readingTitle)
	}
	if userTitle != "用户\x00曲" {
		t.Fatalf("user_title must stay untouched, got %q", userTitle)
	}
	var readingName *string
	if err := f.store.db.QueryRowContext(ctx, `SELECT reading_name FROM artists WHERE id=?`, artistID).Scan(&readingName); err != nil {
		t.Fatal(err)
	}
	if readingName == nil || *readingName != "singer" {
		t.Fatalf("reading_name=%v", readingName)
	}
	// Second run is a no-op: nothing matches the dirty predicate, so even
	// updated_at stays put.
	if _, err := f.store.db.ExecContext(ctx, `UPDATE tracks SET updated_at='2002-02-02T00:00:00.000Z' WHERE id=?`, trackID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var updated string
	if err := f.store.db.QueryRowContext(ctx, `SELECT updated_at FROM tracks WHERE id=?`, trackID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != "2002-02-02T00:00:00.000Z" {
		t.Fatalf("second migrate rewrote the row: %s", updated)
	}
}
