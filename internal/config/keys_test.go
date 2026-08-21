package config

import (
	"sort"
	"testing"
)

func TestValidateKey(t *testing.T) {
	valid := []string{"a", "J", "5", "=", "+", "ctrl+e", "alt+enter", "shift+tab", "ctrl+alt+shift+up", "space", "f5"}
	named := make([]string, 0, len(namedKeys))
	for name := range namedKeys {
		named = append(named, name)
	}
	sort.Strings(named)
	valid = append(valid, named...)

	for _, name := range valid {
		t.Run("valid "+name, func(t *testing.T) {
			msg, hint, ok := validateKey(name)
			if !ok || msg != "" || hint != "" {
				t.Fatalf("validateKey(%q) = (%q, %q, %t), want valid", name, msg, hint, ok)
			}
		})
	}

	invalid := []struct {
		name string
		hint string
	}{
		{name: "shift+j", hint: `write the uppercase letter instead, e.g. "J"`},
		{name: "ctrl+R", hint: `write "ctrl+r"`},
		{name: "CTRL+r", hint: representableKeysHint},
		{name: "shift+ctrl+a", hint: modifierOrderHint},
		{name: "ctrl+ctrl+a", hint: modifierOrderHint},
		{name: "ctrl+", hint: representableKeysHint},
		{name: "ctrl+,", hint: "most terminals cannot send this chord"},
		{name: "ä", hint: representableKeysHint},
		{name: "pgdn", hint: representableKeysHint},
		{name: "escape", hint: representableKeysHint},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			msg, hint, ok := validateKey(tt.name)
			if ok || msg != "key cannot be represented" || hint != tt.hint {
				t.Fatalf("validateKey(%q) = (%q, %q, %t), want (%q, %q, false)", tt.name, msg, hint, ok, "key cannot be represented", tt.hint)
			}
		})
	}
}

func TestReservedKeysDefinition(t *testing.T) {
	want := []ReservedKey{
		{Key: "N", Meaning: "starts create flows (uppercase mutation initiator)"},
		{Key: "D", Meaning: "starts delete flows (uppercase mutation initiator)"},
		{Key: "R", Meaning: "starts restart flows (uppercase mutation initiator)"},
		{Key: "Y", Meaning: "advances mutating confirms (uppercase mutation initiator)"},
		{Key: "esc", Meaning: "always cancels; closes a gate with nothing dispatched"},
		{Key: "enter", Meaning: "accepts typed or selected input only; never an alias for Y"},
	}
	got := ReservedKeys()
	if len(got) != len(want) {
		t.Fatalf("ReservedKeys() has %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ReservedKeys()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
