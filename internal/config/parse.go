package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const maxConfigLineBytes = 1 << 20

type parsedBinding struct {
	line int
	text string
}

type parser struct {
	config                Config
	errors                Errors
	bindingByActionAndKey map[Action]map[string]parsedBinding
}

func parse(r io.Reader) (Config, Errors, error) {
	p := parser{bindingByActionAndKey: make(map[Action]map[string]parsedBinding)}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(nil, maxConfigLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		text := strings.TrimRight(scanner.Text(), "\r")
		if lineNumber == 1 {
			text = strings.TrimPrefix(text, "\uFEFF")
		}
		trimmed := strings.Trim(text, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		separator := strings.IndexByte(text, '=')
		if separator < 0 {
			p.addError(lineNumber, text, "cannot parse line", "expected key = value")
			continue
		}
		name := strings.Trim(text[:separator], " \t")
		value := strings.Trim(text[separator+1:], " \t")
		if name == "" {
			p.addError(lineNumber, text, "cannot parse line", "expected key = value")
			continue
		}

		if name != "keybind" {
			p.addError(lineNumber, text, fmt.Sprintf("unknown config key %q", name), "known keys: keybind")
			continue
		}
		p.parseKeybind(lineNumber, text, value)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			p.addError(lineNumber+1, "", "config line is too long", fmt.Sprintf("keep each line under %d bytes", maxConfigLineBytes))
			return Config{}, p.errors, nil
		}
		return Config{}, nil, err
	}

	p.validateScopeCollisions()
	sort.SliceStable(p.errors, func(i, j int) bool {
		return p.errors[i].Line < p.errors[j].Line
	})
	if len(p.errors) > 0 {
		return Config{}, p.errors, nil
	}
	return p.config, nil, nil
}

func (p *parser) parseKeybind(line int, text, value string) {
	separator := strings.LastIndexByte(value, '=')
	if separator < 0 {
		p.addError(line, text, "cannot parse keybind", "expected keybind = <key>=<action>")
		return
	}
	key := strings.Trim(value[:separator], " \t")
	actionName := strings.Trim(value[separator+1:], " \t")
	if key == "" || actionName == "" {
		p.addError(line, text, "cannot parse keybind", "expected keybind = <key>=<action>")
		return
	}

	if msg, hint, ok := validateKey(key); !ok {
		p.addError(line, text, msg, hint)
		return
	}
	action := Action(actionName)
	if _, found := actionByName[action]; !found {
		p.addError(line, text, fmt.Sprintf("unknown action %q", actionName), actionSuggestion(actionName))
		return
	}
	for _, reserved := range ReservedKeys() {
		if key == reserved.Key {
			p.addError(line, text, fmt.Sprintf("%q is reserved: %s", key, reserved.Meaning), "choose a different key")
			return
		}
	}

	if p.bindingByActionAndKey[action] == nil {
		p.bindingByActionAndKey[action] = make(map[string]parsedBinding)
	}
	if _, duplicate := p.bindingByActionAndKey[action][key]; duplicate {
		return
	}
	p.bindingByActionAndKey[action][key] = parsedBinding{line: line, text: text}
	if p.config.Keybinds == nil {
		p.config.Keybinds = make(Overrides)
	}
	p.config.Keybinds[action] = append(p.config.Keybinds[action], key)
}

func (p *parser) addError(line int, text, msg, hint string) {
	p.errors = append(p.errors, Error{Line: line, Text: text, Msg: msg, Hint: hint})
}

func actionSuggestion(name string) string {
	bestDistance := 3
	best := Action("")
	for _, spec := range actionSpecs {
		distance := levenshtein(name, string(spec.name))
		if distance < bestDistance {
			bestDistance = distance
			best = spec.name
		}
	}
	if best != "" {
		return fmt.Sprintf("did you mean %q?", best)
	}
	return "see rebindable actions in the README"
}

func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			current[j] = min(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}
