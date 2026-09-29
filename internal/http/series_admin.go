package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

// ---------------------------------------------------------------
// Phase 4.6 批次 6：系列建议 Tab 与系列管理页（D51/D54/D58/D61）
// ---------------------------------------------------------------

// seriesMemberGroup 是 D51 的类型分组：系列成员按类型分组显示，组内保持
// 存储层“最早播出在前”的顺序；只有一种类型时 ShowHeader 为 false（不显示
// 分组头）。作品列表展开区、作品详情页系列条与系列管理详情共用。
type seriesMemberGroup struct {
	TypeLabel  string
	Members    []storage.WorkSeriesMember
	ShowHeader bool
}

func groupSeriesMembersByType(members []storage.WorkSeriesMember) []seriesMemberGroup {
	var groups []seriesMemberGroup
	index := map[string]int{}
	for _, member := range members {
		i, ok := index[member.Work.Type]
		if !ok {
			i = len(groups)
			index[member.Work.Type] = i
			groups = append(groups, seriesMemberGroup{TypeLabel: workTypeLabel(member.Work.Type)})
		}
		groups[i].Members = append(groups[i].Members, member)
	}
	show := len(groups) > 1
	for i := range groups {
		groups[i].ShowHeader = show
	}
	return groups
}

// seriesSuggestionCard 是“系列建议”Tab 中一条建议的视图模型（D53/D54）。
type seriesSuggestionCard struct {
	ID            int64
	CSRFToken     string
	WorkA, WorkB  storage.Work
	RelationLabel string // 短标签胶囊，如 “动画 ↔ 游戏”（顺序见下）
	// RelationSentence 是自然语言关系描述，如 “《B》是《A》的续集”。
	RelationSentence string
	EffectPreview    string // 接受后的效果预览（按当前归属）
	// 合并命名（D58）：Merge 为 true 时两端分属不同系列。NamingMode：
	// "both"＝双方都改过名（必须选择，提供单选）；"one"＝只有一方改过名
	// （默认保留该方）；"auto"＝双方都是自动名（不显示命名选项）。
	Merge              bool
	NamingMode         string
	SeriesATitle       string
	SeriesBTitle       string
	DefaultTitleChoice string // "a" | "b"（NamingMode=="one" 时的默认选中）
	DefaultKeepTitle   string // 默认保留的名字（展示用）
}

// seriesSuggestionGroup 把“同一部作品出现在多条建议中”的建议收拢到一个
// 分组头下，避免个别作品（如 FGO）刷屏。
type seriesSuggestionGroup struct {
	Work storage.Work
	// Total 是该作品涉及的建议总数（L1：建议可能被归到另一端的分组，
	// 所以不用 len(Cards)）。
	Total int
	Cards []seriesSuggestionCard
}

// seriesRelationSentence 把双向关系标签翻成自然语言：relationAB 是 A 的
// Bangumi 关系列表里对 B 的标注，也就是在描述 B——所以读作
// “《B》是《A》的 relationAB”，反方向同理。两个方向的关系词相同时
// （例如“不同演绎”）合成一句“互为”。
func seriesRelationSentence(workA, workB storage.Work, relationAB, relationBA string) string {
	if relationAB == relationBA {
		return fmt.Sprintf("《%s》与《%s》互为%s", workA.Title, workB.Title, relationAB)
	}
	return fmt.Sprintf("《%s》是《%s》的%s；《%s》是《%s》的%s", workB.Title, workA.Title, relationAB, workA.Title, workB.Title, relationBA)
}

func (a *App) seriesSuggestionCards(csrfToken string, suggestions []storage.SeriesSuggestion) ([]seriesSuggestionGroup, []seriesSuggestionCard) {
	cards := make([]seriesSuggestionCard, 0, len(suggestions))
	counts := map[int64]int{}
	for _, suggestion := range suggestions {
		counts[suggestion.WorkA.ID]++
		counts[suggestion.WorkB.ID]++
	}
	for _, suggestion := range suggestions {
		card := seriesSuggestionCard{
			ID:            suggestion.ID,
			CSRFToken:     csrfToken,
			WorkA:         suggestion.WorkA,
			WorkB:         suggestion.WorkB,
			RelationLabel: suggestion.RelationBA + " ↔ " + suggestion.RelationAB,
			// 胶囊按 RelationBA ↔ RelationAB 排列：每个词紧挨着它所描述的那部
			// 作品（relationBA 描述 A、靠左；relationAB 描述 B、靠右）。
			RelationSentence: seriesRelationSentence(suggestion.WorkA, suggestion.WorkB, suggestion.RelationAB, suggestion.RelationBA),
		}
		switch {
		case suggestion.SeriesA == nil && suggestion.SeriesB == nil:
			card.EffectPreview = fmt.Sprintf("新建系列（%s、%s）", suggestion.WorkA.Title, suggestion.WorkB.Title)
		case suggestion.SeriesA != nil && suggestion.SeriesB == nil:
			card.EffectPreview = fmt.Sprintf("把 %s 加入《%s》", suggestion.WorkB.Title, suggestion.SeriesA.Title)
		case suggestion.SeriesA == nil && suggestion.SeriesB != nil:
			card.EffectPreview = fmt.Sprintf("把 %s 加入《%s》", suggestion.WorkA.Title, suggestion.SeriesB.Title)
		case suggestion.SeriesA.ID == suggestion.SeriesB.ID:
			card.EffectPreview = "两部作品已在同一系列，接受后仅关闭建议"
		default:
			card.Merge = true
			card.SeriesATitle = suggestion.SeriesA.Title
			card.SeriesBTitle = suggestion.SeriesB.Title
			card.EffectPreview = fmt.Sprintf("合并《%s》与《%s》", suggestion.SeriesA.Title, suggestion.SeriesB.Title)
			manualA := suggestion.SeriesA.TitleSource == "manual"
			manualB := suggestion.SeriesB.TitleSource == "manual"
			switch {
			case manualA && manualB:
				card.NamingMode = "both"
			case manualA:
				card.NamingMode = "one"
				card.DefaultTitleChoice = "a"
				card.DefaultKeepTitle = suggestion.SeriesA.Title
			case manualB:
				card.NamingMode = "one"
				card.DefaultTitleChoice = "b"
				card.DefaultKeepTitle = suggestion.SeriesB.Title
			default:
				card.NamingMode = "auto"
			}
		}
		cards = append(cards, card)
	}
	// 按作品分组：出现 ≥2 次的作品收拢为分组头（同一建议两端都高频时归入 A 端）。
	groupIndex := map[int64]int{}
	var groups []seriesSuggestionGroup
	var singles []seriesSuggestionCard
	for i, suggestion := range suggestions {
		key := int64(0)
		switch {
		case counts[suggestion.WorkA.ID] >= 2:
			key = suggestion.WorkA.ID
		case counts[suggestion.WorkB.ID] >= 2:
			key = suggestion.WorkB.ID
		}
		if key == 0 {
			singles = append(singles, cards[i])
			continue
		}
		idx, ok := groupIndex[key]
		if !ok {
			idx = len(groups)
			groupIndex[key] = idx
			work := suggestion.WorkA
			if work.ID != key {
				work = suggestion.WorkB
			}
			groups = append(groups, seriesSuggestionGroup{Work: work, Total: counts[key]})
		}
		groups[idx].Cards = append(groups[idx].Cards, cards[i])
	}
	return groups, singles
}

// mergeTitleFromForm 按 D58 的单选结果解析合并后的名字：保留 A / 保留 B /
// 新名字。命名选项不可见（auto/one 模式）时返回空串交给存储层自动处理。
func mergeTitleFromForm(r *http.Request) (string, string) {
	switch r.FormValue("titleChoice") {
	case "a":
		return strings.TrimSpace(r.FormValue("seriesTitleA")), ""
	case "b":
		return strings.TrimSpace(r.FormValue("seriesTitleB")), ""
	case "new":
		title := strings.TrimSpace(r.FormValue("title"))
		if title == "" {
			return "", "请填写合并后的新名字"
		}
		return title, ""
	}
	return "", ""
}

// handleAdminSeriesSuggestionDecision 处理系列建议的接受/拒绝（管理员 +
// CSRF + returnTo 回到系列建议 Tab）。冲突与失效都给友好提示，不存在返回
// 404，任何情况都不返回 500。
func (a *App) handleAdminSeriesSuggestionDecision(w http.ResponseWriter, r *http.Request, accept bool) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review?tab=series")
	if !accept {
		if err := a.store.RejectSeriesSuggestion(r.Context(), id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			a.logger.Error("reject series suggestion", "error", err)
			redirectWithNotice(w, r, returnTo, "拒绝建议失败，请重试")
			return
		}
		redirectWithNotice(w, r, returnTo, "建议已拒绝，这对作品以后不会再出现")
		return
	}
	mergeTitle, formErr := mergeTitleFromForm(r)
	if formErr != "" {
		redirectWithNotice(w, r, returnTo, formErr)
		return
	}
	err := a.store.AcceptSeriesSuggestion(r.Context(), id, mergeTitle)
	switch {
	case err == nil:
		redirectWithNotice(w, r, returnTo, "建议已接受")
	case errors.Is(err, storage.ErrSeriesTitleConflict):
		redirectWithNotice(w, r, returnTo, "两个系列都改过名，请选择合并后的名字")
	case errors.Is(err, storage.ErrSeriesSuggestionStale):
		redirectWithNotice(w, r, returnTo, "建议已失效，已移除")
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	default:
		a.logger.Error("accept series suggestion", "error", err)
		redirectWithNotice(w, r, returnTo, "接受建议失败，请重试")
	}
}

// ---------------------------------------------------------------
// 系列管理页
// ---------------------------------------------------------------

type seriesListRow struct {
	Series    storage.WorkSeries
	PosterURL string
	// TypeSpread 是类型分布，如 “动画 3 · 游戏 1”（数量降序，同量按类型名）。
	TypeSpread string
}

type seriesListPageData struct {
	Chrome
	Query, Notice             string
	Rows                      []seriesListRow
	Page, PageCount, PageSize int
	Total                     int
	PrevURL, NextURL          string
	Pages                     []pageLink
}

func seriesTypeSpread(counts map[string]int) string {
	type pair struct {
		typ   string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for typ, count := range counts {
		pairs = append(pairs, pair{typ, count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].typ < pairs[j].typ
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s %d", workTypeLabel(p.typ), p.count))
	}
	return strings.Join(parts, " · ")
}

func (a *App) handleAdminSeriesList(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	page := int(parseInt64(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	const pageSize = 50
	rows, total, err := a.store.ListSeriesPage(r.Context(), query, pageSize, (page-1)*pageSize)
	if err != nil {
		a.logger.Error("series list", "error", err)
		http.Error(w, "series unavailable", http.StatusInternalServerError)
		return
	}
	// 批量取代表作，解析代表作海报（无 N+1）。
	repIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.Series.RepresentativeWorkID != 0 {
			repIDs = append(repIDs, row.Series.RepresentativeWorkID)
		}
	}
	repList, err := a.store.WorksByIDs(r.Context(), repIDs)
	if err != nil {
		a.logger.Error("series list representatives", "error", err)
		http.Error(w, "series unavailable", http.StatusInternalServerError)
		return
	}
	repWorks := make(map[int64]storage.Work, len(repList))
	for _, work := range repList {
		repWorks[work.ID] = work
	}
	data := seriesListPageData{
		Chrome:   a.chromeFor(r.Context(), session, "series"),
		Query:    query,
		Notice:   r.URL.Query().Get("notice"),
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}
	data.Rows = make([]seriesListRow, 0, len(rows))
	for _, row := range rows {
		view := seriesListRow{Series: row.Series, TypeSpread: seriesTypeSpread(row.TypeCounts)}
		if work, ok := repWorks[row.Series.RepresentativeWorkID]; ok {
			view.PosterURL = workPosterURL(a.enrichment, work, 360)
		}
		data.Rows = append(data.Rows, view)
	}
	data.PageCount = (total + pageSize - 1) / pageSize
	if data.PageCount < 1 {
		data.PageCount = 1
	}
	makeURL := func(p int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p))
		return r.URL.Path + "?" + q.Encode()
	}
	if page > 1 {
		data.PrevURL = makeURL(page - 1)
	}
	if page < data.PageCount {
		data.NextURL = makeURL(page + 1)
	}
	start, end := page-2, page+2
	if start < 1 {
		start = 1
	}
	if end > data.PageCount {
		end = data.PageCount
	}
	for i := start; i <= end; i++ {
		data.Pages = append(data.Pages, pageLink{Number: i, URL: makeURL(i), Current: i == page})
	}
	a.render(w, http.StatusOK, "series.html", data)
}

type seriesDetailPageData struct {
	Chrome
	Notice string
	Series storage.WorkSeries
	Groups []seriesMemberGroup
}

func (a *App) handleAdminSeriesDetail(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	id := parseInt64(r.PathValue("id"))
	series, err := a.store.WorkSeriesByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.logger.Error("series detail", "error", err)
		http.Error(w, "series unavailable", http.StatusInternalServerError)
		return
	}
	members, err := a.store.SeriesMembers(r.Context(), id)
	if err != nil {
		a.logger.Error("series members", "error", err)
		http.Error(w, "series unavailable", http.StatusInternalServerError)
		return
	}
	data := seriesDetailPageData{
		Chrome: a.chromeFor(r.Context(), session, "series"),
		Notice: r.URL.Query().Get("notice"),
		Series: series,
		Groups: groupSeriesMembersByType(members),
	}
	a.render(w, http.StatusOK, "series-detail.html", data)
}

// handleAdminSeriesCreate 手动新建系列（D61）：名字可留空（跟随代表作），
// 至少选择一部作品。
func (a *App) handleAdminSeriesCreate(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	var workIDs []int64
	seen := map[int64]bool{}
	for _, value := range r.Form["workId"] {
		id := parseInt64(value)
		if id > 0 && !seen[id] {
			seen[id] = true
			workIDs = append(workIDs, id)
		}
	}
	if len(workIDs) == 0 {
		redirectWithNotice(w, r, "/admin/series", "新建系列至少需要一部作品")
		return
	}
	id, err := a.store.CreateWorkSeries(r.Context(), strings.TrimSpace(r.FormValue("title")), workIDs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			redirectWithNotice(w, r, "/admin/series", "所选作品不存在")
			return
		}
		a.logger.Error("create series", "error", err)
		redirectWithNotice(w, r, "/admin/series", "新建系列失败，请重试")
		return
	}
	redirectWithNotice(w, r, "/admin/series/"+strconv.FormatInt(id, 10), "系列已创建")
}

// handleAdminSeriesAddMember 把一部作品加入（或从别的系列移入）本系列。
func (a *App) handleAdminSeriesAddMember(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	seriesID := parseInt64(r.PathValue("id"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/series/"+strconv.FormatInt(seriesID, 10))
	workID := parseInt64(r.FormValue("workId"))
	if workID <= 0 {
		redirectWithNotice(w, r, returnTo, "请先搜索并选择一部作品")
		return
	}
	// 加入前先记下当前归属，用于“已从《Z》移入”的提示。
	previousTitle := ""
	if current, _, err := a.store.SeriesForWork(r.Context(), workID); err == nil {
		if current.ID == seriesID {
			redirectWithNotice(w, r, returnTo, "该作品已在本系列中")
			return
		}
		previousTitle = current.Title
	}
	if err := a.store.AddWorkToSeries(r.Context(), workID, seriesID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			redirectWithNotice(w, r, returnTo, "作品或系列不存在")
			return
		}
		a.logger.Error("add series member", "error", err)
		redirectWithNotice(w, r, returnTo, "加入系列失败，请重试")
		return
	}
	if previousTitle != "" {
		redirectWithNotice(w, r, returnTo, "已从《"+previousTitle+"》移入本系列")
		return
	}
	redirectWithNotice(w, r, returnTo, "已加入系列")
}

// handleAdminSeriesRemoveMember 把成员移出系列（写锁定，自动归组不再加回）。
func (a *App) handleAdminSeriesRemoveMember(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	seriesID := parseInt64(r.PathValue("id"))
	workID := parseInt64(r.PathValue("workId"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/series/"+strconv.FormatInt(seriesID, 10))
	// 只允许从本系列移出：DetachWorkFromSeries 会把作品从其当前所在系列
	// 拆出，先核对归属，避免借路由误拆别的系列。
	current, _, err := a.store.SeriesForWork(r.Context(), workID)
	if errors.Is(err, sql.ErrNoRows) {
		redirectWithNotice(w, r, returnTo, "该作品不在任何系列中")
		return
	}
	if err != nil {
		a.logger.Error("remove series member lookup", "error", err)
		redirectWithNotice(w, r, returnTo, "移出失败，请重试")
		return
	}
	if current.ID != seriesID {
		redirectWithNotice(w, r, returnTo, "该作品不在本系列中")
		return
	}
	if err = a.store.DetachWorkFromSeries(r.Context(), workID); err != nil {
		a.logger.Error("remove series member", "error", err)
		redirectWithNotice(w, r, returnTo, "移出失败，请重试")
		return
	}
	redirectWithNotice(w, r, returnTo, "已移出本系列，该作品不再参与自动归组")
}

// handleAdminSeriesMerge 把 dropId 指定的系列并入当前系列（D57 成员全部
// 变为手动加入），名字按 D58 的单选规则在服务端解析。
func (a *App) handleAdminSeriesMerge(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	keepID := parseInt64(r.PathValue("id"))
	dropID := parseInt64(r.FormValue("dropId"))
	if dropID <= 0 {
		redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/series/"+strconv.FormatInt(keepID, 10)), "请先搜索并选择要并入的系列")
		return
	}
	if dropID == keepID {
		redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/series/"+strconv.FormatInt(keepID, 10)), "不能把系列合并到自己")
		return
	}
	// returnTo 在合并完成后才决定（保留方可能被存储层交换），这里先只校验
	// 合法性，不做缺省填充。
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "")
	keep, keepErr := a.store.WorkSeriesByID(r.Context(), keepID)
	drop, dropErr := a.store.WorkSeriesByID(r.Context(), dropID)
	if keepErr != nil || dropErr != nil {
		redirectWithNotice(w, r, "/admin/series", "系列不存在")
		return
	}
	// 出错时的 returnTo 缺省回到当前系列详情页。
	if returnTo == "" {
		returnTo = "/admin/series/" + strconv.FormatInt(keepID, 10)
	}
	title := ""
	switch r.FormValue("titleChoice") {
	case "current":
		title = keep.Title
	case "drop":
		title = drop.Title
	case "new":
		title = strings.TrimSpace(r.FormValue("title"))
		if title == "" {
			redirectWithNotice(w, r, returnTo, "请填写合并后的新名字")
			return
		}
	}
	// titleChoice 为空（“自动（按规则）”）时 title 为空串，由存储层按 D58
	// 处理；双方都是自动名时存储层可能交换保留方（成员多的一方留下）。
	keptID, err := a.store.MergeWorkSeries(r.Context(), keepID, dropID, title)
	if err != nil {
		if errors.Is(err, storage.ErrSeriesTitleConflict) {
			redirectWithNotice(w, r, returnTo, "两个系列都改过名，请选择合并后的名字")
			return
		}
		a.logger.Error("merge series", "error", err)
		redirectWithNotice(w, r, returnTo, "合并系列失败，请重试")
		return
	}
	// 合并完成后跳到保留下来的系列：returnTo 指向被删除的一方（或缺省指向
	// 旧保留方）时都改指到 keptID。
	keptPath := "/admin/series/" + strconv.FormatInt(keptID, 10)
	if returnTo == "" || returnTo == "/admin/series/"+strconv.FormatInt(dropID, 10) || returnTo == "/admin/series/"+strconv.FormatInt(keepID, 10) {
		returnTo = keptPath
	}
	// L-new-1：提示方向按实际保留方——两边都是自动名时存储层可能保留了
	// 被并入方（keptID != keepID），此时本系列才是被吸收的一方。
	message := "系列已合并，《" + drop.Title + "》的成员已全部并入"
	if keptID != keepID {
		message = "本系列已并入《" + drop.Title + "》"
	}
	redirectWithNotice(w, r, returnTo, message)
}

// handleAdminSeriesOptions 是系列搜索自动补全接口（最多 10 条），做法与
// /admin/options/works 相同：参数化查询、防抖在客户端、textContent 渲染。
func (a *App) handleAdminSeriesOptions(w http.ResponseWriter, r *http.Request) {
	limit := optionsLimit(r)
	if limit > 10 {
		limit = 10
	}
	values, err := a.store.ListSeriesOptions(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		writeAPIError(w, 500, "query_failed", err.Error())
		return
	}
	items := make([]optionItem, 0, len(values))
	for _, series := range values {
		items = append(items, optionItem{ID: series.ID, Label: fmt.Sprintf("%s（共 %d 部）", series.Title, series.MemberCount)})
	}
	writeJSON(w, 200, items)
}
