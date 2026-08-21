package tui

import (
	"context"
	"fmt"
	"time"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/debuglog"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
)

type projectContextConfirmState int

const (
	projectContextConfirmReady projectContextConfirmState = iota
	projectContextConfirmProbing
	projectContextConfirmExecOffer
	projectContextConfirmSaving
	projectContextConfirmError
)

type projectContextProbedMsg struct {
	reqID  int
	client *k8s.Client
	err    error
}

type projectBindingSavedMsg struct {
	reqID   int
	project store.Project
	client  *k8s.Client
	err     error
}

type projectContextConfirm struct {
	loader
	dialog
	ctx             context.Context
	store           *store.Store
	project         store.Project
	client          *k8s.Client
	target          k8s.ContextInfo
	kubeconfig      string
	debug           *debuglog.Logger
	spinner         spinner.Model
	state           projectContextConfirmState
	always          bool
	nudge           bool
	execNudge       bool
	identityChanged bool
	bindingRequired bool
	confirming      bool
	confirmAlways   bool
	gate            confirmGate
	err             error
}

func newProjectContextConfirm(
	ctx context.Context,
	st *store.Store,
	project store.Project,
	client *k8s.Client,
	target k8s.ContextInfo,
	kubeconfig string,
	debug *debuglog.Logger,
	styles *styles,
) *projectContextConfirm {
	return &projectContextConfirm{
		dialog: newDialog(styles, false),
		ctx:    ctx, store: st, project: project, client: client, target: target, kubeconfig: kubeconfig, debug: debug,
		spinner: newSpinner(styles),
		gate:    newConfirmGate(styles),
	}
}

func (s *projectContextConfirm) Init() tea.Cmd { return nil }

func (s *projectContextConfirm) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case projectContextProbedMsg:
		if !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			s.err = msg.err
			if k8s.IsExecPluginError(msg.err) {
				s.state = projectContextConfirmExecOffer
				s.execNudge = false
			} else {
				s.state = projectContextConfirmError
			}
			return s, nil
		}
		return s, s.completeSwitch(msg.client)
	case execProbeDoneMsg:
		if s.state != projectContextConfirmExecOffer || msg.name != s.target.Name {
			return s, nil
		}
		if msg.err != nil {
			s.err = msg.err
			s.state = projectContextConfirmError
			return s, nil
		}
		return s, s.completeSwitch(msg.client)
	case projectBindingSavedMsg:
		if !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			if !s.bindingRequired {
				return s, openProject(
					s.project,
					msg.client,
					fmt.Sprintf("switched context to %s for project %s; preference not saved: %v", s.target.Name, s.project.Name, msg.err),
				)
			}
			s.err = msg.err
			s.state = projectContextConfirmError
			return s, nil
		}
		return s, openProject(
			msg.project,
			msg.client,
			fmt.Sprintf("switched context to %s for project %s", msg.project.KubeContext, msg.project.Name),
		)
	case spinner.TickMsg:
		if !s.pending {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyPressMsg:
		return s, s.updateKey(msg)
	}
	return s, nil
}

func (s *projectContextConfirm) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if s.pending {
		if s.state == projectContextConfirmSaving {
			return nil
		}
		if bubbleskey.Matches(msg, bindEsc) {
			s.stop()
			return popScreen()
		}
		return nil
	}
	if s.state == projectContextConfirmExecOffer {
		switch {
		case bubbleskey.Matches(msg, bindConfirmY):
			return execProbeCmd(s.ctx, s.kubeconfig, s.target.Name, s.project.Namespace, s.debug)
		case msg.String() == "y" || msg.String() == "enter":
			s.execNudge = true
		case msg.String() == "n":
			s.state = projectContextConfirmReady
			s.execNudge = false
		case bubbleskey.Matches(msg, bindEsc):
			return popScreen()
		}
		return nil
	}
	if s.confirming {
		if bubbleskey.Matches(msg, bindEsc) {
			s.confirming = false
			return nil
		}
		confirmed, cmd := s.gate.handleKey(msg)
		if confirmed {
			s.confirming = false
			return s.startSwitch(s.confirmAlways)
		}
		return cmd
	}
	switch {
	case bubbleskey.Matches(msg, bindConfirmY):
		s.confirming, s.confirmAlways = true, false
		s.nudge = false
		return s.gate.arm()
	case bubbleskey.Matches(msg, bindAlwaysA):
		s.confirming, s.confirmAlways = true, true
		s.nudge = false
		return s.gate.arm()
	case msg.String() == "y" || msg.String() == "a" || msg.String() == "enter":
		s.nudge = true
	case bubbleskey.Matches(msg, bindEsc):
		return popScreen()
	}
	return nil
}

func (s *projectContextConfirm) startSwitch(always bool) tea.Cmd {
	if s.target.Name == s.client.Context && k8s.SameServer(s.target.Server, s.client.Server) {
		s.always = always
		s.nudge = false
		s.identityChanged = false
		return s.completeSwitch(s.client)
	}
	ctx, reqID := s.start(s.ctx)
	kubeconfig := s.kubeconfig
	contextName := s.target.Name
	namespace := s.project.Namespace
	debug := s.debug
	s.always = always
	s.nudge = false
	s.identityChanged = false
	s.err = nil
	s.state = projectContextConfirmProbing
	return tea.Batch(func() tea.Msg {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		client, err := k8s.SwitchContext(probeCtx, kubeconfig, contextName, namespace, debug)
		return projectContextProbedMsg{reqID: reqID, client: client, err: err}
	}, s.spinner.Tick)
}

func (s *projectContextConfirm) completeSwitch(client *k8s.Client) tea.Cmd {
	if client == nil || client.Context != s.target.Name {
		s.err = fmt.Errorf("probed context identity is unavailable")
		s.state = projectContextConfirmError
		return nil
	}
	if s.target.Server != "" && !k8s.SameServer(client.Server, s.target.Server) {
		s.target.Server = client.Server
		s.always = false
		s.identityChanged = true
		s.state = projectContextConfirmReady
		return nil
	}
	s.bindingRequired = s.target.Name != s.project.KubeContext || !k8s.SameServer(s.project.KubeServer, client.Server)
	if !s.always && !s.bindingRequired {
		return openProject(
			s.project,
			client,
			fmt.Sprintf("switched context to %s for project %s", s.project.KubeContext, s.project.Name),
		)
	}
	ctx, reqID := s.start(s.ctx)
	st := s.store
	projectID := s.project.ID
	kubeContext := s.target.Name
	kubeServer := client.Server
	suppress := s.always
	s.state = projectContextConfirmSaving
	save := func() tea.Msg {
		if st == nil {
			return projectBindingSavedMsg{reqID: reqID, client: client, err: fmt.Errorf("project database unavailable")}
		}
		project, err := st.ConfirmProjectContext(ctx, projectID, kubeContext, kubeServer, suppress)
		return projectBindingSavedMsg{reqID: reqID, project: project, client: client, err: err}
	}
	return tea.Batch(save, s.spinner.Tick)
}

func (s *projectContextConfirm) View() string {
	currentServer := s.client.Server
	targetServer := s.target.Server
	content := dialogContent{
		title: "Switch project context?",
		body: []string{
			fmt.Sprintf("Project %s uses context %s.", s.project.Name, s.project.KubeContext),
			fmt.Sprintf("Project server: %s", displayServer(s.project.KubeServer)),
			fmt.Sprintf("Context server: %s", displayServer(targetServer)),
			fmt.Sprintf("This window uses %s (%s).", s.client.Context, displayServer(currentServer)),
			"Switching changes only this sk64 window.",
			"kubeconfig current-context and kubectl are unchanged.",
		},
		prompt: "Y switch once  A always switch for this project  esc cancel",
	}
	nudgeMessage := "press Y to switch once or A to always switch"
	if s.target.Name != s.project.KubeContext {
		content.title = "Rebind project context?"
		content.body = []string{
			fmt.Sprintf("Context %s no longer exists.", s.project.KubeContext),
			fmt.Sprintf("Context %s points at the project's saved server.", s.target.Name),
			fmt.Sprintf("Confirming re-points project %s to context %s.", s.project.Name, s.target.Name),
			fmt.Sprintf("This window uses %s (%s).", s.client.Context, displayServer(currentServer)),
			"Switching changes only this sk64 window.",
			"kubeconfig current-context and kubectl are unchanged.",
		}
		content.prompt = "Y rebind  A rebind and always switch  esc cancel"
		nudgeMessage = "press Y to rebind once or A to rebind and always switch"
	} else if s.project.KubeServer != "" && !k8s.SameServer(s.project.KubeServer, targetServer) {
		content.title = "Rebind project cluster?"
		content.body = append(content.body, "Confirming rebinds this project to the context server.")
		content.prompt = "Y rebind  A rebind and always switch  esc cancel"
		nudgeMessage = "press Y to rebind once or A to rebind and always switch"
	}
	switch s.state {
	case projectContextConfirmProbing:
		content.message = s.spinner.View() + " probing project context..."
	case projectContextConfirmExecOffer:
		content.prompt = "Auth plugin needs the terminal. Y run  n back  esc cancel"
		if s.execNudge {
			content.message = pressYToConfirm
			content.isWarning = true
		}
	case projectContextConfirmSaving:
		content.prompt = "saving (cannot cancel)"
		content.message = s.spinner.View() + " saving project preference..."
	case projectContextConfirmError:
		content.message = fmt.Sprintf("switch failed: %v", s.err)
		content.isError = true
	default:
		if s.identityChanged {
			content.message = "context server changed during the probe; review and confirm again"
			content.isError = true
		} else if s.nudge {
			content.message = nudgeMessage
			content.isWarning = true
		}
	}
	if s.confirming {
		action := "switch this window to context " + s.target.Name + " once"
		if s.confirmAlways {
			action = "always switch to context " + s.target.Name + " for this project"
		}
		content.prompt = action + "\n" + s.gate.promptLines(s.styles, false)
		if s.gate.message != "" {
			content.message, content.isWarning = s.gate.message, true
		}
	}
	return s.render(content)
}

func displayServer(server string) string {
	if server == "" {
		return "unknown"
	}
	return redactServerUserinfo(server)
}

func (s *projectContextConfirm) stop() bool {
	if s.state == projectContextConfirmSaving {
		return false
	}
	return s.loader.stop()
}

func (s *projectContextConfirm) SetSize(width, height int) {
	s.resize(width, height)
	s.gate.setWidth(s.contentWidth())
}
func (s *projectContextConfirm) SetStyles(st *styles) {
	s.styles = st
	s.gate.setStyles(st)
	applySpinnerStyle(&s.spinner, st)
}
func (s *projectContextConfirm) Title() string { return "confirm context" }
func (s *projectContextConfirm) Hints() footerHints {
	if s.state == projectContextConfirmSaving {
		return hintStatus("saving (cannot cancel)")
	}
	if s.pending {
		return hintBindings(bindEsc)
	}
	if s.state == projectContextConfirmExecOffer {
		return hintBindings(hintDesc(bindConfirmY, "run"), displayHint("n", "back"), bindEsc)
	}
	if s.confirming {
		return hintBindings(displayHint("YES", "confirm"), hintDesc(bindEsc, "back"))
	}
	if s.target.Name != s.project.KubeContext {
		return hintBindings(hintDesc(bindConfirmY, "rebind"), bindAlwaysA, bindEsc)
	}
	return hintBindings(hintDesc(bindConfirmY, "switch"), bindAlwaysA, bindEsc)
}
func (s *projectContextConfirm) Help() helpGroup {
	if s.target.Name != s.project.KubeContext {
		return helpGroup{title: "project context", entries: []helpGroupEntry{
			{binding: bindConfirmY, desc: "rebind this sk64 window once"},
			{binding: bindAlwaysA, desc: "rebind and remember this project"},
		}}
	}
	return helpGroup{title: "project context", entries: []helpGroupEntry{
		{binding: bindConfirmY, desc: "switch this sk64 window once"},
		{binding: bindAlwaysA, desc: "switch and remember this project"},
	}}
}
func (s *projectContextConfirm) CapturesInput() bool { return true }
func (s *projectContextConfirm) WantsEsc() bool      { return true }
