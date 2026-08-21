package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func listContinueToken(t *testing.T, action k8stesting.Action) string {
	t.Helper()
	listAction, ok := action.(k8stesting.ListActionImpl)
	if !ok {
		t.Fatalf("client action = %T, want testing.ListActionImpl", action)
	}
	return listAction.GetListOptions().Continue
}

func TestListNamespaces(t *testing.T) {
	client := &Client{Clientset: fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "beta"}},
	)}

	page, err := client.ListNamespaces(context.Background(), DefaultPageSize, "")
	if err != nil {
		t.Fatalf("ListNamespaces() error = %v", err)
	}
	if len(page.Names) != 2 || page.Names[0] != "alpha" || page.Names[1] != "beta" {
		t.Fatalf("ListNamespaces() names = %v", page.Names)
	}
	if page.Continue != "" {
		t.Fatalf("ListNamespaces() continue = %q", page.Continue)
	}
}

func TestListSecretsAndConfigMaps(t *testing.T) {
	immutable := true
	client := &Client{Clientset: fake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "ns"},
			Type:       corev1.SecretTypeTLS,
			Immutable:  &immutable,
		},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "ns"}},
	)}

	secrets, err := client.ListSecrets(context.Background(), "ns", DefaultPageSize, "")
	if err != nil {
		t.Fatalf("ListSecrets() error = %v", err)
	}
	if len(secrets.Items) != 1 || secrets.Items[0].Kind() != KindSecret || secrets.Items[0].Type() != string(corev1.SecretTypeTLS) || !secrets.Items[0].Immutable() {
		t.Fatalf("ListSecrets() items = %#v", secrets.Items)
	}
	configMaps, err := client.ListConfigMaps(context.Background(), "ns", DefaultPageSize, "")
	if err != nil {
		t.Fatalf("ListConfigMaps() error = %v", err)
	}
	if len(configMaps.Items) != 1 || configMaps.Items[0].Kind() != KindConfigMap || configMaps.Items[0].Type() != "" {
		t.Fatalf("ListConfigMaps() items = %#v", configMaps.Items)
	}
}

func TestListAllNamespaces(t *testing.T) {
	client := &Client{Clientset: fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "production"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "staging"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "production"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "staging"}},
	)}

	for _, list := range []struct {
		name string
		list func(context.Context, string, int64, string) (ResourcePage, error)
	}{
		{name: "secrets", list: client.ListSecrets},
		{name: "configmaps", list: client.ListConfigMaps},
	} {
		t.Run(list.name, func(t *testing.T) {
			all, err := list.list(t.Context(), AllNamespaces, DefaultPageSize, "")
			if err != nil {
				t.Fatalf("all-namespace list error = %v", err)
			}
			if len(all.Items) != 2 || all.Items[0].Namespace() != "production" || all.Items[1].Namespace() != "staging" {
				t.Fatalf("all-namespace items = %#v", all.Items)
			}
			one, err := list.list(t.Context(), "production", DefaultPageSize, "")
			if err != nil {
				t.Fatalf("single-namespace list error = %v", err)
			}
			if len(one.Items) != 1 || one.Items[0].Namespace() != "production" {
				t.Fatalf("single-namespace items = %#v", one.Items)
			}
		})
	}
}

func TestListErrorNamespaceLabel(t *testing.T) {
	clientset := fake.NewClientset()
	clientset.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("denied")
	})
	client := &Client{Clientset: clientset}

	for _, test := range []struct {
		name      string
		namespace string
		want      string
	}{
		{name: "all namespaces", namespace: AllNamespaces, want: "list secrets in all namespaces"},
		{name: "single namespace", namespace: "prod", want: `list secrets in namespace "prod"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.ListSecrets(t.Context(), test.namespace, DefaultPageSize, "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ListSecrets() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGetResource(t *testing.T) {
	client := &Client{Clientset: fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: "ns"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "ns"}},
	)}

	secret, err := client.GetResource(context.Background(), KindSecret, "ns", "secret")
	if err != nil || secret.Kind() != KindSecret {
		t.Fatalf("GetResource(secret) = %#v, %v", secret, err)
	}
	configMap, err := client.GetResource(context.Background(), KindConfigMap, "ns", "config")
	if err != nil || configMap.Kind() != KindConfigMap {
		t.Fatalf("GetResource(configmap) = %#v, %v", configMap, err)
	}
	if _, err := client.GetResource(context.Background(), KindSecret, "ns", "missing"); err == nil {
		t.Fatal("GetResource(missing) error = nil")
	}
	if _, err := client.GetResource(context.Background(), KindPod, "ns", "pod"); err == nil {
		t.Fatal("GetResource(unknown kind) error = nil")
	}
}
