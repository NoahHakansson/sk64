package tui

import (
	"context"
	"fmt"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"k8s.io/apimachinery/pkg/util/validation"
)

type createStep int

const (
	stepKind createStep = iota
	stepName
	stepType
)

var createKinds = []string{k8s.KindSecret, k8s.KindConfigMap}

// secretTypeRequirements maps a well-known Secret type to the keys Kubernetes
// requires for it. Sourced from internal/k8s/validate.go.
var secretTypeRequirements = map[string]string{
	"Opaque":                         "no required keys",
	"kubernetes.io/tls":              "requires tls.crt and tls.key",
	"kubernetes.io/dockerconfigjson": "requires .dockerconfigjson",
	"kubernetes.io/basic-auth":       "requires username and password",
	"kubernetes.io/ssh-auth":         "requires ssh-privatekey",
}

type createPrompt struct {
	dialog
	ctx       context.Context
	client    *k8s.Client
	env       editEnv
	km        createPromptKeyMap
	namespace string
	existing  []k8s.Resource
	step      createStep
	kind      string
	cursor    int
	input     textinput.Model
	message   string
}

func newCreatePrompt(ctx context.Context, client *k8s.Client, env editEnv, namespace string, existing []k8s.Resource, st *styles) *createPrompt {
	input := newTextInput(st)
	input.Prompt = "name: "
	prompt := &createPrompt{dialog: newDialog(st, false), ctx: ctx, client: client, env: env, km: env.keymaps().createPrompt, namespace: namespace, existing: existing, input: input}
	if env.noConfigMaps {
		prompt.setStep(stepName)
		prompt.kind = k8s.KindSecret
	}
	return prompt
}

func (s *createPrompt) Init() tea.Cmd {
	if s.step == stepName {
		return s.input.Focus()
	}
	return nil
}

func (s *createPrompt) Update(msg tea.Msg) (screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch s.step {
		case stepKind:
			return s.updateChoice(key, len(createKinds), func() tea.Cmd {
				s.kind = createKinds[s.cursor]
				s.setStep(stepName)
				s.message = ""
				return s.input.Focus()
			}, popScreen)
		case stepName:
			switch {
			case bubbleskey.Matches(key, s.km.Cancel):
				if s.env.noConfigMaps {
					return s, popScreen()
				}
				s.setStep(stepKind)
				s.kind = ""
				s.message = ""
				s.input.Blur()
				return s, nil
			case bubbleskey.Matches(key, s.km.Choose):
				return s, s.acceptName()
			}
		case stepType:
			return s.updateChoice(key, len(k8s.WellKnownSecretTypes()), func() tea.Cmd {
				name := strings.TrimSpace(s.input.Value())
				resource := k8s.NewEmptySecret(s.namespace, name, k8s.WellKnownSecretTypes()[s.cursor])
				ctx, client, env, st := s.ctx, s.client, s.env, s.styles
				return func() tea.Msg {
					return replaceScreenMsg{s: newResourceCreateFlow(ctx, client, env, resource, st)}
				}
			}, func() tea.Cmd {
				s.setStep(stepName)
				s.message = ""
				return s.input.Focus()
			})
		}
	}
	if s.step == stepName {
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s *createPrompt) setStep(step createStep) {
	s.step = step
	s.cursor = 0
}

func (s *createPrompt) updateChoice(key tea.KeyPressMsg, count int, accept, back func() tea.Cmd) (screen, tea.Cmd) {
	if count == 0 {
		return s, nil
	}
	s.cursor = min(max(s.cursor, 0), count-1)
	switch {
	case bubbleskey.Matches(key, s.km.Up):
		if s.cursor > 0 {
			s.cursor--
		}
	case bubbleskey.Matches(key, s.km.Down):
		if s.cursor+1 < count {
			s.cursor++
		}
	case bubbleskey.Matches(key, s.km.Choose):
		return s, accept()
	case bubbleskey.Matches(key, s.km.Cancel):
		return s, back()
	}
	return s, nil
}

func (s *createPrompt) acceptName() tea.Cmd {
	name := strings.TrimSpace(s.input.Value())
	if name == "" {
		s.message = "name is required"
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) != 0 {
		s.message = errs[0]
		return nil
	}
	for _, resource := range s.existing {
		if resource.Kind() == s.kind && resource.Name() == name {
			s.message = fmt.Sprintf("a %s named %q already exists", strings.ToLower(s.kind), name)
			return nil
		}
	}
	if s.kind == k8s.KindConfigMap {
		resource := k8s.NewEmptyConfigMap(s.namespace, name)
		ctx, client, env, st := s.ctx, s.client, s.env, s.styles
		return func() tea.Msg {
			return replaceScreenMsg{s: newResourceCreateFlow(ctx, client, env, resource, st)}
		}
	}
	s.setStep(stepType)
	s.message = ""
	s.input.Blur()
	return nil
}

func (s *createPrompt) View() string {
	name := strings.TrimSpace(s.input.Value())
	kind := s.kind
	switch s.step {
	case stepKind:
		kind, name = "", ""
	case stepType:
		kind = k8s.KindSecret
	}
	content := dialogContent{
		identity: commitIdentityLines("create", kind, s.namespace, name, s.client.Context, s.client.Server, s.contentWidth(), s.styles.glyphs.separator),
		message:  s.message,
		isError:  true,
	}
	switch s.step {
	case stepKind:
		content.title = "New resource in namespace " + s.namespace
		choices := make([]string, 0, len(createKinds))
		for i, kind := range createKinds {
			choices = append(choices, s.styles.renderSelectableRow(kind, i == s.cursor, s.contentWidth()))
		}
		content.body = []string{"the name and type come next; then sk64 opens $EDITOR"}
		content.prompt = strings.Join(choices, "\n")
	case stepName:
		content.title = "New " + s.kind + " in namespace " + s.namespace
		content.body = []string{"names are DNS-1123: lowercase letters, digits, - and ., 253 max"}
		content.prompt = s.input.View()
	case stepType:
		content.title = "New Secret " + strings.TrimSpace(s.input.Value())
		choices := make([]string, 0, len(k8s.WellKnownSecretTypes()))
		for i, secretType := range k8s.WellKnownSecretTypes() {
			choices = append(choices, s.styles.renderSelectableRow(secretType, i == s.cursor, s.contentWidth()))
		}
		content.body = []string{secretTypeRequirements[k8s.WellKnownSecretTypes()[s.cursor]]}
		content.prompt = strings.Join(choices, "\n")
	}
	return s.render(content)
}

func (s *createPrompt) SetSize(width, height int) {
	s.resize(width, height)
	s.input.SetWidth(textInputWidth(s.contentWidth(), s.input.Prompt))
}
func (s *createPrompt) SetStyles(st *styles) {
	s.styles = st
	applyTextInputStyles(&s.input, st)
}
func (s *createPrompt) Title() string { return s.namespace + " (new)" }
func (s *createPrompt) Hints() footerHints {
	if s.step == stepName {
		return hintBindings(hintDesc(s.km.Choose, "next"), hintDesc(s.km.Cancel, "back"))
	}
	return hintBindings(hintDesc(s.km.Choose, "select"), hintDesc(s.km.Cancel, "back"))
}
func (s *createPrompt) Help() helpGroup     { return helpGroup{} }
func (s *createPrompt) CapturesInput() bool { return true }
func (s *createPrompt) WantsEsc() bool      { return true }
