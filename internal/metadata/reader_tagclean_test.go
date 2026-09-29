package metadata

import (
	"testing"
)

// D-19: the reader-side fields derived from raw tags (lyricist, arranger,
// producer, sort names) go through the same cleaning rule as the storage
// layer (D62): split on NUL, first non-empty segment, strip C0 controls,
// tab folds to a space, TrimSpace.

func TestReadFLACCleansCreditAndSortTags(t *testing.T) {
	flac := minimalFLAC(
		"TITLE=Song",
		"ALBUM=Album",
		"ARTIST=Artist",
		"LYRICIST=\x00\x00作詞\t太郎",
		"ARRANGER=編曲\x00",
		"PRODUCER=\x00\x00",
		"TITLESORT=ソング\x00",
		"ARTISTSORT=\x01アーティスト",
	)
	got, err := Read(writeTemp(t, "track.flac", flac))
	if err != nil {
		t.Fatal(err)
	}
	if got.Lyricist != "作詞 太郎" {
		t.Fatalf("Lyricist=%q", got.Lyricist)
	}
	if got.Arranger != "編曲" {
		t.Fatalf("Arranger=%q", got.Arranger)
	}
	if got.Producer != "" {
		t.Fatalf("Producer=%q, want empty (only NULs)", got.Producer)
	}
	if got.TitleSort != "ソング" {
		t.Fatalf("TitleSort=%q", got.TitleSort)
	}
	if got.ArtistSort != "アーティスト" {
		t.Fatalf("ArtistSort=%q", got.ArtistSort)
	}
}

func TestReadFLACCreditFallsBackToNextValue(t *testing.T) {
	// A first value that cleans to empty falls back to the next value of the
	// same key (Vorbis comments repeat the key for multi-values).
	flac := minimalFLAC(
		"TITLE=Song",
		"ALBUM=Album",
		"ARTIST=Artist",
		"LYRICIST=\x00",
		"LYRICIST=第二作词",
	)
	got, err := Read(writeTemp(t, "track.flac", flac))
	if err != nil {
		t.Fatal(err)
	}
	if got.Lyricist != "第二作词" {
		t.Fatalf("Lyricist=%q", got.Lyricist)
	}
}

func TestReadMP3CleansCreditAndSortTags(t *testing.T) {
	// ID3v2.4 text frames (UTF-8) carrying NUL segments and C0 controls.
	var frames []byte
	for _, frame := range [][]byte{
		makeID3Frame(4, "TIT2", encodeTextFields(3, []string{"Song"}), 0),
		makeID3Frame(4, "TALB", encodeTextFields(3, []string{"Album"}), 0),
		makeID3Frame(4, "TPE1", encodeTextFields(3, []string{"Artist"}), 0),
		makeID3Frame(4, "TPE2", encodeTextFields(3, []string{"Artist"}), 0),
		makeID3Frame(4, "TEXT", encodeTextFields(3, []string{"\x00\x00作詞\t太郎"}), 0),
		makeID3Frame(4, "TSOP", encodeTextFields(3, []string{"アーティスト\x00"}), 0),
		// dhowden/tag drops NUL bytes inside UTF-8 text frames before Raw()
		// sees them, so the MP3 path exercises control-character cleaning with
		// a tab instead of an embedded NUL.
		makeID3Frame(4, "TSOT", encodeTextFields(3, []string{"前\t後"}), 0),
	} {
		frames = append(frames, frame...)
	}
	data := makeID3Tag(4, frames)
	got, err := Read(writeTemp(t, "track.mp3", data))
	if err != nil {
		t.Fatal(err)
	}
	if got.Lyricist != "作詞 太郎" {
		t.Fatalf("Lyricist=%q", got.Lyricist)
	}
	if got.ArtistSort != "アーティスト" {
		t.Fatalf("ArtistSort=%q", got.ArtistSort)
	}
	if got.TitleSort != "前 後" {
		t.Fatalf("TitleSort=%q", got.TitleSort)
	}
}

func TestInferTrackWorkFromTagsCleansValues(t *testing.T) {
	// A dirty WORKTITLE value must be cleaned before inference: leading NULs
	// would otherwise poison the inferred work title.
	got := InferTrackWorkFromTags(map[string][]string{"WORKTITLE": {"\x00\x00TVアニメ「葬送のフリーレン」OP1"}}, "勇者")
	if len(got) != 1 || got[0].Title != "葬送のフリーレン" {
		t.Fatalf("InferTrackWorkFromTags dirty value = %#v", got)
	}
	// A value that cleans to empty is skipped instead of inferred.
	if got := InferTrackWorkFromTags(map[string][]string{"WORKTITLE": {"\x00\x00"}}, "勇者"); len(got) != 0 {
		t.Fatalf("InferTrackWorkFromTags only-NUL value = %#v", got)
	}
}
