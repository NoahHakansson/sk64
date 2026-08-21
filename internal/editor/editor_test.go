package editor

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildArgv(t *testing.T) {
	path := "/tmp/value"
	tests := []struct {
		name      string
		flag, env string
		want      []string
		wantErr   bool
	}{
		{name: "environment with args", env: "code --wait", want: []string{"code", "--wait", path}},
		{name: "flag wins", flag: "nano", env: "code --wait", want: []string{"nano", path}},
		{name: "single word", env: "nano", want: []string{"nano", path}},
		{name: "quoted argument", env: `myed --title "my file"`, want: []string{"myed", "--title", "my file", path}},
		{name: "unclosed quote", env: `code "`, wantErr: true},
		{name: "default", want: appendVimFlags("vim", path)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildArgv(test.flag, test.env, path)
			if test.wantErr {
				if err == nil {
					t.Fatal("BuildArgv() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildArgv() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("BuildArgv() = %v, want %v", got, test.want)
			}
		})
	}
}

func appendVimFlags(command, path string) []string {
	if command == "vim" {
		return []string{command, "-n", "-i", "NONE", path}
	}
	return []string{command, path}
}

func TestBuildArgvVimFlags(t *testing.T) {
	for _, command := range []string{"vim", "nvim", "/usr/bin/nvim", "vi", "neovim-qt", "code"} {
		t.Run(command, func(t *testing.T) {
			got, err := BuildArgv(command, "", "value.txt")
			if err != nil {
				t.Fatalf("BuildArgv() error = %v", err)
			}
			wantFlags := command == "vim" || command == "nvim" || command == "/usr/bin/nvim" || command == "vi"
			hasFlags := len(got) >= 5 && reflect.DeepEqual(got[len(got)-4:], []string{"-n", "-i", "NONE", "value.txt"})
			if hasFlags != wantFlags {
				t.Fatalf("BuildArgv(%q) = %v, vim flags = %t", command, got, hasFlags)
			}
		})
	}
}

func TestNormalizeEditedValue(t *testing.T) {
	tests := []struct {
		name             string
		original, edited string
		want             string
	}{
		{name: "unchanged plus newline", original: "value", edited: "value\n", want: "value"},
		{name: "changed plus newline", original: "old", edited: "new\n", want: "new"},
		{name: "original newline", original: "old\n", edited: "new\n", want: "new\n"},
		{name: "empty edited", original: "old", edited: "", want: ""},
		{name: "two newlines", original: "old", edited: "new\n\n", want: "new\n"},
		{name: "original without newline", original: "value", edited: "value\n", want: "value"},
		{name: "original ending newline keeps both", original: "value\n", edited: "value\n\n", want: "value\n\n"},
		{name: "empty plus newline", original: "", edited: "\n", want: ""},
		{name: "only one newline removed", original: "a", edited: "a\n\n", want: "a\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(NormalizeEditedValue([]byte(test.original), []byte(test.edited))); got != test.want {
				t.Fatalf("NormalizeEditedValue() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	fingerprint := FingerprintBytes([]byte("same"))
	if fingerprint != FingerprintBytes([]byte("same")) {
		t.Fatal("equal content has different fingerprints")
	}
	if fingerprint == FingerprintBytes([]byte("different")) {
		t.Fatal("different content has the same fingerprint")
	}
}

func TestSessionStderrCapture(t *testing.T) {
	if _, err := NewSession(nil); err == nil {
		t.Fatal("NewSession(nil) error = nil, want non-nil")
	}
	session, err := NewSession([]string{"sh", "-c", "echo oops >&2; exit 3"})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if session.cmd.WaitDelay != 2*time.Second {
		t.Fatalf("Session WaitDelay = %v, want 2s", session.cmd.WaitDelay)
	}
	session.SetStdin(strings.NewReader(""))
	session.SetStdout(io.Discard)
	session.SetStderr(io.Discard)
	if err := session.Run(); err == nil {
		t.Fatal("Session.Run() error = nil, want non-nil")
	}
	if !strings.Contains(session.Stderr(), "oops") {
		t.Fatalf("Session.Stderr() = %q, want oops", session.Stderr())
	}

	var stderr boundedBuffer
	if _, err := stderr.Write(make([]byte, stderrLimit+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	if len(stderr.String()) != stderrLimit || !strings.HasSuffix(stderr.String(), "tail") {
		t.Fatalf("bounded stderr length/suffix = %d/%q", len(stderr.String()), stderr.String()[stderrLimit-4:])
	}
}
