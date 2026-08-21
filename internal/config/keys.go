package config

import (
	"fmt"
	"strings"
)

var namedKeys = map[string]struct{}{
	"up": {}, "down": {}, "left": {}, "right": {},
	"home": {}, "end": {}, "pgup": {}, "pgdown": {},
	"tab": {}, "enter": {}, "esc": {}, "space": {},
	"backspace": {}, "delete": {}, "insert": {},
	"f1": {}, "f2": {}, "f3": {}, "f4": {}, "f5": {}, "f6": {},
	"f7": {}, "f8": {}, "f9": {}, "f10": {}, "f11": {}, "f12": {},
}

const (
	representableKeysHint = "see representable keys in the README"
	modifierOrderHint     = "write modifiers as ctrl+alt+shift+<key>"
)

func validateKey(name string) (msg, hint string, ok bool) {
	if isBaseKey(name) {
		return "", "", true
	}
	if !strings.Contains(name, "+") {
		return "key cannot be represented", representableKeysHint, false
	}

	parts := strings.Split(name, "+")
	if len(parts) > 4 {
		return "key cannot be represented", representableKeysHint, false
	}
	for _, part := range parts {
		if part == "" {
			return "key cannot be represented", representableKeysHint, false
		}
	}

	modifierOrder := map[string]int{"ctrl": 0, "alt": 1, "shift": 2}
	previous := -1
	modifiers := make(map[string]bool, len(parts)-1)
	for _, modifier := range parts[:len(parts)-1] {
		order, found := modifierOrder[modifier]
		if !found {
			return "key cannot be represented", representableKeysHint, false
		}
		if order <= previous {
			return "key cannot be represented", modifierOrderHint, false
		}
		modifiers[modifier] = true
		previous = order
	}

	base := parts[len(parts)-1]
	if !isBaseKey(base) {
		return "key cannot be represented", representableKeysHint, false
	}
	if isASCIILetter(base) {
		if modifiers["shift"] {
			return "key cannot be represented", `write the uppercase letter instead, e.g. "J"`, false
		}
		if base[0] >= 'A' && base[0] <= 'Z' {
			parts[len(parts)-1] = strings.ToLower(base)
			return "key cannot be represented", fmt.Sprintf("write %q", strings.Join(parts, "+")), false
		}
		return "", "", true
	}
	if isASCIIDigit(base) {
		if modifiers["shift"] {
			return "key cannot be represented", "most terminals cannot send this chord", false
		}
		return "", "", true
	}
	if _, found := namedKeys[base]; found {
		return "", "", true
	}
	return "key cannot be represented", "most terminals cannot send this chord", false
}

func isBaseKey(name string) bool {
	if _, found := namedKeys[name]; found {
		return true
	}
	return len(name) == 1 && name[0] >= '!' && name[0] <= '~'
}

func isASCIILetter(name string) bool {
	return len(name) == 1 && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z'))
}

func isASCIIDigit(name string) bool {
	return len(name) == 1 && name[0] >= '0' && name[0] <= '9'
}
