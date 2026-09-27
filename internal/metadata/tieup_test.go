package metadata

import "testing"

func TestInferTrackWorkExplicitTagOnly(t *testing.T) {
	values := InferTrackWorkFromTags(map[string][]string{"CONTENTGROUP": {"TVアニメ「葬送のフリーレン」OP1"}}, "勇者")
	if len(values) != 1 || values[0].Title != "葬送のフリーレン" || values[0].Role != "op" || values[0].Sequence != 1 {
		t.Fatalf("explicit work: %+v", values)
	}
	if got, ok := InferAlbumWork("葬送のフリーレン Original Soundtrack", "", false); !ok || got.Title != "葬送のフリーレン" || got.Role != "ost" {
		t.Fatalf("album work: %+v, %v", got, ok)
	}
	if got, ok := InferAlbumWork("Greatest Hits", "", false); ok {
		t.Fatalf("generic album: %+v", got)
	}
}
