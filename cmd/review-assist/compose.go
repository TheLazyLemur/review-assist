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
	"github.com/TheLazyLemur/review-assist/internal/adapters/github"
	"github.com/TheLazyLemur/review-assist/internal/adapters/gitrepo"
	"github.com/TheLazyLemur/review-assist/internal/adapters/tui"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

const defaultModel = "deepseek-v4.1-flash:cloud"

type config struct {
	model       anthropic.Config
	maxTurns    int
	concurrency int
	target      string // optional PR argument
}

// run is the composition root: it reads configuration, builds the adapters,
// connects them to the core, and starts the TUI.
func run(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	userConfig, _ := os.UserConfigDir() // only used off unix-likes
	path := configPath(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"), userConfig)
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
		cache = "" // gitrepo reports it only if a mirror is needed
	} else {
		cache = filepath.Join(cache, "review-assist")
	}

	prs := pr.NewService(github.NewClient(runner, repo), repo)
	reviewer := review.NewReviewer(
		anthropic.New(cfg.model),
		gitrepo.Source{Cwd: cwd, CacheDir: cache},
		cfg.maxTurns, cfg.concurrency,
	)
	app := tui.New(tui.Deps{
		PRs:       prs,
		Reviewer:  reviewer,
		ModelName: cfg.model.Model,
		Cwd:       cwd,
		LocalRepo: localRepo,
		OpenPR:    openPR,
		Dark:      lipgloss.HasDarkBackground(os.Stdin, os.Stdout),
	})
	_, err = tea.NewProgram(app).Run()
	return err
}

// parseConfig layers settings: defaults, then the config file, then
// REVIEW_ASSIST_* env vars, then flags. The last one set wins. OLLAMA_HOST and
// ANTHROPIC_API_KEY are shared with other tools, so they only replace the
// built-in defaults and never override the file.
func parseConfig(args []string, getenv func(string) string, configFile string) (config, error) {
	cfg := config{
		model: anthropic.Config{
			BaseURL: firstSet(getenv("OLLAMA_HOST"), "http://localhost:11434"),
			APIKey:  firstSet(getenv("ANTHROPIC_API_KEY"), "ollama"), // the SDK needs a key; Ollama ignores it
			Model:   defaultModel,
		},
		maxTurns:    40,
		concurrency: 4,
	}
	if err := loadConfigFile(configFile, &cfg); err != nil {
		return cfg, err
	}
	for env, dst := range map[string]*string{
		"REVIEW_ASSIST_BASE_URL": &cfg.model.BaseURL,
		"REVIEW_ASSIST_API_KEY":  &cfg.model.APIKey,
		"REVIEW_ASSIST_MODEL":    &cfg.model.Model,
		"REVIEW_ASSIST_LOG":      &cfg.model.LogPath,
	} {
		if v := getenv(env); v != "" {
			*dst = v
		}
	}

	fs := flag.NewFlagSet("review-assist", flag.ContinueOnError)
	fs.StringVar(&cfg.model.BaseURL, "base-url", cfg.model.BaseURL, "Anthropic-compatible API base URL (env REVIEW_ASSIST_BASE_URL)")
	fs.StringVar(&cfg.model.BaseURL, "ollama", cfg.model.BaseURL, "deprecated alias of -base-url")
	// No default shown: -h must never print a key taken from the environment.
	apiKey := fs.String("api-key", "", "API key sent as x-api-key (env REVIEW_ASSIST_API_KEY)")
	fs.StringVar(&cfg.model.Model, "model", cfg.model.Model, "model for review agents; needs tool support (env REVIEW_ASSIST_MODEL)")
	fs.BoolVar(&cfg.model.Think, "think", cfg.model.Think, "let the model use extended thinking (slower)")
	fs.StringVar(&cfg.model.LogPath, "log", cfg.model.LogPath, "append one line per model call to this file (env REVIEW_ASSIST_LOG)")
	fs.IntVar(&cfg.concurrency, "concurrency", cfg.concurrency, "max agents running at once")
	fs.IntVar(&cfg.maxTurns, "max-turns", cfg.maxTurns, "max model turns per agent (min 4)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: review-assist [flags] [PR]

With no PR, lists pull requests of the GitHub repository in the current directory.
PR may be a number (in the current repository), a PR URL on github.com or a
GitHub Enterprise host, OWNER/REPO#N or HOST/OWNER/REPO#N.

Settings come from the config file, then REVIEW_ASSIST_* env vars, then flags;
the last one set wins. Config file: `+configFile+`

flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if *apiKey != "" {
		cfg.model.APIKey = *apiKey
	}
	cfg.model.BaseURL = normaliseBaseURL(cfg.model.BaseURL)
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

// resolveTarget works out which repository to use and which PR, if any, to
// open first. localRepo is true when the cwd is a checkout of that repository.
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
