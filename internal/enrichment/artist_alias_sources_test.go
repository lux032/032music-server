package enrichment

import (
	"context"
	"strings"
	"testing"
)

func TestArtistAliasSourcesRetainLocaleAndIdentityChain(t *testing.T) {
	bt := newBioTransport()
	bt.mbBody = `{"id":"mbid-bio-1","name":"星街すいせい","aliases":[{"name":"星街彗星","locale":"zh"},{"name":"Japanese","locale":"ja"}],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q1"}},{"type":"official homepage","url":{"resource":"https://official.example/artist"}}]}`
	bt.wikidataBody = `{"entities":{"Q1":{"labels":{"zh-hans":{"value":"星街彗星","language":"zh-hans"}},"aliases":{"zh-hant":[{"value":"繁體別名","language":"zh-hant"}]}}}}`
	m, _ := bioBackfillManager(t, bt, "Singer")
	setting, err := m.store.MetadataSourceSetting(context.Background(), "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	if err = m.store.SaveMetadataSourceSetting(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	out, e := m.LoadArtistAliasSources(context.Background(), "mbid-bio-1")
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Names) != 3 || len(out.OfficialURLs) != 1 || out.Names[0].Language != "zh" || out.Names[1].Language != "zh-hans" {
		t.Fatal(out)
	}
	for _, v := range out.Names {
		if !strings.HasPrefix(v.Language, "zh") {
			t.Fatal(v)
		}
	}
	if bt.mbReqs.Load() != 1 || bt.wikidataReqs.Load() != 1 {
		t.Fatal(bt.mbReqs.Load(), bt.wikidataReqs.Load())
	}
}
