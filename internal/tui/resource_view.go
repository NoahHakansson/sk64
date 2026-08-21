package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
)

type resourceFilter int

const (
	filterAll resourceFilter = iota
	filterSecrets
	filterConfigMaps
	filterCount
)

type resourceItem struct {
	resource      k8s.Resource
	styles        *styles
	showNamespace bool
	align         *rowAlignment
}

func (i resourceItem) Title() string {
	identity, columns := i.listColumns()
	return renderRowColumns(0, i.kindBadge()+" "+identity, "", columns...)
}

func (i resourceItem) listColumns() (string, []rowColumn) {
	identity := i.resource.Name()
	if i.showNamespace {
		identity = i.resource.Namespace() + "/" + identity
	}
	typeColumn := ""
	if i.resource.Kind() == k8s.KindSecret {
		typeColumn = i.styles.dim.Render(i.resource.Type())
	}
	immutableColumn := ""
	if i.resource.Immutable() {
		immutableColumn = i.styles.tag.Render(i.styles.glyphs.immutableTag)
	}
	return identity, []rowColumn{{text: typeColumn}, {text: immutableColumn, critical: true}}
}

func (i resourceItem) Description() string         { return "" }
func (i resourceItem) kindBadge() string           { return i.styles.resourceBadge(i.resource.Kind()) }
func (i resourceItem) rowAlignment() *rowAlignment { return i.align }
func (i resourceItem) FilterValue() string {
	if i.showNamespace {
		return i.resource.Namespace() + "/" + i.resource.Name()
	}
	return i.resource.Name()
}

type resourceScreen struct {
	loader
	ctx               context.Context
	client            *k8s.Client
	env               editEnv
	km                resourceKeyMap
	namespace         string
	styles            *styles
	list              list.Model
	spinner           spinner.Model
	err               error
	notice            string
	outcome           resourceOutcome
	all               []k8s.Resource
	filter            resourceFilter
	loadComplete      bool
	cancelled         bool
	pendingKinds      int
	loadContext       context.Context
	width, bodyHeight int
}

func newResourceScreen(ctx context.Context, client *k8s.Client, namespace string, env editEnv, st *styles) *resourceScreen {
	km := env.keymaps().resource
	if env.readOnly {
		km.New.SetEnabled(false)
		km.Delete.SetEnabled(false)
	}
	if env.noConfigMaps {
		km.TypeCycle.SetEnabled(false)
	}
	return &resourceScreen{
		ctx:       ctx,
		client:    client,
		env:       env,
		km:        km,
		namespace: namespace,
		styles:    st,
		list:      newListModel(st, env.keymaps().list),
		spinner:   newSpinner(st),
	}
}

func (s *resourceScreen) Init() tea.Cmd {
	return s.startLoading()
}

func (s *resourceScreen) startLoading() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.loadContext = ctx
	s.err = nil
	s.all = nil
	s.loadComplete = false
	s.cancelled = false
	s.pendingKinds = 2
	if s.env.noConfigMaps {
		s.pendingKinds = 1
	}
	s.list.ResetFilter()
	setItemsCmd := scopeListFilterCmd(&s.list, s.list.SetItems(nil))
	s.layout()
	commands := []tea.Cmd{setItemsCmd, s.fetchSecrets(ctx, reqID, ""), s.spinner.Tick}
	if !s.env.noConfigMaps {
		commands = append(commands, s.fetchConfigMaps(ctx, reqID, ""))
	}
	return tea.Batch(commands...)
}

func (s *resourceScreen) fetchSecrets(ctx context.Context, reqID int, continueToken string) tea.Cmd {
	return func() tea.Msg {
		page, err := s.client.ListSecrets(ctx, s.namespace, k8s.DefaultPageSize, continueToken)
		return resourcesPageMsg{reqID: reqID, kind: k8s.KindSecret, page: page, err: err}
	}
}

func (s *resourceScreen) fetchConfigMaps(ctx context.Context, reqID int, continueToken string) tea.Cmd {
	return func() tea.Msg {
		page, err := s.client.ListConfigMaps(ctx, s.namespace, k8s.DefaultPageSize, continueToken)
		return resourcesPageMsg{reqID: reqID, kind: k8s.KindConfigMap, page: page, err: err}
	}
}

func (s *resourceScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.pending {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case resourceListChangedMsg:
		if s.allNamespaces() || msg.namespace == s.namespace {
			if msg.outcome.verb != outcomeNone {
				s.outcome = msg.outcome
			}
			return s, s.startLoading()
		}
		return s, nil
	case resourcesPageMsg:
		if msg.reqID != s.reqID || !s.pending {
			return s, nil
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				s.finish(msg.reqID)
				s.cancelled = true
				s.layout()
				return s, nil
			}
			if s.err == nil {
				s.err = msg.err
			}
			s.pendingKinds--
			if s.pendingKinds == 0 {
				s.finish(msg.reqID)
				s.loadComplete = s.err == nil
				s.env.log.Count("open-resources", s.namespace, len(s.all))
			}
			s.layout()
			return s, nil
		}

		s.all = append(s.all, msg.page.Items...)
		slices.SortFunc(s.all, func(a, b k8s.Resource) int {
			if s.allNamespaces() {
				return cmp.Or(cmp.Compare(a.Namespace(), b.Namespace()), cmp.Compare(a.Name(), b.Name()), cmp.Compare(a.Kind(), b.Kind()))
			}
			return cmp.Or(cmp.Compare(a.Name(), b.Name()), cmp.Compare(a.Kind(), b.Kind()))
		})
		setItemsCmd := s.setVisibleItems()
		if msg.page.Continue != "" {
			if msg.kind == k8s.KindSecret {
				return s, tea.Batch(setItemsCmd, s.fetchSecrets(s.loadContext, msg.reqID, msg.page.Continue))
			}
			return s, tea.Batch(setItemsCmd, s.fetchConfigMaps(s.loadContext, msg.reqID, msg.page.Continue))
		}

		s.pendingKinds--
		if s.pendingKinds == 0 {
			s.finish(msg.reqID)
			s.loadComplete = s.err == nil
			s.env.log.Count("open-resources", s.namespace, len(s.all))
			s.layout()
		}
		return s, setItemsCmd

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
		case bubbleskey.Matches(msg, s.km.TypeCycle):
			s.filter = (s.filter + 1) % filterCount
			s.list.ResetFilter()
			return s, s.setVisibleItems()
		case bubbleskey.Matches(msg, s.km.Open):
			if selected, ok := s.list.SelectedItem().(resourceItem); ok {
				resource := selected.resource
				return s, pushScreen(newKeyScreen(s.ctx, s.client, resource.Kind(), resource.Namespace(), resource.Name(), s.env, s.styles))
			}
		case bubbleskey.Matches(msg, s.km.Consumers):
			if selected, ok := s.list.SelectedItem().(resourceItem); ok {
				resource := selected.resource
				return s, pushScreen(newConsumersScreen(s.ctx, s.client, resource.Kind(), resource.Namespace(), resource.Name(), s.env, s.styles))
			}
		case bubbleskey.Matches(msg, s.km.Link):
			if selected, ok := s.list.SelectedItem().(resourceItem); ok {
				resource := selected.resource
				link := store.ResourceLink{Kind: resource.Kind(), Namespace: resource.Namespace(), Name: resource.Name(), Source: store.SourceManual}
				return s, func() tea.Msg { return openProjectPickerMsg{link: pendingLink{resource: &link}} }
			}
		case bubbleskey.Matches(msg, s.km.New):
			if s.pending {
				return s, nil
			}
			if s.allNamespaces() {
				s.notice = "create needs one namespace" + s.styles.glyphs.separator + "esc back to the namespace list"
				s.layout()
				return s, nil
			}
			return s, pushScreen(newCreatePrompt(s.ctx, s.client, s.env, s.namespace, s.all, s.styles))
		case bubbleskey.Matches(msg, s.km.Delete):
			if selected, ok := s.list.SelectedItem().(resourceItem); ok {
				resource := selected.resource
				return s, pushScreen(newDeleteConfirm(s.ctx, s.client, resource.Kind(), resource.Namespace(), resource.Name(), s.styles))
			}
		case bubbleskey.Matches(msg, s.km.AllNamespaces):
			if s.allNamespaces() {
				s.stop()
				return s, popScreen()
			}
		}
	}

	return s, updateListModel(&s.list, msg)
}

func (s *resourceScreen) clearNotice() {
	if s.notice == "" && s.outcome.verb == outcomeNone {
		return
	}
	s.notice = ""
	s.outcome = resourceOutcome{}
	s.layout()
}

func (s *resourceScreen) setVisibleItems() tea.Cmd {
	items := make([]list.Item, 0, len(s.all))
	align := &rowAlignment{}
	for _, resource := range s.all {
		if s.filter == filterSecrets && resource.Kind() != k8s.KindSecret {
			continue
		}
		if s.filter == filterConfigMaps && resource.Kind() != k8s.KindConfigMap {
			continue
		}
		items = append(items, resourceItem{resource: resource, styles: s.styles, showNamespace: s.allNamespaces(), align: align})
	}
	*align = measureRowAlignment(items)
	return scopeListFilterCmd(&s.list, s.list.SetItems(items))
}

func (s *resourceScreen) View() string {
	s.list.Title = s.statusRow()
	stateLine := s.stateLine()
	parts := []string{renderListWithoutPrematureEmpty(s.list, stateLine != "")}
	if s.notice != "" {
		parts = append(parts, s.styles.dim.Render(truncateLine(s.notice, s.width, s.styles.glyphs.ellipsis)))
	}
	if notice := s.outcome.render(s.styles, s.width); notice != "" {
		parts = append(parts, notice)
	}
	if stateLine != "" {
		parts = append(parts, stateLine)
	}
	return strings.Join(parts, "\n")
}

func (s *resourceScreen) statusRow() string {
	if s.pending && len(s.all) == 0 {
		return ""
	}
	segments := listStatusSegments(&s.list, "resource")
	if len(segments) == 0 {
		return ""
	}
	if !s.env.noConfigMaps {
		mode := "all kinds"
		switch s.filter {
		case filterSecrets:
			mode = "secrets"
		case filterConfigMaps:
			mode = "configmaps"
		}
		segments = append(segments, mode)
	}
	if s.allNamespaces() {
		segments = append(segments, "all namespaces")
	}
	return statusRowText(s.styles, s.width, segments...)
}

func (s *resourceScreen) stateLine() string {
	if s.pending {
		return renderLoadingLine(s.styles, s.spinner.View(), "loading resources", "esc to cancel", s.width)
	}
	if s.err != nil {
		return renderStateLine(s.styles, stateLineError, fmt.Sprintf("resources unavailable: %v", s.err), bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.cancelled {
		kind := stateLineUnknown
		message := "load cancelled; results unknown"
		if len(s.all) > 0 {
			kind = stateLineIncomplete
			message = "load cancelled; retained rows incomplete"
		}
		return renderStateLine(s.styles, kind, message, bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if filtered := filteredListState(s.list, s.styles, s.width); filtered != "" {
		return filtered
	}
	if !s.loadComplete || len(s.list.Items()) > 0 {
		return ""
	}
	if len(s.all) > 0 {
		message := "no secrets found"
		if s.filter == filterConfigMaps {
			message = "no configmaps found"
		}
		return renderStateLine(s.styles, stateLineEmpty, message, bindingAction(s.km.TypeCycle, "to change type"), s.width)
	}
	action := bindingAction(s.km.Refresh, "to refresh")
	if !s.env.readOnly && !s.allNamespaces() {
		action = "N to create"
	}
	return renderStateLine(s.styles, stateLineEmpty, "no resources found", action, s.width)
}

func (s *resourceScreen) SetSize(width, height int) {
	s.width, s.bodyHeight = width, height
	s.layout()
}

func (s *resourceScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
	applySpinnerStyle(&s.spinner, st)
}

func (s *resourceScreen) layout() {
	extra := 0
	if s.notice != "" {
		extra++
	}
	if s.outcome.verb != outcomeNone {
		extra++
	}
	if s.stateLine() != "" {
		extra++
	}
	s.list.SetSize(s.width, max(0, s.bodyHeight-extra))
}

func (s *resourceScreen) allNamespaces() bool { return s.namespace == k8s.AllNamespaces }
func (s *resourceScreen) Title() string {
	if s.allNamespaces() {
		return "all namespaces"
	}
	return s.namespace
}
func (s *resourceScreen) Hints() footerHints {
	bindings := []bubbleskey.Binding{s.km.Open}
	if !s.allNamespaces() {
		bindings = append(bindings, s.km.New)
	}
	bindings = append(bindings, s.km.Delete, s.km.Consumers, s.km.Link, s.km.TypeCycle)
	if s.allNamespaces() {
		bindings = append(bindings, hintDesc(s.km.AllNamespaces, "one ns"))
	}
	bindings = append(bindings, s.env.keymaps().global.Filter)
	return browsingHints(bindings...)
}
func (s *resourceScreen) Help() helpGroup {
	entries := []helpGroupEntry{{binding: s.km.Open, desc: "list the resource's keys"}}
	if !s.allNamespaces() {
		entries = append(entries, helpGroupEntry{binding: s.km.New, desc: "create a Secret or ConfigMap"})
	}
	entries = append(entries,
		helpGroupEntry{binding: s.km.Delete, desc: "delete (type the name to confirm)"},
		helpGroupEntry{binding: s.km.Consumers, desc: "consumers of this resource (blast radius)"},
		helpGroupEntry{binding: s.km.Link, desc: "link this resource to a project"},
		helpGroupEntry{binding: s.km.TypeCycle, desc: "cycle all / secrets / configmaps"},
	)
	if s.allNamespaces() {
		entries = append(entries, helpGroupEntry{binding: s.km.AllNamespaces, desc: "back to a single namespace"})
	}
	return helpGroup{title: "resources", entries: entries}
}
func (s *resourceScreen) CapturesInput() bool { return s.list.SettingFilter() }
func (s *resourceScreen) WantsEsc() bool {
	return s.pending || s.list.FilterState() != list.Unfiltered
}
