package acquisition

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/sourcegate"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/acquisition/jackett"
	"github.com/lux032/032music-server/internal/acquisition/qbit"
	"github.com/lux032/032music-server/internal/acquisition/torrent"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestMain(m *testing.M) {
	cleanup := storage.EnableMigrationTemplate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

const testRG = "12345678-1234-1234-1234-123456789abc"
const otherRG = "87654321-1234-1234-1234-123456789abc"

var torrentBytes = []byte("d4:infod4:name1:x6:pieces20:aaaaaaaaaaaaaaaaaaaaee")

type fake struct {
	mu                                           sync.Mutex
	adds, logins, tags, downloads                int
	visible, complete, authFail, unknown, reject bool
	onAdd                                        func()
	onInfo                                       func()
	downloadCode                                 int
	downloadAPI                                  int
	delayed                                      bool
}

func (f *fake) handler(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "torrents/info") && f.onInfo != nil {
		f.onInfo()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(r.URL.Path, "torznab"):
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<rss><channel><item><title>album FLAC 24bit</title><link>/dl/jpopsuki/?path=private-reference&amp;jackett_apikey=SECRET</link><size>10</size><attr name="seeders" value="5"/></item><item><title>magnet</title><link>magnet:?xt=urn:btih:abc</link></item></channel></rss>`)
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		f.downloads++
		if f.downloadCode != 0 {
			w.WriteHeader(f.downloadCode)
			return
		}
		if f.downloadAPI != 0 {
			fmt.Fprintf(w, `<error code="%d"/>`, f.downloadAPI)
			return
		}
		w.Write(torrentBytes)
	case strings.HasSuffix(r.URL.Path, "auth/login"):
		f.logins++
		if f.authFail {
			io.WriteString(w, "Fails.")
		} else {
			io.WriteString(w, "Ok.")
		}
	case strings.HasSuffix(r.URL.Path, "app/version"):
		io.WriteString(w, "v5.0")
	case strings.HasSuffix(r.URL.Path, "torrents/info"):
		if !f.visible {
			io.WriteString(w, "[]")
			return
		}
		p := 0.5
		if f.complete {
			p = 1
		}
		json.NewEncoder(w).Encode([]qbit.Torrent{{Hash: r.URL.Query().Get("hashes"), Progress: p, Size: 100, AmountLeft: 50, State: "downloading"}})
	case strings.HasSuffix(r.URL.Path, "torrents/add"):
		f.adds++
		if f.onAdd != nil {
			f.onAdd()
		}
		if f.unknown {
			panic(http.ErrAbortHandler)
		}
		if !f.reject {
			if !f.delayed {
				f.visible = true
			}
			io.WriteString(w, "Ok.")
		} else {
			io.WriteString(w, "Fails.")
		}
	case strings.HasSuffix(r.URL.Path, "torrents/addTags"):
		f.tags++
	default:
		http.NotFound(w, r)
	}
}

type fixture struct {
	s       *Service
	store   *storage.Store
	db      *sql.DB
	f       *fake
	ctx     context.Context
	now     *time.Time
	library int64
	server  *httptest.Server
	logs    *bytes.Buffer
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	st, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	if e = st.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	must := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	must(`UPDATE release_group_rebuild SET status='ready'`)
	for _, rg := range []string{testRG, otherRG} {
		must(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,artist_credit,fetched_at) VALUES(?,'album','Album','Album','artist','now')`, rg)
	}
	if e = st.EnsureLibrary(ctx, "test", t.TempDir()); e != nil {
		t.Fatal(e)
	}
	var lib int64
	if e = db.QueryRow(`SELECT id FROM libraries LIMIT 1`).Scan(&lib); e != nil {
		t.Fatal(e)
	}
	f := &fake{}
	server := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(server.Close)
	if e = st.UpdateAcquisitionSettings(ctx, storage.AcquisitionSettingsUpdate{Enabled: true, JackettURL: server.URL, JackettAPIKey: "SECRET", QBURL: server.URL, QBUsername: "u", QBPassword: "PASSWORD", QBCategory: "music"}); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	logs := &bytes.Buffer{}
	s := New(st, slog.New(slog.NewTextHandler(logs, nil)), Options{Now: func() time.Time { return now }})
	return fixture{s, st, db, f, ctx, &now, lib, server, logs}
}
func (f fixture) confirm(t *testing.T, rg, key string) storage.AcquisitionTask {
	t.Helper()
	v, e := f.s.Search(f.ctx, rg, "artist album", false)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(v)
	for _, secret := range []string{"SECRET", "PASSWORD", "private-reference", "DownloadRef", "http://"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("search leak: %s", b)
		}
	}
	v2, e := f.s.Search(f.ctx, rg, " ARTIST   ALBUM ", false)
	if e != nil || v2.SearchID != v.SearchID {
		t.Fatal("cache", e)
	}
	task, e := f.s.Confirm(f.ctx, rg, v.SearchID, 0, key)
	if e != nil {
		t.Fatal(e)
	}
	return task
}
func (f fixture) task(t *testing.T, v storage.AcquisitionTask) storage.AcquisitionTask {
	t.Helper()
	v, e := f.store.AcquisitionTask(f.ctx, v.ID)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (f fixture) importFile(t *testing.T, path string) {
	t.Helper()
	if e := f.store.ImportTrack(f.ctx, storage.ImportInput{LibraryID: f.library, RelativePath: path, Metadata: metadata.AudioMetadata{Title: "song", DiscNumber: 1, TrackNumber: 1, Album: "album", Artists: []string{"artist"}, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {testRG}}}}); e != nil {
		t.Fatal(e)
	}
}
func TestServiceConfirmIdempotentHashBoundAndReconcile(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "one")
	old, e := f.s.Confirm(f.ctx, testRG, "expired", 99, "one")
	if e != nil || old.ID != v.ID {
		t.Fatal(old, e)
	}
	if _, e = f.s.Confirm(f.ctx, testRG, "expired", 0, "two"); !errors.Is(e, storage.ErrAcquisitionRGActive) {
		t.Fatal(e)
	}
	search, e := f.s.Search(f.ctx, otherRG, "album", true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Confirm(f.ctx, otherRG, search.SearchID, 0, "hash"); !errors.Is(e, storage.ErrAcquisitionHashBound) {
		t.Fatal(e)
	}
	f.s.reconcile(f.ctx, false, true)
	v = f.task(t, v)
	if v.State != "DOWNLOADING" || v.DownloadRef != "" || f.f.adds != 1 || f.f.tags != 1 {
		t.Fatal(v.State, f.f)
	}
	f.f.complete = true
	*f.now = f.now.Add(time.Minute)
	f.s.reconcile(f.ctx, false, true)
	if got := f.task(t, v); got.State != "AWAITING_LIBRARY" || got.DownloadCompletedAt == "" {
		t.Fatal(got.State)
	}
}
func TestServiceUnknownOnlyReconcilesThenReaddsOnce(t *testing.T) {
	f := setup(t)
	f.f.unknown = true
	v := f.confirm(t, testRG, "unknown")
	f.s.reconcile(f.ctx, false, false)
	if f.f.adds != 1 {
		t.Fatal(f.f.adds)
	}
	if _, err := f.db.Exec(`UPDATE acquisition_tasks SET add_unknown_since=? WHERE id=?`, stamp(*f.now), v.ID); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(30 * time.Second)
	f.s.reconcile(f.ctx, false, false)
	if f.f.adds != 1 {
		t.Fatal("blind readd")
	}
	if _, err := f.db.Exec(`UPDATE acquisition_tasks SET add_unknown_since=? WHERE id=?`, stamp(*f.now), v.ID); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(3 * time.Minute)
	f.s.reconcile(f.ctx, false, false)
	v = f.task(t, v)
	if f.f.adds != 2 || v.State != "QB_SUBMITTING" {
		t.Fatal(f.f.adds, v.State, v.AttentionReason)
	}
	if _, err := f.db.Exec(`UPDATE acquisition_tasks SET add_unknown_since=? WHERE id=?`, stamp(*f.now), v.ID); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(3 * time.Minute)
	f.s.reconcile(f.ctx, true, false)
	if got := f.task(t, v); got.State != "ATTENTION" {
		t.Fatal(got.State)
	}
	if f.f.adds != 2 {
		t.Fatal("third add")
	}
}
func TestServiceRestartReconcilesHashBeforeRedownload(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(map[bool]string{false: "redownload", true: "already_present"}[visible], func(t *testing.T) {
			f := setup(t)
			v := f.confirm(t, testRG, "restart")
			f.f.visible = visible
			s := New(f.store, nil, Options{Now: func() time.Time { return *f.now }})
			s.reconcile(f.ctx, true, true)
			v = f.task(t, v)
			if v.State != "DOWNLOADING" {
				t.Fatal(v.State)
			}
			if visible && (f.f.adds != 0 || f.f.downloads != 1) {
				t.Fatal("did not reconcile first")
			}
			if !visible && (f.f.adds != 1 || f.f.downloads != 2) {
				t.Fatal("did not recover bytes")
			}
		})
	}
}
func TestServiceCancelDiscardsLateAddWrite(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "cancel")
	f.f.onAdd = func() {
		if _, e := f.s.Cancel(f.ctx, v.PublicID); e != nil {
			t.Error(e)
		}
	}
	f.s.reconcile(f.ctx, false, false)
	if got := f.task(t, v); got.State != "CANCELLED" {
		t.Fatal(got.State)
	}
}
func TestServiceAuthFailureLatchesUntilSettingsChanged(t *testing.T) {
	f := setup(t)
	f.f.authFail = true
	search, e := f.s.Search(f.ctx, testRG, "album", true)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = f.s.Confirm(f.ctx, testRG, search.SearchID, 0, "auth"); !errors.Is(e, qbit.ErrAuth) {
			t.Fatal(e)
		}
	}
	if f.f.logins != 1 {
		t.Fatal(f.f.logins)
	}
	tasks, _ := f.store.ListAcquisitionTasks(f.ctx)
	if len(tasks) != 0 {
		t.Fatal("created before login")
	}
	f.f.authFail = false
	f.s.SettingsChanged()
	if _, e = f.s.Confirm(f.ctx, testRG, search.SearchID, 0, "auth"); e != nil {
		t.Fatal(e)
	}
}
func TestServiceEvidenceDirectFromDownloadingAndMissing(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "evidence")
	f.s.reconcile(f.ctx, false, true)
	f.f.visible = false
	*f.now = f.now.Add(time.Minute)
	f.s.reconcile(f.ctx, false, true)
	v = f.task(t, v)
	if v.State != "DOWNLOADING" || v.QBMissingSince == "" {
		t.Fatal(v.State)
	}
	if _, e := f.db.Exec(`UPDATE acquisition_tasks SET created_at='2000-01-01T00:00:00.000Z' WHERE id=?`, v.ID); e != nil {
		t.Fatal(e)
	}
	f.importFile(t, "new/song.flac")
	f.s.reconcile(f.ctx, false, true)
	v = f.task(t, v)
	if v.State != "IN_LIBRARY" {
		t.Fatal(v.State)
	}
	ev, e := f.store.AcquisitionEvidence(f.ctx, v.ID)
	if e != nil || ev.DetectedCount != 1 || ev.Source != "file" {
		t.Fatal(ev, e)
	}
}
func TestServiceMissingTimeoutIdentityDelayAndOldFiles(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "old")
	f.s.reconcile(f.ctx, false, false)
	// File predates the task; same-path overwrites retain its original clock.
	f.importFile(t, "old/song.flac")
	if _, e := f.db.Exec(`UPDATE audio_files SET created_at='1999-01-01T00:00:00.000Z'`); e != nil {
		t.Fatal(e)
	}
	f.importFile(t, "old/song.flac")
	f.f.visible = false
	*f.now = f.now.Add(time.Minute)
	f.s.reconcile(f.ctx, false, true)
	*f.now = f.now.Add(73 * time.Hour)
	if _, e := f.db.Exec(`UPDATE release_group_rebuild SET status='not_ready'`); e != nil {
		t.Fatal(e)
	}
	f.s.reconcile(f.ctx, false, true)
	if got := f.task(t, v); got.State != "DOWNLOADING" {
		t.Fatal(got.State)
	}
	if _, e := f.db.Exec(`UPDATE release_group_rebuild SET status='ready'`); e != nil {
		t.Fatal(e)
	}
	f.s.reconcile(f.ctx, false, true)
	v = f.task(t, v)
	if v.State != "ATTENTION" || v.AttentionReason != "qb_missing" {
		t.Fatal(v.State, v.AttentionReason)
	}
}
func TestServiceLifecycleWakeAndWait(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "lifecycle")
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	ticks := make(chan time.Time)
	ready := make(chan struct{}, 10)
	f.s.after = func(time.Duration) <-chan time.Time { ready <- struct{}{}; return ticks }
	// Wake requests before launch deterministically coalesce into one slot.
	for i := 0; i < 100; i++ {
		f.s.Wake()
	}
	if len(f.s.wake) != 1 {
		t.Fatal("Wake not merged")
	}
	if err := f.s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Start(ctx); !errors.Is(err, ErrRunning) {
		t.Fatal(err)
	}
	<-ready // startup done; drain the one queued wake as well
	<-ready
	f.f.mu.Lock()
	adds := f.f.adds
	f.f.mu.Unlock()
	if adds != 1 {
		t.Fatal(adds)
	}
	if _, err := f.db.Exec(`UPDATE acquisition_tasks SET created_at='2000-01-01T00:00:00.000Z' WHERE id=?`, v.ID); err != nil {
		t.Fatal(err)
	}
	f.importFile(t, "tick/song.flac")
	*f.now = f.now.Add(30 * time.Second)
	ticks <- *f.now
	<-ready
	if got := f.task(t, v); got.State != "DOWNLOADING" {
		t.Fatal("premature evidence tick", got.State)
	}
	*f.now = f.now.Add(5 * time.Minute)
	ticks <- *f.now
	<-ready
	if got := f.task(t, v); got.State != "IN_LIBRARY" {
		t.Fatal("missing evidence tick", got.State)
	}
	cancel()
	f.s.Wait()
}
func TestServicePreexistingOwnershipRejected(t *testing.T) {
	f := setup(t)
	f.importFile(t, "owned/song.flac")
	search, e := f.s.Search(f.ctx, testRG, "album", true)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.Confirm(f.ctx, testRG, search.SearchID, 0, "owned")
	var app *Error
	if !errors.As(e, &app) || app.Code != "already_owned" {
		t.Fatal(e)
	}
	if f.f.logins != 0 {
		t.Fatal("logged in before ownership check")
	}
}
func TestServiceManualLinkReplaceAndOwned(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "owned"}[owned], func(t *testing.T) {
			f := setup(t)
			v := f.confirm(t, testRG, "link")
			f.importFile(t, "local/song.flac")
			var album int64
			if e := f.db.QueryRow(`SELECT id FROM albums LIMIT 1`).Scan(&album); e != nil {
				t.Fatal(e)
			}
			if e := f.store.AssociateAlbumReleaseGroup(f.ctx, album, otherRG); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.Link(f.ctx, v.PublicID, album, false); e == nil {
				t.Fatal("missing replaces_rg")
			}
			var e error
			if owned {
				v, e = f.s.MarkOwned(f.ctx, v.PublicID, album, true)
			} else {
				v, e = f.s.Link(f.ctx, v.PublicID, album, true)
			}
			if e != nil || v.State != "IN_LIBRARY" {
				t.Fatal(v.State, e)
			}
		})
	}
}
func TestServiceHashFixture(t *testing.T) {
	if _, e := torrent.Parse(torrentBytes); e != nil {
		t.Fatal(e)
	}
}

func TestServiceIndependentUnreachableAndUnknownClocks(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "clocks")
	// A long-standing missing torrent must not count as 30m of network outage.
	v.QBMissingSince = "2000-01-01T00:00:00.000Z"
	v, e := f.store.CASAcquisitionTask(f.ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	f.s.qbError(f.ctx, v, errors.New("network unavailable"))
	v = f.task(t, v)
	if v.State != "QB_SUBMITTING" || v.QBUnreachableSince == "" || v.AddUnknownSince != "" {
		t.Fatal(v.State)
	}
	if _, err := f.db.Exec(`UPDATE acquisition_tasks SET qb_unreachable_since=? WHERE id=?`, stamp(*f.now), v.ID); err != nil {
		t.Fatal(err)
	}
	v = f.task(t, v)
	*f.now = f.now.Add(31 * time.Minute)
	f.s.qbError(f.ctx, v, errors.New("network unavailable"))
	v = f.task(t, v)
	if v.State != "ATTENTION" || v.AttentionReason != "qb_unreachable" {
		t.Fatal(v.State, v.AttentionReason)
	}
	v, e = f.s.Retry(f.ctx, v.PublicID)
	if e != nil {
		t.Fatal(e)
	}
	if v.QBUnreachableSince != "" || v.AddUnknownSince != "" || v.QBMissingSince != "" {
		t.Fatal("retry retained clocks")
	}
	f.f.visible = true
	f.s.reconcile(f.ctx, true, false)
	v = f.task(t, v)
	if v.State != "DOWNLOADING" || v.AddUnknownSince != "" || v.QBUnreachableSince != "" || v.QBMissingSince != "" {
		t.Fatal("reconciliation did not clear clocks")
	}
}
func TestServiceCancelledLinkDoesNotChangeAlbum(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "cancel-link")
	f.importFile(t, "local/song.flac")
	var album int64
	_ = f.db.QueryRow(`SELECT id FROM albums LIMIT 1`).Scan(&album)
	if e := f.store.AssociateAlbumReleaseGroup(f.ctx, album, otherRG); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Cancel(f.ctx, v.PublicID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Link(f.ctx, v.PublicID, album, true); !errors.Is(e, storage.ErrAcquisitionStale) {
		t.Fatal(e)
	}
	rg, e := f.store.AlbumReleaseGroup(f.ctx, album)
	if e != nil || rg != otherRG {
		t.Fatal(rg, e)
	}
}
func TestServiceBackupRestoreReconcilesRetainedTask(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "backup")
	var dbpath string
	rows, e := f.db.Query(`PRAGMA database_list`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var seq int
		var name, path string
		if e = rows.Scan(&seq, &name, &path); e != nil {
			t.Fatal(e)
		}
		if name == "main" {
			dbpath = path
		}
	}
	rows.Close()
	b := storage.NewBackups(dbpath, filepath.Dir(dbpath), "/music")
	if e = b.Initialize(); e != nil {
		t.Fatal(e)
	}
	name, e := b.Create(f.ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	stage, _, e := b.Stage(f.ctx, filepath.Join(b.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Arm(f.ctx, stage); e != nil {
		t.Fatal(e)
	}
	f.db.Close()
	f.store.Close()
	if _, e = b.PrepareRestore(f.ctx); e != nil {
		t.Fatal(e)
	}
	restored, e := storage.Open(dbpath)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if e = restored.Migrate(f.ctx); e != nil {
		t.Fatal(e)
	}
	if e = b.CompleteRestore(f.ctx, restored); e != nil {
		t.Fatal(e)
	}
	f.f.visible = true
	s := New(restored, nil, Options{})
	s.reconcile(f.ctx, true, true)
	got, e := restored.AcquisitionTask(f.ctx, v.ID)
	if e != nil || got.State != "DOWNLOADING" || f.f.adds != 0 {
		t.Fatal(got.State, e, f.f.adds)
	}
}

func TestServicePartialScanAndAttentionEvidence(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "partial")
	f.s.reconcile(f.ctx, false, false)
	// A partial rebuild is an error, not an empty evidence set. The periodic
	// worker must defer without depending on a successful scanner callback.
	job, e := f.store.CreateScanJob(f.ctx, f.library, "full")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.BeginReleaseGroupRebuild(f.ctx, job); e != nil {
		t.Fatal(e)
	}
	if e = f.store.UpdateScanJob(f.ctx, job, 1, 0, 1, ""); e != nil {
		t.Fatal(e)
	}
	if e = f.store.FinishScanJob(f.ctx, job, "completed", 0, ""); e != nil {
		t.Fatal(e)
	}
	if e = f.store.CompleteReleaseGroupRebuild(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.s.reconcile(f.ctx, false, true)
	if got := f.task(t, v); got.State != "DOWNLOADING" {
		t.Fatal(got.State)
	}
	if _, e = f.db.Exec(`UPDATE release_group_rebuild SET status='ready'; UPDATE acquisition_tasks SET state='ATTENTION',attention_reason='library_timeout',created_at='2000-01-01T00:00:00.000Z'`); e != nil {
		t.Fatal(e)
	}
	f.importFile(t, "after/song.flac")
	f.s.reconcile(f.ctx, false, true)
	if got := f.task(t, v); got.State != "IN_LIBRARY" {
		t.Fatal(got.State)
	}
}
func TestServiceAwaitingTimeoutDiagnosesPreexisting(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "awaiting")
	f.f.complete = true
	f.s.reconcile(f.ctx, false, false)
	f.importFile(t, "old/song.flac")
	if _, e := f.db.Exec(`UPDATE audio_files SET created_at='1999-01-01T00:00:00.000Z'`); e != nil {
		t.Fatal(e)
	}
	*f.now = f.now.Add(73 * time.Hour)
	f.s.reconcile(f.ctx, false, true)
	v = f.task(t, v)
	if v.State != "ATTENTION" || v.AttentionReason != "preexisting_only" {
		t.Fatal(v.State, v.AttentionReason)
	}
}

func TestServiceSecondAddCanSucceedAndAlreadyComplete(t *testing.T) {
	f := setup(t)
	f.f.unknown = true
	v := f.confirm(t, testRG, "second-success")
	f.s.reconcile(f.ctx, false, false)
	if _, err := f.db.Exec(`UPDATE acquisition_tasks SET add_unknown_since=? WHERE id=?`, stamp(*f.now), v.ID); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(3 * time.Minute)
	f.f.unknown = false
	f.f.complete = true
	f.s.reconcile(f.ctx, false, false)
	v = f.task(t, v)
	if v.State != "AWAITING_LIBRARY" || f.f.adds != 2 || v.AddUnknownSince != "" || v.QBUnreachableSince != "" {
		t.Fatal(v.State, f.f.adds)
	}
}
func TestServiceAddExplicitRejectionAndUnavailable(t *testing.T) {
	f := setup(t)
	f.f.reject = true
	v := f.confirm(t, testRG, "reject")
	f.s.reconcile(f.ctx, false, false)
	v = f.task(t, v)
	if v.State != "ATTENTION" || v.AttentionReason != "qb_rejected" {
		t.Fatal(v.State)
	}
	v, e := f.s.Retry(f.ctx, v.PublicID)
	if e != nil {
		t.Fatal(e)
	}
	v.InfoHash = strings.Repeat("0", 40)
	if _, e = f.store.CASAcquisitionTask(f.ctx, v); e != nil {
		t.Fatal(e)
	}
	f.s.reconcile(f.ctx, true, false)
	v = f.task(t, v)
	if v.State != "ATTENTION" || v.AttentionReason != "torrent_unavailable" {
		t.Fatal(v.State, v.AttentionReason)
	}
}

func TestServiceTaskJSONDoesNotExposeDownloadReference(t *testing.T) {
	b, err := json.Marshal(storage.AcquisitionTask{DownloadRef: "/dl/private-reference"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-reference") || strings.Contains(string(b), "DownloadRef") {
		t.Fatal(string(b))
	}
}

func TestServiceMissingDeadlineChecksEvidenceBetweenEvidenceTicks(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "deadline-evidence")
	f.s.reconcile(f.ctx, false, false)
	*f.now = f.now.Add(time.Minute)
	f.f.visible = false
	f.s.reconcile(f.ctx, false, false)
	if _, err := f.db.Exec(`UPDATE acquisition_tasks SET created_at='2000-01-01T00:00:00.000Z' WHERE id=?`, v.ID); err != nil {
		t.Fatal(err)
	}
	f.importFile(t, "deadline/song.flac")
	*f.now = f.now.Add(73 * time.Hour)
	// A qB tick is not normally an evidence tick, but timing out must still
	// inspect evidence before declaring the disappeared torrent needs attention.
	f.s.reconcile(f.ctx, false, false)
	if got := f.task(t, v); got.State != "IN_LIBRARY" {
		t.Fatal(got.State)
	}
}

func TestServiceProbeCachedLoginRejectsOfflineConfirm(t *testing.T) {
	f := setup(t)
	v, err := f.s.Search(f.ctx, testRG, "album", false)
	if err != nil {
		t.Fatal(err)
	}
	q, _, _, err := f.s.clients(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(f.ctx); err != nil {
		t.Fatal(err)
	}
	qbServer := httptest.NewServer(http.HandlerFunc(f.f.handler))
	settings, _ := f.store.AcquisitionSettings(f.ctx)
	if err = f.store.UpdateAcquisitionSettings(f.ctx, storage.AcquisitionSettingsUpdate{Enabled: true, JackettURL: settings.JackettURL, QBURL: qbServer.URL}); err != nil {
		t.Fatal(err)
	}
	f.s.SettingsChanged()
	q, _, _, err = f.s.clients(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Login(f.ctx); err != nil {
		t.Fatal(err)
	}
	downloads := f.f.downloads
	qbServer.Close()
	if _, err = f.s.Confirm(f.ctx, testRG, v.SearchID, 0, "offline"); err == nil {
		t.Fatal("accepted offline qB")
	}
	if f.f.downloads != downloads {
		t.Fatal("downloaded before probe succeeded")
	}
	tasks, _ := f.store.ListAcquisitionTasks(f.ctx)
	if len(tasks) != 0 {
		t.Fatal("task created")
	}
}
func TestServiceRedownloadTemporaryFailures(t *testing.T) {
	for _, code := range []int{429, 500, 503, 404} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := setup(t)
			v := f.confirm(t, testRG, "retry-download")
			f.s.pendingMu.Lock()
			clear(f.s.pending)
			f.s.pendingMu.Unlock()
			f.f.downloadCode = code
			f.s.reconcile(f.ctx, true, false)
			v = f.task(t, v)
			if code == 404 {
				if v.State != "ATTENTION" {
					t.Fatal(v.State)
				}
			} else if v.State != "QB_SUBMITTING" || v.NextCheckAt == "" {
				t.Fatal(v.State)
			}
		})
	}
	for _, code := range []int{500, 501} {
		t.Run(fmt.Sprint("api", code), func(t *testing.T) {
			f := setup(t)
			v := f.confirm(t, testRG, "api")
			f.s.dropPending(v.ID)
			f.f.downloadAPI = code
			f.s.reconcile(f.ctx, true, false)
			if got := f.task(t, v); got.State != "QB_SUBMITTING" || got.NextCheckAt == "" {
				t.Fatal(got.State)
			}
		})
	}
	f := setup(t)
	v := f.confirm(t, testRG, "cooldown")
	f.s.downloadError(f.ctx, v, &sourcegate.CooldownError{RetryAfter: 7 * time.Minute})
	v = f.task(t, v)
	if v.NextCheckAt != stamp(f.now.Add(7*time.Minute)) {
		t.Fatal(v.NextCheckAt)
	}
	f.s.downloadError(f.ctx, v, context.DeadlineExceeded)
	if got := f.task(t, v); got.State != "QB_SUBMITTING" {
		t.Fatal(got.State)
	}
}
func TestServiceSecondAddAsyncVisibilityWindow(t *testing.T) {
	for _, appears := range []bool{false, true} {
		t.Run(fmt.Sprint(appears), func(t *testing.T) {
			f := setup(t)
			f.f.delayed = true
			v := f.confirm(t, testRG, "async")
			f.s.reconcile(f.ctx, false, false)
			first := f.task(t, v)
			*f.now = parseTime(first.AddUnknownSince).Add(3 * time.Minute)
			f.s.reconcile(f.ctx, true, false)
			second := f.task(t, v)
			if second.State != "QB_SUBMITTING" || second.SubmitAttempts != 2 {
				t.Fatal(second.State, second.SubmitAttempts)
			}
			// No database clock edits: age the actual SQLite reset returned by CAS.
			*f.now = parseTime(second.AddUnknownSince).Add(time.Minute)
			f.f.visible = appears
			f.s.reconcile(f.ctx, true, false)
			if appears {
				if got := f.task(t, v); got.State != "DOWNLOADING" {
					t.Fatal(got.State)
				}
			} else {
				if got := f.task(t, v); got.State != "QB_SUBMITTING" {
					t.Fatal("window prematurely expired")
				}
				*f.now = parseTime(second.AddUnknownSince).Add(3 * time.Minute)
				f.s.reconcile(f.ctx, true, false)
				got := f.task(t, v)
				if got.State != "ATTENTION" || got.AttentionReason != "qb_rejected" || f.f.adds != 2 {
					t.Fatal(got.State, got.AttentionReason, f.f.adds)
				}
			}
		})
	}
}
func TestServiceConfirmNotBlockedByReconciliation(t *testing.T) {
	f := setup(t)
	_ = f.confirm(t, testRG, "first")
	search, err := f.s.Search(f.ctx, otherRG, "album", false)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.f.onInfo = func() { once.Do(func() { close(entered); <-release }) }
	go func() { f.s.reconcile(f.ctx, true, false); close(done) }()
	<-entered
	// The duplicate hash is expected; reaching it proves Confirm performed live
	// probe/download/insert while the background Info request was blocked.
	_, err = f.s.Confirm(f.ctx, otherRG, search.SearchID, 0, "second")
	if !errors.Is(err, storage.ErrAcquisitionHashBound) {
		t.Error(err)
	}
	close(release)
	<-done
}
func TestServiceRetryWithoutDownloadRefOnlyReconciles(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "no-ref")
	f.s.reconcile(f.ctx, false, false)
	v = f.task(t, v)
	v.State = "ATTENTION"
	v.AttentionReason = "qb_missing"
	v, err := f.store.CASAcquisitionTask(f.ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := f.s.Retry(f.ctx, v.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	*f.now = parseTime(retried.QBMissingSince)
	f.f.visible = false
	downloads := f.f.downloads
	f.s.reconcile(f.ctx, true, false)
	v = f.task(t, v)
	if v.State != "DOWNLOADING" || v.NextCheckAt == "" || f.f.downloads != downloads {
		t.Fatal(v.State)
	}
}
func TestServiceLogsNeverContainCredentialsOrDownloadRef(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "logs")
	f.s.reconcile(f.ctx, false, false)
	v = f.task(t, v)
	f.s.qbError(f.ctx, v, qbit.ErrAuth)
	f.s.SettingsChanged()
	f.server.Close()
	v = f.task(t, v)
	f.s.qbError(f.ctx, v, fmt.Errorf("unreachable SECRET PASSWORD private-reference"))
	if f.logs.Len() == 0 {
		t.Fatal("no service logging exercised")
	}
	for _, secret := range []string{"SECRET", "PASSWORD", "private-reference", "/dl/"} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal(f.logs.String())
		}
	}
}
func TestServiceUserUpdateRetriesActiveStale(t *testing.T) {
	f := setup(t)
	v := f.confirm(t, testRG, "stale-retry")
	calls := 0
	_, err := f.s.userUpdate(f.ctx, v.PublicID, func(tk storage.AcquisitionTask) (storage.AcquisitionTask, error) {
		calls++
		if calls == 1 {
			if _, err := f.store.CASAcquisitionTask(f.ctx, tk); err != nil {
				t.Fatal(err)
			}
		}
		tk.State = "CANCELLED"
		return f.store.CASAcquisitionTask(f.ctx, tk)
	})
	if err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	bound, err := f.store.ActiveAcquisitionByHash(f.ctx, v.InfoHash)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(bound.PublicID, err)
	}
}

func TestServiceConcurrentConfirmPreservesPendingBytes(t *testing.T) {
	f := setup(t)
	first := f.confirm(t, testRG, "snapshot-first")
	// Retire the first hash so the new task may legitimately bind the same fixture torrent.
	if _, err := f.s.Cancel(f.ctx, first.PublicID); err != nil {
		t.Fatal(err)
	}
	// A separate active task supplies the blocking Info during the snapshot pass.
	first.State = "DOWNLOADING"
	first.ID = 0
	first.PublicID = ""
	first.IdempotencyKey = "blocker"
	first.RGID = testRG
	first.InfoHash = strings.Repeat("b", 40)
	blocker, err := f.store.CreateAcquisitionTask(f.ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	search, err := f.s.Search(f.ctx, otherRG, "album", false)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.f.onInfo = func() { once.Do(func() { close(entered); <-release }) }
	go func() { f.s.reconcile(f.ctx, true, false); close(done) }()
	<-entered
	task, err := f.s.Confirm(f.ctx, otherRG, search.SearchID, 0, "snapshot-new")
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	f.s.pendingMu.Lock()
	n := len(f.s.pending[task.ID])
	f.s.pendingMu.Unlock()
	if n == 0 {
		t.Fatal("new pending bytes lost")
	}
	if _, err = f.s.Cancel(f.ctx, blocker.PublicID); err != nil {
		t.Fatal(err)
	}
	downloads := f.f.downloads
	f.s.reconcile(f.ctx, true, false)
	if f.f.downloads != downloads {
		t.Fatal("unnecessary redownload")
	}
}
func TestServicePermanentDownloadErrorsNeedAttention(t *testing.T) {
	for _, err := range []error{&jackett.APIError{Code: 100}, jackett.ErrMagnetOnly, errors.New("invalid download reference")} {
		t.Run(err.Error(), func(t *testing.T) {
			f := setup(t)
			v := f.confirm(t, testRG, "permanent")
			f.s.downloadError(f.ctx, v, err)
			got := f.task(t, v)
			if got.State != "ATTENTION" || got.AttentionReason != "torrent_unavailable" {
				t.Fatal(got.State)
			}
		})
	}
}
func TestServiceReferenceFreeRetryEvidenceAndDeadline(t *testing.T) {
	for _, evidence := range []bool{false, true} {
		t.Run(fmt.Sprint(evidence), func(t *testing.T) {
			f := setup(t)
			v := f.confirm(t, testRG, "ref-free")
			f.s.reconcile(f.ctx, false, false)
			v = f.task(t, v)
			v.State = "ATTENTION"
			v.AttentionReason = "qb_missing"
			if _, err := f.store.CASAcquisitionTask(f.ctx, v); err != nil {
				t.Fatal(err)
			}
			retried, err := f.s.Retry(f.ctx, v.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			if retried.State != "DOWNLOADING" || retried.QBMissingSince == "" {
				t.Fatal(retried.State)
			}
			*f.now = parseTime(retried.QBMissingSince).Add(73 * time.Hour)
			f.f.visible = false
			if evidence {
				if _, err = f.db.Exec(`UPDATE acquisition_tasks SET created_at='2000-01-01T00:00:00.000Z' WHERE id=?`, v.ID); err != nil {
					t.Fatal(err)
				}
				f.importFile(t, "retried/song.flac")
			}
			f.s.reconcile(f.ctx, true, true)
			got := f.task(t, v)
			if evidence {
				if got.State != "IN_LIBRARY" {
					t.Fatal(got.State)
				}
			} else if got.State != "ATTENTION" || got.AttentionReason != "qb_missing" {
				t.Fatal(got.State, got.AttentionReason)
			}
		})
	}
}

func TestConfirmHashBoundPublicTask(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprint("uniqueConflict=", race), func(t *testing.T) {
			f := setup(t)
			var bound storage.AcquisitionTask
			if !race {
				bound = f.confirm(t, testRG, "bound")
			} else {
				f.s.beforeCreate = func() {
					meta, err := torrent.Parse(torrentBytes)
					if err != nil {
						t.Fatal(err)
					}
					bound, err = f.store.CreateAcquisitionTask(f.ctx, storage.AcquisitionTask{RGID: testRG, Title: "bound", IdempotencyKey: "bound", InfoHash: meta.InfoHash})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			search, err := f.s.Search(f.ctx, otherRG, "album", true)
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.s.Confirm(f.ctx, otherRG, search.SearchID, 0, "conflict")
			if !errors.Is(err, storage.ErrAcquisitionHashBound) || got.PublicID != bound.PublicID || got.PublicID == "" {
				t.Fatalf("binding %q expected %q: %v", got.PublicID, bound.PublicID, err)
			}
		})
	}
}
func TestHashBoundAlreadyFinished(t *testing.T) {
	f := setup(t)
	task := f.confirm(t, testRG, "bound")
	if _, err := f.s.Cancel(f.ctx, task.PublicID); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.hashBound(f.ctx, task.InfoHash)
	if !errors.Is(err, storage.ErrAcquisitionHashBound) || got.PublicID != "" {
		t.Fatalf("%q %v", got.PublicID, err)
	}
}

func TestConfirmRechecksDiscoveryDecisionBeforeCreate(t *testing.T) {
	for _, decision := range []string{"ignored", "already_owned"} {
		t.Run(decision, func(t *testing.T) {
			f := setup(t)
			search, err := f.s.Search(f.ctx, testRG, "album", true)
			if err != nil {
				t.Fatal(err)
			}
			// The hook runs after network preflight but immediately before task creation.
			f.s.beforeCreate = func() {
				if decision == "ignored" {
					if err := f.store.DiscoveryIgnore(f.ctx, testRG, true); err != nil {
						t.Fatal(err)
					}
					return
				}
				if err := f.store.ImportTrack(f.ctx, storage.ImportInput{LibraryID: f.library, RelativePath: "owned.flac", Metadata: metadata.AudioMetadata{Title: "song", DiscNumber: 1, TrackNumber: 1, Album: "album", Artists: []string{"artist"}}}); err != nil {
					t.Fatal(err)
				}
				var album int64
				if err := f.db.QueryRow(`SELECT id FROM albums LIMIT 1`).Scan(&album); err != nil {
					t.Fatal(err)
				}
				if err := f.store.DiscoveryOwned(f.ctx, testRG, album, false); err != nil {
					t.Fatal(err)
				}
			}
			_, err = f.s.Confirm(f.ctx, testRG, search.SearchID, 0, "decision-race")
			var ae *Error
			if !errors.As(err, &ae) || ae.Code != decision {
				t.Fatalf("want %s: %v", decision, err)
			}
			tasks, err := f.store.ListAcquisitionTasks(f.ctx)
			if err != nil || len(tasks) != 0 {
				t.Fatal(tasks, err)
			}
		})
	}
}

func TestCancelDropsUnsubmittedPendingBytes(t *testing.T) {
	f := setup(t)
	task := f.confirm(t, testRG, "cancel-pending")
	if len(f.s.pending[task.ID]) == 0 {
		t.Fatal("missing pending bytes")
	}
	if _, err := f.s.Cancel(f.ctx, task.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.s.pending[task.ID]; ok {
		t.Fatal("cancelled task retained pending bytes")
	}
}
