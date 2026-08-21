package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	diffpkg "github.com/NoahHakansson/sk64/internal/diff"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/undo"
)

type keyItem struct {
	key    string
	filter string
	size   int
	binary bool
	styles *styles
	align  *rowAlignment
}

func (i keyItem) Title() string {
	identity, columns := i.listColumns()
	return renderRowColumns(0, identity, "", columns...)
}

func (i keyItem) listColumns() (string, []rowColumn) {
	binaryColumn := ""
	if i.binary {
		binaryColumn = i.styles.tag.Render(i.styles.glyphs.binaryTag)
	}
	return i.key, []rowColumn{{text: i.styles.dim.Render(diffpkg.HumanSize(i.size))}, {text: binaryColumn, critical: true}}
}

func (i keyItem) Description() string         { return "" }
func (i keyItem) FilterValue() string         { return i.filter }
func (i keyItem) rowAlignment() *rowAlignment { return i.align }

type keyScreen struct {
	loader
	ctx                context.Context
	client             *k8s.Client
	env                editEnv
	km                 keyKeyMap
	kind               string
	namespace          string
	name               string
	styles             *styles
	list               list.Model
	spinner            spinner.Model
	err                error
	outcome            resourceOutcome
	outcomeOperationID uint64
	outcomeFinal       bool
	outcomeCleared     bool
	resource           k8s.Resource
	loadComplete       bool
	cancelled          bool
	valueSearch        bool
	width, bodyHeight  int
}

func newKeyScreen(ctx context.Context, client *k8s.Client, kind, namespace, name string, env editEnv, st *styles) *keyScreen {
	km := env.keymaps().keyScreen
	if env.readOnly {
		setKeyEditingBindingsEnabled(&km, false)
	}
	return &keyScreen{
		ctx:       ctx,
		client:    client,
		env:       env,
		km:        km,
		kind:      kind,
		namespace: namespace,
		name:      name,
		styles:    st,
		list:      newListModel(st, env.keymaps().list),
		spinner:   newSpinner(st),
	}
}

func (s *keyScreen) Init() tea.Cmd {
	return s.startLoading()
}

func (s *keyScreen) startLoading() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.err = nil
	s.resource = nil
	s.loadComplete = false
	s.cancelled = false
	s.list.ResetFilter()
	setItemsCmd := scopeListFilterCmd(&s.list, s.list.SetItems(nil))
	s.layout()
	return tea.Batch(setItemsCmd, s.fetchResource(ctx, reqID), s.spinner.Tick)
}

func (s *keyScreen) fetchResource(ctx context.Context, reqID int) tea.Cmd {
	return func() tea.Msg {
		resource, err := s.client.GetResource(ctx, s.kind, s.namespace, s.name)
		return resourceLoadedMsg{reqID: reqID, res: resource, err: err}
	}
}

func (s *keyScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.pending {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case editSavedMsg:
		if msg.outcome.kind != s.kind || msg.outcome.namespace != s.namespace || msg.outcome.name != s.name {
			return s, nil
		}
		if msg.operationID < s.outcomeOperationID {
			return s, nil
		}
		newOperation := msg.operationID > s.outcomeOperationID
		if !newOperation && s.outcomeCleared {
			return s, nil
		}
		if newOperation {
			s.outcomeOperationID = msg.operationID
			s.outcomeCleared = false
		}
		if !newOperation && s.outcomeFinal && !msg.final {
			return s, nil
		}
		s.outcome = msg.outcome
		s.outcomeFinal = msg.final
		s.layout()
		if newOperation {
			return s, s.startLoading()
		}
		if msg.skipRefresh || s.pending {
			return s, nil
		}
		return s, s.startLoading()
	case resourceLoadedMsg:
		if !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				s.cancelled = true
			} else {
				s.err = msg.err
			}
			s.layout()
			return s, nil
		}
		s.resource = msg.res
		setKeyEditingBindingsEnabled(&s.km, !s.env.readOnly && !msg.res.Immutable())
		s.loadComplete = true
		s.env.log.Resource("open-keys", s.kind, s.namespace, s.name)
		cmd := s.setItems()
		s.layout()
		return s, cmd

	case tea.KeyPressMsg:
		s.clearNotice()
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
			if s.pending {
				return s, nil
			}
			return s, s.startLoading()
		case bubbleskey.Matches(msg, s.km.Open):
			selected, ok := s.list.SelectedItem().(keyItem)
			if !ok || s.resource == nil {
				return s, nil
			}
			if selected.binary {
				return s, pushScreen(newHexScreen(s.resource, selected.key, s.env, s.styles))
			}
			if s.env.readOnly || s.resource.Immutable() {
				return s, pushScreen(newValueScreen(s.resource, selected.key, s.env, s.styles))
			}
			return s, pushScreen(newEditFlow(s.ctx, s.client, s.env, s.resource, selected.key, nil, s.styles))
		case bubbleskey.Matches(msg, s.km.Export):
			selected, ok := s.list.SelectedItem().(keyItem)
			if !ok || s.resource == nil {
				return s, nil
			}
			return s, pushScreen(newFilePrompt(s.ctx, s.client, s.env, s.resource, selected.key, fileExport, s.styles))
		case bubbleskey.Matches(msg, s.km.Consumers):
			if s.resource == nil {
				return s, nil
			}
			return s, pushScreen(newConsumersScreen(s.ctx, s.client, s.kind, s.namespace, s.name, s.env, s.styles))
		case bubbleskey.Matches(msg, s.km.Import):
			selected, ok := s.list.SelectedItem().(keyItem)
			if !ok || s.resource == nil {
				return s, nil
			}
			return s, pushScreen(newFilePrompt(s.ctx, s.client, s.env, s.resource, selected.key, fileImport, s.styles))
		case bubbleskey.Matches(msg, s.km.EditAll):
			if s.resource == nil {
				return s, nil
			}
			return s, pushScreen(newResourceEditFlow(s.ctx, s.client, s.env, s.resource, s.styles))
		case bubbleskey.Matches(msg, s.km.NewKey):
			if s.resource == nil {
				return s, nil
			}
			return s, pushScreen(newKeyNamePrompt(s.ctx, s.client, s.env, s.resource, s.styles))
		case bubbleskey.Matches(msg, s.km.DeleteKey):
			selected, ok := s.list.SelectedItem().(keyItem)
			if !ok || s.resource == nil {
				return s, nil
			}
			return s, pushScreen(newKeyDeleteFlow(s.ctx, s.client, s.env, s.resource, selected.key, s.styles))
		case bubbleskey.Matches(msg, s.km.ValueSearch):
			if s.resource == nil {
				return s, nil
			}
			s.valueSearch = !s.valueSearch
			s.list.ResetFilter()
			s.layout()
			return s, s.setItems()
		case bubbleskey.Matches(msg, s.km.Undo):
			if s.resource == nil {
				return s, nil
			}
			entry, ok := s.env.ring.LatestFor(s.client.Context, s.kind, s.namespace, s.name)
			if !ok {
				return s, nil
			}
			return s, pushScreen(s.undoFlow(entry))
		}
	}

	return s, updateListModel(&s.list, msg)
}

func setKeyEditingBindingsEnabled(km *keyKeyMap, enabled bool) {
	km.Import.SetEnabled(enabled)
	km.EditAll.SetEnabled(enabled)
	km.NewKey.SetEnabled(enabled)
	km.DeleteKey.SetEnabled(enabled)
	km.Undo.SetEnabled(enabled)
}

func (s *keyScreen) editable() bool {
	return s.resource != nil && !s.env.readOnly && !s.resource.Immutable()
}

func (s *keyScreen) clearNotice() {
	if s.outcome.verb == outcomeNone {
		return
	}
	s.outcome = resourceOutcome{}
	s.outcomeFinal = false
	s.outcomeCleared = true
	s.layout()
}

func (s *keyScreen) undoFlow(entry undo.Entry) *editFlow {
	if len(entry.Added) == 0 && len(entry.Previous) == 1 {
		for key, value := range entry.Previous {
			if slices.Contains(s.resource.Keys(), key) {
				return newEditFlow(s.ctx, s.client, s.env, s.resource, key, value, s.styles)
			}
			return newKeyRestoreFlow(s.ctx, s.client, s.env, s.resource, key, value, s.styles)
		}
	}
	if len(entry.Previous) == 0 && len(entry.Added) == 1 {
		return newKeyDeleteFlow(s.ctx, s.client, s.env, s.resource, entry.Added[0], s.styles)
	}
	set := make(map[string]string, len(entry.Previous))
	for key, value := range entry.Previous {
		set[key] = string(value)
	}
	remove := make([]string, 0, len(entry.Added))
	keys := s.resource.Keys()
	for _, key := range entry.Added {
		if slices.Contains(keys, key) {
			remove = append(remove, key)
		}
	}
	return newResourceRevertFlow(s.ctx, s.client, s.env, s.resource, set, remove, s.styles)
}

func (s *keyScreen) setItems() tea.Cmd {
	keys := s.resource.Keys()
	items := make([]list.Item, 0, len(keys))
	align := &rowAlignment{}
	for _, key := range keys {
		value, err := s.resource.Get(key)
		if err != nil {
			continue
		}
		binary := s.resource.IsBinary(key)
		filter := key
		if s.valueSearch && !binary {
			filter += "\n" + string(value)
		}
		items = append(items, keyItem{key: key, filter: filter, size: len(value), binary: binary, styles: s.styles, align: align})
	}
	*align = measureRowAlignment(items)
	return scopeListFilterCmd(&s.list, s.list.SetItems(items))
}

func (s *keyScreen) View() string {
	s.list.Title = s.statusRow()
	stateLine := s.stateLine()
	parts := make([]string, 0, 5)
	parts = append(parts, s.subjectLine())
	if s.resource != nil {
		if s.resource.Immutable() {
			line := truncateLine("immutable resource"+s.styles.glyphs.separator+"Kubernetes forbids editing its data; delete and recreate", s.width, s.styles.glyphs.ellipsis)
			parts = append(parts, s.styles.dim.Render(line))
		}
		if s.valueSearch {
			parts = append(parts, s.styles.dim.Render("value search: on"+s.styles.glyphs.separator+"/ also matches decoded values (binary keys excluded)"))
		}
	}
	parts = append(parts, renderListWithoutPrematureEmpty(s.list, stateLine != ""))
	if notice := s.outcome.render(s.styles, s.width); notice != "" {
		parts = append(parts, notice)
	}
	if stateLine != "" {
		parts = append(parts, stateLine)
	}
	return strings.Join(parts, "\n")
}

func (s *keyScreen) statusRow() string {
	if s.pending {
		return ""
	}
	return statusRowText(s.styles, s.width, listStatusSegments(&s.list, "key")...)
}

func (s *keyScreen) stateLine() string {
	if s.pending {
		return renderLoadingLine(s.styles, s.spinner.View(), "loading keys", "esc to cancel", s.width)
	}
	if s.err != nil {
		return renderStateLine(s.styles, stateLineError, fmt.Sprintf("keys unavailable: %v", s.err), bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.cancelled {
		return renderStateLine(s.styles, stateLineUnknown, "load cancelled; results unknown", bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if filtered := filteredListState(s.list, s.styles, s.width); filtered != "" {
		return filtered
	}
	if !s.loadComplete || len(s.list.Items()) > 0 {
		return ""
	}
	action := bindingAction(s.km.Refresh, "to refresh")
	if s.editable() {
		action = "N to create"
	}
	return renderStateLine(s.styles, stateLineEmpty, "no keys found", action, s.width)
}

func (s *keyScreen) SetSize(width, height int) {
	s.width, s.bodyHeight = width, height
	s.layout()
}

func (s *keyScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
	applySpinnerStyle(&s.spinner, st)
}

func (s *keyScreen) layout() {
	extra := 1
	if s.resource != nil {
		if s.resource.Immutable() {
			extra++
		}
		if s.valueSearch {
			extra++
		}
	}
	if s.outcome.verb != outcomeNone {
		extra++
	}
	if s.stateLine() != "" {
		extra++
	}
	s.list.SetSize(s.width, max(0, s.bodyHeight-extra))
}

func (s *keyScreen) Title() string { return s.name }
func (s *keyScreen) Hints() footerHints {
	var bindings []bubbleskey.Binding
	if s.env.readOnly || s.resource != nil && s.resource.Immutable() {
		bindings = []bubbleskey.Binding{hintDesc(s.km.Open, "view"), s.km.Export, s.km.Consumers, s.km.ValueSearch, s.env.keymaps().global.Filter}
	} else {
		bindings = []bubbleskey.Binding{hintDesc(s.km.Open, "edit"), s.km.EditAll, s.km.NewKey, s.km.DeleteKey, s.km.Import, s.km.Export, s.env.keymaps().global.Filter}
	}
	return browsingHints(bindings...)
}
func (s *keyScreen) Help() helpGroup {
	openDesc := "edit the value in $EDITOR"
	if s.env.readOnly || s.resource != nil && s.resource.Immutable() {
		openDesc = "view the decoded value (hex for binary keys)"
	}
	return helpGroup{title: "keys", entries: []helpGroupEntry{
		{binding: s.km.Open, desc: openDesc},
		{binding: s.km.EditAll, desc: "edit every key as one YAML document"},
		{binding: s.km.NewKey, desc: "add a key"},
		{binding: s.km.DeleteKey, desc: "delete the key"},
		{binding: s.km.Import, desc: "import a file as the key's value"},
		{binding: s.km.Export, desc: "export the raw bytes to a file"},
		{binding: s.km.Consumers, desc: "consumers of this resource"},
		{binding: s.km.ValueSearch, desc: "toggle value search for this resource"},
		{binding: s.km.Undo, desc: "revert the last save to this resource"},
	}}
}
func (s *keyScreen) CapturesInput() bool { return s.list.SettingFilter() }
func (s *keyScreen) WantsEsc() bool {
	return s.pending || s.list.FilterState() != list.Unfiltered
}

func (s *keyScreen) subjectLine() string {
	badge := s.styles.kindBadge.Render(s.styles.resourceBadge(s.kind))
	identity := badge + " " + s.styles.dim.Render(s.kind+" "+s.namespace+"/") + s.name
	columns := make([]rowColumn, 0, 2)
	if s.resource != nil && s.resource.Kind() == k8s.KindSecret {
		columns = append(columns, rowColumn{text: s.styles.dim.Render(s.resource.Type())})
	}
	if s.resource != nil && s.resource.Immutable() {
		columns = append(columns, rowColumn{text: s.styles.tag.Render(s.styles.glyphs.immutableTag)})
	}
	return renderRowColumns(s.width, identity, s.styles.glyphs.ellipsis, columns...)
}
