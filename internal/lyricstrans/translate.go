// Package lyricstrans 用 OpenAI 兼容接口逐行翻译歌词。
package lyricstrans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// 请求上限：防止单次请求过大或被滥用。
const (
	MaxLines        = 500
	MaxLineRunes    = 500
	MaxLanguageLen  = 35
	maxParseRetries = 2
)

var languagePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)

// ValidLanguage 检查目标语言是 BCP 47 形式的代码（如 en、zh-Hans、pt-BR）。
func ValidLanguage(code string) bool {
	return len(code) <= MaxLanguageLen && languagePattern.MatchString(code)
}

// LanguageName 返回目标语言的英文名（如 zh-Hans → Simplified Chinese），用于提示词。
func LanguageName(code string) string {
	tag, err := language.Parse(code)
	if err != nil {
		return code
	}
	if name := display.English.Tags().Name(tag); name != "" {
		return name + " (" + code + ")"
	}
	return code
}

// NormalizeLines 去掉每行首尾空白并校验数量与长度。
func NormalizeLines(lines []string) ([]string, error) {
	if len(lines) == 0 {
		return nil, errors.New("lines must not be empty")
	}
	if len(lines) > MaxLines {
		return nil, fmt.Errorf("too many lines (max %d)", MaxLines)
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if len([]rune(l)) > MaxLineRunes {
			return nil, fmt.Errorf("line %d is too long (max %d characters)", i+1, MaxLineRunes)
		}
		out[i] = l
	}
	return out, nil
}

const systemPrompt = `You are a professional song lyrics translator.
The user sends JSON {"targetLanguage": "...", "lines": [...]} where "lines" are lyric lines in order.
Translate every line into the target language, keeping the meaning, tone and poetic feel of the lyrics.
Reply with JSON {"lines": [...]} containing exactly the same number of strings, in the same order, one translated line per input line.
Never merge, split, skip, reorder or explain lines. If a line is already in the target language, is a name, or cannot be translated, return it unchanged.`

var schema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["lines"],"properties":{"lines":{"type":"array","items":{"type":"string"}}}}`)

// ErrLineMismatch 表示模型返回的行数与原文不一致（重试后仍然如此）。
var ErrLineMismatch = errors.New("模型返回的行数与原文不一致")

// Result 是一次翻译的结果。Lines 与输入等长，空行保持为空。
type Result struct {
	Lines []string
	Usage storage.AIUsage
}

// Translate 翻译 lines（应先经过 NormalizeLines）。只把非空行发给模型，避免模型丢掉空行导致错位。
func Translate(ctx context.Context, chat tagging.Chat, model, reasoningEffort, targetLanguage string, lines []string) (Result, error) {
	res := Result{Lines: make([]string, len(lines))}
	var idx []int
	var send []string
	for i, l := range lines {
		if l != "" {
			idx = append(idx, i)
			send = append(send, l)
		}
	}
	if len(send) == 0 {
		return res, nil
	}
	user, _ := json.Marshal(map[string]any{"targetLanguage": LanguageName(targetLanguage), "lines": send})
	format := "json_schema"
	var lastErr error
	for attempt := 0; attempt < maxParseRetries; attempt++ {
		r, err := chat.Complete(ctx, tagging.ChatRequest{Model: model, System: systemPrompt, User: string(user), Schema: schema, Format: format, ReasoningEffort: reasoningEffort})
		res.Usage.Add(r.Usage)
		if err != nil {
			var le *tagging.LLMError
			if errors.As(err, &le) && le.Kind == tagging.KindFormat && format == "json_schema" {
				format = "json_object" // 不支持结构化输出：降级后重试，不计入次数
				attempt--
				continue
			}
			return res, err
		}
		out, err := parse(r.Content)
		if err != nil {
			lastErr = err
			continue
		}
		if len(out) != len(send) {
			lastErr = fmt.Errorf("%w（原文 %d 行，返回 %d 行）", ErrLineMismatch, len(send), len(out))
			continue
		}
		for i, line := range out {
			res.Lines[idx[i]] = strings.TrimSpace(line)
		}
		return res, nil
	}
	return res, lastErr
}

var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// parse 取出 {"lines": [...]}，兼容推理模型的 <think> 块和 Markdown 代码块。
func parse(content string) ([]string, error) {
	s := thinkBlock.ReplaceAllString(content, "")
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, errors.New("模型输出不是 JSON")
	}
	var out struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil, errors.New("模型输出不是有效的 JSON：" + err.Error())
	}
	return out.Lines, nil
}
