package storage

import "testing"

func TestWorksForEnrichmentExcludesUnreferenced(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	unused, e := s.CreateWork(ctx, WorkInput{Title: "Unreferenced", Type: "anime"})
	if e != nil {
		t.Fatal(e)
	}
	targets, e := s.WorksForEnrichment(ctx, "bangumi", true, 10, unused.ID)
	if e != nil || len(targets) != 0 {
		t.Fatalf("unreferenced target %+v err %v", targets, e)
	}
	attachWorkForEnrichmentTest(t, s, ctx, unused.ID)
	targets, e = s.WorksForEnrichment(ctx, "bangumi", true, 10, unused.ID)
	if e != nil || len(targets) != 1 {
		t.Fatalf("referenced target %+v err %v", targets, e)
	}
}
