package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var timeNow = time.Now

var (
	// ErrNotFound indicates that a requested stored record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate indicates that a project name or root path is already stored.
	ErrDuplicate = errors.New("already exists")
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store provides access to the sk64 database. Its zero value is only safe to Close.
type Store struct {
	// Notice describes a non-fatal database recovery performed by Open.
	Notice string
	db     *sql.DB
}

// DefaultPath returns the default sk64 database path.
func DefaultPath() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "sk64", "sk64.db"), nil
}

// Open opens or creates the sk64 database at path and applies pending migrations.
// If the existing file is corrupt, Open moves it aside and creates a fresh database;
// Notice describes the recovery.
func Open(path string) (*Store, error) {
	db, err := openAndMigrate(path)
	if err == nil {
		return &Store{db: db}, nil
	}
	if !isCorrupt(err) {
		return nil, fmt.Errorf("open project database: %w", err)
	}

	timestamp := timeNow().UTC().Format("20060102T150405")
	corruptPath := fmt.Sprintf("%s.corrupt-%s", path, timestamp)
	if renameErr := os.Rename(path, corruptPath); renameErr != nil {
		return nil, fmt.Errorf("move corrupt project database aside: %w", renameErr)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if renameErr := os.Rename(path+suffix, corruptPath+suffix); renameErr != nil && !errors.Is(renameErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("move corrupt project database %s aside: %w", suffix, renameErr)
		}
	}
	db, err = openAndMigrate(path)
	if err != nil {
		return nil, fmt.Errorf("create fresh project database after corruption: %w", err)
	}
	return &Store{
		Notice: fmt.Sprintf("corrupt database moved aside to %s; created a fresh one", filepath.Base(corruptPath)),
		db:     db,
	}, nil
}

func openAndMigrate(path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	dirInfo, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory %q: %w", dir, err)
		}
		// MkdirAll applies the umask, so chmod the directory sk64 just created to
		// guarantee 0700. Pre-existing directories are deliberately left alone:
		// dir is user-controlled via --db, and widening or narrowing a directory
		// the user already owns is not sk64's business.
		if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- 0700 is the intended mode for a directory; the database file itself is 0600.
			return nil, fmt.Errorf("secure database directory %q: %w", dir, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("inspect database directory %q: %w", dir, err)
	} else if !dirInfo.IsDir() {
		return nil, fmt.Errorf("create database directory %q: not a directory", dir)
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o600) // #nosec G304 -- path is the configured database location.
	if err != nil {
		return nil, fmt.Errorf("create database file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close database file %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure database file %q: %w", path, err)
	}

	dsn := (&url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     path,
		RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

// Close closes the database. It is safe to call on a nil Store.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close project database: %w", err)
	}
	return nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	applied := make(map[int]bool)
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close applied migrations: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied migrations: %w", err)
	}

	entries, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		base := filepath.Base(name)
		prefix, _, ok := strings.Cut(base, "_")
		if !ok {
			return fmt.Errorf("parse migration filename %q: missing separator", base)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("parse migration version %q: %w", base, err)
		}
		if applied[version] {
			continue
		}
		contents, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", base, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.Exec(string(contents)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, timeNow().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

func isCorrupt(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlite3.SQLITE_CORRUPT || code == sqlite3.SQLITE_NOTADB
}

func isConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}
