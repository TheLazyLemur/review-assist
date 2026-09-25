package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// fileConfig is config.json. Pointers tell "not set" apart from zero values,
// so a file only overrides what it names.
type fileConfig struct {
	Model struct {
		BaseURL *string `json:"base_url"`
		APIKey  *string `json:"api_key"`
		Name    *string `json:"name"`
		Think   *bool   `json:"think"`
		Log     *string `json:"log"`
	} `json:"model"`
	Review struct {
		Concurrency *int `json:"concurrency"`
		MaxTurns    *int `json:"max_turns"`
	} `json:"review"`
}

// configPath is ~/.config/review-assist/config.json on macOS, Linux and other
// unix-likes (or $XDG_CONFIG_HOME/review-assist/config.json), and the OS
// config dir elsewhere (%AppData% on Windows). userConfigDir is os.UserConfigDir.
func configPath(goos, home, xdgConfigHome, userConfigDir string) string {
	base := userConfigDir
	switch {
	case goos == "windows" || goos == "plan9":
	case xdgConfigHome != "":
		base = xdgConfigHome
	default:
		// os.UserConfigDir gives ~/Library/Application Support on macOS;
		// CLI tools there conventionally use ~/.config.
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "review-assist", "config.json")
}

// loadConfigFile applies the file at path to cfg. A missing file is fine.
func loadConfigFile(path string, cfg *config) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var fc fileConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // a typo in a key should fail, not be ignored
	if err := dec.Decode(&fc); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	setIf(&cfg.model.BaseURL, fc.Model.BaseURL)
	setIf(&cfg.model.APIKey, fc.Model.APIKey)
	setIf(&cfg.model.Model, fc.Model.Name)
	setIf(&cfg.model.Think, fc.Model.Think)
	setIf(&cfg.model.LogPath, fc.Model.Log)
	setIf(&cfg.concurrency, fc.Review.Concurrency)
	setIf(&cfg.maxTurns, fc.Review.MaxTurns)
	return nil
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}
