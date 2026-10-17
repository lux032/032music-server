package httpapi

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/lux032/032music-server/internal/lyrics"
	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/storage"
)

// Lyrics sources reported to clients besides the online source ids.
const (
	lyricsSourceFile     = "file"
	lyricsSourceEmbedded = "embedded"
)

const (
	// onlineLyricsRetry is how long a "not found online" result is remembered.
	onlineLyricsRetry = 12 * time.Hour
	// onlineLyricsTimeout bounds one automatic online lookup (all sources).
	onlineLyricsTimeout = 20 * time.Second
)

// SetOnlineLyrics enables NetEase / LRCLIB lookups for tracks without local lyrics.
func (a *App) SetOnlineLyrics(client *lyricsonline.Client) { a.onlineLyrics = client }

var errNoLyrics = errors.New("no lyrics")

// resolvedLyrics is the lyrics a track resolves to.
type resolvedLyrics struct {
	Result       lyrics.LyricsResult
	Raw          string // original text (LRC or plain)
	Source       string // file | embedded | netease | lrclib
	Manual       bool
	RemoteTitle  string
	RemoteArtist string
	// TranslationLRC is the source's own translation (NetEase).
	TranslationLRC string
}

func parseLyricsText(raw string) lyrics.LyricsResult {
	result := lyrics.ParseLRC(raw)
	if len(result.Lines) == 0 && strings.TrimSpace(raw) != "" {
		// Treat raw text as unsynced single-line lyrics
		result = lyrics.LyricsResult{Synced: false, Lines: []lyrics.LyricLine{{Text: raw}}}
	}
	return result
}

func fromOnlineRow(row storage.OnlineLyrics) resolvedLyrics {
	return resolvedLyrics{Result: parseLyricsText(row.Lyrics), Raw: row.Lyrics, Source: row.Source, Manual: row.Manual,
		RemoteTitle: row.RemoteTitle, RemoteArtist: row.RemoteArtist, TranslationLRC: row.TranslationLyrics}
}

// localLyrics reads the sidecar .lrc, then the embedded tag. errNoLyrics when neither has lyrics.
func (a *App) localLyrics(ctx context.Context, trackID int64) (resolvedLyrics, error) {
	audioPath, err := a.store.AudioFilePath(ctx, trackID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return resolvedLyrics{}, err
	}
	if err == nil {
		if lrcPath := lyrics.DetectLRCPath(audioPath); lrcPath != "" {
			content, readErr := readExternalLRC(lrcPath)
			if errors.Is(readErr, errExternalLRCTooLarge) {
				return resolvedLyrics{}, readErr
			}
			if readErr == nil {
				raw := string(normalizeLyricsBOM(content))
				if result := lyrics.ParseLRC(raw); len(result.Lines) > 0 {
					return resolvedLyrics{Result: result, Raw: raw, Source: lyricsSourceFile}, nil
				}
			}
		}
	}
	embedded, err := a.store.TrackLyrics(ctx, trackID)
	if err != nil {
		return resolvedLyrics{}, err
	}
	embedded = string(normalizeLyricsBOM([]byte(embedded)))
	if strings.TrimSpace(embedded) == "" {
		return resolvedLyrics{}, errNoLyrics
	}
	return resolvedLyrics{Result: parseLyricsText(embedded), Raw: embedded, Source: lyricsSourceEmbedded}, nil
}

// resolveLyrics returns a track's lyrics: a user's manual choice, then local
// lyrics (sidecar .lrc, embedded tag), then the online cache, then a fresh
// online lookup (cached, misses remembered for onlineLyricsRetry).
// Errors: sql.ErrNoRows (no such track), errExternalLRCTooLarge, errNoLyrics.
func (a *App) resolveLyrics(ctx context.Context, trackID int64) (resolvedLyrics, error) {
	info, err := a.store.LyricsTrackInfo(ctx, trackID)
	if err != nil {
		return resolvedLyrics{}, err
	}
	row, err := a.store.OnlineLyrics(ctx, trackID)
	hasRow := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return resolvedLyrics{}, err
	}
	if hasRow && row.Manual && row.Status == storage.OnlineLyricsFound {
		return fromOnlineRow(row), nil
	}
	if local, err := a.localLyrics(ctx, trackID); !errors.Is(err, errNoLyrics) {
		return local, err
	}
	if hasRow {
		switch row.Status {
		case storage.OnlineLyricsFound:
			return fromOnlineRow(row), nil
		case storage.OnlineLyricsSuppressed:
			return resolvedLyrics{}, errNoLyrics
		case storage.OnlineLyricsNotFound:
			if retry, err := time.Parse(time.RFC3339Nano, row.RetryAfter); err == nil && time.Now().Before(retry) {
				return resolvedLyrics{}, errNoLyrics
			}
		}
	}
	if a.onlineLyrics == nil || info.Instrumental() {
		return resolvedLyrics{}, errNoLyrics
	}
	return a.lyricsFlights.do("track:"+strconv.FormatInt(trackID, 10), func() (resolvedLyrics, error) {
		// Finish (and cache) even when the client skips to the next song.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), onlineLyricsTimeout)
		defer cancel()
		return a.fetchOnlineLyrics(fetchCtx, info)
	})
}

func (a *App) fetchOnlineLyrics(ctx context.Context, info storage.LyricsTrackInfo) (resolvedLyrics, error) {
	q := lyricsonline.Query{Title: info.Title, Artist: info.Artist, Album: info.Album, DurationMs: info.DurationMs}
	found, cand, err := a.onlineLyrics.Best(ctx, q)
	if errors.Is(err, lyricsonline.ErrNotFound) {
		if err := a.store.SaveOnlineLyricsNotFound(ctx, info.TrackID, time.Now().Add(onlineLyricsRetry)); err != nil {
			a.logger.Error("save online lyrics miss", "trackId", info.TrackID, "error", err)
		}
		return resolvedLyrics{}, errNoLyrics
	}
	if err != nil {
		// Network / rate limit: not cached, the next request tries again.
		a.logger.Warn("online lyrics lookup failed", "trackId", info.TrackID, "error", err)
		return resolvedLyrics{}, errNoLyrics
	}
	row := storage.OnlineLyrics{TrackID: info.TrackID, Source: found.Source, RemoteID: found.ID, RemoteTitle: cand.Title,
		RemoteArtist: cand.Artist, Lyrics: found.LRC, TranslationLyrics: found.TranslationLRC}
	if err := a.store.SaveOnlineLyricsFound(ctx, row); err != nil {
		a.logger.Error("save online lyrics", "trackId", info.TrackID, "error", err)
	}
	return fromOnlineRow(row), nil
}

type apiLyricsResponse struct {
	Synced       bool               `json:"synced"`
	Lines        []lyrics.LyricLine `json:"lines"`
	Source       string             `json:"source"`
	SourceName   string             `json:"sourceName"`
	Manual       bool               `json:"manual"`
	RemoteTitle  string             `json:"remoteTitle,omitempty"`
	RemoteArtist string             `json:"remoteArtist,omitempty"`
}

func lyricsSourceName(source string) string {
	switch source {
	case lyricsSourceFile:
		return "LRC 文件"
	case lyricsSourceEmbedded:
		return "内嵌歌词"
	}
	return lyricsonline.SourceName(source)
}

func lyricsResponse(r resolvedLyrics) apiLyricsResponse {
	lines := r.Result.Lines
	if lines == nil {
		lines = []lyrics.LyricLine{}
	}
	return apiLyricsResponse{Synced: r.Result.Synced, Lines: lines, Source: r.Source, SourceName: lyricsSourceName(r.Source),
		Manual: r.Manual, RemoteTitle: r.RemoteTitle, RemoteArtist: r.RemoteArtist}
}

// writeLyricsError maps resolveLyrics errors to API errors; it returns false when err is nil.
func (a *App) writeLyricsError(w http.ResponseWriter, trackID int64, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, sql.ErrNoRows):
		writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
	case errors.Is(err, errExternalLRCTooLarge):
		writeAPIError(w, http.StatusRequestEntityTooLarge, "lyrics_too_large", "External lyrics exceed 1 MiB.")
	case errors.Is(err, errNoLyrics):
		writeAPIError(w, http.StatusNotFound, "no_lyrics", "No lyrics available for this track.")
	default:
		a.logger.Error("resolve lyrics", "trackId", trackID, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
	}
	return true
}

func (a *App) handleAPITrackLyrics(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))
	res, err := a.resolveLyrics(r.Context(), trackID)
	if a.writeLyricsError(w, trackID, err) {
		return
	}
	writeJSON(w, http.StatusOK, lyricsResponse(res))
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

// handleAPITrackLyricsText returns the resolved lyrics as raw text (LRC when synced).
func (a *App) handleAPITrackLyricsText(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))
	res, err := a.resolveLyrics(r.Context(), trackID)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errNoLyrics) {
		writeAPIError(w, http.StatusNotFound, "no_lyrics", "No lyrics available for this track.")
		return
	}
	if errors.Is(err, errExternalLRCTooLarge) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "lyrics_too_large", "External lyrics exceed 1 MiB.")
		return
	}
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(res.Raw))
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
