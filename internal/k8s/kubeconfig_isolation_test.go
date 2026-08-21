package k8s

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	rootModuleDeclaration = "module github.com/NoahHakansson/sk64"
	k8sImportPath         = "github.com/NoahHakansson/sk64/internal/k8s"
	kubetestImportPath    = "github.com/NoahHakansson/sk64/internal/kubetest"
)

type packageIsolation struct {
	touchesK8s      bool
	isolatesAmbient bool
}

func TestPackagesTouchingK8sIsolateAmbientCluster(t *testing.T) {
	root := findRootModule(t)
	packages := make(map[string]packageIsolation)

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "e2e" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		relativeDirectory, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		if relativeDirectory == "internal/kubetest" {
			return nil
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		isolation := packages[relativeDirectory]
		if relativeDirectory == "internal/k8s" || importsPath(parsed, k8sImportPath) {
			isolation.touchesK8s = true
		}
		if strings.HasSuffix(path, "_test.go") && testMainIsolatesAmbientCluster(parsed) {
			isolation.isolatesAmbient = true
		}
		packages[relativeDirectory] = isolation
		return nil
	})
	if err != nil {
		t.Fatalf("walk root module packages: %v", err)
	}

	for packagePath, isolation := range packages {
		if isolation.touchesK8s && !isolation.isolatesAmbient {
			t.Errorf("%s imports %s but its test files do not declare a TestMain that calls kubetest.IsolateAmbientCluster", packagePath, k8sImportPath)
		}
	}
}

func importsPath(file *ast.File, target string) bool {
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err == nil && importPath == target {
			return true
		}
	}
	return false
}

func testMainIsolatesAmbientCluster(file *ast.File) bool {
	kubetestNames := importedNames(file, kubetestImportPath)
	if len(kubetestNames) == 0 {
		return false
	}

	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "TestMain" || function.Body == nil {
			continue
		}
		found := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "IsolateAmbientCluster" {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if ok && kubetestNames[packageName.Name] {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func importedNames(file *ast.File, target string) map[string]bool {
	names := make(map[string]bool)
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil || importPath != target {
			continue
		}
		if imported.Name == nil {
			names[filepath.Base(importPath)] = true
		} else if imported.Name.Name != "." && imported.Name.Name != "_" {
			names[imported.Name.Name] = true
		}
	}
	return names
}

func findRootModule(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate current test file")
	}
	directory := filepath.Dir(currentFile)
	for {
		goModPath := filepath.Join(directory, "go.mod")
		if contents, err := os.ReadFile(goModPath); err == nil { //nolint:gosec // The path is built only from parent directories of this source file.
			if strings.Contains(string(contents), rootModuleDeclaration) {
				return directory
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("read %s: %v", goModPath, err)
		}

		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("find go.mod containing %q", rootModuleDeclaration)
		}
		directory = parent
	}
}
