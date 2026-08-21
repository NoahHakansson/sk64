package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
)

type projectFormMode int

const (
	formCreate projectFormMode = iota
	formEdit
)

type projectFormState int

const (
	projectFormChooseStart projectFormState = iota
	projectFormScanning
	projectFormScanError
	projectFormFields
	projectFormContextsLoading
	projectFormContexts
	projectFormContextsError
	projectFormResolving
	projectFormConfirming
	projectFormSaving
)

type projectFormFocus int

const (
	projectFormName projectFormFocus = iota
	projectFormPath
	projectFormContext
	projectFormNamespaces
	projectFormFieldCount
)

type projectFormScanMsg struct {
	reqID       int
	result      project.ScanResult
	contexts    []k8s.ContextInfo
	contextsErr error
	err         error
}

type projectFormContextsMsg struct {
	reqID    int
	contexts []k8s.ContextInfo
	err      error
}

type projectFormIdentityMsg struct {
	reqID int
	info  k8s.ContextInfo
	err   error
}

type formResultMsg struct {
	reqID   int
	project store.Project
	err     error
}

type projectFormScreen struct {
	loader
	dialog
	ctx                    context.Context
	store                  *store.Store
	keys                   *keyMaps
	km                     projectFormKeyMap
	mode                   projectFormMode
	existing               *store.Project
	kubeconfig             string
	scanRoot               string
	scanCfg                scanConfig
	originalKubeContext    string
	originalKubeServer     string
	selectedKubeContext    string
	selectedKubeServer     string
	switchPromptSuppressed bool
	nameInput              textinput.Model
	pathInput              textinput.Model
	namespacesInput        textinput.Model
	focus                  projectFormFocus
	state                  projectFormState
	startChoice            int
	contextRequired        bool
	scanIncomplete         bool
	contextList            list.Model
	message                string
	messageIsError         bool
	scanErr                error
	contextsErr            error
	gate                   confirmGate
}

func newProjectFormScreen(ctx context.Context, st *store.Store, kubeconfig string, scanCfg scanConfig, mode projectFormMode, existing *store.Project, initial store.ProjectMeta, extraNS []string, keys *keyMaps, styles *styles) *projectFormScreen {
	nameInput := newTextInput(styles)
	nameInput.Prompt = "name: "
	nameInput.SetValue(strings.TrimSpace(initial.Name))
	pathInput := newTextInput(styles)
	pathInput.Prompt = "path: "
	pathInput.SetValue(strings.TrimSpace(initial.RootPath))
	namespacesInput := newTextInput(styles)
	namespacesInput.Prompt = "namespaces: "
	namespacesInput.SetValue(strings.TrimSpace(strings.Join(append([]string{initial.Namespace}, extraNS...), ", ")))

	originalKubeContext := initial.KubeContext
	originalKubeServer := initial.KubeServer
	switchPromptSuppressed := initial.SwitchPromptSuppressed
	if existing != nil {
		originalKubeContext = existing.KubeContext
		originalKubeServer = existing.KubeServer
		switchPromptSuppressed = existing.SwitchPromptSuppressed
	}
	scanRoot := strings.TrimSpace(initial.RootPath)
	state := projectFormFields
	startChoice := 0
	if mode == formCreate {
		state = projectFormChooseStart
		if scanRoot == "" {
			startChoice = 1
		}
	}
	contextList := newListModel(styles, keys.list)
	applyDetailedListStyles(&contextList, styles)
	return &projectFormScreen{
		dialog: newDialog(styles, false), ctx: ctx, store: st, keys: keys, km: keys.projectForm, mode: mode, existing: existing,
		kubeconfig: kubeconfig, scanRoot: scanRoot, scanCfg: scanCfg,
		originalKubeContext: originalKubeContext, originalKubeServer: originalKubeServer,
		selectedKubeContext: initial.KubeContext, selectedKubeServer: initial.KubeServer,
		switchPromptSuppressed: switchPromptSuppressed, nameInput: nameInput, pathInput: pathInput,
		namespacesInput: namespacesInput, state: state, startChoice: startChoice, contextList: contextList, gate: newConfirmGate(styles),
	}
}

func (s *projectFormScreen) Init() tea.Cmd {
	if s.state == projectFormFields {
		return s.focusInput()
	}
	return nil
}

func (s *projectFormScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case projectFormScanMsg:
		if s.state != projectFormScanning || !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			s.scanErr = msg.err
			s.state = projectFormScanError
			return s, nil
		}
		s.applyScan(msg.result, msg.contexts, msg.contextsErr)
		s.state = projectFormFields
		s.focus = projectFormName
		return s, s.focusInput()
	case projectFormContextsMsg:
		if s.state != projectFormContextsLoading || !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			s.contextsErr = msg.err
			s.state = projectFormContextsError
			return s, nil
		}
		s.state = projectFormContexts
		return s, s.setContextItems(msg.contexts)
	case projectFormIdentityMsg:
		if s.state != projectFormResolving || !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			if s.canKeepMissingExistingContext(msg.err) {
				s.selectedKubeServer = s.originalKubeServer
				return s, s.armGate()
			}
			s.state = projectFormFields
			s.setErrorMessage("context unavailable: " + msg.err.Error())
			return s, s.focusInput()
		}
		if strings.TrimSpace(msg.info.Server) == "" {
			s.state = projectFormFields
			s.setErrorMessage("selected context has no API server")
			return s, s.focusInput()
		}
		if s.selectedKubeServer != "" && !k8s.SameServer(s.selectedKubeServer, msg.info.Server) {
			s.state = projectFormFields
			s.setErrorMessage("kubeconfig changed; choose the context again before saving")
			return s, s.focusInput()
		}
		s.selectedKubeServer = msg.info.Server
		return s, s.armGate()
	case formResultMsg:
		if s.state != projectFormSaving || !s.finish(msg.reqID) {
			return s, nil
		}
		if msg.err != nil {
			s.state = projectFormFields
			if errors.Is(msg.err, store.ErrDuplicate) {
				s.setErrorMessage("name or path already used by another project")
			} else {
				s.setErrorMessage("error: " + msg.err.Error())
			}
			return s, s.focusInput()
		}
		if s.mode == formCreate {
			return s, openProject(msg.project, nil, "project created"+s.styles.glyphs.separator+"press s to scan the repo for suggestions")
		}
		return s, tea.Batch(popScreen(), func() tea.Msg { return projectSavedMsg{project: msg.project} })
	case tea.KeyPressMsg:
		return s.updateKey(msg)
	}

	if s.state == projectFormContexts {
		return s, s.updateContextList(msg)
	}
	if s.state == projectFormFields {
		return s.updateFocusedInput(msg)
	}
	return s, nil
}

func (s *projectFormScreen) setMessage(message string) {
	s.message = message
	s.messageIsError = false
}

func (s *projectFormScreen) setErrorMessage(message string) {
	s.message = message
	s.messageIsError = true
}

func (s *projectFormScreen) updateContextList(msg tea.Msg) tea.Cmd {
	return updateListModel(&s.contextList, msg)
}

func (s *projectFormScreen) updateKey(msg tea.KeyPressMsg) (screen, tea.Cmd) {
	if s.state == projectFormContexts && s.contextList.SettingFilter() {
		return s, s.updateContextList(msg)
	}

	switch s.state {
	case projectFormChooseStart:
		switch {
		case bubbleskey.Matches(msg, s.km.Cancel):
			return s, popScreen()
		case bubbleskey.Matches(msg, s.km.Up, s.km.Down):
			if s.scanRoot != "" {
				s.startChoice = 1 - s.startChoice
			}
		case bubbleskey.Matches(msg, s.km.Accept):
			if s.startChoice == 0 && s.scanRoot != "" {
				return s, s.startScan()
			}
			s.state = projectFormFields
			s.focus = projectFormName
			return s, s.focusInput()
		}
	case projectFormScanning:
		if bubbleskey.Matches(msg, s.km.Cancel) && s.stop() {
			s.state = projectFormChooseStart
		}
	case projectFormScanError:
		switch {
		case bubbleskey.Matches(msg, s.km.Accept, s.km.Rescan):
			return s, s.startScan()
		case bubbleskey.Matches(msg, s.km.Manual):
			s.state = projectFormFields
			s.focus = projectFormName
			return s, s.focusInput()
		case bubbleskey.Matches(msg, s.km.Cancel):
			return s, popScreen()
		}
	case projectFormFields:
		switch {
		case bubbleskey.Matches(msg, s.km.Cancel):
			return s, popScreen()
		case bubbleskey.Matches(msg, s.km.Accept):
			if s.focus == projectFormContext {
				return s, s.loadContexts()
			}
			if !s.validateInputs() {
				return s, nil
			}
			return s, s.resolveIdentity()
		case bubbleskey.Matches(msg, s.km.Next):
			return s, s.moveFocus(1)
		case bubbleskey.Matches(msg, s.km.Prev):
			return s, s.moveFocus(-1)
		}
		return s.updateFocusedInput(msg)
	case projectFormContextsLoading:
		if bubbleskey.Matches(msg, s.km.Cancel) && s.stop() {
			s.state = projectFormFields
			return s, s.focusInput()
		}
	case projectFormContexts:
		switch {
		case bubbleskey.Matches(msg, s.km.Cancel):
			s.state = projectFormFields
			return s, s.focusInput()
		case bubbleskey.Matches(msg, s.km.Accept):
			selected, ok := s.contextList.SelectedItem().(contextItem)
			if !ok {
				return s, nil
			}
			s.selectedKubeContext = selected.info.Name
			s.selectedKubeServer = selected.info.Server
			s.contextRequired = false
			s.setMessage("")
			s.state = projectFormFields
			return s, s.focusInput()
		default:
			return s, s.updateContextList(msg)
		}
	case projectFormContextsError:
		switch {
		case bubbleskey.Matches(msg, s.km.Accept):
			return s, s.loadContexts()
		case bubbleskey.Matches(msg, s.km.Cancel):
			s.state = projectFormFields
			return s, s.focusInput()
		}
	case projectFormResolving:
		if bubbleskey.Matches(msg, s.km.Cancel) && s.stop() {
			s.state = projectFormFields
			return s, s.focusInput()
		}
	case projectFormConfirming:
		if bubbleskey.Matches(msg, s.km.Cancel) {
			s.state = projectFormFields
			return s, s.focusInput()
		}
		confirmed, cmd := s.gate.handleKey(msg)
		if confirmed {
			return s, s.submit()
		}
		return s, cmd
	case projectFormSaving:
	}
	return s, nil
}

func (s *projectFormScreen) updateFocusedInput(msg tea.Msg) (screen, tea.Cmd) {
	input := s.focusedInput()
	if input == nil {
		return s, nil
	}
	updated, cmd := input.Update(msg)
	*input = updated
	return s, cmd
}

func (s *projectFormScreen) focusedInput() *textinput.Model {
	switch s.focus {
	case projectFormName:
		return &s.nameInput
	case projectFormPath:
		return &s.pathInput
	case projectFormNamespaces:
		return &s.namespacesInput
	default:
		return nil
	}
}

func (s *projectFormScreen) blurInputs() {
	s.nameInput.Blur()
	s.pathInput.Blur()
	s.namespacesInput.Blur()
}

func (s *projectFormScreen) focusInput() tea.Cmd {
	s.blurInputs()
	if input := s.focusedInput(); input != nil {
		return input.Focus()
	}
	return nil
}

func (s *projectFormScreen) moveFocus(delta int) tea.Cmd {
	s.blurInputs()
	s.focus = projectFormFocus((int(s.focus) + delta + int(projectFormFieldCount)) % int(projectFormFieldCount))
	return s.focusInput()
}

func (s *projectFormScreen) startScan() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.state = projectFormScanning
	s.scanErr = nil
	root := s.scanRoot
	kubeconfig := s.kubeconfig
	options := project.ScanOptions{Root: root, MaxDepth: s.scanCfg.depth, MaxFiles: s.scanCfg.maxFiles, DefaultNamespace: s.defaultNamespace()}
	return func() tea.Msg {
		result, err := project.Scan(ctx, options)
		if err != nil {
			return projectFormScanMsg{reqID: reqID, err: err}
		}
		contexts, contextsErr := k8s.ListContexts(kubeconfig)
		return projectFormScanMsg{reqID: reqID, result: result, contexts: contexts, contextsErr: contextsErr}
	}
}

func (s *projectFormScreen) requireExplicitContextChoice() {
	s.selectedKubeContext = ""
	s.selectedKubeServer = ""
	s.contextRequired = true
}

func (s *projectFormScreen) applyScan(result project.ScanResult, contexts []k8s.ContextInfo, contextsErr error) {
	if strings.TrimSpace(s.nameInput.Value()) == "" {
		s.nameInput.SetValue(filepath.Base(s.scanRoot))
	}
	if strings.TrimSpace(s.pathInput.Value()) == "" {
		s.pathInput.SetValue(s.scanRoot)
	}

	namespaces := scannedNamespaces(result.Suggestions)
	switch len(namespaces) {
	case 0:
	case 1:
		s.namespacesInput.SetValue(namespaces[0])
	default:
		namespaces = append([]string{s.defaultNamespace()}, namespaces...)
		s.namespacesInput.SetValue(strings.Join(dedupeNonEmpty(namespaces), ", "))
	}

	s.scanIncomplete = result.Incomplete
	s.contextRequired = false
	if result.Incomplete {
		s.requireExplicitContextChoice()
	} else {
		switch len(result.ContextHints) {
		case 0:
		case 1:
			matched, found := contextByName(contexts, result.ContextHints[0])
			if found {
				s.selectedKubeContext = matched.Name
				s.selectedKubeServer = matched.Server
			} else {
				s.requireExplicitContextChoice()
			}
		default:
			s.requireExplicitContextChoice()
		}
	}

	parts := []string{fmt.Sprintf("scan found %d suggestions", len(result.Suggestions))}
	if result.Incomplete {
		parts = append(parts, "scan incomplete; review inferred fields")
	} else if len(result.ContextHints) > 1 {
		parts = append(parts, "conflicting context hints require a choice")
	} else if len(result.ContextHints) == 1 && s.contextRequired {
		parts = append(parts, "hinted context is not locally available; choose one")
	}
	if contextsErr != nil && len(result.ContextHints) > 0 {
		parts = append(parts, "local contexts could not be read; choose one")
	}
	s.setMessage(strings.Join(parts, s.styles.glyphs.separator))
}

func scannedNamespaces(suggestions []project.Suggestion) []string {
	namespaces := make([]string, 0)
	for _, suggestion := range suggestions {
		if suggestion.Kind == project.KindNamespace {
			namespaces = append(namespaces, suggestion.Name)
		}
	}
	return dedupeNonEmpty(namespaces)
}

func dedupeNonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func contextByName(contexts []k8s.ContextInfo, name string) (k8s.ContextInfo, bool) {
	for _, info := range contexts {
		if info.Name == name {
			return info, true
		}
	}
	return k8s.ContextInfo{}, false
}

func (s *projectFormScreen) loadContexts() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.blurInputs()
	s.contextList.ResetFilter()
	s.state = projectFormContextsLoading
	s.contextsErr = nil
	kubeconfig := s.kubeconfig
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return projectFormContextsMsg{reqID: reqID, err: err}
		}
		contexts, err := k8s.ListContexts(kubeconfig)
		return projectFormContextsMsg{reqID: reqID, contexts: contexts, err: err}
	}
}

func (s *projectFormScreen) setContextItems(contexts []k8s.ContextInfo) tea.Cmd {
	items := make([]list.Item, len(contexts))
	selected := 0
	for i, info := range contexts {
		items[i] = contextItem{info: info, styles: s.styles}
		if info.Name == s.selectedKubeContext && (s.selectedKubeServer == "" || k8s.SameServer(info.Server, s.selectedKubeServer)) {
			selected = i
		}
	}
	cmd := scopeListFilterCmd(&s.contextList, s.contextList.SetItems(items))
	if len(items) > 0 {
		s.contextList.Select(selected)
	}
	return cmd
}

func (s *projectFormScreen) validateInputs() bool {
	name := strings.TrimSpace(s.nameInput.Value())
	rootPath := strings.TrimSpace(s.pathInput.Value())
	namespaces := splitNamespaces(s.namespacesInput.Value())
	if name == "" || rootPath == "" || s.selectedKubeContext == "" || len(namespaces) == 0 || s.contextRequired {
		s.setErrorMessage("name, path, a selected context, and at least one namespace are required")
		return false
	}
	return true
}

func (s *projectFormScreen) resolveIdentity() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	s.blurInputs()
	s.state = projectFormResolving
	s.setMessage("")
	kubeconfig := s.kubeconfig
	contextName := s.selectedKubeContext
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return projectFormIdentityMsg{reqID: reqID, err: err}
		}
		info, err := k8s.ResolveContextIdentity(kubeconfig, contextName)
		return projectFormIdentityMsg{reqID: reqID, info: info, err: err}
	}
}

func (s *projectFormScreen) canKeepMissingExistingContext(err error) bool {
	return s.mode == formEdit && errors.Is(err, k8s.ErrContextNotFound) &&
		s.selectedKubeContext == s.originalKubeContext && strings.TrimSpace(s.originalKubeServer) != ""
}

func (s *projectFormScreen) armGate() tea.Cmd {
	s.state = projectFormConfirming
	return s.gate.arm()
}

func (s *projectFormScreen) submit() tea.Cmd {
	name := strings.TrimSpace(s.nameInput.Value())
	rootPath := strings.TrimSpace(s.pathInput.Value())
	namespaces := splitNamespaces(s.namespacesInput.Value())
	kubeContext := s.selectedKubeContext
	kubeServer := s.selectedKubeServer
	switchPromptSuppressed := s.switchPromptSuppressed
	if kubeContext != s.originalKubeContext || !k8s.SameServer(kubeServer, s.originalKubeServer) {
		switchPromptSuppressed = false
	}
	meta := store.ProjectMeta{
		Name: name, RootPath: rootPath, KubeContext: kubeContext, KubeServer: kubeServer,
		Namespace: namespaces[0], SwitchPromptSuppressed: switchPromptSuppressed,
	}
	ctx, reqID := s.start(s.ctx)
	s.state = projectFormSaving
	s.setMessage("")
	mode := s.mode
	existingID := int64(0)
	if s.existing != nil {
		existingID = s.existing.ID
	}
	st := s.store
	return func() tea.Msg {
		var saved store.Project
		var err error
		if st == nil {
			err = errors.New("project database unavailable")
		} else if mode == formCreate {
			saved, err = st.CreateProjectWithNamespaces(ctx, meta, namespaces[1:])
		} else if existingID == 0 {
			err = errors.New("edit project is missing its existing row")
		} else {
			saved, err = st.UpdateProjectWithNamespaces(ctx, existingID, meta, namespaces[1:])
		}
		return formResultMsg{reqID: reqID, project: saved, err: err}
	}
}

func splitNamespaces(value string) []string {
	return dedupeNonEmpty(strings.Split(value, ","))
}

func (s *projectFormScreen) defaultNamespace() string {
	if namespaces := splitNamespaces(s.namespacesInput.Value()); len(namespaces) > 0 {
		return namespaces[0]
	}
	return "default"
}

func (s *projectFormScreen) View() string {
	title := "New project"
	if s.mode == formEdit && s.existing != nil {
		title = "Edit project " + s.existing.Name
	}

	switch s.state {
	case projectFormChooseStart:
		return s.renderStartChoice(title)
	case projectFormScanning:
		return s.render(dialogContent{
			title: title, identity: []string{"detected root: " + s.scanRoot},
			prompt: "scanning repository metadata...", message: "esc cancels the scan",
		})
	case projectFormScanError:
		return s.render(dialogContent{
			title: title, identity: []string{"detected root: " + s.scanRoot},
			body: []string{"scan failed: " + s.scanErr.Error()}, prompt: "enter retry  m continue manually  esc discard",
		})
	case projectFormContextsLoading:
		return s.render(dialogContent{title: "Choose project context", prompt: "loading local kube contexts...", message: "esc back to fields"})
	case projectFormContexts:
		prompt := s.contextList.View()
		if len(s.contextList.Items()) == 0 {
			prompt = "no local kube contexts configured"
		}
		return s.render(dialogContent{
			title: "Choose project context", body: []string{"selection only; this does not switch or probe the app client"}, prompt: prompt,
		})
	case projectFormContextsError:
		return s.render(dialogContent{
			title: "Choose project context", body: []string{"could not read local kube contexts: " + s.contextsErr.Error()},
			prompt: "enter retry  esc back to fields",
		})
	case projectFormResolving:
		return s.render(dialogContent{
			title: title, identity: []string{"context: " + s.selectedKubeContext},
			prompt: "verifying local context identity...", message: "esc cancels verification",
		})
	case projectFormConfirming:
		return s.renderReview()
	case projectFormSaving:
		return s.render(dialogContent{
			title: title, identity: s.reviewIdentity(), prompt: "saving (cannot cancel)",
		})
	default:
		return s.renderFields(title)
	}
}

func (s *projectFormScreen) renderStartChoice(title string) string {
	scanLabel := "scan detected project root"
	if s.scanRoot == "" {
		scanLabel += " (unavailable: no detected root)"
	}
	choices := []string{scanLabel, "continue manually"}
	lines := make([]string, len(choices))
	for i, choice := range choices {
		if i == 0 && s.scanRoot == "" {
			choice = s.styles.dim.Render(choice)
		}
		lines[i] = s.styles.renderSelectableRow(choice, i == s.startChoice, s.contentWidth())
	}
	identity := []string{}
	if s.scanRoot != "" {
		identity = []string{"detected root: " + s.scanRoot}
	}
	return s.render(dialogContent{
		title: title, identity: identity,
		body:   []string{"Scan safely infers project metadata only; it creates no links."},
		prompt: strings.Join(lines, "\n"),
	})
}

func (s *projectFormScreen) renderFields(title string) string {
	contextName := s.selectedKubeContext
	if contextName == "" {
		contextName = "choose a local context"
	}
	contextLine := s.styles.renderSelectableRow("context: "+contextName+"  (enter to choose)", s.focus == projectFormContext, s.contentWidth())
	prompt := strings.Join([]string{s.nameInput.View(), s.pathInput.View(), contextLine, s.namespacesInput.View()}, "\n")
	warnings := []string{"context is selection-only and never switches the active client"}
	if s.scanIncomplete {
		warning := "scan incomplete; review inferred fields"
		if s.contextRequired {
			warning += " and choose a context"
		}
		warnings = append(warnings, warning)
	} else if s.contextRequired {
		warnings = append(warnings, "scan context hints require an explicit local context choice")
	}
	return s.render(dialogContent{
		title:            title,
		identity:         []string{"selected server: " + serverOrUnverified(s.selectedKubeServer) + " (read-only)"},
		criticalWarnings: warnings,
		body: []string{
			"namespaces are comma-separated; the first one is the default",
			"sk64 stores names, paths and links only, never secret values",
		},
		prompt: prompt, message: s.message, isError: s.messageIsError,
	})
}

func (s *projectFormScreen) renderReview() string {
	reviewTitle := "Create project?"
	if s.mode == formEdit {
		reviewTitle = "Save project changes?"
	}
	return s.render(dialogContent{
		title: reviewTitle, identity: s.reviewIdentity(),
		body:   []string{"This stores project metadata in sk64's local database."},
		prompt: s.gate.promptLines(s.styles, false), message: s.gate.message, isWarning: s.gate.message != "",
	})
}

func (s *projectFormScreen) reviewIdentity() []string {
	namespaces := splitNamespaces(s.namespacesInput.Value())
	return []string{
		"name: " + strings.TrimSpace(s.nameInput.Value()),
		"path: " + strings.TrimSpace(s.pathInput.Value()),
		"context: " + s.selectedKubeContext,
		"server: " + serverOrUnverified(s.selectedKubeServer),
		"namespaces: " + strings.Join(namespaces, ", ") + " (first is default)",
	}
}

func (s *projectFormScreen) SetSize(width, height int) {
	s.resize(width, height)
	for _, input := range []*textinput.Model{&s.nameInput, &s.pathInput, &s.namespacesInput} {
		input.SetWidth(textInputWidth(s.contentWidth(), input.Prompt))
	}
	s.contextList.SetSize(s.contentWidth(), s.scrollHeight(7))
	s.gate.setWidth(s.contentWidth())
}

func (s *projectFormScreen) SetStyles(st *styles) {
	s.styles = st
	for _, input := range []*textinput.Model{&s.nameInput, &s.pathInput, &s.namespacesInput} {
		applyTextInputStyles(input, st)
	}
	applyDetailedListStyles(&s.contextList, st)
	s.gate.setStyles(st)
}

func (s *projectFormScreen) Title() string {
	if s.mode == formEdit && s.existing != nil {
		return s.existing.Name + " (edit)"
	}
	return "new project"
}

func (s *projectFormScreen) stop() bool {
	if s.state == projectFormSaving {
		return false
	}
	return s.loader.stop()
}

func (s *projectFormScreen) Hints() footerHints {
	switch s.state {
	case projectFormChooseStart:
		return hintBindings(s.km.Accept, hintDesc(s.km.Up, "choose"), hintDesc(s.km.Cancel, "discard"))
	case projectFormScanning, projectFormContextsLoading, projectFormResolving:
		return hintBindings(s.km.Cancel)
	case projectFormScanError:
		return hintBindings(hintDesc(s.km.Accept, "retry"), s.km.Manual, hintDesc(s.km.Cancel, "discard"))
	case projectFormContexts:
		return hintBindings(s.km.Accept, s.keys.global.Filter, hintDesc(s.km.Cancel, "back"))
	case projectFormContextsError:
		return hintBindings(hintDesc(s.km.Accept, "retry"), hintDesc(s.km.Cancel, "back"))
	case projectFormConfirming:
		return hintBindings(displayHint("YES", "confirm"), hintDesc(s.km.Cancel, "back to fields"))
	case projectFormSaving:
		return hintStatus("saving (cannot cancel)")
	default:
		if s.focus == projectFormContext {
			return hintBindings(hintDesc(s.km.Accept, "choose context"), s.km.Next, hintDesc(s.km.Cancel, "discard"))
		}
		return hintBindings(hintDesc(s.km.Accept, "review"), s.km.Next, hintDesc(s.km.Cancel, "discard"))
	}
}

func (s *projectFormScreen) Help() helpGroup     { return helpGroup{} }
func (s *projectFormScreen) CapturesInput() bool { return true }
func (s *projectFormScreen) WantsEsc() bool      { return true }
