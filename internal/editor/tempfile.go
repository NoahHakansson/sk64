package editor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

var (
	extensionPattern = regexp.MustCompile(`\.[A-Za-z0-9]{1,10}$`)
	registryMu       sync.Mutex
	registry         = make(map[*Dir]struct{})
)

const maxTempFileNameBytes = 255

// Dir is a registered per-invocation directory for one plaintext file.
type Dir struct {
	Path     string
	filePath string
}

// NewDir creates and verifies a private temporary directory.
func NewDir() (*Dir, error) {
	return newDirIn(PickParent(os.Getenv, usableDir))
}

func newDirIn(parent string) (*Dir, error) {
	path, err := os.MkdirTemp(parent, "sk64-*")
	if err != nil {
		return nil, fmt.Errorf("create editor temp directory in %q: %w", parent, err)
	}
	cleanup := func() { _ = os.RemoveAll(path) }
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- directories require execute permission and are intentionally private.
		cleanup()
		return nil, fmt.Errorf("secure editor temp directory %q: %w", path, err)
	}
	if err := verifyPrivateDir(path); err != nil {
		cleanup()
		return nil, err
	}

	dir := &Dir{Path: path}
	registryMu.Lock()
	registry[dir] = struct{}{}
	registryMu.Unlock()
	return dir, nil
}

func verifyPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("verify editor temp directory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("verify editor temp directory %q: expected a real 0700 directory", path)
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("verify editor temp directory %q: owner mismatch", path)
	}
	return nil
}

// PickParent selects the first usable runtime-memory or runtime directory, then the system temp directory.
func PickParent(getenv func(string) string, usable func(path string) bool) string {
	if usable("/dev/shm") {
		return "/dev/shm"
	}
	if runtimeDir := getenv("XDG_RUNTIME_DIR"); runtimeDir != "" && usable(runtimeDir) {
		return runtimeDir
	}
	return os.TempDir()
}

func usableDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return writableByCurrentUser(info)
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || int64(stat.Uid) == int64(os.Getuid())
}

func writableByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) == int64(os.Getuid()) {
		return info.Mode().Perm()&0o200 != 0
	}
	return info.Mode().Perm()&0o002 != 0
}

// WriteFile writes content to the directory's single private file.
func (d *Dir) WriteFile(kind, name, key string, content []byte) (string, error) {
	if d.filePath == "" {
		filename := boundedFilename(sanitizeFilename(strings.Join([]string{kind, name, key}, "-")))
		d.filePath = filepath.Join(d.Path, filename)
	}
	if err := os.WriteFile(d.filePath, content, 0o600); err != nil {
		return "", fmt.Errorf("write editor temp file %q: %w", d.filePath, err)
	}
	if err := os.Chmod(d.filePath, 0o600); err != nil {
		return "", fmt.Errorf("secure editor temp file %q: %w", d.filePath, err)
	}
	return d.filePath, nil
}

func boundedFilename(sanitized string) string {
	extension := extensionPattern.FindString(sanitized)
	stem := strings.TrimSuffix(sanitized, extension)
	if extension == "" {
		extension = ".txt"
	}
	if len(stem)+len(extension) > maxTempFileNameBytes {
		stem = stem[:maxTempFileNameBytes-len(extension)]
	}
	return stem + extension
}

func sanitizeFilename(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, value)
}

// Cleanup symbolically zeroes the file, removes the directory, and deregisters it.
func (d *Dir) Cleanup() {
	if d == nil {
		return
	}
	registryMu.Lock()
	_, registered := registry[d]
	delete(registry, d)
	registryMu.Unlock()
	if !registered {
		return
	}
	if d.filePath != "" {
		// The zero pass is symbolic on tmpfs and copy-on-write filesystems; the private 0700 directory is the real mitigation.
		if info, err := os.Stat(d.filePath); err == nil {
			if file, err := os.OpenFile(d.filePath, os.O_WRONLY, 0); err == nil {
				_, _ = file.Write(make([]byte, info.Size()))
				_ = file.Sync()
				_ = file.Close()
			}
		}
	}
	_ = os.RemoveAll(d.Path)
}

// CleanupAll cleans every live editor directory.
func CleanupAll() {
	registryMu.Lock()
	dirs := make([]*Dir, 0, len(registry))
	for dir := range registry {
		dirs = append(dirs, dir)
	}
	registryMu.Unlock()
	for _, dir := range dirs {
		dir.Cleanup()
	}
}
