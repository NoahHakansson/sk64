package project

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NoahHakansson/sk64/internal/k8s"
)

var helmLiteralName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

func extractHelm(ctx context.Context, root, dirRel, helmPath string, valuesHints []string, separator string, runner execToolRunner) (suggestions []Suggestion, notes []string, incomplete bool) {
	dir := filepath.Join(root, filepath.FromSlash(dirRel))
	matchedValues := existingValuesFiles(dir, valuesHints)
	if helmPath == "" {
		suggestions, notes = extractHelmLiteral(root, dirRel)
		return suggestions, append([]string{"helm not on PATH" + separator + "charts parsed literally"}, notes...), true
	}
	output, stderrLine, err := runner.Run(ctx, dir, helmArgv(helmPath, matchedValues))
	if err != nil {
		reason := "failed"
		if errors.Is(err, errRenderTimeout) {
			reason = "timed out"
		}
		note := fmt.Sprintf("helm template %s in %s%sliteral extraction", reason, displayDir(dirRel), separator)
		if stderrLine != "" {
			note += ": " + stderrLine
		}
		suggestions, notes = extractHelmLiteral(root, dirRel)
		return suggestions, append([]string{note}, notes...), true
	}
	detail := "default values"
	if len(matchedValues) > 0 {
		detail = "values: " + strings.Join(matchedValues, ", ")
	}
	provenance := filepath.ToSlash(filepath.Join(dirRel, "Chart.yaml"))
	for _, doc := range splitDocs(output) {
		suggestions = append(suggestions, suggestionsFromDoc(provenance, 0, ModeRendered, detail, doc.content)...)
	}
	return suggestions, nil, false
}

func existingValuesFiles(dir string, hints []string) []string {
	seen := make(map[string]struct{})
	var matches []string
	for _, hint := range hints {
		name := filepath.Base(hint)
		if _, ok := seen[name]; ok {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.Mode().IsRegular() {
			seen[name] = struct{}{}
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	return matches
}

func extractHelmLiteral(root, dirRel string) (suggestions []Suggestion, notes []string) {
	templates := filepath.Join(root, filepath.FromSlash(dirRel), "templates")
	err := filepath.WalkDir(templates, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, readErr := safeReadFile(root, filePath)
		if readErr != nil {
			notes = appendScanFileNote(notes, readErr)
			return nil
		}
		relPath, err := filepath.Rel(root, filePath)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		// Within each document, the line scanner runs only when structured
		// extraction yields nothing. Across documents, both may contribute.
		for _, doc := range splitDocs(data) {
			if fromDoc := suggestionsFromDoc(relPath, doc.line, ModeLiteral, "helm: literal YAML", doc.content); len(fromDoc) > 0 {
				suggestions = append(suggestions, fromDoc...)
				continue
			}
			suggestions = append(suggestions, extractHelmTemplate(relPath, doc.line, doc.content)...)
		}
		return nil
	})
	// A chart may legitimately omit templates/ entirely.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		notes = append(notes, fmt.Sprintf("helm literal extraction failed in %s: %v", displayDir(dirRel), err))
	}
	return suggestions, notes
}

// extractHelmTemplate scans one document whose startLine is 1-based.
func extractHelmTemplate(relPath string, startLine int, data []byte) []Suggestion {
	base := Suggestion{File: relPath, Mode: ModeLiteral, Detail: "helm: templated"}
	var suggestions []Suggestion
	pendingKind := ""
	pendingLines := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, len(data)+1)
	line := startLine - 1
	for scanner.Scan() {
		line++
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "-"))
		switch {
		case strings.HasPrefix(trimmed, "secretKeyRef:") || strings.HasPrefix(trimmed, "secretRef:"):
			pendingKind, pendingLines = k8s.KindSecret, 2
		case strings.HasPrefix(trimmed, "configMapKeyRef:") || strings.HasPrefix(trimmed, "configMapRef:"):
			pendingKind, pendingLines = k8s.KindConfigMap, 2
		case strings.HasPrefix(trimmed, "secretName:"):
			name := cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "secretName:")))
			if helmLiteralName.MatchString(name) {
				item := base
				item.Kind, item.Name, item.Line = k8s.KindSecret, name, line
				suggestions = append(suggestions, item)
			}
		case pendingKind != "" && strings.HasPrefix(trimmed, "name:"):
			name := cleanYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "name:")))
			if helmLiteralName.MatchString(name) {
				item := base
				item.Kind, item.Name, item.Line = pendingKind, name, line
				suggestions = append(suggestions, item)
			}
			pendingKind, pendingLines = "", 0
		default:
			if pendingLines > 0 {
				pendingLines--
				if pendingLines == 0 {
					pendingKind = ""
				}
			}
		}
	}
	return suggestions
}

func cleanYAMLScalar(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], `"'`)
}
