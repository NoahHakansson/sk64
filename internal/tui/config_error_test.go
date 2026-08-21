package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/config"
)

func TestGolden_ConfigErrorUnicode(t *testing.T) {
	configErrorHarness(t, false, 80, 24, config.Errors{
		{Line: 2, Text: "keybind = ctrl+r=sideways", Msg: "unknown action \"sideways\"", Hint: "use one of the documented rebindable actions"},
		{Line: 5, Text: "keybind = ctrl+x=refresh", Msg: "key collides with another action", Hint: "choose a key unused on the same screen"},
		{Line: 9, Msg: "missing keybind value", Hint: "write keybind = <keys>=<action>"},
	}).golden("config_error_unicode")
}

func TestGolden_ConfigErrorASCII(t *testing.T) {
	configErrorHarness(t, true, 80, 24, config.Errors{
		{Line: 2, Text: "keybind = ctrl+r=sideways", Msg: "unknown action \"sideways\"", Hint: "use one of the documented rebindable actions"},
		{Line: 5, Text: "keybind = ctrl+x=refresh", Msg: "key collides with another action", Hint: "choose a key unused on the same screen"},
	}).golden("config_error_ascii")
}

func TestGolden_ConfigErrorNarrow(t *testing.T) {
	configErrorHarness(t, false, 60, 24, config.Errors{
		{Line: 12, Text: "keybind = ctrl+alt+shift+f12=half-page-down", Msg: "key collides with another action on the resource screen", Hint: "choose a key unused on the same screen"},
		{Line: 18, Text: "unknown = value", Msg: "unknown setting \"unknown\"", Hint: "remove the setting"},
	}).golden("config_error_narrow")
}

func TestGolden_ConfigErrorOverflow(t *testing.T) {
	errs := make(config.Errors, 0, 8)
	for line := 1; line <= 8; line++ {
		errs = append(errs, config.Error{
			Line: line,
			Text: "keybind = ctrl+x=refresh",
			Msg:  "key collides with another action",
			Hint: "choose another key",
		})
	}
	h := configErrorHarness(t, false, 80, 12, errs)
	view := h.view()
	if !strings.Contains(view, "...and ") || !strings.Contains(view, " more") {
		t.Fatalf("overflow view lacks counted cue:\n%s", view)
	}
	if !strings.Contains(view, "fix the file and start sk64 again") {
		t.Fatalf("overflow view dropped prompt:\n%s", view)
	}
	h.golden("config_error_overflow")
}

func TestConfigErrorQuitKeys(t *testing.T) {
	for _, value := range []string{"esc", "Q"} {
		t.Run(value, func(t *testing.T) {
			model := newConfigErrorModel("/tmp/config", config.Errors{{Line: 1, Msg: "bad config"}}, true)
			_, cmd := model.Update(key(value))
			if cmd == nil {
				t.Fatal("quit key returned no command")
			}
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); !ok {
				t.Fatalf("quit command returned %T", msg)
			}
		})
	}
}

func TestConfigErrorControlCRequiresDoublePress(t *testing.T) {
	model := newConfigErrorModel("/tmp/config", config.Errors{{Line: 1, Msg: "bad config"}}, true)
	model.quit.tick = func(uint64) tea.Cmd { return func() tea.Msg { return nil } }

	updated, cmd := model.Update(key("ctrl+c"))
	model = updated.(configErrorModel)
	if cmd == nil || !model.quit.armed {
		t.Fatalf("first ctrl+c command = %v, armed = %t", cmd, model.quit.armed)
	}
	if !strings.Contains(model.View().Content, "press ctrl+c again to quit") {
		t.Fatalf("armed footer missing warning:\n%s", model.View().Content)
	}

	_, cmd = model.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("second ctrl+c returned no command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("second ctrl+c command returned %T", msg)
	}
}

func TestConfigErrorIgnoresOtherKeys(t *testing.T) {
	model := newConfigErrorModel("/tmp/config", config.Errors{{Line: 1, Msg: "bad config"}}, true)
	_, cmd := model.Update(key("enter"))
	if cmd != nil {
		t.Fatalf("other key returned command %T", cmd())
	}
}

func TestConfigErrorIgnoresLowercaseQ(t *testing.T) {
	model := newConfigErrorModel("/tmp/config", config.Errors{{Line: 1, Msg: "bad config"}}, true)
	_, cmd := model.Update(key("q"))
	if cmd != nil {
		t.Fatalf("lowercase q returned command %T", cmd())
	}
}

func TestConfigErrorRequestsAndAppliesBackgroundColor(t *testing.T) {
	model := newConfigErrorModel("/tmp/config", config.Errors{{Line: 1, Msg: "bad config"}}, true)
	if model.Init() == nil {
		t.Fatal("Init() did not request the terminal background color")
	}
	darkAccent := model.styles.palette.accent

	updated, cmd := model.Update(tea.BackgroundColorMsg{Color: color.White})
	if cmd != nil {
		t.Fatal("background color update returned a command")
	}
	lightModel := updated.(configErrorModel)
	if sameColor(darkAccent, lightModel.styles.palette.accent) {
		t.Fatal("background color update did not rebuild the semantic palette")
	}
	requireSameColor(t, lightModel.styles.palette.accent, newSemanticPalette(false).accent)
}

func configErrorHarness(t *testing.T, ascii bool, width, height int, errs config.Errors) *harness {
	t.Helper()
	model := newConfigErrorModel("/home/user/.config/sk64/config", errs, ascii)
	updated, cmd := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	if cmd != nil {
		t.Fatalf("resize returned command %T", cmd())
	}
	return &harness{t: t, m: updated, ascii: ascii}
}
