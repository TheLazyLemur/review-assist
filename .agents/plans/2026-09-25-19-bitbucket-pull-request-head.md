# #19 Agent review reads the head commit of a Bitbucket pull request

Issue: https://github.com/TheLazyLemur/review-assist/issues/19

## Decisions

- `review.CodeSource.Open` takes the `*pr.PR` instead of number and SHAs, so
  the branch names (`HeadRef`, `BaseRef`) reach it. `review.Service` passes
  `d.PR`.
- `gitrepo.Source.Open` switches on `repo.Platform`:
  - GitHub: unchanged. Fetch `refs/pull/<n>/head`, then the base by SHA.
  - Bitbucket: fetch `refs/heads/<BaseRef>`, then `refs/heads/<HeadRef>`, as
    two fetches: one missing ref fails a whole fetch, so a fork or a deleted
    source branch would lose the base too. Never by hash: Bitbucket gives
    12-character hashes, too short to fetch by.
  - Any other platform is an error, before anything is resolved.
- On Bitbucket, a branch name that fails `git check-ref-format --branch`
  (empty, or with `:`, `..`, a leading `-`) is an error before any fetch.
- Every fetch passes `--refmap=`, so fetching from a named remote does not
  move its remote-tracking branches.
- Fetch errors stay tolerated. A fork's source branch is in another
  repository, so its head stays missing and the agents work from the diff.
- Nothing compares SHAs; `git cat-file`, `grep` and `log` resolve a
  12-character hash as given, and `Grep` strips the prefix as passed.
- Tests build a bare repository at `<tmp>/bitbucket.org/acme/scheduler.git`
  so `remoteFor` matches the clone's `origin`, with the user's git config
  shut out.
