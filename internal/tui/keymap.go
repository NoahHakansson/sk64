package tui

import (
	"strings"

	"charm.land/bubbles/v2/filepicker"
	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"

	"github.com/NoahHakansson/sk64/internal/config"
)

// footerHints is what a screen or overlay contributes to the footer: either
// an ordered list of key bindings, or a status line for cannot-cancel states.
// Exactly one of bindings/status is set.
type footerHints struct {
	bindings []bubbleskey.Binding
	showHelp bool
	status   string
}

func hintBindings(bindings ...bubbleskey.Binding) footerHints {
	return footerHints{bindings: bindings}
}

func browsingHints(bindings ...bubbleskey.Binding) footerHints {
	return footerHints{bindings: bindings, showHelp: true}
}

func hintStatus(text string) footerHints {
	return footerHints{status: text}
}

// hintDesc returns a copy of b with the same key label and a state-specific
// description.
func hintDesc(b bubbleskey.Binding, desc string) bubbleskey.Binding {
	b.SetHelp(b.Help().Key, desc)
	return b
}

// bindingAction renders a state-line recovery hint from a binding's primary
// key; footer and help labels list every key, but one suggestion is enough here.
func bindingAction(b bubbleskey.Binding, action string) string {
	if keys := b.Keys(); len(keys) > 0 {
		return keys[0] + " " + action
	}
	return b.Help().Key + " " + action
}

// displayHint is a footer or overlay label with no dispatchable key.
func displayHint(label, desc string) bubbleskey.Binding {
	return bubbleskey.NewBinding(bubbleskey.WithKeys(""), bubbleskey.WithHelp(label, desc))
}

var (
	bindEsc         = bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "cancel"))
	bindRefresh     = bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+r"), bubbleskey.WithHelp("ctrl+r", "refresh"))
	bindEnter       = bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "accept"))
	bindConfirmY    = bubbleskey.NewBinding(bubbleskey.WithKeys("Y"), bubbleskey.WithHelp("Y", "confirm"))
	bindNewN        = bubbleskey.NewBinding(bubbleskey.WithKeys("N"), bubbleskey.WithHelp("N", "new"))
	bindDeleteD     = bubbleskey.NewBinding(bubbleskey.WithKeys("D"), bubbleskey.WithHelp("D", "delete"))
	bindRestartR    = bubbleskey.NewBinding(bubbleskey.WithKeys("R"), bubbleskey.WithHelp("R", "restart"))
	bindAlwaysA     = bubbleskey.NewBinding(bubbleskey.WithKeys("A"), bubbleskey.WithHelp("A", "always"))
	bindConfirmQuit = bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+c"), bubbleskey.WithHelp("ctrl+c", "quit"))
)

type globalKeyMap struct {
	Quit          bubbleskey.Binding
	ConfirmQuit   bubbleskey.Binding
	Help          bubbleskey.Binding
	Filter        bubbleskey.Binding
	Search        bubbleskey.Binding
	ContextSwitch bubbleskey.Binding
	ProjectSwitch bubbleskey.Binding
	Back          bubbleskey.Binding
}

type namespaceKeyMap struct {
	Refresh       bubbleskey.Binding
	Open          bubbleskey.Binding
	Workloads     bubbleskey.Binding
	AllNamespaces bubbleskey.Binding
	CancelLoad    bubbleskey.Binding
}

type resourceKeyMap struct {
	Refresh       bubbleskey.Binding
	TypeCycle     bubbleskey.Binding
	Open          bubbleskey.Binding
	Consumers     bubbleskey.Binding
	Link          bubbleskey.Binding
	New           bubbleskey.Binding
	Delete        bubbleskey.Binding
	AllNamespaces bubbleskey.Binding
	CancelLoad    bubbleskey.Binding
}

type keyKeyMap struct {
	Refresh     bubbleskey.Binding
	Open        bubbleskey.Binding
	Export      bubbleskey.Binding
	Consumers   bubbleskey.Binding
	Import      bubbleskey.Binding
	EditAll     bubbleskey.Binding
	NewKey      bubbleskey.Binding
	DeleteKey   bubbleskey.Binding
	ValueSearch bubbleskey.Binding
	Undo        bubbleskey.Binding
	CancelLoad  bubbleskey.Binding
}

type workloadKeyMap struct {
	Refresh    bubbleskey.Binding
	Open       bubbleskey.Binding
	Link       bubbleskey.Binding
	CancelLoad bubbleskey.Binding
}

type consumersKeyMap struct {
	Refresh    bubbleskey.Binding
	CancelLoad bubbleskey.Binding
}

type searchKeyMap struct {
	Up      bubbleskey.Binding
	Down    bubbleskey.Binding
	Open    bubbleskey.Binding
	Refresh bubbleskey.Binding
	Cancel  bubbleskey.Binding
}

type projectKeyMap struct {
	Refresh bubbleskey.Binding
	Open    bubbleskey.Binding
	Unlink  bubbleskey.Binding
	Edit    bubbleskey.Binding
	Scan    bubbleskey.Binding
	Back    bubbleskey.Binding
}

type suggestionKeyMap struct {
	Apply   bubbleskey.Binding
	Refresh bubbleskey.Binding
	Back    bubbleskey.Binding
}

type editFlowKeyMap struct {
	Confirm          bubbleskey.Binding
	Edit             bubbleskey.Binding
	Wrap             bubbleskey.Binding
	Cancel           bubbleskey.Binding
	Accept           bubbleskey.Binding
	RolloutUp        bubbleskey.Binding
	RolloutDown      bubbleskey.Binding
	RolloutToggle    bubbleskey.Binding
	RolloutToggleAll bubbleskey.Binding
	Restart          bubbleskey.Binding
}

type createPromptKeyMap struct {
	Up     bubbleskey.Binding
	Down   bubbleskey.Binding
	Choose bubbleskey.Binding
	Cancel bubbleskey.Binding
}

type projectFormKeyMap struct {
	Up     bubbleskey.Binding
	Down   bubbleskey.Binding
	Next   bubbleskey.Binding
	Prev   bubbleskey.Binding
	Rescan bubbleskey.Binding
	Manual bubbleskey.Binding
	Accept bubbleskey.Binding
	Cancel bubbleskey.Binding
}

type filePromptKeyMap struct {
	CycleFocus bubbleskey.Binding
	Accept     bubbleskey.Binding
	Cancel     bubbleskey.Binding
}

type helpOverlayKeyMap struct {
	Close bubbleskey.Binding
}

type keyMaps struct {
	list                 list.KeyMap
	viewport             viewport.KeyMap
	filePicker           filepicker.KeyMap
	movementHelp         string
	viewportMovementHelp string
	global               globalKeyMap
	namespace            namespaceKeyMap
	resource             resourceKeyMap
	keyScreen            keyKeyMap
	workload             workloadKeyMap
	consumers            consumersKeyMap
	search               searchKeyMap
	project              projectKeyMap
	suggestion           suggestionKeyMap
	editFlow             editFlowKeyMap
	createPrompt         createPromptKeyMap
	projectForm          projectFormKeyMap
	filePrompt           filePromptKeyMap
	helpOverlay          helpOverlayKeyMap
}

func defaultKeyMaps() *keyMaps {
	filePicker := filepicker.DefaultKeyMap()
	filePicker.Back = bubbleskey.NewBinding(bubbleskey.WithKeys("backspace", "h", "left"), bubbleskey.WithHelp("backspace", "up one level"))
	filePicker.Open = bubbleskey.NewBinding(bubbleskey.WithKeys("enter", "l", "right"), bubbleskey.WithHelp("enter", "open"))
	filePicker.Select = bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "select"))
	return &keyMaps{
		list:                 list.DefaultKeyMap(),
		viewport:             viewport.DefaultKeyMap(),
		filePicker:           filePicker,
		movementHelp:         "h/j/k/l",
		viewportMovementHelp: "up/down",
		global: globalKeyMap{
			Quit:          bubbleskey.NewBinding(bubbleskey.WithKeys("Q"), bubbleskey.WithHelp("Q", "quit")),
			ConfirmQuit:   bindConfirmQuit,
			Help:          bubbleskey.NewBinding(bubbleskey.WithKeys("?"), bubbleskey.WithHelp("?", "help")),
			Filter:        bubbleskey.NewBinding(bubbleskey.WithKeys("/"), bubbleskey.WithHelp("/", "filter")),
			Search:        bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+f"), bubbleskey.WithHelp("ctrl+f", "search")),
			ContextSwitch: bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+k"), bubbleskey.WithHelp("ctrl+k", "context")),
			ProjectSwitch: bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+p"), bubbleskey.WithHelp("ctrl+p", "project")),
			Back:          bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "back")),
		},
		namespace: namespaceKeyMap{
			Refresh:       bindRefresh,
			Open:          bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "open")),
			Workloads:     bubbleskey.NewBinding(bubbleskey.WithKeys("w"), bubbleskey.WithHelp("w", "workloads")),
			AllNamespaces: bubbleskey.NewBinding(bubbleskey.WithKeys("a"), bubbleskey.WithHelp("a", "all-ns")),
			CancelLoad:    bindEsc,
		},
		resource: resourceKeyMap{
			Refresh:       bindRefresh,
			TypeCycle:     bubbleskey.NewBinding(bubbleskey.WithKeys("t"), bubbleskey.WithHelp("t", "type")),
			Open:          bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "keys")),
			Consumers:     bubbleskey.NewBinding(bubbleskey.WithKeys("r"), bubbleskey.WithHelp("r", "consumers")),
			Link:          bubbleskey.NewBinding(bubbleskey.WithKeys("L"), bubbleskey.WithHelp("L", "link")),
			New:           bindNewN,
			Delete:        bindDeleteD,
			AllNamespaces: bubbleskey.NewBinding(bubbleskey.WithKeys("a"), bubbleskey.WithHelp("a", "one ns")),
			CancelLoad:    bindEsc,
		},
		keyScreen: keyKeyMap{
			Refresh:     bindRefresh,
			Open:        bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "edit")),
			Export:      bubbleskey.NewBinding(bubbleskey.WithKeys("x"), bubbleskey.WithHelp("x", "export")),
			Consumers:   bubbleskey.NewBinding(bubbleskey.WithKeys("r"), bubbleskey.WithHelp("r", "consumers")),
			Import:      bubbleskey.NewBinding(bubbleskey.WithKeys("i"), bubbleskey.WithHelp("i", "import")),
			EditAll:     bubbleskey.NewBinding(bubbleskey.WithKeys("e"), bubbleskey.WithHelp("e", "edit all")),
			NewKey:      bindNewN,
			DeleteKey:   bindDeleteD,
			ValueSearch: bubbleskey.NewBinding(bubbleskey.WithKeys("v"), bubbleskey.WithHelp("v", "values")),
			Undo:        bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+z"), bubbleskey.WithHelp("ctrl+z", "undo")),
			CancelLoad:  bindEsc,
		},
		workload: workloadKeyMap{
			Refresh:    bindRefresh,
			Open:       bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "open")),
			Link:       bubbleskey.NewBinding(bubbleskey.WithKeys("L"), bubbleskey.WithHelp("L", "link")),
			CancelLoad: bindEsc,
		},
		consumers: consumersKeyMap{
			Refresh:    bindRefresh,
			CancelLoad: bindEsc,
		},
		search: searchKeyMap{
			Up:      bubbleskey.NewBinding(bubbleskey.WithKeys("up"), bubbleskey.WithHelp("up/down", "move")),
			Down:    bubbleskey.NewBinding(bubbleskey.WithKeys("down"), bubbleskey.WithHelp("up/down", "move")),
			Open:    bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "open")),
			Refresh: bubbleskey.NewBinding(bubbleskey.WithKeys("ctrl+r"), bubbleskey.WithHelp("ctrl+r", "rescan")),
			Cancel:  bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "back")),
		},
		project: projectKeyMap{
			Refresh: bindRefresh,
			Open:    bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "open")),
			Unlink:  bubbleskey.NewBinding(bubbleskey.WithKeys("u"), bubbleskey.WithHelp("u", "unlink")),
			Edit:    bubbleskey.NewBinding(bubbleskey.WithKeys("e"), bubbleskey.WithHelp("e", "edit")),
			Scan:    bubbleskey.NewBinding(bubbleskey.WithKeys("s"), bubbleskey.WithHelp("s", "scan")),
			Back:    bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "back")),
		},
		suggestion: suggestionKeyMap{
			Apply:   bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "link")),
			Refresh: bindRefresh,
			Back:    bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "back")),
		},
		editFlow: editFlowKeyMap{
			Confirm:          bindConfirmY,
			Edit:             bubbleskey.NewBinding(bubbleskey.WithKeys("e"), bubbleskey.WithHelp("e", "re-edit")),
			Wrap:             bubbleskey.NewBinding(bubbleskey.WithKeys("w"), bubbleskey.WithHelp("w", "wrap")),
			Cancel:           bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "abort")),
			Accept:           bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "done")),
			RolloutUp:        bubbleskey.NewBinding(bubbleskey.WithKeys("up", "k"), bubbleskey.WithHelp("up/down", "choose")),
			RolloutDown:      bubbleskey.NewBinding(bubbleskey.WithKeys("down", "j"), bubbleskey.WithHelp("up/down", "choose")),
			RolloutToggle:    bubbleskey.NewBinding(bubbleskey.WithKeys("space"), bubbleskey.WithHelp("space", "toggle")),
			RolloutToggleAll: bubbleskey.NewBinding(bubbleskey.WithKeys("a"), bubbleskey.WithHelp("a", "all")),
			Restart:          bindRestartR,
		},
		createPrompt: createPromptKeyMap{
			Up:     bubbleskey.NewBinding(bubbleskey.WithKeys("up", "k"), bubbleskey.WithHelp("up/down", "choose")),
			Down:   bubbleskey.NewBinding(bubbleskey.WithKeys("down", "j"), bubbleskey.WithHelp("up/down", "choose")),
			Choose: bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "select")),
			Cancel: bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "back")),
		},
		projectForm: projectFormKeyMap{
			Up:     bubbleskey.NewBinding(bubbleskey.WithKeys("up", "k"), bubbleskey.WithHelp("up/down", "choose")),
			Down:   bubbleskey.NewBinding(bubbleskey.WithKeys("down", "j"), bubbleskey.WithHelp("up/down", "choose")),
			Next:   bubbleskey.NewBinding(bubbleskey.WithKeys("tab", "down"), bubbleskey.WithHelp("tab", "next field")),
			Prev:   bubbleskey.NewBinding(bubbleskey.WithKeys("shift+tab", "up"), bubbleskey.WithHelp("shift+tab", "previous field")),
			Rescan: bubbleskey.NewBinding(bubbleskey.WithKeys("r"), bubbleskey.WithHelp("r", "rescan")),
			Manual: bubbleskey.NewBinding(bubbleskey.WithKeys("m"), bubbleskey.WithHelp("m", "manual")),
			Accept: bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "select")),
			Cancel: bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "cancel")),
		},
		filePrompt: filePromptKeyMap{
			CycleFocus: bubbleskey.NewBinding(bubbleskey.WithKeys("up", "down", "tab"), bubbleskey.WithHelp("tab", "focus")),
			Accept:     bubbleskey.NewBinding(bubbleskey.WithKeys("enter"), bubbleskey.WithHelp("enter", "accept")),
			Cancel:     bubbleskey.NewBinding(bubbleskey.WithKeys("esc"), bubbleskey.WithHelp("esc", "back")),
		},
		helpOverlay: helpOverlayKeyMap{
			Close: bubbleskey.NewBinding(bubbleskey.WithKeys("esc", "?", "Q"), bubbleskey.WithHelp("esc", "close")),
		},
	}
}

func applyKeybinds(km *keyMaps, overrides config.Overrides) {
	if len(overrides) == 0 {
		return
	}

	rebind := func(binding *bubbleskey.Binding, keys []string) {
		binding.SetKeys(keys...)
		binding.SetHelp(strings.Join(keys, "/"), binding.Help().Desc)
	}
	rebindPair := func(up, down *bubbleskey.Binding) {
		label := up.Keys()[0] + "/" + down.Keys()[0]
		up.SetHelp(label, up.Help().Desc)
		down.SetHelp(label, down.Help().Desc)
	}

	var upChanged, downChanged, movementChanged bool
	for action, keys := range overrides {
		switch action {
		case config.ActionUp:
			upChanged, movementChanged = true, true
			for _, binding := range []*bubbleskey.Binding{&km.list.CursorUp, &km.viewport.Up, &km.filePicker.Up, &km.createPrompt.Up, &km.editFlow.RolloutUp} {
				rebind(binding, keys)
			}
		case config.ActionDown:
			downChanged, movementChanged = true, true
			for _, binding := range []*bubbleskey.Binding{&km.list.CursorDown, &km.viewport.Down, &km.filePicker.Down, &km.createPrompt.Down, &km.editFlow.RolloutDown} {
				rebind(binding, keys)
			}
		case config.ActionTop:
			movementChanged = true
			rebind(&km.list.GoToStart, keys)
			rebind(&km.filePicker.GoToTop, keys)
		case config.ActionBottom:
			movementChanged = true
			rebind(&km.list.GoToEnd, keys)
			rebind(&km.filePicker.GoToLast, keys)
		case config.ActionPageUp:
			movementChanged = true
			rebind(&km.list.PrevPage, keys)
			rebind(&km.viewport.PageUp, keys)
			rebind(&km.filePicker.PageUp, keys)
		case config.ActionPageDown:
			movementChanged = true
			rebind(&km.list.NextPage, keys)
			rebind(&km.viewport.PageDown, keys)
			rebind(&km.filePicker.PageDown, keys)
		case config.ActionHalfPageUp:
			rebind(&km.viewport.HalfPageUp, keys)
		case config.ActionHalfPageDown:
			rebind(&km.viewport.HalfPageDown, keys)
		case config.ActionRefresh:
			for _, binding := range []*bubbleskey.Binding{
				&km.namespace.Refresh,
				&km.resource.Refresh,
				&km.keyScreen.Refresh,
				&km.workload.Refresh,
				&km.consumers.Refresh,
				&km.project.Refresh,
				&km.suggestion.Refresh,
			} {
				rebind(binding, keys)
			}
		case config.ActionFilter:
			rebind(&km.global.Filter, keys)
			rebind(&km.list.Filter, keys)
		case config.ActionAllNamespaces:
			rebind(&km.namespace.AllNamespaces, keys)
			rebind(&km.resource.AllNamespaces, keys)
		case config.ActionTypeCycle:
			rebind(&km.resource.TypeCycle, keys)
		case config.ActionValues:
			rebind(&km.keyScreen.ValueSearch, keys)
		case config.ActionWrap:
			rebind(&km.editFlow.Wrap, keys)
		case config.ActionHelp:
			rebind(&km.global.Help, keys)
		case config.ActionQuit:
			rebind(&km.global.Quit, keys)
		}
	}

	if upChanged || downChanged {
		for _, pair := range [][2]*bubbleskey.Binding{
			{&km.list.CursorUp, &km.list.CursorDown},
			{&km.viewport.Up, &km.viewport.Down},
			{&km.filePicker.Up, &km.filePicker.Down},
			{&km.createPrompt.Up, &km.createPrompt.Down},
			{&km.editFlow.RolloutUp, &km.editFlow.RolloutDown},
		} {
			rebindPair(pair[0], pair[1])
		}
		km.viewportMovementHelp = km.viewport.Up.Keys()[0] + "/" + km.viewport.Down.Keys()[0]
	}

	closeKeys := append([]string{"esc"}, km.global.Help.Keys()...)
	closeKeys = append(closeKeys, km.global.Quit.Keys()...)
	km.helpOverlay.Close.SetKeys(closeKeys...)

	if movementChanged {
		km.movementHelp = km.list.CursorUp.Keys()[0] + "/" + km.list.CursorDown.Keys()[0]
	}
}

var packageDefaultKeyMaps = defaultKeyMaps()
