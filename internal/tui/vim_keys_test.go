package tui

import (
	"testing"

	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
)

// Bubbles-backed lists and viewports ship j/k/h/l in their default keymaps,
// so only sk64's own hand-rolled cursor switches need coverage here.
func TestVimKeysMirrorArrows(t *testing.T) {
	tests := []struct {
		name  string
		drive func(*testing.T) (press func(string), cursor func() int)
	}{
		{
			name: "project rows",
			drive: func(t *testing.T) (func(string), func() int) {
				h, screen := vimProjectHarness(t)
				return func(value string) { h.keys(value) }, func() int { return screen.list.Index() }
			},
		},
		{
			name: "suggestion rows",
			drive: func(t *testing.T) (func(string), func() int) {
				root := t.TempDir()
				writeSuggestionFile(t, root, "kustomization.yaml", "namespace: production\nconfigMapGenerator:\n  - name: j-one\n  - name: j-two\n")
				h, _, _ := newSuggestionHarness(t, root, false)
				h.keys("s")
				screen := h.m.(app).stack[2].(*suggestionScreen)
				return func(value string) { h.keys(value) }, func() int { return screen.list.Index() }
			},
		},
		{
			name: "create choice",
			drive: func(t *testing.T) (func(string), func() int) {
				h := resourceHarness(t, true)
				h.keys("N")
				screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*createPrompt)
				return func(value string) { h.keys(value) }, func() int { return screen.cursor }
			},
		},
		{
			name: "rollout checklist",
			drive: func(t *testing.T) (func(string), func() int) {
				h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
				flow.radiusLoader.stop()
				flow.radius = k8s.NewRefIndex()
				flow.phase = phaseSaved
				flow.rollout = []rolloutItem{
					{kind: k8s.KindDeployment, name: "j-one", selected: true},
					{kind: k8s.KindDeployment, name: "j-two", selected: true},
				}
				_ = flow.rolloutList.SetItems(flow.rolloutChecklistItems())
				flow.SetSize(80, 10)
				flow.refreshContent()
				return func(value string) { h.keys(value) }, func() int { return flow.rolloutList.Index() }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			press, cursor := test.drive(t)
			initial := cursor()

			press("down")
			if got := cursor(); got != initial+1 {
				t.Fatalf("down cursor = %d, want %d", got, initial+1)
			}
			press("up")
			if got := cursor(); got != initial {
				t.Fatalf("up cursor = %d, want %d", got, initial)
			}
			press("j")
			if got := cursor(); got != initial+1 {
				t.Fatalf("j cursor = %d, want %d", got, initial+1)
			}
			press("k")
			if got := cursor(); got != initial {
				t.Fatalf("k cursor = %d, want %d", got, initial)
			}
		})
	}
}

// The screens with a vim cursor switch must still route letters to a capturing
// input instead of navigating.
func TestVimKeysTypeWhileCapturing(t *testing.T) {
	tests := []struct {
		name  string
		press string
		drive func(*testing.T) (press func(string), value func() string, cursor func() int)
	}{
		{
			name:  "row filter",
			press: "j",
			drive: func(t *testing.T) (func(string), func() string, func() int) {
				h, screen := vimProjectHarness(t)
				h.keys("/")
				return func(value string) { h.keys(value) }, func() string { return screen.list.FilterInput.Value() }, func() int { return screen.list.Index() }
			},
		},
		{
			name:  "delete name",
			press: "j",
			drive: func(t *testing.T) (func(string), func() string, func() int) {
				h := resourceHarness(t, true)
				h.keys("D")
				resourceScreen := h.m.(app).stack[len(h.m.(app).stack)-2].(*resourceScreen)
				screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
				h.send(resourceLoadedMsg{reqID: screen.reqID, res: deleteTestSecret(false)})
				return func(value string) { h.keys(value) }, func() string { return screen.input.Value() }, func() int { return resourceScreen.list.Index() }
			},
		},
		{
			name:  "unlink gate",
			press: "k",
			drive: func(t *testing.T) (func(string), func() string, func() int) {
				h, screen := vimProjectHarness(t)
				h.keys("u")
				return func(value string) { h.keys(value) }, func() string { return screen.unlinkGate.input.Value() }, func() int { return screen.list.Index() }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			press, value, cursor := test.drive(t)
			initialCursor := cursor()

			press(test.press)
			if got := value(); got != test.press {
				t.Fatalf("input value = %q, want %q", got, test.press)
			}
			if got := cursor(); got != initialCursor {
				t.Fatalf("cursor = %d, want unchanged at %d", got, initialCursor)
			}
		})
	}
}

func vimProjectHarness(t *testing.T) (*harness, *projectScreen) {
	t.Helper()
	st := newTestStore(t)
	project := createProject(t, st, "vim", "/repos/vim", "other-ctx", "default")
	h, screen := projectHarness(t, st, project, "")
	h.send(
		projectLinksMsg{
			reqID: screen.reqID,
			resources: []store.ResourceLink{
				{Kind: k8s.KindConfigMap, Namespace: "default", Name: "j-one", Source: store.SourceManual},
				{Kind: k8s.KindSecret, Namespace: "default", Name: "j-two", Source: store.SourceManual},
			},
		},
		projectContextMsg{reqID: screen.reqID, found: true},
	)
	return h, screen
}
