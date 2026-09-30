package metadata

import (
	"testing"

	"github.com/dhowden/tag"
)

// MP3 的 TXXX 自定义帧（MusicAutoTagger/Picard 写的 "MusicBrainz Album Type"）
// 需要按描述展开成普通 key，服务端才能读到发行类型。
func TestFlattenRawExpandsTXXXDescriptions(t *testing.T) {
	raw := map[string]interface{}{
		"TXXX":       &tag.Comm{Description: "MusicBrainz Album Type", Text: "single"},
		"TXXX_0":     &tag.Comm{Description: "RELEASETYPE", Text: "ep"},
		"TXXX_1":     &tag.Comm{Description: "ARTISTSORT", Text: "from txxx"},
		"TSOP":       "Sort",
		"ARTISTSORT": "native",
	}
	got := flattenRaw(raw)
	if v := got["MUSICBRAINZ ALBUM TYPE"]; len(v) != 1 || v[0] != "single" {
		t.Fatalf("MUSICBRAINZ ALBUM TYPE = %v", v)
	}
	if v := got["RELEASETYPE"]; len(v) != 1 || v[0] != "ep" {
		t.Fatalf("RELEASETYPE = %v", v)
	}
	// 已存在的同名 key 不被 TXXX 覆盖。
	if v := got["ARTISTSORT"]; len(v) != 1 || v[0] != "native" {
		t.Fatalf("ARTISTSORT = %v", v)
	}
	// 原始 TXXX key 保留。
	if _, ok := got["TXXX"]; !ok {
		t.Fatal("original TXXX key dropped")
	}
}
