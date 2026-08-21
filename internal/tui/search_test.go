package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGolden_SearchPending(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("ctrl+f")
	h.golden("search_pending")
}

func TestGolden_SearchPendingQuery(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("ctrl+f")
	h.send(tea.PasteMsg{Content: "missing"})

	view := h.view()
	if !strings.Contains(view, "searching namespaces...") {
		t.Fatalf("pending search view missing scanning line: %q", view)
	}
	if strings.Contains(view, "no matches") {
		t.Fatalf("pending search view reported no matches: %q", view)
	}
	h.golden("search_pending_query")
}

func TestGolden_SearchResults(t *testing.T) {
	h := searchHarness(t, Options{})
	reqID := h.topReqID()
	h.send(
		searchNamespacesMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"default"}, Continue: "next"}},
		searchNamespacesMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"production"}}},
		searchResourcesMsg{reqID: reqID, namespace: "default", kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{searchSecret("default", "database", "token")}, Continue: "more"}},
		searchResourcesMsg{reqID: reqID, namespace: "default", kind: k8s.KindSecret, page: k8s.ResourcePage{}},
		searchResourcesMsg{reqID: reqID, namespace: "default", kind: k8s.KindConfigMap, page: k8s.ResourcePage{Items: []k8s.Resource{searchConfigMap("default", "settings", "database-url")}}},
		searchResourcesMsg{reqID: reqID, namespace: "production", kind: k8s.KindSecret, page: k8s.ResourcePage{}},
		searchResourcesMsg{reqID: reqID, namespace: "production", kind: k8s.KindConfigMap, page: k8s.ResourcePage{}},
	)
	h.send(tea.PasteMsg{Content: "data"})
	if got := h.m.(app).stack[len(h.m.(app).stack)-1].(*searchScreen).input.Value(); got != "data" {
		t.Fatalf("pasted query = %q, want data", got)
	}
	h.golden("search_results")
}

func TestGolden_SearchNoResults(t *testing.T) {
	h := searchHarness(t, Options{})
	reqID := h.topReqID()
	h.send(
		searchNamespacesMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"default"}}},
		searchResourcesMsg{reqID: reqID, namespace: "default", kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{searchSecret("default", "database", "token")}}},
		searchResourcesMsg{reqID: reqID, namespace: "default", kind: k8s.KindConfigMap, page: k8s.ResourcePage{}},
	)
	typeText(h, "missing")
	h.golden("search_no_results")
}

func TestGolden_KeyValueToggle(t *testing.T) {
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "searchable", Namespace: "default"},
		Data: map[string][]byte{
			"password": []byte("hunter2"),
			"payload":  {0, 1, 2},
		},
	})
	h := keyHarness(t, resource)
	h.keys("v")
	h.golden("key_value_toggle")
}

func TestSearchViewStateOrdering(t *testing.T) {
	tests := []struct {
		name          string
		pending       bool
		query         string
		hasHit        bool
		wantNoMatches bool
		wantTypeHint  bool
	}{
		{name: "pending empty query without hits", pending: true},
		{name: "pending empty query with hits", pending: true, hasHit: true},
		{name: "pending query without hits", pending: true, query: "missing"},
		{name: "pending query with hits", pending: true, query: "data", hasHit: true},
		{name: "complete empty query without hits", wantTypeHint: true},
		{name: "complete empty query with hits", hasHit: true, wantTypeHint: true},
		{name: "complete query without hits", query: "missing", wantNoMatches: true},
		{name: "complete query with hits", query: "data", hasHit: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
			screen.pending = tt.pending
			screen.namespaces = []string{"default"}
			screen.entries = []searchEntry{{namespace: "default", kind: k8s.KindSecret, name: "database"}}
			if tt.hasHit {
				screen.hits = []searchHit{{entry: screen.entries[0]}}
			}
			screen.input.SetValue(tt.query)
			if tt.hasHit {
				hit := searchHit{entry: screen.entries[0]}
				screen.hits = []searchHit{hit}
				align := &rowAlignment{}
				items := []list.Item{searchItem{hit: hit, align: align}}
				*align = measureRowAlignment(items)
				_ = screen.list.SetItems(items)
			}
			screen.SetSize(80, 12)

			view := screen.View()
			if got := strings.Contains(view, "no matches"); got != tt.wantNoMatches {
				t.Fatalf("no-matches visibility = %t, want %t; view = %q", got, tt.wantNoMatches, view)
			}
			if tt.wantNoMatches {
				for _, want := range []string{"[empty] no matches", "ctrl+r to rescan"} {
					if !strings.Contains(ansi.Strip(view), want) {
						t.Fatalf("no-matches state missing %q: %q", want, view)
					}
				}
			}
			if got := strings.Contains(view, "searching default..."); got != tt.pending {
				t.Fatalf("scanning-line visibility = %t, want %t; view = %q", got, tt.pending, view)
			}
			if got := strings.Contains(view, "type to search resource and key names across all namespaces"); got != tt.wantTypeHint {
				t.Fatalf("type-hint visibility = %t, want %t; view = %q", got, tt.wantTypeHint, view)
			}
			if got := strings.Contains(view, "Secret/database"); got != tt.hasHit {
				t.Fatalf("hit visibility = %t, want %t; view = %q", got, tt.hasHit, view)
			}
		})
	}
}

func TestSearchIndexCountsUsePlural(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  string
	}{
		{name: "zero", want: "0 resources across 0 namespaces indexed"},
		{name: "one", count: 1, want: "1 resource across 1 namespace indexed"},
		{name: "two", count: 2, want: "2 resources across 2 namespaces indexed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
			screen.entries = make([]searchEntry, test.count)
			screen.namespaces = make([]string, test.count)
			screen.SetSize(80, 12)

			lines := strings.Split(ansi.Strip(screen.View()), "\n")
			if got := lines[1]; got != test.want {
				t.Fatalf("complete index state = %q, want %q", got, test.want)
			}

			screen.pending = true
			pending := ansi.Strip(screen.View())
			wantPending := "(" + plural(test.count, "resource") + " indexed)"
			if !strings.Contains(pending, wantPending) {
				t.Fatalf("pending index state missing %q: %q", wantPending, pending)
			}
		})
	}
}

func TestSearchRecomputePreservesAndClampsListIndex(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
	screen.entries = []searchEntry{
		{namespace: "default", kind: k8s.KindSecret, name: "alpha"},
		{namespace: "default", kind: k8s.KindSecret, name: "beta"},
		{namespace: "default", kind: k8s.KindSecret, name: "gamma"},
	}
	screen.input.SetValue("a")
	_ = screen.recompute()
	screen.list.Select(2)

	_ = screen.recompute()
	if screen.list.Index() != 2 {
		t.Fatalf("preserved index = %d, want 2", screen.list.Index())
	}

	screen.entries = screen.entries[:1]
	_ = screen.recompute()
	if screen.list.Index() != 0 {
		t.Fatalf("clamped index = %d, want 0", screen.list.Index())
	}
}

func TestSearchWalkOrderAndStreaming(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
	screen.startWalk()
	reqID := screen.reqID
	screen.input.SetValue("data")

	msg := searchCommandMessage(t, screen, searchNamespacesMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"one", "two"}}})
	assertSearchRequest(t, msg, "one", k8s.KindSecret)
	msg = searchCommandMessage(t, screen, searchResourcesMsg{
		reqID: reqID, namespace: "one", kind: k8s.KindSecret,
		page: k8s.ResourcePage{Items: []k8s.Resource{searchSecret("one", "database", "token")}, Continue: "next"},
	})
	if len(screen.hits) != 1 {
		t.Fatalf("streamed hits after first page = %d, want 1", len(screen.hits))
	}
	assertSearchRequest(t, msg, "one", k8s.KindSecret)
	msg = searchCommandMessage(t, screen, searchResourcesMsg{reqID: reqID, namespace: "one", kind: k8s.KindSecret, page: k8s.ResourcePage{}})
	assertSearchRequest(t, msg, "one", k8s.KindConfigMap)
	msg = searchCommandMessage(t, screen, searchResourcesMsg{
		reqID: reqID, namespace: "one", kind: k8s.KindConfigMap,
		page: k8s.ResourcePage{Items: []k8s.Resource{searchConfigMap("one", "settings", "database-url")}},
	})
	if len(screen.hits) != 2 {
		t.Fatalf("streamed hits after configmap page = %d, want 2", len(screen.hits))
	}
	assertSearchRequest(t, msg, "two", k8s.KindSecret)
	msg = searchCommandMessage(t, screen, searchResourcesMsg{reqID: reqID, namespace: "two", kind: k8s.KindSecret, page: k8s.ResourcePage{}})
	assertSearchRequest(t, msg, "two", k8s.KindConfigMap)
	if cmd := searchUpdateCommand(t, screen, searchResourcesMsg{reqID: reqID, namespace: "two", kind: k8s.KindConfigMap, page: k8s.ResourcePage{}}); cmd != nil {
		t.Fatalf("final page returned command message %T", cmd())
	}
	if screen.pending {
		t.Fatal("search walk remained pending after final page")
	}
}

func TestSearchNamespacePaginationForwardsContinuation(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
	screen.startWalk()

	_ = searchCommandMessage(t, screen, searchNamespacesMsg{
		reqID: screen.reqID,
		page:  k8s.NamespacePage{Names: []string{"default"}, Continue: "next-search-namespaces"},
	})

	assertLastListContinueToken(t, screen.client, "next-search-namespaces")
}

func TestSearchJump(t *testing.T) {
	h := searchHarness(t, Options{})
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*searchScreen)
	screen.entries = []searchEntry{{namespace: "production", kind: k8s.KindSecret, name: "database", keys: []string{"password"}}}
	screen.input.SetValue("password")
	screen.recompute()
	h.keys("enter")
	stack := h.m.(app).stack
	if len(stack) != 3 {
		t.Fatalf("search jump stack depth = %d, want 3", len(stack))
	}
	if _, ok := stack[0].(*namespaceScreen); !ok {
		t.Fatalf("search jump root = %T", stack[0])
	}
	resources, ok := stack[1].(*resourceScreen)
	if !ok || resources.namespace != "production" {
		t.Fatalf("search jump resources = %#v", stack[1])
	}
	keys, ok := stack[2].(*keyScreen)
	if !ok || keys.kind != k8s.KindSecret || keys.namespace != "production" || keys.name != "database" {
		t.Fatalf("search jump keys = %#v", stack[2])
	}
}

func TestSearchCancellation(t *testing.T) {
	h := searchHarness(t, Options{})
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*searchScreen)
	screen.entries = []searchEntry{{namespace: "default", kind: k8s.KindSecret, name: "partial"}}
	_, _ = screen.Update(searchNamespacesMsg{reqID: screen.reqID, page: k8s.NamespacePage{Names: []string{"default"}, Continue: "next"}})
	oldReqID := screen.reqID
	h.keys("esc")
	view := h.view()
	if !screen.cancelled || screen.pending || len(screen.entries) != 1 || !strings.Contains(view, "1 resource across 1 namespace indexed") || !strings.Contains(view, "search cancelled") {
		t.Fatalf("cancelled search = cancelled %t pending %t entries %d view %q", screen.cancelled, screen.pending, len(screen.entries), h.view())
	}
	h.send(searchResourcesMsg{reqID: oldReqID, namespace: "default", kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{searchSecret("default", "stale", "key")}}})
	if len(screen.entries) != 1 {
		t.Fatalf("stale page entries = %d, want 1", len(screen.entries))
	}
	h.keys("ctrl+r")
	if !screen.pending || screen.reqID == oldReqID || screen.cancelled {
		t.Fatalf("restarted search = pending %t reqID %d cancelled %t", screen.pending, screen.reqID, screen.cancelled)
	}
	h.send(searchNamespacesMsg{reqID: screen.reqID, page: k8s.NamespacePage{}})
	if screen.pending {
		t.Fatal("empty namespace walk remained pending")
	}
	depth := len(h.m.(app).stack)
	h.keys("esc")
	if len(h.m.(app).stack) != depth-1 {
		t.Fatalf("idle esc stack depth = %d, want %d", len(h.m.(app).stack), depth-1)
	}
}

func TestSearchNamespaceListDenied(t *testing.T) {
	h := searchHarness(t, Options{})
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*searchScreen)
	h.send(searchNamespacesMsg{reqID: screen.reqID, err: errors.New("forbidden")})
	if len(screen.namespaces) != 1 || screen.namespaces[0] != "default" || len(screen.notes) != 1 || !strings.Contains(screen.notes[0].message, "searching default only") {
		t.Fatalf("namespace fallback = namespaces %v notes %v", screen.namespaces, screen.notes)
	}
	if screen.notes[0].namespace != "default" || screen.notes[0].kind != "Namespace" {
		t.Fatalf("namespace fallback note identity = %q/%q", screen.notes[0].namespace, screen.notes[0].kind)
	}
	assertSearchRequest(t, screen.nextFetch()(), "default", k8s.KindSecret)
}

func TestSearchNamespaceErrorSkipped(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{noConfigMaps: true}, testStyles(true))
	screen.startWalk()
	reqID := screen.reqID
	msg := searchCommandMessage(t, screen, searchNamespacesMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"denied", "allowed"}}})
	assertSearchRequest(t, msg, "denied", k8s.KindSecret)
	msg = searchCommandMessage(t, screen, searchResourcesMsg{reqID: reqID, namespace: "denied", kind: k8s.KindSecret, err: errors.New("forbidden")})
	assertSearchRequest(t, msg, "allowed", k8s.KindSecret)
	if len(screen.notes) != 1 || screen.notes[0].namespace != "denied" || screen.notes[0].kind != k8s.KindSecret {
		t.Fatalf("namespace errors = %v", screen.notes)
	}
}

func TestSearchAccessNotesNameNamespaceAndKind(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
	screen.startWalk()
	screen.namespaces = []string{"shared"}
	reqID := screen.reqID

	searchUpdateCommand(t, screen, searchResourcesMsg{reqID: reqID, namespace: "shared", kind: k8s.KindSecret, err: errors.New("secret forbidden")})
	searchUpdateCommand(t, screen, searchResourcesMsg{reqID: reqID, namespace: "shared", kind: k8s.KindConfigMap, err: errors.New("configmap forbidden")})
	screen.SetSize(80, 12)

	view := ansi.Strip(screen.View())
	for _, want := range []string{
		"shared Secret: secret forbidden",
		"shared ConfigMap: configmap forbidden",
		"[incomplete] 1 namespace affected",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("search access notes missing %q:\n%s", want, view)
		}
	}
}

func TestSearchNotesFitBodyAndKeepResultsReachable(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
	}{
		{name: "60x15", width: 60, height: 15},
		{name: "80x24", width: 80, height: 24},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := searchHarness(t, Options{})
			screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*searchScreen)
			screen.stop()
			screen.namespaces = make([]string, 20)
			for i := range screen.namespaces {
				screen.namespaces[i] = fmt.Sprintf("ns-%02d", i)
			}
			for i := 0; i < 40; i++ {
				kind := k8s.KindSecret
				if i%2 == 1 {
					kind = k8s.KindConfigMap
				}
				screen.notes = append(screen.notes, searchNote{
					namespace: screen.namespaces[i/2],
					kind:      kind,
					message:   "forbidden by namespace policy",
				})
			}
			screen.entries = []searchEntry{{namespace: "ns-00", kind: k8s.KindSecret, name: "match"}}
			screen.input.SetValue("match")
			screen.recompute()
			h.send(tea.WindowSizeMsg{Width: test.width, Height: test.height})

			body := ansi.Strip(screen.View())
			if lines := strings.Count(body, "\n") + 1; lines > screen.bodyHeight {
				t.Fatalf("search body height = %d, want <= %d:\n%s", lines, screen.bodyHeight, body)
			}
			for _, want := range []string{"[incomplete]", "20 namespaces", "notes hidden", "Secret/match"} {
				if !strings.Contains(body, want) {
					t.Fatalf("search body missing %q at %s:\n%s", want, test.name, body)
				}
			}
			fullView := ansi.Strip(h.view())
			if lines := strings.Count(fullView, "\n") + 1; lines > test.height {
				t.Fatalf("full app height = %d, want <= %d:\n%s", lines, test.height, fullView)
			}
			if !strings.Contains(strings.Split(fullView, "\n")[len(strings.Split(fullView, "\n"))-1], "enter open") {
				t.Fatalf("footer is not visible at %s:\n%s", test.name, fullView)
			}
		})
	}
}

func TestSearchStateLinePrecedence(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*searchScreen)
		want      string
	}{
		{
			name: "loading hides degraded and empty states",
			configure: func(screen *searchScreen) {
				screen.pending = true
				screen.cancelled = true
				screen.notes = []searchNote{{namespace: "default", kind: k8s.KindSecret, message: "forbidden"}}
				screen.input.SetValue("missing")
			},
			want: "[loading]",
		},
		{
			name: "cancelled without rows is unknown",
			configure: func(screen *searchScreen) {
				screen.cancelled = true
				screen.notes = []searchNote{{namespace: "default", kind: k8s.KindSecret, message: "forbidden"}}
				screen.input.SetValue("missing")
			},
			want: "[unknown]",
		},
		{
			name: "cancelled with rows is incomplete",
			configure: func(screen *searchScreen) {
				screen.cancelled = true
				screen.entries = []searchEntry{{namespace: "default", kind: k8s.KindSecret, name: "retained"}}
				screen.input.SetValue("missing")
				screen.recompute()
			},
			want: "[incomplete]",
		},
		{
			name: "access errors hide empty state",
			configure: func(screen *searchScreen) {
				screen.notes = []searchNote{{namespace: "default", kind: k8s.KindSecret, message: "forbidden"}}
				screen.input.SetValue("missing")
			},
			want: "[incomplete]",
		},
		{
			name: "empty completed search",
			configure: func(screen *searchScreen) {
				screen.input.SetValue("missing")
			},
			want: "[empty]",
		},
		{
			name: "populated search has no state claim",
			configure: func(screen *searchScreen) {
				screen.entries = []searchEntry{{namespace: "default", kind: k8s.KindSecret, name: "match"}}
				screen.input.SetValue("match")
				screen.recompute()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
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

func TestSearchNoConfigMaps(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{noConfigMaps: true}, testStyles(true))
	screen.startWalk()
	reqID := screen.reqID
	msg := searchCommandMessage(t, screen, searchNamespacesMsg{reqID: reqID, page: k8s.NamespacePage{Names: []string{"one", "two"}}})
	assertSearchRequest(t, msg, "one", k8s.KindSecret)
	msg = searchCommandMessage(t, screen, searchResourcesMsg{reqID: reqID, namespace: "one", kind: k8s.KindSecret, page: k8s.ResourcePage{}})
	assertSearchRequest(t, msg, "two", k8s.KindSecret)
	if cmd := searchUpdateCommand(t, screen, searchResourcesMsg{reqID: reqID, namespace: "two", kind: k8s.KindSecret, page: k8s.ResourcePage{}}); cmd != nil {
		t.Fatalf("secret-only final page returned %T", cmd())
	}
	if screen.pending {
		t.Fatal("secret-only walk remained pending")
	}
}

func TestSearchNotPushedFromCapturingScreens(t *testing.T) {
	h, _ := proposedFlowHarness(t, []byte("old"), []byte("new"))
	depth := len(h.m.(app).stack)
	h.keys("ctrl+f")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("edit-flow ctrl+f stack depth = %d, want %d", len(h.m.(app).stack), depth)
	}

	h = searchHarness(t, Options{})
	depth = len(h.m.(app).stack)
	h.keys("ctrl+f")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("search ctrl+f stack depth = %d, want %d", len(h.m.(app).stack), depth)
	}

	h = resourceHarness(t, true)
	depth = len(h.m.(app).stack)
	h.keys("ctrl+f")
	if len(h.m.(app).stack) != depth+1 {
		t.Fatalf("resource ctrl+f stack depth = %d, want %d", len(h.m.(app).stack), depth+1)
	}
}

func TestValueSearchToggle(t *testing.T) {
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "searchable", Namespace: "default"},
		Data: map[string][]byte{
			"password": []byte("hunter2"),
			"payload":  []byte("hunter2\x00"),
		},
	})
	h := keyHarness(t, resource)
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen)
	h.keys("v", "/")
	applyKeyFilter(t, h, "hunt")
	visible := screen.list.VisibleItems()
	if len(visible) != 1 || visible[0].(keyItem).key != "password" {
		t.Fatalf("value-search matches = %#v", visible)
	}
	h.keys("enter", "v")
	if screen.valueSearch || screen.list.FilterState() != list.Unfiltered || screen.list.FilterInput.Value() != "" {
		t.Fatalf("disabled value search = enabled %t state %v query %q", screen.valueSearch, screen.list.FilterState(), screen.list.FilterInput.Value())
	}
	h.keys("/")
	applyKeyFilter(t, h, "hunt")
	if visible = screen.list.VisibleItems(); len(visible) != 0 {
		t.Fatalf("key-only matches = %#v, want none", visible)
	}
}

func TestSearchInputWidthMeasuresPromptCells(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
	screen.input.Prompt = "界: "
	screen.SetSize(12, 6)
	if got := lipgloss.Width(screen.input.View()); got > 12 {
		t.Fatalf("search input view width = %d, want <= 12", got)
	}
}

func TestSearchTruncationFitsBodyHeight(t *testing.T) {
	screen := newSearchScreen(t.Context(), testClient(), editEnv{}, testStyles(true))
	screen.entries = make([]searchEntry, maxSearchHits+1)
	for i := range screen.entries {
		screen.entries[i] = searchEntry{namespace: "default", kind: k8s.KindSecret, name: "match"}
	}
	screen.input.SetValue("match")
	screen.SetSize(80, 6)
	screen.recompute()
	if !screen.truncated {
		t.Fatal("search results were not truncated")
	}
	if lines := strings.Count(screen.View(), "\n") + 1; lines > screen.bodyHeight {
		t.Fatalf("View() height = %d lines, want at most %d", lines, screen.bodyHeight)
	}
}

func renderedStateClaims(view string) []string {
	view = ansi.Strip(view)
	markers := []string{"[loading]", "[error]", "[unknown]", "[incomplete]", "[empty]", "[success]"}
	claims := make([]string, 0, 1)
	for _, marker := range markers {
		for range strings.Count(view, marker) {
			claims = append(claims, marker)
		}
	}
	return claims
}

func searchHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	opts.ASCII = true
	h := namespaceHarnessOptions(t, opts)
	h.keys("ctrl+f")
	return h
}

func testClient() *k8s.Client {
	return &k8s.Client{Clientset: fake.NewClientset(), Context: "test-ctx", Namespace: "default", Server: "https://test.example"}
}

func searchUpdateCommand(t *testing.T, screen *searchScreen, msg tea.Msg) tea.Cmd {
	t.Helper()
	_, cmd := screen.Update(msg)
	return cmd
}

func searchCommandMessage(t *testing.T, screen *searchScreen, msg tea.Msg) tea.Msg {
	t.Helper()
	cmd := searchUpdateCommand(t, screen, msg)
	if cmd == nil {
		t.Fatalf("search update for %T returned no command", msg)
	}
	return cmd()
}

func assertSearchRequest(t *testing.T, msg tea.Msg, namespace, kind string) {
	t.Helper()
	request, ok := msg.(searchResourcesMsg)
	if !ok || request.namespace != namespace || request.kind != kind {
		t.Fatalf("search request = %#v, want namespace %q kind %q", msg, namespace, kind)
	}
}

func searchSecret(namespace, name, key string) k8s.Resource {
	return k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: map[string][]byte{key: []byte("value")}})
}

func searchConfigMap(namespace, name, key string) k8s.Resource {
	return k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: map[string]string{key: "value"}})
}

func applyKeyFilter(t *testing.T, h *harness, value string) {
	t.Helper()
	for _, char := range value {
		model, cmd := h.m.Update(key(string(char)))
		h.m = model
		h.drain(cmd)
	}
}
