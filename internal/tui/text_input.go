package tui

import (
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
)

// makeTextInput exists so TestMain can disable the blinking virtual cursor,
// which otherwise emits nondeterministic tick commands into the golden
// harness. Production keeps textinput.New and its cursor; do not delete this
// indirection, and do not make it configurable at runtime.
var makeTextInput = textinput.New

func newTextInput(st *styles) textinput.Model {
	input := makeTextInput()
	applyTextInputStyles(&input, st)
	return input
}

func applyTextInputStyles(input *textinput.Model, st *styles) {
	input.SetStyles(st.textInputStyle)
}

func newSpinner(st *styles) spinner.Model {
	model := spinner.New()
	applySpinnerStyle(&model, st)
	return model
}

func applySpinnerStyle(model *spinner.Model, st *styles) {
	model.Spinner = st.glyphs.spinner
	model.Style = st.spinnerStyle
}
