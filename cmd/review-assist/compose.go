package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/TheLazyLemur/review-assist/internal/adapters/bitbucket"
	"github.com/TheLazyLemur/review-assist/internal/adapters/claudecode"
	"github.com/TheLazyLemur/review-assist/internal/adapters/github"
	"github.com/TheLazyLemur/review-assist/internal/adapters/gitrepo"
	"github.com/TheLazyLemur/review-assist/internal/adapters/messagesapi"
	"github.com/TheLazyLemur/review-assist/internal/adapters/tui"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

const (
	backendMessagesAPI = "messages-api"
	backendClaudeCode  = "claude-code"

	defaultMessagesModel = "deepseek-v4.1-flash:cloud"

	bitbucketAPI = "https://api.bitbucket.org/2.0"
)

type config struct {
	backend     string
	messages    messagesapi.Config
	claude      claudecode.Config // WorkDir is set by run
	bitbucket   bitbucketConfig
	maxTurns    int
	concurrency int
	target      string // optional PR argument
	file        string // the config file's path, for messages
}

type bitbucketConfig struct {
	email    string // the Atlassian account email, not the Bitbucket username
	apiToken string
}

func (c config) newBackend(cacheDir string) (review.Backend, string, error) {
	switch c.backend {
	case backendMessagesAPI:
		return messagesapi.New(c.messages), c.messages.Model, nil
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
	repo, openPR, remote, err := resolveTarget(context.Background(), cwd, runner, cfg.target)
	if err != nil {
		return err
	}
	host, err := newCodeHost(cfg, repo, remote, cwd, runner)
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("no cache dir: %w", err)
	}
	cache = filepath.Join(cache, "review-assist")
	backend, modelName, err := cfg.newBackend(cache)
	if err != nil {
		return err
	}

	prs := pr.NewService(host, repo)
	reviews := review.NewService(
		backend,
		gitrepo.Source{Cwd: cwd, CacheDir: cache},
		cfg.maxTurns, cfg.concurrency,
	)
	app := tui.New(tui.Deps{
		PRs:       prs,
		Reviews:   reviews,
		ModelName: modelName,
		Cwd:       cwd,
		Remote:    remote,
		OpenPR:    openPR,
		Dark:      lipgloss.HasDarkBackground(os.Stdin, os.Stdout),
	})
	_, err = tea.NewProgram(app).Run()
	return err
}

// newCodeHost gives the repository the code host of its platform. remote is
// "" when the cwd is not a clone of the repository.
func newCodeHost(cfg config, repo pr.Repo, remote, cwd string, runner github.Runner) (pr.CodeHost, error) {
	switch repo.Platform {
	case pr.GitHub:
		return github.NewClient(runner, repo), nil
	case pr.Bitbucket:
		if cfg.bitbucket.email == "" || cfg.bitbucket.apiToken == "" {
			return nil, fmt.Errorf("%s is on Bitbucket, which needs an Atlassian account email and an API token: "+
				"set bitbucket.email and bitbucket.api_token in %s, "+
				"or REVIEW_ASSIST_BITBUCKET_EMAIL and REVIEW_ASSIST_BITBUCKET_API_TOKEN", repo.Qualified(), cfg.file)
		}
		// With no clone, Remote and Dir stay empty and Checkout refuses by name.
		opts := bitbucket.Options{Open: openBrowser}
		if remote != "" {
			opts.Remote, opts.Dir = remote, cwd
		}
		return bitbucket.NewClient(bitbucketAPI, cfg.bitbucket.email, cfg.bitbucket.apiToken, repo, opts), nil
	default:
		return nil, fmt.Errorf("%s: unknown platform %q", repo.Qualified(), repo.Platform)
	}
}

// openBrowser leaves the opener's output unattached, so it cannot draw over
// the TUI.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	return nil
}

// OLLAMA_HOST and ANTHROPIC_API_KEY are shared with other tools, so they only
// replace the built-in defaults and never override the config file.
func parseConfig(args []string, getenv func(string) string, configFile string) (config, error) {
	cfg := config{
		backend: backendMessagesAPI,
		messages: messagesapi.Config{
			BaseURL: firstSet(getenv("OLLAMA_HOST"), "http://localhost:11434"),
			APIKey:  firstSet(getenv("ANTHROPIC_API_KEY"), "ollama"), // the SDK needs a key; Ollama ignores it
			Model:   defaultMessagesModel,
		},
		maxTurns:    40,
		concurrency: 4,
		file:        configFile,
	}
	if err := loadConfigFile(configFile, &cfg); err != nil {
		return cfg, err
	}
	// Model and effort apply to whichever backend ends up chosen.
	model, effort := getenv("REVIEW_ASSIST_MODEL"), getenv("REVIEW_ASSIST_EFFORT")
	for env, dst := range map[string]*string{
		"REVIEW_ASSIST_BACKEND":             &cfg.backend,
		"REVIEW_ASSIST_BASE_URL":            &cfg.messages.BaseURL,
		"REVIEW_ASSIST_API_KEY":             &cfg.messages.APIKey,
		"REVIEW_ASSIST_LOG":                 &cfg.messages.LogPath,
		"REVIEW_ASSIST_CLAUDE_TOKEN":        &cfg.claude.Token,
		"REVIEW_ASSIST_BITBUCKET_EMAIL":     &cfg.bitbucket.email,
		"REVIEW_ASSIST_BITBUCKET_API_TOKEN": &cfg.bitbucket.apiToken,
	} {
		if v := getenv(env); v != "" {
			*dst = v
		}
	}

	fs := flag.NewFlagSet("review-assist", flag.ContinueOnError)
	fs.StringVar(&cfg.backend, "backend", cfg.backend, "backend: messages-api or claude-code (env REVIEW_ASSIST_BACKEND)")
	fs.StringVar(&model, "model", model, "model for the chosen backend (env REVIEW_ASSIST_MODEL)")
	fs.StringVar(&effort, "effort", effort, "effort for the chosen backend: none, low, medium, high, xhigh or max (env REVIEW_ASSIST_EFFORT)")
	fs.StringVar(&cfg.messages.BaseURL, "base-url", cfg.messages.BaseURL, "messages-api: API base URL (env REVIEW_ASSIST_BASE_URL)")
	fs.StringVar(&cfg.messages.BaseURL, "ollama", cfg.messages.BaseURL, "deprecated alias of -base-url")
	// No defaults shown for secrets: -h must never print a key from the file or env.
	apiKey := fs.String("api-key", "", "messages-api: API key sent as x-api-key (env REVIEW_ASSIST_API_KEY)")
	fs.StringVar(&cfg.messages.LogPath, "log", cfg.messages.LogPath, "messages-api: append one line per model call to this file (env REVIEW_ASSIST_LOG)")
	fs.IntVar(&cfg.concurrency, "concurrency", cfg.concurrency, "max agents running at once")
	fs.IntVar(&cfg.maxTurns, "max-turns", cfg.maxTurns, "max model turns per agent (min 4)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: review-assist [flags] [PR]
       review-assist --init    write a first config file that uses Claude Code

With no PR, lists pull requests of the repository found from the git remotes of
the current directory.
PR may be a number (in the current repository), a PR URL on github.com, a
GitHub Enterprise host or bitbucket.org, OWNER/REPO#N or HOST/OWNER/REPO#N.

Settings come from the config file, then REVIEW_ASSIST_* env vars, then flags;
the last one set wins. Config file: `+configFile+`
The claude-code backend's token (from `+"`claude setup-token`"+`) is set only in
the file (claude_code.token) or REVIEW_ASSIST_CLAUDE_TOKEN, never by flag.
Bitbucket's Atlassian account email and API token are set the same way:
bitbucket.email and bitbucket.api_token, or REVIEW_ASSIST_BITBUCKET_EMAIL and
REVIEW_ASSIST_BITBUCKET_API_TOKEN.

flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if *apiKey != "" {
		cfg.messages.APIKey = *apiKey
	}
	cfg.messages.BaseURL = normaliseBaseURL(cfg.messages.BaseURL)
	switch cfg.backend {
	case backendMessagesAPI:
		setIf(&cfg.messages.Model, nonEmpty(model))
		setIf(&cfg.messages.Effort, nonEmpty(review.Effort(effort)))
	case backendClaudeCode:
		setIf(&cfg.claude.Model, nonEmpty(model))
		setIf(&cfg.claude.Effort, nonEmpty(review.Effort(effort)))
		if cfg.claude.Token == "" {
			return cfg, fmt.Errorf("the claude-code backend needs a token: run `claude setup-token` and put it in claude_code.token in %s", configFile)
		}
		if cfg.claude.Effort == review.EffortNone {
			return cfg, errors.New("the claude-code backend has no effort none: use low, medium, high, xhigh or max")
		}
	default:
		return cfg, fmt.Errorf("unknown backend %q: use %s or %s", cfg.backend, backendMessagesAPI, backendClaudeCode)
	}
	for _, e := range []review.Effort{cfg.messages.Effort, cfg.claude.Effort} {
		if !e.Valid() {
			return cfg, fmt.Errorf("unknown effort %q: use none, low, medium, high, xhigh or max", e)
		}
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

// resolveTarget's remote is the one the repository was taken from, or "" when
// the cwd is not a clone of the repository.
func resolveTarget(ctx context.Context, cwd string, runner github.Runner, target string) (repo pr.Repo, openPR int, remote string, err error) {
	local, localRemote, localErr := gitrepo.FindRemote(ctx, cwd, github.Platforms(ctx, runner))
	if target == "" {
		return local, 0, localRemote, localErr
	}
	if n, convErr := strconv.Atoi(target); convErr == nil {
		return local, n, localRemote, localErr
	}
	repo, n, err := pr.ParseRef(target)
	if err != nil {
		return pr.Repo{}, 0, "", err
	}
	if localErr != nil || !sameRepo(local, repo) {
		return repo, n, "", nil
	}
	return repo, n, localRemote, nil
}

// sameRepo ignores case in hostname, owner and name: a remote URL and a pasted
// PR URL may spell them differently.
func sameRepo(a, b pr.Repo) bool {
	return a.Platform == b.Platform &&
		strings.EqualFold(a.Hostname, b.Hostname) &&
		strings.EqualFold(a.Owner, b.Owner) &&
		strings.EqualFold(a.Name, b.Name)
}

func nonEmpty[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func firstSet(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
