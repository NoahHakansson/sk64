package tui

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
)

const longPathPrefixedTestServer = "https://gateway.example/clusters/production/a/very/long/apiserver/path"

var update = flag.Bool("update", false, "regenerate golden files")

type harness struct {
	t       *testing.T
	m       tea.Model
	sawQuit bool
	ascii   bool
}

// newHarness drains Init before sizing the app. Synthetic results should use
// topReqID because request IDs are unique across all loaders in the process.
func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	t.Cleanup(editor.CleanupAll)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "config"))
	opts.Client = &k8s.Client{
		Clientset: fake.NewClientset(),
		Metadata:  newTestMetadataClient(t),
		Context:   "test-ctx",
		Namespace: "default",
		Server:    "https://test.example",
	}
	model := newApp(t.Context(), opts)
	model.quitArm.tick = func(uint64) tea.Cmd { return func() tea.Msg { return nil } }
	h := &harness{t: t, m: model, ascii: opts.ASCII}
	h.drain(h.m.Init())
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	return h
}

func newTestMetadataClient(t *testing.T, objects ...runtime.Object) *metadatafake.FakeMetadataClient {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatalf("add metadata types to scheme: %v", err)
	}
	return metadatafake.NewSimpleMetadataClient(scheme, objects...)
}

func assertLastListContinueToken(t *testing.T, client *k8s.Client, want string) {
	t.Helper()
	actions := client.Clientset.(*fake.Clientset).Actions()
	if len(actions) == 0 {
		t.Fatal("client recorded no actions")
	}
	listAction, ok := actions[len(actions)-1].(k8stesting.ListActionImpl)
	if !ok {
		t.Fatalf("last client action = %T, want testing.ListActionImpl", actions[len(actions)-1])
	}
	if got := listAction.GetListOptions().Continue; got != want {
		t.Fatalf("list continue token = %q, want %q", got, want)
	}
}

func (h *harness) topReqID() int {
	h.t.Helper()
	top := h.m.(app).stack[len(h.m.(app).stack)-1]
	switch top := top.(type) {
	case *namespaceScreen:
		return top.reqID
	case *resourceScreen:
		return top.reqID
	case *keyScreen:
		return top.reqID
	case *workloadScreen:
		return top.reqID
	case *consumersScreen:
		return top.reqID
	case *editFlow:
		return top.reqID
	case *projectScreen:
		return top.reqID
	case *projectFormScreen:
		return top.reqID
	case *suggestionScreen:
		return top.reqID
	case *searchScreen:
		return top.reqID
	case *deleteConfirm:
		return top.reqID
	default:
		h.t.Fatalf("top screen %T has no loader", top)
		return 0
	}
}

func (h *harness) send(msgs ...tea.Msg) {
	h.t.Helper()
	for _, msg := range msgs {
		msg = h.withCurrentStackGeneration(msg)
		model, cmd := h.m.Update(msg)
		h.m = model
		h.drain(cmd)
	}
}

func (h *harness) withCurrentStackGeneration(msg tea.Msg) tea.Msg {
	generation := h.m.(app).stackGeneration
	switch msg := msg.(type) {
	case pushScreenMsg:
		if msg.generation == 0 {
			msg.generation = generation
		}
		return msg
	case popScreenMsg:
		if msg.generation == 0 {
			msg.generation = generation
		}
		return msg
	case replaceScreenMsg:
		if msg.generation == 0 {
			msg.generation = generation
		}
		return msg
	case searchJumpMsg:
		if msg.generation == 0 {
			msg.generation = generation
		}
		return msg
	case openProjectPickerMsg:
		if msg.generation == 0 {
			msg.generation = generation
		}
		return msg
	default:
		return msg
	}
}

func (h *harness) keys(keys ...string) {
	h.t.Helper()
	for _, value := range keys {
		h.send(key(value))
	}
}

func (h *harness) view() string {
	h.t.Helper()
	lines := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	view := strings.Join(lines, "\n")
	if h.ascii {
		for index, value := range []byte(view) {
			if value > 0x7f {
				rendered, _ := utf8.DecodeRuneInString(view[index:])
				h.t.Fatalf("ASCII mode rendered non-ASCII rune %q at byte offset %d", rendered, index)
			}
		}
	}
	return view
}

func TestHarnessASCIIAssertionRejectsUnresolvedChromeGlyph(t *testing.T) {
	const childEnvironment = "SK64_TEST_UNRESOLVED_CHROME_GLYPH"
	if os.Getenv(childEnvironment) == "1" {
		model := newApp(t.Context(), Options{Client: testClient(), ASCII: true})
		model.stack = []screen{&chromeTestScreen{title: "screen" + unicodeSeparator + "detail"}}
		model.width, model.height = 80, 24
		_ = (&harness{t: t, m: model, ascii: true}).view()
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestHarnessASCIIAssertionRejectsUnresolvedChromeGlyph$") // #nosec G204,G702 -- executable is the current test binary and the argument is a constant literal.
	command.Env = append(os.Environ(), childEnvironment+"=1")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("ASCII harness accepted unresolved chrome glyph:\n%s", output)
	}
	if !strings.Contains(string(output), "ASCII mode rendered non-ASCII rune") {
		t.Fatalf("ASCII harness failed for an unexpected reason: %v\n%s", err, output)
	}
}

func (h *harness) golden(name string) {
	h.t.Helper()
	_, source, _, ok := goruntime.Caller(0)
	if !ok {
		h.t.Fatal("locate golden test source")
	}
	path := filepath.Join(filepath.Dir(source), "testdata", name+".golden")
	got := h.view()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			h.t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			h.t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- path is confined to testdata and names are test literals.
	if err != nil {
		h.t.Fatalf("read golden %s: %v", name, err)
	}
	if got != string(want) {
		h.t.Fatalf("view differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestMarkerScreenGlyphModesPreserveLayout(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, bool) *harness
	}{
		{name: "key list", setup: keyListAppearanceHarness},
		{name: "workload references", setup: workloadRefsAppearanceHarness},
		{name: "consumers", setup: consumersAppearanceHarness},
		{name: "project view", setup: projectViewAppearanceHarness},
		{name: "suggestions", setup: suggestionViewAppearanceHarness},
		{
			name: "rollout checklist",
			setup: func(t *testing.T, ascii bool) *harness {
				t.Helper()
				h, _, _ := rolloutOfferHarnessOptions(t, ascii)
				return h
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unicodeView := test.setup(t, false).view()
			asciiView := test.setup(t, true).view()
			unicodeLines := strings.Split(unicodeView, "\n")
			asciiLines := strings.Split(asciiView, "\n")
			if len(unicodeLines) != len(asciiLines) {
				t.Fatalf("line counts = unicode %d ASCII %d", len(unicodeLines), len(asciiLines))
			}
			assertLinesFitWidth(t, unicodeView, 80)
			assertLinesFitWidth(t, asciiView, 80)
			for index, value := range asciiView {
				if value > 0x7f {
					t.Fatalf("ASCII mode rendered non-ASCII rune %q at byte offset %d", value, index)
				}
			}
		})
	}
}

func TestThemeMarkersCoveredByUnicodeGoldens(t *testing.T) {
	if *update {
		t.Skip("marker coverage is checked after regeneration")
	}
	glyphs := newGlyphs(false)
	tests := []struct {
		name    string
		marker  string
		goldens []string
	}{
		{name: "cursor", marker: glyphs.cursorMarker, goldens: []string{"key_list_unicode.golden"}},
		{name: "rollout", marker: glyphs.rolloutMarker, goldens: []string{"workload_refs_unicode.golden", "project_view_unicode.golden"}},
		{name: "subPath", marker: glyphs.subPathMarker, goldens: []string{"workload_refs_unicode.golden", "project_view_unicode.golden"}},
		{name: "warning", marker: glyphs.warnMarker, goldens: []string{"rollout_checklist_unicode.golden"}},
		{name: "config error", marker: glyphs.errMarker, goldens: []string{"config_error_unicode.golden"}},
		{name: "wrap", marker: glyphs.wrapMarker, goldens: []string{"delete_key_wrap_on_unicode.golden"}},
		{name: "rule", marker: glyphs.ruleMarker, goldens: []string{"delete_key_wrap_on_unicode.golden"}},
		{name: "found", marker: glyphs.foundTag, goldens: []string{"suggestion_view_unicode.golden"}},
		{name: "not found", marker: glyphs.notFoundTag, goldens: []string{"suggestion_view_unicode.golden"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range test.goldens {
				contents, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- names are test literals confined to testdata.
				if err != nil {
					continue
				}
				if strings.Contains(string(contents), test.marker) {
					return
				}
			}
			t.Fatalf("marker %q is not exercised by Unicode goldens %v", test.marker, test.goldens)
		})
	}
}

func TestGoldenPromptsDoNotBracketCommitKeys(t *testing.T) {
	if *update {
		t.Skip("golden prompt rules are checked after regeneration")
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "*.golden"))
	if err != nil {
		t.Fatalf("find golden files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden files found")
	}

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(strings.TrimSuffix(name, filepath.Ext(name)), func(t *testing.T) {
			contents, err := os.ReadFile(path) // #nosec G304 -- path is confined to testdata by the glob above.
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			for _, bracketedKey := range []string{"[Y]", "[A]"} {
				if strings.Contains(string(contents), bracketedKey) {
					t.Fatalf("rendered prompt brackets commit key %q", bracketedKey)
				}
			}
		})
	}
}

func TestGoldenLinesFitAssignedViewWidth(t *testing.T) {
	if *update {
		t.Skip("golden widths are checked after regeneration")
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "*.golden"))
	if err != nil {
		t.Fatalf("find golden files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no golden files found")
	}

	viewWidths := map[string]int{
		"blast_radius_line.golden":             60,
		"config_error_narrow.golden":           60,
		"delete_confirm_min_size.golden":       60,
		"edit_flow_diff.golden":                60,
		"export_dir_picker.golden":             60,
		"export_name_prompt.golden":            60,
		"export_name_prompt_unicode.golden":    60,
		"import_prompt.golden":                 60,
		"project_overlay_list_60.golden":       60,
		"resource_list_100x30.golden":          100,
		"rollout_checklist_incomplete.golden":  60,
		"rollout_done_min_many_results.golden": 60,
		"terminal_too_small.golden":            40,
	}
	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(strings.TrimSuffix(name, filepath.Ext(name)), func(t *testing.T) {
			contents, err := os.ReadFile(path) // #nosec G304 -- path is confined to testdata by the glob above.
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			viewWidth := 80
			if assigned, ok := viewWidths[name]; ok {
				viewWidth = assigned
			}
			for lineNumber, line := range strings.Split(string(contents), "\n") {
				if width := lipgloss.Width(line); width > viewWidth {
					t.Fatalf("line %d width = %d, want <= %d: %q", lineNumber+1, width, viewWidth, line)
				}
			}
		})
	}
}

func (h *harness) drain(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, batched := range msg {
			h.drain(batched)
		}
	case tea.QuitMsg:
		h.sawQuit = true
	case list.FilterMatchesMsg, scopedListFilterMatchesMsg, pushScreenMsg, popScreenMsg, replaceScreenMsg, overlayCloseActionMsg, overlayClosedMsg, contextSwitchedMsg, openProjectPickerMsg, projectOpenedMsg, projectSavedMsg, projectBindingSavedMsg, projectContextProbedMsg, scanDoneMsg, suggestionCheckedMsg, prefixMatchMsg, suggestionLinkedMsg, scanLinksAppliedMsg, namespaceFallbackMsg, fatalMsg, deleteDoneMsg, resourceListChangedMsg, editSavedMsg, searchJumpMsg:
		h.send(msg)
	default:
		if msgType := reflect.TypeOf(msg); msgType != nil && msgType.PkgPath() == "charm.land/bubbles/v2/filepicker" {
			h.send(msg)
		}
	}
}

func TestTextInputsDisableVirtualCursorInTests(t *testing.T) {
	input := newTextInput(testStyles(false))
	if input.VirtualCursor() {
		t.Fatal("test text input uses a blinking virtual cursor")
	}
	_ = input.Focus()
	_, cmd := input.Update(key("x"))
	if cmd != nil {
		t.Fatal("test text input returned a cursor update command")
	}
}

func key(value string) tea.KeyPressMsg {
	special := map[string]rune{
		"enter": tea.KeyEnter,
		"esc":   tea.KeyEscape,
		"tab":   tea.KeyTab,
		"space": tea.KeySpace,
		"up":    tea.KeyUp,
		"down":  tea.KeyDown,
	}
	if code, ok := special[value]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	if strings.HasPrefix(value, "ctrl+") {
		runes := []rune(strings.TrimPrefix(value, "ctrl+"))
		if len(runes) == 1 {
			return tea.KeyPressMsg{Code: runes[0], Mod: tea.ModCtrl}
		}
	}
	if utf8.RuneCountInString(value) == 1 {
		code, _ := utf8.DecodeRuneInString(value)
		return tea.KeyPressMsg{Code: code, Text: value}
	}
	panic("unsupported key: " + value)
}
