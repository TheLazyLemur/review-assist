# review-assist

A terminal UI for reviewing GitHub pull requests, with optional AI review
agents. Agents run on any Anthropic-compatible endpoint (Ollama, Anthropic, a
proxy) or on Claude Code. The agents only suggest. They cannot post.

Built with bubbletea, lipgloss and glamour (the renderer glow uses). It talks
to GitHub through `gh`, so github.com and GitHub Enterprise both work with
your existing `gh auth login`.

## Run

```sh
go install ./cmd/review-assist    # or: go build -o review-assist ./cmd/review-assist

review-assist                     # PRs of the repo in the current directory
review-assist 41                  # open PR 41 of that repo
review-assist https://ghe.example.com/acme/widgets/pull/41
review-assist OWNER/REPO#12       # github.com
review-assist HOST/OWNER/REPO#12  # any host
```

## Settings

Each setting can come from the config file, an environment variable or a flag.
They apply in that order, so a flag beats an env var and an env var beats the
file.

| config file key | env | flag | default |
|---|---|---|---|
| `backend` | `REVIEW_ASSIST_BACKEND` | `-backend` | `anthropic`; or `claude-code` |
| (model of the chosen backend) | `REVIEW_ASSIST_MODEL` | `-model` | see below |
| `anthropic.model` | | | `deepseek-v4.1-flash:cloud` |
| `anthropic.base_url` | `REVIEW_ASSIST_BASE_URL` | `-base-url` (alias `-ollama`) | `OLLAMA_HOST`, else `http://localhost:11434` |
| `anthropic.api_key` | `REVIEW_ASSIST_API_KEY` | `-api-key` | `ANTHROPIC_API_KEY`, else a placeholder (local Ollama needs no key) |
| `anthropic.think` | | `-think` | off |
| `anthropic.log` | `REVIEW_ASSIST_LOG` | `-log FILE` | off; one line per model call |
| `claude_code.token` | `REVIEW_ASSIST_CLAUDE_TOKEN` | | required for `claude-code` |
| `claude_code.model` | | | claude's default |
| `claude_code.effort` | | `-effort` | claude's default |
| `claude_code.executable` | | | `claude` on `PATH` |
| `review.concurrency` | | `-concurrency` | 4 agents at once |
| `review.max_turns` | | `-max-turns` | 40 model turns per agent |

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
  "anthropic": {
    "base_url": "http://localhost:11434",
    "model": "deepseek-v4.1-flash:cloud"
  },
  "review": { "concurrency": 4, "max_turns": 40 }
}
```

Keys and tokens in the file are secrets: keep it private (`chmod 600`).

### Backends

- **`anthropic`** loops over the Messages API (`<base URL>/v1/messages`) with
  the official SDK. Ollama serves that API, so the default is local Ollama with
  no key. The model must support tools. Thinking is off by default: with it on,
  models here spent their whole token budget thinking and each turn took
  20–40 s.
- **`claude-code`** runs the `claude` CLI, which loops by itself, through
  [pi-claude](https://github.com/TheLazyLemur/pi-claude). Mint a token with
  `claude setup-token` and put it in `claude_code.token`. claude runs in bare
  mode on that token, with no built-in tools, no settings files, no MCP servers
  and an empty working directory, so the review tools are the only tools it
  has. Bare mode taking the token as `ANTHROPIC_AUTH_TOKEN` is undocumented: if
  reviews start failing with "Not logged in" or hang after a claude upgrade,
  suspect that first.

## Keys

Vim style. Press `?` anywhere for the full list.

- List: `j/k`, `gg/G`, `ctrl+d/u`, `enter` open, `/` filter, `s` open/closed/merged/all, `r` refresh, `o` browser.
- PR: `1 2 3` or `tab` switch Overview / Diff / Agent. `a` approve, `x` request changes, `C` PR comment,
  `m` every other action (review comment, merge / squash / rebase, close, reopen, ready / draft,
  checkout, delete my PR comment, browser), `A` agent review, `r` refresh, `q` back.
- Diff: `j/k` lines, `]/[` files, `}/{` hunks, `h/l` scroll sideways, `c` inline comment,
  `v` then `c` comment on a line range, `F` file comment, `R` reply to the thread on the line,
  `D` delete my comment on the line, `t` hide comments, `f` hide file list, `n/N` next agent finding.
- Editors: `alt+enter` submits (`ctrl+s` and `ctrl+enter` also work where your terminal passes them through; zellij takes `ctrl+s`), `esc` cancels (twice if you typed something).

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
shell, no write tool, and no GitHub access. Paths are checked so they cannot
become git options.

To read the code, agents need the PR commits locally:

- If the current directory is a clone of the PR's repo, the app runs
  `git fetch <remote> refs/pull/N/head` there. That adds objects only. It does
  not touch branches, the index or your working tree.
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
  pr/              PR types, Host port, Service (loads PRs, checks write rules)
  review/          levels and lenses, Reviewer, read-only tools;
                   ports: Agent, CodeSource/Code
internal/adapters/
  tui/             inbound: bubbletea screens calling pr.Service and review.Reviewer
  github/          outbound pr.Host over the gh CLI (github.com and GHE)
  anthropic/       outbound review.Agent: loops over the Anthropic Messages API
  claudecode/      outbound review.Agent: the claude CLI in bare mode, via pi-claude
  gitrepo/         outbound review.CodeSource/Code over git (read commands only)
```

The review core has no path to GitHub: `review.Reviewer` gets an `Agent` and a
`CodeSource`, and neither can post. Each backend runs its own loop but may
only call the tools the core hands it. Only the TUI calls `pr.Service` writes, and
only after you submit or confirm.

## Not verified

The write actions (approve, request changes, comments, merge, close, and so on)
are covered by unit tests of the `gh` arguments and payloads. They were not run
against a live PR while this was built, so try them on a PR you own first.
