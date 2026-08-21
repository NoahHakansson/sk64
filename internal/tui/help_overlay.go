package tui

import (
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type helpGroupEntry struct {
	binding bubbleskey.Binding
	desc    string
}

type helpGroup struct {
	title   string
	entries []helpGroupEntry
}

const (
	helpKeyColumn         = 12
	helpPreferredWidth    = 72
	helpWidthPercent      = 65
	helpMaxWidth          = 84
	helpChromeHeight      = 6
	helpDescriptionIndent = 2 + helpKeyColumn
	helpNoteIndent        = 2
)

type helpOverlay struct {
	styles       *styles
	km           helpOverlayKeyMap
	movementHelp string
	viewport     viewport.Model
	sections     []helpGroup
	notes        []string
	closed       bool
	content      string
	boxWidth     int
	contentWidth int
}

func newHelpOverlay(top screen, env editEnv, st *styles) *helpOverlay {
	viewportModel := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	viewportModel.KeyMap = env.keymaps().viewport
	return &helpOverlay{
		styles:       st,
		km:           env.keymaps().helpOverlay,
		movementHelp: env.keymaps().viewportMovementHelp,
		viewport:     viewportModel,
		sections:     []helpGroup{top.Help(), globalHelpGroup(env.keymaps())},
		notes:        helpNotes(env, st.glyphs.separator),
	}
}

func globalHelpGroup(km *keyMaps) helpGroup {
	quit := displayHint(km.global.Quit.Help().Key+" / "+km.global.ConfirmQuit.Help().Key, "quit")
	movementDesc := "move the cursor"
	if km.movementHelp == "h/j/k/l" {
		movementDesc = "same as the arrow keys"
	}
	return helpGroup{title: "global", entries: []helpGroupEntry{
		{binding: km.global.Help, desc: "this help"},
		{binding: km.global.Back, desc: "back / close / cancel the in-flight request"},
		{binding: displayHint(km.movementHelp, "move"), desc: movementDesc},
		{binding: km.global.Filter, desc: "filter the current list"},
		{binding: km.global.Search, desc: "search resource and key names cluster-wide"},
		{binding: hintDesc(km.namespace.Refresh, "refresh"), desc: "refresh the current screen"},
		{binding: km.global.ContextSwitch, desc: "switch kube context"},
		{binding: km.global.ProjectSwitch, desc: "switch project"},
		{binding: displayHint("ctrl+z", "undo"), desc: "undo the last save (key list)"},
		{binding: displayHint("L", "link"), desc: "link the item under the cursor to a project"},
		{binding: quit, desc: "quit (ctrl+c always requires two quick presses)"},
	}}
}

func helpNotes(env editEnv, separator string) []string {
	notes := []string{
		"session undo (ctrl+z) is in RAM only" + separator + "it dies with sk64",
		"value search (" + env.keymaps().keyScreen.ValueSearch.Help().Key + ") checks this resource, never the cluster",
		"destructive confirms need uppercase Y; esc always cancels",
	}
	if env.readOnly {
		notes = append(notes, "read-only mode: create, edit, delete and rollout disabled")
	}
	return notes
}

func renderHelp(sections []helpGroup, notes []string, st *styles, width int) string {
	lines := []string{st.dialogTitle.Render("sk64" + st.glyphs.separator + "keys")}
	for _, section := range sections {
		if len(section.entries) == 0 {
			continue
		}
		lines = append(lines, "", st.dialogTitle.Render(section.title))
		for _, entry := range section.entries {
			if !entry.binding.Enabled() {
				continue
			}
			descriptionLines := strings.Split(ansi.Wrap(entry.desc, max(1, width-helpDescriptionIndent), ""), "\n")
			for i, line := range descriptionLines {
				prefix := strings.Repeat(" ", helpDescriptionIndent)
				if i == 0 {
					key := entry.binding.Help().Key
					prefix = "  " + st.footerKey.Render(key+strings.Repeat(" ", max(1, helpKeyColumn-lipgloss.Width(key))))
				}
				lines = append(lines, prefix+st.dim.Render(line))
			}
		}
	}
	lines = append(lines, "", st.dialogTitle.Render("notes"))
	for _, note := range notes {
		for _, line := range strings.Split(ansi.Wrap(note, max(1, width-helpNoteIndent), ""), "\n") {
			lines = append(lines, strings.Repeat(" ", helpNoteIndent)+st.dim.Render(line))
		}
	}
	return strings.Join(lines, "\n")
}

func (o *helpOverlay) Init() tea.Cmd { return nil }

func (o *helpOverlay) Update(msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case bubbleskey.Matches(key, o.km.Close):
			o.closed = true
			return nil
		}
	}
	var cmd tea.Cmd
	o.viewport, cmd = o.viewport.Update(msg)
	return cmd
}

func (o *helpOverlay) View() string {
	return o.styles.helpBox.Width(o.boxWidth).Render(o.viewport.View())
}

func (o *helpOverlay) SetSize(width, height int) {
	wasAtBottom := o.viewport.AtBottom() && o.viewport.YOffset() > 0
	previousYOffset := o.viewport.YOffset()

	boxWidth, contentWidth := responsiveBoxWidths(
		width,
		helpPreferredWidth,
		helpWidthPercent,
		helpMaxWidth,
		o.styles.helpBox.GetHorizontalFrameSize(),
	)
	o.boxWidth = boxWidth
	if contentWidth != o.contentWidth {
		o.contentWidth = contentWidth
		o.content = renderHelp(o.sections, o.notes, o.styles, contentWidth)
		o.viewport.SetWidth(contentWidth)
		o.viewport.SetContent(o.content)
	}
	lineCount := strings.Count(o.content, "\n") + 1
	o.viewport.SetHeight(max(1, min(lineCount, height-helpChromeHeight)))
	if wasAtBottom {
		o.viewport.GotoBottom()
	} else {
		o.viewport.SetYOffset(previousYOffset)
	}
}

func (o *helpOverlay) SetStyles(st *styles) { o.styles = st }
func (o *helpOverlay) Hints() footerHints {
	return hintBindings(displayHint(o.movementHelp, "scroll"), o.km.Close)
}
func (o *helpOverlay) isClosed() bool { return o.closed }
