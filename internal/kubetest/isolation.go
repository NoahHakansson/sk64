// Package kubetest provides test-only safeguards for code that can load a Kubernetes client.
package kubetest

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"
)

// IsolateAmbientCluster makes the ambient cluster unreachable for the whole
// test binary: kubeconfig loading rules resolve inside an empty temp dir and
// the in-cluster fallback is disabled. Call it from TestMain before m.Run.
func IsolateAmbientCluster() error {
	home, err := os.MkdirTemp("", "sk64-test-home")
	if err != nil {
		return fmt.Errorf("create isolated HOME: %w", err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		return fmt.Errorf("isolate HOME: %w", err)
	}
	if err := os.Setenv("KUBECONFIG", filepath.Join(home, "absent")); err != nil {
		return fmt.Errorf("isolate KUBECONFIG: %w", err)
	}
	clientcmd.RecommendedHomeFile = filepath.Join(home, ".kube", "config")
	if err := os.Unsetenv("KUBERNETES_SERVICE_HOST"); err != nil {
		return fmt.Errorf("disable in-cluster host fallback: %w", err)
	}
	if err := os.Unsetenv("KUBERNETES_SERVICE_PORT"); err != nil {
		return fmt.Errorf("disable in-cluster port fallback: %w", err)
	}
	return nil
}
