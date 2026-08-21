package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const renderedHelmYAML = `printf '%s\n' 'apiVersion: apps/v1' 'kind: Deployment' 'metadata:' '  name: release-name-mychart' '  namespace: production' 'spec:' '  template:' '    spec:' '      containers:' '        - name: app' '          image: example.invalid/app' '          envFrom:' '            - secretRef:' '                name: app-secrets'`

func TestHelmRendered(t *testing.T) {
	binDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "args")
	t.Setenv("STUB_OUT", record)
	stub := writeStub(t, binDir, "helm", `printf '%s\n' "$@" > "$STUB_OUT"
`+renderedHelmYAML)
	got, _, incomplete := extractHelm(t.Context(), filepath.Join("testdata", "repo_helm"), "mychart", stub, []string{"values-prod.yaml"}, defaultNoteSeparator, execToolRunner{timeout: 5 * time.Second})
	if incomplete {
		t.Fatal("extractHelm() marked successful render incomplete")
	}
	assertSuggestion(t, got, "Secret", "app-secrets", false, ModeRendered, "values: values-prod.yaml")
	args, err := os.ReadFile(record) // #nosec G304 -- record is inside the test's private temporary directory.
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), "--values\nvalues-prod.yaml") {
		t.Fatalf("helm args = %q", args)
	}
}

func TestHelmLiteralNoTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got, notes, incomplete := extractHelm(t.Context(), filepath.Join("testdata", "repo_helm"), "mychart", "", nil, defaultNoteSeparator, execToolRunner{timeout: time.Second})
	assertSuggestion(t, got, "ConfigMap", "literal-config", false, ModeLiteral, "helm: templated")
	assertSuggestion(t, got, "Secret", "literal-secret", false, ModeLiteral, "helm: templated")
	for _, suggestion := range got {
		if strings.Contains(suggestion.Name, "{{") || !strings.HasPrefix(suggestion.File, "mychart/templates/") || suggestion.Line == 0 {
			t.Fatalf("literal suggestion = %+v", suggestion)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "not on PATH") || !incomplete {
		t.Fatalf("notes = %q, incomplete = %t", notes, incomplete)
	}
}

func TestHelmRenderFailureFallsBack(t *testing.T) {
	stub := writeStub(t, t.TempDir(), "helm", "printf 'first failure\\nsecond failure\\n' >&2\nexit 1")
	got, notes, incomplete := extractHelm(t.Context(), filepath.Join("testdata", "repo_helm"), "mychart", stub, nil, defaultNoteSeparator, execToolRunner{timeout: 5 * time.Second})
	assertSuggestion(t, got, "ConfigMap", "literal-config", false, ModeLiteral, "helm: templated")
	if len(notes) != 1 || !strings.Contains(notes[0], "failed") || !strings.Contains(notes[0], "first failure") || strings.Contains(notes[0], "second failure") || !incomplete {
		t.Fatalf("notes = %q, incomplete = %t", notes, incomplete)
	}
}

func TestHelmLiteralWalkErrors(t *testing.T) {
	_, notes := extractHelmLiteral(t.TempDir(), "missing")
	if len(notes) != 0 {
		t.Fatalf("missing templates notes = %q", notes)
	}

	_, notes = extractHelmLiteral(t.TempDir(), "invalid\x00path")
	if len(notes) != 1 || !strings.Contains(notes[0], "helm literal extraction failed") {
		t.Fatalf("unexpected walk error notes = %q", notes)
	}
}

func TestHelmTemplateRefsReportExactLine(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	writeRepoFile(t, root, "chart/Chart.yaml", "apiVersion: v2\nname: chart\nversion: 0.1.0\n")
	writeRepoFile(t, root, "chart/templates/deployment.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: fixture
---
{{- if .Values.enabled }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
        - name: app
          image: example.invalid/app
          env:
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: app-secrets
                  key: token
{{- end }}
`)
	result, err := Scan(t.Context(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, suggestion := range result.Suggestions {
		if suggestion.Kind == "Secret" && suggestion.Name == "app-secrets" {
			if suggestion.Line != 21 || suggestion.Detail != "helm: templated" {
				t.Fatalf("templated Secret = %+v, want line 21 with templated detail", suggestion)
			}
			return
		}
	}
	t.Fatalf("missing templated Secret in %+v", result.Suggestions)
}

func TestHelmLiteralManifestNotDoubleExtracted(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	writeRepoFile(t, root, "chart/Chart.yaml", "apiVersion: v2\nname: chart\nversion: 0.1.0\n")
	writeRepoFile(t, root, "chart/templates/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
  namespace: prod
spec:
  template:
    spec:
      containers:
        - name: app
          image: example.invalid/app
          env:
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: app-secrets
                  key: token
`)
	result, err := Scan(t.Context(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	count := 0
	for _, suggestion := range result.Suggestions {
		if suggestion.Kind == "Secret" && suggestion.Name == "app-secrets" {
			count++
			if suggestion.Namespace != "prod" || suggestion.Detail != "helm: literal YAML" {
				t.Fatalf("literal Secret = %+v, want namespace prod via structured parse", suggestion)
			}
		}
	}
	if count != 1 {
		t.Fatalf("literal Secret appears %d times in %+v, want once", count, result.Suggestions)
	}
}

func TestHelmRealTool(t *testing.T) {
	tool, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed — real-tool test runs in CI")
	}
	absolute, err := filepath.Abs(tool)
	if err != nil {
		t.Fatalf("absolute helm path: %v", err)
	}
	got, _, _ := extractHelm(t.Context(), filepath.Join("testdata", "repo_helm"), "mychart", absolute, nil, defaultNoteSeparator, execToolRunner{timeout: 30 * time.Second})
	assertSuggestion(t, got, "Secret", "app-secrets", false, ModeRendered, "default values")
}
