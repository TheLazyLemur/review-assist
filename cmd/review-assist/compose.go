package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/TheLazyLemur/review-assist/internal/adapters/anthropic"
	"github.com/TheLazyLemur/review-assist/internal/adapters/claudecode"
	"github.com/TheLazyLemur/review-assist/internal/adapters/github"
	"github.com/TheLazyLemur/review-assist/internal/adapters/gitrepo"
	"github.com/TheLazyLemur/review-assist/internal/adapters/tui"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

const (
	backendAnthropic  = "anthropic"
	backendClaudeCode = "claude-code"

	defaultAnthropicModel = "deepseek-v4.1-flash:cloud"
)

type config struct {
	backend     string
	anthropic   anthropic.Config
	claude      claudecode.Config // WorkDir is set by run
	maxTurns    int
	concurrency int
	target      string // optional PR argument
}

func (c config) agent(cacheDir string) (review.Backend, string, error) {
	switch c.backend {
	case backendAnthropic:
		return anthropic.New(c.anthropic), c.anthropic.Model, nil
	case backendClaudeCode:
		// An empty, private directory: claude sees no project there, and
		// reads code only through the review tools.
		dir := filepath.Join(cacheDir, "claude-workdir")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", fmt.Errorf("claude work dir: %w", err)
		}
		cc := c.claude
		cc.WorkDir = dir
		name := "claude " + firstSet(cc.Model, "(default model)")
		return claudecode.New(cc), name, nil
	default:
		return nil, "", fmt.Errorf("unknown backend %q", c.backend)
	}
}

// run is the composition root.
func run(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	userConfig, _ := os.UserConfigDir() // only used off unix-likes
	path := configPath(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"), userConfig)
	if len(args) == 1 && (args[0] == "--init" || args[0] == "-init") {
		return runInit(path)
	}
	cfg, err := parseConfig(args, os.Getenv, path)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner := github.ExecRunner{Dir: cwd}
	repo, openPR, localRepo, err := resolveTarget(context.Background(), runner, cfg.target)
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("no cache dir: %w", err)
	}
	cache = filepath.Join(cache, "review-assist")
	agent, modelName, err := cfg.agent(cache)
	if err != nil {
		return err
	}

	prs := pr.NewService(github.NewClient(runner, repo), repo)
	reviews := review.NewService(
		agent,
		gitrepo.Source{Cwd: cwd, CacheDir: cache},
		cfg.maxTurns, cfg.concurrency,
	)
	app := tui.New(tui.Deps{
		PRs:       prs,
		Reviews:   reviews,
		ModelName: modelName,
		Cwd:       cwd,
		LocalRepo: localRepo,
		OpenPR:    openPR,
		Dark:      lipgloss.HasDarkBackground(os.Stdin, os.Stdout),
	})
	_, err = tea.NewProgram(app).Run()
	return err
}

// OLLAMA_HOST and ANTHROPIC_API_KEY are shared with other tools, so they only
// replace the built-in defaults and never override the config file.
func parseConfig(args []string, getenv func(string) string, configFile string) (config, error) {
	cfg := config{
		backend: backendAnthropic,
		anthropic: anthropic.Config{
			BaseURL: firstSet(getenv("OLLAMA_HOST"), "http://localhost:11434"),
			APIKey:  firstSet(getenv("ANTHROPIC_API_KEY"), "ollama"), // the SDK needs a key; Ollama ignores it
			Model:   defaultAnthropicModel,
		},
		maxTurns:    40,
		concurrency: 4,
	}
	if err := loadConfigFile(configFile, &cfg); err != nil {
		return cfg, err
	}
	// REVIEW_ASSIST_MODEL and -model apply to whichever backend ends up chosen.
	model := getenv("REVIEW_ASSIST_MODEL")
	for env, dst := range map[string]*string{
		"REVIEW_ASSIST_BACKEND":      &cfg.backend,
		"REVIEW_ASSIST_BASE_URL":     &cfg.anthropic.BaseURL,
		"REVIEW_ASSIST_API_KEY":      &cfg.anthropic.APIKey,
		"REVIEW_ASSIST_LOG":          &cfg.anthropic.LogPath,
		"REVIEW_ASSIST_CLAUDE_TOKEN": &cfg.claude.Token,
	} {
		if v := getenv(env); v != "" {
			*dst = v
		}
	}

	fs := flag.NewFlagSet("review-assist", flag.ContinueOnError)
	fs.StringVar(&cfg.backend, "backend", cfg.backend, "agent backend: anthropic or claude-code (env REVIEW_ASSIST_BACKEND)")
	fs.StringVar(&model, "model", model, "model for review agents, for the chosen backend (env REVIEW_ASSIST_MODEL)")
	fs.StringVar(&cfg.anthropic.BaseURL, "base-url", cfg.anthropic.BaseURL, "anthropic: API base URL (env REVIEW_ASSIST_BASE_URL)")
	fs.StringVar(&cfg.anthropic.BaseURL, "ollama", cfg.anthropic.BaseURL, "deprecated alias of -base-url")
	// No defaults shown for secrets: -h must never print a key from the file or env.
	apiKey := fs.String("api-key", "", "anthropic: API key sent as x-api-key (env REVIEW_ASSIST_API_KEY)")
	fs.BoolVar(&cfg.anthropic.Think, "think", cfg.anthropic.Think, "anthropic: let the model use extended thinking (slower)")
	fs.StringVar(&cfg.anthropic.LogPath, "log", cfg.anthropic.LogPath, "anthropic: append one line per model call to this file (env REVIEW_ASSIST_LOG)")
	fs.StringVar(&cfg.claude.Effort, "effort", cfg.claude.Effort, "claude-code: low, medium, high, xhigh or max")
	fs.IntVar(&cfg.concurrency, "concurrency", cfg.concurrency, "max agents running at once")
	fs.IntVar(&cfg.maxTurns, "max-turns", cfg.maxTurns, "max model turns per agent (min 4)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: review-assist [flags] [PR]
       review-assist --init    write a first config file that uses Claude Code

With no PR, lists pull requests of the GitHub repository in the current directory.
PR may be a number (in the current repository), a PR URL on github.com or a
GitHub Enterprise host, OWNER/REPO#N or HOST/OWNER/REPO#N.

Settings come from the config file, then REVIEW_ASSIST_* env vars, then flags;
the last one set wins. Config file: `+configFile+`
The claude-code backend's token (from `+"`claude setup-token`"+`) is set only in
the file (claude_code.token) or REVIEW_ASSIST_CLAUDE_TOKEN, never by flag.

flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if *apiKey != "" {
		cfg.anthropic.APIKey = *apiKey
	}
	cfg.anthropic.BaseURL = normaliseBaseURL(cfg.anthropic.BaseURL)
	switch cfg.backend {
	case backendAnthropic:
		if model != "" {
			cfg.anthropic.Model = model
		}
	case backendClaudeCode:
		if model != "" {
			cfg.claude.Model = model
		}
		if cfg.claude.Token == "" {
			return cfg, fmt.Errorf("the claude-code backend needs a token: run `claude setup-token` and put it in claude_code.token in %s", configFile)
		}
	default:
		return cfg, fmt.Errorf("unknown backend %q: use %s or %s", cfg.backend, backendAnthropic, backendClaudeCode)
	}
	switch {
	case fs.NArg() > 1:
		fs.Usage()
		return cfg, errors.New("expected at most one PR argument")
	case fs.NArg() == 1:
		cfg.target = fs.Arg(0)
	}
	if cfg.maxTurns < 4 || cfg.concurrency < 1 {
		return cfg, errors.New("-max-turns must be at least 4 and -concurrency at least 1")
	}
	return cfg, nil
}

// normaliseBaseURL accepts OLLAMA_HOST forms such as "0.0.0.0:11434".
func normaliseBaseURL(u string) string {
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	u = strings.Replace(u, "://0.0.0.0", "://localhost", 1)
	return strings.TrimSuffix(u, "/")
}

func resolveTarget(ctx context.Context, runner github.Runner, target string) (repo pr.Repo, openPR int, localRepo bool, err error) {
	local, localErr := github.Detect(ctx, runner)
	if target == "" {
		return local, 0, localErr == nil, localErr
	}
	if n, convErr := strconv.Atoi(target); convErr == nil {
		return local, n, localErr == nil, localErr
	}
	repo, n, err := pr.ParseRef(target)
	if err != nil {
		return pr.Repo{}, 0, false, err
	}
	return repo, n, localErr == nil && local == repo, nil
}

func firstSet(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
