package lyricsonline

import (
	"context"
	"testing"
)

func TestRankSyncedTiersOnEqualScore(t *testing.T) {
	yes, no := true, false
	q := Query{Title: "Song", Artist: "Band", DurationMs: 200000}
	_, accepted := Rank(q, []Candidate{
		{ID: "plain", Title: "Song", Artist: "Band", DurationMs: 200000, Synced: &no},
		{ID: "unknown", Title: "Song", Artist: "Band", DurationMs: 200000},
		{ID: "synced", Title: "Song", Artist: "Band", DurationMs: 200000, Synced: &yes},
	})
	if len(accepted) != 3 || accepted[0].ID != "synced" || accepted[1].ID != "unknown" || accepted[2].ID != "plain" {
		t.Fatalf("accepted = %+v", accepted)
	}
	// A clearly better match still wins over a much worse synced one.
	_, accepted = Rank(q, []Candidate{
		{ID: "far", Title: "Sonx", Artist: "Band", DurationMs: 210000, Synced: &yes},
		{ID: "exact", Title: "Song", Artist: "Band", DurationMs: 200000, Synced: &no},
	})
	if accepted[0].ID != "exact" {
		t.Fatalf("accepted = %+v", accepted)
	}
}

// scriptedProvider returns fixed candidates and per-id lyrics.
type scriptedProvider struct {
	id    string
	cands []Candidate
	lrc   map[string]string
	gets  int
}

func (p *scriptedProvider) ID() string { return p.id }
func (p *scriptedProvider) Search(context.Context, Query) ([]Candidate, error) {
	return p.cands, nil
}
func (p *scriptedProvider) Get(_ context.Context, id string) (Lyrics, error) {
	p.gets++
	if l, ok := p.lrc[id]; ok {
		return Lyrics{Source: p.id, ID: id, LRC: l}, nil
	}
	return Lyrics{}, ErrNoLyrics
}

func TestLookupSkipsUnsyncedForLaterSynced(t *testing.T) {
	q := Query{Title: "Song", Artist: "Band", DurationMs: 200000}
	p := &scriptedProvider{id: SourceNetEase, cands: []Candidate{
		{Source: SourceNetEase, ID: "1", Title: "Song", Artist: "Band", DurationMs: 200000},
		{Source: SourceNetEase, ID: "2", Title: "Song", Artist: "Band", DurationMs: 200500},
	}, lrc: map[string]string{"1": "plain one\nplain two", "2": "[00:01.00]synced"}}
	l, cand, report, err := NewWithProviders(p).Lookup(context.Background(), q)
	if err != nil || l.ID != "2" || cand.Synced == nil || !*cand.Synced {
		t.Fatalf("lookup = %+v %+v %v %v", l, cand, report.Downloads, err)
	}

	// Only unsynced lyrics: the best one is used as a fallback.
	p.lrc["2"] = "plain other"
	l, cand, _, err = NewWithProviders(p).Lookup(context.Background(), q)
	if err != nil || l.ID != "1" || cand.Synced == nil || *cand.Synced {
		t.Fatalf("fallback = %+v %+v %v", l, cand, err)
	}
}
