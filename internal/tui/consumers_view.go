package tui

import (
	"context"
	"strings"
	"unicode/utf8"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
)

type consumerItem struct {
	consumer k8s.Consumer
	styles   *styles
	align    *rowAlignment
}

func (i consumerItem) Title() string {
	identity, columns := i.listColumns()
	return renderRowColumns(0, identity, "", columns...)
}
func (i consumerItem) listColumns() (string, []rowColumn) {
	return i.consumer.Kind + "/" + i.consumer.Name, referenceListColumns(i.consumer.Ref, i.styles)
}
func (i consumerItem) Description() string         { return "" }
func (i consumerItem) FilterValue() string         { return i.consumer.Name }
func (i consumerItem) rowAlignment() *rowAlignment { return i.align }
func (i consumerItem) filterMatchOffset() int {
	return utf8.RuneCountInString(i.consumer.Kind + "/")
}

type consumersScreen struct {
	refsCollector
	env               editEnv
	km                consumersKeyMap
	styles            *styles
	kind, name        string
	list              list.Model
	spinner           spinner.Model
	empty             bool
	width, bodyHeight int
}

func newConsumersScreen(ctx context.Context, client *k8s.Client, kind, namespace, name string, env editEnv, st *styles) *consumersScreen {
	return &consumersScreen{
		refsCollector: newRefsCollector(ctx, client, namespace, false),
		env:           env,
		km:            env.keymaps().consumers,
		styles:        st,
		kind:          kind,
		name:          name,
		list:          newListModel(st, env.keymaps().list),
		spinner:       newSpinner(st),
	}
}

func (s *consumersScreen) Init() tea.Cmd { return s.startLoading() }
func (s *consumersScreen) startLoading() tea.Cmd {
	s.list.ResetFilter()
	s.empty = false
	setItems := scopeListFilterCmd(&s.list, s.list.SetItems(nil))
	s.layout()
	return tea.Batch(setItems, s.startCollect(), s.spinner.Tick)
}

func (s *consumersScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
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
		}
	}
	return s, updateListModel(&s.list, msg)
}

func (s *consumersScreen) setItems() tea.Cmd {
	consumers := s.index.ConsumersOf(s.kind, s.name)
	s.empty = len(consumers) == 0
	items := make([]list.Item, 0, len(consumers))
	align := &rowAlignment{}
	for _, consumer := range consumers {
		items = append(items, consumerItem{consumer: consumer, styles: s.styles, align: align})
	}
	*align = measureRowAlignment(items)
	return scopeListFilterCmd(&s.list, s.list.SetItems(items))
}

func (s *consumersScreen) View() string {
	s.list.Title = s.statusRow()
	stateLine := s.stateLine()
	parts := []string{
		renderSubjectLine(s.subject(), s.width, s.styles),
		renderListWithoutPrematureEmpty(s.list, stateLine != ""),
	}
	if stateLine != "" {
		parts = append(parts, stateLine)
	}
	return strings.Join(parts, "\n")
}

func (s *consumersScreen) statusRow() string {
	if s.pending {
		return ""
	}
	return statusRowText(s.styles, s.width, listStatusSegments(&s.list, "consumer")...)
}

func (s *consumersScreen) stateLine() string {
	if s.pending {
		return renderLoadingLine(s.styles, s.spinner.View(), "loading consumers", "esc to cancel", s.width)
	}
	if s.cancelled {
		return renderStateLine(s.styles, stateLineUnknown, "consumer scan cancelled; results unknown", bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if s.index != nil && len(s.index.Notes()) > 0 {
		return renderStateLine(s.styles, stateLineIncomplete, "scan incomplete: "+strings.Join(s.index.Notes(), ", "), bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if filtered := filteredListState(s.list, s.styles, s.width); filtered != "" {
		return filtered
	}
	if s.complete && s.empty {
		return renderStateLine(s.styles, stateLineEmpty, "no consumers found in namespace "+s.namespace, bindingAction(s.km.Refresh, "to refresh"), s.width)
	}
	return ""
}

func (s *consumersScreen) SetSize(width, height int) {
	s.width, s.bodyHeight = width, height
	s.layout()
}
func (s *consumersScreen) SetStyles(st *styles) {
	s.styles = st
	applyListStyles(&s.list, st)
	applySpinnerStyle(&s.spinner, st)
}
func (s *consumersScreen) layout() {
	extra := 1
	if s.stateLine() != "" {
		extra++
	}
	s.list.SetSize(s.width, max(0, s.bodyHeight-extra))
}
func (s *consumersScreen) subject() string {
	return "Consumers of " + resourceSubject(s.kind, s.namespace, s.name)
}
func (s *consumersScreen) Title() string { return s.subject() }
func (s *consumersScreen) Hints() footerHints {
	return browsingHints(s.env.keymaps().global.Filter)
}
func (s *consumersScreen) Help() helpGroup     { return helpGroup{title: "consumers"} }
func (s *consumersScreen) CapturesInput() bool { return s.list.SettingFilter() }
func (s *consumersScreen) WantsEsc() bool {
	return s.pending || s.list.FilterState() != list.Unfiltered
}
