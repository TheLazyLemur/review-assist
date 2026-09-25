package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	cfg, err := parseConfig(args)
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

func parseConfig(args []string) (config, error) {
	cfg := config{
		model: anthropic.Config{
			BaseURL: firstSet(os.Getenv("REVIEW_ASSIST_BASE_URL"), os.Getenv("OLLAMA_HOST"), "http://localhost:11434"),
			APIKey:  firstSet(os.Getenv("REVIEW_ASSIST_API_KEY"), os.Getenv("ANTHROPIC_API_KEY"), "ollama"), // the SDK needs a key; Ollama ignores it
			Model:   firstSet(os.Getenv("REVIEW_ASSIST_MODEL"), defaultModel),
			LogPath: os.Getenv("REVIEW_ASSIST_LOG"),
		},
		maxTurns:    40,
		concurrency: 4,
	}
	fs := flag.NewFlagSet("review-assist", flag.ContinueOnError)
	fs.StringVar(&cfg.model.BaseURL, "base-url", cfg.model.BaseURL, "Anthropic-compatible API base URL (env REVIEW_ASSIST_BASE_URL, then OLLAMA_HOST)")
	fs.StringVar(&cfg.model.BaseURL, "ollama", cfg.model.BaseURL, "deprecated alias of -base-url")
	// No default shown: -h must never print a key taken from the environment.
	apiKey := fs.String("api-key", "", "API key sent as x-api-key (env REVIEW_ASSIST_API_KEY, then ANTHROPIC_API_KEY)")
	fs.StringVar(&cfg.model.Model, "model", cfg.model.Model, "model for review agents; needs tool support (env REVIEW_ASSIST_MODEL)")
	fs.BoolVar(&cfg.model.Think, "think", false, "let the model use extended thinking (slower)")
	fs.StringVar(&cfg.model.LogPath, "log", cfg.model.LogPath, "append one line per model call to this file (env REVIEW_ASSIST_LOG)")
	fs.IntVar(&cfg.concurrency, "concurrency", cfg.concurrency, "max agents running at once")
	fs.IntVar(&cfg.maxTurns, "max-turns", cfg.maxTurns, "max model turns per agent (min 4)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: review-assist [flags] [PR]

With no PR, lists pull requests of the GitHub repository in the current directory.
PR may be a number (in the current repository), a PR URL on github.com or a
GitHub Enterprise host, OWNER/REPO#N or HOST/OWNER/REPO#N.

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
