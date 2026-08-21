package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type namespaceItem string

func (i namespaceItem) Title() string       { return string(i) }
func (i namespaceItem) Description() string { return "" }
func (i namespaceItem) FilterValue() string { return string(i) }

type namespaceScreen struct {
	loader
	ctx               context.Context
	client            *k8s.Client
	env               editEnv
	km                namespaceKeyMap
	styles            *styles
	list              list.Model
	spinner           spinner.Model
	err               error
	names             []string
	loadComplete      bool
	cancelled         bool
	forbiddenFallback bool
	notes             []string
	projectName       string
	loadContext       context.Context
	width, bodyHeight int
}

func newNamespaceScreen(ctx context.Context, client *k8s.Client, projectName string, env editEnv, st *styles) *namespaceScreen {
	return &namespaceScreen{
		ctx:         ctx,
		client:      client,
		env:         env,
		km:          env.keymaps().namespace,
		styles:      st,
		list:        newListModel(st, env.keymaps().list),
		spinner:     newSpinner(st),
		projectName: projectName,
	}
}

func (s *namespaceScreen) Init() tea.Cmd {
	return s.startLoading()
}

func (s *namespaceScreen) startLoading() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.loadContext = ctx
	s.err = nil
	s.names = nil
	s.loadComplete = false
	s.cancelled = false
	s.forbiddenFallback = false
	s.list.ResetFilter()
	setItemsCmd := scopeListFilterCmd(&s.list, s.list.SetItems(nil))
	s.layout()
	return tea.Batch(setItemsCmd, s.fetchPage(ctx, reqID, ""), s.spinner.Tick)
}

func (s *namespaceScreen) fetchPage(ctx context.Context, reqID int, continueToken string) tea.Cmd {
	return func() tea.Msg {
		page, err := s.client.ListNamespaces(ctx, k8s.DefaultPageSize, continueToken)
		return namespacesPageMsg{reqID: reqID, page: page, err: err}
	}
}

func (s *namespaceScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.pending {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case namespacesPageMsg:
		if msg.reqID != s.reqID || !s.pending {
			return s, nil
		}
		if msg.err != nil {
			s.finish(msg.reqID)
			if errors.Is(msg.err, context.Canceled) {
				s.cancelled = true
				s.layout()
				return s, nil
			}
			if apierrors.IsForbidden(msg.err) {
				s.forbiddenFallback = true
				if s.client.Namespace == "" {
					return s, fatalCmd(errors.New("cannot list namespaces and no context namespace to fall back to"))
				}
				ctx, reqID := s.start(s.ctx)
				return s, func() tea.Msg {
					_, err := s.client.ListSecrets(ctx, s.client.Namespace, 1, "")
					return namespaceFallbackMsg{reqID: reqID, err: err}
				}
			}
			s.err = msg.err
			s.layout()
			return s, nil
		}

		s.names = append(s.names, msg.page.Names...)
		setItemsCmd := s.setItems()
		if msg.page.Continue != "" {
			return s, tea.Batch(setItemsCmd, s.fetchPage(s.loadContext, msg.reqID, msg.page.Continue))
		}
		s.finish(msg.reqID)
		s.loadComplete = true
		s.layout()
		return s, setItemsCmd

	case namespaceFallbackMsg:
		if !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err == nil {
			s.names = []string{s.client.Namespace}
			s.loadComplete = true
			cmd := s.setItems()
			s.layout()
			return s, cmd
		}
		if !errors.Is(msg.err, context.Canceled) {
			return s, fatalCmd(fmt.Errorf("cannot list namespaces and cannot access namespace %q: %w", s.client.Namespace, msg.err))
		}
		s.cancelled = true
		s.layout()
		return s, nil

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
			if s.pending {
				return s, nil
			}
			return s, s.startLoading()
		case bubbleskey.Matches(msg, s.km.Open):
			if selected, ok := s.list.SelectedItem().(namespaceItem); ok {
				return s, pushScreen(newResourceScreen(s.ctx, s.client, string(selected), s.env, s.styles))
			}
		case bubbleskey.Matches(msg, s.km.Workloads):
			if selected, ok := s.list.SelectedItem().(namespaceItem); ok {
				return s, pushScreen(newWorkloadScreen(s.ctx, s.client, string(selected), s.env, s.styles))
			}
		case bubbleskey.Matches(msg, s.km.AllNamespaces):
			return s, pushScreen(newResourceScreen(s.ctx, s.client, k8s.AllNamespaces, s.env, s.styles))
		}
	}

	return s, updateListModel(&s.list, msg)
}

func (s *namespaceScreen) setItems() tea.Cmd {
	items := make([]list.Item, len(s.names))
	for i, name := range s.names {
		items[i] = namespaceItem(name)
	}
	return scopeListFilterCmd(&s.list, s.list.SetItems(items))
}

func (s *namespaceScreen) View() string {
	s.list.Title = s.statusRow()
	stateLine := s.stateLine()
	parts := make([]string, 0, 3+len(s.notes))
	if identity := s.identityLine(); identity != "" {
		parts = append(parts, s.styles.dim.Render(identity))
	}
	if s.forbiddenFallback {
		parts = append(parts, renderStateLine(s.styles, stateLineIncomplete, "namespace list forbidden; showing kubeconfig namespace", "", s.width))
	}
	parts = append(parts, renderListWithoutPrematureEmpty(s.list, stateLine != ""))
	for _, note := range s.notes {
		parts = append(parts, s.styles.dim.Render(note))
	}
	if stateLine != "" {
		parts = append(parts, stateLine)
	}
	return strings.Join(parts, "\n")
}

func (s *namespaceScreen) statusRow() string {
	if s.pending {
		return ""
	}
	return statusRowText(s.styles, s.width, listStatusSegments(&s.list, "namespace")...)
}

func (s *namespaceScreen) stateLine() string {
	if s.pending {
		return renderLoadingLine(s.styles, s.spinner.View(), "loading namespaces", "esc to cancel", s.width)
	}
	if s.err != nil {
		return renderStateLine(s.styles, stateLineError, fmt.Sprintf("namespaces unavailable: %v", s.err), bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.cancelled {
		kind := stateLineUnknown
		message := "load cancelled; results unknown"
		if len(s.names) > 0 {
			kind = stateLineIncomplete
			message = "load cancelled; retained rows incomplete"
		}
		return renderStateLine(s.styles, kind, message, bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if filtered := filteredListState(s.list, s.styles, s.width); filtered != "" {
		return filtered
	}
	if s.loadComplete && len(s.names) == 0 {
		return renderStateLine(s.styles, stateLineEmpty, "no namespaces found", bindingAction(s.km.Refresh, "to refresh"), s.width)
	}
	return ""
}

func (s *namespaceScreen) identityLine() string {
	namespace := ""
	if s.client.Namespace != "" {
		namespace = "ns: " + s.client.Namespace
	}
	project := ""
	if s.projectName != "" {
		project = "project: " + s.projectName
	}
	cluster := ""
	if s.client.Cluster != "" {
		cluster = "cluster: " + s.client.Cluster
	}

	switch {
	case namespace != "":
		return renderRowColumns(s.width, namespace, s.styles.glyphs.ellipsis,
			rowColumn{text: project, critical: true},
			rowColumn{text: cluster},
		)
	case project != "":
		return renderRowColumns(s.width, project, s.styles.glyphs.ellipsis, rowColumn{text: cluster})
	default:
		return truncateLine(cluster, s.width, s.styles.glyphs.ellipsis)
	}
}

func (s *namespaceScreen) SetSize(width, height int) {
	s.width, s.bodyHeight = width, height
	s.layout()
}

func (s *namespaceScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
	applySpinnerStyle(&s.spinner, st)
}

func (s *namespaceScreen) layout() {
	extra := 0
	if s.identityLine() != "" {
		extra++
	}
	if s.forbiddenFallback {
		extra++
	}
	extra += len(s.notes)
	if s.stateLine() != "" {
		extra++
	}
	s.list.SetSize(s.width, max(0, s.bodyHeight-extra))
}

func (s *namespaceScreen) Title() string { return "namespaces" }
func (s *namespaceScreen) Hints() footerHints {
	bindings := []bubbleskey.Binding{s.km.Open, s.km.AllNamespaces, s.km.Workloads, s.env.keymaps().global.Filter, s.env.keymaps().global.Quit}
	if s.km.Refresh.Help().Key != packageDefaultKeyMaps.namespace.Refresh.Help().Key {
		bindings = append([]bubbleskey.Binding{s.km.Refresh}, bindings...)
	}
	return browsingHints(bindings...)
}
func (s *namespaceScreen) Help() helpGroup {
	return helpGroup{title: "namespaces", entries: []helpGroupEntry{
		{binding: s.km.Open, desc: "browse this namespace's secrets and configmaps"},
		{binding: s.km.AllNamespaces, desc: "browse every namespace at once"},
		{binding: s.km.Workloads, desc: "browse workloads in this namespace"},
	}}
}
func (s *namespaceScreen) CapturesInput() bool { return s.list.SettingFilter() }
func (s *namespaceScreen) WantsEsc() bool {
	return s.pending || s.list.FilterState() != list.Unfiltered
}
