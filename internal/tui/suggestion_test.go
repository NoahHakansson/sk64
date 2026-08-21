package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/charmbracelet/x/ansi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestGolden_SuggestionView(t *testing.T) {
	suggestionViewAppearanceHarness(t, true).golden("suggestion_view")
}

func TestSuggestionScanFailureUsesStateLine(t *testing.T) {
	st := testStyles(false)
	screen := &suggestionScreen{styles: st, km: packageDefaultKeyMaps.suggestion, scanErr: errors.New("manifest invalid"), width: 80}
	view := ansi.Strip(screen.View())
	want := st.stateMarker(stateLineError) + " scan failed: manifest invalid" + st.glyphs.separator + "ctrl+r to rescan"
	if !strings.Contains(view, want) {
		t.Fatalf("scan failure view = %q, want %q", view, want)
	}
}

func TestGolden_SuggestionViewUnicode(t *testing.T) {
	suggestionViewAppearanceHarness(t, false).golden("suggestion_view_unicode")
}

func TestGolden_SuggestionViewFiltered(t *testing.T) {
	root := t.TempDir()
	writeSuggestionFile(t, root, "k8s/app.yaml", suggestionDeploymentYAML)
	writeSuggestionFile(t, root, "base/kustomization.yaml", "namespace: production\nconfigMapGenerator:\n  - name: app-config\n")
	h, _, client := newSuggestionHarness(t, root, true)
	seedSuggestionObjects(t, client)
	h.keys("s", "/", "c", "o", "n", "f", "i", "g", "enter")
	assertLinesFitWidth(t, h.view(), 80)
	h.golden("suggestion_view_filtered")
}

func TestGolden_SuggestionViewEmpty(t *testing.T) {
	h, _, _ := newSuggestionHarness(t, t.TempDir(), true)
	h.keys("s")
	h.golden("suggestion_view_empty")
}

func TestGolden_SuggestionViewLinked(t *testing.T) {
	root := t.TempDir()
	writeSuggestionFile(t, root, "pod.yaml", suggestionPodYAML)
	h, _, _ := newSuggestionHarness(t, root, true)
	h.keys("s", "enter")
	passCommitGate(h)
	h.golden("suggestion_view_linked")
}

func TestSuggestionConfirmFlow(t *testing.T) {
	root := t.TempDir()
	writeSuggestionFile(t, root, "k8s/app.yaml", suggestionDeploymentYAML)
	writeSuggestionFile(t, root, "base/kustomization.yaml", "namespace: production\nconfigMapGenerator:\n  - name: app-config\n")
	writeSuggestionFile(t, root, ".gitlab-ci.yml", "deploy: kubectl apply -f app.yaml --namespace staging\n")
	h, st, client := newSuggestionHarness(t, root, true)
	seedSuggestionObjects(t, client)
	h.keys("s")
	screen := h.m.(app).stack[2].(*suggestionScreen)

	linkSuggestion(t, h, screen, k8s.KindConfigMap, "app-config")
	resources, err := st.ResourceLinks(t.Context(), screen.project.ID)
	if err != nil || len(resources) != 1 || resources[0].Name != "app-config-abc123" ||
		resources[0].Source != store.SourceScan ||
		resources[0].OriginContext != client.Context || resources[0].OriginServer != client.Server {
		t.Fatalf("ResourceLinks() = %+v, %v", resources, err)
	}

	linkSuggestion(t, h, screen, k8s.KindDeployment, "web")
	workloads, err := st.WorkloadLinks(t.Context(), screen.project.ID)
	if err != nil || len(workloads) != 1 || workloads[0].Name != "web" ||
		workloads[0].OriginContext != client.Context || workloads[0].OriginServer != client.Server {
		t.Fatalf("WorkloadLinks() = %+v, %v", workloads, err)
	}

	linkSuggestion(t, h, screen, project.KindNamespace, "staging")
	namespaces, err := st.Namespaces(t.Context(), screen.project.ID)
	if err != nil || len(namespaces) != 1 || namespaces[0] != "staging" {
		t.Fatalf("Namespaces() = %v, %v", namespaces, err)
	}

	h.keys("esc")
	if len(h.m.(app).stack) != 2 {
		t.Fatalf("stack depth after esc = %d, want project view", len(h.m.(app).stack))
	}
	projectScreen := h.m.(app).stack[1].(*projectScreen)
	h.send(
		projectLinksMsg{reqID: projectScreen.reqID, workloads: workloads, resources: resources, extraNS: namespaces},
		projectContextMsg{reqID: projectScreen.reqID, found: true},
	)
	feedProjectRefs(h, projectScreen, map[string]refsFixture{
		"production": {resources: []k8s.Resource{configMapResourceIn("app-config-abc123", "production")}},
	})
	if !strings.Contains(projectScreen.View(), "app-config-abc123") {
		t.Fatalf("reloaded project view = %q", projectScreen.View())
	}
}

func TestSuggestionViewFilter(t *testing.T) {
	st := newTestStore(t)
	proj := createProject(t, st, "api", t.TempDir(), "test-ctx", "production")
	screen := newSuggestionScreen(t.Context(), testClient(), st, proj, scanConfig{}, false, editEnv{}, testStyles(true))
	screen.rows = []suggestionRow{
		{sug: project.Suggestion{Kind: k8s.KindSecret, Name: "credentials"}, ns: "production", state: rowLinked},
		{sug: project.Suggestion{Kind: k8s.KindConfigMap, Name: "settings"}, ns: "production", state: rowFound},
		{sug: project.Suggestion{Kind: k8s.KindDeployment, Name: "web"}, ns: "production", state: rowNotFound},
	}
	_ = screen.setItems()
	screen.SetSize(80, 10)
	update := func(msg tea.Msg) tea.Cmd {
		_, cmd := screen.Update(msg)
		return cmd
	}
	apply := func(msg tea.Msg) {
		drainScopedListFilterCmd(t, update, update(msg))
	}
	originalStates := []suggestionRowState{screen.rows[0].state, screen.rows[1].state, screen.rows[2].state}

	apply(key("/"))
	for _, value := range []string{"z", "q", "x"} {
		apply(key(value))
	}
	if len(screen.list.VisibleItems()) != 0 || screen.list.SelectedItem() != nil {
		t.Fatalf("non-matching visible/selected = %v/%v, want []/nil", screen.list.VisibleItems(), screen.list.SelectedItem())
	}
	apply(key("esc"))

	apply(key("/"))
	for _, value := range []string{"s", "e", "t", "t", "i", "n", "g", "s"} {
		apply(key(value))
	}
	if !screen.CapturesInput() {
		t.Fatal("suggestion filter stopped capturing before enter")
	}
	visible := screen.list.VisibleItems()
	if len(visible) != 1 || visible[0].(suggestionItem).index != 1 {
		t.Fatalf("filtered visible = %v, want [1]", visible)
	}
	for i, want := range originalStates {
		if screen.rows[i].state != want {
			t.Fatalf("row %d state = %v, want %v", i, screen.rows[i].state, want)
		}
	}
	apply(key("enter"))
	apply(key("enter"))
	for _, char := range "YES" {
		_, _ = screen.Update(key(string(char)))
	}
	_, cmd := screen.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter on filtered suggestion did not start linking")
	}
	message := cmd()
	_, _ = screen.Update(message)
	links, err := st.ResourceLinks(t.Context(), proj.ID)
	if err != nil || len(links) != 1 || links[0].Name != "settings" {
		t.Fatalf("filtered link result = %+v, %v", links, err)
	}
	_, _ = screen.Update(key("esc"))
	if screen.list.FilterState() != list.Unfiltered || len(screen.list.VisibleItems()) != len(screen.rows) {
		t.Fatalf("esc filter state/visible = %v/%v", screen.list.FilterState(), screen.list.VisibleItems())
	}
	if screen.rows[0].state != rowLinked || screen.rows[1].state != rowLinked || screen.rows[2].state != rowNotFound {
		t.Fatalf("row states after filtered link = %#v", screen.rows)
	}
}

func TestSuggestionFilteredEmptyFitsBodyHeight(t *testing.T) {
	screen := newSuggestionScreen(t.Context(), testClient(), nil, store.Project{Name: "api"}, scanConfig{}, false, editEnv{}, testStyles(true))
	screen.rows = []suggestionRow{{sug: project.Suggestion{Kind: k8s.KindConfigMap, Name: "settings"}, ns: "production"}}
	_ = screen.setItems()
	screen.scanned = true
	screen.SetSize(80, 10)
	update := func(msg tea.Msg) tea.Cmd {
		_, cmd := screen.Update(msg)
		return cmd
	}
	apply := func(msg tea.Msg) {
		drainScopedListFilterCmd(t, update, update(msg))
	}

	apply(key("/"))
	for _, value := range "settings" {
		apply(key(string(value)))
	}
	apply(key("enter"))
	screen.rows = []suggestionRow{{sug: project.Suggestion{Kind: k8s.KindSecret, Name: "credentials"}, ns: "production"}}
	drainScopedListFilterCmd(t, update, screen.setItems())

	view := ansi.Strip(screen.View())
	if !strings.Contains(view, "no matching suggestions") || !strings.Contains(view, `filter "settings"`) || !strings.Contains(view, "0 of 1") {
		t.Fatalf("filtered-empty view = %q, want filter status and empty-state message", view)
	}
	lines := strings.Count(view, "\n") + 1
	t.Logf("filtered-empty View rendered %d lines at body height %d", lines, screen.bodyHeight)
	if lines > screen.bodyHeight {
		t.Fatalf("filtered-empty View height = %d lines, want at most %d", lines, screen.bodyHeight)
	}
	if _, cmd := screen.Update(key("enter")); cmd != nil {
		t.Fatalf("enter on filtered-empty suggestions returned %v, want nil", cmd)
	}
}

func TestSuggestionViewTruncationFitsBodyHeight(t *testing.T) {
	screen := newSuggestionScreen(t.Context(), testClient(), nil, store.Project{Name: "api"}, scanConfig{}, false, editEnv{}, testStyles(true))
	screen.rows = make([]suggestionRow, 40)
	for i := range screen.rows {
		screen.rows[i] = suggestionRow{sug: project.Suggestion{Kind: k8s.KindSecret, Name: strings.Repeat("s", i+1)}, ns: "default"}
	}
	_ = screen.setItems()
	screen.list.Select(31)
	screen.SetSize(80, 6)

	view := screen.View()
	if lines := strings.Count(view, "\n") + 1; lines > screen.bodyHeight {
		t.Fatalf("View() height = %d lines, want at most %d", lines, screen.bodyHeight)
	}
	selected := screen.list.SelectedItem().(suggestionItem)
	if !strings.Contains(view, selected.row.sug.Name) || !strings.Contains(view, screen.styles.glyphs.cursorMarker) {
		t.Fatalf("selected visible-index row is not visible:\n%s", view)
	}
}

func TestSuggestionLinkErrorRetry(t *testing.T) {
	root := t.TempDir()
	writeSuggestionFile(t, root, "pod.yaml", suggestionPodYAML)
	st := newTestStore(t)
	missing := store.Project{ID: 9999, Name: "missing", RootPath: root, KubeContext: "test-ctx", Namespace: "production"}
	h := newHarness(t, Options{ASCII: true, Store: st})
	screen := newSuggestionScreen(t.Context(), h.m.(app).client, st, missing, scanConfig{}, false, h.m.(app).editEnv, h.m.(app).styles)
	h.send(pushScreenMsg{s: screen})
	h.keys("enter")
	passCommitGate(h)
	if screen.rows[0].state != rowLinkFailed || !strings.Contains(screen.View(), "link failed:") {
		t.Fatalf("first link state/view = %v, %q", screen.rows[0].state, screen.View())
	}
	firstReqID := screen.linkLoader.reqID
	h.keys("enter")
	passCommitGate(h)
	if screen.rows[0].state != rowLinkFailed || screen.linkLoader.reqID == firstReqID {
		t.Fatalf("retry state/reqID = %v/%d, first reqID %d", screen.rows[0].state, screen.linkLoader.reqID, firstReqID)
	}
}

func TestSuggestionContextNotActive(t *testing.T) {
	root := t.TempDir()
	writeSuggestionFile(t, root, "pod.yaml", suggestionPodYAML)
	st := newTestStore(t)
	proj := createProject(t, st, "api", root, "other-ctx", "production")
	clientset := fake.NewClientset()
	client := &k8s.Client{Clientset: clientset, Context: "test-ctx", Namespace: "default"}
	h := newHarness(t, Options{ASCII: true, Store: st})
	screen := newSuggestionScreen(t.Context(), client, st, proj, scanConfig{}, false, h.m.(app).editEnv, h.m.(app).styles)
	h.send(pushScreenMsg{s: screen})
	if !strings.Contains(screen.View(), screen.styles.glyphs.inactiveTag+" unchecked") || len(clientset.Actions()) != 0 {
		t.Fatalf("unchecked view/actions = %q, %d", screen.View(), len(clientset.Actions()))
	}
	h.keys("enter")
	passCommitGate(h)
	links, err := st.ResourceLinks(t.Context(), proj.ID)
	if err != nil || len(links) != 1 || links[0].Name != "app-secrets" {
		t.Fatalf("ResourceLinks() = %+v, %v", links, err)
	}
}

func TestSuggestionClusterRealityStatesUseBracketTags(t *testing.T) {
	st := testStyles(true)
	tests := []struct {
		name         string
		checkCluster bool
		state        suggestionRowState
		want         string
	}{
		{name: "check failed", checkCluster: true, state: rowCheckFailed, want: st.glyphs.checkFailedTag},
		{name: "context inactive", state: rowUnchecked, want: st.glyphs.inactiveTag + " unchecked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := suggestionRow{
				sug: project.Suggestion{Kind: k8s.KindDeployment, Name: "web"},
				ns:  "production", state: test.state,
			}
			item := suggestionItem{row: &row, styles: st, checkCluster: test.checkCluster}
			identity, columns := item.listColumns()
			if got := ansi.Strip(renderRowColumns(80, identity, st.glyphs.ellipsis, columns...)); !strings.Contains(got, test.want) {
				t.Fatalf("suggestion row = %q, want bracket state %q", got, test.want)
			}
		})
	}
}

func TestSuggestionSharesPrefixListing(t *testing.T) {
	root := t.TempDir()
	writeSuggestionFile(t, root, "kustomization.yaml", `namespace: production
configMapGenerator:
  - name: config-one
  - name: config-two
  - name: config-three
  - name: config-four
secretGenerator:
  - name: secret-one
  - name: secret-two
`)
	h, _, client := newSuggestionHarness(t, root, true)
	metadataClient := client.Metadata.(*metadatafake.FakeMetadataClient)
	for _, object := range []runtime.Object{
		suggestionMetadata("ConfigMap", "production", "config-one-abc123"),
		suggestionMetadata("Secret", "production", "secret-one-def456"),
	} {
		if err := metadataClient.Tracker().Add(object); err != nil {
			t.Fatalf("seed metadata: %v", err)
		}
	}
	metadataClient.ClearActions()

	h.keys("s")
	screen := h.m.(app).stack[2].(*suggestionScreen)
	listConfigMaps, listSecrets := 0, 0
	for _, action := range metadataClient.Actions() {
		if action.GetVerb() != "list" {
			continue
		}
		switch action.GetResource().Resource {
		case "configmaps":
			listConfigMaps++
		case "secrets":
			listSecrets++
		}
	}
	if listConfigMaps != 1 || listSecrets != 1 {
		t.Fatalf("list actions: configmaps=%d secrets=%d, want 1 each", listConfigMaps, listSecrets)
	}
	if screen.pendingChecks != 0 {
		t.Fatalf("pendingChecks = %d, want 0", screen.pendingChecks)
	}
	matched := ""
	for _, row := range screen.rows {
		if row.state == rowUnchecked {
			t.Fatalf("row remained unchecked: %+v", row)
		}
		if row.sug.Kind == k8s.KindConfigMap && row.sug.Name == "config-one" {
			matched = row.matched
		}
	}
	if matched != "config-one-abc123" {
		t.Fatalf("config-one matched = %q, want config-one-abc123", matched)
	}
}

func TestSuggestionChecksAreBounded(t *testing.T) {
	st := newTestStore(t)
	proj := createProject(t, st, "api", t.TempDir(), "test-ctx", "production")
	screen := newSuggestionScreen(t.Context(), &k8s.Client{Clientset: fake.NewClientset()}, st, proj, scanConfig{}, true, editEnv{}, testStyles(true))
	_, reqID := screen.start(t.Context())
	rows := maxSuggestionChecks * 3
	result := project.ScanResult{Suggestions: make([]project.Suggestion, rows)}
	for i := range result.Suggestions {
		result.Suggestions[i] = project.Suggestion{
			Kind:       k8s.KindSecret,
			Namespace:  fmt.Sprintf("namespace-%d", i),
			Name:       fmt.Sprintf("secret-%d", i),
			NamePrefix: i%2 == 1,
		}
	}

	_, cmd := screen.Update(scanDoneMsg{reqID: reqID, result: result})
	initialMsg := cmd()
	initialBatch, ok := initialMsg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("initial command returned %T, want tea.BatchMsg", initialMsg)
	}
	if len(initialBatch) != maxSuggestionChecks+1 {
		t.Fatalf("initial batch size = %d, want %d checks plus spinner", len(initialBatch), maxSuggestionChecks)
	}
	ready := append([]tea.Cmd(nil), initialBatch[:maxSuggestionChecks]...)
	outstanding := len(ready)
	maxOutstanding := outstanding
	if want := rows - maxSuggestionChecks; len(screen.queuedChecks) != want {
		t.Fatalf("initial queued checks = %d, want %d", len(screen.queuedChecks), want)
	}

	for len(ready) > 0 {
		check := ready[0]
		ready = ready[1:]
		msg := check()
		if batch, ok := msg.(tea.BatchMsg); ok {
			ready = append(ready, batch...)
			continue
		}
		outstanding--
		queuedBefore := len(screen.queuedChecks)
		_, next := screen.Update(msg)

		queuedAfter := len(screen.queuedChecks)
		dispatched := queuedBefore - queuedAfter
		if next != nil {
			ready = append(ready, next)
		}
		outstanding += dispatched
		if outstanding > maxOutstanding {
			maxOutstanding = outstanding
		}
		if outstanding > maxSuggestionChecks {
			t.Fatalf("outstanding checks = %d, want at most %d", outstanding, maxSuggestionChecks)
		}

		wantQueued := queuedBefore
		if queuedBefore > 0 {
			wantQueued--
		}
		if queuedAfter != wantQueued {
			t.Fatalf("queued checks after %T = %d, want %d", msg, queuedAfter, wantQueued)
		}
	}

	if screen.pendingChecks != 0 {
		t.Fatalf("pending checks = %d, want 0", screen.pendingChecks)
	}
	if outstanding != 0 {
		t.Fatalf("outstanding checks = %d, want 0", outstanding)
	}
	if maxOutstanding != maxSuggestionChecks {
		t.Fatalf("max outstanding checks = %d, want %d", maxOutstanding, maxSuggestionChecks)
	}
	for i, row := range screen.rows {
		if row.state == rowUnchecked {
			t.Fatalf("row %d remained unchecked", i)
		}
	}
}

func TestSuggestionStaleAndCancel(t *testing.T) {
	st := newTestStore(t)
	proj := createProject(t, st, "api", t.TempDir(), "test-ctx", "production")
	client := &k8s.Client{Clientset: fake.NewClientset(), Context: "test-ctx"}
	screen := newSuggestionScreen(t.Context(), client, st, proj, scanConfig{}, true, editEnv{}, testStyles(true))
	cmd := screen.Init()
	if cmd == nil || !screen.pending {
		t.Fatal("Init() did not start scan")
	}
	oldReqID := screen.reqID
	_, _ = screen.Update(key("esc"))
	if screen.pending || !screen.cancelled {
		t.Fatalf("cancelled screen pending/cancelled = %t/%t", screen.pending, screen.cancelled)
	}
	staleResult := project.ScanResult{Suggestions: []project.Suggestion{{Kind: k8s.KindSecret, Name: "stale"}}}
	_, _ = screen.Update(scanDoneMsg{reqID: oldReqID, result: staleResult})
	_, _ = screen.Update(suggestionCheckedMsg{reqID: oldReqID, index: 0, found: true})
	if len(screen.rows) != 0 {
		t.Fatalf("stale messages populated rows: %+v", screen.rows)
	}
	_, cmd = screen.Update(key("ctrl+r"))
	if cmd == nil || !screen.pending || screen.reqID == oldReqID {
		t.Fatalf("rescan pending/reqID/cmd = %t/%d/%v", screen.pending, screen.reqID, cmd)
	}
}

func TestSuggestionLinkEscapeWaitsForResult(t *testing.T) {
	st := newTestStore(t)
	proj := createProject(t, st, "api", t.TempDir(), "test-ctx", "production")
	screen := newSuggestionScreen(t.Context(), &k8s.Client{Clientset: fake.NewClientset()}, st, proj, scanConfig{}, false, editEnv{}, testStyles(true))
	screen.rows = []suggestionRow{{sug: project.Suggestion{Kind: k8s.KindSecret, Name: "app-secrets"}, ns: "production", state: rowNotFound}}
	_ = screen.setItems()

	cmd := screen.linkSelected()
	if cmd == nil || !screen.linkLoader.pending {
		t.Fatal("linkSelected() did not start link")
	}
	result := cmd().(suggestionLinkedMsg)
	_, _ = screen.Update(key("esc"))
	hints := plainFooter(t, screen, 1)
	if !screen.linkLoader.pending || screen.cancelled || screen.rows[0].state == rowLinkFailed || hints != "linking (cannot cancel)" {
		t.Fatalf("pending link state = pending %t cancelled %t row %v hints %q", screen.linkLoader.pending, screen.cancelled, screen.rows[0].state, hints)
	}
	_, _ = screen.Update(result)
	if screen.linkLoader.pending || screen.rows[0].state != rowLinked || strings.Contains(screen.View(), "link failed") {
		t.Fatalf("linked result state = pending %t row %v view %q", screen.linkLoader.pending, screen.rows[0].state, screen.View())
	}
}

func TestProjectViewScanKey(t *testing.T) {
	st := newTestStore(t)
	proj := createProject(t, st, "api", t.TempDir(), "test-ctx", "production")
	h := newHarness(t, Options{ASCII: true, Store: st, Project: &proj})
	projectView := h.m.(app).stack[1].(*projectScreen)
	h.keys("s")
	if _, ok := h.m.(app).stack[1].(*projectScreen); !ok || len(h.m.(app).stack) != 2 {
		t.Fatalf("s while pending changed stack: %+v", h.m.(app).stack)
	}
	h.send(
		projectLinksMsg{reqID: projectView.reqID},
		projectContextMsg{reqID: projectView.reqID, found: true},
	)
	h.keys("s")
	if _, ok := h.m.(app).stack[2].(*suggestionScreen); !ok {
		t.Fatalf("s pushed %T, want *suggestionScreen", h.m.(app).stack[2])
	}
}

func suggestionViewAppearanceHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	root := t.TempDir()
	writeSuggestionFile(t, root, "k8s/app.yaml", suggestionDeploymentYAML)
	writeSuggestionFile(t, root, "base/kustomization.yaml", "namespace: production\nconfigMapGenerator:\n  - name: app-config\n")
	writeSuggestionFile(t, root, ".gitlab-ci.yml", "deploy: kubectl apply -f app.yaml --namespace staging\n")
	h, _, client := newSuggestionHarnessOptions(t, root, true, ascii)
	seedSuggestionObjects(t, client)
	client.Clientset.(*fake.Clientset).PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	h.keys("s")
	assertLinesFitWidth(t, h.view(), 80)
	return h
}

func newSuggestionHarness(t *testing.T, root string, active bool) (*harness, *store.Store, *k8s.Client) {
	t.Helper()
	return newSuggestionHarnessOptions(t, root, active, true)
}

func newSuggestionHarnessOptions(t *testing.T, root string, active, ascii bool) (*harness, *store.Store, *k8s.Client) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	st := newTestStore(t)
	contextName := "other-ctx"
	if active {
		contextName = "test-ctx"
	}
	proj := createProject(t, st, "api", root, contextName, "production")
	h := newHarness(t, Options{ASCII: ascii, Store: st, Project: &proj})
	projectScreen := h.m.(app).stack[1].(*projectScreen)
	h.send(
		projectLinksMsg{reqID: projectScreen.reqID},
		projectContextMsg{reqID: projectScreen.reqID, found: active},
	)
	return h, st, h.m.(app).client
}

func seedSuggestionObjects(t *testing.T, client *k8s.Client) {
	t.Helper()
	clientset := client.Clientset.(*fake.Clientset)
	if _, err := clientset.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "production"}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("production").Create(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-secrets", Namespace: "production"}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if _, err := clientset.CoreV1().ConfigMaps("production").Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config-abc123", Namespace: "production"}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create configmap: %v", err)
	}
	if _, err := clientset.AppsV1().Deployments("production").Create(t.Context(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "production"}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	metadataClient := client.Metadata.(*metadatafake.FakeMetadataClient)
	for _, object := range []runtime.Object{
		suggestionMetadata("Secret", "production", "app-secrets"),
		suggestionMetadata("ConfigMap", "production", "app-config-abc123"),
	} {
		if err := metadataClient.Tracker().Add(object); err != nil {
			t.Fatalf("seed metadata: %v", err)
		}
	}
}

func suggestionMetadata(kind, namespace, name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: kind},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

func linkSuggestion(t *testing.T, h *harness, screen *suggestionScreen, kind, name string) {
	t.Helper()
	for i, row := range screen.rows {
		if row.sug.Kind == kind && row.sug.Name == name {
			screen.list.Select(i)
			h.keys("enter")
			passCommitGate(h)
			if screen.rows[i].state != rowLinked {
				t.Fatalf("link %s/%s state = %v", kind, name, screen.rows[i].state)
			}
			return
		}
	}
	t.Fatalf("missing suggestion %s/%s in %+v", kind, name, screen.rows)
}

func writeSuggestionFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
}

const suggestionDeploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: production
spec:
  template:
    spec:
      containers:
        - name: web
          image: example.invalid/web
          envFrom:
            - secretRef:
                name: app-secrets
`

const suggestionPodYAML = `apiVersion: v1
kind: Pod
metadata:
  name: helper
spec:
  containers:
    - name: helper
      image: example.invalid/helper
      envFrom:
        - secretRef:
            name: app-secrets
`
