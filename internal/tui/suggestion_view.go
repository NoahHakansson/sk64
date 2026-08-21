package tui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/debuglog"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
)

type scanConfig struct{ depth, maxFiles int }

const maxSuggestionChecks = 8

type prefixMatchMsg struct {
	reqID           int
	kind, namespace string
	matches         map[string]string
	err             error
}

type suggestionRowState int

const (
	rowUnchecked suggestionRowState = iota
	rowFound
	rowNotFound
	rowCheckFailed
	rowLinked
	rowLinkFailed
)

type suggestionRow struct {
	sug     project.Suggestion
	ns      string
	state   suggestionRowState
	matched string
	err     error
}

type suggestionItem struct {
	index        int
	row          *suggestionRow
	align        *rowAlignment
	styles       *styles
	checkCluster bool
}

func (i suggestionItem) Title() string {
	identity, _ := i.listColumns()
	return identity
}
func (i suggestionItem) Description() string { return "" }
func (i suggestionItem) FilterValue() string {
	value := i.row.sug.Kind + "/" + i.row.sug.DisplayName()
	if i.row.sug.Kind != project.KindNamespace {
		value += rowColumnSeparator + i.row.ns
	}
	return value
}
func (i suggestionItem) listColumns() (string, []rowColumn) {
	row := i.row
	identity := row.sug.Kind + "/" + row.sug.DisplayName()
	if row.sug.Kind == k8s.KindSecret || row.sug.Kind == k8s.KindConfigMap {
		identity = row.sug.DisplayName()
	}
	if row.sug.Kind != project.KindNamespace {
		identity += rowColumnSeparator + row.ns
	}
	columns := []rowColumn{{text: row.sug.Provenance()}, {text: row.sug.ModeLabel()}}
	if status := suggestionStatus(row, i.styles, i.checkCluster); status != "" {
		columns = append(columns, rowColumn{text: status, critical: true})
	}
	return identity, columns
}
func (i suggestionItem) rowAlignment() *rowAlignment { return i.align }
func (i suggestionItem) kindBadge() string {
	if i.row.sug.Kind != k8s.KindSecret && i.row.sug.Kind != k8s.KindConfigMap {
		return ""
	}
	return i.styles.resourceBadge(i.row.sug.Kind)
}
func (i suggestionItem) filterMatchOffset() int {
	if i.row.sug.Kind != k8s.KindSecret && i.row.sug.Kind != k8s.KindConfigMap {
		return 0
	}
	return utf8.RuneCountInString(i.row.sug.Kind + "/")
}

type suggestionScreen struct {
	loader
	linkLoader    loader
	ctx           context.Context
	client        *k8s.Client
	store         *store.Store
	project       store.Project
	cfg           scanConfig
	checkCluster  bool
	styles        *styles
	env           editEnv
	km            suggestionKeyMap
	rows          []suggestionRow
	list          list.Model
	notes         []string
	pendingChecks int
	queuedChecks  []tea.Cmd // cluster checks not yet dispatched; see takeChecks
	linkedCount   int
	scanErr       error
	scanned       bool
	cancelled     bool
	confirming    bool
	gate          confirmGate
	spinner       spinner.Model
	width         int
	bodyHeight    int
}

func newSuggestionScreen(ctx context.Context, client *k8s.Client, st *store.Store, proj store.Project, cfg scanConfig, checkCluster bool, env editEnv, styles *styles) *suggestionScreen {
	model := newListModel(styles, env.keymaps().list)
	model.Filter = groupedFilter
	return &suggestionScreen{
		ctx: ctx, client: client, store: st, project: proj, cfg: cfg,
		checkCluster: checkCluster, env: env, km: env.keymaps().suggestion, styles: styles, spinner: newSpinner(styles), list: model,
		gate: newConfirmGate(styles),
	}
}

func (s *suggestionScreen) Init() tea.Cmd { return s.scan() }

func (s *suggestionScreen) scan() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.rows = nil
	s.list.ResetFilter()
	setItems := scopeListFilterCmd(&s.list, s.list.SetItems(nil))
	s.notes = nil
	s.pendingChecks = 0
	s.queuedChecks = nil
	s.scanErr = nil
	s.scanned = false
	s.cancelled = false
	root := s.project.RootPath
	defaultNamespace := s.project.Namespace
	cfg := s.cfg
	separator := s.styles.glyphs.separator
	s.layout()
	return tea.Batch(setItems, func() tea.Msg {
		result, err := project.Scan(ctx, project.ScanOptions{Root: root, MaxDepth: cfg.depth, MaxFiles: cfg.maxFiles, DefaultNamespace: defaultNamespace, NoteSeparator: separator})
		return scanDoneMsg{reqID: reqID, result: result, err: err}
	}, s.spinner.Tick)
}

func (s *suggestionScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	defer s.layout()
	switch msg := msg.(type) {
	case scanDoneMsg:
		if !s.finish(msg.reqID) {
			return s, nil
		}
		s.scanned = true
		s.scanErr = msg.err
		if msg.err != nil {
			s.env.log.Err("scan", debuglog.ClassifyError(msg.err))
			return s, nil
		}
		s.notes = msg.result.Notes
		s.rows = make([]suggestionRow, len(msg.result.Suggestions))
		for i, suggestion := range msg.result.Suggestions {
			namespace := suggestion.Namespace
			if namespace == "" {
				namespace = s.project.Namespace
			}
			s.rows[i] = suggestionRow{sug: suggestion, ns: namespace}
		}
		setItems := s.setItems()
		s.env.log.Count("scan", s.project.RootPath, len(s.rows))
		if !s.checkCluster || len(s.rows) == 0 {
			return s, setItems
		}
		ctx, reqID := s.start(s.ctx)
		s.pendingChecks = len(s.rows)
		type prefixListKey struct{ kind, namespace string }
		prefixSets := make(map[prefixListKey]map[string]struct{})
		var prefixKeys []prefixListKey
		for _, row := range s.rows {
			if !isPrefixServed(row) {
				continue
			}
			key := prefixListKey{kind: row.sug.Kind, namespace: row.ns}
			if prefixSets[key] == nil {
				prefixSets[key] = make(map[string]struct{})
				prefixKeys = append(prefixKeys, key)
			}
			prefixSets[key][row.sug.Name] = struct{}{}
		}
		cmds := make([]tea.Cmd, 0, len(s.rows)+len(prefixKeys))
		for i := range s.rows {
			if !isPrefixServed(s.rows[i]) {
				cmds = append(cmds, s.checkSuggestion(ctx, reqID, i))
			}
		}
		for _, key := range prefixKeys {
			prefixes := slices.Sorted(maps.Keys(prefixSets[key]))
			cmds = append(cmds, s.matchPrefixNames(ctx, reqID, key.kind, key.namespace, prefixes))
		}
		s.queuedChecks = cmds
		initial := append([]tea.Cmd{setItems}, s.takeChecks(maxSuggestionChecks)...)
		initial = append(initial, s.spinner.Tick)
		return s, tea.Batch(initial...)
	case suggestionCheckedMsg:
		if msg.reqID != s.reqID || !s.pending || msg.index < 0 || msg.index >= len(s.rows) {
			return s, nil
		}
		row := &s.rows[msg.index]
		row.matched = msg.matched
		row.err = msg.err
		switch {
		case msg.err != nil:
			row.state = rowCheckFailed
		case msg.found:
			row.state = rowFound
		default:
			row.state = rowNotFound
		}
		s.pendingChecks--
		s.measureAlignment()
		if s.pendingChecks == 0 {
			s.finish(msg.reqID)
		}
		return s, tea.Batch(s.takeChecks(1)...)
	case prefixMatchMsg:
		if msg.reqID != s.reqID || !s.pending {
			return s, nil
		}
		for i := range s.rows {
			row := &s.rows[i]
			if !isPrefixServed(*row) || row.sug.Kind != msg.kind || row.ns != msg.namespace {
				continue
			}
			row.err = msg.err
			row.matched = ""
			switch {
			case msg.err != nil:
				row.state = rowCheckFailed
			default:
				row.state = rowNotFound
				if name, ok := msg.matches[row.sug.Name]; ok {
					row.state, row.matched = rowFound, name
				}
			}
			s.pendingChecks--
		}
		if s.pendingChecks == 0 {
			s.finish(msg.reqID)
		}
		s.measureAlignment()
		return s, tea.Batch(s.takeChecks(1)...)
	case suggestionLinkedMsg:
		if !s.linkLoader.finish(msg.reqID) || msg.index < 0 || msg.index >= len(s.rows) {
			return s, nil
		}
		row := &s.rows[msg.index]
		row.err = msg.err
		if msg.err != nil {
			row.state = rowLinkFailed
			s.measureAlignment()
			return s, nil
		}
		row.state = rowLinked
		s.linkedCount++
		s.measureAlignment()
		return s, nil
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

func isPrefixServed(row suggestionRow) bool {
	return row.sug.NamePrefix && (row.sug.Kind == k8s.KindSecret || row.sug.Kind == k8s.KindConfigMap)
}

// takeChecks pops up to n queued cluster checks. Checks run as ordinary
// commands and the queue lives on the model, so at most maxSuggestionChecks
// goroutines are alive at once without any of them parking on a semaphore. The
// three-index slice prevents callers appending to the result from overwriting
// commands that remain queued. Every dispatched command must produce exactly
// one message that reaches a takeChecks(1) call, or the remaining queue is
// stranded.
func (s *suggestionScreen) takeChecks(n int) []tea.Cmd {
	if n > len(s.queuedChecks) {
		n = len(s.queuedChecks)
	}
	taken := s.queuedChecks[:n:n]
	s.queuedChecks = s.queuedChecks[n:]
	return taken
}

func (s *suggestionScreen) checkSuggestion(ctx context.Context, reqID, index int) tea.Cmd {
	row := s.rows[index]
	client := s.client
	return func() tea.Msg {
		message := suggestionCheckedMsg{reqID: reqID, index: index}
		namespace := row.ns
		if row.sug.Kind == project.KindNamespace {
			namespace = ""
		}
		message.found, message.err = client.Exists(ctx, row.sug.Kind, namespace, row.sug.Name)
		return message
	}
}

func (s *suggestionScreen) matchPrefixNames(ctx context.Context, reqID int, kind, namespace string, prefixes []string) tea.Cmd {
	client := s.client
	return func() tea.Msg {
		message := prefixMatchMsg{reqID: reqID, kind: kind, namespace: namespace}
		message.matches, message.err = client.MatchGeneratedNames(ctx, kind, namespace, prefixes)
		return message
	}
}

func (s *suggestionScreen) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if s.linkLoader.pending {
		return nil
	}
	if s.confirming {
		if bubbleskey.Matches(msg, s.km.Back) {
			s.confirming = false
			return nil
		}
		confirmed, cmd := s.gate.handleKey(msg)
		if confirmed {
			s.confirming = false
			return s.linkSelected()
		}
		return cmd
	}
	if s.pending {
		if bubbleskey.Matches(msg, s.km.Back) {
			s.stop()
			s.cancelled = true
		}
		return nil
	}
	if s.list.SettingFilter() {
		return s.updateList(msg)
	}
	switch {
	case bubbleskey.Matches(msg, s.km.Apply):
		item, ok := s.list.SelectedItem().(suggestionItem)
		if !ok || item.row.state == rowLinked {
			return nil
		}
		s.confirming = true
		return s.gate.arm()
	case bubbleskey.Matches(msg, s.km.Refresh):
		return s.scan()
	case bubbleskey.Matches(msg, s.km.Back):
		if s.list.FilterState() != list.Unfiltered {
			return s.updateList(msg)
		}
		if s.linkedCount > 0 {
			projectID := s.project.ID
			return tea.Batch(popScreen(), func() tea.Msg { return scanLinksAppliedMsg{projectID: projectID} })
		}
		return popScreen()
	}
	return s.updateList(msg)
}

func (s *suggestionScreen) updateList(msg tea.Msg) tea.Cmd {
	return updateListModel(&s.list, msg)
}

func (s *suggestionScreen) linkSelected() tea.Cmd {
	item, ok := s.list.SelectedItem().(suggestionItem)
	if !ok || item.row.state == rowLinked {
		return nil
	}
	ctx, reqID := s.linkLoader.start(s.ctx)
	index := item.index
	row := s.rows[index]
	projectID := s.project.ID
	originContext := s.client.Context
	originServer := s.client.Server
	return func() tea.Msg {
		var err error
		switch row.sug.Kind {
		case project.KindNamespace:
			if row.sug.Name != s.project.Namespace {
				var namespaces []string
				namespaces, err = s.store.Namespaces(ctx, projectID)
				if err == nil && !slices.Contains(namespaces, row.sug.Name) {
					namespaces = append(namespaces, row.sug.Name)
					err = s.store.SetNamespaces(ctx, projectID, namespaces)
				}
			}
		case k8s.KindSecret, k8s.KindConfigMap:
			name := row.sug.Name
			if row.matched != "" {
				name = row.matched
			}
			err = s.store.LinkResource(ctx, projectID, store.ResourceLink{
				Kind: row.sug.Kind, Namespace: row.ns, Name: name, Source: store.SourceScan,
				OriginContext: originContext, OriginServer: originServer,
			})
		default:
			if slices.Contains(k8s.WorkloadKinds, row.sug.Kind) {
				err = s.store.LinkWorkload(ctx, projectID, store.WorkloadLink{
					Kind: row.sug.Kind, Namespace: row.ns, Name: row.sug.Name,
					OriginContext: originContext, OriginServer: originServer,
				})
			} else {
				err = fmt.Errorf("link suggestion: unsupported kind %q", row.sug.Kind)
			}
		}
		return suggestionLinkedMsg{reqID: reqID, index: index, err: err}
	}
}

func (s *suggestionScreen) setItems() tea.Cmd {
	align := &rowAlignment{}
	items := make([]list.Item, len(s.rows))
	for index := range s.rows {
		items[index] = suggestionItem{index: index, row: &s.rows[index], align: align, styles: s.styles, checkCluster: s.checkCluster}
	}
	*align = measureRowAlignment(items)
	cmd := scopeListFilterCmd(&s.list, s.list.SetItems(items))
	if len(items) > 0 {
		s.list.Select(0)
	}
	return cmd
}

func (s *suggestionScreen) measureAlignment() {
	items := s.list.Items()
	if len(items) == 0 {
		return
	}
	item, ok := items[0].(suggestionItem)
	if !ok {
		return
	}
	*item.align = measureRowAlignment(items)
}

func (s *suggestionScreen) View() string {
	parts, tail := s.viewParts()
	parts = append(parts, fitListHeight(renderListWithoutPrematureEmpty(s.list, len(tail) > 0 || s.scanErr != nil), s.list.Height()))
	parts = append(parts, tail...)
	return strings.Join(parts, "\n")
}

func (s *suggestionScreen) viewParts() (header, tail []string) {
	header = []string{"suggestions for " + s.project.Name}
	for _, note := range s.notes {
		header = append(header, s.styles.dim.Render(note))
	}
	if s.scanErr != nil {
		header = append(header, renderStateLine(s.styles, stateLineError, "scan failed: "+s.scanErr.Error(), bindingAction(s.km.Refresh, "to rescan"), s.width))
	}

	if s.anyPending() {
		s.list.Title = ""
	} else {
		s.list.Title = statusRowText(s.styles, s.width, listStatusSegments(&s.list, "suggestion")...)
	}
	filteredEmpty := s.list.FilterState() == list.FilterApplied && len(s.list.VisibleItems()) == 0
	if filteredEmpty {
		header = append(header, renderListTitle(s.list))
	}
	if s.scanned && s.scanErr == nil {
		switch {
		case filteredEmpty:
			tail = append(tail, s.styles.dim.Render("no matching suggestions"))
		case len(s.rows) == 0:
			tail = append(tail, s.styles.dim.Render("no suggestions found"))
		}
	}
	if s.pending {
		label := "scanning repo..."
		if s.pendingChecks > 0 {
			label = "checking cluster..."
		}
		tail = append(tail, s.spinner.View()+" "+label)
	} else if s.linkLoader.pending {
		tail = append(tail, s.spinner.View()+" linking suggestion...")
	} else if item, ok := s.list.SelectedItem().(suggestionItem); s.confirming && ok {
		row := *item.row
		tail = append(tail, truncateLine("link "+row.sug.Kind+"/"+row.sug.Name+" in "+row.ns+" to project "+s.project.Name, s.width, s.styles.glyphs.ellipsis))
		tail = append(tail, strings.Split(s.gate.promptLines(s.styles, false), "\n")...)
		if s.gate.message != "" {
			tail = append(tail, s.styles.warnText.Render(s.gate.message))
		}
	}
	if s.cancelled {
		tail = append(tail, s.styles.dim.Render("scan cancelled"+s.styles.glyphs.separator+bindingAction(s.km.Refresh, "to rescan")))
	}
	return header, tail
}

func suggestionStatus(row *suggestionRow, st *styles, checkCluster bool) string {
	var status string
	switch row.state {
	case rowFound:
		status = st.glyphs.foundTag
		if row.matched != "" {
			status += ": " + row.matched
		}
		status = st.diffAdd.Render(status)
	case rowNotFound:
		status = st.warnText.Render(st.glyphs.notFoundTag)
	case rowCheckFailed:
		status = st.dim.Render(st.glyphs.checkFailedTag)
	case rowLinked:
		status = st.tag.Render("[linked]")
	case rowLinkFailed:
		message := "unknown error"
		if row.err != nil {
			message = strings.ReplaceAll(row.err.Error(), "\n", " ")
		}
		status = st.errText.Render("link failed: " + message)
	default:
		if !checkCluster {
			return st.dim.Render(st.glyphs.inactiveTag + " unchecked")
		}
		return ""
	}
	return status
}

func (s *suggestionScreen) anyPending() bool { return s.pending || s.linkLoader.pending }

func (s *suggestionScreen) stop() bool {
	s.queuedChecks = nil
	return s.loader.stop()
}

func (s *suggestionScreen) SetSize(width, height int) {
	s.width, s.bodyHeight = width, height
	s.gate.setWidth(width)
	s.layout()
}
func (s *suggestionScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
	s.gate.setStyles(st)
	applySpinnerStyle(&s.spinner, st)
}

func (s *suggestionScreen) layout() {
	header, tail := s.viewParts()
	s.list.SetSize(s.width, max(0, s.bodyHeight-len(header)-len(tail)))
}
func (s *suggestionScreen) Title() string { return "scan" }
func (s *suggestionScreen) Hints() footerHints {
	if s.linkLoader.pending {
		return hintStatus("linking (cannot cancel)")
	}
	if s.pending {
		return hintBindings(hintDesc(s.km.Back, "cancel"))
	}
	if s.confirming {
		return hintBindings(displayHint("YES", "confirm"), s.km.Back)
	}
	if s.list.SettingFilter() {
		return hintBindings(displayHint("enter", "accept"), hintDesc(s.km.Back, "cancel"))
	}
	return browsingHints(s.km.Apply, s.env.keymaps().global.Filter)
}
func (s *suggestionScreen) Help() helpGroup {
	return helpGroup{title: "suggestions", entries: []helpGroupEntry{
		{binding: s.km.Apply, desc: "link the suggestion under the cursor"},
	}}
}
func (s *suggestionScreen) CapturesInput() bool { return s.confirming || s.list.SettingFilter() }
func (s *suggestionScreen) WantsEsc() bool      { return true }
