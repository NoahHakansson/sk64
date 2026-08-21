package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDefaultPath(t *testing.T) {
	t.Run("XDG data home", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		got, err := DefaultPath()
		if err != nil {
			t.Fatalf("DefaultPath() error = %v", err)
		}
		if want := filepath.Join(xdg, "sk64", "sk64.db"); got != want {
			t.Fatalf("DefaultPath() = %q, want %q", got, want)
		}
	})
	t.Run("home fallback", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", home)
		got, err := DefaultPath()
		if err != nil {
			t.Fatalf("DefaultPath() error = %v", err)
		}
		if want := filepath.Join(home, ".local", "share", "sk64", "sk64.db"); got != want {
			t.Fatalf("DefaultPath() = %q, want %q", got, want)
		}
	})
	t.Run("home unavailable", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", "")
		if _, err := DefaultPath(); err == nil {
			t.Fatal("DefaultPath() error = nil without XDG_DATA_HOME or HOME")
		}
	})
}

func TestOpenCreatesSecureFiles(t *testing.T) {
	if err := (*Store)(nil).Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "data", "sk64.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
	var journalMode string
	var foreignKeys int
	if err := st.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal mode: %v", err)
	}
	if err := st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign keys: %v", err)
	}
	if journalMode != "wal" || foreignKeys != 1 {
		t.Fatalf("pragmas = journal_mode %q foreign_keys %d", journalMode, foreignKeys)
	}
}

func TestOpenPreservesExistingDirectoryMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { // #nosec G302 -- the test verifies that existing directory permissions are preserved.
		t.Fatalf("Chmod() error = %v", err)
	}
	path := filepath.Join(dir, "sk64.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	assertMode(t, dir, 0o750)
	assertMode(t, path, 0o600)
}

func TestOpenCreatesDirectory0700UnderRestrictiveUmask(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	path := filepath.Join(t.TempDir(), "data", "sk64.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// MkdirAll applies the umask, so without an explicit chmod the directory
	// would land at 0700 &^ umask and the store would be unusable.
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestOpenPathContainingPercent(t *testing.T) {
	root := t.TempDir()
	literalDir := filepath.Join(root, "back%75p")
	decodedDir := filepath.Join(root, "backup")
	if err := os.Mkdir(decodedDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	path := filepath.Join(literalDir, "sk64.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateProject(t.Context(), validMeta("literal", "/literal")); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(decodedDir, "sk64.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("decoded database path exists or cannot be inspected: %v", err)
	}
	assertMode(t, path, 0o600)
}

func TestOpenRejectsUnusableLocation(t *testing.T) {
	tests := []struct {
		name      string
		path      func(*testing.T) string
		wantError string
	}{
		{
			name: "parent is a regular file",
			path: func(t *testing.T) string {
				parent := filepath.Join(t.TempDir(), "parent")
				if err := os.WriteFile(parent, nil, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return filepath.Join(parent, "sk64.db")
			},
			wantError: "create database directory",
		},
		{
			name: "database path is a symlink loop",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "sk64.db")
				if err := os.Symlink(path, path); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
				return path
			},
			wantError: "create database file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, err := Open(test.path(t))
			if st != nil || err == nil {
				if st != nil {
					_ = st.Close()
				}
				t.Fatalf("Open() = %#v, %v, want nil store and error", st, err)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Open() error = %q, want %q", err, test.wantError)
			}
		})
	}
}

func TestCloseIsSafe(t *testing.T) {
	if err := (*Store)(nil).Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
	if err := (&Store{}).Close(); err != nil {
		t.Fatalf("zero Close() error = %v", err)
	}
	st := openTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sk64.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Errorf("deferred close second database: %v", err)
		}
	}()
	var count int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 2 {
		t.Fatalf("migration count = %d, want 2", count)
	}
	for _, table := range []string{"projects", "project_namespaces", "project_workloads", "project_resources", "ui_state"} {
		if err := second.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("find table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
	if err := second.db.Close(); err != nil {
		t.Fatalf("close second database: %v", err)
	}
	if err := migrate(second.db); err == nil {
		t.Fatal("migrate(closed) error = nil")
	}
}

func TestContextIdentityMigrationPreservesLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sk64.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	initialSchema, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read initial migration: %v", err)
	}
	if _, err := db.Exec(string(initialSchema)); err != nil {
		_ = db.Close()
		t.Fatalf("apply initial migration: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create migration table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2026-01-01T00:00:00Z')`); err != nil {
		_ = db.Close()
		t.Fatalf("record initial migration: %v", err)
	}
	result, err := db.Exec(`INSERT INTO projects(name, root_path, kube_context, namespace, created_at, updated_at) VALUES ('legacy', '/legacy', 'ctx', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy project: %v", err)
	}
	projectID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatalf("read legacy project id: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO project_workloads(project_id, kind, namespace, name) VALUES (?, 'Deployment', 'default', 'web')`, projectID); err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy workload link: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO project_resources(project_id, kind, namespace, name, source) VALUES (?, 'Secret', 'default', 'credentials', 'manual')`, projectID); err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy resource link: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project, err := st.ProjectByName(t.Context(), "legacy")
	if err != nil {
		t.Fatalf("ProjectByName() error = %v", err)
	}
	workloads, err := st.WorkloadLinks(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("WorkloadLinks() error = %v", err)
	}
	resources, err := st.ResourceLinks(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("ResourceLinks() error = %v", err)
	}
	if project.KubeServer != "" || project.SwitchPromptSuppressed {
		t.Fatalf("legacy project identity = server %q suppressed %t", project.KubeServer, project.SwitchPromptSuppressed)
	}
	if len(workloads) != 1 || workloads[0].OriginContext != "" || workloads[0].OriginServer != "" {
		t.Fatalf("legacy workload links = %+v", workloads)
	}
	if len(resources) != 1 || resources[0].OriginContext != "" || resources[0].OriginServer != "" {
		t.Fatalf("legacy resource links = %+v", resources)
	}
}

func TestMigrateRejectsInvalidAppliedVersion(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop migration table: %v", err)
	}
	if _, err := st.db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("recreate migration table: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES ('invalid', 'now')`); err != nil {
		t.Fatalf("corrupt migration version: %v", err)
	}
	if err := migrate(st.db); err == nil || !strings.Contains(err.Error(), "scan applied migration") {
		t.Fatalf("migrate() error = %v, want scan applied migration", err)
	}
}

func TestCorruptDBRecovery(t *testing.T) {
	oldNow := timeNow
	fixed := time.Date(2026, 7, 22, 10, 11, 12, 0, time.UTC)
	timeNow = func() time.Time { return fixed }
	t.Cleanup(func() { timeNow = oldNow })
	dir := t.TempDir()
	path := filepath.Join(dir, "sk64.db")
	garbage := []byte("not a sqlite database")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}
	if err := os.WriteFile(path+"-wal", []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale wal: %v", err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if st.Notice == "" {
		t.Fatal("Open() Notice is empty")
	}
	corruptPath := path + ".corrupt-20260722T101112"
	got, err := os.ReadFile(corruptPath) // #nosec G304 -- path is derived from this test's private temporary directory.
	if err != nil {
		t.Fatalf("read moved database: %v", err)
	}
	if string(got) != string(garbage) {
		t.Fatalf("moved database = %q, want %q", got, garbage)
	}
	if _, err := st.CreateProject(context.Background(), validMeta("one", "/one")); err != nil {
		t.Fatalf("CreateProject() after recovery error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("original WAL still exists after recovery: %v", err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("close reopened database: %v", err)
		}
	}()
	if st.Notice != "" {
		t.Fatalf("second Open() Notice = %q, want empty", st.Notice)
	}
	blockedPath := filepath.Join(t.TempDir(), "blocked.db")
	if err := os.WriteFile(blockedPath, garbage, 0o600); err != nil {
		t.Fatalf("write second corrupt database: %v", err)
	}
	if err := os.Mkdir(blockedPath+".corrupt-20260722T101112", 0o700); err != nil {
		t.Fatalf("block corrupt destination: %v", err)
	}
	if _, err := Open(blockedPath); err == nil {
		t.Fatal("Open() error = nil when corrupt database cannot be moved aside")
	}
}

func TestOpenNonCorruptErrorIsFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sk64.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (id INTEGER PRIMARY KEY)`); err != nil {
		_ = db.Close()
		t.Fatalf("create colliding projects table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}
	st, err := Open(path)
	if st != nil {
		_ = st.Close()
	}
	if err == nil || st != nil {
		t.Fatalf("Open() = %#v, %v, want nil store and error", st, err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir() error = %v", readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), filepath.Base(path)+".corrupt-") {
			t.Fatalf("unexpected corrupt rename %q", entry.Name())
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %q = %o, want %o", path, got, want)
	}
}

func validMeta(name, root string) ProjectMeta {
	return ProjectMeta{Name: name, RootPath: root, KubeContext: "ctx", Namespace: "default"}
}
