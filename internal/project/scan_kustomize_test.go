package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const renderedKustomizeYAML = `printf '%s\n' 'apiVersion: apps/v1' 'kind: Deployment' 'metadata:' '  name: web' '  namespace: production' 'spec:' '  template:' '    spec:' '      containers:' '        - name: web' '          image: example.invalid/web' '          envFrom:' '            - secretRef:' '                name: app-secrets-abc123' '            - configMapRef:' '                name: app-config-xyz789'`

func TestKustomizeRendered(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	stub := writeStub(t, binDir, "kustomize", renderedKustomizeYAML)
	got, notes, incomplete := extractKustomize(t.Context(), filepath.Join("testdata", "repo_kustomize"), "", kustomizeArgv(stub, ""), defaultNoteSeparator, execToolRunner{timeout: 5 * time.Second})
	assertSuggestion(t, got, "Deployment", "web", false, ModeRendered, "kustomize")
	assertSuggestion(t, got, "Secret", "app-secrets", true, ModeRendered, "kustomize")
	assertSuggestion(t, got, "ConfigMap", "app-config", true, ModeRendered, "kustomize")
	assertSuggestion(t, got, KindNamespace, "production", false, ModeRendered, "kustomize")
	if len(notes) != 0 || incomplete {
		t.Fatalf("notes = %q, incomplete = %t; want complete with no notes", notes, incomplete)
	}
}

func TestKustomizeKubectlFallback(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	stub := writeStub(t, binDir, "kubectl", `test "$1" = kustomize && test "$2" = . || exit 2
`+renderedKustomizeYAML)
	tool, ok := findTool("kustomize", "kubectl")
	if !ok || tool != stub {
		t.Fatalf("findTool() = %q, %t, want %q", tool, ok, stub)
	}
	got, _, _ := extractKustomize(t.Context(), filepath.Join("testdata", "repo_kustomize"), "", kustomizeArgv("", tool), defaultNoteSeparator, execToolRunner{timeout: 5 * time.Second})
	assertSuggestion(t, got, "Deployment", "web", false, ModeRendered, "kustomize")
}

func TestKustomizeLiteralNoTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got, notes, incomplete := extractKustomize(t.Context(), filepath.Join("testdata", "repo_kustomize"), "", nil, defaultNoteSeparator, execToolRunner{timeout: time.Second})
	assertSuggestion(t, got, KindNamespace, "production", false, ModeLiteral, "kustomize not on PATH")
	assertSuggestion(t, got, "Secret", "app-secrets", true, ModeLiteral, "kustomize not on PATH")
	assertSuggestion(t, got, "ConfigMap", "app-config", true, ModeLiteral, "kustomize not on PATH")
	if len(notes) != 1 || !strings.Contains(notes[0], "not on PATH") || !incomplete {
		t.Fatalf("notes = %q, incomplete = %t", notes, incomplete)
	}
}

func TestKustomizeRenderFailureFallsBack(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		detail  string
		timeout time.Duration
	}{
		{name: "failure", script: "printf 'first failure\\nsecond failure\\n' >&2\nexit 1", detail: "kustomize build failed", timeout: 5 * time.Second},
		{name: "timeout", script: "/bin/sleep 30", detail: "kustomize build timed out", timeout: 100 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binDir := t.TempDir()
			stub := writeStub(t, binDir, "kustomize", test.script)
			got, notes, incomplete := extractKustomize(t.Context(), filepath.Join("testdata", "repo_kustomize"), "", kustomizeArgv(stub, ""), defaultNoteSeparator, execToolRunner{timeout: test.timeout})
			assertSuggestion(t, got, "Secret", "app-secrets", true, ModeLiteral, test.detail)
			if len(notes) != 1 || !strings.Contains(notes[0], strings.TrimPrefix(test.detail, "kustomize build ")) || !incomplete {
				t.Fatalf("notes = %q, incomplete = %t", notes, incomplete)
			}
			if test.name == "failure" && (!strings.Contains(notes[0], "first failure") || strings.Contains(notes[0], "second failure")) {
				t.Fatalf("notes = %q", notes)
			}
		})
	}
}

func TestKustomizeRenderFailurePreservesReadNotes(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	outside := t.TempDir()
	writeRepoFile(t, outside, "kustomization.yaml", "namespace: escaped\n")
	if err := os.Symlink(filepath.Join(outside, "kustomization.yaml"), filepath.Join(root, "kustomization.yaml")); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, root, "kustomization.yml", "namespace: real\n")

	missingBinary := filepath.Join(root, "missing-kustomize")
	got, notes, incomplete := extractKustomize(
		t.Context(),
		resolvedRoot,
		"",
		kustomizeArgv(missingBinary, ""),
		defaultNoteSeparator,
		execToolRunner{timeout: time.Second},
	)

	assertSuggestion(t, got, KindNamespace, "real", false, ModeLiteral, "kustomize build failed")
	if !containsString(notes, "file skipped: symlink target outside repository") || !incomplete {
		t.Fatalf("notes = %q, incomplete = %t; want out-of-root read note", notes, incomplete)
	}
	if len(notes) != 2 || !strings.Contains(notes[1], "kustomize build failed") {
		t.Fatalf("notes = %q, want read and render-failure notes", notes)
	}
}

func TestKustomizeRealTool(t *testing.T) {
	tool, err := exec.LookPath("kustomize")
	if err != nil {
		t.Skip("kustomize not installed — real-tool test runs in CI")
	}
	absolute, err := filepath.Abs(tool)
	if err != nil {
		t.Fatalf("absolute kustomize path: %v", err)
	}
	got, _, _ := extractKustomize(t.Context(), filepath.Join("testdata", "repo_kustomize"), "", kustomizeArgv(absolute, ""), defaultNoteSeparator, execToolRunner{timeout: 30 * time.Second})
	assertSuggestion(t, got, "Secret", "app-secrets", true, ModeRendered, "kustomize")
	assertSuggestion(t, got, "ConfigMap", "app-config", true, ModeRendered, "kustomize")
}

func assertSuggestion(t *testing.T, suggestions []Suggestion, kind, name string, prefix bool, mode RenderMode, detail string) {
	t.Helper()
	for _, suggestion := range suggestions {
		if suggestion.Kind == kind && suggestion.Name == name && suggestion.NamePrefix == prefix && suggestion.Mode == mode && suggestion.Detail == detail {
			return
		}
	}
	t.Fatalf("missing %s/%s prefix=%t mode=%s detail=%q in %+v", kind, name, prefix, mode, detail, suggestions)
}
