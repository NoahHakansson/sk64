package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// DefaultPageSize is the page size used for Kubernetes list operations.
	DefaultPageSize = 500
	// AllNamespaces is the empty namespace value that makes ListSecrets and
	// ListConfigMaps span every namespace the caller can read.
	AllNamespaces = metav1.NamespaceAll
)

// NamespacePage is one page of namespace names in server order.
type NamespacePage struct {
	Names    []string
	Continue string
}

// ResourcePage is one page of Secrets or ConfigMaps.
type ResourcePage struct {
	Items    []Resource
	Continue string
}

// ListNamespaces returns one page of namespace names.
func (c *Client) ListNamespaces(ctx context.Context, limit int64, continueToken string) (NamespacePage, error) {
	result, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return NamespacePage{}, fmt.Errorf("list namespaces: %w", err)
	}

	names := make([]string, len(result.Items))
	for i := range result.Items {
		names[i] = result.Items[i].Name
	}
	return NamespacePage{Names: names, Continue: result.Continue}, nil
}

// ListSecrets returns one page of Secrets from namespace.
func (c *Client) ListSecrets(ctx context.Context, namespace string, limit int64, continueToken string) (ResourcePage, error) {
	result, err := c.Clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return ResourcePage{}, fmt.Errorf("list secrets in %s: %w", namespaceLabel(namespace), err)
	}

	items := make([]Resource, 0, len(result.Items))
	for i := range result.Items {
		item := result.Items[i]
		items = append(items, NewSecret(&item))
	}
	c.Debug.Count("list-secrets", namespace, len(items))
	return ResourcePage{Items: items, Continue: result.Continue}, nil
}

// ListConfigMaps returns one page of ConfigMaps from namespace.
func (c *Client) ListConfigMaps(ctx context.Context, namespace string, limit int64, continueToken string) (ResourcePage, error) {
	result, err := c.Clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return ResourcePage{}, fmt.Errorf("list configmaps in %s: %w", namespaceLabel(namespace), err)
	}

	items := make([]Resource, 0, len(result.Items))
	for i := range result.Items {
		item := result.Items[i]
		items = append(items, NewConfigMap(&item))
	}
	c.Debug.Count("list-configmaps", namespace, len(items))
	return ResourcePage{Items: items, Continue: result.Continue}, nil
}

func namespaceLabel(namespace string) string {
	if namespace == AllNamespaces {
		return "all namespaces"
	}
	return fmt.Sprintf("namespace %q", namespace)
}

// GetResource fetches one Secret or ConfigMap by kind, namespace, and name.
func (c *Client) GetResource(ctx context.Context, kind, namespace, name string) (Resource, error) {
	switch kind {
	case KindSecret:
		secret, err := c.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get secret %q in namespace %q: %w", name, namespace, err)
		}
		return NewSecret(secret), nil
	case KindConfigMap:
		configMap, err := c.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get configmap %q in namespace %q: %w", name, namespace, err)
		}
		return NewConfigMap(configMap), nil
	default:
		return nil, fmt.Errorf("get resource %q in namespace %q: unknown kind %q", name, namespace, kind)
	}
}
