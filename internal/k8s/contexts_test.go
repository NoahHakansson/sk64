package k8s

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
)

func TestListContexts(t *testing.T) {
	kubeconfig := writeKubeconfig(t, "ctx-b", []kubeContext{
		{name: "ctx-b", namespace: "team-b"},
		{name: "ctx-a", namespace: "team-a"},
	})

	contexts, err := ListContexts(kubeconfig)
	if err != nil {
		t.Fatalf("ListContexts() error = %v", err)
	}
	if len(contexts) != 2 {
		t.Fatalf("ListContexts() length = %d", len(contexts))
	}
	if contexts[0].Name != "ctx-a" || contexts[0].Cluster != "cluster-ctx-a" || contexts[0].Server != "http://127.0.0.1" || contexts[0].Namespace != "team-a" || contexts[0].Current {
		t.Fatalf("first context = %+v", contexts[0])
	}
	if contexts[1].Name != "ctx-b" || contexts[1].Cluster != "cluster-ctx-b" || contexts[1].Server != "http://127.0.0.1" || contexts[1].Namespace != "team-b" || !contexts[1].Current {
		t.Fatalf("second context = %+v", contexts[1])
	}
	if _, err := ListContexts(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ListContexts(missing) error = nil")
	}
}

func TestSameServer(t *testing.T) {
	tests := []struct {
		name          string
		first, second string
		want          bool
	}{
		{name: "identical", first: "https://api.example", second: "https://api.example", want: true},
		{name: "trailing slash", first: "https://api.example/", second: "https://api.example", want: true},
		{name: "HTTPS default port", first: "https://api.example:443", second: "https://api.example", want: true},
		{name: "HTTP default port", first: "http://api.example:80", second: "http://api.example/", want: true},
		{name: "scheme and host case", first: "HTTPS://API.EXAMPLE", second: "https://api.example", want: true},
		{name: "IPv6 address case with same zone", first: "HTTPS://[FE80::A%25Prod]:443/", second: "https://[fe80::a%25Prod]", want: true},
		{name: "terminal encoded slash versus double slash", first: "https://api.example/%2F", second: "https://api.example//", want: false},
		{name: "encoded slash mid-path versus double slash", first: "https://api.example/proxy/%2F", second: "https://api.example/proxy//", want: false},
		{name: "case-distinct IPv6 zones", first: "https://[fe80::1%25Prod]:6443", second: "https://[fe80::1%25prod]:6443", want: false},
		{name: "non-default port", first: "https://api.example:6443", second: "https://api.example", want: false},
		{name: "path prefix", first: "https://api.example/proxy", second: "https://api.example", want: false},
		{name: "different host", first: "https://one.example", second: "https://two.example", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SameServer(test.first, test.second); got != test.want {
				t.Fatalf("SameServer(%q, %q) = %t, want %t", test.first, test.second, got, test.want)
			}
			assertNormalizedServer(t, test.first)
			assertNormalizedServer(t, test.second)
		})
	}
}

func TestNormalizeServerReturnsInvalidInputVerbatim(t *testing.T) {
	tests := []struct {
		name   string
		server string
	}{
		{name: "parse error", server: "https://api.example/%zz"},
		{name: "empty scheme", server: "//api.example/proxy"},
		{name: "empty host", server: "https:///proxy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeServer(test.server); got != test.server {
				t.Fatalf("normalizeServer(%q) = %q, want input verbatim", test.server, got)
			}
		})
	}
}

func assertNormalizedServer(t *testing.T, server string) {
	t.Helper()
	normalized := normalizeServer(server)
	if got := normalizeServer(normalized); got != normalized {
		t.Fatalf("normalizeServer(normalizeServer(%q)) = %q, want %q", server, got, normalized)
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		t.Fatalf("url.Parse(normalizeServer(%q)) error = %v", server, err)
	}
	if parsed.RawPath != "" {
		decodedRawPath, err := url.PathUnescape(parsed.RawPath)
		if err != nil {
			t.Fatalf("url.PathUnescape(%q) error = %v", parsed.RawPath, err)
		}
		if decodedRawPath != parsed.Path {
			t.Fatalf("normalizeServer(%q) produced inconsistent Path %q and RawPath %q", server, parsed.Path, parsed.RawPath)
		}
	}
	if got := parsed.String(); got != normalized {
		t.Fatalf("url.Parse(normalizeServer(%q)).String() = %q, want %q", server, got, normalized)
	}
}

func TestResolveContextIdentity(t *testing.T) {
	const (
		sharedServer = "https://shared.example"
		otherServer  = "https://other.example"
	)
	tests := []struct {
		name               string
		contexts           []kubeContext
		currentContext     string
		contextName        string
		comparisonContexts []kubeContext
		comparisonName     string
		want               ContextInfo
		wantSameServer     bool
		wantNotFound       bool
	}{
		{
			name:               "renamed context keeps server identity",
			contexts:           []kubeContext{{name: "new-name", namespace: "team", server: sharedServer}},
			currentContext:     "new-name",
			contextName:        "new-name",
			comparisonContexts: []kubeContext{{name: "old-name", namespace: "team", server: sharedServer}},
			comparisonName:     "old-name",
			want:               ContextInfo{Name: "new-name", Cluster: "cluster-new-name", Server: sharedServer, Namespace: "team", Current: true},
			wantSameServer:     true,
		},
		{
			name:               "same name points at different server",
			contexts:           []kubeContext{{name: "production", server: otherServer}},
			currentContext:     "production",
			contextName:        "production",
			comparisonContexts: []kubeContext{{name: "production", server: sharedServer}},
			comparisonName:     "production",
			want:               ContextInfo{Name: "production", Cluster: "cluster-production", Server: otherServer, Current: true},
		},
		{
			name:           "missing context",
			contexts:       []kubeContext{{name: "present", server: sharedServer}},
			currentContext: "present",
			contextName:    "missing",
			wantNotFound:   true,
		},
		{
			name:               "different context names share server",
			contexts:           []kubeContext{{name: "team-a", server: sharedServer}, {name: "team-b", server: sharedServer}},
			currentContext:     "team-a",
			contextName:        "team-b",
			comparisonContexts: []kubeContext{{name: "team-a", server: sharedServer}, {name: "team-b", server: sharedServer}},
			comparisonName:     "team-a",
			want:               ContextInfo{Name: "team-b", Cluster: "cluster-team-b", Server: sharedServer},
			wantSameServer:     true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeKubeconfig(t, test.currentContext, test.contexts)
			identity, err := ResolveContextIdentity(path, test.contextName)
			if test.wantNotFound {
				if !errors.Is(err, ErrContextNotFound) {
					t.Fatalf("ResolveContextIdentity() error = %v, want ErrContextNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveContextIdentity() error = %v", err)
			}
			if identity != test.want {
				t.Fatalf("ResolveContextIdentity() = %+v, want %+v", identity, test.want)
			}
			if test.comparisonName == "" {
				return
			}
			comparisonPath := writeKubeconfig(t, test.comparisonName, test.comparisonContexts)
			comparison, err := ResolveContextIdentity(comparisonPath, test.comparisonName)
			if err != nil {
				t.Fatalf("comparison ResolveContextIdentity() error = %v", err)
			}
			if gotSameServer := identity.Server == comparison.Server; gotSameServer != test.wantSameServer {
				t.Fatalf("servers %q and %q same = %t, want %t", identity.Server, comparison.Server, gotSameServer, test.wantSameServer)
			}
		})
	}
}

func TestFindContextByServer(t *testing.T) {
	const server = "https://shared.example"
	tests := []struct {
		name      string
		contexts  []kubeContext
		server    string
		want      ContextInfo
		wantFound bool
	}{
		{
			name: "unique match",
			contexts: []kubeContext{
				{name: "renamed", namespace: "production", server: server},
				{name: "other", server: "https://other.example"},
			},
			server: server,
			want: ContextInfo{
				Name: "renamed", Cluster: "cluster-renamed", Server: server, Namespace: "production",
			},
			wantFound: true,
		},
		{
			name:     "normalized unique match",
			contexts: []kubeContext{{name: "renamed", namespace: "production", server: server + ":443/"}},
			server:   server,
			want: ContextInfo{
				Name: "renamed", Cluster: "cluster-renamed", Server: server + ":443/", Namespace: "production",
			},
			wantFound: true,
		},
		{
			name:     "no match",
			contexts: []kubeContext{{name: "other", server: "https://other.example"}},
			server:   server,
		},
		{
			name: "ambiguous match",
			contexts: []kubeContext{
				{name: "first", server: server},
				{name: "second", server: server},
			},
			server: server,
		},
		{
			name:     "empty server",
			contexts: []kubeContext{{name: "context", server: server}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kubeconfig := writeKubeconfig(t, "", test.contexts)
			match, found, err := FindContextByServer(kubeconfig, test.server)
			if err != nil {
				t.Fatalf("FindContextByServer() error = %v", err)
			}
			if found != test.wantFound || match != test.want {
				t.Fatalf("FindContextByServer() = %+v, %t, want %+v, %t", match, found, test.want, test.wantFound)
			}
		})
	}
}

func TestHasContext(t *testing.T) {
	kubeconfig := writeKubeconfig(t, "ctx-b", []kubeContext{{name: "ctx-a"}, {name: "ctx-b"}})

	found, err := HasContext(kubeconfig, "ctx-a")
	if err != nil || !found {
		t.Fatalf("HasContext(existing) = %v, %v", found, err)
	}
	found, err = HasContext(kubeconfig, "missing")
	if err != nil || found {
		t.Fatalf("HasContext(missing) = %v, %v", found, err)
	}
	if _, err := HasContext(filepath.Join(t.TempDir(), "missing"), "ctx-a"); err == nil {
		t.Fatal("HasContext(invalid kubeconfig) error = nil")
	}
}

func TestIsExecPluginError(t *testing.T) {
	execError := fmt.Errorf("switch context: %w", errors.New("getting credentials: exec: executable failed"))
	if !IsExecPluginError(execError) {
		t.Fatal("IsExecPluginError(exec error) = false")
	}
	if IsExecPluginError(errors.New("context deadline exceeded")) {
		t.Fatal("IsExecPluginError(timeout) = true")
	}
}
