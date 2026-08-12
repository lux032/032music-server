package httpapi

import (
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestAdminReturnPathAcceptsOnlyLocalAdminPaths(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected string
	}{
		{name: "admin path", value: "/admin/albums?page=2", expected: "/admin/albums?page=2"},
		{name: "old notice removed", value: "/admin/albums?page=2&notice=old", expected: "/admin/albums?page=2"},
		{name: "absolute URL", value: "https://example.com/admin", expected: "/admin/favorites"},
		{name: "protocol relative URL", value: "//example.com/admin", expected: "/admin/favorites"},
		{name: "non admin path", value: "/api/v1/albums", expected: "/admin/favorites"},
		{name: "admin prefix confusion", value: "/administrator", expected: "/admin/favorites"},
		{name: "empty", value: "", expected: "/admin/favorites"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/admin/favorites/albums/1", nil)
			request.Form = url.Values{"returnTo": {test.value}}
			if actual := adminReturnPath(request, "/admin/favorites"); actual != test.expected {
				t.Fatalf("adminReturnPath() = %q, want %q", actual, test.expected)
			}
		})
	}
}

func TestPlaylistTrackIDsPreservesOrder(t *testing.T) {
	tracks := []storage.Track{{ID: 9}, {ID: 3}, {ID: 12}}
	want := []int64{9, 3, 12}
	if got := playlistTrackIDs(tracks); !reflect.DeepEqual(got, want) {
		t.Fatalf("playlistTrackIDs() = %v, want %v", got, want)
	}
}

func TestIndexOfTrackID(t *testing.T) {
	values := []int64{9, 3, 12}
	if got := indexOfTrackID(values, 3); got != 1 {
		t.Fatalf("indexOfTrackID() = %d, want 1", got)
	}
	if got := indexOfTrackID(values, 99); got != -1 {
		t.Fatalf("indexOfTrackID() = %d, want -1", got)
	}
}
