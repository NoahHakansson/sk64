package tui

import (
	"context"
	"slices"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"k8s.io/apimachinery/pkg/util/validation"
)

type keyNamePrompt struct {
	dialog
	ctx     context.Context
	client  *k8s.Client
	env     editEnv
	res     k8s.Resource
	input   textinput.Model
	message string
}

func newKeyNamePrompt(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, st *styles) *keyNamePrompt {
	input := newTextInput(st)
	input.Prompt = "name: "
	return &keyNamePrompt{dialog: newDialog(st, false), ctx: ctx, client: client, env: env, res: res, input: input}
}

func (s *keyNamePrompt) Init() tea.Cmd { return s.input.Focus() }

func (s *keyNamePrompt) Update(msg tea.Msg) (screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case bubbleskey.Matches(key, bindEsc):
			return s, popScreen()
		case bubbleskey.Matches(key, bindEnter):
			name := strings.TrimSpace(s.input.Value())
			validationMessages := validation.IsConfigMapKey(name)
			switch {
			case name == "":
				s.message = "key name is required"
			case len(validationMessages) != 0:
				s.message = validationMessages[0]
			case slices.Contains(s.res.Keys(), name):
				s.message = "key already exists"
			default:
				ctx, client, env, res, st := s.ctx, s.client, s.env, s.res, s.styles
				return s, func() tea.Msg {
					return replaceScreenMsg{s: newKeyAddFlow(ctx, client, env, res, name, st)}
				}
			}
			return s, nil
		}
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

func (s *keyNamePrompt) View() string {
	return s.render(dialogContent{
		title: "New key in " + s.res.Kind() + " " + s.res.Name(),
		body: []string{
			"namespace " + s.res.Namespace(),
			"key names may contain letters, digits and - _ .",
			"enter opens $EDITOR so you can type the value",
		},
		prompt:  s.input.View(),
		message: s.message,
		isError: true,
	})
}

func (s *keyNamePrompt) SetSize(width, height int) {
	s.resize(width, height)
	s.input.SetWidth(textInputWidth(s.contentWidth(), s.input.Prompt))
}
func (s *keyNamePrompt) SetStyles(st *styles) {
	s.styles = st
	applyTextInputStyles(&s.input, st)
}
func (s *keyNamePrompt) Title() string { return s.res.Name() + " (new key)" }
func (s *keyNamePrompt) Hints() footerHints {
	return hintBindings(hintDesc(bindEnter, "edit value"), bindEsc)
}
func (s *keyNamePrompt) Help() helpGroup     { return helpGroup{} }
func (s *keyNamePrompt) CapturesInput() bool { return true }
func (s *keyNamePrompt) WantsEsc() bool      { return true }
