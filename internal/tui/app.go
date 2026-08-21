package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	bubbleskey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/config"
	"github.com/NoahHakansson/sk64/internal/debuglog"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/NoahHakansson/sk64/internal/undo"
)

const (
	minimumWidth  = 60
	minimumHeight = 15
)

// Options configures the TUI. Client must be non-nil and already probed.
type Options struct {
	Client                *k8s.Client
	Keybinds              config.Overrides
	Kubeconfig            string
	StartNamespace        string
	ASCII                 bool
	Editor                string
	ReadOnly              bool
	NoConfigMaps          bool
	Store                 *store.Store
	Project               *store.Project
	ProjectRoot           string
	StartupNotice         string
	ConfirmProjectContext bool
	ProjectContextTarget  k8s.ContextInfo
	ScanDepth             int
	ScanMaxFiles          int
	Debug                 *debuglog.Logger
}

type appOverlay interface {
	Init() tea.Cmd
	Update(msg tea.Msg) tea.Cmd
	View() string
	SetSize(width, height int)
	SetStyles(*styles)
	Hints() footerHints
	isClosed() bool
}

type screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (screen, tea.Cmd)
	View() string
	SetSize(width, height int)
	SetStyles(*styles)
	Title() string
	// Hints returns ordered bindings or a cannot-cancel status line. Binding help
	// text is ASCII-only and rendered at two-space separation within a 78-column
	// budget. Browsing screens set showHelp. Actions are ordered primary,
	// mutating, secondary, mode-toggle, then list-filter. Global keys live in
	// help, except root "Q quit". Modal and transient states keep their
	// state-specific hints.
	Hints() footerHints
	Help() helpGroup
	CapturesInput() bool
	WantsEsc() bool
}

type noticeClearer interface {
	clearNotice()
}

type app struct {
	ctx                       context.Context
	kubeconfig                string
	client                    *k8s.Client
	styles                    *styles
	glyphs                    glyphs
	editEnv                   editEnv
	store                     *store.Store
	projectRoot               string
	projectName               string
	scanCfg                   scanConfig
	stack                     []screen
	stackGeneration           uint64
	overlay                   appOverlay
	overlayClosing            uint64
	overlayCloseActionApplied uint64
	overlayCloseSequence      uint64
	width, height             int
	fatal                     error
	quitArm                   quitArm
}

type pushScreenMsg struct {
	generation uint64
	s          screen
}

type popScreenMsg struct {
	generation uint64
}

type replaceScreenMsg struct {
	generation uint64
	s          screen
}

type overlayCloseActionMsg struct {
	sequence uint64
	action   tea.Msg
}

type overlayClosedMsg struct {
	sequence uint64
}

func pushScreen(s screen) tea.Cmd {
	return func() tea.Msg { return pushScreenMsg{s: s} }
}

func popScreen() tea.Cmd {
	return func() tea.Msg { return popScreenMsg{} }
}

func scopeNavigation(cmd tea.Cmd, generation uint64) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch msg := msg.(type) {
		case pushScreenMsg:
			msg.generation = generation
			return msg
		case popScreenMsg:
			msg.generation = generation
			return msg
		case replaceScreenMsg:
			msg.generation = generation
			return msg
		case searchJumpMsg:
			msg.generation = generation
			return msg
		case openProjectPickerMsg:
			msg.generation = generation
			return msg
		case tea.BatchMsg:
			for i, batched := range msg {
				msg[i] = scopeNavigation(batched, generation)
			}
			return msg
		default:
			return msg
		}
	}
}

type editEnv struct {
	editorFlag   string
	readOnly     bool
	noConfigMaps bool
	ring         *undo.Ring
	log          *debuglog.Logger
	keys         *keyMaps
}

func (e editEnv) keymaps() *keyMaps {
	if e.keys == nil {
		return packageDefaultKeyMaps
	}
	return e.keys
}

func newApp(ctx context.Context, opts Options) app {
	glyphs := newGlyphs(opts.ASCII)
	st := newStyles(true, glyphs)
	keys := defaultKeyMaps()
	applyKeybinds(keys, opts.Keybinds)
	env := editEnv{editorFlag: opts.Editor, readOnly: opts.ReadOnly, noConfigMaps: opts.NoConfigMaps, ring: undo.NewRing(), log: opts.Debug, keys: keys}
	scanCfg := scanConfig{depth: opts.ScanDepth, maxFiles: opts.ScanMaxFiles}
	projectName := ""
	if opts.Project != nil {
		projectName = opts.Project.Name
	}
	stack := []screen{newNamespaceScreen(ctx, opts.Client, projectName, env, st)}
	namespaceRoot := stack[0].(*namespaceScreen)
	if opts.Project != nil {
		stack = append(stack, newProjectScreen(ctx, opts.Client, opts.Store, opts.Kubeconfig, *opts.Project, opts.StartupNotice, scanCfg, env, st))
		if opts.ConfirmProjectContext {
			stack = append(stack, newProjectContextConfirm(ctx, opts.Store, *opts.Project, opts.Client, opts.ProjectContextTarget, opts.Kubeconfig, opts.Debug, st))
		}
	} else if opts.StartNamespace != "" {
		stack = append(stack, newResourceScreen(ctx, opts.Client, opts.StartNamespace, env, st))
	}
	if opts.Project == nil {
		if opts.StartupNotice != "" {
			namespaceRoot.notes = append(namespaceRoot.notes, opts.StartupNotice)
		}
		if opts.Store != nil && opts.ProjectRoot != "" {
			namespaceRoot.notes = append(namespaceRoot.notes, "no project for this directory"+glyphs.separator+"ctrl+p to create/search")
		}
	}
	return app{ctx: ctx, kubeconfig: opts.Kubeconfig, client: opts.Client, styles: st, glyphs: glyphs, editEnv: env, store: opts.Store, projectRoot: opts.ProjectRoot, projectName: projectName, scanCfg: scanCfg, stack: stack, stackGeneration: 1, quitArm: newQuitArm()}
}

// Run starts the TUI. Options.Client must be non-nil and already probed.
func Run(ctx context.Context, opts Options) error {
	if opts.Client == nil {
		return errors.New("run TUI: client must not be nil")
	}
	model, err := tea.NewProgram(newApp(ctx, opts), tea.WithContext(ctx)).Run()
	if err != nil {
		return normalizeRunError(ctx, err)
	}
	return runResult(model)
}

func runResult(model tea.Model) error {
	if finished, ok := model.(app); ok && finished.fatal != nil {
		return finished.fatal
	}
	return nil
}

func normalizeRunError(ctx context.Context, err error) error {
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("run TUI: %w", err)
}

// bodyHeight is the rows between the app's header and footer that screens and
// overlays render into.
func bodyHeight(height int) int { return max(0, height-2) }

func matchingProjectNamespaceClient(client *k8s.Client, project store.Project) (*k8s.Client, bool) {
	identityMatches := project.KubeContext == client.Context &&
		(project.KubeServer == "" || k8s.SameServer(project.KubeServer, client.Server))
	if !identityMatches || client.Namespace == project.Namespace {
		return nil, false
	}
	updated := *client
	updated.Namespace = project.Namespace
	return &updated, true
}

func (m app) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.stack)+1)
	for _, current := range m.stack {
		cmds = append(cmds, scopeNavigation(current.Init(), m.stackGeneration))
	}
	cmds = append(cmds, tea.RequestBackgroundColor)
	return tea.Batch(cmds...)
}

func (m app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height
		for _, current := range m.stack {
			current.SetSize(size.Width, bodyHeight(size.Height))
		}
		if m.overlay != nil {
			m.overlay.SetSize(size.Width, bodyHeight(size.Height))
		}
		return m, nil
	}

	if background, ok := msg.(tea.BackgroundColorMsg); ok {
		// Replace the pointer instead of overwriting the pointee: in-flight
		// commands hold snapshots of the previous styles and may read them
		// concurrently, so the old object must stay immutable.
		m.styles = newStyles(background.IsDark(), m.glyphs)
		for _, current := range m.stack {
			current.SetStyles(m.styles)
		}
		if m.overlay != nil {
			m.overlay.SetStyles(m.styles)
		}
		return m, nil
	}

	if expired, ok := msg.(quitArmExpiredMsg); ok {
		m.quitArm.expire(expired)
		return m, nil
	}

	if key, ok := msg.(tea.KeyPressMsg); ok {
		if bubbleskey.Matches(key, m.editEnv.keys.global.ConfirmQuit) {
			if m.quitArm.armed {
				if capture := m.quitCapture(); capture != nil && capture.capturesQuit() {
					capture.prepareQuit()
				}
				return m, tea.Quit
			}
			warning := "press ctrl+c again to quit"
			if capture := m.quitCapture(); capture != nil && capture.capturesQuit() {
				capture.prepareQuitArm()
				warning = capture.quitWarning()
			}
			return m, m.quitArm.arm(warning)
		}
		m.quitArm.disarm()
	}

	if m.width > 0 && m.height > 0 && (m.width < minimumWidth || m.height < minimumHeight) {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			if bubbleskey.Matches(key, m.editEnv.keys.global.Quit) {
				return m, tea.Quit
			}
			return m, nil
		}
	}

	var appCmd tea.Cmd
	switch msg := msg.(type) {
	case fatalMsg:
		m.fatal = msg.err
		return m, tea.Quit
	case overlayCloseActionMsg:
		if msg.sequence != m.overlayClosing {
			return m, nil
		}
		var actionCmd tea.Cmd
		if msg.action != nil {
			updated, cmd := m.Update(msg.action)
			m = updated.(app)
			actionCmd = cmd
		}
		if msg.sequence != m.overlayClosing {
			return m, actionCmd
		}
		m.overlayCloseActionApplied = msg.sequence
		sequence := msg.sequence
		release := func() tea.Msg { return overlayClosedMsg{sequence: sequence} }
		return m, tea.Batch(actionCmd, release)
	case overlayClosedMsg:
		if msg.sequence == m.overlayClosing && msg.sequence == m.overlayCloseActionApplied {
			m.overlayClosing = 0
			m.overlayCloseActionApplied = 0
		}
		return m, nil
	case pushScreenMsg:
		if msg.generation != m.stackGeneration {
			return m, nil
		}
		msg.s.SetSize(m.width, bodyHeight(m.height))
		m.stack = append(m.stack, msg.s)
		m.stackGeneration++
		return m, scopeNavigation(msg.s.Init(), m.stackGeneration)
	case popScreenMsg:
		if msg.generation != m.stackGeneration {
			return m, nil
		}
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
			m.stackGeneration++
		}
		return m, nil
	case replaceScreenMsg:
		if msg.generation != m.stackGeneration {
			return m, nil
		}
		msg.s.SetSize(m.width, bodyHeight(m.height))
		m.stack[len(m.stack)-1] = msg.s
		m.stackGeneration++
		return m, scopeNavigation(msg.s.Init(), m.stackGeneration)
	case contextSwitchedMsg:
		stopLoadingScreens(m.stack)
		if !k8s.SameServer(m.client.Server, msg.client.Server) {
			m.editEnv.ring = undo.NewRing()
		}
		m.client = msg.client
		m.overlay = nil
		root := newNamespaceScreen(m.ctx, msg.client, m.projectName, m.editEnv, m.styles)
		root.SetSize(m.width, bodyHeight(m.height))
		m.stack = []screen{root}
		m.stackGeneration++
		return m, root.Init()
	case openProjectPickerMsg:
		if msg.generation != m.stackGeneration {
			return m, nil
		}
		m.overlayClosing = 0
		m.overlayCloseActionApplied = 0
		m.overlay = newProjectOverlay(m.ctx, m.store, m.client, m.kubeconfig, m.projectRoot, m.scanCfg, m.editEnv.log, projectModeLink, msg.link, m.editEnv.keys, m.styles)
		m.overlay.SetSize(m.width, bodyHeight(m.height))
		return m, m.overlay.Init()
	case projectOpenedMsg:
		stopLoadingScreens(m.stack)
		projectClient := msg.client
		if projectClient == nil {
			projectClient, _ = matchingProjectNamespaceClient(m.client, msg.project)
		}
		if projectClient != nil {
			if !k8s.SameServer(m.client.Server, projectClient.Server) {
				m.editEnv.ring = undo.NewRing()
			}
			m.client = projectClient
		}
		m.projectName = msg.project.Name
		m.overlay = nil
		root := newNamespaceScreen(m.ctx, m.client, m.projectName, m.editEnv, m.styles)
		projectView := newProjectScreen(m.ctx, m.client, m.store, m.kubeconfig, msg.project, msg.notice, m.scanCfg, m.editEnv, m.styles)
		root.SetSize(m.width, bodyHeight(m.height))
		projectView.SetSize(m.width, bodyHeight(m.height))
		m.stack = []screen{root, projectView}
		m.stackGeneration++
		cmds := []tea.Cmd{root.Init(), projectView.Init()}
		if m.store != nil {
			st, name, ctx := m.store, msg.project.Name, m.ctx
			cmds = append(cmds, func() tea.Msg { _ = st.SetLastProject(ctx, name); return nil })
		}
		return m, tea.Batch(cmds...)
	case projectSavedMsg:
		for _, current := range m.stack {
			projectView, ok := current.(*projectScreen)
			if !ok || projectView.project.ID != msg.project.ID {
				continue
			}
			renamed := m.projectName != msg.project.Name
			m.projectName = msg.project.Name
			projectClient, namespaceChanged := matchingProjectNamespaceClient(m.client, msg.project)
			if namespaceChanged {
				m.client = projectClient
			}
			if root, ok := m.stack[0].(*namespaceScreen); ok {
				if namespaceChanged {
					root.client = m.client
					appCmd = root.startLoading()
				}
				root.projectName = m.projectName
			}
			if renamed && m.store != nil {
				st, name, ctx := m.store, msg.project.Name, m.ctx
				appCmd = tea.Batch(appCmd, func() tea.Msg { _ = st.SetLastProject(ctx, name); return nil })
			}
			break
		}
	case searchJumpMsg:
		if msg.generation != m.stackGeneration {
			return m, nil
		}
		stopLoadingScreens(m.stack)
		root := newNamespaceScreen(m.ctx, m.client, m.projectName, m.editEnv, m.styles)
		resources := newResourceScreen(m.ctx, m.client, msg.namespace, m.editEnv, m.styles)
		keys := newKeyScreen(m.ctx, m.client, msg.kind, msg.namespace, msg.name, m.editEnv, m.styles)
		for _, current := range []screen{root, resources, keys} {
			current.SetSize(m.width, bodyHeight(m.height))
		}
		m.stack = []screen{root, resources, keys}
		m.stackGeneration++
		return m, tea.Batch(root.Init(), resources.Init(), keys.Init())
	}

	if m.overlayClosing != 0 && isOverlayInput(msg) {
		return m, nil
	}

	if m.overlay != nil {
		generation := m.stackGeneration
		if isOverlayInput(msg) {
			cmd := m.overlay.Update(msg)
			return m, m.finishOverlayUpdate(cmd, generation)
		}

		overlayCmd := m.overlay.Update(msg)
		cmds := make([]tea.Cmd, 0, len(m.stack)+2)
		if appCmd != nil {
			cmds = append(cmds, appCmd)
		}
		for i, current := range m.stack {
			updated, cmd := current.Update(msg)
			m.stack[i] = updated
			cmds = append(cmds, scopeNavigation(cmd, generation))
		}
		cmds = append(cmds, m.finishOverlayUpdate(overlayCmd, generation))
		return m, tea.Batch(cmds...)
	}

	if key, ok := msg.(tea.KeyPressMsg); ok {
		top := m.stack[len(m.stack)-1]
		if top.CapturesInput() {
			updated, cmd := top.Update(msg)
			m.stack[len(m.stack)-1] = updated
			return m, scopeNavigation(cmd, m.stackGeneration)
		}
		if clearer, ok := top.(noticeClearer); ok {
			clearer.clearNotice()
		}

		switch {
		case bubbleskey.Matches(key, m.editEnv.keys.global.Quit):
			return m, tea.Quit
		case bubbleskey.Matches(key, m.editEnv.keys.global.Help):
			m.overlayClosing = 0
			m.overlayCloseActionApplied = 0
			m.overlay = newHelpOverlay(top, m.editEnv, m.styles)
			m.overlay.SetSize(m.width, bodyHeight(m.height))
			return m, m.overlay.Init()
		case bubbleskey.Matches(key, m.editEnv.keys.global.ContextSwitch):
			m.overlayClosing = 0
			m.overlayCloseActionApplied = 0
			m.overlay = newContextOverlay(m.ctx, m.kubeconfig, m.client.Context, m.client.Server, m.editEnv.log, m.editEnv.keys, m.styles)
			m.overlay.SetSize(m.width, bodyHeight(m.height))
			return m, m.overlay.Init()
		case bubbleskey.Matches(key, m.editEnv.keys.global.ProjectSwitch):
			m.overlayClosing = 0
			m.overlayCloseActionApplied = 0
			m.overlay = newProjectOverlay(m.ctx, m.store, m.client, m.kubeconfig, m.projectRoot, m.scanCfg, m.editEnv.log, projectModeSwitch, pendingLink{}, m.editEnv.keys, m.styles)
			m.overlay.SetSize(m.width, bodyHeight(m.height))
			return m, m.overlay.Init()
		case bubbleskey.Matches(key, m.editEnv.keys.global.Search):
			return m, scopeNavigation(pushScreen(newSearchScreen(m.ctx, m.client, m.editEnv, m.styles)), m.stackGeneration)
		case bubbleskey.Matches(key, m.editEnv.keys.global.Back):
			if top.WantsEsc() {
				updated, cmd := top.Update(msg)
				m.stack[len(m.stack)-1] = updated
				return m, scopeNavigation(cmd, m.stackGeneration)
			}
			if len(m.stack) > 1 {
				m.stack = m.stack[:len(m.stack)-1]
				m.stackGeneration++
			}
			return m, nil
		default:
			updated, cmd := top.Update(msg)
			m.stack[len(m.stack)-1] = updated
			return m, scopeNavigation(cmd, m.stackGeneration)
		}
	}

	generation := m.stackGeneration
	cmds := make([]tea.Cmd, 0, len(m.stack)+1)
	if appCmd != nil {
		cmds = append(cmds, appCmd)
	}
	for i, current := range m.stack {
		updated, cmd := current.Update(msg)
		m.stack[i] = updated
		cmds = append(cmds, scopeNavigation(cmd, generation))
	}
	return m, tea.Batch(cmds...)
}

type quitCapture interface {
	capturesQuit() bool
	prepareQuitArm()
	prepareQuit()
	quitWarning() string
}

func (m *app) quitCapture() quitCapture {
	if m.overlay != nil || len(m.stack) == 0 {
		return nil
	}
	capture, _ := m.stack[len(m.stack)-1].(quitCapture)
	return capture
}

func (m *app) finishOverlayUpdate(cmd tea.Cmd, generation uint64) tea.Cmd {
	cmd = scopeNavigation(cmd, generation)
	if !m.overlay.isClosed() {
		return cmd
	}
	m.overlay = nil
	m.overlayCloseSequence++
	m.overlayClosing = m.overlayCloseSequence
	m.overlayCloseActionApplied = 0
	sequence := m.overlayClosing
	return func() tea.Msg {
		var action tea.Msg
		if cmd != nil {
			action = cmd()
		}
		return overlayCloseActionMsg{sequence: sequence, action: action}
	}
}

func isOverlayInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg, tea.PasteMsg:
		return true
	default:
		return false
	}
}

func (m app) View() tea.View {
	if m.width > 0 && m.height > 0 && (m.width < minimumWidth || m.height < minimumHeight) {
		message := fmt.Sprintf("too small: need %dx%d, have %dx%d", minimumWidth, minimumHeight, m.width, m.height)
		message = truncateLine(message, m.width, m.styles.glyphs.ellipsis)
		if !m.quitArm.armed {
			view := tea.NewView(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.styles.tooSmall.Render(message)))
			view.AltScreen = true
			return view
		}
		body := lipgloss.Place(m.width, m.height-1, lipgloss.Center, lipgloss.Center, m.styles.tooSmall.Render(message))
		footer := renderBar(m.styles.warnText, m.width, m.quitArm.message)
		view := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, body, footer))
		view.AltScreen = true
		return view
	}

	titles := make([]string, len(m.stack))
	for i, current := range m.stack {
		titles[i] = current.Title()
	}
	header := renderHeaderBar(m.styles.header, m.styles.activeContext, m.client.Context, m.client.Server, titles, m.width, m.glyphs.ellipsis)

	top := m.stack[len(m.stack)-1]
	body := top.View()
	hints := withNavigationHint(top, len(m.stack), m.editEnv.keymaps())
	if m.overlay != nil {
		body = lipgloss.Place(m.width, bodyHeight(m.height), lipgloss.Center, lipgloss.Center, m.overlay.View())
		hints = hintGroups(m.overlay.Hints())
	}
	footer := renderFooterBar(m.styles, hints, m.width)
	if m.quitArm.armed {
		footer = renderBar(m.styles.warnText, m.width, m.quitArm.message)
	}

	view := tea.NewView(lipgloss.JoinVertical(lipgloss.Left, header, body, footer))
	view.AltScreen = true
	return view
}

const (
	chromeGap            = "  "
	chromeTrailSeparator = " > "
)

type chromeRail struct {
	context string
	server  string
	trail   string
	leaf    string
}

func renderHeaderBar(style, activeContext lipgloss.Style, contextName, server string, titles []string, width int, ellipsis string) string {
	server = redactServerUserinfo(server)
	leaf := ""
	ancestors := []string(nil)
	if len(titles) > 0 {
		leaf = titles[len(titles)-1]
		ancestors = titles[:len(titles)-1]
	}
	trails := chromeTrailCandidates(ancestors, ellipsis)

	rail := chromeRail{context: contextName, server: server, leaf: leaf}
	for _, trail := range trails {
		rail.trail = trail
		if width <= 0 || rail.width() <= width {
			return renderBar(style, width, rail.segments(width, activeContext)...)
		}
	}

	rail.trail = trails[len(trails)-1]
	rail.fit(width, ellipsis)
	return renderBar(style, width, rail.segments(width, activeContext)...)
}

func serverHost(server string) string {
	parsed, err := url.Parse(server)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return server
}

func compactServer(server string, width int, ellipsis string) string {
	if width <= 0 {
		return server
	}
	if ellipsis == "" {
		ellipsis = "..."
	}
	forms := truthfulServerForms(server, width, ellipsis)
	longest := ""
	longestWidth := 0
	for _, form := range forms {
		formWidth := lipgloss.Width(form)
		if formWidth > width || formWidth <= longestWidth {
			continue
		}
		longest = form
		longestWidth = formWidth
	}
	if longest != "" {
		return longest
	}
	return middleElideLine(forms[len(forms)-1], width, ellipsis)
}

func truthfulServerForms(server string, width int, ellipsis string) []string {
	forms := []string{server}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Host == "" {
		return forms
	}

	escapedPath := parsed.EscapedPath()
	pathTail := escapedPath
	if parsed.ForceQuery || parsed.RawQuery != "" {
		pathTail += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		pathTail += "#" + parsed.EscapedFragment()
	}
	identityBearingTail := (escapedPath != "" && escapedPath != "/") || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != ""
	if identityBearingTail {
		schemeHost := parsed.Host
		if parsed.Scheme != "" {
			schemeHost = parsed.Scheme + "://" + parsed.Host
		} else if strings.HasPrefix(server, "//") {
			schemeHost = "//" + parsed.Host
		}
		prefixes := []string{schemeHost}
		if schemeHost != parsed.Host {
			prefixes = append(prefixes, parsed.Host)
		}
		for _, prefix := range prefixes {
			pathWidth := width - lipgloss.Width(prefix)
			if pathWidth <= lipgloss.Width(ellipsis)+1 {
				continue
			}
			form := prefix + middleElideLine(pathTail, pathWidth, ellipsis)
			if form != forms[len(forms)-1] {
				forms = append(forms, form)
			}
		}
		return forms
	}

	hostForms := []string{parsed.Host}
	if parsed.Port() == "" {
		hostForms = append(hostForms, parsed.Hostname())
	}
	for _, form := range hostForms {
		if form != "" && form != forms[len(forms)-1] {
			forms = append(forms, form)
		}
	}
	return forms
}

func chromeTrailCandidates(ancestors []string, ellipsis string) []string {
	if len(ancestors) == 0 {
		return []string{""}
	}
	candidates := []string{strings.Join(ancestors, chromeTrailSeparator)}
	for retained := len(ancestors) - 1; retained >= 0; retained-- {
		leftCount := retained / 2
		rightCount := retained - leftCount
		parts := make([]string, 0, retained+1)
		parts = append(parts, ancestors[:leftCount]...)
		parts = append(parts, ellipsis)
		if rightCount > 0 {
			parts = append(parts, ancestors[len(ancestors)-rightCount:]...)
		}
		candidate := strings.Join(parts, chromeTrailSeparator)
		if candidate != candidates[len(candidates)-1] {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func (r chromeRail) leftItems() []string {
	items := make([]string, 0, 3)
	for _, item := range []string{r.context, r.server, r.trail} {
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

func (r chromeRail) width() int {
	items := r.leftItems()
	leftWidth := 0
	for _, item := range items {
		leftWidth += lipgloss.Width(item)
	}
	if len(items) > 1 {
		leftWidth += lipgloss.Width(chromeGap) * (len(items) - 1)
	}
	gapWidth := 0
	if len(items) > 0 && r.leaf != "" {
		gapWidth = lipgloss.Width(chromeGap)
	}
	return 2 + leftWidth + gapWidth + lipgloss.Width(r.leaf)
}

func (r chromeRail) segments(width int, activeContext lipgloss.Style) []string {
	items := r.leftItems()
	segments := make([]string, 0, len(items)*2+4)
	segments = append(segments, " ")
	for i, item := range items {
		if i > 0 {
			segments = append(segments, chromeGap)
		}
		if r.context != "" && i == 0 {
			item = activeContext.Render(item)
		}
		segments = append(segments, item)
	}
	if len(items) > 0 && r.leaf != "" {
		gapWidth := lipgloss.Width(chromeGap)
		if width > 0 {
			gapWidth += width - r.width()
		}
		segments = append(segments, strings.Repeat(" ", max(0, gapWidth)))
	}
	if r.leaf != "" {
		segments = append(segments, r.leaf)
	}
	segments = append(segments, " ")
	return segments
}

func (r *chromeRail) fit(width int, ellipsis string) {
	if width <= 0 {
		return
	}

	// Exhaust each lower-priority segment before shrinking the next one: trail,
	// leaf target, server, context, then the leaf's operation suffix.
	minimumSegmentWidth := max(1, lipgloss.Width(ellipsis))
	minimumContextWidth := min(lipgloss.Width(r.context), minimumSegmentWidth+2)
	r.trail = ""
	if r.width() <= width {
		return
	}
	r.leaf = shrinkChromeLeaf(r.leaf, r.width()-width, minimumSegmentWidth, ellipsis)
	if r.width() <= width {
		return
	}
	r.server = compactServer(r.server, max(minimumSegmentWidth, lipgloss.Width(r.server)-(r.width()-width)), ellipsis)
	if r.width() <= width {
		return
	}
	r.context = shrinkChromeText(r.context, r.width()-width, minimumContextWidth, ellipsis)
	if r.width() > width {
		r.leaf = shrinkChromeText(r.leaf, r.width()-width, 1, ellipsis)
	}
}

func shrinkChromeText(text string, excess, minimumWidth int, ellipsis string) string {
	if excess <= 0 || text == "" {
		return text
	}
	budget := max(minimumWidth, lipgloss.Width(text)-excess)
	return middleElideLine(text, budget, ellipsis)
}

func shrinkChromeLeaf(title string, excess, minimumTargetWidth int, ellipsis string) string {
	if excess <= 0 || title == "" {
		return title
	}
	target, suffix := chromeLeafParts(title)
	suffixWidth := lipgloss.Width(suffix)
	minimumWidth := suffixWidth + min(minimumTargetWidth, lipgloss.Width(target))
	budget := max(minimumWidth, lipgloss.Width(title)-excess)
	if suffix == "" {
		return middleElideLine(title, budget, ellipsis)
	}
	return middleElideLine(target, max(1, budget-suffixWidth), ellipsis) + suffix
}

func chromeLeafParts(title string) (string, string) {
	for _, suffix := range []string{
		" (new)",
		" (delete)",
		" (conflict)",
		" (edit all)",
		" (create)",
		" (edit)",
		" (file)",
		" (hex)",
		" (new key)",
	} {
		if strings.HasSuffix(title, suffix) {
			return strings.TrimSuffix(title, suffix), suffix
		}
	}
	return title, ""
}

type hintGroup struct {
	key       string
	desc      string
	protected bool
}

func withNavigationHint(top screen, stackDepth int, km *keyMaps) []hintGroup {
	hints := top.Hints()
	groups := hintGroups(hints)
	if hints.status != "" {
		return groups
	}

	for _, group := range groups {
		if group.protected {
			return appendHelpGroup(groups, hints.showHelp, km)
		}
	}

	var escapeHint bubbleskey.Binding
	if top.WantsEsc() {
		escapeHint = hintDesc(km.global.Back, "cancel")
	} else if stackDepth > 1 {
		escapeHint = hintDesc(km.global.Back, "back")
	}
	if escapeHint.Enabled() {
		groups = append(groups, bindingHintGroup(escapeHint))
	}
	return appendHelpGroup(groups, hints.showHelp, km)
}

func hintGroups(hints footerHints) []hintGroup {
	if hints.status != "" {
		key, desc, _ := strings.Cut(hints.status, " ")
		return []hintGroup{{key: key, desc: desc}}
	}
	groups := make([]hintGroup, 0, len(hints.bindings))
	for _, binding := range hints.bindings {
		if binding.Enabled() {
			groups = append(groups, bindingHintGroup(binding))
		}
	}
	return groups
}

func bindingHintGroup(binding bubbleskey.Binding) hintGroup {
	help := binding.Help()
	return hintGroup{key: help.Key, desc: help.Desc, protected: slices.Contains(binding.Keys(), "esc")}
}

func appendHelpGroup(groups []hintGroup, show bool, km *keyMaps) []hintGroup {
	if !show || !km.global.Help.Enabled() {
		return groups
	}
	group := bindingHintGroup(km.global.Help)
	group.protected = true
	return append(groups, group)
}

func renderFooterBar(st *styles, groups []hintGroup, width int) string {
	for width > 0 && hintGroupsWidth(groups) > width {
		removeAt := -1
		for i := len(groups) - 1; i >= 0; i-- {
			if i == 0 || groups[i].protected {
				continue
			}
			removeAt = i
			break
		}
		if removeAt < 0 {
			break
		}
		groups = append(groups[:removeAt], groups[removeAt+1:]...)
	}
	if width > 0 && len(groups) > 0 && hintGroupsWidth(groups) > width {
		otherWidth := hintGroupsWidth(groups[1:])
		if len(groups) > 1 {
			otherWidth += lipgloss.Width(chromeGap)
		}
		truncated := truncateLine(groups[0].key+" "+groups[0].desc, max(1, width-otherWidth), st.glyphs.ellipsis)
		groups[0].key, groups[0].desc, _ = strings.Cut(truncated, " ")
	}
	segments := make([]string, len(groups))
	for i, group := range groups {
		segments[i] = renderHintGroup(st, group)
	}
	bar := strings.Join(segments, st.footer.Inline(true).Render(chromeGap))
	if width > 0 {
		if remaining := width - lipgloss.Width(bar); remaining > 0 {
			bar += strings.Repeat(" ", remaining)
		}
	}
	return bar
}

// renderHintGroup brightens the key so the footer scans as key-then-action
// instead of a uniform muted line. Groups whose key was cut mid-token by the
// width fallback simply render entirely muted.
func renderHintGroup(st *styles, group hintGroup) string {
	if group.desc == "" {
		return st.footerKey.Inline(true).Render(group.key)
	}
	return st.footerKey.Inline(true).Render(group.key) + " " + st.footer.Inline(true).Render(group.desc)
}

func hintGroupsWidth(groups []hintGroup) int {
	width := 0
	for _, group := range groups {
		width += lipgloss.Width(group.key)
		if group.desc != "" {
			width += 1 + lipgloss.Width(group.desc)
		}
	}
	if len(groups) > 1 {
		width += lipgloss.Width(chromeGap) * (len(groups) - 1)
	}
	return width
}

func renderBar(style lipgloss.Style, width int, segments ...string) string {
	rendered := make([]string, len(segments))
	for i, segment := range segments {
		rendered[i] = style.Inline(true).Render(segment)
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
	if width > 0 {
		remaining := width - lipgloss.Width(bar)
		if remaining > 0 {
			fill := style.Inline(true).Render(strings.Repeat(" ", remaining))
			bar = lipgloss.JoinHorizontal(lipgloss.Top, bar, fill)
		}
	}
	return bar
}
