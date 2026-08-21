package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/config"
)

type configErrorModel struct {
	dialog
	path  string
	errs  config.Errors
	width int
	quit  quitArm
}

func newConfigErrorModel(path string, errs config.Errors, ascii bool) configErrorModel {
	glyphs := newGlyphs(ascii)
	st := newStyles(true, glyphs)
	return configErrorModel{
		dialog: newDialog(st, true),
		path:   path,
		errs:   errs,
		quit:   newQuitArm(),
	}
}

func (m configErrorModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m configErrorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.styles = newStyles(msg.IsDark(), m.styles.glyphs)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.resize(msg.Width, bodyHeight(msg.Height))
	case quitArmExpiredMsg:
		m.quit.expire(msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			if m.quit.armed {
				return m, tea.Quit
			}
			return m, m.quit.arm("press ctrl+c again to quit")
		}
		m.quit.disarm()
		switch msg.String() {
		case "esc", "Q":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m configErrorModel) View() tea.View {
	body := m.render(m.dialogContent())
	footer := renderFooterBar(m.styles, []hintGroup{{key: "esc", desc: "quit", protected: true}}, m.width)
	if m.quit.armed {
		footer = renderBar(m.styles.warnText, m.width, m.quit.message)
	}
	return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, body, footer))
}

func (m configErrorModel) dialogContent() dialogContent {
	body := make([]string, 0, len(m.errs))
	for _, configErr := range m.errs {
		firstLine := fmt.Sprintf("%s line %d", m.styles.glyphs.errMarker, configErr.Line)
		if configErr.Text != "" {
			firstLine += m.styles.glyphs.separator + fmt.Sprintf("%q", configErr.Text)
		}
		lines := []string{m.styles.errText.Render(firstLine), "  " + m.styles.dim.Render(configErr.Msg)}
		if configErr.Hint != "" {
			lines = append(lines, "  "+m.styles.dim.Render("fix: "+configErr.Hint))
		}
		body = append(body, strings.Join(lines, "\n"))
	}
	return dialogContent{
		title:    "Config error",
		identity: []string{m.styles.dim.Render(m.path)},
		body:     body,
		prompt:   "fix the file and start sk64 again",
	}
}

// RunConfigError shows validation failures before the main TUI starts.
func RunConfigError(ctx context.Context, path string, errs config.Errors, ascii bool) error {
	_, err := tea.NewProgram(newConfigErrorModel(path, errs, ascii), tea.WithContext(ctx)).Run()
	if err != nil {
		return normalizeRunError(ctx, err)
	}
	return nil
}
