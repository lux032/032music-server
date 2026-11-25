package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

const albumRulesPath = "/admin/library/rules"

type albumRulesSummary struct {
	DeleteCount int64
	MergeCount  int64
}

func (s albumRulesSummary) Total() int64 {
	return s.DeleteCount + s.MergeCount
}

type albumRuleRow struct {
	Value            string
	Action           string
	SourceTitle      string
	GroupingKey      string
	TargetAlbumID    int64
	TargetAlbumTitle string
	CreatedAt        string
}

type albumRulesPageData struct {
	Chrome
	Notice      string
	DeleteRules []albumRuleRow
	MergeRules  []albumRuleRow
	Total       int
}

func (a *App) albumRulesSummary(ctx context.Context) albumRulesSummary {
	delCount, mergeCount, err := a.store.CountAlbumKeyRules(ctx)
	if err != nil {
		a.logger.Warn("count album key rules", "error", err)
		return albumRulesSummary{}
	}
	return albumRulesSummary{DeleteCount: delCount, MergeCount: mergeCount}
}

func (a *App) handleAlbumRulesPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	rules, err := a.store.ListAlbumKeyRules(r.Context())
	if err != nil {
		a.renderAdminFeatureError(w, "rules", err)
		return
	}
	data := albumRulesPageData{
		Chrome: a.chromeFor(r.Context(), session, "rules"),
		Notice: r.URL.Query().Get("notice"),
		Total:  len(rules),
	}
	for _, rule := range rules {
		row := albumRuleRow{
			Value:            strconv.FormatInt(rule.LibraryID, 10) + ":" + rule.GroupingKey,
			Action:           rule.Action,
			SourceTitle:      rule.SourceTitle,
			GroupingKey:      rule.GroupingKey,
			TargetAlbumID:    rule.TargetAlbumID,
			TargetAlbumTitle: rule.TargetAlbumTitle,
			CreatedAt:        rule.CreatedAt,
		}
		if rule.Action == "delete" {
			data.DeleteRules = append(data.DeleteRules, row)
		} else {
			data.MergeRules = append(data.MergeRules, row)
		}
	}
	a.render(w, http.StatusOK, "album-rules.html", data)
}

func parseAlbumRuleKeys(values []string) []storage.AlbumKeyRuleKey {
	keys := make([]storage.AlbumKeyRuleKey, 0, len(values))
	for _, raw := range values {
		libText, groupingKey, ok := strings.Cut(raw, ":")
		if !ok || groupingKey == "" {
			continue
		}
		libID, err := strconv.ParseInt(libText, 10, 64)
		if err != nil || libID <= 0 {
			continue
		}
		keys = append(keys, storage.AlbumKeyRuleKey{LibraryID: libID, GroupingKey: groupingKey})
	}
	return keys
}

func (a *App) handleDeleteAlbumRules(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectWithNotice(w, r, albumRulesPath, "请求无效")
		return
	}
	keys := parseAlbumRuleKeys(r.Form["rule"])
	if len(keys) == 0 {
		redirectWithNotice(w, r, albumRulesPath, "请先选择要解除的规则")
		return
	}
	deleted, err := a.store.DeleteAlbumKeyRules(r.Context(), keys)
	if err != nil {
		a.logger.Error("delete album key rules", "error", err)
		http.Error(w, "delete rules failed", http.StatusInternalServerError)
		return
	}
	if deleted == 0 {
		redirectWithNotice(w, r, albumRulesPath, "没有可解除的规则（可能已被清理）")
		return
	}
	if r.FormValue("rescan") == "1" && a.scanner != nil {
		if _, scanErr := a.scanner.Start(r.Context(), "incremental"); scanErr == nil {
			redirectWithNotice(w, r, albumRulesPath, fmt.Sprintf("已解除 %d 条规则，并已启动增量扫描", deleted))
			return
		} else if errors.Is(scanErr, scanner.ErrScanRunning) {
			redirectWithNotice(w, r, albumRulesPath, fmt.Sprintf("已解除 %d 条规则（当前已有扫描在运行，请稍后重扫）", deleted))
			return
		} else {
			a.logger.Warn("start incremental scan after deleting album rules", "error", scanErr)
			redirectWithNotice(w, r, albumRulesPath, fmt.Sprintf("已解除 %d 条规则，但启动增量扫描失败", deleted))
			return
		}
	}
	redirectWithNotice(w, r, albumRulesPath, fmt.Sprintf("已解除 %d 条规则，下次扫描将按磁盘文件正常入库/归组", deleted))
}
