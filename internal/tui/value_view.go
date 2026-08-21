package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
)

type valueScreen struct {
	resource     k8s.Resource
	key          string
	styles       *styles
	viewport     viewport.Model
	display      string
	err          error
	jsonPretty   bool
	movementHelp string
	width        int
}

func newValueScreen(resource k8s.Resource, key string, env editEnv, st *styles) *valueScreen {
	model := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	model.KeyMap = env.keymaps().viewport
	value, err := resource.Get(key)
	jsonPretty := false
	display := ""
	if err == nil {
		if pretty, ok := prettyJSON(value); ok {
			display = colorizeJSON(pretty, st)
			jsonPretty = true
		} else {
			display = string(value)
		}
		model.SetContent(display)
	}
	return &valueScreen{
		resource:     resource,
		key:          key,
		styles:       st,
		viewport:     model,
		display:      display,
		err:          err,
		jsonPretty:   jsonPretty,
		movementHelp: env.keymaps().viewportMovementHelp,
	}
}

func (s *valueScreen) Init() tea.Cmd { return nil }

func (s *valueScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	var cmd tea.Cmd
	s.viewport, cmd = s.viewport.Update(msg)
	return s, cmd
}

func (s *valueScreen) View() string {
	parts := []string{renderSubjectLine(resourceSubject(s.resource.Kind(), s.resource.Namespace(), s.resource.Name())+" / "+s.key, s.width, s.styles)}
	if s.err != nil {
		parts = append(parts, renderStateLine(s.styles, stateLineError, fmt.Sprintf("value unavailable: %v", s.err), "", s.width))
		return strings.Join(parts, "\n")
	}
	if s.jsonPretty {
		banner := s.styles.dim.Render("json - pretty-printed for display; the stored value is unchanged")
		parts = append(parts, truncateLine(banner, s.width, s.styles.glyphs.ellipsis))
	}
	parts = append(parts, s.viewport.View())
	return strings.Join(parts, "\n")
}

func (s *valueScreen) SetSize(width, height int) {
	if width != s.width {
		s.viewport.SetContent(ansi.Hardwrap(s.display, width, true))
	}
	s.width = width
	s.viewport.SetWidth(width)
	height--
	if s.jsonPretty {
		height--
	}
	s.viewport.SetHeight(max(0, height))
}

func (s *valueScreen) SetStyles(st *styles) {
	s.styles = st
	if !s.jsonPretty {
		return
	}
	s.display = colorizeJSON(ansi.Strip(s.display), st)
	if s.width > 0 {
		s.viewport.SetContent(ansi.Hardwrap(s.display, s.width, true))
	} else {
		s.viewport.SetContent(s.display)
	}
}

func (s *valueScreen) Title() string { return s.resource.Name() + "/" + s.key }
func (s *valueScreen) Hints() footerHints {
	return browsingHints(displayHint(s.movementHelp, "scroll"))
}
func (s *valueScreen) Help() helpGroup {
	return helpGroup{title: "viewer", entries: []helpGroupEntry{
		{binding: displayHint(s.movementHelp, "scroll"), desc: "scroll"},
	}}
}
func (s *valueScreen) CapturesInput() bool { return false }
func (s *valueScreen) WantsEsc() bool      { return false }
