// Package debuglog appends an opt-in, scrubbed operational log.
//
// The API cannot carry secret values: every exported method takes only names,
// kinds, namespaces, key names, sizes, and opaque error classifications. This
// is the second of the two never-rules and is enforced by novalues_test.go.
package debuglog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"sync"
	"time"
)

const errorTypeLimit = 128

var debugNow = time.Now

// ErrorClass is an opaque, value-free classification produced by
// ClassifyError.
type ErrorClass struct {
	kind     string
	typeName string
}

// ClassifyError reduces err to stable diagnostic metadata without retaining
// its message.
func ClassifyError(err error) ErrorClass {
	kind := "other"
	switch {
	case err == nil:
		kind = "none"
	case errors.Is(err, context.Canceled):
		kind = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		kind = "deadline-exceeded"
	case errors.Is(err, fs.ErrNotExist):
		kind = "not-found"
	case errors.Is(err, fs.ErrPermission):
		kind = "permission"
	default:
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			kind = "timeout"
		}
	}
	typeName := "<nil>"
	if err != nil {
		typeName = reflect.TypeOf(err).String()
	}
	if len(typeName) > errorTypeLimit {
		typeName = typeName[:errorTypeLimit]
	}
	return ErrorClass{kind: kind, typeName: typeName}
}

// Logger appends scrubbed records to a file. A nil *Logger is a no-op, so call
// sites never branch. Safe for concurrent use.
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	writeErr error
}

// Open opens or creates path for appending with mode 0600.
func Open(path string) (*Logger, error) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- path is the user-configured log destination.
	if err != nil {
		return nil, fmt.Errorf("open debug log %q: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure debug log %q: %w", path, err)
	}
	return &Logger{file: file}, nil
}

// Close closes the log file. Safe on a nil Logger.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var closeErr error
	if l.file != nil {
		closeErr = l.file.Close()
		l.file = nil
	}
	return errors.Join(l.writeErr, closeErr)
}

// Op records a process-level event, e.g. "startup".
func (l *Logger) Op(name string) {
	if l == nil {
		return
	}
	l.write("op=%s", name)
}

// Resource records an operation on one Secret or ConfigMap or workload.
func (l *Logger) Resource(op, kind, namespace, name string) {
	if l == nil {
		return
	}
	l.write("op=%s kind=%s ns=%s name=%s", op, kind, namespace, name)
}

// Key records an operation on one key, recording its size in bytes, never its
// contents.
func (l *Logger) Key(op, kind, namespace, name, key string, size int) {
	if l == nil {
		return
	}
	l.write("op=%s kind=%s ns=%s name=%s key=%s size=%d", op, kind, namespace, name, key, size)
}

// Count records an operation over a scope and the number of items it touched.
func (l *Logger) Count(op, scope string, n int) {
	if l == nil {
		return
	}
	l.write("op=%s scope=%s count=%d", op, scope, n)
}

// Err records a value-free classification of a failed operation.
func (l *Logger) Err(op string, class ErrorClass) {
	if l == nil {
		return
	}
	if class.kind == "" {
		class.kind = "unknown"
	}
	if class.typeName == "" {
		class.typeName = "unknown"
	}
	l.write("op=%s error_kind=%s error_type=%q", op, class.kind, class.typeName)
}

func (l *Logger) write(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	timestamp := debugNow().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(l.file, timestamp+" "+format+"\n", args...); err != nil && l.writeErr == nil {
		l.writeErr = err
	}
}
