package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Exists reports whether a supported named object exists in the cluster.
func (c *Client) Exists(ctx context.Context, kind, namespace, name string) (bool, error) {
	var err error
	switch kind {
	case KindNamespace:
		_, err = c.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	case KindDeployment:
		_, err = c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	case KindStatefulSet:
		_, err = c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	case KindDaemonSet:
		_, err = c.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	case KindJob:
		_, err = c.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	case KindCronJob:
		_, err = c.Clientset.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
	case KindSecret:
		_, err = c.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	case KindConfigMap:
		_, err = c.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	default:
		return false, fmt.Errorf("check cluster object: unknown kind %q", kind)
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		if namespace == "" {
			return false, fmt.Errorf("check %s/%s: %w", kind, name, err)
		}
		return false, fmt.Errorf("check %s/%s in namespace %q: %w", kind, name, namespace, err)
	}
	return true, nil
}

// MatchGeneratedNames resolves generated-name prefixes to the matching Secret or
// ConfigMap in the namespace, using metadata-only list requests so values never
// leave the apiserver. Prefixes with no match are absent from the result; a
// prefix matching several names resolves to the lexicographically first, which
// is what a sorted listing would have returned.
func (c *Client) MatchGeneratedNames(ctx context.Context, kind, namespace string, prefixes []string) (map[string]string, error) {
	if c.Metadata == nil {
		return nil, errors.New("match generated names: metadata client is not configured")
	}
	var resource schema.GroupVersionResource
	switch kind {
	case KindSecret:
		resource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	case KindConfigMap:
		resource = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	default:
		return nil, fmt.Errorf("match generated names: unknown kind %q", kind)
	}
	matches := make(map[string]string, len(prefixes))
	continueToken := ""
	for {
		page, err := c.Metadata.Resource(resource).Namespace(namespace).List(ctx, metav1.ListOptions{
			Limit:    DefaultPageSize,
			Continue: continueToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list %s names in namespace %q: %w", kind, namespace, err)
		}
		for _, item := range page.Items {
			for _, prefix := range prefixes {
				if !MatchesGeneratedName(prefix, item.Name) {
					continue
				}
				if current, ok := matches[prefix]; !ok || item.Name < current {
					matches[prefix] = item.Name
				}
			}
		}
		continueToken = page.Continue
		if continueToken == "" {
			return matches, nil
		}
	}
}

// MatchesGeneratedName reports whether name is prefix or a prefix-suffixed generated name.
func MatchesGeneratedName(prefix, name string) bool {
	return name == prefix || strings.HasPrefix(name, prefix+"-")
}
