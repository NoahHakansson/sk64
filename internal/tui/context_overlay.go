package tui

import (
	"context"
	"fmt"
	"io"
	"time"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/debuglog"
	"github.com/NoahHakansson/sk64/internal/k8s"
)

type overlayState int

const (
	overlayLoading overlayState = iota
	overlayList
	overlayProbing
	overlayExecOffer
	overlayError
)

const (
	contextPreferredWidth   = 60
	contextWidthPercent     = 70
	contextMaxWidth         = 96
	contextListChromeHeight = 8
	contextListMaxHeight    = 20
)

type contextItem struct {
	info   k8s.ContextInfo
	styles *styles
}

func (i contextItem) Title() string {
	title := i.info.Name
	if i.info.Current {
		title += "  " + i.styles.tag.Render(i.styles.glyphs.currentTag)
	}
	cluster := i.info.Cluster
	if cluster == "" {
		cluster = "unknown"
	}
	return title + "  cluster: " + cluster
}

func (i contextItem) Description() string {
	server := i.info.Server
	if server == "" {
		server = "unknown"
	}
	server = redactServerUserinfo(server)
	namespace := i.info.Namespace
	if namespace == "" {
		namespace = "default"
	}
	return fmt.Sprintf("server: %s  namespace: %s", server, namespace)
}

func (i contextItem) FilterValue() string {
	return i.Title() + " " + i.Description()
}

type contextOverlay struct {
	loader
	ctx            context.Context
	kubeconfig     string
	currentContext string
	currentServer  string
	debug          *debuglog.Logger
	keys           *keyMaps
	styles         *styles
	list           list.Model
	spinner        spinner.Model
	state          overlayState
	err            error
	selectedName   string
	execNudge      bool
	closed         bool
	boxWidth       int
	contentWidth   int
}

func newContextOverlay(ctx context.Context, kubeconfig, currentContext, currentServer string, debug *debuglog.Logger, keys *keyMaps, st *styles) *contextOverlay {
	listModel := newListModel(st, keys.list)
	applyDetailedListStyles(&listModel, st)
	return &contextOverlay{
		ctx:            ctx,
		kubeconfig:     kubeconfig,
		currentContext: currentContext,
		currentServer:  currentServer,
		debug:          debug,
		keys:           keys,
		styles:         st,
		list:           listModel,
		spinner:        newSpinner(st),
		state:          overlayLoading,
	}
}

func (o *contextOverlay) Init() tea.Cmd {
	return tea.Batch(o.loadContexts(), o.spinner.Tick)
}

func (o *contextOverlay) loadContexts() tea.Cmd {
	ctx, reqID := o.start(o.ctx)
	kubeconfig := o.kubeconfig
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return contextsLoadedMsg{reqID: reqID, err: err}
		}
		contexts, err := k8s.ListContexts(kubeconfig)
		return contextsLoadedMsg{reqID: reqID, contexts: contexts, err: err}
	}
}

func (o *contextOverlay) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case contextsLoadedMsg:
		if o.state != overlayLoading || !o.finish(msg.reqID) {
			return nil
		}
		if msg.err != nil {
			o.err = msg.err
			o.state = overlayError
			return nil
		}
		items := make([]list.Item, len(msg.contexts))
		for i, info := range msg.contexts {
			info.Current = info.Name == o.currentContext && k8s.SameServer(info.Server, o.currentServer)
			items[i] = contextItem{info: info, styles: o.styles}
		}
		o.state = overlayList
		return scopeListFilterCmd(&o.list, o.list.SetItems(items))

	case contextProbedMsg:
		if msg.name != o.selectedName || !o.finish(msg.reqID) {
			return nil
		}
		if msg.err == nil {
			return switchContext(msg.client)
		}
		o.err = msg.err
		if k8s.IsExecPluginError(msg.err) {
			o.state = overlayExecOffer
			o.execNudge = false
		} else {
			o.state = overlayError
		}
		return nil

	case execProbeDoneMsg:
		if msg.name != o.selectedName {
			return nil
		}
		if msg.err == nil {
			return switchContext(msg.client)
		}
		o.err = msg.err
		o.state = overlayError
		return nil

	case spinner.TickMsg:
		if o.state != overlayLoading && o.state != overlayProbing {
			return nil
		}
		var cmd tea.Cmd
		o.spinner, cmd = o.spinner.Update(msg)
		return cmd

	case tea.KeyPressMsg:
		return o.updateKey(msg)
	}

	if o.state == overlayList {
		return updateListModel(&o.list, msg)
	}
	return nil
}

func (o *contextOverlay) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if o.state == overlayList && o.list.SettingFilter() {
		return updateListModel(&o.list, msg)
	}

	switch o.state {
	case overlayLoading:
		if bubbleskey.Matches(msg, bindEsc) {
			o.stop()
			o.closed = true
		}
	case overlayList:
		switch {
		case bubbleskey.Matches(msg, bindEsc):
			o.closed = true
		case bubbleskey.Matches(msg, bindEnter):
			selected, ok := o.list.SelectedItem().(contextItem)
			if !ok {
				return nil
			}
			if selected.info.Current {
				o.closed = true
				return nil
			}
			return o.startProbe(selected.info.Name)
		default:
			return updateListModel(&o.list, msg)
		}
	case overlayProbing:
		if bubbleskey.Matches(msg, bindEsc) && o.stop() {
			o.state = overlayList
		}
	case overlayExecOffer:
		switch {
		case bubbleskey.Matches(msg, bindConfirmY):
			return o.execProbe()
		case msg.String() == "y" || msg.String() == "enter":
			o.execNudge = true
		case msg.String() == "n":
			o.state = overlayList
			o.execNudge = false
		case bubbleskey.Matches(msg, bindEsc):
			o.closed = true
			o.execNudge = false
		}
	case overlayError:
		switch {
		case bubbleskey.Matches(msg, bindEnter):
			if o.selectedName != "" {
				return o.startProbe(o.selectedName)
			}
			o.state = overlayLoading
			o.err = nil
			return tea.Batch(o.loadContexts(), o.spinner.Tick)
		case bubbleskey.Matches(msg, bindEsc):
			o.closed = true
		}
	}
	return nil
}

func (o *contextOverlay) startProbe(name string) tea.Cmd {
	ctx, reqID := o.start(o.ctx)
	o.selectedName = name
	o.err = nil
	o.state = overlayProbing
	return tea.Batch(func() tea.Msg {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		client, err := k8s.SwitchContext(probeCtx, o.kubeconfig, name, "", o.debug)
		return contextProbedMsg{reqID: reqID, name: name, client: client, err: err}
	}, o.spinner.Tick)
}

func (o *contextOverlay) execProbe() tea.Cmd {
	return execProbeCmd(o.ctx, o.kubeconfig, o.selectedName, "", o.debug)
}

func execProbeCmd(ctx context.Context, kubeconfig, contextName, namespace string, debug *debuglog.Logger) tea.Cmd {
	probe := &execProbe{ctx: ctx, kubeconfig: kubeconfig, contextName: contextName, namespace: namespace, debug: debug}
	return tea.Exec(probe, func(err error) tea.Msg {
		if probe.err == nil {
			probe.err = err
		}
		return execProbeDoneMsg{name: contextName, client: probe.client, err: probe.err}
	})
}

func switchContext(client *k8s.Client) tea.Cmd {
	return func() tea.Msg { return contextSwitchedMsg{client: client} }
}

func (o *contextOverlay) View() string {
	var content string
	switch o.state {
	case overlayLoading:
		content = renderLoadingLine(o.styles, o.spinner.View(), "loading contexts", "", o.contentWidth)
	case overlayList:
		content = o.list.View()
	case overlayProbing:
		content = renderLoadingLine(o.styles, o.spinner.View(), "probing "+o.selectedName, "", o.contentWidth)
	case overlayExecOffer:
		content = "auth plugin needs the terminal. run it now? [Y/n]"
		if o.execNudge {
			content += "\n\n" + o.styles.warnText.Render(pressYToConfirm)
		}
	case overlayError:
		content = renderStateLine(o.styles, stateLineError, fmt.Sprintf("context unavailable: %v", o.err), "", o.contentWidth)
	}
	return o.styles.dialogBox.Width(o.boxWidth).Render(content)
}

func (o *contextOverlay) SetSize(width, height int) {
	o.boxWidth, o.contentWidth = responsiveBoxWidths(
		width,
		contextPreferredWidth,
		contextWidthPercent,
		contextMaxWidth,
		o.styles.dialogBox.GetHorizontalFrameSize(),
	)
	o.list.SetSize(o.contentWidth, max(1, min(contextListMaxHeight, height-contextListChromeHeight)))
}

func (o *contextOverlay) SetStyles(st *styles) {
	o.styles = st
	applyDetailedListStyles(&o.list, st)
	applySpinnerStyle(&o.spinner, st)
}

func (o *contextOverlay) Hints() footerHints {
	switch o.state {
	case overlayList:
		return hintBindings(hintDesc(bindEnter, "switch"), o.keys.global.Filter, hintDesc(bindEsc, "close"))
	case overlayProbing:
		return hintBindings(bindEsc)
	case overlayExecOffer:
		return hintBindings(hintDesc(bindConfirmY, "run"), displayHint("n", "back"), hintDesc(bindEsc, "close"))
	case overlayError:
		return hintBindings(hintDesc(bindEnter, "retry"), hintDesc(bindEsc, "close"))
	default:
		return hintBindings(hintDesc(bindEsc, "close"))
	}
}

func (o *contextOverlay) isClosed() bool { return o.closed }

type execProbe struct {
	ctx         context.Context
	kubeconfig  string
	contextName string
	namespace   string
	debug       *debuglog.Logger
	client      *k8s.Client
	err         error
}

func (e *execProbe) SetStdin(io.Reader)  {}
func (e *execProbe) SetStdout(io.Writer) {}
func (e *execProbe) SetStderr(io.Writer) {}

func (e *execProbe) Run() error {
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Minute)
	defer cancel()
	e.client, e.err = k8s.SwitchContext(ctx, e.kubeconfig, e.contextName, e.namespace, e.debug)
	return e.err
}
