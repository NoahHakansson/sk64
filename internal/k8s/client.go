package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/NoahHakansson/sk64/internal/debuglog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Config selects how the kube client is built. Zero value = defaults
// (merged $KUBECONFIG list or ~/.kube/config, kubeconfig current-context,
// context's default namespace).
type Config struct {
	Kubeconfig string           // explicit path from --kubeconfig; "" = default loading rules
	Context    string           // context override from --context; "" = current-context
	Namespace  string           // namespace override from --namespace/-n; "" = context default
	Debug      *debuglog.Logger // opt-in scrubbed operational log; nil disables logging
}

// Client is a connected-config handle. Fields are set by New; tests may
// construct Client directly with a fake clientset.
type Client struct {
	Clientset kubernetes.Interface
	// Metadata serves the metadata-only listings used by MatchGeneratedNames.
	// NewForConfig populates it; a Client built by hand in a test without it
	// makes every generated-name check fail.
	Metadata  metadata.Interface
	Context   string           // effective context name
	Namespace string           // effective namespace
	Cluster   string           // cluster name from kubeconfig; empty when unknown
	Server    string           // API server URL from the effective REST config
	Debug     *debuglog.Logger // scrubbed operational log; nil is a no-op
}

// New builds a Kubernetes client from kubeconfig loading rules and overrides.
func New(cfg Config) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.Kubeconfig != "" {
		rules.ExplicitPath = cfg.Kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{CurrentContext: cfg.Context}
	if cfg.Namespace != "" {
		overrides.Context.Namespace = cfg.Namespace
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	namespace, _, err := clientConfig.Namespace()
	if err != nil {
		return nil, fmt.Errorf("resolve kubeconfig namespace: %w", err)
	}

	rawConfig, err := clientConfig.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	contextName := cfg.Context
	if contextName == "" {
		contextName = rawConfig.CurrentContext
	}
	clusterName := ""
	if kubeContext := rawConfig.Contexts[contextName]; kubeContext != nil {
		clusterName = kubeContext.Cluster
	}

	client, err := NewForConfig(restConfig, namespace)
	if err != nil {
		return nil, err
	}
	client.Context = contextName
	client.Cluster = clusterName
	client.Debug = cfg.Debug
	return client, nil
}

// NewForConfig builds a Client from an explicit REST config. Unlike New it
// never consults kubeconfig loading rules, so a caller that must not reach an
// ambient cluster can only talk to the apiserver it built the config for.
func NewForConfig(restConfig *rest.Config, namespace string) (*Client, error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	metadataClient, err := metadata.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes metadata client: %w", err)
	}
	return &Client{
		Clientset: clientset,
		Metadata:  metadataClient,
		Context:   restConfig.Host,
		Namespace: namespace,
		Cluster:   restConfig.Host,
		Server:    restConfig.Host,
	}, nil
}

// Probe performs one cheap, RBAC-free API call (GET /version) honoring ctx.
// Returns the server version on success.
func (c *Client) Probe(ctx context.Context) (*version.Info, error) {
	body, err := c.Clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return nil, fmt.Errorf("cannot reach cluster (context %q): %w", c.Context, err)
	}

	serverVersion := &version.Info{}
	if err := json.Unmarshal(body, serverVersion); err != nil {
		return nil, fmt.Errorf("decode /version response (context %q): %w", c.Context, err)
	}
	return serverVersion, nil
}

// CountSecrets returns the number of Secrets in namespace, listing with
// pagination (Limit + Continue).
func (c *Client) CountSecrets(ctx context.Context, namespace string) (int, error) {
	count := 0
	continueToken := ""
	for {
		secrets, err := c.Clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
			Limit:    DefaultPageSize,
			Continue: continueToken,
		})
		if err != nil {
			return 0, fmt.Errorf("count secrets in namespace %q: %w", namespace, err)
		}

		count += len(secrets.Items)
		continueToken = secrets.Continue
		if continueToken == "" {
			return count, nil
		}
	}
}
