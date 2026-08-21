package tui

import (
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

const confirmGateWord = "YES"

// confirmGate is the shared typed commit gate: every user-triggered mutation
// dispatches only after the user literally types YES and presses enter, and
// esc always closes the gate with nothing done. Hosts embed it in the screen
// that owns the flow and render promptLines inside their dialog prompt, which
// dialog.render never drops.
type confirmGate struct {
	input   textinput.Model
	message string
}

func newConfirmGate(st *styles) confirmGate {
	input := newTextInput(st)
	input.Prompt = "confirm: "
	return confirmGate{input: input}
}

// arm clears previous input and focuses the gate for a fresh confirmation.
func (g *confirmGate) arm() tea.Cmd {
	g.input.SetValue("")
	g.message = ""
	return g.input.Focus()
}

// handleKey consumes one keypress while the gate is armed. confirmed is true
// only for enter with the exact uppercase word; every near miss answers with
// a corrective message instead of silence. esc is not handled here so hosts
// keep their own cancel semantics.
func (g *confirmGate) handleKey(msg tea.KeyPressMsg) (confirmed bool, cmd tea.Cmd) {
	if bubbleskey.Matches(msg, bindEnter) {
		typed := strings.TrimSpace(g.input.Value())
		switch {
		case typed == confirmGateWord:
			g.message = ""
			return true, nil
		case strings.EqualFold(typed, confirmGateWord) || strings.EqualFold(typed, "y"):
			g.message = "type " + confirmGateWord + " in capitals to confirm"
		default:
			g.message = "type " + confirmGateWord + " to confirm"
		}
		return false, nil
	}
	g.input, cmd = g.input.Update(msg)
	return false, cmd
}

// promptLines renders the gate instruction and input, ready for a
// dialogContent prompt, which dialog.render never drops.
func (g *confirmGate) promptLines(st *styles, danger bool) string {
	wordStyle := st.warnText.Bold(true)
	if danger {
		wordStyle = st.errText.Bold(true)
	}
	return "type " + wordStyle.Render(confirmGateWord) + " and press enter to confirm\n" + g.input.View()
}

func (g *confirmGate) setStyles(st *styles) {
	applyTextInputStyles(&g.input, st)
}

func (g *confirmGate) setWidth(contentWidth int) {
	g.input.SetWidth(textInputWidth(contentWidth, g.input.Prompt))
}
