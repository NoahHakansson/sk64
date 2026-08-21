package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
)

type projectCtxState int

const (
	projectCtxChecking projectCtxState = iota
	projectCtxActive
	projectCtxServerMismatch
	projectCtxInactive
	projectCtxNotFound
	projectCtxUnreadable
)

type projectHeadingItem struct {
	text string
	dim  bool
}

func (i projectHeadingItem) FilterValue() string            { return "" }
func (i projectHeadingItem) sectionHeading() (string, bool) { return i.text, i.dim }
func (i projectHeadingItem) unselectableRow() bool          { return true }

type projectLinkItem struct {
	workload *store.WorkloadLink
	resource *store.ResourceLink
	columns  []rowColumn
	align    *rowAlignment
	styles   *styles
}

func (i projectLinkItem) Title() string {
	identity, _ := i.listColumns()
	return identity
}
func (i projectLinkItem) Description() string { return "" }
func (i projectLinkItem) FilterValue() string {
	if i.workload != nil {
		return i.workload.Kind + "/" + i.workload.Name + rowColumnSeparator + i.workload.Namespace
	}
	return i.resource.Kind + "/" + i.resource.Name + rowColumnSeparator + i.resource.Namespace
}
func (i projectLinkItem) listColumns() (string, []rowColumn) {
	if i.workload != nil {
		return i.workload.Kind + "/" + i.workload.Name + rowColumnSeparator + i.workload.Namespace, i.columns
	}
	return i.resource.Name + rowColumnSeparator + i.resource.Namespace, i.columns
}
func (i projectLinkItem) rowAlignment() *rowAlignment { return i.align }
func (i projectLinkItem) kindBadge() string {
	if i.resource == nil {
		return ""
	}
	return i.styles.resourceBadge(i.resource.Kind)
}
func (i projectLinkItem) kindBadgeSeparator() string { return rowColumnSeparator }
func (i projectLinkItem) filterMatchOffset() int {
	if i.resource == nil {
		return 0
	}
	return utf8.RuneCountInString(i.resource.Kind + "/")
}

type projectScreen struct {
	loader
	dialog
	ctx               context.Context
	client            *k8s.Client
	store             *store.Store
	kubeconfig        string
	project           store.Project
	notice            string
	scanCfg           scanConfig
	env               editEnv
	km                projectKeyMap
	spinner           spinner.Model
	ctxState          projectCtxState
	pendingParts      int
	linksOnly         bool
	collectors        map[string]*refsCollector
	collectorsPending int
	loadCancelled     bool
	cancelledPartial  bool
	loaded            bool
	workloads         []store.WorkloadLink
	resources         []store.ResourceLink
	extraNS           []string
	list              list.Model
	confirmUnlink     bool
	unlinkGate        confirmGate
	unlinkPending     bool
	readErr           error
	ctxErr            error
	unlinkErr         error
}

func newProjectScreen(ctx context.Context, client *k8s.Client, st *store.Store, kubeconfig string, project store.Project, notice string, scanCfg scanConfig, env editEnv, styles *styles) *projectScreen {
	model := newListModel(styles, env.keymaps().list)
	model.Filter = groupedFilter
	return &projectScreen{
		dialog: newDialog(styles, false),
		ctx:    ctx, client: client, store: st, kubeconfig: kubeconfig, project: project,
		notice: notice, scanCfg: scanCfg, env: env, km: env.keymaps().project, spinner: newSpinner(styles), collectors: make(map[string]*refsCollector), list: model,
		unlinkGate: newConfirmGate(styles),
	}
}

func (s *projectScreen) Init() tea.Cmd { return s.reload() }

func (s *projectScreen) reload() tea.Cmd {
	s.stopCollectors()
	ctx, reqID := s.start(s.ctx)
	s.linksOnly = false
	s.pendingParts = 2
	s.ctxState = projectCtxChecking
	s.readErr = nil
	s.ctxErr = nil
	s.unlinkErr = nil
	s.collectors = make(map[string]*refsCollector)
	s.collectorsPending = 0
	return tea.Batch(s.readLinks(ctx, reqID), s.checkContext(ctx, reqID), s.spinner.Tick)
}

func (s *projectScreen) readLinks(ctx context.Context, reqID int) tea.Cmd {
	projectID := s.project.ID
	return func() tea.Msg {
		workloads, err := s.store.WorkloadLinks(ctx, projectID)
		if err != nil {
			return projectLinksMsg{reqID: reqID, err: err}
		}
		resources, err := s.store.ResourceLinks(ctx, projectID)
		if err != nil {
			return projectLinksMsg{reqID: reqID, err: err}
		}
		extraNS, err := s.store.Namespaces(ctx, projectID)
		return projectLinksMsg{reqID: reqID, workloads: workloads, resources: resources, extraNS: extraNS, err: err}
	}
}

func (s *projectScreen) checkContext(ctx context.Context, reqID int) tea.Cmd {
	if s.project.KubeContext == s.client.Context {
		projectID := s.project.ID
		kubeServer := s.project.KubeServer
		clientServer := s.client.Server
		st := s.store
		return func() tea.Msg {
			if kubeServer != "" && !k8s.SameServer(kubeServer, clientServer) {
				return projectContextMsg{reqID: reqID, found: true, kubeServer: kubeServer, serverMismatch: true}
			}
			if kubeServer == "" && clientServer != "" && st != nil {
				if err := st.BackfillProjectKubeServer(ctx, projectID, clientServer); err == nil {
					kubeServer = clientServer
				}
			}
			return projectContextMsg{reqID: reqID, found: true, kubeServer: kubeServer}
		}
	}
	wantContext := s.project.KubeContext
	return func() tea.Msg {
		found, err := k8s.HasContext(s.kubeconfig, wantContext)
		return projectContextMsg{reqID: reqID, found: found, err: err}
	}
}

func (s *projectScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	defer s.layout()
	switch msg := msg.(type) {
	case projectLinksMsg:
		if msg.reqID != s.reqID || !s.pending {
			return s, nil
		}
		s.readErr = msg.err
		if msg.err == nil {
			s.workloads, s.resources, s.extraNS = msg.workloads, msg.resources, msg.extraNS
		}
		s.pendingParts--
		if s.pendingParts == 0 {
			return s, s.finishLoad(msg.reqID)
		}
		return s, nil
	case projectContextMsg:
		if msg.reqID != s.reqID || !s.pending || s.linksOnly {
			return s, nil
		}
		s.ctxErr = msg.err
		if msg.kubeServer != "" {
			s.project.KubeServer = msg.kubeServer
		}
		if msg.err != nil {
			s.ctxState = projectCtxUnreadable
		} else if msg.serverMismatch {
			s.ctxState = projectCtxServerMismatch
		} else if s.project.KubeContext == s.client.Context {
			s.ctxState = projectCtxActive
		} else if msg.found {
			s.ctxState = projectCtxInactive
		} else {
			s.ctxState = projectCtxNotFound
		}
		s.pendingParts--
		if s.pendingParts == 0 {
			return s, s.finishLoad(msg.reqID)
		}
		return s, nil
	case refsPageMsg:
		cmds := make([]tea.Cmd, 0, len(s.collectors))
		finished := false
		for _, collector := range s.collectors {
			wasPending := collector.pending
			cmd, _ := collector.handleRefsPage(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
			if wasPending && !collector.pending {
				s.collectorsPending--
				finished = true
			}
		}
		if finished && s.collectorsPending == 0 {
			s.loadCancelled = false
			s.cancelledPartial = false
			s.loaded = true
			cmds = append(cmds, s.setItems())
		}
		return s, tea.Batch(cmds...)
	case projectUnlinkedMsg:
		if !s.finish(msg.reqID) {
			return s, nil
		}
		s.unlinkPending = false
		if msg.err != nil {
			s.unlinkErr = msg.err
			return s, nil
		}
		s.confirmUnlink = false
		return s, s.reloadLinks()
	case projectSavedMsg:
		if msg.project.ID != s.project.ID {
			return s, nil
		}
		s.project = msg.project
		s.notice = ""
		return s, s.reload()
	case scanLinksAppliedMsg:
		if msg.projectID != s.project.ID {
			return s, nil
		}
		return s, s.reload()
	case spinner.TickMsg:
		if !s.anyPending() {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyPressMsg:
		return s, s.updateKey(msg)
	}
	return s, s.updateList(msg)
}

func (s *projectScreen) finishLoad(reqID int) tea.Cmd {
	if !s.finish(reqID) {
		return nil
	}
	if s.linksOnly || s.readErr != nil || s.ctxState != projectCtxActive || len(s.workloads)+len(s.resources) == 0 {
		if s.readErr == nil && (s.linksOnly || s.ctxErr == nil) {
			s.loadCancelled = false
			s.cancelledPartial = false
			s.loaded = true
		}
		return s.setItems()
	}
	namespaces := make(map[string]struct{})
	for _, link := range s.workloads {
		namespaces[link.Namespace] = struct{}{}
	}
	for _, link := range s.resources {
		namespaces[link.Namespace] = struct{}{}
	}
	setItems := s.setItems()
	s.collectors = make(map[string]*refsCollector, len(namespaces))
	cmds := make([]tea.Cmd, 0, len(namespaces)+2)
	cmds = append(cmds, setItems)
	for namespace := range namespaces {
		collector := newRefsCollector(s.ctx, s.client, namespace, true)
		s.collectors[namespace] = &collector
		cmds = append(cmds, collector.startCollect())
	}
	s.collectorsPending = len(s.collectors)
	cmds = append(cmds, s.spinner.Tick)
	return tea.Batch(cmds...)
}

func (s *projectScreen) reloadLinks() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.linksOnly = true
	s.pendingParts = 1
	s.readErr = nil
	return s.readLinks(ctx, reqID)
}

func (s *projectScreen) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if s.unlinkPending {
		return nil
	}
	if s.anyPending() {
		if bubbleskey.Matches(msg, s.km.Back) {
			s.confirmUnlink = false
			return s.cancelPendingLoad()
		}
		return nil
	}
	if s.loadCancelled {
		if bubbleskey.Matches(msg, s.km.Refresh) {
			return s.reload()
		}
		return nil
	}
	if s.confirmUnlink {
		if bubbleskey.Matches(msg, s.km.Back) {
			s.confirmUnlink = false
			return nil
		}
		confirmed, cmd := s.unlinkGate.handleKey(msg)
		if confirmed {
			return s.startUnlink()
		}
		return cmd
	}
	if s.list.SettingFilter() {
		return s.updateList(msg)
	}
	switch {
	case bubbleskey.Matches(msg, s.km.Back):
		if s.list.FilterState() != list.Unfiltered {
			return s.updateList(msg)
		}
	case bubbleskey.Matches(msg, s.km.Open):
		return s.openSelected()
	case bubbleskey.Matches(msg, s.km.Unlink):
		if _, ok := s.list.SelectedItem().(projectLinkItem); ok {
			s.confirmUnlink = true
			s.unlinkErr = nil
			return s.unlinkGate.arm()
		}
	case bubbleskey.Matches(msg, s.km.Edit):
		meta := store.ProjectMeta{
			Name: s.project.Name, RootPath: s.project.RootPath, KubeContext: s.project.KubeContext,
			KubeServer: s.project.KubeServer, Namespace: s.project.Namespace,
			SwitchPromptSuppressed: s.project.SwitchPromptSuppressed,
		}
		return pushScreen(newProjectFormScreen(s.ctx, s.store, s.kubeconfig, s.scanCfg, formEdit, &s.project, meta, s.extraNS, s.env.keymaps(), s.styles))
	case bubbleskey.Matches(msg, s.km.Scan):
		return pushScreen(newSuggestionScreen(s.ctx, s.client, s.store, s.project, s.scanCfg, s.ctxState == projectCtxActive, s.env, s.styles))
	case bubbleskey.Matches(msg, s.km.Refresh):
		return s.reload()
	}
	return s.updateList(msg)
}

func (s *projectScreen) updateList(msg tea.Msg) tea.Cmd {
	previous := s.list.Index()
	cmd := updateListModel(&s.list, msg)
	clampToSelectable(&s.list, previous)
	return cmd
}

func (s *projectScreen) startUnlink() tea.Cmd {
	row, ok := s.list.SelectedItem().(projectLinkItem)
	if !ok {
		return nil
	}
	s.confirmUnlink = false
	s.unlinkPending = true
	ctx, reqID := s.start(s.ctx)
	projectID := s.project.ID
	var workload *store.WorkloadLink
	var resource *store.ResourceLink
	if row.workload != nil {
		link := *row.workload
		workload = &link
	} else {
		link := *row.resource
		resource = &link
	}
	return func() tea.Msg {
		var err error
		if workload != nil {
			err = s.store.UnlinkWorkload(ctx, projectID, *workload)
		} else {
			err = s.store.UnlinkResource(ctx, projectID, resource.Kind, resource.Namespace, resource.Name)
		}
		return projectUnlinkedMsg{reqID: reqID, err: err}
	}
}

func (s *projectScreen) openSelected() tea.Cmd {
	row, ok := s.list.SelectedItem().(projectLinkItem)
	if !ok {
		return nil
	}
	if row.workload != nil {
		collector := s.collectors[row.workload.Namespace]
		if s.ctxState != projectCtxActive || collector == nil || collector.index == nil {
			return nil
		}
		for _, entry := range collector.index.Workloads() {
			if entry.Workload.Kind == row.workload.Kind && entry.Workload.Name == row.workload.Name {
				return pushScreen(newWorkloadRefsScreen(s.ctx, s.client, row.workload.Namespace, refRowsFor(collector.index, entry.Refs), row.workload.Kind, row.workload.Name, s.env, s.styles))
			}
		}
		return nil
	}
	if s.ctxState != projectCtxActive || s.resourceMissing(*row.resource) {
		return nil
	}
	link := row.resource
	return pushScreen(newKeyScreen(s.ctx, s.client, link.Kind, link.Namespace, link.Name, s.env, s.styles))
}

func (s *projectScreen) setItems() tea.Cmd {
	align := &rowAlignment{}
	items := []list.Item{projectHeadingItem{text: "Workloads"}}
	for i := range s.workloads {
		link := &s.workloads[i]
		items = append(items, projectLinkItem{workload: link, columns: s.workloadColumns(*link), align: align, styles: s.styles})
	}
	if len(s.workloads) == 0 {
		items = append(items, projectHeadingItem{text: "(none)", dim: true})
	}
	items = append(items, projectHeadingItem{text: "Directly linked resources"})
	for i := range s.resources {
		link := &s.resources[i]
		items = append(items, projectLinkItem{resource: link, columns: s.resourceColumns(*link), align: align, styles: s.styles})
	}
	if len(s.resources) == 0 {
		items = append(items, projectHeadingItem{text: "(none)", dim: true})
	}
	*align = measureRowAlignment(items)
	cmd := scopeListFilterCmd(&s.list, s.list.SetItems(items))
	s.list.Select(0)
	clampToSelectable(&s.list, s.list.Index())
	return cmd
}

func (s *projectScreen) View() string {
	if s.confirmUnlink {
		return s.render(s.unlinkDialog())
	}
	parts := s.headerLines()
	stateLine := s.stateLine()
	s.list.Title = s.statusRow()
	parts = append(parts, fitListHeight(renderListWithoutPrematureEmpty(s.list, stateLine != ""), s.list.Height()))
	if stateLine != "" {
		parts = append(parts, stateLine)
	}
	return strings.Join(parts, "\n")
}

func (s *projectScreen) headerLines() []string {
	nameLine := s.styles.dialogTitle.Render(s.project.Name) + "  " + s.styles.dim.Render(s.project.RootPath)
	parts := []string{truncateLine(nameLine, s.width, s.styles.glyphs.ellipsis)}
	contextLine := s.styles.dim.Render("context: ") + s.project.KubeContext
	serverLine := s.styles.dim.Render("server: ") + serverOrUnverified(s.project.KubeServer)
	identityLine := contextLine + "   " + serverLine
	if s.width <= 0 || lipgloss.Width(identityLine) <= s.width {
		parts = append(parts, identityLine)
	} else {
		parts = append(parts, truncateLine(contextLine, s.width, s.styles.glyphs.ellipsis), middleElideLine(serverLine, s.width, s.styles.glyphs.ellipsis))
	}
	namespaces := strings.Join(append([]string{s.project.Namespace}, s.extraNS...), ", ")
	parts = append(parts, truncateLine(s.styles.dim.Render("namespaces: ")+namespaces, s.width, s.styles.glyphs.ellipsis))
	if s.notice != "" {
		parts = append(parts, s.styles.dim.Render(s.notice))
	}
	switch s.ctxState {
	case projectCtxNotFound:
		parts = append(parts, s.styles.errText.Render(fmt.Sprintf("%s context %q not found in kubeconfig%se to re-point", s.styles.glyphs.contextNotFoundTag, s.project.KubeContext, s.styles.glyphs.separator)))
	case projectCtxInactive:
		parts = append(parts, s.styles.dim.Render(fmt.Sprintf("%s context %s is not active%sreopen via ctrl+p to switch", s.styles.glyphs.inactiveTag, s.project.KubeContext, s.styles.glyphs.separator)))
	case projectCtxServerMismatch:
		mismatch := renderRowColumns(
			s.width,
			s.styles.glyphs.serverMismatchTag,
			s.styles.glyphs.ellipsis,
			rowColumn{text: "saved " + serverOrUnverified(s.project.KubeServer), critical: true},
			rowColumn{text: "active " + serverOrUnverified(s.client.Server), critical: true},
		)
		parts = append(
			parts,
			s.styles.errText.Render(mismatch),
			s.styles.errText.Render(truncateLine("reopen via ctrl+p to review and rebind", s.width, s.styles.glyphs.ellipsis)),
		)
	}
	return parts
}

func (s *projectScreen) statusRow() string {
	if s.anyPending() {
		return ""
	}
	if s.list.FilterState() == list.FilterApplied {
		visible := 0
		for _, item := range s.list.VisibleItems() {
			if _, ok := item.(projectLinkItem); ok {
				visible++
			}
		}
		return statusRowText(
			s.styles,
			s.width,
			`filter "`+s.list.FilterInput.Value()+`"`,
			fmt.Sprintf("%d of %d", visible, len(s.workloads)+len(s.resources)),
		)
	}
	return statusRowText(s.styles, s.width, plural(len(s.workloads), "workload"), plural(len(s.resources), "resource"))
}

func (s *projectScreen) stateLine() string {
	if s.anyPending() {
		message := "loading project"
		action := "esc to cancel"
		if s.unlinkPending {
			message = "unlinking"
			action = "cannot cancel"
		} else if s.collectorsPending > 0 {
			message = "collecting references"
		}
		return renderLoadingLine(s.styles, s.spinner.View(), message, action, s.width)
	}
	if s.readErr != nil {
		return renderStateLine(s.styles, stateLineError, fmt.Sprintf("project data unavailable: %v", s.readErr), bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.ctxState == projectCtxUnreadable {
		return renderStateLine(s.styles, stateLineError, fmt.Sprintf("could not read kubeconfig: %v", s.ctxErr), bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.unlinkErr != nil {
		return renderStateLine(s.styles, stateLineError, fmt.Sprintf("unlink failed: %v", s.unlinkErr), bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.loadCancelled {
		kind := stateLineUnknown
		message := "project load cancelled; results unknown"
		if s.cancelledPartial {
			kind = stateLineIncomplete
			message = "project load cancelled; retained rows incomplete"
		}
		return renderStateLine(s.styles, kind, message, bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if affected := s.incompleteCollectorNamespaces(); affected > 0 {
		message := fmt.Sprintf("reference collection incomplete: %s affected", plural(affected, "namespace"))
		return renderStateLine(s.styles, stateLineIncomplete, message, bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.loaded && len(s.workloads)+len(s.resources) == 0 {
		return renderStateLine(s.styles, stateLineEmpty, "no linked workloads or resources", "s to scan", s.width)
	}
	return ""
}

func (s *projectScreen) incompleteCollectorNamespaces() int {
	affected := 0
	for _, collector := range s.collectors {
		if collector.index != nil && len(collector.index.FailedSources()) > 0 {
			affected++
		}
	}
	return affected
}

func (s *projectScreen) unlinkDialog() dialogContent {
	content := dialogContent{
		title: "Unlink " + s.selectedName() + " from project " + s.project.Name + "?",
		body: []string{
			"This removes the link from sk64's local project database only.",
			"Nothing in the cluster changes. L or s can link it again.",
		},
	}
	content.prompt = s.unlinkGate.promptLines(s.styles, false)
	if s.unlinkGate.message != "" {
		content.message = s.unlinkGate.message
		content.isWarning = true
	} else if s.unlinkErr != nil {
		content.message = fmt.Sprintf("unlink failed: %v", s.unlinkErr)
		content.isError = true
	}
	return content
}

func (s *projectScreen) workloadColumns(link store.WorkloadLink) []rowColumn {
	origin := s.originDetails(link.OriginContext, link.OriginServer)
	withOrigin := func(columns []rowColumn) []rowColumn {
		if origin != "" {
			columns = append(columns, rowColumn{text: origin, critical: true})
		}
		return columns
	}
	if s.loadCancelled {
		return withOrigin(nil)
	}
	switch s.ctxState {
	case projectCtxNotFound:
		return withOrigin([]rowColumn{{text: s.styles.errText.Render(s.styles.glyphs.contextNotFoundTag), critical: true}})
	case projectCtxInactive:
		return withOrigin([]rowColumn{{text: s.styles.dim.Render(s.styles.glyphs.inactiveTag), critical: true}})
	case projectCtxServerMismatch:
		return withOrigin([]rowColumn{{text: s.styles.errText.Render(s.styles.glyphs.serverMismatchTag), critical: true}})
	case projectCtxActive:
		collector := s.collectors[link.Namespace]
		if collector == nil || collector.index == nil || collector.pending {
			return withOrigin(nil)
		}
		for _, entry := range collector.index.Workloads() {
			if entry.Workload.Kind != link.Kind || entry.Workload.Name != link.Name {
				continue
			}
			columns := []rowColumn{
				{text: entry.Workload.Ready, critical: true},
				{text: fmt.Sprintf("refs: %d", len(entry.Refs))},
			}
			subPath, rollout := false, false
			for _, ref := range entry.Refs {
				subPath = subPath || ref.SubPath
				rollout = rollout || ref.RolloutNeeded
			}
			if markers := consumptionMarkers(subPath, rollout, s.styles); markers != "" {
				columns = append(columns, rowColumn{text: markers, critical: true})
			}
			return withOrigin(columns)
		}
		if slices.Contains(collector.index.FailedSources(), k8s.SourceName(link.Kind)) {
			return withOrigin([]rowColumn{{text: s.styles.dim.Render(s.styles.glyphs.noAccessTag), critical: true}})
		}
		return withOrigin([]rowColumn{{text: s.styles.errText.Render(s.styles.glyphs.missingTag), critical: true}})
	}
	return withOrigin(nil)
}

func (s *projectScreen) resourceColumns(link store.ResourceLink) []rowColumn {
	origin := s.originDetails(link.OriginContext, link.OriginServer)
	withOrigin := func(columns []rowColumn) []rowColumn {
		if origin != "" {
			columns = append(columns, rowColumn{text: origin, critical: true})
		}
		return columns
	}
	if s.loadCancelled {
		return withOrigin([]rowColumn{{text: s.styles.dim.Render("(" + link.Source + ")")}})
	}
	switch s.ctxState {
	case projectCtxNotFound:
		return withOrigin([]rowColumn{{text: s.styles.errText.Render(s.styles.glyphs.contextNotFoundTag), critical: true}})
	case projectCtxInactive:
		return withOrigin([]rowColumn{{text: s.styles.dim.Render(s.styles.glyphs.inactiveTag), critical: true}})
	case projectCtxServerMismatch:
		return withOrigin([]rowColumn{{text: s.styles.errText.Render(s.styles.glyphs.serverMismatchTag), critical: true}})
	case projectCtxActive:
		columns := []rowColumn{{text: s.styles.dim.Render("(" + link.Source + ")")}}
		collector := s.collectors[link.Namespace]
		if collector != nil && collector.index != nil && !collector.pending {
			subPath, rollout := false, false
			for _, consumer := range collector.index.ConsumersOf(link.Kind, link.Name) {
				subPath = subPath || consumer.Ref.SubPath
				rollout = rollout || consumer.Ref.RolloutNeeded
			}
			if markers := consumptionMarkers(subPath, rollout, s.styles); markers != "" {
				columns = append(columns, rowColumn{text: markers, critical: true})
			}
		}
		if s.resourceMissing(link) {
			columns = append(columns, rowColumn{text: s.styles.errText.Render(s.styles.glyphs.missingTag), critical: true})
		}
		return withOrigin(columns)
	default:
		return withOrigin(nil)
	}
}

func (s *projectScreen) originDetails(originContext, originServer string) string {
	if s.project.KubeServer == "" || originServer == "" || k8s.SameServer(originServer, s.project.KubeServer) {
		return ""
	}
	detail := s.styles.glyphs.originMismatchTag
	if originContext != "" {
		detail += " " + originContext
	}
	return s.styles.warnText.Render(detail)
}

func consumptionMarkers(subPath, rollout bool, st *styles) string {
	markers := make([]string, 0, 2)
	if subPath {
		markers = append(markers, st.warnText.Render(st.glyphs.subPathMarker))
	}
	if rollout {
		markers = append(markers, st.warnText.Render(st.glyphs.rolloutMarker))
	}
	return strings.Join(markers, " ")
}

func (s *projectScreen) resourceMissing(link store.ResourceLink) bool {
	if s.ctxState != projectCtxActive {
		return false
	}
	collector := s.collectors[link.Namespace]
	return collector != nil && collector.index != nil && !collector.pending && collector.index.Missing(link.Kind, link.Name)
}

func (s *projectScreen) selectedName() string {
	row, ok := s.list.SelectedItem().(projectLinkItem)
	if !ok {
		return "item"
	}
	if row.workload != nil {
		return row.workload.Kind + "/" + row.workload.Name
	}
	return row.resource.Kind + "/" + row.resource.Name
}

func (s *projectScreen) cancelPendingLoad() tea.Cmd {
	partial := len(s.workloads)+len(s.resources) > 0
	if !s.stop() {
		return nil
	}
	s.loadCancelled = true
	s.cancelledPartial = partial
	if partial {
		return s.setItems()
	}
	return nil
}

func (s *projectScreen) stopCollectors() bool {
	stopped := false
	for _, collector := range s.collectors {
		if collector.stop() {
			stopped = true
		}
	}
	s.collectorsPending = 0
	return stopped
}

func (s *projectScreen) stop() bool {
	stopped := false
	if !s.unlinkPending {
		stopped = s.loader.stop()
	}
	return s.stopCollectors() || stopped
}

func (s *projectScreen) anyPending() bool { return s.pending || s.collectorsPending > 0 }

func (s *projectScreen) SetSize(width, height int) {
	s.resize(width, height)
	s.unlinkGate.setWidth(s.contentWidth())
	s.layout()
}
func (s *projectScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
	s.unlinkGate.setStyles(st)
	applySpinnerStyle(&s.spinner, st)
}

func (s *projectScreen) layout() {
	height := s.height - len(s.headerLines())
	if s.stateLine() != "" {
		height--
	}
	s.list.SetSize(s.width, max(0, height))
}
func (s *projectScreen) Title() string { return s.project.Name }
func (s *projectScreen) Hints() footerHints {
	if s.unlinkPending {
		return hintStatus("unlinking (cannot cancel)")
	}
	if s.anyPending() {
		return hintBindings(hintDesc(s.km.Back, "cancel"))
	}
	if s.loadCancelled {
		return browsingHints(hintDesc(s.km.Refresh, "retry"))
	}
	if s.confirmUnlink {
		return hintBindings(displayHint("YES", "confirm"), hintDesc(s.km.Back, "cancel"))
	}
	if s.list.SettingFilter() {
		return hintBindings(displayHint("enter", "accept"), hintDesc(s.km.Back, "cancel"))
	}
	return browsingHints(s.km.Open, s.km.Scan, s.km.Unlink, s.km.Edit, s.env.keymaps().global.Filter)
}
func (s *projectScreen) Help() helpGroup {
	return helpGroup{title: "project", entries: []helpGroupEntry{
		{binding: s.km.Open, desc: "open the linked workload or resource"},
		{binding: s.km.Scan, desc: "scan the repository for suggestions"},
		{binding: s.km.Unlink, desc: "unlink the item under the cursor"},
		{binding: s.km.Edit, desc: "edit project metadata"},
	}}
}
func (s *projectScreen) CapturesInput() bool { return s.confirmUnlink || s.list.SettingFilter() }
func (s *projectScreen) WantsEsc() bool {
	return s.confirmUnlink || s.list.FilterState() != list.Unfiltered || s.anyPending()
}
