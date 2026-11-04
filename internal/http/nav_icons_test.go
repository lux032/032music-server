package httpapi

import (
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var adminNavEntryRe = regexp.MustCompile(`(?s)<a href="[^"]*" data-nav="([a-z-]+)"[^>]*>.*?</a>`)

// adminNavIcons maps each entry of the sidebar's 管理 group to the sprite
// symbol it references, e.g. `... data-nav="series" ... <use href="...#icon-series">`.
func adminNavIcons(t *testing.T, body string) map[string]string {
	t.Helper()
	start := strings.Index(body, `id="sidebar-admin-items"`)
	if start < 0 {
		t.Fatal("admin nav group missing")
	}
	rest := body[start:]
	if end := strings.Index(rest, "</nav>"); end >= 0 {
		rest = rest[:end]
	}
	icons := make(map[string]string)
	for _, match := range adminNavEntryRe.FindAllStringSubmatch(rest, -1) {
		entry := match[0]
		iconAt := strings.Index(entry, "#icon-")
		if iconAt < 0 {
			t.Fatalf("nav entry %q has no sprite icon", match[1])
		}
		icon := entry[iconAt+1:]
		if space := strings.IndexAny(icon, `"'> `); space >= 0 {
			icon = icon[:space]
		}
		icons[match[1]] = icon
	}
	if len(icons) < 2 {
		t.Fatalf("admin nav group has only %d entries", len(icons))
	}
	return icons
}

// TestAdminNavIconsAreDistinct: 管理菜单里的每个入口都要有自己的图标。
// Issue #7：数据来源/数据库备份与恢复、作品关联审核/系列管理曾经重复，
// 只看文字不容易分辨。图标还必须真实存在于 icons.svg，否则 <use> 渲染为空。
func TestAdminNavIconsAreDistinct(t *testing.T) {
	_, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	sprite, err := fs.ReadFile(webFiles, "assets/icons.svg")
	if err != nil {
		t.Fatalf("read icon sprite: %v", err)
	}

	// 备份页在未配置备份目录时返回 503，但侧栏在每个页面都会渲染，
	// 因此这里只检查能稳定渲染的页面。
	for _, page := range []string{"/admin", "/admin/series", "/admin/settings/metadata", "/admin/work-review"} {
		rec := getWithCookie(handler, page, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", page, rec.Code)
		}
		icons := adminNavIcons(t, rec.Body.String())
		owners := make(map[string]string, len(icons))
		for nav, icon := range icons {
			if other, dup := owners[icon]; dup {
				t.Fatalf("%s: admin nav entries %s and %s share icon %s", page, other, nav, icon)
			}
			owners[icon] = nav
			if !strings.Contains(string(sprite), `id="`+icon+`"`) {
				t.Fatalf("%s: admin nav entry %s references missing sprite symbol %s", page, nav, icon)
			}
		}
	}
}
