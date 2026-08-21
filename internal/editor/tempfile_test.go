package editor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickParent(t *testing.T) {
	tests := []struct {
		name   string
		xdg    string
		usable map[string]bool
		want   string
	}{
		{name: "shared memory", xdg: "/run/user/1", usable: map[string]bool{"/dev/shm": true, "/run/user/1": true}, want: "/dev/shm"},
		{name: "runtime directory", xdg: "/run/user/1", usable: map[string]bool{"/run/user/1": true}, want: "/run/user/1"},
		{name: "empty runtime skipped", usable: map[string]bool{"": true}, want: os.TempDir()},
		{name: "fallback", xdg: "/bad", want: os.TempDir()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := PickParent(func(string) string { return test.xdg }, func(path string) bool { return test.usable[path] })
			if got != test.want {
				t.Fatalf("PickParent() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNewDir(t *testing.T) {
	dir, err := NewDir()
	if err != nil {
		t.Fatalf("NewDir() error = %v", err)
	}
	t.Cleanup(CleanupAll)
	info, err := os.Lstat(dir.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("NewDir() mode = %v, want a 0700 directory", info.Mode())
	}
	dir.Cleanup()
	if _, err := os.Stat(dir.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("NewDir() cleanup error = %v, want not exist", err)
	}
}

func TestUsableDir(t *testing.T) {
	parent := t.TempDir()
	writable := filepath.Join(parent, "writable")
	readOnly := filepath.Join(parent, "read-only")
	regularFile := filepath.Join(parent, "file")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regularFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "writable directory", path: writable, want: true},
		{name: "read-only directory", path: readOnly},
		{name: "regular file", path: regularFile},
		{name: "missing", path: filepath.Join(parent, "missing")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := usableDir(test.path); got != test.want {
				t.Fatalf("usableDir(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}

func TestNewDirInVerifies(t *testing.T) {
	dir, err := newDirIn(t.TempDir())
	if err != nil {
		t.Fatalf("newDirIn() error = %v", err)
	}
	info, err := os.Lstat(dir.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("temp dir mode = %v", info.Mode())
	}
	registryMu.Lock()
	_, registered := registry[dir]
	registryMu.Unlock()
	if !registered {
		t.Fatal("temp dir is not registered")
	}
	dir.Cleanup()
	if _, err := os.Stat(dir.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp dir still exists: %v", err)
	}
	dir.Cleanup()
}

func TestVerifyPrivateDirRejects(t *testing.T) {
	parent := t.TempDir()
	publicDir := filepath.Join(parent, "public")
	if err := os.Mkdir(publicDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(publicDir, 0o755); err != nil { // #nosec G302 -- the public mode is the rejection condition under test.
		t.Fatal(err)
	}
	regularFile := filepath.Join(parent, "file")
	if err := os.WriteFile(regularFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(parent, "link")
	if err := os.Symlink(t.TempDir(), symlink); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "0755 directory", path: publicDir},
		{name: "symlink", path: symlink},
		{name: "regular file", path: regularFile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyPrivateDir(test.path); err == nil {
				t.Fatalf("verifyPrivateDir(%q) error = nil", test.path)
			}
		})
	}
}

func TestWriteFileNaming(t *testing.T) {
	tests := []struct {
		kind, name, key string
		wantName        string
	}{
		{kind: "Secret", name: "db-creds", key: "DB_PASSWORD", wantName: "secret-db-creds-db_password.txt"},
		{kind: "ConfigMap", name: "app", key: "config.yaml", wantName: "configmap-app-config.yaml"},
		{kind: "Secret", name: "app", key: "weird/key name!", wantName: "secret-app-weird_key_name_.txt"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			dir, err := newDirIn(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Cleanup()
			path, err := dir.WriteFile(test.kind, test.name, test.key, []byte("content"))
			if err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if filepath.Base(path) != test.wantName || strings.ContainsAny(filepath.Base(path), "/ ") {
				t.Fatalf("WriteFile() name = %q, want %q", filepath.Base(path), test.wantName)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
			}
			got, err := os.ReadFile(path) // #nosec G304 -- path was created by the test's private temp directory.
			if err != nil || string(got) != "content" {
				t.Fatalf("file content = %q, err = %v", got, err)
			}
		})
	}
}

func TestWriteFileNameIsBounded(t *testing.T) {
	tests := []struct {
		name       string
		resource   string
		key        string
		wantSuffix string
	}{
		{name: "long key", resource: "db-creds", key: strings.Repeat("a", 253), wantSuffix: ".txt"},
		{name: "long resource", resource: strings.Repeat("b", 253), key: "resource.yaml", wantSuffix: ".yaml"},
		{name: "long resource and key", resource: strings.Repeat("b", 253), key: strings.Repeat("a", 253), wantSuffix: ".txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, err := newDirIn(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Cleanup()

			path, err := dir.WriteFile("Secret", test.resource, test.key, []byte("content"))
			if err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			base := filepath.Base(path)
			if len(base) > maxTempFileNameBytes {
				t.Fatalf("WriteFile() basename length = %d, want <= %d", len(base), maxTempFileNameBytes)
			}
			if !strings.HasSuffix(base, test.wantSuffix) {
				t.Fatalf("WriteFile() basename = %q, want suffix %q", base, test.wantSuffix)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat WriteFile() path: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("WriteFile() mode = %o, want 600", info.Mode().Perm())
			}
		})
	}
}

func TestCleanupAll(t *testing.T) {
	first, err := newDirIn(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := newDirIn(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	CleanupAll()
	for _, dir := range []*Dir{first, second} {
		if _, err := os.Stat(dir.Path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("directory %q still exists: %v", dir.Path, err)
		}
	}
	CleanupAll()
}
