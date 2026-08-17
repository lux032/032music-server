package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"os"

	"github.com/lux032/032music-server/internal/lyrics"
)

func (a *App) handleAPITrackLyrics(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))

	// 1. Try external .lrc file alongside the audio file
	audioPath, err := a.store.AudioFilePath(r.Context(), trackID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
			return
		}
		a.logger.Error("get audio path for lyrics", "trackId", trackID, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}

	lrcPath := lyrics.DetectLRCPath(audioPath)
	if lrcContent, readErr := os.ReadFile(lrcPath); readErr == nil {
		result := lyrics.ParseLRC(string(lrcContent))
		if len(result.Lines) > 0 {
			writeJSON(w, http.StatusOK, result)
			return
		}
	}

	// 2. Fall back to embedded lyrics from database
	embeddedLyrics, err := a.store.TrackLyrics(r.Context(), trackID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
			return
		}
		a.logger.Error("get embedded lyrics", "trackId", trackID, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}

	if embeddedLyrics == "" {
		writeAPIError(w, http.StatusNotFound, "no_lyrics", "No lyrics available for this track.")
		return
	}

	result := lyrics.ParseLRC(embeddedLyrics)
	if len(result.Lines) == 0 {
		// Treat raw text as unsynced single-line lyrics
		result = lyrics.LyricsResult{
			Synced: false,
			Lines:  []lyrics.LyricLine{{Text: embeddedLyrics}},
		}
	}
	writeJSON(w, http.StatusOK, result)
}
