package config

import (
	"fmt"
	"sort"
)

// Action names a rebindable behavior in the config file.
type Action string

const (
	ActionUp            Action = "up"
	ActionDown          Action = "down"
	ActionTop           Action = "top"
	ActionBottom        Action = "bottom"
	ActionPageUp        Action = "page-up"
	ActionPageDown      Action = "page-down"
	ActionHalfPageUp    Action = "half-page-up"
	ActionHalfPageDown  Action = "half-page-down"
	ActionRefresh       Action = "refresh"
	ActionFilter        Action = "filter"
	ActionAllNamespaces Action = "all-namespaces"
	ActionTypeCycle     Action = "type-cycle"
	ActionValues        Action = "values"
	ActionWrap          Action = "wrap"
	ActionHelp          Action = "help"
	ActionQuit          Action = "quit"
)

type scopeClass string

const (
	scopeGlobal     scopeClass = "global"
	scopeList       scopeClass = "list"
	scopeViewport   scopeClass = "viewport"
	scopeFilePicker scopeClass = "filepicker"
	scopeNamespace  scopeClass = "namespace"
	scopeResource   scopeClass = "resource"
	scopeKeys       scopeClass = "keys"
	scopeWorkload   scopeClass = "workload"
	scopeProject    scopeClass = "project"
	scopeDiff       scopeClass = "diff"
)

type actionSpec struct {
	name     Action
	defaults map[scopeClass][]string
}

// Navigation overrides apply to non-input list cursors, viewports, and
// choosers. Input-capturing search, filter, form, and gate surfaces keep their
// input keys so printable navigation bindings can still be typed there.
var actionSpecs = []actionSpec{
	{name: ActionUp, defaults: map[scopeClass][]string{scopeList: {"up", "k"}, scopeViewport: {"up", "k"}, scopeFilePicker: {"k", "up", "ctrl+p"}}},
	{name: ActionDown, defaults: map[scopeClass][]string{scopeList: {"down", "j"}, scopeViewport: {"down", "j"}, scopeFilePicker: {"j", "down", "ctrl+n"}}},
	{name: ActionTop, defaults: map[scopeClass][]string{scopeList: {"home", "g"}, scopeFilePicker: {"g"}}},
	{name: ActionBottom, defaults: map[scopeClass][]string{scopeList: {"end", "G"}, scopeFilePicker: {"G"}}},
	{name: ActionPageUp, defaults: map[scopeClass][]string{scopeList: {"left", "h", "pgup", "b", "u"}, scopeViewport: {"pgup", "b"}, scopeFilePicker: {"K", "pgup"}}},
	{name: ActionPageDown, defaults: map[scopeClass][]string{scopeList: {"right", "l", "pgdown", "f", "d"}, scopeViewport: {"pgdown", "space", "f"}, scopeFilePicker: {"J", "pgdown"}}},
	{name: ActionHalfPageUp, defaults: map[scopeClass][]string{scopeViewport: {"u", "ctrl+u"}}},
	{name: ActionHalfPageDown, defaults: map[scopeClass][]string{scopeViewport: {"d", "ctrl+d"}}},
	{name: ActionRefresh, defaults: map[scopeClass][]string{scopeList: {"ctrl+r"}}},
	{name: ActionFilter, defaults: map[scopeClass][]string{scopeList: {"/"}}},
	{name: ActionAllNamespaces, defaults: map[scopeClass][]string{scopeNamespace: {"a"}, scopeResource: {"a"}}},
	{name: ActionTypeCycle, defaults: map[scopeClass][]string{scopeResource: {"t"}}},
	{name: ActionValues, defaults: map[scopeClass][]string{scopeKeys: {"v"}}},
	{name: ActionWrap, defaults: map[scopeClass][]string{scopeDiff: {"w"}}},
	{name: ActionHelp, defaults: map[scopeClass][]string{scopeGlobal: {"?"}}},
	{name: ActionQuit, defaults: map[scopeClass][]string{scopeGlobal: {"Q"}}},
}

var actionByName = func() map[Action]actionSpec {
	actions := make(map[Action]actionSpec, len(actionSpecs))
	for _, spec := range actionSpecs {
		actions[spec.name] = spec
	}
	return actions
}()

var screenClasses = map[string][]scopeClass{
	"namespace":  {scopeGlobal, scopeList, scopeNamespace},
	"resource":   {scopeGlobal, scopeList, scopeResource},
	"keys":       {scopeGlobal, scopeList, scopeKeys},
	"workloads":  {scopeGlobal, scopeList, scopeWorkload},
	"consumers":  {scopeGlobal, scopeList},
	"search":     {},
	"filepicker": {scopeFilePicker},
	"project":    {scopeGlobal, scopeList, scopeProject},
	"diff":       {scopeViewport, scopeDiff},
	"value":      {scopeGlobal, scopeViewport},
	"hex":        {scopeGlobal, scopeViewport},
}

type fixedKey struct {
	key     string
	meaning string
}

var fixedKeys = map[scopeClass][]fixedKey{
	scopeGlobal: {
		{key: "ctrl+c", meaning: "quit confirmation"},
		{key: "ctrl+f", meaning: "search"},
		{key: "ctrl+k", meaning: "context switch"},
		{key: "ctrl+p", meaning: "project switch"},
	},
	scopeViewport: {
		{key: "left", meaning: "move left"},
		{key: "h", meaning: "move left"},
		{key: "right", meaning: "move right"},
		{key: "l", meaning: "move right"},
	},
	scopeFilePicker: {
		{key: "backspace", meaning: "go back"},
		{key: "h", meaning: "go back"},
		{key: "left", meaning: "go back"},
		{key: "enter", meaning: "open"},
		{key: "l", meaning: "open"},
		{key: "right", meaning: "open"},
		{key: "s", meaning: "export here"},
	},
	scopeNamespace: {{key: "w", meaning: "workloads"}},
	scopeResource: {
		{key: "r", meaning: "consumers"},
		{key: "L", meaning: "link"},
	},
	scopeKeys: {
		{key: "x", meaning: "export"},
		{key: "i", meaning: "import"},
		{key: "e", meaning: "edit all"},
		{key: "r", meaning: "consumers"},
		{key: "ctrl+z", meaning: "undo"},
	},
	scopeWorkload: {{key: "L", meaning: "link"}},
	scopeProject: {
		{key: "u", meaning: "unlink"},
		{key: "e", meaning: "edit"},
		{key: "s", meaning: "scan"},
	},
	scopeDiff: {
		{key: "ctrl+c", meaning: "quit confirmation"},
		{key: "e", meaning: "re-edit"},
		{key: "y", meaning: "confirm nudge"},
	},
}

// ScopeDefaultKeys exposes the modeled Bubbles defaults for dependency upgrade tests.
func ScopeDefaultKeys(scope string) map[string][]string {
	class := scopeClass(scope)
	if class != scopeList && class != scopeViewport {
		return nil
	}
	defaults := make(map[string][]string)
	for _, spec := range actionSpecs {
		if keys := spec.defaults[class]; len(keys) > 0 {
			defaults[string(spec.name)] = append([]string(nil), keys...)
		}
	}
	for _, fixed := range fixedKeys[class] {
		defaults[fixed.meaning] = append(defaults[fixed.meaning], fixed.key)
	}
	return defaults
}

type keyClaim struct {
	id    string
	name  string
	line  int
	text  string
	fixed bool
}

func (p *parser) validateScopeCollisions() {
	screens := make([]string, 0, len(screenClasses))
	for screen := range screenClasses {
		screens = append(screens, screen)
	}
	sort.Strings(screens)

	seen := make(map[string]bool)
	for _, screen := range screens {
		claims := p.claimsForScreen(screenClasses[screen])
		keys := make([]string, 0, len(claims))
		for key := range claims {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, key := range keys {
			owners := claims[key]
			for i := 0; i < len(owners); i++ {
				for j := i + 1; j < len(owners); j++ {
					first, second := owners[i], owners[j]
					if first.id == second.id {
						continue
					}
					pair := []string{first.id, second.id}
					sort.Strings(pair)
					collisionID := key + "\x00" + pair[0] + "\x00" + pair[1]
					if seen[collisionID] {
						continue
					}
					seen[collisionID] = true

					introduced := laterClaim(first, second)
					if introduced.line == 0 {
						continue
					}
					msg := fmt.Sprintf("%q is bound to both %s and %s on the %s screen", key, first.name, second.name, screen)
					hint := "rebind the other action too, or pick another key"
					if first.fixed {
						msg = fmt.Sprintf("%q is already used for %s on the %s screen", key, first.name, screen)
						hint = "that key is not rebindable; pick another key"
					} else if second.fixed {
						msg = fmt.Sprintf("%q is already used for %s on the %s screen", key, second.name, screen)
						hint = "that key is not rebindable; pick another key"
					}
					p.addError(introduced.line, introduced.text, msg, hint)
				}
			}
		}
	}
}

func (p *parser) claimsForScreen(classes []scopeClass) map[string][]keyClaim {
	claims := make(map[string][]keyClaim)
	for _, spec := range actionSpecs {
		defaults := defaultsForClasses(spec, classes)
		if len(defaults) == 0 {
			continue
		}

		keys, overridden := p.config.Keybinds[spec.name]
		if !overridden {
			keys = defaults
		}
		for _, key := range keys {
			claim := keyClaim{id: "action:" + string(spec.name), name: string(spec.name)}
			if overridden {
				binding := p.bindingByActionAndKey[spec.name][key]
				claim.line = binding.line
				claim.text = binding.text
			} else {
				claim.name += " (default)"
			}
			claims[key] = append(claims[key], claim)
		}
	}

	for _, class := range classes {
		for _, fixed := range fixedKeys[class] {
			claims[fixed.key] = append(claims[fixed.key], keyClaim{
				id:    "fixed:" + fixed.meaning,
				name:  fixed.meaning,
				fixed: true,
			})
		}
	}
	return claims
}

func defaultsForClasses(spec actionSpec, classes []scopeClass) []string {
	var keys []string
	seen := make(map[string]bool)
	for _, class := range classes {
		for _, key := range spec.defaults[class] {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func laterClaim(first, second keyClaim) keyClaim {
	if second.line > first.line {
		return second
	}
	return first
}
