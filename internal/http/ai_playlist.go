package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/dailymix"
	"github.com/lux032/032music-server/internal/tagging"
)

// AI prompt playlists: POST /api/v1/ai/playlists turns one sentence into an unsaved
// playlist (see dailymix.GenerateFromPrompt). Clients play the returned tracks and
// save them with POST /api/v1/playlists {name, description, trackIds} when wanted.

func (a *App) aiPlaylistsEnabled(ctx context.Context) bool {
	return a.dailyMixes != nil && a.dailyMixes.PromptAvailable(ctx)
}

type aiPlaylistPersonJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type aiPlaylistConditionsJSON struct {
	Moods       []dailyMixTagJSON      `json:"moods"`
	Styles      []dailyMixTagJSON      `json:"styles"`
	People      []aiPlaylistPersonJSON `json:"people"`
	Works       []aiPlaylistPersonJSON `json:"works"`
	Energy      string                 `json:"energy"`
	Familiarity string                 `json:"familiarity"`
}

func aiPlaylistConditions(pl dailymix.PromptPlaylist) aiPlaylistConditionsJSON {
	in := pl.Intent
	out := aiPlaylistConditionsJSON{Moods: []dailyMixTagJSON{}, Styles: []dailyMixTagJSON{}, People: []aiPlaylistPersonJSON{},
		Works: []aiPlaylistPersonJSON{}, Energy: in.Energy, Familiarity: in.Familiarity}
	for _, m := range in.Moods {
		out.Moods = append(out.Moods, dailyMixTagJSON{Kind: "mood", Tag: m, Label: tagging.TagLabel("mood", m)})
	}
	for _, s := range in.Styles {
		out.Styles = append(out.Styles, dailyMixTagJSON{Kind: "style", Tag: s, Label: tagging.TagLabel("style", s)})
	}
	for _, id := range in.PersonIDs {
		out.People = append(out.People, aiPlaylistPersonJSON{ID: id, Name: pl.PersonNames[id]})
	}
	for _, id := range in.WorkIDs {
		out.Works = append(out.Works, aiPlaylistPersonJSON{ID: id, Name: pl.WorkNames[id]})
	}
	return out
}

func (a *App) handleAPIAIPlaylist(w http.ResponseWriter, r *http.Request) {
	if a.dailyMixes == nil {
		writeAPIError(w, http.StatusNotFound, "not_available", "AI playlists are not available.")
		return
	}
	var in struct {
		Prompt     string `json:"prompt"`
		TrackCount int    `json:"trackCount"`
	}
	if !decode(w, r, &in) {
		return
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "prompt is required.")
		return
	}
	if utf8.RuneCountInString(prompt) > dailymix.PromptMaxRunes {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "prompt is too long.")
		return
	}
	if in.TrackCount < 0 || in.TrackCount > dailymix.PromptMaxTracks {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "trackCount must be between 0 and 50 (0 = default).")
		return
	}
	ctx := r.Context()
	pl, err := a.dailyMixes.FromPrompt(ctx, prompt, in.TrackCount)
	var modelErr *dailymix.ModelError
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, dailymix.ErrModelUnavailable):
		writeAPIError(w, http.StatusConflict, "ai_not_configured", "服务器未配置 AI 模型服务（管理 → AI 打标）。")
		return
	case errors.Is(err, dailymix.ErrPromptBusy):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, http.StatusTooManyRequests, "busy", "正在生成另一个 AI 歌单，请稍后再试。")
		return
	case errors.Is(err, dailymix.ErrPromptUnmatched):
		writeAPIError(w, http.StatusUnprocessableEntity, "no_match", "没能从描述中理解出曲库里有的情绪、风格、歌手或作品，换个说法试试。")
		return
	case errors.Is(err, dailymix.ErrPromptTooFew):
		writeAPIError(w, http.StatusUnprocessableEntity, "too_few_tracks", "曲库中符合描述的歌曲太少；AI 打标覆盖更多曲目后会更准确。")
		return
	case errors.As(err, &modelErr):
		writeAPIError(w, http.StatusBadGateway, "model_failed", "AI 模型调用失败："+pl.LLM.Note)
		return
	case err != nil:
		a.writeFeatureError(w, r, err, "generate_failed")
		return
	}
	ids := make([]int64, len(pl.Picks))
	var duration int64
	for i, p := range pl.Picks {
		ids[i] = p.T.ID
		duration += p.T.DurationMs
	}
	tracks, err := a.store.TracksByIDs(ctx, ids)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	out := make([]dailyMixTrackJSON, 0, len(tracks))
	for i, t := range tracks {
		out = append(out, dailyMixTrackJSON{Position: i, Bucket: pl.Picks[i].Bucket, Track: t})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"title": pl.Title, "description": pl.Description, "titleSource": pl.TitleSource,
		"conditions": aiPlaylistConditions(pl), "trackCount": len(out), "durationMillis": duration,
		"tracks": out, "note": pl.LLM.Note,
	})
}
