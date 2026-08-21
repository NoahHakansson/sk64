package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NoahHakansson/sk64/internal/config"
	"github.com/NoahHakansson/sk64/internal/debuglog"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/NoahHakansson/sk64/internal/tui"
	"golang.org/x/term"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type projectContextStartup int

const (
	keepStartupContext projectContextStartup = iota
	confirmStartupContext
	switchStartupContext
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the testable entrypoint. Exit codes: 0 ok/help/version,
// 1 runtime/config failure, 2 flag error.
func run(args []string, stdout, stderr io.Writer) int {
	options, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	defer editor.CleanupAll()

	if options.showVersion {
		_, _ = fmt.Fprintf(stdout, "sk64 %s (commit %s, built %s)\n", version, commit, date)
		return 0
	}

	var debug *debuglog.Logger
	if options.debugLog != "" {
		debug, err = debuglog.Open(options.debugLog)
		if err != nil {
			return reportRuntimeFailure(stderr, err)
		}
		defer func() {
			if closeErr := debug.Close(); closeErr != nil {
				_, _ = fmt.Fprintf(stderr, "sk64: close debug log: %v\n", closeErr)
			}
		}()
		debug.Op("startup")
	}

	ctx, stop := notifyShutdown(context.Background())
	defer stop()

	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		var validation config.Errors
		if errors.As(cfgErr, &validation) {
			path, _ := config.Path()
			if isTerminalWriter(stdout) {
				if popupErr := tui.RunConfigError(ctx, path, validation, options.ascii || tui.DetectASCII(os.Getenv)); popupErr != nil {
					return reportRuntimeFailure(stderr, popupErr)
				}
			} else {
				reportConfigErrors(stderr, path, validation)
			}
			return 1
		}
		if errors.Is(cfgErr, config.ErrPathUnavailable) {
			_, _ = fmt.Fprintf(stderr, "sk64: config unavailable: %v\n", cfgErr)
			cfg = config.Config{}
		} else {
			return reportRuntimeFailure(stderr, cfgErr)
		}
	}

	var st *store.Store
	dbPath := options.db
	if dbPath == "" {
		dbPath, err = store.DefaultPath()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "sk64: project database unavailable: %v\n", err)
		}
	}
	if dbPath != "" {
		st, err = store.Open(dbPath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "sk64: project database unavailable: %v\n", err)
			st = nil
		} else {
			defer func() { _ = st.Close() }()
		}
	}

	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		_, _ = fmt.Fprintf(stderr, "sk64: project resolution unavailable: %v\n", cwdErr)
	}
	resolution, resolveErr := project.Resolve(ctx, st, cwd, options.project, options.noProject || cwdErr != nil)
	if resolveErr != nil {
		if options.project != "" {
			return reportRuntimeFailure(stderr, resolveErr)
		}
		_, _ = fmt.Fprintf(stderr, "sk64: project resolution unavailable: %v\n", resolveErr)
	}

	kubeContext := options.context
	namespace := options.namespace
	switchNotice := ""
	confirmProjectContext := false
	projectContextTarget := k8s.ContextInfo{}
	if resolution.Project != nil {
		if namespace == "" {
			namespace = resolution.Project.Namespace
		}
	}

	client, err := k8s.New(k8s.Config{
		Kubeconfig: options.kubeconfig,
		Context:    kubeContext,
		Namespace:  namespace,
		Debug:      debug,
	})
	if err != nil {
		return reportRuntimeFailure(stderr, err)
	}
	if resolution.Project != nil && options.context == "" {
		target, identityErr := k8s.ResolveContextIdentity(options.kubeconfig, resolution.Project.KubeContext)
		if errors.Is(identityErr, k8s.ErrContextNotFound) && resolution.Project.KubeServer != "" {
			if renamedTarget, found, findErr := k8s.FindContextByServer(options.kubeconfig, resolution.Project.KubeServer); findErr == nil && found {
				projectContextTarget = renamedTarget
				confirmProjectContext = true
			}
		} else if identityErr == nil {
			projectContextTarget = target
			switch startupProjectContext(resolution.Project, client.Context, client.Server, projectContextTarget) {
			case switchStartupContext:
				defaultClient := client
				kubeContext = resolution.Project.KubeContext
				client, err = k8s.New(k8s.Config{
					Kubeconfig: options.kubeconfig,
					Context:    kubeContext,
					Namespace:  namespace,
					Debug:      debug,
				})
				if err != nil {
					return reportRuntimeFailure(stderr, err)
				}
				if !k8s.SameServer(client.Server, projectContextTarget.Server) ||
					(resolution.Project.KubeServer != "" && !k8s.SameServer(client.Server, resolution.Project.KubeServer)) {
					projectContextTarget.Server = client.Server
					client = defaultClient
					confirmProjectContext = true
				} else {
					switchNotice = fmt.Sprintf("switched context to %s for project %s", kubeContext, resolution.Project.Name)
				}
			case confirmStartupContext:
				confirmProjectContext = true
			}
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	serverVersion, err := client.Probe(probeCtx)
	cancel()
	if err != nil {
		debug.Err("probe", debuglog.ClassifyError(err))
		return reportRuntimeFailure(stderr, err)
	}
	if resolution.Project != nil && st != nil {
		_ = st.SetLastProject(ctx, resolution.Project.Name)
	}
	dbNotice := ""
	if st != nil {
		dbNotice = st.Notice
	}

	if isTerminalWriter(stdout) {
		err := tui.Run(ctx, tui.Options{
			Client:                client,
			Keybinds:              cfg.Keybinds,
			Kubeconfig:            options.kubeconfig,
			StartNamespace:        namespace,
			ASCII:                 options.ascii || tui.DetectASCII(os.Getenv),
			Editor:                options.editor,
			ReadOnly:              options.readOnly,
			NoConfigMaps:          options.noConfigMaps,
			Store:                 st,
			Project:               resolution.Project,
			ProjectRoot:           resolution.Root,
			StartupNotice:         joinNonEmpty("; ", dbNotice, switchNotice),
			ConfirmProjectContext: confirmProjectContext,
			ProjectContextTarget:  projectContextTarget,
			ScanDepth:             options.scanDepth,
			ScanMaxFiles:          options.scanMaxFiles,
			Debug:                 debug,
		})
		if err != nil {
			return reportRuntimeFailure(stderr, err)
		}
		return 0
	}

	secretCount, err := client.CountSecrets(ctx, client.Namespace)
	if err != nil {
		return reportRuntimeFailure(stderr, err)
	}

	_, _ = fmt.Fprintf(stdout, "context:   %s\nnamespace: %s\nserver:    %s\nsecrets:   %d\n",
		client.Context, client.Namespace, serverVersion.GitVersion, secretCount)
	return 0
}

func startupProjectContext(project *store.Project, currentContext, currentServer string, target k8s.ContextInfo) projectContextStartup {
	if project.KubeServer != "" && (!k8s.SameServer(project.KubeServer, target.Server) || project.KubeContext == currentContext && !k8s.SameServer(project.KubeServer, currentServer)) {
		return confirmStartupContext
	}
	if project.KubeContext == currentContext {
		return keepStartupContext
	}
	if project.SwitchPromptSuppressed {
		return switchStartupContext
	}
	return confirmStartupContext
}

func notifyShutdown(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

func joinNonEmpty(separator string, values ...string) string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return strings.Join(result, separator)
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func reportRuntimeFailure(stderr io.Writer, err error) int {
	if !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "sk64: %v\n", err)
	}
	return 1
}

func reportConfigErrors(stderr io.Writer, path string, errs config.Errors) {
	_, _ = fmt.Fprintf(stderr, "sk64: invalid config: %s\n", path)
	for _, configErr := range errs {
		_, _ = fmt.Fprintf(stderr, "line %d: %s\n", configErr.Line, configErr.Msg)
		if configErr.Text != "" {
			_, _ = fmt.Fprintf(stderr, "  %q\n", configErr.Text)
		}
		if configErr.Hint != "" {
			_, _ = fmt.Fprintf(stderr, "  fix: %s\n", configErr.Hint)
		}
	}
}
