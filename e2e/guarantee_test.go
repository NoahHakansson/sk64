package e2e

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestSuiteNeverLoadsKubeconfig(t *testing.T) {
	forbiddenLoadingCalls := map[string]bool{
		"New":           true,
		"SwitchContext": true,
		"ListContexts":  true,
		"HasContext":    true,
	}

	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		k8sNames := make(map[string]bool)
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return fmt.Errorf("unquote import path in %s: %w", path, err)
			}
			if importPath == "k8s.io/client-go/tools/clientcmd" ||
				strings.HasPrefix(importPath, "k8s.io/client-go/tools/clientcmd/") {
				t.Errorf("%s: the suite must build its client only from the rest.Config the harness produced; %s is forbidden", path, importPath)
			}
			if importPath != "github.com/NoahHakansson/sk64/internal/k8s" {
				continue
			}
			if imported.Name == nil {
				k8sNames["k8s"] = true
			} else if imported.Name.Name == "." {
				t.Errorf("%s: dot-importing internal/k8s is forbidden because loading-rules calls cannot be statically identified", path)
			} else if imported.Name.Name != "_" {
				k8sNames[imported.Name.Name] = true
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !forbiddenLoadingCalls[selector.Sel.Name] {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if ok && k8sNames[packageName.Name] {
				t.Errorf("%s: the suite must build its client only from the rest.Config the harness produced; %s.%s is forbidden", path, packageName.Name, selector.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk e2e Go files: %v", err)
	}
}

func TestAmbientKubeconfigIsScrubbed(t *testing.T) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG is empty")
	}
	if _, err := os.Stat(kubeconfig); !errors.Is(err, fs.ErrNotExist) { //nolint:gosec // The harness controls this environment path.
		t.Fatalf("KUBECONFIG path %q stat error = %v, want fs.ErrNotExist", kubeconfig, err)
	}

	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME is empty")
	}
	kubeDirectory := filepath.Join(home, ".kube")
	if _, err := os.Stat(kubeDirectory); !errors.Is(err, fs.ErrNotExist) { //nolint:gosec // The harness controls this environment path.
		t.Fatalf("ambient kube directory %q stat error = %v, want fs.ErrNotExist", kubeDirectory, err)
	}
	if value, exists := os.LookupEnv("USE_EXISTING_CLUSTER"); exists {
		t.Fatalf("USE_EXISTING_CLUSTER = %q, want unset", value)
	}
	for _, name := range []string{"KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT"} {
		if value, exists := os.LookupEnv(name); exists {
			t.Fatalf("%s = %q, want unset", name, value)
		}
	}
}

func TestControlPlaneIsLoopback(t *testing.T) {
	if err := requireLoopback(restConfig); err != nil {
		t.Fatalf("requireLoopback() error = %v", err)
	}
	parsed, err := url.Parse(restConfig.Host)
	if err != nil {
		t.Fatalf("parse rest config host %q: %v", restConfig.Host, err)
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("rest config host = %q, want loopback IP", restConfig.Host)
	}
	if _, err := client.Probe(ctxT(t)); err != nil {
		t.Fatalf("probe control plane: %v", err)
	}
	if client.Context != restConfig.Host {
		t.Fatalf("client context = %q, want %q", client.Context, restConfig.Host)
	}
}

func TestRequireLoopbackRejectsRemoteHosts(t *testing.T) {
	tests := []struct {
		host    string
		wantErr bool
	}{
		{host: "https://127.0.0.1:6443"},
		{host: "https://[::1]:6443"},
		{host: "https://localhost:41235"},
		{host: "127.0.0.1:6443"},
		{host: "https://203.0.113.7", wantErr: true},
		{host: "https://gke-prod.example.com", wantErr: true},
		{host: "https://10.0.0.5:6443", wantErr: true},
		{host: "https://127.0.0.1.evil.example.com", wantErr: true},
		{host: "", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			err := requireLoopback(&rest.Config{Host: test.host})
			if (err != nil) != test.wantErr {
				t.Fatalf("requireLoopback(%q) error = %v, wantErr %t", test.host, err, test.wantErr)
			}
		})
	}
}

func TestControlPlaneRefusesExistingCluster(t *testing.T) {
	if controlPlane.UseExistingCluster == nil || *controlPlane.UseExistingCluster {
		t.Fatal("control plane must explicitly refuse an existing cluster")
	}
}
