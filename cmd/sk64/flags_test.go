package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"

	"github.com/NoahHakansson/sk64/internal/project"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    options
		wantErr bool
	}{
		{name: "defaults", want: options{scanDepth: project.DefaultMaxDepth, scanMaxFiles: project.DefaultMaxFiles}},
		{name: "ascii", args: []string{"--ascii"}, want: options{ascii: true, scanDepth: project.DefaultMaxDepth, scanMaxFiles: project.DefaultMaxFiles}},
		{
			name: "long options",
			args: []string{"--kubeconfig", "p", "--context", "c", "--namespace", "ns"},
			want: options{kubeconfig: "p", context: "c", namespace: "ns", scanDepth: project.DefaultMaxDepth, scanMaxFiles: project.DefaultMaxFiles},
		},
		{
			name: "namespace alias",
			args: []string{"-n", "ns"},
			want: options{namespace: "ns", scanDepth: project.DefaultMaxDepth, scanMaxFiles: project.DefaultMaxFiles},
		},
		{
			name: "version long",
			args: []string{"--version"},
			want: options{showVersion: true, scanDepth: project.DefaultMaxDepth, scanMaxFiles: project.DefaultMaxFiles},
		},
		{
			name: "version alias",
			args: []string{"-v"},
			want: options{showVersion: true, scanDepth: project.DefaultMaxDepth, scanMaxFiles: project.DefaultMaxFiles},
		},
		{
			name: "scan overrides",
			args: []string{"--scan-depth", "3", "--scan-max-files", "100"},
			want: options{scanDepth: 3, scanMaxFiles: 100},
		},
		{
			name: "debug log",
			args: []string{"--debug-log", "/tmp/sk64.log"},
			want: options{debugLog: "/tmp/sk64.log", scanDepth: project.DefaultMaxDepth, scanMaxFiles: project.DefaultMaxFiles},
		},
		{name: "scan depth zero", args: []string{"--scan-depth", "0"}, wantErr: true},
		{name: "scan max files negative", args: []string{"--scan-max-files", "-1"}, wantErr: true},
		{
			name:    "unknown flag",
			args:    []string{"--unknown"},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseFlags(test.args, &bytes.Buffer{})
			if test.wantErr {
				if err == nil {
					t.Fatal("parseFlags() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags() error = %v", err)
			}
			if *got != test.want {
				t.Fatalf("parseFlags() = %+v, want %+v", *got, test.want)
			}
		})
	}
}

func TestParseFlags_Help(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseFlags([]string{"-h"}, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags() error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(stderr.String(), "usage: sk64 [flags]") {
		t.Fatalf("help output = %q, want usage text", stderr.String())
	}
}

func TestParseFlagsEditorReadOnly(t *testing.T) {
	configured, err := parseFlags([]string{"--editor", "code --wait", "--read-only"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if configured.editor != "code --wait" || !configured.readOnly {
		t.Fatalf("parseFlags() = %+v", configured)
	}
	defaults, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFlags(defaults) error = %v", err)
	}
	if defaults.editor != "" || defaults.readOnly {
		t.Fatalf("parseFlags(defaults) = %+v", defaults)
	}
}

func TestParseNoConfigMaps(t *testing.T) {
	defaults, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := parseFlags([]string{"--no-configmaps"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.noConfigMaps || !configured.noConfigMaps {
		t.Fatalf("noConfigMaps defaults=%t configured=%t", defaults.noConfigMaps, configured.noConfigMaps)
	}
}

func TestParseProjectFlags(t *testing.T) {
	configured, err := parseFlags([]string{"--project", "api", "--db", "/tmp/sk64.db"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseFlags() error = %v", err)
	}
	if configured.project != "api" || configured.db != "/tmp/sk64.db" || configured.noProject {
		t.Fatalf("parseFlags() = %+v", configured)
	}
	configured, err = parseFlags([]string{"--no-project"}, &bytes.Buffer{})
	if err != nil || !configured.noProject {
		t.Fatalf("parseFlags(--no-project) = %+v, %v", configured, err)
	}
	var stderr bytes.Buffer
	if _, err := parseFlags([]string{"--project", "api", "--no-project"}, &stderr); err == nil {
		t.Fatal("parseFlags(mutually exclusive) error = nil")
	}
	if stderr.Len() == 0 {
		t.Fatal("parseFlags(mutually exclusive) stderr is empty")
	}
}

func TestRun_Version(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run([]string{"--version"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("run() = %d, want 0", exitCode)
	}
	if !strings.Contains(stdout.String(), "dev") {
		t.Fatalf("stdout = %q, want version", stdout.String())
	}
}

func TestIsTerminalWriterRejectsBufferedOutput(t *testing.T) {
	if isTerminalWriter(&bytes.Buffer{}) {
		t.Fatal("isTerminalWriter(buffer) = true, want false")
	}
}

func TestReportRuntimeFailure(t *testing.T) {
	t.Run("ordinary error", func(t *testing.T) {
		var stderr bytes.Buffer
		if exitCode := reportRuntimeFailure(&stderr, errors.New("failure")); exitCode != 1 {
			t.Fatalf("reportRuntimeFailure() = %d, want 1", exitCode)
		}
		if got := stderr.String(); got != "sk64: failure\n" {
			t.Fatalf("stderr = %q, want runtime error", got)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		var stderr bytes.Buffer
		if exitCode := reportRuntimeFailure(&stderr, fmt.Errorf("shutdown: %w", context.Canceled)); exitCode != 1 {
			t.Fatalf("reportRuntimeFailure() = %d, want 1", exitCode)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want no cancellation message", stderr.String())
		}
	})

	t.Run("context deadline", func(t *testing.T) {
		var stderr bytes.Buffer
		if exitCode := reportRuntimeFailure(&stderr, fmt.Errorf("probe: %w", context.DeadlineExceeded)); exitCode != 1 {
			t.Fatalf("reportRuntimeFailure() = %d, want 1", exitCode)
		}
		if got := stderr.String(); got != "sk64: probe: context deadline exceeded\n" {
			t.Fatalf("stderr = %q, want deadline message", got)
		}
	})
}
