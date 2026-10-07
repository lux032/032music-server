package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

const missingFilesPath = "/admin/library/missing"

// purgeMissingOption 是管理页清理策略下拉框的一项。
type purgeMissingOption struct {
	Value    string
	Label    string
	Selected bool
}

// purgeMissingView 是控制台“缺失文件”区块的数据。
type purgeMissingView struct {
	Policy      string
	PolicyLabel string
	Overridden  bool   // 当前值来自管理页设置，而非环境变量
	EnvSummary  string // 环境变量默认值的描述
	Options     []purgeMissingOption
}

type missingFilesPageData struct {
	Chrome
	Notice           string
	Files            []storage.MissingFile
	Total            int64
	Page, PageCount  int
	PrevURL, NextURL string
	Pages            []pageLink
	Purge            purgeMissingView
}

var purgeMissingPolicies = []string{config.PurgeMissingNever, config.PurgeMissingFull, config.PurgeMissingAlways}

func purgeMissingLabel(policy string) string {
	switch policy {
	case config.PurgeMissingAlways:
		return "每次扫描后自动删除"
	case config.PurgeMissingFull:
		return "仅完整重扫后自动删除"
	default:
		return "从不自动删除（仅隐藏）"
	}
}

// effectivePurgeMissing 返回生效的清理策略：管理页覆盖优先，否则取环境变量。
func (a *App) effectivePurgeMissing(ctx context.Context) (string, bool, error) {
	saved, found, err := a.store.PurgeMissingSetting(ctx)
	if err != nil {
		return config.PurgeMissingNever, false, err
	}
	if found && config.ValidPurgeMissing(saved) {
		return saved, true, nil
	}
	if config.ValidPurgeMissing(a.config.PurgeMissing) {
		return a.config.PurgeMissing, false, nil
	}
	return config.PurgeMissingNever, false, nil
}

// PurgeMissingPolicy 是扫描器读取清理策略的入口；读取失败时按 never 处理，
// 宁可不删也不误删。
func (a *App) PurgeMissingPolicy(ctx context.Context) string {
	policy, _, err := a.effectivePurgeMissing(ctx)
	if err != nil {
		a.logger.Error("load purge missing setting", "error", err)
		return config.PurgeMissingNever
	}
	return policy
}

func (a *App) purgeMissingView(ctx context.Context) purgeMissingView {
	policy, overridden, err := a.effectivePurgeMissing(ctx)
	if err != nil {
		a.logger.Error("load purge missing setting", "error", err)
	}
	envPolicy := a.config.PurgeMissing
	if !config.ValidPurgeMissing(envPolicy) {
		envPolicy = config.PurgeMissingNever
	}
	view := purgeMissingView{Policy: policy, PolicyLabel: purgeMissingLabel(policy), Overridden: overridden, EnvSummary: purgeMissingLabel(envPolicy)}
	for _, value := range purgeMissingPolicies {
		view.Options = append(view.Options, purgeMissingOption{Value: value, Label: purgeMissingLabel(value), Selected: value == policy})
	}
	return view
}

func (a *App) handleMissingFilesPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	page := int(parseInt64(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	files, total, err := a.store.ListMissingFiles(r.Context(), adminFeaturePageSize, (page-1)*adminFeaturePageSize)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data := missingFilesPageData{
		Chrome: a.chromeFor(r.Context(), session, "console"), Notice: r.URL.Query().Get("notice"),
		Files: files, Total: total, Page: page, Purge: a.purgeMissingView(r.Context()),
	}
	data.PageCount = int((total + adminFeaturePageSize - 1) / adminFeaturePageSize)
	if page > 1 {
		data.PrevURL = missingFilesPath + "?page=" + strconv.Itoa(page-1)
	}
	if page < data.PageCount {
		data.NextURL = missingFilesPath + "?page=" + strconv.Itoa(page+1)
	}
	for number := 1; number <= data.PageCount; number++ {
		if number == 1 || number == data.PageCount || (number >= page-2 && number <= page+2) {
			data.Pages = append(data.Pages, pageLink{Number: number, URL: missingFilesPath + "?page=" + strconv.Itoa(number), Current: number == page})
		}
	}
	a.render(w, http.StatusOK, "missing-files.html", data)
}

// handlePurgeMissingFiles 永久删除选中（或全部）缺失文件及因此再无文件的歌曲。
// confirm=all 删除全部；confirm=selected 只删除表单里勾选的 id。
func (a *App) handlePurgeMissingFiles(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectWithNotice(w, r, missingFilesPath, "请求无效")
		return
	}
	var ids []int64
	switch r.FormValue("confirm") {
	case "all":
	case "selected":
		for _, raw := range r.Form["id"] {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			redirectWithNotice(w, r, missingFilesPath, "请先勾选要删除的文件")
			return
		}
	default:
		redirectWithNotice(w, r, missingFilesPath, "删除确认无效")
		return
	}
	result, err := a.store.PurgeMissing(r.Context(), 0, ids)
	if err != nil {
		a.logger.Error("purge missing files", "error", err)
		http.Error(w, "purge failed", http.StatusInternalServerError)
		return
	}
	// 删除专辑会级联清除自定义封面记录，顺带回收不再被引用的图片文件。
	a.gcCustomImages(r.Context())
	if result.Files == 0 {
		redirectWithNotice(w, r, missingFilesPath, "没有可删除的缺失文件（可能已重新出现或已被清理）")
		return
	}
	redirectWithNotice(w, r, missingFilesPath, fmt.Sprintf("已删除 %d 个缺失文件、%d 首歌曲、%d 张专辑、%d 位歌手", result.Files, result.Tracks, result.Albums, result.Artists))
}

func (a *App) handleSavePurgeMissing(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	policy := r.FormValue("policy")
	if !config.ValidPurgeMissing(policy) {
		redirectWithNotice(w, r, missingFilesPath, "清理策略无效")
		return
	}
	if err := a.store.SavePurgeMissingSetting(r.Context(), policy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, missingFilesPath, "缺失文件清理策略已设为："+purgeMissingLabel(policy))
}

func (a *App) handleResetPurgeMissing(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.ResetPurgeMissingSetting(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, missingFilesPath, "缺失文件清理策略已恢复环境变量默认值")
}
