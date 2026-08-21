package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NoahHakansson/sk64/internal/store"
)

// RepoRoot returns the nearest ancestor containing .git. It resolves symlinks
// when possible and otherwise searches from the cleaned input path.
func RepoRoot(cwd string) string {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		resolved = filepath.Clean(cwd)
	}
	current := resolved
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return resolved
		}
		current = parent
	}
}

// Resolution describes the selected project and the repository root used for matching.
type Resolution struct {
	Project *store.Project
	Root    string
}

// Resolve selects an explicit project or matches cwd to a stored root. A nil
// store disables cwd matching but is an error for an explicit project selection.
func Resolve(ctx context.Context, st *store.Store, cwd, projectFlag string, noProject bool) (Resolution, error) {
	root := ""
	if !noProject {
		root = RepoRoot(cwd)
	}
	if projectFlag != "" {
		if st == nil {
			return Resolution{}, fmt.Errorf("open project %q: project database unavailable", projectFlag)
		}
		matched, err := st.ProjectByName(ctx, projectFlag)
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve project %q: %w", projectFlag, err)
		}
		return Resolution{Project: &matched, Root: root}, nil
	}
	if noProject {
		return Resolution{}, nil
	}
	resolution := Resolution{Root: root}
	if st == nil {
		return resolution, nil
	}
	matched, err := st.ProjectByRoot(ctx, root)
	if errors.Is(err, store.ErrNotFound) {
		return resolution, nil
	}
	if err != nil {
		return resolution, fmt.Errorf("resolve project for %q: %w", root, err)
	}
	resolution.Project = &matched
	return resolution, nil
}
