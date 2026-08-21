package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NoahHakansson/sk64/internal/store"
)

func TestRepoRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	inner := filepath.Join(repo, "inner")
	worktree := filepath.Join(root, "worktree")
	plain := filepath.Join(root, "plain")
	for _, dir := range []string{filepath.Join(repo, "sub", "dir"), filepath.Join(inner, "x"), worktree, plain} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", dir, err)
		}
	}
	for _, dir := range []string{repo, inner} {
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
			t.Fatalf("Mkdir(.git) error = %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
		t.Fatalf("write worktree .git: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	resolvedRepo, _ := filepath.EvalSymlinks(repo)
	resolvedWorktree, _ := filepath.EvalSymlinks(worktree)
	resolvedPlain, _ := filepath.EvalSymlinks(plain)
	resolvedInner, _ := filepath.EvalSymlinks(inner)
	tests := []struct{ name, cwd, want string }{
		{name: "repo root", cwd: repo, want: resolvedRepo},
		{name: "nested", cwd: filepath.Join(repo, "sub", "dir"), want: resolvedRepo},
		{name: "worktree file", cwd: worktree, want: resolvedWorktree},
		{name: "no git", cwd: plain, want: resolvedPlain},
		{name: "nearest nested", cwd: filepath.Join(inner, "x"), want: resolvedInner},
		{name: "symlink", cwd: filepath.Join(alias, "sub"), want: resolvedRepo},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RepoRoot(test.cwd); got != test.want {
				t.Fatalf("RepoRoot(%q) = %q, want %q", test.cwd, got, test.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	matchDir := filepath.Join(root, "match")
	flagDir := filepath.Join(root, "flag")
	noMatch := filepath.Join(root, "other")
	for _, dir := range []string{matchDir, flagDir, noMatch} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "sk64.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	match, _ := st.CreateProject(ctx, store.ProjectMeta{Name: "match", RootPath: RepoRoot(matchDir), KubeContext: "ctx", Namespace: "ns"})
	flagged, _ := st.CreateProject(ctx, store.ProjectMeta{Name: "flagged", RootPath: RepoRoot(flagDir), KubeContext: "ctx", Namespace: "ns"})
	tests := []struct {
		name, cwd, flag string
		noProject       bool
		st              *store.Store
		wantID          int64
		wantRoot        string
		wantErr         error
	}{
		{name: "flag wins", cwd: matchDir, flag: "flagged", st: st, wantID: flagged.ID, wantRoot: RepoRoot(matchDir)},
		{name: "flag without usable cwd", flag: "flagged", noProject: true, st: st, wantID: flagged.ID},
		{name: "flag unknown", cwd: matchDir, flag: "unknown", st: st, wantErr: store.ErrNotFound},
		{name: "flag nil store", cwd: matchDir, flag: "flagged"},
		{name: "no project", cwd: matchDir, noProject: true, st: st},
		{name: "cwd match", cwd: matchDir, st: st, wantID: match.ID, wantRoot: RepoRoot(matchDir)},
		{name: "cwd no match", cwd: noMatch, st: st, wantRoot: RepoRoot(noMatch)},
		{name: "nil store", cwd: matchDir, wantRoot: RepoRoot(matchDir)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Resolve(ctx, test.st, test.cwd, test.flag, test.noProject)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Resolve() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if test.flag != "" && test.st == nil {
				if err == nil {
					t.Fatal("Resolve() error = nil for explicit flag with nil store")
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.Root != test.wantRoot {
				t.Fatalf("Resolve().Root = %q, want %q", got.Root, test.wantRoot)
			}
			if test.wantID == 0 && got.Project != nil {
				t.Fatalf("Resolve().Project = %+v, want nil", got.Project)
			}
			if test.wantID != 0 && (got.Project == nil || got.Project.ID != test.wantID) {
				t.Fatalf("Resolve().Project = %+v, want ID %d", got.Project, test.wantID)
			}
		})
	}
}
