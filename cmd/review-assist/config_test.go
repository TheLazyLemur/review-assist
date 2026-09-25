package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsApplyFileThenEnvThenFlags(t *testing.T) {
	// given
	// ... a config file setting model, base URL, key and concurrency
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	file := `{"model": {"base_url": "http://file:1", "api_key": "file-key", "name": "file-model"}, "review": {"concurrency": 2}}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	// ... env overriding the model and base URL, plus a generic key that must not beat the file
	env := map[string]string{
		"REVIEW_ASSIST_MODEL":    "env-model",
		"REVIEW_ASSIST_BASE_URL": "http://env:2",
		"ANTHROPIC_API_KEY":      "generic-key",
	}

	// when
	// ... a flag overrides the model again
	cfg, err := parseConfig([]string{"-model", "flag-model"}, func(k string) string { return env[k] }, path)

	// then
	// ... the last source wins for each setting, and untouched settings keep the file value
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "flag-model", cfg.model.Model)
	assertEqual(t, "http://env:2", cfg.model.BaseURL)
	assertEqual(t, "file-key", cfg.model.APIKey)
	assertEqual(t, 2, cfg.concurrency)
	assertEqual(t, 40, cfg.maxTurns)
}

func TestConfigPathUsesDotConfigOnMacAndLinux(t *testing.T) {
	// given
	// ... a home directory, and the platform default from os.UserConfigDir on each OS
	home := "/home/u"

	// when
	// ... the config path is resolved per OS, with and without XDG_CONFIG_HOME
	mac := configPath("darwin", home, "", "/home/u/Library/Application Support")
	linux := configPath("linux", home, "", "/home/u/.config")
	xdg := configPath("linux", home, "/xdg", "/home/u/.config")
	windows := configPath("windows", home, "", `C:\Users\u\AppData\Roaming`)

	// then
	// ... unix-likes use ~/.config (or XDG_CONFIG_HOME), Windows uses its roaming app data
	assertEqual(t, "/home/u/.config/review-assist/config.json", mac)
	assertEqual(t, "/home/u/.config/review-assist/config.json", linux)
	assertEqual(t, "/xdg/review-assist/config.json", xdg)
	assertEqual(t, filepath.Join(`C:\Users\u\AppData\Roaming`, "review-assist", "config.json"), windows)
}

func assertEqual[T comparable](t *testing.T, want, got T) {
	t.Helper()
	if want != got {
		t.Errorf("want %v, got %v", want, got)
	}
}
