package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPath(t *testing.T) {
	tests := []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{
			name: "absolute XDG directory",
			xdg:  filepath.Join(string(filepath.Separator), "tmp", "xdg"),
			home: filepath.Join(string(filepath.Separator), "tmp", "home"),
			want: filepath.Join(string(filepath.Separator), "tmp", "xdg", "sk64", "config"),
		},
		{
			name: "unset XDG directory",
			home: filepath.Join(string(filepath.Separator), "tmp", "home"),
			want: filepath.Join(string(filepath.Separator), "tmp", "home", ".config", "sk64", "config"),
		},
		{
			name: "relative XDG directory",
			xdg:  "relative/config",
			home: filepath.Join(string(filepath.Separator), "tmp", "home"),
			want: filepath.Join(string(filepath.Separator), "tmp", "home", ".config", "sk64", "config"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)

			got, err := Path()
			if err != nil {
				t.Fatalf("Path() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Path() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathWithoutHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("HOME", "")

	if _, err := Path(); err == nil || !errors.Is(err, ErrPathUnavailable) || !strings.Contains(err.Error(), "resolve home directory") {
		t.Fatalf("Path() error = %v, want wrapped home resolution error", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, Config{}) {
		t.Fatalf("Load() = %#v, want zero Config", got)
	}
}

func TestLoadUnreadableFile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	path := filepath.Join(base, "sk64", "config")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want read error")
	}
	var validationErrors Errors
	if errors.As(err, &validationErrors) {
		t.Fatalf("Load() error = %v, want non-validation error", err)
	}
	if !strings.Contains(err.Error(), "read config "+path) {
		t.Fatalf("Load() error = %q, want path context", err)
	}
}

func TestParseGrammar(t *testing.T) {
	tests := []struct {
		name       string
		document   string
		want       Config
		wantErrors Errors
	}{
		{
			name:     "comments and blank lines",
			document: "# heading\n\t # indented\n\n\t \n",
			want:     Config{},
		},
		{
			name:     "spaces around separators",
			document: "  keybind  =  ctrl+e = refresh  ",
			want:     Config{Keybinds: Overrides{ActionRefresh: {"ctrl+e"}}},
		},
		{
			name:     "no spaces",
			document: "keybind=ctrl+e=refresh",
			want:     Config{Keybinds: Overrides{ActionRefresh: {"ctrl+e"}}},
		},
		{
			name:     "UTF-8 BOM on first line",
			document: "\uFEFFkeybind = ctrl+e=refresh",
			want:     Config{Keybinds: Overrides{ActionRefresh: {"ctrl+e"}}},
		},
		{
			name:     "missing separator",
			document: "  not a setting  ",
			wantErrors: Errors{{
				Line: 1, Text: "  not a setting  ", Msg: "cannot parse line", Hint: "expected key = value",
			}},
		},
		{
			name:     "empty config key",
			document: "  = value",
			wantErrors: Errors{{
				Line: 1, Text: "  = value", Msg: "cannot parse line", Hint: "expected key = value",
			}},
		},
		{
			name:     "unknown config key",
			document: "theme = dark",
			wantErrors: Errors{{
				Line: 1, Text: "theme = dark", Msg: `unknown config key "theme"`, Hint: "known keys: keybind",
			}},
		},
		{
			name:     "CRLF line numbers and text",
			document: "# heading\r\n\r\ntheme = dark\r\n",
			wantErrors: Errors{{
				Line: 3, Text: "theme = dark", Msg: `unknown config key "theme"`, Hint: "known keys: keybind",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErrors, err := parse(strings.NewReader(tt.document))
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			assertParseResult(t, got, gotErrors, tt.want, tt.wantErrors)
		})
	}
}

func TestOverlongConfigLineIsValidationError(t *testing.T) {
	_, got, err := parse(strings.NewReader(strings.Repeat("x", maxConfigLineBytes+1)))
	if err != nil {
		t.Fatalf("parse() returned bare error %v", err)
	}
	if len(got) != 1 || got[0].Line != 1 || got[0].Msg != "config line is too long" {
		t.Fatalf("parse() errors = %#v, want line-numbered overlong-line diagnostic", got)
	}
}

func TestKeybindParsing(t *testing.T) {
	tests := []struct {
		name       string
		document   string
		want       Config
		wantErrors Errors
	}{
		{
			name:     "single binding",
			document: "keybind = ctrl+e=refresh",
			want:     Config{Keybinds: Overrides{ActionRefresh: {"ctrl+e"}}},
		},
		{
			name:     "accumulates in file order",
			document: "keybind = ctrl+e=refresh\nkeybind = ctrl+r=refresh",
			want:     Config{Keybinds: Overrides{ActionRefresh: {"ctrl+e", "ctrl+r"}}},
		},
		{
			name:     "dedupes exact repeats",
			document: "keybind = ctrl+e=refresh\nkeybind = ctrl+e=refresh",
			want:     Config{Keybinds: Overrides{ActionRefresh: {"ctrl+e"}}},
		},
		{
			name:     "literal equals key",
			document: "keybind = ==page-down",
			want:     Config{Keybinds: Overrides{ActionPageDown: {"="}}},
		},
		{
			name:     "missing separator",
			document: "keybind = refresh",
			wantErrors: Errors{{
				Line: 1, Text: "keybind = refresh", Msg: "cannot parse keybind", Hint: "expected keybind = <key>=<action>",
			}},
		},
		{
			name:     "empty key",
			document: "keybind = =refresh",
			wantErrors: Errors{{
				Line: 1, Text: "keybind = =refresh", Msg: "cannot parse keybind", Hint: "expected keybind = <key>=<action>",
			}},
		},
		{
			name:     "empty action",
			document: "keybind = ctrl+e=",
			wantErrors: Errors{{
				Line: 1, Text: "keybind = ctrl+e=", Msg: "cannot parse keybind", Hint: "expected keybind = <key>=<action>",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErrors, err := parse(strings.NewReader(tt.document))
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			assertParseResult(t, got, gotErrors, tt.want, tt.wantErrors)
		})
	}
}

func TestErrorsCollected(t *testing.T) {
	document := strings.Join([]string{
		"broken",
		"theme = dark",
		"keybind = ä=refresh",
		"keybind = ctrl+e=refrsh",
		"keybind = Y=refresh",
	}, "\n")

	config, got, err := parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	if !reflect.DeepEqual(config, Config{}) {
		t.Fatalf("Parse() config = %#v, want zero Config", config)
	}
	if len(got) != 5 {
		t.Fatalf("Parse() returned %d errors, want 5: %#v", len(got), got)
	}
	for i, err := range got {
		if err.Line != i+1 {
			t.Errorf("error %d line = %d, want %d", i, err.Line, i+1)
		}
		if err.Text == "" || err.Msg == "" || err.Hint == "" {
			t.Errorf("error %d has an empty field: %#v", i, err)
		}
	}
}

func TestParseSuccessOutput(t *testing.T) {
	document := strings.Join([]string{
		"# Personal key bindings",
		"keybind = ctrl+e=refresh",
		"keybind = ctrl+r=refresh",
		"keybind = ctrl+n=down",
	}, "\n")
	want := Config{Keybinds: Overrides{
		ActionRefresh: {"ctrl+e", "ctrl+r"},
		ActionDown:    {"ctrl+n"},
	}}

	got, gotErrors, err := parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	assertParseResult(t, got, gotErrors, want, nil)
}

func TestEmptyErrorsString(t *testing.T) {
	for _, errs := range []Errors{nil, {}} {
		if got := errs.Error(); got != "config: no errors" {
			t.Fatalf("Errors.Error() = %q, want %q", got, "config: no errors")
		}
	}
}

func assertParseResult(t *testing.T, got Config, gotErrors Errors, want Config, wantErrors Errors) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() config = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(gotErrors, wantErrors) {
		t.Errorf("Parse() errors = %#v, want %#v", gotErrors, wantErrors)
	}
}
