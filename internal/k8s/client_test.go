package k8s

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
)

func TestNew_MergedKubeconfigList(t *testing.T) {
	fileA := writeKubeconfig(t, "ctx-a", []kubeContext{{name: "ctx-a"}})
	fileB := writeKubeconfig(t, "", []kubeContext{{name: "ctx-b"}})
	t.Setenv("KUBECONFIG", strings.Join([]string{fileA, fileB}, string(os.PathListSeparator)))

	tests := []struct {
		name    string
		config  Config
		context string
	}{
		{name: "current context", context: "ctx-a"},
		{name: "context from merged file", config: Config{Context: "ctx-b"}, context: "ctx-b"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.config)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if client.Context != test.context {
				t.Fatalf("Context = %q, want %q", client.Context, test.context)
			}
		})
	}
}

func TestNew_NamespaceResolution(t *testing.T) {
	kubeconfig := writeKubeconfig(t, "ctx-ns", []kubeContext{
		{name: "ctx-ns", namespace: "team-x"},
		{name: "ctx-default"},
	})

	tests := []struct {
		name      string
		config    Config
		namespace string
	}{
		{name: "context namespace", config: Config{Kubeconfig: kubeconfig}, namespace: "team-x"},
		{name: "namespace override", config: Config{Kubeconfig: kubeconfig, Namespace: "other"}, namespace: "other"},
		{name: "default namespace", config: Config{Kubeconfig: kubeconfig, Context: "ctx-default"}, namespace: "default"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.config)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if client.Namespace != test.namespace {
				t.Fatalf("Namespace = %q, want %q", client.Namespace, test.namespace)
			}
		})
	}
}

func TestNew_ClusterResolution(t *testing.T) {
	kubeconfig := writeKubeconfig(t, "ctx-a", []kubeContext{
		{name: "ctx-a"},
		{name: "ctx-b"},
		{name: "no-cluster", emptyCluster: true},
	})

	for _, test := range []struct {
		name    string
		config  Config
		cluster string
	}{
		{name: "current context", config: Config{Kubeconfig: kubeconfig}, cluster: "cluster-ctx-a"},
		{name: "context override", config: Config{Kubeconfig: kubeconfig, Context: "ctx-b"}, cluster: "cluster-ctx-b"},
		{name: "cluster absent", config: Config{Kubeconfig: kubeconfig, Context: "no-cluster"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.config)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if client.Cluster != test.cluster {
				t.Fatalf("Cluster = %q, want %q", client.Cluster, test.cluster)
			}
		})
	}
}

func TestNew_ServerResolution(t *testing.T) {
	kubeconfig := writeKubeconfig(t, "ctx-a", []kubeContext{
		{name: "ctx-a", server: "https://a.example"},
		{name: "ctx-b", server: "https://b.example"},
	})

	tests := []struct {
		name   string
		config Config
		server string
	}{
		{name: "current context", config: Config{Kubeconfig: kubeconfig}, server: "https://a.example"},
		{name: "context override", config: Config{Kubeconfig: kubeconfig, Context: "ctx-b"}, server: "https://b.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.config)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if client.Server != test.server {
				t.Fatalf("Server = %q, want %q", client.Server, test.server)
			}
		})
	}
}

func TestNew_ExplicitKubeconfig(t *testing.T) {
	fileA := writeKubeconfig(t, "ctx-a", []kubeContext{{name: "ctx-a"}})
	fileB := writeKubeconfig(t, "ctx-b", []kubeContext{{name: "ctx-b"}})
	t.Setenv("KUBECONFIG", fileA)

	client, err := New(Config{Kubeconfig: fileB})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.Context != "ctx-b" {
		t.Fatalf("Context = %q, want %q", client.Context, "ctx-b")
	}
}

func TestNew_MissingKubeconfig(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("KUBECONFIG", "")
	originalHomeFile := clientcmd.RecommendedHomeFile
	clientcmd.RecommendedHomeFile = filepath.Join(emptyHome, ".kube", "config")
	t.Cleanup(func() {
		clientcmd.RecommendedHomeFile = originalHomeFile
	})

	if _, err := New(Config{}); err == nil {
		t.Fatal("New() error = nil, want non-nil")
	}
}

func TestNew_UnknownContext(t *testing.T) {
	kubeconfig := writeKubeconfig(t, "ctx-a", []kubeContext{{name: "ctx-a"}})
	if _, err := New(Config{Kubeconfig: kubeconfig, Context: "nope"}); err == nil {
		t.Fatal("New() error = nil, want non-nil")
	}
}

func TestNewForConfigIgnoresKubeconfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))

	client, err := NewForConfig(&rest.Config{Host: "https://127.0.0.1:1"}, "demo")
	if err != nil {
		t.Fatalf("NewForConfig() error = %v", err)
	}
	if client.Namespace != "demo" {
		t.Fatalf("Namespace = %q, want %q", client.Namespace, "demo")
	}
	if client.Server != "https://127.0.0.1:1" {
		t.Fatalf("Server = %q, want %q", client.Server, "https://127.0.0.1:1")
	}
}

func TestSwitchContextPreservesNamespace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"gitVersion":"v1.36.2"}`))
	}))
	t.Cleanup(server.Close)
	kubeconfig := writeKubeconfigForServer(t, "project-context", server.URL)

	client, err := SwitchContext(t.Context(), kubeconfig, "project-context", "project-namespace", nil)
	if err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	if client.Namespace != "project-namespace" {
		t.Fatalf("Namespace = %q, want %q", client.Namespace, "project-namespace")
	}
}

func TestProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"gitVersion":"v1.36.2"}`))
	}))

	kubeconfig := writeKubeconfigForServer(t, "probe-context", server.URL)
	client, err := New(Config{Kubeconfig: kubeconfig})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	serverVersion, err := client.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if serverVersion.GitVersion != "v1.36.2" {
		t.Fatalf("GitVersion = %q, want %q", serverVersion.GitVersion, "v1.36.2")
	}

	server.Close()
	_, err = client.Probe(context.Background())
	if err == nil {
		t.Fatal("Probe() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "probe-context") {
		t.Fatalf("Probe() error = %q, want context name", err)
	}
}

func TestCountSecrets(t *testing.T) {
	clientset := fake.NewClientset()
	page := 0
	clientset.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		page++
		if page == 1 {
			return true, &corev1.SecretList{
				ListMeta: metav1.ListMeta{Continue: "next-secrets"},
				Items:    []corev1.Secret{{}, {}},
			}, nil
		}
		return true, &corev1.SecretList{Items: []corev1.Secret{{}}}, nil
	})
	client := &Client{Clientset: clientset}

	count, err := client.CountSecrets(context.Background(), "ns1")
	if err != nil {
		t.Fatalf("CountSecrets() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("CountSecrets() = %d, want 3", count)
	}
	actions := clientset.Actions()
	if len(actions) != 2 {
		t.Fatalf("CountSecrets() actions = %d, want 2", len(actions))
	}
	if first, second := listContinueToken(t, actions[0]), listContinueToken(t, actions[1]); first != "" || second != "next-secrets" {
		t.Fatalf("CountSecrets() continue tokens = %q, %q", first, second)
	}
	// The reactor answers every list regardless of namespace, so without this the
	// test would still pass if CountSecrets listed the whole cluster.
	for i, action := range actions {
		if ns := action.GetNamespace(); ns != "ns1" {
			t.Fatalf("CountSecrets() action %d namespace = %q, want %q", i, ns, "ns1")
		}
	}
}

type kubeContext struct {
	name         string
	namespace    string
	server       string
	emptyCluster bool
}

func writeKubeconfig(t *testing.T, currentContext string, contexts []kubeContext) string {
	t.Helper()

	contents := "apiVersion: v1\nkind: Config\nclusters:\n"
	for _, kubeCtx := range contexts {
		server := kubeCtx.server
		if server == "" {
			server = "http://127.0.0.1"
		}
		contents += fmt.Sprintf("- name: %q\n  cluster:\n    server: %q\n", kubeCtx.clusterName(), server)
	}
	contents += "users:\n"
	for _, kubeCtx := range contexts {
		contents += fmt.Sprintf("- name: user-%s\n  user:\n    token: test-token\n", kubeCtx.name)
	}
	contents += "contexts:\n"
	for _, kubeCtx := range contexts {
		contents += fmt.Sprintf("- name: %s\n  context:\n", kubeCtx.name)
		contents += fmt.Sprintf("    cluster: %q\n", kubeCtx.clusterName())
		contents += fmt.Sprintf("    user: user-%s\n", kubeCtx.name)
		if kubeCtx.namespace != "" {
			contents += fmt.Sprintf("    namespace: %s\n", kubeCtx.namespace)
		}
	}
	if currentContext != "" {
		contents += fmt.Sprintf("current-context: %s\n", currentContext)
	}

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func (c kubeContext) clusterName() string {
	if c.emptyCluster {
		return ""
	}
	return "cluster-" + c.name
}

func writeKubeconfigForServer(t *testing.T, contextName, serverURL string) string {
	t.Helper()

	contents := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: %s
users:
- name: user
  user:
    token: test-token
contexts:
- name: %s
  context:
    cluster: cluster
    user: user
current-context: %s
`, serverURL, contextName, contextName)
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}
