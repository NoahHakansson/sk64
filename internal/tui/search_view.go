package tui

import (
	"context"
	"fmt"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
)

const maxSearchHits = 200

type searchEntry struct {
	namespace string
	kind      string
	name      string
	keys      []string
}

type searchHit struct {
	entry searchEntry
	key   string
}

type searchNote struct {
	namespace string
	kind      string
	message   string
}

type searchItem struct {
	hit   searchHit
	align *rowAlignment
}

func (i searchItem) Title() string {
	identity, _ := i.listColumns()
	return identity
}
func (i searchItem) Description() string { return "" }
func (i searchItem) FilterValue() string { return i.hit.entry.name }
func (i searchItem) listColumns() (string, []rowColumn) {
	identity := fmt.Sprintf("%s  %s/%s", i.hit.entry.namespace, i.hit.entry.kind, i.hit.entry.name)
	if i.hit.key == "" {
		return identity, nil
	}
	return identity, []rowColumn{{text: "(key: " + i.hit.key + ")"}}
}
func (i searchItem) rowAlignment() *rowAlignment { return i.align }

type searchScreen struct {
	loader
	ctx           context.Context
	client        *k8s.Client
	env           editEnv
	km            searchKeyMap
	styles        *styles
	input         textinput.Model
	spinner       spinner.Model
	entries       []searchEntry
	hits          []searchHit
	list          list.Model
	namespaces    []string
	nsIndex       int
	kindIndex     int
	continueToken string
	walkCtx       context.Context
	notes         []searchNote
	cancelled     bool
	truncated     bool
	width         int
	bodyHeight    int
}

func newSearchScreen(ctx context.Context, client *k8s.Client, env editEnv, st *styles) *searchScreen {
	input := newTextInput(st)
	input.Prompt = "search: "
	model := newListModel(st, env.keymaps().list)
	model.SetFilteringEnabled(false)
	return &searchScreen{ctx: ctx, client: client, env: env, km: env.keymaps().search, styles: st, input: input, spinner: newSpinner(st), list: model}
}

func (s *searchScreen) Init() tea.Cmd {
	return tea.Batch(s.input.Focus(), s.startWalk())
}

func (s *searchScreen) startWalk() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.walkCtx = ctx
	s.entries = nil
	s.hits = nil
	s.namespaces = nil
	s.nsIndex = 0
	s.kindIndex = 0
	s.continueToken = ""
	s.notes = nil
	s.cancelled = false
	s.truncated = false
	setItems := s.recompute()
	s.layout()
	return tea.Batch(setItems, s.fetchNamespaces(ctx, reqID, ""), s.spinner.Tick)
}

func (s *searchScreen) fetchNamespaces(ctx context.Context, reqID int, continueToken string) tea.Cmd {
	return func() tea.Msg {
		page, err := s.client.ListNamespaces(ctx, k8s.DefaultPageSize, continueToken)
		return searchNamespacesMsg{reqID: reqID, page: page, err: err}
	}
}

func (s *searchScreen) kinds() []string {
	if s.env.noConfigMaps {
		return []string{k8s.KindSecret}
	}
	return []string{k8s.KindSecret, k8s.KindConfigMap}
}

func (s *searchScreen) nextFetch() tea.Cmd {
	if s.nsIndex >= len(s.namespaces) {
		s.finish(s.reqID)
		return nil
	}
	namespace := s.namespaces[s.nsIndex]
	kind := s.kinds()[s.kindIndex]
	continueToken := s.continueToken
	ctx, reqID := s.walkCtx, s.reqID
	return func() tea.Msg {
		var page k8s.ResourcePage
		var err error
		if kind == k8s.KindSecret {
			page, err = s.client.ListSecrets(ctx, namespace, k8s.DefaultPageSize, continueToken)
		} else {
			page, err = s.client.ListConfigMaps(ctx, namespace, k8s.DefaultPageSize, continueToken)
		}
		return searchResourcesMsg{reqID: reqID, namespace: namespace, kind: kind, page: page, err: err}
	}
}

func (s *searchScreen) advanceWalk() tea.Cmd {
	s.continueToken = ""
	s.kindIndex++
	if s.kindIndex >= len(s.kinds()) {
		s.kindIndex = 0
		s.nsIndex++
	}
	return s.nextFetch()
}

func (s *searchScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	defer s.layout()
	switch msg := msg.(type) {
	case list.FilterMatchesMsg, scopedListFilterMatchesMsg:
		return s, nil
	case spinner.TickMsg:
		if !s.pending {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case searchNamespacesMsg:
		if msg.reqID != s.reqID || !s.pending {
			return s, nil
		}
		if msg.err != nil {
			if len(s.namespaces) == 0 {
				s.namespaces = []string{s.client.Namespace}
				s.notes = append(s.notes, searchNote{
					namespace: s.client.Namespace,
					kind:      "Namespace",
					message:   fmt.Sprintf("cannot list namespaces%ssearching %s only: %v", s.styles.glyphs.separator, s.client.Namespace, msg.err),
				})
			} else {
				s.notes = append(s.notes, searchNote{namespace: "all", kind: "Namespace", message: "list namespaces: " + msg.err.Error()})
			}
			return s, s.nextFetch()
		}
		s.namespaces = append(s.namespaces, msg.page.Names...)
		if msg.page.Continue != "" {
			return s, s.fetchNamespaces(s.walkCtx, msg.reqID, msg.page.Continue)
		}
		return s, s.nextFetch()
	case searchResourcesMsg:
		if msg.reqID != s.reqID || !s.pending {
			return s, nil
		}
		if msg.err != nil {
			s.notes = append(s.notes, searchNote{namespace: msg.namespace, kind: msg.kind, message: msg.err.Error()})
			return s, s.advanceWalk()
		}
		for _, resource := range msg.page.Items {
			s.entries = append(s.entries, searchEntry{
				namespace: msg.namespace,
				kind:      resource.Kind(),
				name:      resource.Name(),
				keys:      resource.Keys(),
			})
		}
		setItems := s.recompute()
		if msg.page.Continue != "" {
			s.continueToken = msg.page.Continue
			return s, tea.Batch(setItems, s.nextFetch())
		}
		return s, tea.Batch(setItems, s.advanceWalk())
	case tea.KeyPressMsg:
		switch {
		case bubbleskey.Matches(msg, s.km.Up):
			s.list.CursorUp()
			return s, nil
		case bubbleskey.Matches(msg, s.km.Down):
			s.list.CursorDown()
			return s, nil
		case bubbleskey.Matches(msg, s.km.Open):
			item, ok := s.list.SelectedItem().(searchItem)
			if !ok {
				return s, nil
			}
			hit := item.hit
			return s, func() tea.Msg {
				return searchJumpMsg{namespace: hit.entry.namespace, kind: hit.entry.kind, name: hit.entry.name}
			}
		case bubbleskey.Matches(msg, s.km.Refresh):
			if s.pending {
				return s, nil
			}
			return s, s.startWalk()
		case bubbleskey.Matches(msg, s.km.Cancel):
			if s.pending {
				s.stop()
				s.cancelled = true
				return s, nil
			}
			return s, popScreen()
		}
	}
	var cmd tea.Cmd
	before := s.input.Value()
	s.input, cmd = s.input.Update(msg)
	if s.input.Value() != before {
		return s, tea.Batch(cmd, s.recompute())
	}
	return s, cmd
}

func (s *searchScreen) recompute() tea.Cmd {
	previous := s.list.Index()
	query := s.input.Value()
	if query == "" {
		s.hits = nil
		s.truncated = false
		return s.list.SetItems(nil)
	}
	targets := make([]string, 0, len(s.entries))
	candidates := make([]searchHit, 0, len(s.entries))
	for _, entry := range s.entries {
		targets = append(targets, entry.name)
		candidates = append(candidates, searchHit{entry: entry})
		for _, key := range entry.keys {
			targets = append(targets, key)
			candidates = append(candidates, searchHit{entry: entry, key: key})
		}
	}
	ranks := list.DefaultFilter(query, targets)
	s.truncated = len(ranks) > maxSearchHits
	if len(ranks) > maxSearchHits {
		ranks = ranks[:maxSearchHits]
	}
	s.hits = make([]searchHit, len(ranks))
	for i, rank := range ranks {
		s.hits[i] = candidates[rank.Index]
	}
	align := &rowAlignment{}
	items := make([]list.Item, len(s.hits))
	for index, hit := range s.hits {
		items[index] = searchItem{hit: hit, align: align}
	}
	*align = measureRowAlignment(items)
	cmd := s.list.SetItems(items)
	if len(items) > 0 {
		s.list.Select(min(previous, len(items)-1))
	}
	return cmd
}

func (s *searchScreen) View() string {
	parts, tail, suppressEmpty := s.viewParts()
	parts = append(parts, fitListHeight(renderListWithoutPrematureEmpty(s.list, suppressEmpty), s.list.Height()))
	parts = append(parts, tail...)
	if s.bodyHeight > 0 && len(parts) > s.bodyHeight {
		parts = parts[:s.bodyHeight]
	}
	return strings.Join(parts, "\n")
}

func (s *searchScreen) viewParts() (header, tail []string, suppressEmpty bool) {
	header = []string{s.input.View()}
	if !s.pending {
		header = append(header, fmt.Sprintf("%s across %s indexed", plural(len(s.entries), "resource"), plural(len(s.namespaces), "namespace")))
	}

	showTypeHint := s.input.Value() == "" && !s.pending
	fixedLines := len(header)
	if showTypeHint {
		fixedLines++
	}
	if s.stateLine(0) != "" {
		fixedLines++
	}
	if s.truncated {
		fixedLines++
	}
	if len(s.hits) > 0 {
		fixedLines++
		if !s.pending && s.input.Value() != "" {
			fixedLines++
		}
	}

	visibleNotes := len(s.notes)
	if s.pending {
		visibleNotes = 0
	} else if s.bodyHeight > 0 {
		visibleNotes = min(visibleNotes, max(0, s.bodyHeight-fixedLines))
	}
	for _, note := range s.notes[:visibleNotes] {
		line := fmt.Sprintf("%s %s: %s", note.namespace, note.kind, note.message)
		header = append(header, s.styles.dim.Render(truncateLine(line, s.width, s.styles.glyphs.ellipsis)))
	}
	if showTypeHint {
		header = append(header, s.styles.dim.Render(truncateLine("type to search resource and key names across all namespaces", s.width, s.styles.glyphs.ellipsis)))
	}
	stateLine := s.stateLine(len(s.notes) - visibleNotes)
	if stateLine != "" {
		header = append(header, stateLine)
	}

	if s.pending || s.input.Value() == "" {
		s.list.Title = ""
	} else {
		matchNoun := "matches"
		if len(s.hits) == 1 {
			matchNoun = "match"
		}
		s.list.Title = statusRowText(s.styles, s.width, fmt.Sprintf("%d %s", len(s.hits), matchNoun))
	}
	if s.truncated {
		tail = append(tail, truncateLine("+ more matches"+s.styles.glyphs.separator+"refine your search", s.width, s.styles.glyphs.ellipsis))
	}
	return header, tail, s.input.Value() == "" || stateLine != ""
}

func (s *searchScreen) stateLine(hiddenNotes int) string {
	if s.pending {
		message := fmt.Sprintf("searching %s... (%s indexed)", s.currentNamespace(), plural(len(s.entries), "resource"))
		return renderLoadingLine(s.styles, s.spinner.View(), message, "esc to cancel", s.width)
	}
	if s.cancelled {
		kind := stateLineUnknown
		message := "search cancelled; results unknown"
		if len(s.entries) > 0 {
			kind = stateLineIncomplete
			message = "search cancelled; retained results incomplete"
		}
		if affected := s.affectedNamespaces(); affected > 0 {
			message += "; " + plural(affected, "namespace") + " affected"
		}
		return renderStateLine(s.styles, kind, message, bindingAction(s.km.Refresh, "to retry"), s.width)
	}
	if len(s.notes) > 0 {
		message := plural(s.affectedNamespaces(), "namespace") + " affected"
		if hiddenNotes > 0 {
			message = fmt.Sprintf("%s; %s hidden", plural(s.affectedNamespaces(), "namespace"), plural(hiddenNotes, "note"))
		}
		return renderStateLine(s.styles, stateLineIncomplete, message, bindingAction(s.km.Refresh, "retry"), s.width)
	}
	if s.input.Value() != "" && len(s.hits) == 0 {
		return renderStateLine(s.styles, stateLineEmpty, "no matches", bindingAction(s.km.Refresh, "to rescan"), s.width)
	}
	return ""
}

func (s *searchScreen) affectedNamespaces() int {
	namespaces := make(map[string]struct{}, len(s.notes))
	for _, note := range s.notes {
		namespaces[note.namespace] = struct{}{}
	}
	return len(namespaces)
}

func (s *searchScreen) currentNamespace() string {
	if s.nsIndex < len(s.namespaces) {
		return s.namespaces[s.nsIndex]
	}
	return "namespaces"
}

func (s *searchScreen) SetSize(width, height int) {
	s.width = width
	s.bodyHeight = height
	s.input.SetWidth(textInputWidth(width, s.input.Prompt))
	s.layout()
}
func (s *searchScreen) SetStyles(st *styles) {
	s.styles = st
	applyTextInputStyles(&s.input, st)
	applyListStyles(&s.list, st)
	applySpinnerStyle(&s.spinner, st)
}

func (s *searchScreen) layout() {
	header, tail, _ := s.viewParts()
	s.list.SetSize(s.width, max(0, s.bodyHeight-len(header)-len(tail)))
}
func (s *searchScreen) Title() string { return "search" }
func (s *searchScreen) Hints() footerHints {
	if s.pending {
		return hintBindings(s.km.Open, hintDesc(s.km.Cancel, "cancel search"))
	}
	return hintBindings(s.km.Open, s.km.Refresh, s.km.Cancel)
}
func (s *searchScreen) Help() helpGroup     { return helpGroup{} }
func (s *searchScreen) CapturesInput() bool { return true }
func (s *searchScreen) WantsEsc() bool      { return true }
