# review-assist

A terminal UI for reviewing GitHub and Bitbucket Cloud pull requests, with
optional AI review agents. Agents run on a Messages API endpoint (Ollama,
Anthropic, a proxy) or on Claude Code. The agents only suggest. They cannot
post.

Built with bubbletea, lipgloss and glamour (the renderer glow uses). It talks
to GitHub through `gh`, so github.com and GitHub Enterprise both work with
your existing `gh auth login`. It talks to bitbucket.org over its REST API
with an API token (see [Bitbucket](#bitbucket)).

## Install

Needs Go 1.26 or later (`go.mod` asks for 1.26.2; with the default
`GOTOOLCHAIN=auto`, an older Go fetches it).

```sh
go install github.com/TheLazyLemur/review-assist/cmd/review-assist@latest
```

It also needs:

- `git`, always.
- [`gh`](https://cli.github.com), logged in, for GitHub. review-assist also asks
  `gh` about any remote whose host is not github.com or bitbucket.org, to tell
  whether it is a GitHub Enterprise server.
- For agent review, one backend: an endpoint that serves the Anthropic
  Messages API (such as [Ollama](https://ollama.com)), or the
  [`claude`](https://docs.anthropic.com/en/docs/claude-code) CLI with a Claude
  subscription. Review without agents needs neither.

It runs on macOS and Linux. Windows paths are handled but have not been tried.

## Run

```sh

review-assist                     # PRs of the repo in the current directory
review-assist 41                  # open PR 41 of that repo
review-assist https://ghe.example.com/acme/widgets/pull/41
review-assist https://bitbucket.org/acme/scheduler/pull-requests/7
review-assist OWNER/REPO#12       # github.com
review-assist HOST/OWNER/REPO#12  # any host
review-assist --init              # write a first config file that uses Claude Code
```

`--init` must be the only argument.

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

Keys and tokens in the file are secrets. If the file holds one
(`claude_code.token`, `messages_api.api_key` or `bitbucket.api_token`) and
other users can read it, review-assist refuses to start until you run
`chmod 600` on it.

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
  tools. The default model, `deepseek-v4.1-flash:cloud`, is an Ollama cloud
  model: it needs `ollama signin`. For a model that runs on your machine,
  set `messages_api.model` to one you have pulled. Effort defaults to `none`, which turns thinking off: with thinking on,
  models here spent their whole token budget thinking and each turn took
  20–40 s. Any other effort turns on adaptive thinking at that effort.
- **`claude-code`** is a CLI backend: the `claude` CLI, driven through
  [pi-claude](https://github.com/TheLazyLemur/pi-claude), a Go library that
  runs claude and serves it custom tools. Run
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
  checkout, delete my PR comment, browser), `A` run or cancel agent review, `o` browser, `r` refresh, `q` back.
- Diff: `j/k` lines, `]/[` files, `}/{` hunks, `h/l` scroll sideways, `c` comment on the line,
  `v` then `c` comment on a line range, `F` file comment, `R` reply to the thread on the line,
  `D` delete my comment on the line, `t` hide comments, `f` hide file list, `n/N` next agent finding.
- Your own PR: GitHub refuses approve and request changes from a PR's author.
  On your own GitHub PR, `a` and `x` post a comment instead, headed
  `**Approved**` or `**Changes requested:**`. The editor says so before you post.
  Bitbucket accepts them, so on your own Bitbucket PR `a` and `x` send the verdict.
- Editors: `alt+enter` submits (`ctrl+s` and `ctrl+enter` also work where your terminal passes them through; zellij takes `ctrl+s`), `esc` cancels (twice if you typed something). If a post fails, the editor stays open with your text and shows the reason.

## Agent review

`A` asks for a level. The level sets how many agents run. Each agent checks
the change through one lens, one kind of problem:

| lens | checks |
|---|---|
| L1 | correctness and logic |
| L2 | failure and robustness |
| L3 | concurrency and data integrity |
| L4 | security |
| L5 | performance |
| L6 | API and compatibility |
| L7 | tests |
| L8 | maintainability |
| L9 | the project's own rules |

Correctness covers every file exactly once, split into file groups on larger
levels. The other lenses each read the whole change. Above level 1, a
verifier then dedupes the findings, re-checks each one against the code, ranks
them and writes a verdict.

| level | agents at most | correctness groups | other lenses, in priority order |
|---|---|---|---|
| 1 quick | 1 | 1 | none; no verifier |
| 2 standard | 4 | 1 | L9, L2, L4 |
| 3 thorough | 7 | up to 3 | L9, L2, L3, L4, L7 |
| 4 max | 10 | up to 4 | L9, L2, L3, L4, L7, L6, L5, L8 |

When the lenses do not fit under the cap, the last ones are dropped. So on
level 3, a change with three or more files and a rules file gets no L7.

L9 runs only when the PR's head commit has a `CLAUDE.md`, `AGENTS.md`,
`CONTRIBUTING.md`, `.editorconfig` or `GEMINI.md` at the repo root. It needs
the commits locally to see them (see below).

Each finding shows where a comment could go (file, line, side), a short
title, a plain explanation, and a draft comment. `enter` jumps to the line.
`c` opens the comment editor with the draft filled in. Nothing is posted until
you press `alt+enter`. Findings also show inline in the diff as `◆`.

### What agents can do

Agents get these tools and nothing else: `list_changed_files`, `get_diff`,
`read_file`, `list_dir`, `grep`, `git_log`, `pr_description`, and
`submit_findings`. `read_file`, `list_dir`, `grep` and `git_log` each run a
fixed read-only git command (`cat-file`, `ls-tree`, `grep`, `log`) against the
PR's head or base commit. The rest serve the PR data already loaded. There is
no shell, no write tool, and no code host access. Paths are checked so they cannot
become git options.

To read the code, agents need the PR commits locally:

- If the current directory is a clone of the PR's repo, the app fetches the
  PR's commits there. On GitHub it runs `git fetch <remote> refs/pull/N/head`, and fetches the
  base commit too if it is missing.
  On Bitbucket it fetches the base and source branches by name. That adds
  objects only. It does not touch branches, the index or your working tree.
- Otherwise it keeps a bare mirror under the user cache directory
  (`~/Library/Caches/review-assist/` on macOS, `~/.cache/review-assist/` on
  Linux). The first review of a repo clones it, which takes a while
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
  config.go        config file: path and keys
  init.go          --init
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
  gitrepo/         outbound review.CodeSource/Code over git; picks the remote;
                   fetches and mirrors, but agents only get read commands
tools/tracker/     the issue tracker used to plan this repo (see Development)
```

The review core has no path to the code host: `review.Service` gets a `Backend` and a
`CodeSource`, and neither can post. Each backend runs its own loop but may
only call the tools the core hands it. Only the TUI calls `pr.Service` writes, and
only after you submit or confirm.

## Not verified

These have not run against a live pull request, so try them on a PR you own
first:

- GitHub write actions. Only the payload of an inline comment has a unit test;
  approve, request changes, merge, close, reopen, ready / draft and deleting
  a comment have none.
- Some Bitbucket actions. Their requests are tested against a fake server
  only.

## Development

```sh
go test ./...
go vet ./...
```

Work is planned in this repo's GitHub Issues: a slice is a user-visible
change, and its tasks are sub-issues that declare what they depend on.
`./board` shows what can start. `./tracker` reads and writes that model, and
`./tracker validate` checks it. Both are thin wrappers over `tools/tracker`,
which talks to GitHub through `gh`. The agent skills in `.claude/skills/`
write slices and tasks in that shape, and `docs/agents/issue-tracker.md` has
the raw `gh` commands. Plans for finished tasks stay in `.agents/plans/`.

`CONTEXT.md` is the glossary. `docs/adr/` holds the decisions.

## Licence

MIT. See [LICENSE](LICENSE).
