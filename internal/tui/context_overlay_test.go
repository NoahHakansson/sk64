package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/undo"
	"github.com/charmbracelet/x/ansi"
)

func TestContextOverlayCurrentIdentity(t *testing.T) {
	tests := []struct {
		name          string
		currentServer string
		rowServer     string
		wantCurrent   bool
		wantProbe     bool
	}{
		{
			name:          "identical name and server",
			currentServer: "https://api.example",
			rowServer:     "https://api.example",
			wantCurrent:   true,
		},
		{
			name:          "same name different server",
			currentServer: "https://active.example",
			rowServer:     "https://changed.example",
			wantProbe:     true,
		},
		{
			name:          "trailing slash is normalized",
			currentServer: "https://api.example",
			rowServer:     "https://api.example/",
			wantCurrent:   true,
		},
		{
			name:          "default port is normalized",
			currentServer: "https://api.example",
			rowServer:     "https://api.example:443",
			wantCurrent:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			overlay := newContextOverlay(t.Context(), "", "production", test.currentServer, nil, packageDefaultKeyMaps, testStyles(true))
			overlay.SetSize(80, 24)
			_ = overlay.loadContexts()
			overlay.Update(contextsLoadedMsg{reqID: overlay.reqID, contexts: []k8s.ContextInfo{{
				Name: "production", Server: test.rowServer, Current: true,
			}}})

			selected := overlay.list.SelectedItem().(contextItem)
			if selected.info.Current != test.wantCurrent {
				t.Fatalf("Current = %t, want %t", selected.info.Current, test.wantCurrent)
			}
			if markedCurrent := strings.Contains(selected.Title(), newGlyphs(true).currentTag); markedCurrent != test.wantCurrent {
				t.Fatalf("current marker = %t, want %t in %q", markedCurrent, test.wantCurrent, selected.Title())
			}

			cmd := overlay.Update(key("enter"))
			if test.wantProbe {
				if cmd == nil {
					t.Fatal("selecting the changed server returned no probe command")
				}
				if overlay.closed || overlay.state != overlayProbing || !overlay.pending || overlay.selectedName != "production" {
					t.Fatalf("probe state = %d, pending = %t, closed = %t, selected = %q", overlay.state, overlay.pending, overlay.closed, overlay.selectedName)
				}
				overlay.Update(key("esc"))
				if overlay.state != overlayList || overlay.pending || overlay.closed {
					t.Fatalf("cancelled probe state = %d, pending = %t, closed = %t", overlay.state, overlay.pending, overlay.closed)
				}
				return
			}
			if cmd != nil {
				t.Fatal("selecting the current identity returned a command")
			}
			if !overlay.closed || overlay.pending {
				t.Fatalf("current identity closed = %t, pending = %t", overlay.closed, overlay.pending)
			}
		})
	}
}

func TestReopenedContextOverlayRejectsPreviousLoad(t *testing.T) {
	model := newApp(t.Context(), Options{Client: testClient()})

	openedA, _ := model.Update(key("ctrl+k"))
	overlayA := openedA.(app).overlay.(*contextOverlay)
	reqA := overlayA.reqID
	closedA, actionCmd := openedA.(app).Update(key("esc"))
	if actionCmd == nil {
		t.Fatal("closing the overlay returned no action command")
	}
	actionAppliedA, releaseCmd := closedA.(app).Update(actionCmd())
	if releaseCmd == nil {
		t.Fatal("applying the close action returned no release command")
	}
	releasedA, _ := actionAppliedA.(app).Update(releaseCmd())

	openedB, _ := releasedA.(app).Update(key("ctrl+k"))
	overlayB := openedB.(app).overlay.(*contextOverlay)
	reqB := overlayB.reqID
	if reqA == reqB {
		t.Fatalf("reopened overlay request ID = %d, want a new identity", reqB)
	}

	afterStale, _ := openedB.(app).Update(contextsLoadedMsg{
		reqID:    reqA,
		contexts: []k8s.ContextInfo{{Name: "stale", Server: "https://stale.example"}},
	})
	staleOverlay := afterStale.(app).overlay.(*contextOverlay)
	if staleOverlay != overlayB || staleOverlay.state != overlayLoading || !staleOverlay.pending {
		t.Fatalf("stale result changed reopened overlay: same = %t state = %d pending = %t", staleOverlay == overlayB, staleOverlay.state, staleOverlay.pending)
	}

	afterCurrent, _ := afterStale.(app).Update(contextsLoadedMsg{
		reqID:    reqB,
		contexts: []k8s.ContextInfo{{Name: "current", Server: "https://current.example"}},
	})
	currentOverlay := afterCurrent.(app).overlay.(*contextOverlay)
	if currentOverlay.state != overlayList || currentOverlay.pending {
		t.Fatalf("current result state = %d pending = %t, want loaded list", currentOverlay.state, currentOverlay.pending)
	}
	if got := currentOverlay.list.Items()[0].(contextItem).info.Name; got != "current" {
		t.Fatalf("loaded context = %q, want current", got)
	}
}

func TestContextOverlayProbeSwitchResetsUndoAcrossServers(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	model := h.m.(app)
	model.editEnv.ring.Push(undo.Entry{
		Context: model.client.Context, Kind: k8s.KindSecret, Namespace: "default", Name: "credentials",
	})
	h.m = model

	h.keys("ctrl+k")
	overlay := h.m.(app).overlay.(*contextOverlay)
	if overlay.currentContext != model.client.Context || overlay.currentServer != model.client.Server {
		t.Fatalf("overlay identity = %q %q, want %q %q", overlay.currentContext, overlay.currentServer, model.client.Context, model.client.Server)
	}
	h.send(contextsLoadedMsg{reqID: overlay.reqID, contexts: []k8s.ContextInfo{{
		Name: model.client.Context, Server: "https://changed.example",
	}}})

	updated, probeCmd := h.m.Update(key("enter"))
	h.m = updated
	if probeCmd == nil {
		t.Fatal("selecting the changed server returned no probe command")
	}
	overlay = h.m.(app).overlay.(*contextOverlay)
	probedClient := *model.client
	probedClient.Server = "https://changed.example"
	h.send(contextProbedMsg{
		reqID:  overlay.reqID,
		name:   model.client.Context,
		client: &probedClient,
	})

	switched := h.m.(app)
	if switched.client.Server != probedClient.Server {
		t.Fatalf("active server = %q, want %q", switched.client.Server, probedClient.Server)
	}
	if got := switched.editEnv.ring.Len(); got != 0 {
		t.Fatalf("undo ring length = %d, want 0", got)
	}
}

func TestContextOverlayRowsShowServerAndDefaultNamespace(t *testing.T) {
	overlay := newContextOverlay(context.Background(), "", "ctx-a", "https://one.example", nil, packageDefaultKeyMaps, testStyles(true))
	overlay.SetSize(160, 40)
	_ = overlay.loadContexts()
	overlay.Update(contextsLoadedMsg{reqID: overlay.reqID, contexts: []k8s.ContextInfo{
		{Name: "ctx-a", Cluster: "shared-cluster", Server: "https://one.example"},
		{Name: "ctx-b", Cluster: "shared-cluster", Server: "https://two.example", Namespace: "production"},
	}})

	tests := []struct {
		name            string
		index           int
		wantTitle       []string
		wantDescription []string
	}{
		{
			name:            "process-local current context with implicit default namespace",
			index:           0,
			wantTitle:       []string{"ctx-a", newGlyphs(true).currentTag, "cluster: shared-cluster"},
			wantDescription: []string{"server: https://one.example", "namespace: default"},
		},
		{
			name:            "aliased context with explicit namespace",
			index:           1,
			wantTitle:       []string{"ctx-b", "cluster: shared-cluster"},
			wantDescription: []string{"server: https://two.example", "namespace: production"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item, ok := overlay.list.Items()[test.index].(contextItem)
			if !ok {
				t.Fatalf("item %d = %T, want contextItem", test.index, overlay.list.Items()[test.index])
			}
			for _, want := range test.wantTitle {
				if !strings.Contains(item.Title(), want) {
					t.Fatalf("title = %q, want %q", item.Title(), want)
				}
			}
			for _, want := range test.wantDescription {
				if !strings.Contains(item.Description(), want) {
					t.Fatalf("description = %q, want %q", item.Description(), want)
				}
			}
		})
	}

	view := ansi.Strip(overlay.View())
	for _, want := range []string{"https://one.example", "https://two.example", "namespace: default", "namespace: production"} {
		if !strings.Contains(view, want) {
			t.Fatalf("context view missing %q:\n%s", want, view)
		}
	}
}

func TestContextOverlaySelectedRowUsesOneCursorMarker(t *testing.T) {
	for _, test := range []struct {
		name  string
		ascii bool
	}{
		{name: "unicode"},
		{name: "ASCII", ascii: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := testStyles(test.ascii)
			marker := st.glyphs.cursorMarker
			overlay := newContextOverlay(context.Background(), "", "ctx-a", "https://a.example", nil, packageDefaultKeyMaps, st)
			overlay.SetSize(80, 24)
			_ = overlay.loadContexts()
			overlay.Update(contextsLoadedMsg{reqID: overlay.reqID, contexts: []k8s.ContextInfo{
				{Name: "ctx-a", Cluster: "cluster-a", Server: "https://a.example", Namespace: "default"},
				{Name: "ctx-b", Cluster: "cluster-b", Server: "https://b.example", Namespace: "production"},
			}})

			view := overlay.list.View()
			selectedTitle := lineContaining(t, view, "ctx-a")
			selectedDescription := lineContaining(t, view, "server: https://a.example")
			unselectedTitle := lineContaining(t, view, "ctx-b")
			unselectedDescription := lineContaining(t, view, "server: https://b.example")

			rows := []struct {
				name        string
				lines       []string
				wantMarkers int
			}{
				{name: "selected", lines: []string{selectedTitle, selectedDescription}, wantMarkers: 1},
				{name: "unselected", lines: []string{unselectedTitle, unselectedDescription}},
			}
			for _, row := range rows {
				t.Run(row.name, func(t *testing.T) {
					if got := strings.Count(strings.Join(row.lines, "\n"), marker); got != row.wantMarkers {
						t.Fatalf("cursor markers = %d, want %d in row:\n%s", got, row.wantMarkers, strings.Join(row.lines, "\n"))
					}
				})
			}

			selectedPrefix, _, selectedFound := strings.Cut(selectedDescription, "server:")
			unselectedPrefix, _, unselectedFound := strings.Cut(unselectedDescription, "server:")
			if !selectedFound || !unselectedFound {
				t.Fatalf("description lines have unexpected content: selected %q unselected %q", selectedDescription, unselectedDescription)
			}
			if selectedColumn, unselectedColumn := ansi.StringWidth(selectedPrefix), ansi.StringWidth(unselectedPrefix); selectedColumn != unselectedColumn {
				t.Fatalf("description columns differ: selected %d unselected %d", selectedColumn, unselectedColumn)
			}
			assertLinesFitWidth(t, view, overlay.list.Width())
		})
	}
}

func TestContextOverlayProbeStates(t *testing.T) {
	overlay := loadedContextOverlay(t)
	overlay.Update(key("down"))
	overlay.Update(key("enter"))

	if overlay.state != overlayProbing || !overlay.pending || overlay.selectedName != "ctx-b" {
		t.Fatalf("probe state = %d, pending = %t, selected = %q", overlay.state, overlay.pending, overlay.selectedName)
	}

	overlay.Update(key("esc"))
	if overlay.state != overlayList || overlay.pending {
		t.Fatalf("cancelled probe state = %d, pending = %t", overlay.state, overlay.pending)
	}

	overlay.Update(key("enter"))
	staleReqID := overlay.reqID - 1
	overlay.Update(contextProbedMsg{reqID: staleReqID, name: "ctx-b", err: context.Canceled})
	if overlay.state != overlayProbing || !overlay.pending {
		t.Fatalf("stale result changed reprobe state = %d, pending = %t", overlay.state, overlay.pending)
	}
	overlay.Update(contextProbedMsg{reqID: overlay.reqID, name: "ctx-b", err: errors.New("getting credentials: exec: login required")})
	if overlay.state != overlayExecOffer {
		t.Fatalf("exec-plugin failure state = %d", overlay.state)
	}

	overlay.Update(key("n"))
	overlay.Update(key("enter"))
	client := &k8s.Client{Context: "ctx-b"}
	cmd := overlay.Update(contextProbedMsg{reqID: overlay.reqID, name: "ctx-b", client: client})
	msg, ok := cmd().(contextSwitchedMsg)
	if !ok || msg.client != client {
		t.Fatalf("successful probe message = %#v", msg)
	}
}

func TestExecOfferRequiresUppercase(t *testing.T) {
	overlay := loadedContextOverlay(t)
	overlay.state = overlayExecOffer
	overlay.selectedName = "ctx-b"

	if cmd := overlay.Update(key("y")); cmd != nil {
		t.Fatal("lowercase y returned an exec command")
	}
	if overlay.state != overlayExecOffer || !overlay.execNudge {
		t.Fatalf("lowercase y state = %d nudge %t", overlay.state, overlay.execNudge)
	}
	if view := ansi.Strip(overlay.View()); !strings.Contains(view, pressYToConfirm) {
		t.Fatalf("lowercase y view has no confirmation nudge:\n%s", view)
	}
	if cmd := overlay.Update(key("Y")); cmd == nil {
		t.Fatal("uppercase Y did not return an exec command")
	}

	overlay.Update(execProbeDoneMsg{name: "ctx-b", err: errors.New("login failed")})
	if cmd := overlay.Update(key("enter")); cmd == nil {
		t.Fatal("retry did not start a context probe")
	}
	overlay.Update(contextProbedMsg{
		reqID: overlay.reqID,
		name:  "ctx-b",
		err:   errors.New("getting credentials: exec: login required"),
	})
	if overlay.state != overlayExecOffer || overlay.execNudge || strings.Contains(ansi.Strip(overlay.View()), pressYToConfirm) {
		t.Fatalf("re-entered exec offer kept stale nudge: state %d nudge %t view %q", overlay.state, overlay.execNudge, ansi.Strip(overlay.View()))
	}
}

func TestContextOverlayFilterCapturesEnterAndEscape(t *testing.T) {
	t.Run("enter applies filter", func(t *testing.T) {
		overlay := loadedContextOverlay(t)
		overlay.Update(key("/"))
		overlay.Update(key("c"))

		if !overlay.list.SettingFilter() {
			t.Fatal("list is not editing a filter")
		}
		overlay.Update(key("enter"))
		if overlay.state != overlayList || overlay.pending {
			t.Fatalf("enter started a probe: state = %d, pending = %t", overlay.state, overlay.pending)
		}
		if overlay.list.SettingFilter() {
			t.Fatal("enter did not apply the filter")
		}
	})

	t.Run("escape cancels filter", func(t *testing.T) {
		overlay := loadedContextOverlay(t)
		overlay.Update(key("/"))
		overlay.Update(key("x"))
		overlay.Update(key("esc"))

		if overlay.closed {
			t.Fatal("escape closed the overlay while editing a filter")
		}
		if overlay.list.SettingFilter() {
			t.Fatal("escape did not cancel the filter")
		}
	})
}

func TestContextOverlayStateLines(t *testing.T) {
	tests := []struct {
		name       string
		state      overlayState
		err        error
		selected   string
		want       string
		wantHints  string
		absentBody string
	}{
		{name: "loading", state: overlayLoading, want: "[loading] loading contexts", wantHints: "esc close"},
		{name: "probing", state: overlayProbing, selected: "ctx-b", want: "[loading] probing ctx-b", wantHints: "esc cancel", absentBody: "esc to cancel"},
		{name: "error", state: overlayError, err: errors.New("probe failed"), want: "[error] context unavailable: probe failed", wantHints: "enter retry  esc close", absentBody: "enter retry  esc close"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			overlay := loadedContextOverlay(t)
			overlay.state = test.state
			overlay.err = test.err
			overlay.selectedName = test.selected

			view := ansi.Strip(overlay.View())
			if !strings.Contains(view, test.want) {
				t.Fatalf("state view missing %q:\n%s", test.want, view)
			}
			if test.absentBody != "" && strings.Contains(view, test.absentBody) {
				t.Fatalf("state view duplicated footer hint %q:\n%s", test.absentBody, view)
			}
			if got := plainHints(t, overlay.Hints()); got != test.wantHints {
				t.Fatalf("Hints() = %q, want %q", got, test.wantHints)
			}
		})
	}
}

func TestContextOverlayErrorDescribesEscapeBehavior(t *testing.T) {
	overlay := loadedContextOverlay(t)
	overlay.state = overlayError
	overlay.err = errors.New("probe failed")

	if hints := plainHints(t, overlay.Hints()); hints != "enter retry  esc close" {
		t.Fatalf("error hints = %q, want enter retry and esc close", hints)
	}
	overlay.Update(key("esc"))
	if !overlay.closed {
		t.Fatal("escape did not close the error overlay")
	}
}

func TestContextOverlayResponsiveSize(t *testing.T) {
	overlay := loadedContextOverlay(t)
	overlay.Update(key("down"))

	overlay.SetSize(80, 22)
	if overlay.boxWidth != 60 || overlay.contentWidth != 54 {
		t.Fatalf("standard widths = %d/%d, want 60/54", overlay.boxWidth, overlay.contentWidth)
	}
	if overlay.list.Width() != overlay.contentWidth || overlay.list.Height() != 14 {
		t.Fatalf("standard list size = %dx%d, want %dx14", overlay.list.Width(), overlay.list.Height(), overlay.contentWidth)
	}
	if got := ansi.StringWidth(strings.Split(overlay.View(), "\n")[0]); got != overlay.boxWidth {
		t.Fatalf("rendered width = %d, want %d", got, overlay.boxWidth)
	}

	overlay.SetSize(160, 40)
	if overlay.boxWidth != 96 || overlay.contentWidth != 90 || overlay.list.Height() != 20 {
		t.Fatalf("tall size = %d/%d list height %d, want 96/90 and 20", overlay.boxWidth, overlay.contentWidth, overlay.list.Height())
	}
	if overlay.list.Index() != 1 {
		t.Fatalf("resize moved selection to %d, want 1", overlay.list.Index())
	}
	overlay.state = overlayError
	overlay.err = errors.New("probe failed")
	if got := ansi.StringWidth(strings.Split(overlay.View(), "\n")[0]); got != overlay.boxWidth {
		t.Fatalf("error-state width = %d, want %d", got, overlay.boxWidth)
	}
}

func TestAppSizesContextOverlayToBodyRectangle(t *testing.T) {
	h := namespaceHarness(t)
	h.send(tea.WindowSizeMsg{Width: 60, Height: 15})
	h.keys("ctrl+k")
	overlay := h.m.(app).overlay.(*contextOverlay)
	if overlay.list.Height() != 5 {
		t.Fatalf("minimum-size list height = %d, want 5 from the 13-row body", overlay.list.Height())
	}
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	if overlay.list.Height() != 14 {
		t.Fatalf("resized list height = %d, want 14 from the 22-row body", overlay.list.Height())
	}
}

func loadedContextOverlay(t *testing.T) *contextOverlay {
	t.Helper()
	overlay := newContextOverlay(context.Background(), "", "ctx-a", "https://a.example", nil, packageDefaultKeyMaps, testStyles(true))
	overlay.SetSize(80, 24)
	_ = overlay.loadContexts()
	overlay.Update(contextsLoadedMsg{reqID: overlay.reqID, contexts: []k8s.ContextInfo{
		{Name: "ctx-a", Cluster: "cluster-a", Server: "https://a.example", Namespace: "default"},
		{Name: "ctx-b", Cluster: "cluster-b", Server: "https://b.example", Namespace: "production"},
	}})
	return overlay
}
