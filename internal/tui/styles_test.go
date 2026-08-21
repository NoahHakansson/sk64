package tui

import (
	"image/color"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestGlyphModesResolveChromeAtConstruction(t *testing.T) {
	for _, test := range []struct {
		name              string
		ascii             bool
		wantSeparator     string
		wantEllipsis      string
		wantActivePage    string
		wantInactivePage  string
		ellipsisCellWidth int
	}{
		{name: "unicode", wantSeparator: " · ", wantEllipsis: "…", wantActivePage: "●", wantInactivePage: "○", ellipsisCellWidth: 1},
		{name: "ASCII", ascii: true, wantSeparator: " - ", wantEllipsis: "...", wantActivePage: "*", wantInactivePage: ".", ellipsisCellWidth: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			glyphs := newGlyphs(test.ascii)
			if glyphs.separator != test.wantSeparator {
				t.Fatalf("separator = %q, want %q", glyphs.separator, test.wantSeparator)
			}
			if glyphs.ellipsis != test.wantEllipsis {
				t.Fatalf("ellipsis = %q, want %q", glyphs.ellipsis, test.wantEllipsis)
			}
			if glyphs.activePage != test.wantActivePage {
				t.Fatalf("active page = %q, want %q", glyphs.activePage, test.wantActivePage)
			}
			if glyphs.inactivePage != test.wantInactivePage {
				t.Fatalf("inactive page = %q, want %q", glyphs.inactivePage, test.wantInactivePage)
			}
			if glyphs.activePage == glyphs.inactivePage {
				t.Fatalf("active and inactive pagination glyphs must differ: %q", glyphs.activePage)
			}
			listStyle := newStyles(true, glyphs).listStyle
			for name, dot := range map[string]string{
				"active":   listStyle.ActivePaginationDot.String(),
				"inactive": listStyle.InactivePaginationDot.String(),
			} {
				if !strings.HasSuffix(ansi.Strip(dot), " ") {
					t.Fatalf("%s pagination dot %q lacks the separating space; adjacent dots touch", name, dot)
				}
			}
			if width := lipgloss.Width(glyphs.ellipsis); width != test.ellipsisCellWidth {
				t.Fatalf("ellipsis width = %d, want %d", width, test.ellipsisCellWidth)
			}
		})
	}
}

func TestGlyphModesResolveSemanticAtoms(t *testing.T) {
	tests := []struct {
		name         string
		ascii        bool
		spinner      spinner.Spinner
		cron         string
		stateMarkers [stateLineKindCount]string
		tags         []string
	}{
		{
			name: "unicode", spinner: spinner.MiniDot, cron: "↻",
			stateMarkers: [stateLineKindCount]string{"", "✓", "✗", "○", "⚠", "⚠"},
			tags:         []string{"binary", "immutable", "missing", "no access", "active", "inactive", "server mismatch", "context not found", "current", "check failed", "origin mismatch"},
		},
		{
			name: "ASCII", ascii: true, spinner: spinner.Line, cron: "[cron]",
			stateMarkers: [stateLineKindCount]string{"[loading]", "[success]", "[error]", "[empty]", "[incomplete]", "[unknown]"},
			tags:         []string{"[binary]", "[immutable]", "[missing]", "[no access]", "[active]", "[inactive]", "[server mismatch]", "[context not found]", "[current]", "[check failed]", "[origin mismatch]"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			glyphs := newGlyphs(test.ascii)
			if !slices.Equal(glyphs.spinner.Frames, test.spinner.Frames) {
				t.Fatalf("spinner frames = %q, want %q", glyphs.spinner.Frames, test.spinner.Frames)
			}
			if glyphs.cronMarker != test.cron || glyphs.stateMarkers != test.stateMarkers {
				t.Fatalf("cron/state markers = %q/%q, want %q/%q", glyphs.cronMarker, glyphs.stateMarkers, test.cron, test.stateMarkers)
			}
			gotTags := []string{glyphs.binaryTag, glyphs.immutableTag, glyphs.missingTag, glyphs.noAccessTag, glyphs.activeTag, glyphs.inactiveTag, glyphs.serverMismatchTag, glyphs.contextNotFoundTag, glyphs.currentTag, glyphs.checkFailedTag, glyphs.originMismatchTag}
			if !slices.Equal(gotTags, test.tags) {
				t.Fatalf("tags = %q, want %q", gotTags, test.tags)
			}
		})
	}
}

func TestTruncateLineUsesActiveMarkerWithinWidth(t *testing.T) {
	for _, test := range []struct {
		name     string
		ellipsis string
	}{
		{name: "unicode", ellipsis: "…"},
		{name: "ASCII", ellipsis: "..."},
	} {
		t.Run(test.name, func(t *testing.T) {
			const width = 7
			got := truncateLine("abcdefghijklmnop", width, test.ellipsis)
			if !strings.Contains(got, test.ellipsis) {
				t.Fatalf("truncated line = %q, want marker %q", got, test.ellipsis)
			}
			if gotWidth := lipgloss.Width(got); gotWidth > width {
				t.Fatalf("truncated width = %d, want <= %d: %q", gotWidth, width, got)
			}
			if got := truncateLine("unchanged", 0, test.ellipsis); got != "unchanged" {
				t.Fatalf("width-zero line = %q, want unchanged", got)
			}
		})
	}
}

func TestMiddleElideLineUsesActiveMarkerWithinWidth(t *testing.T) {
	for _, test := range []struct {
		name     string
		ellipsis string
	}{
		{name: "unicode", ellipsis: "…"},
		{name: "ASCII", ellipsis: "..."},
	} {
		t.Run(test.name, func(t *testing.T) {
			const width = 7
			got := middleElideLine("abcdefghijklmnop", width, test.ellipsis)
			if !strings.Contains(got, test.ellipsis) {
				t.Fatalf("middle-elided line = %q, want marker %q", got, test.ellipsis)
			}
			if gotWidth := lipgloss.Width(got); gotWidth > width {
				t.Fatalf("middle-elided width = %d, want <= %d: %q", gotWidth, width, got)
			}
		})
	}
}

func TestSemanticPaletteAdaptsToBackground(t *testing.T) {
	roles := []struct {
		name  string
		color func(semanticPalette) color.Color
		light color.RGBA
		dark  color.RGBA
	}{
		{name: "fg", color: func(p semanticPalette) color.Color { return p.fg }, light: rgb(0x18, 0x22, 0x30), dark: rgb(0xE7, 0xED, 0xF4)},
		{name: "fg muted", color: func(p semanticPalette) color.Color { return p.fgMuted }, light: rgb(0x58, 0x65, 0x74), dark: rgb(0xA7, 0xB3, 0xC0)},
		{name: "fg faint", color: func(p semanticPalette) color.Color { return p.fgFaint }, light: rgb(0x85, 0x90, 0x9C), dark: rgb(0x6F, 0x7C, 0x89)},
		{name: "accent", color: func(p semanticPalette) color.Color { return p.accent }, light: rgb(0x6C, 0x3F, 0xC5), dark: rgb(0xC7, 0x95, 0xFF)},
		{name: "success", color: func(p semanticPalette) color.Color { return p.success }, light: rgb(0x23, 0x7A, 0x4B), dark: rgb(0x65, 0xD3, 0x91)},
		{name: "warning", color: func(p semanticPalette) color.Color { return p.warning }, light: rgb(0x94, 0x60, 0x00), dark: rgb(0xF0, 0xB8, 0x5B)},
		{name: "danger", color: func(p semanticPalette) color.Color { return p.danger }, light: rgb(0xB4, 0x23, 0x2C), dark: rgb(0xFF, 0x7B, 0x84)},
		{name: "brand", color: func(p semanticPalette) color.Color { return p.brand }, light: rgb(0x5F, 0x5F, 0xAF), dark: rgb(0x5F, 0x5F, 0xAF)},
		{name: "on brand", color: func(p semanticPalette) color.Color { return p.onBrand }, light: rgb(0xFF, 0xFF, 0xFF), dark: rgb(0xF2, 0xF1, 0xFF)},
		{name: "gold", color: func(p semanticPalette) color.Color { return p.gold }, light: rgb(0x8A, 0x6D, 0x00), dark: rgb(0xFF, 0xD7, 0x5F)},
		{name: "cyan", color: func(p semanticPalette) color.Color { return p.cyan }, light: rgb(0x0B, 0x72, 0x85), dark: rgb(0x5F, 0xD7, 0xFF)},
		{name: "chrome higher", color: func(p semanticPalette) color.Color { return p.chromeHigher }, light: rgb(0xD9, 0xE2, 0xEC), dark: rgb(0x24, 0x31, 0x3E)},
	}

	for _, role := range roles {
		t.Run(role.name, func(t *testing.T) {
			requireColorRGBA(t, role.color(newSemanticPalette(false)), role.light)
			requireColorRGBA(t, role.color(newSemanticPalette(true)), role.dark)
		})
	}
}

func TestStylesUseSemanticRoles(t *testing.T) {
	for _, theme := range []struct {
		name   string
		isDark bool
	}{
		{name: "light"},
		{name: "dark", isDark: true},
	} {
		t.Run(theme.name, func(t *testing.T) {
			st := newStyles(theme.isDark, newGlyphs(false))
			p := st.palette
			checks := []struct {
				name string
				got  color.Color
				want color.Color
			}{
				{name: "header foreground", got: st.header.GetForeground(), want: p.onBrand},
				{name: "header brand", got: st.header.GetBackground(), want: p.brand},
				{name: "active context", got: st.activeContext.GetForeground(), want: p.gold},
				{name: "active context brand", got: st.activeContext.GetBackground(), want: p.brand},
				{name: "footer foreground", got: st.footer.GetForeground(), want: p.fgMuted},
				{name: "error", got: st.errText.GetForeground(), want: p.danger},
				{name: "success", got: st.successText.GetForeground(), want: p.success},
				{name: "tag", got: st.tag.GetForeground(), want: p.gold},
				{name: "dim", got: st.dim.GetForeground(), want: p.fgMuted},
				{name: "too small", got: st.tooSmall.GetForeground(), want: p.danger},
				{name: "diff add", got: st.diffAdd.GetForeground(), want: p.success},
				{name: "diff delete", got: st.diffDel.GetForeground(), want: p.danger},
				{name: "json key", got: st.jsonKey.GetForeground(), want: p.cyan},
				{name: "json string", got: st.jsonString.GetForeground(), want: p.success},
				{name: "json number", got: st.jsonNumber.GetForeground(), want: p.warning},
				{name: "json literal", got: st.jsonLiteral.GetForeground(), want: p.accent},
				{name: "help text", got: st.helpBox.GetForeground(), want: p.fg},
				{name: "help border", got: st.helpBox.GetBorderLeftForeground(), want: p.fgFaint},
				{name: "dialog text", got: st.dialogBox.GetForeground(), want: p.fg},
				{name: "dialog border", got: st.dialogBox.GetBorderLeftForeground(), want: p.brand},
				{name: "danger dialog border", got: st.dialogDanger.GetBorderLeftForeground(), want: p.danger},
				{name: "dialog title", got: st.dialogTitle.GetForeground(), want: p.fg},
				{name: "warning", got: st.warnText.GetForeground(), want: p.warning},
				{name: "kind badge", got: st.kindBadge.GetForeground(), want: p.fg},
				{name: "spinner", got: st.spinnerStyle.GetForeground(), want: p.accent},
				{name: "selected row", got: st.selectedRow.GetForeground(), want: p.fg},
				{name: "selected row background", got: st.selectedRow.GetBackground(), want: p.chromeHigher},
				{name: "cursor", got: st.cursorText.GetForeground(), want: p.accent},
			}
			for _, check := range checks {
				t.Run(check.name, func(t *testing.T) {
					requireSameColor(t, check.got, check.want)
				})
			}
			requireNoColor(t, st.footer.GetBackground())
			if st.header.GetBold() {
				t.Fatal("header is uniformly bold")
			}
			if !st.activeContext.GetBold() || !st.kindBadge.GetBold() {
				t.Fatal("active context and kind badge must retain weight emphasis")
			}
		})
	}
}

func TestSharedComponentStylesUseSemanticPalette(t *testing.T) {
	for _, theme := range []struct {
		name   string
		isDark bool
	}{
		{name: "light"},
		{name: "dark", isDark: true},
	} {
		t.Run(theme.name, func(t *testing.T) {
			st := newStyles(theme.isDark, newGlyphs(false))
			p := st.palette

			delegate := newListDelegate(st)
			delegateChecks := []struct {
				name string
				got  color.Color
				want color.Color
			}{
				{name: "normal title", got: delegate.Styles.NormalTitle.GetForeground(), want: p.fg},
				{name: "normal description", got: delegate.Styles.NormalDesc.GetForeground(), want: p.fgMuted},
				{name: "selected title", got: delegate.Styles.SelectedTitle.GetForeground(), want: p.fg},
				{name: "selected title background", got: delegate.Styles.SelectedTitle.GetBackground(), want: p.chromeHigher},
				{name: "selected description", got: delegate.Styles.SelectedDesc.GetForeground(), want: p.fgMuted},
				{name: "selected description background", got: delegate.Styles.SelectedDesc.GetBackground(), want: p.chromeHigher},
				{name: "dimmed title", got: delegate.Styles.DimmedTitle.GetForeground(), want: p.fgFaint},
				{name: "dimmed description", got: delegate.Styles.DimmedDesc.GetForeground(), want: p.fgFaint},
			}
			for _, check := range delegateChecks {
				t.Run("delegate "+check.name, func(t *testing.T) {
					requireSameColor(t, check.got, check.want)
				})
			}
			requireNoColor(t, delegate.Styles.FilterMatch.GetForeground())
			if !delegate.Styles.FilterMatch.GetUnderline() {
				t.Fatal("delegate filter match lost its underline")
			}
			defaultSelection := list.NewDefaultDelegate().Styles.SelectedTitle.GetForeground()
			if sameColor(delegate.Styles.SelectedTitle.GetForeground(), defaultSelection) {
				t.Fatal("selected list title retained Bubbles' default selection color")
			}

			model := newListModel(st, packageDefaultKeyMaps.list)
			listChecks := []struct {
				name string
				got  color.Color
				want color.Color
			}{
				{name: "title", got: model.Styles.Title.GetForeground(), want: p.fgMuted},
				{name: "spinner", got: model.Styles.Spinner.GetForeground(), want: p.accent},
				{name: "status", got: model.Styles.StatusBar.GetForeground(), want: p.fgMuted},
				{name: "empty status", got: model.Styles.StatusEmpty.GetForeground(), want: p.fgFaint},
				{name: "active filter status", got: model.Styles.StatusBarActiveFilter.GetForeground(), want: p.fg},
				{name: "filter count", got: model.Styles.StatusBarFilterCount.GetForeground(), want: p.fgFaint},
				{name: "no items", got: model.Styles.NoItems.GetForeground(), want: p.fgMuted},
				{name: "help", got: model.Styles.HelpStyle.GetForeground(), want: p.fgMuted},
				{name: "active page", got: model.Styles.ActivePaginationDot.GetForeground(), want: p.accent},
				{name: "inactive page", got: model.Styles.InactivePaginationDot.GetForeground(), want: p.fgFaint},
				{name: "arabic page", got: model.Styles.ArabicPagination.GetForeground(), want: p.fgMuted},
				{name: "divider", got: model.Styles.DividerDot.GetForeground(), want: p.fgFaint},
			}
			for _, check := range listChecks {
				t.Run("list "+check.name, func(t *testing.T) {
					requireSameColor(t, check.got, check.want)
				})
			}
			requireNoColor(t, model.Styles.DefaultFilterCharacterMatch.GetForeground())
			requireNoColor(t, model.Styles.Title.GetBackground())
			top, right, bottom, left := model.Styles.TitleBar.GetPadding()
			if top != 0 || right != 0 || bottom != 0 || left != 2 {
				t.Fatalf("title bar padding = %d,%d,%d,%d", top, right, bottom, left)
			}
			if !model.Styles.DefaultFilterCharacterMatch.GetUnderline() {
				t.Fatal("list filter match lost its underline")
			}
			if model.Paginator.ActiveDot != st.listStyle.ActivePaginationDot.String() ||
				model.Paginator.InactiveDot != st.listStyle.InactivePaginationDot.String() {
				t.Fatalf("paginator dots = %q, %q; want themed dots %q, %q", model.Paginator.ActiveDot, model.Paginator.InactiveDot, st.listStyle.ActivePaginationDot.String(), st.listStyle.InactivePaginationDot.String())
			}

			requireTextInputPalette(t, model.Styles.Filter, p)
			requireTextInputPalette(t, model.FilterInput.Styles(), p)
			input := newTextInput(st)
			requireTextInputPalette(t, input.Styles(), p)
			spinner := newSpinner(st)
			requireSameColor(t, spinner.Style.GetForeground(), p.accent)
		})
	}
}

func TestRenderStyleAroundANSIRestoresBackground(t *testing.T) {
	st := testStyles(true)
	content := "before " + st.errText.Render("error") + " after"
	rendered := renderStyleAroundANSI(st.selectedRow, content)
	restore := ansi.NewStyle().Bold().ForegroundColor(st.palette.fg).BackgroundColor(st.palette.chromeHigher).String()
	if !strings.Contains(rendered, ansi.ResetStyle+restore+" after") {
		t.Fatalf("selected background was not restored after reset: %q", rendered)
	}
}

func TestStateLinesRemainDistinctWithoutColour(t *testing.T) {
	st := newStyles(false, newGlyphs(true))
	tests := []struct {
		name string
		kind stateLineKind
	}{
		{name: "loading", kind: stateLineLoading},
		{name: "success", kind: stateLineSuccess},
		{name: "error", kind: stateLineError},
		{name: "empty", kind: stateLineEmpty},
		{name: "incomplete", kind: stateLineIncomplete},
		{name: "unknown", kind: stateLineUnknown},
	}
	seen := make(map[string]struct{}, len(tests))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ansi.Strip(renderStateLine(st, test.kind, "message", "", 0))
			want := "[" + test.name + "] message"
			if got != want {
				t.Fatalf("state line = %q, want %q", got, want)
			}
			if _, duplicate := seen[got]; duplicate {
				t.Fatalf("state line %q is not distinct", got)
			}
			seen[got] = struct{}{}
		})
	}
}

func requireTextInputPalette(t *testing.T, got textinput.Styles, palette semanticPalette) {
	t.Helper()
	checks := []struct {
		name string
		got  color.Color
		want color.Color
	}{
		{name: "focused text", got: got.Focused.Text.GetForeground(), want: palette.fg},
		{name: "focused placeholder", got: got.Focused.Placeholder.GetForeground(), want: palette.fgFaint},
		{name: "focused suggestion", got: got.Focused.Suggestion.GetForeground(), want: palette.fgMuted},
		{name: "focused prompt", got: got.Focused.Prompt.GetForeground(), want: palette.accent},
		{name: "blurred text", got: got.Blurred.Text.GetForeground(), want: palette.fgMuted},
		{name: "blurred placeholder", got: got.Blurred.Placeholder.GetForeground(), want: palette.fgFaint},
		{name: "blurred suggestion", got: got.Blurred.Suggestion.GetForeground(), want: palette.fgFaint},
		{name: "blurred prompt", got: got.Blurred.Prompt.GetForeground(), want: palette.fgMuted},
		{name: "cursor", got: got.Cursor.Color, want: palette.accent},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			requireSameColor(t, check.got, check.want)
		})
	}
	if got.Cursor.Shape != tea.CursorBlock || !got.Cursor.Blink {
		t.Fatalf("cursor = shape %v blink %t, want block and blinking", got.Cursor.Shape, got.Cursor.Blink)
	}
}

func rgb(red, green, blue byte) color.RGBA {
	return color.RGBA{R: red, G: green, B: blue, A: 0xFF}
}

func requireColorRGBA(t *testing.T, got color.Color, want color.RGBA) {
	t.Helper()
	if !sameColor(got, want) {
		gotRed, gotGreen, gotBlue, gotAlpha := got.RGBA()
		wantRed, wantGreen, wantBlue, wantAlpha := want.RGBA()
		t.Fatalf("color = rgba(%04x,%04x,%04x,%04x), want rgba(%04x,%04x,%04x,%04x)", gotRed, gotGreen, gotBlue, gotAlpha, wantRed, wantGreen, wantBlue, wantAlpha)
	}
}

func requireNoColor(t *testing.T, got color.Color) {
	t.Helper()
	if _, ok := got.(lipgloss.NoColor); !ok {
		t.Fatalf("color = %#v, want no color", got)
	}
}

func requireSameColor(t *testing.T, got, want color.Color) {
	t.Helper()
	if !sameColor(got, want) {
		gotRed, gotGreen, gotBlue, gotAlpha := got.RGBA()
		wantRed, wantGreen, wantBlue, wantAlpha := want.RGBA()
		t.Fatalf("color = rgba(%04x,%04x,%04x,%04x), want rgba(%04x,%04x,%04x,%04x)", gotRed, gotGreen, gotBlue, gotAlpha, wantRed, wantGreen, wantBlue, wantAlpha)
	}
}

func sameColor(left, right color.Color) bool {
	leftRed, leftGreen, leftBlue, leftAlpha := left.RGBA()
	rightRed, rightGreen, rightBlue, rightAlpha := right.RGBA()
	return leftRed == rightRed && leftGreen == rightGreen && leftBlue == rightBlue && leftAlpha == rightAlpha
}

func TestRedactServerUserinfoStripsCredentialsForDisplay(t *testing.T) {
	tests := []struct {
		name, server, want string
	}{
		{name: "no userinfo", server: "https://api.example:6443", want: "https://api.example:6443"},
		{name: "user and password", server: "https://user:secret@api.example:6443", want: "https://api.example:6443"}, // #nosec G101 -- fabricated credential exercising the redaction path.
		{name: "user only", server: "https://token@api.example/prefix", want: "https://api.example/prefix"},
		{name: "empty", server: "", want: ""},
		{name: "unparseable with userinfo", server: "https://user:se%zzcret@api.example/a", want: "https://api.example/a"}, // #nosec G101 -- fabricated credential exercising the redaction path.
		{name: "unparseable without userinfo", server: "https://api.example/%zz", want: "https://api.example/%zz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := redactServerUserinfo(test.server); got != test.want {
				t.Fatalf("redactServerUserinfo(%q) = %q, want %q", test.server, got, test.want)
			}
		})
	}
}

func TestServerDisplayHelpersNeverEchoCredentials(t *testing.T) {
	const server = "https://user:hunter2@api.example:6443" // #nosec G101 -- fabricated credential; the test proves it is never rendered.
	for name, got := range map[string]string{
		"serverOrUnverified": serverOrUnverified(server),
		"displayServer":      displayServer(server),
		"identity lines":     strings.Join(clusterIdentityLines("ctx", server, 0, " - "), " "),
		"header bar":         renderHeaderBar(lipgloss.NewStyle(), lipgloss.NewStyle(), "ctx", server, []string{"screen"}, 200, "..."),
	} {
		if strings.Contains(got, "hunter2") || strings.Contains(got, "user:") {
			t.Errorf("%s rendered credentials: %q", name, got)
		}
		if !strings.Contains(got, "api.example") {
			t.Errorf("%s lost the host identity: %q", name, got)
		}
	}
}
