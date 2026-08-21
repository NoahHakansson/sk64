package project

import (
	"bufio"
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

type ciHints struct {
	namespaces   []Suggestion
	contexts     []string
	contextNotes []string
	valuesFiles  []string
}

var (
	ciTool         = regexp.MustCompile(`(?:^|&&|\|\||[;|])\s*(?:-\s+)?(?:(?:run|script):\s+)?["']?(kubectl|helm)(?:\s+|$)`)
	ciNamespace    = regexp.MustCompile(`(?:--namespace(?:=|\s+)|(?:^|\s)-n\s+)["']?([^\s"']+)`)
	kubectlContext = regexp.MustCompile(`(?:--context(?:=|\s+))["']?([^\s"']+)`)
	helmContext    = regexp.MustCompile(`(?:--kube-context(?:=|\s+))["']?([^\s"']+)`)
	ciValues       = regexp.MustCompile(`(?:--values(?:=|\s+)|(?:^|\s)-f\s+)["']?([^\s"']+\.ya?ml)`)
)

const maxJoinedCommandBytes = 64 << 10

type ciToolCommand struct {
	tool string
	args string
}

func extractCIToolCommands(command string) []ciToolCommand {
	command = trimCIComment(command)
	matches := ciTool.FindAllStringSubmatchIndex(command, -1)
	commands := make([]ciToolCommand, 0, len(matches))
	for _, match := range matches {
		args := command[match[3]:]
		end := len(args)
		for _, separator := range []string{"&&", ";", "|"} {
			if index := strings.Index(args, separator); index >= 0 && index < end {
				end = index
			}
		}
		commands = append(commands, ciToolCommand{tool: command[match[2]:match[3]], args: args[:end]})
	}
	return commands
}

func trimCIComment(command string) string {
	for index := 0; index < len(command); index++ {
		if command[index] == '#' && (index == 0 || command[index-1] == ' ' || command[index-1] == '\t') {
			return command[:index]
		}
	}
	return command
}

func extractCI(relPath string, data []byte) ciHints {
	var result ciHints
	namespaces := make(map[string]struct{})
	contexts := make(map[string]struct{})
	values := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, len(data)+1)
	line := 0
	addCommand := func(command string, startLine int) {
		if !strings.Contains(command, "kubectl") && !strings.Contains(command, "helm") {
			return
		}
		for _, match := range ciNamespace.FindAllStringSubmatch(command, -1) {
			value := match[1]
			if strings.Contains(value, "$") {
				continue
			}
			if _, ok := namespaces[value]; ok {
				continue
			}
			namespaces[value] = struct{}{}
			result.namespaces = append(result.namespaces, Suggestion{Kind: KindNamespace, Name: value, File: relPath, Line: startLine, Mode: ModeCI})
		}
		for _, toolCommand := range extractCIToolCommands(command) {
			contextPattern := kubectlContext
			if toolCommand.tool == "helm" {
				contextPattern = helmContext
			}
			for _, match := range contextPattern.FindAllStringSubmatch(toolCommand.args, -1) {
				value := match[1]
				if strings.Contains(value, "$") {
					continue
				}
				result.contexts = append(result.contexts, value)
				note := fmt.Sprintf("CI references kube context %q (%s:%d)", value, relPath, startLine)
				if _, ok := contexts[note]; !ok {
					contexts[note] = struct{}{}
					result.contextNotes = append(result.contextNotes, note)
				}
			}
		}
		if strings.Contains(command, "helm") {
			for _, match := range ciValues.FindAllStringSubmatch(command, -1) {
				value := match[1]
				if strings.Contains(value, "$") {
					continue
				}
				name := filepath.Base(value)
				if _, ok := values[name]; !ok {
					values[name] = struct{}{}
					result.valuesFiles = append(result.valuesFiles, name)
				}
			}
		}
	}
	var joined strings.Builder
	firstLine := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if joined.Len() == 0 {
			firstLine = line
		}
		if strings.HasSuffix(text, `\`) && joined.Len()+len(text) <= maxJoinedCommandBytes {
			joined.WriteString(strings.TrimSuffix(text, `\`))
			joined.WriteByte(' ')
			continue
		}
		joined.WriteString(text)
		addCommand(joined.String(), firstLine)
		joined.Reset()
	}
	if joined.Len() != 0 {
		addCommand(joined.String(), firstLine)
	}
	return result
}
