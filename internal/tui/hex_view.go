package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
)

type hexScreen struct {
	resource     k8s.Resource
	key          string
	styles       *styles
	viewport     viewport.Model
	err          error
	value        []byte
	movementHelp string
	width        int
}

func styledHexLines(value []byte, st *styles) []string {
	lines := editor.HexDump(value)
	faint := lipgloss.NewStyle().Foreground(st.palette.fgFaint)
	for lineIndex, line := range lines {
		lineValue := value[lineIndex*16 : min(len(value), (lineIndex+1)*16)]
		var styled strings.Builder
		styled.WriteString(faint.Render(line[:8]))
		cursor := 8
		for byteIndex, sourceByte := range lineValue {
			pairStart := 10 + byteIndex*3
			if byteIndex >= 8 {
				pairStart++
			}
			styled.WriteString(line[cursor:pairStart])
			pair := line[pairStart : pairStart+2]
			if sourceByte >= 0x20 && sourceByte <= 0x7e {
				styled.WriteString(pair)
			} else {
				styled.WriteString(faint.Render(pair))
			}
			cursor = pairStart + 2
		}
		asciiStart := strings.Index(line[cursor:], "|") + cursor
		styled.WriteString(line[cursor:asciiStart])
		styled.WriteString(faint.Render("|"))
		for start := 0; start < len(lineValue); {
			printable := lineValue[start] >= 0x20 && lineValue[start] <= 0x7e
			end := start + 1
			for end < len(lineValue) && (lineValue[end] >= 0x20 && lineValue[end] <= 0x7e) == printable {
				end++
			}
			run := line[asciiStart+1+start : asciiStart+1+end]
			if printable {
				styled.WriteString(st.jsonKey.Render(run))
			} else {
				styled.WriteString(faint.Render(run))
			}
			start = end
		}
		styled.WriteString(faint.Render("|"))
		lines[lineIndex] = styled.String()
	}
	return lines
}

func newHexScreen(resource k8s.Resource, key string, env editEnv, st *styles) *hexScreen {
	model := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	model.KeyMap = env.keymaps().viewport
	value, err := resource.Get(key)
	if err == nil {
		lines := styledHexLines(value, st)
		if len(lines) == 0 {
			model.SetContent("(empty value)")
		} else {
			model.SetContentLines(lines)
		}
	}
	return &hexScreen{resource: resource, key: key, styles: st, viewport: model, err: err, value: value, movementHelp: env.keymaps().viewportMovementHelp}
}

func (s *hexScreen) Init() tea.Cmd { return nil }

func (s *hexScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	var cmd tea.Cmd
	s.viewport, cmd = s.viewport.Update(msg)
	return s, cmd
}

func (s *hexScreen) View() string {
	parts := []string{renderSubjectLine(resourceSubject(s.resource.Kind(), s.resource.Namespace(), s.resource.Name())+" / "+s.key+" (hex)", s.width, s.styles)}
	if s.err != nil {
		parts = append(parts, renderStateLine(s.styles, stateLineError, fmt.Sprintf("value unavailable: %v", s.err), "", s.width))
		return strings.Join(parts, "\n")
	}
	parts = append(parts, s.viewport.View())
	return strings.Join(parts, "\n")
}

func (s *hexScreen) SetSize(width, height int) {
	s.width = width
	s.viewport.SetWidth(width)
	s.viewport.SetHeight(max(0, height-1))
}

func (s *hexScreen) SetStyles(st *styles) {
	s.styles = st
	if len(s.value) > 0 {
		s.viewport.SetContentLines(styledHexLines(s.value, st))
	}
}

func (s *hexScreen) Title() string { return s.resource.Name() + "/" + s.key + " (hex)" }
func (s *hexScreen) Hints() footerHints {
	return browsingHints(displayHint(s.movementHelp, "scroll"))
}
func (s *hexScreen) Help() helpGroup {
	return helpGroup{title: "viewer", entries: []helpGroupEntry{
		{binding: displayHint(s.movementHelp, "scroll"), desc: "scroll"},
	}}
}
func (s *hexScreen) CapturesInput() bool { return false }
func (s *hexScreen) WantsEsc() bool      { return false }
