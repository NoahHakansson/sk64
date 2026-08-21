package tui

import (
	"context"
	"fmt"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const consumerCheckPendingMessage = "consumer check is still running; esc cancels"

type deleteConfirm struct {
	loader
	radiusLoader loader
	dialog
	ctx            context.Context
	client         *k8s.Client
	kind           string
	namespace      string
	name           string
	res            k8s.Resource
	input          textinput.Model
	message        string
	messageIsError bool
	deleting       bool
	conflicted     bool
	spinner        spinner.Model
	radiusSummary  blastRadius
	radiusErr      error
}

func newDeleteConfirm(ctx context.Context, client *k8s.Client, kind, namespace, name string, st *styles) *deleteConfirm {
	input := newTextInput(st)
	input.Prompt = "confirm: "
	return &deleteConfirm{
		dialog: newDialog(st, true),
		ctx:    ctx, client: client, kind: kind, namespace: namespace, name: name,
		input: input, spinner: newSpinner(st),
	}
}

func (s *deleteConfirm) Init() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	radiusCtx, radiusReqID := s.radiusLoader.start(s.ctx)
	return tea.Batch(func() tea.Msg {
		resource, err := s.client.GetResource(ctx, s.kind, s.namespace, s.name)
		return resourceLoadedMsg{reqID: reqID, res: resource, err: err}
	}, s.spinner.Tick, func() tea.Msg {
		index, err := s.client.CollectNamespaceRefs(radiusCtx, s.namespace)
		return blastRadiusMsg{reqID: radiusReqID, index: index, err: err}
	})
}

func (s *deleteConfirm) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case resourceLoadedMsg:
		if !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			if apierrors.IsNotFound(msg.err) {
				s.message = "resource no longer exists" + s.styles.glyphs.separator + "esc to go back"
				s.messageIsError = false
			} else {
				s.message = "error: " + msg.err.Error()
				s.messageIsError = true
			}
			return s, nil
		}
		s.res = msg.res
		s.conflicted = false
		return s, s.input.Focus()
	case blastRadiusMsg:
		if !s.radiusLoader.finish(msg.reqID) {
			return s, nil
		}
		if s.message == consumerCheckPendingMessage {
			s.message = ""
			s.messageIsError = false
		}
		s.radiusSummary = summarizeBlastRadius(msg.index, s.kind, s.name)
		s.radiusErr = msg.err
		return s, nil
	case deleteDoneMsg:
		if !s.deleting || !s.finish(msg.reqID) {
			return s, nil
		}
		s.deleting = false
		switch msg.result.Outcome {
		case k8s.DeleteSucceeded:
			s.stop()
			outcome := resourceOutcome{
				verb:      outcomeDeleted,
				kind:      s.kind,
				namespace: s.namespace,
				name:      s.name,
			}
			return s, tea.Batch(popScreen(), func() tea.Msg {
				return resourceListChangedMsg{namespace: outcome.namespace, outcome: outcome}
			})
		case k8s.DeleteConflict:
			s.conflicted = true
			s.message = "resource changed since this prompt opened" + s.styles.glyphs.separator + "press esc, refresh the list, and retry"
			if msg.result.Message != "" {
				s.message += "\n" + msg.result.Message
			}
			s.messageIsError = false
		case k8s.DeleteForbidden:
			s.message = "delete forbidden: " + msg.result.Message
			s.messageIsError = false
		case k8s.DeleteFailed:
			s.message = "error: " + msg.result.Message
			s.messageIsError = true
		}
		return s, nil
	case spinner.TickMsg:
		if !s.pending && !s.radiusLoader.pending && !s.deleting {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyPressMsg:
		switch {
		case bubbleskey.Matches(msg, bindEsc):
			if s.deleting {
				return s, nil
			}
			s.stop()
			return s, popScreen()
		case bubbleskey.Matches(msg, bindEnter):
			if s.res == nil || s.deleting || s.conflicted {
				return s, nil
			}
			if strings.TrimSpace(s.input.Value()) != s.name {
				s.message = "name does not match"
				s.messageIsError = false
				return s, nil
			}
			if !s.canDelete() {
				s.message = consumerCheckPendingMessage
				s.messageIsError = false
				return s, nil
			}
			ctx, reqID := s.start(s.ctx)
			s.deleting = true
			s.message = ""
			s.messageIsError = false
			return s, tea.Batch(func() tea.Msg {
				return deleteDoneMsg{reqID: reqID, result: s.client.DeleteResource(ctx, s.res)}
			}, s.spinner.Tick)
		}
	}
	if s.res != nil && !s.deleting {
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s *deleteConfirm) canDelete() bool {
	consumerCheckComplete := s.radiusSummary.known || s.radiusErr != nil
	return s.res != nil && !s.deleting && !s.conflicted && !s.radiusLoader.pending && consumerCheckComplete
}

func (s *deleteConfirm) stop() bool {
	resourceStopped := s.loader.stop()
	radiusStopped := s.radiusLoader.stop()
	return resourceStopped || radiusStopped
}

func (s *deleteConfirm) View() string {
	content := dialogContent{
		title:            "Delete " + s.kind + " " + s.name,
		identity:         commitIdentityLines("delete", s.kind, s.namespace, s.name, s.client.Context, s.client.Server, s.contentWidth(), s.styles.glyphs.separator),
		criticalWarnings: []string{"Permanent. ctrl+z cannot restore a deleted resource."},
		message:          s.message,
		isError:          s.messageIsError,
	}
	switch {
	case s.deleting:
		content.body = []string{s.spinner.View() + " deleting..."}
	case s.pending && s.res == nil:
		content.body = []string{s.spinner.View() + " loading " + s.kind + "..."}
	case s.res != nil:
		details := plural(len(s.res.Keys()), "key")
		if s.res.Kind() == k8s.KindSecret {
			details = "type " + s.res.Type() + s.styles.glyphs.separator + details
		}
		if s.res.Immutable() {
			details += s.styles.glyphs.separator + s.styles.tag.Render(s.styles.glyphs.immutableTag)
		}
		content.body = append(content.body, details)
		switch {
		case s.radiusLoader.pending:
			content.body = append(content.body, s.spinner.View()+" checking consumers")
		case s.radiusSummary.known && s.radiusSummary.total() > 0:
			content.body = append(content.body, "consumers: "+s.radiusSummary.consumerList())
		case s.radiusSummary.known:
			content.body = append(content.body, "no workloads, pods or serviceaccounts in this namespace reference it")
		}
		if s.radiusSummary.known && s.radiusSummary.total() > 0 {
			content.criticalWarnings = append(content.criticalWarnings, fmt.Sprintf("%s will break once this %s is gone.", s.radiusSummary.subject(), s.kind))
		}
		if s.radiusErr != nil {
			content.criticalWarnings = append(content.criticalWarnings, "Consumer check failed; workloads you cannot see here may depend on this "+s.kind+".")
		}
		if len(s.radiusSummary.notes) > 0 {
			content.criticalWarnings = append(content.criticalWarnings, "Consumer list is incomplete: "+strings.Join(s.radiusSummary.notes, ", ")+".")
		}
		content.prompt = "type " + s.styles.errText.Bold(true).Render(s.name) + " to confirm\n" + s.input.View()
	}
	return s.render(content)
}

func (s *deleteConfirm) SetSize(width, height int) {
	s.resize(width, height)
	s.input.SetWidth(textInputWidth(s.contentWidth(), s.input.Prompt))
}
func (s *deleteConfirm) SetStyles(st *styles) {
	s.styles = st
	applyTextInputStyles(&s.input, st)
	applySpinnerStyle(&s.spinner, st)
}
func (s *deleteConfirm) Title() string { return s.name + " (delete)" }
func (s *deleteConfirm) Hints() footerHints {
	if s.deleting {
		return hintStatus("deleting (cannot cancel)")
	}
	if s.canDelete() {
		return hintBindings(hintDesc(bindEnter, "delete"), bindEsc)
	}
	return hintBindings(bindEsc)
}
func (s *deleteConfirm) Help() helpGroup     { return helpGroup{} }
func (s *deleteConfirm) CapturesInput() bool { return true }
func (s *deleteConfirm) WantsEsc() bool      { return true }
