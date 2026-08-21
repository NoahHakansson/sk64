package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/filepicker"
	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	diffpkg "github.com/NoahHakansson/sk64/internal/diff"
	"github.com/NoahHakansson/sk64/internal/k8s"
)

const (
	maxValueSize               = 1 << 20
	exportFilePickerChromeRows = 8
	importFilePickerChromeRows = 12
)

type fileMode int

const (
	fileExport fileMode = iota
	fileImport
)

type filePromptPhase int

const (
	filePhasePick filePromptPhase = iota
	filePhaseName
	filePhaseGate
	filePhaseDone
)

type fileNameState int

const (
	fileNameInvalid fileNameState = iota
	fileNameExists
	fileNameNew
)

type filePromptScreen struct {
	dialog
	ctx              context.Context
	client           *k8s.Client
	env              editEnv
	km               filePromptKeyMap
	res              k8s.Resource
	key              string
	mode             fileMode
	phase            filePromptPhase
	picker           filepicker.Model
	startDir         string
	dir              string
	dirRowFocused    bool
	stat             func(string) (fs.FileInfo, error)
	input            textinput.Model
	gate             confirmGate
	message          string
	messageIsError   bool
	messageIsWarning bool
}

func newFilePrompt(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, keyName string, mode fileMode, st *styles) *filePromptScreen {
	startDir, err := os.Getwd()
	if err != nil {
		startDir = "."
	}
	if absolute, absoluteErr := filepath.Abs(startDir); absoluteErr == nil {
		startDir = absolute
	}

	input := newTextInput(st)
	input.Prompt = "name: "
	input.SetValue(sanitizePathName(keyName))

	picker := filepicker.New()
	picker.KeyMap = env.keymaps().filePicker
	picker.CurrentDirectory = startDir
	picker.FileAllowed = mode == fileImport
	picker.DirAllowed = false
	picker.ShowSize = mode == fileImport
	picker.ShowPermissions = false
	picker.ShowHidden = true
	picker.AutoHeight = false
	picker.Cursor = st.glyphs.cursorMarker
	picker.Styles = newFilePickerStyles(st, mode == fileExport)

	danger := mode == fileExport && res.Kind() == k8s.KindSecret
	return &filePromptScreen{
		dialog:   newDialog(st, danger),
		ctx:      ctx,
		client:   client,
		env:      env,
		km:       env.keymaps().filePrompt,
		res:      res,
		key:      keyName,
		mode:     mode,
		picker:   picker,
		startDir: startDir,
		stat:     os.Lstat,
		input:    input,
		gate:     newConfirmGate(st),
	}
}

func sanitizePathName(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, value)
}

func (s *filePromptScreen) Init() tea.Cmd { return s.picker.Init() }

func (s *filePromptScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch s.phase {
	case filePhasePick:
		return s.updatePicker(msg)
	case filePhaseName:
		return s.updateName(msg)
	case filePhaseGate:
		return s.updateGate(msg)
	case filePhaseDone:
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok && bubbleskey.Matches(keyMsg, s.km.Cancel) {
			return s, popScreen()
		}
	}
	return s, nil
}

func (s *filePromptScreen) updatePicker(msg tea.Msg) (screen, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyPressMsg)
	if isKey {
		if bubbleskey.Matches(keyMsg, s.km.Cancel) {
			return s, popScreen()
		}

		if bubbleskey.Matches(keyMsg, s.picker.KeyMap.Open) {
			path, isDir, err := s.descendTarget()
			if err != nil {
				s.setCannotOpen(path, err)
				return s, nil
			}
			if s.mode == fileExport && path != "" && !isDir {
				s.setMessage("pick a directory"+s.styles.glyphs.separator+"s exports into this directory", false, true)
				return s, nil
			}
			if isDir {
				if _, err := os.ReadDir(path); err != nil {
					s.setCannotOpen(path, err)
					return s, nil
				}
			}
		}

		if bubbleskey.Matches(keyMsg, s.picker.KeyMap.Back) {
			path := filepath.Dir(s.picker.CurrentDirectory)
			if _, err := os.ReadDir(path); err != nil {
				s.setCannotOpen(path, err)
				return s, nil
			}
		}

		if s.mode == fileExport && keyMsg.String() == "s" {
			return s.enterNamePhase(s.picker.CurrentDirectory)
		}
	}

	var cmd tea.Cmd
	s.picker, cmd = s.picker.Update(msg)
	if !isKey || s.mode != fileImport {
		return s, cmd
	}
	if ok, path := s.picker.DidSelectFile(msg); ok {
		return s, tea.Batch(cmd, s.importFile(path))
	}
	return s, cmd
}

func (s *filePromptScreen) descendTarget() (string, bool, error) {
	path := s.picker.HighlightedPath()
	if path == "" {
		return "", false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return path, false, err
	}
	return path, info.IsDir(), nil
}

func (s *filePromptScreen) setCannotOpen(path string, err error) {
	name := filepath.Base(path)
	if name == "." || name == "" {
		name = path
	}
	s.setMessage(s.styles.stateMarker(stateLineError)+" cannot open "+name+": "+err.Error(), true, false)
}

func (s *filePromptScreen) enterNamePhase(dir string) (screen, tea.Cmd) {
	s.phase = filePhaseName
	s.dir = dir
	s.dirRowFocused = false
	s.refreshNameFeedback()
	return s, s.input.Focus()
}

func (s *filePromptScreen) updateName(msg tea.Msg) (screen, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case bubbleskey.Matches(keyMsg, s.km.Cancel):
			s.phase = filePhasePick
			s.input.Blur()
			s.clearMessage()
			return s, nil
		case bubbleskey.Matches(keyMsg, s.km.CycleFocus):
			s.dirRowFocused = !s.dirRowFocused
			if s.dirRowFocused {
				s.input.Blur()
				return s, nil
			}
			return s, s.input.Focus()
		case bubbleskey.Matches(keyMsg, s.km.Accept):
			if s.dirRowFocused {
				s.phase = filePhasePick
				s.input.Blur()
				s.clearMessage()
				return s, nil
			}
			return s.submitName()
		}
	}

	if s.dirRowFocused {
		return s, nil
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	s.refreshNameFeedback()
	return s, cmd
}

func (s *filePromptScreen) submitName() (screen, tea.Cmd) {
	if strings.TrimSpace(s.input.Value()) == "" {
		s.setMessage("enter a name first", false, false)
		return s, nil
	}
	if s.classifyName() != fileNameNew {
		return s, nil
	}
	s.phase = filePhaseGate
	s.input.Blur()
	return s, s.gate.arm()
}

func (s *filePromptScreen) updateGate(msg tea.Msg) (screen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	if bubbleskey.Matches(keyMsg, s.km.Cancel) {
		s.phase = filePhaseName
		s.refreshNameFeedback()
		return s, s.input.Focus()
	}
	confirmed, cmd := s.gate.handleKey(keyMsg)
	if !confirmed {
		return s, cmd
	}
	s.export()
	if s.phase == filePhaseName {
		return s, s.input.Focus()
	}
	return s, nil
}

func (s *filePromptScreen) refreshNameFeedback() {
	s.classifyName()
}

func (s *filePromptScreen) classifyName() fileNameState {
	name := strings.TrimSpace(s.input.Value())
	if name == "" {
		s.clearMessage()
		return fileNameInvalid
	}
	if strings.Contains(name, "/") {
		s.setMessage("name must not contain /", false, true)
		return fileNameInvalid
	}
	_, err := s.stat(filepath.Join(s.dir, name))
	switch {
	case err == nil:
		s.setExistsFeedback()
		return fileNameExists
	case errors.Is(err, fs.ErrNotExist):
		s.setNewFileFeedback()
		return fileNameNew
	default:
		s.setMessage("error: inspect export: "+err.Error(), true, false)
		return fileNameInvalid
	}
}

func (s *filePromptScreen) setNewFileFeedback() {
	s.setMessage(s.styles.stateMarker(stateLineSuccess)+" file is new", false, false)
}

func (s *filePromptScreen) setExistsFeedback() {
	s.setMessage(s.styles.glyphs.warnMarker+" file exists"+s.styles.glyphs.separator+"export never overwrites", false, true)
}

func (s *filePromptScreen) setMessage(message string, isError, isWarning bool) {
	s.message = message
	s.messageIsError = isError
	s.messageIsWarning = isWarning
}

func (s *filePromptScreen) clearMessage() {
	s.setMessage("", false, false)
}

func (s *filePromptScreen) export() {
	// Local-path I/O is deliberately synchronous; unlike cluster calls, it does not warrant loader machinery.
	path := filepath.Join(s.dir, strings.TrimSpace(s.input.Value()))
	value, err := s.res.Get(s.key)
	if err != nil {
		s.phase = filePhaseName
		s.setMessage(fmt.Sprintf("error: %v", err), true, false)
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- exporting to the user-selected path is the feature.
	if err != nil {
		s.phase = filePhaseName
		if errors.Is(err, fs.ErrExist) {
			s.setMessage("file exists"+s.styles.glyphs.separator+"choose another name", false, true)
		} else {
			s.setMessage(fmt.Sprintf("error: export to %q: %v", path, err), true, false)
		}
		return
	}
	written, writeErr := file.Write(value)
	closeErr := file.Close()
	if writeErr == nil && written != len(value) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		s.phase = filePhaseName
		s.setMessage(fmt.Sprintf("error: write export %q: %v", path, writeErr), true, false)
		return
	}
	if closeErr != nil {
		s.phase = filePhaseName
		s.setMessage(fmt.Sprintf("error: close export %q: %v", path, closeErr), true, false)
		return
	}
	s.phase = filePhaseDone
	s.setMessage(fmt.Sprintf("exported %d bytes to %s", len(value), path), false, false)
}

func (s *filePromptScreen) importFile(path string) tea.Cmd {
	// Local-path I/O is deliberately synchronous and imports are capped at 1 MiB, so loader machinery is unnecessary.
	info, err := os.Stat(path)
	if err != nil {
		s.setMessage(fmt.Sprintf("error: inspect import %q: %v", path, err), true, false)
		return nil
	}
	if info.Size() > maxValueSize {
		s.setMessage(fmt.Sprintf("file is %s, exceeds the 1 MiB Secret/ConfigMap value limit", diffpkg.HumanSize(int(info.Size()))), false, false)
		return nil
	}
	value, err := os.ReadFile(path) // #nosec G304 -- importing from a user-selected path is the intended feature.
	if err != nil {
		s.setMessage(fmt.Sprintf("error: read import %q: %v", path, err), true, false)
		return nil
	}
	ctx, client, env, res, keyName, st := s.ctx, s.client, s.env, s.res, s.key, s.styles
	return func() tea.Msg {
		return replaceScreenMsg{s: newEditFlow(ctx, client, env, res, keyName, value, st)}
	}
}

func (s *filePromptScreen) View() string {
	operation := "import"
	if s.mode == fileExport {
		operation = "export"
	}
	content := dialogContent{
		identity:  commitIdentityLines(operation, s.res.Kind(), s.res.Namespace(), s.res.Name(), s.client.Context, s.client.Server, s.contentWidth(), s.styles.glyphs.separator, "key "+s.key),
		message:   s.message,
		isError:   s.messageIsError,
		isWarning: s.messageIsWarning,
	}
	if s.mode == fileExport {
		content.title = "Export " + s.res.Name() + "/" + s.key + " to a file"
		if s.res.Kind() == k8s.KindSecret {
			content.criticalWarnings = []string{"This writes the plaintext secret to disk. sk64 never removes it."}
		}
	} else {
		content.title = "Import a file into " + s.res.Name() + "/" + s.key
	}

	switch s.phase {
	case filePhasePick:
		content.summary = "in " + s.relativePickerDirectory()
		if s.mode == fileImport {
			content.body = []string{
				"replaces the current value of this key",
				"you will see the diff and a dry-run before anything is written",
				"maximum 1 MiB",
			}
		}
		content.prompt = s.pickerPrompt()
	case filePhaseName:
		content.prompt = s.directoryRow() + "\n" + s.input.View()
	case filePhaseGate:
		if value, err := s.res.Get(s.key); err == nil {
			content.body = append(content.body, fmt.Sprintf("write the decoded value, %s, with mode 0600", diffpkg.HumanSize(len(value))))
		}
		content.body = append(content.body, "to "+filepath.Join(s.dir, strings.TrimSpace(s.input.Value())))
		content.prompt = s.gate.promptLines(s.styles, true)
		content.message, content.isWarning, content.isError = s.gate.message, s.gate.message != "", false
	}
	return s.render(content)
}

func (s *filePromptScreen) relativePickerDirectory() string {
	relative, err := filepath.Rel(s.startDir, s.picker.CurrentDirectory)
	if err != nil {
		return s.picker.CurrentDirectory
	}
	return relative
}

func (s *filePromptScreen) pickerPrompt() string {
	view := strings.TrimRight(s.picker.View(), "\n")
	lines := strings.Split(view, "\n")
	for i := range lines {
		lines[i] = truncateLine(lines[i], s.contentWidth(), s.styles.glyphs.ellipsis)
	}
	return strings.Join(lines, "\n")
}

func (s *filePromptScreen) directoryRow() string {
	dir := filepath.Clean(s.dir)
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	gutterWidth := lipgloss.Width(s.styles.glyphs.cursorMarker) + 1
	prefix := "into "
	dir = middleElideLine(dir, max(1, s.contentWidth()-gutterWidth-lipgloss.Width(prefix)), s.styles.glyphs.ellipsis)
	line := prefix + dir
	if s.dirRowFocused {
		return s.styles.cursorText.Render(s.styles.glyphs.cursorMarker) + " " + s.styles.dialogTitle.Render(line)
	}
	return strings.Repeat(" ", gutterWidth) + s.styles.dim.Render(line)
}

func (s *filePromptScreen) SetSize(width, height int) {
	s.resize(width, height)
	s.input.SetWidth(textInputWidth(s.contentWidth(), s.input.Prompt))
	s.gate.setWidth(s.contentWidth())
	chrome := exportFilePickerChromeRows
	if s.mode == fileImport {
		chrome = importFilePickerChromeRows
	}
	s.picker.SetHeight(s.scrollHeight(chrome))
}

func (s *filePromptScreen) SetStyles(st *styles) {
	s.styles = st
	applyTextInputStyles(&s.input, st)
	s.gate.setStyles(st)
	s.picker.Styles = newFilePickerStyles(st, s.mode == fileExport)
	s.picker.Cursor = st.glyphs.cursorMarker
}

func (s *filePromptScreen) Title() string { return s.res.Name() + "/" + s.key + " (file)" }

func (s *filePromptScreen) Hints() footerHints {
	switch s.phase {
	case filePhasePick:
		if s.mode == fileExport {
			return hintBindings(hintDesc(s.picker.KeyMap.Open, "open"), displayHint("s", "export here"), hintDesc(s.picker.KeyMap.Back, "up"), s.km.Cancel)
		}
		return hintBindings(hintDesc(s.picker.KeyMap.Select, "pick"), hintDesc(s.picker.KeyMap.Back, "up"), s.km.Cancel)
	case filePhaseName:
		if s.dirRowFocused {
			return hintBindings(hintDesc(s.km.Accept, "re-pick dir"), hintDesc(s.km.CycleFocus, "name"), s.km.Cancel)
		}
		return hintBindings(hintDesc(s.km.Accept, "export"), hintDesc(s.km.CycleFocus, "dir"), s.km.Cancel)
	case filePhaseGate:
		return hintBindings(displayHint("YES", "confirm"), s.km.Cancel)
	default:
		return hintBindings(s.km.Cancel)
	}
}

func (s *filePromptScreen) Help() helpGroup { return helpGroup{} }

func (s *filePromptScreen) CapturesInput() bool { return true }
func (s *filePromptScreen) WantsEsc() bool      { return true }
