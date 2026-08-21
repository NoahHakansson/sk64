// Package config loads and validates sk64 configuration files.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Overrides maps an action to its replacement keys, in file order, deduped.
// A present entry replaces the action's default keys everywhere it is live.
type Overrides map[Action][]string

// Config is a successfully validated user configuration.
type Config struct {
	Keybinds Overrides
}

// ErrPathUnavailable identifies failures resolving the optional config path.
var ErrPathUnavailable = errors.New("config path unavailable")

// Path resolves the config file location without touching the filesystem.
func Path() (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" && filepath.IsAbs(base) {
		return filepath.Join(base, "sk64", "config"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: resolve home directory: %w", ErrPathUnavailable, err)
	}
	return filepath.Join(home, ".config", "sk64", "config"), nil
}

// Load reads and validates the config file. A missing file is a valid empty
// configuration.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	file, err := os.Open(path) // #nosec G304 -- path is the documented user configuration location.
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	config, validationErrors, readErr := parse(file)
	if readErr != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, readErr)
	}
	if len(validationErrors) > 0 {
		return Config{}, validationErrors
	}
	return config, nil
}
