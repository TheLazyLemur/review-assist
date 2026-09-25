# #15 Repositories are found from git remotes, not from gh

## Goal

With no argument, review-assist finds the repository from one git remote of
the clone, picked by the rule under **Remote** in `CONTEXT.md`, instead of
`gh repo view`. Pull request URLs on bitbucket.org parse to the Bitbucket
platform. A Bitbucket repository still refuses to start: no Bitbucket code
host exists yet (#16 to #20).

## Rules

The remote (CONTEXT.md, README "Which remote"): the first of these that points
at a supported code host:

1. the remote the current branch tracks (`branch.<current>.remote`)
2. `origin`
3. the only remote on a supported code host

Otherwise refuse. Other remotes are ignored.

The platform of a hostname:

- `bitbucket.org` is Bitbucket.
- `github.com` is GitHub.
- A hostname `gh auth status --json hosts` lists (any key of `hosts`) is GitHub.
- Anything else is not a supported code host.

Errors, exactly as the README prints them (the `review-assist: ` prefix comes
from `main`, check `cmd/review-assist/main.go`, do not double it):

```
no git remote points at a supported code host (GitHub, Bitbucket)
```

```
several git remotes point at code hosts; check out a branch that tracks one
  acme    bitbucket.org/acme/scheduler
  mirror  bitbucket.org/acme-mirror/scheduler
```

Candidates are listed in `git remote` order, names padded to one column.

## Steps

1. `internal/core/pr/pr.go`: add `const Bitbucket Platform = "bitbucket"`.
   `ParseRef` parses `https://bitbucket.org/<workspace>/<repo>/pull-requests/<n>`
   (with or without scheme, trailing path allowed like the GitHub form) into
   `Repo{Platform: Bitbucket, Hostname: "bitbucket.org", Owner: workspace, Name: repo}`
   and n. The Repo.Hostname comment mentions GitHub only; fix it.
   Test in `pr_test.go`, next to `TestParseRefKeepsTheEnterpriseHost`.

2. Pure rule, in `internal/adapters/gitrepo` (new file `remote.go`):
   parse a remote URL into hostname, owner, name. Forms:
   `git@host:owner/name(.git)`, `ssh://git@host(:port)/owner/name(.git)`,
   `https://host/owner/name(.git)` (also `https://user@host/...`, which
   Bitbucket's clone URLs use). Then pick the remote by the rule, given the
   remotes in order, the tracked remote name ("" when none), and a
   `func(hostname string) (pr.Platform, bool)`. Export what the table test
   and the composition root need; nothing else.

3. Table test for step 2 (`remote_test.go`). One case per criterion:
   - tracked remote wins over `origin`
   - `origin` when nothing is tracked
   - the only remote on a code host, no `origin`, nothing tracked (add a
     remote on an unsupported host alongside it)
   - a scheduler clone (`acme` → `bitbucket.org:acme/scheduler`,
     `mirror` → `bitbucket.org:acme-mirror/scheduler`)
     tracking `acme` picks acme
   - that clone tracking nothing refuses with the several-remotes
     message listing both
   - no remote on a supported host refuses with the no-remote message
   - `github.com` → GitHub; `ghe.example.com` (logged in) → GitHub on
     `ghe.example.com`; `bitbucket.org` → Bitbucket; `gitlab.com` → unsupported
   Every case runs twice: once with SSH URLs (`git@host:owner/name.git`),
   once with HTTPS URLs (`https://host/owner/name.git`). Build both from one
   case row so there is no branching in the test body. The platform lookup in
   the test is a fake: github.com and ghe.example.com are GitHub, bitbucket.org is
   Bitbucket.

4. The git reading: an exported function in `gitrepo` that runs in a
   directory, reads `git remote -v` (fetch lines), the current branch and
   `branch.<b>.remote`, and applies the rule. Detached HEAD tracks nothing.
   Not a git repo is its own error, as today ("not in a ... repository").

5. The gh hosts lookup: in `internal/adapters/github`, a function that runs
   `gh auth status --json hosts` through the existing `Runner` and returns the
   hostname → platform func. `github.com` is GitHub even when gh lists nothing.
   `bitbucket.org` is Bitbucket without asking gh.

6. `cmd/review-assist/compose.go` `resolveTarget`: replace `github.Detect`
   with steps 4 and 5. Delete `github.Detect`. After the repo is resolved
   (local or from the argument), a Bitbucket repo returns an error that says
   Bitbucket is not supported yet. Keep today's behaviour otherwise: a URL
   argument still works when the local detection fails.

7. README "Which remote": delete the line `Not yet: today the repository
   comes from \`gh repo view\`.` Nothing else in the README changes.

## Out of scope

- `gitrepo.remoteFor` keeps its own URL matching. Folding it into step 2 is
  not this task.
- No Bitbucket code host, no config.

## Verify

- `go test ./...` and `go vet ./...` pass; `golangci-lint run` if configured.
- `go run ./cmd/review-assist` in this repo (GitHub) gets past repository
  detection. It needs a TTY for the TUI; check detection by running
  resolveTarget from a test or by reading the error it prints, not by driving
  the TUI.
- In a clone of `acme/storefront`, running the
  built binary prints the Bitbucket-not-supported error.
