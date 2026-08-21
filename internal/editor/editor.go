package editor

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kballard/go-shellquote"
)

const stderrLimit = 4 * 1024

// BuildArgv resolves the configured editor command and appends path.
func BuildArgv(editorFlag, editorEnv, path string) ([]string, error) {
	command := editorFlag
	if command == "" {
		command = editorEnv
	}
	if command == "" {
		command = "vim"
	}

	argv, err := shellquote.Split(command)
	if err != nil {
		return nil, fmt.Errorf("parse editor command %q: %w", command, err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("parse editor command %q: command is empty", command)
	}
	switch filepath.Base(argv[0]) {
	case "vim", "nvim", "vi":
		argv = append(argv, "-n", "-i", "NONE")
	}
	return append(argv, path), nil
}

// Session runs an editor and retains a bounded copy of its stderr.
// It implements Bubble Tea's ExecCommand interface.
type Session struct {
	cmd    *exec.Cmd
	stderr boundedBuffer
}

// NewSession creates an editor session after resolving its executable.
func NewSession(argv []string) (*Session, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("create editor session: empty argument list")
	}
	executable, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("find editor %q: %w", argv[0], err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve editor %q: %w", argv[0], err)
	}
	// #nosec G204 -- the executable is resolved with LookPath and arguments are passed without a shell.
	cmd := exec.Command(executable, argv[1:]...)
	cmd.WaitDelay = 2 * time.Second
	return &Session{cmd: cmd}, nil
}

// Run executes the configured editor and waits for it to exit.
func (s *Session) Run() error { return s.cmd.Run() }

// SetStdin connects the editor's standard input.
func (s *Session) SetStdin(r io.Reader) { s.cmd.Stdin = r }

// SetStdout connects the editor's standard output.
func (s *Session) SetStdout(w io.Writer) { s.cmd.Stdout = w }

// SetStderr connects the editor's standard error and retains a bounded copy.
func (s *Session) SetStderr(w io.Writer) { s.cmd.Stderr = io.MultiWriter(w, &s.stderr) }

// Stderr returns the retained standard error without surrounding whitespace.
func (s *Session) Stderr() string { return strings.TrimSpace(s.stderr.String()) }

type boundedBuffer struct {
	data []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) >= stderrLimit {
		b.data = append(b.data[:0], p[len(p)-stderrLimit:]...)
		return len(p), nil
	}
	overflow := len(b.data) + len(p) - stderrLimit
	if overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.data) }

// Fingerprint identifies file content for no-op detection.
type Fingerprint struct {
	Size int64
	Sum  [32]byte
}

// FingerprintBytes fingerprints data in memory.
func FingerprintBytes(data []byte) Fingerprint {
	return Fingerprint{Size: int64(len(data)), Sum: sha256.Sum256(data)}
}

// NormalizeEditedValue removes one editor-added trailing newline when the original had none.
func NormalizeEditedValue(original, edited []byte) []byte {
	if len(edited) == 0 || bytes.HasSuffix(original, []byte{'\n'}) || !bytes.HasSuffix(edited, []byte{'\n'}) {
		return edited
	}
	return edited[:len(edited)-1]
}
