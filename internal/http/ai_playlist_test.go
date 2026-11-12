package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/dailymix"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

type aiPlaylistChat struct {
	reply string
	err   error
}

func (c *aiPlaylistChat) Complete(context.Context, tagging.ChatRequest) (tagging.ChatResult, error) {
	return tagging.ChatResult{Content: c.reply, Usage: storage.AIUsage{Requests: 1, PromptTokens: 800, CompletionTokens: 60}}, c.err
}

func TestAIPlaylistAPI(t *testing.T) {
	app, store, handler, _ := dailyMixTestApp(t)
	ctx := context.Background()
	chat := &aiPlaylistChat{reply: `{"title":"深夜爵士","description":"微醺的夜晚，来点爵士","moods":["relaxed"],"styles":["jazz"],"people":[],"works":[],"energy":"low","familiarity":"mixed"}`}
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, dailymix.Zone)
	app.SetDailyMixes(dailymix.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), dailymix.Options{
		Now: func() time.Time { return now }, NewChat: func(storage.AISettings) (tagging.Chat, error) { return chat, nil }}))

	if capabilityFeature(t, handler, "aiPlaylists") {
		t.Fatal("aiPlaylists without a configured model")
	}
	if rec := apiRequest(handler, http.MethodPost, "/api/v1/ai/playlists", `{"prompt":"深夜爵士"}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "ai_not_configured") {
		t.Fatalf("unconfigured = %d %s", rec.Code, rec.Body.String())
	}
	ai := storage.DefaultAISettings()
	ai.BaseURL, ai.Model, ai.APIKey = "https://x.test/v1", "lite", "k"
	if err := store.SaveAISettings(ctx, ai); err != nil {
		t.Fatal(err)
	}
	if !capabilityFeature(t, handler, "aiPlaylists") {
		t.Fatal("aiPlaylists capability with a configured model")
	}

	for _, bad := range []string{`{}`, `{"prompt":"   "}`, `{"prompt":"x","trackCount":51}`, `{"prompt":"x","extra":1}`, `{"prompt":"` + strings.Repeat("长", 201) + `"}`} {
		if rec := apiRequest(handler, http.MethodPost, "/api/v1/ai/playlists", bad); rec.Code != 400 {
			t.Fatalf("%.40s → %d", bad, rec.Code)
		}
	}

	rec := apiRequest(handler, http.MethodPost, "/api/v1/ai/playlists", `{"prompt":"深夜听的放松爵士","trackCount":20}`)
	var body struct {
		Title       string                   `json:"title"`
		Description string                   `json:"description"`
		TitleSource string                   `json:"titleSource"`
		Conditions  aiPlaylistConditionsJSON `json:"conditions"`
		TrackCount  int                      `json:"trackCount"`
		Duration    int64                    `json:"durationMillis"`
		Tracks      []dailyMixTrackJSON      `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("generate %d %s", rec.Code, rec.Body.String())
	}
	if body.Title != "深夜爵士" || body.TitleSource != "llm" || body.TrackCount != len(body.Tracks) || body.TrackCount < dailymix.MinTracks || body.Duration <= 0 {
		t.Fatalf("body = %+v", body)
	}
	if len(body.Conditions.Styles) != 1 || body.Conditions.Styles[0].Label != "爵士/融合" || body.Conditions.Energy != "low" || body.Conditions.People == nil {
		t.Fatalf("conditions = %+v", body.Conditions)
	}
	if body.Tracks[0].Track.Title == "" || body.Tracks[0].Bucket == "" || body.Tracks[0].Position != 0 {
		t.Fatalf("track = %+v", body.Tracks[0])
	}
	if strings.Contains(rec.Body.String(), `"reason`) {
		t.Fatal("internal explanations are not exposed")
	}

	chat.reply = `{"title":"x","description":"","moods":[],"styles":[],"people":["Nobody"],"works":[],"energy":"any","familiarity":"mixed"}`
	if rec = apiRequest(handler, http.MethodPost, "/api/v1/ai/playlists", `{"prompt":"???"}`); rec.Code != 422 || !strings.Contains(rec.Body.String(), "no_match") {
		t.Fatalf("no match = %d %s", rec.Code, rec.Body.String())
	}
	chat.err = errors.New("quota exceeded")
	if rec = apiRequest(handler, http.MethodPost, "/api/v1/ai/playlists", `{"prompt":"爵士"}`); rec.Code != 502 || !strings.Contains(rec.Body.String(), "model_failed") {
		t.Fatalf("model failure = %d %s", rec.Code, rec.Body.String())
	}
}
