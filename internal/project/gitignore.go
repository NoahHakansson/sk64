package project

import (
	"bufio"
	"bytes"
	"path"
	"strings"
)

type ignoreRule struct {
	dirRel   string
	negated  bool
	dirOnly  bool
	anchored bool
	segments []string
}

// ignoreMatcher evaluates the supported subset of collected .gitignore rules.
type ignoreMatcher struct{ rules []ignoreRule }

func newIgnoreMatcher() *ignoreMatcher { return &ignoreMatcher{} }

func (m *ignoreMatcher) addFile(dirRel string, content []byte) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(nil, len(content)+1)
	for scanner.Scan() {
		pattern := strings.TrimSpace(scanner.Text())
		if pattern == "" || strings.HasPrefix(pattern, "#") || strings.Contains(pattern, "\\") {
			continue
		}
		rule := ignoreRule{dirRel: strings.Trim(dirRel, "/")}
		if strings.HasPrefix(pattern, "!") {
			rule.negated = true
			pattern = strings.TrimPrefix(pattern, "!")
		}
		if pattern == "" {
			continue
		}
		rule.dirOnly = strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		rule.anchored = strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "/")
		pattern = strings.TrimPrefix(pattern, "/")
		if pattern == "" {
			continue
		}
		rule.segments = strings.Split(pattern, "/")
		m.rules = append(m.rules, rule)
	}
}

func (m *ignoreMatcher) ignored(rel string, isDir bool) bool {
	rel = strings.Trim(rel, "/")
	ignored := false
	for _, rule := range m.rules {
		if rule.dirOnly && !isDir {
			continue
		}
		candidate, ok := belowRuleDir(rel, rule.dirRel)
		if !ok {
			continue
		}
		segments := splitPath(candidate)
		matched := false
		if !rule.anchored && len(rule.segments) == 1 {
			for _, segment := range segments {
				if segmentMatch(rule.segments[0], segment) {
					matched = true
					break
				}
			}
		} else {
			matched = matchSegments(rule.segments, segments)
		}
		if matched {
			ignored = !rule.negated
		}
	}
	return ignored
}

func belowRuleDir(rel, dirRel string) (string, bool) {
	if dirRel == "" {
		return rel, true
	}
	if rel == dirRel {
		return "", true
	}
	prefix := dirRel + "/"
	if !strings.HasPrefix(rel, prefix) {
		return "", false
	}
	return strings.TrimPrefix(rel, prefix), true
}

func splitPath(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func matchSegments(patterns, values []string) bool {
	if len(patterns) == 0 {
		return len(values) == 0
	}
	if patterns[0] == "**" {
		if matchSegments(patterns[1:], values) {
			return true
		}
		return len(values) > 0 && matchSegments(patterns, values[1:])
	}
	if len(values) == 0 || !segmentMatch(patterns[0], values[0]) {
		return false
	}
	return matchSegments(patterns[1:], values[1:])
}

func segmentMatch(pattern, value string) bool {
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}
