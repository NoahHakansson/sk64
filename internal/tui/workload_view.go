package tui

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
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

type workloadItem struct {
	entry      k8s.WorkloadEntry
	styles     *styles
	kindColumn string
	align      *rowAlignment
}

var workloadReadinessPattern = regexp.MustCompile(`^(\d+)/(\d+) ready$`)

func (i workloadItem) Title() string {
	identity, columns := i.listColumns()
	return i.entry.Workload.Kind + rowColumnSeparator + renderRowColumns(0, identity, "", columns...)
}
func (i workloadItem) listColumns() (string, []rowColumn) {
	workload := i.entry.Workload
	readiness := workload.Ready
	if workload.Kind == k8s.KindCronJob {
		readiness = i.styles.dim.Render(i.styles.glyphs.cronMarker + " " + readiness)
	} else if matches := workloadReadinessPattern.FindStringSubmatch(readiness); matches != nil {
		ready, _ := strconv.Atoi(matches[1])
		desired, _ := strconv.Atoi(matches[2])
		switch ready {
		case desired:
			readiness = i.styles.successText.Render(readiness)
		case 0:
			readiness = i.styles.errText.Render(readiness)
		default:
			readiness = i.styles.warnText.Render(readiness)
		}
	}
	return workload.Name, []rowColumn{
		{text: readiness},
		{text: fmt.Sprintf("refs: %d", len(i.entry.Refs)), critical: true},
	}
}
func (i workloadItem) Description() string         { return "" }
func (i workloadItem) FilterValue() string         { return i.entry.Workload.Name }
func (i workloadItem) rowAlignment() *rowAlignment { return i.align }
func (i workloadItem) prefixColumn() string        { return i.kindColumn }

type orphanItem struct {
	podNames   []string
	refs       []k8s.ResourceRef
	kindColumn string
	align      *rowAlignment
}

func (i orphanItem) Title() string {
	identity, columns := i.listColumns()
	return "Pods" + rowColumnSeparator + renderRowColumns(0, identity, "", columns...)
}
func (i orphanItem) listColumns() (string, []rowColumn) {
	return fmt.Sprintf("orphaned (%d)", len(i.podNames)), []rowColumn{{text: fmt.Sprintf("refs: %d", len(i.refs)), critical: true}}
}
func (i orphanItem) Description() string         { return "" }
func (i orphanItem) FilterValue() string         { return strings.Join(i.podNames, " ") }
func (i orphanItem) rowAlignment() *rowAlignment { return i.align }
func (i orphanItem) prefixColumn() string        { return i.kindColumn }

type workloadScreen struct {
	refsCollector
	env               editEnv
	km                workloadKeyMap
	styles            *styles
	list              list.Model
	spinner           spinner.Model
	width, bodyHeight int
}

func newWorkloadScreen(ctx context.Context, client *k8s.Client, namespace string, env editEnv, st *styles) *workloadScreen {
	return &workloadScreen{
		refsCollector: newRefsCollector(ctx, client, namespace, true),
		env:           env,
		km:            env.keymaps().workload,
		styles:        st,
		list:          newListModel(st, env.keymaps().list),
		spinner:       newSpinner(st),
	}
}

func (s *workloadScreen) Init() tea.Cmd {
	return s.startLoading()
}

func (s *workloadScreen) startLoading() tea.Cmd {
	s.list.ResetFilter()
	setItems := scopeListFilterCmd(&s.list, s.list.SetItems(nil))
	s.layout()
	return tea.Batch(setItems, s.startCollect(), s.spinner.Tick)
}

func (s *workloadScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.pending {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case refsPageMsg:
		cmd, done := s.handleRefsPage(msg)
		if !done {
			return s, cmd
		}
		setItems := s.setItems()
		s.layout()
		return s, tea.Batch(cmd, setItems)
	case tea.KeyPressMsg:
		if s.list.SettingFilter() {
			return s, updateListModel(&s.list, msg)
		}
		switch {
		case bubbleskey.Matches(msg, s.km.CancelLoad):
			if s.stop() {
				s.cancelled = true
				s.layout()
				return s, nil
			}
		case bubbleskey.Matches(msg, s.km.Refresh):
			if !s.pending {
				return s, s.startLoading()
			}
		case bubbleskey.Matches(msg, s.km.Open):
			switch selected := s.list.SelectedItem().(type) {
			case workloadItem:
				return s, pushScreen(newWorkloadRefsScreen(s.ctx, s.client, s.namespace, s.rowsFor(selected.entry.Refs), selected.entry.Workload.Kind, selected.entry.Workload.Name, s.env, s.styles))
			case orphanItem:
				return s, pushScreen(newWorkloadRefsScreen(s.ctx, s.client, s.namespace, s.rowsFor(selected.refs), "", "orphaned pods", s.env, s.styles))
			}
		case bubbleskey.Matches(msg, s.km.Link):
			if selected, ok := s.list.SelectedItem().(workloadItem); ok {
				workload := selected.entry.Workload
				link := store.WorkloadLink{Kind: workload.Kind, Namespace: s.namespace, Name: workload.Name}
				return s, func() tea.Msg { return openProjectPickerMsg{link: pendingLink{workload: &link}} }
			}
		}
	}
	return s, updateListModel(&s.list, msg)
}

func (s *workloadScreen) setItems() tea.Cmd {
	entries := s.index.Workloads()
	items := make([]list.Item, 0, len(entries)+1)
	podNames, refs := s.index.Orphans()
	kindWidth := 0
	for _, entry := range entries {
		kindWidth = max(kindWidth, lipgloss.Width(entry.Workload.Kind))
	}
	if len(podNames) > 0 {
		kindWidth = max(kindWidth, lipgloss.Width("Pods"))
	}
	align := &rowAlignment{}
	for _, entry := range entries {
		kind := entry.Workload.Kind
		items = append(items, workloadItem{
			entry: entry, styles: s.styles, align: align,
			kindColumn: kind + strings.Repeat(" ", kindWidth-lipgloss.Width(kind)),
		})
	}
	if len(podNames) > 0 {
		items = append(items, orphanItem{
			podNames: podNames, refs: refs, align: align,
			kindColumn: "Pods" + strings.Repeat(" ", kindWidth-lipgloss.Width("Pods")),
		})
	}
	*align = measureRowAlignment(items)
	return scopeListFilterCmd(&s.list, s.list.SetItems(items))
}

func (s *workloadScreen) rowsFor(refs []k8s.ResourceRef) []refRow {
	return refRowsFor(s.index, refs)
}

func refRowsFor(index *k8s.RefIndex, refs []k8s.ResourceRef) []refRow {
	rows := make([]refRow, 0, len(refs))
	for _, ref := range refs {
		rows = append(rows, refRow{ref: ref, missing: index.Missing(ref.Kind, ref.Name)})
	}
	return rows
}

func (s *workloadScreen) View() string {
	s.list.Title = s.statusRow()
	stateLine := s.stateLine()
	parts := []string{renderListWithoutPrematureEmpty(s.list, stateLine != "")}
	if stateLine != "" {
		parts = append(parts, stateLine)
	}
	return strings.Join(parts, "\n")
}

func (s *workloadScreen) statusRow() string {
	if s.pending || len(s.list.Items()) == 0 {
		return ""
	}
	if s.list.FilterState() == list.FilterApplied {
		return statusRowText(s.styles, s.width, listStatusSegments(&s.list, "workload")...)
	}
	count := 0
	for _, item := range s.list.Items() {
		if _, ok := item.(workloadItem); ok {
			count++
		}
	}
	return statusRowText(s.styles, s.width, plural(count, "workload"))
}

func (s *workloadScreen) stateLine() string {
	if s.pending {
		return renderLoadingLine(s.styles, s.spinner.View(), "loading workload references", "esc to cancel", s.width)
	}
	if s.cancelled {
		return renderStateLine(s.styles, stateLineUnknown, "workload scan cancelled; results unknown", bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if sources := failedSources(s.index); len(sources) > 0 {
		return renderStateLine(s.styles, stateLineIncomplete, "scan incomplete: "+strings.Join(sources, ", ")+" unavailable", bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if filtered := filteredListState(s.list, s.styles, s.width); filtered != "" {
		return filtered
	}
	if s.complete && len(s.list.Items()) == 0 {
		return renderStateLine(s.styles, stateLineEmpty, "no workloads found in namespace "+s.namespace, bindingAction(s.km.Refresh, "to refresh"), s.width)
	}
	return ""
}

func (s *workloadScreen) SetSize(width, height int) {
	s.width, s.bodyHeight = width, height
	s.layout()
}

func (s *workloadScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
	applySpinnerStyle(&s.spinner, st)
}

func (s *workloadScreen) layout() {
	extra := 0
	if s.stateLine() != "" {
		extra++
	}
	s.list.SetSize(s.width, max(0, s.bodyHeight-extra))
}

func (s *workloadScreen) Title() string { return s.namespace + " workloads" }
func (s *workloadScreen) Hints() footerHints {
	return browsingHints(hintDesc(s.km.Open, "refs"), s.km.Link, s.env.keymaps().global.Filter)
}
func (s *workloadScreen) Help() helpGroup {
	return helpGroup{title: "workloads", entries: []helpGroupEntry{
		{binding: s.km.Open, desc: "resources this workload references"},
		{binding: s.km.Link, desc: "link this workload to a project"},
	}}
}
func (s *workloadScreen) CapturesInput() bool { return s.list.SettingFilter() }
func (s *workloadScreen) WantsEsc() bool {
	return s.pending || s.list.FilterState() != list.Unfiltered
}

func failedSources(index *k8s.RefIndex) []string {
	if index == nil {
		return nil
	}
	return index.FailedSources()
}

type refRow struct {
	ref     k8s.ResourceRef
	missing bool
}

type refItem struct {
	row    refRow
	styles *styles
	align  *rowAlignment
}

func (i refItem) Title() string {
	identity, columns := i.listColumns()
	return renderRowColumns(0, identity, "", columns...)
}
func (i refItem) listColumns() (string, []rowColumn) {
	columns := referenceListColumns(i.row.ref, i.styles)
	if i.row.missing {
		columns = append(columns, rowColumn{text: i.styles.errText.Render(i.styles.glyphs.missingTag), critical: true})
	} else {
		columns = append(columns, rowColumn{})
	}
	return i.styles.resourceBadge(i.row.ref.Kind) + " " + i.row.ref.Name, columns
}
func (i refItem) Description() string         { return "" }
func (i refItem) FilterValue() string         { return i.row.ref.Name }
func (i refItem) rowAlignment() *rowAlignment { return i.align }
func (i refItem) filterMatchOffset() int {
	return utf8.RuneCountInString(i.styles.resourceBadge(i.row.ref.Kind) + " ")
}

type workloadRefsScreen struct {
	ctx        context.Context
	client     *k8s.Client
	namespace  string
	kind, name string
	env        editEnv
	km         workloadKeyMap
	styles     *styles
	list       list.Model
	items      []list.Item
	width      int
}

func newWorkloadRefsScreen(ctx context.Context, client *k8s.Client, namespace string, rows []refRow, kind, name string, env editEnv, st *styles) *workloadRefsScreen {
	items := make([]list.Item, 0, len(rows))
	align := &rowAlignment{}
	for _, row := range rows {
		items = append(items, refItem{row: row, styles: st, align: align})
	}
	*align = measureRowAlignment(items)
	return &workloadRefsScreen{ctx: ctx, client: client, namespace: namespace, kind: kind, name: name, env: env, km: env.keymaps().workload, styles: st, list: newListModel(st, env.keymaps().list), items: items}
}

func (s *workloadRefsScreen) Init() tea.Cmd {
	return scopeListFilterCmd(&s.list, s.list.SetItems(s.items))
}
func (s *workloadRefsScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if s.list.SettingFilter() {
			return s, updateListModel(&s.list, msg)
		}
		switch {
		case bubbleskey.Matches(key, s.km.Open):
			ref, ok := s.selectedRef()
			if !ok {
				return s, nil
			}
			return s, pushScreen(newKeyScreen(s.ctx, s.client, ref.Kind, s.namespace, ref.Name, s.env, s.styles))
		case bubbleskey.Matches(key, s.km.Link):
			ref, ok := s.selectedRef()
			if !ok {
				return s, nil
			}
			link := store.ResourceLink{Kind: ref.Kind, Namespace: s.namespace, Name: ref.Name, Source: store.SourceManual}
			return s, func() tea.Msg { return openProjectPickerMsg{link: pendingLink{resource: &link}} }
		}
	}
	return s, updateListModel(&s.list, msg)
}

func (s *workloadRefsScreen) selectedRef() (k8s.ResourceRef, bool) {
	selected, ok := s.list.SelectedItem().(refItem)
	if !ok || selected.row.missing {
		return k8s.ResourceRef{}, false
	}
	return selected.row.ref, true
}

func (s *workloadRefsScreen) View() string {
	s.list.Title = s.statusRow()
	return strings.Join([]string{renderSubjectLine(s.subject(), s.width, s.styles), s.list.View()}, "\n")
}
func (s *workloadRefsScreen) statusRow() string {
	return statusRowText(s.styles, s.width, listStatusSegments(&s.list, "reference")...)
}
func (s *workloadRefsScreen) SetSize(width, height int) {
	s.width = width
	s.list.SetSize(width, max(0, height-1))
}
func (s *workloadRefsScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
}
func (s *workloadRefsScreen) subject() string {
	if s.kind == "" {
		return s.name + " in " + s.namespace
	}
	return s.kind + " " + s.namespace + "/" + s.name
}
func (s *workloadRefsScreen) Title() string {
	if s.kind == "" {
		return s.name
	}
	return s.kind + "/" + s.name
}
func (s *workloadRefsScreen) Hints() footerHints {
	return browsingHints(s.km.Open, s.km.Link, s.env.keymaps().global.Filter)
}
func (s *workloadRefsScreen) Help() helpGroup {
	return helpGroup{title: "referenced resources", entries: []helpGroupEntry{
		{binding: s.km.Open, desc: "open the resource"},
		{binding: s.km.Link, desc: "link this resource to a project"},
	}}
}
func (s *workloadRefsScreen) CapturesInput() bool { return s.list.SettingFilter() }
func (s *workloadRefsScreen) WantsEsc() bool      { return s.list.FilterState() != list.Unfiltered }

func referenceListColumns(ref k8s.ResourceRef, st *styles) []rowColumn {
	tags := make([]string, 0, len(ref.Tags))
	for _, tag := range ref.Tags {
		value := string(tag)
		if tag == k8s.TagEnv && len(ref.Keys) > 0 {
			value += "(" + strings.Join(ref.Keys, ",") + ")"
		}
		tags = append(tags, value)
	}
	tagColumn := ""
	if len(tags) > 0 {
		tagColumn = st.dim.Render(strings.Join(tags, " "))
	}
	return []rowColumn{
		{text: tagColumn},
		{text: consumptionMarkers(ref.SubPath, ref.RolloutNeeded, st), critical: true},
	}
}
