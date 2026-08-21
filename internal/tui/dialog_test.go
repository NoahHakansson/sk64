package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestTextInputWidthUsesDisplayCells(t *testing.T) {
	input := newTextInput(testStyles(true))
	input.Prompt = lipgloss.NewStyle().Bold(true).Render("界: ")
	input.SetWidth(textInputWidth(12, input.Prompt))
	if got := lipgloss.Width(input.View()); got > 12 {
		t.Fatalf("input view width = %d, want <= 12 for four-cell prompt %q", got, input.Prompt)
	}
}

func TestResponsiveBoxWidths(t *testing.T) {
	frameSize := testStyles(true).dialogBox.GetHorizontalFrameSize()
	availableWidths := []int{60, 80, 120, 160}
	tests := []struct {
		name                         string
		preferred, percent, widthCap int
		want                         []int
	}{
		{name: "context", preferred: contextPreferredWidth, percent: contextWidthPercent, widthCap: contextMaxWidth, want: []int{56, 60, 84, 96}},
		{name: "project", preferred: projectPreferredWidth, percent: projectWidthPercent, widthCap: projectMaxWidth, want: []int{56, 70, 90, 120}},
		{name: "help", preferred: helpPreferredWidth, percent: helpWidthPercent, widthCap: helpMaxWidth, want: []int{56, 72, 78, 84}},
		{name: "dialog", preferred: dialogPreferredWidth, percent: dialogWidthPercent, widthCap: dialogMaxWidth, want: []int{56, 72, 84, 96}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i, availableWidth := range availableWidths {
				outerWidth, innerWidth := responsiveBoxWidths(availableWidth, test.preferred, test.percent, test.widthCap, frameSize)
				if outerWidth != test.want[i] {
					t.Fatalf("width %d outer = %d, want %d", availableWidth, outerWidth, test.want[i])
				}
				if innerWidth != outerWidth-frameSize {
					t.Fatalf("width %d inner = %d, want outer %d - frame %d", availableWidth, innerWidth, outerWidth, frameSize)
				}
			}
		})
	}
}

func TestDialogFillsBodyRectangle(t *testing.T) {
	content := dialogContent{
		title: "Confirm the change",
		body: []string{
			"namespace default",
			"context test-ctx",
			"first detail",
			"second detail",
			"third detail",
		},
		warnings: []string{
			"This is a long warning that wraps when the dialog is narrow.",
			"This is another long warning that also needs room to wrap.",
		},
		prompt:  "Y confirm  esc cancel",
		message: "press Y (shift+y) to confirm",
	}
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{name: "standard", width: 80, height: 22},
		{name: "minimum", width: 60, height: 13},
		{name: "large", width: 200, height: 60},
	} {
		t.Run(size.name, func(t *testing.T) {
			renderer := newDialog(testStyles(true), false)
			renderer.resize(size.width, size.height)
			lines := strings.Split(renderer.render(content), "\n")
			if len(lines) != size.height {
				t.Fatalf("line count = %d, want %d", len(lines), size.height)
			}
			for i, line := range lines {
				if width := ansi.StringWidth(line); width != size.width {
					t.Fatalf("line %d width = %d, want %d", i, width, size.width)
				}
			}
		})
	}
}

func TestDialogClampsToBodyHeight(t *testing.T) {
	renderer := newDialog(testStyles(true), false)
	renderer.resize(60, 13)
	body := make([]string, 20)
	for i := range body {
		body[i] = fmt.Sprintf("body line %d", i)
	}
	got := renderer.render(dialogContent{
		title:    "Confirm",
		body:     body,
		warnings: []string{"first long warning that wraps", "second long warning that wraps", "third long warning that wraps"},
		prompt:   "Y confirm",
		message:  "message",
	})
	if lines := strings.Count(got, "\n") + 1; lines != 13 {
		t.Fatalf("line count = %d, want 13", lines)
	}
}

func TestDialogKeepsPromptAndMessage(t *testing.T) {
	renderer := newDialog(testStyles(true), false)
	renderer.resize(60, 13)
	body := make([]string, 20)
	for i := range body {
		body[i] = fmt.Sprintf("body line %d", i)
	}
	got := ansi.Strip(renderer.render(dialogContent{
		title:   "Confirm",
		body:    body,
		prompt:  "Y confirms this action",
		message: "press Y to continue",
	}))
	if !strings.Contains(got, "Y confirms this action") || !strings.Contains(got, "press Y to continue") {
		t.Fatalf("prompt or message dropped:\n%s", got)
	}
	if strings.Contains(got, body[len(body)-1]) {
		t.Fatalf("last body line survived:\n%s", got)
	}
}

func TestDialogTitleRuleAndWarningMessage(t *testing.T) {
	st := testStyles(true)
	renderer := newDialog(st, false)
	renderer.resize(80, 20)
	rule := st.dim.Render(strings.Repeat(st.glyphs.ruleMarker, renderer.contentWidth()))
	view := renderer.render(dialogContent{title: "Confirm save", message: "press Y to confirm", isWarning: true})
	if !strings.Contains(view, rule) {
		t.Fatalf("dialog lost dim title rule: %q", view)
	}
	if warning := strings.TrimSuffix(st.warnText.Render("press Y to confirm"), "\x1b[m"); !strings.Contains(view, warning) {
		t.Fatalf("dialog warning message = %q, want styled run %q", view, warning)
	}

	renderer.resize(80, 7)
	pressured := ansi.Strip(renderer.render(dialogContent{title: "Confirm save", prompt: "Y confirm"}))
	if !strings.Contains(pressured, "Confirm save") {
		t.Fatalf("height pressure dropped title text: %q", pressured)
	}
	if strings.Contains(pressured, "|  "+strings.Repeat(st.glyphs.ruleMarker, renderer.contentWidth())+"  |") {
		t.Fatalf("height pressure retained rule before title text: %q", pressured)
	}
}

func TestDialogJoinsUseModeSeparator(t *testing.T) {
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		t.Run(fmt.Sprintf("ascii=%t", ascii), func(t *testing.T) {
			identity := commitIdentityLines("save", "Secret", "default", "credentials", "ctx", "https://api.example", 200, st.glyphs.separator, "key password")
			joined := strings.Join(identity, "\n")
			for _, want := range []string{"save Secret default/credentials" + st.glyphs.separator + "key password", "context ctx" + st.glyphs.separator + "server https://api.example"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("identity = %q, want %q", joined, want)
				}
			}
			wantRow := "type Opaque" + st.glyphs.separator + "2 keys" + st.glyphs.separator + "immutable"
			if row := compactDialogRow([]string{"type Opaque", "2 keys", "immutable"}, 80, st.glyphs.ellipsis, st.glyphs.separator); row != wantRow {
				t.Fatalf("compact row = %q, want %q", row, wantRow)
			}
		})
	}
}

func TestDialogKeepsCommitIdentityAndCountedCue(t *testing.T) {
	renderer := newDialog(testStyles(true), true)
	renderer.resize(60, 13)
	identity := commitIdentityLines(
		"delete",
		"Secret",
		"default",
		"app-credentials",
		"production",
		"https://api.example:6443",
		renderer.contentWidth(),
		renderer.styles.glyphs.separator,
	)
	got := ansi.Strip(renderer.render(dialogContent{
		title:    "Delete Secret app-credentials",
		identity: identity,
		body:     []string{"type Opaque", "1 key", "checking consumers...", "optional evidence"},
		warnings: []string{"Permanent. ctrl+z cannot restore a deleted resource."},
		prompt:   "type app-credentials to confirm\nconfirm: app-credentials",
	}))
	for _, want := range []string{
		"delete Secret default/app-credentials",
		"context production",
		"server https://api.example:6443",
		"...and 4 more",
		"type app-credentials to confirm",
		"confirm: app-credentials",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("height-pressured dialog lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "optional evidence") {
		t.Fatalf("optional evidence survived height pressure:\n%s", got)
	}
}

func TestDialogKeepsCriticalWarningWithIdentity(t *testing.T) {
	renderer := newDialog(testStyles(true), true)
	renderer.resize(60, 13)
	got := ansi.Strip(renderer.render(dialogContent{
		title: "Export Secret key",
		identity: commitIdentityLines(
			"export",
			"Secret",
			"default",
			"app-credentials",
			"production",
			"https://api.example:6443",
			renderer.contentWidth(),
			renderer.styles.glyphs.separator,
			"key password",
		),
		body:             []string{"decoded size 1 KiB", "relative paths use the startup directory", "existing files are not overwritten"},
		criticalWarnings: []string{"This writes the plaintext secret to disk. sk64 never removes it."},
		prompt:           "path: password",
	}))
	for _, want := range []string{
		"export Secret default/app-credentials",
		"key password",
		"context production",
		"api.example:6443",
		"plaintext secret",
		"...and 3 more",
		"path: password",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("critical commit content lost %q:\n%s", want, got)
		}
	}
}

func TestDialogCompactsMaximumCommitIdentityWithinHeight(t *testing.T) {
	longName := strings.Repeat("n", 249) + "-one"
	longKey := strings.Repeat("k", 246) + "keytail"
	longContext := strings.Repeat("context-segment-", 10) + "production"
	server := "https://gateway.example/" + strings.Repeat("shared-path/", 12) + "clusters/one"
	tests := []struct {
		name                          string
		terminalWidth, terminalHeight int
	}{
		{name: "minimum terminal", terminalWidth: 60, terminalHeight: 15},
		{name: "standard terminal", terminalWidth: 80, terminalHeight: 24},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			renderer := newDialog(testStyles(true), true)
			renderer.resize(test.terminalWidth, bodyHeight(test.terminalHeight))
			view := renderer.render(dialogContent{
				title: "Delete Secret " + longName,
				identity: commitIdentityLines(
					"delete",
					"Secret",
					"default",
					longName,
					longContext,
					server,
					renderer.contentWidth(),
					renderer.styles.glyphs.separator,
					"key "+longKey,
				),
				body:             []string{"optional evidence", "more optional evidence"},
				criticalWarnings: []string{"Permanent. ctrl+z cannot restore a deleted resource."},
				warnings:         []string{"optional warning"},
				prompt:           "type " + longName + " to confirm\nconfirm: ready",
				message:          "optional message",
			})
			plainView := ansi.Strip(view)
			assignedHeight := bodyHeight(test.terminalHeight)
			if got := lipgloss.Height(view); got != assignedHeight {
				t.Fatalf("dialog height = %d, want %d:\n%s", got, assignedHeight, plainView)
			}
			assertRenderedLinesFitWidth(t, view, test.terminalWidth)
			assertRenderedLineContains(t, plainView, "server ", "gateway.example")
			if test.terminalWidth == 60 {
				for _, want := range []string{"keytail", "production", "Permanent.", "type ", "to confirm", "confirm: ready"} {
					if !strings.Contains(plainView, want) {
						t.Fatalf("minimum dialog lost %q:\n%s", want, plainView)
					}
				}
			}
		})
	}
}

func TestClusterIdentityLines(t *testing.T) {
	tests := []struct {
		name        string
		contextName string
		server      string
		width       int
		want        []string
	}{
		{
			name:        "fits combined",
			contextName: "test",
			server:      "https://api.example",
			width:       64,
			want:        []string{"context test - server https://api.example"},
		},
		{
			name:        "fits on its own line",
			contextName: "production",
			server:      "https://api.example",
			width:       32,
			want:        []string{"context production", "server https://api.example"},
		},
		{
			name:   "empty context",
			server: "https://api.example",
			width:  64,
			want:   []string{"context unknown - server https://api.example"},
		},
		{
			name:        "empty server",
			contextName: "test",
			width:       64,
			want:        []string{"context test - server unknown"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := clusterIdentityLines(test.contextName, test.server, test.width, " - ")
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("clusterIdentityLines() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClusterIdentityLongServerWrapsLosslessly(t *testing.T) {
	servers := []string{
		"https://gateway.example:6443/clusters/production/a/very/long/apiserver/path",
		"https://api.example:65535/prefix/with/a/long/path?cluster=production#endpoint",
		"https://例え.テスト:6443/clusters/本番/a/very/long/apiserver/path",
		"https://example.test/clusters/éndpoint/with/a/very/long/path",
	}
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		for _, server := range servers {
			for _, width := range []int{20, 30, 40, 50, 60, 70} {
				t.Run(fmt.Sprintf("ascii=%t/width=%d/server=%s", ascii, width, serverHost(server)), func(t *testing.T) {
					identity := clusterIdentityLines("production", server, width, st.glyphs.separator)
					if got := reassembleClusterServer(t, identity); got != server {
						t.Fatalf("reassembled server = %q, want %q from %q", got, server, identity)
					}
					for _, line := range identity {
						if got := lipgloss.Width(line); got > width {
							t.Fatalf("identity line width = %d, want <= %d: %q", got, width, line)
						}
					}
					if rendered := strings.Join(identity, "\n"); strings.Contains(rendered, "...") || strings.Contains(rendered, "…") || strings.Contains(rendered, st.glyphs.ellipsis) {
						t.Fatalf("wrapped server contains an ellipsis: %q", identity)
					}
				})
			}
		}
	}
}

func reassembleClusterServer(t *testing.T, identity []string) string {
	t.Helper()
	for i, line := range identity {
		if index := strings.Index(line, "server "); index >= 0 {
			var server strings.Builder
			server.WriteString(line[index+len("server "):])
			for _, continuation := range identity[i+1:] {
				server.WriteString(continuation)
			}
			return server.String()
		}
	}
	t.Fatalf("cluster identity has no server line: %q", identity)
	return ""
}

func TestDialogDistinguishesPathPrefixedServersAtMinimumWidth(t *testing.T) {
	render := func(server string) string {
		renderer := newDialog(testStyles(true), true)
		renderer.resize(60, 13)
		return ansi.Strip(renderer.render(dialogContent{
			title: "Confirm save",
			identity: commitIdentityLines(
				"save",
				"Secret",
				"default",
				"app-credentials",
				"production",
				server,
				renderer.contentWidth(),
				renderer.styles.glyphs.separator,
			),
			prompt: "Y save  esc cancel",
		}))
	}

	serverOne := "https://gateway.example/clusters/one"
	serverTwo := "https://gateway.example/clusters/two"
	one := render(serverOne)
	two := render(serverTwo)
	if one == two {
		t.Fatalf("path-prefixed server dialogs are identical:\n%s", one)
	}
	for _, rendered := range []struct {
		name   string
		server string
		view   string
	}{
		{name: "one", server: serverOne, view: one},
		{name: "two", server: serverTwo, view: two},
	} {
		t.Run(rendered.name, func(t *testing.T) {
			if !strings.Contains(rendered.view, "server "+rendered.server) {
				t.Fatalf("dialog lost server %q:\n%s", rendered.server, rendered.view)
			}
			assertRenderedLinesFitWidth(t, rendered.view, 60)
		})
	}
}

func assertRenderedLineContains(t *testing.T, view, anchor, want string) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, anchor) {
			if !strings.Contains(line, want) {
				t.Fatalf("line containing %q lost %q: %q", anchor, want, line)
			}
			return
		}
	}
	t.Fatalf("rendered output has no line containing %q:\n%s", anchor, view)
}

func assertRenderedLinesFitWidth(t *testing.T, view string, width int) {
	t.Helper()
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, width, line)
		}
	}
}

func TestDialogDropsWholeWrappedBodyEntries(t *testing.T) {
	renderer := newDialog(testStyles(true), false)
	renderer.resize(40, 14)
	first := "first body entry stays complete when it wraps"
	second := "second body entry is removed as one logical unit"
	got := ansi.Strip(renderer.render(dialogContent{
		title:   "Confirm",
		body:    []string{first, second},
		prompt:  "Y confirm",
		message: "message",
	}))
	for _, line := range wrapDialogLines(first, renderer.contentWidth()) {
		if !strings.Contains(got, line) {
			t.Fatalf("retained body entry lost wrapped row %q:\n%s", line, got)
		}
	}
	for _, line := range wrapDialogLines(second, renderer.contentWidth()) {
		if strings.Contains(got, line) {
			t.Fatalf("dropped body entry retained wrapped row %q:\n%s", line, got)
		}
	}
}

func TestDialogPrioritizesControlsUnderHeightPressure(t *testing.T) {
	renderer := newDialog(testStyles(true), false)
	renderer.resize(60, 13)
	controls := []string{
		"name: api",
		"path: /repos/api",
		"context: production",
		"namespaces: default, staging",
		"[x] restart deployments",
		"[ ] restart statefulsets",
		"enter save  esc cancel",
	}
	got := ansi.Strip(renderer.render(dialogContent{
		title:    "Create or edit a project with settings that require another title row",
		body:     []string{"first optional detail", "second optional detail", "third optional detail"},
		warnings: []string{"first warning that can be shortened", "second warning that can be shortened"},
		prompt:   strings.Join(controls, "\n"),
		message:  "name or path already used",
	}))
	for _, control := range controls {
		if !strings.Contains(got, control) {
			t.Fatalf("control %q was dropped:\n%s", control, got)
		}
	}
	if !strings.Contains(got, "name or path already used") {
		t.Fatalf("message was dropped:\n%s", got)
	}
	if lines := strings.Count(got, "\n") + 1; lines != 13 {
		t.Fatalf("line count = %d, want 13", lines)
	}
}

func TestDialogUnsizedDoesNotPanic(t *testing.T) {
	renderer := newDialog(testStyles(true), true)
	got := renderer.render(dialogContent{
		title:    "Delete Secret",
		body:     []string{"namespace default"},
		warnings: []string{"Permanent"},
		prompt:   "Y confirm",
	})
	if got == "" || !strings.Contains(got, "Delete Secret") {
		t.Fatalf("unsized dialog = %q", got)
	}
	if strings.ContainsAny(got, "+╭") {
		t.Fatalf("unsized dialog contains a border: %q", got)
	}
}

func TestDialogDangerUsesADifferentBorderColour(t *testing.T) {
	content := dialogContent{title: "Confirm", body: []string{"same content"}}
	st := testStyles(false)
	normal := newDialog(st, false)
	normal.resize(80, 22)
	danger := newDialog(st, true)
	danger.resize(80, 22)
	normalView := normal.render(content)
	dangerView := danger.render(content)
	if normalView == dangerView {
		t.Fatal("normal and danger dialogs are identical")
	}
	if ansi.Strip(normalView) != ansi.Strip(dangerView) {
		t.Fatal("normal and danger dialog text differs")
	}

	brandRed, brandGreen, brandBlue, _ := st.palette.brand.RGBA()
	dangerRed, dangerGreen, dangerBlue, _ := st.palette.danger.RGBA()
	brandANSI := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", brandRed>>8, brandGreen>>8, brandBlue>>8)
	dangerANSI := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", dangerRed>>8, dangerGreen>>8, dangerBlue>>8)
	if !strings.Contains(normalView, brandANSI) || strings.Contains(normalView, dangerANSI) {
		t.Fatalf("normal dialog does not use only its brand border colour: %q", normalView)
	}
	if !strings.Contains(dangerView, dangerANSI) || strings.Contains(dangerView, brandANSI) {
		t.Fatalf("danger dialog does not use only its danger border colour: %q", dangerView)
	}
}
