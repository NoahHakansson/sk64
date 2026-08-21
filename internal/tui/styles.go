package tui

import (
	"fmt"
	"image/color"
	"net/url"
	"slices"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	unicodeSeparator = " · "
	unicodeEllipsis  = "…"
)

type semanticPalette struct {
	fg, fgMuted, fgFaint             color.Color
	accent, success, warning, danger color.Color
	brand, onBrand, gold, cyan       color.Color
	chromeHigher                     color.Color
}

type glyphs struct {
	spinner                                       spinner.Spinner
	secretBadge, configMapBadge                   string
	cursorMarker, rolloutMarker, cronMarker       string
	subPathMarker                                 string
	warnMarker, errMarker, wrapMarker, ruleMarker string
	foundTag, notFoundTag                         string
	binaryTag, immutableTag, missingTag           string
	noAccessTag                                   string
	activeTag, inactiveTag                        string
	serverMismatchTag, contextNotFoundTag         string
	currentTag, checkFailedTag, originMismatchTag string
	stateMarkers                                  [stateLineKindCount]string
	activePage, inactivePage, divider             string
	separator, ellipsis                           string
	border                                        lipgloss.Border
}

type styles struct {
	palette semanticPalette
	glyphs  glyphs

	header, activeContext, footer, footerKey                lipgloss.Style
	errText, successText, tag, dim, tooSmall                lipgloss.Style
	diffAdd, diffDel                                        lipgloss.Style
	jsonKey, jsonString, jsonNumber, jsonLiteral            lipgloss.Style
	helpBox, dialogBox, dialogDanger, dialogTitle, warnText lipgloss.Style
	kindBadge, spinnerStyle, selectedRow, cursorText        lipgloss.Style
	listStyle                                               list.Styles
	listItemStyle                                           list.DefaultItemStyles
	textInputStyle                                          textinput.Styles
}

func newGlyphs(ascii bool) glyphs {
	if ascii {
		return glyphs{
			spinner:            spinner.Line,
			secretBadge:        "[S]",
			configMapBadge:     "[C]",
			cursorMarker:       ">",
			rolloutMarker:      "[rollout]",
			cronMarker:         "[cron]",
			subPathMarker:      "[subPath]",
			warnMarker:         "!",
			errMarker:          "x",
			wrapMarker:         ">",
			ruleMarker:         "-",
			foundTag:           "[found]",
			notFoundTag:        "[not in cluster]",
			binaryTag:          "[binary]",
			immutableTag:       "[immutable]",
			missingTag:         "[missing]",
			noAccessTag:        "[no access]",
			activeTag:          "[active]",
			inactiveTag:        "[inactive]",
			serverMismatchTag:  "[server mismatch]",
			contextNotFoundTag: "[context not found]",
			currentTag:         "[current]",
			checkFailedTag:     "[check failed]",
			originMismatchTag:  "[origin mismatch]",
			stateMarkers:       [stateLineKindCount]string{"[loading]", "[success]", "[error]", "[empty]", "[incomplete]", "[unknown]"},
			activePage:         "*",
			inactivePage:       ".",
			divider:            " * ",
			separator:          " - ",
			ellipsis:           "...",
			border:             lipgloss.ASCIIBorder(),
		}
	}
	return glyphs{
		spinner:            spinner.MiniDot,
		secretBadge:        "🔒",
		configMapBadge:     "📄",
		cursorMarker:       "▸",
		rolloutMarker:      "↻ rollout",
		cronMarker:         "↻",
		subPathMarker:      "⚠ subPath",
		warnMarker:         "⚠",
		errMarker:          "✗",
		wrapMarker:         "↳",
		ruleMarker:         "─",
		foundTag:           "✓ found",
		notFoundTag:        "✗ not in cluster",
		binaryTag:          "binary",
		immutableTag:       "immutable",
		missingTag:         "missing",
		noAccessTag:        "no access",
		activeTag:          "active",
		inactiveTag:        "inactive",
		serverMismatchTag:  "server mismatch",
		contextNotFoundTag: "context not found",
		currentTag:         "current",
		checkFailedTag:     "check failed",
		originMismatchTag:  "origin mismatch",
		stateMarkers:       [stateLineKindCount]string{"", "✓", "✗", "○", "⚠", "⚠"},
		activePage:         "●",
		inactivePage:       "○",
		divider:            " · ",
		separator:          unicodeSeparator,
		ellipsis:           unicodeEllipsis,
		border:             lipgloss.RoundedBorder(),
	}
}

func newStyles(isDark bool, glyphs glyphs) *styles {
	palette := newSemanticPalette(isDark)
	header := lipgloss.NewStyle().Foreground(palette.onBrand).Background(palette.brand)
	textInputStyle := newTextInputStyles(palette)
	spinnerStyle := lipgloss.NewStyle().Foreground(palette.accent)
	borderedSurface := lipgloss.NewStyle().Foreground(palette.fg).Border(glyphs.border).Padding(1, 2)

	return &styles{
		palette:        palette,
		glyphs:         glyphs,
		header:         header,
		activeContext:  header.Bold(true).Foreground(palette.gold),
		footer:         lipgloss.NewStyle().Foreground(palette.fgMuted),
		footerKey:      lipgloss.NewStyle().Foreground(palette.fg),
		errText:        lipgloss.NewStyle().Foreground(palette.danger),
		successText:    lipgloss.NewStyle().Foreground(palette.success),
		tag:            lipgloss.NewStyle().Foreground(palette.gold),
		dim:            lipgloss.NewStyle().Foreground(palette.fgMuted),
		tooSmall:       lipgloss.NewStyle().Bold(true).Foreground(palette.danger),
		diffAdd:        lipgloss.NewStyle().Foreground(palette.success),
		diffDel:        lipgloss.NewStyle().Foreground(palette.danger),
		jsonKey:        lipgloss.NewStyle().Foreground(palette.cyan),
		jsonString:     lipgloss.NewStyle().Foreground(palette.success),
		jsonNumber:     lipgloss.NewStyle().Foreground(palette.warning),
		jsonLiteral:    lipgloss.NewStyle().Foreground(palette.accent),
		helpBox:        borderedSurface.BorderForeground(palette.fgFaint),
		dialogBox:      borderedSurface.BorderForeground(palette.brand),
		dialogDanger:   borderedSurface.BorderForeground(palette.danger),
		dialogTitle:    lipgloss.NewStyle().Bold(true).Foreground(palette.fg),
		warnText:       lipgloss.NewStyle().Foreground(palette.warning),
		kindBadge:      lipgloss.NewStyle().Bold(true).Foreground(palette.fg),
		spinnerStyle:   spinnerStyle,
		selectedRow:    lipgloss.NewStyle().Bold(true).Foreground(palette.fg).Background(palette.chromeHigher),
		cursorText:     lipgloss.NewStyle().Foreground(palette.accent),
		listStyle:      newListStyles(palette, glyphs, textInputStyle, spinnerStyle),
		listItemStyle:  newListItemStyles(palette, glyphs),
		textInputStyle: textInputStyle,
	}
}

func newSemanticPalette(isDark bool) semanticPalette {
	lightDark := lipgloss.LightDark(isDark)
	return semanticPalette{
		fg:           lightDark(lipgloss.Color("#182230"), lipgloss.Color("#E7EDF4")),
		fgMuted:      lightDark(lipgloss.Color("#586574"), lipgloss.Color("#A7B3C0")),
		fgFaint:      lightDark(lipgloss.Color("#85909C"), lipgloss.Color("#6F7C89")),
		accent:       lightDark(lipgloss.Color("#6C3FC5"), lipgloss.Color("#C795FF")),
		success:      lightDark(lipgloss.Color("#237A4B"), lipgloss.Color("#65D391")),
		warning:      lightDark(lipgloss.Color("#946000"), lipgloss.Color("#F0B85B")),
		danger:       lightDark(lipgloss.Color("#B4232C"), lipgloss.Color("#FF7B84")),
		brand:        lipgloss.Color("#5F5FAF"),
		onBrand:      lightDark(lipgloss.Color("#FFFFFF"), lipgloss.Color("#F2F1FF")),
		gold:         lightDark(lipgloss.Color("#8A6D00"), lipgloss.Color("#FFD75F")),
		cyan:         lightDark(lipgloss.Color("#0B7285"), lipgloss.Color("#5FD7FF")),
		chromeHigher: lightDark(lipgloss.Color("#D9E2EC"), lipgloss.Color("#24313E")),
	}
}

func newTextInputStyles(palette semanticPalette) textinput.Styles {
	return textinput.Styles{
		Focused: textinput.StyleState{
			Text:        lipgloss.NewStyle().Foreground(palette.fg),
			Placeholder: lipgloss.NewStyle().Foreground(palette.fgFaint),
			Suggestion:  lipgloss.NewStyle().Foreground(palette.fgMuted),
			Prompt:      lipgloss.NewStyle().Foreground(palette.accent),
		},
		Blurred: textinput.StyleState{
			Text:        lipgloss.NewStyle().Foreground(palette.fgMuted),
			Placeholder: lipgloss.NewStyle().Foreground(palette.fgFaint),
			Suggestion:  lipgloss.NewStyle().Foreground(palette.fgFaint),
			Prompt:      lipgloss.NewStyle().Foreground(palette.fgMuted),
		},
		Cursor: textinput.CursorStyle{
			Color: palette.accent,
			Shape: tea.CursorBlock,
			Blink: true,
		},
	}
}

func newFilePickerStyles(st *styles, dirMode bool) filepicker.Styles {
	fileStyle := lipgloss.NewStyle().Foreground(st.palette.fg)
	if dirMode {
		fileStyle = fileStyle.Foreground(st.palette.fgFaint)
	}
	return filepicker.Styles{
		Cursor:           st.cursorText,
		DisabledCursor:   lipgloss.NewStyle().Foreground(st.palette.fgFaint),
		Selected:         lipgloss.NewStyle().Bold(true).Foreground(st.palette.fg).Background(st.palette.chromeHigher),
		DisabledSelected: lipgloss.NewStyle().Foreground(st.palette.fgMuted).Background(st.palette.chromeHigher),
		Directory:        lipgloss.NewStyle().Foreground(st.palette.fg),
		File:             fileStyle,
		DisabledFile:     lipgloss.NewStyle().Foreground(st.palette.fgFaint),
		Symlink:          lipgloss.NewStyle().Foreground(st.palette.cyan),
		FileSize:         lipgloss.NewStyle().Foreground(st.palette.fgFaint).Width(7).Align(lipgloss.Right),
		Permission:       lipgloss.NewStyle().Foreground(st.palette.fgFaint),
		EmptyDirectory:   lipgloss.NewStyle().Foreground(st.palette.fgMuted).SetString("empty directory"),
	}
}

func newListStyles(palette semanticPalette, glyphs glyphs, textInputStyle textinput.Styles, spinnerStyle lipgloss.Style) list.Styles {
	return list.Styles{
		TitleBar:                    lipgloss.NewStyle().Padding(0, 0, 0, 2),
		Title:                       lipgloss.NewStyle().Foreground(palette.fgMuted),
		Spinner:                     spinnerStyle,
		Filter:                      textInputStyle,
		DefaultFilterCharacterMatch: lipgloss.NewStyle().Underline(true),
		StatusBar:                   lipgloss.NewStyle().Foreground(palette.fgMuted).Padding(0, 0, 1, 2),
		StatusEmpty:                 lipgloss.NewStyle().Foreground(palette.fgFaint),
		StatusBarActiveFilter:       lipgloss.NewStyle().Foreground(palette.fg),
		StatusBarFilterCount:        lipgloss.NewStyle().Foreground(palette.fgFaint),
		NoItems:                     lipgloss.NewStyle().Foreground(palette.fgMuted),
		PaginationStyle:             lipgloss.NewStyle().PaddingLeft(2),
		HelpStyle:                   lipgloss.NewStyle().Foreground(palette.fgMuted).Padding(1, 0, 0, 2),
		ActivePaginationDot:         lipgloss.NewStyle().Foreground(palette.accent).SetString(glyphs.activePage + " "),
		InactivePaginationDot:       lipgloss.NewStyle().Foreground(palette.fgFaint).SetString(glyphs.inactivePage + " "),
		ArabicPagination:            lipgloss.NewStyle().Foreground(palette.fgMuted),
		DividerDot:                  lipgloss.NewStyle().Foreground(palette.fgFaint).SetString(glyphs.divider),
	}
}

func newListItemStyles(palette semanticPalette, glyphs glyphs) list.DefaultItemStyles {
	gutterWidth := lipgloss.Width(glyphs.cursorMarker + " ")
	normal := lipgloss.NewStyle().Foreground(palette.fg).PaddingLeft(gutterWidth)
	description := normal.Foreground(palette.fgMuted)
	selected := lipgloss.NewStyle().
		Bold(true).
		Foreground(palette.fg).
		Background(palette.chromeHigher)
	dimmed := lipgloss.NewStyle().Foreground(palette.fgFaint).PaddingLeft(gutterWidth)
	return list.DefaultItemStyles{
		NormalTitle:   normal,
		NormalDesc:    description,
		SelectedTitle: selected,
		SelectedDesc:  lipgloss.NewStyle().Foreground(palette.fgMuted).Background(palette.chromeHigher),
		DimmedTitle:   dimmed,
		DimmedDesc:    dimmed.Foreground(palette.fgFaint),
		FilterMatch:   lipgloss.NewStyle().Underline(true),
	}
}

const (
	rowColumnSeparator      = "  "
	minimumRowIdentityWidth = 6
)

type rowColumn struct {
	text     string
	critical bool
}

type rowAlignment struct {
	identity int
	columns  []int
}

func (s *styles) selectionGutter() string {
	return strings.Repeat(" ", lipgloss.Width(s.glyphs.cursorMarker)+1)
}

func (s *styles) rowContentWidth(width int) int {
	if width <= 0 {
		return 0
	}
	return max(0, width-lipgloss.Width(s.selectionGutter()))
}

func (s *styles) renderSelectableRow(content string, selected bool, width int) string {
	if width > 0 {
		contentWidth := s.rowContentWidth(width)
		if contentWidth == 0 {
			content = ""
		} else {
			content = truncateLine(content, contentWidth, s.glyphs.ellipsis)
		}
	}
	if selected {
		return s.renderSelectionBand(content, width)
	}
	return s.selectionGutter() + content
}

func (s *styles) renderSelectionBand(content string, width int) string {
	row := s.cursorText.Render(s.glyphs.cursorMarker) + " " + content
	if width > 0 {
		row += strings.Repeat(" ", max(0, width-lipgloss.Width(row)))
	}
	return renderStyleAroundANSI(s.selectedRow, row)
}

func renderStyleAroundANSI(style lipgloss.Style, content string) string {
	restore := ansi.NewStyle()
	if style.GetBold() {
		restore = restore.Bold()
	}
	if foreground := style.GetForeground(); foreground != nil {
		if _, noColor := foreground.(lipgloss.NoColor); !noColor {
			restore = restore.ForegroundColor(foreground)
		}
	}
	if background := style.GetBackground(); background != nil {
		if _, noColor := background.(lipgloss.NoColor); !noColor {
			restore = restore.BackgroundColor(background)
		}
	}
	if style.GetUnderline() {
		restore = restore.Underline(true)
	}
	if restoreSequence := restore.String(); restoreSequence != ansi.ResetStyle {
		content = strings.ReplaceAll(content, ansi.ResetStyle, ansi.ResetStyle+restoreSequence)
	}
	return style.Render(content)
}

func measureRowAlignment(items []list.Item) rowAlignment {
	var alignment rowAlignment
	for _, item := range items {
		columned, ok := item.(columnedListItem)
		if !ok {
			continue
		}
		identity, columns := columned.listColumns()
		alignment.identity = max(alignment.identity, lipgloss.Width(identity))
		if len(columns) > len(alignment.columns) {
			alignment.columns = append(alignment.columns, make([]int, len(columns)-len(alignment.columns))...)
		}
		for position, column := range columns {
			alignment.columns[position] = max(alignment.columns[position], lipgloss.Width(column.text))
		}
	}
	return alignment
}

func renderAlignedRowColumns(width int, identity, ellipsis string, alignment *rowAlignment, columns ...rowColumn) string {
	if alignment == nil || width <= 0 {
		return renderRowColumns(width, identity, ellipsis, columns...)
	}
	parts := []string{identity + strings.Repeat(" ", max(0, alignment.identity-lipgloss.Width(identity)))}
	for position, columnWidth := range alignment.columns {
		if columnWidth == 0 {
			continue
		}
		text := ""
		if position < len(columns) {
			text = columns[position].text
		}
		parts = append(parts, text+strings.Repeat(" ", max(0, columnWidth-lipgloss.Width(text))))
	}
	row := strings.TrimRight(strings.Join(parts, rowColumnSeparator), " ")
	if lipgloss.Width(row) > width {
		return renderRowColumns(width, identity, ellipsis, columns...)
	}
	return row
}

// renderRowColumns fits an identity and its metadata columns within width.
// An ellipsis is unused when width <= 0 because the row is returned untruncated.
func renderRowColumns(width int, identity string, ellipsis string, columns ...rowColumn) string {
	columns = slices.Clone(columns)
	columns = slices.DeleteFunc(columns, func(column rowColumn) bool { return column.text == "" })
	join := func(identity string, widths []int) string {
		parts := make([]string, 1, len(columns)+1)
		parts[0] = identity
		for i, column := range columns {
			text := column.text
			if widths != nil {
				text = truncateCriticalColumn(text, widths[i], ellipsis)
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, rowColumnSeparator)
	}
	if width <= 0 {
		return join(identity, nil)
	}
	for lipgloss.Width(join(identity, nil)) > width {
		optional := -1
		for i := len(columns) - 1; i >= 0; i-- {
			if !columns[i].critical {
				optional = i
				break
			}
		}
		if optional < 0 {
			break
		}
		columns = slices.Delete(columns, optional, optional+1)
	}
	if row := join(identity, nil); lipgloss.Width(row) <= width {
		return row
	}

	identityFloor := min(lipgloss.Width(identity), minimumRowIdentityWidth)
	separatorWidth := lipgloss.Width(rowColumnSeparator)
	for len(columns) > 0 {
		textWidth := width - separatorWidth*len(columns)
		minimums := make([]int, len(columns))
		minimumTotal := identityFloor
		for i, column := range columns {
			minimums[i] = criticalColumnMinimumWidth(column.text)
			minimumTotal += minimums[i]
		}
		if textWidth < minimumTotal {
			columns = columns[:len(columns)-1]
			continue
		}

		fullCriticalWidth := 0
		for _, column := range columns {
			fullCriticalWidth += lipgloss.Width(column.text)
		}
		criticalBudget := min(fullCriticalWidth, textWidth-identityFloor)
		criticalWidths := slices.Clone(minimums)
		remaining := criticalBudget
		for _, minimum := range minimums {
			remaining -= minimum
		}
		for remaining > 0 {
			grew := false
			for i, column := range columns {
				if criticalWidths[i] >= lipgloss.Width(column.text) {
					continue
				}
				criticalWidths[i]++
				remaining--
				grew = true
				if remaining == 0 {
					break
				}
			}
			if !grew {
				break
			}
		}

		usedCriticalWidth := 0
		for _, criticalWidth := range criticalWidths {
			usedCriticalWidth += criticalWidth
		}
		identityWidth := textWidth - usedCriticalWidth
		return join(truncateLine(identity, identityWidth, ellipsis), criticalWidths)
	}
	return truncateLine(identity, width, ellipsis)
}

func criticalColumnMinimumWidth(text string) int {
	textWidth := lipgloss.Width(text)
	if tokenWidth, ok := leadingTokenWidth(ansi.Strip(text)); ok {
		return min(textWidth, tokenWidth)
	}
	return min(textWidth, minimumRowIdentityWidth)
}

func truncateCriticalColumn(text string, width int, ellipsis string) string {
	textWidth := lipgloss.Width(text)
	if textWidth <= width {
		return text
	}
	ellipsisWidth := lipgloss.Width(ellipsis)
	plain := ansi.Strip(text)
	if tokenWidth, ok := leadingTokenWidth(plain); ok {
		if tokenWidth > width {
			return ""
		}
		if width < tokenWidth+ellipsisWidth {
			return ansi.Cut(text, 0, tokenWidth)
		}
	}
	if width <= ellipsisWidth {
		return ""
	}

	prefixWidth := width - ellipsisWidth
	for offset := 0; offset < len(plain); {
		open := strings.IndexByte(plain[offset:], '[')
		if open < 0 {
			break
		}
		open += offset
		close := strings.IndexByte(plain[open:], ']')
		if close < 0 {
			if prefixWidth > lipgloss.Width(plain[:open]) {
				prefixWidth = lipgloss.Width(plain[:open])
			}
			break
		}
		close += open
		tokenStart := lipgloss.Width(plain[:open])
		tokenEnd := lipgloss.Width(plain[:close+1])
		if prefixWidth >= tokenStart && prefixWidth < tokenEnd {
			if tokenStart == 0 && tokenEnd <= width {
				return ansi.Cut(text, 0, tokenEnd)
			}
			prefixWidth = tokenStart
			break
		}
		offset = close + 1
	}
	if prefixWidth == 0 {
		return ""
	}
	return ansi.Cut(text, 0, prefixWidth) + ellipsis
}

func leadingTokenWidth(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	var closing byte
	switch text[0] {
	case '[':
		closing = ']'
	case '(':
		closing = ')'
	default:
		return 0, false
	}
	end := strings.IndexByte(text, closing)
	if end < 0 {
		return 0, false
	}
	return lipgloss.Width(text[:end+1]), true
}

func (s *styles) resourceBadge(kind string) string {
	if kind == "Secret" {
		return s.glyphs.secretBadge
	}
	return s.glyphs.configMapBadge
}

func resourceSubject(kind, namespace, name string) string {
	return kind + " " + namespace + "/" + name
}

func serverOrUnverified(server string) string {
	if server == "" {
		return "unverified"
	}
	return redactServerUserinfo(server)
}

// redactServerUserinfo removes URL userinfo before a server address reaches
// any rendered surface: kubeconfig accepts credential-bearing server URLs and
// client-go preserves them, so display paths must never echo them back.
// Connection, identity-comparison, and persistence paths keep the raw value.
func redactServerUserinfo(server string) string {
	if parsed, err := url.Parse(server); err == nil {
		if parsed.User == nil {
			return server
		}
		parsed.User = nil
		return parsed.String()
	}
	prefix, rest := "", server
	if scheme, tail, ok := strings.Cut(server, "://"); ok {
		prefix, rest = scheme+"://", tail
	}
	authority := rest
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		authority = rest[:slash]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		return prefix + authority[at+1:] + rest[len(authority):]
	}
	return server
}

func renderSubjectLine(subject string, width int, st *styles) string {
	return st.dim.Render(truncateLine(subject, width, st.glyphs.ellipsis))
}

// truncateLine shortens text to width columns, marking the cut with the mode's ellipsis glyph.
// width <= 0 returns text unchanged.
func truncateLine(text string, width int, ellipsis string) string {
	if width <= 0 {
		return text
	}
	return ansi.Truncate(text, width, ellipsis)
}

func middleElideLine(text string, width int, ellipsis string) string {
	textWidth := lipgloss.Width(text)
	if width <= 0 || textWidth <= width {
		return text
	}
	ellipsisWidth := lipgloss.Width(ellipsis)
	if width <= ellipsisWidth {
		return ansi.Cut(ellipsis, 0, width)
	}
	remaining := width - ellipsisWidth
	leftWidth := (remaining + 1) / 2
	rightWidth := remaining - leftWidth
	return ansi.Cut(text, 0, leftWidth) + ellipsis + ansi.Cut(text, textWidth-rightWidth, textWidth)
}

// plural renders a count with its noun, adding a trailing s for every count
// other than one.
func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// DetectASCII reports whether the terminal or locale declares that it cannot
// display UTF-8. TERM is checked first: an unset or non-UTF-8-capable terminal
// is decisive regardless of locale. A locale that names a non-UTF-8 charset
// also forces ASCII, but a machine with no locale variables at all keeps
// Unicode: every modern terminal renders UTF-8, and the -ascii flag remains
// the escape hatch.
func DetectASCII(getenv func(string) string) bool {
	term := strings.ToLower(getenv("TERM"))
	if term == "" || term == "dumb" || term == "linux" || legacyVT(term) || strings.HasPrefix(term, "ansi") {
		return true
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		value := strings.ToLower(getenv(name))
		if value == "" {
			continue
		}
		return !strings.Contains(value, "utf-8") && !strings.Contains(value, "utf8")
	}
	return false
}

func legacyVT(term string) bool {
	rest, ok := strings.CutPrefix(term, "vt")
	if !ok || rest == "" {
		return false
	}
	return strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' }) == -1
}
