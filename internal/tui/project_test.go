package tui

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/NoahHakansson/sk64/internal/undo"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestGolden_ProjectOverlayList(t *testing.T) {
	const activeServer = "https://gateway.example/clusters/production"
	tests := []struct {
		name   string
		width  int
		golden string
	}{
		{name: "80 columns", width: 80, golden: "project_overlay_list"},
		{name: "60 columns", width: 60, golden: "project_overlay_list_60"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			local := setProjectServer(t, st, createProject(t, st, "local", "/repos/local", "test-ctx", "default"), activeServer)
			foreign := setProjectServer(t, st, createProject(t, st, "production", "/repos/production", "prod-ctx", "production"), "https://prod.example")
			mismatched := setProjectServer(t, st, createProject(t, st, "stale", "/repos/stale", "test-ctx", "default"), "https://gateway.example/clusters/staging")
			h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
			model := h.m.(app)
			model.client.Server = activeServer
			h.m = model
			h.send(tea.WindowSizeMsg{Width: test.width, Height: 24})
			h.keys("ctrl+p")
			overlay := h.m.(app).overlay.(*projectOverlay)
			h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{local, foreign, mismatched}})
			if test.width == 60 {
				description := overlay.list.Items()[2].(projectItem).Description()
				for _, tail := range []string{"/clusters/staging", "/clusters/production"} {
					if !strings.Contains(description, tail) {
						t.Fatalf("60-column mismatch lost distinguishing tail %q: %q", tail, description)
					}
				}
			}
			h.golden(test.golden)
		})
	}
}

func TestProjectPickerMarksContextAndServerStatus(t *testing.T) {
	st := testStyles(true)
	tests := []struct {
		name            string
		project         store.Project
		wantStatus      projectItemStatus
		wantTitle       string
		wantDescription []string
	}{
		{
			name: "active",
			project: store.Project{
				Name: "local", RootPath: "/repos/local", KubeContext: "test-ctx", KubeServer: "https://test.example",
			},
			wantStatus:      projectItemActive,
			wantTitle:       newGlyphs(true).activeTag,
			wantDescription: []string{"context: test-ctx", "saved server: https://test.example"},
		},
		{
			name: "inactive",
			project: store.Project{
				Name: "production", RootPath: "/repos/production", KubeContext: "prod-ctx", KubeServer: "https://prod.example",
			},
			wantStatus:      projectItemInactive,
			wantTitle:       newGlyphs(true).inactiveTag,
			wantDescription: []string{"context: prod-ctx", "saved server: https://prod.example"},
		},
		{
			name: "matching context name with different server",
			project: store.Project{
				Name: "stale", RootPath: "/repos/stale", KubeContext: "test-ctx", KubeServer: "https://old.example",
			},
			wantStatus:      projectItemServerMismatch,
			wantTitle:       newGlyphs(true).serverMismatchTag,
			wantDescription: []string{"context: test-ctx", "saved https://old.example", "active https://test.example"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := projectItem{project: test.project, currentContext: "test-ctx", currentServer: "https://test.example", styles: st}
			if got := item.status(); got != test.wantStatus {
				t.Fatalf("status = %v, want %v", got, test.wantStatus)
			}
			if title := item.Title(); !strings.Contains(title, test.wantTitle) {
				t.Fatalf("title = %q, want marker %q", title, test.wantTitle)
			}
			for _, want := range test.wantDescription {
				if description := item.Description(); !strings.Contains(description, want) {
					t.Fatalf("description = %q, want %q", description, want)
				}
			}
		})
	}

	overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
	overlay.SetSize(80, 22)
	_, reqID := overlay.start(t.Context())
	projects := make([]store.Project, len(tests))
	for i := range tests {
		projects[i] = tests[i].project
	}
	overlay.Update(projectsLoadedMsg{reqID: reqID, projects: projects})
	view := ansi.Strip(overlay.View())
	for _, want := range []string{"[active]", "[inactive]", "[server mismatch]", "saved https://old.example", "active https://test.example"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}
}

func TestProjectViewAndPickerShareClusterStateTags(t *testing.T) {
	st := testStyles(true)
	link := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}
	tests := []struct {
		name       string
		viewState  projectCtxState
		pickerItem projectItem
	}{
		{
			name:      "inactive",
			viewState: projectCtxInactive,
			pickerItem: projectItem{
				project: store.Project{KubeContext: "saved"}, currentContext: "active", styles: st,
			},
		},
		{
			name:      "server mismatch",
			viewState: projectCtxServerMismatch,
			pickerItem: projectItem{
				project:        store.Project{KubeContext: "active", KubeServer: "https://saved.example"},
				currentContext: "active", currentServer: "https://active.example", styles: st,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			screen := &projectScreen{dialog: newDialog(st, false), ctxState: test.viewState}
			columns := screen.workloadColumns(link)
			if len(columns) != 1 {
				t.Fatalf("project view columns = %#v, want one state tag", columns)
			}
			if got, want := ansi.Strip(columns[0].text), ansi.Strip(test.pickerItem.statusTag()); got != want {
				t.Fatalf("project view state = %q, picker state = %q", got, want)
			}
		})
	}
}

func TestProjectViewUsesBracketTagsForUnavailableClusterStates(t *testing.T) {
	st := testStyles(true)
	link := store.WorkloadLink{Kind: k8s.KindStatefulSet, Namespace: "default", Name: "database"}

	notFound := &projectScreen{dialog: newDialog(st, false), ctxState: projectCtxNotFound}
	if got := ansi.Strip(notFound.workloadColumns(link)[0].text); got != st.glyphs.contextNotFoundTag {
		t.Fatalf("context-not-found state = %q, want %q", got, st.glyphs.contextNotFoundTag)
	}

	index := k8s.NewRefIndex()
	index.AddSourceError(k8s.SourceName(link.Kind))
	noAccess := &projectScreen{
		dialog: newDialog(st, false), ctxState: projectCtxActive,
		collectors: map[string]*refsCollector{"default": {index: index}},
	}
	if got := ansi.Strip(noAccess.workloadColumns(link)[0].text); got != st.glyphs.noAccessTag {
		t.Fatalf("no-access state = %q, want %q", got, st.glyphs.noAccessTag)
	}
}

func TestProjectStatusUsesNormalizedServerIdentity(t *testing.T) {
	tests := []struct {
		name          string
		saved, active string
		want          projectItemStatus
	}{
		{name: "identical", saved: "https://api.example", active: "https://api.example", want: projectItemActive},
		{name: "trailing slash", saved: "https://api.example/", active: "https://api.example", want: projectItemActive},
		{name: "HTTPS default port", saved: "https://api.example:443", active: "https://api.example", want: projectItemActive},
		{name: "HTTP default port", saved: "http://api.example:80", active: "http://api.example/", want: projectItemActive},
		{name: "scheme and host case", saved: "HTTPS://API.EXAMPLE", active: "https://api.example", want: projectItemActive},
		{name: "non-default port", saved: "https://api.example:6443", active: "https://api.example", want: projectItemServerMismatch},
		{name: "path prefix", saved: "https://api.example/proxy", active: "https://api.example", want: projectItemServerMismatch},
		{name: "different host", saved: "https://one.example", active: "https://two.example", want: projectItemServerMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := store.Project{Name: "api", KubeContext: "test-ctx", KubeServer: test.saved}
			item := projectItem{project: project, currentContext: "test-ctx", currentServer: test.active}
			if got := item.status(); got != test.want {
				t.Fatalf("status = %v, want %v", got, test.want)
			}
			description := item.Description()
			if !strings.Contains(description, test.saved) {
				t.Fatalf("description changed saved server %q: %q", test.saved, description)
			}
			if test.want == projectItemServerMismatch && !strings.Contains(description, test.active) {
				t.Fatalf("mismatch description omitted active server %q: %q", test.active, description)
			}
			if item.project.KubeServer != test.saved {
				t.Fatalf("stored server = %q, want exact %q", item.project.KubeServer, test.saved)
			}
		})
	}
}

func TestProjectPickerServerMismatchKeepsBothServersAtEveryWidth(t *testing.T) {
	servers := []struct {
		name   string
		saved  string
		active string
	}{
		{name: "different hosts", saved: "https://old.example", active: "https://test.example"},
		{name: "same host different paths", saved: "https://g.example/staging", active: "https://g.example/production"},
	}
	widths := []struct {
		name  string
		width int
	}{
		{name: "60 columns", width: 60},
		{name: "80 columns", width: 80},
		{name: "wide", width: 160},
	}
	for _, server := range servers {
		for _, width := range widths {
			t.Run(server.name+"/"+width.name, func(t *testing.T) {
				client := testClient()
				client.Server = server.active
				projects := []store.Project{
					{Name: "local", RootPath: "/repos/local", KubeContext: "test-ctx", KubeServer: server.active},
					{Name: "production", RootPath: "/repos/production", KubeContext: "prod-ctx", KubeServer: "https://prod.example"},
					{Name: "stale", RootPath: "/repos/stale", KubeContext: "test-ctx", KubeServer: server.saved},
				}
				overlay := newProjectOverlay(t.Context(), newTestStore(t), client, "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
				overlay.SetSize(width.width, 22)
				_, reqID := overlay.start(t.Context())
				overlay.Update(projectsLoadedMsg{reqID: reqID, projects: projects})

				item := overlay.list.Items()[2].(projectItem)
				description := item.Description()
				lines := strings.Split(description, "\n")
				wantMultiline := width.width < 160
				if got := len(lines) > 1; got != wantMultiline {
					t.Fatalf("multiline description = %t, want %t: %q", got, wantMultiline, description)
				}

				var savedForm, activeForm string
				if wantMultiline {
					if len(lines) != 3 || !strings.HasPrefix(lines[0], "context: ") || !strings.HasPrefix(lines[1], "saved ") || !strings.HasPrefix(lines[2], "active ") {
						t.Fatalf("multiline description layout = %q", description)
					}
					savedForm = strings.TrimPrefix(lines[1], "saved ")
					activeForm = strings.TrimPrefix(lines[2], "active ")
				} else {
					_, serverColumns, found := strings.Cut(description, rowColumnSeparator+"saved ")
					if !found {
						t.Fatalf("description lost saved label: %q", description)
					}
					savedForm, activeForm, found = strings.Cut(serverColumns, rowColumnSeparator+"active ")
					if !found {
						t.Fatalf("description lost active label: %q", description)
					}
				}
				if savedForm == "" || activeForm == "" || savedForm == activeForm {
					t.Fatalf("different servers rendered as saved %q active %q: %q", savedForm, activeForm, description)
				}
				if strings.Contains(description, "saved server:") || strings.Contains(description, "active server:") {
					t.Fatalf("mismatch labels differ from project view wording: %q", description)
				}
				if width.width == 160 && (savedForm != server.saved || activeForm != server.active) {
					t.Fatalf("wide description compacted servers to saved %q active %q", savedForm, activeForm)
				}
				for _, original := range []string{server.saved, server.active} {
					if !strings.Contains(item.FilterValue(), original) {
						t.Fatalf("filter value lost server %q: %q", original, item.FilterValue())
					}
				}
				if got := lipgloss.Width(description); got > overlay.itemDescriptionWidth {
					t.Fatalf("mismatch description width = %d, want <= %d: %q", got, overlay.itemDescriptionWidth, description)
				}
				assertLinesFitWidth(t, overlay.list.View(), overlay.contentWidth)
				if got := lipgloss.Height(overlay.list.View()); got > overlay.list.Height() {
					t.Fatalf("list height = %d, want <= %d:\n%s", got, overlay.list.Height(), ansi.Strip(overlay.list.View()))
				}
				view := ansi.Strip(overlay.View())
				for _, want := range []string{"[server mismatch]", "saved", savedForm, "active", activeForm} {
					if !strings.Contains(view, want) {
						t.Fatalf("rendered overlay lost %q:\n%s", want, view)
					}
				}
			})
		}
	}
}

func TestElideSharedServerPrefixPreservesDistinguishingTails(t *testing.T) {
	tests := []struct {
		name             string
		saved            string
		active           string
		width            int
		wantSavedSuffix  string
		wantActiveSuffix string
		wantElision      bool
	}{
		{
			name: "cluster paths", saved: "https://gateway.example/clusters/staging", active: "https://gateway.example/clusters/production", width: 24,
			wantSavedSuffix: "/clusters/staging", wantActiveSuffix: "/clusters/production", wantElision: true,
		},
		{
			name: "query values", saved: "https://api.example/proxy?cluster=one", active: "https://api.example/proxy?cluster=two", width: 18,
			wantSavedSuffix: "cluster=one", wantActiveSuffix: "cluster=two", wantElision: true,
		},
		{
			name: "both fit", saved: "https://api.example/one", active: "https://api.example/two", width: 40,
			wantSavedSuffix: "https://api.example/one", wantActiveSuffix: "https://api.example/two",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			saved, active := elideSharedServerPrefix(test.saved, test.active, test.width, "...")
			if !strings.HasSuffix(saved, test.wantSavedSuffix) || !strings.HasSuffix(active, test.wantActiveSuffix) {
				t.Fatalf("elided servers = saved %q active %q, want suffixes %q and %q", saved, active, test.wantSavedSuffix, test.wantActiveSuffix)
			}
			if got := strings.HasPrefix(saved, "...") && strings.HasPrefix(active, "..."); got != test.wantElision {
				t.Fatalf("both servers elided = %t, want %t: saved %q active %q", got, test.wantElision, saved, active)
			}
			if saved == active {
				t.Fatalf("different servers rendered identically: %q", saved)
			}
			if lipgloss.Width(saved) > test.width || lipgloss.Width(active) > test.width {
				t.Fatalf("elided widths = %d/%d, want <= %d", lipgloss.Width(saved), lipgloss.Width(active), test.width)
			}
		})
	}
}

func TestProjectOverlaySelectsLastProject(t *testing.T) {
	st := newTestStore(t)
	createProject(t, st, "first", "/repos/first", "test-ctx", "default")
	second := createProject(t, st, "second", "/repos/second", "test-ctx", "default")

	tests := []struct {
		name      string
		last      string
		wantIndex int
	}{
		{name: "known", last: second.Name, wantIndex: 1},
		{name: "unknown", last: "missing", wantIndex: 0},
		{name: "empty", wantIndex: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := st.SetLastProject(t.Context(), test.last); err != nil {
				t.Fatalf("SetLastProject(%q) error = %v", test.last, err)
			}
			overlay := newProjectOverlay(t.Context(), st, testClient(), "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
			load := overlay.loadProjects()
			overlay.Update(load())
			if got := overlay.list.Index(); got != test.wantIndex {
				t.Fatalf("selected index = %d, want %d", got, test.wantIndex)
			}
		})
	}
}

func TestProjectOverlayResponsiveSize(t *testing.T) {
	st := newTestStore(t)
	createProject(t, st, "first", "/repos/first", "test-ctx", "default")
	createProject(t, st, "second", "/repos/second", "test-ctx", "default")
	overlay := newProjectOverlay(t.Context(), st, testClient(), "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
	overlay.Update(overlay.loadProjects()())
	overlay.Update(key("down"))

	overlay.SetSize(80, 22)
	if overlay.boxWidth != 70 || overlay.contentWidth != 64 {
		t.Fatalf("standard widths = %d/%d, want 70/64", overlay.boxWidth, overlay.contentWidth)
	}
	if overlay.list.Width() != overlay.contentWidth || overlay.list.Height() != 14 {
		t.Fatalf("standard list size = %dx%d, want %dx14", overlay.list.Width(), overlay.list.Height(), overlay.contentWidth)
	}
	if got := ansi.StringWidth(strings.Split(overlay.View(), "\n")[0]); got != overlay.boxWidth {
		t.Fatalf("rendered width = %d, want %d", got, overlay.boxWidth)
	}

	overlay.SetSize(160, 40)
	if overlay.boxWidth != 120 || overlay.contentWidth != 114 || overlay.list.Height() != 20 {
		t.Fatalf("tall size = %d/%d list height %d, want 120/114 and 20", overlay.boxWidth, overlay.contentWidth, overlay.list.Height())
	}
	if overlay.list.Index() != 1 {
		t.Fatalf("resize moved selection to %d, want 1", overlay.list.Index())
	}
	overlay.state = projectOverlayLinked
	overlay.linkedName = "second"
	if got := ansi.StringWidth(strings.Split(overlay.View(), "\n")[0]); got != overlay.boxWidth {
		t.Fatalf("linked-state width = %d, want %d", got, overlay.boxWidth)
	}
}

func TestProjectExecOfferRequiresUppercase(t *testing.T) {
	overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
	overlay.SetSize(80, 22)
	overlay.state = projectOverlayExecOffer
	overlay.selected = store.Project{ID: 1, Name: "api", KubeContext: "prod-ctx"}

	if cmd := overlay.Update(key("y")); cmd != nil {
		t.Fatal("lowercase y returned an exec command")
	}
	if overlay.state != projectOverlayExecOffer || !overlay.execNudge {
		t.Fatalf("lowercase y state = %d nudge %t", overlay.state, overlay.execNudge)
	}
	if view := ansi.Strip(overlay.View()); !strings.Contains(view, pressYToConfirm) {
		t.Fatalf("lowercase y view has no confirmation nudge:\n%s", view)
	}
	if cmd := overlay.Update(key("Y")); cmd == nil {
		t.Fatal("uppercase Y did not return an exec command")
	}

	overlay.Update(execProbeDoneMsg{name: "prod-ctx", err: errors.New("login failed")})
	if cmd := overlay.Update(key("enter")); cmd == nil {
		t.Fatal("retry did not start a project identity check")
	}
	overlay.Update(projectIdentityResolvedMsg{
		reqID:   overlay.reqID,
		project: overlay.selected,
		err:     errors.New("getting credentials: exec: login required"),
	})
	if overlay.state != projectOverlayError || overlay.execNudge || strings.Contains(ansi.Strip(overlay.View()), pressYToConfirm) {
		t.Fatalf("identity retry kept stale nudge: state %d nudge %t view %q", overlay.state, overlay.execNudge, ansi.Strip(overlay.View()))
	}
}

func TestGolden_ProjectOverlayUnavailable(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("ctrl+p")
	h.golden("project_overlay_unavailable")
}

func TestProjectOverlayInitialLoadErrorDoesNotBecomeEmptyList(t *testing.T) {
	st := newTestStore(t)
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, err: errors.New("disk failed")})

	view := h.view()
	if overlay.state != projectOverlayError || overlay.listReady || !strings.Contains(view, "[error]") || !strings.Contains(view, "enter to retry") {
		t.Fatalf("initial project error state/list/view = %v/%t:\n%s", overlay.state, overlay.listReady, view)
	}
	if strings.Contains(view, "no projects yet") || strings.Contains(view, "[empty]") {
		t.Fatalf("initial project error collapsed to empty state:\n%s", view)
	}

	h.keys("esc")
	if h.m.(app).overlay != nil {
		t.Fatalf("esc from initial project error left overlay %T", h.m.(app).overlay)
	}
}

func TestGolden_ProjectOverlayLinkPicker(t *testing.T) {
	workloadLink := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "production", Name: "web"}
	resourceLink := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "db-creds", Source: store.SourceManual}
	tests := []struct {
		name          string
		link          pendingLink
		pickerGolden  string
		linkingGolden string
		linkedGolden  string
		failedGolden  string
	}{
		{
			name:          "workload",
			link:          pendingLink{workload: &workloadLink},
			pickerGolden:  "project_overlay_link_picker",
			linkingGolden: "project_overlay_linking",
			linkedGolden:  "project_overlay_linked",
			failedGolden:  "project_overlay_link_failed",
		},
		{
			name:          "resource",
			link:          pendingLink{resource: &resourceLink},
			pickerGolden:  "project_overlay_resource_link_picker",
			linkingGolden: "project_overlay_resource_linking",
			linkedGolden:  "project_overlay_resource_linked",
			failedGolden:  "project_overlay_resource_link_failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project := setProjectServer(t, st, createProject(t, st, "api", "/repos/api", "test-ctx", "default"), "https://test.example")
			h := linkPickerHarness(t, st, project, test.link)
			subject := test.link.subject()
			if view := h.view(); !strings.Contains(view, "Link "+subject+" to project") {
				t.Fatalf("picker missing subject %q:\n%s", subject, view)
			}
			h.golden(test.pickerGolden)

			h.keys("enter")
			passCommitGate(h)
			overlay := h.m.(app).overlay.(*projectOverlay)
			if overlay.state != projectOverlayLinking || !strings.Contains(h.view(), "link "+subject+" -> "+project.Name) {
				t.Fatalf("linking state/view = %v:\n%s", overlay.state, h.view())
			}
			h.golden(test.linkingGolden)

			h.send(projectLinkedMsg{reqID: overlay.reqID, projectName: project.Name})
			if count := strings.Count(h.view(), subject); count < 2 {
				t.Fatalf("linked view repeats subject %d times, want at least 2:\n%s", count, h.view())
			}
			h.golden(test.linkedGolden)

			failureHarness := linkPickerHarness(t, st, project, test.link)
			failureHarness.keys("enter")
			passCommitGate(failureHarness)
			failureOverlay := failureHarness.m.(app).overlay.(*projectOverlay)
			failureHarness.send(projectLinkedMsg{reqID: failureOverlay.reqID, projectName: project.Name, err: errors.New("database is read-only")})
			failureView := failureHarness.view()
			if count := strings.Count(failureView, subject); count < 2 {
				t.Fatalf("failed view repeats subject %d times, want at least 2:\n%s", count, failureView)
			}
			for _, want := range []string{"database is read-only", "enter to close"} {
				if !strings.Contains(failureView, want) {
					t.Fatalf("failed view missing %q:\n%s", want, failureView)
				}
			}
			failureHarness.golden(test.failedGolden)
		})
	}
}

func TestProjectOverlayLinkFailureKeepsReasonActionAndIdentity(t *testing.T) {
	linkTypes := []struct {
		name       string
		kind       string
		isResource bool
	}{
		{name: "workload", kind: k8s.KindDeployment},
		{name: "resource", kind: k8s.KindSecret, isResource: true},
	}
	scenarios := []struct {
		name        string
		longSubject bool
		longError   bool
	}{
		{name: "long subject with short error", longSubject: true},
		{name: "short subject with long error", longError: true},
		{name: "both long", longSubject: true, longError: true},
	}
	for _, linkType := range linkTypes {
		for _, scenario := range scenarios {
			t.Run(linkType.name+"/"+scenario.name, func(t *testing.T) {
				namespace := "prod"
				name := "db"
				if scenario.longSubject {
					namespace = "production-environment-with-a-long-name"
					name = linkType.name + "-component-link-tail"
				}
				var link pendingLink
				if linkType.isResource {
					resource := store.ResourceLink{Kind: linkType.kind, Namespace: namespace, Name: name, Source: store.SourceManual}
					link.resource = &resource
				} else {
					workload := store.WorkloadLink{Kind: linkType.kind, Namespace: namespace, Name: name}
					link.workload = &workload
				}
				errorText := "database is read-only"
				if scenario.longError {
					errorText += " while opening project metadata after the filesystem was remounted"
				}

				for _, width := range []int{60, 80} {
					t.Run(fmt.Sprintf("%d columns", width), func(t *testing.T) {
						overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), "", "", scanConfig{}, nil, projectModeLink, link, packageDefaultKeyMaps, testStyles(true))
						overlay.SetSize(width, 22)
						overlay.state = projectOverlayLinked
						overlay.linkedName = "api"
						overlay.err = errors.New(errorText)

						rendered := overlay.View()
						view := ansi.Strip(rendered)
						var identityLine, errorLine string
						for _, line := range strings.Split(view, "\n") {
							if strings.Contains(line, " -> api failed") {
								identityLine = line
							}
							if strings.Contains(line, "database is read-only") {
								errorLine = line
							}
						}
						if errorLine == "" || !strings.Contains(errorLine, "enter to close") {
							t.Fatalf("failure reason/action not preserved at %d columns:\n%s", width, view)
						}
						if !strings.Contains(identityLine, "link "+linkType.kind) {
							t.Fatalf("failure identity lost kind at %d columns: %q\n%s", width, identityLine, view)
						}
						if scenario.longSubject {
							if !strings.Contains(identityLine, overlay.styles.glyphs.ellipsis) || !strings.Contains(identityLine, "link-tail") {
								t.Fatalf("failure identity was not middle-elided at %d columns: %q", width, identityLine)
							}
						} else if !strings.Contains(identityLine, link.subject()) {
							t.Fatalf("failure identity lost short subject at %d columns: %q", width, identityLine)
						}
						for lineNumber, line := range strings.Split(rendered, "\n") {
							if got := lipgloss.Width(line); got > width {
								t.Fatalf("line %d width = %d, want <= %d: %q", lineNumber+1, got, width, ansi.Strip(line))
							}
						}
					})
				}
			})
		}
	}
}

func TestProjectOverlayLinkStatesKeepPendingSubject(t *testing.T) {
	workloadLink := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "production", Name: "web"}
	resourceLink := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "db-creds", Source: store.SourceManual}
	tests := []struct {
		name string
		link pendingLink
	}{
		{name: "workload", link: pendingLink{workload: &workloadLink}},
		{name: "resource", link: pendingLink{resource: &resourceLink}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := store.Project{ID: 1, Name: "api"}
			for _, outcome := range []struct {
				name string
				err  error
			}{
				{name: "success"},
				{name: "failure", err: errors.New("database is read-only")},
			} {
				t.Run(outcome.name, func(t *testing.T) {
					overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), "", "", scanConfig{}, nil, projectModeLink, test.link, packageDefaultKeyMaps, testStyles(true))
					overlay.SetSize(80, 22)
					overlay.state = projectOverlayList
					if view := ansi.Strip(overlay.View()); !strings.Contains(view, "Link "+test.link.subject()+" to project") {
						t.Fatalf("picker missing subject:\n%s", view)
					}

					_ = overlay.startLink(project)
					if view := ansi.Strip(overlay.View()); overlay.state != projectOverlayLinking || !strings.Contains(view, "link "+test.link.subject()+" -> api") {
						t.Fatalf("linking state/view = %v:\n%s", overlay.state, view)
					}

					overlay.Update(projectLinkedMsg{reqID: overlay.reqID, projectName: project.Name, err: outcome.err})
					view := ansi.Strip(overlay.View())
					if overlay.state != projectOverlayLinked || strings.Count(view, test.link.subject()) < 2 {
						t.Fatalf("linked state/view = %v:\n%s", overlay.state, view)
					}
				})
			}
		})
	}
}

func TestProjectOverlayLinkingKeepsIdentityAtMinimumWidth(t *testing.T) {
	link := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "db-creds", Source: store.SourceManual}
	overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), "", "", scanConfig{}, nil, projectModeLink, pendingLink{resource: &link}, packageDefaultKeyMaps, testStyles(true))
	overlay.SetSize(minimumWidth, bodyHeight(minimumHeight))
	_ = overlay.startLink(store.Project{ID: 1, Name: "api"})

	view := ansi.Strip(overlay.View())
	for _, want := range []string{"Secret production/db-creds", "project api", "cannot cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("minimum-width linking view lost %q:\n%s", want, view)
		}
	}
}

func TestDispatchedProjectWritesIgnoreEscapeAndReconcileResult(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "project form create and edit",
			run: func(t *testing.T) {
				t.Run("create", func(t *testing.T) {
					st := newTestStore(t)
					h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
					form := newProjectFormScreen(t.Context(), st, "", scanConfig{}, formCreate, nil, store.ProjectMeta{
						Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx",
						KubeServer: "https://test.example", Namespace: "default",
					}, nil, packageDefaultKeyMaps, h.m.(app).styles)
					h.send(pushScreenMsg{s: form})
					result := form.submit()().(formResultMsg)
					depth := len(h.m.(app).stack)

					h.keys("esc")

					hints := plainFooter(t, form, 1)
					if form.stop() || !form.pending || len(h.m.(app).stack) != depth || hints != "saving (cannot cancel)" {
						t.Fatalf("create save pending/stack/hints = %t/%d/%q", form.pending, len(h.m.(app).stack), hints)
					}
					h.send(result)
					if _, ok := h.m.(app).stack[1].(*projectScreen); !ok {
						t.Fatalf("create result opened %T, want *projectScreen", h.m.(app).stack[1])
					}
					if _, err := st.ProjectByName(t.Context(), "api"); err != nil {
						t.Fatalf("created project lookup: %v", err)
					}
				})

				t.Run("edit", func(t *testing.T) {
					st := newTestStore(t)
					project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
					h, screen := projectHarness(t, st, project, "")
					h.send(projectLinksMsg{reqID: screen.reqID}, projectContextMsg{reqID: screen.reqID, found: true})
					h.keys("e")
					form := h.m.(app).stack[2].(*projectFormScreen)
					form.nameInput.SetValue("renamed")
					result := form.submit()().(formResultMsg)
					depth := len(h.m.(app).stack)

					h.keys("esc")

					hints := plainFooter(t, form, 1)
					if form.stop() || !form.pending || len(h.m.(app).stack) != depth || hints != "saving (cannot cancel)" {
						t.Fatalf("edit save pending/stack/hints = %t/%d/%q", form.pending, len(h.m.(app).stack), hints)
					}
					h.send(result)
					if top := h.m.(app).stack[len(h.m.(app).stack)-1].(*projectScreen); top.project.Name != "renamed" {
						t.Fatalf("edited project name = %q, want renamed", top.project.Name)
					}
				})
			},
		},
		{
			name: "context binding",
			run: func(t *testing.T) {
				st := newTestStore(t)
				project := setProjectServer(t, st, createProject(t, st, "api", "/repos/api", "test-ctx", "default"), "https://old.example")
				h := newHarness(t, Options{
					ASCII: true, Store: st, Project: &project, ConfirmProjectContext: true,
					ProjectContextTarget: k8s.ContextInfo{Name: "test-ctx", Server: "https://test.example"},
				})
				confirm := h.m.(app).stack[2].(*projectContextConfirm)
				saveBatch := confirm.startSwitch(false)().(tea.BatchMsg)
				result := saveBatch[0]().(projectBindingSavedMsg)
				depth := len(h.m.(app).stack)

				h.keys("esc")

				hints := plainFooter(t, confirm, 1)
				if confirm.stop() || !confirm.pending || confirm.state != projectContextConfirmSaving || len(h.m.(app).stack) != depth || hints != "saving (cannot cancel)" {
					t.Fatalf("binding save pending/state/stack/hints = %t/%v/%d/%q", confirm.pending, confirm.state, len(h.m.(app).stack), hints)
				}
				if view := ansi.Strip(confirm.View()); !strings.Contains(view, "saving (cannot cancel)") {
					t.Fatalf("binding save view claims cancellation:\n%s", view)
				}
				h.send(result)
				stored, err := st.ProjectByName(t.Context(), project.Name)
				if err != nil || stored.KubeServer != "https://test.example" {
					t.Fatalf("stored binding server = %q, %v", stored.KubeServer, err)
				}
				if h.m.(app).client.Server != "https://test.example" {
					t.Fatalf("active client server = %q, want reconciled server", h.m.(app).client.Server)
				}
			},
		},
		{
			name: "project picker link",
			run: func(t *testing.T) {
				st := newTestStore(t)
				project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
				link := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "db-creds", Source: store.SourceManual}
				h := linkPickerHarness(t, st, project, pendingLink{resource: &link})
				overlay := h.m.(app).overlay.(*projectOverlay)
				linkBatch := overlay.startLink(project)().(tea.BatchMsg)
				result := linkBatch[0]().(projectLinkedMsg)

				h.keys("esc")

				hints := plainHints(t, overlay.Hints())
				if overlay.stop() || !overlay.pending || overlay.state != projectOverlayLinking || h.m.(app).overlay != overlay || hints != "linking (cannot cancel)" {
					t.Fatalf("picker link pending/state/overlay/hints = %t/%v/%T/%q", overlay.pending, overlay.state, h.m.(app).overlay, hints)
				}
				h.send(result)
				links, err := st.ResourceLinks(t.Context(), project.ID)
				if err != nil || len(links) != 1 || overlay.state != projectOverlayLinked || overlay.err != nil {
					t.Fatalf("picker link result = %+v, %v; state/error %v/%v", links, err, overlay.state, overlay.err)
				}
			},
		},
		{
			name: "suggestion link",
			run: func(t *testing.T) {
				root := t.TempDir()
				writeSuggestionFile(t, root, "pod.yaml", suggestionPodYAML)
				h, st, _ := newSuggestionHarness(t, root, true)
				h.keys("s")
				screen := h.m.(app).stack[2].(*suggestionScreen)
				result := screen.linkSelected()().(suggestionLinkedMsg)
				selected := screen.list.SelectedItem().(suggestionItem)
				depth := len(h.m.(app).stack)

				h.keys("esc")

				hints := plainFooter(t, screen, 1)
				if screen.stop() || !screen.linkLoader.pending || len(h.m.(app).stack) != depth || hints != "linking (cannot cancel)" || selected.row.state == rowLinkFailed {
					t.Fatalf("suggestion link pending/stack/hints/state = %t/%d/%q/%v", screen.linkLoader.pending, len(h.m.(app).stack), hints, selected.row.state)
				}
				h.send(result)
				links, err := st.ResourceLinks(t.Context(), screen.project.ID)
				if err != nil || len(links) != 1 || selected.row.state != rowLinked {
					t.Fatalf("suggestion link result = %+v, %v; state %v", links, err, selected.row.state)
				}
			},
		},
		{
			name: "project unlink",
			run: func(t *testing.T) {
				st := newTestStore(t)
				project := createProject(t, st, "api", "/repos/api", "gone-ctx", "default")
				link := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}
				if err := st.LinkWorkload(t.Context(), project.ID, link); err != nil {
					t.Fatalf("seed workload link: %v", err)
				}
				h, screen := projectHarness(t, st, project, "")
				h.send(projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{link}}, projectContextMsg{reqID: screen.reqID, found: false})
				screen.confirmUnlink = true
				result := screen.startUnlink()().(projectUnlinkedMsg)
				depth := len(h.m.(app).stack)

				h.keys("esc")

				hints := plainFooter(t, screen, 1)
				if screen.stop() || !screen.pending || !screen.unlinkPending || len(h.m.(app).stack) != depth || hints != "unlinking (cannot cancel)" {
					t.Fatalf("unlink pending/state/stack/hints = %t/%t/%d/%q", screen.pending, screen.unlinkPending, len(h.m.(app).stack), hints)
				}
				h.send(result)
				h.send(projectLinksMsg{reqID: screen.reqID})
				links, err := st.WorkloadLinks(t.Context(), project.ID)
				if err != nil || len(links) != 0 || screen.unlinkErr != nil || strings.Contains(screen.View(), "unlink failed") {
					t.Fatalf("unlink result = %+v, %v; error/view %v/%q", links, err, screen.unlinkErr, screen.View())
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestGolden_ProjectForm(t *testing.T) {
	st := newTestStore(t)
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st, ProjectRoot: "/repos/api"})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID})
	h.keys("N")
	h.golden("project_form")
}

func TestProjectOverlayNewProjectUsesUppercase(t *testing.T) {
	st := newTestStore(t)
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID})
	depth := len(h.m.(app).stack)

	h.keys("n")
	if len(h.m.(app).stack) != depth || h.m.(app).overlay != overlay {
		t.Fatalf("lowercase n changed stack/overlay: depth %d overlay %T", len(h.m.(app).stack), h.m.(app).overlay)
	}
	h.keys("N")
	if len(h.m.(app).stack) != depth+1 {
		t.Fatalf("uppercase N stack depth = %d, want %d", len(h.m.(app).stack), depth+1)
	}
	top := h.m.(app).stack[len(h.m.(app).stack)-1]
	if _, ok := top.(*projectFormScreen); !ok {
		t.Fatalf("uppercase N top screen = %T, want projectFormScreen", top)
	}
}

func TestProjectFormWithoutRootStartsEmpty(t *testing.T) {
	st := newTestStore(t)
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID})
	h.keys("N")

	form := h.m.(app).stack[1].(*projectFormScreen)
	if name, root := form.nameInput.Value(), form.pathInput.Value(); name != "" || root != "" {
		t.Fatalf("form name/root = %q, %q, want empty", name, root)
	}
}

func TestProjectFormPreservesInitialCommas(t *testing.T) {
	st := newTestStore(t)
	initial := store.ProjectMeta{Name: "api,", RootPath: "/repos/api,", KubeContext: "test-ctx,", Namespace: "default"}
	form := newProjectFormScreen(t.Context(), st, "", scanConfig{}, formEdit, nil, initial, nil, packageDefaultKeyMaps, testStyles(false))

	if got := form.nameInput.Value(); got != initial.Name {
		t.Fatalf("name = %q, want %q", got, initial.Name)
	}
	if got := form.pathInput.Value(); got != initial.RootPath {
		t.Fatalf("path = %q, want %q", got, initial.RootPath)
	}
	if got := form.selectedKubeContext; got != initial.KubeContext {
		t.Fatalf("context = %q, want %q", got, initial.KubeContext)
	}
}

func TestProjectFormRoutesEditingKeysOnlyToTextFields(t *testing.T) {
	form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formEdit, nil, store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(false))
	_ = form.Init()

	_, _ = form.Update(key("x"))
	if got := form.nameInput.Value(); got != "apix" {
		t.Fatalf("typed name = %q, want apix", got)
	}
	_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := form.nameInput.Value(); got != "api" {
		t.Fatalf("edited name = %q, want api", got)
	}

	_, _ = form.Update(key("tab"))
	_, _ = form.Update(key("x"))
	if got := form.pathInput.Value(); got != "/repos/apix" {
		t.Fatalf("typed path = %q, want /repos/apix", got)
	}
	_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := form.pathInput.Value(); got != "/repos/api" {
		t.Fatalf("edited path = %q, want /repos/api", got)
	}

	_, _ = form.Update(key("tab"))
	beforeName := form.nameInput.Value()
	beforePath := form.pathInput.Value()
	beforeNamespaces := form.namespacesInput.Value()
	beforeContext := form.selectedKubeContext
	_, _ = form.Update(key("x"))
	_, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if form.nameInput.Value() != beforeName || form.pathInput.Value() != beforePath ||
		form.namespacesInput.Value() != beforeNamespaces || form.selectedKubeContext != beforeContext {
		t.Fatalf("context selector accepted editing keys: name %q path %q context %q namespaces %q",
			form.nameInput.Value(), form.pathInput.Value(), form.selectedKubeContext, form.namespacesInput.Value())
	}
}

func TestProjectFormInputWidthsAccountForEachPrompt(t *testing.T) {
	form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formCreate, nil, store.ProjectMeta{}, nil, packageDefaultKeyMaps, testStyles(true))
	form.SetSize(80, 22)

	for _, input := range []*textinput.Model{&form.nameInput, &form.pathInput, &form.namespacesInput} {
		if got := lipgloss.Width(input.View()); got > form.contentWidth() {
			t.Fatalf("input view width = %d, want <= %d for prompt %q", got, form.contentWidth(), input.Prompt)
		}
	}
}

func TestProjectFormShowsSelectedServerAndSelectionOnlyContext(t *testing.T) {
	tests := []struct {
		name       string
		server     string
		wantServer string
	}{
		{name: "verified binding", server: "https://saved.example", wantServer: "selected server: https://saved.example (read-only)"},
		{name: "unverified binding", wantServer: "selected server: unverified (read-only)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			meta := store.ProjectMeta{
				Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx",
				KubeServer: test.server, Namespace: "default", SwitchPromptSuppressed: true,
			}
			form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formEdit, nil, meta, nil, packageDefaultKeyMaps, testStyles(true))
			view := ansi.Strip(form.View())
			for _, want := range []string{
				test.wantServer,
				"context is selection-only and never switches the active client",
			} {
				if !strings.Contains(view, want) {
					t.Fatalf("form view missing %q:\n%s", want, view)
				}
			}

			form.SetSize(minimumWidth, bodyHeight(minimumHeight))
			lines := strings.Split(ansi.Strip(form.View()), "\n")
			for i := range lines {
				lines[i] = strings.TrimSpace(strings.Trim(strings.TrimSpace(lines[i]), "|"))
			}
			compactView := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
			if !strings.Contains(compactView, "context is selection-only and never switches the active client") {
				t.Fatalf("minimum-size form dropped reset warning:\n%s", ansi.Strip(form.View()))
			}
		})
	}
}

func TestProjectFormScanUsesFullScannerAndInfersSafeMetadata(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o750); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "deploy.yml"), []byte("run: kubectl --context deploy-ctx --namespace staging apply -f app.yaml\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app-settings\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	kubeconfig := writeProjectKubeconfig(t, "test-ctx", []projectKubeconfigContext{
		{name: "test-ctx", server: "https://test.example"},
		{name: "deploy-ctx", server: "https://deploy.example"},
	})
	form := newProjectFormScreen(t.Context(), newTestStore(t), kubeconfig, scanConfig{}, formCreate, nil, store.ProjectMeta{
		RootPath: root, KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(true))

	msg := form.startScan()().(projectFormScanMsg)
	if msg.err != nil || len(msg.result.Suggestions) == 0 || len(msg.result.ContextHints) != 1 {
		t.Fatalf("scan result = %d suggestions, %d context hints, error %v", len(msg.result.Suggestions), len(msg.result.ContextHints), msg.err)
	}
	_, _ = form.Update(msg)

	if form.state != projectFormFields || form.nameInput.Value() != filepath.Base(root) || form.pathInput.Value() != root {
		t.Fatalf("scan draft state/name/path = %v/%q/%q", form.state, form.nameInput.Value(), form.pathInput.Value())
	}
	if form.selectedKubeContext != "deploy-ctx" || form.selectedKubeServer != "https://deploy.example" || form.contextRequired {
		t.Fatalf("scan context = %q %q required %t", form.selectedKubeContext, form.selectedKubeServer, form.contextRequired)
	}
	if got := splitNamespaces(form.namespacesInput.Value()); !slices.Equal(got, []string{"staging"}) {
		t.Fatalf("scan namespaces = %v, want [staging]", got)
	}
}

func TestProjectFormScanInferenceIsConservative(t *testing.T) {
	contexts := []k8s.ContextInfo{
		{Name: "test-ctx", Server: "https://test.example"},
		{Name: "deploy-ctx", Server: "https://deploy.example"},
	}
	tests := []struct {
		name           string
		result         project.ScanResult
		wantContext    string
		wantServer     string
		wantRequired   bool
		wantNamespaces []string
	}{
		{
			name:        "no context hints keeps active context and one namespace becomes default",
			result:      project.ScanResult{Suggestions: []project.Suggestion{{Kind: project.KindNamespace, Name: "staging"}}},
			wantContext: "test-ctx", wantServer: "https://test.example", wantNamespaces: []string{"staging"},
		},
		{
			name:        "one matching context hint is selected",
			result:      project.ScanResult{ContextHints: []string{"deploy-ctx"}},
			wantContext: "deploy-ctx", wantServer: "https://deploy.example", wantNamespaces: []string{"default"},
		},
		{
			name:         "unavailable context hint requires chooser",
			result:       project.ScanResult{ContextHints: []string{"missing-ctx"}},
			wantRequired: true, wantNamespaces: []string{"default"},
		},
		{
			name: "conflicting hints require chooser and multiple namespaces preserve active default",
			result: project.ScanResult{
				ContextHints: []string{"test-ctx", "deploy-ctx"},
				Suggestions: []project.Suggestion{
					{Kind: project.KindNamespace, Name: "production"},
					{Kind: project.KindNamespace, Name: "staging"},
					{Kind: project.KindNamespace, Name: "production"},
				},
			},
			wantRequired: true, wantNamespaces: []string{"default", "production", "staging"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formCreate, nil, store.ProjectMeta{
				RootPath: "/repos/api", KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
			}, nil, packageDefaultKeyMaps, testStyles(true))
			form.applyScan(test.result, contexts, nil)
			if form.messageIsError {
				t.Fatalf("scan message %q marked as an error", form.message)
			}
			if form.selectedKubeContext != test.wantContext || form.selectedKubeServer != test.wantServer || form.contextRequired != test.wantRequired {
				t.Fatalf("context = %q %q required %t, want %q %q required %t", form.selectedKubeContext, form.selectedKubeServer, form.contextRequired, test.wantContext, test.wantServer, test.wantRequired)
			}
			if got := splitNamespaces(form.namespacesInput.Value()); !slices.Equal(got, test.wantNamespaces) {
				t.Fatalf("namespaces = %v, want %v", got, test.wantNamespaces)
			}
		})
	}
}

func TestProjectFormIncompleteScanPreservesSuggestionsAndRequiresContextChoice(t *testing.T) {
	form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formCreate, nil, store.ProjectMeta{
		RootPath: "/repos/api", KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(true))
	form.applyScan(project.ScanResult{
		Incomplete:   true,
		ContextHints: []string{"deploy-ctx"},
		Suggestions:  []project.Suggestion{{Kind: project.KindNamespace, Name: "staging"}},
	}, []k8s.ContextInfo{{Name: "deploy-ctx", Server: "https://deploy.example"}}, nil)
	form.state = projectFormFields
	form.focus = projectFormName

	if form.nameInput.Value() != "api" || form.pathInput.Value() != "/repos/api" {
		t.Fatalf("incomplete scan name/path = %q/%q, want editable inferred values", form.nameInput.Value(), form.pathInput.Value())
	}
	if got := splitNamespaces(form.namespacesInput.Value()); !slices.Equal(got, []string{"staging"}) {
		t.Fatalf("incomplete scan namespaces = %v, want [staging]", got)
	}
	if form.selectedKubeContext != "" || form.selectedKubeServer != "" || !form.contextRequired || !form.scanIncomplete {
		t.Fatalf("incomplete scan context = %q %q required %t incomplete %t", form.selectedKubeContext, form.selectedKubeServer, form.contextRequired, form.scanIncomplete)
	}
	if form.validateInputs() {
		t.Fatal("incomplete scan validated without an explicit context choice")
	}
	if !form.messageIsError {
		t.Fatalf("validation message %q was not marked as an error", form.message)
	}

	form.SetSize(60, 15)
	view := ansi.Strip(form.View())
	for _, want := range []string{"scan incomplete", "choose a local context", "name: api", "namespaces: staging"} {
		if !strings.Contains(view, want) {
			t.Fatalf("60x15 incomplete scan form missing %q:\n%s", want, view)
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) > 15 {
		t.Fatalf("60x15 incomplete scan form height = %d, want <= 15:\n%s", len(lines), view)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width > 60 {
			t.Fatalf("60x15 incomplete scan line %d width = %d, want <= 60: %q", index+1, width, line)
		}
	}
}

func TestProjectFormRejectsStaleScanResults(t *testing.T) {
	form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formCreate, nil, store.ProjectMeta{
		RootPath: t.TempDir(), KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(true))
	_ = form.startScan()
	staleReqID := form.reqID
	_ = form.startScan()
	freshReqID := form.reqID

	_, _ = form.Update(projectFormScanMsg{
		reqID:  staleReqID,
		result: project.ScanResult{Suggestions: []project.Suggestion{{Kind: project.KindNamespace, Name: "stale"}}},
	})
	if !form.pending || form.state != projectFormScanning || form.namespacesInput.Value() != "default" {
		t.Fatalf("stale scan changed pending/state/namespaces = %t/%v/%q", form.pending, form.state, form.namespacesInput.Value())
	}
	_, _ = form.Update(projectFormScanMsg{
		reqID:  freshReqID,
		result: project.ScanResult{Suggestions: []project.Suggestion{{Kind: project.KindNamespace, Name: "fresh"}}},
	})
	if form.pending || form.state != projectFormFields || form.namespacesInput.Value() != "fresh" {
		t.Fatalf("fresh scan pending/state/namespaces = %t/%v/%q", form.pending, form.state, form.namespacesInput.Value())
	}
}

func TestProjectFormScanCanCancelRetryOrContinueManually(t *testing.T) {
	form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formCreate, nil, store.ProjectMeta{
		RootPath: t.TempDir(), KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(true))
	_ = form.startScan()
	if !form.pending || form.state != projectFormScanning {
		t.Fatalf("scan start pending/state = %t/%v", form.pending, form.state)
	}
	_, _ = form.Update(key("esc"))
	if form.pending || form.state != projectFormChooseStart {
		t.Fatalf("scan cancel pending/state = %t/%v", form.pending, form.state)
	}

	_ = form.startScan()
	_, _ = form.Update(projectFormScanMsg{reqID: form.reqID, err: errors.New("walk failed")})
	if form.state != projectFormScanError {
		t.Fatalf("scan failure state = %v", form.state)
	}
	_, retry := form.Update(key("r"))
	if retry == nil || form.state != projectFormScanning {
		t.Fatalf("scan retry command/state = %t/%v", retry != nil, form.state)
	}
	form.stop()
	form.state = projectFormScanError
	_, _ = form.Update(key("m"))
	if form.state != projectFormFields {
		t.Fatalf("manual continuation state = %v", form.state)
	}
}

func TestProjectFormContextChooserOnlyChangesDraftSelection(t *testing.T) {
	kubeconfig := writeProjectKubeconfig(t, "test-ctx", []projectKubeconfigContext{
		{name: "test-ctx", server: "https://test.example"},
		{name: "prod-ctx", server: "https://prod.example"},
	})
	form := newProjectFormScreen(t.Context(), newTestStore(t), kubeconfig, scanConfig{}, formEdit, nil, store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(true))
	form.focus = projectFormContext

	_, load := form.Update(key("enter"))
	if load == nil || form.state != projectFormContextsLoading {
		t.Fatalf("context load command/state = %t/%v", load != nil, form.state)
	}
	_, _ = form.Update(load())
	if form.state != projectFormContexts {
		t.Fatalf("context list state = %v", form.state)
	}
	for i, item := range form.contextList.Items() {
		if item.(contextItem).info.Name == "prod-ctx" {
			form.contextList.Select(i)
		}
	}
	_, cmd := form.Update(key("enter"))
	if cmd != nil || form.state != projectFormFields || form.selectedKubeContext != "prod-ctx" || form.selectedKubeServer != "https://prod.example" {
		t.Fatalf("context selection command/state/draft = %t/%v/%q/%q", cmd != nil, form.state, form.selectedKubeContext, form.selectedKubeServer)
	}
	contexts, err := k8s.ListContexts(kubeconfig)
	if err != nil {
		t.Fatalf("ListContexts() error = %v", err)
	}
	current, found := contextByName(contexts, "test-ctx")
	if !found || !current.Current {
		t.Fatalf("chooser changed kubeconfig current context: %+v", contexts)
	}

	form.focus = projectFormContext
	_, _ = form.Update(key("x"))
	if form.selectedKubeContext != "prod-ctx" {
		t.Fatalf("context accepted free text: %q", form.selectedKubeContext)
	}
}

func TestProjectFormContextChooserResetsFilterWhenReopened(t *testing.T) {
	kubeconfig := writeProjectKubeconfig(t, "test-ctx", []projectKubeconfigContext{
		{name: "test-ctx", server: "https://test.example"},
		{name: "prod-ctx", server: "https://prod.example"},
	})
	form := newProjectFormScreen(t.Context(), newTestStore(t), kubeconfig, scanConfig{}, formEdit, nil, store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(true))
	form.focus = projectFormContext

	_, load := form.Update(key("enter"))
	_, _ = form.Update(load())
	form.contextList.SetFilterText("prod")
	if form.contextList.FilterState() != list.FilterApplied || form.contextList.FilterInput.Value() != "prod" {
		t.Fatalf("initial chooser filter = %v/%q", form.contextList.FilterState(), form.contextList.FilterInput.Value())
	}
	_, _ = form.Update(key("esc"))
	form.focus = projectFormContext
	_, reload := form.Update(key("enter"))
	if reload == nil || form.contextList.FilterState() != list.Unfiltered || form.contextList.FilterInput.Value() != "" {
		t.Fatalf("reopened chooser filter = command %t state %v query %q", reload != nil, form.contextList.FilterState(), form.contextList.FilterInput.Value())
	}
	_, _ = form.Update(reload())
	if len(form.contextList.Items()) != 2 || len(form.contextList.VisibleItems()) != 2 {
		t.Fatalf("reopened chooser items = %d visible %d, want complete context list", len(form.contextList.Items()), len(form.contextList.VisibleItems()))
	}
}

func TestProjectFormIdentityVerificationFailsClosedOnKubeconfigChange(t *testing.T) {
	form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formEdit, nil, store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "prod-ctx", KubeServer: "https://old.example", Namespace: "default",
	}, nil, packageDefaultKeyMaps, testStyles(true))
	_ = form.resolveIdentity()
	_, _ = form.Update(projectFormIdentityMsg{
		reqID: form.reqID, info: k8s.ContextInfo{Name: "prod-ctx", Server: "https://new.example"},
	})
	if form.state != projectFormFields || !strings.Contains(form.message, "kubeconfig changed") {
		t.Fatalf("changed identity state/message = %v/%q", form.state, form.message)
	}
}

func TestProjectFormMissingExistingContextCanKeepStoredIdentity(t *testing.T) {
	existing := store.Project{ID: 7, Name: "api", RootPath: "/repos/api", KubeContext: "gone-ctx", KubeServer: "https://gone.example", Namespace: "default"}
	form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formEdit, &existing, store.ProjectMeta{
		Name: existing.Name, RootPath: existing.RootPath, KubeContext: existing.KubeContext,
		KubeServer: existing.KubeServer, Namespace: existing.Namespace,
	}, nil, packageDefaultKeyMaps, testStyles(true))
	_ = form.resolveIdentity()
	_, _ = form.Update(projectFormIdentityMsg{reqID: form.reqID, err: fmt.Errorf("resolve: %w", k8s.ErrContextNotFound)})
	if form.state != projectFormConfirming || form.selectedKubeServer != existing.KubeServer {
		t.Fatalf("missing existing context state/server = %v/%q", form.state, form.selectedKubeServer)
	}
}

func TestProjectFormWithoutRootDisablesScanAndSelectsManual(t *testing.T) {
	for _, root := range []string{"", " \t\n "} {
		t.Run(fmt.Sprintf("root_%q", root), func(t *testing.T) {
			form := newProjectFormScreen(t.Context(), newTestStore(t), "", scanConfig{}, formCreate, nil, store.ProjectMeta{
				RootPath: root, KubeContext: "test-ctx", KubeServer: "https://test.example", Namespace: "default",
			}, nil, packageDefaultKeyMaps, testStyles(true))
			if form.scanRoot != "" || form.startChoice != 1 || !strings.Contains(ansi.Strip(form.View()), "unavailable: no detected root") {
				t.Fatalf("empty-root normalized/choice = %q/%d:\n%s", form.scanRoot, form.startChoice, ansi.Strip(form.View()))
			}
			_, _ = form.Update(key("enter"))
			if form.state != projectFormFields || form.pending {
				t.Fatalf("empty-root enter state/pending = %v/%t", form.state, form.pending)
			}
		})
	}
}

func TestSplitNamespacesDeduplicates(t *testing.T) {
	got := splitNamespaces("default, prod, prod, , default, staging")
	want := []string{"default", "prod", "staging"}
	if !slices.Equal(got, want) {
		t.Fatalf("splitNamespaces() = %v, want %v", got, want)
	}
}

func TestGolden_ProjectView(t *testing.T) {
	projectViewAppearanceHarness(t, true).golden("project_view")
}

func TestGolden_ProjectViewUnicode(t *testing.T) {
	projectViewAppearanceHarness(t, false).golden("project_view_unicode")
}

func TestProjectDirectNavigationSubjects(t *testing.T) {
	exerciseProjectDirectNavigationSubjects(t, func(*harness, string) {})
}

func TestGolden_ProjectDirectNavigationSubjects(t *testing.T) {
	exerciseProjectDirectNavigationSubjects(t, func(h *harness, name string) { h.golden(name) })
}

func exerciseProjectDirectNavigationSubjects(t *testing.T, capture func(*harness, string)) {
	t.Helper()
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "production")
	h := newHarness(t, Options{ASCII: true, ReadOnly: true, Store: st, Project: &project})
	screen := h.m.(app).stack[1].(*projectScreen)
	workloadLink := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "production", Name: "web"}
	resourceLink := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "staging", Name: "payloads", Source: store.SourceManual}
	h.send(
		projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{workloadLink}, resources: []store.ResourceLink{resourceLink}, extraNS: []string{"staging"}},
		projectContextMsg{reqID: screen.reqID, found: true},
	)
	workload := workloadInNamespace(k8s.KindDeployment, "web", "production", "1/1 ready", "workload-secret", k8s.TagEnv, k8s.KindSecret)
	linkedResource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: resourceLink.Name, Namespace: resourceLink.Namespace},
		Data: map[string][]byte{
			"config":  []byte("visible configuration"),
			"payload": {0, 1, 2, 0xff},
		},
	})
	feedProjectRefs(h, screen, map[string]refsFixture{
		"production": {
			workloads: map[string][]k8s.Workload{k8s.KindDeployment: {workload}},
			resources: []k8s.Resource{k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "workload-secret", Namespace: "production"}})},
		},
		"staging": {resources: []k8s.Resource{linkedResource}},
	})

	h.keys("enter")
	if got := firstRenderedLine(h.m.(app).stack[len(h.m.(app).stack)-1].View()); got != "Deployment production/web" {
		t.Fatalf("direct workload subject = %q", got)
	}
	capture(h, "project_direct_workload_refs")

	h.keys("esc", "down", "enter")
	h.send(resourceLoadedMsg{reqID: h.topReqID(), res: linkedResource})
	if got := firstRenderedLine(h.m.(app).stack[len(h.m.(app).stack)-1].View()); got != "[S] Secret staging/payloads  Opaque" {
		t.Fatalf("direct resource subject = %q", got)
	}
	capture(h, "project_direct_resource_keys")

	h.keys("enter")
	if got := firstRenderedLine(h.m.(app).stack[len(h.m.(app).stack)-1].View()); got != "Secret staging/payloads / config" {
		t.Fatalf("direct value subject = %q", got)
	}
	capture(h, "project_direct_value")

	h.keys("esc", "down", "enter")
	if got := firstRenderedLine(h.m.(app).stack[len(h.m.(app).stack)-1].View()); got != "Secret staging/payloads / payload (hex)" {
		t.Fatalf("direct hex subject = %q", got)
	}
	capture(h, "project_direct_hex")
}

func TestProjectDirectEditableSaveReturnsToProjectKeyScreen(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "production")
	h := newHarness(t, Options{ASCII: true, Editor: "true", Store: st, Project: &project})
	projectView := h.m.(app).stack[1].(*projectScreen)
	resourceLink := store.ResourceLink{
		Kind: k8s.KindSecret, Namespace: "staging", Name: "payloads", Source: store.SourceManual,
	}
	h.send(
		projectLinksMsg{reqID: projectView.reqID, resources: []store.ResourceLink{resourceLink}, extraNS: []string{"staging"}},
		projectContextMsg{reqID: projectView.reqID, found: true},
	)
	linkedResource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: resourceLink.Name, Namespace: resourceLink.Namespace, ResourceVersion: "10",
		},
		Data: map[string][]byte{"config": []byte("before")},
	})
	feedProjectRefs(h, projectView, map[string]refsFixture{
		"staging": {resources: []k8s.Resource{linkedResource}},
	})

	h.keys("enter")
	keyScreen := topKeyScreen(t, h)
	h.send(resourceLoadedMsg{reqID: keyScreen.reqID, res: linkedResource})
	if stack := h.m.(app).stack; len(stack) != 3 {
		t.Fatalf("linked resource stack depth = %d, want namespace/project/key", len(stack))
	}
	initialKeyReqID := keyScreen.reqID

	h.keys("enter")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "after")
	h.send(editorFinishedMsg{})
	resolveNoConsumers(h, flow)
	enterSaving(h)
	clientset := h.m.(app).client.Clientset.(*fake.Clientset)
	countResourceGets := func() int {
		count := 0
		for _, action := range clientset.Actions() {
			get, ok := action.(k8stesting.GetAction)
			if ok && action.GetResource().Resource == "secrets" && action.GetNamespace() == resourceLink.Namespace && get.GetName() == resourceLink.Name {
				count++
			}
		}
		return count
	}
	getsBeforeSaveResult := countResourceGets()

	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	resolveNoConsumers(h, flow)
	h.keys("enter")

	stack := h.m.(app).stack
	if len(stack) != 3 {
		t.Fatalf("post-save stack depth = %d, want namespace/project/key", len(stack))
	}
	if _, ok := stack[0].(*namespaceScreen); !ok {
		t.Fatalf("post-save root = %T, want *namespaceScreen", stack[0])
	}
	if got, ok := stack[1].(*projectScreen); !ok || got.project.ID != project.ID {
		t.Fatalf("post-save project screen = %#v, want project %d", stack[1], project.ID)
	}
	if stack[2] != keyScreen {
		t.Fatalf("post-save leaf = %T, want original *keyScreen", stack[2])
	}
	if keyScreen.reqID == initialKeyReqID || !keyScreen.pending {
		t.Fatalf("key reload = reqID %d -> %d pending %t, want one pending reload", initialKeyReqID, keyScreen.reqID, keyScreen.pending)
	}
	if got := countResourceGets() - getsBeforeSaveResult; got != 1 {
		t.Fatalf("key reload GET count = %d, want exactly 1", got)
	}
	wantNotice := "[success] saved Secret staging/payloads - no eligible workloads to restart"
	if view := h.view(); !strings.Contains(view, wantNotice) {
		t.Fatalf("post-save view lost resolved notice %q:\n%s", wantNotice, view)
	}

	reloaded := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: resourceLink.Name, Namespace: resourceLink.Namespace, ResourceVersion: "11",
		},
		Data: map[string][]byte{"config": []byte("after")},
	})
	h.send(resourceLoadedMsg{reqID: keyScreen.reqID, res: reloaded})
	view := h.view()
	if keyScreen.pending || !strings.Contains(view, wantNotice) {
		t.Fatalf("completed key reload = pending %t, want notice to survive:\n%s", keyScreen.pending, view)
	}
	header := strings.Split(view, "\n")[0]
	for _, want := range []string{project.Name, resourceLink.Name} {
		if !strings.Contains(header, want) {
			t.Fatalf("post-save rail lost %q: %q", want, header)
		}
	}
}

func TestProjectViewShowsConsumptionHints(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "production")
	h, screen := projectHarness(t, st, project, "")
	workloadLink := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "production", Name: "web"}
	resourceLink := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "app-creds", Source: store.SourceManual}
	h.send(
		projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{workloadLink}, resources: []store.ResourceLink{resourceLink}},
		projectContextMsg{reqID: screen.reqID, found: true},
	)
	workload := k8s.Workload{
		Kind: k8s.KindDeployment, Name: "web", Namespace: "production", Ready: "0/0 ready",
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "app",
				Env: []corev1.EnvVar{{
					Name: "DB_PASSWORD",
					ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "app-creds"},
						Key:                  "DB_PASSWORD",
					}},
				}},
				VolumeMounts: []corev1.VolumeMount{{Name: "creds", MountPath: "/creds", SubPath: "DB_PASSWORD"}},
			}},
			Volumes: []corev1.Volume{{
				Name:         "creds",
				VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "app-creds"}},
			}},
		},
	}
	feedProjectRefs(h, screen, map[string]refsFixture{
		"production": {
			workloads: map[string][]k8s.Workload{k8s.KindDeployment: {workload}},
			resources: []k8s.Resource{k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-creds", Namespace: "production"}})},
		},
	})
	view := screen.View()
	if strings.Count(view, "[subPath]") != 2 || strings.Count(view, "[rollout]") != 2 {
		t.Fatalf("consumption hints do not appear on both rows:\n%s", view)
	}
}

func TestProjectViewFilter(t *testing.T) {
	screen := newProjectScreen(t.Context(), testClient(), nil, "", store.Project{Name: "api"}, "", scanConfig{}, editEnv{}, testStyles(true))
	screen.workloads = []store.WorkloadLink{
		{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"},
		{Kind: k8s.KindStatefulSet, Namespace: "default", Name: "database"},
	}
	screen.resources = []store.ResourceLink{
		{Kind: k8s.KindSecret, Namespace: "default", Name: "credentials"},
		{Kind: k8s.KindConfigMap, Namespace: "default", Name: "settings"},
	}
	_ = screen.setItems()
	screen.SetSize(80, 20)
	update := func(msg tea.Msg) tea.Cmd {
		_, cmd := screen.Update(msg)
		return cmd
	}
	apply := func(msg tea.Msg) {
		drainScopedListFilterCmd(t, update, update(msg))
	}
	fullCount := len(screen.list.Items())
	apply(key("/"))
	if !screen.CapturesInput() {
		t.Fatal("/ did not make the project filter capture input")
	}
	for _, value := range []string{"w", "e", "b"} {
		apply(key(value))
	}
	if len(screen.list.VisibleItems()) >= fullCount {
		t.Fatalf("filtered rows = %d, full rows = %d", len(screen.list.VisibleItems()), fullCount)
	}
	if !strings.Contains(screen.View(), "Workloads") || !strings.Contains(screen.View(), "Directly linked resources") {
		t.Fatalf("filtered view lost headings:\n%s", screen.View())
	}
	if selected, ok := screen.list.SelectedItem().(projectLinkItem); !ok || selected.workload == nil || selected.workload.Name != "web" {
		t.Fatalf("filtered cursor selected %#v, want workload web", selected)
	}
	apply(key("enter"))
	if screen.CapturesInput() || !strings.Contains(screen.View(), `filter "web"`) || !strings.Contains(screen.View(), "1 of 4") {
		t.Fatalf("accepted filter capture/view = %t/%q", screen.CapturesInput(), screen.View())
	}
	apply(key("esc"))
	if screen.list.FilterState() != list.Unfiltered || len(screen.list.VisibleItems()) != fullCount {
		t.Fatalf("esc filter state/rows = %v/%d, want unfiltered/%d", screen.list.FilterState(), len(screen.list.VisibleItems()), fullCount)
	}
	apply(key("/"))
	for _, value := range []string{"s", "e", "c", "r", "e", "t"} {
		apply(key(value))
	}
	var filteredResources []string
	for _, row := range screen.list.VisibleItems() {
		if item, ok := row.(projectLinkItem); ok && item.resource != nil {
			filteredResources = append(filteredResources, item.resource.Name)
		}
	}
	if !slices.Equal(filteredResources, []string{"credentials"}) {
		t.Fatalf("resources filtered by kind = %v, want [credentials]", filteredResources)
	}
	apply(key("enter"))
	drainScopedListFilterCmd(t, update, screen.setItems())
	selected, ok := screen.list.SelectedItem().(projectLinkItem)
	if !ok || selected.resource == nil || selected.resource.Name != "credentials" {
		t.Fatalf("selection after filtered refresh = %#v, want resource credentials", selected)
	}
}

func TestProjectViewTruncationFitsBodyHeight(t *testing.T) {
	screen := newProjectScreen(t.Context(), testClient(), nil, "", store.Project{Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx", Namespace: "default"}, "", scanConfig{}, editEnv{}, testStyles(true))
	screen.workloads = make([]store.WorkloadLink, 30)
	for i := range screen.workloads {
		screen.workloads[i] = store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "default", Name: strings.Repeat("w", i+1)}
	}
	_ = screen.setItems()
	screen.list.Select(25)
	screen.SetSize(80, 6)

	view := screen.View()
	if lines := strings.Count(view, "\n") + 1; lines > screen.height {
		t.Fatalf("View() height = %d lines, want at most %d", lines, screen.height)
	}
	selected, ok := screen.list.SelectedItem().(projectLinkItem)
	if !ok {
		t.Fatalf("selected row is nil:\n%s", view)
	}
	if !strings.Contains(view, selected.workload.Name) || !strings.Contains(view, screen.styles.glyphs.cursorMarker) {
		t.Fatalf("selected row is not visible:\n%s", view)
	}
}

func TestGolden_ProjectViewContextNotFound(t *testing.T) {
	st := newTestStore(t)
	project := setProjectServer(t, st, createProject(t, st, "api", "/repos/api", "gone-ctx", "production"), "https://gone.example")
	h, screen := projectHarness(t, st, project, "")
	h.send(
		projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{{Kind: k8s.KindDeployment, Namespace: "production", Name: "web"}}, resources: []store.ResourceLink{{Kind: k8s.KindSecret, Namespace: "production", Name: "credentials", Source: store.SourceManual}}},
		projectContextMsg{reqID: screen.reqID, found: false},
	)
	h.golden("project_view_context_not_found")
}

func TestProjectViewUnreadableKubeconfig(t *testing.T) {
	project := store.Project{KubeContext: "missing-ctx", Namespace: "default"}
	client := &k8s.Client{Context: "active-ctx"}
	screen := newProjectScreen(t.Context(), client, nil, filepath.Join(t.TempDir(), "missing"), project, "", scanConfig{}, editEnv{}, testStyles(false))
	_, reqID := screen.start(t.Context())
	screen.pendingParts = 1

	msg := screen.checkContext(t.Context(), reqID)().(projectContextMsg)
	if msg.err == nil {
		t.Fatal("checkContext() error = nil for unreadable kubeconfig")
	}
	screen.Update(msg)
	if screen.ctxState != projectCtxUnreadable {
		t.Fatalf("context state = %v, want unreadable", screen.ctxState)
	}
	if view := screen.View(); !strings.Contains(view, "could not read kubeconfig: "+msg.err.Error()) {
		t.Fatalf("unreadable kubeconfig view = %q", view)
	}
}

func TestGolden_ProjectViewUnlinkConfirm(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	h, screen := projectHarness(t, st, project, "")
	h.send(projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}}}, projectContextMsg{reqID: screen.reqID, found: true})
	feedProjectRefs(h, screen, map[string]refsFixture{"default": {workloads: map[string][]k8s.Workload{k8s.KindDeployment: {workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "secret", k8s.TagEnv)}}}})
	h.keys("u")
	h.golden("project_view_unlink_confirm")
}

func TestGolden_NamespaceNoProjectHint(t *testing.T) {
	st := newTestStore(t)
	h := newHarness(t, Options{ASCII: true, Store: st, ProjectRoot: "/repos/unmatched"})
	h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{"default"}}})
	h.golden("namespace_no_project_hint")
}

func TestCtrlPOverlayLifecycle(t *testing.T) {
	st := newTestStore(t)
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("ctrl+p")
	if _, ok := h.m.(app).overlay.(*projectOverlay); !ok {
		t.Fatalf("ctrl+p opened %T", h.m.(app).overlay)
	}
	h.keys("esc")
	if h.m.(app).overlay != nil {
		t.Fatal("esc did not close project overlay")
	}
	h.keys("/")
	if !h.m.(app).stack[0].(*namespaceScreen).CapturesInput() {
		t.Fatal("namespace screen did not enter filtering")
	}
	h.keys("ctrl+p")
	if h.m.(app).overlay != nil {
		t.Fatal("ctrl+p opened while screen captured filter input")
	}
}

func TestProjectOpenSameContext(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
	}{
		{name: "unchanged namespace", namespace: "default"},
		{name: "project namespace", namespace: "production"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project := createProject(t, st, "api", "/repos/api", "test-ctx", test.namespace)
			h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
			activeClient := h.m.(app).client
			h.m.(app).editEnv.ring.Push(undo.Entry{Context: activeClient.Context})

			h.keys("ctrl+p")
			overlay := h.m.(app).overlay.(*projectOverlay)
			h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
			h.keys("enter")

			model := h.m.(app)
			if len(model.stack) != 2 {
				t.Fatalf("stack depth = %d, want 2", len(model.stack))
			}
			if screen, ok := model.stack[1].(*projectScreen); !ok || screen.project.ID != project.ID {
				t.Fatalf("top screen = %#v", model.stack[1])
			}
			if model.client == activeClient {
				t.Fatal("same-context open reused the active client pointer")
			}
			if model.client.Namespace != test.namespace {
				t.Fatalf("active namespace = %q, want %q", model.client.Namespace, test.namespace)
			}
			if activeClient.Namespace != "default" {
				t.Fatalf("pre-existing client namespace = %q, want default", activeClient.Namespace)
			}
			if model.client.Clientset != activeClient.Clientset || model.client.Context != activeClient.Context || model.client.Server != activeClient.Server {
				t.Fatalf("same-context open replaced cluster identity: before %#v after %#v", activeClient, model.client)
			}
			if got := model.editEnv.ring.Len(); got != 1 {
				t.Fatalf("undo ring length = %d, want 1", got)
			}
			if name, ok, err := st.LastProject(t.Context()); err != nil || !ok || name != project.Name {
				t.Fatalf("LastProject() = %q, %v, %v", name, ok, err)
			}
		})
	}
}

func TestProjectOpenSameContextAcceptsEquivalentServers(t *testing.T) {
	tests := []struct {
		name, saved string
	}{
		{name: "scheme and host case", saved: "HTTPS://TEST.EXAMPLE"},
		{name: "explicit default port", saved: "https://test.example:443"},
		{name: "trailing slash", saved: "https://test.example/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project := setProjectServer(t, st, createProject(t, st, "api", "/repos/api", "test-ctx", "default"), test.saved)
			h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
			h.keys("ctrl+p")
			overlay := h.m.(app).overlay.(*projectOverlay)
			h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
			h.keys("enter")

			model := h.m.(app)
			if len(model.stack) != 2 {
				t.Fatalf("stack depth = %d, want project view without confirmation", len(model.stack))
			}
			screen, ok := model.stack[1].(*projectScreen)
			if !ok {
				t.Fatalf("top screen = %T, want *projectScreen", model.stack[1])
			}
			if screen.project.KubeServer != test.saved {
				t.Fatalf("project server = %q, want exact %q", screen.project.KubeServer, test.saved)
			}
			stored, err := st.ProjectByName(t.Context(), project.Name)
			if err != nil {
				t.Fatalf("ProjectByName() error = %v", err)
			}
			if stored.KubeServer != test.saved {
				t.Fatalf("stored server = %q, want exact %q", stored.KubeServer, test.saved)
			}
		})
	}
}

func TestStartupProjectContextConfirmation(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "project-ctx", "production")
	h := newHarness(t, Options{
		ASCII: true, Store: st, Project: &project, ConfirmProjectContext: true,
		ProjectContextTarget: k8s.ContextInfo{Name: project.KubeContext, Server: "https://project.example"},
	})
	model := h.m.(app)
	if len(model.stack) != 3 {
		t.Fatalf("stack depth = %d, want 3", len(model.stack))
	}
	confirm, ok := model.stack[2].(*projectContextConfirm)
	if !ok {
		t.Fatalf("first visible screen = %T, want *projectContextConfirm", model.stack[2])
	}
	if view := h.view(); !strings.Contains(view, "Switch project context?") ||
		!strings.Contains(view, "only this sk64 window") ||
		!strings.Contains(view, "kubectl are unchanged") {
		t.Fatalf("confirmation view missing scope explanation:\n%s", view)
	}

	h.keys("y")
	if !confirm.nudge || h.m.(app).client.Context != "test-ctx" {
		t.Fatalf("lowercase y nudge/client = %t/%q", confirm.nudge, h.m.(app).client.Context)
	}
	h.keys("esc")
	if len(h.m.(app).stack) != 2 {
		t.Fatalf("esc stack depth = %d, want project view", len(h.m.(app).stack))
	}
}

func TestStartupProjectContextAlwaysSwitchPersistsPreference(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "project-ctx", "production")
	h := newHarness(t, Options{
		ASCII: true, Store: st, Project: &project, ConfirmProjectContext: true,
		ProjectContextTarget: k8s.ContextInfo{Name: project.KubeContext, Server: "https://project.example"},
	})
	confirm := h.m.(app).stack[2].(*projectContextConfirm)
	confirm.always = true
	_, reqID := confirm.start(t.Context())
	switchedClient := &k8s.Client{
		Clientset: fake.NewClientset(), Context: project.KubeContext,
		Namespace: project.Namespace, Server: "https://project.example",
	}
	h.send(projectContextProbedMsg{reqID: reqID, client: switchedClient})

	model := h.m.(app)
	if model.client != switchedClient || len(model.stack) != 2 {
		t.Fatalf("switched app = client %#v stack %d", model.client, len(model.stack))
	}
	stored, err := st.ProjectByName(t.Context(), project.Name)
	if err != nil {
		t.Fatalf("ProjectByName() error = %v", err)
	}
	if !stored.SwitchPromptSuppressed {
		t.Fatal("SwitchPromptSuppressed = false, want true")
	}
}

func TestProjectOpenDifferentContextUsesConfirmation(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "prod-ctx", "production")
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
	h.keys("enter")
	overlay = h.m.(app).overlay.(*projectOverlay)
	h.send(projectIdentityResolvedMsg{
		reqID: overlay.reqID, project: project,
		target: k8s.ContextInfo{Name: project.KubeContext, Server: "https://prod.example"},
	})
	confirm := h.m.(app).stack[1].(*projectContextConfirm)
	_, reqID := confirm.start(t.Context())
	client := &k8s.Client{Clientset: fake.NewClientset(), Context: "prod-ctx", Namespace: "production", Server: "https://prod.example"}
	h.send(projectContextProbedMsg{reqID: reqID, client: client})
	model := h.m.(app)
	if model.client != client || !strings.Contains(model.stack[1].(*projectScreen).notice, "switched context") {
		t.Fatalf("switched app = client %#v notice %q", model.client, model.stack[1].(*projectScreen).notice)
	}

	h = namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("ctrl+p")
	overlay = h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
	h.keys("enter")
	overlay = h.m.(app).overlay.(*projectOverlay)
	h.send(projectIdentityResolvedMsg{
		reqID: overlay.reqID, project: project,
		target: k8s.ContextInfo{Name: project.KubeContext, Server: "https://prod.example"},
	})
	confirm = h.m.(app).stack[1].(*projectContextConfirm)
	_, reqID = confirm.start(t.Context())
	h.send(projectContextProbedMsg{reqID: reqID, err: errors.New("probe failed")})
	if confirm.state != projectContextConfirmError {
		t.Fatalf("probe failure state = %v", confirm.state)
	}
	h.keys("esc")
	if len(h.m.(app).stack) != 1 {
		t.Fatalf("esc stack depth = %d, want namespace root", len(h.m.(app).stack))
	}
}

func TestProjectContextProbeRemainsCancellable(t *testing.T) {
	project := store.Project{Name: "api", KubeContext: "prod-ctx", Namespace: "production"}
	confirm := newProjectContextConfirm(
		t.Context(), newTestStore(t), project, testClient(),
		k8s.ContextInfo{Name: project.KubeContext, Server: "https://prod.example"}, "", nil, testStyles(true),
	)

	if cmd := confirm.startSwitch(false); cmd == nil || !confirm.pending || confirm.state != projectContextConfirmProbing {
		t.Fatalf("probe start = cmd %t pending %t state %v", cmd != nil, confirm.pending, confirm.state)
	}
	if hints := plainFooter(t, confirm, 1); hints != "esc cancel" {
		t.Fatalf("probe hints = %q, want esc cancel", hints)
	}
	if cmd := confirm.updateKey(key("esc")); cmd == nil || confirm.pending {
		t.Fatalf("probe escape = cmd %t pending %t", cmd != nil, confirm.pending)
	}
}

func TestProjectBindingSaveFailureRemainsVisible(t *testing.T) {
	project := store.Project{ID: 1, Name: "api", KubeContext: "test-ctx", KubeServer: "https://old.example", Namespace: "default"}
	client := testClient()
	confirm := newProjectContextConfirm(
		t.Context(), nil, project, client,
		k8s.ContextInfo{Name: client.Context, Server: client.Server}, "", nil, testStyles(true),
	)
	saveBatch := confirm.startSwitch(false)().(tea.BatchMsg)
	result := saveBatch[0]().(projectBindingSavedMsg)
	if result.err == nil {
		t.Fatal("binding save without a store returned no error")
	}

	_, _ = confirm.Update(result)

	view := ansi.Strip(confirm.View())
	if confirm.pending || confirm.state != projectContextConfirmError || !strings.Contains(view, "switch failed: project database unavailable") {
		t.Fatalf("binding failure pending/state/view = %t/%v:\n%s", confirm.pending, confirm.state, view)
	}
}

func TestProjectContextConfirmationPromptsUseBareCommitKeys(t *testing.T) {
	project := store.Project{
		Name:        "api",
		KubeContext: "prod-ctx",
		KubeServer:  "https://prod.example",
		Namespace:   "production",
	}
	tests := []struct {
		name       string
		target     k8s.ContextInfo
		state      projectContextConfirmState
		wantPrompt string
	}{
		{
			name:       "switch once or always",
			target:     k8s.ContextInfo{Name: "prod-ctx", Server: "https://prod.example"},
			wantPrompt: "Y switch once  A always switch for this project  esc cancel",
		},
		{
			name:       "rebind renamed context",
			target:     k8s.ContextInfo{Name: "renamed-ctx", Server: "https://prod.example"},
			wantPrompt: "Y rebind  A rebind and always switch  esc cancel",
		},
		{
			name:       "rebind changed server",
			target:     k8s.ContextInfo{Name: "prod-ctx", Server: "https://new-prod.example"},
			wantPrompt: "Y rebind  A rebind and always switch  esc cancel",
		},
		{
			name:       "run auth plugin",
			target:     k8s.ContextInfo{Name: "prod-ctx", Server: "https://prod.example"},
			state:      projectContextConfirmExecOffer,
			wantPrompt: "Auth plugin needs the terminal. Y run  n back  esc cancel",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			confirm := newProjectContextConfirm(
				t.Context(), nil, project, testClient(), test.target, "", nil, testStyles(true),
			)
			confirm.state = test.state
			confirm.SetSize(100, 22)
			view := ansi.Strip(confirm.View())
			for _, bracketedKey := range []string{"[Y]", "[A]"} {
				if strings.Contains(view, bracketedKey) {
					t.Fatalf("prompt brackets commit key %q:\n%s", bracketedKey, view)
				}
			}
			if !strings.Contains(view, test.wantPrompt) {
				t.Fatalf("prompt missing %q:\n%s", test.wantPrompt, view)
			}
		})
	}
}

func TestProjectContextConfirmationCommitKeys(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		suppress bool
	}{
		{name: "switch once", key: "Y"},
		{name: "always switch", key: "A", suppress: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project := createProject(t, st, "api", "/repos/api", "prod-ctx", "production")
			target := k8s.ContextInfo{Name: project.KubeContext, Server: "https://prod.example"}
			h := newHarness(t, Options{
				ASCII: true, Store: st, Project: &project, ConfirmProjectContext: true,
				ProjectContextTarget: target,
			})
			confirm := h.m.(app).stack[2].(*projectContextConfirm)
			_ = confirm.updateKey(key(test.key))
			if !confirm.confirming || confirm.confirmAlways != test.suppress {
				t.Fatalf("%s did not arm the typed gate: confirming %t always %t", test.key, confirm.confirming, confirm.confirmAlways)
			}
			for _, char := range "YES" {
				_ = confirm.updateKey(key(string(char)))
			}
			cmd := confirm.updateKey(key("enter"))
			if cmd == nil || confirm.state != projectContextConfirmProbing || confirm.always != test.suppress {
				t.Fatalf("%s state/cmd/always = %v/%t/%t", test.key, confirm.state, cmd != nil, confirm.always)
			}
			client := &k8s.Client{
				Clientset: fake.NewClientset(), Context: project.KubeContext,
				Namespace: project.Namespace, Server: target.Server,
			}
			h.send(projectContextProbedMsg{reqID: confirm.reqID, client: client})

			stored, err := st.ProjectByName(t.Context(), project.Name)
			if err != nil {
				t.Fatalf("ProjectByName() error = %v", err)
			}
			if stored.KubeServer != target.Server || stored.SwitchPromptSuppressed != test.suppress {
				t.Fatalf("stored identity = server %q suppressed %t", stored.KubeServer, stored.SwitchPromptSuppressed)
			}
		})
	}
}

func TestProjectContextConfirmationAcceptsEquivalentServers(t *testing.T) {
	const (
		savedServer  = "HTTPS://API.EXAMPLE:443/"
		targetServer = "https://api.example:443/"
		activeServer = "https://api.example"
	)
	project := store.Project{Name: "api", KubeContext: "test-ctx", KubeServer: savedServer, Namespace: "default"}
	client := testClient()
	client.Server = activeServer
	confirm := newProjectContextConfirm(
		t.Context(), nil, project, client,
		k8s.ContextInfo{Name: project.KubeContext, Server: targetServer}, "", nil, testStyles(true),
	)
	confirm.SetSize(100, 22)
	if view := ansi.Strip(confirm.View()); strings.Contains(view, "Rebind project cluster?") {
		t.Fatalf("equivalent server prompted rebind:\n%s", view)
	}

	cmd := confirm.startSwitch(false)
	if cmd == nil {
		t.Fatal("equivalent active identity returned no open command")
	}
	msg := cmd()
	opened, ok := msg.(projectOpenedMsg)
	if !ok {
		t.Fatalf("equivalent active identity returned %T, want projectOpenedMsg", msg)
	}
	if opened.client != client || opened.project.KubeServer != savedServer {
		t.Fatalf("opened identity = client %p server %q, want client %p server %q", opened.client, opened.project.KubeServer, client, savedServer)
	}
	if confirm.target.Server != targetServer || confirm.identityChanged || confirm.bindingRequired {
		t.Fatalf("confirmation identity = target %q changed %t binding %t", confirm.target.Server, confirm.identityChanged, confirm.bindingRequired)
	}
}

func TestProjectContextConfirmationRebindsActiveContext(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	project, err := st.UpdateProjectWithNamespaces(t.Context(), project.ID, store.ProjectMeta{
		Name: project.Name, RootPath: project.RootPath, KubeContext: project.KubeContext,
		KubeServer: "https://old.example", Namespace: project.Namespace,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateProjectWithNamespaces() error = %v", err)
	}
	h := newHarness(t, Options{
		ASCII: true, Store: st, Project: &project, ConfirmProjectContext: true,
		ProjectContextTarget: k8s.ContextInfo{Name: project.KubeContext, Server: "https://test.example"},
	})
	if view := h.view(); !strings.Contains(view, "Rebind project cluster?") ||
		!strings.Contains(view, "https://old.example") || !strings.Contains(view, "https://test.example") {
		t.Fatalf("rebind warning missing identity details:\n%s", view)
	}

	h.keys("Y")
	passCommitGate(h)

	stored, err := st.ProjectByName(t.Context(), project.Name)
	if err != nil {
		t.Fatalf("ProjectByName() error = %v", err)
	}
	if stored.KubeServer != "https://test.example" || h.m.(app).stack[1].(*projectScreen).ctxState == projectCtxServerMismatch {
		t.Fatalf("rebound project = server %q state %v", stored.KubeServer, h.m.(app).stack[1].(*projectScreen).ctxState)
	}
}

func TestProjectOverlayRebindsMissingContextByServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"gitVersion":"v1.36.2"}`))
	}))
	t.Cleanup(server.Close)

	kubeconfig := writeProjectKubeconfig(t, "test-ctx", []projectKubeconfigContext{
		{name: "test-ctx", server: "https://test.example"},
		{name: "renamed-ctx", server: server.URL},
	})
	st := newTestStore(t)
	project, err := st.CreateProject(t.Context(), store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "missing-ctx",
		KubeServer: server.URL, Namespace: "production", SwitchPromptSuppressed: true,
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st, Kubeconfig: kubeconfig})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})

	resolveBatch := overlay.startResolve(project)().(tea.BatchMsg)
	resolved := resolveBatch[0]().(projectIdentityResolvedMsg)
	if resolved.err != nil || resolved.target.Name != "renamed-ctx" {
		t.Fatalf("resolved target = %+v, error %v", resolved.target, resolved.err)
	}
	h.send(resolved)

	confirm, ok := h.m.(app).stack[1].(*projectContextConfirm)
	if !ok {
		t.Fatalf("unique server match opened %T, want *projectContextConfirm", h.m.(app).stack[1])
	}
	if view := h.view(); !strings.Contains(view, "Rebind project context?") ||
		!strings.Contains(view, "Context missing-ctx no longer exists.") ||
		!strings.Contains(view, "Context renamed-ctx points at the project's saved server.") {
		t.Fatalf("rebind view missing renamed-context explanation:\n%s", view)
	}
	h.golden("project_context_rebind")

	_ = confirm.updateKey(key("Y"))
	for _, char := range "YES" {
		_ = confirm.updateKey(key(string(char)))
	}
	probeBatch := confirm.updateKey(key("enter"))().(tea.BatchMsg)
	probed := probeBatch[0]().(projectContextProbedMsg)
	if probed.err != nil {
		t.Fatalf("project context probe error = %v", probed.err)
	}
	if probed.client.Context != "renamed-ctx" {
		t.Fatalf("probed context = %q, want renamed-ctx", probed.client.Context)
	}
	_, saveCmd := confirm.Update(probed)
	saveBatch := saveCmd().(tea.BatchMsg)
	saved := saveBatch[0]().(projectBindingSavedMsg)
	if saved.err != nil {
		t.Fatalf("project binding save error = %v", saved.err)
	}

	stored, err := st.ProjectByName(t.Context(), project.Name)
	if err != nil {
		t.Fatalf("ProjectByName() error = %v", err)
	}
	if stored.KubeContext != "renamed-ctx" || stored.KubeServer != server.URL {
		t.Fatalf("stored binding = context %q server %q", stored.KubeContext, stored.KubeServer)
	}
}

func TestProjectOverlayMissingContextWithoutUniqueServerMatchFails(t *testing.T) {
	const savedServer = "https://saved.example"
	tests := []struct {
		name     string
		contexts []projectKubeconfigContext
	}{
		{
			name: "no match",
			contexts: []projectKubeconfigContext{
				{name: "test-ctx", server: "https://test.example"},
				{name: "other-ctx", server: "https://other.example"},
			},
		},
		{
			name: "ambiguous match",
			contexts: []projectKubeconfigContext{
				{name: "test-ctx", server: "https://test.example"},
				{name: "renamed-one", server: savedServer},
				{name: "renamed-two", server: savedServer},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kubeconfig := writeProjectKubeconfig(t, "test-ctx", test.contexts)
			project := store.Project{
				ID: 1, Name: "api", KubeContext: "missing-ctx",
				KubeServer: savedServer, Namespace: "production", SwitchPromptSuppressed: true,
			}
			overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), kubeconfig, "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
			resolveBatch := overlay.startResolve(project)().(tea.BatchMsg)
			resolved := resolveBatch[0]().(projectIdentityResolvedMsg)
			if !errors.Is(resolved.err, k8s.ErrContextNotFound) {
				t.Fatalf("resolve error = %v, want ErrContextNotFound", resolved.err)
			}

			overlay.Update(resolved)
			if overlay.state != projectOverlayError {
				t.Fatalf("overlay state = %v, want projectOverlayError", overlay.state)
			}
		})
	}
}

func TestProjectOverlayAcceptsEquivalentResolvedAndProbedServers(t *testing.T) {
	const (
		savedServer  = "HTTPS://API.EXAMPLE:443/"
		targetServer = "https://api.example"
		probedServer = "https://api.example/"
	)
	project := store.Project{
		ID: 1, Name: "api", KubeContext: "prod-ctx", KubeServer: savedServer,
		Namespace: "production", SwitchPromptSuppressed: true,
	}
	overlay := newProjectOverlay(t.Context(), newTestStore(t), testClient(), "", "", scanConfig{}, nil, projectModeSwitch, pendingLink{}, packageDefaultKeyMaps, testStyles(true))
	overlay.selected = project
	_, reqID := overlay.start(t.Context())
	cmd := overlay.Update(projectIdentityResolvedMsg{
		reqID: reqID, project: project,
		target: k8s.ContextInfo{Name: project.KubeContext, Server: targetServer},
	})
	if cmd == nil || overlay.state != projectOverlayProbing || overlay.closed {
		t.Fatalf("resolved equivalent identity = cmd %t state %v closed %t", cmd != nil, overlay.state, overlay.closed)
	}
	if overlay.target.Server != targetServer {
		t.Fatalf("resolved target server = %q, want exact %q", overlay.target.Server, targetServer)
	}

	client := &k8s.Client{
		Clientset: fake.NewClientset(), Context: project.KubeContext,
		Namespace: project.Namespace, Server: probedServer,
	}
	openCmd := overlay.completeProbe(project, client)
	msg := openCmd()
	opened, ok := msg.(projectOpenedMsg)
	if !ok {
		t.Fatalf("probed equivalent identity returned %T, want projectOpenedMsg", msg)
	}
	if opened.client != client || opened.project.KubeServer != savedServer || client.Server != probedServer {
		t.Fatalf("opened identity = client %p project server %q client server %q", opened.client, opened.project.KubeServer, client.Server)
	}
}

func TestProjectOverlayRejectsServerMismatch(t *testing.T) {
	tests := []struct {
		name           string
		projectContext string
		resolve        bool
	}{
		{name: "active context", projectContext: "test-ctx"},
		{name: "suppressed context switch", projectContext: "prod-ctx", resolve: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project, err := st.CreateProject(t.Context(), store.ProjectMeta{
				Name: "api", RootPath: "/repos/api", KubeContext: test.projectContext,
				KubeServer: "https://old.example", Namespace: "default", SwitchPromptSuppressed: true,
			})
			if err != nil {
				t.Fatalf("CreateProject() error = %v", err)
			}
			h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
			h.keys("ctrl+p")
			overlay := h.m.(app).overlay.(*projectOverlay)
			h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
			h.keys("enter")
			if test.resolve {
				overlay = h.m.(app).overlay.(*projectOverlay)
				h.send(projectIdentityResolvedMsg{
					reqID: overlay.reqID, project: project,
					target: k8s.ContextInfo{Name: project.KubeContext, Server: "https://new.example"},
				})
			}
			if _, ok := h.m.(app).stack[1].(*projectContextConfirm); !ok || !strings.Contains(h.view(), "Rebind project cluster?") {
				t.Fatalf("server mismatch opened %T:\n%s", h.m.(app).stack[1], h.view())
			}
		})
	}
}

func TestProjectOverlayRechecksServerAfterProbe(t *testing.T) {
	st := newTestStore(t)
	project, err := st.CreateProject(t.Context(), store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "prod-ctx",
		KubeServer: "https://expected.example", Namespace: "default", SwitchPromptSuppressed: true,
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
	h.keys("enter")
	overlay = h.m.(app).overlay.(*projectOverlay)
	h.send(projectIdentityResolvedMsg{
		reqID: overlay.reqID, project: project,
		target: k8s.ContextInfo{Name: project.KubeContext, Server: project.KubeServer},
	})
	overlay = h.m.(app).overlay.(*projectOverlay)
	h.send(projectProbedMsg{
		reqID: overlay.reqID, project: project,
		client: &k8s.Client{Context: project.KubeContext, Server: "https://changed.example"},
	})

	if _, ok := h.m.(app).stack[1].(*projectContextConfirm); !ok || !strings.Contains(h.view(), "https://changed.example") {
		t.Fatalf("post-probe mismatch opened %T:\n%s", h.m.(app).stack[1], h.view())
	}
}

func TestProjectCreateFlow(t *testing.T) {
	st := newTestStore(t)
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st, ProjectRoot: "/repos/api"})
	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID})
	h.keys("N", "down", "enter")
	form := h.m.(app).stack[1].(*projectFormScreen)
	form.nameInput.SetValue("api")
	form.namespacesInput.SetValue("production")
	h.keys("enter")
	h.send(projectFormIdentityMsg{reqID: form.reqID, info: k8s.ContextInfo{Name: "test-ctx", Server: "https://test.example"}})
	passCommitGate(h)
	project, err := st.ProjectByName(t.Context(), "api")
	if err != nil {
		t.Fatalf("created project lookup: %v", err)
	}
	if project.KubeServer != "https://test.example" {
		t.Fatalf("created project KubeServer = %q, want %q", project.KubeServer, "https://test.example")
	}
	originalClient := h.m.(app).client
	h.send(formResultMsg{reqID: form.reqID, project: project})
	if _, ok := h.m.(app).stack[1].(*projectScreen); !ok {
		t.Fatalf("create opened %T", h.m.(app).stack[1])
	}
	if model := h.m.(app); model.client == originalClient || model.client.Namespace != "production" || originalClient.Namespace != "default" {
		t.Fatalf("created project namespace adoption = client cloned %t active %q original %q", model.client != originalClient, model.client.Namespace, originalClient.Namespace)
	}
}

func TestProjectCreatedAdoptsNamespaceOnlyForActiveClusterIdentity(t *testing.T) {
	tests := []struct {
		name           string
		projectContext string
		projectServer  string
		wantAdopted    bool
	}{
		{name: "different context", projectContext: "other-ctx"},
		{name: "different server", projectContext: "test-ctx", projectServer: "https://other.example"},
		{name: "matching unverified server", projectContext: "test-ctx", wantAdopted: true},
		{name: "matching normalized server", projectContext: "test-ctx", projectServer: "HTTPS://TEST.EXAMPLE:443/", wantAdopted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			created, err := st.CreateProject(t.Context(), store.ProjectMeta{
				Name: "api", RootPath: "/repos/api", KubeContext: test.projectContext,
				KubeServer: test.projectServer, Namespace: "production",
			})
			if err != nil {
				t.Fatalf("CreateProject() error = %v", err)
			}
			h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
			originalClient := h.m.(app).client

			h.send(projectOpenedMsg{project: created, notice: "project created"})
			model := h.m.(app)
			wantNamespace := "default"
			if test.wantAdopted {
				wantNamespace = "production"
			}
			if model.client.Context != originalClient.Context || model.client.Server != originalClient.Server || model.client.Namespace != wantNamespace {
				t.Fatalf("active client = %q/%q/%q, want unchanged identity and namespace %q", model.client.Context, model.client.Server, model.client.Namespace, wantNamespace)
			}
			if (model.client != originalClient) != test.wantAdopted {
				t.Fatalf("active client cloned = %t, want %t", model.client != originalClient, test.wantAdopted)
			}
			if originalClient.Namespace != "default" {
				t.Fatalf("pre-existing client namespace = %q, want default", originalClient.Namespace)
			}
			root := model.stack[0].(*namespaceScreen)
			projectView := model.stack[1].(*projectScreen)
			if root.client != model.client || projectView.client != model.client || root.client.Namespace != wantNamespace {
				t.Fatalf("opened project clients = app %p root %p project %p namespace %q", model.client, root.client, projectView.client, root.client.Namespace)
			}
		})
	}
}

func TestProjectEditFlow(t *testing.T) {
	tests := []struct {
		name         string
		projectName  string
		namespace    string
		clientCloned bool
		rootReloaded bool
	}{
		{name: "rename", projectName: "renamed", namespace: "default"},
		{name: "default namespace", projectName: "api", namespace: "production", clientCloned: true, rootReloaded: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
			h, screen := projectHarness(t, st, project, "")
			h.send(projectLinksMsg{reqID: screen.reqID}, projectContextMsg{reqID: screen.reqID, found: true})
			originalRoot := h.m.(app).stack[0].(*namespaceScreen)
			originalRootReqID := originalRoot.reqID
			originalProjectReqID := screen.reqID
			originalClient := h.m.(app).client
			h.m.(app).editEnv.ring.Push(undo.Entry{Context: originalClient.Context})

			h.keys("e")
			form := h.m.(app).stack[2].(*projectFormScreen)
			form.nameInput.SetValue(test.projectName)
			form.namespacesInput.SetValue(test.namespace)
			h.keys("enter")
			h.send(projectFormIdentityMsg{reqID: form.reqID, info: k8s.ContextInfo{Name: "test-ctx", Server: "https://test.example"}})
			passCommitGate(h)
			updated, err := st.ProjectByName(t.Context(), test.projectName)
			if err != nil {
				t.Fatalf("updated project lookup: %v", err)
			}
			h.send(formResultMsg{reqID: form.reqID, project: updated})

			model := h.m.(app)
			if len(model.stack) != 2 || model.stack[0] != originalRoot || model.stack[1] != screen {
				t.Fatalf("post-save stack = %#v, want original namespace and project screens", model.stack)
			}
			if got := originalRoot.reqID != originalRootReqID; got != test.rootReloaded {
				t.Fatalf("root reloaded = %t, want %t (request ID %d -> %d)", got, test.rootReloaded, originalRootReqID, originalRoot.reqID)
			}
			if screen.reqID == originalProjectReqID {
				t.Fatalf("project screen request ID = %d, want reload after save", screen.reqID)
			}
			if model.projectName != test.projectName {
				t.Fatalf("app project name = %q, want %q", model.projectName, test.projectName)
			}
			if model.client.Namespace != test.namespace {
				t.Fatalf("app namespace = %q, want %q", model.client.Namespace, test.namespace)
			}
			if (model.client != originalClient) != test.clientCloned {
				t.Fatalf("client cloned = %t, want %t", model.client != originalClient, test.clientCloned)
			}
			if originalClient.Namespace != "default" {
				t.Fatalf("pre-existing client namespace = %q, want default", originalClient.Namespace)
			}
			if got := model.editEnv.ring.Len(); got != 1 {
				t.Fatalf("undo ring length = %d, want 1", got)
			}
			if top := model.stack[len(model.stack)-1].(*projectScreen); top.project.Name != test.projectName || top.project.Namespace != test.namespace {
				t.Fatalf("project screen identity = %q/%q, want %q/%q", top.project.Name, top.project.Namespace, test.projectName, test.namespace)
			}

			h.send(searchJumpMsg{namespace: test.namespace, kind: k8s.KindSecret, name: "credentials"})
			model = h.m.(app)
			root := model.stack[0].(*namespaceScreen)
			if root == originalRoot {
				t.Fatal("search jump did not rebuild the namespace root")
			}
			identity := strings.TrimRight(ansi.Strip(root.identityLine()), " ")
			wantIdentity := "ns: " + test.namespace + "  project: " + test.projectName
			if identity != wantIdentity {
				t.Fatalf("rebuilt root identity = %q, want %q", identity, wantIdentity)
			}
		})
	}
}

func TestProjectSavedAdoptsNamespaceOnlyForActiveClusterIdentity(t *testing.T) {
	tests := []struct {
		name           string
		projectContext string
		projectServer  string
		wantAdopted    bool
	}{
		{name: "different context", projectContext: "other-ctx"},
		{name: "different server", projectContext: "test-ctx", projectServer: "https://other.example"},
		{name: "matching context with unverified server", projectContext: "test-ctx", wantAdopted: true},
		{name: "matching normalized server", projectContext: "test-ctx", projectServer: "HTTPS://TEST.EXAMPLE:443/", wantAdopted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
			h, projectView := projectHarness(t, st, project, "")
			root := h.m.(app).stack[0].(*namespaceScreen)
			h.send(
				namespacesPageMsg{reqID: root.reqID, page: k8s.NamespacePage{Names: []string{"default"}}},
				projectLinksMsg{reqID: projectView.reqID},
				projectContextMsg{reqID: projectView.reqID, found: true},
			)

			originalClient := h.m.(app).client
			originalRootClient := root.client
			originalRootReqID := root.reqID
			originalProjectReqID := projectView.reqID
			originalActions := len(originalClient.Clientset.(*fake.Clientset).Actions())
			updated := project
			updated.Namespace = "production"
			updated.KubeContext = test.projectContext
			updated.KubeServer = test.projectServer

			model, cmd := h.m.Update(projectSavedMsg{project: updated})
			h.m = model
			current := model.(app)

			if current.client.Context != originalClient.Context || current.client.Server != originalClient.Server {
				t.Fatalf("active identity changed from %q/%q to %q/%q", originalClient.Context, originalClient.Server, current.client.Context, current.client.Server)
			}
			wantNamespace := "default"
			if test.wantAdopted {
				wantNamespace = "production"
			}
			if got := current.client.Namespace; got != wantNamespace {
				t.Fatalf("active namespace = %q, want %q", got, wantNamespace)
			}
			if got := root.reqID != originalRootReqID; got != test.wantAdopted {
				t.Fatalf("root reloaded = %t, want %t (request ID %d -> %d)", got, test.wantAdopted, originalRootReqID, root.reqID)
			}
			if test.wantAdopted {
				if current.client == originalClient || root.client != current.client || len(root.names) != 0 {
					t.Fatalf("adopted client/root state = cloned %t shared %t names %v", current.client != originalClient, root.client == current.client, root.names)
				}
			} else {
				if current.client != originalClient || root.client != originalRootClient || !slices.Equal(root.names, []string{"default"}) {
					t.Fatalf("guarded client/root state = app %p root %p names %v", current.client, root.client, root.names)
				}
			}
			if originalClient.Namespace != "default" {
				t.Fatalf("pre-existing client namespace = %q, want default", originalClient.Namespace)
			}
			if projectView.reqID == originalProjectReqID || projectView.project != updated {
				t.Fatalf("project row was not refreshed: request %d -> %d project %+v", originalProjectReqID, projectView.reqID, projectView.project)
			}

			h.drain(cmd)
			gotActions := len(originalClient.Clientset.(*fake.Clientset).Actions()) - originalActions
			wantActions := 0
			if test.wantAdopted {
				wantActions = 1
			}
			if gotActions != wantActions {
				t.Fatalf("namespace reload actions = %d, want %d", gotActions, wantActions)
			}
		})
	}
}

func TestProjectRenameUpdatesLastProjectAndPickerSelection(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	if err := st.SetLastProject(t.Context(), project.Name); err != nil {
		t.Fatalf("SetLastProject(%q) error = %v", project.Name, err)
	}
	h, projectView := projectHarness(t, st, project, "")
	root := h.m.(app).stack[0].(*namespaceScreen)
	h.send(
		namespacesPageMsg{reqID: root.reqID, page: k8s.NamespacePage{Names: []string{"default"}}},
		projectLinksMsg{reqID: projectView.reqID},
		projectContextMsg{reqID: projectView.reqID, found: true},
	)
	updated, err := st.UpdateProjectWithNamespaces(t.Context(), project.ID, store.ProjectMeta{
		Name: "renamed", RootPath: project.RootPath, KubeContext: "other-ctx", Namespace: "production",
	}, nil)
	if err != nil {
		t.Fatalf("UpdateProjectWithNamespaces(rename) error = %v", err)
	}
	originalClient := h.m.(app).client
	originalRootReqID := root.reqID

	model, cmd := h.m.Update(projectSavedMsg{project: updated})
	h.m = model
	current := model.(app)
	if current.projectName != updated.Name || root.projectName != updated.Name || projectView.project.Name != updated.Name {
		t.Fatalf("renamed project identities = app %q root %q row %q", current.projectName, root.projectName, projectView.project.Name)
	}
	if current.client != originalClient || root.client != originalClient || root.reqID != originalRootReqID {
		t.Fatalf("mismatched rename changed namespace client/reload = app %p root %p request %d -> %d", current.client, root.client, originalRootReqID, root.reqID)
	}
	if name, ok, err := st.LastProject(t.Context()); err != nil || !ok || name != project.Name {
		t.Fatalf("LastProject() before command = %q, %v, %v; want %q", name, ok, err, project.Name)
	}

	h.drain(cmd)
	if name, ok, err := st.LastProject(t.Context()); err != nil || !ok || name != updated.Name {
		t.Fatalf("LastProject() after command = %q, %v, %v; want %q", name, ok, err, updated.Name)
	}

	h.keys("ctrl+p")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(overlay.loadProjects()())
	selected, ok := overlay.list.SelectedItem().(projectItem)
	if !ok || selected.project.Name != updated.Name {
		t.Fatalf("picker selection = %#v, want project %q", overlay.list.SelectedItem(), updated.Name)
	}
}

func TestProjectNamespaceEditReloadsForbiddenFallback(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	h, projectView := projectHarness(t, st, project, "")
	h.send(projectLinksMsg{reqID: projectView.reqID}, projectContextMsg{reqID: projectView.reqID, found: true})

	root := h.m.(app).stack[0].(*namespaceScreen)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", errors.New("denied"))
	h.send(namespacesPageMsg{reqID: root.reqID, err: forbidden})
	if !root.forbiddenFallback || !slices.Equal(root.names, []string{"default"}) {
		t.Fatalf("initial fallback = forbidden %t names %v, want default", root.forbiddenFallback, root.names)
	}
	staleReqID := root.reqID

	h.keys("e")
	form := h.m.(app).stack[2].(*projectFormScreen)
	form.namespacesInput.SetValue("production")
	h.keys("enter")
	h.send(projectFormIdentityMsg{reqID: form.reqID, info: k8s.ContextInfo{Name: "test-ctx", Server: "https://test.example"}})
	passCommitGate(h)
	updated, err := st.ProjectByName(t.Context(), project.Name)
	if err != nil {
		t.Fatalf("updated project lookup: %v", err)
	}
	h.send(formResultMsg{reqID: form.reqID, project: updated})

	model := h.m.(app)
	if len(model.stack) != 2 || model.stack[0] != root || model.stack[1] != projectView {
		t.Fatalf("reload changed stack = %#v, want original namespace and project screens", model.stack)
	}
	if root.reqID == staleReqID || !root.pending {
		t.Fatalf("root reload request = %d -> %d pending %t, want fresh pending request", staleReqID, root.reqID, root.pending)
	}
	freshReqID := root.reqID
	h.send(namespacesPageMsg{reqID: staleReqID, page: k8s.NamespacePage{Names: []string{"default"}}})
	if root.reqID != freshReqID || !root.pending || len(root.names) != 0 {
		t.Fatalf("stale namespace result changed reload = request %d pending %t names %v", root.reqID, root.pending, root.names)
	}

	h.send(namespacesPageMsg{reqID: freshReqID, err: forbidden})
	if !root.forbiddenFallback || !slices.Equal(root.names, []string{"production"}) {
		t.Fatalf("reloaded fallback = forbidden %t names %v, want production", root.forbiddenFallback, root.names)
	}
	selected, ok := root.list.SelectedItem().(namespaceItem)
	if !ok || selected != namespaceItem("production") {
		t.Fatalf("fallback selection = %#v, want production", root.list.SelectedItem())
	}
	h.send(
		projectLinksMsg{reqID: projectView.reqID},
		projectContextMsg{reqID: projectView.reqID, found: true},
	)

	h.keys("esc")
	if len(h.m.(app).stack) != 1 || h.m.(app).stack[0] != root {
		t.Fatalf("return to root stack = %#v", h.m.(app).stack)
	}
	view := h.view()
	for _, want := range []string{"ns: production", "[incomplete] namespace list forbidden; showing kubeconfig namespace", "production"} {
		if !strings.Contains(view, want) {
			t.Fatalf("reloaded root missing %q:\n%s", want, view)
		}
	}

	h.keys("enter")
	resourceView, ok := h.m.(app).stack[1].(*resourceScreen)
	if !ok || resourceView.namespace != "production" {
		t.Fatalf("fallback selection opened %#v, want production resources", h.m.(app).stack[1])
	}
}

func TestProjectFormSubmitCapturesExistingID(t *testing.T) {
	st := newTestStore(t)
	existing := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	meta := store.ProjectMeta{
		Name: existing.Name, RootPath: existing.RootPath, KubeContext: existing.KubeContext,
		KubeServer: "https://cluster.example", Namespace: existing.Namespace, SwitchPromptSuppressed: true,
	}
	var err error
	existing, err = st.UpdateProjectWithNamespaces(t.Context(), existing.ID, meta, nil)
	if err != nil {
		t.Fatalf("UpdateProjectWithNamespaces(identity) error = %v", err)
	}
	existingID := existing.ID
	other := createProject(t, st, "other", "/repos/other", "test-ctx", "default")
	form := newProjectFormScreen(t.Context(), st, "", scanConfig{}, formEdit, &existing, meta, nil, packageDefaultKeyMaps, testStyles(false))
	form.nameInput.SetValue("renamed")
	form.pathInput.SetValue("/repos/renamed")

	cmd := form.submit()
	existing.ID = other.ID
	msg := cmd().(formResultMsg)
	if msg.err != nil {
		t.Fatalf("submit() error = %v", msg.err)
	}
	if msg.project.ID != existingID {
		t.Fatalf("updated project ID = %d, want %d", msg.project.ID, existingID)
	}
	if msg.project.KubeServer != meta.KubeServer || !msg.project.SwitchPromptSuppressed {
		t.Fatalf("updated project identity = server %q suppressed %t", msg.project.KubeServer, msg.project.SwitchPromptSuppressed)
	}
	unchanged, err := st.ProjectByName(t.Context(), other.Name)
	if err != nil || unchanged.ID != other.ID {
		t.Fatalf("other project = %+v, %v", unchanged, err)
	}
}

func TestProjectFormEditRollsBackMetadataWhenNamespaceUpdateFails(t *testing.T) {
	st, path := newTestStoreAt(t)
	originalMeta := store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx",
		KubeServer: "https://test.example", Namespace: "default",
	}
	existing, err := st.CreateProjectWithNamespaces(t.Context(), originalMeta, []string{"old-a", "old-b"})
	if err != nil {
		t.Fatalf("CreateProjectWithNamespaces() error = %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER reject_project_namespace BEFORE INSERT ON project_namespaces WHEN NEW.namespace = 'reject' BEGIN SELECT RAISE(ABORT, 'namespace rejected'); END`); err != nil {
		t.Fatalf("create rejection trigger: %v", err)
	}

	form := newProjectFormScreen(t.Context(), st, "", scanConfig{}, formEdit, &existing, originalMeta, []string{"old-a", "old-b"}, packageDefaultKeyMaps, testStyles(true))
	form.nameInput.SetValue("changed")
	form.pathInput.SetValue("/repos/changed")
	form.namespacesInput.SetValue("production, saved, reject")
	result := form.submit()().(formResultMsg)
	if result.err == nil || !strings.Contains(result.err.Error(), `add namespace "reject"`) {
		t.Fatalf("form submit error = %v, want namespace failure", result.err)
	}

	stored, err := st.ProjectByName(t.Context(), originalMeta.Name)
	if err != nil {
		t.Fatalf("ProjectByName(original) error = %v", err)
	}
	if stored.RootPath != originalMeta.RootPath || stored.Namespace != originalMeta.Namespace {
		t.Fatalf("stored project after rollback = %+v", stored)
	}
	if _, err := st.ProjectByName(t.Context(), "changed"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ProjectByName(changed) error = %v, want ErrNotFound", err)
	}
	namespaces, err := st.Namespaces(t.Context(), existing.ID)
	if err != nil {
		t.Fatalf("Namespaces() error = %v", err)
	}
	if !slices.Equal(namespaces, []string{"old-a", "old-b"}) {
		t.Fatalf("Namespaces() after rollback = %v, want [old-a old-b]", namespaces)
	}
}

func TestProjectFormContextSelectionStoresResolvedIdentity(t *testing.T) {
	st := newTestStore(t)
	meta := store.ProjectMeta{
		Name: "api", RootPath: "/repos/api", KubeContext: "old-context",
		KubeServer: "https://old.example", Namespace: "default", SwitchPromptSuppressed: true,
	}
	existing, err := st.CreateProject(t.Context(), meta)
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	form := newProjectFormScreen(t.Context(), st, "", scanConfig{}, formEdit, &existing, meta, nil, packageDefaultKeyMaps, testStyles(false))
	form.selectedKubeContext = "new-context"
	form.selectedKubeServer = "https://new.example"
	msg := form.submit()().(formResultMsg)
	if msg.err != nil {
		t.Fatalf("submit() error = %v", msg.err)
	}
	if msg.project.KubeContext != "new-context" || msg.project.KubeServer != "https://new.example" || msg.project.SwitchPromptSuppressed {
		t.Fatalf("changed context identity = context %q server %q suppressed %t", msg.project.KubeContext, msg.project.KubeServer, msg.project.SwitchPromptSuppressed)
	}
}

func TestProjectFormDuplicate(t *testing.T) {
	st := newTestStore(t)
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st, ProjectRoot: "/repos/api"})
	h.send(pushScreenMsg{s: newProjectFormScreen(t.Context(), st, "", scanConfig{}, formCreate, nil, store.ProjectMeta{Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx", Namespace: "default"}, nil, packageDefaultKeyMaps, h.m.(app).styles)})
	form := h.m.(app).stack[1].(*projectFormScreen)
	_, reqID := form.start(t.Context())
	form.state = projectFormSaving
	h.send(formResultMsg{reqID: reqID, err: store.ErrDuplicate})
	if !strings.Contains(form.View(), "name or path already used") || len(h.m.(app).stack) != 2 {
		t.Fatalf("duplicate form view = %q stack %d", form.View(), len(h.m.(app).stack))
	}
}

func TestLinkWorkloadFlow(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("w")
	feedRefs(h, h.topReqID(), refsFixture{workloads: map[string][]k8s.Workload{k8s.KindDeployment: {workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "secret", k8s.TagEnv)}}})
	h.keys("L")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
	h.keys("enter")
	passCommitGate(h)
	overlay = h.m.(app).overlay.(*projectOverlay)
	h.send(projectLinkedMsg{reqID: overlay.reqID, projectName: project.Name})
	h.keys("enter")
	links, _ := st.WorkloadLinks(t.Context(), project.ID)
	if len(links) != 1 || links[0].Name != "web" ||
		links[0].OriginContext != "test-ctx" || links[0].OriginServer != "https://test.example" ||
		h.m.(app).overlay != nil {
		t.Fatalf("workload links = %+v overlay %T", links, h.m.(app).overlay)
	}
}

func TestLinkResourceFlow(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	h := resourceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.keys("L")
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
	h.keys("enter")
	passCommitGate(h)
	overlay = h.m.(app).overlay.(*projectOverlay)
	h.send(projectLinkedMsg{reqID: overlay.reqID, projectName: project.Name})
	links, _ := st.ResourceLinks(t.Context(), project.ID)
	if len(links) != 1 || links[0].Source != store.SourceManual ||
		links[0].OriginContext != "test-ctx" || links[0].OriginServer != "https://test.example" {
		t.Fatalf("resource links = %+v", links)
	}
}

func TestProjectViewMarksOriginServerMismatch(t *testing.T) {
	project := store.Project{
		Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx",
		KubeServer: "https://project.example", Namespace: "default",
	}
	tests := []struct {
		name      string
		workloads []store.WorkloadLink
		resources []store.ResourceLink
		wantMark  bool
	}{
		{
			name: "workload mismatch",
			workloads: []store.WorkloadLink{{
				Kind: k8s.KindDeployment, Namespace: "default", Name: "web",
				OriginContext: "other-ctx", OriginServer: "https://other.example",
			}},
			wantMark: true,
		},
		{
			name: "resource mismatch",
			resources: []store.ResourceLink{{
				Kind: k8s.KindSecret, Namespace: "default", Name: "credentials", Source: store.SourceManual,
				OriginContext: "other-ctx", OriginServer: "https://other.example",
			}},
			wantMark: true,
		},
		{
			name: "legacy resource origin",
			resources: []store.ResourceLink{{
				Kind: k8s.KindSecret, Namespace: "default", Name: "credentials", Source: store.SourceManual,
			}},
		},
		{
			name: "legacy workload origin",
			workloads: []store.WorkloadLink{{
				Kind: k8s.KindDeployment, Namespace: "default", Name: "web",
			}},
		},
		{
			name: "matching server",
			workloads: []store.WorkloadLink{{
				Kind: k8s.KindDeployment, Namespace: "default", Name: "web",
				OriginContext: "renamed-ctx", OriginServer: project.KubeServer,
			}},
		},
		{
			name: "normalized matching workload server",
			workloads: []store.WorkloadLink{{
				Kind: k8s.KindDeployment, Namespace: "default", Name: "web",
				OriginContext: "renamed-ctx", OriginServer: "HTTPS://PROJECT.EXAMPLE:443/",
			}},
		},
		{
			name: "normalized matching resource server",
			resources: []store.ResourceLink{{
				Kind: k8s.KindSecret, Namespace: "default", Name: "credentials", Source: store.SourceManual,
				OriginContext: "renamed-ctx", OriginServer: "HTTPS://PROJECT.EXAMPLE:443/",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			screen := newProjectScreen(t.Context(), testClient(), nil, "", project, "", scanConfig{}, editEnv{}, testStyles(true))
			screen.ctxState = projectCtxActive
			screen.workloads = test.workloads
			screen.resources = test.resources
			_ = screen.setItems()
			screen.SetSize(80, 12)
			marked := strings.Contains(screen.View(), screen.styles.glyphs.originMismatchTag)
			if marked != test.wantMark {
				t.Fatalf("origin mismatch marked = %t, want %t:\n%s", marked, test.wantMark, screen.View())
			}
		})
	}
}

func TestProjectViewBackfillsLegacyKubeServer(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	client := testClient()
	screen := newProjectScreen(t.Context(), client, st, "", project, "", scanConfig{}, editEnv{}, testStyles(true))
	ctx, reqID := screen.start(t.Context())
	msg := screen.checkContext(ctx, reqID)().(projectContextMsg)
	if msg.kubeServer != client.Server {
		t.Fatalf("backfilled message server = %q, want %q", msg.kubeServer, client.Server)
	}
	stored, err := st.ProjectByName(t.Context(), project.Name)
	if err != nil {
		t.Fatalf("ProjectByName() error = %v", err)
	}
	if stored.KubeServer != client.Server {
		t.Fatalf("stored KubeServer = %q, want %q", stored.KubeServer, client.Server)
	}
}

func TestProjectViewActivatesEquivalentServerSpellings(t *testing.T) {
	tests := []struct {
		name, saved string
	}{
		{name: "scheme and host case", saved: "HTTPS://TEST.EXAMPLE"},
		{name: "explicit default port", saved: "https://test.example:443"},
		{name: "trailing slash", saved: "https://test.example/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := store.Project{
				ID: 1, Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx",
				KubeServer: test.saved, Namespace: "default",
			}
			client := testClient()
			screen := newProjectScreen(t.Context(), client, nil, "", project, "", scanConfig{}, editEnv{}, testStyles(true))
			ctx, reqID := screen.start(t.Context())
			msg := screen.checkContext(ctx, reqID)().(projectContextMsg)
			if msg.serverMismatch {
				t.Fatalf("equivalent servers marked mismatched: saved %q active %q", test.saved, client.Server)
			}
			screen.pendingParts = 1
			screen.Update(msg)
			if screen.ctxState != projectCtxActive {
				t.Fatalf("context state = %v, want active", screen.ctxState)
			}
			if screen.project.KubeServer != test.saved || !strings.Contains(ansi.Strip(screen.View()), test.saved) {
				t.Fatalf("project server changed or was not displayed exactly: %q", screen.project.KubeServer)
			}
		})
	}
}

func TestProjectViewDoesNotActivateServerMismatch(t *testing.T) {
	project := store.Project{
		ID: 1, Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx",
		KubeServer: "https://stored.example", Namespace: "default",
	}
	client := testClient()
	screen := newProjectScreen(t.Context(), client, nil, "", project, "", scanConfig{}, editEnv{}, testStyles(true))
	ctx, reqID := screen.start(t.Context())
	msg := screen.checkContext(ctx, reqID)().(projectContextMsg)
	if !msg.serverMismatch {
		t.Fatalf("server mismatch = false for stored %q active %q", project.KubeServer, client.Server)
	}
	screen.pendingParts = 1
	screen.Update(msg)
	view := ansi.Strip(screen.View())
	for _, want := range []string{
		"server: https://stored.example",
		screen.styles.glyphs.serverMismatchTag,
		"saved https://stored.example",
		"active https://test.example",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("context state/view = %v, missing %q:\n%s", screen.ctxState, want, view)
		}
	}
	if screen.ctxState != projectCtxServerMismatch || strings.Contains(view, "project server differs from the active context") {
		t.Fatalf("context state/view kept bare mismatch sentence = %v:\n%s", screen.ctxState, view)
	}
}

func TestGolden_ProjectViewServerMismatch(t *testing.T) {
	st := newTestStore(t)
	project := setProjectServer(t, st, createProject(t, st, "api", "/repos/api", "test-ctx", "default"), "https://stored.example")
	h, screen := projectHarness(t, st, project, "")
	h.send(
		projectLinksMsg{reqID: screen.reqID, resources: []store.ResourceLink{{Kind: k8s.KindSecret, Namespace: "default", Name: "credentials", Source: store.SourceManual}}},
		projectContextMsg{reqID: screen.reqID, found: true, serverMismatch: true},
	)
	h.golden("project_view_server_mismatch")
}

func TestUnlinkRequiresUppercase(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "gone-ctx", "default")
	link := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}
	if err := st.LinkWorkload(t.Context(), project.ID, link); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	h, screen := projectHarness(t, st, project, "")
	h.send(projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{link}}, projectContextMsg{reqID: screen.reqID, found: false})
	h.keys("u", "esc")
	links, _ := st.WorkloadLinks(t.Context(), project.ID)
	if len(links) != 1 {
		t.Fatal("cancel removed workload link")
	}
	h.keys("u")
	typeText(h, "yes")
	h.keys("enter")
	links, _ = st.WorkloadLinks(t.Context(), project.ID)
	if len(links) != 1 || !screen.confirmUnlink {
		t.Fatalf("lowercase yes links = %+v confirm %t", links, screen.confirmUnlink)
	}
	if view := h.view(); !strings.Contains(view, "type YES in capitals to confirm") {
		t.Fatalf("lowercase yes view has no corrective message:\n%s", view)
	}
	h.keys("ctrl+u")
	passCommitGate(h)
	links, _ = st.WorkloadLinks(t.Context(), project.ID)
	if len(links) != 0 {
		t.Fatalf("typed YES did not unlink: %+v", links)
	}
	h.send(projectUnlinkedMsg{reqID: screen.reqID})
	h.send(projectLinksMsg{reqID: screen.reqID})
	links, _ = st.WorkloadLinks(t.Context(), project.ID)
	if len(links) != 0 || strings.Contains(screen.View(), "Deployment/web") {
		t.Fatalf("links after unlink = %+v view %q", links, screen.View())
	}
}

func TestProjectViewNavigation(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	h, screen := projectHarness(t, st, project, "")
	h.send(projectLinksMsg{reqID: screen.reqID,
		workloads: []store.WorkloadLink{{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}, {Kind: k8s.KindStatefulSet, Namespace: "default", Name: "missing"}},
		resources: []store.ResourceLink{{Kind: k8s.KindSecret, Namespace: "default", Name: "exists", Source: store.SourceManual}, {Kind: k8s.KindSecret, Namespace: "default", Name: "missing", Source: store.SourceManual}}},
		projectContextMsg{reqID: screen.reqID, found: true})
	feedProjectRefs(h, screen, map[string]refsFixture{"default": {workloads: map[string][]k8s.Workload{k8s.KindDeployment: {workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "exists", k8s.TagEnv)}}, resources: []k8s.Resource{secretResource("exists")}}})
	h.keys("enter")
	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top.Title() != "Deployment/web" {
		t.Fatalf("workload navigation title = %q", top.Title())
	}
	h.keys("esc", "down")
	depth := len(h.m.(app).stack)
	h.keys("enter")
	if len(h.m.(app).stack) != depth {
		t.Fatal("missing workload was navigable")
	}
	h.keys("down", "enter")
	if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen); !ok {
		t.Fatalf("resource navigation opened %T", h.m.(app).stack[len(h.m.(app).stack)-1])
	}
	h.keys("esc", "down")
	depth = len(h.m.(app).stack)
	h.keys("enter")
	if len(h.m.(app).stack) != depth {
		t.Fatal("missing resource was navigable")
	}
	if _, ok := screen.list.SelectedItem().(projectLinkItem); !ok {
		t.Fatalf("cursor landed on heading at %d", screen.list.Index())
	}
	before := screen.list.Index()
	h.send(refsPageMsg{reqID: screen.collectors["default"].reqID - 1, source: "pods"})
	if screen.list.Index() != before {
		t.Fatalf("stale refs message moved cursor from %d to %d", before, screen.list.Index())
	}
}

func TestProjectViewInactiveLinksAreNotNavigable(t *testing.T) {
	tests := []struct {
		name      string
		workloads []store.WorkloadLink
		resources []store.ResourceLink
	}{
		{
			name:      "workload",
			workloads: []store.WorkloadLink{{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}},
		},
		{
			name:      "resource",
			resources: []store.ResourceLink{{Kind: k8s.KindSecret, Namespace: "default", Name: "credentials", Source: store.SourceManual}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := newTestStore(t)
			project := createProject(t, st, "api", "/repos/api", "prod-ctx", "default")
			h, screen := projectHarness(t, st, project, "")
			h.send(
				projectLinksMsg{reqID: screen.reqID, workloads: test.workloads, resources: test.resources},
				projectContextMsg{reqID: screen.reqID, found: true},
			)
			depth := len(h.m.(app).stack)

			h.keys("enter")

			if len(h.m.(app).stack) != depth {
				t.Fatalf("inactive %s link changed stack depth from %d to %d", test.name, depth, len(h.m.(app).stack))
			}
			if view := screen.View(); !strings.Contains(view, "context prod-ctx is not active") {
				t.Fatalf("inactive %s link view has no inactive-context feedback:\n%s", test.name, view)
			}
		})
	}
}

func TestProjectViewCollectorLifecycle(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "one")
	workloadLink := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: "one", Name: "web"}
	resourceLink := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "two", Name: "secret", Source: store.SourceManual}
	h, screen := projectHarness(t, st, project, "")
	h.send(
		projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{workloadLink}, resources: []store.ResourceLink{resourceLink}},
		projectContextMsg{reqID: screen.reqID, found: true},
	)
	if len(screen.collectors) != 2 || screen.collectorsPending != 2 {
		t.Fatalf("collectors = %d pending %d", len(screen.collectors), screen.collectorsPending)
	}
	if hints := plainFooter(t, screen, 1); hints != "esc cancel" {
		t.Fatalf("collector-pending hints = %q, want esc cancel", hints)
	}

	before := screen.collectorsPending
	onePending := screen.collectors["one"].pendingSrc
	twoPending := screen.collectors["two"].pendingSrc
	h.send(refsPageMsg{reqID: 0, source: "pods"})
	if screen.collectorsPending != before || screen.collectors["one"].pendingSrc != onePending || screen.collectors["two"].pendingSrc != twoPending {
		t.Fatalf(
			"stale refs message changed collector state: collectors %d/%d pending sources %d/%d, want %d/%d",
			screen.collectorsPending,
			before,
			screen.collectors["one"].pendingSrc,
			screen.collectors["two"].pendingSrc,
			onePending,
			twoPending,
		)
	}
	h.send(refsPageMsg{
		reqID:  screen.collectors["one"].reqID,
		source: k8s.SourceName(k8s.KindDeployment),
		workloads: k8s.WorkloadPage{Items: []k8s.Workload{{
			Kind: k8s.KindDeployment, Namespace: "one", Name: "web", Ready: "1/1 ready",
		}}},
	})

	h.keys("esc")
	if screen.anyPending() || len(h.m.(app).stack) != 2 {
		t.Fatalf("esc pending = %v stack %d", screen.anyPending(), len(h.m.(app).stack))
	}
	view := ansi.Strip(screen.View())
	for _, want := range []string{"Deployment/web", "secret  two", "[incomplete]", "retained rows incomplete", "ctrl+r to retry"} {
		if !strings.Contains(view, want) {
			t.Fatalf("cancelled collector view missing %q:\n%s", want, view)
		}
	}
	if claims := renderedStateClaims(view); len(claims) != 1 || claims[0] != "[incomplete]" {
		t.Fatalf("cancelled collector state claims = %v:\n%s", claims, view)
	}
	if hints := plainFooter(t, screen, 1); hints != "ctrl+r retry  ? help" {
		t.Fatalf("cancelled collector hints = %q", hints)
	}
	depth := len(h.m.(app).stack)
	h.keys("enter")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("cancelled collector enter changed stack depth from %d to %d", depth, len(h.m.(app).stack))
	}

	h.keys("ctrl+r")
	if hints := plainFooter(t, screen, 1); !screen.pending || hints != "esc cancel" {
		t.Fatalf("restarted project load = pending %t hints %q", screen.pending, hints)
	}
	h.send(
		projectLinksMsg{reqID: screen.reqID, workloads: []store.WorkloadLink{workloadLink}, resources: []store.ResourceLink{resourceLink}},
		projectContextMsg{reqID: screen.reqID, found: true},
	)
	feedProjectRefs(h, screen, map[string]refsFixture{
		"one": {workloads: map[string][]k8s.Workload{k8s.KindDeployment: {{Kind: k8s.KindDeployment, Namespace: "one", Name: "web", Ready: "1/1 ready"}}}},
		"two": {resources: []k8s.Resource{k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: "two"}})}},
	})
	view = ansi.Strip(screen.View())
	if screen.loadCancelled || strings.Contains(view, "[unknown]") || strings.Contains(view, "[incomplete]") {
		t.Fatalf("successful refresh retained degraded state:\n%s", view)
	}
}

func TestProjectViewPendingLoadCancellationIsUnknownUntilRefresh(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	h, screen := projectHarness(t, st, project, "")
	if hints := plainFooter(t, screen, 1); hints != "esc cancel" {
		t.Fatalf("initial pending hints = %q, want esc cancel", hints)
	}

	h.keys("esc")
	view := ansi.Strip(screen.View())
	hints := plainFooter(t, screen, 1)
	if !strings.Contains(view, "[unknown] project load cancelled; results unknown") || hints != "ctrl+r retry  ? help" {
		t.Fatalf("cancelled initial load state/hints = %q:\n%s", hints, view)
	}

	h.keys("ctrl+r")
	h.send(projectLinksMsg{reqID: screen.reqID}, projectContextMsg{reqID: screen.reqID, found: true})
	view = ansi.Strip(screen.View())
	if screen.loadCancelled || strings.Contains(view, "[unknown]") || !strings.Contains(view, "[empty]") {
		t.Fatalf("successful empty refresh state:\n%s", view)
	}
}

func TestProjectViewStateLinePrecedence(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*projectScreen)
		want      string
	}{
		{
			name: "loading hides errors and degraded states",
			configure: func(screen *projectScreen) {
				screen.pending = true
				screen.readErr = errors.New("disk failed")
				screen.loadCancelled = true
				screen.loaded = true
			},
			want: "[loading]",
		},
		{
			name: "error hides cancelled state",
			configure: func(screen *projectScreen) {
				screen.readErr = errors.New("disk failed")
				screen.loadCancelled = true
			},
			want: "[error]",
		},
		{
			name: "cancelled without rows is unknown",
			configure: func(screen *projectScreen) {
				screen.loadCancelled = true
			},
			want: "[unknown]",
		},
		{
			name: "cancelled with rows is incomplete",
			configure: func(screen *projectScreen) {
				screen.loadCancelled = true
				screen.cancelledPartial = true
				screen.loaded = true
			},
			want: "[incomplete]",
		},
		{
			name: "collector failures hide empty state",
			configure: func(screen *projectScreen) {
				index := k8s.NewRefIndex()
				index.AddSourceError("pods")
				screen.collectors = map[string]*refsCollector{"default": {index: index}}
				screen.loaded = true
			},
			want: "[incomplete]",
		},
		{
			name: "empty completed project",
			configure: func(screen *projectScreen) {
				screen.loaded = true
			},
			want: "[empty]",
		},
		{
			name: "populated project has no state claim",
			configure: func(screen *projectScreen) {
				screen.loaded = true
				screen.workloads = []store.WorkloadLink{{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}}
				_ = screen.setItems()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			screen := newProjectScreen(
				t.Context(), testClient(), nil, "",
				store.Project{Name: "api", RootPath: "/repos/api", KubeContext: "test-ctx", Namespace: "default"},
				"", scanConfig{}, editEnv{}, testStyles(true),
			)
			screen.ctxState = projectCtxActive
			screen.SetSize(80, 12)
			test.configure(screen)

			claims := renderedStateClaims(screen.View())
			if test.want == "" {
				if len(claims) != 0 {
					t.Fatalf("state claims = %v, want none; view = %q", claims, ansi.Strip(screen.View()))
				}
				return
			}
			if len(claims) != 1 || claims[0] != test.want {
				t.Fatalf("state claims = %v, want [%s]; view = %q", claims, test.want, ansi.Strip(screen.View()))
			}
		})
	}
}

func TestProjectViewStoreReadError(t *testing.T) {
	st := newTestStore(t)
	project := createProject(t, st, "api", "/repos/api", "test-ctx", "default")
	h, screen := projectHarness(t, st, project, "")
	h.send(projectLinksMsg{reqID: screen.reqID, err: errors.New("disk failed")}, projectContextMsg{reqID: screen.reqID, found: true})
	if !strings.Contains(screen.View(), "project data unavailable: disk failed") {
		t.Fatalf("project error view = %q", screen.View())
	}
	h.keys("esc")
	if len(h.m.(app).stack) != 1 {
		t.Fatalf("esc stack depth = %d", len(h.m.(app).stack))
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, _ := newTestStoreAt(t)
	return st
}

func newTestStoreAt(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sk64.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

func createProject(t *testing.T, st *store.Store, name, path, kubeContext, namespace string) store.Project {
	t.Helper()
	project, err := st.CreateProject(t.Context(), store.ProjectMeta{Name: name, RootPath: path, KubeContext: kubeContext, Namespace: namespace})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	return project
}

func setProjectServer(t *testing.T, st *store.Store, project store.Project, server string) store.Project {
	t.Helper()
	namespaces, err := st.Namespaces(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("Namespaces() error = %v", err)
	}
	updated, err := st.UpdateProjectWithNamespaces(t.Context(), project.ID, store.ProjectMeta{
		Name: project.Name, RootPath: project.RootPath, KubeContext: project.KubeContext,
		KubeServer: server, Namespace: project.Namespace, SwitchPromptSuppressed: project.SwitchPromptSuppressed,
	}, namespaces)
	if err != nil {
		t.Fatalf("UpdateProjectWithNamespaces(server) error = %v", err)
	}
	return updated
}

func linkPickerHarness(t *testing.T, st *store.Store, project store.Project, link pendingLink) *harness {
	t.Helper()
	h := namespaceHarnessOptions(t, Options{ASCII: true, Store: st})
	h.send(openProjectPickerMsg{link: link})
	overlay := h.m.(app).overlay.(*projectOverlay)
	h.send(projectsLoadedMsg{reqID: overlay.reqID, projects: []store.Project{project}})
	return h
}

type projectKubeconfigContext struct {
	name   string
	server string
}

func writeProjectKubeconfig(t *testing.T, currentContext string, contexts []projectKubeconfigContext) string {
	t.Helper()
	var contents strings.Builder
	contents.WriteString("apiVersion: v1\nkind: Config\nclusters:\n")
	for index, kubeContext := range contexts {
		_, _ = fmt.Fprintf(&contents, "- name: cluster-%d\n  cluster:\n    server: %q\n", index, kubeContext.server)
	}
	contents.WriteString("contexts:\n")
	for index, kubeContext := range contexts {
		_, _ = fmt.Fprintf(&contents, "- name: %q\n  context:\n    cluster: cluster-%d\n", kubeContext.name, index)
	}
	_, _ = fmt.Fprintf(&contents, "current-context: %q\n", currentContext)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func namespaceHarnessOptions(t *testing.T, opts Options) *harness {
	t.Helper()
	h := newHarness(t, opts)
	h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{"default"}}})
	return h
}

func resourceHarnessOptions(t *testing.T, opts Options) *harness {
	t.Helper()
	h := namespaceHarnessOptions(t, opts)
	h.keys("enter")
	h.send(resourceMessages(h.topReqID())...)
	return h
}

func projectViewAppearanceHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	st := newTestStore(t)
	project := setProjectServer(t, st, createProject(t, st, "api", "/repos/api", "test-ctx", "production"), "https://test.example")
	h, screen := projectHarnessOptions(t, st, project, "opened from this repository", ascii)
	h.send(projectLinksMsg{
		reqID: screen.reqID,
		workloads: []store.WorkloadLink{
			{Kind: k8s.KindDeployment, Namespace: "production", Name: "web"},
			{Kind: k8s.KindStatefulSet, Namespace: "production", Name: "database"},
		},
		resources: []store.ResourceLink{
			{Kind: k8s.KindConfigMap, Namespace: "production", Name: "settings", Source: store.SourceManual},
			{Kind: k8s.KindSecret, Namespace: "production", Name: "gone", Source: store.SourceManual},
		},
		extraNS: []string{"staging"},
	}, projectContextMsg{reqID: screen.reqID, found: true})
	web := workloadInNamespace(k8s.KindDeployment, "web", "production", "2/3 ready", "settings", k8s.TagEnvFrom, k8s.KindConfigMap)
	web.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "settings", MountPath: "/settings", SubPath: "LOG_LEVEL"}}
	web.Spec.Volumes = []corev1.Volume{{
		Name:         "settings",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}},
	}}
	feedProjectRefs(h, screen, map[string]refsFixture{
		"production": {
			workloads: map[string][]k8s.Workload{k8s.KindDeployment: {web}},
			resources: []k8s.Resource{configMapResourceIn("settings", "production")},
			errors:    map[string]error{k8s.SourceName(k8s.KindStatefulSet): errors.New("forbidden")},
		},
	})
	return h
}

func projectHarness(t *testing.T, st *store.Store, project store.Project, notice string) (*harness, *projectScreen) {
	t.Helper()
	return projectHarnessOptions(t, st, project, notice, true)
}

func projectHarnessOptions(t *testing.T, st *store.Store, project store.Project, notice string, ascii bool) (*harness, *projectScreen) {
	t.Helper()
	h := newHarness(t, Options{ASCII: ascii, Store: st, Project: &project, StartupNotice: notice})
	return h, h.m.(app).stack[1].(*projectScreen)
}

func feedProjectRefs(h *harness, screen *projectScreen, fixtures map[string]refsFixture) {
	h.t.Helper()
	for namespace, collector := range screen.collectors {
		fixture := fixtures[namespace]
		for _, kind := range k8s.WorkloadKinds {
			source := k8s.SourceName(kind)
			h.send(refsPageMsg{reqID: collector.reqID, source: source, workloads: k8s.WorkloadPage{Items: fixture.workloads[kind]}, err: fixture.errors[source]})
		}
		h.send(
			refsPageMsg{reqID: collector.reqID, source: "pods", pods: k8s.PodPage{Items: fixture.pods}, err: fixture.errors["pods"]},
			refsPageMsg{reqID: collector.reqID, source: "serviceaccounts", sas: k8s.ServiceAccountPage{Items: fixture.sas}, err: fixture.errors["serviceaccounts"]},
		)
		for _, kind := range []string{k8s.KindSecret, k8s.KindConfigMap} {
			source := resourceSource(kind)
			items := make([]k8s.Resource, 0)
			for _, resource := range fixture.resources {
				if resource.Kind() == kind {
					items = append(items, resource)
				}
			}
			h.send(refsPageMsg{reqID: collector.reqID, source: source, resources: k8s.ResourcePage{Items: items}, err: fixture.errors[source]})
		}
	}
}

func workloadInNamespace(kind, name, namespace, ready, resourceName string, tag k8s.RefTag, resourceKind string) k8s.Workload {
	workload := workloadWithRef(kind, name, ready, resourceName, tag)
	workload.Namespace = namespace
	if resourceKind == k8s.KindConfigMap {
		workload.Spec.Containers[0].EnvFrom[0] = corev1.EnvFromSource{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: resourceName}}}
	}
	return workload
}

func configMapResourceIn(name, namespace string) k8s.Resource {
	return k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
}
