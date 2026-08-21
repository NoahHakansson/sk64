package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	borderedSurfaceMargin = 4
	dialogPreferredWidth  = 72
	dialogWidthPercent    = 70
	dialogMaxWidth        = 96
)

// responsiveBoxWidths returns the outer border-inclusive width and usable inner width.
func responsiveBoxWidths(availableWidth, preferredWidth, widthPercent, widthCap, frameSize int) (outerWidth, innerWidth int) {
	availableOuterWidth := max(0, availableWidth-borderedSurfaceMargin)
	responsiveWidth := max(preferredWidth, availableWidth*widthPercent/100)
	outerWidth = min(availableOuterWidth, min(responsiveWidth, widthCap))
	innerWidth = max(0, outerWidth-frameSize)
	return outerWidth, innerWidth
}

// pressYToConfirm is shown when a lowercase accept key is pressed on a
// confirmation. Uppercase is deliberate: a stray keystroke must not commit.
const pressYToConfirm = "press Y (shift+y) to confirm"

// dialog lays out a centered, bordered modal panel inside the body rectangle a
// screen was given. Screens embed it, forward SetSize to resize, and render
// through render.
type dialog struct {
	styles        *styles
	danger        bool
	width, height int
}

// dialogContent is one panel's text in fixed visual order: title, identity,
// summary, body, warnings, prompt, message. Empty fields are dropped along with
// the blank line that would separate them. Under height pressure, summary,
// prompt controls, one feedback row and critical warnings are reserved while
// optional body entries and regular warnings yield first. Prompt, critical
// warnings and identity compact before feedback is dropped.
type dialogContent struct {
	title            string
	identity         []string
	summary          string
	body             []string
	criticalWarnings []string
	warnings         []string
	prompt           string
	message          string
	isError          bool
	isWarning        bool
}

func newDialog(st *styles, danger bool) dialog {
	return dialog{styles: st, danger: danger}
}

func (d *dialog) resize(width, height int) {
	d.width, d.height = width, height
}

func (d *dialog) widths() (outerWidth, innerWidth int) {
	return responsiveBoxWidths(
		d.width,
		dialogPreferredWidth,
		dialogWidthPercent,
		dialogMaxWidth,
		d.styles.dialogBox.GetHorizontalFrameSize(),
	)
}

// boxWidth is the panel's total width, border included.
func (d *dialog) boxWidth() int {
	outerWidth, _ := d.widths()
	return outerWidth
}

// contentWidth is the usable text width inside the border and padding. Screens
// size their text inputs and viewports against it.
func (d *dialog) contentWidth() int {
	_, innerWidth := d.widths()
	return innerWidth
}

// scrollHeight is the number of rows a scrolling region inside the panel may
// use, given the rows the panel's other sections occupy.
func (d *dialog) scrollHeight(chrome int) int {
	return max(1, d.height-d.styles.dialogBox.GetVerticalFrameSize()-chrome)
}

func (d *dialog) render(c dialogContent) string {
	if d.width <= 0 || d.height <= 0 {
		lines := make([]string, 0, 3+len(c.identity)+len(c.body)+len(c.criticalWarnings)+len(c.warnings))
		if c.title != "" {
			lines = append(lines, c.title)
		}
		lines = append(lines, c.identity...)
		if c.summary != "" {
			lines = append(lines, c.summary)
		}
		lines = append(lines, c.body...)
		lines = append(lines, c.criticalWarnings...)
		lines = append(lines, c.warnings...)
		if c.prompt != "" {
			lines = append(lines, c.prompt)
		}
		if c.message != "" {
			lines = append(lines, c.message)
		}
		return strings.Join(lines, "\n")
	}

	box := d.styles.dialogBox
	if d.danger {
		box = d.styles.dialogDanger
	}
	inner := d.contentWidth()
	budget := max(1, d.height-box.GetVerticalFrameSize())

	var title []string
	if c.title != "" {
		for _, line := range wrapDialogLines(c.title, inner) {
			title = append(title, d.styles.dialogTitle.Render(line))
		}
	}
	if len(title) > 0 && inner > 0 {
		title = append(title, d.styles.dim.Render(strings.Repeat(d.styles.glyphs.ruleMarker, inner)))
	}
	identity := make([]string, 0, len(c.identity))
	for _, line := range c.identity {
		identity = append(identity, wrapDialogLines(line, inner)...)
	}
	var summary []string
	if c.summary != "" {
		summary = wrapDialogLines(c.summary, inner)
	}
	body := make([][]string, 0, len(c.body))
	for _, line := range c.body {
		body = append(body, wrapDialogLines(line, inner))
	}
	criticalWarnings := make([]string, 0, len(c.criticalWarnings))
	for _, warning := range c.criticalWarnings {
		for _, line := range warningLines(warning, d.styles.glyphs.warnMarker, inner) {
			criticalWarnings = append(criticalWarnings, d.styles.warnText.Render(line))
		}
	}
	warnings := make([]string, 0, len(c.warnings))
	for _, warning := range c.warnings {
		for _, line := range warningLines(warning, d.styles.glyphs.warnMarker, inner) {
			warnings = append(warnings, d.styles.warnText.Render(line))
		}
	}
	var prompt []string
	if c.prompt != "" {
		prompt = wrapDialogLines(c.prompt, inner)
	}
	var message []string
	if c.message != "" {
		message = wrapDialogLines(c.message, inner)
		if c.isError {
			for i := range message {
				message[i] = d.styles.errText.Render(message[i])
			}
		} else if c.isWarning {
			for i := range message {
				message[i] = d.styles.warnText.Render(message[i])
			}
		}
	}

	compactSpacing := false
	groups := func() [][]string {
		return [][]string{title, identity, summary, flattenDialogLines(body), criticalWarnings, warnings, prompt, message}
	}
	totalRows := func() int {
		rows := 0
		groupCount := 0
		for _, group := range groups() {
			if len(group) == 0 {
				continue
			}
			rows += len(group)
			groupCount++
		}
		if !compactSpacing {
			rows += max(0, groupCount-1)
		}
		return rows
	}
	// Dropped body entries get a counted cue: an entry the user cannot reach
	// (there are no scroll keys here) must not vanish without trace. The cue's
	// own row is reserved as soon as anything is dropped.
	dropped := 0
	for totalRows()+min(dropped, 1) > budget && len(body) > 0 {
		body = body[:len(body)-1]
		dropped++
	}
	if dropped > 0 {
		body = append(body, []string{d.styles.dim.Render(fmt.Sprintf("...and %d more", dropped))})
	}
	warningEllipsis := d.styles.warnText.Render("...")
	for totalRows() > budget && len(warnings) > 1 {
		warnings = warnings[:len(warnings)-1]
		warnings[len(warnings)-1] = warningEllipsis
	}
	if totalRows() > budget && len(warnings) == 1 {
		warnings = nil
	}
	for totalRows() > budget && len(title) > 1 {
		title = title[:len(title)-1]
	}
	if totalRows() > budget && len(title) > 0 {
		title = nil
	}
	if totalRows() > budget {
		compactSpacing = true
	}
	for totalRows() > budget && len(message) > 1 {
		message = message[:len(message)-1]
	}
	if totalRows() > budget && dropped > 0 {
		body = nil
	}
	if totalRows() > budget && len(prompt) > 0 {
		promptLines := strings.Split(c.prompt, "\n")
		prompt = compactDialogRows(promptLines, inner, len(promptLines), d.styles.glyphs.ellipsis, d.styles.glyphs.separator)
	}
	if totalRows() > budget {
		criticalWarnings = compactDialogWarnings(c.criticalWarnings, d.styles.glyphs.warnMarker, inner, len(c.criticalWarnings), d.styles.glyphs.ellipsis, d.styles.glyphs.separator, d.styles.warnText)
	}
	if totalRows() > budget {
		identity = compactDialogRows(c.identity, inner, len(c.identity), d.styles.glyphs.ellipsis, d.styles.glyphs.separator)
	}
	if totalRows() > budget {
		message = nil
	}
	if totalRows() > budget && len(identity) > 0 {
		identityBudget := max(1, budget-(totalRows()-len(identity)))
		identity = compactDialogRows(c.identity, inner, identityBudget, d.styles.glyphs.ellipsis, d.styles.glyphs.separator)
	}
	if totalRows() > budget && len(criticalWarnings) > 0 {
		warningBudget := max(1, budget-(totalRows()-len(criticalWarnings)))
		criticalWarnings = compactDialogWarnings(c.criticalWarnings, d.styles.glyphs.warnMarker, inner, warningBudget, d.styles.glyphs.ellipsis, d.styles.glyphs.separator, d.styles.warnText)
	}
	if totalRows() > budget && len(prompt) > 0 {
		promptBudget := max(1, budget-(totalRows()-len(prompt)))
		prompt = compactDialogRows(strings.Split(c.prompt, "\n"), inner, promptBudget, d.styles.glyphs.ellipsis, d.styles.glyphs.separator)
	}

	lines := make([]string, 0, min(totalRows(), budget))
	for _, group := range groups() {
		if len(group) == 0 {
			continue
		}
		if len(lines) > 0 && !compactSpacing {
			lines = append(lines, "")
		}
		lines = append(lines, group...)
	}
	if len(lines) > budget {
		lines = lines[len(lines)-budget:]
	}

	return lipgloss.Place(
		d.width,
		d.height,
		lipgloss.Center,
		lipgloss.Center,
		box.Width(d.boxWidth()).Render(strings.Join(lines, "\n")),
	)
}

func flattenDialogLines(groups [][]string) []string {
	var lines []string
	for _, group := range groups {
		lines = append(lines, group...)
	}
	return lines
}

func compactDialogRows(lines []string, width, maxRows int, ellipsis, separator string) []string {
	var logicalLines []string
	for _, line := range lines {
		logicalLines = append(logicalLines, strings.Split(line, "\n")...)
	}
	if len(logicalLines) == 0 || maxRows <= 0 {
		return nil
	}
	maxRows = min(maxRows, len(logicalLines))
	compacted := make([]string, 0, maxRows)
	for row := range maxRows {
		start := row * len(logicalLines) / maxRows
		end := (row + 1) * len(logicalLines) / maxRows
		compacted = append(compacted, compactDialogRow(logicalLines[start:end], width, ellipsis, separator))
	}
	return compacted
}

func compactDialogRow(parts []string, width int, ellipsis, separator string) string {
	separatorWidth := lipgloss.Width(separator) * (len(parts) - 1)
	contentWidth := width - separatorWidth
	if len(parts) == 1 || contentWidth < len(parts) {
		return middleElideLine(strings.Join(parts, separator), width, ellipsis)
	}
	compacted := make([]string, len(parts))
	for i, part := range parts {
		partWidth := contentWidth / len(parts)
		if i < contentWidth%len(parts) {
			partWidth++
		}
		compacted[i] = middleElideLine(part, partWidth, ellipsis)
	}
	return strings.Join(compacted, separator)
}

func compactDialogWarnings(warnings []string, marker string, width, maxRows int, ellipsis, separator string, style lipgloss.Style) []string {
	prefixed := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		prefixed = append(prefixed, marker+" "+strings.Join(strings.Split(warning, "\n"), " "))
	}
	compacted := compactDialogRows(prefixed, width, maxRows, ellipsis, separator)
	for i := range compacted {
		compacted[i] = style.Render(compacted[i])
	}
	return compacted
}

func commitIdentityLines(operation, kind, namespace, name, contextName, server string, width int, separator string, details ...string) []string {
	subject := kind
	switch {
	case namespace != "" && name != "":
		subject += " " + namespace + "/" + name
	case namespace != "" && kind != "":
		subject += " " + namespace + "/<name>"
	case namespace != "":
		subject = "namespace " + namespace
	}
	if operation != "" {
		subject = operation + " " + subject
	}
	lines := []string{strings.TrimSpace(subject)}
	for _, detail := range details {
		candidate := lines[0] + separator + detail
		if width <= 0 || lipgloss.Width(candidate) <= width {
			lines[0] = candidate
		} else {
			lines = append(lines, detail)
		}
	}
	return append(lines, clusterIdentityLines(contextName, server, width, separator)...)
}

func clusterIdentityLines(contextName, server string, width int, separator string) []string {
	if contextName == "" {
		contextName = "unknown"
	}
	if server == "" {
		server = "unknown"
	}
	server = redactServerUserinfo(server)
	contextLine := "context " + contextName
	serverPrefix := "server "
	serverLine := serverPrefix + server
	combined := contextLine + separator + serverLine
	if width <= 0 || lipgloss.Width(combined) <= width {
		return []string{combined}
	}

	lines := []string{contextLine}
	serverWidth := lipgloss.Width(server)
	start := 0
	lineWidth := max(1, width-lipgloss.Width(serverPrefix))
	for start < serverWidth {
		end := min(serverWidth, start+lineWidth)
		chunk := ansi.Cut(server, start, end)
		if len(lines) == 1 {
			chunk = serverPrefix + chunk
		}
		lines = append(lines, chunk)
		start = end
		lineWidth = max(1, width)
	}
	if serverWidth == 0 {
		lines = append(lines, serverLine)
	}
	return lines
}

func textInputWidth(contentWidth int, prompt string) int {
	return max(0, contentWidth-lipgloss.Width(prompt)-1)
}

// wrapDialogLines word-wraps text to width columns, hard-breaking words only
// when necessary and preserving existing line breaks. width <= 0 returns the
// text split on its own newlines.
func wrapDialogLines(text string, width int) []string {
	if width <= 0 {
		return strings.Split(text, "\n")
	}
	return strings.Split(lipgloss.NewStyle().Width(width).Render(text), "\n")
}

// warningLines renders one warning with the style's marker on the first line
// and a two-column hanging indent on continuations.
func warningLines(text string, marker string, width int) []string {
	firstPrefix := marker + " "
	continuationPrefix := strings.Repeat(" ", lipgloss.Width(firstPrefix))
	lines := wrapDialogLines(text, max(1, width-lipgloss.Width(firstPrefix)))
	for i := range lines {
		prefix := continuationPrefix
		if i == 0 {
			prefix = firstPrefix
		}
		lines[i] = prefix + lines[i]
	}
	return lines
}
