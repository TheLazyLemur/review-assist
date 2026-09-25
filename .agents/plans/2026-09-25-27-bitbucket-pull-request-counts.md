# #27 Bitbucket pull requests show +0 -0 in 0 files

Issue: https://github.com/TheLazyLemur/review-assist/issues/27

## Decisions

- The Bitbucket adapter fills `Summary.Additions`, `Deletions` and
  `ChangedFiles` from `GET .../pullrequests/{id}/diffstat`. The port and the
  TUI do not change.
- One helper, `addCounts`, serves `Get` and `List`. It reads the diffstat with
  `getAll`: the first request goes to the pull request's diffstat path and
  net/http follows Bitbucket's 302 to `/diffstat/{spec}`; the `next` links
  that follow are on the API host, so `getAll`'s same-host check passes them.
- `lines_added` and `lines_removed` are plain `int`: a binary file's `null`
  decodes as zero. `ChangedFiles` is the number of entries over all pages.
- `List` fetches the diffstats with `errgroup`, at most 8 in flight
  (`SetLimit`). `golang.org/x/sync` was already in the module graph as an
  indirect dependency; it moves to a direct one.
- A failed diffstat fails the whole call, never leaves zeros. The error keeps
  Bitbucket's reason first and ends naming the pull request:
  `<reason> (...) (diffstat of pull request N)`.
- Tests: existing list tests whose fake servers answer every path go through
  `withEmptyDiffstats`, which answers any `/diffstat` with no files. The Get
  field-mapping test serves the diffstat through a redirect, as Bitbucket does.
