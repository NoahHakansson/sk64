package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/config"
	"github.com/NoahHakansson/sk64/internal/debuglog"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAppAppliesRefreshOverride(t *testing.T) {
	h := namespaceHarnessOptions(t, Options{
		ASCII:    true,
		Keybinds: config.Overrides{config.ActionRefresh: {"ctrl+e"}},
	})

	view := h.view()
	if !strings.Contains(view, "ctrl+e refresh") || strings.Contains(view, "ctrl+r") {
		t.Fatalf("namespace footer does not reflect refresh override:\n%s", view)
	}
	h.keys("?")
	if view := h.view(); !strings.Contains(view, "ctrl+e") || !strings.Contains(view, "refresh the current screen") {
		t.Fatalf("help overlay does not reflect refresh override:\n%s", view)
	}
	h.keys("esc")

	screen := h.m.(app).stack[0].(*namespaceScreen)
	previousReqID := screen.reqID
	h.keys("ctrl+e")
	if screen.reqID == previousReqID {
		t.Fatal("rebound refresh key did not start a request")
	}
	h.send(namespacesPageMsg{reqID: screen.reqID, page: k8s.NamespacePage{}})
	previousReqID = screen.reqID
	h.keys("ctrl+r")
	if screen.reqID != previousReqID {
		t.Fatal("default refresh key still started a request")
	}
}

func TestRefreshOverrideReachesStateLine(t *testing.T) {
	h := namespaceHarnessOptions(t, Options{
		ASCII:    true,
		Keybinds: config.Overrides{config.ActionRefresh: {"ctrl+e"}},
	})
	screen := h.m.(app).stack[0].(*namespaceScreen)
	screen.err = errors.New("API unavailable")

	view := h.view()
	if !strings.Contains(view, "ctrl+e to retry") || strings.Contains(view, "ctrl+r to retry") {
		t.Fatalf("namespace state line does not reflect refresh override:\n%s", view)
	}
}

func TestSearchInputAcceptsPrintableNavigationOverride(t *testing.T) {
	h := searchHarness(t, Options{
		Keybinds: config.Overrides{config.ActionDown: {"e"}},
	})
	h.keys("e")
	search := h.m.(app).stack[len(h.m.(app).stack)-1].(*searchScreen)
	if got := search.input.Value(); got != "e" {
		t.Fatalf("search input = %q, want e", got)
	}
}

func TestNormalizeRunError(t *testing.T) {
	t.Run("canceled program", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := normalizeRunError(ctx, fmt.Errorf("tea: %w", tea.ErrProgramKilled))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("normalizeRunError() = %v, want context.Canceled", err)
		}
	})

	t.Run("uncanceled failure", func(t *testing.T) {
		failure := errors.New("terminal failure")
		err := normalizeRunError(context.Background(), failure)
		if !errors.Is(err, failure) {
			t.Fatalf("normalizeRunError() = %v, want wrapped failure", err)
		}
	})
}

func TestRunResultReportsFatal(t *testing.T) {
	fatal := errors.New("boom")
	err := runResult(app{fatal: fatal})
	if err != fatal {
		t.Fatalf("runResult(fatal) = %#v, want original error", err)
	}
	if err := runResult(app{}); err != nil {
		t.Fatalf("runResult(clean) = %v, want nil", err)
	}
}

func TestAppDropsDuplicateAndStaleNavigation(t *testing.T) {
	t.Run("two queued escapes pop only their modal", func(t *testing.T) {
		model := newNavigationTestApp(t, true)

		firstModel, firstPop := model.Update(key("esc"))
		secondModel, secondPop := firstModel.(app).Update(key("esc"))
		if firstPop == nil || secondPop == nil {
			t.Fatal("escape did not queue both modal pop commands")
		}

		afterFirst, _ := secondModel.(app).Update(firstPop())
		afterSecond, _ := afterFirst.(app).Update(secondPop())
		if got := len(afterSecond.(app).stack); got != 1 {
			t.Fatalf("stack depth = %d, want root only", got)
		}
	})

	t.Run("two queued enters push one destination", func(t *testing.T) {
		model := newNavigationTestApp(t, false)

		firstModel, firstPush := model.Update(key("enter"))
		secondModel, secondPush := firstModel.(app).Update(key("enter"))
		if firstPush == nil || secondPush == nil {
			t.Fatal("enter did not queue both destination pushes")
		}

		afterFirst, _ := secondModel.(app).Update(firstPush())
		afterSecond, _ := afterFirst.(app).Update(secondPush())
		stack := afterSecond.(app).stack
		if got := len(stack); got != 3 {
			t.Fatalf("stack depth = %d, want root, source, and one destination", got)
		}
		if stack[len(stack)-1].Title() != "destination" {
			t.Fatalf("top screen = %q, want destination", stack[len(stack)-1].Title())
		}
	})

	for _, test := range []struct {
		name        string
		wantDepth   int
		leaveSource func(app) app
	}{
		{
			name:      "source popped",
			wantDepth: 1,
			leaveSource: func(model app) app {
				updated, _ := model.Update(key("esc"))
				return updated.(app)
			},
		},
		{
			name:      "source replaced",
			wantDepth: 2,
			leaveSource: func(model app) app {
				updated, _ := model.Update(replaceScreenMsg{
					generation: model.stackGeneration,
					s:          &chromeTestScreen{title: "replacement"},
				})
				return updated.(app)
			},
		},
	} {
		t.Run("queued push ignored after "+test.name, func(t *testing.T) {
			model := newNavigationTestApp(t, false)
			queuedModel, queuedPush := model.Update(key("enter"))
			if queuedPush == nil {
				t.Fatal("enter did not queue a destination push")
			}

			left := test.leaveSource(queuedModel.(app))
			updated, _ := left.Update(queuedPush())
			stack := updated.(app).stack
			if got := len(stack); got != test.wantDepth {
				t.Fatalf("stack depth = %d, want %d", got, test.wantDepth)
			}
			if stack[len(stack)-1].Title() == "destination" {
				t.Fatal("stale destination push was accepted")
			}
		})
	}
}

func TestAppScopesAndRejectsStaleCustomNavigation(t *testing.T) {
	resourceLink := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "production", Name: "credentials", Source: store.SourceManual}
	tests := []struct {
		name    string
		message tea.Msg
		assert  func(*testing.T, app, app)
	}{
		{
			name:    "search jump",
			message: searchJumpMsg{namespace: "production", kind: k8s.KindSecret, name: "credentials"},
			assert: func(t *testing.T, before, after app) {
				t.Helper()
				if after.stackGeneration != before.stackGeneration+1 {
					t.Fatalf("stack generation = %d, want %d", after.stackGeneration, before.stackGeneration+1)
				}
				if len(after.stack) != 3 {
					t.Fatalf("stack depth = %d, want namespace, resource, and key screens", len(after.stack))
				}
				resources, ok := after.stack[1].(*resourceScreen)
				if !ok || resources.namespace != "production" {
					t.Fatalf("resource screen = %#v, want production namespace", after.stack[1])
				}
				keys, ok := after.stack[2].(*keyScreen)
				if !ok || keys.kind != k8s.KindSecret || keys.namespace != "production" || keys.name != "credentials" {
					t.Fatalf("key screen = %#v, want production Secret credentials", after.stack[2])
				}
			},
		},
		{
			name:    "project picker",
			message: openProjectPickerMsg{link: pendingLink{resource: &resourceLink}},
			assert: func(t *testing.T, before, after app) {
				t.Helper()
				if after.stackGeneration != before.stackGeneration {
					t.Fatalf("stack generation = %d, want %d", after.stackGeneration, before.stackGeneration)
				}
				if len(after.stack) != len(before.stack) {
					t.Fatalf("stack depth = %d, want %d", len(after.stack), len(before.stack))
				}
				overlay, ok := after.overlay.(*projectOverlay)
				if !ok {
					t.Fatalf("overlay = %T, want *projectOverlay", after.overlay)
				}
				if overlay.mode != projectModeLink || overlay.link.resource == nil || *overlay.link.resource != resourceLink {
					t.Fatalf("project overlay mode/link = %v/%#v, want link mode with %#v", overlay.mode, overlay.link.resource, resourceLink)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name+" current generation", func(t *testing.T) {
			model := newApp(t.Context(), Options{Client: testClient()})
			model.stack = append(model.stack, &navigationTestScreen{title: "source", message: test.message})
			model.stackGeneration++

			queuedModel, queuedCmd := model.Update(key("enter"))
			if queuedCmd == nil {
				t.Fatal("enter returned no custom navigation command")
			}
			before := queuedModel.(app)
			queued := queuedCmd()
			if got := customNavigationGeneration(t, queued); got != before.stackGeneration {
				t.Fatalf("queued generation = %d, want %d", got, before.stackGeneration)
			}

			updated, _ := before.Update(queued)
			test.assert(t, before, updated.(app))
		})

		t.Run(test.name+" stale after escape", func(t *testing.T) {
			model := newApp(t.Context(), Options{Client: testClient()})
			root := model.stack[0]
			model.stack = append(model.stack, &navigationTestScreen{title: "source", message: test.message})
			model.stackGeneration++

			queuedModel, queuedCmd := model.Update(key("enter"))
			if queuedCmd == nil {
				t.Fatal("enter returned no custom navigation command")
			}
			queued := queuedCmd()
			escapedModel, _ := queuedModel.(app).Update(key("esc"))
			escaped := escapedModel.(app)
			if len(escaped.stack) != 1 || escaped.stack[0] != root {
				t.Fatalf("escape stack = %#v, want original root only", escaped.stack)
			}

			updated, cmd := escaped.Update(queued)
			if cmd != nil {
				t.Fatal("stale custom navigation returned a command")
			}
			after := updated.(app)
			if len(after.stack) != 1 || after.stack[0] != root {
				t.Fatalf("stale custom navigation changed stack to %#v", after.stack)
			}
			if after.stackGeneration != escaped.stackGeneration {
				t.Fatalf("stale custom navigation changed generation to %d, want %d", after.stackGeneration, escaped.stackGeneration)
			}
			if after.overlay != nil {
				t.Fatalf("stale custom navigation opened overlay %T", after.overlay)
			}
		})
	}
}

func customNavigationGeneration(t *testing.T, msg tea.Msg) uint64 {
	t.Helper()
	switch msg := msg.(type) {
	case searchJumpMsg:
		return msg.generation
	case openProjectPickerMsg:
		return msg.generation
	default:
		t.Fatalf("custom navigation message = %T", msg)
		return 0
	}
}

func TestAppLatchesOverlayClosureUntilRelease(t *testing.T) {
	model := newApp(t.Context(), Options{Client: testClient()})
	model.stack = append(model.stack, &chromeTestScreen{title: "parent"})
	model.stackGeneration++
	model.overlay = newHelpOverlay(model.stack[len(model.stack)-1], model.editEnv, model.styles)

	firstModel, release := model.Update(key("esc"))
	if release == nil {
		t.Fatal("closing the overlay returned no release command")
	}
	secondModel, secondCmd := firstModel.(app).Update(key("esc"))
	if secondCmd != nil {
		t.Fatal("queued second escape returned a parent command")
	}
	if got := len(secondModel.(app).stack); got != 2 {
		t.Fatalf("stack depth = %d, want parent retained", got)
	}
}

func TestAppOrdersOverlayClosureActionBeforeRelease(t *testing.T) {
	tests := []struct {
		name                    string
		applyActionBeforeRepeat bool
	}{
		{name: "repeat while action pending"},
		{name: "repeat between action and release", applyActionBeforeRepeat: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newNavigationTestApp(t, false)
			model.overlay = &closingNavigationOverlay{destination: &chromeTestScreen{title: "overlay destination"}}

			closedModel, actionCmd := model.Update(key("enter"))
			if actionCmd == nil {
				t.Fatal("closing the overlay returned no action command")
			}
			closed := closedModel.(app)
			sequence := closed.overlayClosing
			rawAction := actionCmd()
			actionMsg, ok := rawAction.(overlayCloseActionMsg)
			if !ok {
				t.Fatalf("close command returned %T, want overlayCloseActionMsg", rawAction)
			}

			prematureRelease, _ := closed.Update(overlayClosedMsg{sequence: sequence})
			current := prematureRelease.(app)
			if current.overlayClosing != sequence {
				t.Fatal("release cleared the latch before the overlay action was applied")
			}

			var releaseCmd tea.Cmd
			if test.applyActionBeforeRepeat {
				actionApplied, release := current.Update(actionMsg)
				current, releaseCmd = actionApplied.(app), release
			}

			generationBeforeRepeat := current.stackGeneration
			stackDepthBeforeRepeat := len(current.stack)
			repeatedModel, repeatedCmd := current.Update(key("enter"))
			if repeatedCmd != nil {
				t.Fatal("repeated enter returned an underlying-screen command")
			}
			current = repeatedModel.(app)
			if current.stackGeneration != generationBeforeRepeat {
				t.Fatalf("stack generation = %d, want %d", current.stackGeneration, generationBeforeRepeat)
			}
			if len(current.stack) != stackDepthBeforeRepeat {
				t.Fatalf("stack depth = %d, want %d", len(current.stack), stackDepthBeforeRepeat)
			}

			if !test.applyActionBeforeRepeat {
				actionApplied, release := current.Update(actionMsg)
				current, releaseCmd = actionApplied.(app), release
			}
			if releaseCmd == nil {
				t.Fatal("applying the overlay action returned no release command")
			}
			if got := current.stack[len(current.stack)-1].Title(); got != "overlay destination" {
				t.Fatalf("top screen = %q, want overlay destination", got)
			}
			if current.overlayClosing != sequence {
				t.Fatal("overlay latch released before its release message")
			}

			releasedModel, _ := current.Update(releaseCmd())
			if releasedModel.(app).overlayClosing != 0 {
				t.Fatal("overlay latch remained engaged after the ordered release")
			}
		})
	}
}

func TestAppDiscardsSupersededOverlayRelease(t *testing.T) {
	model := newApp(t.Context(), Options{Client: testClient()})
	model.overlay = newHelpOverlay(model.stack[len(model.stack)-1], model.editEnv, model.styles)

	closedA, actionA := model.Update(key("esc"))
	if actionA == nil {
		t.Fatal("first overlay close returned no action command")
	}
	actionAppliedA, releaseA := closedA.(app).Update(actionA())
	if releaseA == nil {
		t.Fatal("first overlay action returned no release command")
	}

	current := actionAppliedA.(app)
	openedB, _ := current.Update(openProjectPickerMsg{generation: current.stackGeneration})
	closedB, actionB := openedB.(app).Update(key("esc"))
	if actionB == nil {
		t.Fatal("second overlay close returned no action command")
	}
	actionAppliedB, releaseB := closedB.(app).Update(actionB())
	if releaseB == nil {
		t.Fatal("second overlay action returned no release command")
	}
	sequenceB := actionAppliedB.(app).overlayClosing

	afterStaleRelease, _ := actionAppliedB.(app).Update(releaseA())
	if got := afterStaleRelease.(app).overlayClosing; got != sequenceB {
		t.Fatalf("stale release changed overlay sequence to %d, want %d", got, sequenceB)
	}
	afterCurrentRelease, _ := afterStaleRelease.(app).Update(releaseB())
	if afterCurrentRelease.(app).overlayClosing != 0 {
		t.Fatal("current overlay release did not clear the latch")
	}
}

func newNavigationTestApp(t *testing.T, wantsEsc bool) app {
	t.Helper()
	model := newApp(t.Context(), Options{Client: testClient()})
	model.stack = append(model.stack, &navigationTestScreen{
		title:       "source",
		wantsEsc:    wantsEsc,
		destination: &chromeTestScreen{title: "destination"},
	})
	model.stackGeneration++
	return model
}

type navigationTestScreen struct {
	title       string
	wantsEsc    bool
	destination screen
	message     tea.Msg
}

func (s *navigationTestScreen) Init() tea.Cmd { return nil }
func (s *navigationTestScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	switch key.String() {
	case "enter":
		if s.message != nil {
			message := s.message
			return s, func() tea.Msg { return message }
		}
		return s, pushScreen(s.destination)
	case "esc":
		return s, popScreen()
	default:
		return s, nil
	}
}
func (s *navigationTestScreen) View() string      { return "body" }
func (s *navigationTestScreen) SetSize(int, int)  {}
func (s *navigationTestScreen) SetStyles(*styles) {}
func (s *navigationTestScreen) Title() string     { return s.title }
func (s *navigationTestScreen) Hints() footerHints {
	return hintBindings(hintDesc(bindEnter, "open"), hintDesc(bindEsc, "close"))
}
func (s *navigationTestScreen) Help() helpGroup     { return helpGroup{title: s.title} }
func (s *navigationTestScreen) CapturesInput() bool { return false }
func (s *navigationTestScreen) WantsEsc() bool      { return s.wantsEsc }

type closingNavigationOverlay struct {
	destination screen
	closed      bool
}

func (o *closingNavigationOverlay) Init() tea.Cmd { return nil }
func (o *closingNavigationOverlay) Update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok || key.String() != "enter" {
		return nil
	}
	o.closed = true
	return pushScreen(o.destination)
}
func (o *closingNavigationOverlay) View() string      { return "overlay" }
func (o *closingNavigationOverlay) SetSize(int, int)  {}
func (o *closingNavigationOverlay) SetStyles(*styles) {}
func (o *closingNavigationOverlay) Hints() footerHints {
	return hintBindings(hintDesc(bindEnter, "open"), hintDesc(bindEsc, "close"))
}
func (o *closingNavigationOverlay) isClosed() bool { return o.closed }

func TestDebugLogRecordsNamesNotValues(t *testing.T) {
	t.Cleanup(editor.CleanupAll)
	path := filepath.Join(t.TempDir(), "debug.log")
	logger, err := debuglog.Open(path)
	if err != nil {
		t.Fatalf("open debug log: %v", err)
	}
	plaintext := "hunter2-plaintext"
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "default", ResourceVersion: "10"},
		Data:       map[string][]byte{"DB_PASSWORD": []byte(plaintext)},
	})
	h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true, Editor: "true", Debug: logger})
	h.keys("enter")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "changed")
	h.send(editorFinishedMsg{})
	if err := logger.Close(); err != nil {
		t.Fatalf("close debug log: %v", err)
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- path is created inside this test's temporary directory.
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	logged := string(contents)
	for _, want := range []string{"op=open-keys", "op=edit", "key=DB_PASSWORD size="} {
		if !strings.Contains(logged, want) {
			t.Fatalf("debug log missing %q:\n%s", want, logged)
		}
	}
	if strings.Contains(logged, plaintext) {
		t.Fatalf("debug log contains secret plaintext:\n%s", logged)
	}
}

func TestBackgroundColorRebuildsThemeWithoutNavigationChange(t *testing.T) {
	model := newApp(t.Context(), Options{Client: testClient()})
	root := model.stack[0].(*namespaceScreen)
	root.list.SetSize(40, 10)
	_ = root.list.SetItems([]list.Item{namespaceItem("one"), namespaceItem("two")})
	root.list.Select(1)

	search := newSearchScreen(t.Context(), model.client, model.editEnv, model.styles)
	search.input.SetValue("keep this query")
	search.entries = []searchEntry{{name: "keep this query 0"}, {name: "keep this query 1"}, {name: "keep this query 2"}, {name: "keep this query 3"}, {name: "keep this query 4"}}
	_ = search.recompute()
	search.list.Select(4)
	model.stack = append(model.stack, search)

	overlay := newContextOverlay(t.Context(), "", model.client.Context, model.client.Server, nil, packageDefaultKeyMaps, model.styles)
	overlay.state = overlayList
	overlay.selectedName = "keep-this-context"
	model.overlay = overlay
	model.width, model.height = 100, 30

	beforeStyles := model.styles
	beforeAccent := model.styles.palette.accent
	beforeStack := append([]screen(nil), model.stack...)
	beforeOverlay := model.overlay

	updatedModel, cmd := model.Update(tea.BackgroundColorMsg{Color: color.White})
	if cmd != nil {
		t.Fatal("background color update returned a command")
	}
	updated := updatedModel.(app)
	if updated.styles == beforeStyles {
		t.Fatal("background color update mutated the shared styles object instead of replacing it")
	}
	if sameColor(beforeAccent, updated.styles.palette.accent) {
		t.Fatal("background color update did not rebuild the semantic palette")
	}
	light := newSemanticPalette(false)
	requireSameColor(t, updated.styles.palette.accent, light.accent)

	if len(updated.stack) != len(beforeStack) {
		t.Fatalf("stack length = %d, want %d", len(updated.stack), len(beforeStack))
	}
	for index := range beforeStack {
		if updated.stack[index] != beforeStack[index] {
			t.Fatalf("stack screen %d was replaced during retheme", index)
		}
	}
	if updated.overlay != beforeOverlay {
		t.Fatal("overlay was replaced during retheme")
	}
	if updated.width != 100 || updated.height != 30 {
		t.Fatalf("size = %dx%d, want 100x30", updated.width, updated.height)
	}
	if root.list.Index() != 1 {
		t.Fatalf("list index = %d, want 1", root.list.Index())
	}
	if search.input.Value() != "keep this query" || search.list.Index() != 4 {
		t.Fatalf("search state = query %q cursor %d", search.input.Value(), search.list.Index())
	}
	if overlay.state != overlayList || overlay.selectedName != "keep-this-context" {
		t.Fatalf("overlay state = %v selected %q", overlay.state, overlay.selectedName)
	}

	for name, screenStyles := range map[string]*styles{
		"root":    root.styles,
		"search":  search.styles,
		"overlay": overlay.styles,
	} {
		if screenStyles != updated.styles {
			t.Errorf("%s styles pointer was not kept shared", name)
		}
	}
	requireSameColor(t, updated.styles.header.GetBackground(), light.brand)
	requireSameColor(t, updated.styles.activeContext.GetForeground(), light.gold)
	requireSameColor(t, updated.styles.activeContext.GetBackground(), light.brand)
	requireSameColor(t, updated.styles.helpBox.GetBorderLeftForeground(), light.fgFaint)
	requireSameColor(t, updated.styles.dialogBox.GetBorderLeftForeground(), light.brand)
	requireSameColor(t, updated.styles.dialogDanger.GetBorderLeftForeground(), light.danger)
	requireNoColor(t, updated.styles.footer.GetBackground())
	if updated.styles.header.GetBold() || !updated.styles.activeContext.GetBold() {
		t.Fatal("rethemed rail did not reserve weight for the active context")
	}
	requireNoColor(t, root.list.Styles.DefaultFilterCharacterMatch.GetForeground())
	if !root.list.Styles.DefaultFilterCharacterMatch.GetUnderline() {
		t.Fatal("rethemed filter match lost its underline")
	}
	requireSameColor(t, root.list.FilterInput.Styles().Cursor.Color, light.accent)
	requireSameColor(t, search.input.Styles().Focused.Prompt.GetForeground(), light.accent)
	requireSameColor(t, overlay.list.Styles.ActivePaginationDot.GetForeground(), light.accent)
	requireSameColor(t, overlay.list.FilterInput.Styles().Cursor.Color, light.accent)
	requireSameColor(t, overlay.spinner.Style.GetForeground(), light.accent)
	if root.list.Paginator.ActiveDot != updated.styles.listStyle.ActivePaginationDot.String() ||
		root.list.Paginator.InactiveDot != updated.styles.listStyle.InactivePaginationDot.String() {
		t.Fatal("root paginator retained its previous theme")
	}
	selectedRow := updated.styles.renderSelectionBand("two", root.list.Width())
	if !strings.Contains(root.list.View(), selectedRow) {
		t.Fatalf("selected row did not use rebuilt delegate style: %q", root.list.View())
	}
}

func TestRawAppViewRendersThemeRolesAndSelectionEmphasis(t *testing.T) {
	themes := []struct {
		name       string
		background color.Color
	}{
		{name: "light", background: color.White},
		{name: "dark", background: color.Black},
	}
	var previousRaw, previousPlain string
	for _, theme := range themes {
		t.Run(theme.name, func(t *testing.T) {
			client := testClient()
			model := newApp(t.Context(), Options{Client: client})
			root := model.stack[0].(*namespaceScreen)
			root.names = []string{"production", "staging"}
			root.loadComplete = true
			_ = root.setItems()

			updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			model = updated.(app)
			updated, _ = model.Update(tea.BackgroundColorMsg{Color: theme.background})
			model = updated.(app)

			raw := model.View().Content
			plain := ansi.Strip(raw)
			if raw == plain || !strings.Contains(raw, "\x1b[") {
				t.Fatalf("raw app view contains no ANSI styling:\n%s", plain)
			}
			styledFragments := []struct {
				name  string
				value string
			}{
				{name: "header", value: model.styles.header.Inline(true).Render(client.Server)},
				{name: "identity", value: model.styles.dim.Render("ns: default")},
				{name: "selection", value: model.styles.renderSelectionBand("production", root.list.Width())},
				{name: "footer", value: model.styles.footerKey.Inline(true).Render("enter") + " " + model.styles.footer.Inline(true).Render("open")},
			}
			for _, fragment := range styledFragments {
				if !strings.Contains(raw, fragment.value) {
					t.Fatalf("raw app view missing %s role %q", fragment.name, fragment.value)
				}
			}
			if marker := model.styles.glyphs.cursorMarker + " production"; !strings.Contains(plain, marker) {
				t.Fatalf("selection lacks monochrome marker %q:\n%s", marker, plain)
			}

			if previousRaw != "" {
				if raw == previousRaw {
					t.Fatal("light and dark raw views are identical")
				}
				if plain != previousPlain {
					t.Fatal("light and dark themes changed visible text or layout")
				}
			}
			previousRaw, previousPlain = raw, plain
		})
	}
}

func TestRawResourceListReservesAccentForSelection(t *testing.T) {
	for _, theme := range []struct {
		name       string
		background color.Color
	}{
		{name: "light", background: color.White},
		{name: "dark", background: color.Black},
	} {
		t.Run(theme.name, func(t *testing.T) {
			client := testClient()
			model := newApp(t.Context(), Options{Client: client, ASCII: true})
			root := model.stack[0].(*namespaceScreen)
			resources := newResourceScreen(t.Context(), client, "default", model.editEnv, model.styles)
			resources.all = []k8s.Resource{
				k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "default"}}),
				k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "beta", Namespace: "default"}}),
			}
			resources.loadComplete = true
			_ = resources.setVisibleItems()
			model.stack = []screen{root, resources}

			updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			model = updated.(app)
			updated, _ = model.Update(tea.BackgroundColorMsg{Color: theme.background})
			model = updated.(app)

			rawLines := strings.Split(model.View().Content, "\n")
			plainLines := strings.Split(ansi.Strip(model.View().Content), "\n")
			if len(rawLines) != len(plainLines) || len(rawLines) < 3 {
				t.Fatalf("raw/plain line count = %d/%d", len(rawLines), len(plainLines))
			}

			accentRGB := colorRGB(model.styles.palette.accent)
			goldRGB := colorRGB(model.styles.palette.gold)
			if got := strings.TrimSpace(ansiTextMatching(rawLines[0], func(state ansiStyleState) bool {
				return state.hasForeground && state.foreground == goldRGB
			})); got != client.Context {
				t.Fatalf("active rail gold text = %q, want %q", got, client.Context)
			}

			accentedBodyLines := 0
			for index := 1; index < len(rawLines)-1; index++ {
				accented := ansiTextMatching(rawLines[index], func(state ansiStyleState) bool {
					return state.hasForeground && state.foreground == accentRGB
				})
				if accented == "" {
					continue
				}
				accentedBodyLines++
				if !strings.Contains(plainLines[index], "alpha") {
					t.Fatalf("accent appears outside selected row: %q", plainLines[index])
				}
				if accented != model.styles.glyphs.cursorMarker {
					t.Fatalf("selected accent text = %q, want cursor glyph only", accented)
				}
			}
			if accentedBodyLines != 1 {
				t.Fatalf("accented body lines = %d, want selected row only\n%s", accentedBodyLines, strings.Join(plainLines, "\n"))
			}

			betaLine := -1
			for index, line := range plainLines {
				if strings.Contains(line, "[C] beta") {
					betaLine = index
					break
				}
			}
			if betaLine < 0 {
				t.Fatalf("unselected ConfigMap row missing:\n%s", strings.Join(plainLines, "\n"))
			}
			boldText := ansiTextMatching(rawLines[betaLine], func(state ansiStyleState) bool { return state.bold })
			if boldText != "[C]" {
				t.Fatalf("unselected bold text = %q, want kind badge only", boldText)
			}
			if accented := ansiTextMatching(rawLines[betaLine], func(state ansiStyleState) bool {
				return state.hasForeground && state.foreground == accentRGB
			}); accented != "" {
				t.Fatalf("unselected row carries accent text %q", accented)
			}
		})
	}
}

func TestAppChromeMatrixAcrossRealScreens(t *testing.T) {
	scenarios := []struct {
		name, bodyText, title, hints, escapeHint     string
		wantsEsc, overlay, escPops, escClosesOverlay bool
	}{
		{
			name: "namespace", bodyText: "ns: default", title: "namespaces",
			hints: "enter open  a all-ns  w workloads  / filter  Q quit  ? help",
		},
		{
			name: "resource", bodyText: "[S] resource-", title: "namespace-with-a-deliberately-long-name",
			hints:      "enter keys  N new  D delete  r consumers  L link  t type  / filter  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "key", bodyText: "[S] Secret", title: "resource-with-a-deliberately-long-name",
			hints:      "enter edit  e edit all  N new  D delete  i import  x export  / filter  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "edit", bodyText: "Save key", title: "resource-with-a-deliberately-long-name/key-with-a-deliberately-long-name (edit)",
			hints:      "Y save  e re-edit  up/down scroll  w wrap  esc abort",
			escapeHint: "esc abort", wantsEsc: true, escPops: true,
		},
		{
			name: "project", bodyText: "context:", title: "project-with-a-deliberately-long-name",
			hints:      "enter open  s scan  u unlink  e edit  / filter  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "deep stack overlay", bodyText: "keys", title: "resource-with-a-deliberately-long-name",
			hints:      "enter edit  e edit all  N new  D delete  i import  x export  / filter  ? help",
			escapeHint: "esc close", overlay: true, escClosesOverlay: true,
		},
		{
			name: "search", bodyText: "search: resource", title: "search",
			hints:      "enter open  ctrl+r rescan  esc back",
			escapeHint: "esc back", wantsEsc: true, escPops: true,
		},
		{
			name: "workload", bodyText: "Deployment  workload-00", title: "namespace-with-a-deliberately-long-name workloads",
			hints:      "enter refs  L link  / filter  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "workload refs", bodyText: "[S] referenced-secret-00", title: "Deployment/workload-00",
			hints:      "enter open  L link  / filter  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "consumers", bodyText: "Consumers of Secret", title: "Consumers of Secret namespace-with-a-deliberately-long-name/resource-with-a-deliberately-long-name",
			hints:      "/ filter  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "value", bodyText: "visible value line", title: "resource-with-a-deliberately-long-name/key-with-a-deliberately-long-name",
			hints:      "up/down scroll  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "hex", bodyText: "00000000", title: "resource-with-a-deliberately-long-name/binary-key-with-a-deliberately-long-name (hex)",
			hints:      "up/down scroll  ? help",
			escapeHint: "esc back", escPops: true,
		},
		{
			name: "create prompt", bodyText: "> Secret", title: "namespace-with-a-deliberately-long-name (new)",
			hints:      "enter select  esc back",
			escapeHint: "esc back", wantsEsc: true, escPops: true,
		},
		{
			name: "delete confirm", bodyText: "confirm:", title: "resource-with-a-deliberately-long-name (delete)",
			hints:      "enter delete  esc cancel",
			escapeHint: "esc cancel", wantsEsc: true, escPops: true,
		},
		{
			name: "project context confirm", bodyText: "Switch project context?", title: "confirm context",
			hints:      "Y switch  A always  esc cancel",
			escapeHint: "esc cancel", wantsEsc: true, escPops: true,
		},
		{
			name: "suggestion", bodyText: "suggestions for project-with-a-deliberately-long-name", title: "scan",
			hints:      "enter link  / filter  esc cancel  ? help",
			escapeHint: "esc cancel", wantsEsc: true, escPops: true,
		},
	}
	sizes := []struct {
		name          string
		width, height int
	}{
		{name: "minimum", width: 60, height: 15},
		{name: "normal", width: 80, height: 24},
	}
	for _, scenario := range scenarios {
		for _, size := range sizes {
			t.Run(scenario.name+"/"+size.name, func(t *testing.T) {
				model := realScreenChromeApp(t, scenario.name)
				updated, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				model = updated.(app)
				if scenario.overlay {
					updated, _ = model.Update(key("?"))
					model = updated.(app)
					if model.overlay == nil {
						t.Fatal("help overlay did not open from the deep stack")
					}
				}

				top := model.stack[len(model.stack)-1]
				if got := top.Title(); got != scenario.title {
					t.Fatalf("Title() = %q, want %q", got, scenario.title)
				}
				hints := plainFooter(t, top, 1)
				if got := hints; got != scenario.hints {
					t.Fatalf("Hints() = %q, want %q", got, scenario.hints)
				}
				if got := top.WantsEsc(); got != scenario.wantsEsc {
					t.Fatalf("WantsEsc() = %t, want %t", got, scenario.wantsEsc)
				}
				if width := ansi.StringWidth(hints); width > 78 {
					t.Fatalf("hint width = %d, want <= 78: %q", width, hints)
				}
				for _, char := range hints {
					if char > 127 {
						t.Fatalf("hints contain non-ASCII character %q: %q", char, hints)
					}
				}

				plain := ansi.Strip(model.View().Content)
				lines := strings.Split(plain, "\n")
				if len(lines) != size.height {
					t.Fatalf("view height = %d, want %d:\n%s", len(lines), size.height, plain)
				}
				for lineNumber, line := range lines {
					if width := ansi.StringWidth(line); width > size.width {
						t.Fatalf("line %d width = %d, want <= %d: %q", lineNumber+1, width, size.width, line)
					}
				}

				header, body, footer := lines[0], strings.Join(lines[1:len(lines)-1], "\n"), lines[len(lines)-1]
				segments := chromeSegments(header)
				if len(segments) < 3 {
					t.Fatalf("header segments = %q, want context, server, and leaf: %q", segments, header)
				}
				contextSegment, serverSegment := segments[0], segments[1]
				if want := middleElideLine(model.client.Context, lipgloss.Width(contextSegment), model.glyphs.ellipsis); contextSegment != want {
					t.Fatalf("header context segment = %q, want %q: %q", contextSegment, want, header)
				}
				if serverSegment != model.client.Server {
					if want := compactServer(model.client.Server, lipgloss.Width(serverSegment), model.glyphs.ellipsis); serverSegment != want {
						t.Fatalf("header server segment = %q, want %q: %q", serverSegment, want, header)
					}
				}
				leafTarget, leafSuffix := chromeLeafParts(top.Title())
				renderedTarget, renderedSuffix := chromeLeafParts(segments[len(segments)-1])
				if renderedSuffix != leafSuffix {
					t.Fatalf("header leaf suffix = %q, want %q: %q", renderedSuffix, leafSuffix, header)
				}
				if want := middleElideLine(leafTarget, lipgloss.Width(renderedTarget), model.glyphs.ellipsis); renderedTarget != want {
					t.Fatalf("header leaf target = %q, want %q: %q", renderedTarget, want, header)
				}
				bodyText := strings.NewReplacer("[S]", model.glyphs.secretBadge, "> ", model.glyphs.cursorMarker+" ").Replace(scenario.bodyText)
				if !strings.Contains(body, bodyText) {
					t.Fatalf("body missing screen marker %q:\n%s", bodyText, body)
				}
				if scenario.escapeHint != "" && !strings.Contains(footer, scenario.escapeHint) {
					t.Fatalf("footer missing %q: %q", scenario.escapeHint, footer)
				}
				if width := ansi.StringWidth(header); width != size.width {
					t.Fatalf("header width = %d, want %d: %q", width, size.width, header)
				}
				if width := ansi.StringWidth(footer); width != size.width {
					t.Fatalf("footer width = %d, want %d: %q", width, size.width, footer)
				}

				beforeDepth := len(model.stack)
				escapeHarness := &harness{t: t, m: model}
				escapeHarness.keys("esc")
				escaped := escapeHarness.m.(app)
				switch {
				case scenario.escClosesOverlay:
					if escaped.overlay != nil || len(escaped.stack) != beforeDepth {
						t.Fatalf("esc overlay result = overlay %T stack depth %d, want closed overlay and depth %d", escaped.overlay, len(escaped.stack), beforeDepth)
					}
				case scenario.escPops:
					if len(escaped.stack) != beforeDepth-1 {
						t.Fatalf("esc stack depth = %d, want %d", len(escaped.stack), beforeDepth-1)
					}
				default:
					if len(escaped.stack) != beforeDepth || escaped.overlay != model.overlay {
						t.Fatalf("esc changed root state: stack %d -> %d overlay %T -> %T", beforeDepth, len(escaped.stack), model.overlay, escaped.overlay)
					}
				}
			})
		}
	}
}

func realScreenChromeApp(t *testing.T, scenario string) app {
	t.Helper()
	client := testClient()
	client.Context = "production-west"
	client.Server = "https://api.production.example:6443"
	model := newApp(t.Context(), Options{Client: client})
	root := model.stack[0].(*namespaceScreen)
	root.names = []string{"namespace-with-a-deliberately-long-name", "staging"}
	root.loadComplete = true
	_ = root.setItems()

	namespace := "namespace-with-a-deliberately-long-name"
	resourceName := "resource-with-a-deliberately-long-name"
	keyName := "key-with-a-deliberately-long-name"
	binaryKeyName := "binary-key-with-a-deliberately-long-name"
	binaryValue := make([]byte, 16*30)
	for index := range binaryValue {
		binaryValue[index] = byte(index)
	}
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
		Data: map[string][]byte{
			keyName:       []byte(strings.Repeat("visible value line\n", 30)),
			binaryKeyName: binaryValue,
		},
	})
	resources := newResourceScreen(t.Context(), client, namespace, model.editEnv, model.styles)
	resources.all = []k8s.Resource{resource}
	resources.loadComplete = true
	_ = resources.setVisibleItems()
	keys := newKeyScreen(t.Context(), client, k8s.KindSecret, namespace, resourceName, model.editEnv, model.styles)
	keys.resource = resource
	keys.loadComplete = true
	_ = keys.setItems()
	flow := newEditFlow(t.Context(), client, model.editEnv, resource, keyName, []byte("after"), model.styles)

	projectRecord := store.Project{
		Name:        "project-with-a-deliberately-long-name",
		RootPath:    "/workspace/project-with-a-deliberately-long-name",
		KubeContext: client.Context,
		KubeServer:  client.Server,
		Namespace:   namespace,
	}
	projectView := newProjectScreen(t.Context(), client, nil, "", projectRecord, "", scanConfig{}, model.editEnv, model.styles)
	projectView.ctxState = projectCtxActive
	for index := range 30 {
		link := store.ResourceLink{
			Kind:      k8s.KindSecret,
			Namespace: namespace,
			Name:      fmt.Sprintf("linked-resource-%02d", index),
			Source:    store.SourceManual,
		}
		projectView.resources = append(projectView.resources, link)
	}
	_ = projectView.setItems()

	search := newSearchScreen(t.Context(), client, model.editEnv, model.styles)
	search.input.SetValue("resource")
	search.namespaces = []string{namespace}
	for index := range 30 {
		entry := searchEntry{namespace: namespace, kind: k8s.KindSecret, name: fmt.Sprintf("resource-%02d", index), keys: []string{keyName}}
		search.entries = append(search.entries, entry)
	}
	_ = search.recompute()

	workloads := newWorkloadScreen(t.Context(), client, namespace, model.editEnv, model.styles)
	workloads.index = k8s.NewRefIndex()
	for index := range 30 {
		workloads.index.AddWorkload(workloadInNamespace(
			k8s.KindDeployment, fmt.Sprintf("workload-%02d", index), namespace, "1/1 ready", resourceName, k8s.TagEnv, k8s.KindSecret,
		))
	}
	workloads.complete = true
	_ = workloads.setItems()

	refRows := make([]refRow, 30)
	for index := range refRows {
		refRows[index] = refRow{ref: k8s.ResourceRef{
			Kind: k8s.KindSecret, Name: fmt.Sprintf("referenced-secret-%02d", index), Tags: []k8s.RefTag{k8s.TagEnv},
		}}
	}
	workloadRefs := newWorkloadRefsScreen(t.Context(), client, namespace, refRows, k8s.KindDeployment, "workload-00", model.editEnv, model.styles)
	_ = workloadRefs.list.SetItems(workloadRefs.items)

	consumers := newConsumersScreen(t.Context(), client, k8s.KindSecret, namespace, resourceName, model.editEnv, model.styles)
	consumers.index = k8s.NewRefIndex()
	for index := range 30 {
		consumers.index.AddWorkload(workloadInNamespace(
			k8s.KindDeployment, fmt.Sprintf("consumer-%02d", index), namespace, "1/1 ready", resourceName, k8s.TagEnv, k8s.KindSecret,
		))
	}
	consumers.complete = true
	_ = consumers.setItems()

	value := newValueScreen(resource, keyName, editEnv{}, model.styles)
	hex := newHexScreen(resource, binaryKeyName, editEnv{}, model.styles)
	create := newCreatePrompt(t.Context(), client, model.editEnv, namespace, []k8s.Resource{resource}, model.styles)
	deletePrompt := newDeleteConfirm(t.Context(), client, k8s.KindSecret, namespace, resourceName, model.styles)
	deletePrompt.res = resource
	deletePrompt.radiusSummary = summarizeBlastRadius(k8s.NewRefIndex(), k8s.KindSecret, resourceName)
	confirmProject := store.Project{
		Name: "context-project", KubeContext: "project-context", KubeServer: "https://project.example", Namespace: namespace,
	}
	contextConfirm := newProjectContextConfirm(
		t.Context(), nil, confirmProject, client,
		k8s.ContextInfo{Name: confirmProject.KubeContext, Server: confirmProject.KubeServer}, "", nil, model.styles,
	)
	suggestions := newSuggestionScreen(t.Context(), client, nil, projectRecord, scanConfig{}, false, model.editEnv, model.styles)
	suggestions.scanned = true
	for index := range 30 {
		suggestions.rows = append(suggestions.rows, suggestionRow{
			sug: project.Suggestion{
				Kind: k8s.KindSecret, Name: fmt.Sprintf("suggested-secret-%02d", index), File: "deploy.yaml", Line: index + 1, Mode: project.ModeManifest,
			},
			ns: namespace,
		})
	}
	_ = suggestions.setItems()

	switch scenario {
	case "namespace":
		model.stack = []screen{root}
	case "resource":
		model.stack = []screen{root, resources}
	case "key", "deep stack overlay":
		model.stack = []screen{root, resources, keys}
	case "edit":
		model.stack = []screen{root, resources, keys, flow}
	case "project":
		model.stack = []screen{root, projectView}
	case "search":
		model.stack = []screen{root, resources, keys, search}
	case "workload":
		model.stack = []screen{root, workloads}
	case "workload refs":
		model.stack = []screen{root, workloads, workloadRefs}
	case "consumers":
		model.stack = []screen{root, resources, keys, consumers}
	case "value":
		model.stack = []screen{root, resources, keys, value}
	case "hex":
		model.stack = []screen{root, resources, keys, hex}
	case "create prompt":
		model.stack = []screen{root, resources, create}
	case "delete confirm":
		model.stack = []screen{root, resources, deletePrompt}
	case "project context confirm":
		model.stack = []screen{root, projectView, contextConfirm}
	case "suggestion":
		model.stack = []screen{root, projectView, suggestions}
	default:
		t.Fatalf("unknown chrome scenario %q", scenario)
	}
	return model
}

func TestChromePreservesIdentityAndLeafAcrossStackShapes(t *testing.T) {
	stacks := []struct {
		name   string
		titles []string
		leaf   string
	}{
		{
			name:   "normal stack",
			titles: []string{"namespaces", "default", "item", "item/key-name (hex)"},
			leaf:   "item/key-name (hex)",
		},
		{
			name:   "project stack",
			titles: []string{"namespaces", "api", "scan", "item/key-name (conflict)"},
			leaf:   "item/key-name (conflict)",
		},
	}
	for _, stack := range stacks {
		for _, width := range []int{60, 80, 100} {
			t.Run(fmt.Sprintf("%s/%d", stack.name, width), func(t *testing.T) {
				client := testClient()
				model := newChromeTestApp(t, client, width, stack.titles,
					"enter edit  e edit all  N new  D delete  i import  x export  / filter  ? help",
				)
				header, footer := chromeLines(t, model)

				for _, want := range []string{client.Context, serverHost(client.Server), stack.leaf} {
					if !strings.Contains(header, want) {
						t.Fatalf("header missing %q: %q", want, header)
					}
				}
				if got := ansi.StringWidth(header); got != width {
					t.Fatalf("header width = %d, want %d: %q", got, width, header)
				}
				for _, want := range []string{"enter edit", "esc back", "? help"} {
					if !strings.Contains(footer, want) {
						t.Fatalf("footer missing %q: %q", want, footer)
					}
				}
				if got := ansi.StringWidth(footer); got != width {
					t.Fatalf("footer width = %d, want %d: %q", got, width, footer)
				}
				for _, char := range footer {
					if char > 127 {
						t.Fatalf("footer contains non-ASCII character %q: %q", char, footer)
					}
				}
			})
		}
	}
}

func TestChromeRailFitMatrix(t *testing.T) {
	const leaf = "resource/key-name (conflict)"
	stacks := []struct {
		name      string
		ancestors []string
		fullTrail string
	}{
		{name: "normal stack", ancestors: []string{"namespaces", "default", "item"}, fullTrail: "namespaces > default > item"},
		{name: "project stack", ancestors: []string{"namespaces", "api", "scan"}, fullTrail: "namespaces > api > scan"},
	}
	modes := []struct {
		name       string
		ascii      bool
		ellipsis   string
		narrowLeaf string
	}{
		{name: "Unicode", ellipsis: "…", narrowLeaf: "resourc…ey-name (conflict)"},
		{name: "ASCII", ascii: true, ellipsis: "...", narrowLeaf: "resour...y-name (conflict)"},
	}
	for _, mode := range modes {
		for _, stack := range stacks {
			for _, width := range []int{60, 72, 80, 100} {
				t.Run(fmt.Sprintf("%s/%s/%d", mode.name, stack.name, width), func(t *testing.T) {
					client := testClient()
					titles := append(append([]string(nil), stack.ancestors...), leaf)
					model := newChromeTestAppMode(t, client, width, titles, "enter open  ? help", mode.ascii)
					header, _ := chromeLines(t, model)

					want := []string{client.Context, client.Server}
					switch width {
					case 100:
						want = append(want, stack.fullTrail)
					case 80:
						want = append(want, mode.ellipsis+" > "+stack.ancestors[len(stack.ancestors)-1])
					case 72:
						if mode.ascii {
							want = append(want, mode.ellipsis)
						} else {
							want = append(want, mode.ellipsis+" > "+stack.ancestors[len(stack.ancestors)-1])
						}
					}
					wantLeaf := leaf
					if width == minimumWidth {
						wantLeaf = mode.narrowLeaf
					}
					want = append(want, wantLeaf)
					if got := chromeSegments(header); fmt.Sprint(got) != fmt.Sprint(want) {
						t.Fatalf("header segments = %q, want %q: %q", got, want, header)
					}
					if strings.Contains(header, "sk64") {
						t.Fatalf("header retained retired wordmark: %q", header)
					}
					if got := lipgloss.Width(header); got != width {
						t.Fatalf("header width = %d, want %d: %q", got, width, header)
					}
				})
			}
		}
	}
}

func TestChromeFitPreservesSegmentPriority(t *testing.T) {
	const (
		contextName = "production-west"
		server      = "https://gateway.example/clusters/production"
		leaf        = "resource-with-long-name/key-with-long-name (conflict)"
	)
	withoutTrail := chromeRail{context: contextName, server: server, leaf: leaf}
	tests := []struct {
		name        string
		width       int
		wantContext string
		wantServer  string
		wantLeaf    string
	}{
		{
			name:        "trail drops before protected segments",
			width:       withoutTrail.width(),
			wantContext: contextName,
			wantServer:  server,
			wantLeaf:    leaf,
		},
		{
			name:        "leaf target shrinks before identities",
			width:       100,
			wantContext: contextName,
			wantServer:  server,
			wantLeaf:    "resource-wi...h-long-name (conflict)",
		},
		{
			name:        "leaf target fully elides before server",
			width:       78,
			wantContext: contextName,
			wantServer:  server,
			wantLeaf:    "... (conflict)",
		},
		{
			name:        "server retains identity-bearing tail before context",
			width:       60,
			wantContext: contextName,
			wantServer:  "gateway.example/clu...ion",
			wantLeaf:    "... (conflict)",
		},
		{
			name:        "server identity elides before context",
			width:       40,
			wantContext: contextName,
			wantServer:  "h...n",
			wantLeaf:    "... (conflict)",
		},
		{
			name:        "context shrinks only after server is exhausted",
			width:       35,
			wantContext: "produ...west",
			wantServer:  "...",
			wantLeaf:    "... (conflict)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rail := chromeRail{context: contextName, server: server, trail: "...", leaf: leaf}
			rail.fit(test.width, "...")

			if rail.trail != "" {
				t.Fatalf("trail = %q, want dropped before protected segments", rail.trail)
			}
			if rail.context != test.wantContext || rail.server != test.wantServer || rail.leaf != test.wantLeaf {
				t.Fatalf("rail = context %q, server %q, leaf %q; want context %q, server %q, leaf %q",
					rail.context, rail.server, rail.leaf, test.wantContext, test.wantServer, test.wantLeaf)
			}
			if rail.context == "..." {
				t.Fatalf("context was reduced to a bare ellipsis: %#v", rail)
			}
			if !strings.HasSuffix(rail.leaf, " (conflict)") {
				t.Fatalf("leaf operation suffix was clipped: %q", rail.leaf)
			}
			if got := rail.width(); got > test.width {
				t.Fatalf("rail width = %d, want <= %d: %#v", got, test.width, rail)
			}
		})
	}
}

func TestDeleteConfirmationRailKeepsContextLegibleAtNarrowWidths(t *testing.T) {
	const (
		contextName = "test-ctx"
		server      = "https://gateway.example/clusters/production/a/very/long/apiserver/path"
	)
	st := testStyles(true)
	for _, width := range []int{60, 40} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			header := ansi.Strip(renderHeaderBar(
				st.header,
				st.activeContext,
				contextName,
				server,
				[]string{"namespaces", "default", "app-credentials (delete)"},
				width,
				"...",
			))
			segments := chromeSegments(header)
			if len(segments) != 3 {
				t.Fatalf("header segments = %q, want context, server, and leaf: %q", segments, header)
			}
			if segments[0] != contextName {
				t.Fatalf("context = %q, want intact %q: %q", segments[0], contextName, header)
			}
			if !strings.HasSuffix(segments[2], " (delete)") {
				t.Fatalf("delete suffix was clipped: %q", segments[2])
			}
			if got := lipgloss.Width(header); got != width {
				t.Fatalf("header width = %d, want %d: %q", got, width, header)
			}
		})
	}
}

func TestCompactServerSelectsLongestTruthfulFormThatFits(t *testing.T) {
	pathServer := "https://gateway.example:6443/clusters/production"
	tailFreeServer := "https://gateway.example:6443"
	noPortServer := "https://gateway.example"
	tests := []struct {
		name   string
		server string
		width  int
		want   string
	}{
		{name: "full path URL", server: pathServer, width: lipgloss.Width(pathServer), want: pathServer},
		{name: "elided path retains scheme and host", server: pathServer, width: 40, want: "https://gateway.example:6443/clus...tion"},
		{name: "path fallback retains URL ends", server: pathServer, width: 20, want: "https://g...oduction"},
		{name: "narrow path fallback retains URL ends", server: pathServer, width: 10, want: "http...ion"},
		{name: "query retained", server: "https://gateway.example?cluster=production", width: 35, want: "https://gateway.example?clus...tion"},
		{name: "empty query marker retained", server: tailFreeServer + "?", width: 20, want: "https://g...le:6443?"},
		{name: "fragment retained", server: "https://gateway.example#cluster=production", width: 35, want: "https://gateway.example#clus...tion"},
		{name: "full tail-free URL", server: tailFreeServer, width: lipgloss.Width(tailFreeServer), want: tailFreeServer},
		{name: "host and port fallback", server: tailFreeServer, width: 20, want: "gateway.example:6443"},
		{name: "named host port is visibly elided", server: tailFreeServer, width: 15, want: "gatewa...e:6443"},
		{name: "root path port is visibly elided", server: tailFreeServer + "/", width: 15, want: "gatewa...e:6443"},
		{name: "narrow IPv4 port is visibly elided", server: "https://10.0.0.5:6443", width: 10, want: "10.0...443"},
		{name: "narrow bracketed IPv6 port is visibly elided", server: "https://[2001:db8::1]:6443", width: 13, want: "[2001...:6443"},
		{name: "hostname fallback without port", server: noPortServer, width: 15, want: "gateway.example"},
		{name: "root path uses host without port", server: noPortServer + "/", width: 15, want: "gateway.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := compactServer(test.server, test.width, "...")
			if got != test.want {
				t.Fatalf("compactServer() = %q, want %q", got, test.want)
			}
			if gotWidth := lipgloss.Width(got); gotWidth > test.width {
				t.Fatalf("compactServer() width = %d, want <= %d: %q", gotWidth, test.width, got)
			}
		})
	}
}

func TestCompactServerMarksElidedExplicitPorts(t *testing.T) {
	servers := []string{
		"https://10.0.0.5:6443",
		"https://gateway.example:6443",
		"https://[2001:db8::1]:6443",
	}
	for _, server := range servers {
		t.Run(server, func(t *testing.T) {
			for width := lipgloss.Width("..."); width < lipgloss.Width(server); width++ {
				got := compactServer(server, width, "...")
				if !strings.Contains(got, ":6443") && !strings.Contains(got, "...") {
					t.Fatalf("compactServer() dropped the explicit port without marking elision at width %d: %q", width, got)
				}
				if gotWidth := lipgloss.Width(got); gotWidth > width {
					t.Fatalf("compactServer() width = %d, want <= %d: %q", gotWidth, width, got)
				}
			}
		})
	}
}

func TestRenderHeaderBarMarksElidedServerPortAtMinimumWidth(t *testing.T) {
	const (
		contextName = "production-context-west-longname"
		server      = "https://10.0.0.5:6443"
		leaf        = "resource-with-long-name/key-with-long-name (conflict)"
	)
	st := testStyles(true)
	header := ansi.Strip(renderHeaderBar(
		st.header,
		st.activeContext,
		contextName,
		server,
		[]string{"namespaces", leaf},
		minimumWidth,
		"...",
	))
	segments := chromeSegments(header)
	if len(segments) != 3 {
		t.Fatalf("header segments = %q, want context, server, and leaf: %q", segments, header)
	}
	if segments[0] != contextName {
		t.Fatalf("context = %q, want intact %q: %q", segments[0], contextName, header)
	}
	if segments[1] != "10....43" {
		t.Fatalf("server = %q, want marked port elision: %q", segments[1], header)
	}
	if !strings.HasSuffix(segments[2], " (conflict)") {
		t.Fatalf("operation suffix was clipped: %q", segments[2])
	}
	if lines := strings.Split(header, "\n"); len(lines) != 1 {
		t.Fatalf("header rows = %d, want 1: %q", len(lines), header)
	}
	if got := lipgloss.Width(header); got != minimumWidth {
		t.Fatalf("header width = %d, want %d: %q", got, minimumWidth, header)
	}
}

func TestCompactServerDistinguishesPathIdentitiesWheneverSuffixFits(t *testing.T) {
	production := "https://gateway.example:6443/clusters/production"
	staging := "https://gateway.example:6443/clusters/staging"
	for width := lipgloss.Width("h...g"); width <= lipgloss.Width(production); width++ {
		productionForm := compactServer(production, width, "...")
		stagingForm := compactServer(staging, width, "...")
		if productionForm == stagingForm {
			t.Fatalf("compactServer() collapsed distinct paths at width %d: %q", width, productionForm)
		}
		for _, rendered := range []struct {
			server string
			form   string
		}{
			{server: production, form: productionForm},
			{server: staging, form: stagingForm},
		} {
			if gotWidth := lipgloss.Width(rendered.form); gotWidth > width {
				t.Fatalf("compactServer(%q) width = %d, want <= %d: %q", rendered.server, gotWidth, width, rendered.form)
			}
		}
	}
}

func TestChromePreservesOperationSuffixesAtMinimumWidth(t *testing.T) {
	tests := []struct {
		name   string
		suffix string
	}{
		{name: "delete", suffix: " (delete)"},
		{name: "conflict", suffix: " (conflict)"},
		{name: "hex", suffix: " (hex)"},
	}
	for _, ascii := range []bool{false, true} {
		for _, test := range tests {
			t.Run(fmt.Sprintf("%s/ASCII=%t", test.name, ascii), func(t *testing.T) {
				client := testClient()
				client.Context = "production-west"
				client.Server = "https://gateway.example/clusters/production"
				leaf := "resource-with-long-name/key-with-long-name" + test.suffix
				model := newChromeTestAppMode(t, client, minimumWidth,
					[]string{"namespaces", "default", "resource", leaf},
					"enter open  ? help",
					ascii,
				)
				header, _ := chromeLines(t, model)
				segments := chromeSegments(header)
				if len(segments) != 3 {
					t.Fatalf("minimum-width segments = %q, want context, server, and leaf only: %q", segments, header)
				}
				if want := middleElideLine(client.Context, lipgloss.Width(segments[0]), model.glyphs.ellipsis); segments[0] != want {
					t.Fatalf("context segment = %q, want truthful form %q", segments[0], want)
				}
				if want := compactServer(client.Server, lipgloss.Width(segments[1]), model.glyphs.ellipsis); segments[1] != want {
					t.Fatalf("server segment = %q, want truthful form %q", segments[1], want)
				}
				if !strings.Contains(segments[1], serverHost(client.Server)) {
					t.Fatalf("minimum-width server lost host %q: %q", serverHost(client.Server), segments[1])
				}
				if !strings.HasSuffix(segments[2], test.suffix) {
					t.Fatalf("leaf emitted a clipped %q suffix: %q", test.suffix, segments[2])
				}
				if got := lipgloss.Width(header); got != minimumWidth {
					t.Fatalf("header width = %d, want %d: %q", got, minimumWidth, header)
				}
			})
		}
	}
}

func TestChromeLongParenthesizedProjectTitleKeepsOneRowHeader(t *testing.T) {
	title := "project-name (" + strings.Repeat("x", 60) + ")"
	for _, width := range []int{40, 60} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			st := testStyles(true)
			header := ansi.Strip(renderHeaderBar(
				st.header,
				st.activeContext,
				"ctx",
				"https://a.test",
				[]string{"namespaces", title},
				width,
				"...",
			))
			if lines := strings.Split(header, "\n"); len(lines) != 1 {
				t.Fatalf("header rows = %d, want 1:\n%s", len(lines), header)
			}
			if got := lipgloss.Width(header); got != width {
				t.Fatalf("header width = %d, want %d: %q", got, width, header)
			}
			if !strings.Contains(header, "project") {
				t.Fatalf("header lost project identity: %q", header)
			}
			if strings.Contains(header, strings.Repeat("x", 60)) {
				t.Fatalf("header retained the full user-controlled suffix: %q", header)
			}
		})
	}

	renderProject := func(name string) []string {
		client := testClient()
		project := store.Project{
			Name:        name,
			RootPath:    "/workspace/project",
			KubeContext: client.Context,
			KubeServer:  client.Server,
		}
		model := newApp(t.Context(), Options{Client: client, Project: &project})
		updated, _ := model.Update(tea.WindowSizeMsg{Width: minimumWidth, Height: minimumHeight})
		return strings.Split(ansi.Strip(updated.(app).View().Content), "\n")
	}
	shortView := renderProject("project-name")
	longView := renderProject(title)
	if len(longView) != len(shortView) {
		t.Fatalf("long-title view height = %d, want short-title height %d:\n%s", len(longView), len(shortView), strings.Join(longView, "\n"))
	}
	for lineNumber, line := range longView {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("line %d width = %d, want <= %d: %q", lineNumber+1, width, minimumWidth, line)
		}
	}
}

func TestChromeCompactsLongServerBeforeContext(t *testing.T) {
	client := testClient()
	client.Context = "prod-eu"
	client.Server = "https://api-server.production.example:6443/cluster"
	leaf := "resource-with-a-very-long-name/key-with-a-very-long-name (conflict)"
	model := newChromeTestApp(t, client, minimumWidth, []string{
		"namespaces",
		"namespace-with-a-very-long-name",
		"project-with-a-very-long-name",
		"resource-with-a-very-long-name",
		leaf,
	}, "enter open  ? help")

	header, _ := chromeLines(t, model)
	for _, want := range []string{"(conflict)", model.glyphs.ellipsis} {
		if !strings.Contains(header, want) {
			t.Fatalf("long-identity header missing %q: %q", want, header)
		}
	}
	segments := chromeSegments(header)
	if len(segments) < 3 {
		t.Fatalf("header segments = %q, want context, server, and leaf", segments)
	}
	if segments[0] != client.Context {
		t.Fatalf("context = %q, want intact %q before server shrink", segments[0], client.Context)
	}
	leafTarget, leafSuffix := chromeLeafParts(segments[len(segments)-1])
	if leafTarget != model.glyphs.ellipsis || leafSuffix != " (conflict)" {
		t.Fatalf("leaf = target %q, suffix %q; want fully elided target and intact suffix", leafTarget, leafSuffix)
	}
	serverSegment := segments[1]
	if want := compactServer(client.Server, lipgloss.Width(serverSegment), model.glyphs.ellipsis); serverSegment != want {
		t.Fatalf("server segment = %q, want %q", serverSegment, want)
	}
	if strings.Contains(header, "sk64") || strings.Contains(header, client.Server) {
		t.Fatalf("long-identity header retained lower-priority chrome: %q", header)
	}
	if got := lipgloss.Width(header); got != minimumWidth {
		t.Fatalf("header width = %d, want %d: %q", got, minimumWidth, header)
	}
}

func TestChromeElisionUsesActiveGlyph(t *testing.T) {
	for _, test := range []struct {
		name       string
		ascii      bool
		marker     string
		unexpected string
	}{
		{name: "unicode", marker: "…", unexpected: "..."},
		{name: "ASCII", ascii: true, marker: "...", unexpected: "…"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := testClient()
			client.Context = "production-context-with-a-long-name"
			client.Server = "https://api-server-with-a-long-name.production.example/cluster"
			model := newApp(t.Context(), Options{Client: client, ASCII: test.ascii})
			model.stack = []screen{
				&chromeTestScreen{title: "namespaces"},
				&chromeTestScreen{title: "namespace-with-a-long-name"},
				&chromeTestScreen{title: "resource-with-a-long-name/key-with-a-long-name (conflict)"},
			}
			model.width, model.height = minimumWidth, minimumHeight

			header, _ := chromeLines(t, model)
			if !strings.Contains(header, test.marker) || strings.Contains(header, test.unexpected) {
				t.Fatalf("header elision = %q, want %q without %q", header, test.marker, test.unexpected)
			}
			if width := ansi.StringWidth(header); width != minimumWidth {
				t.Fatalf("header width = %d, want %d: %q", width, minimumWidth, header)
			}
		})
	}
}

func TestNamespaceIdentityAppearsOnceAndPrioritizesActionableFields(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		cluster     string
		wantLine    string
		wantCluster bool
	}{
		{
			name:     "long alias drops before namespace and project",
			width:    60,
			cluster:  strings.Repeat("cluster-alias-", 8),
			wantLine: "ns: default  project: api",
		},
		{
			name:        "alias appears when it fits",
			width:       100,
			cluster:     "local-cluster",
			wantLine:    "ns: default  project: api  cluster: local-cluster",
			wantCluster: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient()
			client.Cluster = test.cluster
			model := newApp(t.Context(), Options{Client: client, ASCII: true})
			root := newNamespaceScreen(t.Context(), client, "api", model.editEnv, model.styles)
			root.SetSize(test.width, bodyHeight(15))
			model.stack = []screen{root}
			model.width, model.height = test.width, 15

			view := ansi.Strip(model.View().Content)
			lines := strings.Split(view, "\n")
			identityLine := strings.TrimRight(lines[1], " ")
			if count := strings.Count(view, client.Context); count != 1 {
				t.Fatalf("context occurrence count = %d, want 1:\n%s", count, view)
			}
			if !strings.Contains(lines[0], client.Server) {
				t.Fatalf("header missing API server %q: %q", client.Server, lines[0])
			}
			if identityLine != test.wantLine {
				t.Fatalf("namespace identity line = %q, want %q", identityLine, test.wantLine)
			}
			if strings.Contains(identityLine, "context:") {
				t.Fatalf("namespace identity repeats context: %q", identityLine)
			}
			if strings.Contains(identityLine, "cluster:") != test.wantCluster {
				t.Fatalf("namespace cluster visibility = %t, want %t: %q", strings.Contains(identityLine, "cluster:"), test.wantCluster, identityLine)
			}
			if got := ansi.StringWidth(lines[0]); got != test.width {
				t.Fatalf("header width = %d, want %d: %q", got, test.width, lines[0])
			}
			if got := ansi.StringWidth(lines[len(lines)-1]); got != test.width {
				t.Fatalf("footer width = %d, want %d: %q", got, test.width, lines[len(lines)-1])
			}
			if got := ansi.StringWidth(lines[1]); got > test.width {
				t.Fatalf("namespace identity width = %d, want <= %d: %q", got, test.width, lines[1])
			}
		})
	}
}

func TestOverlayKeepsDeepRailIdentity(t *testing.T) {
	client := testClient()
	leaf := "item/key-name (hex)"
	model := newChromeTestApp(t, client, minimumWidth,
		[]string{"namespaces", "default", "item", leaf},
		"enter open  ? help",
	)
	beforeHeader, _ := chromeLines(t, model)

	updatedModel, cmd := model.Update(key("?"))
	if cmd != nil {
		_ = cmd()
	}
	updated := updatedModel.(app)
	if updated.overlay == nil {
		t.Fatal("help key did not open an overlay")
	}
	header, footer := chromeLines(t, updated)
	if header != beforeHeader {
		t.Fatalf("overlay changed rail\n--- before ---\n%s\n--- after ---\n%s", beforeHeader, header)
	}
	for _, want := range []string{client.Context, serverHost(client.Server), leaf} {
		if !strings.Contains(header, want) {
			t.Fatalf("overlay header missing %q: %q", want, header)
		}
	}
	if !strings.Contains(footer, "esc close") {
		t.Fatalf("overlay footer missing esc close: %q", footer)
	}
	if got := ansi.StringWidth(header); got != minimumWidth {
		t.Fatalf("overlay header width = %d, want %d: %q", got, minimumWidth, header)
	}
	if got := ansi.StringWidth(footer); got != minimumWidth {
		t.Fatalf("overlay footer width = %d, want %d: %q", got, minimumWidth, footer)
	}
}

func newChromeTestApp(t *testing.T, client *k8s.Client, width int, titles []string, hints string) app {
	t.Helper()
	return newChromeTestAppMode(t, client, width, titles, hints, true)
}

func newChromeTestAppMode(t *testing.T, client *k8s.Client, width int, titles []string, hints string, ascii bool) app {
	t.Helper()
	model := newApp(t.Context(), Options{Client: client, ASCII: ascii})
	model.stack = make([]screen, len(titles))
	for i, title := range titles {
		model.stack[i] = &chromeTestScreen{title: title, hints: hints}
	}
	model.width, model.height = width, minimumHeight
	return model
}

func chromeSegments(header string) []string {
	parts := strings.Split(strings.TrimSpace(header), chromeGap)
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if segment := strings.TrimSpace(part); segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

func chromeLines(t *testing.T, model app) (string, string) {
	t.Helper()
	lines := strings.Split(ansi.Strip(model.View().Content), "\n")
	if len(lines) < 2 {
		t.Fatalf("app view has %d lines, want at least 2: %q", len(lines), lines)
	}
	return lines[0], lines[len(lines)-1]
}

type ansiStyleState struct {
	foreground    [3]uint8
	hasForeground bool
	bold          bool
}

func colorRGB(value color.Color) [3]uint8 {
	rgba := color.RGBAModel.Convert(value).(color.RGBA)
	return [3]uint8{rgba.R, rgba.G, rgba.B}
}

func ansiTextMatching(value string, matches func(ansiStyleState) bool) string {
	var result strings.Builder
	state := ansiStyleState{}
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			if end := strings.IndexByte(value[index+2:], 'm'); end >= 0 {
				sequenceEnd := index + 2 + end
				applyANSIStyle(&state, value[index+2:sequenceEnd])
				index = sequenceEnd + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if matches(state) {
			result.WriteRune(r)
		}
		index += size
	}
	return result.String()
}

func ansiColorComponent(value string) (uint8, bool) {
	component, err := strconv.Atoi(value)
	if err != nil || component < 0 || component > 255 {
		return 0, false
	}
	return uint8(component), true
}

func applyANSIStyle(state *ansiStyleState, parameters string) {
	parts := strings.Split(parameters, ";")
	for index := 0; index < len(parts); index++ {
		code := 0
		if parts[index] != "" {
			parsed, err := strconv.Atoi(parts[index])
			if err != nil {
				continue
			}
			code = parsed
		}
		switch code {
		case 0:
			*state = ansiStyleState{}
		case 1:
			state.bold = true
		case 22:
			state.bold = false
		case 38:
			if index+4 >= len(parts) || parts[index+1] != "2" {
				continue
			}
			red, redOK := ansiColorComponent(parts[index+2])
			green, greenOK := ansiColorComponent(parts[index+3])
			blue, blueOK := ansiColorComponent(parts[index+4])
			if redOK && greenOK && blueOK {
				state.foreground = [3]uint8{red, green, blue}
				state.hasForeground = true
			}
			index += 4
		case 39:
			state.hasForeground = false
		}
	}
}

type chromeTestScreen struct {
	title string
	hints string
}

func (s *chromeTestScreen) Init() tea.Cmd                    { return nil }
func (s *chromeTestScreen) Update(tea.Msg) (screen, tea.Cmd) { return s, nil }
func (s *chromeTestScreen) View() string                     { return "body" }
func (s *chromeTestScreen) SetSize(int, int)                 {}
func (s *chromeTestScreen) SetStyles(*styles)                {}
func (s *chromeTestScreen) Title() string                    { return s.title }
func (s *chromeTestScreen) Hints() footerHints               { return testFooterHints(s.hints) }
func (s *chromeTestScreen) Help() helpGroup                  { return helpGroup{title: s.title} }
func (s *chromeTestScreen) CapturesInput() bool              { return false }
func (s *chromeTestScreen) WantsEsc() bool                   { return false }

func testFooterHints(text string) footerHints {
	if text == "" {
		return footerHints{}
	}
	parts := strings.Split(text, chromeGap)
	bindings := make([]bubbleskey.Binding, 0, len(parts))
	showHelp := false
	for _, part := range parts {
		label, desc, _ := strings.Cut(part, " ")
		if label == packageDefaultKeyMaps.global.Help.Help().Key {
			showHelp = true
			continue
		}
		bindings = append(bindings, bubbleskey.NewBinding(bubbleskey.WithKeys(label), bubbleskey.WithHelp(label, desc)))
	}
	hints := hintBindings(bindings...)
	hints.showHelp = showHelp
	return hints
}

func TestDebugLogRecordsWholeResourceEditHonestly(t *testing.T) {
	t.Cleanup(editor.CleanupAll)
	path := filepath.Join(t.TempDir(), "debug.log")
	logger, err := debuglog.Open(path)
	if err != nil {
		t.Fatalf("open debug log: %v", err)
	}
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"before": []byte("old")})
	h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true, Editor: "true", Debug: logger})
	h.keys("e")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "after: new\n")
	h.send(editorFinishedMsg{})
	if err := logger.Close(); err != nil {
		t.Fatalf("close debug log: %v", err)
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- path is created inside this test's temporary directory.
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	logged := string(contents)
	if !strings.Contains(logged, "op=edit-resource kind=Secret ns=default name=edit-target") {
		t.Fatalf("debug log missing whole-resource edit:\n%s", logged)
	}
	if strings.Contains(logged, "resource.yaml") {
		t.Fatalf("debug log claims a synthetic key was edited:\n%s", logged)
	}
}

// TestBackgroundColorUpdateLeavesPriorStylesImmutable guards the fix for a
// data race: asynchronous screen-constructor commands hold the styles pointer
// they captured, so a theme update must install a new object rather than
// overwrite the shared one. The goroutine stands in for an in-flight
// constructor reading the old snapshot while the event loop retheme runs.
func TestBackgroundColorUpdateLeavesPriorStylesImmutable(t *testing.T) {
	model := newApp(t.Context(), Options{Client: testClient()})
	before := model.styles
	beforeSpinner := before.spinnerStyle.GetForeground()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			_ = newSpinner(before)
		}
	}()
	current := tea.Model(model)
	for i := range 500 {
		background := color.Color(color.White)
		if i%2 == 0 {
			background = color.Black
		}
		current, _ = current.(app).Update(tea.BackgroundColorMsg{Color: background})
	}
	<-done

	updated := current.(app)
	if updated.styles == before {
		t.Fatal("theme update reused the shared styles object")
	}
	requireSameColor(t, before.spinnerStyle.GetForeground(), beforeSpinner)
}
