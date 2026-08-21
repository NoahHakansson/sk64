package tui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func newListModel(st *styles, km list.KeyMap) list.Model {
	delegate := newListDelegate(st)
	model := list.New(nil, delegate, 0, 0)
	model.KeyMap = km
	model.Title = ""
	model.SetShowTitle(true)
	model.SetShowHelp(false)
	model.SetShowStatusBar(false)
	model.SetShowPagination(true)
	model.DisableQuitKeybindings()
	applyListStyles(&model, st)
	return model
}

func applyListStyles(model *list.Model, st *styles) {
	model.Styles = st.listStyle
	model.FilterInput.SetStyles(st.textInputStyle)
	model.Paginator.ActiveDot = st.listStyle.ActivePaginationDot.String()
	model.Paginator.InactiveDot = st.listStyle.InactivePaginationDot.String()
	model.SetDelegate(newListDelegate(st))
}

func applyDetailedListStyles(model *list.Model, st *styles) {
	applyListStyles(model, st)
	delegate := newListDelegate(st)
	delegate.ShowDescription = true
	model.SetDelegate(delegate)
}

type listDelegate struct {
	ShowDescription  bool
	DescriptionLines int
	Styles           list.DefaultItemStyles
	st               *styles
}

func newListDelegate(st *styles) listDelegate {
	return listDelegate{Styles: st.listItemStyle, st: st}
}

func (d listDelegate) Height() int {
	if d.ShowDescription {
		return 1 + max(1, d.DescriptionLines)
	}
	return 1
}

func (d listDelegate) Spacing() int { return 0 }

func (d listDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

type kindBadgedItem interface {
	kindBadge() string
}

type kindBadgeSeparatorItem interface {
	kindBadgeSeparator() string
}

type columnedListItem interface {
	listColumns() (string, []rowColumn)
}

type alignedListItem interface {
	rowAlignment() *rowAlignment
}

type prefixColumnItem interface {
	prefixColumn() string
}

type offsetFilterMatchItem interface {
	filterMatchOffset() int
}

type sectionHeadingItem interface {
	sectionHeading() (text string, dim bool)
}

type unselectableListItem interface {
	unselectableRow() bool
}

func (d listDelegate) Render(w io.Writer, model list.Model, index int, item list.Item) {
	if model.Width() <= 0 {
		return
	}
	if heading, ok := item.(sectionHeadingItem); ok {
		text, dim := heading.sectionHeading()
		if dim {
			text = d.st.dim.Render("  " + text)
		} else {
			text = d.st.dialogTitle.Render(text)
		}
		_, _ = fmt.Fprint(w, ansi.Truncate(text, model.Width(), d.st.glyphs.ellipsis))
		return
	}

	current, ok := item.(list.DefaultItem)
	if !ok {
		return
	}

	styles := &d.Styles
	textWidth := model.Width() - styles.NormalTitle.GetPaddingLeft() - styles.NormalTitle.GetPaddingRight()
	if textWidth <= 0 {
		return
	}

	identity := current.Title()
	var columns []rowColumn
	columned, hasColumns := item.(columnedListItem)
	if hasColumns {
		identity, columns = columned.listColumns()
	}
	titleWidth := textWidth
	prefix := ""
	if prefixed, ok := item.(prefixColumnItem); ok {
		candidate := prefixed.prefixColumn()
		candidateWidth := lipgloss.Width(candidate + rowColumnSeparator)
		if candidate != "" && titleWidth-candidateWidth >= minimumRowIdentityWidth {
			prefix = candidate
			titleWidth -= candidateWidth
		}
	}
	badge := ""
	badgePrefixRunes := 0
	badgeSeparator := " "
	if badged, ok := item.(kindBadgedItem); ok {
		candidate := badged.kindBadge()
		if separated, ok := item.(kindBadgeSeparatorItem); ok {
			badgeSeparator = separated.kindBadgeSeparator()
		}
		if candidate != "" {
			if lipgloss.Width(candidate+badgeSeparator) < textWidth {
				badge = candidate
			} else {
				badgePrefix := candidate + badgeSeparator
				identity = badgePrefix + identity
				badgePrefixRunes = utf8.RuneCountInString(badgePrefix)
			}
		}
	}
	if badge != "" {
		titleWidth -= lipgloss.Width(badge + badgeSeparator)
	}
	var alignment *rowAlignment
	if aligned, ok := item.(alignedListItem); ok {
		alignment = aligned.rowAlignment()
	}
	var title string
	if hasColumns {
		title = renderAlignedRowColumns(titleWidth, identity, d.st.glyphs.ellipsis, alignment, columns...)
	} else {
		title = ansi.Truncate(identity, titleWidth, d.st.glyphs.ellipsis)
	}
	withPrefixAndBadge := func(title string) string {
		if prefix != "" {
			title = d.st.dim.Render(prefix+rowColumnSeparator) + title
		}
		if badge == "" {
			return title
		}
		return d.st.kindBadge.Render(badge) + badgeSeparator + title
	}

	description := current.Description()
	if d.ShowDescription {
		lines := make([]string, 0, d.Height()-1)
		for lineIndex, line := range strings.Split(description, "\n") {
			if lineIndex >= d.Height()-1 {
				break
			}
			lines = append(lines, ansi.Truncate(line, textWidth, d.st.glyphs.ellipsis))
		}
		description = strings.Join(lines, "\n")
	}

	selected := index == model.Index()
	emptyFilter := model.FilterState() == list.Filtering && model.FilterValue() == ""
	filtered := model.FilterState() == list.Filtering || model.FilterState() == list.FilterApplied
	matchedRunes := []int(nil)
	if filtered {
		matchedRunes = displayMatchRunesWithBadgePrefix(item, model.MatchesForItem(index), badgePrefixRunes)
	}

	switch {
	case emptyFilter:
		title = renderStyleAroundANSI(styles.DimmedTitle, withPrefixAndBadge(title))
		description = styles.DimmedDesc.Render(description)
	case selected && model.FilterState() != list.Filtering:
		if filtered {
			unmatched := styles.SelectedTitle.Inline(true)
			matched := unmatched.Inherit(styles.FilterMatch)
			title = lipgloss.StyleRunes(title, matchedRunes, matched, unmatched)
		}
		title = d.st.renderSelectionBand(withPrefixAndBadge(title), model.Width())
		if d.ShowDescription {
			gutter := d.st.selectionGutter()
			lines := strings.Split(description, "\n")
			for lineIndex, line := range lines {
				line = gutter + line
				line += strings.Repeat(" ", max(0, model.Width()-lipgloss.Width(line)))
				lines[lineIndex] = renderStyleAroundANSI(styles.SelectedDesc, line)
			}
			description = strings.Join(lines, "\n")
		}
	default:
		if filtered {
			unmatched := styles.NormalTitle.Inline(true)
			matched := unmatched.Inherit(styles.FilterMatch)
			title = lipgloss.StyleRunes(title, matchedRunes, matched, unmatched)
		}
		title = renderStyleAroundANSI(styles.NormalTitle, withPrefixAndBadge(title))
		description = styles.NormalDesc.Render(description)
	}

	if d.ShowDescription {
		_, _ = fmt.Fprintf(w, "%s\n%s", title, description)
		return
	}
	_, _ = fmt.Fprint(w, title)
}

func groupedFilter(term string, targets []string) []list.Rank {
	matches := make(map[int][]int)
	for _, rank := range list.DefaultFilter(term, targets) {
		matches[rank.Index] = rank.MatchedIndexes
	}
	ranks := make([]list.Rank, 0, len(targets))
	for index, target := range targets {
		matchedIndexes, matched := matches[index]
		if target == "" || matched {
			ranks = append(ranks, list.Rank{Index: index, MatchedIndexes: matchedIndexes})
		}
	}
	return ranks
}

func clampToSelectable(model *list.Model, previous int) {
	selected, ok := model.SelectedItem().(unselectableListItem)
	if !ok || !selected.unselectableRow() {
		return
	}
	items := model.VisibleItems()
	direction := 1
	if model.Index() < previous {
		direction = -1
	}
	for _, step := range []int{direction, -direction} {
		for index := model.Index() + step; index >= 0 && index < len(items); index += step {
			unselectable, ok := items[index].(unselectableListItem)
			if !ok || !unselectable.unselectableRow() {
				model.Select(index)
				return
			}
		}
	}
}

// listStatusSegments returns the count/filter segments for a list screen.
func listStatusSegments(model *list.Model, noun string) []string {
	if len(model.Items()) == 0 {
		return nil
	}
	if model.FilterState() == list.FilterApplied {
		return []string{
			`filter "` + model.FilterInput.Value() + `"`,
			fmt.Sprintf("%d of %d", len(model.VisibleItems()), len(model.Items())),
		}
	}
	return []string{plural(len(model.Items()), noun)}
}

// statusRowText joins and pre-truncates segments for the Bubbles title row.
func statusRowText(st *styles, width int, segments ...string) string {
	nonEmpty := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment != "" {
			nonEmpty = append(nonEmpty, segment)
		}
	}
	maxWidth := max(0, width-6)
	if len(nonEmpty) == 0 || maxWidth == 0 {
		return ""
	}
	return truncateLine(strings.Join(nonEmpty, st.glyphs.separator), maxWidth, st.glyphs.ellipsis)
}

func displayMatchRunesWithBadgePrefix(item list.Item, matchedRunes []int, badgePrefixRunes int) []int {
	offsetItem, ok := item.(offsetFilterMatchItem)
	if !ok {
		return matchedRunes
	}
	offset := offsetItem.filterMatchOffset()
	if offset == 0 {
		return matchedRunes
	}
	translated := make([]int, len(matchedRunes))
	_, badged := item.(kindBadgedItem)
	for index, matchedRune := range matchedRunes {
		if badged {
			translated[index] = matchedRune - offset + badgePrefixRunes
		} else {
			translated[index] = matchedRune + offset
		}
	}
	return translated
}

type scopedListFilterMatchesMsg struct {
	model   *list.Model
	matches list.FilterMatchesMsg
}

func updateListModel(model *list.Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case scopedListFilterMatchesMsg:
		if msg.model != model {
			return nil
		}
		updated, cmd := model.Update(msg.matches)
		*model = updated
		return scopeListFilterCmd(model, cmd)
	case list.FilterMatchesMsg:
		return nil
	default:
		updated, cmd := model.Update(msg)
		*model = updated
		return scopeListFilterCmd(model, cmd)
	}
}

func scopeListFilterCmd(model *list.Model, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch msg := cmd().(type) {
		case list.FilterMatchesMsg:
			return scopedListFilterMatchesMsg{model: model, matches: msg}
		case tea.BatchMsg:
			for index, batched := range msg {
				msg[index] = scopeListFilterCmd(model, batched)
			}
			return msg
		default:
			return msg
		}
	}
}

type stateLineKind int

const (
	stateLineLoading stateLineKind = iota
	stateLineSuccess
	stateLineError
	stateLineEmpty
	stateLineIncomplete
	stateLineUnknown
	stateLineKindCount
)

func (st *styles) stateMarker(kind stateLineKind) string {
	return st.glyphs.stateMarkers[kind]
}

func renderStateLine(st *styles, kind stateLineKind, message, action string, width int) string {
	var style lipgloss.Style
	switch kind {
	case stateLineSuccess:
		style = st.successText
	case stateLineError:
		style = st.errText
	case stateLineIncomplete, stateLineUnknown:
		style = st.warnText
	default:
		style = st.dim
	}
	prefix := style.Render(st.stateMarker(kind) + " " + message)
	return renderMarkedLine(st, prefix, action, width)
}

func renderLoadingLine(st *styles, frame, message, action string, width int) string {
	marker := st.stateMarker(stateLineLoading)
	if marker != "" {
		marker = st.spinnerStyle.Render(marker)
	} else {
		marker = frame
	}
	prefix := marker + " " + st.spinnerStyle.Render(message)
	return renderMarkedLine(st, prefix, action, width)
}

func renderMarkedLine(st *styles, prefix, action string, width int) string {
	if action == "" {
		return truncateLine(prefix, width, st.glyphs.ellipsis)
	}
	styledAction := st.dim.Render(action)
	suffix := st.dim.Render(st.glyphs.separator + action)
	if width <= 0 || lipgloss.Width(prefix)+lipgloss.Width(suffix) <= width {
		return prefix + suffix
	}
	prefixWidth := width - lipgloss.Width(suffix)
	if prefixWidth <= 0 {
		return truncateLine(styledAction, width, st.glyphs.ellipsis)
	}
	return truncateLine(prefix, prefixWidth, st.glyphs.ellipsis) + suffix
}

func renderListWithoutPrematureEmpty(model list.Model, suppressEmpty bool) string {
	if !suppressEmpty || model.SettingFilter() || len(model.VisibleItems()) > 0 {
		return model.View()
	}
	return strings.Repeat("\n", max(0, model.Height()-1))
}

func renderListTitle(model list.Model) string {
	if model.Title == "" {
		return ""
	}
	return model.Styles.TitleBar.Render(model.Styles.Title.Render(model.Title))
}

func fitListHeight(view string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func filteredListState(model list.Model, st *styles, width int) string {
	if model.FilterState() != list.FilterApplied || len(model.VisibleItems()) > 0 {
		return ""
	}
	return renderStateLine(st, stateLineEmpty, "no matches", bindingAction(model.KeyMap.Filter, "to refine"), width)
}
