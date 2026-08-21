package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeRepoFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func namespaceManifest(name string) string {
	return "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + name + "\n"
}

func TestScanWalkRespectsGitignoreDepthAndCaps(t *testing.T) {
	t.Run("gitignore and git directory", func(t *testing.T) {
		root := t.TempDir()
		writeRepoFile(t, root, ".gitignore", "ignored/\n")
		writeRepoFile(t, root, "visible.yaml", namespaceManifest("visible"))
		writeRepoFile(t, root, "ignored/hidden.yaml", namespaceManifest("hidden"))
		writeRepoFile(t, root, ".git/hidden.yaml", namespaceManifest("git-hidden"))
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		if suggestionNames(result.Suggestions) != "visible" {
			t.Fatalf("suggestions = %+v", result.Suggestions)
		}
	})
	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		writeRepoFile(t, root, "a/b/kept.yaml", namespaceManifest("kept"))
		writeRepoFile(t, root, "a/b/c/deep.yaml", namespaceManifest("deep"))
		result, err := Scan(t.Context(), ScanOptions{Root: root, MaxDepth: 1})
		if err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		if suggestionNames(result.Suggestions) != "kept" {
			t.Fatalf("suggestions = %+v", result.Suggestions)
		}
	})
	t.Run("file cap", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"a", "b", "c", "d"} {
			writeRepoFile(t, root, name+".yaml", namespaceManifest(name))
		}
		for _, test := range []struct {
			name          string
			noteSeparator string
			want          string
		}{
			{name: "default", want: "file cap reached (3) — scan truncated"},
			{name: "ASCII", noteSeparator: " - ", want: "file cap reached (3) - scan truncated"},
		} {
			t.Run(test.name, func(t *testing.T) {
				result, err := Scan(t.Context(), ScanOptions{Root: root, MaxFiles: 3, NoteSeparator: test.noteSeparator})
				if err != nil {
					t.Fatalf("Scan() error = %v", err)
				}
				if !reflect.DeepEqual(result.Notes, []string{test.want}) || len(result.Suggestions) != 3 || !result.Incomplete {
					t.Fatalf("result = %+v", result)
				}
			})
		}
	})
}

func TestScanOrchestratorCombined(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	writeRepoFile(t, root, "app.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: production
spec:
  template:
    spec:
      containers:
        - name: web
          image: example.invalid/web
          envFrom:
            - secretRef:
                name: app-secrets
`)
	writeRepoFile(t, root, "base/kustomization.yaml", "namespace: production\nsecretGenerator:\n  - name: app-secrets\n")
	writeRepoFile(t, root, "chart/Chart.yaml", "apiVersion: v2\nname: chart\nversion: 0.1.0\n")
	writeRepoFile(t, root, "chart/templates/deployment.yaml", "secretName: chart-secret\n")
	writeRepoFile(t, root, ".github/workflows/deploy.yml", "run: helm upgrade app chart -f values-prod.yaml --namespace staging --kube-context prod\n")
	result, err := Scan(t.Context(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, want := range []string{"Namespace/production", "Namespace/staging", "Deployment/web", "Secret/app-secrets", "Secret/chart-secret"} {
		if !containsSuggestion(result.Suggestions, want) {
			t.Fatalf("missing %s in %+v", want, result.Suggestions)
		}
	}
	for _, want := range []string{
		`CI references kube context "prod" (.github/workflows/deploy.yml:1)`,
		"kustomize not on PATH — kustomizations parsed literally",
		"helm not on PATH — charts parsed literally",
	} {
		count := 0
		for _, note := range result.Notes {
			if note == want {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("note %q appears %d times in %q, want once", want, count, result.Notes)
		}
	}
	productionNamespaces := 0
	for _, suggestion := range result.Suggestions {
		if suggestion.Kind == KindNamespace && suggestion.Name == "production" {
			productionNamespaces++
			if suggestion.File != "app.yaml" {
				t.Fatalf("manifest namespace provenance did not win: %+v", suggestion)
			}
		}
	}
	if productionNamespaces != 1 {
		t.Fatalf("Namespace/production appears %d times in %+v, want once", productionNamespaces, result.Suggestions)
	}
}

func TestScanEmptyRepo(t *testing.T) {
	result, err := Scan(t.Context(), ScanOptions{Root: t.TempDir()})
	if err != nil || len(result.Suggestions) != 0 || len(result.Notes) != 0 || result.Incomplete {
		t.Fatalf("Scan() = %+v, %v", result, err)
	}
}

func TestScanMarksRenderFailureIncomplete(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	writeStub(t, binDir, "helm", "echo render failed >&2\nexit 1")
	t.Setenv("PATH", binDir)
	writeRepoFile(t, root, "chart/Chart.yaml", "apiVersion: v2\nname: chart\nversion: 0.1.0\n")
	writeRepoFile(t, root, "chart/templates/secret.yaml", "secretName: fallback-secret\n")

	result, err := Scan(t.Context(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if !result.Incomplete {
		t.Fatalf("ScanResult.Incomplete = false, notes = %q", result.Notes)
	}
	if !containsString(result.Notes, "helm template failed in chart — literal extraction: render failed") {
		t.Fatalf("Notes = %q", result.Notes)
	}
}

func TestScanMissingRoot(t *testing.T) {
	_, err := Scan(t.Context(), ScanOptions{Root: filepath.Join(t.TempDir(), "missing")})
	if err == nil {
		t.Fatal("Scan() error = nil")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Scan(cancelled, ScanOptions{Root: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan(cancelled) error = %v", err)
	}
}

func TestScanSkipsChartTemplateManifests(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	writeStub(t, binDir, "helm", "exit 0")
	t.Setenv("PATH", binDir)
	writeRepoFile(t, root, "mychart/Chart.yaml", "apiVersion: v2\nname: mychart\nversion: 0.1.0\n")
	writeRepoFile(t, root, "mychart/templates/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: templated-workload
  namespace: "{{ .Values.namespace }}"
spec:
  template:
    spec:
      containers:
        - name: app
          image: example.invalid/app
`)
	writeRepoFile(t, root, "mychart/templates/ns.yaml", namespaceManifest("literal-in-template"))
	writeRepoFile(t, root, "mychart/crds/thing.yaml", namespaceManifest("chart-crd"))

	result, err := Scan(t.Context(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if containsSuggestion(result.Suggestions, "Namespace/literal-in-template") {
		t.Fatalf("chart template was scanned as a plain manifest: %+v", result.Suggestions)
	}
	if !containsSuggestion(result.Suggestions, "Namespace/chart-crd") {
		t.Fatalf("chart CRD manifest was dropped: %+v", result.Suggestions)
	}
}

func TestScanHelmTemplatesWithoutHelm(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	writeRepoFile(t, root, "mychart/Chart.yaml", "apiVersion: v2\nname: mychart\nversion: 0.1.0\n")
	writeRepoFile(t, root, "mychart/templates/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: "{{ .Values.namespace }}"
spec:
  template:
    spec:
      containers:
        - name: web
          image: example.invalid/web
          env:
            - name: CONFIG
              valueFrom:
                configMapKeyRef:
                  name: '{{ include "chart.fullname" . }}-config'
                  key: config
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: app-{{ .Release.Name }}-secrets
                  key: token
`)
	writeRepoFile(t, root, "mychart/templates/namespace.yaml", namespaceManifest("literal-in-template"))
	writeRepoFile(t, root, "mychart/templates/literal.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: literal-app
spec:
  template:
    spec:
      imagePullSecrets:
        - name: registry-credentials
      containers:
        - name: app
          image: example.invalid/app
      volumes:
        - name: config
          configMap:
            name: app-config
`)
	writeRepoFile(t, root, "mychart/crds/thing.yaml", namespaceManifest("chart-crd"))

	result, err := Scan(t.Context(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, suggestion := range result.Suggestions {
		if strings.Contains(suggestion.Name, "{{") {
			t.Fatalf("templated suggestion survived: %+v", suggestion)
		}
	}
	for _, want := range []string{
		"Namespace/chart-crd",
		"Namespace/literal-in-template",
		"Deployment/literal-app",
		"Secret/registry-credentials",
		"ConfigMap/app-config",
	} {
		if !containsSuggestion(result.Suggestions, want) {
			t.Fatalf("missing %s from %+v", want, result.Suggestions)
		}
	}
}

func TestScanSymlinkAndSpecialFiles(t *testing.T) {
	t.Run("escape", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeRepoFile(t, outside, "secret-stuff.yaml", namespaceManifest("escaped-ns"))
		if err := os.Symlink(filepath.Join(outside, "secret-stuff.yaml"), filepath.Join(root, "link.yaml")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "secret-stuff.yaml"), filepath.Join(root, "second-link.yaml")); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || len(result.Suggestions) != 0 || !result.Incomplete || !containsString(result.Notes, "2 files skipped: symlink target outside repository") {
			t.Fatalf("Scan() = %+v, %v; want incomplete scan with no suggestions", result, err)
		}
	})

	t.Run("in-root link still followed", func(t *testing.T) {
		root := t.TempDir()
		writeRepoFile(t, root, "real/app.txt", `apiVersion: v1
kind: Namespace
metadata:
  name: only-via-link
`)
		if err := os.Symlink("real/app.txt", filepath.Join(root, "a.yaml")); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || !containsSuggestion(result.Suggestions, "Namespace/only-via-link") {
			t.Fatalf("Scan() = %+v, %v; want linked namespace", result, err)
		}
	})

	t.Run("dangling", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink("nope.yaml", filepath.Join(root, "dangling.yaml")); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || len(result.Suggestions) != 0 || !containsString(result.Notes, "1 file skipped: dangling symlink") {
			t.Fatalf("Scan() = %+v, %v; want dangling-symlink note", result, err)
		}
	})

	t.Run("FIFO", func(t *testing.T) {
		root := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(root, "blocking.yaml"), 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := Scan(t.Context(), ScanOptions{Root: root})
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Scan() blocked reading a FIFO")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		root := t.TempDir()
		writeRepoFile(t, root, "big.yaml", namespaceManifest("too-big")+strings.Repeat("#", maxScanFileBytes))
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || len(result.Suggestions) != 0 || !containsString(result.Notes, "1 file skipped: larger than 4 MiB") {
			t.Fatalf("Scan() = %+v, %v; want oversized-file note", result, err)
		}
	})

	t.Run("kustomization escape", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		t.Setenv("PATH", t.TempDir())
		writeRepoFile(t, outside, "kustomization.yaml", "namespace: escaped\n")
		if err := os.Symlink(filepath.Join(outside, "kustomization.yaml"), filepath.Join(root, "kustomization.yaml")); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || len(result.Suggestions) != 0 || !containsString(result.Notes, "1 file skipped: symlink target outside repository") {
			t.Fatalf("Scan() = %+v, %v; want contained kustomization reads", result, err)
		}
	})

	t.Run("two kustomization markers", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		t.Setenv("PATH", t.TempDir())
		writeRepoFile(t, outside, "kustomization.yaml", "namespace: escaped\n")
		if err := os.Symlink(filepath.Join(outside, "kustomization.yaml"), filepath.Join(root, "kustomization.yaml")); err != nil {
			t.Fatal(err)
		}
		writeRepoFile(t, root, "kustomization.yml", "namespace: real\n")
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || !containsSuggestion(result.Suggestions, "Namespace/real") {
			t.Fatalf("Scan() = %+v, %v; want Namespace/real", result, err)
		}
		count := 0
		for _, note := range result.Notes {
			if note == "1 file skipped: symlink target outside repository" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("skip note appears %d times in %q, want once", count, result.Notes)
		}
	})

	t.Run("helm template escape", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		t.Setenv("PATH", t.TempDir())
		writeRepoFile(t, root, ".gitignore", "mychart/templates/escaped.yaml\n")
		writeRepoFile(t, root, "mychart/Chart.yaml", "apiVersion: v2\nname: mychart\nversion: 0.1.0\n")
		writeRepoFile(t, root, "mychart/templates/.keep", "")
		writeRepoFile(t, outside, "escaped.yaml", "secretName: escaped-secret\n")
		if err := os.Symlink(filepath.Join(outside, "escaped.yaml"), filepath.Join(root, "mychart/templates/escaped.yaml")); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || containsSuggestion(result.Suggestions, "Secret/escaped-secret") || !containsString(result.Notes, "1 file skipped: symlink target outside repository") {
			t.Fatalf("Scan() = %+v, %v; want contained Helm reads", result, err)
		}
	})

	t.Run("gitignore escape", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeRepoFile(t, outside, ".gitignore", "*.yaml\n")
		writeRepoFile(t, root, "visible.yaml", namespaceManifest("visible"))
		if err := os.Symlink(filepath.Join(outside, ".gitignore"), filepath.Join(root, ".gitignore")); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(t.Context(), ScanOptions{Root: root})
		if err != nil || !containsSuggestion(result.Suggestions, "Namespace/visible") || !containsString(result.Notes, "1 file skipped: symlink target outside repository") {
			t.Fatalf("Scan() = %+v, %v; want contained .gitignore reads", result, err)
		}
	})
}

func TestAppendScanFileNote(t *testing.T) {
	tests := []struct {
		name  string
		notes []string
		err   error
		want  []string
	}{
		{name: "unknown", err: errors.New("boom"), want: []string{"file skipped: unreadable"}},
		{name: "wrapped I/O error", err: fmt.Errorf("read: %w", syscall.EIO), want: []string{"file skipped: unreadable"}},
		{name: "permission", err: fs.ErrPermission},
		{name: "missing", err: fs.ErrNotExist},
		{name: "outside root", err: errScanFileOutsideRoot, want: []string{"file skipped: symlink target outside repository"}},
		{name: "dangling symlink", err: errScanFileDangling, want: []string{"file skipped: dangling symlink"}},
		{
			name:  "wrapped not regular",
			notes: []string{"existing note"},
			err:   fmt.Errorf("open: %w", errScanFileNotRegular),
			want:  []string{"existing note", "file skipped: not a regular file"},
		},
		{name: "too large", err: errScanFileTooLarge, want: []string{"file skipped: larger than 4 MiB"}},
		{name: "changed", err: errScanFileChanged, want: []string{"file skipped: path changed during scan"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := appendScanFileNote(test.notes, test.err); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("appendScanFileNote(%q, %v) = %q, want %q", test.notes, test.err, got, test.want)
			}
		})
	}
}

func TestScanDedupesOnResolvedNamespace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	writeRepoFile(t, root, "base/app.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
        - name: web
          image: example.invalid/web
          envFrom:
            - secretRef:
                name: app-secrets
`)
	writeRepoFile(t, root, "overlays/prod/app.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: production
spec:
  template:
    spec:
      containers:
        - name: web
          image: example.invalid/web
          envFrom:
            - secretRef:
                name: app-secrets
`)

	resolved, err := Scan(t.Context(), ScanOptions{Root: root, DefaultNamespace: "production"})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, target := range []string{"Deployment/web", "Secret/app-secrets"} {
		count := 0
		for _, suggestion := range resolved.Suggestions {
			if suggestion.Kind+"/"+suggestion.Name == target {
				count++
				if suggestion.File != "base/app.yaml" {
					t.Fatalf("%s provenance = %q, want base/app.yaml", target, suggestion.File)
				}
				if suggestion.Namespace != "production" {
					t.Fatalf("%s namespace = %q, want production", target, suggestion.Namespace)
				}
			}
		}
		if count != 1 {
			t.Fatalf("%s appears %d times in %+v, want once", target, count, resolved.Suggestions)
		}
	}

	unresolved, err := Scan(t.Context(), ScanOptions{Root: root})
	if err != nil {
		t.Fatalf("Scan() without default namespace error = %v", err)
	}
	for _, target := range []string{"Deployment/web", "Secret/app-secrets"} {
		count := 0
		for _, suggestion := range unresolved.Suggestions {
			if suggestion.Kind+"/"+suggestion.Name == target {
				count++
				if suggestion.File == "base/app.yaml" && suggestion.Namespace != "" {
					t.Fatalf("%s base namespace = %q, want empty", target, suggestion.Namespace)
				}
			}
		}
		if count != 2 {
			t.Fatalf("%s appears %d times in %+v, want twice", target, count, unresolved.Suggestions)
		}
	}
}

func TestSuggestionHelpers(t *testing.T) {
	tests := []struct {
		name        string
		suggestion  Suggestion
		provenance  string
		modeLabel   string
		displayName string
	}{
		{name: "manifest", suggestion: Suggestion{File: "app.yaml", Line: 4, Mode: ModeManifest, Name: "app"}, provenance: "app.yaml:4", modeLabel: "[manifest]", displayName: "app"},
		{name: "rendered", suggestion: Suggestion{File: "Chart.yaml", Mode: ModeRendered, Detail: "default values", Name: "app"}, provenance: "Chart.yaml", modeLabel: "[rendered: default values]", displayName: "app"},
		{name: "literal prefix", suggestion: Suggestion{File: "kustomization.yaml", Mode: ModeLiteral, Detail: "kustomize not on PATH", Name: "app", NamePrefix: true}, provenance: "kustomization.yaml", modeLabel: "[literal: kustomize not on PATH]", displayName: "app-*"},
		{name: "CI", suggestion: Suggestion{File: "ci.yml", Mode: ModeCI, Name: "prod"}, provenance: "ci.yml", modeLabel: "[ci]", displayName: "prod"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.suggestion.Provenance(); got != test.provenance {
				t.Errorf("Provenance() = %q, want %q", got, test.provenance)
			}
			if got := test.suggestion.ModeLabel(); got != test.modeLabel {
				t.Errorf("ModeLabel() = %q, want %q", got, test.modeLabel)
			}
			if got := test.suggestion.DisplayName(); got != test.displayName {
				t.Errorf("DisplayName() = %q, want %q", got, test.displayName)
			}
		})
	}
}

func suggestionNames(suggestions []Suggestion) string {
	var names []string
	for _, suggestion := range suggestions {
		names = append(names, suggestion.Name)
	}
	return strings.Join(names, ",")
}

func containsSuggestion(suggestions []Suggestion, value string) bool {
	for _, suggestion := range suggestions {
		if suggestion.Kind+"/"+suggestion.Name == value {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
