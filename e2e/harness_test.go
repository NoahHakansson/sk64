package e2e

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	envtestVersion  = "v1.36.2"
	envtestIndexURL = "https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/2b2bbdfb5ed4d62cf0c665425c2a424223c46df8/envtest-releases.yaml"
)

var controlPlane *envtest.Environment

func startControlPlane() (*rest.Config, func() error, error) {
	assets, err := envtest.SetupEnvtestDefaultBinaryAssetsDirectory()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve envtest binary directory: %w", err)
	}
	if err := scrubAmbientCluster(); err != nil {
		return nil, nil, err
	}

	useExistingCluster := false
	controlPlane = &envtest.Environment{
		UseExistingCluster:           &useExistingCluster,
		BinaryAssetsDirectory:        assets,
		DownloadBinaryAssets:         true,
		DownloadBinaryAssetsVersion:  envtestVersion,
		DownloadBinaryAssetsIndexURL: envtestIndexURL,
		ControlPlaneStartTimeout:     3 * time.Minute,
		ControlPlaneStopTimeout:      time.Minute,
	}

	cfg, err := controlPlane.Start()
	if err != nil {
		return nil, nil, fmt.Errorf("start envtest control plane: %w", err)
	}
	if err := requireLoopback(cfg); err != nil {
		return nil, nil, errors.Join(err, controlPlane.Stop())
	}
	return cfg, controlPlane.Stop, nil
}

// scrubAmbientCluster makes the developer's kubeconfig unreachable for the
// lifetime of the test process. Nothing in the suite loads it, and if a future
// import tried, it would resolve to an empty directory and fail.
func scrubAmbientCluster() error {
	home, err := os.MkdirTemp("", "sk64-e2e-home")
	if err != nil {
		return fmt.Errorf("create isolated HOME: %w", err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		return fmt.Errorf("isolate HOME: %w", err)
	}
	if err := os.Setenv("KUBECONFIG", filepath.Join(home, "no-such-kubeconfig")); err != nil {
		return fmt.Errorf("isolate KUBECONFIG: %w", err)
	}
	if err := os.Unsetenv("USE_EXISTING_CLUSTER"); err != nil {
		return fmt.Errorf("clear USE_EXISTING_CLUSTER: %w", err)
	}
	if err := os.Unsetenv("KUBERNETES_SERVICE_HOST"); err != nil {
		return fmt.Errorf("disable in-cluster host fallback: %w", err)
	}
	if err := os.Unsetenv("KUBERNETES_SERVICE_PORT"); err != nil {
		return fmt.Errorf("disable in-cluster port fallback: %w", err)
	}
	return nil
}

// requireLoopback refuses any apiserver that is not on this machine. The
// harness builds the only rest.Config the suite sees; this asserts that config
// still points at the control plane the harness itself started.
func requireLoopback(cfg *rest.Config) error {
	raw := cfg.Host
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse apiserver host %q: %w", cfg.Host, err)
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to run against apiserver %q: the e2e suite only ever talks to a loopback control plane it started itself", cfg.Host)
}
