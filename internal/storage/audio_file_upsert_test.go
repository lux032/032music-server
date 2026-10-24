package storage

import (
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestAudioFileUpsertKeepsTagsOnTargetFile(t *testing.T) {
	f := newAlbumMergeFixture(t)
	for pass := 0; pass < 3; pass++ {
		for album := 0; album < 3; album++ {
			for file := 0; file < 8; file++ {
				path := fmt.Sprintf("album%d/%d.flac", album, file)
				value := fmt.Sprintf("%s-pass%d", path, pass)
				err := f.store.ImportTrack(f.ctx, ImportInput{LibraryID: f.library, RelativePath: path, Metadata: metadata.AudioMetadata{DiscNumber: 1, TrackNumber: 1, Title: path, Album: fmt.Sprintf("Album%d", album), Artists: []string{"Singer"}, Raw: map[string][]string{"TEST": {value}}}})
				if err != nil {
					t.Fatal(err)
				}
				var got string
				if err = f.store.db.QueryRowContext(f.ctx, `SELECT value FROM audio_file_tags tag JOIN audio_files af ON af.id=tag.audio_file_id WHERE af.library_id=? AND af.relative_path=? AND field_name='TEST'`, f.library, path).Scan(&got); err != nil || got != value {
					t.Fatalf("%s: got %q want %q: %v", path, got, value, err)
				}
			}
		}
		if n := f.count(t, `SELECT COUNT(*) FROM audio_file_tags WHERE field_name='TEST'`); n != 24 {
			t.Fatalf("lost other file tags: %d", n)
		}
	}
}
