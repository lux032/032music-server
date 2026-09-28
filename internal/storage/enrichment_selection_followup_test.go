package storage

import "testing"

func makeBoundReferencedWork(t *testing.T, f albumMergeFixture, title, external string, raw string) int64 {
	t.Helper()
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: title, Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE works SET origin='auto',type_locked=0 WHERE id=?`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,type,raw_json,fetched_at) VALUES(?,'bangumi',?,?,'anime',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, work.ID, external, title, raw); err != nil {
		t.Fatal(err)
	}
	f.importFile(t, title+"/01.flac", title, "Song", 1, 1)
	album := f.albumID(t, title)
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source) VALUES(?,?,'other','bangumi')`, album, work.ID); err != nil {
		t.Fatal(err)
	}
	return work.ID
}

func TestDeleteWorkRemovesProvenance(t *testing.T) {
	f := newAlbumMergeFixture(t)
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Delete Me", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.RecordBangumiTypeCorrectionEvidence(f.ctx, work.ID, "50", "test", 0); err != nil {
		t.Fatal(err)
	}
	if err = f.store.DeleteWork(f.ctx, work.ID); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM enrichment_provenance WHERE entity_type='work' AND entity_id=?`, work.ID); n != 0 {
		t.Fatalf("provenance left=%d", n)
	}
}

func TestWorksForEnrichmentSkipsCorrectionEvidenceUnlessForcedOrTypeEdited(t *testing.T) {
	f := newAlbumMergeFixture(t)
	id := makeBoundReferencedWork(t, f, "Conflict", "51", `{"id":51,"type":2,"platform":"剧场版"}`)
	if err := f.store.RecordBangumiTypeCorrectionEvidence(f.ctx, id, "51", "identity conflict", 0); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.WorksForEnrichment(f.ctx, "bangumi", false, 0, id); err != nil || len(got) != 0 {
		t.Fatalf("non-force=%+v err=%v", got, err)
	}
	if got, err := f.store.WorksForEnrichment(f.ctx, "bangumi", true, 0, id); err != nil || len(got) != 1 {
		t.Fatalf("force=%+v err=%v", got, err)
	}
	if _, err := f.store.UpdateWork(f.ctx, id, WorkInput{Title: "Conflict", Type: "game"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type_correction_skipped'`, id); n != 0 {
		t.Fatalf("evidence not cleared=%d", n)
	}
	if got, err := f.store.WorksForEnrichment(f.ctx, "bangumi", false, 0, id); err != nil || len(got) != 1 {
		t.Fatalf("after edit=%+v err=%v", got, err)
	}
}

func TestSuccessfulTypeCorrectionClearsSkippedEvidence(t *testing.T) {
	f := newAlbumMergeFixture(t)
	id := makeBoundReferencedWork(t, f, "Collision Clear", "54", `{"id":54,"type":2,"platform":"剧场版"}`)
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE works SET year=2024 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	collision, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Collision Clear", Type: "movie", Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := f.store.CorrectBangumiWorkType(f.ctx, id, "anime", "movie", "54", 0)
	if err != nil || changed {
		t.Fatalf("conflict changed=%v err=%v", changed, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type_correction_skipped'`, id); n != 1 {
		t.Fatalf("skipped evidence=%d", n)
	}
	if err = f.store.DeleteWork(f.ctx, collision.ID); err != nil {
		t.Fatal(err)
	}
	changed, err = f.store.CorrectBangumiWorkType(f.ctx, id, "anime", "movie", "54", 0)
	if err != nil || !changed {
		t.Fatalf("retry changed=%v err=%v", changed, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM enrichment_provenance WHERE entity_type='work' AND entity_id=? AND field_name='type_correction_skipped'`, id); n != 0 {
		t.Fatalf("skipped evidence left=%d", n)
	}
}

func TestWorksForEnrichmentEmptyPlatformDoesNotRefetch(t *testing.T) {
	f := newAlbumMergeFixture(t)
	id := makeBoundReferencedWork(t, f, "Empty Platform", "52", `{"id":52,"type":2,"platform":""}`)
	got, err := f.store.WorksForEnrichment(f.ctx, "bangumi", false, 0, id)
	if err != nil || len(got) != 0 {
		t.Fatalf("selected=%+v err=%v", got, err)
	}
}

func TestWorksForEnrichmentInvalidRawJSONDoesNotFail(t *testing.T) {
	f := newAlbumMergeFixture(t)
	id := makeBoundReferencedWork(t, f, "Broken JSON", "53", `{broken`)
	got, err := f.store.WorksForEnrichment(f.ctx, "bangumi", false, 0, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("invalid raw selected=%+v", got)
	}
	// The profile remains readable as the original malformed bytes even though
	// it is excluded from JSON extraction.
	var raw string
	if err = f.store.db.QueryRowContext(f.ctx, `SELECT raw_json FROM work_external_profiles WHERE work_id=?`, id).Scan(&raw); err != nil || raw != `{broken` {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
}

func TestManualTrackAcceptanceKeepsGameSuppressionForAnimeWork(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	track := trackIDOf(t, f, "Album")
	candidate := saveOneTrackCandidate(t, f, track, "8", []BangumiTieup{{SubjectID: 5, Title: "Show", Type: "anime", Role: "op"}}, "exact")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,'show|game|0')`, track); err != nil {
		t.Fatal(err)
	}
	outcome, _, err := f.store.ConfirmTrackSubjectCandidate(f.ctx, track, candidate.ID, 0, "", false, []int64{5}, nil)
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key='show|game|0'`, track); n != 1 {
		t.Fatalf("game suppression count=%d", n)
	}
}
