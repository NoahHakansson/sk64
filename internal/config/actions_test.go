package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestUnknownActionSuggestion(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "refrsh", want: `did you mean "refresh"?`},
		{name: "hlep", want: `did you mean "help"?`},
		{name: "completely-different", want: "see rebindable actions in the README"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, err := parse(strings.NewReader("keybind = ctrl+e=" + tt.name))
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("Parse() errors = %#v, want one", got)
			}
			if got[0].Hint != tt.want {
				t.Fatalf("Parse() hint = %q, want %q", got[0].Hint, tt.want)
			}
		})
	}
}

func TestReservedCollision(t *testing.T) {
	for _, reserved := range ReservedKeys() {
		t.Run(reserved.Key, func(t *testing.T) {
			line := "keybind = " + reserved.Key + "=refresh"
			_, got, err := parse(strings.NewReader(line))
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("Parse() errors = %#v, want one", got)
			}
			wantMsg := `"` + reserved.Key + `" is reserved: ` + reserved.Meaning
			if got[0].Msg != wantMsg || got[0].Hint != "choose a different key" {
				t.Fatalf("Parse() error = %#v, want message %q and reserved hint", got[0], wantMsg)
			}
		})
	}
}

func TestScopeCollision(t *testing.T) {
	tests := []struct {
		name     string
		document string
		wantLine int
		wantMsg  string
	}{
		{
			name:     "override versus override",
			document: "keybind = x=up\nkeybind = x=down",
			wantLine: 2,
			wantMsg:  `"x" is bound to both up and down on the consumers screen`,
		},
		{
			name:     "override versus untouched default",
			document: "keybind = k=down",
			wantLine: 1,
			wantMsg:  `"k" is bound to both up (default) and down on the consumers screen`,
		},
		{
			name:     "replacement legalizes default key",
			document: "keybind = k=down\nkeybind = ctrl+e=up",
		},
		{
			name:     "cross class collision",
			document: "keybind = u=wrap",
			wantLine: 1,
			wantMsg:  `"u" is bound to both half-page-up (default) and wrap on the diff screen`,
		},
		{
			name:     "cross class separation",
			document: "keybind = w=values",
		},
		{
			name:     "global fixed key",
			document: "keybind = ctrl+c=refresh",
			wantLine: 1,
			wantMsg:  `"ctrl+c" is already used for quit confirmation on the consumers screen`,
		},
		{
			name:     "screen fixed key",
			document: "keybind = ctrl+z=values",
			wantLine: 1,
			wantMsg:  `"ctrl+z" is already used for undo on the keys screen`,
		},
		{name: "keys export", document: "keybind = x=quit", wantLine: 1, wantMsg: `"x" is already used for export on the keys screen`},
		{name: "keys edit all", document: "keybind = e=refresh", wantLine: 1, wantMsg: `"e" is already used for edit all on the keys screen`},
		{name: "resource link", document: "keybind = L=refresh", wantLine: 1, wantMsg: `"L" is already used for link on the resource screen`},
		{name: "namespace workloads", document: "keybind = w=all-namespaces", wantLine: 1, wantMsg: `"w" is already used for workloads on the namespace screen`},
		{name: "keys import", document: "keybind = i=help", wantLine: 1, wantMsg: `"i" is already used for import on the keys screen`},
		{name: "file picker back", document: "keybind = backspace=up", wantLine: 1, wantMsg: `"backspace" is already used for go back on the filepicker screen`},
		{name: "resource consumers", document: "keybind = r=type-cycle", wantLine: 1, wantMsg: `"r" is already used for consumers on the resource screen`},
		{name: "diff quit confirmation", document: "keybind = ctrl+c=wrap", wantLine: 1, wantMsg: `"ctrl+c" is already used for quit confirmation on the diff screen`},
		{name: "diff confirm nudge", document: "keybind = y=wrap", wantLine: 1, wantMsg: `"y" is already used for confirm nudge on the diff screen`},
		{name: "viewer quit", document: "keybind = Q=half-page-down", wantLine: 1, wantMsg: `"Q" is bound to both half-page-down and quit (default) on the hex screen`},
		{name: "viewer page down", document: "keybind = space=quit", wantLine: 1, wantMsg: `"space" is bound to both page-down (default) and quit on the hex screen`},
		{name: "viewer search", document: "keybind = ctrl+f=half-page-down", wantLine: 1, wantMsg: `"ctrl+f" is already used for search on the hex screen`},
		{
			name:     "attributed to later line",
			document: "keybind = x=down\n# spacer\nkeybind = x=up",
			wantLine: 3,
			wantMsg:  `"x" is bound to both up and down on the consumers screen`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, got, err := parse(strings.NewReader(tt.document))
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			if tt.wantMsg == "" {
				if len(got) != 0 {
					t.Fatalf("Parse() errors = %#v, want none", got)
				}
				if config.Keybinds == nil {
					t.Fatal("Parse() returned no overrides")
				}
				return
			}
			if !reflect.DeepEqual(config, Config{}) {
				t.Errorf("Parse() config = %#v, want zero Config", config)
			}
			var matched *Error
			for i := range got {
				if got[i].Line == tt.wantLine && got[i].Msg == tt.wantMsg {
					matched = &got[i]
					break
				}
			}
			if matched == nil {
				t.Fatalf("Parse() errors = %#v, want line %d message %q", got, tt.wantLine, tt.wantMsg)
			}
			wantHint := "rebind the other action too, or pick another key"
			if strings.Contains(tt.wantMsg, "is already used for") {
				wantHint = "that key is not rebindable; pick another key"
			}
			if matched.Text == "" || matched.Hint != wantHint {
				t.Fatalf("Parse() error metadata = %#v, want hint %q", *matched, wantHint)
			}
		})
	}
}

func TestRegistryInvariants(t *testing.T) {
	actionNames := make(map[Action]bool)
	reserved := make(map[string]bool)
	for _, entry := range ReservedKeys() {
		reserved[entry.Key] = true
	}
	referencedClasses := make(map[scopeClass]bool)
	for _, spec := range actionSpecs {
		if actionNames[spec.name] {
			t.Errorf("duplicate action %q", spec.name)
		}
		actionNames[spec.name] = true
		if !isKebabCaseASCII(string(spec.name)) {
			t.Errorf("action %q is not ASCII kebab-case", spec.name)
		}
		for class, keys := range spec.defaults {
			referencedClasses[class] = true
			for _, key := range keys {
				if msg, hint, ok := validateKey(key); !ok {
					t.Errorf("default %s key %q is invalid: %s (%s)", spec.name, key, msg, hint)
				}
				if reserved[key] {
					t.Errorf("default %s key %q is reserved", spec.name, key)
				}
			}
		}
	}
	for class := range fixedKeys {
		referencedClasses[class] = true
	}
	for class := range referencedClasses {
		found := false
		for _, classes := range screenClasses {
			for _, screenClass := range classes {
				if screenClass == class {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("class %q is not used by a screen", class)
		}
	}

	p := parser{bindingByActionAndKey: make(map[Action]map[string]parsedBinding)}
	for screen, classes := range screenClasses {
		claims := p.claimsForScreen(classes)
		for key, owners := range claims {
			for i := range owners {
				for j := i + 1; j < len(owners); j++ {
					if owners[i].id != owners[j].id && !owners[i].fixed && !owners[j].fixed {
						t.Errorf("default key %q collides between %s and %s on %s", key, owners[i].name, owners[j].name, screen)
					}
				}
			}
		}
	}
}

func isKebabCaseASCII(name string) bool {
	if name == "" || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	previousHyphen := false
	for i := range len(name) {
		char := name[i]
		if char == '-' {
			if previousHyphen {
				return false
			}
			previousHyphen = true
			continue
		}
		if char < 'a' || char > 'z' {
			return false
		}
		previousHyphen = false
	}
	return true
}
