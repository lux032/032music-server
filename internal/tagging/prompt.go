package tagging

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/lyrics"
	"github.com/lux032/032music-server/internal/storage"
)

// LyricsExcerptRunes 是歌词片段的字数上限。
const LyricsExcerptRunes = 600

// Input 是发送给模型的一首曲目（JSON 序列化后作为 user 消息）。字段名保持简短稳定：
// 改动结构时递增 PromptVersion。
type Input struct {
	Title       string         `json:"title"`
	Artist      string         `json:"artist,omitempty"`
	Album       string         `json:"album,omitempty"`
	AlbumArtist string         `json:"albumArtist,omitempty"`
	Year        int            `json:"year,omitempty"`
	DurationSec int64          `json:"durationSec,omitempty"`
	TrackType   string         `json:"trackType,omitempty"` // regular/instrumental/off_vocal/tv_size/drama_track/remix
	GenreTags   []string       `json:"fileGenreTags,omitempty"`
	Composer    string         `json:"composer,omitempty"`
	Lyricist    string         `json:"lyricist,omitempty"`
	Arranger    string         `json:"arranger,omitempty"`
	Works       []InputWork    `json:"works,omitempty"`
	Audio       *AudioEvidence `json:"audio,omitempty"`
	AlbumAudio  *AlbumSummary  `json:"albumAudio,omitempty"`
	HasLyrics   bool           `json:"hasLyrics"`
	Lyrics      string         `json:"lyricsExcerpt,omitempty"`
}

// InputWork 是关联作品（动画/游戏等）及曲目在其中的用途（OP/ED/插曲…）。
type InputWork struct {
	Title string `json:"title"`
	Type  string `json:"type,omitempty"`
	Role  string `json:"role,omitempty"`
}

// BuildInput 组装输入。lyricsText 为原始歌词（LRC 或纯文本，空表示没有）；includeLyrics=false 时不发送歌词。
func BuildInput(info storage.TaggingTrackInfo, audio *AudioEvidence, album *AlbumSummary, lyricsText string, includeLyrics bool) Input {
	in := Input{
		Title: info.Title, Artist: info.Artist, Album: info.Album, Year: info.Year,
		DurationSec: info.DurationMs / 1000, GenreTags: info.Genres,
		Composer: info.Composer, Lyricist: info.Lyricist, Arranger: info.Arranger,
		Audio: audio, AlbumAudio: album,
	}
	if info.AlbumArtist != "" && info.AlbumArtist != info.Artist {
		in.AlbumArtist = info.AlbumArtist
	}
	if info.TrackType != "" && info.TrackType != "regular" {
		in.TrackType = info.TrackType
	}
	for _, w := range info.Works {
		iw := InputWork{Title: w.Title}
		if w.Type != "other" {
			iw.Type = w.Type
		}
		if w.Role != "other" {
			iw.Role = w.Role
		}
		in.Works = append(in.Works, iw)
	}
	instrumental := info.TrackType == "instrumental" || info.TrackType == "off_vocal"
	excerpt := ""
	if !instrumental {
		excerpt = LyricsExcerpt(lyricsText, LyricsExcerptRunes)
	}
	in.HasLyrics = excerpt != ""
	if includeLyrics {
		in.Lyrics = excerpt
	}
	return in
}

// Marshal 返回 user 消息内容（紧凑 JSON）与其哈希（用于幂等）。
func (in Input) Marshal() (string, string) {
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(append([]byte{byte(VocabVersion), byte(PromptVersion)}, raw...))
	return string(raw), hex.EncodeToString(sum[:16])
}

var creditLine = regexp.MustCompile(`(?i)^\s*(作词|作詞|作曲|编曲|編曲|词|詞|曲|演唱|歌|vocal|lyrics?|music|composed|arranged|arrangement|written)\s*(by)?\s*[:：／/]`)

// LyricsExcerpt 提取歌词片段：去掉时间轴、制作人员行与空行，取开头约 12 行，再补充重复次数最多
// 的句子（多为副歌），总长不超过 maxRunes。
func LyricsExcerpt(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	parsed := lyrics.ParseLRC(text)
	var lines []string
	if len(parsed.Lines) > 0 {
		for _, l := range parsed.Lines {
			lines = append(lines, l.Text)
		}
	} else {
		lines = strings.Split(text, "\n")
	}
	var clean []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || creditLine.MatchString(l) {
			continue
		}
		if len(clean) > 0 && clean[len(clean)-1] == l {
			continue // 同一句的双语/重复时间轴
		}
		clean = append(clean, l)
	}
	if len(clean) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, l := range clean {
		counts[l]++
	}
	var picked []string
	seen := map[string]bool{}
	for _, l := range clean {
		if len(picked) >= 12 {
			break
		}
		if !seen[l] {
			picked = append(picked, l)
			seen[l] = true
		}
	}
	type rep struct {
		line string
		n    int
	}
	var reps []rep
	for l, n := range counts {
		if n >= 2 && !seen[l] {
			reps = append(reps, rep{l, n})
		}
	}
	sort.Slice(reps, func(i, j int) bool {
		if reps[i].n != reps[j].n {
			return reps[i].n > reps[j].n
		}
		return reps[i].line < reps[j].line
	})
	for i := 0; i < len(reps) && i < 4; i++ {
		picked = append(picked, reps[i].line)
	}
	out := strings.Join(picked, "\n")
	if utf8.RuneCountInString(out) > maxRunes {
		out = string([]rune(out)[:maxRunes])
	}
	return out
}

// SystemPrompt 是固定的系统提示（各曲目相同，利于服务端前缀缓存）。
func SystemPrompt() string {
	var b strings.Builder
	b.WriteString(`你是音乐标注员。根据一首歌曲的声学分析结果、元数据和歌词片段，从固定词表中为它选择情绪与风格标签。

输入是 JSON：
- audio：声学模型的结果。discogsStyles 为 Discogs 风格概率（多标签，单项 0.3 以上已较明确）；moodTheme 为情绪/主题概率（数值普遍偏低，看相对高低）；instruments 为乐器；heads 为二分类概率（voice=有人声，danceable、happy、sad、relaxed、aggressive、party 等）；arousal/valence 为唤醒度/效价（1–9，5 为中性；唤醒度高=激烈，效价高=积极）；energyLevel 为 1–5 的能量等级；styleHints/moodHints 为映射到词表的参考得分；lockedStyles 为声学高置信的风格。
- albumAudio：同专辑其他曲目的声学风格摘要。
- works：关联作品（anime/game/movie…）及用途（op/ed/insert/theme/character/ost/image_song）。
- lyricsExcerpt：歌词片段（可能没有）。hasLyrics=false 时多为纯音乐或没有歌词数据。

情绪词表（moods，id：名称 — 说明）：
`)
	for _, t := range Moods {
		b.WriteString("- " + t.ID + "：" + t.Label + " — " + t.Hint + "\n")
	}
	b.WriteString("\n风格词表（styles）：\n")
	for _, t := range Styles {
		b.WriteString("- " + t.ID + "：" + t.Label + " — " + t.Hint + "\n")
	}
	b.WriteString(`
规则：
1. 只能使用上面词表中的 id，不得自创。
2. moods 给 1–3 个，按 weight 从高到低；weight 表示该情绪在这首歌中的占比，合计约为 1，主情绪 weight ≥ 0.4。confidence（0–1）表示你的把握，证据不足时如实给低分。
3. 歌曲整体或歌词明显悲伤、黑暗或孤独时，必须标出 sad/dark/lonely，不要硬归到相近的正向情绪（例如不要把悲伤的歌标成 healing）。曲调明快而歌词悲伤时，可以同时给出两种情绪并分配权重。
4. nostalgic 的 confidence 不超过 0.5。
5. 有歌词时，情绪以歌词语义与声学（唤醒度、效价、moodTheme）共同判断；没有歌词或纯音乐时以声学为主。高唤醒的情绪（hotblooded、uplifting、cheerful、epic）与低唤醒（healing、relaxed、dreamy）不要与 arousal 明显矛盾。
6. styles 给 1–3 个，按把握从高到低。lockedStyles 中的风格必须包含。声学与元数据（流派标签、作品、歌手）共同判断；动画/游戏歌曲本身不是风格，按实际曲风选择。
7. idol：只有元数据能明确说明是偶像组合、偶像企划、声优偶像团体或偶像角色歌时填 yes，并在 styles 中包含 idol；明确不是填 no；无法判断填 unsure（不要猜）。
8. vocal：vocal=人声为主，instrumental=纯音乐/伴奏，mixed=人声很少或只有和声。参考 heads.voice 与 trackType。
9. energy：1–5，参考 energyLevel 与 arousal。
10. 只输出 JSON，不要解释。格式：
{"moods":[{"tag":"情绪id","weight":0.6,"confidence":0.8}],"styles":[{"tag":"风格id","confidence":0.9}],"idol":"yes|no|unsure","vocal":"vocal|instrumental|mixed","energy":3}
`)
	return b.String()
}

// ResponseSchema 是结构化输出的 JSON Schema（OpenAI response_format=json_schema）。
func ResponseSchema() json.RawMessage {
	item := func(enum []string, withWeight bool) map[string]any {
		props := map[string]any{
			"tag":        map[string]any{"type": "string", "enum": enum},
			"confidence": map[string]any{"type": "number"},
		}
		req := []string{"tag", "confidence"}
		if withWeight {
			props["weight"] = map[string]any{"type": "number"}
			req = []string{"tag", "weight", "confidence"}
		}
		return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"moods":  map[string]any{"type": "array", "items": item(ids(Moods), true)},
			"styles": map[string]any{"type": "array", "items": item(ids(Styles), false)},
			"idol":   map[string]any{"type": "string", "enum": []string{"yes", "no", "unsure"}},
			"vocal":  map[string]any{"type": "string", "enum": []string{"vocal", "instrumental", "mixed"}},
			"energy": map[string]any{"type": "integer"},
		},
		"required":             []string{"moods", "styles", "idol", "vocal", "energy"},
		"additionalProperties": false,
	}
	raw, _ := json.Marshal(schema)
	return raw
}
