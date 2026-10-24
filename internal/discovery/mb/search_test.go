package mb

import (
	"context"
	"github.com/lux032/032music-server/internal/sourcegate"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchReleaseGroupsLucene(t *testing.T) {
	title := `A / B + "C" \ D:(E) [F] {G} ^ ~ * ? ! & | -`
	want := `releasegroup:"A \/ B \+ \"C\" \\ D\:\(E\) \[F\] \{G\} \^ \~ \* \? \! \& \| \-"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != want || r.URL.Query().Get("limit") != "10" || r.URL.Query().Has("artist") {
			t.Error(r.URL.Query())
		}
		w.Write([]byte(`{"release-groups":[{"id":"11111111-1111-1111-1111-111111111111","title":"A","score":99}]}`))
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL, HTTP: server.Client(), Gate: sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0}})}
	got, e := c.SearchReleaseGroups(context.Background(), title)
	if e != nil || len(got) != 1 || got[0].Score != 99 {
		t.Fatal(got, e)
	}
}
