package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

type libraryPageData struct {
	Username, CSRFToken, Section, Query, Genre, Sort, Notice string
	Year                                                     int
	ArtistID, AlbumID                                        int64
	Artists                                                  []storage.Artist
	Albums                                                   []storage.Album
	Tracks                                                   []storage.Track
	Genres                                                   []string
	Years                                                    []int
	AlbumDetail                                              *storage.Album
	Total                                                    int64
	Page, PageCount, PageSize                                int
	PrevURL, NextURL                                         string
	Pages                                                    []pageLink
}

type pageLink struct {
	Number  int
	URL     string
	Current bool
}

func filters(r *http.Request) storage.Filters {
	q := r.URL.Query()
	return storage.Filters{Query: strings.TrimSpace(q.Get("q")), Genre: strings.TrimSpace(q.Get("genre")), Sort: q.Get("sort"), ArtistID: parseInt64(q.Get("artist")), AlbumID: parseInt64(q.Get("album")), Year: int(parseInt64(q.Get("year"))), Limit: int(parseInt64(q.Get("limit"))), Offset: int(parseInt64(q.Get("offset")))}
}

func (a *App) pageBase(r *http.Request, section string) (libraryPageData, error) {
	session, _ := a.sessions.get(r)
	f := filters(r)
	genres, err := a.store.Genres(r.Context())
	if err != nil {
		return libraryPageData{}, err
	}
	years, err := a.store.Years(r.Context())
	if err != nil {
		return libraryPageData{}, err
	}
	return libraryPageData{Username: session.Username, CSRFToken: session.CSRFToken, Section: section, Query: f.Query, Genre: f.Genre, Sort: f.Sort, Year: f.Year, ArtistID: f.ArtistID, AlbumID: f.AlbumID, Genres: genres, Years: years, Notice: r.URL.Query().Get("notice")}, nil
}

func (a *App) handleArtistsPage(w http.ResponseWriter, r *http.Request) {
	data, err := a.pageBase(r, "artists")
	f := filters(r)
	applyPage(r, &f, 60)
	if err == nil {
		data.Artists, err = a.store.ListArtists(r.Context(), f)
	}
	if err == nil {
		data.Total, err = a.store.CountArtists(r.Context(), f)
	}
	setPagination(r, &data, f)
	a.renderLibrary(w, data, err)
}
func (a *App) handleAlbumsPage(w http.ResponseWriter, r *http.Request) {
	data, err := a.pageBase(r, "albums")
	f := filters(r)
	applyPage(r, &f, 48)
	if err == nil {
		data.Albums, err = a.store.ListAlbums(r.Context(), f)
	}
	if err == nil {
		data.Total, err = a.store.CountAlbums(r.Context(), f)
	}
	if err == nil {
		data.Artists, _ = a.store.ListArtists(r.Context(), storage.Filters{Limit: 500})
	}
	setPagination(r, &data, f)
	a.renderLibrary(w, data, err)
}
func (a *App) handleTracksPage(w http.ResponseWriter, r *http.Request) {
	data, err := a.pageBase(r, "tracks")
	f := filters(r)
	applyPage(r, &f, 50)
	if err == nil {
		data.Tracks, err = a.store.ListTracks(r.Context(), f)
	}
	if err == nil {
		data.Total, err = a.store.CountTracks(r.Context(), f)
	}
	if err == nil {
		data.Artists, _ = a.store.ListArtists(r.Context(), storage.Filters{Limit: 500})
		data.Albums, _ = a.store.ListAlbums(r.Context(), storage.Filters{Limit: 500})
	}
	setPagination(r, &data, f)
	a.renderLibrary(w, data, err)
}

func (a *App) handleAlbumPage(w http.ResponseWriter, r *http.Request) {
	data, err := a.pageBase(r, "albums")
	if err == nil {
		album, e := a.store.AlbumByID(r.Context(), parseInt64(r.PathValue("id")))
		err = e
		data.AlbumDetail = &album
	}
	if err == nil {
		data.Tracks, err = a.store.ListTracks(r.Context(), storage.Filters{AlbumID: data.AlbumDetail.ID, Limit: 500})
	}
	if err == nil {
		trackArtists, artistErr := a.store.ArtistsForAlbumTracks(r.Context(), data.AlbumDetail.ID)
		err = artistErr
		for index := range data.Tracks {
			data.Tracks[index].Artists = trackArtists[data.Tracks[index].ID]
		}
	}
	if err == nil {
		data.Artists, err = a.store.ArtistsForAlbum(r.Context(), data.AlbumDetail.ID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		a.renderLibrary(w, data, err)
		return
	}
	a.render(w, http.StatusOK, "album.html", data)
}
func (a *App) renderLibrary(w http.ResponseWriter, data libraryPageData, err error) {
	if err != nil {
		a.logger.Error("load library page", "section", data.Section, "error", err)
		http.Error(w, "library unavailable", http.StatusInternalServerError)
		return
	}
	a.render(w, http.StatusOK, "library.html", data)
}

func (a *App) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.Statistics(r.Context())
	if err != nil {
		writeAPIError(w, 500, "statistics_unavailable", err.Error())
		return
	}
	job := latestScan(a.store, r)
	writeJSON(w, 200, map[string]any{"statistics": stats, "scan": job})
}
func latestScan(store *storage.Store, r *http.Request) storage.ScanJob {
	job, err := store.LatestScanJob(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ScanJob{Status: "never"}
	}
	return job
}
func (a *App) handleStartScan(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	job, err := a.scanner.Start(r.Context(), r.FormValue("type"))
	if err != nil {
		http.Redirect(w, r, "/admin?notice="+"扫描已在运行", 303)
		return
	}
	a.logger.Info("manual scan started", "jobId", job)
	http.Redirect(w, r, "/admin?notice="+"扫描已启动", 303)
}

func (a *App) handleUpdateArtist(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	_ = r.ParseForm()
	if err := a.store.UpdateArtist(r.Context(), parseInt64(r.PathValue("id")), r.FormValue("name")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/artists?notice=已保存", 303)
}
func (a *App) handleUpdateAlbum(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	_ = r.ParseForm()
	id := parseInt64(r.PathValue("id"))
	edit := storage.AlbumEdit{Title: r.FormValue("title"), PerformedBy: r.FormValue("performedBy"), AlbumType: r.FormValue("albumType"), Version: r.FormValue("version"), Year: int(parseInt64(r.FormValue("year"))), ReleaseDate: r.FormValue("releaseDate"), OriginalReleaseDate: r.FormValue("originalReleaseDate"), Label: r.FormValue("label"), CatalogNumber: r.FormValue("catalogNumber"), Country: r.FormValue("country"), Review: r.FormValue("review"), Compilation: r.FormValue("compilation") != "", Live: r.FormValue("live") != "", Bootleg: r.FormValue("bootleg") != "", Genres: splitCSV(r.FormValue("genres"))}
	if err := a.store.UpdateAlbum(r.Context(), id, edit); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/albums/"+strconv.FormatInt(id, 10)+"?notice=专辑信息已保存", 303)
}
func (a *App) handleUpdateTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	_ = r.ParseForm()
	if err := a.store.UpdateTrack(r.Context(), parseInt64(r.PathValue("id")), r.FormValue("title"), int(parseInt64(r.FormValue("disc"))), int(parseInt64(r.FormValue("number"))), r.FormValue("composer"), splitCSV(r.FormValue("genres"))); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/tracks?notice=已保存", 303)
}
func (a *App) validCSRF(r *http.Request) bool {
	_ = r.ParseForm()
	session, ok := a.sessions.get(r)
	return ok && secureEqual(r.FormValue("csrfToken"), session.CSRFToken)
}

func (a *App) handleAPIArtists(w http.ResponseWriter, r *http.Request) {
	values, err := a.store.ListArtists(r.Context(), filters(r))
	apiResult(w, values, err)
}
func (a *App) handleAPIAlbums(w http.ResponseWriter, r *http.Request) {
	f := filters(r)
	if f.Limit <= 0 {
		f.Limit = 48
	}
	values, err := a.store.ListAlbums(r.Context(), f)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	total, err := a.store.CountAlbums(r.Context(), f)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": values, "total": total, "limit": f.Limit, "offset": f.Offset})
}
func (a *App) handleAPIAlbum(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.AlbumByID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, 404, "not_found", "Album not found.")
			return
		}
		apiResult(w, value, err)
		return
	}
	tracks, err := a.store.ListTracks(r.Context(), storage.Filters{AlbumID: value.ID, Limit: 500})
	if err != nil {
		apiResult(w, value, err)
		return
	}
	writeJSON(w, 200, map[string]any{"album": value, "tracks": tracks})
}
func (a *App) handleAPITracks(w http.ResponseWriter, r *http.Request) {
	values, err := a.store.ListTracks(r.Context(), filters(r))
	apiResult(w, values, err)
}
func apiResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeAPIError(w, 500, "query_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": value})
}

func (a *App) handleAPIUpdateArtist(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &v) {
		return
	}
	apiNoContent(w, a.store.UpdateArtist(r.Context(), parseInt64(r.PathValue("id")), v.Name))
}
func (a *App) handleAPIUpdateAlbum(w http.ResponseWriter, r *http.Request) {
	var v storage.AlbumEdit
	if !decode(w, r, &v) {
		return
	}
	apiNoContent(w, a.store.UpdateAlbum(r.Context(), parseInt64(r.PathValue("id")), v))
}
func (a *App) handleAPIUpdateTrack(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Title, Composer         string
		DiscNumber, TrackNumber int
		Genres                  []string
	}
	if !decode(w, r, &v) {
		return
	}
	apiNoContent(w, a.store.UpdateTrack(r.Context(), parseInt64(r.PathValue("id")), v.Title, v.DiscNumber, v.TrackNumber, v.Composer, v.Genres))
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeAPIError(w, 400, "invalid_json", err.Error())
		return false
	}
	return true
}
func apiNoContent(w http.ResponseWriter, err error) {
	if err != nil {
		writeAPIError(w, 500, "update_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	path, mimeType, err := a.store.AudioPath(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	http.ServeFile(w, r, path)
}
func (a *App) handleArtwork(w http.ResponseWriter, r *http.Request) {
	path, mimeType, err := a.store.ArtworkPath(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
func (a *App) handleArtistImage(w http.ResponseWriter, r *http.Request) {
	path, mimeType, err := a.store.ArtistImagePath(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func parseInt64(value string) int64 { number, _ := strconv.ParseInt(value, 10, 64); return number }
func splitCSV(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '、' })
}

func applyPage(r *http.Request, f *storage.Filters, size int) {
	page := int(parseInt64(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	f.Limit = size
	f.Offset = (page - 1) * size
}
func setPagination(r *http.Request, data *libraryPageData, f storage.Filters) {
	data.PageSize = f.Limit
	data.Page = f.Offset/f.Limit + 1
	data.PageCount = int((data.Total + int64(f.Limit) - 1) / int64(f.Limit))
	if data.PageCount < 1 {
		data.PageCount = 1
	}
	makeURL := func(page int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(page))
		return r.URL.Path + "?" + q.Encode()
	}
	if data.Page > 1 {
		data.PrevURL = makeURL(data.Page - 1)
	}
	if data.Page < data.PageCount {
		data.NextURL = makeURL(data.Page + 1)
	}
	start := data.Page - 2
	if start < 1 {
		start = 1
	}
	end := start + 4
	if end > data.PageCount {
		end = data.PageCount
		start = end - 4
		if start < 1 {
			start = 1
		}
	}
	for i := start; i <= end; i++ {
		data.Pages = append(data.Pages, pageLink{Number: i, URL: makeURL(i), Current: i == data.Page})
	}
}
