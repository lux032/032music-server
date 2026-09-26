package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexLinksAllRetainsFilters(t *testing.T) {
	request := httptest.NewRequest("GET", "/admin/albums?q=E2E&index=E&year=2020&page=3", nil)
	links := indexLinks(request, "/admin/albums")
	if links[0].Value != "全部" || links[0].Current || links[0].URL != "/admin/albums?q=E2E&year=2020" {
		t.Fatalf("all index: %+v", links[0])
	}
	if links[1].Value != "A" {
		t.Fatalf("first letter: %+v", links[1])
	}
	for _, link := range links {
		if link.Value == "E" && (!link.Current || !strings.Contains(link.URL, "q=E2E")) {
			t.Fatalf("selected index: %+v", link)
		}
	}
	request = httptest.NewRequest("GET", "/admin/albums", nil)
	if !indexLinks(request, "/admin/albums")[0].Current {
		t.Fatal("all should be selected without index")
	}
}

func TestFormatLibraryCount(t *testing.T) {
	for _, test := range []struct {
		n    int64
		want string
	}{{0, "0"}, {999, "999"}, {1260, "1,260"}, {1234567, "1,234,567"}} {
		if got := formatLibraryCount(test.n); got != test.want {
			t.Errorf("formatLibraryCount(%d) = %q, want %q", test.n, got, test.want)
		}
	}
}

func TestLibrarySortLabel(t *testing.T) {
	for _, tc := range []struct{ value, label string }{
		{"title", "标题"}, {"year", "发行年份"}, {"added", "加入时间"},
		{"name", "名称"}, {"albums", "专辑关联数量"}, {"tracks", "单曲关联数量"},
	} {
		if got := librarySortLabel(tc.value); got != tc.label {
			t.Errorf("sort %q: %q, want %q", tc.value, got, tc.label)
		}
		req := httptest.NewRequest("GET", "/admin/albums?sort="+tc.value, nil)
		tags := filterTags(req, "/admin/albums")
		if len(tags) != 1 || tags[0].Value != tc.label || tags[0].Label != "排序："+tc.label {
			t.Errorf("sort tag %q: %+v", tc.value, tags)
		}
	}
}

func TestFirstGenre(t *testing.T) {
	for _, input := range []string{"流行,摇滚", "流行;摇滚", "流行、摇滚"} {
		if got := firstGenre(input); got != "流行" {
			t.Errorf("firstGenre(%q) = %q", input, got)
		}
	}
}
