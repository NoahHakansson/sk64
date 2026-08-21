package debuglog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenAppendsAndEnforces0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	if err := os.WriteFile(path, []byte("existing\n"), 0o644); err != nil { // #nosec G306 -- the test verifies Open tightens an existing permissive file.
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // #nosec G302 -- the test deliberately establishes the insecure precondition.
		t.Fatalf("Chmod() error = %v", err)
	}
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	first.Op("first")
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	second.Op("second")
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- path is confined to the test's temporary directory.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(contents), "existing") || !strings.Contains(string(contents), "op=first") || !strings.Contains(string(contents), "op=second") {
		t.Fatalf("log = %q, want existing content and both appended records", contents)
	}
}

func TestCloseReportsWriteFailure(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	logger := &Logger{file: reader}
	logger.Op("cannot-write-to-read-end")
	if err := logger.Close(); err == nil {
		t.Fatal("Close() error = nil after write failure")
	}
}

func TestNilLoggerIsNoOp(t *testing.T) {
	var logger *Logger
	logger.Op("startup")
	logger.Resource("open", "Secret", "ns", "name")
	logger.Key("edit", "Secret", "ns", "name", "key", 12)
	logger.Count("scan", "/repo", 3)
	logger.Err("probe", ClassifyError(errors.New("failed")))
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestRecordFormats(t *testing.T) {
	oldNow := debugNow
	debugNow = func() time.Time { return time.Date(2026, 7, 26, 20, 43, 1, 0, time.FixedZone("test", 2*60*60)) }
	t.Cleanup(func() { debugNow = oldNow })

	path := filepath.Join(t.TempDir(), "debug.log")
	logger, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	tests := []struct {
		name string
		call func()
		want string
	}{
		{name: "operation", call: func() { logger.Op("startup") }, want: "op=startup"},
		{name: "resource", call: func() { logger.Resource("open-keys", "Secret", "production", "app-creds") }, want: "op=open-keys kind=Secret ns=production name=app-creds"},
		{name: "key", call: func() { logger.Key("edit", "Secret", "production", "app-creds", "DB_PASSWORD", 17) }, want: "op=edit kind=Secret ns=production name=app-creds key=DB_PASSWORD size=17"},
		{name: "count", call: func() { logger.Count("list-secrets", "production", 12) }, want: "op=list-secrets scope=production count=12"},
		{name: "error", call: func() { logger.Err("save", ClassifyError(errors.New(`secrets "app-creds" is forbidden`))) }, want: `op=save error_kind=other error_type="*errors.errorString"`},
	}
	for _, test := range tests {
		test.call()
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- path is confined to the test's temporary directory.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != len(tests) {
		t.Fatalf("line count = %d, want %d: %q", len(lines), len(tests), contents)
	}
	for i, test := range tests {
		if want := "2026-07-26T18:43:01Z " + test.want; lines[i] != want {
			t.Errorf("%s line = %q, want %q", test.name, lines[i], want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", want: "none"},
		{name: "canceled", err: fmt.Errorf("wrapped: %w", context.Canceled), want: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "deadline-exceeded"},
		{name: "not found", err: fs.ErrNotExist, want: "not-found"},
		{name: "permission", err: fs.ErrPermission, want: "permission"},
		{name: "timeout", err: debugTimeoutError{}, want: "timeout"},
		{name: "other", err: errors.New("details"), want: "other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyError(test.err); got.kind != test.want {
				t.Fatalf("ClassifyError() kind = %q, want %q", got.kind, test.want)
			}
		})
	}
}

func TestValueBearingErrorIsNotLogged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	logger, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	const secretValue = "super-secret-edited-value"
	logger.Err("parse", ClassifyError(errors.New(secretValue)))
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- path is confined to the test's temporary directory.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(contents), secretValue) {
		t.Fatalf("debug log contains value-bearing error: %q", contents)
	}
	if !strings.Contains(string(contents), `error_kind=other error_type="*errors.errorString"`) {
		t.Fatalf("debug log classification = %q", contents)
	}
}

type debugTimeoutError struct{}

func (debugTimeoutError) Error() string { return "value-bearing timeout details" }
func (debugTimeoutError) Timeout() bool { return true }
