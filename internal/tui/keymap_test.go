package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	"github.com/NoahHakansson/sk64/internal/config"
	"github.com/NoahHakansson/sk64/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestApplyKeybinds(t *testing.T) {
	tests := []struct {
		action  config.Action
		keys    []string
		targets []string
	}{
		{action: config.ActionUp, keys: []string{"ctrl+1"}, targets: []string{"list.CursorUp", "viewport.Up", "filePicker.Up", "createPrompt.Up", "editFlow.RolloutUp"}},
		{action: config.ActionDown, keys: []string{"ctrl+2"}, targets: []string{"list.CursorDown", "viewport.Down", "filePicker.Down", "createPrompt.Down", "editFlow.RolloutDown"}},
		{action: config.ActionTop, keys: []string{"ctrl+3"}, targets: []string{"list.GoToStart", "filePicker.GoToTop"}},
		{action: config.ActionBottom, keys: []string{"ctrl+4"}, targets: []string{"list.GoToEnd", "filePicker.GoToLast"}},
		{action: config.ActionPageUp, keys: []string{"ctrl+5"}, targets: []string{"list.PrevPage", "viewport.PageUp", "filePicker.PageUp"}},
		{action: config.ActionPageDown, keys: []string{"ctrl+6"}, targets: []string{"list.NextPage", "viewport.PageDown", "filePicker.PageDown"}},
		{action: config.ActionHalfPageUp, keys: []string{"ctrl+7"}, targets: []string{"viewport.HalfPageUp"}},
		{action: config.ActionHalfPageDown, keys: []string{"ctrl+8"}, targets: []string{"viewport.HalfPageDown"}},
		{action: config.ActionRefresh, keys: []string{"ctrl+9"}, targets: []string{"namespace.Refresh", "resource.Refresh", "keyScreen.Refresh", "workload.Refresh", "consumers.Refresh", "project.Refresh", "suggestion.Refresh"}},
		{action: config.ActionFilter, keys: []string{"alt+1"}, targets: []string{"global.Filter", "list.Filter"}},
		{action: config.ActionAllNamespaces, keys: []string{"alt+2"}, targets: []string{"namespace.AllNamespaces", "resource.AllNamespaces"}},
		{action: config.ActionTypeCycle, keys: []string{"alt+3"}, targets: []string{"resource.TypeCycle"}},
		{action: config.ActionValues, keys: []string{"alt+4"}, targets: []string{"keyScreen.ValueSearch"}},
		{action: config.ActionWrap, keys: []string{"alt+5"}, targets: []string{"editFlow.Wrap"}},
		{action: config.ActionHelp, keys: []string{"alt+6"}, targets: []string{"global.Help"}},
		{action: config.ActionQuit, keys: []string{"alt+7"}, targets: []string{"global.Quit"}},
	}

	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			km := defaultKeyMaps()
			before := bindingKeys(testBindingRefs(km))
			applyKeybinds(km, config.Overrides{test.action: test.keys})
			bindings := testBindingRefs(km)
			targets := make(map[string]bool, len(test.targets))
			for _, name := range test.targets {
				targets[name] = true
				binding, found := bindings[name]
				if !found {
					t.Fatalf("unknown target %s", name)
				}
				if !reflect.DeepEqual(binding.Keys(), test.keys) {
					t.Errorf("%s keys = %v, want %v", name, binding.Keys(), test.keys)
				}
				wantHelp := test.keys[0]
				switch test.action {
				case config.ActionUp:
					wantHelp += "/down"
					if strings.HasPrefix(name, "filePicker.") {
						wantHelp = test.keys[0] + "/j"
					}
				case config.ActionDown:
					wantHelp = "up/" + wantHelp
					if strings.HasPrefix(name, "filePicker.") {
						wantHelp = "k/" + test.keys[0]
					}
				}
				if got := binding.Help().Key; got != wantHelp {
					t.Errorf("%s help key = %q, want %q", name, got, wantHelp)
				}
			}
			for name, binding := range bindings {
				if targets[name] {
					continue
				}
				if name == "helpOverlay.Close" && (test.action == config.ActionHelp || test.action == config.ActionQuit) {
					continue
				}
				if got := binding.Keys(); !reflect.DeepEqual(got, before[name]) {
					t.Errorf("unmapped %s keys = %v, want %v", name, got, before[name])
				}
			}
		})
	}
}

func TestApplyKeybindsEmptyIsNoOp(t *testing.T) {
	for _, overrides := range []config.Overrides{nil, {}} {
		got := defaultKeyMaps()
		want := defaultKeyMaps()
		applyKeybinds(got, overrides)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("applyKeybinds(%v) changed defaults", overrides)
		}
	}
}

func TestApplyKeybindsPreservesDisabledState(t *testing.T) {
	km := defaultKeyMaps()
	km.resource.TypeCycle.SetEnabled(false)
	applyKeybinds(km, config.Overrides{config.ActionTypeCycle: {"ctrl+t"}})
	if km.resource.TypeCycle.Enabled() {
		t.Fatal("rebinding re-enabled TypeCycle")
	}
}

func TestApplyKeybindsRebuildsHelpOverlayClose(t *testing.T) {
	km := defaultKeyMaps()
	applyKeybinds(km, config.Overrides{
		config.ActionHelp: {"ctrl+h", "f1"},
		config.ActionQuit: {"ctrl+q"},
	})
	want := []string{"esc", "ctrl+h", "f1", "ctrl+q"}
	if got := km.helpOverlay.Close.Keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Close keys = %v, want %v", got, want)
	}
	if got := km.helpOverlay.Close.Help().Key; got != "esc" {
		t.Fatalf("Close help key = %q, want esc", got)
	}
}

func TestApplyKeybindsMovementHelp(t *testing.T) {
	tests := []struct {
		name         string
		overrides    config.Overrides
		want         string
		wantViewport string
	}{
		{name: "non-navigation", overrides: config.Overrides{config.ActionRefresh: {"ctrl+e"}}, want: "h/j/k/l", wantViewport: "up/down"},
		{name: "up", overrides: config.Overrides{config.ActionUp: {"ctrl+e"}}, want: "ctrl+e/down", wantViewport: "ctrl+e/down"},
		{name: "down", overrides: config.Overrides{config.ActionDown: {"ctrl+y"}}, want: "up/ctrl+y", wantViewport: "up/ctrl+y"},
		{name: "other navigation", overrides: config.Overrides{config.ActionTop: {"home"}}, want: "up/down", wantViewport: "up/down"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			km := defaultKeyMaps()
			applyKeybinds(km, test.overrides)
			if km.movementHelp != test.want {
				t.Fatalf("movementHelp = %q, want %q", km.movementHelp, test.want)
			}
			if km.viewportMovementHelp != test.wantViewport {
				t.Fatalf("viewportMovementHelp = %q, want %q", km.viewportMovementHelp, test.wantViewport)
			}
		})
	}
}

func TestReboundLabelsReachViewportAndValueHelp(t *testing.T) {
	km := defaultKeyMaps()
	applyKeybinds(km, config.Overrides{
		config.ActionUp:     {"ctrl+u"},
		config.ActionDown:   {"ctrl+n"},
		config.ActionValues: {"alt+v"},
	})

	resource := navigationSecret()
	viewer := newValueScreen(resource, "config", editEnv{keys: km}, testStyles(true))
	if got := viewer.Hints().bindings[0].Help().Key; got != "ctrl+u/ctrl+n" {
		t.Fatalf("viewer movement hint = %q, want ctrl+u/ctrl+n", got)
	}
	if got := strings.Join(helpNotes(editEnv{keys: km}, " | "), "\n"); !strings.Contains(got, "value search (alt+v)") {
		t.Fatalf("help notes do not use rebound values key:\n%s", got)
	}
}

func TestBubblesScopeDefaultsMatchRegistry(t *testing.T) {
	listKeys := list.DefaultKeyMap()
	viewportKeys := viewport.DefaultKeyMap()
	wantList := map[string][]string{
		string(config.ActionUp): listKeys.CursorUp.Keys(), string(config.ActionDown): listKeys.CursorDown.Keys(),
		string(config.ActionTop): listKeys.GoToStart.Keys(), string(config.ActionBottom): listKeys.GoToEnd.Keys(),
		string(config.ActionPageUp): listKeys.PrevPage.Keys(), string(config.ActionPageDown): listKeys.NextPage.Keys(),
		string(config.ActionFilter): listKeys.Filter.Keys(), string(config.ActionRefresh): {"ctrl+r"},
	}
	wantViewport := map[string][]string{
		string(config.ActionUp): viewportKeys.Up.Keys(), string(config.ActionDown): viewportKeys.Down.Keys(),
		string(config.ActionPageUp): viewportKeys.PageUp.Keys(), string(config.ActionPageDown): viewportKeys.PageDown.Keys(),
		string(config.ActionHalfPageUp): viewportKeys.HalfPageUp.Keys(), string(config.ActionHalfPageDown): viewportKeys.HalfPageDown.Keys(),
		"move left": viewportKeys.Left.Keys(), "move right": viewportKeys.Right.Keys(),
	}
	if got := config.ScopeDefaultKeys("list"); !reflect.DeepEqual(got, wantList) {
		t.Fatalf("list defaults = %#v, want %#v", got, wantList)
	}
	if got := config.ScopeDefaultKeys("viewport"); !reflect.DeepEqual(got, wantViewport) {
		t.Fatalf("viewport defaults = %#v, want %#v", got, wantViewport)
	}
}

// loadValidOverrides routes a keybind document through the real config file
// loader so tests can only exercise override sets validation actually accepts.
func loadValidOverrides(t *testing.T, document string) config.Overrides {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "sk64")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", base)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("override document must pass real validation: %v", err)
	}
	return cfg.Keybinds
}

func TestApplyKeybindsLeavesInputNavigationUntouched(t *testing.T) {
	km := defaultKeyMaps()
	wantSearch := km.search
	wantProjectForm := km.projectForm
	wantFilePrompt := km.filePrompt
	applyKeybinds(km, loadValidOverrides(t, "keybind = alt+k=up\nkeybind = alt+j=down\nkeybind = o=refresh\n"))
	if !reflect.DeepEqual(km.search, wantSearch) {
		t.Fatal("search keymap changed")
	}
	if !reflect.DeepEqual(km.projectForm, wantProjectForm) {
		t.Fatal("project form keymap changed")
	}
	if !reflect.DeepEqual(km.filePrompt, wantFilePrompt) {
		t.Fatal("file prompt keymap changed")
	}
}

func TestFilePromptUsesReboundPickerKeyMap(t *testing.T) {
	km := defaultKeyMaps()
	applyKeybinds(km, loadValidOverrides(t, "keybind = alt+k=up\nkeybind = alt+j=down\nkeybind = alt+u=page-up\nkeybind = alt+n=page-down\n"))
	screen := newFilePrompt(t.Context(), testClient(), editEnv{keys: km}, navigationSecret(), "config", fileImport, testStyles(true))

	for name, test := range map[string]struct {
		got, want []string
	}{
		"up":        {screen.picker.KeyMap.Up.Keys(), []string{"alt+k"}},
		"down":      {screen.picker.KeyMap.Down.Keys(), []string{"alt+j"}},
		"page up":   {screen.picker.KeyMap.PageUp.Keys(), []string{"alt+u"}},
		"page down": {screen.picker.KeyMap.PageDown.Keys(), []string{"alt+n"}},
	} {
		if !reflect.DeepEqual(test.got, test.want) {
			t.Errorf("%s keys = %v, want %v", name, test.got, test.want)
		}
	}
}

func testBindingRefs(km *keyMaps) map[string]*bubbleskey.Binding {
	return map[string]*bubbleskey.Binding{
		"list.CursorUp":           &km.list.CursorUp,
		"list.CursorDown":         &km.list.CursorDown,
		"list.NextPage":           &km.list.NextPage,
		"list.PrevPage":           &km.list.PrevPage,
		"list.GoToStart":          &km.list.GoToStart,
		"list.GoToEnd":            &km.list.GoToEnd,
		"list.Filter":             &km.list.Filter,
		"viewport.PageDown":       &km.viewport.PageDown,
		"viewport.PageUp":         &km.viewport.PageUp,
		"viewport.HalfPageUp":     &km.viewport.HalfPageUp,
		"viewport.HalfPageDown":   &km.viewport.HalfPageDown,
		"viewport.Down":           &km.viewport.Down,
		"viewport.Up":             &km.viewport.Up,
		"filePicker.GoToTop":      &km.filePicker.GoToTop,
		"filePicker.GoToLast":     &km.filePicker.GoToLast,
		"filePicker.PageDown":     &km.filePicker.PageDown,
		"filePicker.PageUp":       &km.filePicker.PageUp,
		"filePicker.Down":         &km.filePicker.Down,
		"filePicker.Up":           &km.filePicker.Up,
		"global.Quit":             &km.global.Quit,
		"global.Help":             &km.global.Help,
		"global.Filter":           &km.global.Filter,
		"global.Search":           &km.global.Search,
		"namespace.Refresh":       &km.namespace.Refresh,
		"namespace.AllNamespaces": &km.namespace.AllNamespaces,
		"resource.Refresh":        &km.resource.Refresh,
		"resource.TypeCycle":      &km.resource.TypeCycle,
		"resource.AllNamespaces":  &km.resource.AllNamespaces,
		"keyScreen.Refresh":       &km.keyScreen.Refresh,
		"keyScreen.ValueSearch":   &km.keyScreen.ValueSearch,
		"workload.Refresh":        &km.workload.Refresh,
		"consumers.Refresh":       &km.consumers.Refresh,
		"search.Refresh":          &km.search.Refresh,
		"search.Up":               &km.search.Up,
		"search.Down":             &km.search.Down,
		"project.Refresh":         &km.project.Refresh,
		"suggestion.Refresh":      &km.suggestion.Refresh,
		"editFlow.Wrap":           &km.editFlow.Wrap,
		"editFlow.RolloutUp":      &km.editFlow.RolloutUp,
		"editFlow.RolloutDown":    &km.editFlow.RolloutDown,
		"createPrompt.Up":         &km.createPrompt.Up,
		"createPrompt.Down":       &km.createPrompt.Down,
		"projectForm.Up":          &km.projectForm.Up,
		"projectForm.Down":        &km.projectForm.Down,
		"filePrompt.CycleFocus":   &km.filePrompt.CycleFocus,
		"helpOverlay.Close":       &km.helpOverlay.Close,
	}
}

func bindingKeys(bindings map[string]*bubbleskey.Binding) map[string][]string {
	keys := make(map[string][]string, len(bindings))
	for name, binding := range bindings {
		keys[name] = slices.Clone(binding.Keys())
	}
	return keys
}

func TestEveryBindingHasKeysAndHelp(t *testing.T) {
	walkDefaultBindings(t, func(t *testing.T, _ string, binding bubbleskey.Binding) {
		if len(binding.Keys()) == 0 {
			t.Fatal("has no keys")
		}
		help := binding.Help()
		if help.Key == "" || help.Desc == "" {
			t.Fatalf("has incomplete help: %#v", help)
		}
		for _, value := range []string{help.Key, help.Desc} {
			if strings.IndexFunc(value, func(r rune) bool { return r > 127 }) >= 0 {
				t.Fatalf("help is not ASCII: %q", value)
			}
		}
	})
}

func TestDefaultKeyMapsAvoidReservedKeys(t *testing.T) {
	reservedKeys := config.ReservedKeys()
	reserved := make(map[string]struct{}, len(reservedKeys))
	for _, entry := range reservedKeys {
		reserved[entry.Key] = struct{}{}
	}
	walkDefaultBindings(t, func(t *testing.T, name string, binding bubbleskey.Binding) {
		if reservedBindingRole(name) {
			return
		}
		for _, key := range binding.Keys() {
			if _, found := reserved[key]; found {
				t.Fatalf("uses reserved key %q", key)
			}
		}
	})
}

func TestModeDisablesBindings(t *testing.T) {
	tests := []struct {
		name  string
		check func(*testing.T)
	}{
		{
			name: "key screen read-only",
			check: func(t *testing.T) {
				screen := newKeyScreen(t.Context(), testClient(), k8s.KindSecret, "default", "example", editEnv{readOnly: true}, testStyles(true))
				assertKeyEditingBindingsEnabled(t, screen.km, false)
			},
		},
		{
			name: "key screen immutable resource",
			check: func(t *testing.T) {
				screen := newKeyScreen(t.Context(), testClient(), k8s.KindSecret, "default", "example", editEnv{}, testStyles(true))
				_, reqID := screen.start(t.Context())
				immutable := true
				resource := k8s.NewSecret(&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
					Immutable:  &immutable,
				})
				_, _ = screen.Update(resourceLoadedMsg{reqID: reqID, res: resource})
				assertKeyEditingBindingsEnabled(t, screen.km, false)
			},
		},
		{
			name: "resource screen read-only",
			check: func(t *testing.T) {
				screen := newResourceScreen(t.Context(), testClient(), "default", editEnv{readOnly: true}, testStyles(true))
				if screen.km.New.Enabled() || screen.km.Delete.Enabled() {
					t.Fatal("New and Delete must be disabled")
				}
			},
		},
		{
			name: "resource screen no ConfigMaps",
			check: func(t *testing.T) {
				screen := newResourceScreen(t.Context(), testClient(), "default", editEnv{noConfigMaps: true}, testStyles(true))
				if screen.km.TypeCycle.Enabled() {
					t.Fatal("TypeCycle must be disabled")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, test.check)
	}
}

func walkDefaultBindings(t *testing.T, check func(*testing.T, string, bubbleskey.Binding)) {
	t.Helper()
	maps := defaultKeyMaps()
	groups := []struct {
		name  string
		value any
	}{
		{name: "global", value: maps.global},
		{name: "namespace", value: maps.namespace},
		{name: "resource", value: maps.resource},
		{name: "keyScreen", value: maps.keyScreen},
		{name: "workload", value: maps.workload},
		{name: "consumers", value: maps.consumers},
		{name: "search", value: maps.search},
		{name: "project", value: maps.project},
		{name: "suggestion", value: maps.suggestion},
		{name: "editFlow", value: maps.editFlow},
		{name: "createPrompt", value: maps.createPrompt},
		{name: "projectForm", value: maps.projectForm},
		{name: "filePrompt", value: maps.filePrompt},
		{name: "filePicker", value: maps.filePicker},
		{name: "helpOverlay", value: maps.helpOverlay},
	}
	bindingType := reflect.TypeFor[bubbleskey.Binding]()
	for _, group := range groups {
		value := reflect.ValueOf(group.value)
		typeOfValue := value.Type()
		for index := range value.NumField() {
			field := typeOfValue.Field(index)
			if field.Type != bindingType {
				t.Fatalf("%s.%s has type %s, want key.Binding", group.name, field.Name, field.Type)
			}
			binding := value.Field(index).Interface().(bubbleskey.Binding)
			t.Run(group.name+"."+field.Name, func(t *testing.T) {
				check(t, field.Name, binding)
			})
		}
	}
}

func reservedBindingRole(name string) bool {
	return slices.Contains([]string{"Restart", "Confirm", "Back", "Accept", "Open", "Select", "Choose", "Apply", "Close"}, name) ||
		strings.HasPrefix(name, "New") || strings.HasPrefix(name, "Delete") || strings.HasPrefix(name, "Cancel")
}

func assertKeyEditingBindingsEnabled(t *testing.T, km keyKeyMap, enabled bool) {
	t.Helper()
	bindings := map[string]bubbleskey.Binding{
		"Import": km.Import, "EditAll": km.EditAll, "NewKey": km.NewKey,
		"DeleteKey": km.DeleteKey, "Undo": km.Undo,
	}
	for name, binding := range bindings {
		if binding.Enabled() != enabled {
			t.Errorf("%s enabled = %t, want %t", name, binding.Enabled(), enabled)
		}
	}
}

func TestRebindLabelsListEveryKeyButStateLinesUsePrimary(t *testing.T) {
	km := defaultKeyMaps()
	applyKeybinds(km, loadValidOverrides(t, "keybind = ctrl+e=refresh\nkeybind = o=refresh\n"))
	if got := km.namespace.Refresh.Help().Key; got != "ctrl+e/o" {
		t.Fatalf("refresh help label = %q, want %q", got, "ctrl+e/o")
	}
	if got := bindingAction(km.namespace.Refresh, "to retry"); got != "ctrl+e to retry" {
		t.Fatalf("state-line action = %q, want primary key only", got)
	}
}
