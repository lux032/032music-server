package enrichment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
)

func TestMusicBrainzStructuredAliasesParsingAndSerialization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/ws/2/artist/" {
			_, _ = w.Write([]byte(`{
				"artists": [{
					"id": "11111111-1111-4111-8111-111111111111",
					"name": "Example JP Artist",
					"sort-name": "Example JP Artist",
					"country": "JP",
					"type": "Person",
					"score": 100,
					"aliases": [
						{"name": "サンプル歌手", "locale": "ja", "primary": true, "type": "Artist name", "ended": false},
						{"name": "本名太郎", "locale": "ja", "primary": true, "type": "Legal name", "ended": false}
					],
					"tags": [{"name": "j-pop"}]
				}]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id": "11111111-1111-4111-8111-111111111111",
			"name": "Example JP Artist",
			"sort-name": "Example JP Artist",
			"country": "JP",
			"type": "Person",
			"aliases": [
				{"name": "サンプル歌手", "locale": "ja", "primary": true, "type": "Artist name", "ended": false},
				{"name": "旧名義", "locale": "ja", "primary": true, "type": "Artist name", "ended": true}
			],
			"tags": [{"name": "j-pop"}],
			"relations": []
		}`))
	}))
	defer srv.Close()

	m := &Manager{
		client:          srv.Client(),
		musicBrainzBase: srv.URL + "/ws/2",
		now:             time.Now,
		blockedUntil:    map[string]time.Time{},
		sleep:           func(context.Context, time.Duration) error { return nil },
	}
	m.gate = sourcegate.New(sourcegate.Options{
		Intervals: map[string]time.Duration{"musicbrainz": 0},
		Now:       time.Now,
		Sleep:     func(context.Context, time.Duration) error { return nil },
	})
	setting := storage.MetadataSourceSetting{Source: "musicbrainz", Enabled: true, AutoMatch: true, Contact: "test@example.com"}

	// Search path preserves flat aliases and structuredAliases in payload.
	cands, profiles, err := m.musicBrainzSearch(context.Background(), storage.ArtistMatchInput{Name: "Example JP Artist"}, setting)
	if err != nil {
		t.Fatalf("musicBrainzSearch: %v", err)
	}
	if len(cands) != 1 || len(profiles) != 1 {
		t.Fatalf("expected 1 search result, got cands=%d profiles=%d", len(cands), len(profiles))
	}
	prof := profiles["11111111-1111-4111-8111-111111111111"]
	if len(prof.Aliases) != 2 || len(prof.StructuredAliases) != 2 {
		t.Fatalf("expected 2 aliases & structuredAliases, got %+v / %+v", prof.Aliases, prof.StructuredAliases)
	}
	if prof.StructuredAliases[0].Locale != "ja" || !prof.StructuredAliases[0].Primary || prof.StructuredAliases[0].Type != "Artist name" {
		t.Fatalf("unexpected first structured alias: %+v", prof.StructuredAliases[0])
	}
	if prof.StructuredAliases[1].Type != "Legal name" {
		t.Fatalf("expected Legal name preserved for filtering, got %+v", prof.StructuredAliases[1])
	}
	var payloadObj struct {
		Aliases           []string                        `json:"aliases"`
		StructuredAliases []storage.StructuredArtistAlias `json:"structuredAliases"`
	}
	if err = json.Unmarshal(cands[0].Payload, &payloadObj); err != nil {
		t.Fatalf("unmarshal search payload: %v", err)
	}
	if len(payloadObj.Aliases) != 2 || len(payloadObj.StructuredAliases) != 2 {
		t.Fatalf("unexpected serialized search payload: %+v", payloadObj)
	}

	// Lookup path preserves ended flag and structuredAliases.
	cand, lookupProf, err := m.musicBrainzLookup(context.Background(), "11111111-1111-4111-8111-111111111111", setting)
	if err != nil {
		t.Fatalf("musicBrainzLookup: %v", err)
	}
	if len(lookupProf.StructuredAliases) != 2 || !lookupProf.StructuredAliases[1].Ended {
		t.Fatalf("expected ended=true on second lookup alias, got %+v", lookupProf.StructuredAliases)
	}
	var lookupPayload struct {
		StructuredAliases []storage.StructuredArtistAlias `json:"structuredAliases"`
	}
	if err = json.Unmarshal(cand.Payload, &lookupPayload); err != nil {
		t.Fatalf("unmarshal lookup payload: %v", err)
	}
	if len(lookupPayload.StructuredAliases) != 2 || !lookupPayload.StructuredAliases[1].Ended {
		t.Fatalf("unexpected lookup payload structuredAliases: %+v", lookupPayload)
	}
}
