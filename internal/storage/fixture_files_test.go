package storage

import (
	"testing"
)

// giveTracksFiles gives every track that has no audio_files row an available
// file, as a scan would have. Browse queries hide tracks without an available
// file (visibility.go), so raw-SQL fixtures must call this before asserting.
func giveTracksFiles(t testing.TB, store *Store) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,container,mime_type,status) SELECT a.library_id,t.id,'fixture/'||t.id||'.flac',1,1,'flac','audio/flac','available' FROM tracks t JOIN albums a ON a.id=t.album_id WHERE NOT EXISTS(SELECT 1 FROM audio_files af WHERE af.track_id=t.id)`); err != nil {
		t.Fatal(err)
	}
}
