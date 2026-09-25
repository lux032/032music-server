package httpapi

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf16"

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
	if lrcContent, readErr := readExternalLRC(lrcPath); errors.Is(readErr, errExternalLRCTooLarge) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "lyrics_too_large", "External lyrics exceed 1 MiB.")
		return
	} else if readErr == nil {
		result := lyrics.ParseLRC(string(normalizeLyricsBOM(lrcContent)))
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

	if strings.TrimSpace(embeddedLyrics) == "" {
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

// Large sidecars are rejected rather than read into unbounded memory.
const maxExternalLRCBytes = 1 << 20

var errExternalLRCTooLarge = errors.New("external lyrics exceed 1 MiB")

func readExternalLRC(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return nil, err
	} else if info.Size() > maxExternalLRCBytes {
		return nil, errExternalLRCTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(file, maxExternalLRCBytes+1))
	if err == nil && len(content) > maxExternalLRCBytes {
		return nil, errExternalLRCTooLarge
	}
	return content, err
}

func (a *App) handleAPITrackLyricsText(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	path, err := a.store.AudioFilePath(r.Context(), id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	var content []byte
	if err == nil {
		if lrc := lyrics.DetectLRCPath(path); lrc != "" {
			var readErr error
			content, readErr = readExternalLRC(lrc)
			if errors.Is(readErr, errExternalLRCTooLarge) {
				writeAPIError(w, http.StatusRequestEntityTooLarge, "lyrics_too_large", "External lyrics exceed 1 MiB.")
				return
			}
		}
	}
	if strings.TrimSpace(string(normalizeLyricsBOM(content))) == "" {
		text, e := a.store.TrackLyrics(r.Context(), id)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			a.writeFeatureError(w, r, e, "query_failed")
			return
		}
		content = []byte(text)
	}
	content = normalizeLyricsBOM(content)
	if strings.TrimSpace(string(content)) == "" {
		writeAPIError(w, http.StatusNotFound, "no_lyrics", "No lyrics available for this track.")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(content)
}
func normalizeLyricsBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		return b[3:]
	}
	if len(b) < 2 || !((b[0] == 0xff && b[1] == 0xfe) || (b[0] == 0xfe && b[1] == 0xff)) {
		return b
	}
	var order binary.ByteOrder = binary.LittleEndian
	if b[0] == 0xfe {
		order = binary.BigEndian
	}
	units := make([]uint16, 0, (len(b)-2)/2)
	for i := 2; i+1 < len(b); i += 2 {
		units = append(units, order.Uint16(b[i:i+2]))
	}
	return []byte(string(utf16.Decode(units)))
}
