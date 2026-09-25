# #18 Bitbucket Cloud code host acts on pull requests

## Goal

`internal/adapters/bitbucket.Client` gains the rest of `pr.CodeHost`: `Merge`,
`Close`, `Reopen`, `MarkReady`, `ConvertToDraft`, `Checkout`, `OpenInBrowser`,
and `var _ pr.CodeHost = (*Client)(nil)`. Nothing wires the client in yet
(#20).

## Bitbucket Cloud API (2.0)

Paths under `/repositories/{workspace}/{slug}/pullrequests/{id}`:

- `POST /merge` with `{"merge_strategy": ...}`: `pr.MergeCommit` is
  `merge_commit`, `pr.Squash` is `squash`, `pr.Rebase` is
  `rebase_fast_forward`. The schema lists `type` as required, but no value is
  documented; it is not sent.
- A merge that takes too long answers 202 with the task-status URL
  (`.../merge/task-status/{task_id}`) in the `Location` header, not in the
  body. The client resolves it against the request URL and refuses one outside
  the API root, as `getAll` does for `next`, so the credentials stay on
  Bitbucket.
- `GET` the task status until `task_status` is `SUCCESS`. `PENDING` waits one
  poll interval and asks again; any other value is an error. A failed merge
  answers the poll with an error status and Bitbucket's error body, so `do`
  already returns it reason first. The wait is bounded by ctx and by
  `MergeWait` (default 5 minutes), because the TUI acts with a context that
  never ends.
- `POST /decline` closes.
- `PUT` the pull request with `{"draft": false}` / `{"draft": true}`.
- Bitbucket cannot reopen a declined pull request: `Reopen` returns an error
  naming reopening and sends nothing.

## Options

`NewClient` takes an `Options` struct:

- `Remote`: the git remote review-assist picked (CONTEXT.md "Remote").
- `Dir`: the working directory `Checkout` runs git in.
- `Open`: opens a URL in the browser; the production opener is #20's choice.
- `PollInterval`: wait between merge task polls; zero means one second.
- `MergeWait`: cap on the wait for a merge task; zero means 5 minutes.

`Checkout` and `OpenInBrowser` fail naming the missing option if it is unset,
rather than guessing a default.

## Checkout

Git, not the API, after reading the pull request:

1. Source and destination `repository.full_name` differ: a fork; error naming
   forks. Either missing: error.
2. The branch must pass `git check-ref-format refs/heads/<b>` (the plain
   form: `--branch` accepts `@{-1}`) and not start with `-`, so `feat*` is
   not fetched as a pattern.
3. `git fetch <remote> +refs/heads/<b>:refs/remotes/<remote>/<b>`
4. Local branch `<b>` exists (`git rev-parse --verify --quiet refs/heads/<b>`):
   `git merge-base --is-ancestor refs/heads/<b> <remote>/<b>` must hold, so a
   diverged branch fails before HEAD moves; then `git switch <b>` and
   `git merge --ff-only <remote>/<b>`. Otherwise
   `git switch -c <b> --track <remote>/<b>`.

git runs with `GIT_TERMINAL_PROMPT=0`, so a credential prompt cannot hang or
draw over the TUI.

## OpenInBrowser

Opens `https://bitbucket.org/<workspace>/<repo>/pull-requests/<n>`, built from
the repository (`repo.URL()`), with no request to the API.

## Tests

One per criterion on #18, in `bitbucket_test.go`, reusing `record`, `accept`,
`serve`, `repo`, `prPath`, `sentRequest`. Checkout runs real git against a
`git init`'d directory in `t.TempDir()` with a bare repository as the remote.
