package lyricstrans

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/tagging"
)

func TestMixedLanguageCorrection(t *testing.T) {
	first := func(tagging.ChatRequest) (string, error) {
		return `{"lines":["绝不选择沉默以对","The walls have ears","Don't even breathe"]}`, nil
	}
	second := func(req tagging.ChatRequest) (string, error) {
		if !strings.Contains(req.User, `"linesNeedingTranslation":[2,3]`) || !strings.Contains(req.User, "previousTranslation") {
			t.Fatalf("missing correction context: %s", req.User)
		}
		if got := sentLines(t, req); len(got) != 3 || got[1] != "The walls have ears" {
			t.Fatalf("source lost: %v", got)
		}
		return `{"lines":["绝不选择沉默以对","隔墙有耳","连呼吸都不要"]}`, nil
	}
	chat := &scriptedChat{replies: []func(tagging.ChatRequest) (string, error){first, second}}
	res, err := Translate(context.Background(), chat, "m", "", "zh-Hans", []string{"黙る選択など取らない", "", "The walls have ears", "Don't even breathe"})
	if err != nil || strings.Join(res.Lines, "|") != "绝不选择沉默以对||隔墙有耳|连呼吸都不要" || res.Usage.Requests != 2 {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestIncompleteTranslationRejected(t *testing.T) {
	for _, output := range []string{`{"lines":[""]}`, `{"lines":["The walls have ears"]}`, `{"lines":["THE WALLS HAVE EARS!"]}`} {
		chat := &scriptedChat{replies: []func(tagging.ChatRequest) (string, error){func(tagging.ChatRequest) (string, error) { return output, nil }}}
		res, err := Translate(context.Background(), chat, "m", "", "zh-Hans", []string{"The walls have ears"})
		if !errors.Is(err, ErrIncompleteTranslation) || len(chat.reqs) != maxParseRetries || res.Usage.Requests != 2 {
			t.Fatalf("result=%+v err=%v calls=%d", res, err, len(chat.reqs))
		}
	}
}

func TestCompletenessHeuristics(t *testing.T) {
	for _, text := range []string{"You pretend to know the story", "But you were wrong", "I don't wanna be fooled again", "Got a killer in me, I got a skeleton", "Get away, I don't need a good Samaritan", "The walls have ears", "Don't even breathe", "So get the justice", "Don't you fake it now?", "Drink your justice", "黙る選択など取らない"} {
		if len(incompleteLines([]string{text}, []string{text}, "zh-Hans")) != 1 {
			t.Errorf("missed %q", text)
		}
	}
	for _, text := range []string{"你好", "Taylor Swift", "la la la", "Oh oh oh", "Hello", "我爱你 baby"} {
		if len(incompleteLines([]string{text}, []string{text}, "zh-Hans")) != 0 {
			t.Errorf("false positive %q", text)
		}
	}
	if len(incompleteLines([]string{"The walls have ears"}, []string{"The walls have ears"}, "en")) != 0 {
		t.Fatal("target-language text rejected")
	}
	if len(incompleteLines([]string{"hello"}, []string{"  "}, "fr")) != 1 {
		t.Fatal("blank translation accepted")
	}
}
