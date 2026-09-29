package storage

import (
	"context"
	"testing"
)

func TestBatch95MigrateCleansOnlyPendingEmptyAlbumCandidatesIdempotently(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	albumID := f.albumID(t, "Album")
	for _, tc := range []struct {
		external, status, tieups string
	}{
		{"empty", "candidate", "[]"},
		{"blank", "candidate", ""},
		{"null", "candidate", "null"},
		{"invalid", "candidate", "{"},
		{"confirmed", "confirmed", "[]"},
		{"rejected", "rejected", "[]"},
		{"pending", "candidate", `[{"subjectId":1}]`},
	} {
		if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_subject_candidates(album_id,source,external_id,title,score,tieups_json,status) VALUES(?,'bangumi',?,?,80,?,?)`, albumID, tc.external, tc.external, tc.tieups, tc.status); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := f.store.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.store.db.QueryContext(f.ctx, `SELECT external_id,status FROM album_subject_candidates ORDER BY external_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var external, status string
		if err = rows.Scan(&external, &status); err != nil {
			t.Fatal(err)
		}
		got[external] = status
	}
	if len(got) != 3 || got["confirmed"] != "confirmed" || got["rejected"] != "rejected" || got["pending"] != "candidate" {
		t.Fatalf("remaining=%v", got)
	}
}
