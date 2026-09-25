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

// Pointers tell "not set" apart from zero values, so a file only overrides
// what it names.
type fileConfig struct {
	Backend   *string `json:"backend"`
	Anthropic struct {
		BaseURL *string `json:"base_url"`
		APIKey  *string `json:"api_key"`
		Model   *string `json:"model"`
		Think   *bool   `json:"think"`
		Log     *string `json:"log"`
	} `json:"anthropic"`
	ClaudeCode struct {
		Token      *string `json:"token"`
		Model      *string `json:"model"`
		Effort     *string `json:"effort"`
		Executable *string `json:"executable"`
	} `json:"claude_code"`
	Review struct {
		Concurrency *int `json:"concurrency"`
		MaxTurns    *int `json:"max_turns"`
	} `json:"review"`
}

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

// A missing file is fine.
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
	setIf(&cfg.backend, fc.Backend)
	setIf(&cfg.anthropic.BaseURL, fc.Anthropic.BaseURL)
	setIf(&cfg.anthropic.APIKey, fc.Anthropic.APIKey)
	setIf(&cfg.anthropic.Model, fc.Anthropic.Model)
	setIf(&cfg.anthropic.Think, fc.Anthropic.Think)
	setIf(&cfg.anthropic.LogPath, fc.Anthropic.Log)
	setIf(&cfg.claude.Token, fc.ClaudeCode.Token)
	setIf(&cfg.claude.Model, fc.ClaudeCode.Model)
	setIf(&cfg.claude.Effort, fc.ClaudeCode.Effort)
	setIf(&cfg.claude.Executable, fc.ClaudeCode.Executable)
	setIf(&cfg.concurrency, fc.Review.Concurrency)
	setIf(&cfg.maxTurns, fc.Review.MaxTurns)
	return nil
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}
