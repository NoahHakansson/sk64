package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/NoahHakansson/sk64/internal/k8s"
	"sigs.k8s.io/yaml"
)

type kustomization struct {
	Namespace          string               `json:"namespace"`
	ConfigMapGenerator []kustomizeGenerator `json:"configMapGenerator"`
	SecretGenerator    []kustomizeGenerator `json:"secretGenerator"`
}

type kustomizeGenerator struct {
	Name string `json:"name"`
}

func extractKustomize(ctx context.Context, root, dirRel string, argv []string, separator string, runner execToolRunner) (suggestions []Suggestion, notes []string, incomplete bool) {
	dir := filepath.Join(root, filepath.FromSlash(dirRel))
	marker, data, readNotes, readIncomplete := readKustomization(root, dir)
	if marker == "" {
		return nil, readNotes, readIncomplete
	}
	relPath := filepath.ToSlash(filepath.Join(dirRel, marker))
	var literal kustomization
	_ = yaml.Unmarshal(data, &literal)
	if len(argv) == 0 {
		return kustomizeLiteralSuggestions(relPath, literal, "kustomize not on PATH"),
			append(readNotes, "kustomize not on PATH"+separator+"kustomizations parsed literally"), true
	}
	output, stderrLine, err := runner.Run(ctx, dir, argv)
	if err != nil {
		detail := "kustomize build failed"
		reason := "failed"
		if errors.Is(err, errRenderTimeout) {
			detail = "kustomize build timed out"
			reason = "timed out"
		}
		note := fmt.Sprintf("kustomize build %s in %s%sliteral extraction", reason, displayDir(dirRel), separator)
		if stderrLine != "" {
			note += ": " + stderrLine
		}
		return kustomizeLiteralSuggestions(relPath, literal, detail), append(readNotes, note), true
	}
	for _, doc := range splitDocs(output) {
		suggestions = append(suggestions, suggestionsFromDoc(relPath, 0, ModeRendered, "kustomize", doc.content)...)
	}
	for i := range suggestions {
		generators := literal.ConfigMapGenerator
		if suggestions[i].Kind == k8s.KindSecret {
			generators = literal.SecretGenerator
		} else if suggestions[i].Kind != k8s.KindConfigMap {
			continue
		}
		for _, generator := range generators {
			if strings.HasPrefix(suggestions[i].Name, generator.Name+"-") {
				suggestions[i].Name = generator.Name
				suggestions[i].NamePrefix = true
				break
			}
		}
	}
	return suggestions, readNotes, readIncomplete
}

func readKustomization(root, dir string) (string, []byte, []string, bool) {
	var notes []string
	incomplete := false
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		data, err := safeReadFile(root, filepath.Join(dir, name))
		if err == nil {
			return name, data, notes, incomplete
		}
		notes = appendScanFileNote(notes, err)
		if !errors.Is(err, fs.ErrNotExist) {
			incomplete = true
		}
	}
	return "", nil, notes, incomplete
}

func kustomizeLiteralSuggestions(relPath string, literal kustomization, detail string) []Suggestion {
	base := Suggestion{File: relPath, Mode: ModeLiteral, Detail: detail}
	var suggestions []Suggestion
	if literal.Namespace != "" {
		item := base
		item.Kind, item.Name = KindNamespace, literal.Namespace
		suggestions = append(suggestions, item)
	}
	for _, generator := range literal.SecretGenerator {
		if generator.Name != "" {
			item := base
			item.Kind, item.Name, item.Namespace, item.NamePrefix = k8s.KindSecret, generator.Name, literal.Namespace, true
			suggestions = append(suggestions, item)
		}
	}
	for _, generator := range literal.ConfigMapGenerator {
		if generator.Name != "" {
			item := base
			item.Kind, item.Name, item.Namespace, item.NamePrefix = k8s.KindConfigMap, generator.Name, literal.Namespace, true
			suggestions = append(suggestions, item)
		}
	}
	return suggestions
}

func displayDir(dirRel string) string {
	if dirRel == "" {
		return "."
	}
	return dirRel
}
