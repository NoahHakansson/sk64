package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
)

func TestSharedTextInputViewsFitAssignedWidths(t *testing.T) {
	type namedInput struct {
		name  string
		model *textinput.Model
	}
	type inputSurface struct {
		width  int
		dialog bool
		inputs []namedInput
	}

	surfaces := []struct {
		name  string
		build func(*testing.T, *styles) inputSurface
	}{
		{
			name: "create",
			build: func(t *testing.T, st *styles) inputSurface {
				prompt := newCreatePrompt(t.Context(), testClient(), editEnv{noConfigMaps: true}, "default", nil, st)
				prompt.SetSize(minimumWidth, bodyHeight(minimumHeight))
				return inputSurface{width: prompt.contentWidth(), dialog: true, inputs: []namedInput{{name: "name", model: &prompt.input}}}
			},
		},
		{
			name: "delete",
			build: func(t *testing.T, st *styles) inputSurface {
				prompt := newDeleteConfirm(t.Context(), testClient(), k8s.KindConfigMap, "default", "settings", st)
				prompt.SetSize(minimumWidth, bodyHeight(minimumHeight))
				return inputSurface{width: prompt.contentWidth(), dialog: true, inputs: []namedInput{{name: "confirmation", model: &prompt.input}}}
			},
		},
		{
			name: "key",
			build: func(t *testing.T, st *styles) inputSurface {
				prompt := newKeyNamePrompt(t.Context(), testClient(), editEnv{}, k8s.NewEmptyConfigMap("default", "settings"), st)
				prompt.SetSize(minimumWidth, bodyHeight(minimumHeight))
				return inputSurface{width: prompt.contentWidth(), dialog: true, inputs: []namedInput{{name: "name", model: &prompt.input}}}
			},
		},
		{
			name: "file export",
			build: func(t *testing.T, st *styles) inputSurface {
				prompt := newFilePrompt(t.Context(), testClient(), editEnv{}, k8s.NewEmptyConfigMap("default", "settings"), "entry", fileExport, st)
				prompt.phase = filePhaseName
				prompt.dir = "/exports"
				prompt.SetSize(minimumWidth, bodyHeight(minimumHeight))
				return inputSurface{width: prompt.contentWidth(), dialog: true, inputs: []namedInput{{name: "name", model: &prompt.input}}}
			},
		},
		{
			name: "project form",
			build: func(t *testing.T, st *styles) inputSurface {
				form := newProjectFormScreen(t.Context(), nil, "", scanConfig{}, formCreate, nil, store.ProjectMeta{}, nil, packageDefaultKeyMaps, st)
				form.SetSize(minimumWidth, bodyHeight(minimumHeight))
				inputs := []namedInput{
					{name: "name", model: &form.nameInput},
					{name: "path", model: &form.pathInput},
					{name: "namespaces", model: &form.namespacesInput},
				}
				return inputSurface{width: form.contentWidth(), dialog: true, inputs: inputs}
			},
		},
		{
			name: "search",
			build: func(t *testing.T, st *styles) inputSurface {
				screen := newSearchScreen(t.Context(), testClient(), editEnv{}, st)
				screen.SetSize(minimumWidth, bodyHeight(minimumHeight))
				return inputSurface{width: minimumWidth, inputs: []namedInput{{name: "query", model: &screen.input}}}
			},
		},
	}

	values := []struct {
		name    string
		value   func(int) string
		focused bool
	}{
		{name: "empty focused", value: func(int) string { return "" }, focused: true},
		{name: "empty blurred", value: func(int) string { return "" }},
		{name: "partial ASCII focused", value: func(int) string { return "alpha" }, focused: true},
		{name: "partial ASCII blurred", value: func(int) string { return "alpha" }},
		{name: "full ASCII focused", value: func(width int) string { return strings.Repeat("x", width+4) }, focused: true},
		{name: "full ASCII blurred", value: func(width int) string { return strings.Repeat("x", width+4) }},
		{name: "partial Unicode focused", value: func(int) string { return "界é" }, focused: true},
		{name: "partial Unicode blurred", value: func(int) string { return "界é" }},
		{name: "full Unicode focused", value: func(width int) string { return strings.Repeat("界", width) }, focused: true},
		{name: "full Unicode blurred", value: func(width int) string { return strings.Repeat("界", width) }},
	}

	for _, glyphMode := range []struct {
		name  string
		ascii bool
	}{
		{name: "ASCII", ascii: true},
		{name: "Unicode"},
	} {
		t.Run(glyphMode.name, func(t *testing.T) {
			for _, surfaceTest := range surfaces {
				t.Run(surfaceTest.name, func(t *testing.T) {
					surface := surfaceTest.build(t, testStyles(glyphMode.ascii))
					for _, input := range surface.inputs {
						t.Run(input.name, func(t *testing.T) {
							for _, cursorMode := range []struct {
								name    string
								virtual bool
							}{
								{name: "virtual cursor", virtual: true},
								{name: "real cursor"},
							} {
								t.Run(cursorMode.name, func(t *testing.T) {
									input.model.SetVirtualCursor(cursorMode.virtual)
									for _, valueTest := range values {
										t.Run(valueTest.name, func(t *testing.T) {
											input.model.SetValue(valueTest.value(surface.width))
											input.model.CursorEnd()
											if valueTest.focused {
												_ = input.model.Focus()
											} else {
												input.model.Blur()
											}

											view := input.model.View()
											if strings.Contains(view, "\n") {
												t.Fatalf("input view contains a newline: %q", view)
											}
											if got := lipgloss.Width(view); got > surface.width {
												t.Fatalf("input view width = %d, want <= %d: %q", got, surface.width, view)
											}
											if surface.dialog && len(wrapDialogLines(view, surface.width)) != 1 {
												t.Fatalf("minimum-size dialog input wrapped: %q", view)
											}
										})
									}
								})
							}
						})
					}
				})
			}
		})
	}
}
