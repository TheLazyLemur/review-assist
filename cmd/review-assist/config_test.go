package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

func TestSettingsApplyFileThenEnvThenFlags(t *testing.T) {
	// given
	// ... a config file choosing Claude Code, with a token and effort, plus Messages API settings and concurrency
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	file := `{"backend": "claude-code",
		"claude_code": {"token": "file-token", "model": "file-model", "effort": "low"},
		"messages_api": {"base_url": "http://file:1", "api_key": "file-key", "effort": "medium"},
		"review": {"concurrency": 2}}`
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
	// ... flags override the model and the effort again
	cfg, err := parseConfig([]string{"-model", "flag-model", "-effort", "high"}, func(k string) string { return env[k] }, path)

	// then
	// ... the last source wins for each setting, -model and -effort apply to the chosen backend, and untouched settings keep the file value
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "claude-code", cfg.backend)
	assertEqual(t, "flag-model", cfg.claude.Model)
	assertEqual(t, review.EffortHigh, cfg.claude.Effort)
	assertEqual(t, "file-token", cfg.claude.Token)
	assertEqual(t, "http://env:2", cfg.messages.BaseURL)
	assertEqual(t, "file-key", cfg.messages.APIKey)
	assertEqual(t, review.EffortMedium, cfg.messages.Effort)
	assertEqual(t, 2, cfg.concurrency)
	assertEqual(t, 40, cfg.maxTurns)
}

func TestClaudeCodeBackendNeedsASetupToken(t *testing.T) {
	// given
	// ... no config file and no token anywhere
	noEnv := func(string) string { return "" }

	// when
	// ... the Claude Code backend is chosen by flag
	_, err := parseConfig([]string{"-backend", "claude-code"}, noEnv, filepath.Join(t.TempDir(), "none.json"))

	// then
	// ... it refuses to start rather than fall back to another login
	if err == nil {
		t.Fatal("want an error for a missing claude_code.token")
	}
}

func TestClaudeCodeRejectsNoEffort(t *testing.T) {
	// given
	// ... a claude-code config with a token
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"backend": "claude-code", "claude_code": {"token": "t"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// when
	// ... effort none is asked for, which only the Messages API backend supports
	_, err := parseConfig([]string{"-effort", "none"}, func(string) string { return "" }, path)

	// then
	// ... it refuses to start
	if err == nil {
		t.Fatal("want an error for effort none on claude-code")
	}
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

func TestInitWritesAPrivateClaudeCodeConfigOnce(t *testing.T) {
	// given
	// ... no config file yet, in a directory that does not exist
	path := filepath.Join(t.TempDir(), "review-assist", "config.json")

	// when
	// ... init writes a token, then tries again
	err := writeInitConfig(path, "sk-ant-oat01-abc")
	again := writeInitConfig(path, "sk-ant-oat01-other")

	// then
	// ... the file loads as a claude-code config with that token
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(nil, func(string) string { return "" }, path)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "claude-code", cfg.backend)
	assertEqual(t, "sk-ant-oat01-abc", cfg.claude.Token)

	// ... only the owner can read it
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, os.FileMode(0o600), info.Mode().Perm())

	// ... and the second write fails without touching the file
	if again == nil {
		t.Error("want an error when the config file already exists")
	}
	cfg, _ = parseConfig(nil, func(string) string { return "" }, path)
	assertEqual(t, "sk-ant-oat01-abc", cfg.claude.Token)
}

func TestExampleConfigShowsEveryOptionAndLoads(t *testing.T) {
	// given
	// ... a stale example file from an older version
	path := filepath.Join(t.TempDir(), "review-assist", "config.example.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"old": true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	// ... the example is written
	err := writeExampleConfig(path)

	// then
	// ... it replaces the stale file and loads through the config loader
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := loadConfigFile(path, &cfg); err != nil {
		t.Fatalf("example does not load: %v", err)
	}

	// ... and it sets every option the loader knows
	data, _ := os.ReadFile(path)
	var fc fileConfig
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&fc); err != nil {
		t.Fatal(err)
	}
	for _, missing := range unsetFields(reflect.ValueOf(fc), "") {
		t.Errorf("example leaves out %s", missing)
	}
}

// unsetFields lists nil pointer fields, by JSON key path.
func unsetFields(v reflect.Value, prefix string) []string {
	var out []string
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		key := prefix + f.Tag.Get("json")
		switch fv := v.Field(i); fv.Kind() {
		case reflect.Pointer:
			if fv.IsNil() {
				out = append(out, key)
			}
		case reflect.Struct:
			out = append(out, unsetFields(fv, key+".")...)
		}
	}
	return out
}
