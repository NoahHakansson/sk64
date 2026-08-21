package tui

import (
	"slices"
	"strings"
	"testing"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHelpKeyPaddingUsesDisplayCells(t *testing.T) {
	st := testStyles(true)
	content := ansi.Strip(renderHelp([]helpGroup{{title: "wide", entries: []helpGroupEntry{
		{binding: displayHint("界", "display"), desc: "description"},
	}}}, nil, st, helpPreferredWidth))
	for _, line := range strings.Split(content, "\n") {
		before, found := strings.CutSuffix(line, "description")
		if !found {
			continue
		}
		if got := ansi.StringWidth(before); got != helpDescriptionIndent {
			t.Fatalf("description indent = %d, want %d: %q", got, helpDescriptionIndent, line)
		}
		return
	}
	t.Fatalf("rendered help lacks description:\n%s", content)
}

func TestHelpOverlayUsesInformationalBorder(t *testing.T) {
	for _, theme := range []struct {
		name   string
		isDark bool
	}{
		{name: "light"},
		{name: "dark", isDark: true},
	} {
		t.Run(theme.name, func(t *testing.T) {
			st := newStyles(theme.isDark, newGlyphs(false))
			help := newHelpOverlay(browsingHelpScreens(t)[0].screen, editEnv{}, st)
			help.SetSize(80, 22)
			view := help.View()
			faintRGB := colorRGB(st.palette.fgFaint)
			if border := ansiTextMatching(view, func(state ansiStyleState) bool {
				return state.hasForeground && state.foreground == faintRGB
			}); !strings.Contains(border, st.glyphs.border.TopLeft) {
				t.Fatalf("help border does not use informational colour: %q", border)
			}
			accentRGB := colorRGB(st.palette.accent)
			if accent := ansiTextMatching(view, func(state ansiStyleState) bool {
				return state.hasForeground && state.foreground == accentRGB
			}); accent != "" {
				t.Fatalf("help overlay carries decision accent %q", accent)
			}
		})
	}
}

func TestGolden_HelpOverlayNamespaces(t *testing.T) {
	h := namespaceHarness(t)
	h.send(tea.WindowSizeMsg{Width: 80, Height: 34})
	h.keys("?")
	h.golden("help_overlay_namespaces")
}

func TestGolden_HelpOverlayKeys(t *testing.T) {
	h := keyHarness(t, navigationSecret())
	h.send(tea.WindowSizeMsg{Width: 80, Height: 42})
	h.keys("?")
	view := h.view()
	if !strings.Contains(view, "RAM only") || !strings.Contains(view, "never the cluster") {
		t.Fatalf("key help is missing mandated caveats:\n%s", view)
	}
	h.golden("help_overlay_keys")
}

func TestHelpOverlayLifecycle(t *testing.T) {
	t.Run("open and close", func(t *testing.T) {
		h := resourceHarness(t, true)
		h.keys("?")
		if _, ok := h.m.(app).overlay.(*helpOverlay); !ok || !strings.Contains(h.view(), "up/down scroll  esc close") {
			t.Fatalf("? opened overlay %T with view %q", h.m.(app).overlay, h.view())
		}
		h.keys("esc")
		if h.m.(app).overlay != nil {
			t.Fatal("esc did not close help")
		}
	})

	t.Run("reopen and scroll", func(t *testing.T) {
		h := resourceHarness(t, true)
		h.keys("?", "esc", "?", "down")
		if _, ok := h.m.(app).overlay.(*helpOverlay); !ok {
			t.Fatalf("? reopened overlay %T", h.m.(app).overlay)
		}
	})

	t.Run("search captures help key", func(t *testing.T) {
		h := searchHarness(t, Options{})
		h.keys("?")
		if h.m.(app).overlay != nil {
			t.Fatal("? opened help while search captured input")
		}
	})

	t.Run("active overlay is preserved", func(t *testing.T) {
		h := namespaceHarness(t)
		h.keys("ctrl+k")
		original := h.m.(app).overlay
		h.keys("?")
		if h.m.(app).overlay != original {
			t.Fatalf("? replaced active overlay %T with %T", original, h.m.(app).overlay)
		}
	})
}

func TestHelpReadOnlyNote(t *testing.T) {
	for _, test := range []struct {
		name     string
		readOnly bool
		want     bool
	}{
		{name: "read only", readOnly: true, want: true},
		{name: "writable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, Options{ASCII: true, ReadOnly: test.readOnly})
			h.keys("?")
			help := h.m.(app).overlay.(*helpOverlay)
			if got := strings.Contains(strings.Join(help.notes, "\n"), "read-only mode"); got != test.want {
				t.Fatalf("read-only note present = %t, want %t", got, test.want)
			}
		})
	}
}

func TestHelpOverlayWrapsNarrowContentWithHangingIndents(t *testing.T) {
	st := testStyles(true)
	help := newHelpOverlay(browsingHelpScreens(t)[0].screen, editEnv{}, st)
	help.SetSize(60, 13)

	if help.boxWidth != 56 || help.contentWidth != 50 || help.viewport.Width() != help.contentWidth {
		t.Fatalf("narrow widths = box %d content %d viewport %d, want 56/50/50", help.boxWidth, help.contentWidth, help.viewport.Width())
	}
	if got := ansi.StringWidth(strings.Split(help.View(), "\n")[0]); got != help.boxWidth {
		t.Fatalf("rendered width = %d, want %d", got, help.boxWidth)
	}
	lines := strings.Split(ansi.Strip(help.content), "\n")
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > help.contentWidth {
			t.Fatalf("help line is %d columns, want <= %d: %q", width, help.contentWidth, line)
		}
	}

	keyLine := -1
	noteLine := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "  Q / ctrl+c") {
			keyLine = i
		}
		if strings.Contains(line, "destructive confirms") {
			noteLine = i
		}
	}
	if keyLine < 0 || keyLine+1 >= len(lines) || !strings.HasPrefix(lines[keyLine+1], strings.Repeat(" ", helpDescriptionIndent)) {
		t.Fatalf("wrapped key description lacks a hanging indent:\n%s", help.content)
	}
	if noteLine < 0 || noteLine+1 >= len(lines) || !strings.HasPrefix(lines[noteLine+1], "  ") || strings.HasPrefix(lines[noteLine+1], "   ") {
		t.Fatalf("wrapped note lacks a two-space hanging indent:\n%s", help.content)
	}
}

func TestHelpOverlayKeepsBottomReachableAcrossResize(t *testing.T) {
	help := newHelpOverlay(browsingHelpScreens(t)[0].screen, editEnv{}, testStyles(true))
	help.SetSize(60, 13)
	help.viewport.GotoBottom()
	if !help.viewport.AtBottom() || help.viewport.YOffset() == 0 {
		t.Fatalf("narrow help did not scroll to bottom: offset %d", help.viewport.YOffset())
	}

	help.SetSize(160, 13)
	if !help.viewport.AtBottom() || help.viewport.PastBottom() {
		t.Fatalf("resized viewport offset %d is not at a valid bottom", help.viewport.YOffset())
	}
	lastNote := help.notes[len(help.notes)-1]
	if !strings.Contains(ansi.Strip(help.viewport.View()), lastNote) {
		t.Fatalf("final note is not reachable after resize:\n%s", help.viewport.View())
	}
}

func TestHelpOverlayPreservesMidScrollAcrossResize(t *testing.T) {
	help := newHelpOverlay(browsingHelpScreens(t)[0].screen, editEnv{}, testStyles(true))
	help.SetSize(60, 13)
	help.viewport.SetYOffset(3)
	if help.viewport.YOffset() != 3 || help.viewport.AtBottom() {
		t.Fatalf("mid-scroll setup offset = %d, at bottom = %t", help.viewport.YOffset(), help.viewport.AtBottom())
	}

	help.SetSize(160, 13)
	if help.viewport.YOffset() != 3 {
		t.Fatalf("resized viewport offset = %d, want 3", help.viewport.YOffset())
	}
}

func TestHelpCoversEveryHintKey(t *testing.T) {
	for _, test := range browsingHelpScreens(t) {
		t.Run(test.name, func(t *testing.T) {
			keys := make(map[string]bool)
			for _, section := range []helpGroup{test.screen.Help(), globalHelpGroup(packageDefaultKeyMaps)} {
				for _, entry := range section.entries {
					if !entry.binding.Enabled() {
						continue
					}
					label := entry.binding.Help().Key
					keys[label] = true
					for _, alias := range strings.Split(label, " / ") {
						keys[alias] = true
					}
				}
			}
			for _, binding := range test.screen.Hints().bindings {
				if !binding.Enabled() || slices.Equal(binding.Keys(), []string{""}) {
					continue
				}
				label := binding.Help().Key
				if !keys[label] {
					t.Fatalf("help for %s does not declare hint key %q", test.name, label)
				}
			}
		})
	}
}

func TestFooterSkipsDisabledBindings(t *testing.T) {
	enabled := bubbleskey.NewBinding(bubbleskey.WithKeys("e"), bubbleskey.WithHelp("e", "enabled"))
	disabled := bubbleskey.NewBinding(bubbleskey.WithKeys("d"), bubbleskey.WithHelp("d", "disabled"))
	disabled.SetEnabled(false)

	if got := plainHintGroups(hintGroups(hintBindings(enabled, disabled))); got != "e enabled" {
		t.Fatalf("footer groups = %q, want %q", got, "e enabled")
	}
	help := ansi.Strip(renderHelp([]helpGroup{{title: "test", entries: []helpGroupEntry{
		{binding: enabled, desc: "enabled entry"},
		{binding: disabled, desc: "disabled entry"},
	}}}, nil, testStyles(true), helpPreferredWidth))
	if strings.Contains(help, "disabled entry") {
		t.Fatalf("disabled binding rendered in help:\n%s", help)
	}
}

func TestHelpLinesFitBox(t *testing.T) {
	for _, test := range browsingHelpScreens(t) {
		t.Run(test.name, func(t *testing.T) {
			st := testStyles(true)
			help := newHelpOverlay(test.screen, editEnv{readOnly: true}, st)
			help.SetSize(80, 22)
			for _, line := range strings.Split(help.content, "\n") {
				if width := ansi.StringWidth(line); width > help.contentWidth {
					t.Fatalf("help line is %d columns, want <= %d: %q", width, help.contentWidth, line)
				}
			}
		})
	}
}
