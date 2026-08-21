package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/store"
)

func TestStartupProjectContext(t *testing.T) {
	project := store.Project{KubeContext: "project-context"}
	suppressedProject := project
	suppressedProject.SwitchPromptSuppressed = true
	repointedProject := suppressedProject
	repointedProject.KubeServer = "https://stored.example"
	caseVariantProject := suppressedProject
	caseVariantProject.KubeServer = "HTTPS://API.EXAMPLE"
	defaultPortProject := suppressedProject
	defaultPortProject.KubeServer = "https://api.example:443"
	trailingSlashProject := suppressedProject
	trailingSlashProject.KubeServer = "https://api.example/"
	tests := []struct {
		name           string
		project        *store.Project
		currentContext string
		currentServer  string
		targetServer   string
		want           projectContextStartup
	}{
		{name: "divergence asks", project: &project, currentContext: "default-context", want: confirmStartupContext},
		{name: "same context stays", project: &project, currentContext: "project-context", want: keepStartupContext},
		{name: "suppressed divergence switches", project: &suppressedProject, currentContext: "default-context", want: switchStartupContext},
		{name: "suppressed repointed context asks", project: &repointedProject, currentContext: "default-context", targetServer: "https://new.example", want: confirmStartupContext},
		{name: "active repointed context asks", project: &repointedProject, currentContext: "project-context", currentServer: "https://new.example", targetServer: "https://new.example", want: confirmStartupContext},
		{name: "active scheme and host case stays", project: &caseVariantProject, currentContext: "project-context", currentServer: "https://api.example", targetServer: "https://api.example", want: keepStartupContext},
		{name: "active explicit default port stays", project: &defaultPortProject, currentContext: "project-context", currentServer: "https://api.example", targetServer: "https://api.example", want: keepStartupContext},
		{name: "active trailing slash stays", project: &trailingSlashProject, currentContext: "project-context", currentServer: "https://api.example", targetServer: "https://api.example", want: keepStartupContext},
		{name: "suppressed equivalent target switches", project: &caseVariantProject, currentContext: "default-context", targetServer: "https://api.example", want: switchStartupContext},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := startupProjectContext(test.project, test.currentContext, test.currentServer, k8s.ContextInfo{Server: test.targetServer}); got != test.want {
				t.Fatalf("startupProjectContext() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRunFailsWhenDebugLogUnopenable(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "missing", "debug.log")
	if exitCode := run([]string{"--debug-log", path}, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "open debug log") {
		t.Fatalf("stderr = %q, want debug log failure", stderr.String())
	}
}

func TestRunReportsInvalidConfigWithoutStartingClusterSetup(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := filepath.Join(configRoot, "sk64", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	contents := "# invalid\ntheme = dark\nkeybind = ctrl+e=refrsh\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(nil, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	got := stderr.String()
	for _, want := range []string{"invalid config", path, "line 2:", `"theme = dark"`, "fix:", "line 3:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "kubeconfig") || strings.Contains(got, "cluster") || strings.Contains(got, "probe") {
		t.Fatalf("config failure reached cluster setup:\n%s", got)
	}
}

func TestRunMissingConfigContinuesStartup(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(nil, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("run() = %d, want isolated kubeconfig failure 1", exitCode)
	}
	if strings.Contains(stderr.String(), "sk64: invalid config:") || strings.Contains(stderr.String(), filepath.Join(configRoot, "sk64", "config")) {
		t.Fatalf("missing user config was reported as an error:\n%s", stderr.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("startup did not reach the isolated kubeconfig failure")
	}
}

func TestRunUnavailableConfigPathContinuesStartup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(nil, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("run() = %d, want isolated kubeconfig failure 1", exitCode)
	}
	got := stderr.String()
	if !strings.Contains(got, "sk64: config unavailable: config path unavailable") {
		t.Fatalf("stderr = %q, want optional config notice", got)
	}
	if !strings.Contains(got, "kubeconfig") {
		t.Fatalf("startup did not continue to isolated kubeconfig failure:\n%s", got)
	}
}

func TestRunUnreadableConfigUsesRuntimeFailureFormat(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := filepath.Join(configRoot, "sk64", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("keybind = ctrl+e=refresh\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("make config unreadable: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(nil, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	got := stderr.String()
	if !strings.Contains(got, "sk64: read config "+path) {
		t.Fatalf("stderr = %q, want runtime config read failure", got)
	}
	if strings.Contains(got, "invalid config") || strings.Contains(got, "line 1:") || strings.Contains(got, "fix:") {
		t.Fatalf("I/O failure used validation formatting:\n%s", got)
	}
}

func TestHangupRunsEditorCleanup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHangupChild$") // #nosec G204,G702 -- the test deliberately re-executes its own trusted binary.
	cmd.Env = append(os.Environ(), "SK64_TEST_HANGUP=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("read child temp path: %v", scanner.Err())
	}
	path := scanner.Text()
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("signal child: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child exit: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("editor temp directory still exists: %v", err)
	}
}

func TestHangupChild(t *testing.T) {
	if os.Getenv("SK64_TEST_HANGUP") != "1" {
		return
	}
	dir, err := editor.NewDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dir.WriteFile("Secret", "x", "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	ctx, stop := notifyShutdown(context.Background())
	defer stop()
	defer editor.CleanupAll()
	timeout := time.AfterFunc(10*time.Second, func() { os.Exit(2) })
	defer timeout.Stop()
	if _, err := os.Stdout.WriteString(dir.Path + "\n"); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
}
