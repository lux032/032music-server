package httpapi

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

type libraryPageData struct {
	Username, CSRFToken, Section, Query, Genre, Sort, Index, Notice, ReturnTo string
	ArtistRole, ArtistRoleLabel, ClearPath                                    string
	Year                                                                      int
	ArtistID, AlbumID                                                         int64
	Artists                                                                   []storage.Artist
	Albums                                                                    []storage.Album
	Tracks                                                                    []storage.Track
	Genres                                                                    []string
	Years                                                                     []int
	AlbumDetail                                                               *storage.Album
	Total                                                                     int64
	Page, PageCount, PageSize                                                 int
	PrevURL, NextURL                                                          string
	Pages                                                                     []pageLink
	SearchFields                                                              []queryField
	IndexLinks                                                                []indexLink
	FilterTags                                                                []filterTag
}

// queryField is a hidden form field that keeps the current filter state when
// a search form is submitted.
type queryField struct {
	Name, Value string
}

// indexLink is one entry of the letter index bar, carrying the full current
// filter state so letters refine rather than replace it.
type indexLink struct {
	Value, URL string
	Current    bool
}

// filterTag is a removable active-condition chip shown above the results.
type filterTag struct {
	Label, RemoveURL string
}

type pageLink struct {
	Number  int
	URL     string
	Current bool
}

func filters(r *http.Request) storage.Filters {
	q := r.URL.Query()
	return storage.Filters{Query: strings.TrimSpace(q.Get("q")), Genre: strings.TrimSpace(q.Get("genre")), Sort: q.Get("sort"), ArtistRole: q.Get("role"), Index: strings.TrimSpace(q.Get("index")), ArtistID: parseInt64(q.Get("artist")), AlbumID: parseInt64(q.Get("album")), Year: int(parseInt64(q.Get("year"))), Limit: int(parseInt64(q.Get("limit"))), Offset: int(parseInt64(q.Get("offset"))), HideInstrumental: q.Get("hideInstrumental") == "true", Favorite: q.Get("favorite") == "true"}
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
	path := "/admin/" + section
	return libraryPageData{Username: session.Username, CSRFToken: session.CSRFToken, Section: section, Query: f.Query, Genre: f.Genre, Sort: f.Sort, Index: f.Index, Year: f.Year, ArtistID: f.ArtistID, AlbumID: f.AlbumID, Genres: genres, Years: years, Notice: r.URL.Query().Get("notice"), ReturnTo: r.URL.RequestURI(), ClearPath: path, SearchFields: searchFields(r), IndexLinks: indexLinks(r, path), FilterTags: filterTags(r, path)}, nil
}

// filterQuery rebuilds the current filter query parameters without paging or
// notice state, so every control that changes a condition also resets to the
// first page.
func filterQuery(r *http.Request) url.Values {
	source := r.URL.Query()
	values := url.Values{}
	for _, name := range []string{"q", "artist", "album", "year", "genre", "sort", "index"} {
		if value := source.Get(name); value != "" && value != "0" {
			values.Set(name, value)
		}
	}
	return values
}

// searchFields keeps every active filter except the visible keyword input as
// hidden fields, so submitting a search refines the current view instead of
// discarding the other conditions.
func searchFields(r *http.Request) []queryField {
	values := filterQuery(r)
	values.Del("q")
	fields := make([]queryField, 0, len(values))
	for _, name := range []string{"artist", "album", "year", "genre", "sort", "index"} {
		if value := values.Get(name); value != "" {
			fields = append(fields, queryField{Name: name, Value: value})
		}
	}
	return fields
}

func indexLinks(r *http.Request, path string) []indexLink {
	base := filterQuery(r)
	current := base.Get("index")
	base.Del("index")
	links := make([]indexLink, 0, len(indexLetters))
	for _, letter := range indexLetters {
		values := url.Values{}
		for name, list := range base {
			values[name] = append([]string(nil), list...)
		}
		values.Set("index", letter)
		links = append(links, indexLink{Value: letter, URL: path + "?" + values.Encode(), Current: letter == current})
	}
	return links
}

// filterTags renders the active conditions as removable chips. Names for
// artist and album conditions are resolved by resolveFilterTagNames once the
// handler has loaded the option lists.
func filterTags(r *http.Request, path string) []filterTag {
	base := filterQuery(r)
	tags := []filterTag{}
	add := func(name, label string) {
		values := url.Values{}
		for key, list := range base {
			values[key] = append([]string(nil), list...)
		}
		values.Del(name)
		removeURL := path
		if encoded := values.Encode(); encoded != "" {
			removeURL += "?" + encoded
		}
		tags = append(tags, filterTag{Label: label, RemoveURL: removeURL})
	}
	if value := base.Get("q"); value != "" {
		add("q", "关键词："+value)
	}
	if value := base.Get("artist"); value != "" {
		add("artist", "artist:"+value)
	}
	if value := base.Get("album"); value != "" {
		add("album", "album:"+value)
	}
	if value := base.Get("year"); value != "" {
		add("year", "年份："+value)
	}
	if value := base.Get("genre"); value != "" {
		add("genre", "流派："+value)
	}
	if value := base.Get("index"); value != "" {
		add("index", "首字母："+value)
	}
	if value := base.Get("sort"); value != "" {
		add("sort", "排序："+value)
	}
	return tags
}

// ensureSelectedArtist appends the currently filtered artist to the dropdown
// option list when it falls outside the first options page, so the <select>
// can display it and resolveFilterTagNames can label its chip.
func (a *App) ensureSelectedArtist(r *http.Request, data *libraryPageData) {
	if data.ArtistID == 0 {
		return
	}
	for _, artist := range data.Artists {
		if artist.ID == data.ArtistID {
			return
		}
	}
	if name, err := a.store.ArtistNameByID(r.Context(), data.ArtistID); err == nil {
		data.Artists = append(data.Artists, storage.Artist{ID: data.ArtistID, Name: name})
	}
}

// ensureSelectedAlbum does the same for the album dropdown. It must only run
// on pages where data.Albums holds dropdown options (the tracks page): on the
// albums page data.Albums IS the result grid, and appending a bare
// Album{ID,Title} would render a phantom card.
func (a *App) ensureSelectedAlbum(r *http.Request, data *libraryPageData) {
	if data.AlbumID == 0 {
		return
	}
	for _, album := range data.Albums {
		if album.ID == data.AlbumID {
			return
		}
	}
	if title, err := a.store.AlbumTitleByID(r.Context(), data.AlbumID); err == nil {
		data.Albums = append(data.Albums, storage.Album{ID: data.AlbumID, Title: title})
	}
}

// adminOptionsPageLimit caps how many dropdown options browse pages load.
// It is a var so tests can shrink it to exercise the beyond-first-page
// fallback in ensureSelectedArtist/ensureSelectedAlbum.
var adminOptionsPageLimit = 500

// optionItem is the JSON shape of the lightweight dropdown endpoints.
type optionItem struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

// optionsLimit parses the limit query parameter for the dropdown endpoints:
// default 20, capped at 100.
func optionsLimit(r *http.Request) int {
	limit := int(parseInt64(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return limit
}

func (a *App) handleAdminArtistOptions(w http.ResponseWriter, r *http.Request) {
	artists, err := a.store.ListArtistOptions(r.Context(), r.URL.Query().Get("role"), r.URL.Query().Get("q"), optionsLimit(r))
	if err != nil {
		writeAPIError(w, 500, "query_failed", err.Error())
		return
	}
	items := make([]optionItem, 0, len(artists))
	for _, artist := range artists {
		items = append(items, optionItem{ID: artist.ID, Label: artist.Name})
	}
	writeJSON(w, 200, items)
}

func (a *App) handleAdminAlbumOptions(w http.ResponseWriter, r *http.Request) {
	albums, err := a.store.ListAlbumOptions(r.Context(), r.URL.Query().Get("q"), optionsLimit(r))
	if err != nil {
		writeAPIError(w, 500, "query_failed", err.Error())
		return
	}
	items := make([]optionItem, 0, len(albums))
	for _, album := range albums {
		items = append(items, optionItem{ID: album.ID, Label: album.Title})
	}
	writeJSON(w, 200, items)
}

// resolveFilterTagNames replaces raw ID placeholders in filter chips with the
// display names of the selected artist and album.
func (data *libraryPageData) resolveFilterTagNames() {
	artistNames := map[string]string{}
	for _, artist := range data.Artists {
		artistNames[strconv.FormatInt(artist.ID, 10)] = artist.Name
	}
	albumTitles := map[string]string{}
	for _, album := range data.Albums {
		albumTitles[strconv.FormatInt(album.ID, 10)] = album.Title
	}
	for index := range data.FilterTags {
		label := data.FilterTags[index].Label
		if name, ok := artistNames[strings.TrimPrefix(label, "artist:")]; ok && strings.HasPrefix(label, "artist:") {
			data.FilterTags[index].Label = "歌手：" + name
		}
		if title, ok := albumTitles[strings.TrimPrefix(label, "album:")]; ok && strings.HasPrefix(label, "album:") {
			data.FilterTags[index].Label = "专辑：" + title
		}
	}
}

func (a *App) handleArtistsPage(w http.ResponseWriter, r *http.Request) {
	target := "/admin/artists/album"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) handleAlbumArtistsPage(w http.ResponseWriter, r *http.Request) {
	a.handleArtistsByRole(w, r, "album", "专辑歌手")
}

func (a *App) handleTrackArtistsPage(w http.ResponseWriter, r *http.Request) {
	a.handleArtistsByRole(w, r, "track", "单曲歌手")
}

func (a *App) handleArtistsByRole(w http.ResponseWriter, r *http.Request, role, label string) {
	data, err := a.pageBase(r, "artists")
	f := filters(r)
	f.ArtistRole = role
	f.Favorite = false // The favorite query parameter belongs to the public artists API.
	data.ArtistRole = role
	data.ArtistRoleLabel = label
	data.ClearPath = "/admin/artists/" + role
	data.IndexLinks = indexLinks(r, data.ClearPath)
	data.FilterTags = filterTags(r, data.ClearPath)
	applyPage(r, &f, 60)
	if err == nil {
		data.Artists, err = a.store.ListArtists(r.Context(), f)
	}
	if err == nil {
		data.Total, err = a.store.CountArtists(r.Context(), f)
	}
	data.resolveFilterTagNames()
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
		data.Artists, _ = a.store.ListArtistOptions(r.Context(), "album", "", adminOptionsPageLimit)
	}
	if err == nil {
		// data.Albums is the result grid here, not a dropdown: only the
		// artist filter may append its selected option.
		a.ensureSelectedArtist(r, &data)
	}
	data.resolveFilterTagNames()
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
		data.Artists, _ = a.store.ListArtistOptions(r.Context(), "track", "", adminOptionsPageLimit)
		data.Albums, _ = a.store.ListAlbumOptions(r.Context(), "", adminOptionsPageLimit)
	}
	if err == nil {
		a.ensureSelectedArtist(r, &data)
		a.ensureSelectedAlbum(r, &data)
	}
	data.resolveFilterTagNames()
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
	redirectWithNotice(w, r, adminReturnPath(r, "/admin/artists/album"), "已保存")
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
	if err := a.store.UpdateTrack(r.Context(), parseInt64(r.PathValue("id")), r.FormValue("title"), int(parseInt64(r.FormValue("disc"))), int(parseInt64(r.FormValue("number"))), r.FormValue("composer"), r.FormValue("trackType"), splitCSV(r.FormValue("genres"))); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	redirectWithNotice(w, r, adminReturnPath(r, "/admin/tracks"), "已保存")
}
func (a *App) validCSRF(r *http.Request) bool {
	_ = r.ParseForm()
	session, ok := a.sessions.get(r)
	return ok && secureEqual(r.FormValue("csrfToken"), session.CSRFToken)
}

func (a *App) handleAPIArtists(w http.ResponseWriter, r *http.Request) {
	f := filters(r)
	limit, offset := pageValues(r)
	f.Limit, f.Offset = limit, offset
	values, err := a.store.ListArtists(r.Context(), f)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	total, err := a.store.CountArtists(r.Context(), f)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	writePage(w, values, total, limit, offset)
}
func (a *App) handleAPIAlbums(w http.ResponseWriter, r *http.Request) {
	f := filters(r)
	if f.Limit <= 0 {
		f.Limit = 48
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
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
	f := filters(r)
	limit, offset := pageValues(r)
	f.Limit, f.Offset = limit, offset
	values, err := a.store.ListTracks(r.Context(), f)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	total, err := a.store.CountTracks(r.Context(), f)
	if err != nil {
		apiResult(w, values, err)
		return
	}
	writePage(w, values, total, limit, offset)
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
		Title, Composer, TrackType string
		DiscNumber, TrackNumber    int
		Genres                     []string
	}
	if !decode(w, r, &v) {
		return
	}
	apiNoContent(w, a.store.UpdateTrack(r.Context(), parseInt64(r.PathValue("id")), v.Title, v.DiscNumber, v.TrackNumber, v.Composer, v.TrackType, v.Genres))
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
	trackID := parseInt64(r.PathValue("id"))
	path, mimeType, err := a.store.AudioPath(r.Context(), trackID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		a.logger.Error("open audio file for streaming", "trackID", trackID, "path", path, "error", err)
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".flac":
		mimeType = "audio/flac"
	case ".mp3":
		mimeType = "audio/mpeg"
	case ".m4a", ".mp4":
		mimeType = "audio/mp4"
	case ".aac":
		mimeType = "audio/aac"
	case ".ogg", ".oga":
		mimeType = "audio/ogg"
	case ".opus":
		mimeType = "audio/opus"
	case ".wav":
		mimeType = "audio/wav"
	case ".webm":
		mimeType = "audio/webm"
	default:
		if mimeType == "" || mimeType == "application/octet-stream" {
			mimeType = "audio/flac"
		}
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Authorization, Content-Type, Accept")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
	// Remove CSP header for media streams: it is only meaningful for HTML
	// documents and can confuse certain browser media pipelines.
	w.Header().Del("Content-Security-Policy")

	var content io.ReadSeeker = file
	if ext == ".flac" {
		if patches := flacMetadataPatches(file); len(patches) > 0 {
			content = &patchedReadSeeker{source: file, size: info.Size(), patches: patches}
		}
	}
	http.ServeContent(w, r, "stream"+filepath.Ext(path), info.ModTime(), content)
}

// flacMetadataPatches walks the FLAC metadata section and returns byte
// patches that replace invalid PICTURE block picture types with "front
// cover" (3). Chromium's demuxer hard-fails on picture types outside the
// spec range 0-20 and reports the whole file as unplayable, while most
// desktop players merely warn, so files like this look fine everywhere
// except the browser.
func flacMetadataPatches(file *os.File) map[int64][4]byte {
	var magic [4]byte
	if _, err := file.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "fLaC" {
		return nil
	}
	var patches map[int64][4]byte
	offset := int64(4)
	for range 128 { // generous upper bound on metadata block count
		var header [4]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			break
		}
		isLast := header[0]&0x80 != 0
		blockType := header[0] & 0x7f
		length := int64(header[1])<<16 | int64(header[2])<<8 | int64(header[3])
		if blockType == 6 && length >= 4 {
			var picType [4]byte
			if _, err := file.ReadAt(picType[:], offset+4); err == nil && binary.BigEndian.Uint32(picType[:]) > 20 {
				if patches == nil {
					patches = make(map[int64][4]byte)
				}
				patches[offset+4] = [4]byte{0, 0, 0, 3}
			}
		}
		offset += 4 + length
		if isLast {
			break
		}
	}
	return patches
}

// patchedReadSeeker serves the underlying file with a few byte ranges
// replaced in flight. Sizes and offsets are unchanged, so range requests
// and http.ServeContent keep working as if the file itself were fixed.
type patchedReadSeeker struct {
	source  io.ReaderAt
	size    int64
	offset  int64
	patches map[int64][4]byte
}

func (p *patchedReadSeeker) Read(buf []byte) (int, error) {
	if p.offset >= p.size {
		return 0, io.EOF
	}
	n, err := p.source.ReadAt(buf, p.offset)
	for patchOffset, replacement := range p.patches {
		for i := range int64(len(replacement)) {
			bufIndex := patchOffset + i - p.offset
			if bufIndex >= 0 && bufIndex < int64(n) {
				buf[bufIndex] = replacement[i]
			}
		}
	}
	p.offset += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func (p *patchedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		p.offset = offset
	case io.SeekCurrent:
		p.offset += offset
	case io.SeekEnd:
		p.offset = p.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if p.offset < 0 {
		return 0, errors.New("negative seek position")
	}
	return p.offset, nil
}
func (a *App) handleArtwork(w http.ResponseWriter, r *http.Request) {
	size, err := thumbnailSize(r)
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "Invalid size.")
		return
	}
	id := parseInt64(r.PathValue("id"))
	path, mimeType, err := a.store.ArtworkPath(r.Context(), id)
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
	if size != 0 {
		if a.serveThumbnail(w, r, file, info, "artwork", id, size, "public, max-age=31536000, immutable") {
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Type", mimeType)
	if size == 0 {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
func (a *App) handleArtistImage(w http.ResponseWriter, r *http.Request) {
	size, err := thumbnailSize(r)
	if err != nil {
		writeAPIError(w, 400, "invalid_request", "Invalid size.")
		return
	}
	id := parseInt64(r.PathValue("id"))
	path, mimeType, err := a.store.ArtistImagePath(r.Context(), id)
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
	if size != 0 {
		if a.serveThumbnail(w, r, file, info, "artist", id, size, "public, max-age=86400") {
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	w.Header().Set("Content-Type", mimeType)
	if size == 0 {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
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
