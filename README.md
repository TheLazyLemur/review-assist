# review-assist

A terminal UI for reviewing GitHub and Bitbucket Cloud pull requests, with
optional AI review agents. Agents run on a Messages API endpoint (Ollama,
Anthropic, a proxy) or on Claude Code. The agents only suggest. They cannot
post.

Built with bubbletea, lipgloss and glamour (the renderer glow uses). It talks
to GitHub through `gh`, so github.com and GitHub Enterprise both work with
your existing `gh auth login`. It talks to bitbucket.org over its REST API
with an API token (see [Bitbucket](#bitbucket)).

## Run

```sh
go install ./cmd/review-assist    # or: go build -o review-assist ./cmd/review-assist

review-assist                     # PRs of the repo in the current directory
review-assist 41                  # open PR 41 of that repo
review-assist https://ghe.example.com/acme/widgets/pull/41
review-assist https://bitbucket.org/acme/scheduler/pull-requests/7
review-assist OWNER/REPO#12       # github.com
review-assist HOST/OWNER/REPO#12  # any host
review-assist --init              # write a first config file that uses Claude Code
```

`--init` asks for a token from `claude setup-token` and writes a config file
that uses the `claude-code` backend. Paste a token you already have, or press
enter and it runs `claude setup-token` for you. The token is read without echo.
The file is created readable by you only, and `--init` never overwrites an
existing file.

`--init` also writes `config.example.json` next to the config file. It sets
every option, so use it as a reference. `--init` rewrites it on every run, so
it always matches the installed version. Its secrets are empty.

### Which remote

With no argument, review-assist takes the repository from one git remote of
the clone. It uses the first of these that points at a supported code host:

1. the remote the current branch tracks
2. `origin`
3. the only remote on a supported code host

Other remotes are ignored. Say a clone has two remotes on bitbucket.org,
`acme` and `mirror`, and none called `origin`. If its `develop` branch tracks
`acme/develop`, review-assist uses `acme`. On a branch that tracks
nothing, neither step 2 nor step 3 applies, so it refuses to start.

The header shows the remote it picked next to the repository, such as
`bitbucket.org/acme/scheduler  remote acme`. On Bitbucket, checkout fetches
from that remote. For a PR URL of a repository the current directory is not a
clone of, there is no remote, so the header shows none and checkout is not
offered.

When no remote points at a supported code host, it lists the remotes it
ignored. An SSH host alias such as `github-work` is not resolved, so it shows
up here:

```
review-assist: no git remote points at a supported code host (GitHub, Bitbucket)
  origin  github-work/TheLazyLemur/review-assist
```

When several do and the rule picks none, it lists them:

```
review-assist: several git remotes point at code hosts; check out a branch that tracks one
  acme    bitbucket.org/acme/scheduler
  mirror  bitbucket.org/acme-mirror/scheduler
```

## Settings

Each setting can come from the config file, an environment variable or a flag.
They apply in that order, so a flag beats an env var and an env var beats the
file.

| config file key | env | flag | default |
|---|---|---|---|
| `backend` | `REVIEW_ASSIST_BACKEND` | `-backend` | `messages-api`; or `claude-code` |
| (model of the chosen backend) | `REVIEW_ASSIST_MODEL` | `-model` | see below |
| (effort of the chosen backend) | `REVIEW_ASSIST_EFFORT` | `-effort` | see below |
| `messages_api.model` | | | `deepseek-v4.1-flash:cloud` |
| `messages_api.effort` | | | `none` (thinking off) |
| `messages_api.base_url` | `REVIEW_ASSIST_BASE_URL` | `-base-url` (alias `-ollama`) | `OLLAMA_HOST`, else `http://localhost:11434` |
| `messages_api.api_key` | `REVIEW_ASSIST_API_KEY` | `-api-key` | `ANTHROPIC_API_KEY`, else a placeholder (local Ollama needs no key) |
| `messages_api.log` | `REVIEW_ASSIST_LOG` | `-log FILE` | off; one line per model call |
| `claude_code.token` | `REVIEW_ASSIST_CLAUDE_TOKEN` | | required for `claude-code` |
| `claude_code.model` | | | claude's default |
| `claude_code.effort` | | | claude's default |
| `claude_code.executable` | | | `claude` on `PATH` |
| `bitbucket.email` | `REVIEW_ASSIST_BITBUCKET_EMAIL` | | required for Bitbucket; your Atlassian account email |
| `bitbucket.api_token` | `REVIEW_ASSIST_BITBUCKET_API_TOKEN` | | required for Bitbucket; an API token |
| `review.concurrency` | | `-concurrency` | 4 agents at once |
| `review.max_turns` | | `-max-turns` | 40 model turns per agent |

Effort is `none`, `low`, `medium`, `high`, `xhigh` or `max`: how hard each
model reasons. It is separate from the review level, which sets how many
agents run. `claude-code` has no `none`.

`OLLAMA_HOST` and `ANTHROPIC_API_KEY` are shared with other tools, so they only
replace the built-in default. They never override the config file.

The config file is JSON:

- macOS and Linux: `~/.config/review-assist/config.json`, or
  `$XDG_CONFIG_HOME/review-assist/config.json` when that is set.
- Windows: `%AppData%\review-assist\config.json`.

`review-assist -h` prints the path it uses. A missing file is fine. An unknown
key is an error, so a typo fails loudly instead of being ignored.

```json
{
  "backend": "claude-code",
  "claude_code": {
    "token": "sk-ant-oat01-…",
    "model": "sonnet",
    "effort": "low"
  },
  "messages_api": {
    "base_url": "http://localhost:11434",
    "model": "deepseek-v4.1-flash:cloud",
    "effort": "none"
  },
  "bitbucket": {
    "email": "you@example.com",
    "api_token": "ATATT…"
  },
  "review": { "concurrency": 4, "max_turns": 40 }
}
```

Keys and tokens in the file are secrets: keep it private (`chmod 600`).

### Bitbucket

review-assist signs in to Bitbucket Cloud with two settings, and refuses to
start on a bitbucket.org repository until both are set. A GitHub repository
needs neither.

- `bitbucket.email` is your Atlassian account email, not your Bitbucket
  username.
- `bitbucket.api_token` is an API token you mint. A password is never
  accepted.

To mint the token: Atlassian account settings → Security → Create and manage
API tokens (<https://id.atlassian.com/manage-profile/security/api-tokens>) →
Create API token with scopes. Give it a name and an expiry, pick
the app Bitbucket, then these scopes:

| scope | used for |
|---|---|
| `read:user:bitbucket` | who you are, to tell your own pull requests apart |
| `read:pullrequest:bitbucket` | list pull requests, read one, its comments; post and delete comments |
| `write:pullrequest:bitbucket` | approve, request changes, merge, decline, mark as draft or ready |
| `read:repository:bitbucket` | the diff: Bitbucket redirects a pull request's diff to the repository's; the git mirror agent review reads code from |

The token is shown once, so copy it into the config file or
`REVIEW_ASSIST_BITBUCKET_API_TOKEN` straight away. Agent review of a
Bitbucket repository with no local clone mirrors it with git using this token;
it is given to git only for the clone and fetches of that mirror, and never
written to it. A
local clone, for checkout and agent review, fetches with its remote's own
credentials.

### Backends

There are two kinds of backend. An API backend is looped by review-assist.
A CLI backend is a command-line agent that runs its own loop; it qualifies
only if its built-in tools can be turned off (see
[ADR 0001](docs/adr/0001-cli-backends-must-disable-built-in-tools.md)).

- **`messages-api`** is an API backend. It loops over the Anthropic Messages
  API (`<base URL>/v1/messages`) with the official SDK. Ollama serves that
  API, so the default is local Ollama with no key. The model must support
  tools. Effort defaults to `none`, which turns thinking off: with thinking on,
  models here spent their whole token budget thinking and each turn took
  20–40 s. Any other effort turns on adaptive thinking at that effort.
- **`claude-code`** is a CLI backend: the `claude` CLI, through
  [pi-claude](https://github.com/TheLazyLemur/pi-claude). Run
  `review-assist --init`, or mint a token with `claude setup-token` and put it
  in `claude_code.token` yourself. claude runs in bare
  mode on that token, with no built-in tools, no settings files, no MCP servers
  and an empty working directory, so the review tools are the only tools it
  has. Bare mode taking the token as `ANTHROPIC_AUTH_TOKEN` is undocumented: if
  reviews start failing with "Not logged in" or hang after a claude upgrade,
  suspect that first.

## Keys

Vim style. Press `?` anywhere for the full list.

- List: `j/k`, `gg/G`, `ctrl+d/u`, `enter` open, `/` filter, `s` open/closed/merged/all, `r` refresh, `o` browser.
- PR: `1 2 3` or `tab` switch Overview / Diff / Agent. `a` approve, `x` request changes, `C` PR comment,
  `m` every other action (merge / squash / rebase, close, reopen, ready / draft,
  checkout, delete my PR comment, browser), `A` agent review, `r` refresh, `q` back.
- Diff: `j/k` lines, `]/[` files, `}/{` hunks, `h/l` scroll sideways, `c` comment on the line,
  `v` then `c` comment on a line range, `F` file comment, `R` reply to the thread on the line,
  `D` delete my comment on the line, `t` hide comments, `f` hide file list, `n/N` next agent finding.
- Your own PR: GitHub refuses approve and request changes from a PR's author.
  On your own GitHub PR, `a` and `x` post a comment instead, headed
  `**Approved**` or `**Changes requested:**`. The editor says so before you post.
  Bitbucket accepts them, so on your own Bitbucket PR `a` and `x` send the verdict.
- Editors: `alt+enter` submits (`ctrl+s` and `ctrl+enter` also work where your terminal passes them through; zellij takes `ctrl+s`), `esc` cancels (twice if you typed something). If a post fails, the editor stays open with your text and shows the reason.

## Agent review

`A` asks for a level. The level sets how many agents run, following the
fanout-review skill: one lens per agent, correctness on every file, then a
verifier that dedupes, re-checks each finding against the code, ranks them and
writes a verdict.

| level | specialists | verifier |
|---|---|---|
| 1 quick | 1 correctness pass | no |
| 2 standard | correctness, robustness, security (+ project rules) | yes |
| 3 thorough | correctness split over up to 3 file groups, robustness, concurrency, security, tests (+ rules) | yes |
| 4 max | up to 10: correctness over up to 4 file groups plus the other lenses | yes |

The rules lens (L9) is added when the repo has a `CLAUDE.md`, `AGENTS.md`,
`CONTRIBUTING.md`, `.editorconfig` or `GEMINI.md` at its root.

Each finding shows where a comment could go (file, line, side), a short
title, a plain explanation, and a draft comment. `enter` jumps to the line.
`c` opens the comment editor with the draft filled in. Nothing is posted until
you press `alt+enter`. Findings also show inline in the diff as `◆`.

### What agents can do

Agents get these tools and nothing else: `list_changed_files`, `get_diff`,
`read_file`, `list_dir`, `grep`, `git_log`, `pr_description`, and
`submit_findings`. Each runs a fixed read-only git command (`cat-file`,
`ls-tree`, `grep`, `log`) against the PR's head or base commit. There is no
shell, no write tool, and no code host access. Paths are checked so they cannot
become git options.

To read the code, agents need the PR commits locally:

- If the current directory is a clone of the PR's repo, the app fetches the
  PR's commits there. On GitHub it runs `git fetch <remote> refs/pull/N/head`.
  On Bitbucket it fetches the base and source branches by name. That adds
  objects only. It does not touch branches, the index or your working tree.
- Otherwise it keeps a bare mirror under `~/Library/Caches/review-assist/`
  (`os.UserCacheDir`). The first review of a repo clones it, which takes a while
  for a big repo.

If the commits cannot be fetched, agents still get the diff, and the agent tab
says so.

## Layout

Hexagonal: the core holds the domain, its rules and its ports; adapters
connect it to the outside; `cmd/review-assist` wires them together.

```
cmd/review-assist/
  main.go          entry point: calls run()
  compose.go       composition root: flags/env, builds adapters, starts the TUI
internal/core/                 domain core (imports no adapter)
  diff/            unified diff parser; maps each line to its comment anchor
  pr/              PR types, CodeHost port, Service (loads PRs, checks write rules)
  review/          levels and lenses, Service, read-only tools;
                   ports: Backend, CodeSource/Code
internal/adapters/
  tui/             inbound: bubbletea screens calling pr.Service and review.Service
  github/          outbound pr.CodeHost over the gh CLI (github.com and GHE)
  bitbucket/       outbound pr.CodeHost over the Bitbucket Cloud REST API
  messagesapi/     outbound review.Backend: loops over the Anthropic Messages API
  claudecode/      outbound review.Backend: the claude CLI in bare mode, via pi-claude
  gitrepo/         outbound review.CodeSource/Code over git (read commands only)
```

The review core has no path to GitHub: `review.Service` gets a `Backend` and a
`CodeSource`, and neither can post. Each backend runs its own loop but may
only call the tools the core hands it. Only the TUI calls `pr.Service` writes, and
only after you submit or confirm.

## Not verified

The write actions (approve, request changes, comments, merge, close, and so on)
are covered by unit tests of the `gh` arguments and payloads. They were not run
against a live PR while this was built, so try them on a PR you own first.
