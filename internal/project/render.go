package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultRenderTimeout = 30 * time.Second

var errRenderTimeout = errors.New("render timed out")

type execToolRunner struct{ timeout time.Duration }

func (r execToolRunner) Run(ctx context.Context, dir string, argv []string) ([]byte, string, error) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, "", errors.New("run renderer: argv[0] must be an absolute path")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	// #nosec G204 -- argv[0] is an absolute LookPath result and arguments are passed without a shell.
	cmd := exec.CommandContext(timeoutCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return nil, firstLine(stderr.String()), fmt.Errorf("%s: %w", argv[0], errRenderTimeout)
		}
		return nil, firstLine(stderr.String()), fmt.Errorf("run %s: %w", argv[0], err)
	}
	return stdout.Bytes(), "", nil
}

func firstLine(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func findTool(names ...string) (string, bool) {
	for _, name := range names {
		resolved, err := exec.LookPath(name)
		// err includes exec.ErrDot: reject relative-to-cwd resolutions.
		if err != nil {
			continue
		}
		absolute, err := filepath.Abs(resolved)
		if err == nil {
			return absolute, true
		}
	}
	return "", false
}

func kustomizeArgv(kustomizePath, kubectlPath string) []string {
	if kustomizePath != "" {
		return []string{kustomizePath, "build", "."}
	}
	if kubectlPath != "" {
		return []string{kubectlPath, "kustomize", "."}
	}
	return nil
}

func helmArgv(helmPath string, valuesFiles []string) []string {
	if helmPath == "" {
		return nil
	}
	files := append([]string(nil), valuesFiles...)
	sort.Strings(files)
	argv := []string{helmPath, "template", "."}
	for _, file := range files {
		argv = append(argv, "--values", file)
	}
	return argv
}
