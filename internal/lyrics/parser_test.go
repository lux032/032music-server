package lyrics

import (
	"path/filepath"
	"testing"
)

func TestParseLRC_Empty(t *testing.T) {
	result := ParseLRC("")
	if len(result.Lines) != 0 {
		t.Errorf("expected no lines, got %d", len(result.Lines))
	}
	if result.Synced {
		t.Error("expected synced=false for empty input")
	}
}

func TestParseLRC_SyncedBasic(t *testing.T) {
	input := `[00:12.34]夢が覚めたら
[00:18.56]もう一度会えるかな
[01:02.00]最後の言葉`
	result := ParseLRC(input)
	if !result.Synced {
		t.Fatal("expected synced=true")
	}
	if len(result.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(result.Lines))
	}
	tests := []struct {
		timeMs int64
		text   string
	}{
		{12340, "夢が覚めたら"},
		{18560, "もう一度会えるかな"},
		{62000, "最後の言葉"},
	}
	for i, tt := range tests {
		if result.Lines[i].TimeMs != tt.timeMs {
			t.Errorf("line %d: timeMs=%d, want %d", i, result.Lines[i].TimeMs, tt.timeMs)
		}
		if result.Lines[i].Text != tt.text {
			t.Errorf("line %d: text=%q, want %q", i, result.Lines[i].Text, tt.text)
		}
	}
}

func TestParseLRC_MultipleTimestampsPerLine(t *testing.T) {
	input := `[00:10.00][01:30.00]繰り返すサビ`
	result := ParseLRC(input)
	if !result.Synced {
		t.Fatal("expected synced=true")
	}
	if len(result.Lines) != 2 {
		t.Fatalf("expected 2 lines (expanded), got %d", len(result.Lines))
	}
	if result.Lines[0].TimeMs != 10000 {
		t.Errorf("first timeMs=%d, want 10000", result.Lines[0].TimeMs)
	}
	if result.Lines[1].TimeMs != 90000 {
		t.Errorf("second timeMs=%d, want 90000", result.Lines[1].TimeMs)
	}
	for _, line := range result.Lines {
		if line.Text != "繰り返すサビ" {
			t.Errorf("text=%q, want 繰り返すサビ", line.Text)
		}
	}
}

func TestParseLRC_UnsyncedLyrics(t *testing.T) {
	input := `夢が覚めたら
もう一度会えるかな`
	result := ParseLRC(input)
	if result.Synced {
		t.Error("expected synced=false")
	}
	if len(result.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(result.Lines))
	}
	if result.Lines[0].Text != "夢が覚めたら" {
		t.Errorf("line 0 text=%q", result.Lines[0].Text)
	}
}

func TestParseLRC_SkipsMetadataTags(t *testing.T) {
	input := `[ti:テスト曲]
[ar:テスト歌手]
[al:テストアルバム]
[00:05.00]歌詞の一行目`
	result := ParseLRC(input)
	if !result.Synced {
		t.Fatal("expected synced=true")
	}
	if len(result.Lines) != 1 {
		t.Fatalf("expected 1 line (metadata skipped), got %d", len(result.Lines))
	}
	if result.Lines[0].Text != "歌詞の一行目" {
		t.Errorf("text=%q", result.Lines[0].Text)
	}
}

func TestParseLRC_ThreeDigitMilliseconds(t *testing.T) {
	input := `[00:05.123]precise timing`
	result := ParseLRC(input)
	if !result.Synced {
		t.Fatal("expected synced=true")
	}
	if result.Lines[0].TimeMs != 5123 {
		t.Errorf("timeMs=%d, want 5123", result.Lines[0].TimeMs)
	}
}

func TestParseLRC_OneDigitFraction(t *testing.T) {
	input := `[00:05.1]one digit`
	result := ParseLRC(input)
	if !result.Synced {
		t.Fatal("expected synced=true")
	}
	if result.Lines[0].TimeMs != 5100 {
		t.Errorf("timeMs=%d, want 5100", result.Lines[0].TimeMs)
	}
}

func TestParseLRC_NoFraction(t *testing.T) {
	input := `[01:30]no fraction`
	result := ParseLRC(input)
	if !result.Synced {
		t.Fatal("expected synced=true")
	}
	if result.Lines[0].TimeMs != 90000 {
		t.Errorf("timeMs=%d, want 90000", result.Lines[0].TimeMs)
	}
}

func TestParseLRC_SortsByTimestamp(t *testing.T) {
	input := `[01:00.00]second
[00:30.00]first`
	result := ParseLRC(input)
	if !result.Synced {
		t.Fatal("expected synced=true")
	}
	if result.Lines[0].TimeMs != 30000 {
		t.Errorf("first line timeMs=%d, want 30000", result.Lines[0].TimeMs)
	}
	if result.Lines[0].Text != "first" {
		t.Errorf("first line text=%q, want first", result.Lines[0].Text)
	}
}

func TestParseLRC_WindowsLineEndings(t *testing.T) {
	input := "[00:05.00]line one\r\n[00:10.00]line two"
	result := ParseLRC(input)
	if len(result.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(result.Lines))
	}
}

func TestDetectLRCPath(t *testing.T) {
	tests := []struct {
		audio string
		want  string
	}{
		{filepath.Join("music", "album", "01 track.flac"), filepath.Join("music", "album", "01 track.lrc")},
		{filepath.Join("music", "song.mp3"), filepath.Join("music", "song.lrc")},
	}
	for _, tt := range tests {
		got := DetectLRCPath(tt.audio)
		if got != tt.want {
			t.Errorf("DetectLRCPath(%q) = %q, want %q", tt.audio, got, tt.want)
		}
	}
}
