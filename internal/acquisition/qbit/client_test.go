package qbit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginContractAndAuthLatch(t *testing.T) {
	for _, tc := range []struct {
		body     string
		status   int
		wantAuth bool
	}{{"Ok.", 200, false}, {"Fails.", 200, true}, {"blocked", 403, true}, {"unknown", 200, false}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			var logins, versions int
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "http://"+r.Host+"/" {
					t.Error("invalid CSRF headers")
				}
				switch r.URL.Path {
				case "/api/v2/auth/login":
					logins++
					r.ParseForm()
					if r.Form.Get("username") != "user" || r.Form.Get("password") != "PASSWORD" {
						t.Error("credentials")
					}
					http.SetCookie(w, &http.Cookie{Name: "custom-session", Value: "cookie", Path: "/"})
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				case "/api/v2/app/version":
					versions++
					if _, e := r.Cookie("custom-session"); e != nil {
						t.Error("cookie missing")
					}
					fmt.Fprint(w, "v5.0")
				default:
					t.Error(r.URL.Path)
				}
			}))
			defer s.Close()
			c, _ := New(Config{s.URL, "user", "PASSWORD"})
			e := c.Login(context.Background())
			if tc.body == "Ok." {
				if e != nil || versions != 1 {
					t.Fatal(e, versions)
				}
			} else if e == nil {
				t.Fatal("login accepted")
			}
			if tc.wantAuth {
				if !errors.Is(e, ErrAuth) {
					t.Fatal(e)
				}
				c.Login(context.Background())
				if logins != 1 {
					t.Fatal("repeated failed login")
				}
			}
		})
	}
}
func TestLoginRequiresVersion200(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "login") {
			fmt.Fprint(w, "Ok.")
		} else {
			w.WriteHeader(403)
		}
	}))
	defer s.Close()
	c, _ := New(Config{s.URL, "u", "p"})
	if e := c.Login(context.Background()); !errors.Is(e, ErrAuth) {
		t.Fatal(e)
	}
}
func TestAddOutcomesAndReconcileExistingTags(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		want   AddOutcome
	}{{"Ok.", 200, AddOK}, {"Fails.", 200, AddRejected}, {"conflict", 409, AddRejected}, {`{"pending":true}`, 200, AddUnknown}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			var tags bool
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "http://"+r.Host+"/" {
					t.Error("headers")
				}
				switch r.URL.Path {
				case "/api/v2/auth/login":
					fmt.Fprint(w, "Ok.")
				case "/api/v2/app/version":
					fmt.Fprint(w, "v5")
				case "/api/v2/torrents/add":
					if e := r.ParseMultipartForm(1024 * 1024); e != nil {
						t.Error(e)
					}
					defer r.MultipartForm.RemoveAll()
					f, _, e := r.FormFile("torrents")
					if e != nil {
						t.Error(e)
					} else {
						f.Close()
					}
					if r.FormValue("tags") != "032acq-public-uuid" || r.FormValue("category") != "existing category" {
						t.Error(r.Form)
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				case "/api/v2/torrents/info":
					if r.URL.Query().Get("hashes") != "hash" || r.URL.Query().Get("tag") != "" {
						t.Error(r.URL)
					}
					fmt.Fprint(w, `[{"hash":"hash","progress":1,"state":"stalledUP","tags":"other","size":123}]`)
				case "/api/v2/torrents/addTags":
					r.ParseForm()
					tags = r.Form.Get("hashes") == "hash" && r.Form.Get("tags") == "032acq-public-uuid"
				case "/api/v2/torrents/categories":
					fmt.Fprint(w, `{"existing category":{"name":"existing category","savePath":"/music"}}`)
				default:
					t.Error(r.URL.Path)
				}
			}))
			defer s.Close()
			c, _ := New(Config{s.URL, "u", "p"})
			out, e := c.AddTorrent(context.Background(), []byte("torrent"), "public-uuid", "existing category")
			if e != nil || out != tc.want {
				t.Fatal(out, e)
			}
			info, e := c.Info(context.Background(), "hash", "")
			if e != nil || len(info) != 1 || !Complete(info[0]) {
				t.Fatal(info, e)
			}
			if e = c.AddTags(context.Background(), info[0].Hash, Tag("public-uuid")); e != nil || !tags {
				t.Fatal(e, tags)
			}
			cats, e := c.Categories(context.Background())
			if e != nil || cats["existing category"].SavePath != "/music" {
				t.Fatal(cats, e)
			}
		})
	}
}
func TestAPIRelogsOnlyOnce(t *testing.T) {
	var logins atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			logins.Add(1)
			fmt.Fprint(w, "Ok.")
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5")
		default:
			w.WriteHeader(403)
		}
	}))
	defer s.Close()
	c, _ := New(Config{s.URL, "u", "p"})
	if _, e := c.Info(context.Background(), "hash", ""); !errors.Is(e, ErrAuth) {
		t.Fatal(e)
	}
	if logins.Load() != 2 {
		t.Fatal(logins.Load())
	}
	c.Info(context.Background(), "hash", "")
	if logins.Load() != 2 {
		t.Fatal("failed auth loop")
	}
}
func TestAddTimeoutUnknownAndRedacted(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			fmt.Fprint(w, "Ok.")
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer s.Close()
	c, _ := New(Config{s.URL, "user", "PASSWORD"})
	if e := c.Login(context.Background()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out, e := c.AddTorrent(ctx, []byte("torrent"), "uuid", "")
	if out != AddUnknown || e == nil || strings.Contains(e.Error(), "PASSWORD") || strings.Contains(e.Error(), s.URL) {
		t.Fatal(out, e)
	}
}
func TestComplete(t *testing.T) {
	for _, tc := range []struct {
		t    Torrent
		want bool
	}{{Torrent{Progress: 1, State: "uploading"}, true}, {Torrent{Size: 1, State: "stoppedUP"}, true}, {Torrent{Progress: 1, State: "checkingUP"}, false}, {Torrent{Progress: 1, State: "checkingDL"}, false}, {Torrent{}, false}, {Torrent{Size: 1, AmountLeft: 1}, false}} {
		if Complete(tc.t) != tc.want {
			t.Fatal(tc)
		}
	}
}

func TestTransportErrorsHideCredentials(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := s.URL
	s.Close()
	c, _ := New(Config{base, "USERNAME", "PASSWORD"})
	err := c.Login(context.Background())
	if err == nil {
		t.Fatal("expected connection error")
	}
	for _, secret := range []string{base, "USERNAME", "PASSWORD"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("leaked %s: %s", secret, err)
		}
	}
}

func TestAPIRelogSuccessDoesNotLatch(t *testing.T) {
	var logins, infos int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			logins++
			fmt.Fprint(w, "Ok.")
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5")
		case "/api/v2/torrents/info":
			infos++
			if infos == 1 {
				w.WriteHeader(403)
			} else {
				fmt.Fprint(w, "[]")
			}
		}
	}))
	defer s.Close()
	c, _ := New(Config{s.URL, "u", "p"})
	for i := 0; i < 2; i++ {
		if _, err := c.Info(context.Background(), "hash", ""); err != nil {
			t.Fatal(err)
		}
	}
	if logins != 2 || c.authFailed || infos != 3 {
		t.Fatal(logins, infos, c.authFailed)
	}
}

func TestConfigFormatting(t *testing.T) {
	c := Config{BaseURL: "http://host", Username: "user", Password: "PASSWORD"}
	b, _ := json.Marshal(c)
	output := string(b) + fmt.Sprintf("%v %#v", c, c)
	for _, secret := range []string{"PASSWORD"} {
		if strings.Contains(output, secret) {
			t.Fatal(output)
		}
	}
}

func TestProbeCachedSessionReauthenticatesOnce(t *testing.T) {
	logins, versions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "auth/login") {
			logins++
			fmt.Fprint(w, "Ok.")
			return
		}
		versions++
		if versions == 3 {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, "v5")
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = client.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logins != 2 || versions != 5 {
		t.Fatal(logins, versions)
	}
}
