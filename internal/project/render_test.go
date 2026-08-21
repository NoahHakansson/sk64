package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeStub(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- the owner must be able to execute the private test stub.
		t.Fatalf("make stub executable: %v", err)
	}
	return path
}

func TestToolArgv(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "kustomize", got: kustomizeArgv("/tools/kustomize", "/tools/kubectl"), want: []string{"/tools/kustomize", "build", "."}},
		{name: "kubectl", got: kustomizeArgv("", "/tools/kubectl"), want: []string{"/tools/kubectl", "kustomize", "."}},
		{name: "helm defaults", got: helmArgv("/tools/helm", nil), want: []string{"/tools/helm", "template", "."}},
		{name: "helm values", got: helmArgv("/tools/helm", []string{"z.yaml", "a.yml"}), want: []string{"/tools/helm", "template", ".", "--values", "a.yml", "--values", "z.yaml"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !reflect.DeepEqual(test.got, test.want) {
				t.Fatalf("argv = %q, want %q", test.got, test.want)
			}
			for _, arg := range test.got {
				if arg == "install" || strings.HasPrefix(arg, "--repo") || strings.HasPrefix(arg, "--dependency-update") {
					t.Fatalf("unsafe argv element %q", arg)
				}
			}
		})
	}
}

func TestExecRunnerRunsInDirWithArgs(t *testing.T) {
	binDir := t.TempDir()
	runDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "record")
	t.Setenv("STUB_OUT", outPath)
	stub := writeStub(t, binDir, "record", `printf '%s\n' "$PWD" "$@" > "$STUB_OUT"
printf rendered`)
	output, _, err := (execToolRunner{timeout: 5 * time.Second}).Run(t.Context(), runDir, []string{stub, "one", "two words"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(output) != "rendered" {
		t.Fatalf("Run() output = %q", output)
	}
	record, err := os.ReadFile(outPath) // #nosec G304 -- outPath is inside the test's private temporary directory.
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	want := runDir + "\none\ntwo words\n"
	if string(record) != want {
		t.Fatalf("record = %q, want %q", record, want)
	}
}

func TestExecRunnerTimeout(t *testing.T) {
	stub := writeStub(t, t.TempDir(), "hang", "/bin/sleep 30")
	started := time.Now()
	_, _, err := (execToolRunner{timeout: 100 * time.Millisecond}).Run(context.Background(), t.TempDir(), []string{stub})
	if !errors.Is(err, errRenderTimeout) {
		t.Fatalf("Run() error = %v, want errRenderTimeout", err)
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("Run() took %s", elapsed)
	}
}

func TestExecRunnerFailure(t *testing.T) {
	stub := writeStub(t, t.TempDir(), "fail", "echo boom >&2\nexit 1")
	_, stderrLine, err := (execToolRunner{timeout: 5 * time.Second}).Run(t.Context(), t.TempDir(), []string{stub})
	if err == nil || errors.Is(err, errRenderTimeout) || stderrLine != "boom" {
		t.Fatalf("Run() stderr = %q, error = %v", stderrLine, err)
	}
}
