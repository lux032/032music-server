package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	db retryDB
}

type Statistics struct {
	Libraries      int64 `json:"libraries"`
	Artists        int64 `json:"artists"`
	Albums         int64 `json:"albums"`
	Tracks         int64 `json:"tracks"`
	AudioFiles     int64 `json:"audioFiles"`
	ScanJobs       int64 `json:"scanJobs"`
	Playlists      int64 `json:"playlists"`
	FavoriteAlbums int64 `json:"favoriteAlbums"`
	FavoriteTracks int64 `json:"favoriteTracks"`
}

func Open(databasePath string) (*Store, error) {
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}

	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	databaseURIPath := filepath.ToSlash(absolutePath)
	if runtime.GOOS == "windows" {
		// A Windows drive path must be represented as file:///D:/path.
		// Without the leading slash, SQLite interprets "D:" as a URI authority.
		databaseURIPath = "/" + databaseURIPath
	}
	dsn := (&url.URL{Scheme: "file", Path: databaseURIPath, RawQuery: query.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	return &Store{db: retryDB{db}}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		) STRICT;
	`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionText, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(versionText)
		if err != nil {
			return fmt.Errorf("parse migration version from %q: %w", entry.Name(), err)
		}

		var alreadyApplied bool
		if err := s.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)", version,
		).Scan(&alreadyApplied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if alreadyApplied {
			continue
		}

		script, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %d: %w", version, err)
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("acquire migration connection %d: %w", version, err)
		}
		// Foreign keys are disabled for the duration of each migration so
		// table-rebuild migrations (drop + rename) can run safely. The
		// pragma is a no-op inside a transaction, so it must be set on the
		// dedicated connection before BeginTx.
		if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
			conn.Close()
			return fmt.Errorf("disable foreign keys for migration %d: %w", version, err)
		}
		err = s.applyMigration(ctx, conn, version, entry.Name(), string(script))
		_, _ = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		conn.Close()
		if err != nil {
			return err
		}
	}

	// D-9/D62: SQL migrations cannot remove NUL bytes from TEXT values, so
	// the control-character cleanup of tag-derived album fields runs here in
	// Go. It is idempotent: once no row matches the dirty predicate this is a
	// no-op.
	if err := s.cleanAlbumTagControlChars(ctx); err != nil {
		return fmt.Errorf("clean album tag control characters: %w", err)
	}
	// D-19: same repair for the track-level fields derived from the
	// reader-side rawFirst closure (lyricist/arranger/reading_title) and the
	// tag-derived artist reading name.
	if err := s.cleanTrackTagControlChars(ctx); err != nil {
		return fmt.Errorf("clean track tag control characters: %w", err)
	}
	// Batch 9.5: old builds stored album review candidates that had no
	// anime/game tie-up and therefore could never be accepted. This Go-side
	// repair needs no schema migration and is safe to run after every migrate.
	if err := s.cleanEmptyAlbumSubjectCandidates(ctx); err != nil {
		return fmt.Errorf("clean empty album subject candidates: %w", err)
	}

	return nil
}

func (s *Store) cleanEmptyAlbumSubjectCandidates(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM album_subject_candidates WHERE status='candidate' AND CASE WHEN json_valid(tieups_json) THEN json_array_length(tieups_json)=0 ELSE 1 END`)
	return err
}

var addColumnPattern = regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+COLUMN\s+([A-Za-z_][A-Za-z0-9_]*)`)

var createObjectPattern = regexp.MustCompile(`(?i)^(\s*)CREATE\s+(TABLE|UNIQUE\s+INDEX|INDEX|TRIGGER)\s+`)

func (s *Store) applyMigration(ctx context.Context, conn *sql.Conn, version int, name, script string) error {
	transaction, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}
	// M12: make migrations idempotent against databases where objects were
	// already created by hand or by an older build. ADD COLUMN statements
	// whose column already exists are filtered out line-by-line (trigger
	// bodies never start with ALTER TABLE, so multi-line statements are
	// unaffected); CREATE TABLE/INDEX/TRIGGER gain IF NOT EXISTS.
	lines := strings.Split(script, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if match := addColumnPattern.FindStringSubmatch(line); match != nil {
			exists, probeErr := columnExists(ctx, transaction, match[1], match[2])
			if probeErr != nil {
				transaction.Rollback()
				return fmt.Errorf("probe column for migration %d: %w", version, probeErr)
			}
			if exists {
				continue
			}
		}
		if match := createObjectPattern.FindStringSubmatch(line); match != nil && !strings.Contains(strings.ToUpper(line), "IF NOT EXISTS") {
			line = createObjectPattern.ReplaceAllString(line, "${1}CREATE ${2} IF NOT EXISTS ")
		}
		kept = append(kept, line)
	}
	if _, err := transaction.ExecContext(ctx, strings.Join(kept, "\n")); err != nil {
		transaction.Rollback()
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name) VALUES (?, ?)", version, name,
	); err != nil {
		transaction.Rollback()
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	return nil
}

func columnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) EnsureLibrary(ctx context.Context, name, rootPath string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO libraries(name, root_path) VALUES (?, ?)
		ON CONFLICT(root_path) DO UPDATE SET
			name = excluded.name,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, name, rootPath)
	if err != nil {
		return fmt.Errorf("ensure configured library: %w", err)
	}
	return nil
}

func (s *Store) Statistics(ctx context.Context) (Statistics, error) {
	var stats Statistics
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM libraries),
			(SELECT COUNT(*) FROM artists WHERE merged_into_artist_id IS NULL),
			(SELECT COUNT(*) FROM albums),
			(SELECT COUNT(*) FROM tracks),
			(SELECT COUNT(*) FROM audio_files),
			(SELECT COUNT(*) FROM scan_jobs),
			(SELECT COUNT(*) FROM playlists),
			(SELECT COUNT(*) FROM albums WHERE is_favorite=1),
			(SELECT COUNT(*) FROM tracks WHERE is_favorite=1)
	`).Scan(&stats.Libraries, &stats.Artists, &stats.Albums, &stats.Tracks, &stats.AudioFiles, &stats.ScanJobs, &stats.Playlists, &stats.FavoriteAlbums, &stats.FavoriteTracks)
	if err != nil {
		return Statistics{}, fmt.Errorf("query library statistics: %w", err)
	}
	return stats, nil
}
