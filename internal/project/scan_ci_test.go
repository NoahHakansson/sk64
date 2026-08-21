package project

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestExtractCIGitHub(t *testing.T) {
	path := filepath.Join("testdata", "repo_ci", ".github", "workflows", "deploy.yml")
	data, err := os.ReadFile(path) // #nosec G304 -- path names a repository-owned test fixture.
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := extractCI(".github/workflows/deploy.yml", data)
	wantNamespaces := []Suggestion{
		{Kind: KindNamespace, Name: "staging", File: ".github/workflows/deploy.yml", Line: 4, Mode: ModeCI},
		{Kind: KindNamespace, Name: "production", File: ".github/workflows/deploy.yml", Line: 5, Mode: ModeCI},
		{Kind: KindNamespace, Name: "qa", File: ".github/workflows/deploy.yml", Line: 6, Mode: ModeCI},
		{Kind: KindNamespace, Name: "release", File: ".github/workflows/deploy.yml", Line: 7, Mode: ModeCI},
	}
	if !reflect.DeepEqual(got.namespaces, wantNamespaces) {
		t.Fatalf("namespaces = %+v, want %+v", got.namespaces, wantNamespaces)
	}
	if len(got.contextNotes) != 1 || got.contextNotes[0] != `CI references kube context "prod-ctx" (.github/workflows/deploy.yml:6)` {
		t.Fatalf("context notes = %q", got.contextNotes)
	}
	if !reflect.DeepEqual(got.valuesFiles, []string{"values-prod.yaml"}) {
		t.Fatalf("values files = %q", got.valuesFiles)
	}
}

func TestExtractCIGitLab(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "repo_ci", ".gitlab-ci.yml")) // #nosec G304 -- path names a repository-owned test fixture.
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := extractCI(".gitlab-ci.yml", data)
	if len(got.namespaces) != 1 || got.namespaces[0].Name != "gitlab" || got.namespaces[0].Line != 3 {
		t.Fatalf("namespaces = %+v", got.namespaces)
	}
}

func TestExtractCIContinuationLines(t *testing.T) {
	workflow := `name: deploy
jobs:
  deploy:
    run: |
      helm upgrade app ./chart \
        --namespace prod \
        --kube-context prod-cluster -f values-prod.yaml
`
	got := extractCI("deploy.yml", []byte(workflow))
	if len(got.namespaces) != 1 || got.namespaces[0].Name != "prod" || got.namespaces[0].Line != 5 {
		t.Fatalf("namespaces = %+v, want prod at line 5", got.namespaces)
	}
	if !reflect.DeepEqual(got.contexts, []string{"prod-cluster"}) {
		t.Fatalf("contexts = %+v", got.contexts)
	}
	if !reflect.DeepEqual(got.contextNotes, []string{`CI references kube context "prod-cluster" (deploy.yml:5)`}) {
		t.Fatalf("context notes = %q", got.contextNotes)
	}
	if !reflect.DeepEqual(got.valuesFiles, []string{"values-prod.yaml"}) {
		t.Fatalf("values files = %q", got.valuesFiles)
	}
}

func TestScanContextHints(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		wantHints []string
		wantNotes []string
	}{
		{
			name: "literal context flags",
			files: map[string]string{
				".github/workflows/deploy.yml": "run: kubectl --context staging get pods\nrun: helm --kube-context=production list\n",
			},
			wantHints: []string{"staging", "production"},
			wantNotes: []string{
				`CI references kube context "staging" (.github/workflows/deploy.yml:1)`,
				`CI references kube context "production" (.github/workflows/deploy.yml:2)`,
			},
		},
		{
			name: "commented commands are ignored",
			files: map[string]string{
				".github/workflows/deploy.yml": "# kubectl --context commented get pods | helm --kube-context piped list\nrun: # helm --kube-context hidden list\nrun: echo ready # kubectl --context trailing get pods\n",
			},
		},
		{
			name: "non-command YAML fields are ignored",
			files: map[string]string{
				".github/workflows/deploy.yml": "name: kubectl --context display-only get pods\ndescription: helm --kube-context also-display-only list\n",
			},
		},
		{
			name: "unrelated tool context is ignored before kubectl",
			files: map[string]string{
				".github/workflows/deploy.yml": "run: docker --context desktop build . && kubectl --context production get pods\n",
			},
			wantHints: []string{"production"},
			wantNotes: []string{`CI references kube context "production" (.github/workflows/deploy.yml:1)`},
		},
		{
			name: "display-only command text is ignored",
			files: map[string]string{
				".github/workflows/deploy.yml": "run: echo 'kubectl --context display-only get pods'\n",
			},
		},
		{
			name: "variables are ignored",
			files: map[string]string{
				".gitlab-ci.yml": "script:\n  - kubectl --context $KUBE_CONTEXT get pods\n  - helm --kube-context=${DEPLOY_CONTEXT} list\n",
			},
		},
		{
			name: "duplicate names are deduplicated while keeping all notes",
			files: map[string]string{
				".github/workflows/a.yml": "run: kubectl --context shared get pods\n",
				".github/workflows/b.yml": "run: helm --kube-context shared list\n",
			},
			wantHints: []string{"shared"},
			wantNotes: []string{
				`CI references kube context "shared" (.github/workflows/a.yml:1)`,
				`CI references kube context "shared" (.github/workflows/b.yml:1)`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range test.files {
				writeRepoFile(t, root, path, content)
			}
			result, err := Scan(t.Context(), ScanOptions{Root: root})
			if err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
			if !slices.Equal(result.ContextHints, test.wantHints) {
				t.Fatalf("ContextHints = %+v, want %+v", result.ContextHints, test.wantHints)
			}
			if !slices.Equal(result.Notes, test.wantNotes) {
				t.Fatalf("Notes = %q, want %q", result.Notes, test.wantNotes)
			}
		})
	}
}
