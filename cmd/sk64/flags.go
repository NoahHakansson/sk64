package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/NoahHakansson/sk64/internal/project"
)

type options struct {
	kubeconfig   string
	context      string
	namespace    string
	ascii        bool
	readOnly     bool
	noConfigMaps bool
	editor       string
	project      string
	noProject    bool
	db           string
	debugLog     string
	scanDepth    int
	scanMaxFiles int
	showVersion  bool
}

// parseFlags parses args (without the program name). Returns flag.ErrHelp
// when -h/--help was requested (usage already printed to stderr).
func parseFlags(args []string, stderr io.Writer) (*options, error) {
	fs := flag.NewFlagSet("sk64", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: sk64 [flags]")
		fs.PrintDefaults()
	}

	o := &options{}
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "path to kubeconfig")
	fs.StringVar(&o.context, "context", "", "kubeconfig context name")
	fs.StringVar(&o.namespace, "namespace", "", "Kubernetes namespace")
	fs.StringVar(&o.namespace, "n", "", "Kubernetes namespace")
	fs.BoolVar(&o.ascii, "ascii", false, "force ASCII markers (no emoji glyphs)")
	fs.BoolVar(&o.readOnly, "read-only", false, "disable all mutating operations")
	fs.BoolVar(&o.noConfigMaps, "no-configmaps", false, "Secrets only (hide ConfigMaps)")
	fs.StringVar(&o.editor, "editor", "", "editor command (overrides $EDITOR)")
	fs.StringVar(&o.project, "project", "", "open this project (overrides cwd resolution)")
	fs.BoolVar(&o.noProject, "no-project", false, "skip cwd project resolution")
	fs.StringVar(&o.db, "db", "", "database location override")
	fs.StringVar(&o.debugLog, "debug-log", "", "append a scrubbed debug log to PATH (never values)")
	fs.IntVar(&o.scanDepth, "scan-depth", project.DefaultMaxDepth, "scanner max directory depth")
	fs.IntVar(&o.scanMaxFiles, "scan-max-files", project.DefaultMaxFiles, "scanner max file count")
	fs.BoolVar(&o.showVersion, "version", false, "print version information")
	fs.BoolVar(&o.showVersion, "v", false, "print version information")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if o.project != "" && o.noProject {
		err := errors.New("--project and --no-project cannot be used together")
		_, _ = fmt.Fprintf(stderr, "sk64: %v\n", err)
		return nil, err
	}
	if o.scanDepth < 1 || o.scanMaxFiles < 1 {
		err := errors.New("--scan-depth and --scan-max-files must be at least 1")
		_, _ = fmt.Fprintf(stderr, "sk64: %v\n", err)
		return nil, err
	}
	return o, nil
}
