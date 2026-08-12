package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"path/filepath"
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
	db *sql.DB
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

	return &Store{db: db}, nil
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
		transaction, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := transaction.ExecContext(ctx, string(script)); err != nil {
			transaction.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name) VALUES (?, ?)", version, entry.Name(),
		); err != nil {
			transaction.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}

	return nil
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
