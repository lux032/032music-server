package storage

import "testing"

func TestDiscoveryMappingMetadataPromotionAndScore(t *testing.T) {
	first := DiscoveryItem{DiscoveryCandidate: DiscoveryCandidate{DiscoveryReleaseGroup: DiscoveryReleaseGroup{RGID: "first"}, SubjectID: 1, Score: 12}}
	second := DiscoveryItem{DiscoveryCandidate: DiscoveryCandidate{DiscoveryReleaseGroup: DiscoveryReleaseGroup{RGID: "second"}, SubjectID: 1, Score: 8}}
	first.MappingOptions = []DiscoveryItem{first, second}
	metadata := map[string]discoveryMappingDisplay{discoveryMappingKey(1, "second"): {name: "条目", nameCN: "中文名", score: 95, evidence: []string{"title_strict"}}}
	item, ok := enrichDiscoveryMappingItem(first, metadata)
	if !ok || item.RGID != "second" || len(item.MappingOptions) != 1 || item.SubjectName != "条目" || item.SubjectNameCN != "中文名" || item.MappingScore != 95 || item.Score != 8 {
		t.Fatal(item, ok)
	}
	item, ok = enrichDiscoveryMappingItem(second, metadata)
	if !ok || item.Score != 8 || item.MappingScore != 95 {
		t.Fatal(item, ok)
	}
	if _, ok := enrichDiscoveryMappingItem(first, map[string]discoveryMappingDisplay{}); ok {
		t.Fatal("empty group was retained")
	}
}
