package storage

import "testing"

func TestDiscoveryMappingMetadataPromotionAndScore(t *testing.T) {
	first := DiscoveryItem{DiscoveryCandidate: DiscoveryCandidate{DiscoveryReleaseGroup: DiscoveryReleaseGroup{RGID: "first"}, SubjectID: 1, Score: 12}}
	second := DiscoveryItem{DiscoveryCandidate: DiscoveryCandidate{DiscoveryReleaseGroup: DiscoveryReleaseGroup{RGID: "second"}, SubjectID: 1, Score: 8}}
	first.MappingOptions = []DiscoveryItem{first, second}
	metadata := map[string]discoveryMappingDisplay{discoveryMappingKey(1, "second"): {name: "条目", nameCN: "中文名", score: 95, evidence: []string{"title_strict"}}}
	item, ok, err := enrichDiscoveryMappingItem(first, metadata)
	if err != nil || !ok || item.RGID != "second" || len(item.MappingOptions) != 1 || item.SubjectName != "条目" || item.SubjectNameCN != "中文名" || item.MappingScore != 95 || item.Score != 8 || len(item.MappingEvidence) != 1 || item.MappingEvidence[0] != "title_strict" {
		t.Fatal(item, ok, err)
	}
	item, ok, err = enrichDiscoveryMappingItem(second, metadata)
	if err != nil || !ok || item.Score != 8 || item.MappingScore != 95 {
		t.Fatal(item, ok, err)
	}
	if _, ok, err := enrichDiscoveryMappingItem(first, map[string]discoveryMappingDisplay{}); err != nil || ok {
		t.Fatal("empty group was retained", ok, err)
	}
}
