package enrichment

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const otherMBID = "33333333-3333-4333-8333-333333333333"

func TestResetIdentityIsNotAutoReboundAndManualSearchCanPickAnother(t *testing.T) {
	manager, artist := independentArtistManager(t, false)
	ctx := context.Background()
	manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/artist/"+otherMBID) {
			return cannedResponse(200, http.Header{}, `{"id":"`+otherMBID+`","name":"ACE+","sort-name":"ACE+","disambiguation":"doujin circle","aliases":[],"tags":[],"relations":[]}`), nil
		}
		if r.URL.Query().Get("query") != "" {
			return cannedResponse(200, http.Header{}, `{"artists":[{"id":"`+safetyMBID+`","name":"ACE+","score":100},{"id":"`+otherMBID+`","name":"ACE+","disambiguation":"doujin circle","score":100}]}`), nil
		}
		return cannedResponse(200, http.Header{}, `{"id":"`+safetyMBID+`","name":"ACE+","sort-name":"ACE+","aliases":[],"tags":[],"relations":[]}`), nil
	})
	result, err := manager.MatchArtist(ctx, artist)
	if err != nil || !result.AutoMatched {
		t.Fatalf("initial match %+v %v", result, err)
	}
	if err = manager.store.ResetArtistIdentity(ctx, artist, "musicbrainz", safetyMBID); err != nil {
		t.Fatal(err)
	}
	result, err = manager.MatchArtist(ctx, artist)
	if err != nil || result.AutoMatched {
		t.Fatalf("reset identity was auto-bound again: %+v %v", result, err)
	}
	if _, err = manager.store.ArtistExternalID(ctx, artist, "musicbrainz"); err == nil {
		t.Fatal("reset identity rebound")
	}

	if _, err = manager.SearchArtistCandidates(ctx, artist, "  "); !errors.Is(err, ErrManualSearchEmpty) {
		t.Fatal(err)
	}
	search, err := manager.SearchArtistCandidates(ctx, artist, "ACE+")
	if err != nil || search.CandidateCount != 2 || search.ConfirmCandidateID != 0 {
		t.Fatalf("%+v %v", search, err)
	}
	candidates, _ := manager.store.ArtistCandidates(ctx, artist)
	statuses := map[string]string{}
	for _, c := range candidates {
		if c.Score > 85 && c.Status == "candidate" {
			t.Fatalf("manual search result could auto-bind: %+v", c)
		}
		statuses[c.ExternalID] = c.Status
	}
	if statuses[safetyMBID] != "rejected" || statuses[otherMBID] != "candidate" {
		t.Fatalf("statuses %+v", statuses)
	}

	pick, err := manager.SearchArtistCandidates(ctx, artist, "https://musicbrainz.org/artist/"+strings.ToUpper(otherMBID))
	if err != nil || pick.ConfirmCandidateID == 0 {
		t.Fatalf("%+v %v", pick, err)
	}
	if err = manager.store.ConfirmArtistCandidate(ctx, artist, pick.ConfirmCandidateID); err != nil {
		t.Fatal(err)
	}
	if id, _ := manager.store.ArtistExternalID(ctx, artist, "musicbrainz"); id != otherMBID {
		t.Fatalf("bound %q", id)
	}

	// The previously rejected identity can still be chosen explicitly.
	rejectedID, err := manager.store.ArtistCandidateID(ctx, artist, "musicbrainz", safetyMBID)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.store.ConfirmArtistCandidate(ctx, artist, rejectedID); err != nil {
		t.Fatal(err)
	}
	if id, _ := manager.store.ArtistExternalID(ctx, artist, "musicbrainz"); id != safetyMBID {
		t.Fatalf("switch back bound %q", id)
	}
}
