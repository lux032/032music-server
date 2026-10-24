package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/acquisition/jackett"
)

func TestAcquisitionSettingsWriteOnly(t *testing.T) {
	f := newAlbumMergeFixture(t)
	s := f.store
	c := f.ctx
	v, e := s.AcquisitionSettings(c)
	if e != nil || v.KeySet || v.HasPassword || v.JackettIndexer != "jpopsuki" {
		t.Fatal(v, e)
	}
	u := AcquisitionSettingsUpdate{Enabled: true, JackettURL: "http://jackett", JackettAPIKey: "KEY", QBURL: "http://qb", QBUsername: "user", QBPassword: "PASS", QBCategory: "music"}
	if e = s.UpdateAcquisitionSettings(c, u); e != nil {
		t.Fatal(e)
	}
	u.JackettAPIKey = ""
	u.QBPassword = ""
	if e = s.UpdateAcquisitionSettings(c, u); e != nil {
		t.Fatal(e)
	}
	secret, e := s.AcquisitionCredentials(c)
	if e != nil || secret.JackettAPIKey != "KEY" || secret.QBPassword != "PASS" {
		t.Fatal(secret, e)
	}
	v, e = s.AcquisitionSettings(c)
	if e != nil || !v.KeySet || !v.HasPassword || v.QBCategory != "music" {
		t.Fatal(v, e)
	}
	u.ClearKey = true
	u.ClearPassword = true
	if e = s.UpdateAcquisitionSettings(c, u); e != nil {
		t.Fatal(e)
	}
	v, e = s.AcquisitionSettings(c)
	if e != nil || v.KeySet || v.HasPassword {
		t.Fatal(v, e)
	}
}
func TestAcquisitionSearchSnapshotAndExpiry(t *testing.T) {
	f := newAlbumMergeFixture(t)
	s := f.store
	c := f.ctx
	empty, e := s.SaveAcquisitionSearch(c, AcquisitionSearch{RGID: rgA, Indexer: "jpopsuki", Query: " Artist  Title "})
	if e != nil {
		t.Fatal(e)
	}
	cached, e := s.CachedAcquisitionSearch(c, strings.ToUpper(rgA), "jpopsuki", "artist title")
	if e != nil || cached.ID != empty.ID || cached.Results == nil || len(cached.Results) != 0 {
		t.Fatal(cached, e)
	}
	v, e := s.SaveAcquisitionSearch(c, AcquisitionSearch{RGID: rgA, Indexer: "jpopsuki", Query: "other", Results: []jackett.Result{{Title: "FLAC", DownloadRef: "http://wrong/dl/jpopsuki/?jackett_apikey=SECRET&path=p"}, {Title: "magnet", MagnetOnly: true}}})
	if e != nil {
		t.Fatal(e)
	}
	v, e = s.AcquisitionSearch(c, v.ID)
	if e != nil || len(v.Results) != 2 || strings.Contains(v.Results[0].DownloadRef, "SECRET") || !v.Results[1].MagnetOnly {
		t.Fatal(v, e)
	}
	if _, e = s.db.ExecContext(c, `UPDATE acquisition_searches SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-16 minutes') WHERE id=?`, v.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AcquisitionSearch(c, v.ID); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	if e = s.CleanupAcquisitionSearches(c); e != nil {
		t.Fatal(e)
	}
	if n := f.count(t, `SELECT count(*) FROM acquisition_search_results`); n != 0 {
		t.Fatal(n)
	}
	if _, e = s.SaveAcquisitionSearch(c, AcquisitionSearch{RGID: rgA, Results: []jackett.Result{{DownloadRef: "/api/evil"}}}); e == nil {
		t.Fatal("invalid ref saved")
	}
}
func createAcquisition(t *testing.T, f albumMergeFixture, rg, key, hash string) AcquisitionTask {
	t.Helper()
	v, e := f.store.CreateAcquisitionTask(f.ctx, AcquisitionTask{RGID: rg, IdempotencyKey: key, Title: "album", Artist: "artist", Kind: "Album", TorrentTitle: "torrent", InfoHash: hash, DownloadRef: "/dl/jpopsuki/?path=x"})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestAcquisitionTaskIdempotencyUniqueAndCAS(t *testing.T) {
	f := newAlbumMergeFixture(t)
	hash := strings.Repeat("a", 40)
	v := createAcquisition(t, f, rgA, "request", hash)
	if len(v.PublicID) != 36 || v.CreatedAt == "" || v.State != "QB_SUBMITTING" {
		t.Fatal(v)
	}
	old, e := f.store.CreateAcquisitionTask(f.ctx, AcquisitionTask{IdempotencyKey: "request"})
	if e != nil || old.ID != v.ID || old.PublicID != v.PublicID {
		t.Fatal(old, e)
	}
	if _, e = f.store.CreateAcquisitionTask(f.ctx, AcquisitionTask{RGID: rgA, IdempotencyKey: "another"}); !errors.Is(e, ErrAcquisitionRGActive) {
		t.Fatal(e)
	}
	if _, e = f.store.CreateAcquisitionTask(f.ctx, AcquisitionTask{RGID: rgB, IdempotencyKey: "another", InfoHash: hash}); !errors.Is(e, ErrAcquisitionHashBound) {
		t.Fatal(e)
	}
	if _, e = f.store.CreateAcquisitionTask(f.ctx, AcquisitionTask{RGID: rgB, IdempotencyKey: "bad-hash", InfoHash: strings.Repeat("A", 40)}); e == nil {
		t.Fatal("bad hash accepted")
	}
	late := v
	v.State = "CANCELLED"
	v, e = f.store.CASAcquisitionTask(f.ctx, v)
	if e != nil || v.Version != 1 {
		t.Fatal(v, e)
	}
	late.State = "DOWNLOADING"
	if _, e = f.store.CASAcquisitionTask(f.ctx, late); !errors.Is(e, ErrAcquisitionStale) {
		t.Fatal(e)
	}
	next := createAcquisition(t, f, rgA, "new-request", hash)
	next.State = "DOWNLOADING"
	next, e = f.store.CASAcquisitionTask(f.ctx, next)
	if e != nil || next.DownloadRef != "" {
		t.Fatal(next, e)
	}
	list, e := f.store.ListAcquisitionTasks(f.ctx)
	if e != nil || len(list) != 1 || list[0].ID != next.ID {
		t.Fatal(list, e)
	}
}
func TestAcquisitionEvidenceAndIdentity(t *testing.T) {
	f := newAlbumMergeFixture(t)
	v := createAcquisition(t, f, rgA, "request", strings.Repeat("b", 40))
	if _, e := f.store.AcquisitionFileEvidence(f.ctx, v); !errors.Is(e, ErrIdentityNotReady) {
		t.Fatal(e)
	}
	if _, e := f.store.CompleteAcquisitionWithEvidence(f.ctx, v, AcquisitionEvidence{Source: "file"}); !errors.Is(e, ErrIdentityNotReady) {
		t.Fatal(e)
	}
	if _, e := f.store.DiagnoseAcquisition(f.ctx, v); !errors.Is(e, ErrIdentityNotReady) {
		t.Fatal(e)
	}
	setReleaseGroupIdentityReady(t, f)
	importRG(t, f, "A/1.flac", "A", rgA)
	// Same-millisecond insertion is deliberately not evidence; set fixture's
	// creation clock earlier, while production clocks always come from SQLite.
	if _, e := f.store.db.ExecContext(f.ctx, `UPDATE acquisition_tasks SET created_at='2000-01-01T00:00:00.000Z' WHERE id=?`, v.ID); e != nil {
		t.Fatal(e)
	}
	v, _ = f.store.AcquisitionTask(f.ctx, v.ID)
	files, e := f.store.AcquisitionFileEvidence(f.ctx, v)
	if e != nil || len(files) != 1 {
		t.Fatal(files, e)
	}
	late := v
	v.State = "DOWNLOADING"
	v, e = f.store.CASAcquisitionTask(f.ctx, v)
	if e != nil {
		t.Fatal(e)
	}
	ev := AcquisitionEvidence{Source: "file", AlbumID: files[0].AlbumID, AudioFileID: files[0].AudioFileID}
	if _, e = f.store.CompleteAcquisitionWithEvidence(f.ctx, late, ev); !errors.Is(e, ErrAcquisitionStale) {
		t.Fatal(e)
	}
	done, e := f.store.CompleteAcquisitionWithEvidence(f.ctx, v, ev)
	if e != nil || done.State != "IN_LIBRARY" {
		t.Fatal(done, e)
	}
	stored, e := f.store.AcquisitionEvidence(f.ctx, v.ID)
	if e != nil || stored.DetectedCount != 1 || stored.VerifiedAt == "" {
		t.Fatal(stored, e)
	}
	list, e := f.store.ListAcquisitionTasks(f.ctx)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	if _, e = f.store.db.ExecContext(f.ctx, `UPDATE acquisition_tasks SET finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-8 days') WHERE id=?`, v.ID); e != nil {
		t.Fatal(e)
	}
	list, e = f.store.ListAcquisitionTasks(f.ctx)
	if e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
}
func TestAcquisitionDiagnostics(t *testing.T) {
	for _, reason := range []string{"library_timeout", "rg_conflict", "preexisting_only", "no_rg_files", "rule_suppressed"} {
		t.Run(reason, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			setReleaseGroupIdentityReady(t, f)
			v := createAcquisition(t, f, rgA, "request", "")
			if reason == "preexisting_only" {
				importRG(t, f, "A/1.flac", "album", rgA)
				time.Sleep(5 * time.Millisecond)
				// Model a legacy/restored task created after these files. New
				// confirmations now correctly reject an already-owned RG.
				if _, err := f.store.db.ExecContext(f.ctx, `UPDATE acquisition_tasks SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, v.ID); err != nil {
					t.Fatal(err)
				}
				v, _ = f.store.AcquisitionTask(f.ctx, v.ID)
			}
			time.Sleep(5 * time.Millisecond)
			switch reason {
			case "rg_conflict":
				importRG(t, f, "A/1.flac", "A", rgA, rgB)
			case "preexisting_only":
				importRG(t, f, "A/1.flac", "album", rgA) // overwrite is not new evidence
			case "no_rg_files":
				importRG(t, f, "album/1.flac", "album")
			case "rule_suppressed":
				_, e := f.store.db.ExecContext(f.ctx, `INSERT INTO album_key_rules(library_id,grouping_key,action,source_title) VALUES(?,'test','delete','album deluxe')`, f.library)
				if e != nil {
					t.Fatal(e)
				}
			}
			got, e := f.store.DiagnoseAcquisition(f.ctx, v)
			if e != nil || got != reason {
				t.Fatal(got, e)
			}
		})
	}
}
func TestAcquisitionBackupPreservesConnectionGroupAndClearsSnapshots(t *testing.T) {
	ctx := context.Background()
	old, source := backupFixture(t, 0)
	mustExecBackup(t, old, `UPDATE acquisition_settings SET jackett_url='http://old',jackett_api_key='OLD',qb_url='http://old-qb',qb_password='OLD'; INSERT INTO acquisition_searches(id,rgid,indexer,query,query_key,result_count) VALUES('search','`+rgA+`','jpopsuki','q','q',1); INSERT INTO acquisition_search_results(search_id,idx,title,download_ref) VALUES('search',0,'title','/dl/test'); INSERT INTO acquisition_tasks(public_id,idempotency_key,rgid,title,artist,kind,torrent_title,state) VALUES('`+rgB+`','old-task','`+rgA+`','title','artist','Album','torrent','DOWNLOADING')`)
	name, e := source.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	current, b := backupFixture(t, 0)
	mustExecBackup(t, current, `UPDATE acquisition_settings SET jackett_url='http://current',jackett_api_key='KEY',jackett_indexer='custom',qb_url='http://current-qb',qb_username='user',qb_password='PASS',qb_category='music'`)
	stage, _, e := b.Stage(ctx, filepath.Join(source.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Arm(ctx, stage); e != nil {
		t.Fatal(e)
	}
	current.Close()
	if _, e = b.PrepareRestore(ctx); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(b.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if e = restored.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = b.CompleteRestore(ctx, restored); e != nil {
		t.Fatal(e)
	}
	settings, e := restored.AcquisitionSettings(ctx)
	if e != nil || settings.JackettURL != "http://current" || settings.JackettIndexer != "custom" || settings.QBURL != "http://current-qb" || settings.QBUsername != "user" || settings.QBCategory != "music" {
		t.Fatal(settings, e)
	}
	credentials, e := restored.AcquisitionCredentials(ctx)
	if e != nil || credentials.JackettAPIKey != "KEY" || credentials.QBPassword != "PASS" {
		t.Fatal(credentials, e)
	}
	for _, table := range []string{"acquisition_searches", "acquisition_search_results"} {
		var n int
		if e = restored.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != 0 {
			t.Fatal(table, n, e)
		}
	}
	tasks, e := restored.ListAcquisitionTasks(ctx)
	if e != nil || len(tasks) != 1 || tasks[0].IdempotencyKey != "old-task" {
		t.Fatal(tasks, e)
	}
}

func TestAcquisitionRejectsForgedClockAndEvidenceBypass(t *testing.T) {
	f := newAlbumMergeFixture(t)
	setReleaseGroupIdentityReady(t, f)
	task := createAcquisition(t, f, rgA, "one", strings.Repeat("a", 40))
	importRG(t, f, "album/old.flac", "album", rgA)
	time.Sleep(5 * time.Millisecond)
	// Seed a restored task whose evidence predates creation, without bypassing
	// the production creation guard for already-owned release groups.
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE acquisition_tasks SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	task, _ = f.store.AcquisitionTask(f.ctx, task.ID)
	importRG(t, f, "album/old.flac", "album", rgA)
	files, e := f.store.ReleaseGroupFileEvidence(f.ctx, rgA, "")
	if e != nil || len(files) != 1 {
		t.Fatal(files, e)
	}
	forged := AcquisitionTask{ID: task.ID, Version: task.Version, CreatedAt: "", RGID: rgA}
	if _, e = f.store.CompleteAcquisitionWithEvidence(f.ctx, forged, AcquisitionEvidence{Source: "file", AlbumID: files[0].AlbumID, AudioFileID: files[0].AudioFileID}); e == nil {
		t.Fatal("old file accepted")
	}
	if reason, e := f.store.DiagnoseAcquisition(f.ctx, forged); e != nil || reason != "preexisting_only" {
		t.Fatal(reason, e)
	}
	task.State = "IN_LIBRARY"
	if _, e = f.store.CASAcquisitionTask(f.ctx, task); e == nil {
		t.Fatal("evidence bypass")
	}
	other := createAcquisition(t, f, rgB, "two", strings.Repeat("b", 40))
	other.InfoHash = task.InfoHash
	if _, e = f.store.CASAcquisitionTask(f.ctx, other); !errors.Is(e, ErrAcquisitionHashBound) {
		t.Fatal(e)
	}
}
func TestAcquisitionUnrelatedFilesNotDiagnosed(t *testing.T) {
	f := newAlbumMergeFixture(t)
	setReleaseGroupIdentityReady(t, f)
	task := createAcquisition(t, f, rgA, "one", "")
	time.Sleep(5 * time.Millisecond)
	importRG(t, f, "unrelated/new.flac", "unrelated")
	if reason, e := f.store.DiagnoseAcquisition(f.ctx, task); e != nil || reason != "library_timeout" {
		t.Fatal(reason, e)
	}
}
func TestAcquisitionSettingsFormatting(t *testing.T) {
	v := AcquisitionSettingsUpdate{JackettAPIKey: "SECRET", QBPassword: "PASSWORD"}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b)+fmt.Sprintf("%v %#v", v, v), "SECRET") || strings.Contains(string(b)+fmt.Sprintf("%v %#v", v, v), "PASSWORD") {
		t.Fatal("settings leaked")
	}
}
func TestAcquisitionRestoreMissingSettingsDefaults(t *testing.T) {
	f := newAlbumMergeFixture(t)
	conn, err := f.store.db.Conn(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, e := conn.ExecContext(f.ctx, `ATTACH DATABASE ':memory:' AS prerestore`); e != nil {
		t.Fatal(e)
	}
	tx, e := conn.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = preserveCredentials(f.ctx, tx); e != nil {
		t.Fatal(e)
	}
	var indexer string
	if e = tx.QueryRowContext(f.ctx, `SELECT jackett_indexer FROM acquisition_settings`).Scan(&indexer); e != nil || indexer != "jpopsuki" {
		t.Fatal(indexer, e)
	}
}

func TestAcquisitionEvidenceReadsStoredClockAndOtherConnectionWrite(t *testing.T) {
	f := newAlbumMergeFixture(t)
	setReleaseGroupIdentityReady(t, f)
	v := createAcquisition(t, f, rgA, "clock", strings.Repeat("c", 40))
	importRG(t, f, "new/1.flac", "new", rgA)
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE acquisition_tasks SET created_at='2000-01-01T00:00:00.000Z' WHERE id=?`, v.ID); err != nil {
		t.Fatal(err)
	}
	files, err := f.store.AcquisitionFileEvidence(f.ctx, AcquisitionTask{ID: v.ID, RGID: rgB, CreatedAt: "9999"})
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	conn, err := f.store.db.Conn(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(f.ctx, `PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}
	// Attempt a competing commit after the first task read but before its
	// update. The early write lock must reject it; without that lock this
	// commit succeeds and upgrading the evidence transaction gets BUSY.
	called := false
	f.store.acquisitionAfterRead = func() {
		called = true
		if _, err := conn.ExecContext(f.ctx, `UPDATE acquisition_settings SET enabled=1`); !IsBusyError(err) {
			t.Errorf("expected held write lock, got %v", err)
		}
	}
	defer func() { f.store.acquisitionAfterRead = nil }()
	if _, err = f.store.CompleteAcquisitionWithEvidence(f.ctx, v, AcquisitionEvidence{Source: "file", AlbumID: files[0].AlbumID, AudioFileID: files[0].AudioFileID}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("interleaving hook not invoked")
	}
}
func TestAcquisitionManualLinkAtomicCASAndReplace(t *testing.T) {
	f := newAlbumMergeFixture(t)
	setReleaseGroupIdentityReady(t, f)
	importRG(t, f, "old/1.flac", "old", rgB)
	album := f.albumID(t, "old")
	v := createAcquisition(t, f, rgA, "link", strings.Repeat("d", 40))
	if _, err := f.store.LinkAcquisition(f.ctx, v, album, false, "manual"); err == nil {
		t.Fatal("expected replaces_rg")
	}
	done, err := f.store.LinkAcquisition(f.ctx, v, album, true, "manual")
	if err != nil || done.State != "IN_LIBRARY" {
		t.Fatal(done, err)
	}
	if _, err = f.store.LinkAcquisition(f.ctx, v, album, true, "owned"); !errors.Is(err, ErrAcquisitionStale) {
		t.Fatal(err)
	}
	ev, err := f.store.AcquisitionEvidence(f.ctx, v.ID)
	if err != nil || ev.Source != "manual" {
		t.Fatal(ev, err)
	}
}

func TestAcquisitionReconciliationClocksUseSQLite(t *testing.T) {
	f := newAlbumMergeFixture(t)
	v := createAcquisition(t, f, rgA, "sqlite-clock", strings.Repeat("e", 40))
	v.AddUnknownSince = "1900-01-01T00:00:00.000Z"
	v.QBUnreachableSince = v.AddUnknownSince
	v, err := f.store.CASAcquisitionTask(f.ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if v.AddUnknownSince < "2020" || v.QBUnreachableSince < "2020" {
		t.Fatal("caller clock used")
	}
	first := v.AddUnknownSince
	v.AddUnknownSince = "2099"
	v, err = f.store.CASAcquisitionTask(f.ctx, v)
	if err != nil || v.AddUnknownSince != first {
		t.Fatal("clock reset", err)
	}
	v.AddUnknownSince = ""
	v.QBUnreachableSince = ""
	v, err = f.store.CASAcquisitionTask(f.ctx, v)
	if err != nil || v.AddUnknownSince != "" || v.QBUnreachableSince != "" {
		t.Fatal("clock not cleared", err)
	}
}

func TestAcquisitionSecondAttemptResetsUnknownClockAtomically(t *testing.T) {
	f := newAlbumMergeFixture(t)
	v := createAcquisition(t, f, rgA, "second-clock", strings.Repeat("f", 40))
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE acquisition_tasks SET submit_attempts=1,add_unknown_since='2000-01-01T00:00:00.000Z' WHERE id=?`, v.ID); err != nil {
		t.Fatal(err)
	}
	v, err := f.store.AcquisitionTask(f.ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	v.SubmitAttempts = 2
	v, err = f.store.CASAcquisitionTask(f.ctx, v)
	if err != nil || v.AddUnknownSince < "2020" {
		t.Fatal(v.AddUnknownSince, err)
	}
}
