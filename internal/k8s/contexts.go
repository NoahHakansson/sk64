package k8s

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/NoahHakansson/sk64/internal/debuglog"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrContextNotFound indicates that a requested kubeconfig context does not exist.
var ErrContextNotFound = errors.New("context not found")

// ContextInfo describes one context from the merged kubeconfig.
type ContextInfo struct {
	Name      string
	Cluster   string
	Server    string
	Namespace string
	Current   bool
}

// SameServer reports whether two API server URLs identify the same endpoint.
func SameServer(first, second string) bool {
	return normalizeServer(first) == normalizeServer(second)
}

func normalizeServer(server string) string {
	parsed, err := url.Parse(server)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return server
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := parsed.Hostname()
	if strings.Contains(hostname, ":") {
		if zoneStart := strings.LastIndexByte(hostname, '%'); zoneStart >= 0 {
			hostname = strings.ToLower(hostname[:zoneStart]) + hostname[zoneStart:]
		} else {
			hostname = strings.ToLower(hostname)
		}
		hostname = "[" + hostname + "]"
	} else {
		hostname = strings.ToLower(hostname)
	}

	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	parsed.Host = hostname
	if port != "" {
		parsed.Host += ":" + port
	}

	escapedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	parsed.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return server
	}
	parsed.RawPath = escapedPath
	return parsed.String()
}

// ResolveContextIdentity resolves a context and its API server without contacting the cluster.
func ResolveContextIdentity(kubeconfig, name string) (ContextInfo, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	config, err := rules.Load()
	if err != nil {
		return ContextInfo{}, fmt.Errorf("load kubeconfig contexts: %w", err)
	}
	kubeContext, found := config.Contexts[name]
	if !found || kubeContext == nil {
		return ContextInfo{}, fmt.Errorf("resolve context %q: %w", name, ErrContextNotFound)
	}
	server := ""
	if cluster := config.Clusters[kubeContext.Cluster]; cluster != nil {
		server = cluster.Server
	}
	return ContextInfo{
		Name:      name,
		Cluster:   kubeContext.Cluster,
		Server:    server,
		Namespace: kubeContext.Namespace,
		Current:   name == config.CurrentContext,
	}, nil
}

// ListContexts reads and sorts contexts from the merged kubeconfig.
func ListContexts(kubeconfig string) ([]ContextInfo, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	config, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig contexts: %w", err)
	}

	contexts := make([]ContextInfo, 0, len(config.Contexts))
	for name, kubeContext := range config.Contexts {
		server := ""
		if cluster := config.Clusters[kubeContext.Cluster]; cluster != nil {
			server = cluster.Server
		}
		contexts = append(contexts, ContextInfo{
			Name: name, Cluster: kubeContext.Cluster, Server: server,
			Namespace: kubeContext.Namespace, Current: name == config.CurrentContext,
		})
	}
	slices.SortFunc(contexts, func(a, b ContextInfo) int { return cmp.Compare(a.Name, b.Name) })
	return contexts, nil
}

// FindContextByServer returns the only context bound to server.
func FindContextByServer(kubeconfig, server string) (ContextInfo, bool, error) {
	if server == "" {
		return ContextInfo{}, false, nil
	}
	contexts, err := ListContexts(kubeconfig)
	if err != nil {
		return ContextInfo{}, false, fmt.Errorf("find context by server: %w", err)
	}
	var match ContextInfo
	found := false
	for _, kubeContext := range contexts {
		if !SameServer(kubeContext.Server, server) {
			continue
		}
		if found {
			return ContextInfo{}, false, nil
		}
		match = kubeContext
		found = true
	}
	return match, found, nil
}

// HasContext reports whether name exists in the merged kubeconfig.
func HasContext(kubeconfig, name string) (bool, error) {
	contexts, err := ListContexts(kubeconfig)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(contexts, func(info ContextInfo) bool { return info.Name == name }), nil
}

// SwitchContext builds and probes a client for contextName and namespace.
func SwitchContext(ctx context.Context, kubeconfig, contextName, namespace string, debug *debuglog.Logger) (*Client, error) {
	client, err := New(Config{Kubeconfig: kubeconfig, Context: contextName, Namespace: namespace, Debug: debug})
	if err != nil {
		return nil, fmt.Errorf("switch to context %q: %w", contextName, err)
	}
	if _, err := client.Probe(ctx); err != nil {
		return nil, fmt.Errorf("probe selected context: %w", err)
	}
	return client, nil
}

// IsExecPluginError reports whether err resembles a credential exec-plugin failure.
// It matches error text because client-go exposes no typed or sentinel error for
// exec credential plugin failures.
func IsExecPluginError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "exec plugin") || strings.Contains(message, "getting credentials: exec")
}
