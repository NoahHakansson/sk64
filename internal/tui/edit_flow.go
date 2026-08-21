package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"

	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	diffpkg "github.com/NoahHakansson/sk64/internal/diff"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/natsort"
	"github.com/NoahHakansson/sk64/internal/resyaml"
	"github.com/NoahHakansson/sk64/internal/undo"
	"github.com/charmbracelet/x/ansi"
)

type editPhase int

const (
	phaseEditing editPhase = iota
	phaseEditorFailed
	phaseDiff
	phaseDryRun
	phaseDryRunRejected
	phaseDryRunUnsupported
	phaseSaving
	phaseConflict
	phaseForbidden
	phaseParseFailed
	phaseBinaryCollision
	phaseValidateWarn
	phaseSaved
	phaseRollingOut
	phaseRolloutDone
	phaseCommitGate
	phaseRolloutGate
)

type flowTarget int

const (
	targetKey flowTarget = iota
	targetNewKey
	targetDeleteKey
	targetResource
	targetCreate
)

type editFlow struct {
	loader
	radiusLoader loader
	dialog
	ctx                  context.Context
	client               *k8s.Client
	env                  editEnv
	km                   editFlowKeyMap
	res                  k8s.Resource
	key                  string
	original             []byte
	edited               []byte
	target               flowTarget
	originalMap          map[string]string
	editedMap            map[string]string
	binaryKeys           []resyaml.BinaryKey
	rawDoc               []byte
	touched              []string
	warnings             []k8s.Warning
	applied              bool
	proposedMode         bool
	phase                editPhase
	dir                  *editor.Dir
	filePath             string
	seeded               editor.Fingerprint
	editorSeededOriginal bool
	editorReturnPhase    editPhase
	editorReturnMessage  string
	session              *editor.Session
	message              string
	nudge                bool
	wrap                 bool
	viewport             viewport.Model
	spinner              spinner.Model
	radius               *k8s.RefIndex
	radiusSummary        blastRadius
	radiusErr            error
	radiusKind           string
	radiusName           string
	rollout              []rolloutItem
	rolloutList          list.Model
	rolloutResults       []rolloutResult
	saveOperationID      uint64
	gate                 confirmGate
	gateSkippedDryRun    bool
}

type rolloutItem struct {
	kind     string
	name     string
	selected bool
}

type rolloutChecklistItem struct {
	item *rolloutItem
}

func (i rolloutChecklistItem) Title() string {
	checkbox := "[ ] "
	if i.item.selected {
		checkbox = "[x] "
	}
	return checkbox + i.item.kind + "/" + i.item.name
}
func (i rolloutChecklistItem) Description() string { return "" }
func (i rolloutChecklistItem) FilterValue() string { return i.item.kind + "/" + i.item.name }

type rolloutResult struct {
	kind string
	name string
	err  error
}

var lastSaveOperationID atomic.Uint64

type savedResolution int

const (
	savedChecking savedResolution = iota
	savedUnavailable
	savedNothingToRestart
	savedIncompleteRestartOffer
	savedRestartOffer
)

func (r savedResolution) offersRestart() bool {
	return r == savedIncompleteRestartOffer || r == savedRestartOffer
}

func newEditFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, key string, proposed []byte, st *styles) *editFlow {
	flow := newBaseEditFlow(ctx, client, env, res, st)
	flow.key = key
	var err error
	flow.original, err = readFlowValue(res, key)
	if err != nil {
		flow.message = fmt.Sprintf("read original value: %v", err)
		flow.phase = phaseEditorFailed
		flow.refreshContent()
		return flow
	}
	flow.proposedMode = proposed != nil
	if proposed != nil {
		flow.edited = bytes.Clone(proposed)
		flow.phase = phaseDiff
		flow.refreshContent()
	}
	return flow
}

func newKeyAddFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, key string, st *styles) *editFlow {
	flow := newBaseEditFlow(ctx, client, env, res, st)
	flow.target = targetNewKey
	flow.key = key
	return flow
}

func newKeyRestoreFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, key string, value []byte, st *styles) *editFlow {
	flow := newKeyAddFlow(ctx, client, env, res, key, st)
	flow.proposedMode = true
	flow.edited = bytes.Clone(value)
	flow.phase = phaseDiff
	flow.refreshContent()
	return flow
}

func newKeyDeleteFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, key string, st *styles) *editFlow {
	flow := newBaseEditFlow(ctx, client, env, res, st)
	flow.target = targetDeleteKey
	flow.key = key
	flow.proposedMode = true
	var err error
	flow.original, err = readFlowValue(res, key)
	if err != nil {
		flow.message = fmt.Sprintf("read original value: %v", err)
		flow.phase = phaseEditorFailed
	} else {
		flow.phase = phaseDiff
	}
	flow.refreshContent()
	return flow
}

func newResourceEditFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, st *styles) *editFlow {
	flow := newBaseEditFlow(ctx, client, env, res, st)
	flow.target = targetResource
	var err error
	flow.originalMap, flow.binaryKeys, err = resyaml.FromResource(res)
	if err != nil {
		flow.message = fmt.Sprintf("read resource values: %v", err)
		flow.phase = phaseEditorFailed
		flow.refreshContent()
	}
	return flow
}

func newResourceCreateFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, st *styles) *editFlow {
	flow := newBaseEditFlow(ctx, client, env, res, st)
	flow.target = targetCreate
	flow.originalMap = map[string]string{}
	return flow
}

func newResourceRevertFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, set map[string]string, remove []string, st *styles) *editFlow {
	flow := newResourceEditFlow(ctx, client, env, res, st)
	flow.proposedMode = true
	if flow.phase == phaseEditorFailed {
		return flow
	}
	flow.editedMap = maps.Clone(flow.originalMap)
	for key, value := range set {
		flow.editedMap[key] = value
	}
	for _, key := range remove {
		delete(flow.editedMap, key)
	}
	flow.phase = phaseDiff
	flow.refreshContent()
	return flow
}

func newBaseEditFlow(ctx context.Context, client *k8s.Client, env editEnv, res k8s.Resource, st *styles) *editFlow {
	rolloutList := newListModel(st, env.keymaps().list)
	rolloutList.SetFilteringEnabled(false)
	rolloutList.SetShowTitle(false)
	rolloutList.SetShowPagination(true)
	viewportModel := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	viewportModel.KeyMap = env.keymaps().viewport
	return &editFlow{
		dialog:      newDialog(st, false),
		ctx:         ctx,
		client:      client,
		env:         env,
		km:          env.keymaps().editFlow,
		res:         res,
		phase:       phaseEditing,
		viewport:    viewportModel,
		rolloutList: rolloutList,
		spinner:     newSpinner(st),
		gate:        newConfirmGate(st),
	}
}

func readFlowValue(res k8s.Resource, key string) ([]byte, error) {
	value, err := res.Get(key)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(value), nil
}

func (s *editFlow) Init() tea.Cmd {
	if s.phase == phaseEditorFailed {
		return nil
	}
	if s.proposedMode {
		if (s.target == targetResource && maps.Equal(s.originalMap, s.editedMap)) || (s.target == targetKey && bytes.Equal(s.original, s.edited)) {
			s.cancelAndCleanup()
			return popScreen()
		}
		return s.startBlastRadius()
	}
	if s.target == targetCreate {
		return s.openEditor()
	}
	return tea.Batch(s.openEditor(), s.startBlastRadius())
}

func (s *editFlow) startBlastRadius() tea.Cmd {
	ctx, reqID := s.radiusLoader.start(s.ctx)
	namespace := s.res.Namespace()
	s.radiusKind = s.res.Kind()
	s.radiusName = s.res.Name()
	s.radius = nil
	s.radiusSummary = blastRadius{}
	s.radiusErr = nil
	s.refreshContent()
	collect := func() tea.Msg {
		index, err := s.client.CollectNamespaceRefs(ctx, namespace)
		return blastRadiusMsg{reqID: reqID, index: index, err: err}
	}
	return tea.Batch(collect, s.spinner.Tick)
}

func (s *editFlow) openEditor() tea.Cmd {
	var content []byte
	seededOriginal := false
	fileKey := s.key
	if s.mapTarget() {
		fileKey = "resource.yaml"
		if s.rawDoc != nil {
			content = bytes.Clone(s.rawDoc)
		} else {
			values := s.originalMap
			if s.editedMap != nil {
				values = s.editedMap
			} else {
				seededOriginal = true
			}
			var err error
			content, err = resyaml.Serialize(resourceHeader(s.res), values, s.binaryKeys, s.styles.glyphs.separator)
			if err != nil {
				s.editorFailure(fmt.Errorf("serialize editor document: %w", err))
				return nil
			}
		}
	} else {
		content = s.original
		if s.edited != nil {
			content = s.edited
		} else {
			seededOriginal = true
		}
	}
	if s.dir == nil {
		dir, err := editor.NewDir()
		if err != nil {
			s.editorFailure(err)
			return nil
		}
		s.dir = dir
	}
	path, err := s.dir.WriteFile(s.res.Kind(), s.res.Name(), fileKey, content)
	if err != nil {
		s.editorFailure(err)
		return nil
	}
	s.filePath = path
	s.seeded = editor.FingerprintBytes(content)
	s.editorSeededOriginal = seededOriginal
	s.editorReturnPhase = s.phase
	s.editorReturnMessage = s.message
	argv, err := editor.BuildArgv(s.env.editorFlag, os.Getenv("EDITOR"), path)
	if err != nil {
		s.editorFailure(err)
		return nil
	}
	s.session, err = editor.NewSession(argv)
	if err != nil {
		s.editorFailure(err)
		return nil
	}
	s.phase = phaseEditing
	s.message = ""
	return tea.Exec(s.session, func(err error) tea.Msg { return editorFinishedMsg{err: err} })
}

func resourceHeader(res k8s.Resource) resyaml.Header {
	return resyaml.Header{Kind: res.Kind(), Name: res.Name(), Namespace: res.Namespace(), ResourceVersion: res.ResourceVersion()}
}

func (s *editFlow) editorFailure(err error) {
	s.phase = phaseEditorFailed
	s.message = err.Error()
	s.refreshContent()
}

func (s *editFlow) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch msg := msg.(type) {
	case blastRadiusMsg:
		if !s.radiusLoader.finish(msg.reqID) {
			return s, nil
		}
		s.radius = msg.index
		s.radiusSummary = summarizeBlastRadius(msg.index, s.radiusKind, s.radiusName)
		s.radiusErr = msg.err
		if s.phase == phaseSaved {
			s.rollout = s.rolloutCandidates()
			items := s.rolloutChecklistItems()
			setItems := s.rolloutList.SetItems(items)
			s.rolloutList.Select(0)
			s.refreshContent()
			return s, tea.Batch(setItems, s.savedNoticeCmd(saveNoticeForResolution(s.savedResolution()), true, true))
		}
		s.refreshContent()
		return s, nil
	case editorFinishedMsg:
		return s.finishEditor(msg)
	case dryRunDoneMsg:
		if s.phase != phaseDryRun || !s.finish(msg.reqID) {
			return s, nil
		}
		s.message = msg.result.Message
		switch msg.result.Outcome {
		case k8s.DryRunOK:
			s.phase = phaseCommitGate
			s.gateSkippedDryRun = false
			s.refreshContent()
			return s, s.gate.arm()
		case k8s.DryRunRejected:
			s.phase = phaseDryRunRejected
		case k8s.DryRunUnsupported:
			s.phase = phaseDryRunUnsupported
		case k8s.DryRunConflict:
			s.enterConflict(msg.result.Cluster)
		case k8s.DryRunFailed:
			s.phase = phaseDiff
		}
		s.refreshContent()
		return s, nil
	case saveDoneMsg:
		if s.phase != phaseSaving || !s.finish(msg.reqID) {
			return s, nil
		}
		s.message = msg.result.Message
		switch msg.result.Outcome {
		case k8s.SaveSucceeded:
			if s.target != targetCreate {
				s.env.ring.Push(s.undoEntry())
			}
			s.applied = false
			s.cleanup()
			s.edited, s.editedMap, s.rawDoc = nil, nil, nil
			if s.target == targetCreate {
				outcome := resourceOutcome{
					verb:      outcomeCreated,
					kind:      s.res.Kind(),
					namespace: s.res.Namespace(),
					name:      s.res.Name(),
				}
				return s, tea.Batch(popScreen(), func() tea.Msg {
					return resourceListChangedMsg{namespace: outcome.namespace, outcome: outcome}
				})
			}
			s.saveOperationID = lastSaveOperationID.Add(1)
			if s.env.readOnly {
				saved := s.savedNoticeCmd(saveNoticeForResolution(s.savedResolution()), false, true)
				s.radiusLoader.stop()
				return s, tea.Batch(saved, popScreen())
			}
			s.phase = phaseSaved
			s.rollout = nil
			setItems := s.rolloutList.SetItems(nil)
			saved := s.savedNoticeCmd(saveNoticeComplete, false, false)
			consumerCheck := s.startBlastRadius()
			return s, tea.Batch(setItems, saved, consumerCheck)
		case k8s.SaveConflict:
			s.enterConflict(msg.result.Cluster)
		case k8s.SaveForbidden:
			s.phase = phaseForbidden
		case k8s.SaveFailed:
			s.phase = phaseDiff
		}
		s.refreshContent()
		return s, nil
	case rolloutDoneMsg:
		if s.phase != phaseRollingOut || !s.finish(msg.reqID) {
			return s, nil
		}
		s.rolloutResults = msg.results
		s.phase = phaseRolloutDone
		s.refreshContent()
		return s, nil
	case spinner.TickMsg:
		if !s.radiusLoader.pending && s.phase != phaseDryRun && s.phase != phaseSaving && s.phase != phaseRollingOut {
			return s, nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	case tea.KeyPressMsg:
		// The nudge lives in the diff header, so the viewport reclaims its row
		// when the next key is processed.
		if s.nudge {
			s.nudge = false
			s.layoutViewport()
		}
		if s.phase == phaseCommitGate || s.phase == phaseRolloutGate {
			return s.updateGateKey(msg)
		}
		cmd, consumed := s.updateKey(msg)
		if consumed {
			return s, cmd
		}
	}

	if s.diffPhase() {
		var cmd tea.Cmd
		s.viewport, cmd = s.viewport.Update(msg)
		return s, cmd
	}
	if s.phase == phaseSaved && s.savedResolution().offersRestart() {
		if wheel, ok := msg.(tea.MouseWheelMsg); ok {
			switch wheel.Button {
			case tea.MouseWheelUp:
				s.rolloutList.CursorUp()
			case tea.MouseWheelDown:
				s.rolloutList.CursorDown()
			}
			return s, nil
		}
	}
	if s.phase == phaseRolloutDone {
		var cmd tea.Cmd
		s.viewport, cmd = s.viewport.Update(msg)
		return s, cmd
	}
	return s, nil
}

func (s *editFlow) finishEditor(msg editorFinishedMsg) (screen, tea.Cmd) {
	if msg.err != nil && !errors.Is(msg.err, exec.ErrWaitDelay) {
		s.message = msg.err.Error()
		if s.session != nil && s.session.Stderr() != "" {
			s.message += "\n" + s.session.Stderr()
		}
		s.phase = phaseEditorFailed
		s.refreshContent()
		return s, nil
	}
	value, err := os.ReadFile(s.filePath)
	if err != nil {
		s.editorFailure(fmt.Errorf("read edited value %q: %w", s.filePath, err))
		return s, nil
	}
	if editor.FingerprintBytes(value) == s.seeded {
		if !s.editorSeededOriginal {
			s.phase = s.editorReturnPhase
			s.message = s.editorReturnMessage
			s.refreshContent()
			return s, nil
		}
		s.restoreOriginal()
		s.cancelAndCleanup()
		return s, popScreen()
	}
	if s.mapTarget() {
		s.env.log.Resource("edit-resource", s.res.Kind(), s.res.Namespace(), s.res.Name())
	} else {
		s.env.log.Key("edit", s.res.Kind(), s.res.Namespace(), s.res.Name(), s.key, len(value))
	}
	if s.mapTarget() {
		s.rawDoc = bytes.Clone(value)
		parsed, err := resyaml.Parse(value)
		if err != nil {
			s.phase = phaseParseFailed
			s.message = err.Error()
			s.refreshContent()
			return s, nil
		}
		if name := binaryCollision(parsed, s.binaryKeys); name != "" {
			s.phase = phaseBinaryCollision
			s.message = fmt.Sprintf("key %q is binary and omitted from this document; edit it via import (i) instead", name)
			s.refreshContent()
			return s, nil
		}
		s.editedMap = parsed
		if s.target != targetCreate && maps.Equal(s.originalMap, s.editedMap) {
			s.restoreOriginal()
			s.cancelAndCleanup()
			return s, popScreen()
		}
	} else {
		s.edited = editor.NormalizeEditedValue(s.original, value)
		if (s.target == targetNewKey && len(s.edited) == 0) || (s.target == targetKey && bytes.Equal(s.original, s.edited)) {
			s.restoreOriginal()
			s.cancelAndCleanup()
			return s, popScreen()
		}
	}
	s.phase = phaseDiff
	s.message = ""
	s.refreshContent()
	return s, nil
}

// updateGateKey routes keys while a typed commit gate is armed. Esc always
// closes the gate with nothing dispatched: the commit gate returns to the diff
// for further review, and the rollout gate returns to the checklist with the
// selections intact. Everything else feeds the gate's input.
func (s *editFlow) updateGateKey(msg tea.KeyPressMsg) (screen, tea.Cmd) {
	if bubbleskey.Matches(msg, bindEsc) {
		if s.phase == phaseRolloutGate {
			s.phase = phaseSaved
		} else {
			s.phase = phaseDiff
		}
		s.message = ""
		s.refreshContent()
		return s, nil
	}
	confirmed, cmd := s.gate.handleKey(msg)
	if !confirmed {
		return s, cmd
	}
	if s.phase == phaseRolloutGate {
		return s, s.startRollout()
	}
	return s, s.startSaving()
}

func (s *editFlow) updateKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	key := msg.String()
	if (key == "y" || key == "enter") && s.confirmPhase() {
		s.nudge = true
		s.refreshContent()
		return nil, true
	}
	switch s.phase {
	case phaseEditorFailed, phaseDryRunRejected, phaseForbidden, phaseParseFailed, phaseBinaryCollision:
		if bubbleskey.Matches(msg, s.km.Edit) {
			return s.reEdit(), true
		}
		if bubbleskey.Matches(msg, s.km.Cancel) {
			return s.abort(), true
		}
	case phaseDiff:
		if bubbleskey.Matches(msg, s.km.Confirm) {
			return s.confirm(), true
		}
		if bubbleskey.Matches(msg, s.km.Edit) {
			return s.reEdit(), true
		}
		if bubbleskey.Matches(msg, s.km.Wrap) {
			s.toggleWrap()
			return nil, true
		}
		if bubbleskey.Matches(msg, s.km.Cancel) {
			return s.abort(), true
		}
	case phaseValidateWarn:
		if bubbleskey.Matches(msg, s.km.Confirm) {
			return s.startDryRun(), true
		}
		if bubbleskey.Matches(msg, s.km.Edit) {
			return s.reEdit(), true
		}
		if bubbleskey.Matches(msg, s.km.Cancel) {
			return s.abort(), true
		}
	case phaseDryRun:
		if bubbleskey.Matches(msg, s.km.Cancel) {
			s.stop()
			s.phase = phaseDiff
			s.message = ""
			s.refreshContent()
			return nil, true
		}
	case phaseDryRunUnsupported:
		if bubbleskey.Matches(msg, s.km.Confirm) {
			s.phase = phaseCommitGate
			s.gateSkippedDryRun = true
			s.message = ""
			s.refreshContent()
			return s.gate.arm(), true
		}
		if bubbleskey.Matches(msg, s.km.Edit) {
			return s.reEdit(), true
		}
		if bubbleskey.Matches(msg, s.km.Cancel) {
			return s.abort(), true
		}
	case phaseConflict:
		if bubbleskey.Matches(msg, s.km.Confirm) {
			return s.confirm(), true
		}
		if bubbleskey.Matches(msg, s.km.Wrap) {
			s.toggleWrap()
			return nil, true
		}
		if bubbleskey.Matches(msg, s.km.Edit) {
			if s.proposedMode {
				s.phase = phaseDiff
				s.message = ""
				s.refreshContent()
				return nil, true
			}
			return s.openEditor(), true
		}
		if bubbleskey.Matches(msg, s.km.Cancel) {
			return s.abort(), true
		}
	case phaseSaved:
		resolution := s.savedResolution()
		if resolution.offersRestart() {
			switch {
			case bubbleskey.Matches(msg, s.km.RolloutUp):
				s.rolloutList.CursorUp()
				return nil, true
			case bubbleskey.Matches(msg, s.km.RolloutDown):
				s.rolloutList.CursorDown()
				return nil, true
			case bubbleskey.Matches(msg, s.km.RolloutToggle):
				index := s.rolloutList.Index()
				if index >= 0 && index < len(s.rollout) {
					s.rollout[index].selected = !s.rollout[index].selected
				}
				return nil, true
			case bubbleskey.Matches(msg, s.km.RolloutToggleAll):
				selectAll := false
				for _, item := range s.rollout {
					if !item.selected {
						selectAll = true
						break
					}
				}
				for i := range s.rollout {
					s.rollout[i].selected = selectAll
				}
				return nil, true
			case bubbleskey.Matches(msg, s.km.Restart):
				if !s.hasSelectedRollout() {
					notice := s.savedNoticeCmd(saveNoticeRestartSkipped, true, true)
					s.cancelAndCleanup()
					return tea.Batch(notice, popScreen()), true
				}
				s.phase = phaseRolloutGate
				s.message = ""
				s.refreshContent()
				return s.gate.arm(), true
			}
		}
		switch {
		case bubbleskey.Matches(msg, s.km.Cancel):
			var notice tea.Cmd
			switch resolution {
			case savedIncompleteRestartOffer, savedRestartOffer:
				notice = s.savedNoticeCmd(saveNoticeRestartSkipped, true, true)
			case savedChecking:
				notice = s.savedNoticeCmd(saveNoticeConsumerCheckUnavailable, true, true)
			}
			s.cancelAndCleanup()
			if notice != nil {
				return tea.Batch(notice, popScreen()), true
			}
			return popScreen(), true
		case bubbleskey.Matches(msg, s.km.Accept):
			if resolution == savedNothingToRestart || resolution == savedUnavailable {
				s.cancelAndCleanup()
				return popScreen(), true
			}
			return nil, true
		}
	case phaseRolloutDone:
		if bubbleskey.Matches(msg, s.km.Accept, s.km.Cancel) {
			failed := 0
			for _, result := range s.rolloutResults {
				if result.err != nil {
					failed++
				}
			}
			if failed > 0 {
				notice := saveNoticeRestartIncomplete
				if failed == len(s.rolloutResults) {
					notice = saveNoticeRestartFailed
				}
				return tea.Batch(s.savedNoticeCmd(notice, true, true), popScreen()), true
			}
			return popScreen(), true
		}
	}
	return nil, false
}

// confirmPhase reports the phases whose accept key is Y.
func (s *editFlow) confirmPhase() bool {
	switch s.phase {
	case phaseDiff, phaseValidateWarn, phaseDryRunUnsupported, phaseConflict:
		return true
	}
	return false
}

func (s *editFlow) savedResolution() savedResolution {
	if s.radiusLoader.pending {
		return savedChecking
	}
	if s.radiusErr != nil || s.radius == nil {
		return savedUnavailable
	}
	if len(s.rollout) > 0 {
		if len(s.radius.FailedSources()) > 0 {
			return savedIncompleteRestartOffer
		}
		return savedRestartOffer
	}
	if len(s.radius.FailedSources()) > 0 {
		return savedUnavailable
	}
	return savedNothingToRestart
}

func saveNoticeForResolution(resolution savedResolution) saveNotice {
	switch resolution {
	case savedUnavailable:
		return saveNoticeConsumerCheckUnavailable
	case savedNothingToRestart:
		return saveNoticeNoEligibleRestart
	case savedIncompleteRestartOffer:
		return saveNoticeConsumerCheckIncomplete
	default:
		return saveNoticeComplete
	}
}

func (s *editFlow) savedNoticeCmd(notice saveNotice, skipRefresh, final bool) tea.Cmd {
	outcome := resourceOutcome{
		verb:      outcomeSaved,
		kind:      s.res.Kind(),
		namespace: s.res.Namespace(),
		name:      s.res.Name(),
		save:      notice,
	}
	operationID := s.saveOperationID
	return func() tea.Msg {
		return editSavedMsg{operationID: operationID, outcome: outcome, skipRefresh: skipRefresh, final: final}
	}
}

func (s *editFlow) rolloutCandidates() []rolloutItem {
	if s.radius == nil {
		return nil
	}
	consumers := s.radius.ConsumersOf(s.radiusKind, s.radiusName)
	items := make([]rolloutItem, 0, len(consumers))
	for _, consumer := range consumers {
		if consumer.Ref.RolloutNeeded && k8s.RestartableKind(consumer.Kind) {
			items = append(items, rolloutItem{kind: consumer.Kind, name: consumer.Name, selected: true})
		}
	}
	return items
}

func (s *editFlow) rolloutChecklistItems() []list.Item {
	items := make([]list.Item, len(s.rollout))
	for index := range s.rollout {
		items[index] = rolloutChecklistItem{item: &s.rollout[index]}
	}
	return items
}

func (s *editFlow) hasSelectedRollout() bool {
	return s.selectedRolloutCount() > 0
}

func (s *editFlow) selectedRolloutCount() int {
	count := 0
	for _, item := range s.rollout {
		if item.selected {
			count++
		}
	}
	return count
}

func (s *editFlow) selectedRolloutNames() []string {
	names := make([]string, 0, len(s.rollout))
	for _, item := range s.rollout {
		if item.selected {
			names = append(names, item.kind+"/"+item.name)
		}
	}
	return names
}

func (s *editFlow) startRollout() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	items := slices.Clone(s.rollout)
	namespace := s.res.Namespace()
	s.phase = phaseRollingOut
	s.refreshContent()
	return tea.Batch(func() tea.Msg {
		results := make([]rolloutResult, 0, len(items))
		for _, item := range items {
			if !item.selected {
				continue
			}
			err := s.client.RestartWorkload(ctx, item.kind, namespace, item.name)
			results = append(results, rolloutResult{kind: item.kind, name: item.name, err: err})
		}
		return rolloutDoneMsg{reqID: reqID, results: results}
	}, s.spinner.Tick)
}

func (s *editFlow) reEdit() tea.Cmd {
	if s.target == targetDeleteKey || (s.target == targetResource && s.proposedMode) {
		s.phase = phaseDiff
		s.message = ""
		s.refreshContent()
		return nil
	}
	return s.openEditor()
}

// binaryCollision returns the first edited key whose current value on the
// cluster is binary and therefore absent from the editable document.
func binaryCollision(values map[string]string, binaryKeys []resyaml.BinaryKey) string {
	for _, binaryKey := range binaryKeys {
		if _, exists := values[binaryKey.Name]; exists {
			return binaryKey.Name
		}
	}
	return ""
}

func (s *editFlow) confirm() tea.Cmd {
	if s.mapTarget() {
		if name := binaryCollision(s.editedMap, s.binaryKeys); name != "" {
			s.phase = phaseBinaryCollision
			s.message = fmt.Sprintf("key %q is binary on the cluster and cannot be changed from this flow", name)
			s.refreshContent()
			return nil
		}
	}
	if err := s.applyChanges(); err != nil {
		s.message = fmt.Sprintf("prepare edited value: %v", err)
		s.refreshContent()
		return nil
	}
	s.warnings = s.res.Validate()
	if len(s.warnings) != 0 {
		s.phase = phaseValidateWarn
		s.message = ""
		s.refreshContent()
		return nil
	}
	return s.startDryRun()
}

func (s *editFlow) applyChanges() error {
	if s.applied {
		s.restoreOriginal()
	}
	s.touched = nil
	switch s.target {
	case targetKey, targetNewKey:
		if err := s.res.Set(s.key, s.edited); err != nil {
			return fmt.Errorf("set key %q: %w", s.key, err)
		}
		s.touched = []string{s.key}
	case targetDeleteKey:
		if err := s.res.Delete(s.key); err != nil {
			return fmt.Errorf("delete key %q: %w", s.key, err)
		}
		s.touched = []string{s.key}
	case targetResource, targetCreate:
		s.applied = true // A partial failure must still restore keys already mutated below.
		for _, key := range slices.SortedFunc(maps.Keys(s.editedMap), natsort.Compare) {
			value := s.editedMap[key]
			if original, exists := s.originalMap[key]; exists && original == value {
				continue
			}
			if err := s.res.Set(key, []byte(value)); err != nil {
				return fmt.Errorf("set key %q: %w", key, err)
			}
			s.touched = append(s.touched, key)
		}
		for _, key := range slices.SortedFunc(maps.Keys(s.originalMap), natsort.Compare) {
			if _, exists := s.editedMap[key]; exists {
				continue
			}
			if err := s.res.Delete(key); err != nil {
				return fmt.Errorf("delete key %q: %w", key, err)
			}
			s.touched = append(s.touched, key)
		}
	}
	s.applied = true
	return nil
}

func (s *editFlow) startDryRun() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	snapshot := s.res.Clone()
	s.phase = phaseDryRun
	s.message = ""
	s.refreshContent()
	return tea.Batch(func() tea.Msg {
		if s.target == targetCreate {
			return dryRunDoneMsg{reqID: reqID, result: s.client.DryRunCreate(ctx, snapshot)}
		}
		return dryRunDoneMsg{reqID: reqID, result: s.client.DryRunSave(ctx, snapshot)}
	}, s.spinner.Tick)
}

func (s *editFlow) startSaving() tea.Cmd {
	ctx, reqID := s.start(s.ctx)
	snapshot := s.res.Clone()
	s.phase = phaseSaving
	s.refreshContent()
	return tea.Batch(func() tea.Msg {
		if s.target == targetCreate {
			return saveDoneMsg{reqID: reqID, result: s.client.Create(ctx, snapshot)}
		}
		return saveDoneMsg{reqID: reqID, result: s.client.Save(ctx, snapshot)}
	}, s.spinner.Tick)
}

func (s *editFlow) enterConflict(cluster k8s.Resource) {
	// Create and dry-run create never return conflict outcomes.
	s.restoreOriginal()
	if cluster == nil {
		s.phase = phaseDiff
		s.message = "conflict response did not include the current resource"
		return
	}
	if s.mapTarget() {
		values, binaryKeys, err := resyaml.FromResource(cluster)
		if err != nil {
			s.phase = phaseDiff
			s.message = fmt.Sprintf("read resource values: %v", err)
			return
		}
		s.res = cluster
		s.originalMap = values
		s.binaryKeys = binaryKeys
		s.rawDoc = nil
		if name := binaryCollision(s.editedMap, binaryKeys); name != "" {
			s.phase = phaseBinaryCollision
			s.message = fmt.Sprintf("key %q became binary on the cluster during this edit; remove or rename it, or edit it via import (i) instead", name)
			return
		}
	} else {
		s.res = cluster
		// A concurrently deleted key is intentionally represented as an empty original value.
		s.original, _ = cluster.Get(s.key)
		s.original = bytes.Clone(s.original)
	}
	s.phase = phaseConflict
}

func (s *editFlow) abort() tea.Cmd {
	s.restoreOriginal()
	s.cancelAndCleanup()
	return popScreen()
}

func (s *editFlow) restoreOriginal() {
	if !s.applied {
		return
	}
	switch s.target {
	case targetKey, targetDeleteKey:
		_ = s.res.Set(s.key, s.original)
	case targetNewKey:
		_ = s.res.Delete(s.key)
	case targetResource, targetCreate:
		for _, key := range s.touched {
			if value, exists := s.originalMap[key]; exists {
				_ = s.res.Set(key, []byte(value))
			} else {
				_ = s.res.Delete(key)
			}
		}
	}
	s.applied = false
}

func (s *editFlow) undoEntry() undo.Entry {
	entry := undo.Entry{Context: s.client.Context, Kind: s.res.Kind(), Namespace: s.res.Namespace(), Name: s.res.Name()}
	switch s.target {
	case targetKey, targetDeleteKey:
		entry.Previous = map[string][]byte{s.key: bytes.Clone(s.original)}
	case targetNewKey:
		entry.Added = []string{s.key}
	case targetResource:
		for _, key := range s.touched {
			if value, exists := s.originalMap[key]; exists {
				if entry.Previous == nil {
					entry.Previous = make(map[string][]byte)
				}
				entry.Previous[key] = []byte(value)
			} else {
				entry.Added = append(entry.Added, key)
			}
		}
	}
	return entry
}

func (s *editFlow) cleanup() {
	s.stop()
	s.dir.Cleanup()
	s.dir = nil
}

func (s *editFlow) cancelAndCleanup() {
	s.radiusLoader.stop()
	s.cleanup()
}

const (
	// rolloutChromeLines is the panel rows the rollout checklist's title,
	// warning, separators and paginator occupy at the narrowest supported width.
	rolloutChromeLines = 5
)

func (s *editFlow) refreshContent() {
	s.danger = s.dangerPhase()
	s.layoutViewport()
	s.viewport.SetContent(s.content())
}

func (s *editFlow) content() string {
	switch s.phase {
	case phaseDiff:
		return s.renderDiff(false)
	case phaseConflict:
		return s.renderDiff(true)
	case phaseRolloutDone:
		return strings.Join(s.rolloutResultLines(s.contentWidth()), "\n")
	default:
		return ""
	}
}

func (s *editFlow) rolloutResultLines(width int) []string {
	rows := s.rolloutResultRows(width)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, row...)
	}
	return lines
}

func (s *editFlow) rolloutResultRows(width int) [][]string {
	rows := make([][]string, 0, len(s.rolloutResults))
	appendRows := func(failed bool) {
		for _, result := range s.rolloutResults {
			if (result.err != nil) != failed {
				continue
			}
			kind := stateLineSuccess
			status := "restarted"
			if failed {
				kind = stateLineError
				status = "failed: " + result.err.Error()
			}
			marker := s.styles.stateMarker(kind)
			wrapped := wrapDialogLines(marker+" "+result.kind+"/"+result.name+"  "+status, width)
			if len(wrapped) > 0 {
				style := s.styles.successText
				if failed {
					style = s.styles.errText
				}
				wrapped[0] = strings.Replace(wrapped[0], marker, style.Render(marker), 1)
			}
			rows = append(rows, wrapped)
		}
	}
	appendRows(true)
	appendRows(false)
	return rows
}

func (s *editFlow) rolloutResultSummary() string {
	failed := 0
	for _, result := range s.rolloutResults {
		if result.err != nil {
			failed++
		}
	}
	return fmt.Sprintf("%d restarted, %d failed", len(s.rolloutResults)-failed, failed)
}

// diffPhase reports the two phases whose job is showing evidence rather than
// asking a question. They render full-width, unboxed, with a pinned header.
func (s *editFlow) diffPhase() bool {
	return s.phase == phaseDiff || s.phase == phaseConflict
}

// removesData reports whether confirming this flow deletes key material.
func (s *editFlow) removesData() bool {
	return s.target == targetDeleteKey || s.removedKeyCount() > 0
}

// removedKeyCount is the number of cluster keys this edit drops.
func (s *editFlow) removedKeyCount() int {
	if !s.mapTarget() || s.editedMap == nil {
		return 0
	}
	count := 0
	for key := range s.originalMap {
		if _, exists := s.editedMap[key]; !exists {
			count++
		}
	}
	return count
}

func (s *editFlow) commitVerb() string {
	switch s.target {
	case targetCreate:
		return "create"
	case targetDeleteKey:
		return "delete"
	case targetNewKey:
		return "add"
	default:
		return "save"
	}
}

func (s *editFlow) dialogOperation() string {
	switch s.phase {
	case phaseEditing, phaseEditorFailed, phaseParseFailed, phaseBinaryCollision:
		return "edit"
	case phaseDryRun, phaseDryRunRejected:
		return "dry-run " + s.commitVerb()
	case phaseSaved:
		if s.savedResolution().offersRestart() {
			return "restart"
		}
		return s.commitVerb()
	case phaseRollingOut, phaseRolloutDone:
		return "restart"
	default:
		return s.commitVerb()
	}
}

func (s *editFlow) identityLines(width int) []string {
	details := []string(nil)
	if s.key != "" {
		details = append(details, "key "+s.key)
	}
	return commitIdentityLines(
		s.dialogOperation(),
		s.res.Kind(),
		s.res.Namespace(),
		s.res.Name(),
		s.client.Context,
		s.client.Server,
		width,
		s.styles.glyphs.separator,
		details...,
	)
}

// actionLine states, in one sentence, what confirming will do.
func (s *editFlow) actionLine() string {
	switch {
	case s.phase == phaseConflict:
		return "Conflict: " + s.targetLabel() + " changed in the cluster"
	case s.target == targetCreate:
		return "Create " + s.targetLabel()
	case s.target == targetDeleteKey:
		return "Delete key " + s.key + " from " + s.targetLabel()
	case s.target == targetNewKey:
		return "Add key " + s.key + " to " + s.targetLabel()
	case s.target == targetKey:
		return "Save key " + s.key + " to " + s.targetLabel()
	default:
		return "Save " + s.targetLabel()
	}
}

func (s *editFlow) targetLabel() string {
	return s.res.Kind() + " " + s.res.Namespace() + "/" + s.res.Name()
}

func (s *editFlow) dangerPhase() bool {
	switch s.phase {
	case phaseEditorFailed, phaseParseFailed, phaseBinaryCollision, phaseDryRunRejected, phaseDryRunUnsupported, phaseForbidden, phaseValidateWarn, phaseCommitGate, phaseRolloutGate:
		return true
	}
	return false
}

func (s *editFlow) diffHeader() []string {
	actionStyle := s.styles.dialogTitle
	if s.phase == phaseConflict || s.removesData() {
		actionStyle = s.styles.errText
	}
	var lines []string
	for _, action := range wrapDialogLines(s.actionLine(), s.width) {
		lines = append(lines, actionStyle.Render(action))
	}
	for _, identity := range clusterIdentityLines(s.client.Context, s.client.Server, s.width, s.styles.glyphs.separator) {
		for _, line := range wrapDialogLines(identity, s.width) {
			lines = append(lines, s.styles.dim.Render(line))
		}
	}
	if s.phase == phaseConflict {
		lines = append(lines,
			s.styles.dim.Render(fmt.Sprintf(
				"Y overwrites the other writer's change; cluster is now at resourceVersion %s",
				s.res.ResourceVersion(),
			)),
			s.styles.dim.Render(s.blastRadiusLine(s.width)),
		)
	} else if s.target != targetCreate {
		lines = append(lines, s.styles.dim.Render(s.blastRadiusLine(s.width)))
	}
	if s.target == targetResource && s.removedKeyCount() > 0 {
		lines = append(lines, s.styles.warnText.Render("this edit removes "+plural(s.removedKeyCount(), "key")))
	}
	if s.nudge {
		lines = append(lines, s.styles.warnText.Render(pressYToConfirm))
	}
	if s.message != "" {
		for _, line := range strings.Split(s.message, "\n") {
			lines = append(lines, s.styles.errText.Render(line))
		}
	}
	formatted := make([]string, 0, len(lines))
	for _, entry := range lines {
		for _, line := range strings.Split(entry, "\n") {
			formatted = append(formatted, truncateLine(line, s.width, s.styles.glyphs.ellipsis))
		}
	}
	return formatted
}

func (s *editFlow) dialogContent() dialogContent {
	content := dialogContent{identity: s.identityLines(s.contentWidth())}
	switch s.phase {
	case phaseEditing:
		content.title = "Waiting for $EDITOR"
		content.body = []string{"close the editor to come back to sk64"}
	case phaseDryRun:
		content.title = "Checking with the apiserver"
		content.body = []string{s.spinner.View() + " server-side dry-run, nothing is written yet"}
	case phaseSaving:
		if s.target == targetCreate {
			content.title = "Creating " + s.targetLabel()
			content.body = []string{s.spinner.View() + " writing to the cluster"}
		} else {
			content.title = "Saving " + s.targetLabel()
			content.body = []string{s.spinner.View() + " writing at resourceVersion " + s.res.ResourceVersion()}
		}
	case phaseRollingOut:
		content.title = "Restarting workloads"
		content.body = []string{s.spinner.View() + " patching kubectl.kubernetes.io/restartedAt"}
	case phaseEditorFailed:
		content.title = "Editor failed"
		content.body = []string{"sk64 read nothing back. Nothing was sent to the cluster."}
		content.message, content.isError = s.message, true
	case phaseParseFailed:
		content.title = "YAML parse failed"
		content.body = []string{"Your edit is still in the temporary file; e reopens it.", "Nothing was sent to the cluster."}
		content.message, content.isError = s.message, true
	case phaseBinaryCollision:
		content.title = "Binary key cannot be edited as YAML"
		if s.proposedMode {
			content.body = []string{
				"This change cannot be applied from this flow.",
				"e returns to the diff; esc closes it.",
				"Use import (i) from the key screen to change a binary key.",
			}
		} else {
			content.body = []string{
				"This document names a binary key that YAML editing omits.",
				"e reopens the document with your edits kept; esc aborts this change.",
			}
		}
		content.message, content.isError = s.message, true
	case phaseDryRunRejected:
		content.title = "Dry-run rejected"
		content.body = []string{"The apiserver or an admission webhook refused this change.", "Nothing was written."}
		content.message, content.isError = s.message, true
	case phaseDryRunUnsupported:
		content.title = "Save without a dry-run check?"
		content.body = []string{"This cluster refused DryRun: All, so the change cannot be validated before it is written."}
		content.warnings = []string{"Y writes immediately, with no admission pre-check."}
		content.message, content.isError = s.message, true
	case phaseForbidden:
		content.title = "Save forbidden"
		content.body = []string{"RBAC denied the write. Nothing in the cluster changed.", "e reopens the editor so you can copy your work out."}
		content.message, content.isError = s.message, true
	case phaseValidateWarn:
		switch s.target {
		case targetCreate:
			content.title = "Create " + s.targetLabel() + " despite warnings?"
		case targetDeleteKey:
			content.title = "Delete key " + s.key + " from " + s.targetLabel() + " despite warnings?"
			content.body = []string{s.blastRadiusLine(s.contentWidth())}
		default:
			content.title = "Save " + s.targetLabel() + " despite warnings?"
			content.body = []string{s.blastRadiusLine(s.contentWidth())}
		}
		for _, warning := range s.warnings {
			content.warnings = append(content.warnings, string(warning))
		}
	case phaseSaved:
		resolution := s.savedResolution()
		switch resolution {
		case savedChecking:
			content.title = "Saved: checking for workloads to restart"
			content.body = []string{"The save succeeded. The consumer check is still running."}
		case savedUnavailable:
			content.title = "Saved: restart check unavailable"
			content.body = []string{"The save succeeded, but the consumer check is unavailable."}
		case savedNothingToRestart:
			content.title = "Saved: nothing to restart"
			content.body = []string{"The consumer check completed; no restartable workloads need a restart."}
		case savedIncompleteRestartOffer, savedRestartOffer:
			selected := s.selectedRolloutCount()
			content.title = fmt.Sprintf("Saved: restart affected workloads?  %d/%d selected", selected, len(s.rollout))
			if resolution == savedIncompleteRestartOffer {
				content.criticalWarnings = []string{"consumer check incomplete: " + strings.Join(s.radius.Notes(), ", ")}
			}
			content.warnings = []string{"Restarting recreates pods; single-replica workloads drop traffic."}
			content.prompt = fitListHeight(s.rolloutList.View(), s.rolloutList.Height())
		}
	case phaseCommitGate:
		switch s.target {
		case targetCreate:
			content.title = "Create " + s.targetLabel() + "?"
			content.body = []string{"This writes a new " + s.res.Kind() + " to the cluster."}
		case targetDeleteKey:
			content.title = "Delete key " + s.key + " from " + s.targetLabel() + "?"
			content.body = []string{s.blastRadiusLine(s.contentWidth())}
		default:
			content.title = "Save " + s.targetLabel() + "?"
			content.body = []string{s.blastRadiusLine(s.contentWidth())}
		}
		if s.target != targetCreate {
			content.body = append(content.body, "writing at resourceVersion "+s.res.ResourceVersion())
		}
		if s.gateSkippedDryRun {
			content.criticalWarnings = append(content.criticalWarnings, "No dry-run pre-check: this cluster refused DryRun: All.")
		} else {
			content.body = append(content.body, "dry-run passed; nothing is written yet")
		}
		if removed := s.removedKeyCount(); removed > 0 {
			content.criticalWarnings = append(content.criticalWarnings, fmt.Sprintf("This change removes %s.", plural(removed, "key")))
		}
		content.prompt = s.gate.promptLines(s.styles, false)
		content.message, content.isWarning = s.gate.message, s.gate.message != ""
	case phaseRolloutGate:
		selected := s.selectedRolloutNames()
		content.title = fmt.Sprintf("Restart %s?", plural(len(selected), "workload"))
		content.body = []string{truncateLine(strings.Join(selected, ", "), s.contentWidth(), s.styles.glyphs.ellipsis)}
		content.criticalWarnings = []string{"Restarting recreates pods; single-replica workloads drop traffic."}
		content.prompt = s.gate.promptLines(s.styles, false)
		content.message, content.isWarning = s.gate.message, s.gate.message != ""
	case phaseRolloutDone:
		content.title = "Rollout results"
		if s.viewport.TotalLineCount() > s.viewport.Height() {
			rows := s.rolloutResultRows(s.contentWidth())
			top := s.viewport.YOffset()
			bottom := top + s.viewport.Height()
			line, first, last := 0, 0, 0
			for i, row := range rows {
				rowBottom := line + len(row)
				if rowBottom > top && line < bottom {
					if first == 0 {
						first = i + 1
					}
					last = i + 1
				}
				line = rowBottom
			}
			content.title = fmt.Sprintf("Rollout results  %d-%d of %d shown", first, last, len(rows))
		}
		content.summary = s.rolloutResultSummary()
		content.prompt = s.viewport.View()
	}
	switch {
	case s.nudge:
		content.message = pressYToConfirm
		content.isWarning = true
	}
	return content
}

func (s *editFlow) blastRadiusLine(width int) string {
	if s.radiusLoader.pending {
		return renderLoadingLine(s.styles, s.spinner.View(), "checking consumers", "", width)
	}
	if s.radiusErr != nil {
		return renderStateLine(s.styles, stateLineUnknown, "blast radius unavailable", "", width)
	}
	return s.radiusSummary.renderLine(s.styles, width)
}

func (s *editFlow) renderDiff(conflict bool) string {
	if s.mapTarget() {
		before, err := resyaml.SerializeValues(s.originalMap)
		if err != nil {
			return fmt.Sprintf("render original values: %v", err)
		}
		after, err := resyaml.SerializeValues(s.editedMap)
		if err != nil {
			return fmt.Sprintf("render edited values: %v", err)
		}
		oldLabel, newLabel := "keys (cluster)", "keys (edited)"
		if conflict {
			oldLabel, newLabel = "keys (cluster now)", "keys (mine)"
		}
		return styledDiff(diffpkg.Render(oldLabel, newLabel, before, after, s.styles.glyphs.ellipsis), s.styles)
	}
	oldLabel, newLabel := s.key+" (cluster)", s.key+" (edited)"
	before, after := s.original, s.edited
	if conflict {
		oldLabel = s.key + " (cluster now)"
		newLabel = s.key + " (mine)"
	}
	if s.target == targetNewKey && !conflict {
		oldLabel, newLabel = s.key+" (absent)", s.key+" (new)"
	}
	if s.target == targetDeleteKey {
		newLabel = s.key + " (deleted)"
		after = nil
	}
	return styledDiff(diffpkg.Render(oldLabel, newLabel, before, after, s.styles.glyphs.ellipsis), s.styles)
}

// diffFileHeaderLines is the number of "--- old" / "+++ new" rows a unified
// diff emits before its first hunk. Only those rows are file headers: a body
// line beginning with --- is a deleted line whose value starts with two
// dashes, as every PEM block does.
const diffFileHeaderLines = 2

func styledDiff(value string, st *styles) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		switch {
		case i < diffFileHeaderLines && (strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++")):
			lines[i] = st.dim.Render(line)
		case strings.HasPrefix(line, "@@"):
			lines[i] = st.tag.Render(line)
		case strings.HasPrefix(line, "+"):
			lines[i] = st.diffAdd.Render(line)
		case strings.HasPrefix(line, "-"):
			lines[i] = st.diffDel.Render(line)
		case strings.HasPrefix(line, `\ `):
			lines[i] = st.dim.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func (s *editFlow) View() string {
	if s.diffPhase() {
		return strings.Join(s.diffHeader(), "\n") + "\n" + s.diffRule() + "\n" + s.viewport.View()
	}
	return s.render(s.dialogContent())
}

// diffRule is the separator between the pinned header and the scrolling diff.
// It carries the wrap state, and the scroll position when the body overflows.
func (s *editFlow) diffRule() string {
	label := "wrap off"
	if s.wrap {
		label = "wrap on"
	}
	if s.viewport.TotalLineCount() > s.viewport.Height() {
		label += fmt.Sprintf("  %.0f%%", s.viewport.ScrollPercent()*100)
	}
	rule := s.styles.glyphs.ruleMarker + s.styles.glyphs.ruleMarker + " " + label + " "
	if fill := s.width - ansi.StringWidth(rule); fill > 0 {
		rule += strings.Repeat(s.styles.glyphs.ruleMarker, fill)
	}
	return s.styles.dim.Render(truncateLine(rule, s.width, s.styles.glyphs.ellipsis))
}

const diffGutterWidth = 2

func (s *editFlow) diffGutter(ctx viewport.GutterContext) string {
	if ctx.Soft {
		return s.styles.dim.Render(s.styles.glyphs.wrapMarker) + " "
	}
	return "  "
}

func (s *editFlow) SetSize(width, height int) {
	s.resize(width, height)
	s.gate.setWidth(s.contentWidth())
	s.layoutViewport()
}

func (s *editFlow) SetStyles(st *styles) {
	s.styles = st
	applySpinnerStyle(&s.spinner, st)
	applyListStyles(&s.rolloutList, st)
	s.gate.setStyles(st)
	s.refreshContent()
}

func (s *editFlow) layoutViewport() {
	s.viewport.SoftWrap = false
	s.viewport.LeftGutterFunc = viewport.NoGutter
	switch {
	case s.phase == phaseSaved && s.savedResolution().offersRestart():
		s.rolloutList.SetSize(s.contentWidth(), min(max(1, len(s.rollout)), s.scrollHeight(rolloutChromeLines)))
		s.viewport.SetHeight(0)
	case s.phase == phaseRolloutDone:
		width := s.contentWidth()
		lines := s.rolloutResultLines(width)
		fixedRows := len(s.identityLines(width)) + 5
		s.viewport.SetWidth(width)
		s.viewport.SetHeight(min(max(1, len(lines)), s.scrollHeight(fixedRows)))
	case s.diffPhase():
		header := s.diffHeader()
		s.viewport.SoftWrap = s.wrap
		s.viewport.LeftGutterFunc = s.diffGutter
		s.viewport.SetWidth(s.width)
		s.viewport.SetHeight(max(0, s.height-len(header)-1))
	default:
		s.viewport.SetWidth(s.contentWidth())
		s.viewport.SetHeight(0)
	}
}

// toggleWrap flips soft wrapping and keeps the top content line in place. The
// viewport's y-offset counts wrapped rows while wrapping is on and content
// lines while it is off, so the offset has to be converted, not carried over.
func (s *editFlow) toggleWrap() {
	lines := strings.Split(s.viewport.GetContent(), "\n")
	width := max(1, s.width-diffGutterWidth)
	top := s.viewport.YOffset()
	if s.wrap {
		top = diffContentLine(lines, width, top)
	}
	s.wrap = !s.wrap
	s.viewport.SetXOffset(0)
	s.layoutViewport()
	if s.wrap {
		top = diffWrappedRow(lines, width, top)
	}
	s.viewport.SetYOffset(top)
}

// diffLineRows is the number of viewport rows one content line occupies when
// soft wrapping is on.
func diffLineRows(line string, width int) int {
	return max(1, (ansi.StringWidth(line)+width-1)/width)
}

// diffWrappedRow is the wrapped-row offset that puts content line at the top.
func diffWrappedRow(lines []string, width, line int) int {
	rows := 0
	for i := 0; i < line && i < len(lines); i++ {
		rows += diffLineRows(lines[i], width)
	}
	return rows
}

// diffContentLine is the content line showing at wrapped-row offset row.
func diffContentLine(lines []string, width, row int) int {
	rows := 0
	for i, line := range lines {
		rows += diffLineRows(line, width)
		if row < rows {
			return i
		}
	}
	return max(0, len(lines)-1)
}

func (s *editFlow) Title() string {
	if s.phase == phaseConflict {
		if s.target != targetResource {
			return s.res.Name() + "/" + s.key + " (conflict)"
		}
		return s.res.Name() + " (conflict)"
	}
	if s.target == targetResource {
		return s.res.Name() + " (edit all)"
	}
	if s.target == targetCreate {
		return s.res.Name() + " (create)"
	}
	if s.target == targetDeleteKey {
		return s.res.Name() + "/" + s.key + " (delete)"
	}
	return s.res.Name() + "/" + s.key + " (edit)"
}

func (s *editFlow) Hints() footerHints {
	wrap := "wrap"
	if s.wrap {
		wrap = "unwrap"
	}
	switch s.phase {
	case phaseEditorFailed, phaseDryRunRejected, phaseParseFailed:
		return hintBindings(s.km.Edit, s.km.Cancel)
	case phaseBinaryCollision:
		if s.proposedMode {
			return hintBindings(hintDesc(s.km.Edit, "back to diff"), s.km.Cancel)
		}
		return hintBindings(s.km.Edit, s.km.Cancel)
	case phaseDiff:
		if s.target == targetCreate {
			return hintBindings(hintDesc(s.km.Confirm, "create"), s.km.Edit, displayHint(s.env.keymaps().viewportMovementHelp, "scroll"), hintDesc(s.km.Wrap, wrap), s.km.Cancel)
		}
		if s.target == targetDeleteKey {
			return hintBindings(hintDesc(s.km.Confirm, "delete"), displayHint(s.env.keymaps().viewportMovementHelp, "scroll"), hintDesc(s.km.Wrap, wrap), s.km.Cancel)
		}
		if s.target == targetResource && s.proposedMode {
			return hintBindings(hintDesc(s.km.Confirm, "save"), displayHint(s.env.keymaps().viewportMovementHelp, "scroll"), hintDesc(s.km.Wrap, wrap), s.km.Cancel)
		}
		return hintBindings(hintDesc(s.km.Confirm, "save"), s.km.Edit, displayHint(s.env.keymaps().viewportMovementHelp, "scroll"), hintDesc(s.km.Wrap, wrap), s.km.Cancel)
	case phaseValidateWarn:
		if s.target == targetCreate {
			return hintBindings(hintDesc(s.km.Confirm, "create anyway"), s.km.Edit, s.km.Cancel)
		}
		if s.target == targetDeleteKey {
			return hintBindings(hintDesc(s.km.Confirm, "delete anyway"), s.km.Cancel)
		}
		return hintBindings(hintDesc(s.km.Confirm, "save anyway"), s.km.Edit, s.km.Cancel)
	case phaseDryRun:
		return hintBindings(hintDesc(s.km.Cancel, "cancel dry-run"))
	case phaseDryRunUnsupported:
		return hintBindings(hintDesc(s.km.Confirm, "proceed without dry-run"), s.km.Edit, s.km.Cancel)
	case phaseSaving:
		if s.target == targetCreate {
			return hintStatus("creating (cannot cancel)")
		}
		return hintStatus("saving (cannot cancel)")
	case phaseConflict:
		return hintBindings(hintDesc(s.km.Confirm, "re-apply mine"), hintDesc(s.km.Edit, "re-edit mine"), displayHint(s.env.keymaps().viewportMovementHelp, "scroll"), hintDesc(s.km.Wrap, wrap), s.km.Cancel)
	case phaseForbidden:
		return hintBindings(hintDesc(s.km.Edit, "reopen editor to copy out"), s.km.Cancel)
	case phaseSaved:
		if s.savedResolution().offersRestart() {
			return hintBindings(s.km.RolloutToggle, s.km.RolloutToggleAll, hintDesc(s.km.Restart, "restart selected"), hintDesc(s.km.Cancel, "skip"))
		}
		if s.savedResolution() == savedChecking {
			return hintBindings(hintDesc(s.km.Cancel, "close"))
		}
		return hintBindings(s.km.Accept, hintDesc(s.km.Cancel, "close"))
	case phaseRollingOut:
		return hintStatus("restarting (cannot cancel)")
	case phaseRolloutDone:
		return hintBindings(displayHint(s.env.keymaps().viewportMovementHelp, "scroll"), s.km.Accept, hintDesc(s.km.Cancel, "close"))
	case phaseCommitGate:
		return hintBindings(displayHint("YES", "confirm"), hintDesc(s.km.Cancel, "back to diff"))
	case phaseRolloutGate:
		return hintBindings(displayHint("YES", "confirm"), hintDesc(s.km.Cancel, "back to selection"))
	default:
		return footerHints{}
	}
}

func (s *editFlow) Help() helpGroup     { return helpGroup{} }
func (s *editFlow) CapturesInput() bool { return true }
func (s *editFlow) WantsEsc() bool      { return true }
func (s *editFlow) capturesQuit() bool {
	return s.edited != nil || s.editedMap != nil || s.rawDoc != nil
}

func (s *editFlow) prepareQuitArm() {
	if s.nudge {
		s.nudge = false
		s.layoutViewport()
	}
}

func (s *editFlow) prepareQuit() {
	s.cancelAndCleanup()
}

func (s *editFlow) quitWarning() string {
	return "unsaved edit - ctrl+c again to quit"
}

func (s *editFlow) mapTarget() bool {
	return s.target == targetResource || s.target == targetCreate
}
