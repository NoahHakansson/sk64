package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGolden_ResourceListAllNamespaces(t *testing.T) {
	h := allNamespacesHarness(t)
	h.golden("resource_list_all_namespaces")
}

func TestResourceItemTitleNamespaceOrdering(t *testing.T) {
	resource := allNamespaceSecret("production", "one")
	styles := testStyles(true)
	for _, test := range []struct {
		name          string
		showNamespace bool
		want          string
	}{
		{name: "single namespace", want: "[S] one  Opaque"},
		{name: "all namespaces", showNamespace: true, want: "[S] production/one  Opaque"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ansi.Strip(resourceItem{resource: resource, styles: styles, showNamespace: test.showNamespace}.Title())
			if got != test.want {
				t.Fatalf("title = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAllNamespacesLongResourceRowFitsWidth(t *testing.T) {
	const width = 60
	screen := newResourceScreen(t.Context(), testClient(), k8s.AllNamespaces, editEnv{}, testStyles(true))
	screen.all = []k8s.Resource{allNamespaceSecret(strings.Repeat("namespace-", 8), strings.Repeat("name-", 12))}
	_ = screen.setVisibleItems()
	screen.SetSize(width, 15)

	row := lineContaining(t, screen.list.View(), "[S]")
	if !strings.HasPrefix(row, "> [S] ") {
		t.Fatalf("row = %q, want badge in the leading identity column", row)
	}
	if got := lipgloss.Width(row); got > width {
		t.Fatalf("row width = %d, want <= %d: %q", got, width, row)
	}
}

func TestAllNamespacesPagination(t *testing.T) {
	screen := newResourceScreen(t.Context(), testClient(), k8s.AllNamespaces, editEnv{}, testStyles(true))
	screen.startLoading()
	reqID := screen.reqID

	_, cmd := screen.Update(resourcesPageMsg{
		reqID: reqID,
		kind:  k8s.KindSecret,
		page: k8s.ResourcePage{
			Items:    []k8s.Resource{allNamespaceSecret("production", "one")},
			Continue: "tok",
		},
	})
	if !screen.pending || !resourceCommandHasKind(cmd, k8s.KindSecret) {
		t.Fatal("first secret page did not keep loading and issue the next secret fetch")
	}
	assertLastListContinueToken(t, screen.client, "tok")
	_, _ = screen.Update(resourcesPageMsg{
		reqID: reqID,
		kind:  k8s.KindSecret,
		page:  k8s.ResourcePage{Items: []k8s.Resource{allNamespaceSecret("staging", "two")}},
	})
	if !screen.pending {
		t.Fatal("resource screen finished before the configmap page")
	}
	_, _ = screen.Update(resourcesPageMsg{
		reqID: reqID,
		kind:  k8s.KindConfigMap,
		page:  k8s.ResourcePage{Items: []k8s.Resource{allNamespaceConfigMap("staging", "three")}},
	})
	if screen.pending || len(screen.all) != 3 {
		t.Fatalf("completed pagination = pending %t items %d, want false and 3", screen.pending, len(screen.all))
	}
}

func TestAllNamespacesRowNamespace(t *testing.T) {
	newScreen := func() *resourceScreen {
		screen := newResourceScreen(t.Context(), testClient(), k8s.AllNamespaces, editEnv{}, testStyles(true))
		screen.all = []k8s.Resource{
			allNamespaceSecret("production", "one"),
			allNamespaceConfigMap("staging", "two"),
		}
		_ = screen.setVisibleItems()
		return screen
	}

	screen := newScreen()
	_, cmd := screen.Update(key("enter"))
	openedKeys := cmd().(pushScreenMsg).s.(*keyScreen)
	if openedKeys.namespace != "production" || openedKeys.kind != k8s.KindSecret || openedKeys.name != "one" {
		t.Fatalf("enter opened %#v", openedKeys)
	}

	screen = newScreen()
	_, cmd = screen.Update(key("r"))
	consumers := cmd().(pushScreenMsg).s.(*consumersScreen)
	if consumers.namespace != "production" || consumers.kind != k8s.KindSecret || consumers.name != "one" {
		t.Fatalf("r opened %#v", consumers)
	}

	screen = newScreen()
	_, cmd = screen.Update(key("D"))
	confirm := cmd().(pushScreenMsg).s.(*deleteConfirm)
	if confirm.namespace != "production" || confirm.kind != k8s.KindSecret || confirm.name != "one" {
		t.Fatalf("D opened %#v", confirm)
	}

	screen = newScreen()
	_, cmd = screen.Update(key("L"))
	picker := cmd().(openProjectPickerMsg)
	link := picker.link.resource
	if link == nil || *link != (store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "one", Source: store.SourceManual}) {
		t.Fatalf("L emitted resource link %#v", link)
	}
}

func TestAllNamespacesFilter(t *testing.T) {
	for _, test := range []struct {
		query string
		name  string
	}{
		{query: "stag", name: "two"},
		{query: "one", name: "one"},
	} {
		t.Run(test.query, func(t *testing.T) {
			h := allNamespacesHarness(t)
			h.keys("/")
			typeFilter(t, h, test.query)
			screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
			visible := screen.list.VisibleItems()
			if len(visible) != 1 || visible[0].(resourceItem).resource.Name() != test.name {
				t.Fatalf("filter %q visible items = %#v", test.query, visible)
			}
		})
	}
}

func TestAllNamespacesCreateBlocked(t *testing.T) {
	h := allNamespacesHarness(t)
	depth := len(h.m.(app).stack)
	h.keys("N")
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	hints := plainFooter(t, screen, 1)
	if len(h.m.(app).stack) != depth || !strings.Contains(screen.View(), "create needs one namespace") || strings.Contains(hints, "N new") {
		t.Fatalf("blocked create = depth %d notice %q hints %q", len(h.m.(app).stack), screen.notice, hints)
	}
	h.keys("down")
	if screen.notice != "" {
		t.Fatalf("next key left notice %q", screen.notice)
	}
}

func TestAllNamespacesToggle(t *testing.T) {
	h := allNamespacesHarness(t)
	h.keys("a")
	if len(h.m.(app).stack) != 1 {
		t.Fatalf("all-namespace a stack depth = %d, want 1", len(h.m.(app).stack))
	}

	h = resourceHarness(t, true)
	depth := len(h.m.(app).stack)
	h.keys("a")
	if len(h.m.(app).stack) != depth || h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen).allNamespaces() {
		t.Fatal("single-namespace a changed the resource screen")
	}
}

func TestAllNamespacesToggleStopsPendingLoad(t *testing.T) {
	screen := newResourceScreen(t.Context(), testClient(), k8s.AllNamespaces, editEnv{}, testStyles(true))
	loadContext, cancel := context.WithCancel(t.Context())
	screen.pending = true
	screen.cancel = cancel

	_, cmd := screen.Update(key("a"))

	if screen.pending {
		t.Fatal("all-namespace toggle left load pending")
	}
	select {
	case <-loadContext.Done():
	default:
		t.Fatal("all-namespace toggle did not cancel load context")
	}
	msg := cmd()
	if _, ok := msg.(popScreenMsg); !ok {
		t.Fatalf("toggle command returned %T, want popScreenMsg", msg)
	}
}

func TestAllNamespacesListChanged(t *testing.T) {
	h := allNamespacesHarness(t)
	all := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	h.send(resourceListChangedMsg{namespace: "other"})
	if !all.pending {
		t.Fatal("all-namespace screen did not refresh for another namespace")
	}

	h = resourceHarness(t, true)
	single := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	h.send(resourceListChangedMsg{namespace: "other"})
	if single.pending {
		t.Fatal("single-namespace screen refreshed for another namespace")
	}
}

func allNamespacesHarness(t *testing.T) *harness {
	t.Helper()
	h := namespaceHarness(t)
	h.keys("a")
	h.send(
		resourcesPageMsg{reqID: h.topReqID(), kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{
			allNamespaceSecret("production", "one"),
		}}},
		resourcesPageMsg{reqID: h.topReqID(), kind: k8s.KindConfigMap, page: k8s.ResourcePage{Items: []k8s.Resource{
			allNamespaceConfigMap("staging", "two"),
		}}},
	)
	return h
}

func allNamespaceSecret(namespace, name string) k8s.Resource {
	return k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
}

func allNamespaceConfigMap(namespace, name string) k8s.Resource {
	return k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
}

func resourceCommandHasKind(cmd tea.Cmd, kind string) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, nested := range msg {
			if resourceCommandHasKind(nested, kind) {
				return true
			}
		}
	case resourcesPageMsg:
		return msg.kind == kind
	}
	return false
}
