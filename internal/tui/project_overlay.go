package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/debuglog"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/charmbracelet/x/ansi"
)

type projectOverlayMode int

const (
	projectModeSwitch projectOverlayMode = iota
	projectModeLink
)

type projectOverlayState int

const (
	projectOverlayLoading projectOverlayState = iota
	projectOverlayList
	projectOverlayResolving
	projectOverlayProbing
	projectOverlayExecOffer
	projectOverlayConfirmLink
	projectOverlayLinking
	projectOverlayLinked
	projectOverlayError
	projectOverlayUnavailable
)

const (
	projectPreferredWidth   = 70
	projectWidthPercent     = 75
	projectMaxWidth         = 120
	projectListChromeHeight = 8
	projectListMaxHeight    = 20
)

type projectItemStatus int

const (
	projectItemActive projectItemStatus = iota
	projectItemInactive
	projectItemServerMismatch
)

type projectItem struct {
	project          store.Project
	currentContext   string
	currentServer    string
	descriptionWidth *int
	ellipsis         string
	styles           *styles
}

func (i projectItem) status() projectItemStatus {
	if i.project.KubeContext != i.currentContext {
		return projectItemInactive
	}
	if i.project.KubeServer != "" && !k8s.SameServer(i.project.KubeServer, i.currentServer) {
		return projectItemServerMismatch
	}
	return projectItemActive
}

func (i projectItem) Title() string {
	return fmt.Sprintf("%s  %s  %s", i.statusTag(), i.project.Name, i.project.RootPath)
}

func (i projectItem) statusTag() string {
	switch i.status() {
	case projectItemInactive:
		return i.styles.dim.Render(i.styles.glyphs.inactiveTag)
	case projectItemServerMismatch:
		return i.styles.errText.Render(i.styles.glyphs.serverMismatchTag)
	default:
		return i.styles.successText.Render(i.styles.glyphs.activeTag)
	}
}

func (i projectItem) Description() string {
	width := 0
	if i.descriptionWidth != nil {
		width = *i.descriptionWidth
	}
	return i.description(width)
}

func (i projectItem) description(width int) string {
	contextName := i.project.KubeContext
	if contextName == "" {
		contextName = "unknown"
	}
	if i.status() == projectItemServerMismatch {
		return i.serverMismatchDescription(contextName, width)
	}
	server := i.project.KubeServer
	if server == "" && i.status() == projectItemActive {
		return fmt.Sprintf("context: %s  saved server: unverified  active server: %s", contextName, serverOrUnverified(i.currentServer))
	}
	return fmt.Sprintf("context: %s  saved server: %s", contextName, serverOrUnverified(server))
}

func (i projectItem) serverMismatchDescription(contextName string, width int) string {
	const (
		savedLabel  = "saved "
		activeLabel = "active "
	)
	identity := "context: " + contextName
	savedServer := serverOrUnverified(i.project.KubeServer)
	activeServer := serverOrUnverified(i.currentServer)
	if width <= 0 {
		return renderRowColumns(
			width,
			identity,
			i.ellipsis,
			rowColumn{text: savedLabel + savedServer, critical: true},
			rowColumn{text: activeLabel + activeServer, critical: true},
		)
	}

	endpointWidth := max(
		1,
		(width-lipgloss.Width(identity)-lipgloss.Width(rowColumnSeparator)*2-lipgloss.Width(savedLabel)-lipgloss.Width(activeLabel))/2,
	)
	savedNeedsElision := lipgloss.Width(savedServer) > endpointWidth
	activeNeedsElision := lipgloss.Width(activeServer) > endpointWidth
	if !savedNeedsElision || !activeNeedsElision {
		compactSaved := savedServer
		if savedNeedsElision {
			compactSaved = compactServer(compactSaved, endpointWidth, i.ellipsis)
		}
		compactActive := activeServer
		if activeNeedsElision {
			compactActive = compactServer(compactActive, endpointWidth, i.ellipsis)
		}
		return renderRowColumns(
			width,
			identity,
			i.ellipsis,
			rowColumn{text: savedLabel + compactSaved, critical: true},
			rowColumn{text: activeLabel + compactActive, critical: true},
		)
	}

	serverWidth := max(1, width-lipgloss.Width(activeLabel))
	savedServer, activeServer = elideSharedServerPrefix(savedServer, activeServer, serverWidth, i.ellipsis)
	return strings.Join([]string{
		truncateLine(identity, width, i.ellipsis),
		savedLabel + savedServer,
		activeLabel + activeServer,
	}, "\n")
}

func elideSharedServerPrefix(saved, active string, width int, ellipsis string) (string, string) {
	if commonStringPrefix(saved, active) == "" {
		return middleElideLine(saved, width, ellipsis), middleElideLine(active, width, ellipsis)
	}
	return leadingElideLine(saved, width, ellipsis), leadingElideLine(active, width, ellipsis)
}

func commonStringPrefix(left, right string) string {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	limit := min(len(leftRunes), len(rightRunes))
	for index := 0; index < limit; index++ {
		if leftRunes[index] != rightRunes[index] {
			return string(leftRunes[:index])
		}
	}
	return string(leftRunes[:limit])
}

func leadingElideLine(text string, width int, ellipsis string) string {
	textWidth := lipgloss.Width(text)
	if width <= 0 || textWidth <= width {
		return text
	}
	ellipsisWidth := lipgloss.Width(ellipsis)
	if width <= ellipsisWidth {
		return ansi.Cut(ellipsis, 0, width)
	}
	return ellipsis + ansi.Cut(text, textWidth-(width-ellipsisWidth), textWidth)
}

func (i projectItem) FilterValue() string {
	value := i.Title() + " " + i.description(0)
	if i.status() == projectItemServerMismatch {
		value += " " + i.project.KubeServer + " " + i.currentServer
	}
	return value
}

type projectOverlay struct {
	loader
	ctx                  context.Context
	store                *store.Store
	client               *k8s.Client
	kubeconfig           string
	projectRoot          string
	scanCfg              scanConfig
	debug                *debuglog.Logger
	mode                 projectOverlayMode
	link                 pendingLink
	keys                 *keyMaps
	styles               *styles
	list                 list.Model
	spinner              spinner.Model
	state                projectOverlayState
	gate                 confirmGate
	err                  error
	selected             store.Project
	target               k8s.ContextInfo
	linkedName           string
	execNudge            bool
	listReady            bool
	closed               bool
	boxWidth             int
	contentWidth         int
	itemDescriptionWidth int
}

func newProjectOverlay(ctx context.Context, st *store.Store, client *k8s.Client, kubeconfig, projectRoot string, scanCfg scanConfig, debug *debuglog.Logger, mode projectOverlayMode, link pendingLink, keys *keyMaps, styles *styles) *projectOverlay {
	state := projectOverlayLoading
	if st == nil {
		state = projectOverlayUnavailable
	}
	listModel := newListModel(styles, keys.list)
	applyDetailedListStyles(&listModel, styles)
	return &projectOverlay{
		ctx: ctx, store: st, client: client, kubeconfig: kubeconfig, projectRoot: projectRoot, scanCfg: scanCfg, debug: debug,
		mode: mode, link: link, keys: keys, styles: styles, list: listModel, spinner: newSpinner(styles), state: state,
		gate: newConfirmGate(styles),
	}
}

func (o *projectOverlay) Init() tea.Cmd {
	if o.store == nil {
		return nil
	}
	return tea.Batch(o.loadProjects(), o.spinner.Tick)
}

func (o *projectOverlay) loadProjects() tea.Cmd {
	ctx, reqID := o.start(o.ctx)
	o.state = projectOverlayLoading
	o.err = nil
	return func() tea.Msg {
		projects, err := o.store.ListProjects(ctx)
		if err != nil {
			return projectsLoadedMsg{reqID: reqID, err: err}
		}
		// The last-opened project only pre-positions the cursor; a store read
		// failure must not fail the overlay.
		last, found, err := o.store.LastProject(ctx)
		if err != nil || !found {
			last = ""
		}
		return projectsLoadedMsg{reqID: reqID, projects: projects, last: last}
	}
}

func (o *projectOverlay) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case projectsLoadedMsg:
		if !o.finish(msg.reqID) {
			return nil
		}
		if msg.err != nil {
			o.err = msg.err
			o.state = projectOverlayError
			return nil
		}
		items := make([]list.Item, len(msg.projects))
		for i, project := range msg.projects {
			items[i] = projectItem{
				project: project, currentContext: o.client.Context, currentServer: o.client.Server,
				descriptionWidth: &o.itemDescriptionWidth, ellipsis: o.styles.glyphs.ellipsis, styles: o.styles,
			}
		}
		o.listReady = true
		o.state = projectOverlayList
		cmd := scopeListFilterCmd(&o.list, o.list.SetItems(items))
		o.applyProjectListStyles()
		if msg.last != "" {
			for i, project := range msg.projects {
				if project.Name == msg.last {
					o.list.Select(i)
					break
				}
			}
		}
		return cmd
	case projectProbedMsg:
		if msg.project.ID != o.selected.ID || !o.finish(msg.reqID) {
			return nil
		}
		if msg.err == nil {
			return o.completeProbe(msg.project, msg.client)
		}
		o.err = msg.err
		if k8s.IsExecPluginError(msg.err) {
			o.state = projectOverlayExecOffer
			o.execNudge = false
		} else {
			o.state = projectOverlayError
		}
		return nil
	case projectIdentityResolvedMsg:
		if msg.project.ID != o.selected.ID || !o.finish(msg.reqID) {
			return nil
		}
		if msg.err != nil {
			o.err = msg.err
			o.state = projectOverlayError
			return nil
		}
		o.target = msg.target
		if msg.target.Name != msg.project.KubeContext || !msg.project.SwitchPromptSuppressed ||
			msg.project.KubeServer != "" && !k8s.SameServer(msg.project.KubeServer, msg.target.Server) {
			o.closed = true
			return o.confirmProjectContext(msg.project, msg.target)
		}
		return o.startProbe(msg.project)
	case execProbeDoneMsg:
		if msg.name != o.selected.KubeContext {
			return nil
		}
		if msg.err == nil {
			return o.completeProbe(o.selected, msg.client)
		}
		o.err = msg.err
		o.state = projectOverlayError
		return nil
	case projectLinkedMsg:
		if !o.finish(msg.reqID) {
			return nil
		}
		o.err = msg.err
		o.linkedName = msg.projectName
		o.state = projectOverlayLinked
		return nil
	case spinner.TickMsg:
		if o.state != projectOverlayLoading && o.state != projectOverlayResolving && o.state != projectOverlayProbing && o.state != projectOverlayLinking {
			return nil
		}
		var cmd tea.Cmd
		o.spinner, cmd = o.spinner.Update(msg)
		return cmd
	case tea.KeyPressMsg:
		return o.updateKey(msg)
	}
	if o.state == projectOverlayList {
		return updateListModel(&o.list, msg)
	}
	return nil
}

func (o *projectOverlay) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if o.state == projectOverlayList && o.list.SettingFilter() {
		return updateListModel(&o.list, msg)
	}
	switch o.state {
	case projectOverlayUnavailable:
		if bubbleskey.Matches(msg, bindEsc) {
			o.closed = true
		}
	case projectOverlayLoading:
		if bubbleskey.Matches(msg, bindEsc) {
			o.stop()
			o.closed = true
		}
	case projectOverlayList:
		switch {
		case bubbleskey.Matches(msg, bindEsc):
			o.closed = true
		case bubbleskey.Matches(msg, bindNewN):
			if o.mode == projectModeSwitch {
				o.closed = true
				initial := store.ProjectMeta{KubeContext: o.client.Context, KubeServer: o.client.Server, Namespace: o.client.Namespace}
				if o.projectRoot != "" {
					initial.Name = filepath.Base(o.projectRoot)
					initial.RootPath = o.projectRoot
				}
				return pushScreen(newProjectFormScreen(o.ctx, o.store, o.kubeconfig, o.scanCfg, formCreate, nil, initial, nil, o.keys, o.styles))
			}
		case bubbleskey.Matches(msg, bindEnter):
			selected, ok := o.list.SelectedItem().(projectItem)
			if !ok {
				return nil
			}
			o.selected = selected.project
			if o.mode == projectModeLink {
				o.state = projectOverlayConfirmLink
				return o.gate.arm()
			}
			if selected.project.KubeContext == o.client.Context {
				o.closed = true
				if selected.project.KubeServer != "" && !k8s.SameServer(selected.project.KubeServer, o.client.Server) {
					target := k8s.ContextInfo{Name: o.client.Context, Server: o.client.Server, Namespace: o.client.Namespace}
					return o.confirmProjectContext(selected.project, target)
				}
				client := *o.client
				client.Namespace = selected.project.Namespace
				return openProject(selected.project, &client, "")
			}
			return o.startResolve(selected.project)
		default:
			return updateListModel(&o.list, msg)
		}
	case projectOverlayConfirmLink:
		if bubbleskey.Matches(msg, bindEsc) {
			o.state = projectOverlayList
			return nil
		}
		confirmed, cmd := o.gate.handleKey(msg)
		if confirmed {
			return o.startLink(o.selected)
		}
		return cmd
	case projectOverlayResolving, projectOverlayProbing:
		if bubbleskey.Matches(msg, bindEsc) && o.stop() {
			o.state = projectOverlayList
		}
	case projectOverlayLinking:
	case projectOverlayExecOffer:
		switch {
		case bubbleskey.Matches(msg, bindConfirmY):
			return o.execProbe()
		case msg.String() == "y" || msg.String() == "enter":
			o.execNudge = true
		case msg.String() == "n":
			o.state = projectOverlayList
			o.execNudge = false
		case bubbleskey.Matches(msg, bindEsc):
			o.closed = true
			o.execNudge = false
		}
	case projectOverlayLinked:
		if bubbleskey.Matches(msg, bindEnter, bindEsc) {
			o.closed = true
		}
	case projectOverlayError:
		switch {
		case bubbleskey.Matches(msg, bindEnter):
			if o.selected.ID != 0 {
				return o.startResolve(o.selected)
			}
			return o.loadProjects()
		case bubbleskey.Matches(msg, bindEsc):
			if o.listReady {
				o.state = projectOverlayList
			} else {
				o.closed = true
			}
		}
	}
	return nil
}

func (o *projectOverlay) startResolve(project store.Project) tea.Cmd {
	ctx, reqID := o.start(o.ctx)
	kubeconfig := o.kubeconfig
	o.selected = project
	o.err = nil
	o.execNudge = false
	o.state = projectOverlayResolving
	return tea.Batch(func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return projectIdentityResolvedMsg{reqID: reqID, project: project, err: err}
		}
		target, err := k8s.ResolveContextIdentity(kubeconfig, project.KubeContext)
		if errors.Is(err, k8s.ErrContextNotFound) && project.KubeServer != "" {
			if renamedTarget, found, findErr := k8s.FindContextByServer(kubeconfig, project.KubeServer); findErr == nil && found {
				return projectIdentityResolvedMsg{reqID: reqID, project: project, target: renamedTarget}
			}
		}
		return projectIdentityResolvedMsg{reqID: reqID, project: project, target: target, err: err}
	}, o.spinner.Tick)
}

func (o *projectOverlay) startProbe(project store.Project) tea.Cmd {
	if project.KubeServer != "" && o.target.Server != "" && !k8s.SameServer(project.KubeServer, o.target.Server) {
		o.closed = true
		return o.confirmProjectContext(project, o.target)
	}
	ctx, reqID := o.start(o.ctx)
	o.selected = project
	o.err = nil
	o.state = projectOverlayProbing
	return tea.Batch(func() tea.Msg {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		client, err := k8s.SwitchContext(probeCtx, o.kubeconfig, project.KubeContext, project.Namespace, o.debug)
		return projectProbedMsg{reqID: reqID, project: project, client: client, err: err}
	}, o.spinner.Tick)
}

func (o *projectOverlay) completeProbe(project store.Project, client *k8s.Client) tea.Cmd {
	if client == nil || !k8s.SameServer(client.Server, o.target.Server) || project.KubeServer != "" && !k8s.SameServer(client.Server, project.KubeServer) {
		o.closed = true
		target := o.target
		if client != nil {
			target.Server = client.Server
		}
		return o.confirmProjectContext(project, target)
	}
	o.closed = true
	return openProject(project, client, fmt.Sprintf("switched context to %s for project %s", project.KubeContext, project.Name))
}

func (o *projectOverlay) confirmProjectContext(project store.Project, target k8s.ContextInfo) tea.Cmd {
	return pushScreen(newProjectContextConfirm(o.ctx, o.store, project, o.client, target, o.kubeconfig, o.debug, o.styles))
}

func (o *projectOverlay) execProbe() tea.Cmd {
	return execProbeCmd(o.ctx, o.kubeconfig, o.selected.KubeContext, o.selected.Namespace, o.debug)
}

func (o *projectOverlay) startLink(project store.Project) tea.Cmd {
	ctx, reqID := o.start(o.ctx)
	o.selected = project
	o.err = nil
	o.linkedName = ""
	o.state = projectOverlayLinking
	var workload *store.WorkloadLink
	var resource *store.ResourceLink
	if o.link.workload != nil {
		link := *o.link.workload
		link.OriginContext = o.client.Context
		link.OriginServer = o.client.Server
		workload = &link
	} else if o.link.resource != nil {
		link := *o.link.resource
		link.OriginContext = o.client.Context
		link.OriginServer = o.client.Server
		resource = &link
	}
	return tea.Batch(func() tea.Msg {
		var err error
		if workload != nil {
			err = o.store.LinkWorkload(ctx, project.ID, *workload)
		} else if resource != nil {
			err = o.store.LinkResource(ctx, project.ID, *resource)
		} else {
			err = fmt.Errorf("no item selected for linking")
		}
		return projectLinkedMsg{reqID: reqID, projectName: project.Name, err: err}
	}, o.spinner.Tick)
}

func openProject(project store.Project, client *k8s.Client, notice string) tea.Cmd {
	return func() tea.Msg { return projectOpenedMsg{project: project, client: client, notice: notice} }
}

func (l pendingLink) subject() string {
	if l.workload != nil {
		return resourceSubject(l.workload.Kind, l.workload.Namespace, l.workload.Name)
	}
	if l.resource != nil {
		return resourceSubject(l.resource.Kind, l.resource.Namespace, l.resource.Name)
	}
	return "item"
}

func (o *projectOverlay) View() string {
	var content string
	switch o.state {
	case projectOverlayLoading:
		content = renderLoadingLine(o.styles, o.spinner.View(), "loading projects", "esc to close", o.contentWidth)
	case projectOverlayList:
		if len(o.list.Items()) == 0 {
			if o.mode == projectModeSwitch {
				content = renderStateLine(o.styles, stateLineEmpty, "no projects yet", "N to create", o.contentWidth)
			} else {
				content = renderStateLine(o.styles, stateLineEmpty, "no projects to link to", "esc to cancel", o.contentWidth)
			}
		} else {
			content = o.list.View()
		}
	case projectOverlayProbing:
		content = renderLoadingLine(o.styles, o.spinner.View(), "probing "+o.selected.KubeContext, "esc to cancel", o.contentWidth)
	case projectOverlayResolving:
		content = renderLoadingLine(o.styles, o.spinner.View(), "checking "+o.selected.KubeContext, "esc to cancel", o.contentWidth)
	case projectOverlayExecOffer:
		content = "auth plugin needs the terminal. run it now? [Y/n]"
		if o.execNudge {
			content += "\n\n" + o.styles.warnText.Render(pressYToConfirm)
		}
	case projectOverlayConfirmLink:
		content = truncateLine(fmt.Sprintf("link %s -> %s", o.link.subject(), o.selected.Name), o.contentWidth, o.styles.glyphs.ellipsis) +
			"\n" + o.styles.dim.Render("stores the link in sk64's local project database") +
			"\n\n" + o.gate.promptLines(o.styles, false)
		if o.gate.message != "" {
			content += "\n" + o.styles.warnText.Render(o.gate.message)
		}
	case projectOverlayLinking:
		content = renderLoadingLine(o.styles, o.spinner.View(), fmt.Sprintf("link %s -> %s", o.link.subject(), o.selected.Name), "cannot cancel", o.contentWidth)
	case projectOverlayLinked:
		if o.err != nil {
			identity := "link " + o.link.subject()
			suffix := " -> " + o.linkedName + " failed"
			identityWidth := o.contentWidth - lipgloss.Width(suffix)
			switch {
			case o.contentWidth <= 0 || lipgloss.Width(identity)+lipgloss.Width(suffix) <= o.contentWidth:
				identity += suffix
			case identityWidth > 0:
				identity = middleElideLine(identity, identityWidth, o.styles.glyphs.ellipsis) + suffix
			default:
				identity = middleElideLine(identity+suffix, o.contentWidth, o.styles.glyphs.ellipsis)
			}
			content = identity + "\n" + renderStateLine(o.styles, stateLineError, o.err.Error(), "enter to close", o.contentWidth)
		} else {
			content = fmt.Sprintf("linked %s to project %s", o.link.subject(), o.linkedName)
		}
	case projectOverlayError:
		content = renderStateLine(o.styles, stateLineError, fmt.Sprintf("project operation unavailable: %v", o.err), "enter to retry", o.contentWidth)
	case projectOverlayUnavailable:
		content = renderStateLine(o.styles, stateLineUnknown, "project database unavailable", "esc to close", o.contentWidth)
	}
	if o.mode == projectModeLink {
		titleText := "Link " + o.link.subject() + " to project"
		switch o.state {
		case projectOverlayConfirmLink, projectOverlayLinking:
			titleText += " " + o.selected.Name
		case projectOverlayLinked:
			titleText += " " + o.linkedName
		}
		title := strings.Join(wrapDialogLines(titleText, o.contentWidth), "\n")
		content = o.styles.dialogTitle.Render(title) + "\n\n" + content
	}
	return o.styles.dialogBox.Width(o.boxWidth).Render(content)
}

func (o *projectOverlay) SetSize(width, height int) {
	o.boxWidth, o.contentWidth = responsiveBoxWidths(
		width,
		projectPreferredWidth,
		projectWidthPercent,
		projectMaxWidth,
		o.styles.dialogBox.GetHorizontalFrameSize(),
	)
	o.list.SetSize(o.contentWidth, max(1, min(projectListMaxHeight, height-projectListChromeHeight)))
	o.itemDescriptionWidth = max(
		0,
		o.list.Width()-o.styles.listItemStyle.NormalTitle.GetPaddingLeft()-o.styles.listItemStyle.NormalTitle.GetPaddingRight(),
	)
	o.gate.setWidth(o.contentWidth)
	o.applyProjectListStyles()
}

func (o *projectOverlay) SetStyles(st *styles) {
	o.styles = st
	o.applyProjectListStyles()
	o.gate.setStyles(st)
	applySpinnerStyle(&o.spinner, st)
}

func (o *projectOverlay) applyProjectListStyles() {
	applyListStyles(&o.list, o.styles)
	delegate := newListDelegate(o.styles)
	delegate.ShowDescription = true
	delegate.DescriptionLines = 1
	for _, item := range o.list.Items() {
		project, ok := item.(projectItem)
		if !ok {
			continue
		}
		delegate.DescriptionLines = max(delegate.DescriptionLines, strings.Count(project.Description(), "\n")+1)
	}
	o.list.SetDelegate(delegate)
}

func (o *projectOverlay) stop() bool {
	if o.state == projectOverlayLinking {
		return false
	}
	return o.loader.stop()
}

func (o *projectOverlay) Hints() footerHints {
	switch o.state {
	case projectOverlayList:
		if o.mode == projectModeLink {
			return hintBindings(hintDesc(bindEnter, "link here"), o.keys.global.Filter, bindEsc)
		}
		return hintBindings(hintDesc(bindEnter, "open"), bindNewN, o.keys.global.Filter, hintDesc(bindEsc, "close"))
	case projectOverlayProbing, projectOverlayResolving:
		return hintBindings(bindEsc)
	case projectOverlayConfirmLink:
		return hintBindings(displayHint("YES", "confirm"), hintDesc(bindEsc, "back"))
	case projectOverlayLinking:
		return hintStatus("linking (cannot cancel)")
	case projectOverlayExecOffer:
		return hintBindings(hintDesc(bindConfirmY, "run"), displayHint("n", "back"), hintDesc(bindEsc, "close"))
	case projectOverlayLinked:
		return hintBindings(hintDesc(bindEnter, "close"))
	case projectOverlayError:
		if o.listReady {
			return hintBindings(hintDesc(bindEnter, "retry"), hintDesc(bindEsc, "back"))
		}
		return hintBindings(hintDesc(bindEnter, "retry"), hintDesc(bindEsc, "close"))
	default:
		return hintBindings(hintDesc(bindEsc, "close"))
	}
}

func (o *projectOverlay) isClosed() bool { return o.closed }
