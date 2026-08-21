package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/config"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/NoahHakansson/sk64/internal/undo"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestGolden_NamespaceLoading(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	h.golden("namespace_loading")
}

func TestGolden_NamespaceList(t *testing.T) {
	h := namespaceHarness(t)
	h.golden("namespace_list")
}

func TestGolden_NamespacePaged(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	reqID := h.topReqID()
	h.send(
		namespacesPageMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"default", "kube-public"}, Continue: "next"}},
		namespacesPageMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"kube-system", "production", "staging"}}},
	)
	h.golden("namespace_list")
}

func TestNamespacePaginationForwardsContinuation(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	h.send(namespacesPageMsg{
		reqID: h.topReqID(),
		page:  k8s.NamespacePage{Names: []string{"default"}, Continue: "next-namespaces"},
	})
	assertLastListContinueToken(t, h.m.(app).client, "next-namespaces")
}

func TestGolden_NamespaceError(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	h.send(namespacesPageMsg{reqID: h.topReqID(), err: errors.New("API unavailable")})
	h.golden("namespace_error")
}

func TestNamespaceForbiddenFallbackSucceeds(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	screen := h.m.(app).stack[0].(*namespaceScreen)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", errors.New("denied"))
	h.m.(app).client.Clientset.(*fake.Clientset).PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden
	})
	h.send(screen.fetchPage(t.Context(), screen.reqID, "")())

	model := h.m.(app)
	screen = model.stack[0].(*namespaceScreen)
	if model.fatal != nil || h.sawQuit {
		t.Fatalf("fallback fatal/quit = %q/%t", model.fatal, h.sawQuit)
	}
	if len(screen.names) != 1 || screen.names[0] != "default" {
		t.Fatalf("fallback names = %v, want [default]", screen.names)
	}
	view := ansi.Strip(screen.View())
	line := lineContaining(t, view, "namespace list forbidden")
	if want := "[incomplete] namespace list forbidden; showing kubeconfig namespace"; line != want {
		t.Fatalf("fallback state line = %q, want %q", line, want)
	}
}

func TestNamespaceForbiddenFallbackExits(t *testing.T) {
	t.Run("fallback namespace forbidden", func(t *testing.T) {
		h := newHarness(t, Options{ASCII: true})
		model := h.m.(app)
		screen := model.stack[0].(*namespaceScreen)
		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "items"}, "", errors.New("denied"))
		clientset := model.client.Clientset.(*fake.Clientset)
		clientset.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, forbidden
		})
		clientset.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, forbidden
		})
		h.send(screen.fetchPage(t.Context(), screen.reqID, "")())
		model = h.m.(app)
		if !h.sawQuit || model.fatal == nil || !strings.Contains(model.fatal.Error(), `namespace "default"`) {
			t.Fatalf("fallback fatal/quit = %q/%t", model.fatal, h.sawQuit)
		}
	})

	t.Run("no context namespace", func(t *testing.T) {
		h := newHarness(t, Options{ASCII: true})
		model := h.m.(app)
		model.client.Namespace = ""
		h.m = model
		screen := model.stack[0].(*namespaceScreen)
		screen.client.Namespace = ""
		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", errors.New("denied"))
		h.send(namespacesPageMsg{reqID: screen.reqID, err: forbidden})
		model = h.m.(app)
		if !h.sawQuit || model.fatal == nil || model.fatal.Error() != "cannot list namespaces and no context namespace to fall back to" {
			t.Fatalf("empty namespace fatal/quit = %q/%t", model.fatal, h.sawQuit)
		}
	})

	t.Run("canceled fallback is not fatal", func(t *testing.T) {
		h := newHarness(t, Options{ASCII: true})
		model := h.m.(app)
		screen := model.stack[0].(*namespaceScreen)
		model.client.Clientset.(*fake.Clientset).PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, context.Canceled
		})
		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", errors.New("denied"))
		h.send(namespacesPageMsg{reqID: screen.reqID, err: forbidden})
		model = h.m.(app)
		if model.fatal != nil || h.sawQuit {
			t.Fatalf("canceled fallback fatal/quit = %q/%t", model.fatal, h.sawQuit)
		}
	})
}

func TestGolden_ResourceList(t *testing.T) {
	h := resourceHarness(t, false)
	h.golden("resource_list")
}

func TestGolden_ResourceList60(t *testing.T) {
	immutable := true
	h := newHarness(t, Options{StartNamespace: "default", ASCII: true})
	reqID := h.topReqID()
	h.send(
		resourcesPageMsg{reqID: reqID, kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{
			k8s.NewSecret(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("long-secret-name-", 4) + "tail", Namespace: "default"},
				Type:       corev1.SecretTypeTLS,
				Immutable:  &immutable,
			}),
		}}},
		resourcesPageMsg{reqID: reqID, kind: k8s.KindConfigMap, page: k8s.ResourcePage{}},
		tea.WindowSizeMsg{Width: 60, Height: 18},
	)
	h.golden("resource_list_60")
}

func TestGolden_ResourceTypeCycle(t *testing.T) {
	h := resourceHarness(t, false)
	h.keys("t")
	h.golden("resource_secrets")
	h.keys("t")
	h.golden("resource_configmaps")
	h.keys("t")
	h.golden("resource_list")
}

func TestGolden_ResourceFilter(t *testing.T) {
	h := resourceHarness(t, false)
	h.keys("/")
	typeFilter(t, h, "app")
	h.golden("resource_filter")
}

func TestResourceFilterCapturesTypeCycleAndEnter(t *testing.T) {
	h := resourceHarness(t, false)
	initialDepth := len(h.m.(app).stack)
	h.keys("/")
	typeFilter(t, h, "tls")
	h.keys("enter")

	model := h.m.(app)
	screen := model.stack[len(model.stack)-1].(*resourceScreen)
	if screen.filter != filterAll {
		t.Fatalf("resource type filter = %d, want all", screen.filter)
	}
	if screen.list.SettingFilter() {
		t.Fatal("enter did not apply the name filter")
	}
	if len(model.stack) != initialDepth {
		t.Fatalf("stack depth = %d, want %d", len(model.stack), initialDepth)
	}
}

func TestGolden_NavigationStack(t *testing.T) {
	h := newHarness(t, Options{ASCII: true, ReadOnly: true})
	h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{
		"default", "kube-public", "kube-system", "production", "staging",
	}}})
	h.golden("namespace_list")
	h.keys("enter")
	h.send(resourceMessages(h.topReqID())...)
	h.golden("resource_list_read_only")
	h.keys("enter")
	resource := navigationSecret()
	h.send(resourceLoadedMsg{reqID: h.topReqID(), res: resource})
	h.golden("navigation_keys")
	h.keys("enter")
	h.golden("value_view")
	h.keys("esc")
	h.golden("navigation_keys")
	h.keys("esc")
	h.golden("resource_list_read_only")
	h.keys("esc")
	h.golden("namespace_list")
}

func TestGolden_KeyList(t *testing.T) {
	keyListAppearanceHarness(t, true).golden("key_list")
}

func TestGolden_KeyListUnicode(t *testing.T) {
	keyListAppearanceHarness(t, false).golden("key_list_unicode")
}

func TestGolden_KeyList60(t *testing.T) {
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "long-key-secret", Namespace: "default"},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			strings.Repeat("long-binary-key-", 4) + "tail": {0, 1, 2},
		},
	})
	h := keyHarness(t, resource)
	h.send(tea.WindowSizeMsg{Width: 60, Height: 18})
	h.golden("key_list_60")
}

func TestGolden_ValueView(t *testing.T) {
	h := keyHarnessOptions(t, navigationSecret(), Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
	h.keys("enter")
	h.golden("value_view")
}

func TestValueViewErrorUsesStateLine(t *testing.T) {
	resource := navigationSecret()
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "read failed", err: errors.New("read failed")},
		{name: "key missing", err: errors.New("key missing")},
	} {
		t.Run(test.name, func(t *testing.T) {
			screen := newValueScreen(valueGetErrorResource{Resource: resource, err: test.err}, "config", editEnv{}, testStyles(true))
			screen.SetSize(80, 20)

			line := lineContaining(t, ansi.Strip(screen.View()), test.err.Error())
			want := "[error] value unavailable: " + test.err.Error()
			if line != want {
				t.Fatalf("error state line = %q, want %q", line, want)
			}
		})
	}
}

type valueGetErrorResource struct {
	k8s.Resource
	err error
}

func (r valueGetErrorResource) Get(string) ([]byte, error) {
	return nil, r.err
}

func TestGolden_ValueViewJSON(t *testing.T) {
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "json-secret", Namespace: "default"},
		Data:       map[string][]byte{"TENANTS": []byte(`[{"id":"district-sodervik","organization_id":"org_0TEST"}]`)},
	})
	h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
	h.keys("enter")
	h.golden("value_view_json")
}

func TestGolden_ValueViewWrap(t *testing.T) {
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "long-secret", Namespace: "default"},
		Data:       map[string][]byte{"value": []byte(strings.Repeat("x", 200))},
	})
	h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
	h.keys("enter")
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*valueScreen)
	lines := strings.Split(ansi.Strip(screen.viewport.View()), "\n")
	contentLines := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			contentLines++
		}
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("wrapped line width = %d, want <= 80: %q", width, line)
		}
	}
	if contentLines != 3 {
		t.Fatalf("wrapped value content lines = %d, want 3: %q", contentLines, lines)
	}
	h.golden("value_view_wrap")
}

func TestGolden_HexView(t *testing.T) {
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "binary-secret", Namespace: "default"},
		Data:       map[string][]byte{"payload": {0x53, 0x65, 0x63, 0x72, 0x65, 0x74, 0, 1, 2, 0xff}},
	})
	h := keyHarness(t, resource)
	h.keys("enter")
	h.golden("hex_view")
}

func TestGolden_ContextOverlay(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("ctrl+k")
	overlay := h.m.(app).overlay.(*contextOverlay)
	h.send(contextsLoadedMsg{reqID: overlay.reqID, contexts: contextFixtures()})
	h.golden("context_overlay")
}

func TestGolden_ContextOverlayExecOffer(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("ctrl+k")
	overlay := h.m.(app).overlay.(*contextOverlay)
	h.send(contextsLoadedMsg{reqID: overlay.reqID, contexts: contextFixtures()})
	h.keys("down", "enter")
	overlay = h.m.(app).overlay.(*contextOverlay)
	h.send(contextProbedMsg{reqID: overlay.reqID, name: "work-ctx", err: errors.New("getting credentials: exec: login required")})
	h.golden("context_overlay_exec_offer")
}

func TestGolden_Resize(t *testing.T) {
	h := resourceHarness(t, false)
	h.golden("resource_list")
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.golden("resource_list_100x30")
}

func TestGolden_MinSize(t *testing.T) {
	h := resourceHarness(t, false)
	h.golden("resource_list")
	h.send(tea.WindowSizeMsg{Width: 40, Height: 10})
	h.golden("terminal_too_small")
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.golden("resource_list")
}

func TestTooSmallProcessesAsyncMessages(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	h.send(tea.WindowSizeMsg{Width: 40, Height: 10})
	h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{"default"}}})
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})

	screen := h.m.(app).stack[0].(*namespaceScreen)
	if screen.pending {
		t.Fatal("namespace screen remains pending after async result")
	}
	if len(screen.names) != 1 || screen.names[0] != "default" {
		t.Fatalf("namespace names = %v, want [default]", screen.names)
	}
}

func TestGolden_ASCIIMarkers(t *testing.T) {
	h := resourceHarness(t, true)
	h.golden("resource_list_ascii")
}

func TestFilterCapturesQuit(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("/", "q")

	model := h.m.(app)
	screen := model.stack[len(model.stack)-1].(*namespaceScreen)
	if screen.list.FilterInput.Value() != "q" {
		t.Fatalf("filter value = %q, want q", screen.list.FilterInput.Value())
	}
	if h.sawQuit {
		t.Fatal("q produced tea.QuitMsg")
	}
}

func TestControlCArmsWhileFiltering(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("/", "ctrl+c")
	if h.sawQuit || !h.m.(app).quitArm.armed {
		t.Fatalf("first ctrl+c quit = %t, armed = %t", h.sawQuit, h.m.(app).quitArm.armed)
	}
	if !strings.Contains(h.view(), "press ctrl+c again to quit") {
		t.Fatalf("armed footer missing warning:\n%s", h.view())
	}
	warning := h.m.(app).styles.warnText.Inline(true).Render("press ctrl+c again to quit")
	if !strings.Contains(h.m.View().Content, warning) {
		t.Fatal("armed footer does not use warning style")
	}

	h.keys("x")
	model := h.m.(app)
	screen := model.stack[len(model.stack)-1].(*namespaceScreen)
	if model.quitArm.armed {
		t.Fatal("other key did not disarm quit")
	}
	if got := screen.list.FilterInput.Value(); got != "x" {
		t.Fatalf("filter value = %q, want x", got)
	}

	h.keys("ctrl+c", "ctrl+c")
	if !h.sawQuit {
		t.Fatal("second ctrl+c did not produce tea.QuitMsg")
	}
}

func TestDefaultQuitKeyIsUppercaseQ(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("q")
	if h.sawQuit {
		t.Fatal("lowercase q produced tea.QuitMsg")
	}
	h.keys("Q")
	if !h.sawQuit {
		t.Fatal("uppercase Q did not produce tea.QuitMsg")
	}
}

func TestOverlayCapturesInput(t *testing.T) {
	h := namespaceHarness(t)
	initialDepth := len(h.m.(app).stack)
	h.keys("ctrl+k")
	overlay := h.m.(app).overlay.(*contextOverlay)
	h.send(contextsLoadedMsg{reqID: overlay.reqID, contexts: contextFixtures()})
	h.keys("ctrl+c")
	if h.sawQuit || !h.m.(app).quitArm.armed || h.m.(app).overlay == nil {
		t.Fatalf("overlay ctrl+c quit = %t, armed = %t, overlay = %T", h.sawQuit, h.m.(app).quitArm.armed, h.m.(app).overlay)
	}
	h.keys("q")
	if h.m.(app).quitArm.armed {
		t.Fatal("overlay input did not disarm quit")
	}
	if h.m.(app).overlay == nil {
		t.Fatal("q closed the overlay")
	}
	if h.sawQuit {
		t.Fatal("q produced tea.QuitMsg")
	}
	h.keys("esc")
	model := h.m.(app)
	if model.overlay != nil {
		t.Fatal("esc did not close the overlay")
	}
	if len(model.stack) != initialDepth {
		t.Fatalf("stack depth = %d, want %d", len(model.stack), initialDepth)
	}
}

func TestQuitArmExpiresAndRejectsStaleTicks(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("ctrl+c")
	firstID := h.m.(app).quitArm.id
	h.send(quitArmExpiredMsg{id: firstID})
	if h.m.(app).quitArm.armed {
		t.Fatal("matching timeout did not disarm quit")
	}

	h.keys("ctrl+c")
	secondID := h.m.(app).quitArm.id
	if secondID == firstID {
		t.Fatal("new arm reused the previous timeout id")
	}
	h.send(quitArmExpiredMsg{id: firstID})
	if !h.m.(app).quitArm.armed {
		t.Fatal("stale timeout disarmed the newer arm")
	}
	h.send(quitArmExpiredMsg{id: secondID})
	if h.m.(app).quitArm.armed {
		t.Fatal("current timeout did not disarm quit")
	}
}

func TestGolden_QuitArmed(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("ctrl+c")
	h.golden("quit_armed")
}

func TestRefreshDedup(t *testing.T) {
	h := newHarness(t, Options{StartNamespace: "default", ASCII: true})
	model := h.m.(app)
	screen := model.stack[len(model.stack)-1].(*resourceScreen)
	reqID := screen.reqID
	if !screen.pending {
		t.Fatalf("initial loader reqID %d is not pending", reqID)
	}
	h.keys("ctrl+r")
	if screen.reqID != reqID {
		t.Fatalf("reqID after duplicate refresh = %d, want %d", screen.reqID, reqID)
	}
}

func TestEscCancelsInFlight(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	model := h.m.(app)
	screen := model.stack[0].(*namespaceScreen)
	reqID := screen.reqID
	requestContext := screen.loadContext
	h.keys("esc")
	if screen.pending {
		t.Fatal("screen remains pending after esc")
	}
	select {
	case <-requestContext.Done():
	default:
		t.Fatal("request context was not cancelled")
	}
	if len(h.m.(app).stack) != 1 {
		t.Fatal("esc popped the loading root screen")
	}
	want := h.view()
	h.send(namespacesPageMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"stale"}}})
	if got := h.view(); got != want {
		t.Fatalf("stale result changed view\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestContextSwitchIgnoresDiscardedScreenResult(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	oldScreen := h.m.(app).stack[0].(*namespaceScreen)
	oldReqID := oldScreen.reqID
	oldContext := oldScreen.loadContext
	client := *h.m.(app).client
	client.Context = "other-ctx"

	h.send(contextSwitchedMsg{client: &client})

	newScreen := h.m.(app).stack[0].(*namespaceScreen)
	if newScreen.reqID == oldReqID {
		t.Fatalf("new reqID = old reqID = %d", oldReqID)
	}
	select {
	case <-oldContext.Done():
	default:
		t.Fatal("discarded screen request context was not cancelled")
	}

	h.send(namespacesPageMsg{reqID: oldReqID, page: k8s.NamespacePage{Names: []string{"stale"}}})
	if !newScreen.pending {
		t.Fatal("new screen stopped pending after discarded result")
	}
	if len(newScreen.names) != 0 {
		t.Fatalf("new screen names = %v, want none", newScreen.names)
	}
}

func TestClientReplacementScopesUndoHistoryToServer(t *testing.T) {
	routes := []struct {
		name string
		open func(*testing.T, *harness, *k8s.Client)
	}{
		{
			name: "context switch",
			open: func(t *testing.T, h *harness, client *k8s.Client) {
				t.Helper()
				h.send(contextSwitchedMsg{client: client})
			},
		},
		{
			name: "project open",
			open: func(t *testing.T, h *harness, client *k8s.Client) {
				t.Helper()
				st := h.m.(app).store
				project := createProject(t, st, "api", "/repos/api", client.Context, client.Namespace)
				h.send(projectOpenedMsg{project: project, client: client})
			},
		},
	}
	servers := []struct {
		name    string
		server  string
		wantLen int
	}{
		{name: "equivalent spelling", server: "HTTPS://TEST.EXAMPLE:443/", wantLen: 1},
		{name: "different endpoint", server: "https://other.example", wantLen: 0},
	}
	for _, route := range routes {
		for _, server := range servers {
			t.Run(route.name+"/"+server.name, func(t *testing.T) {
				st := newTestStore(t)
				h := newHarness(t, Options{ASCII: true, Store: st})
				model := h.m.(app)
				model.editEnv.ring.Push(undo.Entry{Context: model.client.Context, Kind: k8s.KindSecret, Namespace: "default", Name: "credentials"})
				h.m = model
				client := *model.client
				client.Server = server.server

				route.open(t, h, &client)

				if got := h.m.(app).editEnv.ring.Len(); got != server.wantLen {
					t.Fatalf("undo ring length = %d, want %d", got, server.wantLen)
				}
			})
		}
	}
}

func TestResourceScreenKeepsFirstError(t *testing.T) {
	h := newHarness(t, Options{StartNamespace: "default", ASCII: true})
	firstErr := errors.New("secrets forbidden")
	secondErr := errors.New("configmaps timed out")
	reqID := h.topReqID()

	h.send(
		resourcesPageMsg{reqID: reqID, kind: k8s.KindSecret, err: firstErr},
		resourcesPageMsg{reqID: reqID, kind: k8s.KindConfigMap, err: secondErr},
	)

	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	if !errors.Is(screen.err, firstErr) {
		t.Fatalf("resource error = %v, want first error %v", screen.err, firstErr)
	}
}

func TestAsyncListScreensClaimOneState(t *testing.T) {
	tests := []struct {
		name          string
		view          func(*testing.T, string) string
		errorMarker   string
		errorAction   string
		emptyAction   string
		populatedText string
	}{
		{name: "namespaces", view: namespaceAsyncStateView, errorMarker: "[error]", errorAction: "ctrl+r", emptyAction: "ctrl+r", populatedText: "default"},
		{name: "resources", view: resourceAsyncStateView, errorMarker: "[error]", errorAction: "ctrl+r", emptyAction: "N to create", populatedText: "app-secret"},
		{name: "keys", view: keyAsyncStateView, errorMarker: "[error]", errorAction: "ctrl+r", emptyAction: "N to create", populatedText: "config"},
		{name: "workloads", view: workloadAsyncStateView, errorMarker: "[incomplete]", errorAction: "ctrl+r", emptyAction: "ctrl+r", populatedText: "Deployment  web"},
		{name: "consumers", view: consumerAsyncStateView, errorMarker: "[incomplete]", errorAction: "ctrl+r", emptyAction: "ctrl+r", populatedText: "Deployment/web"},
		{name: "project overlay", view: projectOverlayAsyncStateView, errorMarker: "[error]", errorAction: "enter", emptyAction: "N to create", populatedText: "api"},
	}
	states := []string{"pending", "error", "empty", "populated"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, state := range states {
				t.Run(state, func(t *testing.T) {
					view := test.view(t, state)
					if strings.Contains(view, "No items.") {
						t.Fatalf("%s view rendered Bubbles empty output:\n%s", state, view)
					}
					if state == "populated" {
						if !strings.Contains(view, test.populatedText) || countStateMarkers(view) != 0 {
							t.Fatalf("populated view missing content or claiming state:\n%s", view)
						}
						return
					}
					marker, action := "[loading]", "esc"
					switch state {
					case "error":
						marker, action = test.errorMarker, test.errorAction
					case "empty":
						marker, action = "[empty]", test.emptyAction
					}
					if !strings.Contains(view, marker) || !strings.Contains(view, action) || countStateMarkers(view) != 1 {
						t.Fatalf("%s view does not claim exactly one actionable state:\n%s", state, view)
					}
				})
			}
		})
	}
}

func TestResourceStatePrecedenceWhileAnotherKindLoads(t *testing.T) {
	h := newHarness(t, Options{StartNamespace: "default", ASCII: true})
	reqID := h.topReqID()
	h.send(resourcesPageMsg{reqID: reqID, kind: k8s.KindSecret, err: errors.New("secrets unavailable")})

	view := h.view()
	if !strings.Contains(view, "[loading]") || strings.Contains(view, "[error]") || strings.Contains(view, "No items.") {
		t.Fatalf("pending state did not take precedence over an early error:\n%s", view)
	}

	h.send(resourcesPageMsg{reqID: reqID, kind: k8s.KindConfigMap, page: k8s.ResourcePage{}})
	view = h.view()
	if !strings.Contains(view, "[error]") || strings.Contains(view, "[loading]") || strings.Contains(view, "[empty]") || strings.Contains(view, "No items.") {
		t.Fatalf("completed error state claimed another async state:\n%s", view)
	}
}

func TestResourceSuccessfulCompletionMarksTrueEmpty(t *testing.T) {
	h := newHarness(t, Options{StartNamespace: "default", ASCII: true})
	reqID := h.topReqID()
	h.send(
		resourcesPageMsg{reqID: reqID, kind: k8s.KindSecret, page: k8s.ResourcePage{}},
		resourcesPageMsg{reqID: reqID, kind: k8s.KindConfigMap, page: k8s.ResourcePage{}},
	)

	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	view := h.view()
	if !screen.loadComplete || !strings.Contains(view, "[empty]") || strings.Contains(view, "[loading]") || strings.Contains(view, "No items.") {
		t.Fatalf("successful resource completion did not render true empty state:\n%s", view)
	}
}

func TestCancelledLoaderResultsUseCancellationStates(t *testing.T) {
	tests := []struct {
		name       string
		marker     string
		retained   string
		renderView func(*testing.T) string
	}{
		{
			name:     "resource page",
			marker:   "[incomplete]",
			retained: "partial-secret",
			renderView: func(t *testing.T) string {
				t.Helper()
				screen := newResourceScreen(t.Context(), testClient(), "default", editEnv{}, testStyles(true))
				screen.SetSize(80, 22)
				screen.startLoading()
				resource := k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "partial-secret", Namespace: "default"}})
				_, _ = screen.Update(resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{resource}, Continue: "next"}})
				_, _ = screen.Update(resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindSecret, err: context.Canceled})
				return ansi.Strip(screen.View())
			},
		},
		{
			name:   "key load",
			marker: "[unknown]",
			renderView: func(t *testing.T) string {
				t.Helper()
				screen := newKeyScreen(t.Context(), testClient(), k8s.KindSecret, "default", "app-secret", editEnv{}, testStyles(true))
				screen.SetSize(80, 22)
				screen.startLoading()
				_, _ = screen.Update(resourceLoadedMsg{reqID: screen.reqID, err: context.Canceled})
				return ansi.Strip(screen.View())
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := test.renderView(t)
			if !strings.Contains(view, test.marker) || !strings.Contains(view, "cancelled") || !strings.Contains(view, "ctrl+r to retry") {
				t.Fatalf("cancelled loader result was not labelled for retry:\n%s", view)
			}
			if test.retained != "" && !strings.Contains(view, test.retained) {
				t.Fatalf("cancelled loader result dropped retained row %q:\n%s", test.retained, view)
			}
			if strings.Contains(view, "[error]") || strings.Contains(view, "[empty]") || strings.Contains(view, "No items.") {
				t.Fatalf("cancelled loader result claimed another state:\n%s", view)
			}
		})
	}
}

func TestPaginatedListCancellationRetainsIncompleteRows(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (*harness, string)
	}{
		{
			name: "namespaces",
			setup: func(t *testing.T) (*harness, string) {
				t.Helper()
				h := newHarness(t, Options{ASCII: true})
				h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{"partial-ns"}, Continue: "next"}})
				return h, "partial-ns"
			},
		},
		{
			name: "resources",
			setup: func(t *testing.T) (*harness, string) {
				t.Helper()
				h := newHarness(t, Options{StartNamespace: "default", ASCII: true})
				resource := k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "partial-secret", Namespace: "default"}})
				h.send(resourcesPageMsg{reqID: h.topReqID(), kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{resource}, Continue: "next"}})
				return h, "partial-secret"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, retained := test.setup(t)
			h.keys("esc")
			view := h.view()
			if !strings.Contains(view, retained) || !strings.Contains(view, "[incomplete]") || !strings.Contains(view, "cancelled") || !strings.Contains(view, "ctrl+r to retry") {
				t.Fatalf("cancelled pagination did not retain and label partial rows:\n%s", view)
			}
			if strings.Contains(view, "No items.") || strings.Contains(view, "[empty]") {
				t.Fatalf("cancelled pagination claimed an empty state:\n%s", view)
			}
			h.keys("ctrl+r")
			refreshed := h.view()
			if !strings.Contains(refreshed, "[loading]") || strings.Contains(refreshed, "[incomplete]") {
				t.Fatalf("refresh did not clear incomplete state:\n%s", refreshed)
			}
		})
	}
}

func countStateMarkers(view string) int {
	count := 0
	for _, marker := range []string{"[loading]", "[success]", "[error]", "[empty]", "[incomplete]", "[unknown]"} {
		count += strings.Count(view, marker)
	}
	return count
}

func namespaceAsyncStateView(t *testing.T, state string) string {
	t.Helper()
	screen := newNamespaceScreen(t.Context(), testClient(), "", editEnv{}, testStyles(true))
	switch state {
	case "pending":
		screen.pending = true
	case "error":
		screen.err = errors.New("API unavailable")
	case "empty":
		screen.loadComplete = true
	case "populated":
		screen.loadComplete = true
		screen.names = []string{"default"}
		_ = screen.setItems()
	}
	screen.SetSize(80, 22)
	return ansi.Strip(screen.View())
}

func resourceAsyncStateView(t *testing.T, state string) string {
	t.Helper()
	screen := newResourceScreen(t.Context(), testClient(), "default", editEnv{}, testStyles(true))
	switch state {
	case "pending":
		screen.pending = true
	case "error":
		screen.err = errors.New("API unavailable")
	case "empty":
		screen.loadComplete = true
	case "populated":
		screen.loadComplete = true
		screen.all = []k8s.Resource{k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-secret", Namespace: "default"}})}
		_ = screen.setVisibleItems()
	}
	screen.SetSize(80, 22)
	return ansi.Strip(screen.View())
}

func keyAsyncStateView(t *testing.T, state string) string {
	t.Helper()
	screen := newKeyScreen(t.Context(), testClient(), k8s.KindSecret, "default", "app-secret", editEnv{}, testStyles(true))
	switch state {
	case "pending":
		screen.pending = true
	case "error":
		screen.err = errors.New("API unavailable")
	case "empty":
		screen.loadComplete = true
		screen.resource = k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-secret", Namespace: "default"}})
		_ = screen.setItems()
	case "populated":
		screen.loadComplete = true
		screen.resource = navigationSecret()
		_ = screen.setItems()
	}
	screen.SetSize(80, 22)
	return ansi.Strip(screen.View())
}

func workloadAsyncStateView(t *testing.T, state string) string {
	t.Helper()
	screen := newWorkloadScreen(t.Context(), testClient(), "default", editEnv{}, testStyles(true))
	screen.index = k8s.NewRefIndex()
	switch state {
	case "pending":
		screen.pending = true
	case "error":
		screen.complete = true
		screen.index.AddSourceError("pods")
		_ = screen.setItems()
	case "empty":
		screen.complete = true
		_ = screen.setItems()
	case "populated":
		screen.complete = true
		screen.index.AddWorkload(workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "app-secret", k8s.TagEnv))
		_ = screen.setItems()
	}
	screen.SetSize(80, 22)
	return ansi.Strip(screen.View())
}

func consumerAsyncStateView(t *testing.T, state string) string {
	t.Helper()
	screen := newConsumersScreen(t.Context(), testClient(), k8s.KindSecret, "default", "shared", editEnv{}, testStyles(true))
	screen.index = k8s.NewRefIndex()
	switch state {
	case "pending":
		screen.pending = true
	case "error":
		screen.complete = true
		screen.index.AddSourceError("pods")
		_ = screen.setItems()
	case "empty":
		screen.complete = true
		_ = screen.setItems()
	case "populated":
		screen.complete = true
		screen.index.AddWorkload(workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "shared", k8s.TagEnv))
		_ = screen.setItems()
	}
	screen.SetSize(80, 22)
	return ansi.Strip(screen.View())
}

func projectOverlayAsyncStateView(t *testing.T, state string) string {
	t.Helper()
	overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
	switch state {
	case "pending":
		overlay.state = projectOverlayLoading
	case "error":
		overlay.state = projectOverlayError
		overlay.err = errors.New("disk failed")
	case "empty":
		overlay.state = projectOverlayList
		overlay.listReady = true
	case "populated":
		overlay.state = projectOverlayList
		overlay.listReady = true
		_ = overlay.list.SetItems([]list.Item{projectItem{project: store.Project{Name: "api", RootPath: "/repos/api"}, styles: overlay.styles}})
	}
	overlay.SetSize(80, 22)
	return ansi.Strip(overlay.View())
}

func TestKeyRefreshClearsStaleData(t *testing.T) {
	h := keyHarness(t, navigationSecret())
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen)

	h.keys("ctrl+r")

	if !screen.pending {
		t.Fatal("key screen is not pending after refresh")
	}
	if screen.resource != nil {
		t.Fatal("key screen retained its stale resource during refresh")
	}
	if items := screen.list.Items(); len(items) != 0 {
		t.Fatalf("key list retained %d stale items during refresh", len(items))
	}
}

func TestDetectASCII(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "LC_ALL C", env: map[string]string{"LC_ALL": "C"}, want: true},
		{name: "LANG UTF-8", env: map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"}},
		{name: "LC_CTYPE utf8", env: map[string]string{"TERM": "xterm-256color", "LC_CTYPE": "sv_SE.utf8"}},
		{name: "TERM dumb", env: map[string]string{"TERM": "dumb"}, want: true},
		{name: "TERM linux", env: map[string]string{"TERM": "linux"}, want: true},
		{name: "TERM vt220", env: map[string]string{"TERM": "vt220"}, want: true},
		{name: "TERM vte", env: map[string]string{"TERM": "vte-256color", "LANG": "en_US.UTF-8"}},
		{name: "TERM ansi", env: map[string]string{"TERM": "ansi"}, want: true},
		{name: "TERM empty overrides UTF-8", env: map[string]string{"LANG": "en_US.UTF-8"}, want: true},
		{name: "modern TERM without locale keeps Unicode", env: map[string]string{"TERM": "xterm-256color"}},
		{name: "all empty", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DetectASCII(func(name string) string { return test.env[name] }); got != test.want {
				t.Fatalf("DetectASCII() = %t, want %t", got, test.want)
			}
		})
	}
}

type helpScreenCase struct {
	name   string
	screen screen
}

func plainHintGroups(groups []hintGroup) string {
	plain := make([]string, 0, len(groups))
	for _, group := range groups {
		text := group.key
		if group.desc != "" {
			text += " " + group.desc
		}
		plain = append(plain, text)
	}
	return strings.Join(plain, chromeGap)
}

func plainHints(t testing.TB, hints footerHints) string {
	t.Helper()
	return plainHintGroups(hintGroups(hints))
}

func plainFooter(t testing.TB, current screen, depth int) string {
	t.Helper()
	return plainHintGroups(withNavigationHint(current, depth, packageDefaultKeyMaps))
}

type footerHintTestScreen struct {
	hints    footerHints
	wantsEsc bool
}

func (s *footerHintTestScreen) Init() tea.Cmd                    { return nil }
func (s *footerHintTestScreen) Update(tea.Msg) (screen, tea.Cmd) { return s, nil }
func (s *footerHintTestScreen) View() string                     { return "" }
func (s *footerHintTestScreen) SetSize(int, int)                 {}
func (s *footerHintTestScreen) SetStyles(*styles)                {}
func (s *footerHintTestScreen) Title() string                    { return "test" }
func (s *footerHintTestScreen) Hints() footerHints               { return s.hints }
func (s *footerHintTestScreen) Help() helpGroup                  { return helpGroup{} }
func (s *footerHintTestScreen) CapturesInput() bool              { return false }
func (s *footerHintTestScreen) WantsEsc() bool                   { return s.wantsEsc }

func TestFooterStatusSuppressesEscInjection(t *testing.T) {
	current := &footerHintTestScreen{hints: hintStatus("saving (cannot cancel)"), wantsEsc: true}
	groups := withNavigationHint(current, 2, packageDefaultKeyMaps)
	if got := plainHintGroups(groups); got != "saving (cannot cancel)" {
		t.Fatalf("status footer = %q, want %q", got, "saving (cannot cancel)")
	}
	if len(groups) != 1 {
		t.Fatalf("status footer groups = %d, want 1", len(groups))
	}
}

func browsingHelpScreens(t *testing.T) []helpScreenCase {
	t.Helper()
	client := testClient()
	st := testStyles(true)
	resource := navigationSecret()
	immutable := true
	immutableResource := k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "immutable", Namespace: "default"}, Immutable: &immutable})
	writableKeys := newKeyScreen(t.Context(), client, k8s.KindSecret, "default", "secret", editEnv{}, st)
	writableKeys.resource = resource
	readOnlyKeys := newKeyScreen(t.Context(), client, k8s.KindSecret, "default", "secret", editEnv{readOnly: true}, st)
	readOnlyKeys.resource = resource
	immutableKeys := newKeyScreen(t.Context(), client, k8s.KindSecret, "default", "immutable", editEnv{}, st)
	immutableKeys.resource = immutableResource
	return []helpScreenCase{
		{name: "namespaces", screen: newNamespaceScreen(t.Context(), client, "", editEnv{}, st)},
		{name: "resources writable", screen: newResourceScreen(t.Context(), client, "default", editEnv{}, st)},
		{name: "resources read only", screen: newResourceScreen(t.Context(), client, "default", editEnv{readOnly: true}, st)},
		{name: "resources all namespaces", screen: newResourceScreen(t.Context(), client, k8s.AllNamespaces, editEnv{}, st)},
		{name: "resources all namespaces read only", screen: newResourceScreen(t.Context(), client, k8s.AllNamespaces, editEnv{readOnly: true}, st)},
		{name: "resources secrets only", screen: newResourceScreen(t.Context(), client, "default", editEnv{noConfigMaps: true}, st)},
		{name: "keys writable", screen: writableKeys},
		{name: "keys read only", screen: readOnlyKeys},
		{name: "keys immutable", screen: immutableKeys},
		{name: "project", screen: newProjectScreen(t.Context(), client, nil, "", store.Project{Name: "project"}, "", scanConfig{}, editEnv{}, st)},
		{name: "workloads", screen: newWorkloadScreen(t.Context(), client, "default", editEnv{}, st)},
		{name: "workload references", screen: newWorkloadRefsScreen(t.Context(), client, "default", nil, k8s.KindDeployment, "refs", editEnv{}, st)},
		{name: "consumers", screen: newConsumersScreen(t.Context(), client, k8s.KindSecret, "default", "secret", editEnv{}, st)},
		{name: "suggestions", screen: newSuggestionScreen(t.Context(), client, nil, store.Project{Name: "project"}, scanConfig{}, false, editEnv{}, st)},
		{name: "hex", screen: newHexScreen(resource, "config", editEnv{}, st)},
		{name: "value", screen: newValueScreen(resource, "config", editEnv{}, st)},
	}
}

func TestHintLinesFitEightyColumns(t *testing.T) {
	for _, test := range browsingHelpScreens(t) {
		t.Run(test.name, func(t *testing.T) {
			groups := withNavigationHint(test.screen, 1, packageDefaultKeyMaps)
			hints := plainHintGroups(groups)
			if width := ansi.StringWidth(hints); width > 78 {
				t.Fatalf("Hints() width = %d, want <= 78: %q", width, hints)
			}
			for _, group := range groups {
				plain := plainHintGroups([]hintGroup{group})
				if ansi.StringWidth(plain) != len(plain) {
					t.Fatalf("Hints() group is not ASCII-only: %q", plain)
				}
			}
			if got := groups[len(groups)-1].key + " " + groups[len(groups)-1].desc; got != "? help" {
				t.Fatalf("browsing Hints() final group = %q, want ? help", got)
			}
		})
	}

	client := testClient()
	st := testStyles(true)
	resource := navigationSecret()
	writableKeys := newKeyScreen(t.Context(), client, k8s.KindSecret, "default", "secret", editEnv{}, st)
	writableKeys.resource = resource
	projectConfirm := newProjectScreen(t.Context(), client, nil, "", store.Project{Name: "project"}, "", scanConfig{}, editEnv{}, st)
	projectConfirm.confirmUnlink = true
	projectFiltering := newProjectScreen(t.Context(), client, nil, "", store.Project{Name: "project"}, "", scanConfig{}, editEnv{}, st)
	_, _ = projectFiltering.Update(key("/"))
	projectContextConfirm := newProjectContextConfirm(t.Context(), nil, store.Project{Name: "project"}, client, k8s.ContextInfo{}, "", nil, st)
	suggestionsPending := newSuggestionScreen(t.Context(), client, nil, store.Project{Name: "project"}, scanConfig{}, false, editEnv{}, st)
	suggestionsPending.pending = true
	suggestionsFiltering := newSuggestionScreen(t.Context(), client, nil, store.Project{Name: "project"}, scanConfig{}, false, editEnv{}, st)
	_, _ = suggestionsFiltering.Update(key("/"))
	diffWrapOff := newEditFlow(t.Context(), client, editEnv{}, resource, "config", []byte("changed"), st)
	diffWrapOff.phase = phaseDiff
	diffWrapOn := newEditFlow(t.Context(), client, editEnv{}, resource, "config", []byte("changed"), st)
	diffWrapOn.phase, diffWrapOn.wrap = phaseDiff, true
	conflictWrapOff := newEditFlow(t.Context(), client, editEnv{}, resource, "config", []byte("changed"), st)
	conflictWrapOff.phase = phaseConflict
	conflictWrapOn := newEditFlow(t.Context(), client, editEnv{}, resource, "config", []byte("changed"), st)
	conflictWrapOn.phase, conflictWrapOn.wrap = phaseConflict, true
	rolloutDone := newEditFlow(t.Context(), client, editEnv{}, resource, "config", []byte("changed"), st)
	rolloutDone.phase = phaseRolloutDone
	fileImportPick := newFilePrompt(t.Context(), client, editEnv{}, resource, "config", fileImport, st)
	fileExportPick := newFilePrompt(t.Context(), client, editEnv{}, resource, "config", fileExport, st)
	fileNameInput := newFilePrompt(t.Context(), client, editEnv{}, resource, "config", fileExport, st)
	fileNameInput.phase = filePhaseName
	fileNameDir := newFilePrompt(t.Context(), client, editEnv{}, resource, "config", fileExport, st)
	fileNameDir.phase, fileNameDir.dirRowFocused = filePhaseName, true
	fileGate := newFilePrompt(t.Context(), client, editEnv{}, resource, "config", fileExport, st)
	fileGate.phase = filePhaseGate
	fileDone := newFilePrompt(t.Context(), client, editEnv{}, resource, "config", fileExport, st)
	fileDone.phase = filePhaseDone

	for _, test := range []struct {
		name  string
		hints footerHints
	}{
		{name: "project confirm", hints: projectConfirm.Hints()},
		{name: "project filtering", hints: projectFiltering.Hints()},
		{name: "project context confirm", hints: projectContextConfirm.Hints()},
		{name: "suggestions pending", hints: suggestionsPending.Hints()},
		{name: "suggestions filtering", hints: suggestionsFiltering.Hints()},
		{name: "help overlay", hints: newHelpOverlay(writableKeys, editEnv{}, st).Hints()},
		{name: "context overlay", hints: newContextOverlay(t.Context(), "", client.Context, client.Server, nil, packageDefaultKeyMaps, st).Hints()},
		{name: "project overlay", hints: newProjectOverlay(t.Context(), nil, client, "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, st).Hints()},
		{name: "diff wrap off", hints: diffWrapOff.Hints()},
		{name: "diff wrap on", hints: diffWrapOn.Hints()},
		{name: "conflict wrap off", hints: conflictWrapOff.Hints()},
		{name: "conflict wrap on", hints: conflictWrapOn.Hints()},
		{name: "rollout done", hints: rolloutDone.Hints()},
		{name: "file import picker", hints: fileImportPick.Hints()},
		{name: "file export picker", hints: fileExportPick.Hints()},
		{name: "file name input", hints: fileNameInput.Hints()},
		{name: "file name directory", hints: fileNameDir.Hints()},
		{name: "file gate", hints: fileGate.Hints()},
		{name: "file done", hints: fileDone.Hints()},
	} {
		t.Run(test.name, func(t *testing.T) {
			hints := plainHints(t, test.hints)
			if width := ansi.StringWidth(hints); width > 78 {
				t.Fatalf("Hints() width = %d, want <= 78: %q", width, hints)
			}
			if ansi.StringWidth(hints) != len(hints) {
				t.Fatalf("Hints() is not ASCII-only: %q", hints)
			}
		})
	}
}

func TestRenderedHintTiersPreserveMinimumWidthEssentials(t *testing.T) {
	st := testStyles(true)
	for _, test := range browsingHelpScreens(t) {
		t.Run(test.name, func(t *testing.T) {
			groups := withNavigationHint(test.screen, 2, packageDefaultKeyMaps)
			primary := plainHintGroups(groups[:1])
			footer := ansi.Strip(renderFooterBar(st, groups, minimumWidth))
			for _, want := range []string{primary, "? help"} {
				if !strings.Contains(footer, want) {
					t.Fatalf("minimum-width footer missing %q: %q", want, footer)
				}
			}
			if !strings.Contains(footer, "esc ") {
				t.Fatalf("minimum-width footer missing esc action: %q", footer)
			}
			if width := ansi.StringWidth(footer); width != minimumWidth {
				t.Fatalf("minimum-width footer width = %d, want %d: %q", width, minimumWidth, footer)
			}
		})
	}
}

func TestManyKeyRebindCannotOverflowFooter(t *testing.T) {
	km := defaultKeyMaps()
	keys := make([]string, 40)
	modifiers := []string{"ctrl", "alt", "shift", "ctrl+alt"}
	for i := range keys {
		keys[i] = fmt.Sprintf("%s+f%d", modifiers[i/12], i%12+1)
	}
	applyKeybinds(km, config.Overrides{config.ActionHelp: keys})
	groups := []hintGroup{bindingHintGroup(km.global.Help)}

	const width = 60
	footer := renderFooterBar(testStyles(true), groups, width)
	if got := ansi.StringWidth(footer); got != width {
		t.Fatalf("footer width = %d, want %d: %q", got, width, ansi.Strip(footer))
	}
	if got := ansi.StringWidth(plainHintGroups(groups)); got > 78 {
		t.Fatalf("hint width = %d, want <= 78", got)
	}
}

func TestKeyHintsAdvertiseImport(t *testing.T) {
	screen := newKeyScreen(t.Context(), testClient(), k8s.KindSecret, "default", "secret", editEnv{}, testStyles(true))
	screen.resource = navigationSecret()
	if hints := plainFooter(t, screen, 1); !strings.Contains(hints, "i import") {
		t.Fatalf("writable key hints = %q, want i import", hints)
	}
}

func TestGolden_NamespaceListHeader(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	model := h.m.(app)
	model.client.Cluster = "local-cluster"
	root := newNamespaceScreen(t.Context(), model.client, "api", model.editEnv, model.styles)
	root.SetSize(model.width, max(0, model.height-2))
	model.stack = []screen{root}
	h.m = model
	h.drain(root.Init())
	h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{
		"default", "kube-public", "kube-system", "production", "staging",
	}}})
	h.golden("namespace_list_header")
}

func TestResourceStatusRowTracksModeAndNamespaceScope(t *testing.T) {
	st := testStyles(true)
	resources := []k8s.Resource{
		k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: "default"}}),
		k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "default"}}),
	}
	screen := newResourceScreen(t.Context(), testClient(), "default", editEnv{}, st)
	screen.all = resources
	screen.loadComplete = true
	screen.SetSize(80, 20)
	_ = screen.setVisibleItems()
	if got := screen.statusRow(); got != "2 resources - all kinds" {
		t.Fatalf("all-kinds status = %q", got)
	}
	_, _ = screen.Update(key("t"))
	if got := screen.statusRow(); got != "1 resource - secrets" {
		t.Fatalf("secret status = %q", got)
	}
	_, _ = screen.Update(key("t"))
	if got := screen.statusRow(); got != "1 resource - configmaps" {
		t.Fatalf("configmap status = %q", got)
	}
	screen.filter = filterAll
	_ = screen.setVisibleItems()
	screen.list.SetFilterText("secret")
	if got := screen.statusRow(); got != `filter "secret" - 1 of 2 - all kinds` {
		t.Fatalf("filtered status = %q", got)
	}

	allNamespaces := newResourceScreen(t.Context(), testClient(), k8s.AllNamespaces, editEnv{}, st)
	allNamespaces.all = resources
	allNamespaces.loadComplete = true
	allNamespaces.SetSize(80, 20)
	_ = allNamespaces.setVisibleItems()
	if got := allNamespaces.statusRow(); got != "2 resources - all kinds - all namespaces" {
		t.Fatalf("all-namespaces status = %q", got)
	}
}

func TestNamespaceHeaderTruncates(t *testing.T) {
	client := testClient()
	client.Cluster = strings.Repeat("very-long-cluster-", 5)
	root := newNamespaceScreen(t.Context(), client, "", editEnv{}, testStyles(true))
	root.SetSize(40, 20)
	line := strings.Split(root.View(), "\n")[0]
	if width := ansi.StringWidth(line); width > 40 {
		t.Fatalf("namespace header width = %d, want <= 40: %q", width, ansi.Strip(line))
	}
}

func TestSubjectLinesUseScreenIdentityAndFitBodyWidth(t *testing.T) {
	st := testStyles(true)
	client := testClient()
	namespace := strings.Repeat("namespace-", 4)
	name := strings.Repeat("resource-", 4)
	keyName := strings.Repeat("key-", 6)
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{keyName: []byte("value")},
	})
	keys := newKeyScreen(t.Context(), client, k8s.KindSecret, namespace, name, editEnv{}, st)
	keys.resource = resource
	keyIdentity := "[S] " + resourceSubject(k8s.KindSecret, namespace, name)

	tests := []struct {
		name       string
		screen     screen
		full       string
		narrowLine string
	}{
		{name: "keys", screen: keys, full: keyIdentity + "  Opaque", narrowLine: truncateLine(keyIdentity, minimumWidth, st.glyphs.ellipsis)},
		{name: "value", screen: newValueScreen(resource, keyName, editEnv{}, st), full: resourceSubject(k8s.KindSecret, namespace, name) + " / " + keyName},
		{name: "hex", screen: newHexScreen(resource, keyName, editEnv{}, st), full: resourceSubject(k8s.KindSecret, namespace, name) + " / " + keyName + " (hex)"},
		{name: "secret consumers", screen: newConsumersScreen(t.Context(), client, k8s.KindSecret, namespace, name, editEnv{}, st), full: "Consumers of " + resourceSubject(k8s.KindSecret, namespace, name)},
		{name: "configmap consumers", screen: newConsumersScreen(t.Context(), client, k8s.KindConfigMap, namespace, name, editEnv{}, st), full: "Consumers of " + resourceSubject(k8s.KindConfigMap, namespace, name)},
		{name: "workload references", screen: newWorkloadRefsScreen(t.Context(), client, namespace, nil, k8s.KindDeployment, name, editEnv{}, st), full: k8s.KindDeployment + " " + namespace + "/" + name},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.screen.SetSize(200, 20)
			if got := firstRenderedLine(test.screen.View()); got != test.full {
				t.Fatalf("full subject = %q, want %q", got, test.full)
			}

			test.screen.SetSize(minimumWidth, 20)
			want := test.narrowLine
			if want == "" {
				want = truncateLine(test.full, minimumWidth, st.glyphs.ellipsis)
			}
			got := firstRenderedLine(test.screen.View())
			if got != want {
				t.Fatalf("narrow subject = %q, want %q", got, want)
			}
			if width := ansi.StringWidth(got); width > minimumWidth {
				t.Fatalf("narrow subject width = %d, want <= %d: %q", width, minimumWidth, got)
			}
		})
	}
}

func firstRenderedLine(view string) string {
	return strings.SplitN(ansi.Strip(view), "\n", 2)[0]
}

func TestGolden_KeyListImmutable(t *testing.T) {
	immutable := true
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "immutable", Namespace: "default"},
		Immutable:  &immutable,
		Data:       map[string][]byte{"password": []byte("secret")},
	})
	h := keyHarness(t, resource)
	h.golden("key_list_immutable")
}

func TestWorkloadRefsLink(t *testing.T) {
	client := testClient()
	screen := newWorkloadRefsScreen(t.Context(), client, "production", []refRow{
		{ref: k8s.ResourceRef{Kind: k8s.KindSecret, Name: "credentials"}},
		{ref: k8s.ResourceRef{Kind: k8s.KindConfigMap, Name: "missing"}, missing: true},
	}, k8s.KindDeployment, "refs", editEnv{}, testStyles(true))
	_ = screen.Init()

	_, cmd := screen.Update(key("L"))
	picker := cmd().(openProjectPickerMsg)
	want := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "credentials", Source: store.SourceManual}
	if picker.link.resource == nil || *picker.link.resource != want {
		t.Fatalf("L emitted resource link %#v, want %#v", picker.link.resource, want)
	}
	_, _ = screen.Update(key("down"))
	_, cmd = screen.Update(key("L"))
	if cmd != nil {
		t.Fatalf("L on missing row emitted %T", cmd())
	}
}

func namespaceHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, Options{ASCII: true})
	h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{
		"default", "kube-public", "kube-system", "production", "staging",
	}}})
	return h
}

func resourceHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	h := newHarness(t, Options{StartNamespace: "default", ASCII: ascii})
	h.send(resourceMessages(h.topReqID())...)
	return h
}

func resourceMessages(reqID int) []tea.Msg {
	immutable := true
	secrets := []k8s.Resource{
		k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: "default"}, Type: corev1.SecretTypeOpaque}),
		k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls-cert", Namespace: "default"}, Type: corev1.SecretTypeTLS, Immutable: &immutable}),
	}
	configMaps := []k8s.Resource{
		k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-settings", Namespace: "default"}}),
		k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "feature-flags", Namespace: "default"}}),
	}
	return []tea.Msg{
		resourcesPageMsg{reqID: reqID, kind: k8s.KindSecret, page: k8s.ResourcePage{Items: secrets}},
		resourcesPageMsg{reqID: reqID, kind: k8s.KindConfigMap, page: k8s.ResourcePage{Items: configMaps}},
	}
}

func keyListAppearanceHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	return keyHarnessOptions(t, keyListSecret(), Options{StartNamespace: "default", ASCII: ascii})
}

func keyHarness(t *testing.T, resource k8s.Resource) *harness {
	return keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true})
}

func keyHarnessOptions(t *testing.T, resource k8s.Resource, opts Options) *harness {
	t.Helper()
	h := newHarness(t, opts)
	otherKind := k8s.KindSecret
	if resource.Kind() == k8s.KindSecret {
		otherKind = k8s.KindConfigMap
	}
	h.send(
		resourcesPageMsg{reqID: h.topReqID(), kind: resource.Kind(), page: k8s.ResourcePage{Items: []k8s.Resource{resource}}},
		resourcesPageMsg{reqID: h.topReqID(), kind: otherKind, page: k8s.ResourcePage{}},
	)
	h.keys("enter")
	h.send(resourceLoadedMsg{reqID: h.topReqID(), res: resource})
	return h
}

func navigationSecret() k8s.Resource {
	return k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: "default"},
		Data:       map[string][]byte{"config": []byte("first line\nsecond line\nthird line")},
	})
}

func keyListSecret() k8s.Resource {
	immutable := true
	return k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "large-secret", Namespace: "default"},
		Immutable:  &immutable,
		Data: map[string][]byte{
			"binary":    {0, 1, 2},
			"kilobytes": make([]byte, 4200),
			"megabytes": make([]byte, 1258291),
			"small":     []byte("hello"),
		},
	})
}

func contextFixtures() []k8s.ContextInfo {
	return []k8s.ContextInfo{
		{Name: "test-ctx", Cluster: "shared-cluster", Server: "https://test.example", Namespace: "default", Current: true},
		{Name: "work-ctx", Cluster: "shared-cluster", Server: "https://work.example", Namespace: "production"},
	}
}

func typeFilter(t *testing.T, h *harness, value string) {
	t.Helper()
	for _, char := range value {
		model, cmd := h.m.Update(key(string(char)))
		h.m = model
		if cmd == nil {
			continue
		}
		h.drain(cmd)
	}
	model := h.m.(app)
	screen := model.stack[len(model.stack)-1].(*resourceScreen)
	if screen.list.FilterState() != list.Filtering || !strings.Contains(screen.list.FilterInput.Value(), value) {
		t.Fatalf("filter state = %v value = %q", screen.list.FilterState(), screen.list.FilterInput.Value())
	}
}
