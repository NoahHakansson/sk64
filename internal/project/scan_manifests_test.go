package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractManifestMultiDoc(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "repo_manifests", "multi.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := extractManifest("multi.yaml", data)
	want := map[string]int{
		"Namespace/production":  1,
		"Deployment/web":        1,
		"Secret/app-secrets":    1,
		"Secret/tls-cert":       1,
		"ConfigMap/app-config":  1,
		"Namespace/jobs":        35,
		"Secret/worker-secrets": 35,
		"Namespace/staging":     48,
	}
	if len(got) != len(want) {
		t.Fatalf("extractManifest() len = %d, want %d: %+v", len(got), len(want), got)
	}
	for _, suggestion := range got {
		key := suggestion.Kind + "/" + suggestion.Name
		line, ok := want[key]
		if !ok {
			t.Fatalf("unexpected suggestion %+v", suggestion)
		}
		if suggestion.Line != line || suggestion.Mode != ModeManifest {
			t.Fatalf("suggestion %+v, want line %d manifest", suggestion, line)
		}
	}
}

func TestExtractManifestGarbage(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("kind: ["),
		[]byte("metadata:\n  name: nope\n"),
		[]byte("kind: Deployment\nmetadata:\n  name: {{ .Values.name }}\n"),
	} {
		if got := extractManifest("bad.yaml", data); len(got) != 0 {
			t.Fatalf("extractManifest(%q) = %+v, want nil", data, got)
		}
	}
}

func TestSplitDocsSeparatorMustBeAtColumnZero(t *testing.T) {
	t.Run("indented block scalar separator", func(t *testing.T) {
		data := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: bundle
data:
  install.yaml: |
    apiVersion: v1
    kind: Namespace
    metadata:
      name: embedded
    ---
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: phantom-deploy
      namespace: phantom-ns
    spec:
      template:
        spec:
          containers:
            - name: app
              image: example.invalid/app
              envFrom:
                - secretRef:
                    name: phantom-secret
`)
		if got := extractManifest("bundle.yaml", data); len(got) != 0 {
			t.Fatalf("extractManifest() = %+v, want no suggestions", got)
		}
	})

	for _, separator := range []string{"---", "--- ", "---\t"} {
		t.Run("column zero "+separator, func(t *testing.T) {
			data := []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: first\n" + separator + "\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: second\n")
			got := extractManifest("namespaces.yaml", data)
			if len(got) != 2 || got[0].Name != "first" || got[0].Line != 1 || got[1].Name != "second" || got[1].Line != 6 {
				t.Fatalf("extractManifest() = %+v, want Namespace/first at 1 and Namespace/second at 6", got)
			}
		})
	}
}

func TestSuggestionsFromDocSkipsTemplatedValues(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{
			name: "namespace and reference",
			doc: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: "{{ .Values.ns }}"
spec:
  template:
    spec:
      containers:
        - name: web
          image: example.invalid/web
          env:
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: app-{{ .Release.Name }}-secrets
                  key: token
`,
		},
		{
			name: "reference",
			doc: `apiVersion: v1
kind: Pod
metadata:
  name: web
spec:
  containers:
    - name: web
      image: example.invalid/web
      env:
        - name: TOKEN
          valueFrom:
            secretKeyRef:
              name: app-{{ .Release.Name }}-secrets
              key: token
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := suggestionsFromDoc("deployment.yaml", 1, ModeManifest, "", []byte(test.doc)); len(got) != 0 {
				t.Fatalf("suggestionsFromDoc() = %+v, want no suggestions", got)
			}
		})
	}
}
